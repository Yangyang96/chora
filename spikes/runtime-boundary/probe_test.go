package main

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/runtimeboundary"
)

func TestCompiledProbeAttemptsEveryBoundaryOperation(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	runtimeHome := filepath.Join(root, "runtime-home")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runtimeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(root, "product-data.txt")
	runtimeFile := filepath.Join(runtimeHome, "required.txt")
	control := filepath.Join(root, "chora-control")
	for path, content := range map[string]string{
		decoy:       "decoy",
		runtimeFile: "required",
		control:     "#!/bin/sh\nexit 0\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	probe := filepath.Join(root, "probe")
	command := exec.Command("go", "build", "-o", probe, ".")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build probe: %v\n%s", err, output)
	}
	output, err := exec.Command(probe, probeArgs(runtimeboundary.Config{
		Workspace:       workspace,
		RuntimeHomeFile: runtimeFile,
		ForbiddenRead:   decoy,
		ControlPlane:    control,
		LoopbackAddress: listener.Addr().String(),
		OutsideWrite:    filepath.Join(root, "outside.txt"),
	})...).Output()
	if err != nil {
		t.Fatalf("run compiled probe: %v", err)
	}
	var report runtimeboundary.ProbeReport
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("decode report: %v\n%s", err, output)
	}
	if report.ForbiddenReadDenied || report.ControlPlaneDenied || report.LoopbackDenied || report.OutsideWriteDenied {
		t.Fatalf("unsandboxed forbidden operations unexpectedly denied: %+v", report)
	}
	if !report.WorkspaceReadWriteAllowed || !report.RuntimeHomeReadAllowed {
		t.Fatalf("allowed operations failed: %+v", report)
	}
}

func probeArgs(config runtimeboundary.Config) []string {
	return []string{
		"--workspace", config.Workspace,
		"--runtime-home-file", config.RuntimeHomeFile,
		"--forbidden-read", config.ForbiddenRead,
		"--control-plane", config.ControlPlane,
		"--loopback", config.LoopbackAddress,
		"--outside-write", config.OutsideWrite,
	}
}
