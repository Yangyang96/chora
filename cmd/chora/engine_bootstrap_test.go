package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/Yangyang96/chora/internal/productinstall"
)

func TestColimaBootstrapLeavesCompatibleEngineUntouched(t *testing.T) {
	ports := &fakeColimaPorts{failOnCall: true}
	if err := bootstrapColima(context.Background(), true, colimaBootstrapRequest{}, ports); err != nil {
		t.Fatal(err)
	}
	if ports.downloads != 0 || ports.runs != 0 {
		t.Fatalf("compatible Engine side effects = downloads %d runs %d", ports.downloads, ports.runs)
	}
}

func TestColimaBootstrapWithoutBothAuthoritiesIsZeroMutation(t *testing.T) {
	parent := canonicalTempRoot(t)
	root := filepath.Join(parent, "tools")
	request := colimaBootstrapRequest{Enabled: true, ToolRoot: root, Profile: "chora-local"}
	ports := &fakeColimaPorts{}
	if err := bootstrapColima(context.Background(), false, request, ports); !errors.Is(err, productinstall.ErrAuthorityRequired) {
		t.Fatalf("bootstrap error = %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) || ports.downloads != 0 || ports.runs != 0 {
		t.Fatalf("unauthorized bootstrap mutated state: stat=%v downloads=%d runs=%d", err, ports.downloads, ports.runs)
	}
}

func TestColimaBootstrapRejectsMissingRootUnderSymlinkedParent(t *testing.T) {
	base := canonicalTempRoot(t)
	realParent := filepath.Join(base, "real-parent")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	symlinkedParent := filepath.Join(base, "symlinked-parent")
	if err := os.Symlink(realParent, symlinkedParent); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(symlinkedParent, "tools")
	policy := fixtureColimaPolicy([]byte("fixture"))
	ports := &fakeColimaPorts{download: []byte("fixture")}
	if err := bootstrapColimaWithPolicy(context.Background(), false, authorizedColimaRequest(root, policy), ports, policy); !errors.Is(err, productinstall.ErrInvalidRequest) {
		t.Fatalf("bootstrap error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(realParent, "tools")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("symlinked parent received tool root: %v", err)
	}
	if ports.downloads != 0 || ports.runs != 0 {
		t.Fatalf("unsafe root side effects = downloads %d runs %d", ports.downloads, ports.runs)
	}
}

func TestColimaBootstrapChecksumMismatchNeverCreatesVM(t *testing.T) {
	parent := canonicalTempRoot(t)
	root := filepath.Join(parent, "tools")
	policy := fixtureColimaPolicy([]byte("expected"))
	request := authorizedColimaRequest(root, policy)
	ports := &fakeColimaPorts{download: []byte("hostile replacement")}
	if err := bootstrapColimaWithPolicy(context.Background(), false, request, ports, policy); err == nil {
		t.Fatal("checksum mismatch passed")
	}
	if ports.runs != 0 {
		t.Fatalf("checksum mismatch started VM %d times", ports.runs)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 0 {
		t.Fatalf("checksum mismatch retained download: %v / %+v", err, entries)
	}
}

func TestColimaBootstrapInstallsPinnedClientAndUsesExactVMGrant(t *testing.T) {
	fixture := []byte("fixture colima binary")
	policy := fixtureColimaPolicy(fixture)
	root := filepath.Join(canonicalTempRoot(t), "tools")
	request := authorizedColimaRequest(root, policy)
	ports := &fakeColimaPorts{download: fixture}
	if err := bootstrapColimaWithPolicy(context.Background(), false, request, ports, policy); err != nil {
		t.Fatal(err)
	}
	wantPath := filepath.Join(root, "colima-"+policy.version)
	wantDockerPath := filepath.Join(root, "docker-"+policy.dockerVersion)
	wantArgs := []string{"start", "--profile", "chora-local", "--cpu", "2", "--memory", "4", "--disk", "20", "--runtime", "docker"}
	if ports.downloads != 2 || ports.runs != 1 || ports.executable != wantPath || !slices.Equal(ports.args, wantArgs) {
		t.Fatalf("bootstrap trace = downloads=%d runs=%d executable=%q args=%v", ports.downloads, ports.runs, ports.executable, ports.args)
	}
	if info, err := os.Lstat(wantPath); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("installed client mode = %v / %v", info, err)
	}
	if info, err := os.Lstat(wantDockerPath); err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("installed Docker client mode = %v / %v", info, err)
	}
	if err := bootstrapColimaWithPolicy(context.Background(), false, request, ports, policy); err != nil {
		t.Fatal(err)
	}
	if ports.downloads != 2 || ports.runs != 2 {
		t.Fatalf("idempotent bootstrap trace = downloads=%d runs=%d", ports.downloads, ports.runs)
	}
}

func canonicalTempRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func fixtureColimaPolicy(fixture []byte) colimaBootstrapPolicy {
	digest := sha256.Sum256(fixture)
	return colimaBootstrapPolicy{
		version: "test", sha256: fmt.Sprintf("%x", digest[:]), dockerVersion: "test", dockerSHA256: fmt.Sprintf("%x", digest[:]),
		platformOS: "darwin", platformArch: "arm64",
	}
}

func authorizedColimaRequest(root string, policy colimaBootstrapPolicy) colimaBootstrapRequest {
	request := colimaBootstrapRequest{Enabled: true, ToolRoot: root, Profile: "chora-local", DownloadAuthorization: policy.downloadGrant(),
		ColimaSource: "/fixture/colima", DockerClientSource: "/fixture/docker"}
	request.VMAuthorization = request.expectedVMAuthorization()
	return request
}

type fakeColimaPorts struct {
	download   []byte
	downloads  int
	runs       int
	executable string
	args       []string
	failOnCall bool
}

func (ports *fakeColimaPorts) Download(_ context.Context, _ string, output io.Writer) error {
	if ports.failOnCall {
		return errors.New("unexpected download")
	}
	ports.downloads++
	_, err := io.Copy(output, bytes.NewReader(ports.download))
	return err
}

func (ports *fakeColimaPorts) Run(_ context.Context, executable string, args []string) error {
	if ports.failOnCall {
		return errors.New("unexpected run")
	}
	ports.runs++
	ports.executable = executable
	ports.args = slices.Clone(args)
	return nil
}
