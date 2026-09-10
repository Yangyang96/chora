// Package trustedhost supervises explicitly trusted, native Agent processes.
//
// It deliberately makes no filesystem, credential, or network isolation claim.
// Its boundary is process lifecycle ownership: every launch gets a fresh private
// workspace, an immutable argv/environment, a dedicated process group, durable
// process identity, bounded streams, and fail-closed recovery.
// A native child can deliberately escape that lifecycle boundary by creating a
// new session/process group (for example, by double-forking); Trusted Local does
// not claim to contain or discover such escaped descendants.
package trustedhost

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Yangyang96/chora/internal/execution"
)

// RuntimeClosureValidator performs a read-only validation of the selected Pi
// Runtime closure and returns the exact identity it observed. It must not
// discover, install, repair, replace, or otherwise mutate Runtime state.
type RuntimeClosureValidator func(context.Context) ([sha256.Size]byte, error)

const (
	defaultMaxRuntime     = 20 * time.Minute
	defaultTermWait       = 3 * time.Second
	defaultKillWait       = 3 * time.Second
	defaultReapWait       = 3 * time.Second
	defaultProbeInterval  = 25 * time.Millisecond
	defaultStreamCapacity = int64(10 << 20)

	maxRuntimeCeiling     = 20 * time.Minute
	stopWaitCeiling       = 10 * time.Second
	streamCapacityCeiling = int64(10 << 20)
)

var (
	ErrInvalidConfig     = errors.New("invalid trusted-host supervisor config")
	ErrUnknownHandle     = errors.New("unknown trusted-host runtime handle")
	ErrInvalidStreamRead = errors.New("invalid trusted-host stream read")
	ErrInvalidDrain      = errors.New("invalid trusted-host stream drain")
	ErrFinalizeNotReady  = errors.New("trusted-host runtime is not ready to finalize")
	ErrRecoveryUncertain = errors.New("trusted-host recovery identity is uncertain")
	errLaunchStateExists = errors.New("trusted-host launch state already exists")
)

// Config is installation-scoped policy. AllowedExecutable must be the exact
// absolute Pi path selected and fingerprinted by the Runtime Adapter.
type Config struct {
	RuntimeRoot         string
	AllowedAdapterID    string
	AllowedExecutable   ExecutableIdentity
	AllowedArguments    []string
	AllowedObserverPath string
	ValidateObserver    func(context.Context) error
	// DirectWorkingRoot runs the native process in Invocation.WorkingRoot
	// instead of a private copy. This is only for explicitly disclosed Local
	// Connected execution where the caller owns and has already proven the Task
	// worktree. The default remains the M1 fresh-copy behavior.
	DirectWorkingRoot bool
	// AllowedTrailingPathRoot, when non-empty, permits exactly one extra final
	// argument that must be an absolute clean path inside this root (used for a
	// per-Attempt Pi session directory). When empty, arguments must match
	// AllowedArguments exactly.
	AllowedTrailingPathRoot string
	// RuntimeClosureIdentity binds the complete selected Pi Runtime snapshot,
	// including dependencies. ValidateRuntimeClosure must re-read that closure
	// without mutation and return the identity it observed. Both are optional:
	// a PATH-discovered Pi has no managed closure and relies on the executable
	// identity re-verification at spawn as its drift check.
	RuntimeClosureIdentity [sha256.Size]byte
	ValidateRuntimeClosure RuntimeClosureValidator
	// AmbientEnvironment is captured once when the supervisor is constructed.
	// Nil uses the production process environment; a non-nil slice is an exact
	// injectable snapshot for tests and embedding callers.
	AmbientEnvironment []string
	MaxRuntime         time.Duration
	TermWait           time.Duration
	KillWait           time.Duration
	ReapWait           time.Duration
	GateWait           time.Duration
	ProbeInterval      time.Duration
	StreamCapacity     int64
}

type normalizedConfig struct {
	Config
	ambientEnvironment       []string
	ambientEnvironmentDigest string
	runtimeClosureIdentity   [sha256.Size]byte
	validateRuntimeClosure   RuntimeClosureValidator
}

// Supervisor implements execution.ProcessSupervisor for Trusted Local Pi.
type Supervisor struct {
	config   normalizedConfig
	platform processPlatform
	timers   timerFactory

	startMu    sync.Mutex
	mu         sync.Mutex
	byHandle   map[string]*processRecord
	byIdentity map[string]*processRecord
	byLaunch   map[string]*processRecord
}

