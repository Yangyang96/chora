package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/desktop"
)

func TestDesktopWorkbenchProcess(t *testing.T) {
	if os.Getenv("CHORA_DESKTOP_TEST_CHILD") == "1" {
		os.Exit(runWorkbench([]string{"--source", os.Getenv("CHORA_DESKTOP_TEST_SOURCE"), "--data", os.Getenv("CHORA_DESKTOP_TEST_DATA"), "--port", "0", "--desktop-token", "desktop-test-token-at-least-32-characters", "--desktop-ready", os.Getenv("CHORA_DESKTOP_TEST_READY")}, os.Stdout, os.Stderr))
	}
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source, root, ready := filepath.Join(base, "bundle"), filepath.Join(base, "data"), filepath.Join(base, "ready.json")
	if err = os.MkdirAll(filepath.Join(source, "web", "dist"), 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(source, "web", "dist", "index.html"), []byte("desktop fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	start := func() (*exec.Cmd, string) {
		t.Helper()
		cmd := exec.Command(os.Args[0], "-test.run=^TestDesktopWorkbenchProcess$")
		cmd.Env = append(os.Environ(), "CHORA_DESKTOP_TEST_CHILD=1", "CHORA_DESKTOP_TEST_SOURCE="+source, "CHORA_DESKTOP_TEST_DATA="+root, "CHORA_DESKTOP_TEST_READY="+ready, "PATH=/usr/bin:/bin", "PI_CODING_AGENT_DIR="+filepath.Join(base, "pi"))
		log, e := os.CreateTemp(base, "process-*.log")
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { log.Close() })
		cmd.Stdout = log
		cmd.Stderr = log
		if e = cmd.Start(); e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() {
			if cmd.ProcessState == nil {
				cmd.Process.Kill()
				cmd.Wait()
			}
		})
		deadline := time.Now().Add(20 * time.Second)
		for time.Now().Before(deadline) {
			b, e := os.ReadFile(ready)
			var state struct {
				URL string `json:"url"`
				PID int    `json:"pid"`
			}
			if e == nil && json.Unmarshal(b, &state) == nil && state.PID == cmd.Process.Pid && state.URL != "" {
				return cmd, state.URL
			}
			time.Sleep(25 * time.Millisecond)
		}
		logs, _ := os.ReadFile(log.Name())
		t.Fatalf("not ready: %s", logs)
		return nil, ""
	}
	stop := func(cmd *exec.Cmd) {
		t.Helper()
		if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		select {
		case e := <-done:
			if e != nil {
				t.Fatal(e)
			}
		case <-time.After(15 * time.Second):
			t.Fatal("shutdown timed out")
		}
		if _, e := os.Stat(ready); !os.IsNotExist(e) {
			t.Fatalf("ready file survived exit: %v", e)
		}
	}
	cmd, url := start()
	client := &http.Client{Timeout: 5 * time.Second}
	request := func(token string, want int) {
		t.Helper()
		req, _ := http.NewRequest("GET", url+"/desktop/status", nil)
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, e := client.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		if res.StatusCode != want {
			b, _ := io.ReadAll(res.Body)
			t.Fatalf("status %d: %s", res.StatusCode, b)
		}
		if want == 200 {
			var state desktop.ActivityReport
			if json.NewDecoder(res.Body).Decode(&state) != nil || !state.Idle {
				t.Fatalf("unexpected activity: %+v", state)
			}
		}
	}
	request("", 401)
	request("incorrect", 401)
	request("desktop-test-token-at-least-32-characters", 200)
	if lock, e := desktop.AcquireOwner(root); !errors.Is(e, desktop.ErrInUse) {
		if lock != nil {
			lock.Close()
		}
		t.Fatalf("live root not protected: %v", e)
	}
	if _, e := desktop.Backup(root, filepath.Join(base, "live-backup")); !errors.Is(e, desktop.ErrInUse) {
		t.Fatalf("live backup accepted: %v", e)
	}
	stop(cmd)
	backup := filepath.Join(base, "backup")
	if _, err = desktop.Backup(root, backup); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(root, "newer-data"), []byte("retain on recovery"), 0600); err != nil {
		t.Fatal(err)
	}
	recovery, err := desktop.Restore(root, backup)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(recovery, "newer-data")); err != nil {
		t.Fatal(err)
	}
	cmd, url = start()
	request("desktop-test-token-at-least-32-characters", 200)
	stop(cmd)
}
