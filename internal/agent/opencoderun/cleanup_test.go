package opencoderun

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/opencodediscovery"
)

func TestMetadataCleanupRefusesPublicConstructionAndReview(t *testing.T) {
	for _, phase := range []string{"version", "help"} {
		for _, entry := range []string{"new", "reviewed", "validate"} {
			t.Run(phase+"/"+entry, func(t *testing.T) {
				if os.Geteuid() == 0 {
					t.Skip("requires non-root permission enforcement")
				}
				root, err := filepath.EvalSymlinks(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				home := filepath.Join(root, "selected")
				if err := os.Mkdir(home, 0700); err != nil {
					t.Fatal(err)
				}
				observed, executable := filepath.Join(root, "observed"), filepath.Join(root, "fixture")
				argument := "--version"
				if phase == "help" {
					argument = "run --help"
				}
				body := fmt.Sprintf("#!/bin/sh\nif [ \"$*\" = %q ]; then\nmkdir \"$HOME/locked\"\necho retained > \"$HOME/locked/file\"\nchmod 000 \"$HOME/locked\"\nprintf '%%s\\n' \"$HOME\" >> %q\nfi\ncase \"$*\" in --version) echo 1.20.0;; 'run --help') echo '--pure --format --model';; *) exit 99;; esac\n", argument, observed)
				if err := os.WriteFile(executable, []byte(body), 0700); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() {
					raw, _ := os.ReadFile(observed)
					for _, disposable := range strings.Fields(string(raw)) {
						if err := os.Chmod(filepath.Join(disposable, "locked"), 0700); err != nil {
							t.Error(err)
						}
						if err := os.RemoveAll(filepath.Dir(disposable)); err != nil {
							t.Error(err)
						}
					}
				})
				r, _ := reviewService(t)
				c := Config{Executable: executable, Home: home, Model: "deepseek/fixture"}
				switch entry {
				case "new":
					adapter, callErr := New(context.Background(), c)
					err = callErr
					if adapter != nil {
						t.Fatal("failed probe returned adapter")
					}
				case "reviewed":
					adapter, callErr := NewReviewed(context.Background(), c, r)
					err = callErr
					if adapter != nil {
						t.Fatal("failed probe returned reviewed adapter")
					}
				case "validate":
					request, callErr := r.Validate(context.Background(), c, true, time.Hour)
					err = callErr
					if request != (ProtocolReviewRequest{}) || len(r.pending) != 0 {
						t.Fatal("failed probe issued challenge", request)
					}
				}
				if !errors.Is(err, ErrVersion) || !errors.Is(err, opencodediscovery.ErrMetadataCleanup) || !errors.Is(err, opencodediscovery.ErrProbe) {
					t.Fatal("cleanup error chain lost", err)
				}
				if phase == "help" && entry != "validate" {
					var qualification *QualificationError
					if !errors.As(err, &qualification) || !errors.Is(err, ErrNativeConfig) || !errors.Is(err, ErrNativeRetry) || !errors.Is(err, ErrAuthScope) {
						t.Fatal("public refusal diagnostics lost", err)
					}
					for _, evidence := range qualification.Report.Evidence {
						if evidence.Capability == "launch_arguments" && evidence.State == execution.QualificationVerified {
							t.Fatal("failed help metadata was trusted")
						}
					}
				}
				entries, err := os.ReadDir(home)
				if err != nil || len(entries) != 0 {
					t.Fatal("selected scope changed", entries, err)
				}
			})
		}
	}
}