type processRecord struct {
	mu            sync.Mutex
	terminationMu sync.Mutex
	persistMu     sync.Mutex

	root, workspace, outputDir string
	stdoutPath, stderrPath     string
	gateReceiptPath, gateNonce string
	launchContractDigest       string
	ambientEnvironmentDigest   string
	runtimeClosureDigest       string
	gateOwnerPID               int
	gateOwnerBirth             string
	handle                     execution.RuntimeHandle
	identity                   execution.ProcessIdentity
	launch                     execution.LaunchToken
	terminal                   execution.TerminalFiles
	pid, pgid                  int
	birth                      string
	child                      childProcess
	sink                       execution.RuntimeSink
	recovered                  bool
	done                       chan struct{}
	doneOnce                   sync.Once
	leaderReaped               bool
	exitCode                   int
	outputLimitExceeded        bool
	terminalProof              bool
	terminalPersisted          bool
	state                      manifestState
	persistErr                 error
	diagnostic                 string
	directWorkspace            bool
	drained                    map[execution.StreamKind]bool
	notified                   map[execution.StreamKind]int64
}

// New creates a supervisor and restores only exact, valid records below its
// private RuntimeRoot. Any malformed owned record fails construction closed.
func New(config Config) (*Supervisor, error) {
	return newWithDependencies(config, newOSPlatform(), realTimerFactory{})
}

func newWithDependencies(config Config, platform processPlatform, timers timerFactory) (*Supervisor, error) {
	normalized, err := normalizeConfig(config)
	if err != nil || platform == nil || timers == nil {
		return nil, ErrInvalidConfig
	}
	if err := normalized.revalidateRuntimeClosure(context.Background()); err != nil {
		return nil, fmt.Errorf("%w: Runtime closure validation: %v", ErrInvalidConfig, err)
	}
	supervisor := &Supervisor{
		config: normalized, platform: platform, timers: timers,
		byHandle: make(map[string]*processRecord), byIdentity: make(map[string]*processRecord),
		byLaunch: make(map[string]*processRecord),
	}
	if err := supervisor.loadRecords(); err != nil {
		return nil, err
	}
	return supervisor, nil
}

func normalizeConfig(config Config) (normalizedConfig, error) {
	config.RuntimeRoot = strings.TrimSpace(config.RuntimeRoot)
	config.AllowedAdapterID = strings.TrimSpace(config.AllowedAdapterID)
	if config.RuntimeRoot == "" || !filepath.IsAbs(config.RuntimeRoot) || filepath.Clean(config.RuntimeRoot) != config.RuntimeRoot ||
		config.AllowedAdapterID == "" || config.MaxRuntime < 0 || config.TermWait < 0 || config.KillWait < 0 || config.ReapWait < 0 || config.GateWait < 0 || config.ProbeInterval < 0 || config.StreamCapacity < 0 {
		return normalizedConfig{}, ErrInvalidConfig
	}
	if config.MaxRuntime == 0 || config.MaxRuntime > maxRuntimeCeiling {
		config.MaxRuntime = defaultMaxRuntime
	}
	if config.TermWait == 0 || config.TermWait > stopWaitCeiling {
		config.TermWait = defaultTermWait
	}
	if config.KillWait == 0 || config.KillWait > stopWaitCeiling {
		config.KillWait = defaultKillWait
	}
	if config.ReapWait == 0 || config.ReapWait > stopWaitCeiling {
		config.ReapWait = defaultReapWait
	}
	if config.GateWait == 0 || config.GateWait > stopWaitCeiling {
		config.GateWait = defaultKillWait
	}
	if config.ProbeInterval == 0 || config.ProbeInterval > time.Second {
		config.ProbeInterval = defaultProbeInterval
	}
	if config.StreamCapacity == 0 || config.StreamCapacity > streamCapacityCeiling {
		config.StreamCapacity = defaultStreamCapacity
	}
	root, err := ensurePrivateRoot(config.RuntimeRoot)
	if err != nil {
		return normalizedConfig{}, fmt.Errorf("%w: runtime root: %v", ErrInvalidConfig, err)
	}
	config.RuntimeRoot = root
	if err := validateExecutableIdentity(config.AllowedExecutable); err != nil {
		return normalizedConfig{}, fmt.Errorf("%w: allowed executable: %v", ErrInvalidConfig, err)
	}
	if validateArguments(config.AllowedArguments) != nil {
		return normalizedConfig{}, fmt.Errorf("%w: valid exact native Pi arguments are required", ErrInvalidConfig)
	}
	if config.AllowedTrailingPathRoot != "" {
		root := strings.TrimSpace(config.AllowedTrailingPathRoot)
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return normalizedConfig{}, fmt.Errorf("%w: allowed trailing path root must be absolute and clean", ErrInvalidConfig)
		}
		root, err = ensurePrivateRoot(root)
		if err != nil {
			return normalizedConfig{}, fmt.Errorf("%w: allowed trailing path root must be private: %v", ErrInvalidConfig, err)
		}
		config.AllowedTrailingPathRoot = root
	}
	if (config.RuntimeClosureIdentity == ([sha256.Size]byte{})) != (config.ValidateRuntimeClosure == nil) {
		return normalizedConfig{}, fmt.Errorf("%w: Runtime closure identity and validator must be configured together", ErrInvalidConfig)
	}
	ambientSource := config.AmbientEnvironment
	if ambientSource == nil {
		ambientSource = os.Environ()
	}
	ambientEnvironment, err := captureAmbientEnvironment(ambientSource)
	if err != nil {
		return normalizedConfig{}, fmt.Errorf("%w: ambient environment: %v", ErrInvalidConfig, err)
	}
	config.AllowedArguments = append([]string(nil), config.AllowedArguments...)
	config.AmbientEnvironment = append([]string(nil), ambientEnvironment...)
	return normalizedConfig{
		Config:                   config,
		ambientEnvironment:       append([]string(nil), ambientEnvironment...),
		ambientEnvironmentDigest: digestEnvironment("chora.trusted-host.ambient-environment.v1", ambientEnvironment),
		runtimeClosureIdentity:   config.RuntimeClosureIdentity,
		validateRuntimeClosure:   config.ValidateRuntimeClosure,
	}, nil
}

