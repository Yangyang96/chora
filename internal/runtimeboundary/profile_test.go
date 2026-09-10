package runtimeboundary

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestRenderedProfileRunsCompiledProbeAndFreezesDarwinBoundary(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin boundary is exercised only on Darwin")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	runtimeHome := filepath.Join(root, "runtime-home")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runtimeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(root, "chora-product-data.txt")
	runtimeFile := filepath.Join(runtimeHome, "auth.json")
	for path, content := range map[string]string{decoy: "decoy", runtimeFile: "runtime"} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	controlPlane := filepath.Join(root, "chora-control-plane")
	buildFromRepo(t, controlPlane, "./cmd/chora")
	if output, err := exec.Command(controlPlane, "version").CombinedOutput(); err != nil {
		t.Fatalf("control-plane preflight failed outside sandbox: %v\n%s", err, output)
	}
	probeBinary := filepath.Join(root, "runtime-probe")
	buildFromRepo(t, probeBinary, "./spikes/runtime-boundary")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	config := Config{
		Workspace:       workspace,
		RuntimeHomeFile: runtimeFile,
		ForbiddenRead:   decoy,
		ControlPlane:    controlPlane,
		LoopbackAddress: listener.Addr().String(),
		OutsideWrite:    filepath.Join(root, "outside.txt"),
	}
	report, err := Run(context.Background(), config, probeBinary)
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !report.ForbiddenReadDenied || !report.ControlPlaneDenied || !report.LoopbackDenied || !report.OutsideWriteDenied {
		t.Fatalf("required isolation was not proven: %+v", report)
	}
	if !report.WorkspaceReadWriteAllowed || !report.RuntimeHomeReadAllowed {
		t.Fatalf("required access was not preserved: %+v", report)
	}
	if _, err := os.Stat(filepath.Join(workspace, "probe-write.txt")); err != nil {
		t.Fatalf("compiled probe did not write workspace artifact: %v", err)
	}
}

func TestRunRejectsControlPlaneCommandThatFailsWithoutSandbox(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Darwin boundary is exercised only on Darwin")
	}
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	runtimeHome := filepath.Join(root, "runtime-home")
	if err := os.MkdirAll(workspace, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(runtimeHome, 0o700); err != nil {
		t.Fatal(err)
	}
	decoy := filepath.Join(root, "decoy")
	runtimeFile := filepath.Join(runtimeHome, "runtime")
	control := filepath.Join(root, "control")
	for path, content := range map[string]string{
		decoy:       "decoy",
		runtimeFile: "runtime",
		control:     "#!/bin/sh\nexit 2\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	probe := filepath.Join(root, "probe")
	buildFromRepo(t, probe, "./spikes/runtime-boundary")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	_, err = Run(context.Background(), Config{
		Workspace:       workspace,
		RuntimeHomeFile: runtimeFile,
		ForbiddenRead:   decoy,
		ControlPlane:    control,
		LoopbackAddress: listener.Addr().String(),
		OutsideWrite:    filepath.Join(root, "outside"),
	}, probe)
	if err == nil || !strings.Contains(err.Error(), "preflight") {
		t.Fatalf("Run() error = %v, want unsandboxed control-plane preflight failure", err)
	}
}

func TestRenderIncludesOnlyCanonicalAbsoluteInputs(t *testing.T) {
	root := t.TempDir()
	workspace := filepath.Join(root, "chora-workspace")
	productDir := filepath.Join(root, "product")
	binDir := filepath.Join(root, "bin")
	runtimeDir := filepath.Join(root, "runtime")
	for _, directory := range []string{workspace, productDir, binDir, runtimeDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(productDir, "data.json"), filepath.Join(binDir, "chora"), filepath.Join(runtimeDir, "auth.json")} {
		if err := os.WriteFile(path, []byte("fixture"), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	profile, err := Render(Config{
		Workspace:       workspace,
		RuntimeHomeFile: filepath.Join(runtimeDir, "auth.json"),
		ForbiddenRead:   filepath.Join(productDir, "data.json"),
		ControlPlane:    filepath.Join(binDir, "chora"),
		LoopbackAddress: "127.0.0.1:1234",
		OutsideWrite:    filepath.Join(root, "outside.txt"),
	})
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{filepath.Join(canonicalRoot, "chora-workspace"), filepath.Join(canonicalRoot, "product/data.json"), filepath.Join(canonicalRoot, "bin/chora")} {
		if !strings.Contains(string(profile), expected) {
			t.Fatalf("profile missing %q:\n%s", expected, profile)
		}
	}
}

func buildFromRepo(t *testing.T, output, pkg string) {
	t.Helper()
	command := exec.Command("go", "build", "-o", output, pkg)
	command.Dir = filepath.Join("..", "..")
	if combined, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", pkg, err, combined)
	}
}
