package opencoderun

import (
	"context"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/execution"
)

type scopeFile struct {
	Mode     os.FileMode
	Modified int64
	Digest   [32]byte
}

func scopeSnapshot(t *testing.T, root string) map[string]scopeFile {
	t.Helper()
	result := map[string]scopeFile{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		var data []byte
		if info.Mode().IsRegular() {
			data, err = os.ReadFile(path)
			if err != nil {
				return err
			}
		}
		result[path] = scopeFile{info.Mode(), info.ModTime().UnixNano(), sha256.Sum256(data)}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func TestPublicMetadataLeavesSelectedScopeUnchanged(t *testing.T) {
	for _, mode := range []string{"success", "failure", "unknown", "missing_flag", "cancel", "timeout", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			scope := filepath.Join(root, "selected")
			if err := os.Mkdir(scope, 0700); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"home", "data", "config", "cache", "state"} {
				if err := os.Mkdir(filepath.Join(scope, name), 0700); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(filepath.Join(scope, "home", "auth-canary"), []byte("synthetic private scope"), 0600); err != nil {
				t.Fatal(err)
			}
			c := Config{Executable: filepath.Join(root, "metadata"), Home: filepath.Join(scope, "home"), Model: "deepseek/fixture", DataHome: filepath.Join(scope, "data"), ConfigHome: filepath.Join(scope, "config"), CacheHome: filepath.Join(scope, "cache"), StateHome: filepath.Join(scope, "state")}
			version, help := "1.19.5", "--pure --format --model"
			if mode == "unknown" {
				version = "1.20.0"
			}
			if mode == "missing_flag" {
				help = "--format --model"
			}
			script := "#!/bin/sh\nfor target in \"$HOME\" \"$XDG_DATA_HOME\" \"$XDG_CONFIG_HOME\" \"$XDG_CACHE_HOME\" \"$XDG_STATE_HOME\" \"$TMPDIR\" \"$PWD\"; do echo bootstrap > \"$target/metadata.marker\"; done\n"
			if mode == "failure" {
				script += "exit 1\n"
			}
			if mode == "timeout" {
				script += "sleep 30\n"
			}
			script += "case \"$1\" in --version) echo '" + version + "';; run) echo '" + help + "';; *) exit 99;; esac\n"
			if err := os.WriteFile(c.Executable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			if mode == "symlink" {
				alias := filepath.Join(root, "home-alias")
				if err := os.Symlink(c.Home, alias); err != nil {
					t.Fatal(err)
				}
				c.Home = alias
			}
			before := scopeSnapshot(t, scope)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if mode == "cancel" {
				cancel()
			}
			if mode == "timeout" {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, 100*time.Millisecond)
				defer stop()
			}
			a, err := New(ctx, c)
			if a != nil || err == nil {
				t.Fatal("public entry admitted native execution", a, err)
			}
			if mode == "success" && (!errors.Is(err, ErrNativeConfig) || !errors.Is(err, ErrNativeRetry) || !errors.Is(err, ErrAuthentication)) {
				t.Fatal("exact safe-start diagnostics lost", err)
			}
			if mode == "missing_flag" {
				var q *QualificationError
				if !errors.As(err, &q) || !strings.Contains(err.Error(), ErrVersion.Error()) {
					t.Fatal(err)
				}
				found := false
				for _, e := range q.Report.Evidence {
					if e.Capability == "launch_arguments" && e.Reference == "missing_flag:--pure" {
						found = true
					}
				}
				if !found {
					t.Fatal(q.Report)
				}
			}
			if after := scopeSnapshot(t, scope); !reflect.DeepEqual(before, after) {
				t.Fatal("selected scope mutated", before, after)
			}
		})
	}
}

func TestDotenvFileAloneIsNotAnOpenCodeBootstrapFinding(t *testing.T) {
	a := fixture(t, "exit 0\n")
	req := request(t, a)
	if err := os.WriteFile(filepath.Join(req.WorkspaceRoot, ".env"), []byte("PUBLIC_SYNTHETIC=1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := a.PrepareBoundStart(context.Background(), req); err != nil {
		t.Fatal("unsupported dotenv restriction", err)
	}
	if a.Describe().Require("trusted_host", "code", "skills") == nil || a.Describe().Require("trusted_host", "code", "mcp") == nil {
		t.Fatal(execution.ErrUnsupportedCapability)
	}
}
