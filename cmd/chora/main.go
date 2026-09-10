package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Yangyang96/chora/internal/acceptanceauthority"
	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/buildinfo"
	"github.com/Yangyang96/chora/internal/localweb"
	"github.com/Yangyang96/chora/internal/preflight"
	"github.com/Yangyang96/chora/internal/productinstall"
	"github.com/Yangyang96/chora/internal/releaseassets"
	"github.com/Yangyang96/chora/internal/sourcebundle"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	return runWithProductExecutor(args, stdout, stderr, productionProductExecutor{})
}

func runWithProductExecutor(args []string, stdout, stderr io.Writer, productExecutor productExecutor) int {
	if len(args) > 0 && args[0] == "internal-resource-fingerprint" {
		return runResourceCheckFingerprint(args[1:], os.Stdin, stdout, stderr)
	}
	if len(args) == 1 && args[0] == "version" {
		info := buildinfo.Current()
		fmt.Fprintf(stdout, "chora %s\n", info.Version)
		return 0
	}
	if len(args) > 0 && args[0] == "serve" {
		return runServe(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "source-checkout" {
		return runSourceCheckout(args[1:], stdout, stderr)
	}
	if len(args) > 1 && args[0] == "workbench" && args[1] == "doctor" {
		return runWorkbenchDoctor(args[2:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "workbench" {
		return runWorkbench(args[1:], stdout, stderr)
	}
	if len(args) > 0 && args[0] == "doctor" {
		return runDoctor(args[1:], stdout, stderr, preflight.DefaultProbes())
	}
	if len(args) > 0 && args[0] == "installed-doctor" {
		return runInstalledDoctor(args[1:], stdout, stderr, preflight.DefaultProbes())
	}
	if len(args) > 0 && args[0] == "setup" {
		return runProduct(append([]string{"setup"}, args[1:]...), stdout, stderr, productExecutor)
	}
	if len(args) > 0 && args[0] == "product" {
		return runProduct(args[1:], stdout, stderr, productExecutor)
	}

	fmt.Fprintln(stderr, usage)
	return 2
}

const usage = "usage: chora version | chora source-checkout [explicit development options] | chora workbench [explicit project-entry options] | chora workbench doctor [redacted diagnostics options] | chora doctor [fresh Preflight options] | chora installed-doctor [exact post-Setup installed proof options] | chora serve [exact installed service options] | chora setup [explicit options] | chora product doctor|setup|upgrade|gc|uninstall [explicit options]"

func runDoctor(args []string, stdout, stderr io.Writer, probes preflight.Probes) int {
	flags := flag.NewFlagSet("doctor", flag.ContinueOnError)
	flags.SetOutput(stderr)
	sourceRoot := flags.String("source", "", "immutable source root")
	sourceManifest := flags.String("source-manifest", "", "immutable source manifest path")
	bundleAggregate := flags.String("bundle-aggregate", "", "expected source bundle aggregate")
	installRoot := flags.String("install", "", "fresh installation root")
	dataRoot := flags.String("data", "", "fresh data root")
	authFile := flags.String("auth", "", "explicit openai-codex OAuth file")
	caFile := flags.String("ca", "", "pinned enterprise CA file")
	proxyURL := flags.String("proxy", "", "fixed enterprise proxy URL")
	modelURL := flags.String("model-url", "", "allowlisted model base URL")
	port := flags.Int("port", 0, "loopback Chora port")
	jsonOutput := flags.Bool("json", false, "emit JSON report")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *sourceRoot == "" || *sourceManifest == "" || !validFingerprint(*bundleAggregate) || *installRoot == "" || *dataRoot == "" || *authFile == "" || *caFile == "" || *proxyURL == "" || *modelURL == "" || *port < 1 || *port > 65535 {
		fmt.Fprintln(stderr, usage)
		return 2
	}
	report := preflight.Run(context.Background(), preflight.Config{
		Deadline: 60 * time.Second, SourceRoot: *sourceRoot, InstallRoot: *installRoot,
		DataRoot: *dataRoot, AuthFile: *authFile, CAFile: *caFile, ProxyURL: *proxyURL,
		ModelURL: *modelURL, Port: *port, SourceManifest: *sourceManifest, BundleAggregate: *bundleAggregate,
	}, probes)
	if *jsonOutput {
		if err := json.NewEncoder(stdout).Encode(report); err != nil {
			fmt.Fprintln(stderr, "encode doctor report failed")
			return 2
		}
	} else if report.Failure != nil {
		fmt.Fprintf(stdout, "FAIL %s\nobserved: %s\nrequired: %s\naction: %s\nelapsed: %s\ninput fingerprint: %s\nresources created: %t\n",
			report.Failure.Boundary, report.Failure.Observed, report.Failure.Required, report.Failure.Action,
			report.Elapsed, report.InputFingerprint, report.ResourcesCreated)
	} else {
		fmt.Fprintf(stdout, "PASS preflight\nelapsed: %s\ninput fingerprint: %s\nresources created: %t\n",
			report.Elapsed, report.InputFingerprint, report.ResourcesCreated)
	}
	if report.Status == preflight.StatusPassed {
		return 0
	}
	return 1
}

func runServe(args []string, stdout, stderr io.Writer) int {
	return runServeWithProbes(args, stdout, stderr, preflight.DefaultProbes())
}

func runServeWithProbes(args []string, stdout, stderr io.Writer, probes preflight.Probes) int {
	return runServeWithProbesAndListen(args, stdout, stderr, probes, func(server *http.Server) error {
		return server.ListenAndServe()
	})
}

func runServeWithProbesAndListen(args []string, stdout, stderr io.Writer, probes preflight.Probes, listen func(*http.Server) error) int {
	if listen == nil {
		return 2
	}
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	flags.SetOutput(stderr)
	databasePath := flags.String("db", "", "SQLite database path (default: DATA/chora.db)")
	webRoot := flags.String("web", "", "built Web UI directory (default: INSTALL/build/web-workspace/web/dist)")
	port := flags.Int("port", 8787, "local HTTP port")
	sourceRoot := flags.String("source", "", "immutable source root")
	sourceManifest := flags.String("source-manifest", "", "immutable source manifest path")
	bundleAggregate := flags.String("bundle-aggregate", "", "expected source bundle aggregate")
	installRoot := flags.String("install", "", "installed product root")
	dataRoot := flags.String("data", "", "Chora data root")
	repositoryRoot := flags.String("repository", "", "exact local Git repository used to create Task worktrees")
	authFile := flags.String("auth", "", "explicit openai-codex OAuth file")
	caFile := flags.String("ca", "", "pinned enterprise CA file")
	proxyURL := flags.String("proxy", "", "fixed enterprise proxy URL")
	modelURL := flags.String("model-url", "", "allowlisted model base URL")
	priorFingerprint := flags.String("preflight-fingerprint", "", "prior doctor input fingerprint")
	installationStateRoot := flags.String("installation-state-root", "", "owner-private installed product state root")
	generationID := flags.String("generation", "", "exact activated product generation")
	dockerCLI := flags.String("docker-cli", "", "exact installed Docker CLI")
	dockerContext := flags.String("docker-context", "", "explicit Docker context")
	endpointDigest := flags.String("endpoint-digest", "", "explicit Docker endpoint digest")
	releaseRoot := flags.String("release-root", "", "installed release root")
	releaseManifest := flags.String("release-manifest", "", "root-relative installed release manifest")
	manifestSHA := flags.String("manifest-sha256", "", "installed release manifest digest")
	releaseID := flags.String("release-id", "", "installed release identity")
	privatePiRoot := flags.String("private-pi-root", "", "owner-private Pi installation root")
	privatePiSource := flags.String("private-pi-source", "", "authenticated private Pi source root")
	privatePiManifest := flags.String("private-pi-manifest", "", "authenticated private Pi manifest")
	privatePiManifestSHA := flags.String("private-pi-manifest-sha256", "", "private Pi manifest digest")
	probeRuntimeRoot := flags.String("probe-runtime-root", "", "capability Probe runtime root")
	colimaToolRoot := flags.String("colima-tool-root", "", "owner-private pinned Engine tools root")
	colimaProfile := flags.String("colima-profile", "", "explicit Colima profile")
	colimaSource := flags.String("colima-source", "", "authenticated exact Colima executable source")
	dockerClientSource := flags.String("docker-client-source", "", "authenticated exact Docker client executable source")
	activeGenerationID := flags.String("active-generation", "", "exact currently active generation")
	activeReleaseRoot := flags.String("active-release-root", "", "currently active installed release root")
	activeReleaseManifest := flags.String("active-release-manifest", "", "currently active root-relative release manifest")
	activeManifestSHA := flags.String("active-manifest-sha256", "", "currently active release manifest digest")
	activeReleaseID := flags.String("active-release-id", "", "currently active release identity")
	o4AuthorityPath := flags.String("o4-acceptance-authority", "", "owner-private O4 acceptance authority")
	o4AuthoritySHA := flags.String("o4-acceptance-authority-sha256", "", "exact O4 acceptance authority digest")
	o4CandidateTuple := flags.String("o4-candidate-tuple", "", "exact O4 candidate tuple identity")
	o4SessionAuthority := flags.String("o4-runner-session-authority", "", "owner-readonly O4 Runner session authority")
	o4SessionAuthoritySHA := flags.String("o4-runner-session-authority-sha256", "", "exact raw O4 Runner session authority digest")
	o4DoctorReport := flags.String("o4-installed-doctor-report", "", "owner-readonly installed Doctor v2 report")
	o4DoctorReportSHA := flags.String("o4-installed-doctor-report-sha256", "", "exact raw installed Doctor report digest")
	o4EvidenceTuple := flags.String("o4-evidence-tuple", "", "exact O4 evidence tuple identity")
	o4A3Ledger := flags.String("o4-a3-ledger", "", "mutable owner-only O4 A3 ledger")
	o4VerifierLedger := flags.String("o4-verifier-ledger", "", "mutable owner-only O4 Verifier ledger")
	o4CResourceDir := flags.String("o4-c-resource-dir", "", "owner-private O4 phase C resource directory")
	o4DResourceDir := flags.String("o4-d-resource-dir", "", "owner-private O4 phase D resource directory")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *databasePath == "" && *dataRoot != "" {
		*databasePath = filepath.Join(*dataRoot, "chora.db")
	}
	if *webRoot == "" && *installRoot != "" {
		*webRoot = filepath.Join(*installRoot, "build", "web-workspace", "web", "dist")
	}
	o4EvidenceValues := []string{*o4SessionAuthority, *o4SessionAuthoritySHA, *o4DoctorReport, *o4DoctorReportSHA, *o4EvidenceTuple, *o4A3Ledger, *o4VerifierLedger, *o4CResourceDir, *o4DResourceDir}
	o4EvidenceEnabled := false
	o4EvidenceComplete := true
	for _, value := range o4EvidenceValues {
		o4EvidenceEnabled = o4EvidenceEnabled || value != ""
		o4EvidenceComplete = o4EvidenceComplete && value != ""
	}
	if flags.NArg() != 0 || *port < 1 || *port > 65535 || !canonicalAbsolute(*sourceRoot) || *sourceRoot != *installRoot ||
		!canonicalAbsolute(*sourceManifest) || *sourceManifest != filepath.Join(*installRoot, sourcebundle.ManifestName) || !validFingerprint(*bundleAggregate) ||
		!canonicalAbsolute(*installRoot) || !canonicalAbsolute(*dataRoot) || !filepath.IsAbs(*repositoryRoot) || filepath.Clean(*repositoryRoot) != *repositoryRoot ||
		*authFile == "" || *caFile == "" || *proxyURL == "" || *modelURL == "" || !validFingerprint(*priorFingerprint) ||
		!canonicalAbsolute(*installationStateRoot) || !productIdentifier.MatchString(*generationID) ||
		!canonicalAbsolute(*dockerCLI) || !productIdentifier.MatchString(*dockerContext) || !validFingerprint(*endpointDigest) ||
		!canonicalAbsolute(*releaseRoot) || *releaseManifest == "" || filepath.IsAbs(*releaseManifest) || filepath.Clean(*releaseManifest) != *releaseManifest ||
		!validFingerprint(*manifestSHA) || !productIdentifier.MatchString(*releaseID) || !canonicalAbsolute(*privatePiRoot) ||
		!canonicalAbsolute(*privatePiSource) || !canonicalAbsolute(*privatePiManifest) || !validFingerprint(*privatePiManifestSHA) ||
		!canonicalAbsolute(*probeRuntimeRoot) || !canonicalAbsolute(*colimaToolRoot) || !productIdentifier.MatchString(*colimaProfile) ||
		!canonicalAbsolute(*colimaSource) || !canonicalAbsolute(*dockerClientSource) || *dockerCLI != filepath.Join(*colimaToolRoot, "docker-"+dockerClientVersion) ||
		!pathInside(*dataRoot, *databasePath) || filepath.Clean(*webRoot) != filepath.Join(*installRoot, "build", "web-workspace", "web", "dist") ||
		pathsOverlap(*repositoryRoot, *sourceRoot) || pathsOverlap(*repositoryRoot, *installRoot) || pathsOverlap(*repositoryRoot, *dataRoot) ||
		o4EvidenceEnabled && (!o4EvidenceComplete || !canonicalAbsolute(*o4SessionAuthority) || !validFingerprint(*o4SessionAuthoritySHA) ||
			!canonicalAbsolute(*o4DoctorReport) || !validFingerprint(*o4DoctorReportSHA) || !validFingerprint(*o4EvidenceTuple) ||
			!canonicalAbsolute(*o4A3Ledger) || !canonicalAbsolute(*o4VerifierLedger) || !canonicalAbsolute(*o4CResourceDir) || !canonicalAbsolute(*o4DResourceDir)) {
		fmt.Fprintln(stderr, "serve requires explicit Preflight paths, identities, prior fingerprint, valid --port, and no positional arguments")
		return 2
	}
	if err := acceptanceauthority.RejectLegacyEnvironment(); err != nil {
		fmt.Fprintln(stderr, "obsolete acceptance environment authority is forbidden")
		return 1
	}
	var loadedAuthority acceptanceauthority.Loaded
	if *o4AuthorityPath != "" || *o4AuthoritySHA != "" || *o4CandidateTuple != "" {
		executable, executableErr := os.Executable()
		if executableErr == nil {
			executable, executableErr = filepath.EvalSymlinks(executable)
		}
		if executableErr != nil {
			fmt.Fprintln(stderr, "installed executable identity unavailable")
			return 1
		}
		loaded, loadErr := acceptanceauthority.Load(acceptanceauthority.StartupInput{
			AuthorityPath: *o4AuthorityPath, AuthoritySHA256: *o4AuthoritySHA, CandidateTuple: *o4CandidateTuple,
			GenerationID: *generationID, DataRoot: *dataRoot, BinaryPath: filepath.Clean(executable),
			SourceAggregate: *bundleAggregate, PolicySHA256: agentpi.PolicySHA256, InstalledProduct: true,
		})
		if loadErr != nil {
			fmt.Fprintln(stderr, "O4 acceptance authority unavailable")
			return 1
		}
		loadedAuthority = loaded
	}
	config := preflight.Config{
		Deadline: 60 * time.Second, SourceRoot: *sourceRoot, InstallRoot: *installRoot,
		DataRoot: *dataRoot, AuthFile: *authFile, CAFile: *caFile, ProxyURL: *proxyURL,
		ModelURL: *modelURL, Port: *port, SourceManifest: *sourceManifest, BundleAggregate: *bundleAggregate,
	}
	candidate := productCommand{
		action:    productinstall.ActionSetup,
		target:    productinstall.EngineTarget{CLIPath: *dockerCLI, ContextName: *dockerContext, EndpointDigest: *endpointDigest},
		stateRoot: *installationStateRoot, releaseRoot: *releaseRoot, releaseManifest: *releaseManifest,
		manifestSHA256: *manifestSHA, releaseID: *releaseID, generationID: *generationID, probeRuntime: *probeRuntimeRoot,
		privatePiRoot: *privatePiRoot, privatePiSource: *privatePiSource, privatePiManifest: *privatePiManifest,
		privatePiManifestSHA256: *privatePiManifestSHA,
		colima: colimaBootstrapRequest{Enabled: true, ToolRoot: *colimaToolRoot, Profile: *colimaProfile,
			ColimaSource: *colimaSource, DockerClientSource: *dockerClientSource},
	}
	installationController, err := newProductInstallationController(candidate, productionProductExecutor{})
	if err != nil {
		fmt.Fprintln(stderr, "installed product lifecycle unavailable")
		return 1
	}
	installationState, err := installationController.backend.Load(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, "installed product state unavailable")
		return 1
	}
	hasActiveGeneration := installationState.ActiveGenerationID != ""
	authorityGeneration := ""
	if loadedAuthority.Enabled() {
		authorityGeneration = loadedAuthority.Document().GenerationID
	}
	if err := requireO4AuthorityActiveGeneration(loadedAuthority.Enabled(), authorityGeneration, installationState.ActiveGenerationID); err != nil {
		fmt.Fprintln(stderr, "O4 acceptance authority requires the exact active installed generation")
		return 1
	}
	var acceptanceController *acceptanceauthority.Controller
	if loadedAuthority.Enabled() {
		acceptanceController, err = loadedAuthority.BindDatabase(context.Background(), *databasePath)
		if err != nil {
			fmt.Fprintln(stderr, "O4 acceptance Task/Snapshot binding unavailable")
			return 1
		}
	}
	installedOptions := localweb.ProductOptions{
		InstallationStateRoot: *installationStateRoot, InstallationController: installationController,
		O4AcceptanceAuthority: acceptanceController,
	}
	serviceProbes := probes
	if o4EvidenceEnabled && !hasActiveGeneration {
		fmt.Fprintln(stderr, "O4 evidence producer requires the exact active installed generation")
		return 1
	}
	if hasActiveGeneration {
		if *activeGenerationID != installationState.ActiveGenerationID || !canonicalAbsolute(*activeReleaseRoot) || *activeReleaseManifest == "" ||
			filepath.IsAbs(*activeReleaseManifest) || filepath.Clean(*activeReleaseManifest) != *activeReleaseManifest ||
			!validFingerprint(*activeManifestSHA) || !productIdentifier.MatchString(*activeReleaseID) {
			fmt.Fprintln(stderr, "active installed release inputs unavailable")
			return 1
		}
		installedOptions, err = loadInstalledRuntime(context.Background(), installedRuntimeInput{
			StateRoot: *installationStateRoot, GenerationID: *activeGenerationID, Target: candidate.target,
			Release:   releaseassets.ActivationInput{Root: *activeReleaseRoot, ManifestPath: *activeReleaseManifest, ManifestSHA256: *activeManifestSHA, ReleaseID: *activeReleaseID},
			PrivatePi: candidate, AllowPrivatePiInstall: true,
		})
		if err != nil {
			fmt.Fprintln(stderr, "installed product activation unavailable")
			return 1
		}
		defer func() {
			if closeErr := installedOptions.CloseOwnedDockerOperationLedger(); closeErr != nil {
				fmt.Fprintln(stderr, "installed Docker operation ledger close failed")
			}
		}()
		installedOptions.InstallationController = installationController
		installedOptions.O4AcceptanceAuthority = acceptanceController
		if o4EvidenceEnabled {
			if loadedAuthority.Enabled() && loadedAuthority.Document().TupleIdentity != *o4EvidenceTuple {
				fmt.Fprintln(stderr, "O4 evidence/fault tuple identities differ")
				return 1
			}
			installedOptions.O4Evidence = &localweb.O4EvidenceOptions{
				RunnerSessionAuthorityPath: *o4SessionAuthority, RunnerSessionAuthoritySHA256: *o4SessionAuthoritySHA,
				InstalledDoctorReportPath: *o4DoctorReport, InstalledDoctorReportSHA256: *o4DoctorReportSHA,
				PreflightFingerprint: *priorFingerprint, TupleIdentity: *o4EvidenceTuple, GenerationID: *activeGenerationID,
				A3LedgerPath: *o4A3Ledger, VerifierLedgerPath: *o4VerifierLedger,
				CResourceDirectory: *o4CResourceDir, DResourceDirectory: *o4DResourceDir,
				ActivatedRelease: installedOptions.ActivatedRelease, LocalPiSelection: installedOptions.LocalPiSelection,
			}
		}
		serviceProbes, err = qualifiedInstalledPreflightProbes(
			context.Background(), probes, installedOptions.DockerRunner, installedOptions.DockerCapability,
			installedOptions.DockerQualification, installedOptions.LocalPiSelection, installedOptions.ActivatedRelease,
			*activeGenerationID, *activeManifestSHA,
		)
		if err != nil {
			fmt.Fprintln(stderr, "installed product runtime preflight unavailable")
			return 1
		}
	}
	report := preflight.Revalidate(context.Background(), config, serviceProbes, preflight.ModeServiceRecovery, *priorFingerprint, false)
	if report.Status != preflight.StatusPassed {
		if report.Failure != nil {
			fmt.Fprintf(stderr, "preflight blocked %s: %s action: %s\n", report.Failure.Boundary, report.Failure.Observed, report.Failure.Action)
		} else {
			fmt.Fprintln(stderr, "preflight blocked without an actionable boundary")
		}
		if hasActiveGeneration {
			return 1
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	logger := log.New(stderr, "chora: ", log.LstdFlags)
	installedOptions.RepoRoot = filepath.Join(*installRoot, "source")
	installedOptions.RepositoryRoot = *repositoryRoot
	installedOptions.PiAuthFile = *authFile
	installedOptions.PiPreflight = func(attemptContext context.Context) preflight.Report {
		return preflight.Revalidate(attemptContext, config, serviceProbes, preflight.ModePreAttempt, *priorFingerprint, true)
	}
	roomServer, err := localweb.NewProduct(ctx, *databasePath, *webRoot, logger, installedOptions)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer func() {
		if closeErr := roomServer.Close(); closeErr != nil {
			fmt.Fprintln(stderr, "installed product close failed")
		}
	}()
	report = preflight.Revalidate(context.Background(), config, serviceProbes, preflight.ModeServiceStart, *priorFingerprint, false)
	if report.Status != preflight.StatusPassed {
		if report.Failure != nil {
			fmt.Fprintf(stderr, "post-recovery preflight blocked %s: %s action: %s\n", report.Failure.Boundary, report.Failure.Observed, report.Failure.Action)
		} else {
			fmt.Fprintln(stderr, "post-recovery preflight blocked without an actionable boundary")
		}
	}

	address := net.JoinHostPort("127.0.0.1", strconv.Itoa(*port))
	httpServer := &http.Server{
		Addr: address, Handler: roomServer.Handler(), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdownContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownContext)
	}()
	fmt.Fprintf(stdout, "Chora Room available at http://%s\n", address)
	if err := listen(httpServer); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func pathsOverlap(left, right string) bool {
	left, right = canonicalPath(left), canonicalPath(right)
	if left == "" || right == "" {
		return true
	}
	return left == right || pathInside(left, right) || pathInside(right, left)
}

func requireO4AuthorityActiveGeneration(enabled bool, authorityGeneration, activeGeneration string) error {
	if !enabled {
		return nil
	}
	if authorityGeneration == "" || activeGeneration == "" || authorityGeneration != activeGeneration {
		return errors.New("O4 acceptance authority generation does not match the active installed generation")
	}
	return nil
}

func canonicalPath(value string) string {
	clean := filepath.Clean(value)
	if resolved, err := filepath.EvalSymlinks(clean); err == nil {
		return resolved
	}
	return clean
}

func validFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func pathInside(root, child string) bool {
	if !filepath.IsAbs(root) || !filepath.IsAbs(child) {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(root), filepath.Clean(child))
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func defaultDatabasePath() string {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return filepath.Join(os.TempDir(), "chora", "chora.db")
	}
	return filepath.Join(home, ".chora", "chora.db")
}
