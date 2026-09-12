// Package isolatedenv prepares the credential-free public Pi Docker runtime.
package isolatedenv

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/piinstall"
)

const (
	recordSchema          = "chora.public-isolated-environment.v1"
	workspaceProbeVersion = "chora.workbench-local-tmpfs-volume.v1"
	nodeImage             = "node@sha256:4a4884e8a44826194dff92ba316264f392056cbe243dcc9fd3551e71cea02b90"
)

var observerDigest = agentpi.ResourceObserverSHA256

type Record struct {
	Source         agentpi.IsolatedSource                   `json:"-"`
	Runner         dockersupervisor.CommandRunner           `json:"-"`
	EngineIdentity dockersupervisor.EngineIdentity          `json:"-"`
	Capability     dockersupervisor.CapabilityProbeContract `json:"-"`
	Qualification  dockersupervisor.EngineQualification     `json:"-"`
	GitVersion     string                                   `json:"-"`
}

type diskRecord struct {
	Schema               string                                         `json:"schema"`
	Source               agentpi.IsolatedSourceParams                   `json:"source"`
	DockerContext        string                                         `json:"dockerContext"`
	DockerEndpointDigest string                                         `json:"dockerEndpointDigest"`
	Engine               dockersupervisor.EngineIdentityRecord          `json:"engine"`
	Capability           dockersupervisor.CapabilityProbeContractRecord `json:"capability"`
	Qualification        dockersupervisor.EngineQualificationRecord     `json:"qualification"`
	ObserverSHA256       string                                         `json:"observerSHA256"`
	GitVersion           string                                         `json:"gitVersion"`
	WorkspaceProbe       string                                         `json:"workspaceProbe"`
}

func Prepare(ctx context.Context, sourceRoot, dataRoot string) (Record, error) {
	if err := validatePlatform(runtime.GOOS, runtime.GOARCH); err != nil {
		return Record{}, err
	}
	if err := validateRoots(sourceRoot, dataRoot); err != nil {
		return Record{}, err
	}
	runner, contextName, endpoint, err := dockerAuthority(ctx, dataRoot)
	if err != nil {
		return Record{}, err
	}
	stage, err := os.MkdirTemp(dataRoot, "isolated-build-")
	if err != nil {
		return Record{}, err
	}
	defer os.RemoveAll(stage)
	helper := filepath.Join(stage, "helper-build")
	moduleCache, err := publicModuleCache(ctx, sourceRoot)
	if err != nil {
		return Record{}, err
	}
	build := exec.CommandContext(ctx, "/usr/bin/env", "GOOS=linux", "GOARCH=arm64", "CGO_ENABLED=0", "go", "build", "-trimpath", "-o", helper, "./cmd/chora")
	build.Dir = sourceRoot
	build.Env = sanitizedBuildEnv(stage, moduleCache)
	var stderr limitedBuffer
	build.Stderr = &stderr
	if err := build.Run(); err != nil {
		return Record{}, fmt.Errorf("cross-compile public helper: %w: %s", err, stderr.String())
	}
	helperHash, err := digestRegular(helper, 512<<20)
	if err != nil {
		return Record{}, err
	}
	contextRoot := filepath.Join(stage, "context")
	if err := os.Mkdir(contextRoot, 0700); err != nil {
		return Record{}, err
	}
	tarball := filepath.Join(stage, "qualified-package.tgz")
	if err := downloadFrozenTarball(ctx, tarball); err != nil {
		return Record{}, err
	}
	if err := writeBuildContext(sourceRoot, contextRoot, helper, tarball); err != nil {
		return Record{}, err
	}
	iid := filepath.Join(stage, "image-id")
	result, err := runner.Run(ctx, dockersupervisor.Command{Args: []string{"build", "--platform=linux/arm64", "--iidfile", iid, "--tag", "chora-public-pi:" + agentpi.IsolatedPiVersion, contextRoot}})
	if err != nil || result.ExitCode != 0 {
		return Record{}, fmt.Errorf("build public isolated image: %w: %s", err, boundedDiagnostic(result.Stderr))
	}
	imageBytes, err := os.ReadFile(iid)
	if err != nil {
		return Record{}, err
	}
	imageID := strings.TrimSpace(string(imageBytes))
	gitResult, err := runner.Run(ctx, dockersupervisor.Command{Args: []string{"run", "--rm", "--pull=never", "--network", "none", "--entrypoint", "/usr/bin/git", imageID, "--version"}})
	if err != nil || gitResult.ExitCode != 0 {
		return Record{}, fmt.Errorf("verify public image Git: %w: %s", err, boundedDiagnostic(gitResult.Stderr))
	}
	gitVersion := strings.TrimSpace(string(gitResult.Stdout))
	if !validGitVersion(gitVersion) {
		return Record{}, errors.New("public image Git version is invalid")
	}
	source, err := agentpi.NewIsolatedSource(agentpi.IsolatedSourceParams{ImageID: imageID, HelperSHA256: helperHash, PolicySHA256: dockersupervisor.WorkbenchPolicyDigest})
	if err != nil {
		return Record{}, err
	}
	identity, err := dockersupervisor.ObserveEngine(ctx, runner)
	if err != nil {
		return Record{}, err
	}
	contract, err := dockersupervisor.NewCapabilityProbeContract(imageID, dockersupervisor.WorkbenchPolicyDigest)
	if err != nil {
		return Record{}, err
	}
	probeRoot := filepath.Join(dataRoot, "capability-probe")
	if err := os.MkdirAll(probeRoot, 0700); err != nil {
		return Record{}, err
	}
	probe, err := dockersupervisor.NewCapabilityProbe(dockersupervisor.CapabilityProbeConfig{Runner: runner, RuntimeRoot: probeRoot, Identity: identity, Contract: contract})
	if err != nil {
		return Record{}, err
	}
	qualification, err := probe.Probe(ctx, dockersupervisor.AuthorizeCapabilityProbe())
	if err != nil || !qualification.ValidFor(identity, contract) {
		return Record{}, fmt.Errorf("qualify public isolated image: %w", err)
	}
	if err := dockersupervisor.VerifyWorkbenchWorkspaceVolume(ctx, runner, imageID, probeRoot); err != nil {
		return Record{}, fmt.Errorf("qualify public workspace volume: %w", err)
	}
	disk := diskRecord{recordSchema, source.Record(), contextName, endpoint, identity.Record(), contract.Record(), qualification.Record(), observerDigest(), gitVersion, workspaceProbeVersion}
	if err := writeMetadata(dataRoot, disk); err != nil {
		return Record{}, err
	}
	return Record{source, runner, identity, contract, qualification, gitVersion}, nil
}