func (config normalizedConfig) revalidateRuntimeClosure(ctx context.Context) error {
	if config.validateRuntimeClosure == nil {
		return nil
	}
	observed, err := config.validateRuntimeClosure(ctx)
	if err != nil {
		return err
	}
	if observed == ([sha256.Size]byte{}) || observed != config.runtimeClosureIdentity {
		return errors.New("Runtime closure identity drift")
	}
	return nil
}

func (supervisor *Supervisor) Start(ctx context.Context, invocation execution.Invocation, sink execution.RuntimeSink) execution.StartOutcome {
	launch := invocation.LaunchToken()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := supervisor.validateInvocation(invocation, sink); err != nil {
		return noChild(launch, err.Error())
	}
	supervisor.startMu.Lock()
	defer supervisor.startMu.Unlock()

	supervisor.mu.Lock()
	existing := supervisor.byLaunch[launch.Value]
	supervisor.mu.Unlock()
	if existing != nil {
		return reconciliationRequired(existing, "launch token is already owned by trusted-host state")
	}
	if err := ctx.Err(); err != nil {
		return noChild(launch, err.Error())
	}

	prepared, spec, stdoutFile, stderrFile, err := supervisor.prepare(ctx, invocation, sink)
	if err != nil {
		if errors.Is(err, errLaunchStateExists) {
			return execution.StartOutcome{Kind: execution.StartReconciliationRequired, LaunchToken: launch, Diagnostic: err.Error()}
		}
		return noChild(launch, err.Error())
	}
	if err := ctx.Err(); err != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		_ = removeOwnedPrepared(prepared)
		return noChild(launch, err.Error())
	}
	// Revalidate at the final supervisor boundary before every spawn. The gated
	// launcher repeats this check after release immediately before exec.
	if identityErr := validateExecutableIdentity(supervisor.config.AllowedExecutable); identityErr != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		_ = removeOwnedPrepared(prepared)
		return noChild(launch, "allowed executable identity drift before spawn: "+identityErr.Error())
	}
	if closureErr := supervisor.config.revalidateRuntimeClosure(ctx); closureErr != nil {
		_ = stdoutFile.Close()
		_ = stderrFile.Close()
		_ = removeOwnedPrepared(prepared)
		return noChild(launch, "Runtime closure identity drift before spawn: "+closureErr.Error())
	}
	child, startErr := supervisor.platform.Start(spec)
	closeErr := errors.Join(stdoutFile.Close(), stderrFile.Close())
	if startErr != nil {
		_ = removeOwnedPrepared(prepared)
		return noChild(launch, fmt.Sprintf("start trusted-host child: %v", startErr))
	}
	if closeErr != nil {
		prepared.diagnostic = "close parent stream files: " + closeErr.Error()
	}

	pid := child.PID()
	observation, observeErr := supervisor.platform.Observe(pid)
	if observeErr != nil || observation.PID != pid || observation.PGID != pid || observation.Birth == "" {
		return supervisor.containUnidentifiedChild(prepared, child, pid, observation, observeErr)
	}
	prepared.child = child
	prepared.pid = observation.PID
	prepared.pgid = observation.PGID
	prepared.birth = observation.Birth
	prepared.identity = processIdentity(prepared)
	prepared.state = stateRunning
	if err := supervisor.persist(prepared); err != nil {
		prepared.persistErr = err
		prepared.diagnostic = "persist exact process identity: " + err.Error()
	}
	supervisor.register(prepared)
	if prepared.persistErr != nil {
		_ = child.AbortGate()
		supervisor.startLiveMonitors(prepared)
		proved, diagnostic := supervisor.terminateAndProve(context.Background(), prepared, true)
		if !proved {
			supervisor.markUncertain(prepared, diagnostic)
		}
		return execution.StartOutcome{
			Kind: execution.StartReconciliationRequired, Identity: prepared.identity,
			LaunchToken: launch, Diagnostic: prepared.diagnostic,
		}
	}
	if err := child.Release(); err != nil {
		prepared.diagnostic = joinDiagnostic(prepared.diagnostic, "release persisted launch gate: "+err.Error())
		_ = child.AbortGate()
		supervisor.startLiveMonitors(prepared)
		proved, diagnostic := supervisor.terminateAndProve(context.Background(), prepared, true)
		if !proved {
			supervisor.markUncertain(prepared, diagnostic)
		}
		return reconciliationRequired(prepared, prepared.diagnostic)
	}
	supervisor.startLiveMonitors(prepared)
	return execution.StartOutcome{
		Kind: execution.Started, Handle: prepared.handle, Identity: prepared.identity,
		LaunchToken: launch,
	}
}

