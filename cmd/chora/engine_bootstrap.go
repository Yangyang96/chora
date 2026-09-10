package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"

	"github.com/Yangyang96/chora/internal/productinstall"
)

const (
	colimaVersion        = "0.10.3"
	colimaSHA256         = "55278419bd4288e4ab11eb30be13f5e655f3886c0979b68e435df910dbc9bbdf"
	dockerClientVersion  = "29.6.1"
	dockerClientSHA256   = "e8a1e5351c4d12337a4ee2b54523bc0107b4d13f795c9d6e791b9e4cf835f385"
	maxColimaBinaryBytes = 128 << 20
)

type colimaBootstrapRequest struct {
	Enabled               bool
	ToolRoot              string
	Profile               string
	DownloadAuthorization string
	VMAuthorization       string
	ColimaSource          string
	DockerClientSource    string
}

func (request colimaBootstrapRequest) expectedVMAuthorization() string {
	return "create-colima-vm-" + request.Profile + "-cpu2-memory4-disk20"
}

type colimaBootstrapPorts interface {
	Download(context.Context, string, io.Writer) error
	Run(context.Context, string, []string) error
}

type colimaBootstrapPolicy struct {
	version       string
	sha256        string
	dockerVersion string
	dockerSHA256  string
	platformOS    string
	platformArch  string
}

func productionColimaPolicy() colimaBootstrapPolicy {
	return colimaBootstrapPolicy{
		version: colimaVersion, sha256: colimaSHA256,
		dockerVersion: dockerClientVersion, dockerSHA256: dockerClientSHA256,
		platformOS:   runtime.GOOS,
		platformArch: runtime.GOARCH,
	}
}

func (policy colimaBootstrapPolicy) downloadGrant() string {
	return "install-engine-tools-colima-" + policy.version + "-" + policy.sha256 + "-docker-" + policy.dockerVersion + "-" + policy.dockerSHA256
}

// bootstrapColima performs no work when an explicit compatible Engine was
// already observed. Otherwise both the immutable client installation and the
// exact VM allocation require separate, visible authorizations.
func bootstrapColima(ctx context.Context, compatible bool, request colimaBootstrapRequest, ports colimaBootstrapPorts) error {
	return bootstrapColimaWithPolicy(ctx, compatible, request, ports, productionColimaPolicy())
}

func bootstrapColimaWithPolicy(ctx context.Context, compatible bool, request colimaBootstrapRequest, ports colimaBootstrapPorts, policy colimaBootstrapPolicy) error {
	if compatible {
		return nil
	}
	if !request.Enabled || ports == nil || !canonicalAbsolute(request.ToolRoot) || !productIdentifier.MatchString(request.Profile) ||
		!canonicalAbsolute(request.ColimaSource) || !canonicalAbsolute(request.DockerClientSource) ||
		request.DownloadAuthorization != policy.downloadGrant() || request.VMAuthorization != request.expectedVMAuthorization() {
		return productinstall.ErrAuthorityRequired
	}
	if policy.version == "" || !validFingerprint(policy.sha256) || policy.dockerVersion == "" || !validFingerprint(policy.dockerSHA256) || policy.platformOS != "darwin" || policy.platformArch != "arm64" {
		return productinstall.ErrInvalidRequest
	}
	if err := ensureOwnerPrivateDirectory(request.ToolRoot); err != nil {
		return err
	}
	dockerPath := filepath.Join(request.ToolRoot, "docker-"+policy.dockerVersion)
	dockerPresent, err := exactRegularFileSHA256(dockerPath, policy.dockerSHA256)
	if err != nil {
		return err
	}
	if !dockerPresent {
		if err := installPinnedTool(ctx, request.ToolRoot, dockerPath, request.DockerClientSource, policy.dockerSHA256, ports, ".docker-client-"); err != nil {
			return errors.New("install pinned Docker client failed")
		}
	}
	binaryPath := filepath.Join(request.ToolRoot, "colima-"+policy.version)
	present, err := exactRegularFileSHA256(binaryPath, policy.sha256)
	if err != nil {
		return err
	}
	if !present {
		if err := installPinnedTool(ctx, request.ToolRoot, binaryPath, request.ColimaSource, policy.sha256, ports, ".colima-client-"); err != nil {
			return err
		}
	}
	if err := validateOwnerPrivateDirectory(request.ToolRoot); err != nil {
		return err
	}
	present, err = exactRegularFileSHA256(binaryPath, policy.sha256)
	if err != nil || !present {
		return productinstall.ErrInvalidRequest
	}
	args := []string{"start", "--profile", request.Profile, "--cpu", "2", "--memory", "4", "--disk", "20", "--runtime", "docker"}
	if err := ports.Run(ctx, binaryPath, args); err != nil {
		return errors.New("start explicitly authorized Colima Engine failed")
	}
	return nil
}