func Load(ctx context.Context, dataRoot string) (Record, error) {
	if err := validatePlatform(runtime.GOOS, runtime.GOARCH); err != nil {
		return Record{}, err
	}
	var d diskRecord
	if err := readMetadata(dataRoot, &d); err != nil || d.Schema != recordSchema {
		return Record{}, errors.New("public isolated environment metadata is unavailable or invalid")
	}
	if d.WorkspaceProbe != workspaceProbeVersion || d.ObserverSHA256 == "" || d.ObserverSHA256 != agentpi.ResourceObserverSHA256() || !validGitVersion(d.GitVersion) {
		return Record{}, errors.New("public isolated observer or Git identity drifted")
	}
	runner, err := dockersupervisor.NewDockerCommandRunnerWithConfigRoot(dockersupervisor.DockerCommandAuthority{ContextName: d.DockerContext, ContextEndpointDigest: d.DockerEndpointDigest}, filepath.Join(dataRoot, "docker-authority"))
	if err != nil {
		return Record{}, err
	}
	source, err := agentpi.NewIsolatedSource(d.Source)
	if err != nil {
		return Record{}, err
	}
	identity, err := dockersupervisor.RestoreEngineIdentity(d.Engine)
	if err != nil {
		return Record{}, err
	}
	contract, err := dockersupervisor.RestoreCapabilityProbeContract(d.Capability)
	if err != nil {
		return Record{}, err
	}
	qualification, err := dockersupervisor.RestoreEngineQualification(d.Qualification)
	if err != nil {
		return Record{}, err
	}
	observed, err := dockersupervisor.ObserveEngine(ctx, runner)
	if err != nil || observed.Digest() != identity.Digest() || !qualification.ValidFor(observed, contract) || contract.ProbeImageID() != source.ImageID() || contract.SandboxPolicyDigest() != dockersupervisor.WorkbenchPolicyDigest {
		return Record{}, errors.New("public isolated environment identity or qualification drifted")
	}
	result, err := runner.Run(ctx, dockersupervisor.Command{Args: []string{"image", "inspect", "--format", "{{.Id}}", source.ImageID()}})
	if err != nil || result.ExitCode != 0 || strings.TrimSpace(string(result.Stdout)) != source.ImageID() {
		return Record{}, errors.New("public isolated image is unavailable or drifted")
	}
	return Record{source, runner, observed, contract, qualification, d.GitVersion}, nil
}