func (supervisor *Supervisor) validateInvocation(invocation execution.Invocation, sink execution.RuntimeSink) error {
	if invocation.AdapterID() != supervisor.config.AllowedAdapterID || invocation.Executable() != supervisor.config.AllowedExecutable.Path {
		return errors.New("invocation adapter or executable is not allowed")
	}
	if !filepath.IsAbs(invocation.Executable()) || filepath.Clean(invocation.Executable()) != invocation.Executable() {
		return errors.New("invocation executable must be absolute and clean")
	}
	if !invocation.LaunchToken().Valid() || sink == nil || sink.Binding() != invocation.LaunchToken() {
		return errors.New("runtime sink binding does not match launch token")
	}
	if err := validateAllowedArguments(invocation.Arguments(), supervisor.config.AllowedArguments, supervisor.config.AllowedTrailingPathRoot, supervisor.config.AllowedObserverPath); err != nil {
		return err
	}
	if err := validateEnvironment(invocation.Environment()); err != nil {
		return err
	}
	if err := validateExecutableIdentity(supervisor.config.AllowedExecutable); err != nil {
		return errors.New("allowed executable identity drift: " + err.Error())
	}
	if args := invocation.Arguments(); len(args) >= 4 && args[2] == "--extension" {
		if supervisor.config.ValidateObserver == nil {
			return errors.New("observer validation is unavailable")
		}
		if err := supervisor.config.ValidateObserver(context.Background()); err != nil {
			return err
		}
	}
	root := invocation.WorkingRoot()
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("source workspace must be absolute and clean")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("source workspace must be a real directory")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil || !filepath.IsAbs(resolvedRoot) {
		return errors.New("source workspace cannot be resolved")
	}
	if pathsOverlap(resolvedRoot, supervisor.config.RuntimeRoot) {
		return errors.New("source workspace and trusted-host runtime root overlap")
	}
	return nil
}

func (supervisor *Supervisor) containUnidentifiedChild(record *processRecord, child childProcess, pid int, observation processObservation, observeErr error) execution.StartOutcome {
	record.child = child
	record.pid = pid
	record.pgid = pid
	if observation.PGID > 0 {
		record.pgid = observation.PGID
	}
	record.state = stateUncertain
	record.diagnostic = "child started but exact PID/PGID/birth identity could not be captured"
	if observeErr != nil {
		record.diagnostic += ": " + observeErr.Error()
	}
	_ = supervisor.persist(record)
	supervisor.register(record)
	// The just-created child is still held by an exact parent handle. Kill the
	// Setpgid group immediately, then reap it; no later recovery path may signal
	// this process without a birth identity.
	_ = child.AbortGate()
	_ = supervisor.platform.SignalGroup(pid, syscall.SIGKILL)
	go supervisor.waitUnidentified(record, child, pid)
	return execution.StartOutcome{Kind: execution.StartReconciliationRequired, LaunchToken: record.launch, Diagnostic: record.diagnostic}
}

func (supervisor *Supervisor) waitUnidentified(record *processRecord, child childProcess, pgid int) {
	exitCode, waitErr := child.Wait()
	record.mu.Lock()
	record.leaderReaped = true
	record.exitCode = exitCode
	if waitErr != nil {
		record.diagnostic = joinDiagnostic(record.diagnostic, "wait unidentified child: "+waitErr.Error())
	}
	record.mu.Unlock()
	if gone, err := supervisor.platform.WaitGroupGone(context.Background(), pgid, supervisor.config.KillWait, supervisor.config.ProbeInterval); gone {
		supervisor.markTerminal(record, "unidentified child contained and reaped")
	} else {
		diagnostic := "unidentified child was reaped but process-group death was not proved"
		if err != nil {
			diagnostic += ": " + err.Error()
		}
		supervisor.markUncertain(record, diagnostic)
	}
	record.doneOnce.Do(func() { close(record.done) })
	safeExited(record.sink)
}

func (supervisor *Supervisor) startLiveMonitors(record *processRecord) {
	go supervisor.waitAndReap(record)
	go supervisor.expire(record)
	go supervisor.monitorStreams(record)
}

