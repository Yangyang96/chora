package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/buildinfo"
	"github.com/Yangyang96/chora/internal/piinstall"
	"github.com/Yangyang96/chora/migrations"
	_ "modernc.org/sqlite"
)

const workbenchSchemaVersion = 41

var diagnosticVersionPattern = regexp.MustCompile(`(?:^|[^0-9A-Za-z_])(?:go|v)?(\d{1,4})\.(\d{1,4})(?:\.(\d{1,4}))?\b`)

type workbenchDoctorReport struct {
	Status        string                   `json:"status"`
	Build         workbenchDoctorBuild     `json:"build"`
	Data          workbenchDoctorData      `json:"data"`
	Prerequisites []workbenchDoctorPrereq  `json:"prerequisites"`
	Counts        map[string]int64         `json:"counts,omitempty"`
	Findings      []workbenchDoctorFinding `json:"findings,omitempty"`
}

type workbenchDoctorBuild struct {
	Version        string `json:"version"`
	SourceRevision string `json:"source_revision"`
}

type workbenchDoctorData struct {
	Root       string `json:"root"`
	Database   string `json:"database"`
	Schema     int    `json:"schema"`
	Supported  int    `json:"supported_schema"`
	Integrity  string `json:"integrity"`
	Worktrees  string `json:"task_workspaces"`
	PiSessions string `json:"pi_sessions"`
	PiInstall  string `json:"pi_installation"`
	Runtime    string `json:"runtime"`
}

type workbenchDoctorPrereq struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Observed string `json:"observed,omitempty"`
	Required string `json:"required"`
	Action   string `json:"action,omitempty"`
}

type workbenchDoctorFinding struct {
	Code   string `json:"code"`
	Action string `json:"action"`
}

type workbenchDoctorOptions struct {
	runVersion     func(context.Context, string, ...string) (string, error)
	goVersion      string
	newPiInstaller func(string) (piinstall.Installer, error)
}

func runWorkbenchDoctor(args []string, stdout, stderr io.Writer) int {
	return runWorkbenchDoctorWithOptions(args, stdout, stderr, workbenchDoctorOptions{
		runVersion: boundedVersionCommand,
		goVersion:  runtime.Version(),
		newPiInstaller: func(dataRoot string) (piinstall.Installer, error) {
			return piinstall.New(piinstall.Config{DataRoot: dataRoot})
		},
	})
}

func runWorkbenchDoctorWithOptions(args []string, stdout, stderr io.Writer, options workbenchDoctorOptions) int {
	flags := flag.NewFlagSet("workbench doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sourceRoot := flags.String("source", "", "absolute Chora source checkout")
	dataRoot := flags.String("data", "", "absolute Chora data root (default $HOME/.chora/data)")
	jsonOutput := flags.Bool("json", false, "emit stable redacted JSON")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *dataRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil || !canonicalAbsolute(home) {
			fmt.Fprintln(stderr, "workbench doctor cannot resolve home directory; specify absolute --data")
			return 2
		}
		*dataRoot = filepath.Join(home, ".chora", "data")
	}
	if flags.NArg() != 0 || !canonicalAbsolute(*sourceRoot) || !canonicalAbsolute(*dataRoot) || pathsOverlap(*sourceRoot, *dataRoot) {
		fmt.Fprintln(stderr, "workbench doctor requires absolute --source, --data, and a data root outside the source checkout")
		return 2
	}
	if options.runVersion == nil {
		return 2
	}

	report := inspectWorkbench(context.Background(), *sourceRoot, *dataRoot, options)
	if *jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		if err := encoder.Encode(report); err != nil {
			fmt.Fprintln(stderr, "workbench doctor could not encode its redacted report")
			return 2
		}
	} else {
		writeWorkbenchDoctorText(stdout, report)
	}
	if report.Status == "ready" {
		return 0
	}
	return 1
}

