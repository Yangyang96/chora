package trustedhost

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/execution"
)

type recordingSink struct {
	launch  execution.LaunchToken
	mu      sync.Mutex
	offsets map[execution.StreamKind][]int64
	exited  chan struct{}
	once    sync.Once
}

func newRecordingSink(launch execution.LaunchToken) *recordingSink {
	return &recordingSink{launch: launch, offsets: map[execution.StreamKind][]int64{}, exited: make(chan struct{})}
}

func (sink *recordingSink) Binding() execution.LaunchToken { return sink.launch }
func (sink *recordingSink) Notify(kind execution.StreamKind, offset int64) {
	sink.mu.Lock()
	sink.offsets[kind] = append(sink.offsets[kind], offset)
	sink.mu.Unlock()
}
func (sink *recordingSink) Exited() { sink.once.Do(func() { close(sink.exited) }) }

type fakeChild struct {
	pid          int
	once         sync.Once
	done         chan struct{}
	mu           sync.Mutex
	exitCode     int
	waitErr      error
	waits        int
	released     bool
	aborted      bool
	stdinClosed  bool
	releaseCheck func() error
	waitGate     <-chan struct{}
}

func newFakeChild(pid int) *fakeChild {
	return &fakeChild{pid: pid, done: make(chan struct{}), exitCode: -1}
}
func (child *fakeChild) PID() int { return child.pid }
func (child *fakeChild) Release() error {
	child.mu.Lock()
	child.released = true
	check := child.releaseCheck
	child.mu.Unlock()
	if check != nil {
		return check()
	}
	return nil
}
func (child *fakeChild) AbortGate() error {
	child.mu.Lock()
	child.aborted = true
	child.mu.Unlock()
	return nil
}
func (child *fakeChild) CloseStdin() error {
	child.mu.Lock()
	child.stdinClosed = true
	child.mu.Unlock()
	return nil
}
func (child *fakeChild) Wait() (int, error) {
	<-child.done
	if child.waitGate != nil {
		<-child.waitGate
	}
	child.mu.Lock()
	defer child.mu.Unlock()
	child.waits++
	return child.exitCode, child.waitErr
}
func (child *fakeChild) exit(code int) {
	child.mu.Lock()
	child.exitCode = code
	child.mu.Unlock()
	child.once.Do(func() { close(child.done) })
}

type fakePlatform struct {
	mu                  sync.Mutex
	pid                 int
	birth               string
	startErr            error
	observeErr          error
	observationOverride *processObservation
	groupAlive          bool
	termStops           bool
	termReapsLeader     bool
	killStops           bool
	signals             []syscall.Signal
	spec                processSpec
	child               *fakeChild
	stdout, stderr      []byte
	releaseCheck        func(processSpec) error
	childWaitGate       <-chan struct{}
}

func newFakePlatform() *fakePlatform {
	return &fakePlatform{pid: 4107, birth: "fake-birth-1", groupAlive: true, killStops: true}
}

func (platform *fakePlatform) Start(spec processSpec) (childProcess, error) {
	platform.mu.Lock()
	defer platform.mu.Unlock()
	platform.spec = cloneProcessSpec(spec)
	if platform.startErr != nil {
		return nil, platform.startErr
	}
	if len(platform.stdout) > 0 {
		_, _ = spec.Stdout.Write(platform.stdout)
	}
	if len(platform.stderr) > 0 {
		_, _ = spec.Stderr.Write(platform.stderr)
	}
	platform.child = newFakeChild(platform.pid)
	platform.child.waitGate = platform.childWaitGate
	if platform.releaseCheck != nil {
		platform.child.releaseCheck = func() error { return platform.releaseCheck(platform.spec) }
	}
	return platform.child, nil
}

func cloneProcessSpec(spec processSpec) processSpec {
	return processSpec{
		ExecutableIdentity: spec.ExecutableIdentity, Arguments: append([]string(nil), spec.Arguments...),
		Environment: append([]string(nil), spec.Environment...), Directory: spec.Directory,
		Stdin: append([]byte(nil), spec.Stdin...), Stdout: spec.Stdout, Stderr: spec.Stderr,
		GateReceiptPath: spec.GateReceiptPath, GateNonce: spec.GateNonce, GateWait: spec.GateWait,
	}
}

func (platform *fakePlatform) Observe(pid int) (processObservation, error) {
	platform.mu.Lock()
	defer platform.mu.Unlock()
	if platform.observeErr != nil {
		return processObservation{}, platform.observeErr
	}
	if platform.observationOverride != nil {
		return *platform.observationOverride, nil
	}
	if !platform.groupAlive {
		return processObservation{}, os.ErrProcessDone
	}
	return processObservation{PID: pid, PGID: pid, Birth: platform.birth}, nil
}

func (platform *fakePlatform) GroupAlive(int) (bool, error) {
	platform.mu.Lock()
	defer platform.mu.Unlock()
	return platform.groupAlive, nil
}

func (platform *fakePlatform) SignalGroup(_ int, signal syscall.Signal) error {
	platform.mu.Lock()
	defer platform.mu.Unlock()
	platform.signals = append(platform.signals, signal)
	if signal == syscall.SIGTERM && platform.termReapsLeader && platform.child != nil {
		platform.child.exit(0)
		platform.observationOverride = &processObservation{PID: platform.pid, PGID: platform.pid, Birth: "reused-unrelated-birth"}
	}
	stops := signal == syscall.SIGTERM && platform.termStops || signal == syscall.SIGKILL && platform.killStops
	if stops {
		platform.groupAlive = false
		if platform.child != nil {
			platform.child.exit(-1)
		}
	}
	return nil
}

func (platform *fakePlatform) WaitGroupGone(context.Context, int, time.Duration, time.Duration) (bool, error) {
	platform.mu.Lock()
	defer platform.mu.Unlock()
	return !platform.groupAlive, nil
}

func (platform *fakePlatform) snapshotSignals() []syscall.Signal {
	platform.mu.Lock()
	defer platform.mu.Unlock()
	return append([]syscall.Signal(nil), platform.signals...)
}

type manualTimerFactory struct {
	mu         sync.Mutex
	maxRuntime time.Duration
	deadline   chan time.Time
}

func (factory *manualTimerFactory) NewTimer(duration time.Duration) timer {
	if duration == factory.maxRuntime {
		factory.mu.Lock()
		if factory.deadline == nil {
			factory.deadline = make(chan time.Time, 1)
		}
		deadline := factory.deadline
		factory.mu.Unlock()
		return manualTimer{channel: deadline}
	}
	return realTimerFactory{}.NewTimer(duration)
}

