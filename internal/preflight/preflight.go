package preflight

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/pidistribution"
	"github.com/Yangyang96/chora/internal/sourcebundle"
)

const (
	StatusPassed                = "passed"
	StatusFailed                = "failed"
	AgentImageID                = "sha256:91698efead5641a633519f5f229373e08a59264045ca27f6d01fc06505deeea7"
	BoundaryImageID             = "sha256:4f7746f3cdbe55dc454775ead5958a9ed8a78b93776ea1df598255c1606b25c6"
	AllowedModelURL             = "https://chatgpt.com/backend-api/codex/responses"
	AllowedAuthURL              = "https://auth.openai.com"
	FixedProxyURL               = "http://host.docker.internal:9981"
	HostProxyProbeURL           = "http://127.0.0.1:9981"
	RequiredCA256               = "3444d3f6d9ef36946ff29ef382e402017a96ff6c97f7b8ae08643acceb2e0dac"
	ModeFreshInstall       Mode = "fresh-install"
	ModeInstalledDoctor    Mode = "installed-doctor"
	ModeServiceRecovery    Mode = "service-recovery"
	ModeServiceStart       Mode = "service-start"
	ModePreAttempt         Mode = "pre-attempt"
	modelProbeMaxAttempts       = 3
	preflightDeadlineLimit      = 59 * time.Second
)

var errUndeclaredRedirectDestination = errors.New("undeclared redirect destination")

type Mode string

type Config struct {
	Deadline              time.Duration
	SourceRoot            string
	InstallRoot           string
	DataRoot              string
	AuthFile              string
	CAFile                string
	ProxyURL              string
	ModelURL              string
	Port                  int
	Mode                  Mode
	PriorInputFingerprint string
	ChoraOwnsPort         bool
	SourceManifest        string
	BundleAggregate       string
	SetupReceiptSHA256    string
	ModelAuthoritySHA256  string
}

type CommandResult struct {
	Stdout             string
	Stderr             string
	Err                error
	RetryableTransport bool
}

type Probes struct {
	Host          func() (goos, goarch string)
	Command       func(context.Context, string, ...string) CommandResult
	File          func(string) FileMetadata
	ReadFile      func(string) ([]byte, error)
	Digest        func(string) (string, error)
	Proxy         func(context.Context, ProxyRequest) CommandResult
	Model         func(context.Context, ModelRequest) CommandResult
	DiskFree      func(string) (uint64, error)
	PortAvailable func(int) bool
	Bundle        func(context.Context, BundleRequest) BundleResult
	DataIdentity  func(Config) error
	Residue       func(context.Context, Config) ResidueResult
	Now           func() time.Time
	// QualifiedRuntime replaces only the legacy provider-specific Engine and
	// PATH Pi probes. It is accepted solely when the same exact Runner
	// re-observes Identity and Qualification remains valid for Contract.
	QualifiedRuntime *QualifiedRuntime
}

// QualifiedRuntime is immutable evidence reconstructed from one active
// installed generation. Runner remains explicit so preflight can re-observe
// the qualified Engine and inspect exact pinned images/residue without using
// Docker's current/default context. PiSelection is revalidated directly and
// never rediscovered through PATH.
type QualifiedRuntime struct {
	Runner          dockersupervisor.CommandRunner
	Identity        dockersupervisor.EngineIdentity
	Contract        dockersupervisor.CapabilityProbeContract
	Qualification   dockersupervisor.EngineQualification
	PiSelection     pidistribution.Selection
	ManagedImageID  string
	VerifierImageID string
	BoundaryImageID string
	GenerationID    string
	ReleaseID       string
	ManifestSHA256  string
}

type FileMetadata struct {
	Exists    bool
	Directory bool
	Owner     bool
	Mode      uint32
	Symlink   bool
	Identity  string
}

type ModelRequest struct {
	URL, ContractProxyURL, DialProxyURL, CAFile, AccessToken, AccountID string
}

type ProxyRequest struct {
	URL, ContractProxyURL, DialProxyURL, CAFile string
}

type BundleRequest struct {
	Mode              Mode
	SourceRoot        string
	InstallRoot       string
	SourceManifest    string
	ExpectedAggregate string
}

type BundleResult struct {
	Aggregate string
	Valid     bool
	Err       error
}

type ResidueResult struct {
	AgentWorkspaces    int
	VerifierWorkspaces int
	Err                error
}

type Failure struct {
	Boundary string `json:"boundary"`
	Observed string `json:"observed"`
	Required string `json:"required"`
	Action   string `json:"action"`
}

type Report struct {
	Status           string           `json:"status"`
	Failure          *Failure         `json:"failure,omitempty"`
	ResourcesCreated bool             `json:"resourcesCreated"`
	Elapsed          time.Duration    `json:"elapsed"`
	InputFingerprint string           `json:"inputFingerprint"`
	ModelProbe       *ModelProbeAudit `json:"modelProbe,omitempty"`
	InputEvidence    *InputEvidence   `json:"inputEvidence,omitempty"`
}

// InputEvidence contains only content identities captured by the same Doctor
// pass. It never contains credential, CA, proxy, or model URL contents.
type InputEvidence struct {
	AuthFileSHA256 string `json:"authFileSha256"`
	CAFileSHA256   string `json:"caFileSha256"`
	ProxyURLSHA256 string `json:"proxyUrlSha256"`
	ModelURLSHA256 string `json:"modelUrlSha256"`
}

type ModelProbeAudit struct {
	MaxAttempts int                 `json:"maxAttempts"`
	Attempts    []ModelProbeAttempt `json:"attempts"`
}

type ModelProbeAttempt struct {
	Attempt    int    `json:"attempt"`
	Outcome    string `json:"outcome"`
	StatusCode int    `json:"statusCode,omitempty"`
	Retryable  bool   `json:"retryable"`
	Retried    bool   `json:"retried"`
}