func inspectWorkbench(ctx context.Context, sourceRoot, dataRoot string, options workbenchDoctorOptions) workbenchDoctorReport {
	report := workbenchDoctorReport{
		Status: "ready",
		Build:  workbenchDoctorBuild{Version: safeIdentity(buildinfo.Current().Version), SourceRevision: "unknown"},
		Data: workbenchDoctorData{Root: pathAvailability(dataRoot), Database: "missing", Supported: workbenchSchemaVersion,
			Integrity: "not_checked", Worktrees: pathAvailability(filepath.Join(dataRoot, "task-workspaces")),
			PiSessions: pathAvailability(filepath.Join(dataRoot, "pi-sessions")), PiInstall: pathAvailability(filepath.Join(dataRoot, "pi")),
			Runtime: pathAvailability(filepath.Join(dataRoot, "runtime"))},
		Counts: map[string]int64{},
	}
	if revision, err := options.runVersion(ctx, "git", "-C", sourceRoot, "rev-parse", "--verify", "HEAD"); err == nil {
		if candidate := strings.Fields(revision); len(candidate) > 0 && isHexIdentity(candidate[0], 40, 64) {
			report.Build.SourceRevision = candidate[0]
		}
	}
	if report.Build.SourceRevision == "unknown" {
		report.Status = "attention_required"
		report.Findings = append(report.Findings, workbenchDoctorFinding{Code: "source_revision_unknown", Action: "Confirm --source points to the exact Chora Git checkout used to build and run Workbench."})
	}

	report.Prerequisites = []workbenchDoctorPrereq{
		checkPrerequisite(ctx, options.runVersion, "go", options.goVersion, []int{1, 26, 0}, "1.26+", "Install the documented Go version, then rebuild Chora."),
		checkCommandPrerequisite(ctx, options.runVersion, "node", []string{"--version"}, []int{22, 19, 0}, "22.19+", "Install a compatible Node.js release before using Pi."),
		checkCommandPrerequisite(ctx, options.runVersion, "npm", []string{"--version"}, []int{11, 0, 0}, "11+", "Install the npm version declared by the Workbench setup guide."),
		inspectPiPrerequisite(ctx, dataRoot, options),
	}
	for _, prerequisite := range report.Prerequisites {
		if prerequisite.Status != "ready" {
			report.Status = "attention_required"
		}
	}

	dbPath := filepath.Join(dataRoot, "chora.db")
	if info, err := os.Stat(dbPath); err != nil || !info.Mode().IsRegular() {
		report.Findings = append(report.Findings, workbenchDoctorFinding{Code: "database_missing", Action: "Confirm the exact --data value or restore the stopped whole data root at its original absolute path."})
		report.Status = "attention_required"
		return report
	}
	report.Data.Database = "available"
	inspectWorkbenchDatabase(ctx, dbPath, &report)
	return report
}

func inspectPiPrerequisite(ctx context.Context, dataRoot string, options workbenchDoctorOptions) workbenchDoctorPrereq {
	const required = "0.84.2+ (0.85.1 qualified)"
	const setupAction = "Use Workbench installation or the declared upstream Pi route, then configure Pi with /login and /model."
	const managedAction = "Repair or remove the invalid managed Pi installation through Workbench; the doctor will not fall back to PATH while managed state is present."
	newInstaller := options.newPiInstaller
	if newInstaller == nil {
		newInstaller = func(root string) (piinstall.Installer, error) {
			return piinstall.New(piinstall.Config{DataRoot: root})
		}
	}
	installer, err := newInstaller(dataRoot)
	if err != nil {
		return workbenchDoctorPrereq{Name: "pi", Status: "managed_invalid", Required: required, Action: managedAction}
	}
	state, inspectErr := installer.Inspect(ctx)
	if inspectErr != nil {
		return workbenchDoctorPrereq{Name: "pi", Status: "managed_invalid", Required: required, Action: managedAction}
	}
	selection, selectionErr := installer.ResolveSelection(ctx)
	if selectionErr == nil {
		// ResolveSelection validates the frozen installation closure and invokes
		// the selected absolute executable with --version. Report only its
		// normalized version, never the private executable path.
		return checkPrerequisite(ctx, options.runVersion, "pi", selection.PiVersion, []int{0, 84, 2}, required, managedAction)
	}
	selectionRecordAbsent := managedPiSelectionRecordAbsent(dataRoot)
	if state.Code != piinstall.StateMissing || state.SelectionPresent || !selectionRecordAbsent {
		return workbenchDoctorPrereq{Name: "pi", Status: "managed_invalid", Required: required, Action: managedAction}
	}
	return checkCommandPrerequisite(ctx, options.runVersion, "pi", []string{"--version"}, []int{0, 84, 2}, required, setupAction)
}

func managedPiSelectionRecordAbsent(dataRoot string) bool {
	root := filepath.Clean(dataRoot)
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	_, err := os.Lstat(filepath.Join(root, "pi", "selection.json"))
	return os.IsNotExist(err)
}

