package codexdiscovery

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReadOnlyProbeRejectsExecutionAndBoundsDiagnostics(t *testing.T) {
	for _, args := range [][]string{{"exec"}, {"login"}, {"logout"}, {"login", "--device-auth"}} {
		if _, err := Probe(context.Background(), "/nonexistent", "/nonexistent", "/nonexistent", args...); err != ErrProbe {
			t.Fatal("mutating probe accepted")
		}
	}
	b := &boundedOutput{}
	_, _ = b.Write([]byte(strings.Repeat("x", 4097)))
	if !b.exceeded || len(b.data) > 4096 {
		t.Fatal("unbounded output")
	}
}

// Simulates native pre-parse bootstrap; neither selected directory may be used.
func TestMetadataBootstrapIsolationAndCleanup(t *testing.T) {
	for _, mode := range []string{"success", "failure", "cancel", "timeout", "overflow"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			home, auth := filepath.Join(root, "selected-home"), filepath.Join(root, "selected-auth")
			for _, p := range []string{home, auth} {
				if err := os.Mkdir(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			marker := filepath.Join(root, "observed")
			body := "printf '%s\\n%s\\n%s\\n' \"$HOME\" \"$CODEX_HOME\" \"$PWD\" > '" + marker + "'\nmkdir -p \"$CODEX_HOME/tmp\"\n"
			switch mode {
			case "failure":
				body += "exit 1\n"
			case "cancel", "timeout":
				body += "sleep 30\n"
			case "overflow":
				body += "head -c 40000 /dev/zero\n"
			default:
				body += "echo codex-cli 0.168.0\n"
			}
			binary := filepath.Join(root, "probe")
			if err := os.WriteFile(binary, []byte("#!/bin/sh\n"+body), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "timeout" {
				var c context.CancelFunc
				ctx, c = context.WithTimeout(ctx, 2*time.Second)
				defer c()
			}
			if mode == "cancel" {
				go func() {
					for ctx.Err() == nil {
						if _, err := os.Stat(marker); err == nil {
							cancel()
							return
						}
						time.Sleep(time.Millisecond)
					}
				}()
			}
			_, err := Probe(ctx, binary, home, auth, "--version")
			if (err == nil) != (mode == "success") {
				t.Fatal(mode, err)
			}
			raw, err := os.ReadFile(marker)
			if err != nil {
				t.Fatal(err)
			}
			dirs := strings.Fields(string(raw))
			if len(dirs) != 3 || dirs[0] == home || dirs[1] == auth || dirs[0] != dirs[2] {
				t.Fatal("selected scope used", dirs)
			}
			if _, err := os.Stat(dirs[0]); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("disposable root leaked", dirs, err)
			}
			for _, p := range []string{home, auth} {
				entries, err := os.ReadDir(p)
				if err != nil || len(entries) != 0 {
					t.Fatal("selected scope changed", p, entries, err)
				}
			}
		})
	}
}

func TestMetadataRejectsTempRootInsideSelectedScopeBeforeWriting(t *testing.T) {
	for _, scope := range []string{"home", "auth"} {
		t.Run(scope, func(t *testing.T) {
			root := t.TempDir()
			home, auth := filepath.Join(root, "home"), filepath.Join(root, "auth")
			for _, p := range []string{home, auth} {
				if err := os.Mkdir(p, 0700); err != nil {
					t.Fatal(err)
				}
			}
			selected := home
			if scope == "auth" {
				selected = auth
			}
			t.Setenv("TMPDIR", selected)
			if _, err := Probe(context.Background(), "/bin/echo", home, auth, "--version"); err != ErrProbe {
				t.Fatal("selected scope used as temp root", err)
			}
			for _, p := range []string{home, auth} {
				entries, err := os.ReadDir(p)
				if err != nil || len(entries) != 0 {
					t.Fatal("rejected probe wrote selected scope", p, entries, err)
				}
			}
		})
	}
}

