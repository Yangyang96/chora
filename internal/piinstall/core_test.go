package piinstall

import (
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

var fixtureTarball = []byte("qualified Pi tarball fixture\n")

func fixtureContract(t *testing.T) Contract {
	t.Helper()
	manifest := []byte(`{"name":"chora-pi-install-qualification","version":"0.0.0","private":true,"dependencies":{"@earendil-works/pi-coding-agent":"file:../qualified-package.tgz"}}`)
	lockDocument := map[string]any{
		"name": "chora-pi-install-qualification", "version": "0.0.0", "lockfileVersion": 3, "requires": true,
		"packages": map[string]any{
			"":                             map[string]any{"name": "chora-pi-install-qualification", "version": "0.0.0", "dependencies": map[string]string{fixedPackage: "file:../qualified-package.tgz"}},
			"node_modules/" + fixedPackage: map[string]any{"version": fixedVersion, "resolved": "file:../qualified-package.tgz", "integrity": fixtureSRI(), "bin": map[string]string{"pi": fixedExecutable}},
		},
	}
	lock, err := json.Marshal(lockDocument)
	if err != nil {
		t.Fatal(err)
	}
	c := Contract{Package: fixedPackage, Version: fixedVersion, Registry: fixedRegistry, TarballURL: fixedTarball,
		TarballSRI: fixtureSRI(), ConsumerManifest: manifest, ConsumerLock: lock, ConsumerLockSHA256: hexSHA256(lock),
		ExecutableRelative: fixedExecutable, MinimumNodeVersion: "22.19.0", MinimumNPMMajor: 11,
		MaxTarballBytes: 1 << 20, NPMArguments: append([]string(nil), npmArgumentTemplate...), LifecycleScriptsOff: true}
	c.Digest = contractDigest(c)
	return c
}

func fixtureSRI() string {
	sum := sha512.Sum512(fixtureTarball)
	return "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

type fixtureDownloader struct {
	mu      sync.Mutex
	data    []byte
	block   chan struct{}
	started chan struct{}
	calls   int
}

func (d *fixtureDownloader) Download(ctx context.Context, request DownloadRequest) error {
	d.mu.Lock()
	d.calls++
	d.mu.Unlock()
	if d.started != nil {
		select {
		case d.started <- struct{}{}:
		default:
		}
	}
	if d.block != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-d.block:
		}
	}
	d.mu.Lock()
	data := append([]byte(nil), d.data...)
	d.mu.Unlock()
	return os.WriteFile(request.DestinationPath, data, 0o600)
}

type fixtureProbe struct{}

func (fixtureProbe) Probe(context.Context) (Toolchain, error) {
	return Toolchain{NodeExecutable: "/qualified/bin/node", NodeVersion: "v22.19.0", NPMExecutable: "/qualified/bin/npm", NPMVersion: "11.6.0"}, nil
}

type fixtureRunner struct {
	mu       sync.Mutex
	commands []Command
	mutate   func(string) error
}

func (r *fixtureRunner) Run(_ context.Context, command Command) (CommandResult, error) {
	r.mu.Lock()
	r.commands = append(r.commands, command)
	r.mu.Unlock()
	if len(command.Arguments) > 0 && command.Arguments[0] == "ci" {
		if r.mutate != nil {
			if err := r.mutate(command.Directory); err != nil {
				return CommandResult{}, err
			}
		}
		root := filepath.Join(command.Directory, "node_modules", "@earendil-works", "pi-coding-agent")
		if err := os.MkdirAll(filepath.Join(root, "dist", "bundle"), 0o700); err != nil {
			return CommandResult{}, err
		}
		metadata := []byte(`{"name":"@earendil-works/pi-coding-agent","version":"0.85.1","bin":{"pi":"dist/bundle/cli.js"}}`)
		if err := os.WriteFile(filepath.Join(root, "package.json"), metadata, 0o600); err != nil {
			return CommandResult{}, err
		}
		if err := os.WriteFile(filepath.Join(root, "dist", "bundle", "cli.js"), []byte("#!/usr/bin/env node\n"), 0o700); err != nil {
			return CommandResult{}, err
		}
		return CommandResult{}, nil
	}
	if len(command.Arguments) == 1 && command.Arguments[0] == "--version" {
		return CommandResult{Stdout: fixedVersion + "\n"}, nil
	}
	return CommandResult{}, errors.New("unexpected command")
}