func DefaultProbes() Probes {
	return Probes{
		Host: func() (string, string) { return runtime.GOOS, runtime.GOARCH },
		Command: func(ctx context.Context, name string, arguments ...string) CommandResult {
			command := exec.CommandContext(ctx, name, arguments...)
			var stdout, stderr boundedBuffer
			command.Stdout, command.Stderr = &stdout, &stderr
			err := command.Run()
			return CommandResult{Stdout: stdout.String(), Stderr: stderr.String(), Err: err}
		},
		File: func(path string) FileMetadata {
			info, err := os.Lstat(path)
			if err != nil {
				return FileMetadata{Symlink: pathHasSymlinkAncestor(path)}
			}
			owner := false
			identity := ""
			if stat, ok := info.Sys().(*syscall.Stat_t); ok {
				owner = int(stat.Uid) == os.Geteuid()
				identity = fmt.Sprintf("%d:%d:%d:%d", stat.Dev, stat.Ino, info.Size(), info.ModTime().UnixNano())
			}
			return FileMetadata{Exists: true, Directory: info.IsDir(), Owner: owner, Mode: uint32(info.Mode().Perm()), Symlink: pathHasSymlinkAncestor(path), Identity: identity}
		},
		ReadFile: readBoundedFile,
		Digest: func(path string) (string, error) {
			file, err := os.Open(path)
			if err != nil {
				return "", err
			}
			defer file.Close()
			hash := sha256.New()
			written, err := io.Copy(hash, io.LimitReader(file, (16<<20)+1))
			if err != nil {
				return "", err
			}
			if written > 16<<20 {
				return "", fmt.Errorf("file exceeds 16 MiB")
			}
			return fmt.Sprintf("%x", hash.Sum(nil)), nil
		},
		Proxy:    probeProxy,
		Model:    probeModel,
		DiskFree: diskFree,
		PortAvailable: func(port int) bool {
			listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
			if err != nil {
				return false
			}
			return listener.Close() == nil
		},
		Bundle: defaultBundleProbe,
		DataIdentity: func(config Config) error {
			return sourcebundle.VerifyData(config.InstallRoot, config.DataRoot, config.BundleAggregate)
		},
		Residue: defaultResidueProbe,
		Now:     time.Now,
	}
}

func pathHasSymlinkAncestor(path string) bool {
	probe := filepath.Clean(path)
	for {
		info, err := os.Lstat(probe)
		if err == nil && info.Mode()&os.ModeSymlink != 0 {
			return true
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return false
		}
		probe = parent
	}
}

type boundedBuffer struct{ bytes.Buffer }

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	const limit = 64 << 10
	written := len(value)
	if buffer.Len() < limit {
		remaining := limit - buffer.Len()
		if len(value) > remaining {
			value = value[:remaining]
		}
		_, _ = buffer.Buffer.Write(value)
	}
	return written, nil
}

func readBoundedFile(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("file exceeds 1 MiB")
	}
	return data, nil
}

func probeModel(ctx context.Context, request ModelRequest) CommandResult {
	if request.URL != AllowedModelURL || request.ContractProxyURL != FixedProxyURL || request.DialProxyURL != HostProxyProbeURL || request.AccessToken == "" {
		return CommandResult{Err: fmt.Errorf("model probe identity mismatch")}
	}
	if request.AccountID == "" {
		return CommandResult{Err: fmt.Errorf("model account identity missing")}
	}
	return probeHTTP(ctx, http.MethodPost, request.URL, request.DialProxyURL, request.CAFile, request.AccessToken, request.AccountID)
}

func probeProxy(ctx context.Context, request ProxyRequest) CommandResult {
	if request.URL != AllowedAuthURL || request.ContractProxyURL != FixedProxyURL || request.DialProxyURL != HostProxyProbeURL {
		return CommandResult{Err: fmt.Errorf("proxy probe identity mismatch")}
	}
	return probeHTTP(ctx, http.MethodGet, request.URL, request.DialProxyURL, request.CAFile, "", "")
}

func probeHTTP(ctx context.Context, method, target, proxyURL, caFile, accessToken, accountID string) CommandResult {
	ca, err := readBoundedFile(caFile)
	if err != nil {
		return CommandResult{Err: err}
	}
	roots, err := x509.SystemCertPool()
	if err != nil || roots == nil {
		roots = x509.NewCertPool()
	}
	if !roots.AppendCertsFromPEM(ca) {
		return CommandResult{Err: fmt.Errorf("invalid pinned CA")}
	}
	proxy, err := url.Parse(proxyURL)
	if err != nil {
		return CommandResult{Err: err}
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxy), TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}}
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport: transport,
		Timeout:   10 * time.Second,
		CheckRedirect: func(next *http.Request, via []*http.Request) error {
			if len(via) >= 3 || next.URL.Scheme != "https" || (next.URL.Hostname() != "auth.openai.com" && next.URL.Hostname() != "chatgpt.com") || (next.URL.Port() != "" && next.URL.Port() != "443") {
				return errUndeclaredRedirectDestination
			}
			return nil
		},
	}
	var body io.Reader
	if method == http.MethodPost {
		body = bytes.NewBufferString("{}")
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return CommandResult{Err: err}
	}
	if accessToken != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+accessToken)
		httpRequest.Header.Set("ChatGPT-Account-ID", accountID)
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	response, err := client.Do(httpRequest)
	if err != nil {
		return CommandResult{Err: err, RetryableTransport: retryableHTTPTransportError(ctx, err)}
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
	return CommandResult{Stdout: strconv.Itoa(response.StatusCode)}
}

func diskFree(path string) (uint64, error) {
	probe := filepath.Clean(path)
	for {
		var stat syscall.Statfs_t
		if err := syscall.Statfs(probe, &stat); err == nil {
			return uint64(stat.Bavail) * uint64(stat.Bsize), nil
		}
		parent := filepath.Dir(probe)
		if parent == probe {
			return 0, fmt.Errorf("no existing parent for %s", path)
		}
		probe = parent
	}
}

func defaultBundleProbe(ctx context.Context, request BundleRequest) BundleResult {
	result := make(chan BundleResult, 1)
	go func() {
		result <- verifyBundle(request)
	}()
	select {
	case <-ctx.Done():
		return BundleResult{Err: ctx.Err()}
	case verified := <-result:
		return verified
	}
}

func defaultResidueProbe(ctx context.Context, config Config) ResidueResult {
	if err := ctx.Err(); err != nil {
		return ResidueResult{Err: err}
	}
	agent, err := ownedWorkspaceCount(filepath.Join(config.DataRoot, "runtime", "pi"))
	if err != nil {
		return ResidueResult{Err: err}
	}
	verifier, err := ownedWorkspaceCount(filepath.Join(config.DataRoot, "runtime", "verification", "workspaces"))
	if err != nil {
		return ResidueResult{Err: err}
	}
	return ResidueResult{AgentWorkspaces: agent, VerifierWorkspaces: verifier}
}