func (supervisor *Supervisor) waitAndReap(record *processRecord) {
	exitCode, waitErr := record.child.Wait()
	record.mu.Lock()
	record.leaderReaped = true
	record.exitCode = exitCode
	if waitErr != nil {
		record.diagnostic = joinDiagnostic(record.diagnostic, "wait child: "+waitErr.Error())
	}
	record.mu.Unlock()
	// Never signal a numeric PGID after reaping its leader. Without a fresh
	// owned-member birth proof, the number may already identify unrelated work.
	proved, diagnostic := supervisor.proveGroupGoneAfterReap(record)
	if proved {
		supervisor.markTerminal(record, diagnostic)
	} else {
		supervisor.markUncertain(record, diagnostic)
	}
	record.doneOnce.Do(func() { close(record.done) })
	safeExited(record.sink)
}

func (supervisor *Supervisor) proveGroupGoneAfterReap(record *processRecord) (bool, string) {
	gone, err := supervisor.platform.WaitGroupGone(context.Background(), record.pgid, supervisor.config.KillWait, supervisor.config.ProbeInterval)
	if gone {
		return true, "leader reaped and process group proved absent without a post-reap signal"
	}
	diagnostic := "leader reaped but process group remains; no post-reap signal sent without fresh owned-member birth proof"
	if err != nil {
		diagnostic += ": " + err.Error()
	}
	return false, diagnostic
}

func (supervisor *Supervisor) expire(record *processRecord) {
	timer := supervisor.timers.NewTimer(supervisor.config.MaxRuntime)
	defer timer.Stop()
	select {
	case <-record.done:
		return
	case <-timer.C():
		_, _ = supervisor.Stop(context.Background(), record.handle, execution.StopIntent{Kind: execution.StopForCancel, Reason: "trusted-host attempt timeout"})
	}
}

