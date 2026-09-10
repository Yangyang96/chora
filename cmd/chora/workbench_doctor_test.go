package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/piinstall"
	"github.com/Yangyang96/chora/migrations"
	_ "modernc.org/sqlite"
)

func TestWorkbenchDoctorReportsOnlyBoundedRedactedState(t *testing.T) {
	root := canonicalDoctorTempDir(t)
	source := filepath.Join(root, "source-private-name")
	data := filepath.Join(root, "data-private-name")
	for _, path := range []string{source, data, filepath.Join(data, "task-workspaces"), filepath.Join(data, "pi-sessions"), filepath.Join(data, "pi"), filepath.Join(data, "runtime")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	createDoctorDatabase(t, filepath.Join(data, "chora.db"))
	secret := "TOKEN-secret-private-transcript"
	runner := func(_ context.Context, name string, args ...string) (string, error) {
		switch name {
		case "git":
			return strings.Repeat("a", 40) + "\n" + secret, nil
		case "node":
			return "v22.19.0\n" + secret, nil
		case "npm":
			return "11.0.0\n" + secret, nil
		case "pi":
			return "pi 0.85.1\n" + secret, nil
		default:
			return "", os.ErrNotExist
		}
	}
	var stdout, stderr bytes.Buffer
	code := runWorkbenchDoctorWithOptions([]string{"--source", source, "--data", data, "--json"}, &stdout, &stderr, workbenchDoctorOptions{runVersion: runner, goVersion: "go1.26.5 " + secret})
	if code != 0 {
		t.Fatalf("code=%d stderr=%s stdout=%s", code, stderr.String(), stdout.String())
	}
	for _, forbidden := range []string{root, "private-name", secret, "TOKEN", "transcript"} {
		if strings.Contains(stdout.String(), forbidden) {
			t.Fatalf("report leaked %q: %s", forbidden, stdout.String())
		}
	}
	var report workbenchDoctorReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.Status != "ready" || report.Data.Schema != workbenchSchemaVersion || report.Data.Integrity != "ok" {
		t.Fatalf("unexpected report: %+v", report)
	}
	if report.Build.SourceRevision != strings.Repeat("a", 40) {
		t.Fatalf("revision=%q", report.Build.SourceRevision)
	}
	if len(report.Counts) != 12 {
		t.Fatalf("counts=%v", report.Counts)
	}
	if report.Prerequisites[0].Observed != "1.26.5" {
		t.Fatalf("Go version was not normalized correctly: %+v", report.Prerequisites[0])
	}
}

func TestWorkbenchDoctorMakesSchemaAndPrerequisiteFailuresActionable(t *testing.T) {
	root := canonicalDoctorTempDir(t)
	source := filepath.Join(root, "source")
	data := filepath.Join(root, "data")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	createDoctorDatabaseThroughVersion(t, filepath.Join(data, "chora.db"), workbenchSchemaVersion-1)
	runner := func(_ context.Context, name string, _ ...string) (string, error) {
		switch name {
		case "git":
			return "not-a-revision /Users/private", nil
		case "node":
			return "v20.1.0", nil
		case "npm":
			return "11.1.0", nil
		case "pi":
			return "credential at /Users/private", nil
		default:
			return "", os.ErrNotExist
		}
	}
	var stdout, stderr bytes.Buffer
	code := runWorkbenchDoctorWithOptions([]string{"--source", source, "--data", data}, &stdout, &stderr, workbenchDoctorOptions{runVersion: runner, goVersion: "go1.26.5"})
	if code != 1 {
		t.Fatalf("code=%d stderr=%s", code, stderr.String())
	}
	output := stdout.String()
	for _, expected := range []string{"attention_required", "schema_upgrade_required", "make a whole-data-root backup", "node status=incompatible", "pi status=unknown", "configure Pi with /login and /model"} {
		if !strings.Contains(output, expected) {
			t.Fatalf("missing %q in %s", expected, output)
		}
	}
	for _, forbidden := range []string{"/Users/private", "credential"} {
		if strings.Contains(output, forbidden) {
			t.Fatalf("leaked %q in %s", forbidden, output)
		}
	}
}

func TestWorkbenchDoctorDoesNotCreateMissingData(t *testing.T) {
	root := canonicalDoctorTempDir(t)
	source := filepath.Join(root, "source")
	data := filepath.Join(root, "missing-data")
	if err := os.MkdirAll(source, 0700); err != nil {
		t.Fatal(err)
	}
	runner := func(_ context.Context, name string, _ ...string) (string, error) {
		if name == "git" {
			return strings.Repeat("b", 40), nil
		}
		return "99.99.99", nil
	}
	var stdout, stderr bytes.Buffer
	code := runWorkbenchDoctorWithOptions([]string{"--source", source, "--data", data}, &stdout, &stderr, workbenchDoctorOptions{runVersion: runner, goVersion: "go99.99.99"})
	if code != 1 || !strings.Contains(stdout.String(), "database_missing") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, stdout.String(), stderr.String())
	}
	if _, err := os.Stat(data); !os.IsNotExist(err) {
		t.Fatalf("doctor created or changed missing data root: %v", err)
	}
}

func canonicalDoctorTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func createDoctorDatabase(t *testing.T, path string) {
	t.Helper()
	createDoctorDatabaseThroughVersion(t, path, workbenchSchemaVersion)
}

