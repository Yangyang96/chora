package opencodediscovery

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScratchCleanupInvalidatesMetadataAndPreservesCauses(t *testing.T) {
	for _, mode := range []string{"success", "exit", "cancel"} {
		t.Run(mode, func(t *testing.T) {
			if os.Geteuid() == 0 {
				t.Skip("requires non-root permission enforcement")
			}
			root := t.TempDir()
			selected := filepath.Join(root, "selected")
			if err := os.Mkdir(selected, 0700); err != nil {
				t.Fatal(err)
			}
			canary := filepath.Join(root, "canary")
			if err := os.WriteFile(canary, []byte("external target"), 0400); err != nil {
				t.Fatal(err)
			}
			before, _ := os.Stat(canary)
			observed, binary := filepath.Join(root, "observed"), filepath.Join(root, "fixture")
			tail := "echo 1.19.5\n"
			if mode == "exit" {
				tail += "exit 23\n"
			} else if mode == "cancel" {
				tail += "exec /bin/sleep 30\n"
			}
			body := fmt.Sprintf("#!/bin/sh\nmkdir \"$HOME/locked\"\necho retained > \"$HOME/locked/file\"\nln -s %q \"$HOME/link\"\nchmod 000 \"$HOME/locked\"\nprintf '%%s' \"$HOME\" > %q\n", canary, observed) + tail
			if err := os.WriteFile(binary, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			// Only the test owner restores its deliberate permission barrier.
			t.Cleanup(func() {
				raw, err := os.ReadFile(observed)
				if err != nil {
					return
				}
				home := string(raw)
				if err := os.Chmod(filepath.Join(home, "locked"), 0700); err != nil {
					t.Error(err)
				}
				if err := os.RemoveAll(filepath.Dir(home)); err != nil {
					t.Error(err)
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			type result struct {
				output string
				err    error
			}
			done := make(chan result, 1)
			go func() {
				output, err := Probe(ctx, binary, selected, nil, "--version")
				done <- result{output, err}
			}()
			if mode == "cancel" {
				for {
					if raw, err := os.ReadFile(observed); err == nil && len(raw) != 0 {
						break
					}
					if ctx.Err() != nil {
						t.Fatal("fixture did not become ready")
					}
					time.Sleep(5 * time.Millisecond)
				}
				cancel()
			}
			got := <-done
			if got.output != "" || !errors.Is(got.err, ErrProbe) || !errors.Is(got.err, ErrMetadataCleanup) {
				t.Fatal("cleanup accepted metadata", got)
			}
			if mode == "exit" && !strings.Contains(got.err.Error(), "opencode_metadata_exit_23") {
				t.Fatal("execution cause lost", got.err)
			}
			if mode == "cancel" && !errors.Is(got.err, context.Canceled) {
				t.Fatal("cancellation cause lost", got.err)
			}
			raw, err := os.ReadFile(observed)
			if err != nil {
				t.Fatal(err)
			}
			locked, err := os.Stat(filepath.Join(string(raw), "locked"))
			if err != nil || locked.Mode().Perm() != 0 {
				t.Fatal("production changed locked permissions", locked, err)
			}
			after, err := os.Stat(canary)
			data, readErr := os.ReadFile(canary)
			if err != nil || readErr != nil || string(data) != "external target" || after.Mode() != before.Mode() || !after.ModTime().Equal(before.ModTime()) {
				t.Fatal("external symlink target changed", err, readErr)
			}
			entries, err := os.ReadDir(selected)
			if err != nil || len(entries) != 0 {
				t.Fatal("selected scope changed", entries, err)
			}
		})
	}
}