func (supervisor *Supervisor) Stop(ctx context.Context, handle execution.RuntimeHandle, _ execution.StopIntent) (execution.StopOutcome, error) {
	record, err := supervisor.recordForHandle(handle)
	if err != nil {
		return execution.StopOutcome{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if record.isTerminal() {
		return supervisor.awaitTerminalPersistence(ctx, record, "process group already proved dead"), nil
	}
	proved, diagnostic := supervisor.terminateAndProve(ctx, record, !record.recovered)
	if !proved {
		supervisor.markUncertain(record, diagnostic)
		return execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: diagnostic}, nil
	}
	if record.recovered {
		record.mu.Lock()
		record.exitCode = -1
		record.mu.Unlock()
		supervisor.markTerminal(record, diagnostic)
		record.doneOnce.Do(func() { close(record.done) })
		return supervisor.awaitTerminalPersistence(ctx, record, diagnostic), nil
	}

	return supervisor.awaitTerminalPersistence(ctx, record, diagnostic), nil
}

func (supervisor *Supervisor) awaitTerminalPersistence(ctx context.Context, record *processRecord, diagnostic string) execution.StopOutcome {
	wait := supervisor.timers.NewTimer(supervisor.config.ReapWait)
	defer wait.Stop()
	select {
	case <-record.done:
		record.mu.Lock()
		confirmed := record.terminalProof && record.terminalPersisted && (record.child == nil || record.leaderReaped)
		persistErr := record.persistErr
		record.mu.Unlock()
		if confirmed {
			return execution.StopOutcome{Kind: execution.StopConfirmed}
		}
		if persistErr != nil {
			diagnostic = joinDiagnostic(diagnostic, "persist terminal process proof: "+persistErr.Error())
		} else {
			diagnostic = joinDiagnostic(diagnostic, "owned leader reap or terminal persistence was not proved")
		}
	case <-ctx.Done():
		diagnostic = joinDiagnostic(diagnostic, "await owned leader reap and terminal persistence: "+ctx.Err().Error())
	case <-wait.C():
		diagnostic = joinDiagnostic(diagnostic, "configured owned leader reap and terminal persistence ceiling exceeded")
	}
	return execution.StopOutcome{Kind: execution.StopUncertain, Diagnostic: diagnostic}
}

func (supervisor *Supervisor) terminateAndProve(ctx context.Context, record *processRecord, hasLiveOwnership bool) (bool, string) {
	record.terminationMu.Lock()
	defer record.terminationMu.Unlock()

	if record.isTerminal() {
		return true, "process group already proved dead"
	}
	alive, err := supervisor.platform.GroupAlive(record.pgid)
	if err != nil {
		return false, "probe process group before termination: " + err.Error()
	}
	if !alive {
		return true, "process group is absent"
	}

	if safe, proof := supervisor.proveLeaderOwnershipForSignal(record); !safe {
		prefix := "process leader identity cannot be freshly proved"
		if !hasLiveOwnership {
			prefix = "recovered process leader identity cannot be freshly proved"
		}
		return false, prefix + "; no signal sent: " + proof
	}

	diagnostic := ""
	if err := supervisor.platform.SignalGroup(record.pgid, syscall.SIGTERM); err != nil && !errors.Is(err, os.ErrProcessDone) {
		diagnostic = joinDiagnostic(diagnostic, "signal process group TERM: "+err.Error())
	}
	if gone, waitErr := supervisor.platform.WaitGroupGone(ctx, record.pgid, supervisor.config.TermWait, supervisor.config.ProbeInterval); gone {
		return true, joinDiagnostic(diagnostic, "process group exited after TERM")
	} else if waitErr != nil && ctx.Err() == nil {
		diagnostic = joinDiagnostic(diagnostic, "prove process group after TERM: "+waitErr.Error())
	}
	// TERM may reap the leader while leaving descendants. Re-prove the exact
	// leader immediately before KILL; otherwise numeric PGID reuse is ambiguous.
	if safe, proof := supervisor.proveLeaderOwnershipForSignal(record); !safe {
		return false, joinDiagnostic(diagnostic, "KILL withheld because fresh owned leader proof is unavailable: "+proof)
	}
	if err := supervisor.platform.SignalGroup(record.pgid, syscall.SIGKILL); err != nil && !errors.Is(err, os.ErrProcessDone) {
		diagnostic = joinDiagnostic(diagnostic, "signal process group KILL: "+err.Error())
	}
	if gone, waitErr := supervisor.platform.WaitGroupGone(context.Background(), record.pgid, supervisor.config.KillWait, supervisor.config.ProbeInterval); gone {
		return true, joinDiagnostic(diagnostic, "process group exited after KILL")
	} else if waitErr != nil {
		diagnostic = joinDiagnostic(diagnostic, "prove process group after KILL: "+waitErr.Error())
	}
	return false, joinDiagnostic(diagnostic, "process group death could not be proved")
}

func (supervisor *Supervisor) proveLeaderOwnershipForSignal(record *processRecord) (bool, string) {
	record.mu.Lock()
	reaped := record.leaderReaped
	record.mu.Unlock()
	if reaped {
		return false, "owned leader has already been reaped"
	}
	observation, err := supervisor.platform.Observe(record.pid)
	if err != nil {
		return false, err.Error()
	}
	if !exactObservation(record, observation) {
		return false, "PID/PGID/birth identity mismatch"
	}
	return true, "exact PID/PGID/birth identity matched"
}

func (supervisor *Supervisor) Reconcile(ctx context.Context, identity execution.ProcessIdentity) (execution.ReconcileOutcome, error) {
	supervisor.mu.Lock()
	record := supervisor.byIdentity[identity.Value]
	supervisor.mu.Unlock()
	if record == nil || !identity.Valid() {
		return uncertain("process identity is not owned by this trusted-host state"), nil
	}
	return supervisor.reconcileRecord(ctx, record), nil
}

func (supervisor *Supervisor) ReconcileLaunch(ctx context.Context, launch execution.LaunchToken) (execution.ReconcileOutcome, error) {
	supervisor.mu.Lock()
	record := supervisor.byLaunch[launch.Value]
	supervisor.mu.Unlock()
	if record == nil || !launch.Valid() {
		return uncertain("launch token is not owned by this trusted-host state"), nil
	}
	return supervisor.reconcileRecord(ctx, record), nil
}

func (supervisor *Supervisor) reconcileRecord(ctx context.Context, record *processRecord) execution.ReconcileOutcome {
	if ctx == nil {
		ctx = context.Background()
	}
	if record.isTerminal() {
		return execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: record.handle, LaunchToken: record.launch}
	}
	if !record.identity.Valid() || record.pid <= 0 || record.pgid <= 0 || record.birth == "" {
		return execution.ReconcileOutcome{Kind: execution.ReconcileUncertain, Handle: record.handle, LaunchToken: record.launch, Diagnostic: "durable exact PID/PGID/birth identity is unavailable"}
	}
	observation, err := supervisor.platform.Observe(record.pid)
	if err == nil && exactObservation(record, observation) {
		return execution.ReconcileOutcome{Kind: execution.ReconcileAlive, Handle: record.handle, LaunchToken: record.launch}
	}
	if err == nil {
		return execution.ReconcileOutcome{Kind: execution.ReconcileUncertain, Handle: record.handle, LaunchToken: record.launch, Diagnostic: "PID/PGID/birth identity mismatch; refusing to associate current process"}
	}
	if !errors.Is(err, os.ErrProcessDone) {
		return execution.ReconcileOutcome{Kind: execution.ReconcileUncertain, Handle: record.handle, LaunchToken: record.launch, Diagnostic: "inspect recorded process identity: " + err.Error()}
	}
	// EOF-driven natural exit can make the leader disappear before the owned
	// Wait goroutine has proved group death and persisted the terminal record.
	// Let that existing bounded proof finish; absence alone never proves death
	// and does not authorize a signal to a potentially reused process group.
	if record.child != nil {
		proof := supervisor.awaitTerminalPersistence(ctx, record, "recorded leader is absent; awaiting owned terminal proof")
		if proof.Kind == execution.StopConfirmed {
			return execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: record.handle, LaunchToken: record.launch}
		}
		return execution.ReconcileOutcome{Kind: execution.ReconcileUncertain, Handle: record.handle, LaunchToken: record.launch, Diagnostic: proof.Diagnostic}
	}
	groupAlive, groupErr := supervisor.platform.GroupAlive(record.pgid)
	if groupErr != nil || groupAlive {
		diagnostic := "recorded leader is absent but process-group death is not proved"
		if groupErr != nil {
			diagnostic += ": " + groupErr.Error()
		}
		return execution.ReconcileOutcome{Kind: execution.ReconcileUncertain, Handle: record.handle, LaunchToken: record.launch, Diagnostic: diagnostic}
	}
	record.mu.Lock()
	record.exitCode = -1
	record.mu.Unlock()
	supervisor.markTerminal(record, "restart reconciliation proved process group absent")
	if !record.isTerminal() {
		return execution.ReconcileOutcome{Kind: execution.ReconcileUncertain, Handle: record.handle, LaunchToken: record.launch, Diagnostic: "process group is absent but terminal proof could not be persisted"}
	}
	record.doneOnce.Do(func() { close(record.done) })
	return execution.ReconcileOutcome{Kind: execution.ReconcileDead, Handle: record.handle, LaunchToken: record.launch}
}