func (factory *manualTimerFactory) fire() {
	for {
		factory.mu.Lock()
		deadline := factory.deadline
		factory.mu.Unlock()
		if deadline != nil {
			deadline <- time.Now()
			return
		}
		time.Sleep(time.Millisecond)
	}
}

type manualTimer struct{ channel <-chan time.Time }

func (timer manualTimer) C() <-chan time.Time { return timer.channel }
func (manualTimer) Stop() bool                { return true }

func TestStartUsesFreshPrivateWorkspaceAndFrozenNativeInvocation(t *testing.T) {
	platform := newFakePlatform()
	platform.stdout = []byte("stdout")
	platform.stderr = []byte("stderr")
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
	launch := execution.LaunchToken{Value: "launch-fresh"}
	arguments := []string{"--mode", "rpc"}
	environment := trustedEnvironment()
	environment["HOME"] = "/Users/example"
	invocation := invocationFor(t, config, source, launch, arguments, environment)
	arguments[0] = "mutated"
	environment["HOME"] = "mutated"
	sink := newRecordingSink(launch)
	outcome := supervisor.Start(context.Background(), invocation, sink)
	if outcome.Kind != execution.Started || !outcome.Handle.Valid() || !outcome.Identity.Valid() || outcome.LaunchToken != launch {
		t.Fatalf("Start = %#v", outcome)
	}

	platform.mu.Lock()
	spec := cloneProcessSpec(platform.spec)
	platform.mu.Unlock()
	if spec.ExecutableIdentity != config.AllowedExecutable || !reflect.DeepEqual(spec.Arguments, []string{"--mode", "rpc"}) || string(spec.Stdin) != "frozen prompt" {
		t.Fatalf("frozen process spec = %#v", spec)
	}
	if spec.Directory == source || !strings.HasPrefix(spec.Directory, config.RuntimeRoot+string(filepath.Separator)) {
		t.Fatalf("process directory = %q", spec.Directory)
	}
	if got, err := os.ReadFile(filepath.Join(spec.Directory, "tracked.txt")); err != nil || string(got) != "original" {
		t.Fatalf("fresh workspace copy = %q, %v", got, err)
	}
	if err := os.WriteFile(filepath.Join(spec.Directory, "tracked.txt"), []byte("attempt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(source, "tracked.txt")); string(got) != "original" {
		t.Fatalf("source workspace was mutated: %q", got)
	}
	for current := spec.Directory; current != config.RuntimeRoot; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("non-private attempt path %q: %v mode %v", current, err, info.Mode())
		}
	}
	if !sort.StringsAreSorted(spec.Environment) || containsEnvironmentKey(spec.Environment, "SHOULD_NOT_BE_AMBIENT") || environmentValue(spec.Environment, "HOME") != "/Users/example" {
		t.Fatalf("native environment = %#v", spec.Environment)
	}
	if environmentValue(spec.Environment, "PWD") != "" {
		t.Fatalf("stale PWD was inherited: %#v", spec.Environment)
	}
	for key, output := range terminalEnvironment {
		value := environmentValue(spec.Environment, key)
		if value != filepath.Join(filepath.Dir(spec.Directory), "output", output.name) {
			t.Fatalf("terminal mapping %s = %q", key, value)
		}
	}
	manifest, err := readManifest(filepath.Dir(spec.Directory))
	if err != nil || manifest.PID != platform.pid || manifest.PGID != platform.pid || manifest.Birth != platform.birth || manifest.Identity != outcome.Identity.Value || manifest.State != stateRunning {
		t.Fatalf("durable identity = %#v, %v", manifest, err)
	}

	duplicate := supervisor.Start(context.Background(), invocation, sink)
	if duplicate.Kind != execution.StartReconciliationRequired {
		t.Fatalf("duplicate Start = %#v", duplicate)
	}
	stopAndFinalize(t, supervisor, platform, outcome.Handle)
}

func TestExplicitDirectWorkingRootPersistsIdentityAndPreservesTaskWorktree(t *testing.T) {
	platform := newFakePlatform()
	base := t.TempDir()
	source := testSource(t, base)
	config := testConfig(t, base, 64, time.Minute)
	config.DirectWorkingRoot = true
	supervisor, err := newWithDependencies(config, platform, realTimerFactory{})
	if err != nil {
		t.Fatal(err)
	}
	config = supervisor.config.Config
	launch := execution.LaunchToken{Value: "launch-direct-worktree"}
	outcome := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), newRecordingSink(launch))
	if outcome.Kind != execution.Started {
		t.Fatalf("Start = %#v", outcome)
	}

	platform.mu.Lock()
	spec := cloneProcessSpec(platform.spec)
	platform.mu.Unlock()
	canonicalSource, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Directory != canonicalSource {
		t.Fatalf("direct process directory = %q, want %q", spec.Directory, canonicalSource)
	}
	record, err := supervisor.recordForHandle(outcome.Handle)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := readManifest(record.root)
	if err != nil || manifest.Schema != directArgvManifestSchema || manifest.WorkspaceMode != directWorkspaceMode || manifest.Workspace != canonicalSource {
		t.Fatalf("direct manifest = %#v, %v", manifest, err)
	}
	baseDigest := digestLaunchContract(config.AllowedExecutable, manifest.ArgumentDigest, manifest.EnvironmentDigest, manifest.RuntimeClosureDigest)
	if manifest.LaunchContractDigest != digestDirectLaunchContract(baseDigest, canonicalSource) {
		t.Fatal("direct launch contract does not bind the Task worktree")
	}
	if err := os.WriteFile(filepath.Join(spec.Directory, "tracked.txt"), []byte("attempt"), 0o600); err != nil {
		t.Fatal(err)
	}
	stopAndFinalize(t, supervisor, platform, outcome.Handle)
	if got, err := os.ReadFile(filepath.Join(source, "tracked.txt")); err != nil || string(got) != "attempt" {
		t.Fatalf("Finalize did not preserve direct Task worktree = %q, %v", got, err)
	}
	if _, err := os.Lstat(record.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Finalize retained owned runtime state: %v", err)
	}
}