type fixtureReadiness struct{ configured bool }

func (r fixtureReadiness) Probe(context.Context, string) (Readiness, error) {
	return Readiness{Configured: r.configured}, nil
}

func newFixtureCore(t *testing.T, downloader *fixtureDownloader, runner *fixtureRunner) *Core {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	sequence := 0
	core, err := newCore(Config{DataRoot: root, Downloader: downloader, ToolchainProbe: fixtureProbe{}, CommandRunner: runner,
		ReadinessProbe: fixtureReadiness{configured: true}, Clock: func() time.Time { return time.Unix(1000+int64(sequence), 0) },
		NewOperationID: func() string { sequence++; return "operation_" + strings.Repeat("a", 8) + string(rune('0'+sequence)) }}, fixtureContract(t), false)
	if err != nil {
		t.Fatal(err)
	}
	return core
}

func installFixture(t *testing.T, core *Core, key string, version uint64) Outcome {
	t.Helper()
	outcome, err := core.Install(context.Background(), InstallRequest{IdempotencyKey: key, ExpectedStateVersion: version})
	if err != nil {
		t.Fatalf("Install() error = %v", err)
	}
	return outcome
}

func TestFrozenContractIsDefensiveAndAccepted(t *testing.T) {
	c := FrozenContract()
	if hexSHA256(c.ConsumerManifest) != fixedManifestSHA256 || c.ConsumerLockSHA256 != fixedLockSHA256 || hexSHA256(c.ConsumerLock) != fixedLockSHA256 {
		t.Fatalf("lock digest = %s", hexSHA256(c.ConsumerLock))
	}
	if !c.LifecycleScriptsOff || !contains(c.NPMArguments, "--ignore-scripts") || contains(c.NPMArguments, "install") {
		t.Fatalf("unsafe npm arguments: %v", c.NPMArguments)
	}
	c.ConsumerLock[0] ^= 0xff
	c.NPMArguments[0] = "install"
	again := FrozenContract()
	if hexSHA256(again.ConsumerLock) != fixedLockSHA256 || again.NPMArguments[0] != "ci" {
		t.Fatal("FrozenContract returned mutable shared storage")
	}
}

func TestFrozenContractQualifiedDarwinARM64Shape(t *testing.T) {
	c := FrozenContract()
	var lock lockDocument
	if err := json.Unmarshal(c.ConsumerLock, &lock); err != nil {
		t.Fatal(err)
	}
	omitted := 0
	for path, pkg := range lock.Packages {
		if path != "" && pkg.Optional && (!platformAllows(pkg.OS, "darwin") || !platformAllows(pkg.CPU, "arm64")) {
			omitted++
		}
	}
	if len(lock.Packages) != 162 || omitted != 33 {
		t.Fatalf("packages=%d optional omissions=%d", len(lock.Packages), omitted)
	}
	if got := reflect.TypeOf(InstallRequest{}).NumField(); got != 2 {
		t.Fatalf("InstallRequest has %d fields, want 2 identity-only fields", got)
	}
}

func TestInstallPersistsSelectionAndRequiresRestart(t *testing.T) {
	downloader := &fixtureDownloader{data: fixtureTarball}
	runner := &fixtureRunner{}
	core := newFixtureCore(t, downloader, runner)
	outcome := installFixture(t, core, "request-0001", 0)
	if outcome.State.Code != StateInstalled || !outcome.State.SelectionPresent || !outcome.RestartRequired || outcome.Selection.PiVersion != fixedVersion {
		t.Fatalf("outcome = %#v", outcome)
	}
	if outcome.State.DestinationPath != core.piRoot || !outcome.State.Configured {
		t.Fatalf("state = %#v", outcome.State)
	}
	selection, err := core.ResolveSelection(context.Background())
	if err != nil || selection.ExecutablePath != outcome.Selection.ExecutablePath {
		t.Fatalf("ResolveSelection() = %#v, %v", selection, err)
	}
	runner.mu.Lock()
	commands := append([]Command(nil), runner.commands...)
	runner.mu.Unlock()
	if len(commands) < 3 {
		t.Fatalf("commands = %d", len(commands))
	}
	npm := commands[0]
	if npm.Executable != "/qualified/bin/npm" || !contains(npm.Arguments, "--ignore-scripts") || !contains(npm.Arguments, "--omit=dev") || strings.Contains(strings.Join(npm.Arguments, " "), " install ") {
		t.Fatalf("npm command = %#v", npm)
	}
	if strings.Contains(strings.Join(npm.Environment, "\n"), "TOKEN") || strings.Contains(strings.Join(npm.Environment, "\n"), "npm_config") {
		t.Fatalf("unsafe environment = %v", npm.Environment)
	}
}