func TestMetadataCleanupFailurePreservesExecutionCause(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires an unprivileged process")
	}
	for _, mode := range []string{"success", "exit", "overflow", "cancel", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			home, auth := filepath.Join(root, "home"), filepath.Join(root, "auth")
			for _, scope := range []string{home, auth} {
				if err := os.Mkdir(scope, 0700); err != nil {
					t.Fatal(err)
				}
			}
			observed := filepath.Join(root, "observed")
			binary := filepath.Join(root, "metadata")
			body := "#!/bin/sh\nmkdir \"$HOME/locked\"\necho fixture > \"$HOME/locked/retained\"\nchmod 000 \"$HOME/locked\"\nprintf '%s' \"$HOME\" > '" + observed + "'\n"
			switch mode {
			case "exit":
				body += "exit 23\n"
			case "overflow":
				body += "head -c 40000 /dev/zero\n"
			case "cancel", "timeout":
				body += "sleep 30\n"
			default:
				body += "echo codex-cli 0.168.0\n"
			}
			if err := os.WriteFile(binary, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			// Only the fixture owner restores its deliberately locked directory.
			// Production cleanup must report uncertainty without changing modes.
			t.Cleanup(func() {
				raw, err := os.ReadFile(observed)
				if err != nil {
					t.Error(err)
					return
				}
				metadata := string(raw)
				if err := os.Chmod(filepath.Join(metadata, "locked"), 0700); err != nil {
					t.Error(err)
				}
				if err := os.RemoveAll(metadata); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "timeout" {
				var deadlineCancel context.CancelFunc
				ctx, deadlineCancel = context.WithTimeout(ctx, 2*time.Second)
				defer deadlineCancel()
			}
			if mode == "cancel" {
				go func() {
					for ctx.Err() == nil {
						if _, err := os.Stat(observed); err == nil {
							cancel()
							return
						}
						time.Sleep(time.Millisecond)
					}
				}()
			}
			began := time.Now()
			out, err := Probe(ctx, binary, home, auth, "--version")
			if out != "" || !errors.Is(err, ErrProbe) || !errors.Is(err, ErrCleanup) {
				t.Fatalf("metadata escaped cleanup failure: %q %v", out, err)
			}
			if elapsed := time.Since(began); elapsed > 7*time.Second {
				t.Fatal("cleanup failure did not return promptly", elapsed)
			}
			switch mode {
			case "exit":
				var exit *exec.ExitError
				if !errors.As(err, &exit) || exit.ExitCode() != 23 {
					t.Fatal("execution failure lost", err)
				}
			case "cancel":
				if !errors.Is(err, context.Canceled) {
					t.Fatal("cancellation lost", err)
				}
			case "timeout":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal("deadline failure lost", err)
				}
			}
			raw, err := os.ReadFile(observed)
			if err != nil {
				t.Fatal("fixture did not reach its permission failure", err)
			}
			info, err := os.Lstat(filepath.Join(string(raw), "locked"))
			if err != nil || info.Mode().Perm() != 0 {
				t.Fatal("cleanup changed locked directory permissions", info, err)
			}
			for _, scope := range []string{home, auth} {
				entries, err := os.ReadDir(scope)
				if err != nil || len(entries) != 0 {
					t.Fatal("selected scope changed", entries, err)
				}
			}
		})
	}
}

func TestMetadataCleanupDoesNotFollowSymlinks(t *testing.T) {
	root := t.TempDir()
	home, auth := filepath.Join(root, "home"), filepath.Join(root, "auth")
	for _, scope := range []string{home, auth} {
		if err := os.Mkdir(scope, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(scope, "retained"), []byte("selected scope"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	observed, binary := filepath.Join(root, "observed"), filepath.Join(root, "metadata")
	body := "#!/bin/sh\nprintf '%s' \"$HOME\" > '" + observed + "'\nln -s '" + home + "' \"$HOME/home-link\"\nln -s '" + auth + "' \"$HOME/auth-link\"\necho codex-cli 0.168.0\n"
	if err := os.WriteFile(binary, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	if out, err := Probe(context.Background(), binary, home, auth, "--version"); err != nil || strings.TrimSpace(out) != "codex-cli 0.168.0" {
		t.Fatal(out, err)
	}
	metadata, err := os.ReadFile(observed)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(string(metadata)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("temporary symlinks retained", err)
	}
	for _, scope := range []string{home, auth} {
		raw, err := os.ReadFile(filepath.Join(scope, "retained"))
		if err != nil || string(raw) != "selected scope" {
			t.Fatal("cleanup followed a symlink", err)
		}
	}
}