func TestDirectWorkingRootRecoveryRejectsWorkspaceModeDrift(t *testing.T) {
	platform := newFakePlatform()
	base := t.TempDir()
	source := testSource(t, base)
	config := testConfig(t, base, 64, time.Minute)
	config.DirectWorkingRoot = true
	supervisor, err := newWithDependencies(config, platform, realTimerFactory{})
	if err != nil {
		t.Fatal(err)
	}
	config = supervisor.config.Config
	launch := execution.LaunchToken{Value: "launch-direct-mode-drift"}
	outcome := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), newRecordingSink(launch))
	if outcome.Kind != execution.Started {
		t.Fatalf("Start = %#v", outcome)
	}
	if stopped, err := supervisor.Stop(context.Background(), outcome.Handle, execution.StopIntent{}); err != nil || stopped.Kind != execution.StopConfirmed {
		t.Fatalf("Stop = %#v, %v", stopped, err)
	}
	if _, err := supervisor.Drain(context.Background(), outcome.Handle, execution.StreamOffsets{}, 1024); err != nil {
		t.Fatal(err)
	}

	copyConfig := config
	copyConfig.DirectWorkingRoot = false
	if _, err := newWithDependencies(copyConfig, platform, realTimerFactory{}); !errors.Is(err, ErrRecoveryUncertain) {
		t.Fatalf("workspace-mode drift recovery = %v", err)
	}
	if err := supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
}

func TestStartMergesCapturedAmbientEnvironmentWithoutAmbientChoraAuthority(t *testing.T) {
	platform := newFakePlatform()
	base := t.TempDir()
	config := testConfig(t, base, 64, time.Minute)
	secret := "provider-secret-must-not-be-persisted"
	ambient := []string{
		"PATH=/native/pi/bin:/usr/bin",
		"HOME=/Users/native-pi",
		"OPENAI_API_KEY=" + secret,
		"CHORA_RUN_ID=ambient-must-not-win",
		"PWD=/stale/ambient",
	}
	config.AmbientEnvironment = ambient
	supervisor, err := newWithDependencies(config, platform, realTimerFactory{})
	if err != nil {
		t.Fatal(err)
	}
	ambient[0] = "PATH=/mutated-after-construction"
	config.AmbientEnvironment[1] = "HOME=/mutated-after-construction"

	source := testSource(t, base)
	launch := execution.LaunchToken{Value: "launch-ambient-native"}
	environment := trustedEnvironment()
	environment["CHORA_RUN_ID"] = "invocation-authority"
	outcome := supervisor.Start(context.Background(), invocationFor(t, supervisor.config.Config, source, launch, nil, environment), newRecordingSink(launch))
	if outcome.Kind != execution.Started {
		t.Fatalf("Start = %#v", outcome)
	}
	platform.mu.Lock()
	spec := cloneProcessSpec(platform.spec)
	platform.mu.Unlock()
	if environmentValue(spec.Environment, "HOME") != "/Users/native-pi" ||
		environmentValue(spec.Environment, "PATH") != "/native/pi/bin:/usr/bin" ||
		environmentValue(spec.Environment, "OPENAI_API_KEY") != secret ||
		environmentValue(spec.Environment, "CHORA_RUN_ID") != "invocation-authority" ||
		environmentValue(spec.Environment, "PWD") != "" {
		t.Fatalf("merged native environment = %#v", spec.Environment)
	}
	manifestPath := filepath.Join(filepath.Dir(spec.Directory), manifestName)
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := readManifest(filepath.Dir(spec.Directory))
	if err != nil || manifest.AmbientEnvironmentDigest == "" || manifest.EnvironmentDigest == "" || strings.Contains(string(encoded), secret) {
		t.Fatalf("manifest leaked environment or omitted binding: %s, %#v, %v", encoded, manifest, err)
	}
	stopAndFinalize(t, supervisor, platform, outcome.Handle)
}

func TestAmbientEnvironmentValidationRejectsMalformedDuplicateAndInjectionEntries(t *testing.T) {
	tests := map[string][]string{
		"missing separator": {"HOME"},
		"malformed key":     {"BAD-KEY=value"},
		"nul":               {"HOME=bad\x00value"},
		"duplicate":         {"HOME=/one", "HOME=/two"},
		"dynamic loader":    {"LD_PRELOAD=/tmp/inject.so"},
		"interpreter":       {"NODE_OPTIONS=--require=/tmp/inject.js"},
	}
	for name, environment := range tests {
		t.Run(name, func(t *testing.T) {
			base := t.TempDir()
			config := testConfig(t, base, 64, time.Minute)
			config.AmbientEnvironment = environment
			if _, err := newWithDependencies(config, newFakePlatform(), realTimerFactory{}); !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("New error = %v", err)
			}
		})
	}
}

func TestRecoveryRejectsAmbientEnvironmentDigestDrift(t *testing.T) {
	platform := newFakePlatform()
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
	launch := execution.LaunchToken{Value: "launch-ambient-drift"}
	outcome := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), newRecordingSink(launch))
	if outcome.Kind != execution.Started {
		t.Fatalf("Start = %#v", outcome)
	}
	config.AmbientEnvironment = append([]string(nil), config.AmbientEnvironment...)
	for index, entry := range config.AmbientEnvironment {
		if strings.HasPrefix(entry, "HOME=") {
			config.AmbientEnvironment[index] = "HOME=/drifted-home"
		}
	}
	if _, err := newWithDependencies(config, platform, realTimerFactory{}); !errors.Is(err, ErrRecoveryUncertain) {
		t.Fatalf("recovery environment drift error = %v", err)
	}
	stopAndFinalize(t, supervisor, platform, outcome.Handle)
}