func writeBuildContext(source, stage, helper, tarball string) error {
	observer := filepath.Join(source, "internal", "agent", "pi", "resource_check_observer.mjs")
	b, err := os.ReadFile(observer)
	if err != nil || digestBytes(b) != observerDigest() {
		return errors.New("embedded observer source is unavailable or drifted")
	}
	contract := piinstall.FrozenContract()
	files := map[string][]byte{"package.json": contract.ConsumerManifest, "package-lock.json": contract.ConsumerLock, "resource_check_observer.mjs": b}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(stage, name), data, 0600); err != nil {
			return err
		}
	}
	h, err := os.ReadFile(helper)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "chora"), h, 0700); err != nil {
		return err
	}
	packageBytes, err := os.ReadFile(tarball)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(stage, "qualified-package.tgz"), packageBytes, 0600); err != nil {
		return err
	}
	dockerfile := `FROM ` + nodeImage + `
RUN sed -i -e 's|http://deb.debian.org/debian-security|http://snapshot.debian.org/archive/debian-security/20260901T000000Z|' -e 's|http://deb.debian.org/debian$|http://snapshot.debian.org/archive/debian/20260901T000000Z|' /etc/apt/sources.list.d/debian.sources && apt-get -o Acquire::Check-Valid-Until=false -o APT::Update::Error-Mode=any update && apt-get install --yes --no-install-recommends git=1:2.39.5-0+deb12u3 && rm -rf /var/lib/apt/lists/* && git --version
WORKDIR /opt/pi
COPY qualified-package.tgz /opt/qualified-package.tgz
COPY package.json package-lock.json ./
RUN npm ci --ignore-scripts --omit=dev --no-audit --no-fund --registry=https://registry.npmjs.org && npm cache clean --force
COPY chora /usr/local/bin/chora
COPY resource_check_observer.mjs ` + agentpi.IsolatedObserverPath + `
RUN chmod 0555 /usr/local/bin/chora && chmod 0444 ` + agentpi.IsolatedObserverPath + ` && git --version && node --version && /usr/local/bin/chora version >/dev/null && test -f /opt/pi/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js && node /opt/pi/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js --version && ln -s /opt/pi/node_modules/@earendil-works/pi-coding-agent/dist/bundle/cli.js /usr/local/bin/chora-pi-rpc
USER 1000:1000
ENTRYPOINT ["chora-pi-rpc"]
`
	return os.WriteFile(filepath.Join(stage, "Dockerfile"), []byte(dockerfile), 0600)
}

func validatePlatform(goos, arch string) error {
	if goos != "darwin" || arch != "arm64" {
		return errors.New("public isolated environment currently requires Apple Silicon macOS")
	}
	return nil
}
func validateRoots(source, data string) error {
	for _, p := range []string{source, data} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return errors.New("roots must be clean absolute paths")
		}
		i, e := os.Lstat(p)
		if e != nil || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
			return errors.New("roots must be real directories")
		}
	}
	return os.Chmod(data, 0700)
}
func dockerAuthority(ctx context.Context, dataRoot string) (dockersupervisor.CommandRunner, string, string, error) {
	name, err := dockerCLI(ctx, "context", "show")
	if err != nil {
		return nil, "", "", err
	}
	endpointJSON, err := dockerCLI(ctx, "context", "inspect", "--format", "{{json .Endpoints.docker.Host}}", name)
	if err != nil {
		return nil, "", "", err
	}
	var endpoint string
	if json.Unmarshal([]byte(endpointJSON), &endpoint) != nil {
		return nil, "", "", errors.New("invalid Docker context endpoint")
	}
	digest, err := dockersupervisor.DigestContextEndpoint(endpoint)
	if err != nil {
		return nil, "", "", err
	}
	if !strings.HasPrefix(endpoint, "unix://") {
		return nil, "", "", errors.New("public isolated environment requires a local unix Docker endpoint")
	}
	name = "chora-isolated"
	configRoot := filepath.Join(dataRoot, "docker-authority")
	metaRoot := filepath.Join(configRoot, "contexts", "meta", digestBytes([]byte(name)))
	if err := os.MkdirAll(metaRoot, 0700); err != nil {
		return nil, "", "", err
	}
	metadata := map[string]any{"Name": name, "Metadata": map[string]any{}, "Endpoints": map[string]any{"docker": map[string]any{"Host": endpoint, "SkipTLSVerify": false}}}
	b, _ := json.Marshal(metadata)
	if err := os.WriteFile(filepath.Join(metaRoot, "meta.json"), b, 0600); err != nil {
		return nil, "", "", err
	}
	runner, err := dockersupervisor.NewDockerCommandRunnerWithConfigRoot(dockersupervisor.DockerCommandAuthority{ContextName: name, ContextEndpointDigest: digest}, configRoot)
	return runner, name, digest, err
}

