package opencodediscovery

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func waitFixturePID(t *testing.T, path string) int {
	t.Helper()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		raw, err := os.ReadFile(path)
		pid, parseErr := strconv.Atoi(strings.TrimSpace(string(raw)))
		if err == nil && parseErr == nil && pid > 1 {
			return pid
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("fixture did not start")
	return 0
}

func TestMetadataProbeGroupLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "failure", "overflow", "cancel", "timeout", "parent_exit_inherited", "parent_exit_redirected"} {
		t.Run(mode, func(t *testing.T) {
			root, _ := filepath.EvalSymlinks(t.TempDir())
			home := filepath.Join(root, "selected")
			if err := os.Mkdir(home, 0700); err != nil {
				t.Fatal(err)
			}
			executable := filepath.Join(root, "metadata")
			pidPath, markerPath, scratchPath := filepath.Join(root, "child.pid"), filepath.Join(root, "late.marker"), filepath.Join(root, "scratch.path")
			delay := "1"
			if mode == "timeout" {
				delay = "7"
			}
			child := "/bin/sh -c 'sleep " + delay + "; echo survived > " + markerPath + "'"
			if mode == "parent_exit_redirected" || mode == "cancel" || mode == "timeout" {
				child += " >/dev/null 2>&1"
			}
			script := "#!/bin/sh\npwd > " + scratchPath + "\n" + child + " &\necho $! > " + pidPath + "\necho metadata\n"
			switch mode {
			case "success":
				script += "wait\n"
			case "failure":
				script += "wait\nexit 7\n"
			case "overflow":
				script += "/usr/bin/head -c 40000 /dev/zero\nwait\n"
			case "cancel", "timeout":
				script += "wait\nsleep 30\n"
			case "parent_exit_inherited", "parent_exit_redirected":
				script += "exit 0\n"
			}
			if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			done := make(chan error, 1)
			go func() { _, err := Probe(ctx, executable, home, nil, "--version"); done <- err }()
			pid := waitFixturePID(t, pidPath)
			if mode == "cancel" || mode == "timeout" {
				if err := syscall.Kill(pid, 0); err != nil {
					t.Fatal("fixture not alive before stop", err)
				}
			}
			if mode == "cancel" {
				cancel()
			}
			var err error
			select {
			case err = <-done:
			case <-time.After(8 * time.Second):
				t.Fatal("probe did not converge")
			}
			if mode == "success" && err != nil {
				t.Fatal(err)
			}
			if (mode == "cancel" || mode == "timeout" || mode == "failure" || mode == "overflow") && !errors.Is(err, ErrProbe) {
				t.Fatal("missing failure", err)
			}
			if syscall.Kill(pid, 0) == nil {
				t.Fatal("descendant alive after probe returned", mode)
			}
			if mode != "success" && mode != "failure" {
				time.Sleep(1100 * time.Millisecond)
				if _, err := os.Stat(markerPath); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("descendant wrote after cleanup", mode, err)
				}
			}
			raw, readErr := os.ReadFile(scratchPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if _, err := os.Stat(filepath.Dir(strings.TrimSpace(string(raw)))); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("metadata scratch survived", err)
			}
			entries, err := os.ReadDir(home)
			if err != nil || len(entries) != 0 {
				t.Fatal("selected scope changed", entries, err)
			}
		})
	}
}

func TestMetadataScopeAndTemporaryBase(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	home := filepath.Join(root, "selected")
	outside := filepath.Join(root, "temporary")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outside, 0700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "metadata")
	script := "#!/bin/sh\nfor target in \"$HOME\" \"$XDG_DATA_HOME\" \"$XDG_CONFIG_HOME\" \"$XDG_CACHE_HOME\" \"$XDG_STATE_HOME\" \"$TMPDIR\" \"$PWD\"; do echo bootstrap > \"$target/metadata.marker\"; done\necho metadata\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"outside", "inside_home", "inside_xdg", "symlink_home", "symlink_xdg"} {
		t.Run(mode, func(t *testing.T) {
			env := map[string]string{"HOME": home, "XDG_CONFIG_HOME": home}
			base := outside
			if mode == "inside_home" {
				base = home
			}
			if mode == "inside_xdg" {
				env["XDG_DATA_HOME"] = outside
			}
			if mode == "symlink_home" || mode == "symlink_xdg" {
				alias := filepath.Join(root, "alias-"+mode)
				if err := os.Symlink(home, alias); err != nil {
					t.Fatal(err)
				}
				base = alias
				if mode == "symlink_xdg" {
					env["XDG_STATE_HOME"] = alias
				}
			}
			t.Setenv("TMPDIR", base)
			out, err := Probe(context.Background(), executable, home, env, "--version")
			if mode == "outside" {
				if err != nil || out != "metadata\n" {
					t.Fatal(out, err)
				}
			} else if !errors.Is(err, ErrProbe) {
				t.Fatal("unsafe temp base accepted", err)
			}
			for _, path := range []string{home, outside} {
				entries, err := os.ReadDir(path)
				if err != nil || len(entries) != 0 {
					t.Fatal("selected or temp scope changed", path, entries, err)
				}
			}
		})
	}
}