func createDoctorDatabaseThroughVersion(t *testing.T, path string, through int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE schema_migrations (version INTEGER PRIMARY KEY, name TEXT NOT NULL UNIQUE, checksum BLOB NOT NULL, applied_at TEXT NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	entries, err := migrations.Files.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	version := 0
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		version++
		if version > through {
			break
		}
		body, err := migrations.Files.ReadFile(entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		digest := sha256.Sum256(body)
		if _, err := db.Exec(`INSERT INTO schema_migrations(version,name,checksum,applied_at) VALUES(?,?,?,?)`, version, entry.Name(), digest[:], time.Now().UTC().Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if through == workbenchSchemaVersion {
		for _, table := range []string{"projects", "rooms", "tasks", "runs", "attempts", "resource_result_groups", "runtime_sessions", "task_repository_worktrees", "task_worktrees", "resource_result_reviews", "task_delivery_operations", "apply_operations"} {
			if _, err := db.Exec(`CREATE TABLE ` + table + ` (id TEXT)`); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestWorkbenchDoctorSchemaMatchesEmbeddedMigrations(t *testing.T) {
	identities, err := expectedMigrationIdentities()
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != workbenchSchemaVersion {
		t.Fatalf("supported schema=%d embedded migrations=%d", workbenchSchemaVersion, len(identities))
	}
}

type doctorPiInstaller struct {
	state        piinstall.State
	inspectErr   error
	selection    piinstall.Selection
	selectionErr error
	resolved     int
}

func (f *doctorPiInstaller) Inspect(context.Context) (piinstall.State, error) {
	return f.state, f.inspectErr
}

func (f *doctorPiInstaller) ResolveSelection(context.Context) (piinstall.Selection, error) {
	f.resolved++
	return f.selection, f.selectionErr
}

func (f *doctorPiInstaller) Install(context.Context, piinstall.InstallRequest) (piinstall.Outcome, error) {
	panic("doctor must not install Pi")
}

func (f *doctorPiInstaller) Cancel(context.Context, string) error {
	panic("doctor must not cancel Pi installation")
}

func TestWorkbenchDoctorPrefersValidatedManagedPiWithoutPATHFallback(t *testing.T) {
	managed := &doctorPiInstaller{
		state:     piinstall.State{Code: piinstall.StateInstalled, SelectionPresent: true},
		selection: piinstall.Selection{PiVersion: "0.85.1", ExecutablePath: "/private/managed/pi"},
	}
	pathCalls := 0
	result := inspectPiPrerequisite(context.Background(), t.TempDir(), workbenchDoctorOptions{
		newPiInstaller: func(string) (piinstall.Installer, error) { return managed, nil },
		runVersion: func(_ context.Context, name string, _ ...string) (string, error) {
			if name == "pi" {
				pathCalls++
			}
			return "", os.ErrNotExist
		},
	})
	if result.Status != "ready" || result.Observed != "0.85.1" || managed.resolved != 1 || pathCalls != 0 {
		t.Fatalf("result=%+v resolved=%d PATH calls=%d", result, managed.resolved, pathCalls)
	}
	if strings.Contains(result.Observed+result.Action, "/private/") {
		t.Fatalf("managed path leaked: %+v", result)
	}
}

func TestWorkbenchDoctorRejectsInvalidManagedPiWithoutPATHFallback(t *testing.T) {
	for _, test := range []struct {
		name      string
		state     piinstall.State
		writeLink bool
	}{
		{name: "claimed installed selection missing", state: piinstall.State{Code: piinstall.StateInstalled}},
		{name: "drifted selection", state: piinstall.State{Code: piinstall.StateDrifted, SelectionPresent: true}, writeLink: true},
		{name: "stale installing state", state: piinstall.State{Code: piinstall.StateInstalling}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			if test.writeLink {
				if err := os.MkdirAll(filepath.Join(root, "pi"), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("missing-selection", filepath.Join(root, "pi", "selection.json")); err != nil {
					t.Fatal(err)
				}
			}
			managed := &doctorPiInstaller{state: test.state, selectionErr: errors.New("selection unavailable")}
			pathCalls := 0
			result := inspectPiPrerequisite(context.Background(), root, workbenchDoctorOptions{
				newPiInstaller: func(string) (piinstall.Installer, error) { return managed, nil },
				runVersion: func(_ context.Context, name string, _ ...string) (string, error) {
					if name == "pi" {
						pathCalls++
					}
					return "0.85.1", nil
				},
			})
			if result.Status != "managed_invalid" || pathCalls != 0 {
				t.Fatalf("result=%+v PATH calls=%d", result, pathCalls)
			}
		})
	}
}

func TestWorkbenchDoctorUsesPATHOnlyWithoutManagedState(t *testing.T) {
	managed := &doctorPiInstaller{state: piinstall.State{Code: piinstall.StateMissing}, selectionErr: os.ErrNotExist}
	pathCalls := 0
	result := inspectPiPrerequisite(context.Background(), t.TempDir(), workbenchDoctorOptions{
		newPiInstaller: func(string) (piinstall.Installer, error) { return managed, nil },
		runVersion: func(_ context.Context, name string, args ...string) (string, error) {
			if name != "pi" || len(args) != 1 || args[0] != "--version" {
				t.Fatalf("unexpected command: %s %v", name, args)
			}
			pathCalls++
			return "0.85.1", nil
		},
	})
	if result.Status != "ready" || result.Observed != "0.85.1" || pathCalls != 1 {
		t.Fatalf("result=%+v PATH calls=%d", result, pathCalls)
	}
}