func downloadFrozenTarball(ctx context.Context, path string) error {
	c := piinstall.FrozenContract()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.TarballURL, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("download frozen Pi tarball: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download frozen Pi tarball: HTTP %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, c.MaxTarballBytes+1))
	if err != nil || int64(len(body)) > c.MaxTarballBytes {
		return errors.New("frozen Pi tarball is unavailable or oversized")
	}
	expected, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(c.TarballSRI, "sha512-"))
	if err != nil {
		return err
	}
	actual := sha512.Sum512(body)
	if !bytes.Equal(actual[:], expected) {
		return errors.New("frozen Pi tarball failed strict SRI verification")
	}
	return os.WriteFile(path, body, 0600)
}
func dockerCLI(ctx context.Context, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "docker", args...)
	c.Env = []string{"DOCKER_CONFIG=" + dockerConfigRoot()}
	var out, stderr limitedBuffer
	c.Stdout = &out
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		return "", fmt.Errorf("resolve Docker context: %w: %s", err, stderr.String())
	}
	return strings.TrimSpace(out.String()), nil
}
func dockerConfigRoot() string {
	if p := strings.TrimSpace(os.Getenv("DOCKER_CONFIG")); p != "" {
		return p
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".docker")
}
func sanitizedBuildEnv(stage, moduleCache string) []string {
	allowed := map[string]bool{"PATH": true, "TMPDIR": true, "GOPROXY": true, "GOROOT": true, "GOSUMDB": true}
	out := []string{"HOME=/var/empty", "GOENV=off", "GOPRIVATE=", "GONOSUMDB=", "GOCACHE=" + filepath.Join(stage, "go-cache"), "GOPATH=" + filepath.Join(stage, "go-path"), "GOMODCACHE=" + moduleCache}
	for _, e := range os.Environ() {
		k := strings.SplitN(e, "=", 2)[0]
		if allowed[k] {
			out = append(out, e)
		}
	}
	return out
}

func publicModuleCache(ctx context.Context, root string) (string, error) {
	command := exec.CommandContext(ctx, "go", "env", "GOMODCACHE")
	command.Dir = root
	var out, stderr limitedBuffer
	command.Stdout = &out
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("resolve public Go module cache: %w: %s", err, stderr.String())
	}
	path := strings.TrimSpace(out.String())
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("public Go module cache path is invalid")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("public Go module cache is unavailable or unsafe")
	}
	return path, nil
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	const limit = 1 << 20
	if b.Len() >= limit {
		return len(p), nil
	}
	if len(p) > limit-b.Len() {
		_, _ = b.Buffer.Write(p[:limit-b.Len()])
		return len(p), nil
	}
	return b.Buffer.Write(p)
}
func boundedDiagnostic(b []byte) string {
	if len(b) > 4096 {
		b = b[:4096]
	}
	return string(bytes.ToValidUTF8(b, []byte("?")))
}
func digestBytes(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func validGitVersion(value string) bool {
	value = strings.TrimPrefix(value, "git version ")
	if value == "" || len(value) > 96 {
		return false
	}
	for _, r := range value {
		if !(r >= '0' && r <= '9' || r == '.' || r == '-' || r == '+' || r == '~') {
			return false
		}
	}
	return true
}
func digestRegular(path string, limit int64) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	i, e := f.Stat()
	if e != nil || !i.Mode().IsRegular() || i.Size() > limit {
		return "", errors.New("artifact is unsafe or oversized")
	}
	h := sha256.New()
	_, e = io.Copy(h, io.LimitReader(f, limit+1))
	return hex.EncodeToString(h.Sum(nil)), e
}
func writeMetadata(root string, d diskRecord) error {
	b, e := json.Marshal(d)
	if e != nil {
		return e
	}
	tmp, e := os.CreateTemp(root, "isolated-env-")
	if e != nil {
		return e
	}
	name := tmp.Name()
	defer os.Remove(name)
	if e = tmp.Chmod(0600); e == nil {
		_, e = tmp.Write(b)
	}
	if e == nil {
		e = tmp.Sync()
	}
	e = errors.Join(e, tmp.Close())
	if e != nil {
		return e
	}
	return os.Rename(name, filepath.Join(root, "isolated-environment.json"))
}
func readMetadata(root string, d *diskRecord) error {
	path := filepath.Join(root, "isolated-environment.json")
	i, e := os.Lstat(path)
	if e != nil || !i.Mode().IsRegular() || i.Mode().Perm() != 0600 {
		return errors.New("unsafe metadata")
	}
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
	decoder.DisallowUnknownFields()
	return decoder.Decode(d)
}