func TestRuntimeClosureDriftAtFinalSpawnGatePreventsProcessCreation(t *testing.T) {
	platform := newFakePlatform()
	base := t.TempDir()
	dependency := filepath.Join(base, "pi-dependency.js")
	original := []byte("export const runtime = 'exact';")
	if err := os.WriteFile(dependency, original, 0o600); err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256(original)
	validationCalls := 0
	config := testConfig(t, base, 64, time.Minute)
	config.RuntimeClosureIdentity = expected
	config.ValidateRuntimeClosure = func(context.Context) ([sha256.Size]byte, error) {
		validationCalls++
		content, err := os.ReadFile(dependency)
		if err != nil {
			return [sha256.Size]byte{}, err
		}
		return sha256.Sum256(content), nil
	}
	supervisor, err := newWithDependencies(config, platform, realTimerFactory{})
	if err != nil {
		t.Fatal(err)
	}
	if validationCalls != 1 {
		t.Fatalf("construction validations = %d", validationCalls)
	}
	if err := os.WriteFile(dependency, []byte("export const runtime = 'drifted';"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := testSource(t, base)
	launch := execution.LaunchToken{Value: "launch-closure-drift"}
	outcome := supervisor.Start(context.Background(), invocationFor(t, supervisor.config.Config, source, launch, nil, trustedEnvironment()), newRecordingSink(launch))
	if outcome.Kind != execution.StartProvenNoChild || platform.child != nil || validationCalls != 2 {
		t.Fatalf("Start = %#v, child = %#v, validations = %d", outcome, platform.child, validationCalls)
	}
	entries, err := os.ReadDir(supervisor.config.RuntimeRoot)
	if err != nil || len(entries) != 0 {
		t.Fatalf("failed final gate retained prepared state: %#v, %v", entries, err)
	}
}

func TestRuntimeClosureValidatorCannotBeReassignedAfterConstruction(t *testing.T) {
	platform := newFakePlatform()
	base := t.TempDir()
	dependency := filepath.Join(base, "pi-dependency.js")
	original := []byte("exact closure")
	if err := os.WriteFile(dependency, original, 0o600); err != nil {
		t.Fatal(err)
	}
	expected := sha256.Sum256(original)
	config := testConfig(t, base, 64, time.Minute)
	config.RuntimeClosureIdentity = expected
	config.ValidateRuntimeClosure = func(context.Context) ([sha256.Size]byte, error) {
		content, err := os.ReadFile(dependency)
		if err != nil {
			return [sha256.Size]byte{}, err
		}
		return sha256.Sum256(content), nil
	}
	supervisor, err := newWithDependencies(config, platform, realTimerFactory{})
	if err != nil {
		t.Fatal(err)
	}
	config.ValidateRuntimeClosure = func(context.Context) ([sha256.Size]byte, error) { return expected, nil }
	config.RuntimeClosureIdentity = sha256.Sum256([]byte("caller-mutated-identity"))
	if err := os.WriteFile(dependency, []byte("drift after construction"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := testSource(t, base)
	launch := execution.LaunchToken{Value: "launch-validator-reassignment"}
	outcome := supervisor.Start(context.Background(), invocationFor(t, supervisor.config.Config, source, launch, nil, trustedEnvironment()), newRecordingSink(launch))
	if outcome.Kind != execution.StartProvenNoChild || platform.child != nil {
		t.Fatalf("caller reassignment changed captured validator: %#v, child %#v", outcome, platform.child)
	}
}

func TestExactRuntimeClosureIsValidatedAndDurablyBound(t *testing.T) {
	platform := newFakePlatform()
	base := t.TempDir()
	expected := sha256.Sum256([]byte("complete exact Pi snapshot identity"))
	validationCalls := 0
	config := testConfig(t, base, 64, time.Minute)
	config.RuntimeClosureIdentity = expected
	config.ValidateRuntimeClosure = func(context.Context) ([sha256.Size]byte, error) {
		validationCalls++
		return expected, nil
	}
	supervisor, err := newWithDependencies(config, platform, realTimerFactory{})
	if err != nil {
		t.Fatal(err)
	}
	source := testSource(t, base)
	launch := execution.LaunchToken{Value: "launch-valid-closure"}
	outcome := supervisor.Start(context.Background(), invocationFor(t, supervisor.config.Config, source, launch, nil, trustedEnvironment()), newRecordingSink(launch))
	if outcome.Kind != execution.Started || validationCalls != 2 {
		t.Fatalf("Start = %#v, validations = %d", outcome, validationCalls)
	}
	platform.mu.Lock()
	spec := cloneProcessSpec(platform.spec)
	platform.mu.Unlock()
	manifest, err := readManifest(filepath.Dir(spec.Directory))
	if err != nil || manifest.RuntimeClosureDigest != fmt.Sprintf("%x", expected) ||
		manifest.LaunchContractDigest != digestLaunchContract(config.AllowedExecutable, manifest.ArgumentDigest, manifest.EnvironmentDigest, manifest.RuntimeClosureDigest) {
		t.Fatalf("durable Runtime closure binding = %#v, %v", manifest, err)
	}
	stopAndFinalize(t, supervisor, platform, outcome.Handle)
}

func TestRecoveryRequiresSameValidatedRuntimeClosureAuthority(t *testing.T) {
	platform := newFakePlatform()
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
	launch := execution.LaunchToken{Value: "launch-closure-recovery"}
	outcome := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), newRecordingSink(launch))
	if outcome.Kind != execution.Started {
		t.Fatalf("Start = %#v", outcome)
	}
	drifted := sha256.Sum256([]byte("different exact Runtime closure"))
	config.RuntimeClosureIdentity = drifted
	config.ValidateRuntimeClosure = func(context.Context) ([sha256.Size]byte, error) { return drifted, nil }
	if _, err := newWithDependencies(config, platform, realTimerFactory{}); !errors.Is(err, ErrRecoveryUncertain) {
		t.Fatalf("recovery closure drift error = %v", err)
	}
	config.ValidateRuntimeClosure = nil
	if _, err := newWithDependencies(config, platform, realTimerFactory{}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("recovery missing validator error = %v", err)
	}
	stopAndFinalize(t, supervisor, platform, outcome.Handle)
}

func TestStopEscalatesWholeGroupWaitsReapsAndBoundsStreams(t *testing.T) {
	platform := newFakePlatform()
	platform.stdout = []byte("0123456789")
	platform.stderr = []byte("error")
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 8)
	launch := execution.LaunchToken{Value: "launch-stop"}
	sink := newRecordingSink(launch)
	outcome := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), sink)
	if outcome.Kind != execution.Started {
		t.Fatalf("Start = %#v", outcome)
	}
	stopped, err := supervisor.Stop(context.Background(), outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	if err != nil || stopped.Kind != execution.StopConfirmed {
		t.Fatalf("Stop = %#v, %v", stopped, err)
	}
	if got := platform.snapshotSignals(); !reflect.DeepEqual(got, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("group signals = %#v", got)
	}
	platform.child.mu.Lock()
	waits := platform.child.waits
	platform.child.mu.Unlock()
	if waits != 1 {
		t.Fatalf("leader Wait calls = %d", waits)
	}
	stdout, err := supervisor.Read(context.Background(), outcome.Handle, execution.StreamStdout, 0, 64)
	if err != nil || string(stdout.Data) != "01234567" || !stdout.EOF {
		t.Fatalf("bounded stdout = %#v, %v", stdout, err)
	}
	first, err := supervisor.Drain(context.Background(), outcome.Handle, execution.StreamOffsets{}, 9)
	if err != nil || string(first.Chunks[execution.StreamStdout]) != "01234567" || string(first.Chunks[execution.StreamStderr]) != "e" || first.EOF[execution.StreamStderr] {
		t.Fatalf("first Drain = %#v, %v", first, err)
	}
	second, err := supervisor.Drain(context.Background(), outcome.Handle, first.Offsets, 16)
	if err != nil || string(second.Chunks[execution.StreamStderr]) != "rror" || !second.EOF[execution.StreamStdout] || !second.EOF[execution.StreamStderr] || second.TerminalFiles.ExitCode != -1 {
		t.Fatalf("second Drain = %#v, %v", second, err)
	}
	record, _ := supervisor.recordForHandle(outcome.Handle)
	if info, err := os.Stat(record.stdoutPath); err != nil || info.Size() != 8 {
		t.Fatalf("terminal stdout size = %v, %v", info, err)
	}
	if err := supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(record.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("owned attempt state remains: %v", err)
	}
}