func TestIntegrityFailurePreservesPriorSelection(t *testing.T) {
	downloader := &fixtureDownloader{data: fixtureTarball}
	runner := &fixtureRunner{}
	core := newFixtureCore(t, downloader, runner)
	first := installFixture(t, core, "request-0001", 0)
	downloader.mu.Lock()
	downloader.data = []byte("corrupt")
	downloader.mu.Unlock()
	_, err := core.Install(context.Background(), InstallRequest{IdempotencyKey: "request-0002", ExpectedStateVersion: first.State.StateVersion})
	var installErr *Error
	if !errors.As(err, &installErr) || installErr.Reason != ReasonIntegrity {
		t.Fatalf("second Install() error = %v", err)
	}
	selection, resolveErr := core.ResolveSelection(context.Background())
	if resolveErr != nil || selection.ExecutableSHA256 != first.Selection.ExecutableSHA256 {
		t.Fatalf("prior selection lost: %#v, %v", selection, resolveErr)
	}
	state, inspectErr := core.Inspect(context.Background())
	if inspectErr != nil || state.Code != StateInstalled || !state.SelectionPresent {
		t.Fatalf("Inspect() = %#v, %v", state, inspectErr)
	}
}

func TestInstalledIdempotencyDoesNotRepeatDownload(t *testing.T) {
	downloader := &fixtureDownloader{data: fixtureTarball}
	core := newFixtureCore(t, downloader, &fixtureRunner{})
	first := installFixture(t, core, "request-0001", 0)
	second, err := core.Install(context.Background(), InstallRequest{IdempotencyKey: "request-0001", ExpectedStateVersion: 0})
	if err != nil || second.Selection.ExecutableSHA256 != first.Selection.ExecutableSHA256 {
		t.Fatalf("duplicate Install() = %#v, %v", second, err)
	}
	downloader.mu.Lock()
	calls := downloader.calls
	downloader.mu.Unlock()
	if calls != 1 {
		t.Fatalf("download calls = %d, want 1", calls)
	}
}

func TestResolveSelectionRejectsExecutableDrift(t *testing.T) {
	core := newFixtureCore(t, &fixtureDownloader{data: fixtureTarball}, &fixtureRunner{})
	outcome := installFixture(t, core, "request-0001", 0)
	if err := os.WriteFile(outcome.Selection.ExecutablePath, []byte("changed\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := core.ResolveSelection(context.Background()); err == nil {
		t.Fatal("ResolveSelection accepted drift")
	}
	state, err := core.Inspect(context.Background())
	if err != nil || state.Code != StateDrifted || state.Reason != ReasonSelection {
		t.Fatalf("Inspect() = %#v, %v", state, err)
	}
}

func TestInspectRejectsSymlinkedSelection(t *testing.T) {
	core := newFixtureCore(t, &fixtureDownloader{data: fixtureTarball}, &fixtureRunner{})
	installFixture(t, core, "request-0001", 0)
	selectionPath := filepath.Join(core.piRoot, "selection.json")
	if err := os.Remove(selectionPath); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "selection.json")
	if err := os.WriteFile(external, []byte(`{}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, selectionPath); err != nil {
		t.Fatal(err)
	}
	state, err := core.Inspect(context.Background())
	if err != nil || state.Code != StateDrifted || state.Reason != ReasonSelection {
		t.Fatalf("Inspect() = %#v, %v", state, err)
	}
}

func TestInstallRejectsUnexpectedPackage(t *testing.T) {
	runner := &fixtureRunner{mutate: func(consumer string) error {
		root := filepath.Join(consumer, "node_modules", "unexpected")
		if err := os.MkdirAll(root, 0o700); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"unexpected","version":"1.0.0"}`), 0o600)
	}}
	core := newFixtureCore(t, &fixtureDownloader{data: fixtureTarball}, runner)
	_, err := core.Install(context.Background(), InstallRequest{IdempotencyKey: "request-0001", ExpectedStateVersion: 0})
	var installErr *Error
	if !errors.As(err, &installErr) || installErr.Reason != ReasonClosure {
		t.Fatalf("Install() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(core.piRoot, "selection.json")); !os.IsNotExist(err) {
		t.Fatalf("selection exists after closure failure: %v", err)
	}
}