func inspectWorkbenchDatabase(ctx context.Context, dbPath string, report *workbenchDoctorReport) {
	dsn := (&url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(1000)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		databaseFinding(report, "database_unreadable", "Stop Workbench, confirm data-root permissions, and retry. Preserve the whole data root.")
		return
	}
	defer db.Close()
	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := db.PingContext(queryCtx); err != nil {
		databaseFinding(report, "database_unreadable", "Stop Workbench, confirm data-root permissions, and retry. Preserve the whole data root.")
		return
	}
	var integrity string
	if err := db.QueryRowContext(queryCtx, "PRAGMA quick_check(1)").Scan(&integrity); err != nil || integrity != "ok" {
		report.Data.Integrity = "failed"
		databaseFinding(report, "database_integrity_failed", "Stop using this copy and restore a verified whole-data-root backup with matching code.")
		return
	}
	report.Data.Integrity = "ok"

	version, state, err := inspectMigrationLedger(queryCtx, db)
	report.Data.Schema = version
	if err != nil {
		databaseFinding(report, state, migrationAction(state))
		return
	}
	switch {
	case version > workbenchSchemaVersion:
		databaseFinding(report, "schema_newer_than_supported", "Stop. Use code matching this database or restore a matching backup; do not edit the migration ledger.")
		return
	case version < workbenchSchemaVersion:
		databaseFinding(report, "schema_upgrade_required", "Stop active work and make a whole-data-root backup before starting this Chora version to migrate forward.")
		return
	}

	for label, table := range map[string]string{
		"projects": "projects", "rooms": "rooms", "tasks": "tasks", "runs": "runs", "attempts": "attempts",
		"result_groups": "resource_result_groups", "runtime_sessions": "runtime_sessions",
		"task_worktrees": "task_repository_worktrees", "legacy_task_worktrees": "task_worktrees",
		"reviews": "resource_result_reviews", "delivery_operations": "task_delivery_operations", "apply_operations": "apply_operations",
	} {
		var count int64
		if err := db.QueryRowContext(queryCtx, "SELECT count(*) FROM "+table).Scan(&count); err != nil {
			databaseFinding(report, "schema_required_table_missing", "Stop. Use matching code and a matching backup; do not edit the database schema manually.")
			return
		}
		report.Counts[label] = count
	}
}

func inspectMigrationLedger(ctx context.Context, db *sql.DB) (int, string, error) {
	expected, err := expectedMigrationIdentities()
	if err != nil {
		return 0, "migration_source_invalid", err
	}
	rows, err := db.QueryContext(ctx, "SELECT version,name,hex(checksum) FROM schema_migrations ORDER BY version")
	if err != nil {
		return 0, "migration_ledger_missing", err
	}
	defer rows.Close()
	version := 0
	for rows.Next() {
		var observedVersion int
		var name, checksum string
		if err := rows.Scan(&observedVersion, &name, &checksum); err != nil {
			return version, "migration_ledger_unreadable", err
		}
		if observedVersion != version+1 {
			return observedVersion, "migration_gap", fmt.Errorf("migration gap")
		}
		version = observedVersion
		if version <= len(expected) && (name != expected[version-1].name || !strings.EqualFold(checksum, expected[version-1].checksum)) {
			return version, "migration_checksum_drift", fmt.Errorf("migration identity mismatch")
		}
	}
	if err := rows.Err(); err != nil {
		return version, "migration_ledger_unreadable", err
	}
	return version, "", nil
}

type migrationIdentity struct{ name, checksum string }

func expectedMigrationIdentities() ([]migrationIdentity, error) {
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		return nil, err
	}
	identities := make([]migrationIdentity, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix, _, ok := strings.Cut(entry.Name(), "_")
		version, parseErr := strconv.Atoi(prefix)
		if !ok || parseErr != nil || version < 1 {
			return nil, fmt.Errorf("invalid migration identity")
		}
		body, readErr := migrations.Files.ReadFile(entry.Name())
		if readErr != nil {
			return nil, readErr
		}
		digest := sha256.Sum256(body)
		identities = append(identities, migrationIdentity{name: entry.Name(), checksum: hex.EncodeToString(digest[:])})
	}
	sort.Slice(identities, func(i, j int) bool { return identities[i].name < identities[j].name })
	for index, identity := range identities {
		version, _ := strconv.Atoi(strings.SplitN(identity.name, "_", 2)[0])
		if version != index+1 {
			return nil, fmt.Errorf("migration source gap")
		}
	}
	return identities, nil
}

func databaseFinding(report *workbenchDoctorReport, code, action string) {
	report.Status = "attention_required"
	report.Findings = append(report.Findings, workbenchDoctorFinding{Code: code, Action: action})
}