func exactObservation(record *processRecord, observation processObservation) bool {
	return observation.PID == record.pid && observation.PGID == record.pgid && observation.Birth == record.birth
}

func (supervisor *Supervisor) Read(_ context.Context, handle execution.RuntimeHandle, kind execution.StreamKind, offset int64, limit int) (execution.StreamChunk, error) {
	record, err := supervisor.recordForHandle(handle)
	if err != nil {
		return execution.StreamChunk{}, err
	}
	path, ok := record.streamPath(kind)
	if !ok || offset < 0 || limit <= 0 {
		return execution.StreamChunk{}, ErrInvalidStreamRead
	}
	data, next, visible, err := readBoundedFile(path, offset, limit, supervisor.config.StreamCapacity)
	if err != nil {
		return execution.StreamChunk{}, ErrInvalidStreamRead
	}
	return execution.StreamChunk{Data: data, NextOffset: next, EOF: record.isTerminal() && next == visible}, nil
}

// CloseStdin closes the child's stdin pipe, letting a real Pi RPC process
// observe EOF and shut down cleanly after its terminal signal. It is a no-op
// when the child is no longer running or has no held stdin.
func (supervisor *Supervisor) CloseStdin(_ context.Context, handle execution.RuntimeHandle) error {
	record, err := supervisor.recordForHandle(handle)
	if err != nil {
		return err
	}
	record.mu.Lock()
	child := record.child
	record.mu.Unlock()
	if child == nil {
		return nil
	}
	return child.CloseStdin()
}

func (supervisor *Supervisor) Drain(_ context.Context, handle execution.RuntimeHandle, offsets execution.StreamOffsets, limit int) (execution.DrainOutcome, error) {
	record, err := supervisor.recordForHandle(handle)
	if err != nil {
		return execution.DrainOutcome{}, err
	}
	if limit <= 0 {
		return execution.DrainOutcome{}, ErrInvalidDrain
	}
	for kind, offset := range offsets {
		if (kind != execution.StreamStdout && kind != execution.StreamStderr) || offset < 0 {
			return execution.DrainOutcome{}, ErrInvalidDrain
		}
	}
	outcome := execution.DrainOutcome{
		Chunks: make(map[execution.StreamKind][]byte, 2), Offsets: make(execution.StreamOffsets, 2),
		EOF: make(map[execution.StreamKind]bool, 2), TerminalFiles: cloneTerminal(record.terminal),
	}
	remaining := limit
	for _, kind := range []execution.StreamKind{execution.StreamStdout, execution.StreamStderr} {
		path, _ := record.streamPath(kind)
		data, next, visible, readErr := readBoundedFile(path, offsets[kind], remaining, supervisor.config.StreamCapacity)
		if readErr != nil {
			return execution.DrainOutcome{}, ErrInvalidDrain
		}
		outcome.Chunks[kind], outcome.Offsets[kind] = data, next
		remaining -= len(data)
		outcome.EOF[kind] = record.isTerminal() && next == visible
		if outcome.EOF[kind] {
			record.mu.Lock()
			record.drained[kind] = true
			record.mu.Unlock()
		}
	}
	record.mu.Lock()
	if record.terminalProof {
		outcome.TerminalFiles.ExitCode = record.exitCode
		if record.outputLimitExceeded {
			outcome.TerminalFiles.TerminationCause = execution.TerminationOutputLimit
		}
	}
	record.mu.Unlock()
	return outcome, nil
}