func TestStopWaitsForOwnedReapAndTerminalPersistenceAfterGroupDeath(t *testing.T) {
	platform := newFakePlatform()
	waitGate := make(chan struct{})
	platform.childWaitGate = waitGate
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
	launch := execution.LaunchToken{Value: "launch-stop-owned-reap"}
	outcome := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), newRecordingSink(launch))
	if outcome.Kind != execution.Started {
		t.Fatalf("Start = %#v", outcome)
	}
	type stopResult struct {
		outcome execution.StopOutcome
		err     error
	}
	stopped := make(chan stopResult, 1)
	go func() {
		value, err := supervisor.Stop(context.Background(), outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel})
		stopped <- stopResult{outcome: value, err: err}
	}()
	deadline := time.Now().Add(time.Second)
	for len(platform.snapshotSignals()) < 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if signals := platform.snapshotSignals(); !reflect.DeepEqual(signals, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("group death was not proved before reap gate: %#v", signals)
	}
	select {
	case result := <-stopped:
		t.Fatalf("Stop returned before owned Wait/terminal persistence: %#v, %v", result.outcome, result.err)
	default:
	}
	close(waitGate)
	select {
	case result := <-stopped:
		if result.err != nil || result.outcome.Kind != execution.StopConfirmed {
			t.Fatalf("Stop after owned reap = %#v, %v", result.outcome, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("Stop did not complete after owned reap")
	}
	if _, err := supervisor.Drain(context.Background(), outcome.Handle, execution.StreamOffsets{}, 1024); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
}

func TestStopBackgroundContextHasFiniteOwnedReapCeiling(t *testing.T) {
	platform := newFakePlatform()
	waitGate := make(chan struct{})
	platform.childWaitGate = waitGate
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
	supervisor.config.ReapWait = 20 * time.Millisecond
	launch := execution.LaunchToken{Value: "launch-stop-reap-ceiling"}
	sink := newRecordingSink(launch)
	outcome := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), sink)
	if outcome.Kind != execution.Started {
		t.Fatalf("Start = %#v", outcome)
	}
	type stopResult struct {
		outcome execution.StopOutcome
		err     error
	}
	stopped := make(chan stopResult, 1)
	go func() {
		value, err := supervisor.Stop(context.Background(), outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel})
		stopped <- stopResult{outcome: value, err: err}
	}()
	select {
	case result := <-stopped:
		if result.err != nil || result.outcome.Kind != execution.StopUncertain || !strings.Contains(result.outcome.Diagnostic, "ceiling exceeded") {
			t.Fatalf("bounded background Stop = %#v, %v", result.outcome, result.err)
		}
	case <-time.After(time.Second):
		t.Fatal("background Stop exceeded its configured owned-reap ceiling")
	}
	close(waitGate)
	select {
	case <-sink.exited:
	case <-time.After(time.Second):
		t.Fatal("owned Wait did not complete after test gate release")
	}
	if _, err := supervisor.Drain(context.Background(), outcome.Handle, execution.StreamOffsets{}, 1024); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
}

func TestReconcileWaitsForOwnedNaturalExitProof(t *testing.T) {
	for _, groupAlive := range []bool{false, true} {
		t.Run(fmt.Sprintf("groupAlive=%v", groupAlive), func(t *testing.T) {
			platform := newFakePlatform()
			gate := make(chan struct{})
			platform.childWaitGate = gate
			var release sync.Once
			defer release.Do(func() { close(gate) })
			supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
			launch := execution.LaunchToken{Value: "launch-natural-exit-reconcile"}
			sink := newRecordingSink(launch)
			outcome := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), sink)
			if outcome.Kind != execution.Started {
				t.Fatalf("Start = %#v", outcome)
			}
			platform.mu.Lock()
			platform.observeErr = os.ErrProcessDone
			platform.groupAlive = groupAlive
			platform.mu.Unlock()
			platform.child.exit(0)
			result := make(chan execution.ReconcileOutcome, 1)
			go func() {
				reconciled, _ := supervisor.Reconcile(context.Background(), outcome.Identity)
				result <- reconciled
			}()
			select {
			case reconciled := <-result:
				t.Fatalf("reconcile raced ahead of owned reap: %#v", reconciled)
			case <-time.After(20 * time.Millisecond):
			}
			platform.mu.Lock()
			platform.groupAlive = false
			platform.mu.Unlock()
			release.Do(func() { close(gate) })
			select {
			case reconciled := <-result:
				if reconciled.Kind != execution.ReconcileDead || reconciled.Handle != outcome.Handle || reconciled.LaunchToken != launch {
					t.Fatalf("reconcile after owned reap = %#v", reconciled)
				}
			case <-time.After(time.Second):
				t.Fatal("reconcile did not finish after owned terminal proof")
			}
			if signals := platform.snapshotSignals(); len(signals) != 0 {
				t.Fatalf("natural exit was signalled: %#v", signals)
			}
		})
	}
}