func migrationAction(code string) string {
	switch code {
	case "migration_checksum_drift", "migration_gap", "migration_source_invalid":
		return "Stop. Use matching code and a matching whole-data-root backup; do not edit schema_migrations."
	default:
		return "Stop Workbench, preserve the whole data root, and retry with code matching this database."
	}
}

func checkCommandPrerequisite(ctx context.Context, run func(context.Context, string, ...string) (string, error), name string, args []string, minimum []int, required, action string) workbenchDoctorPrereq {
	observed, err := run(ctx, name, args...)
	if err != nil {
		return workbenchDoctorPrereq{Name: name, Status: "missing", Required: required, Action: action}
	}
	return checkPrerequisite(ctx, run, name, observed, minimum, required, action)
}

func checkPrerequisite(_ context.Context, _ func(context.Context, string, ...string) (string, error), name, observed string, minimum []int, required, action string) workbenchDoctorPrereq {
	version, ok := normalizedVersion(observed)
	result := workbenchDoctorPrereq{Name: name, Status: "incompatible", Required: required, Action: action}
	if !ok {
		result.Status = "unknown"
		return result
	}
	result.Observed = version
	if versionAtLeast(version, minimum) {
		result.Status = "ready"
		result.Action = ""
	}
	return result
}

func normalizedVersion(value string) (string, bool) {
	match := diagnosticVersionPattern.FindStringSubmatch(value)
	if match == nil {
		return "", false
	}
	patch := match[3]
	if patch == "" {
		patch = "0"
	}
	return match[1] + "." + match[2] + "." + patch, true
}

func versionAtLeast(version string, minimum []int) bool {
	parts := strings.Split(version, ".")
	for index := 0; index < 3; index++ {
		observed, _ := strconv.Atoi(parts[index])
		if observed != minimum[index] {
			return observed > minimum[index]
		}
	}
	return true
}

func boundedVersionCommand(ctx context.Context, name string, args ...string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	output, err := exec.CommandContext(commandCtx, name, args...).CombinedOutput()
	if len(output) > 4096 {
		output = output[:4096]
	}
	return string(output), err
}

func pathAvailability(path string) string {
	info, err := os.Stat(path)
	if err == nil && info.IsDir() {
		return "available"
	}
	if os.IsNotExist(err) {
		return "missing"
	}
	return "unavailable"
}

func safeIdentity(value string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > 80 {
		return "unknown"
	}
	for _, character := range value {
		if !(character == '.' || character == '-' || character == '_' || character >= '0' && character <= '9' || character >= 'A' && character <= 'Z' || character >= 'a' && character <= 'z') {
			return "unknown"
		}
	}
	return value
}

func isHexIdentity(value string, lengths ...int) bool {
	validLength := false
	for _, length := range lengths {
		validLength = validLength || len(value) == length
	}
	if !validLength {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

func writeWorkbenchDoctorText(writer io.Writer, report workbenchDoctorReport) {
	fmt.Fprintf(writer, "Chora Workbench doctor: %s\n", report.Status)
	fmt.Fprintf(writer, "build: version=%s source_revision=%s\n", report.Build.Version, report.Build.SourceRevision)
	fmt.Fprintf(writer, "data: root=%s database=%s schema=%d supported=%d integrity=%s task_workspaces=%s pi_sessions=%s pi_installation=%s runtime=%s\n",
		report.Data.Root, report.Data.Database, report.Data.Schema, report.Data.Supported, report.Data.Integrity,
		report.Data.Worktrees, report.Data.PiSessions, report.Data.PiInstall, report.Data.Runtime)
	for _, prerequisite := range report.Prerequisites {
		fmt.Fprintf(writer, "prerequisite: %s status=%s observed=%s required=%s\n", prerequisite.Name, prerequisite.Status, emptyAsUnknown(prerequisite.Observed), prerequisite.Required)
		if prerequisite.Action != "" {
			fmt.Fprintf(writer, "action: %s\n", prerequisite.Action)
		}
	}
	labels := make([]string, 0, len(report.Counts))
	for label := range report.Counts {
		labels = append(labels, label)
	}
	sort.Strings(labels)
	for _, label := range labels {
		fmt.Fprintf(writer, "count: %s=%d\n", label, report.Counts[label])
	}
	for _, finding := range report.Findings {
		fmt.Fprintf(writer, "finding: %s\naction: %s\n", finding.Code, finding.Action)
	}
}

func emptyAsUnknown(value string) string {
	if value == "" {
		return "unknown"
	}
	return value
}