func ownedWorkspaceCount(root string) (int, error) {
	info, err := os.Lstat(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != 0o700 {
		return 0, errors.New("owned workspace root identity is unsafe")
	}
	if stat, ok := info.Sys().(*syscall.Stat_t); !ok || int(stat.Uid) != os.Geteuid() {
		return 0, errors.New("owned workspace root owner mismatch")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return 0, err
	}
	return len(entries), nil
}

func verifyBundle(request BundleRequest) BundleResult {
	rootTopologyValid := false
	switch request.Mode {
	case ModeFreshInstall:
		rootTopologyValid = allAbsoluteDistinct(request.SourceRoot, request.InstallRoot)
	case ModeInstalledDoctor, ModeServiceRecovery, ModeServiceStart, ModePreAttempt:
		rootTopologyValid = request.SourceRoot == request.InstallRoot &&
			filepath.IsAbs(request.InstallRoot) && filepath.Clean(request.InstallRoot) == request.InstallRoot
	}
	if !rootTopologyValid || request.SourceManifest != filepath.Join(request.SourceRoot, sourcebundle.ManifestName) {
		return BundleResult{Err: fmt.Errorf("source manifest path mismatch")}
	}
	manifest, err := sourcebundle.Verify(request.SourceRoot)
	if err != nil || manifest.AggregateSHA256 != request.ExpectedAggregate {
		return BundleResult{Aggregate: manifest.AggregateSHA256, Err: errorsOr(err, "source bundle aggregate mismatch")}
	}
	if info, statErr := os.Lstat(request.SourceManifest); statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o444 {
		return BundleResult{Aggregate: manifest.AggregateSHA256, Err: fmt.Errorf("source manifest mode mismatch")}
	}
	if request.Mode == ModeFreshInstall {
		return BundleResult{Aggregate: manifest.AggregateSHA256, Valid: true}
	}
	installed, err := sourcebundle.Verify(request.InstallRoot)
	if err != nil || installed.AggregateSHA256 != request.ExpectedAggregate || len(installed.Files) != len(manifest.Files) {
		return BundleResult{Aggregate: installed.AggregateSHA256, Err: errorsOr(err, "installed source aggregate mismatch")}
	}
	markerPath := filepath.Join(request.InstallRoot, sourcebundle.InstallName)
	markerBytes, err := readBoundedFile(markerPath)
	if err != nil {
		return BundleResult{Aggregate: installed.AggregateSHA256, Err: fmt.Errorf("installation marker unavailable")}
	}
	var installation sourcebundle.Installation
	decoder := json.NewDecoder(bytes.NewReader(markerBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&installation); err != nil || !jsonDecoderAtEOF(decoder) || installation.SchemaVersion != sourcebundle.InstallSchema ||
		installation.AggregateSHA256 != request.ExpectedAggregate || installation.Files != len(installed.Files) {
		return BundleResult{Aggregate: installed.AggregateSHA256, Err: fmt.Errorf("installation marker identity mismatch")}
	}
	if info, statErr := os.Lstat(markerPath); statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o444 {
		return BundleResult{Aggregate: installed.AggregateSHA256, Err: fmt.Errorf("installation marker mode mismatch")}
	}
	return BundleResult{Aggregate: installed.AggregateSHA256, Valid: true}
}

func errorsOr(err error, fallback string) error {
	if err != nil {
		return err
	}
	return errors.New(fallback)
}

func Run(ctx context.Context, config Config, probes Probes) Report {
	if config.Mode == "" {
		config.Mode = ModeFreshInstall
	}
	report := run(ctx, config, probes)
	if report.Status == StatusPassed && config.Mode != ModeFreshInstall && config.Mode != ModeInstalledDoctor && config.PriorInputFingerprint != report.InputFingerprint {
		report.Status = StatusFailed
		report.Failure = &Failure{
			Boundary: "input.drift", Observed: report.InputFingerprint,
			Required: config.PriorInputFingerprint,
			Action:   "Rerun chora doctor and restart from the newly issued input fingerprint.",
		}
	}
	return report
}

func Revalidate(ctx context.Context, config Config, probes Probes, mode Mode, priorInputFingerprint string, choraOwnsPort bool) Report {
	config.Mode = mode
	config.PriorInputFingerprint = priorInputFingerprint
	config.ChoraOwnsPort = choraOwnsPort
	return Run(ctx, config, probes)
}

func run(ctx context.Context, config Config, probes Probes) (report Report) {
	now := probes.Now
	if now == nil {
		now = time.Now
	}
	started := now()
	var modelProbeAudit *ModelProbeAudit
	inputEvidence := &InputEvidence{ProxyURLSHA256: digestString(config.ProxyURL), ModelURLSHA256: digestString(config.ModelURL)}
	fingerprintParts := []string{
		"chora-preflight-v1", config.SourceRoot, config.InstallRoot, config.DataRoot,
		config.AuthFile, config.CAFile, config.ProxyURL, config.ModelURL, strconv.Itoa(config.Port),
		config.SourceManifest, config.BundleAggregate,
		AgentImageID, BoundaryImageID, RequiredCA256, HostProxyProbeURL,
	}
	// Installed Doctor issues the exact stable continuation fingerprint consumed
	// by service recovery/start and pre-attempt. Its Setup receipt and model
	// authority are validated and bound by the outer installed-product proof;
	// adding those proof-only digests here would make every continuation drift.
	defer func() {
		report.ModelProbe = modelProbeAudit
		report.InputEvidence = inputEvidence
		report.Elapsed = now().Sub(started)
		if report.Elapsed <= 0 {
			report.Elapsed = time.Nanosecond
		}
		digest := sha256.Sum256([]byte(strings.Join(fingerprintParts, "\x00")))
		report.InputFingerprint = fmt.Sprintf("%x", digest)
	}()
	if config.Deadline <= 0 || config.Deadline > preflightDeadlineLimit {
		config.Deadline = preflightDeadlineLimit
	}
	ctx, cancel := context.WithTimeout(ctx, config.Deadline)
	defer cancel()

	if probes.Host == nil || probes.Command == nil {
		return failed("preflight.internal", "required system probe unavailable", "complete Preflight probe set", "Reinstall Chora from the manifest-bound source bundle, then rerun chora doctor.")
	}
	if config.Mode == ModeInstalledDoctor && probes.QualifiedRuntime == nil {
		return failed("runtime.qualification", "installed runtime evidence unavailable", "release-bound active Generation, Engine qualification, and exact Runner", "Run installed Doctor only after successful Setup of the exact active Generation.")
	}
	goos, goarch := probes.Host()
	fingerprintParts = append(fingerprintParts, goos, goarch)
	if goos != "darwin" || goarch != "arm64" {
		return failed("host.platform", goos+"/"+goarch, "darwin/arm64 (macOS Apple Silicon)", "Run Chora on a supported macOS Apple Silicon host.")
	}

	dockerCommand := probes.Command
	managedImageID, verifierImageID, boundaryImageID := AgentImageID, AgentImageID, BoundaryImageID
	probeImageID := ""
	result := CommandResult{}
	if probes.QualifiedRuntime != nil {
		qualified, qualifiedErr := validateQualifiedRuntime(ctx, *probes.QualifiedRuntime)
		if qualifiedErr != nil {
			return failed("runtime.qualification", "installed runtime evidence unavailable", "stable exact Runner, Engine qualification, and Pi selection", "Repair or repeat explicit Chora setup, then restart Chora.")
		}
		dockerCommand = qualifiedDockerCommand(probes.QualifiedRuntime.Runner)
		managedImageID, verifierImageID, boundaryImageID = qualified.ManagedImageID, qualified.VerifierImageID, qualified.BoundaryImageID
		probeImageID = qualified.Contract.ProbeImageID()
		result = dockerCommand(ctx, "docker", "version", "--format", "{{.Client.Version}}|{{.Server.Version}}")
		parts := strings.Split(strings.TrimSpace(result.Stdout), "|")
		if result.Err != nil || len(parts) != 2 {
			return failed("docker.version", "qualified Docker Engine unavailable", "exact Docker client 29.6.1 and qualified compatible Engine", "Repair the exact installed Engine, then restart Chora.")
		}
		client, server := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if client != "29.6.1" || server == "" || server != qualified.Identity.EngineVersion() {
			return failed("docker.version", "qualified Docker version mismatch", "exact Docker client 29.6.1 and re-observed qualified Engine version", "Repair the exact installed Engine, then restart Chora.")
		}
		fingerprintParts = append(fingerprintParts, "qualified-runtime", qualified.GenerationID, qualified.ReleaseID, qualified.ManifestSHA256,
			qualified.Identity.Digest(), qualified.Qualification.Digest(), qualified.Contract.Digest(), managedImageID, verifierImageID, boundaryImageID, probeImageID, client, server, qualified.PiSelection.Version())
	} else {
		result = dockerCommand(ctx, "docker", "version", "--format", "{{.Client.Version}}|{{.Server.Version}}")
		parts := strings.Split(strings.TrimSpace(result.Stdout), "|")
		if result.Err != nil || len(parts) != 2 {
			return failed("docker.version", "Docker daemon unavailable", "Docker client and server 29.6.1", "Start Colima with Docker 29.6.1, then rerun chora doctor.")
		}
		client, server := strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
		if client != "29.6.1" || server != "29.6.1" {
			return failed("docker.version", "client "+client+"; server "+server, "Docker client and server 29.6.1", "Install Docker 29.6.1 and restart the Colima profile, then rerun chora doctor.")
		}
		fingerprintParts = append(fingerprintParts, client, server)
		result = dockerCommand(ctx, "docker", "context", "show")
		if result.Err != nil || strings.TrimSpace(result.Stdout) != "colima" {
			return failed("docker.context", strings.TrimSpace(result.Stdout), "Docker context colima", "Run docker context use colima, then rerun chora doctor.")
		}
		fingerprintParts = append(fingerprintParts, strings.TrimSpace(result.Stdout))
		result = probes.Command(ctx, "colima", "version")
		if result.Err != nil || !hasVersionToken(result.Stdout, "0.10.3") {
			return failed("colima.version", strings.TrimSpace(result.Stdout), "Colima 0.10.3", "Install Colima 0.10.3, then rerun chora doctor.")
		}
		fingerprintParts = append(fingerprintParts, strings.TrimSpace(result.Stdout))
		result = probes.Command(ctx, "colima", "list", "--json")
		status, statusErr := decodeColimaStatus([]byte(result.Stdout))
		if result.Err != nil || statusErr != nil || !strings.EqualFold(status.Status, "running") || status.Runtime != "docker" || status.Arch != "aarch64" {
			return failed("colima.state", "Colima not running", "running Colima profile", "Start Colima 0.10.3 with the declared capacity, then rerun chora doctor.")
		}
		if status.CPUs < 2 || status.Memory < 4<<30 || status.Disk < 20<<30 {
			return failed("colima.capacity", fmt.Sprintf("cpu %d; memory %d GiB; disk %d GiB", status.CPUs, status.Memory>>30, status.Disk>>30), "at least 2 CPU, 4 GiB memory, and 20 GiB disk", "Restart Colima with --cpu 2 --memory 4 --disk 20 or greater, then rerun chora doctor.")
		}
		fingerprintParts = append(fingerprintParts, fmt.Sprintf("%s:%s:%s:%d:%d:%d", status.Status, status.Arch, status.Runtime, status.CPUs, status.Memory, status.Disk))
	}
	for _, image := range []struct{ boundary, id string }{
		{"image.agent", managedImageID},
		{"image.verifier", verifierImageID},
		{"image.boundary", boundaryImageID},
		{"image.capability_probe", probeImageID},
	} {
		if image.id == "" {
			continue
		}
		result = dockerCommand(ctx, "docker", "image", "inspect", image.id, "--format", "{{.Id}}|{{.Os}}/{{.Architecture}}")
		want := image.id + "|linux/arm64"
		if result.Err != nil || strings.TrimSpace(result.Stdout) != want {
			action := "Load the exact pinned Linux ARM64 image into Colima, then rerun chora doctor."
			if probes.QualifiedRuntime != nil {
				action = "Restore the exact pinned Linux ARM64 image in the qualified installed Engine, then restart Chora."
			}
			return failed(image.boundary, safeCommandObservation(result), want, action)
		}
		fingerprintParts = append(fingerprintParts, strings.TrimSpace(result.Stdout))
	}
	if probes.File == nil || probes.ReadFile == nil {
		return failed("preflight.internal", "filesystem probe unavailable", "safe filesystem probes", "Reinstall Chora from the manifest-bound source bundle, then rerun chora doctor.")
	}
	authMeta := probes.File(config.AuthFile)
	if !authMeta.Exists || authMeta.Directory || authMeta.Symlink || !authMeta.Owner || authMeta.Mode&0o077 != 0 {
		return failed("oauth.file", fmt.Sprintf("exists %t; owner %t; mode %04o; regular %t", authMeta.Exists, authMeta.Owner, authMeta.Mode&0o777, !authMeta.Directory && !authMeta.Symlink), "owner-only regular CHORA_PI_CODEX_AUTH_FILE (mode 0600 or stricter)", "Set CHORA_PI_CODEX_AUTH_FILE to an owner-only regular file containing the openai-codex entry, then rerun chora doctor.")
	}
	authBytes, err := probes.ReadFile(config.AuthFile)
	authDigest := sha256.Sum256(authBytes)
	inputEvidence.AuthFileSHA256 = fmt.Sprintf("%x", authDigest)
	fingerprintParts = append(fingerprintParts, fmt.Sprintf("%x", authDigest), fmt.Sprintf("%04o", authMeta.Mode&0o777))
	var auth map[string]map[string]any
	if err != nil || json.Unmarshal(authBytes, &auth) != nil {
		return failed("oauth.entry", "openai-codex entry unreadable", "readable openai-codex provider entry", "Refresh the owner-only OAuth file with pi, then rerun chora doctor.")
	}
	provider, ok := auth["openai-codex"]
	if !ok || len(provider) == 0 {
		return failed("oauth.entry", "openai-codex entry absent", "readable openai-codex provider entry", "Authenticate the openai-codex provider with pi, then rerun chora doctor.")
	}
	if probes.Digest == nil {
		return failed("preflight.internal", "digest probe unavailable", "SHA-256 filesystem probe", "Reinstall Chora from the manifest-bound source bundle, then rerun chora doctor.")
	}
	caDigest, err := probes.Digest(config.CAFile)
	inputEvidence.CAFileSHA256 = caDigest
	if err != nil || caDigest != RequiredCA256 {
		return failed("trust.ca", safeDigest(caDigest, err), RequiredCA256, "Set --ca to the pinned StarPoint root CA file, then rerun chora doctor.")
	}
	fingerprintParts = append(fingerprintParts, caDigest)
	if config.ProxyURL != FixedProxyURL {
		return failed("proxy.route", safeURLObservation(config.ProxyURL), FixedProxyURL, "Set --proxy to the fixed enterprise proxy endpoint, then rerun chora doctor.")
	}
	if config.ModelURL != AllowedModelURL {
		return failed("model.destination", safeURLObservation(config.ModelURL), AllowedModelURL, "Set --model-url to the allowlisted Codex endpoint, then rerun chora doctor.")
	}
	if probes.Proxy == nil {
		return failed("preflight.internal", "proxy probe unavailable", "bounded fixed proxy probe", "Reinstall Chora from the manifest-bound source bundle, then rerun chora doctor.")
	}
	result = probes.Proxy(ctx, ProxyRequest{URL: AllowedAuthURL, ContractProxyURL: config.ProxyURL, DialProxyURL: HostProxyProbeURL, CAFile: config.CAFile})
	proxyCategory, proxyOK := proxyStatusCategory(result.Stdout)
	if result.Err != nil || !proxyOK {
		return failed("proxy.reachability", "fixed proxy TLS request failed", "TLS reachability through fixed enterprise proxy to auth.openai.com", "Restore the fixed enterprise proxy and pinned CA route, then rerun chora doctor.")
	}
	fingerprintParts = append(fingerprintParts, "proxy:"+proxyCategory)
	if probes.Model == nil {
		return failed("preflight.internal", "model probe unavailable", "bounded authenticated model probe", "Reinstall Chora from the manifest-bound source bundle, then rerun chora doctor.")
	}
	token := credentialString(provider)
	accountID := credentialField(provider, "accountId", "account_id", "accountID")
	if token == "" || accountID == "" {
		return failed("oauth.entry", "openai-codex credential fields absent", "readable openai-codex provider entry", "Refresh the openai-codex OAuth credential with pi, then rerun chora doctor.")
	}
	request := ModelRequest{URL: config.ModelURL, ContractProxyURL: config.ProxyURL, DialProxyURL: HostProxyProbeURL, CAFile: config.CAFile, AccessToken: token, AccountID: accountID}
	modelProbeAudit = &ModelProbeAudit{MaxAttempts: modelProbeMaxAttempts}
	result, _, modelOK := runModelProbe(ctx, probes.Model, request, modelProbeAudit)
	if result.Err != nil || !modelOK {
		return failed("model.reachability", "authenticated request failed", "authenticated reachability through fixed proxy to allowlisted Codex endpoint", "Verify enterprise proxy access and refresh openai-codex OAuth, then rerun chora doctor.")
	}
	fingerprintParts = append(fingerprintParts, "model:authenticated-reachable")
	toolChecks := []struct {
		boundary string
		name     string
		args     []string
		required string
		valid    func(string) bool
	}{
		{"tool.go", "go", []string{"version"}, "Go 1.26 or newer", func(value string) bool { return versionAtLeast(value, "go", 1, 26, 0) }},
		{"tool.node", "node", []string{"--version"}, "Node.js 22.12.0 or newer", func(value string) bool { return versionAtLeast(value, "v", 22, 12, 0) }},
		{"tool.npm", "npm", []string{"--version"}, "npm 11 or newer", func(value string) bool { return versionAtLeast(value, "", 11, 0, 0) }},
		{"tool.pi", "pi", []string{"--version"}, "Pi Coding Agent 0.84.2", func(value string) bool { return strings.TrimSpace(value) == "0.84.2" }},
	}
	for _, check := range toolChecks {
		if check.name == "pi" && probes.QualifiedRuntime != nil {
			continue
		}
		result = probes.Command(ctx, check.name, check.args...)
		if result.Err != nil || !check.valid(result.Stdout) {
			return failed(check.boundary, safeCommandObservation(result), check.required, "Install the required pinned tool version, then rerun chora doctor.")
		}
		fingerprintParts = append(fingerprintParts, strings.TrimSpace(result.Stdout))
	}
	if config.Port < 1 || config.Port > 65535 || !validFingerprint(config.BundleAggregate) ||
		!validRootInputs(config.Mode, config.SourceRoot, config.InstallRoot, config.DataRoot, config.AuthFile, config.CAFile) ||
		!filepath.IsAbs(config.SourceManifest) || filepath.Clean(config.SourceManifest) != config.SourceManifest || config.SourceManifest != filepath.Join(config.SourceRoot, sourcebundle.ManifestName) {
		return failed("root.inputs", "invalid or overlapping explicit paths/port", "mode-valid absolute source, install, data, auth, and CA paths plus port 1-65535", "Use distinct roots for fresh install; for an installed product, --source may equal the exact --install root while --data and credentials remain separate. Then rerun chora doctor.")
	}
	if probes.DiskFree == nil {
		return failed("preflight.internal", "disk probe unavailable", "filesystem capacity probe", "Reinstall Chora from the manifest-bound source bundle, then rerun chora doctor.")
	}
	free, err := probes.DiskFree(config.InstallRoot)
	if err != nil || free < 20<<30 {
		return failed("disk.capacity", fmt.Sprintf("%d GiB free", free>>30), "at least 20 GiB free", "Free at least 20 GiB on the installation volume, then rerun chora doctor.")
	}
	source := probes.File(config.SourceRoot)
	if !source.Exists || !source.Directory || source.Symlink || !source.Owner || source.Mode&0o777 != 0o700 {
		return failed("root.source", safeFileMetadata(source), "existing owner-controlled non-symlink source directory", "Set --source to the immutable manifest-bound source root, then rerun chora doctor.")
	}
	fingerprintParts = append(fingerprintParts, source.Identity, fmt.Sprintf("%04o", source.Mode&0o777))
	for _, root := range []struct{ boundary, path string }{{"root.install", config.InstallRoot}, {"root.data", config.DataRoot}} {
		metadata := probes.File(root.path)
		if config.Mode == ModeFreshInstall && (metadata.Exists || metadata.Symlink) {
			return failed(root.boundary, safeFileMetadata(metadata), "fresh absent root", "Choose a new empty path for the root, then rerun chora doctor.")
		}
		if config.Mode != ModeFreshInstall && (!metadata.Exists || !metadata.Directory || metadata.Symlink || !metadata.Owner || metadata.Mode&0o777 != 0o700) {
			return failed(root.boundary, safeFileMetadata(metadata), "prior-fingerprint-bound owner-controlled installed root", "Restore the exact Chora-owned root from the prior doctor report, then rerun Preflight.")
		}
	}
	if probes.Bundle == nil {
		return failed("preflight.internal", "bundle probe unavailable", "manifest-bound source verifier", "Reinstall Chora from the manifest-bound source bundle, then rerun chora doctor.")
	}
	bundle := probes.Bundle(ctx, BundleRequest{Mode: config.Mode, SourceRoot: config.SourceRoot, InstallRoot: config.InstallRoot, SourceManifest: config.SourceManifest, ExpectedAggregate: config.BundleAggregate})
	bundleBoundary := "source.bundle"
	if config.Mode != ModeFreshInstall {
		bundleBoundary = "install.bundle"
	}
	if bundle.Err != nil || !bundle.Valid || bundle.Aggregate != config.BundleAggregate {
		return failed(bundleBoundary, safeBundleObservation(bundle), config.BundleAggregate, "Restore the exact manifest-bound bundle aggregate, then rerun Preflight.")
	}
	if config.Mode != ModeFreshInstall {
		if probes.DataIdentity == nil {
			return failed("preflight.internal", "data identity probe unavailable", "marker-bound data identity probe", "Reinstall Chora from the manifest-bound source bundle, then rerun Preflight.")
		}
		if err := probes.DataIdentity(config); err != nil {
			return failed("data.identity", "data root is not bound to the installed product", "owner-only data root with exact installation marker", "Restore the marker-bound data root or repeat the fresh installation flow, then rerun Preflight.")
		}
	}
	portReady := probes.PortAvailable != nil && probes.PortAvailable(config.Port)
	if config.Mode == ModePreAttempt {
		portReady = config.ChoraOwnsPort
	}
	if !portReady {
		return failed("port.loopback", strconv.Itoa(config.Port), "available loopback TCP port", "Stop the process using the requested loopback port or pass another --port, then rerun chora doctor.")
	}
	for _, residue := range []struct {
		boundary string
		global   []string
		scoped   []string
	}{
		{"residue.container",
			[]string{"ps", "-a", "--filter", "label=chora.owner", "--format", "{{.ID}}"},
			[]string{"ps", "-a", "--filter", "label=chora.owner=dockersupervisor", "--filter", "label=chora.runtime_scope=" + recoveryScope(filepath.Join(config.DataRoot, "runtime", "pi")), "--format", "{{.ID}}"}},
		{"residue.verifier_container",
			[]string{"ps", "-a", "--filter", "label=chora.verifier_runtime_id", "--format", "{{.ID}}"},
			[]string{"ps", "-a", "--filter", "label=chora.verifier_runtime_id=" + recoveryScope(filepath.Join(config.DataRoot, "runtime")), "--format", "{{.ID}}"}},
		{"residue.network",
			[]string{"network", "ls", "--filter", "label=chora.owner", "--format", "{{.ID}}"},
			[]string{"network", "ls", "--filter", "label=chora.owner=dockersupervisor", "--filter", "label=chora.runtime_scope=" + recoveryScope(filepath.Join(config.DataRoot, "runtime", "pi")), "--format", "{{.ID}}"}},
	} {
		result = dockerCommand(ctx, "docker", residue.global...)
		if result.Err != nil {
			return failed(residue.boundary, residueObservation(result), "no Chora-owned residue", "Run the bounded Chora cleanup command for the reported owned residue, then rerun chora doctor.")
		}
		if config.Mode == ModeServiceRecovery {
			scoped := dockerCommand(ctx, "docker", residue.scoped...)
			if scoped.Err != nil || !sameResourceIDs(result.Stdout, scoped.Stdout) {
				return failed(residue.boundary, residueObservation(scoped), "only exact current-installation recovery residue", "Stop and inspect the reported Chora residue; do not recover resources outside this installation scope.")
			}
		} else if strings.TrimSpace(result.Stdout) != "" {
			return failed(residue.boundary, residueObservation(result), "no Chora-owned residue", "Run the bounded Chora cleanup command for the reported owned residue, then rerun chora doctor.")
		}
	}
	if probes.Residue == nil {
		return failed("preflight.internal", "workspace residue probe unavailable", "Agent and Verifier workspace inventory", "Reinstall Chora from the manifest-bound source bundle, then rerun chora doctor.")
	}
	workspaceResidue := probes.Residue(ctx, config)
	if workspaceResidue.Err != nil || config.Mode != ModeServiceRecovery && (workspaceResidue.AgentWorkspaces != 0 || workspaceResidue.VerifierWorkspaces != 0) {
		return failed("residue.workspace", safeResidueObservation(workspaceResidue), "zero Agent and Verifier attempt Workspaces; immutable baseline and empty owned parents allowed", "Run the bounded Chora cleanup flow for the same data root, then rerun chora doctor.")
	}
	return Report{Status: StatusPassed}
}

func recoveryScope(root string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(root)))
	return "sha256:" + fmt.Sprintf("%x", digest)
}