func TestRecoveredIdentityMismatchFailsClosedWithoutSignal(t *testing.T) {
	platform := newFakePlatform()
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
	launch := execution.LaunchToken{Value: "launch-recovery-mismatch"}
	outcome := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), newRecordingSink(launch))
	if outcome.Kind != execution.Started {
		t.Fatalf("Start = %#v", outcome)
	}
	restarted, err := newWithDependencies(config, platform, realTimerFactory{})
	if err != nil {
		t.Fatal(err)
	}
	alive, err := restarted.Reconcile(context.Background(), outcome.Identity)
	if err != nil || alive.Kind != execution.ReconcileAlive || alive.Handle != outcome.Handle || alive.LaunchToken != launch {
		t.Fatalf("exact recovered reconcile = %#v, %v", alive, err)
	}
	platform.mu.Lock()
	platform.observationOverride = &processObservation{PID: platform.pid, PGID: platform.pid, Birth: "different-process-birth"}
	platform.mu.Unlock()
	mismatch, err := restarted.Reconcile(context.Background(), outcome.Identity)
	if err != nil || mismatch.Kind != execution.ReconcileUncertain {
		t.Fatalf("mismatch reconcile = %#v, %v", mismatch, err)
	}
	stopped, err := restarted.Stop(context.Background(), outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	if err != nil || stopped.Kind != execution.StopUncertain || len(platform.snapshotSignals()) != 0 {
		t.Fatalf("mismatched recovered Stop = %#v, %v, signals %#v", stopped, err, platform.snapshotSignals())
	}
	platform.mu.Lock()
	platform.observationOverride = nil
	platform.observeErr = os.ErrProcessDone
	platform.mu.Unlock()
	missingLeader, err := restarted.Reconcile(context.Background(), outcome.Identity)
	if err != nil || missingLeader.Kind != execution.ReconcileUncertain {
		t.Fatalf("missing leader with live group reconcile = %#v, %v", missingLeader, err)
	}
	stopped, err = restarted.Stop(context.Background(), outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	if err != nil || stopped.Kind != execution.StopUncertain || len(platform.snapshotSignals()) != 0 {
		t.Fatalf("missing recovered leader Stop = %#v, %v, signals %#v", stopped, err, platform.snapshotSignals())
	}
	platform.mu.Lock()
	platform.observeErr = nil
	platform.mu.Unlock()
	stopAndFinalize(t, supervisor, platform, outcome.Handle)
}

func TestRecoveredExactIdentityMayTerminateOwnedGroup(t *testing.T) {
	platform := newFakePlatform()
	platform.termStops = true
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
	launch := execution.LaunchToken{Value: "launch-recovery-exact"}
	outcome := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), newRecordingSink(launch))
	if outcome.Kind != execution.Started {
		t.Fatalf("Start = %#v", outcome)
	}
	restarted, err := newWithDependencies(config, platform, realTimerFactory{})
	if err != nil {
		t.Fatal(err)
	}
	stopped, err := restarted.Stop(context.Background(), outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	if err != nil || stopped.Kind != execution.StopConfirmed {
		t.Fatalf("recovered Stop = %#v, %v", stopped, err)
	}
	if got := platform.snapshotSignals(); !reflect.DeepEqual(got, []syscall.Signal{syscall.SIGTERM}) {
		t.Fatalf("recovered group signals = %#v", got)
	}
	select {
	case <-platform.child.done:
	case <-time.After(time.Second):
		t.Fatal("original child was not terminated")
	}
}

func TestUnidentifiedStartedChildIsKilledReapedAndNeverSignalledOnRecovery(t *testing.T) {
	platform := newFakePlatform()
	platform.observeErr = errors.New("birth probe unavailable")
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
	launch := execution.LaunchToken{Value: "launch-no-birth"}
	sink := newRecordingSink(launch)
	outcome := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), sink)
	if outcome.Kind != execution.StartReconciliationRequired || outcome.Identity.Valid() {
		t.Fatalf("Start = %#v", outcome)
	}
	if got := platform.snapshotSignals(); !reflect.DeepEqual(got, []syscall.Signal{syscall.SIGKILL}) {
		t.Fatalf("containment signals = %#v", got)
	}
	select {
	case <-sink.exited:
	case <-time.After(time.Second):
		t.Fatal("unidentified child was not reaped")
	}
	reconciled, err := supervisor.ReconcileLaunch(context.Background(), launch)
	if err != nil || reconciled.Kind != execution.ReconcileDead {
		t.Fatalf("ReconcileLaunch = %#v, %v", reconciled, err)
	}
}

func TestRuntimeDeadlineTerminatesProcessGroup(t *testing.T) {
	platform := newFakePlatform()
	deadline := time.Second
	timers := &manualTimerFactory{maxRuntime: deadline}
	supervisor, config, source := newTestSupervisorWithConfig(t, platform, timers, 64, deadline)
	launch := execution.LaunchToken{Value: "launch-timeout"}
	sink := newRecordingSink(launch)
	outcome := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), sink)
	if outcome.Kind != execution.Started {
		t.Fatalf("Start = %#v", outcome)
	}
	timers.fire()
	select {
	case <-sink.exited:
	case <-time.After(time.Second):
		t.Fatal("timeout did not reap child")
	}
	if got := platform.snapshotSignals(); !reflect.DeepEqual(got, []syscall.Signal{syscall.SIGTERM, syscall.SIGKILL}) {
		t.Fatalf("timeout group signals = %#v", got)
	}
}

func TestFinalizeRejectsChangedOwnershipMarker(t *testing.T) {
	platform := newFakePlatform()
	platform.termStops = true
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
	launch := execution.LaunchToken{Value: "launch-finalize-owner"}
	outcome := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), newRecordingSink(launch))
	if outcome.Kind != execution.Started {
		t.Fatalf("Start = %#v", outcome)
	}
	if stopped, err := supervisor.Stop(context.Background(), outcome.Handle, execution.StopIntent{}); err != nil || stopped.Kind != execution.StopConfirmed {
		t.Fatalf("Stop = %#v, %v", stopped, err)
	}
	if _, err := supervisor.Drain(context.Background(), outcome.Handle, execution.StreamOffsets{}, 64); err != nil {
		t.Fatal(err)
	}
	record, _ := supervisor.recordForHandle(outcome.Handle)
	manifest, err := readManifest(record.root)
	if err != nil {
		t.Fatal(err)
	}
	manifest.Owner = "not-chora"
	if err := writeManifest(record.root, manifest); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Finalize(context.Background(), outcome.Handle, execution.RetentionPolicy{}); !errors.Is(err, ErrFinalizeNotReady) {
		t.Fatalf("Finalize with changed ownership = %v", err)
	}
	if _, err := os.Stat(record.root); err != nil {
		t.Fatalf("Finalize removed unverified path: %v", err)
	}
}