func TestInstallRejectsConsumerLockMutation(t *testing.T) {
	runner := &fixtureRunner{mutate: func(consumer string) error {
		return os.WriteFile(filepath.Join(consumer, "package-lock.json"), []byte(`{"changed":true}`), 0o600)
	}}
	core := newFixtureCore(t, &fixtureDownloader{data: fixtureTarball}, runner)
	_, err := core.Install(context.Background(), InstallRequest{IdempotencyKey: "request-0001", ExpectedStateVersion: 0})
	var installErr *Error
	if !errors.As(err, &installErr) || installErr.Reason != ReasonLockDrift {
		t.Fatalf("Install() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(core.piRoot, "selection.json")); !os.IsNotExist(err) {
		t.Fatalf("selection exists after lock drift: %v", err)
	}
}

func TestInterprocessLockRejectsConcurrentInstall(t *testing.T) {
	core := newFixtureCore(t, &fixtureDownloader{data: fixtureTarball}, &fixtureRunner{})
	if err := core.ensurePrivateRoot(); err != nil {
		t.Fatal(err)
	}
	lock, err := core.acquireLock()
	if err != nil {
		t.Fatal(err)
	}
	defer lock.close()
	_, err = core.Install(context.Background(), InstallRequest{IdempotencyKey: "request-0001", ExpectedStateVersion: 0})
	var installErr *Error
	if !errors.As(err, &installErr) || installErr.Reason != ReasonInstallBusy {
		t.Fatalf("Install() error = %v", err)
	}
}

func TestCancelInterruptsAndCleansOnlyOwnedStage(t *testing.T) {
	started := make(chan struct{}, 1)
	downloader := &fixtureDownloader{data: fixtureTarball, block: make(chan struct{}), started: started}
	core := newFixtureCore(t, downloader, &fixtureRunner{})
	done := make(chan error, 1)
	go func() {
		_, err := core.Install(context.Background(), InstallRequest{IdempotencyKey: "request-0001", ExpectedStateVersion: 0})
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("download did not start")
	}
	state, err := core.Inspect(context.Background())
	if err != nil || state.Code != StateInstalling {
		t.Fatalf("Inspect() = %#v, %v", state, err)
	}
	if err := core.Cancel(context.Background(), state.OperationID); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		var installErr *Error
		if !errors.As(err, &installErr) || installErr.Reason != ReasonCancelled {
			t.Fatalf("Install() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled install did not stop")
	}
	if _, err := os.Stat(filepath.Join(core.piRoot, ".stage-"+state.OperationID)); !os.IsNotExist(err) {
		t.Fatalf("owned stage remains: %v", err)
	}
	final, err := core.Inspect(context.Background())
	if err != nil || final.Code != StateCancelled {
		t.Fatalf("Inspect() = %#v, %v", final, err)
	}
}

func TestNewCanonicalizesTrustedRootAndRejectsSymlinkedPiRoot(t *testing.T) {
	realRoot := t.TempDir()
	linkParent := t.TempDir()
	link := filepath.Join(linkParent, "data")
	if err := os.Symlink(realRoot, link); err != nil {
		t.Fatal(err)
	}
	core, err := newCore(Config{DataRoot: link, Downloader: &fixtureDownloader{}, ToolchainProbe: fixtureProbe{}, CommandRunner: &fixtureRunner{}}, fixtureContract(t), false)
	if err != nil {
		t.Fatal(err)
	}
	canonicalRoot, err := filepath.EvalSymlinks(realRoot)
	if err != nil {
		t.Fatal(err)
	}
	if core.dataRoot != canonicalRoot {
		t.Fatalf("data root = %q, want %q", core.dataRoot, canonicalRoot)
	}
	if err := os.Symlink(t.TempDir(), filepath.Join(canonicalRoot, "pi")); err != nil {
		t.Fatal(err)
	}
	_, err = core.Install(context.Background(), InstallRequest{IdempotencyKey: "request-0001", ExpectedStateVersion: 0})
	var installErr *Error
	if !errors.As(err, &installErr) || installErr.Reason != ReasonPrivateRoot {
		t.Fatalf("Install() error = %v", err)
	}
}

func TestInstallLockDoesNotFollowSymlink(t *testing.T) {
	core := newFixtureCore(t, &fixtureDownloader{data: fixtureTarball}, &fixtureRunner{})
	if err := core.ensurePrivateRoot(); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(t.TempDir(), "external-lock")
	if err := os.WriteFile(external, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(external, filepath.Join(core.piRoot, ".install.lock")); err != nil {
		t.Fatal(err)
	}
	_, err := core.Install(context.Background(), InstallRequest{IdempotencyKey: "request-0001", ExpectedStateVersion: 0})
	var installErr *Error
	if !errors.As(err, &installErr) || installErr.Reason != ReasonInstallBusy {
		t.Fatalf("Install() error = %v", err)
	}
	data, err := os.ReadFile(external)
	if err != nil || string(data) != "keep" {
		t.Fatalf("external lock changed: %q, %v", data, err)
	}
}

func TestResolveSelectionRejectsHardLinkedExecutable(t *testing.T) {
	core := newFixtureCore(t, &fixtureDownloader{data: fixtureTarball}, &fixtureRunner{})
	outcome := installFixture(t, core, "request-0001", 0)
	link := filepath.Join(t.TempDir(), "linked-pi")
	if err := os.Link(outcome.Selection.ExecutablePath, link); err != nil {
		t.Fatal(err)
	}
	if _, err := core.ResolveSelection(context.Background()); err == nil {
		t.Fatal("ResolveSelection accepted hard-linked executable")
	}
}

func TestUnconfiguredInstallKeepsInstalledStateAndNativeAction(t *testing.T) {
	root := t.TempDir()
	core, err := newCore(Config{DataRoot: root, Downloader: &fixtureDownloader{data: fixtureTarball}, ToolchainProbe: fixtureProbe{}, CommandRunner: &fixtureRunner{}, ReadinessProbe: fixtureReadiness{}, NewOperationID: func() string { return "operation_unconfig1" }}, fixtureContract(t), false)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := core.Install(context.Background(), InstallRequest{IdempotencyKey: "request-0001", ExpectedStateVersion: 0})
	if err != nil {
		t.Fatal(err)
	}
	if outcome.State.Code != StateInstalled || outcome.State.Configured || outcome.State.ConfigurationAction != ConfigurationCommand(outcome.Selection.ExecutablePath) || !outcome.RestartRequired {
		t.Fatalf("outcome = %#v", outcome)
	}
}

type failingDownloader struct{ failure error }

func (d failingDownloader) Download(context.Context, DownloadRequest) error { return d.failure }

func TestPersistedFailureIsBoundedAndRedacted(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	core, err := newCore(Config{DataRoot: root, Downloader: failingDownloader{failure: errors.New("TOKEN secret at /Users/private/credentials")}, ToolchainProbe: fixtureProbe{}, CommandRunner: &fixtureRunner{}, NewOperationID: func() string { return "operation_redacted1" }}, fixtureContract(t), false)
	if err != nil {
		t.Fatal(err)
	}
	_, err = core.Install(context.Background(), InstallRequest{IdempotencyKey: "request-0001", ExpectedStateVersion: 0})
	if err == nil {
		t.Fatal("Install succeeded")
	}
	stateBytes, readErr := os.ReadFile(filepath.Join(core.piRoot, "state.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	operationBytes, readErr := os.ReadFile(filepath.Join(core.piRoot, ".operations", "operation_redacted1.json"))
	if readErr != nil {
		t.Fatal(readErr)
	}
	persisted := string(stateBytes) + string(operationBytes)
	if strings.Contains(persisted, "TOKEN") || strings.Contains(persisted, "/Users/") || len(persisted) > 4096 {
		t.Fatalf("unsafe persisted failure: %s", persisted)
	}
}

type failingProbe struct{}

func (failingProbe) Probe(context.Context) (Toolchain, error) {
	return Toolchain{}, errors.New("missing node")
}

func TestPrerequisiteFailureHasDistinctState(t *testing.T) {
	root := t.TempDir()
	core, err := newCore(Config{DataRoot: root, Downloader: &fixtureDownloader{}, ToolchainProbe: failingProbe{}, CommandRunner: &fixtureRunner{}, NewOperationID: func() string { return "operation_prereq1" }}, fixtureContract(t), false)
	if err != nil {
		t.Fatal(err)
	}
	outcome, err := core.Install(context.Background(), InstallRequest{IdempotencyKey: "request-0001", ExpectedStateVersion: 0})
	if err == nil || outcome.State.Code != StatePrerequisiteBlocked || outcome.State.Reason != ReasonPrerequisite {
		t.Fatalf("Install() = %#v, %v", outcome, err)
	}
}

func TestStandardDownloaderRejectsCallerOriginWithoutNetwork(t *testing.T) {
	destination := filepath.Join(t.TempDir(), "package.tgz")
	err := (standardDownloader{}).Download(context.Background(), DownloadRequest{URL: "https://example.invalid/package.tgz", DestinationPath: destination, MaximumBytes: 1024})
	if err == nil {
		t.Fatal("downloader accepted non-registry origin")
	}
	if _, statErr := os.Stat(destination); !os.IsNotExist(statErr) {
		t.Fatalf("destination exists: %v", statErr)
	}
}

func TestPlatformRuleUsesNPMArchitectureNames(t *testing.T) {
	if !platformAllows([]string{"x64"}, "amd64") {
		t.Fatal("amd64 did not map to npm x64")
	}
	if runtime.GOOS == "darwin" && runtime.GOARCH == "arm64" && !platformAllows([]string{"darwin"}, runtime.GOOS) {
		t.Fatal("qualified platform rejected")
	}
}

func TestInspectRecoversInstalledSelectionFromStaleInstallingState(t *testing.T) {
	core := newFixtureCore(t, &fixtureDownloader{data: fixtureTarball}, &fixtureRunner{})
	outcome := installFixture(t, core, "request-0001", 0)
	stale := outcome.State
	stale.Code = StateInstalling
	stale.OperationID = "operation_stale01"
	stale.StateVersion++
	if err := core.saveState(stateRecord{State: stale, IdempotencyHash: hexSHA256([]byte("request-0001"))}); err != nil {
		t.Fatal(err)
	}
	state, err := core.Inspect(context.Background())
	if err != nil || state.Code != StateInstalled || state.Version != fixedVersion || !state.SelectionPresent {
		t.Fatalf("Inspect() = %#v, %v", state, err)
	}
}

func TestInstallCanRecoverFromStaleInstallingState(t *testing.T) {
	downloader := &fixtureDownloader{data: fixtureTarball}
	core := newFixtureCore(t, downloader, &fixtureRunner{})
	first := installFixture(t, core, "request-0001", 0)
	stale := first.State
	stale.Code = StateInstalling
	stale.OperationID = "operation_stale01"
	stale.StateVersion++
	if err := core.saveState(stateRecord{State: stale, IdempotencyHash: hexSHA256([]byte("request-0001"))}); err != nil {
		t.Fatal(err)
	}
	second, err := core.Install(context.Background(), InstallRequest{IdempotencyKey: "request-0002", ExpectedStateVersion: stale.StateVersion})
	if err != nil || second.State.Code != StateInstalled || second.Selection.ExecutablePath != first.Selection.ExecutablePath {
		t.Fatalf("recovery Install() = %#v, %v", second, err)
	}
}

func TestNewSuppliesStandardPorts(t *testing.T) {
	root := t.TempDir()
	core, err := New(Config{DataRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := core.downloader.(standardDownloader); !ok {
		t.Fatalf("downloader = %T", core.downloader)
	}
	if _, ok := core.runner.(standardCommandRunner); !ok {
		t.Fatalf("runner = %T", core.runner)
	}
	if _, ok := core.toolchain.(standardToolchainProbe); !ok {
		t.Fatalf("toolchain = %T", core.toolchain)
	}
}

func TestStandardCommandRunnerBoundsOutput(t *testing.T) {
	result, err := (standardCommandRunner{}).Run(context.Background(), Command{
		Executable: "/bin/sh", Arguments: []string{"-c", "printf 123456789"}, OutputLimit: 4,
	})
	if err != nil || result.ExitCode != 0 || result.Stdout != "1234\n[output truncated]" {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
}
