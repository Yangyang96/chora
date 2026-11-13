package codexdiscovery

import (
	"context"
	"errors"
	"os"
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