func installPinnedTool(ctx context.Context, root, target, source, expected string, ports colimaBootstrapPorts, prefix string) (returnErr error) {
	if err := validateOwnerPrivateDirectory(root); err != nil {
		return err
	}
	file, err := os.CreateTemp(root, prefix+"*")
	if err != nil {
		return errors.New("create private Colima download failed")
	}
	temporary := file.Name()
	defer func() {
		closeErr := file.Close()
		removeErr := os.Remove(temporary)
		if returnErr == nil && closeErr != nil {
			returnErr = errors.New("close private Colima download failed")
		}
		if returnErr == nil && removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			returnErr = errors.New("clean private Colima download failed")
		}
	}()
	if err := file.Chmod(0o600); err != nil {
		return errors.New("secure private Colima download failed")
	}
	limited := &boundedWriter{writer: file, remaining: maxColimaBinaryBytes}
	if err := ports.Download(ctx, source, limited); err != nil || limited.exceeded {
		return errors.New("download pinned Colima client failed")
	}
	if err := file.Sync(); err != nil {
		return errors.New("persist pinned Colima client failed")
	}
	if err := file.Chmod(0o700); err != nil {
		return errors.New("secure pinned Colima client failed")
	}
	present, err := exactRegularFileSHA256(temporary, expected)
	if err != nil || !present {
		return errors.New("pinned Colima checksum mismatch")
	}
	if _, err := os.Lstat(target); err == nil || !errors.Is(err, os.ErrNotExist) {
		return errors.New("private Colima client already exists")
	}
	if err := os.Rename(temporary, target); err != nil {
		return errors.New("activate pinned Colima client failed")
	}
	return syncBootstrapDirectory(root)
}

type boundedWriter struct {
	writer    io.Writer
	remaining int64
	exceeded  bool
}

func (writer *boundedWriter) Write(data []byte) (int, error) {
	if int64(len(data)) > writer.remaining {
		writer.exceeded = true
		return 0, errors.New("download exceeds limit")
	}
	n, err := writer.writer.Write(data)
	writer.remaining -= int64(n)
	return n, err
}

func ensureOwnerPrivateDirectory(path string) error {
	if _, err := os.Lstat(path); err == nil {
		return validateOwnerPrivateDirectory(path)
	} else if !errors.Is(err, os.ErrNotExist) {
		return productinstall.ErrInvalidRequest
	}
	if err := validateExistingDirectoryChain(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		return errors.New("create private Colima tool root failed")
	}
	if err := validateOwnerPrivateDirectory(path); err != nil {
		return err
	}
	if err := syncBootstrapDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	return validateOwnerPrivateDirectory(path)
}

func validateExistingDirectoryChain(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return productinstall.ErrInvalidRequest
	}
	components := []string{}
	for current := path; ; current = filepath.Dir(current) {
		components = append(components, current)
		next := filepath.Dir(current)
		if next == current {
			break
		}
	}
	for index := len(components) - 1; index >= 0; index-- {
		info, err := os.Lstat(components[index])
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return productinstall.ErrInvalidRequest
		}
	}
	return nil
}

func validateOwnerPrivateDirectory(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return productinstall.ErrInvalidRequest
	}
	metadata, ok := info.Sys().(*syscall.Stat_t)
	if !ok || metadata.Uid != uint32(os.Geteuid()) {
		return productinstall.ErrInvalidRequest
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return productinstall.ErrInvalidRequest
	}
	return nil
}

func exactRegularFileSHA256(path, expected string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 || info.Size() > maxColimaBinaryBytes {
		return false, productinstall.ErrInvalidRequest
	}
	metadata, ok := info.Sys().(*syscall.Stat_t)
	if !ok || metadata.Uid != uint32(os.Geteuid()) {
		return false, productinstall.ErrInvalidRequest
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return false, productinstall.ErrInvalidRequest
	}
	file, err := os.Open(path)
	if err != nil {
		return false, errors.New("inspect private Colima client failed")
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, io.LimitReader(file, maxColimaBinaryBytes+1)); err != nil {
		return false, errors.New("inspect private Colima client failed")
	}
	return fmt.Sprintf("%x", digest.Sum(nil)) == expected, nil
}

func syncBootstrapDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return errors.New("open private tool directory failed")
	}
	defer directory.Close()
	if err := directory.Sync(); err != nil {
		return errors.New("sync private tool directory failed")
	}
	return nil
}

type productionColimaPorts struct{}

func (productionColimaPorts) Download(ctx context.Context, source string, output io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("authenticated private tool source unavailable")
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil || resolved != source {
		return errors.New("authenticated private tool source unavailable")
	}
	file, err := os.Open(source)
	if err != nil {
		return errors.New("authenticated private tool source unavailable")
	}
	defer file.Close()
	_, err = io.Copy(output, file)
	return err
}

func (productionColimaPorts) Run(ctx context.Context, executable string, args []string) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Env = []string{"HOME=" + os.Getenv("HOME"), "PATH=/usr/bin:/bin"}
	if err := command.Run(); err != nil {
		return errors.New("Colima execution failed")
	}
	return nil
}
