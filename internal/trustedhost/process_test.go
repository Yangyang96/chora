package trustedhost

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestOSPlatformStartsDedicatedProcessGroupWithBirthIdentityAndReaps(t *testing.T) {
	root := t.TempDir()
	stdout, err := os.OpenFile(filepath.Join(root, "stdout"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	stderr, err := os.OpenFile(filepath.Join(root, "stderr"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	platform := newOSPlatform()
	executable, err := InspectExecutable("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	child, err := platform.Start(processSpec{
		ExecutableIdentity: executable, Arguments: []string{"-c", "exec sleep 30"}, Directory: root,
		Environment: []string{"PATH=/usr/bin:/bin"}, Stdout: stdout, Stderr: stderr,
		GateReceiptPath: filepath.Join(root, "gate-receipt.json"), GateNonce: "test-gate-nonce", GateWait: time.Second,
	})
	_ = stdout.Close()
	_ = stderr.Close()
	if err != nil {
		t.Fatal(err)
	}
	reaped := false
	defer func() {
		if !reaped {
			_ = platform.SignalGroup(child.PID(), syscall.SIGKILL)
			_, _ = child.Wait()
		}
	}()
	observation, err := platform.Observe(child.PID())
	if err != nil || observation.PID != child.PID() || observation.PGID != child.PID() || observation.Birth == "" {
		t.Fatalf("process observation = %#v, %v", observation, err)
	}
	if err := child.Release(); err != nil {
		t.Fatal(err)
	}
	if err := platform.SignalGroup(observation.PGID, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	if _, err := child.Wait(); err == nil {
		t.Fatal("signal-killed child returned no wait error")
	}
	reaped = true
	dead, err := platform.WaitGroupGone(context.Background(), observation.PGID, time.Second, time.Millisecond)
	if err != nil || !dead {
		t.Fatalf("process group death = %v, %v", dead, err)
	}
}

func TestOSPlatformGateEOFPreventsTargetExecution(t *testing.T) {
	root := t.TempDir()
	marker := filepath.Join(root, "target-executed")
	executable, err := InspectExecutable("/bin/sh")
	if err != nil {
		t.Fatal(err)
	}
	platform := newOSPlatform()
	child, err := platform.Start(processSpec{
		ExecutableIdentity: executable,
		Arguments:          []string{"-c", "printf executed > target-executed"},
		Environment:        []string{"PATH=/usr/bin:/bin"},
		Directory:          root,
		GateReceiptPath:    filepath.Join(root, "gate-receipt.json"),
		GateNonce:          "crash-window-gate-nonce",
		GateWait:           time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := child.AbortGate(); err != nil {
		t.Fatal(err)
	}
	_, _ = child.Wait()
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("target crossed an unreleased gate: %v", err)
	}
	gone, err := platform.WaitGroupGone(context.Background(), child.PID(), time.Second, time.Millisecond)
	if err != nil || !gone {
		t.Fatalf("aborted gate process group death = %v, %v", gone, err)
	}
}