func TestInvocationValidationFailsBeforeCreatingChild(t *testing.T) {
	platform := newFakePlatform()
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
	launch := execution.LaunchToken{Value: "launch-invalid"}
	environment := trustedEnvironment()
	environment["BAD-KEY"] = "value"
	invocation := invocationFor(t, config, source, launch, nil, environment)
	outcome := supervisor.Start(context.Background(), invocation, newRecordingSink(launch))
	if outcome.Kind != execution.StartProvenNoChild || platform.child != nil {
		t.Fatalf("invalid environment Start = %#v, child %#v", outcome, platform.child)
	}
	wrong, err := execution.NewInvocation(execution.InvocationParams{
		AdapterID: config.AllowedAdapterID, Executable: "/bin/echo", WorkingRoot: source,
		Environment: trustedEnvironment(), LaunchToken: execution.LaunchToken{Value: "wrong-executable"},
	})
	if err != nil {
		t.Fatal(err)
	}
	outcome = supervisor.Start(context.Background(), wrong, newRecordingSink(wrong.LaunchToken()))
	if outcome.Kind != execution.StartProvenNoChild || platform.child != nil {
		t.Fatalf("wrong executable Start = %#v", outcome)
	}
}

func TestStartPersistsExactLaunchContractBeforeGateRelease(t *testing.T) {
	platform := newFakePlatform()
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
	platform.releaseCheck = func(spec processSpec) error {
		manifest, err := readManifest(filepath.Dir(spec.Directory))
		if err != nil {
			return err
		}
		expected := digestLaunchContract(config.AllowedExecutable, digestStrings(spec.Arguments), digestStrings(spec.Environment), manifest.RuntimeClosureDigest)
		if manifest.State != stateRunning || manifest.PID != platform.pid || manifest.PGID != platform.pid || manifest.Birth != platform.birth ||
			manifest.Executable != config.AllowedExecutable.Path || manifest.ResolvedExecutable != config.AllowedExecutable.ResolvedPath ||
			manifest.LaunchContractDigest != expected || manifest.GateProtocol != gateProtocol || manifest.GateReceiptPath != spec.GateReceiptPath || manifest.GateNonce != spec.GateNonce {
			return errors.New("gate released before exact launch contract and live identity were durable")
		}
		return nil
	}
	launch := execution.LaunchToken{Value: "launch-contract-order"}
	outcome := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), newRecordingSink(launch))
	if outcome.Kind != execution.Started {
		t.Fatalf("Start = %#v", outcome)
	}
	platform.child.mu.Lock()
	released := platform.child.released
	platform.child.mu.Unlock()
	if !released {
		t.Fatal("launch gate was not released")
	}
	stopAndFinalize(t, supervisor, platform, outcome.Handle)
}

func TestPreparedCrashRecoveryProvesGateInertBeforeRemoval(t *testing.T) {
	platform := newFakePlatform()
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
	launch := execution.LaunchToken{Value: "launch-crash-pre-persist"}
	record, _, stdout, stderr, err := supervisor.prepare(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), newRecordingSink(launch))
	if err != nil {
		t.Fatal(err)
	}
	_ = stdout.Close()
	_ = stderr.Close()
	// The receipt proves a launcher may have been created. A prepared manifest
	// still proves it was never released, so parent-pipe EOF makes it inert.
	if err := writeGateReceipt(record.gateReceiptPath, gateReceipt{Protocol: gateProtocol, Nonce: record.gateNonce, PID: platform.pid}); err != nil {
		t.Fatal(err)
	}
	manifest, err := readManifest(record.root)
	if err != nil {
		t.Fatal(err)
	}
	manifest.GateOwnerPID = os.Getpid()
	manifest.GateOwnerBirth = "crashed-owner-birth-now-reused-pid"
	if err := writeManifest(record.root, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := newWithDependencies(config, platform, realTimerFactory{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(record.root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inert prepared root was not removed: %v", err)
	}
}

func TestPreparedRecoveryNeverDeletesWhileReleaseOwnerLives(t *testing.T) {
	platform := newFakePlatform()
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
	launch := execution.LaunchToken{Value: "launch-active-gate-owner"}
	record, _, stdout, stderr, err := supervisor.prepare(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), newRecordingSink(launch))
	if err != nil {
		t.Fatal(err)
	}
	_ = stdout.Close()
	_ = stderr.Close()
	if _, err := newWithDependencies(config, platform, realTimerFactory{}); !errors.Is(err, ErrRecoveryUncertain) {
		t.Fatalf("active prepared owner recovery = %v", err)
	}
	if _, err := os.Lstat(record.root); err != nil {
		t.Fatalf("active owner's prepared state was removed: %v", err)
	}
}

func TestPreparedRecoveryRejectsBrokenGateProof(t *testing.T) {
	platform := newFakePlatform()
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
	launch := execution.LaunchToken{Value: "launch-crash-broken-proof"}
	record, _, stdout, stderr, err := supervisor.prepare(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), newRecordingSink(launch))
	if err != nil {
		t.Fatal(err)
	}
	_ = stdout.Close()
	_ = stderr.Close()
	manifest, err := readManifest(record.root)
	if err != nil {
		t.Fatal(err)
	}
	manifest.GateProtocol = "unknown-gate"
	if err := writeManifest(record.root, manifest); err != nil {
		t.Fatal(err)
	}
	if _, err := newWithDependencies(config, platform, realTimerFactory{}); !errors.Is(err, ErrRecoveryUncertain) {
		t.Fatalf("broken prepared proof recovery = %v", err)
	}
	if _, err := os.Lstat(record.root); err != nil {
		t.Fatalf("uncertain prepared root was removed: %v", err)
	}
}

func TestLeaderReapNeverSignalsPossiblyReusedGroup(t *testing.T) {
	platform := newFakePlatform()
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
	launch := execution.LaunchToken{Value: "launch-reaped-reused-group"}
	sink := newRecordingSink(launch)
	outcome := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), sink)
	if outcome.Kind != execution.Started {
		t.Fatalf("Start = %#v", outcome)
	}
	platform.mu.Lock()
	platform.observationOverride = &processObservation{PID: platform.pid, PGID: platform.pid, Birth: "unrelated-reused-birth"}
	platform.mu.Unlock()
	platform.child.exit(0)
	select {
	case <-sink.exited:
	case <-time.After(time.Second):
		t.Fatal("leader was not reaped")
	}
	if signals := platform.snapshotSignals(); len(signals) != 0 {
		t.Fatalf("possibly reused group was signalled: %#v", signals)
	}
	reconciled, err := supervisor.Reconcile(context.Background(), outcome.Identity)
	if err != nil || reconciled.Kind != execution.ReconcileUncertain {
		t.Fatalf("reaped surviving group reconcile = %#v, %v", reconciled, err)
	}
}