func sameResourceIDs(left, right string) bool {
	leftFields, rightFields := strings.Fields(left), strings.Fields(right)
	if len(leftFields) != len(rightFields) {
		return false
	}
	seen := make(map[string]int, len(leftFields))
	for _, field := range leftFields {
		seen[field]++
	}
	for _, field := range rightFields {
		if seen[field] == 0 {
			return false
		}
		seen[field]--
	}
	return true
}

func jsonDecoderAtEOF(decoder *json.Decoder) bool {
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

func versionAtLeast(raw, marker string, major, minor, patch int) bool {
	value := strings.TrimSpace(raw)
	if marker == "go" {
		index := strings.Index(value, "go1.")
		if index < 0 {
			return false
		}
		value = value[index+2:]
	} else {
		value = strings.TrimPrefix(value, marker)
	}
	fields := strings.Fields(value)
	if len(fields) == 0 {
		return false
	}
	value = fields[0]
	parts := strings.Split(value, ".")
	got := [3]int{}
	for index := range got {
		if index >= len(parts) {
			break
		}
		digits := strings.TrimLeftFunc(parts[index], func(r rune) bool { return r < '0' || r > '9' })
		digits = strings.TrimRightFunc(digits, func(r rune) bool { return r < '0' || r > '9' })
		parsed, err := strconv.Atoi(digits)
		if err != nil {
			return false
		}
		got[index] = parsed
	}
	want := [3]int{major, minor, patch}
	for index := range got {
		if got[index] != want[index] {
			return got[index] > want[index]
		}
	}
	return true
}

type colimaStatus struct {
	Name    string `json:"name"`
	Status  string `json:"status"`
	Arch    string `json:"arch"`
	CPUs    int    `json:"cpus"`
	Memory  uint64 `json:"memory"`
	Disk    uint64 `json:"disk"`
	Runtime string `json:"runtime"`
}

func decodeColimaStatus(data []byte) (colimaStatus, error) {
	var single colimaStatus
	if json.Unmarshal(data, &single) == nil && single.Name != "" {
		return single, nil
	}
	var profiles []colimaStatus
	if err := json.Unmarshal(data, &profiles); err != nil {
		return colimaStatus{}, err
	}
	for _, profile := range profiles {
		if profile.Name == "default" {
			return profile, nil
		}
	}
	return colimaStatus{}, fmt.Errorf("default Colima profile absent")
}

func hasVersionToken(raw, wanted string) bool {
	for _, token := range strings.Fields(raw) {
		if token == wanted {
			return true
		}
	}
	return false
}

func allAbsoluteDistinct(paths ...string) bool {
	seen := map[string]bool{}
	for _, path := range paths {
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || seen[path] {
			return false
		}
		seen[path] = true
	}
	for left := range paths {
		for right := left + 1; right < len(paths); right++ {
			if pathContains(paths[left], paths[right]) || pathContains(paths[right], paths[left]) {
				return false
			}
		}
	}
	return true
}

func validRootInputs(mode Mode, sourceRoot, installRoot, dataRoot, authFile, caFile string) bool {
	rootTopologyValid := false
	switch mode {
	case ModeFreshInstall:
		rootTopologyValid = allAbsoluteDistinct(sourceRoot, installRoot, dataRoot)
	case ModeInstalledDoctor, ModeServiceRecovery, ModeServiceStart, ModePreAttempt:
		rootTopologyValid = sourceRoot == installRoot && allAbsoluteDistinct(installRoot, dataRoot)
	}
	if !rootTopologyValid {
		return false
	}
	for _, file := range []string{authFile, caFile} {
		if !filepath.IsAbs(file) || filepath.Clean(file) != file || file == string(filepath.Separator) {
			return false
		}
	}
	// Credentials are mutable private operator input and may never be bundled or
	// placed beneath a Chora-owned output root. The pinned public CA may live in
	// the immutable source bundle but not beneath mutable install/data roots.
	if authFile == sourceRoot || authFile == installRoot || authFile == dataRoot || caFile == installRoot || caFile == dataRoot ||
		pathContains(sourceRoot, authFile) || pathContains(installRoot, authFile) || pathContains(dataRoot, authFile) ||
		pathContains(installRoot, caFile) || pathContains(dataRoot, caFile) || authFile == caFile {
		return false
	}
	return true
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func statusCode(value string) (int, bool) {
	trimmed := strings.TrimSpace(value)
	if len(trimmed) != 3 {
		return 0, false
	}
	status, err := strconv.Atoi(trimmed)
	return status, err == nil && status >= 100 && status <= 599
}

func proxyStatusCategory(value string) (string, bool) {
	status, ok := statusCode(value)
	if !ok || status == http.StatusProxyAuthRequired || status >= 500 || status < 200 {
		return "", false
	}
	switch {
	case status < 300:
		return "success", true
	case status < 400:
		return "redirect", true
	default:
		return "client-response", true
	}
}

func validFingerprint(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func safeBundleObservation(result BundleResult) string {
	if result.Err != nil {
		return "bundle verification failed"
	}
	if result.Aggregate == "" {
		return "bundle aggregate absent"
	}
	return result.Aggregate
}

func safeFileMetadata(metadata FileMetadata) string {
	return fmt.Sprintf("exists %t; directory %t; symlink %t; owner %t; mode %04o", metadata.Exists, metadata.Directory, metadata.Symlink, metadata.Owner, metadata.Mode&0o777)
}

func residueObservation(result CommandResult) string {
	if result.Err != nil {
		return "owned-resource inventory failed"
	}
	return "owned residue present"
}

func safeResidueObservation(result ResidueResult) string {
	if result.Err != nil {
		return "owned workspace inventory failed"
	}
	return fmt.Sprintf("Agent Workspaces %d; Verifier Workspaces %d", result.AgentWorkspaces, result.VerifierWorkspaces)
}

func safeDigest(value string, err error) string {
	if err != nil || value == "" {
		return "CA unreadable"
	}
	return value
}

func safeURLObservation(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Hostname() == "" {
		return "invalid URL"
	}
	host := parsed.Hostname()
	if port := parsed.Port(); port != "" {
		host = net.JoinHostPort(host, port)
	}
	return parsed.Scheme + "://" + host + " (path, query, and user info omitted)"
}

func credentialString(provider map[string]any) string {
	return credentialField(provider, "access", "access_token", "accessToken", "token")
}

func credentialField(provider map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := provider[key].(string); ok && strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func runModelProbe(ctx context.Context, probe func(context.Context, ModelRequest) CommandResult, request ModelRequest, audit *ModelProbeAudit) (CommandResult, string, bool) {
	delays := [...]time.Duration{250 * time.Millisecond, 500 * time.Millisecond}
	var result CommandResult
	for attempt := 1; attempt <= modelProbeMaxAttempts; attempt++ {
		result = probe(ctx, request)
		category, accepted := modelStatusCategory(result.Stdout)
		status, hasStatus := statusCode(result.Stdout)
		record := ModelProbeAttempt{Attempt: attempt}
		switch {
		case result.Err != nil:
			if result.RetryableTransport {
				record.Outcome = "transport_error"
			} else {
				record.Outcome = "permanent_probe_error"
			}
			record.Retryable = result.RetryableTransport && ctx.Err() == nil
		case accepted:
			record.Outcome = strings.ReplaceAll(category, "-", "_")
			record.StatusCode = status
		case hasStatus && status >= http.StatusInternalServerError && status <= 599:
			record.Outcome = "http_5xx"
			record.StatusCode = status
			record.Retryable = true
		case hasStatus:
			record.Outcome = "http_permanent_failure"
			record.StatusCode = status
		default:
			record.Outcome = "invalid_status"
		}
		audit.Attempts = append(audit.Attempts, record)
		if result.Err == nil && accepted {
			return result, category, true
		}
		if !record.Retryable || attempt == modelProbeMaxAttempts {
			return result, "", false
		}
		if !waitForModelRetry(ctx, delays[attempt-1]) {
			return result, "", false
		}
		audit.Attempts[len(audit.Attempts)-1].Retried = true
	}
	return result, "", false
}

func retryableHTTPTransportError(ctx context.Context, err error) bool {
	if err == nil || ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, errUndeclaredRedirectDestination) {
		return false
	}
	var certificateVerificationError *tls.CertificateVerificationError
	var unknownAuthorityError x509.UnknownAuthorityError
	var certificateInvalidError x509.CertificateInvalidError
	var hostnameError x509.HostnameError
	return !errors.As(err, &certificateVerificationError) &&
		!errors.As(err, &unknownAuthorityError) &&
		!errors.As(err, &certificateInvalidError) &&
		!errors.As(err, &hostnameError)
}

func waitForModelRetry(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func modelStatusCategory(value string) (string, bool) {
	status, ok := statusCode(value)
	if !ok {
		return "", false
	}
	if status >= 200 && status < 300 {
		return "accepted", true
	}
	switch status {
	case http.StatusBadRequest, http.StatusConflict, http.StatusUnprocessableEntity:
		return "authenticated-schema", true
	case http.StatusTooManyRequests:
		return "authenticated-quota", true
	default:
		return "", false
	}
}

func safeCommandObservation(result CommandResult) string {
	if result.Err != nil {
		return "command failed"
	}
	return strings.TrimSpace(result.Stdout)
}

func validateQualifiedRuntime(ctx context.Context, runtime QualifiedRuntime) (QualifiedRuntime, error) {
	if runtime.Runner == nil || !runtime.PiSelection.Configured() ||
		!runtime.Qualification.ValidFor(runtime.Identity, runtime.Contract) ||
		!validImageID(runtime.ManagedImageID) || !validImageID(runtime.VerifierImageID) || !validImageID(runtime.BoundaryImageID) ||
		runtime.GenerationID == "" || runtime.ReleaseID == "" || !validDigest(runtime.ManifestSHA256) {
		return QualifiedRuntime{}, errors.New("invalid installed runtime qualification")
	}
	observed, err := dockersupervisor.ObserveEngine(ctx, runtime.Runner)
	if err != nil || observed.Digest() != runtime.Identity.Digest() {
		return QualifiedRuntime{}, errors.New("installed Engine identity changed")
	}
	if err := pidistribution.ValidateSelection(ctx, runtime.PiSelection); err != nil {
		return QualifiedRuntime{}, errors.New("installed Pi selection changed")
	}
	return runtime, nil
}

func validDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func validImageID(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	for _, character := range value[len("sha256:"):] {
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return false
		}
	}
	return true
}

func digestString(value string) string {
	digest := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", digest)
}

func qualifiedDockerCommand(runner dockersupervisor.CommandRunner) func(context.Context, string, ...string) CommandResult {
	return func(ctx context.Context, name string, args ...string) CommandResult {
		if name != "docker" || runner == nil {
			return CommandResult{Err: errors.New("exact installed Docker Runner unavailable")}
		}
		result, err := runner.Run(ctx, dockersupervisor.Command{Args: slices.Clone(args)})
		if err == nil && result.ExitCode != 0 {
			err = errors.New("exact installed Docker command failed")
		}
		return CommandResult{Stdout: string(result.Stdout), Stderr: string(result.Stderr), Err: err}
	}
}

func failed(boundary, observed, required, action string) Report {
	return Report{Status: StatusFailed, Failure: &Failure{Boundary: boundary, Observed: observed, Required: required, Action: action}}
}