func (supervisor *Supervisor) Finalize(_ context.Context, handle execution.RuntimeHandle, _ execution.RetentionPolicy) error {
	record, err := supervisor.recordForHandle(handle)
	if err != nil {
		return err
	}
	record.mu.Lock()
	ready := record.terminalProof && record.terminalPersisted && record.persistErr == nil && record.drained[execution.StreamStdout] && record.drained[execution.StreamStderr]
	if record.child != nil {
		ready = ready && record.leaderReaped
	}
	record.mu.Unlock()
	if !ready {
		return ErrFinalizeNotReady
	}
	// A terminal manifest is a historical proof. Do not reinterpret a later PGID
	// reuse as this Attempt, and never signal during Finalize.
	if err := verifyOwnedRecord(record, supervisor.config.RuntimeRoot); err != nil {
		return fmt.Errorf("%w: %v", ErrFinalizeNotReady, err)
	}
	if err := os.RemoveAll(record.root); err != nil {
		return fmt.Errorf("remove owned trusted-host attempt state: %w", err)
	}
	if _, err := os.Lstat(record.root); !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("prove owned trusted-host attempt removal: %w", err)
	}
	if directory, err := os.Open(supervisor.config.RuntimeRoot); err != nil {
		return fmt.Errorf("open trusted-host runtime root after cleanup: %w", err)
	} else {
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if err := errors.Join(syncErr, closeErr); err != nil {
			return fmt.Errorf("persist trusted-host cleanup: %w", err)
		}
	}
	supervisor.mu.Lock()
	delete(supervisor.byHandle, record.handle.Value)
	if record.identity.Valid() {
		delete(supervisor.byIdentity, record.identity.Value)
	}
	delete(supervisor.byLaunch, record.launch.Value)
	supervisor.mu.Unlock()
	return nil
}

func (supervisor *Supervisor) register(record *processRecord) {
	supervisor.mu.Lock()
	supervisor.byHandle[record.handle.Value] = record
	if record.identity.Valid() {
		supervisor.byIdentity[record.identity.Value] = record
	}
	supervisor.byLaunch[record.launch.Value] = record
	supervisor.mu.Unlock()
}

func (supervisor *Supervisor) recordForHandle(handle execution.RuntimeHandle) (*processRecord, error) {
	if !handle.Valid() {
		return nil, ErrUnknownHandle
	}
	supervisor.mu.Lock()
	record := supervisor.byHandle[handle.Value]
	supervisor.mu.Unlock()
	if record == nil {
		return nil, ErrUnknownHandle
	}
	return record, nil
}

func (record *processRecord) isTerminal() bool {
	record.mu.Lock()
	defer record.mu.Unlock()
	return record.terminalProof
}

func (record *processRecord) streamPath(kind execution.StreamKind) (string, bool) {
	switch kind {
	case execution.StreamStdout:
		return record.stdoutPath, true
	case execution.StreamStderr:
		return record.stderrPath, true
	default:
		return "", false
	}
}

func noChild(launch execution.LaunchToken, diagnostic string) execution.StartOutcome {
	return execution.StartOutcome{Kind: execution.StartProvenNoChild, LaunchToken: launch, Diagnostic: diagnostic}
}

func reconciliationRequired(record *processRecord, diagnostic string) execution.StartOutcome {
	return execution.StartOutcome{Kind: execution.StartReconciliationRequired, Identity: record.identity, LaunchToken: record.launch, Diagnostic: diagnostic}
}

func uncertain(diagnostic string) execution.ReconcileOutcome {
	return execution.ReconcileOutcome{Kind: execution.ReconcileUncertain, Diagnostic: diagnostic}
}

func joinDiagnostic(existing, next string) string {
	existing, next = strings.TrimSpace(existing), strings.TrimSpace(next)
	if existing == "" {
		return next
	}
	if next == "" {
		return existing
	}
	return existing + "; " + next
}

func safeNotify(sink execution.RuntimeSink, kind execution.StreamKind, offset int64) {
	if sink == nil {
		return
	}
	defer func() { _ = recover() }()
	sink.Notify(kind, offset)
}

func safeExited(sink execution.RuntimeSink) {
	if sink == nil {
		return
	}
	defer func() { _ = recover() }()
	sink.Exited()
}

func cloneTerminal(files execution.TerminalFiles) execution.TerminalFiles {
	clone := execution.TerminalFiles{ExitCode: files.ExitCode, Paths: make(map[string]string, len(files.Paths))}
	for key, value := range files.Paths {
		clone.Paths[key] = value
	}
	return clone
}

var _ execution.ProcessSupervisor = (*Supervisor)(nil)