func TestTERMLeaderReapWithholdsKILLFromReusedGroup(t *testing.T) {
	platform := newFakePlatform()
	platform.termReapsLeader = true
	supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
	launch := execution.LaunchToken{Value: "launch-term-reaps-leader"}
	outcome := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, trustedEnvironment()), newRecordingSink(launch))
	if outcome.Kind != execution.Started {
		t.Fatalf("Start = %#v", outcome)
	}
	stopped, err := supervisor.Stop(context.Background(), outcome.Handle, execution.StopIntent{Kind: execution.StopForCancel})
	if err != nil || stopped.Kind != execution.StopUncertain {
		t.Fatalf("Stop = %#v, %v", stopped, err)
	}
	if signals := platform.snapshotSignals(); !reflect.DeepEqual(signals, []syscall.Signal{syscall.SIGTERM}) {
		t.Fatalf("post-reap group signals = %#v", signals)
	}
}

func TestRuntimeInjectionEnvironmentIsRejectedBeforeSpawn(t *testing.T) {
	for index, key := range []string{"LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "NODE_OPTIONS", "PYTHONPATH", "RUBYOPT", "PERL5OPT", "JAVA_TOOL_OPTIONS", "GCONV_PATH"} {
		t.Run(key, func(t *testing.T) {
			platform := newFakePlatform()
			supervisor, config, source := newTestSupervisor(t, platform, realTimerFactory{}, 64)
			environment := trustedEnvironment()
			environment[key] = "injection"
			launch := execution.LaunchToken{Value: fmt.Sprintf("launch-injection-%d", index)}
			outcome := supervisor.Start(context.Background(), invocationFor(t, config, source, launch, nil, environment), newRecordingSink(launch))
			if outcome.Kind != execution.StartProvenNoChild || platform.child != nil {
				t.Fatalf("Start = %#v, child = %#v", outcome, platform.child)
			}
		})
	}
}

func newTestSupervisor(t *testing.T, platform *fakePlatform, timers timerFactory, capacity int64) (*Supervisor, Config, string) {
	return newTestSupervisorWithConfig(t, platform, timers, capacity, time.Minute)
}

func newTestSupervisorWithConfig(t *testing.T, platform *fakePlatform, timers timerFactory, capacity int64, maxRuntime time.Duration) (*Supervisor, Config, string) {
	t.Helper()
	base := t.TempDir()
	source := testSource(t, base)
	config := testConfig(t, base, capacity, maxRuntime)
	supervisor, err := newWithDependencies(config, platform, timers)
	if err != nil {
		t.Fatal(err)
	}
	config = supervisor.config.Config
	return supervisor, config, source
}

func testSource(t *testing.T, base string) string {
	t.Helper()
	source := filepath.Join(base, "source")
	if err := os.Mkdir(source, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "tracked.txt"), []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	return source
}

func testConfig(t *testing.T, base string, capacity int64, maxRuntime time.Duration) Config {
	t.Helper()
	runtimeClosureIdentity := sha256.Sum256([]byte("exact-test-Pi-Runtime-closure"))
	config := Config{
		RuntimeRoot: filepath.Join(base, "runtime"), AllowedAdapterID: "pi",
		AmbientEnvironment:     []string{"HOME=/ambient/home", "PATH=/ambient/bin", "OPENAI_API_KEY=test-provider-secret", "CHORA_RUN_ID=ambient-chora-must-be-filtered", "PWD=/ambient/stale"},
		RuntimeClosureIdentity: runtimeClosureIdentity,
		ValidateRuntimeClosure: func(context.Context) ([sha256.Size]byte, error) { return runtimeClosureIdentity, nil },
		MaxRuntime:             maxRuntime, TermWait: time.Millisecond, KillWait: 50 * time.Millisecond,
		GateWait: 50 * time.Millisecond, ProbeInterval: time.Millisecond, StreamCapacity: capacity,
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	config.AllowedExecutable, err = InspectExecutable(executable)
	if err != nil {
		t.Fatal(err)
	}
	config.AllowedArguments = []string{"--mode", "rpc"}
	return config
}

func invocationFor(t *testing.T, config Config, source string, launch execution.LaunchToken, arguments []string, environment map[string]string) execution.Invocation {
	t.Helper()
	if arguments == nil {
		arguments = append([]string(nil), config.AllowedArguments...)
	}
	invocation, err := execution.NewInvocation(execution.InvocationParams{
		AdapterID: config.AllowedAdapterID, Executable: config.AllowedExecutable.Path,
		Arguments: arguments, Environment: environment, WorkingRoot: source,
		Stdin: []byte("frozen prompt"), LaunchToken: launch,
	})
	if err != nil {
		t.Fatal(err)
	}
	return invocation
}

func trustedEnvironment() map[string]string {
	environment := make(map[string]string, len(terminalEnvironment)+1)
	for key, output := range terminalEnvironment {
		environment[key] = output.guest
	}
	environment["PWD"] = "/stale/source"
	return environment
}

func containsEnvironmentKey(environment []string, key string) bool {
	return environmentValue(environment, key) != ""
}

func environmentValue(environment []string, key string) string {
	prefix := key + "="
	for _, entry := range environment {
		if strings.HasPrefix(entry, prefix) {
			return strings.TrimPrefix(entry, prefix)
		}
	}
	return ""
}

func stopAndFinalize(t *testing.T, supervisor *Supervisor, platform *fakePlatform, handle execution.RuntimeHandle) {
	t.Helper()
	stopped, err := supervisor.Stop(context.Background(), handle, execution.StopIntent{Kind: execution.StopForCancel})
	if err != nil || stopped.Kind != execution.StopConfirmed {
		t.Fatalf("Stop = %#v, %v", stopped, err)
	}
	if _, err := supervisor.Drain(context.Background(), handle, execution.StreamOffsets{}, 1024); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Finalize(context.Background(), handle, execution.RetentionPolicy{}); err != nil {
		t.Fatal(err)
	}
	_ = platform
}
