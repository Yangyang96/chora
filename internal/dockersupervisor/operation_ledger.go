package dockersupervisor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	OperationPhaseSetup    OperationPhase = "setup"
	OperationPhaseAttempt  OperationPhase = "attempt"
	OperationPhaseRetry    OperationPhase = "retry"
	OperationPhaseRestart  OperationPhase = "restart"
	OperationPhaseRecovery OperationPhase = "recovery"
	OperationPhaseVerifier OperationPhase = "verifier"

	maxOperationRecords = 4096
	maxOperationBytes   = 2 << 20
	zeroOperationDigest = "0000000000000000000000000000000000000000000000000000000000000000"
)

type OperationPhase string

type operationPhaseContextKey struct{}
type operationSafeTargetContextKey struct{}
type operationLedgerBoundaryContextKey struct{}

// WithOperationPhase explicitly scopes Docker operations without changing the
// long-standing CommandRunner interface.
func WithOperationPhase(ctx context.Context, phase OperationPhase) context.Context {
	return context.WithValue(ctx, operationPhaseContextKey{}, phase)
}

// WithOperationSafeTargetSHA256 binds a redacted, content-addressed target to
// the next audited image operation. Setup uses it to record the exact archive
// byte digest without persisting a local pathname.
func WithOperationSafeTargetSHA256(ctx context.Context, digest string) context.Context {
	return context.WithValue(ctx, operationSafeTargetContextKey{}, digest)
}

type OperationRecord struct {
	Sequence         int     `json:"sequence"`
	Phase            string  `json:"phase"`
	Subsystem        string  `json:"subsystem"`
	Invocation       string  `json:"invocation"`
	OperationClass   string  `json:"operationClass"`
	SafeTargetSHA256 *string `json:"safeTargetSha256"`
	Result           string  `json:"result"`
	PreviousDigest   string  `json:"previousDigest"`
	Digest           string  `json:"digest"`
}

type PhaseImageOperationCounts struct {
	Build int `json:"build"`
	Pull  int `json:"pull"`
	Load  int `json:"load"`
}

type OperationLedgerSnapshot struct {
	CommandAudit         []OperationRecord                    `json:"commandAudit"`
	AuditRecordCount     int                                  `json:"auditRecordCount"`
	AuditFinalDigest     string                               `json:"auditFinalDigest"`
	AuditSealed          bool                                 `json:"auditSealed"`
	PhaseOperationCounts map[string]PhaseImageOperationCounts `json:"phaseOperationCounts"`
}

type operationLedgerState struct {
	CommandAudit     []OperationRecord `json:"commandAudit"`
	AuditRecordCount int               `json:"auditRecordCount"`
	AuditFinalDigest string            `json:"auditFinalDigest"`
	AuditSealed      bool              `json:"auditSealed"`
}

// OperationLedger is one durable, exclusive command audit over an exact
// Runner. Reservations are pessimistically durable as cancelled before the
// underlying command can execute, so a crash cannot manufacture success.
type OperationLedger struct {
	mu       sync.Mutex
	boundary sync.RWMutex
	runner   CommandRunner
	rootPath string
	rootInfo os.FileInfo
	dirPath  string
	dirInfo  os.FileInfo
	filePath string
	fileInfo os.FileInfo
	fileHash [sha256.Size]byte
	lockPath string
	lockInfo os.FileInfo
	lock     *os.File
	state    operationLedgerState
	closed   bool
	active   map[int]struct{}
}

func OpenOperationLedger(runner CommandRunner, stateRoot, generationID string) (*OperationLedger, error) {
	if runner == nil || !filepath.IsAbs(stateRoot) || filepath.Clean(stateRoot) != stateRoot ||
		!validLedgerGeneration(generationID) {
		return nil, errors.New("invalid Docker operation ledger authority")
	}
	resolvedParent, err := filepath.EvalSymlinks(filepath.Dir(stateRoot))
	if err != nil || resolvedParent != filepath.Dir(stateRoot) {
		return nil, errors.New("Docker operation ledger state root has a symbolic-link ancestor")
	}
	if err := ensureOwnerDirectory(stateRoot); err != nil {
		return nil, err
	}
	resolvedRoot, err := filepath.EvalSymlinks(stateRoot)
	if err != nil || !filepath.IsAbs(resolvedRoot) || resolvedRoot != stateRoot {
		return nil, errors.New("resolve Docker operation ledger state root failed")
	}
	directory := filepath.Join(stateRoot, "docker-operation-ledgers")
	if err := ensureOwnerDirectory(directory); err != nil {
		return nil, err
	}
	identity := sha256.Sum256([]byte(generationID))
	base := hex.EncodeToString(identity[:])
	lockPath := filepath.Join(directory, base+".lock")
	fd, err := unix.Open(lockPath, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, errors.New("open Docker operation ledger lock failed")
	}
	lock := os.NewFile(uintptr(fd), lockPath)
	if lock == nil {
		_ = unix.Close(fd)
		return nil, errors.New("open Docker operation ledger lock failed")
	}
	if err := validateOwnerFile(lock, true); err != nil {
		_ = lock.Close()
		return nil, err
	}
	lockInfo, _ := lock.Stat()
	if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lock.Close()
		return nil, errors.New("Docker operation ledger is already open")
	}
	rootInfo, _ := os.Lstat(stateRoot)
	dirInfo, _ := os.Lstat(directory)
	ledger := &OperationLedger{runner: runner, rootPath: stateRoot, rootInfo: rootInfo, dirPath: directory, dirInfo: dirInfo, filePath: filepath.Join(directory, base+".json"), lockPath: lockPath, lockInfo: lockInfo, lock: lock, active: map[int]struct{}{}}
	if err := ledger.load(); err != nil {
		_ = ledger.Close()
		return nil, err
	}
	return ledger, nil
}

func validLedgerGeneration(value string) bool {
	return validText(value, 128) && value != "." && value != ".." && !strings.ContainsAny(value, `/\\`)
}

func ensureOwnerDirectory(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return errors.New("create Docker operation ledger directory failed")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o077 != 0 || !ownerOnly(info) {
		return errors.New("Docker operation ledger directory is unsafe")
	}
	return nil
}

func ownerOnly(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Uid == uint32(os.Getuid())
}

func validateOwnerFile(file *os.File, requireSingleLink bool) error {
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !ownerOnly(info) {
		return errors.New("Docker operation ledger file is unsafe")
	}
	if requireSingleLink {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || stat.Nlink != 1 {
			return errors.New("Docker operation ledger hard links are forbidden")
		}
	}
	return nil
}

func (ledger *OperationLedger) load() error {
	file, err := os.OpenFile(ledger.filePath, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if errors.Is(err, os.ErrNotExist) {
		ledger.state = operationLedgerState{CommandAudit: []OperationRecord{}, AuditFinalDigest: zeroOperationDigest}
		return ledger.persistLocked()
	}
	if err != nil {
		return errors.New("open Docker operation ledger failed")
	}
	defer file.Close()
	if err := validateOwnerFile(file, true); err != nil {
		return err
	}
	ledger.fileInfo, _ = file.Stat()
	if ledger.fileInfo.Size() <= 0 || ledger.fileInfo.Size() > maxOperationBytes {
		return errors.New("Docker operation ledger size is invalid")
	}
	ledger.fileHash, err = operationLedgerFileHash(file)
	if err != nil {
		return err
	}
	if _, err := file.Seek(0, 0); err != nil {
		return errors.New("rewind Docker operation ledger failed")
	}
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&ledger.state); err != nil {
		return errors.New("decode Docker operation ledger failed")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("decode Docker operation ledger trailing data")
	}
	wantMode := os.FileMode(0o600)
	if ledger.state.AuditSealed {
		wantMode = 0o400
	}
	if ledger.fileInfo.Mode().Perm() != wantMode {
		return errors.New("Docker operation ledger mutability does not match seal state")
	}
	return ledger.validateLocked()
}

type operationPhaseRunner struct {
	runner CommandRunner
	phase  OperationPhase
}

// RunnerForOperationPhase scopes every operation from a subsystem while
// preserving CommandRunner compatibility for existing adapters.
func RunnerForOperationPhase(runner CommandRunner, phase OperationPhase) CommandRunner {
	if runner == nil || !validOperationPhase(phase) {
		return nil
	}
	return operationPhaseRunner{runner: runner, phase: phase}
}

func (runner operationPhaseRunner) Run(ctx context.Context, command Command) (CommandResult, error) {
	if phase, ok := ctx.Value(operationPhaseContextKey{}).(OperationPhase); !ok || !validOperationPhase(phase) {
		ctx = WithOperationPhase(ctx, runner.phase)
	}
	return runner.runner.Run(ctx, command)
}

func (runner operationPhaseRunner) Start(ctx context.Context, command Command) (Process, error) {
	if phase, ok := ctx.Value(operationPhaseContextKey{}).(OperationPhase); !ok || !validOperationPhase(phase) {
		ctx = WithOperationPhase(ctx, runner.phase)
	}
	return runner.runner.Start(ctx, command)
}

func (ledger *OperationLedger) Run(ctx context.Context, command Command) (CommandResult, error) {
	sequence, err := ledger.reserve(ctx, "run", command)
	if err != nil {
		return CommandResult{ExitCode: -1}, err
	}
	result, runErr := ledger.runner.Run(ctx, command)
	settleErr := ledger.settle(sequence, operationResult(ctx, result.ExitCode, runErr))
	return result, errors.Join(runErr, settleErr)
}

func (ledger *OperationLedger) Start(ctx context.Context, command Command) (Process, error) {
	sequence, err := ledger.reserve(ctx, "start", command)
	if err != nil {
		return nil, err
	}
	process, startErr := ledger.runner.Start(ctx, command)
	if startErr != nil && process != nil {
		killErr := process.Kill()
		exitCode, waitErr := process.Wait()
		settleErr := ledger.settle(sequence, operationResult(ctx, exitCode, startErr))
		return nil, errors.Join(startErr, killErr, waitErr, settleErr)
	}
	if startErr != nil || process == nil {
		settleErr := ledger.settle(sequence, operationResult(ctx, -1, startErr))
		if startErr == nil {
			startErr = errors.New("Docker Runner returned no process")
		}
		return nil, errors.Join(startErr, settleErr)
	}
	return &auditedProcess{process: process, ledger: ledger, sequence: sequence, ctx: ctx}, nil
}

func (ledger *OperationLedger) reserve(ctx context.Context, invocation string, command Command) (int, error) {
	// A publication boundary blocks new reservations while taking an exact
	// settled projection. Only the boundary itself may issue its independently
	// observed read-only commands through the private context authority.
	if authority, _ := ctx.Value(operationLedgerBoundaryContextKey{}).(*OperationLedger); authority != ledger {
		ledger.boundary.RLock()
		defer ledger.boundary.RUnlock()
	}
	phase, ok := ctx.Value(operationPhaseContextKey{}).(OperationPhase)
	if !ok || !validOperationPhase(phase) {
		return 0, errors.New("Docker operation phase is required")
	}
	class, target := classifyDockerOperation(command.Args)
	switch class {
	case "build", "pull":
		return 0, errors.New("Docker build and pull are forbidden in every operation phase")
	case "load":
		digest, bound := ctx.Value(operationSafeTargetContextKey{}).(string)
		if phase != OperationPhaseSetup || invocation != "run" || command.Stdin == nil || !bound || !validCanonicalDigest(digest) {
			return 0, errors.New("Docker image load requires Setup and an exact archive digest binding")
		}
		target = &digest
	}
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed || ledger.state.AuditSealed || len(ledger.state.CommandAudit) >= maxOperationRecords {
		return 0, errors.New("Docker operation ledger is closed, sealed, or full")
	}
	record := OperationRecord{Sequence: len(ledger.state.CommandAudit) + 1, Phase: string(phase), Subsystem: phaseSubsystem(phase), Invocation: invocation, OperationClass: class, SafeTargetSHA256: target, Result: "cancelled"}
	ledger.state.CommandAudit = append(ledger.state.CommandAudit, record)
	ledger.active[record.Sequence] = struct{}{}
	ledger.rechainLocked()
	if err := ledger.persistLocked(); err != nil {
		delete(ledger.active, record.Sequence)
		ledger.state.CommandAudit = ledger.state.CommandAudit[:len(ledger.state.CommandAudit)-1]
		ledger.rechainLocked()
		return 0, err
	}
	return record.Sequence, nil
}

func (ledger *OperationLedger) settle(sequence int, result string) error {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed {
		return errors.New("Docker operation ledger closed before settlement")
	}
	if _, ok := ledger.active[sequence]; !ok {
		return nil
	}
	delete(ledger.active, sequence)
	ledger.state.CommandAudit[sequence-1].Result = result
	ledger.rechainLocked()
	if err := ledger.persistLocked(); err != nil {
		ledger.state.CommandAudit[sequence-1].Result = "cancelled"
		ledger.rechainLocked()
		return err
	}
	return nil
}

func operationResult(ctx context.Context, exitCode int, err error) string {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "cancelled"
	}
	if err == nil && exitCode == 0 {
		return "succeeded"
	}
	return "failed"
}

type auditedProcess struct {
	process  Process
	ledger   *OperationLedger
	sequence int
	ctx      context.Context
	once     sync.Once
	waitCode int
	waitErr  error
}

func (process *auditedProcess) Write(data []byte) (int, error) { return process.process.Write(data) }
func (process *auditedProcess) Close() error                   { return process.process.Close() }
func (process *auditedProcess) Kill() error                    { return process.process.Kill() }
func (process *auditedProcess) Wait() (int, error) {
	process.once.Do(func() {
		process.waitCode, process.waitErr = process.process.Wait()
		process.waitErr = errors.Join(process.waitErr, process.ledger.settle(process.sequence, operationResult(process.ctx, process.waitCode, process.waitErr)))
	})
	return process.waitCode, process.waitErr
}

func (ledger *OperationLedger) Snapshot() (OperationLedgerSnapshot, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed || len(ledger.active) != 0 {
		return OperationLedgerSnapshot{}, errors.New("Docker operation ledger has unsettled operations")
	}
	if err := ledger.validateLocked(); err != nil {
		return OperationLedgerSnapshot{}, err
	}
	if err := ledger.validateStorageLocked(); err != nil {
		return OperationLedgerSnapshot{}, err
	}
	return ledger.snapshotLocked(), nil
}

func (ledger *OperationLedger) Seal() (OperationLedgerSnapshot, error) {
	ledger.mu.Lock()
	defer ledger.mu.Unlock()
	if ledger.closed || len(ledger.active) != 0 {
		return OperationLedgerSnapshot{}, errors.New("Docker operation ledger has unsettled operations")
	}
	if ledger.state.AuditSealed {
		if err := ledger.validateLocked(); err != nil {
			return OperationLedgerSnapshot{}, err
		}
		if err := ledger.validateStorageLocked(); err != nil {
			return OperationLedgerSnapshot{}, err
		}
		return ledger.snapshotLocked(), nil
	}
	ledger.state.AuditSealed = true
	ledger.rechainLocked()
	if err := ledger.persistLocked(); err != nil {
		return OperationLedgerSnapshot{}, err
	}
	return ledger.snapshotLocked(), nil
}

func (ledger *OperationLedger) snapshotLocked() OperationLedgerSnapshot {
	counts := map[string]PhaseImageOperationCounts{}
	for _, phase := range []OperationPhase{OperationPhaseSetup, OperationPhaseAttempt, OperationPhaseRetry, OperationPhaseRestart, OperationPhaseRecovery, OperationPhaseVerifier} {
		counts[string(phase)] = PhaseImageOperationCounts{}
	}
	for _, record := range ledger.state.CommandAudit {
		value := counts[record.Phase]
		switch record.OperationClass {
		case "build":
			value.Build++
		case "pull":
			value.Pull++
		case "load":
			value.Load++
		}
		counts[record.Phase] = value
	}
	return OperationLedgerSnapshot{CommandAudit: cloneOperationRecords(ledger.state.CommandAudit), AuditRecordCount: ledger.state.AuditRecordCount, AuditFinalDigest: ledger.state.AuditFinalDigest, AuditSealed: ledger.state.AuditSealed, PhaseOperationCounts: counts}
}

func cloneOperationRecords(records []OperationRecord) []OperationRecord {
	cloned := slices.Clone(records)
	for index := range cloned {
		if records[index].SafeTargetSHA256 != nil {
			value := *records[index].SafeTargetSHA256
			cloned[index].SafeTargetSHA256 = &value
		}
	}
	return cloned
}

func (ledger *OperationLedger) Close() error {
	ledger.mu.Lock()
	if ledger.closed {
		ledger.mu.Unlock()
		return nil
	}
	if len(ledger.active) != 0 {
		ledger.mu.Unlock()
		return errors.New("Docker operation ledger has unsettled operations")
	}
	ledger.closed = true
	ledger.active = map[int]struct{}{}
	lock := ledger.lock
	ledger.lock = nil
	ledger.mu.Unlock()
	if lock == nil {
		return nil
	}
	return errors.Join(unix.Flock(int(lock.Fd()), unix.LOCK_UN), lock.Close())
}

func (ledger *OperationLedger) rechainLocked() {
	previous := zeroOperationDigest
	for index := range ledger.state.CommandAudit {
		record := &ledger.state.CommandAudit[index]
		record.Sequence = index + 1
		record.PreviousDigest = previous
		record.Digest = operationRecordDigest(*record)
		previous = record.Digest
	}
	ledger.state.AuditRecordCount = len(ledger.state.CommandAudit)
	ledger.state.AuditFinalDigest = previous
}

func operationRecordDigest(record OperationRecord) string {
	value := map[string]any{"invocation": record.Invocation, "operationClass": record.OperationClass, "phase": record.Phase, "previousDigest": record.PreviousDigest, "result": record.Result, "safeTargetSha256": record.SafeTargetSHA256, "sequence": record.Sequence, "subsystem": record.Subsystem}
	encoded, _ := json.Marshal(value)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func (ledger *OperationLedger) validateLocked() error {
	if len(ledger.state.CommandAudit) > maxOperationRecords || ledger.state.AuditRecordCount != len(ledger.state.CommandAudit) {
		return errors.New("Docker operation ledger metadata is invalid")
	}
	previous := zeroOperationDigest
	for index, record := range ledger.state.CommandAudit {
		if record.Sequence != index+1 || !validOperationPhase(OperationPhase(record.Phase)) || record.Subsystem != phaseSubsystem(OperationPhase(record.Phase)) ||
			(record.Invocation != "run" && record.Invocation != "start") || !validOperationClass(record.OperationClass) ||
			(record.Result != "succeeded" && record.Result != "failed" && record.Result != "cancelled") || record.PreviousDigest != previous || record.Digest != operationRecordDigest(record) {
			return errors.New("Docker operation ledger chain is invalid")
		}
		if slices.Contains([]string{"build", "pull", "load"}, record.OperationClass) && (record.SafeTargetSHA256 == nil || !validCanonicalDigest(*record.SafeTargetSHA256)) {
			return errors.New("Docker operation ledger target digest is invalid")
		}
		if record.OperationClass == "build" || record.OperationClass == "pull" ||
			record.OperationClass == "load" && (record.Phase != string(OperationPhaseSetup) || record.Invocation != "run") {
			return errors.New("Docker operation ledger contains a forbidden image acquisition")
		}
		if !slices.Contains([]string{"build", "pull", "load"}, record.OperationClass) && record.SafeTargetSHA256 != nil {
			return errors.New("Docker operation ledger non-image target is not redacted")
		}
		previous = record.Digest
	}
	if ledger.state.AuditFinalDigest != previous {
		return errors.New("Docker operation ledger final digest is invalid")
	}
	return nil
}

func (ledger *OperationLedger) persistLocked() error {
	ledger.rechainLocked()
	if err := ledger.validateStorageLocked(); err != nil {
		return err
	}
	encoded, err := json.Marshal(ledger.state)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > maxOperationBytes {
		return errors.New("Docker operation ledger exceeds its durable bound")
	}
	file, err := os.CreateTemp(ledger.dirPath, filepath.Base(ledger.filePath)+".tmp-")
	if err != nil {
		return errors.New("create Docker operation ledger update failed")
	}
	temporary := file.Name()
	cleanup := func() { _ = os.Remove(temporary) }
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		cleanup()
		return errors.New("secure Docker operation ledger update failed")
	}
	if err := validateOwnerFile(file, true); err != nil {
		_ = file.Close()
		cleanup()
		return err
	}
	if _, err = file.Write(encoded); err == nil && ledger.state.AuditSealed {
		err = file.Chmod(0o400)
	}
	if err == nil {
		err = file.Sync()
	}
	if closeErr := file.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		cleanup()
		return errors.New("persist Docker operation ledger failed")
	}
	if err = os.Rename(temporary, ledger.filePath); err != nil {
		cleanup()
		return errors.New("replace Docker operation ledger failed")
	}
	journal, err := os.OpenFile(ledger.filePath, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return errors.New("reopen Docker operation ledger failed")
	}
	if err := validateOwnerFile(journal, true); err != nil {
		_ = journal.Close()
		return err
	}
	ledger.fileInfo, _ = journal.Stat()
	ledger.fileHash = sha256.Sum256(encoded)
	wantMode := os.FileMode(0o600)
	if ledger.state.AuditSealed {
		wantMode = 0o400
	}
	if ledger.fileInfo.Mode().Perm() != wantMode {
		_ = journal.Close()
		return errors.New("Docker operation ledger mutability does not match seal state")
	}
	_ = journal.Close()
	directory, err := os.Open(filepath.Dir(ledger.filePath))
	if err != nil {
		return errors.New("sync Docker operation ledger directory failed")
	}
	err = directory.Sync()
	_ = directory.Close()
	return err
}

func (ledger *OperationLedger) validateStorageLocked() error {
	root, rootErr := os.Lstat(ledger.rootPath)
	directory, dirErr := os.Lstat(ledger.dirPath)
	resolvedRoot, resolveRootErr := filepath.EvalSymlinks(ledger.rootPath)
	resolvedDir, resolveDirErr := filepath.EvalSymlinks(ledger.dirPath)
	if rootErr != nil || dirErr != nil || resolveRootErr != nil || resolveDirErr != nil ||
		resolvedRoot != ledger.rootPath || resolvedDir != ledger.dirPath || !os.SameFile(root, ledger.rootInfo) || !os.SameFile(directory, ledger.dirInfo) ||
		!root.IsDir() || !directory.IsDir() || root.Mode().Perm()&0o077 != 0 || directory.Mode().Perm()&0o077 != 0 || !ownerOnly(root) || !ownerOnly(directory) {
		return errors.New("Docker operation ledger storage authority changed")
	}
	lockInfo, lockErr := os.Lstat(ledger.lockPath)
	if lockErr != nil {
		return errors.New("Docker operation ledger lock authority changed")
	}
	lockStat, lockStatOK := lockInfo.Sys().(*syscall.Stat_t)
	if lockInfo.Mode()&os.ModeSymlink != 0 || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm() != 0o600 ||
		!ownerOnly(lockInfo) || !os.SameFile(lockInfo, ledger.lockInfo) || !lockStatOK || lockStat.Nlink != 1 {
		return errors.New("Docker operation ledger lock authority changed")
	}
	info, err := os.Lstat(ledger.filePath)
	if ledger.fileInfo == nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return errors.New("Docker operation ledger appeared unexpectedly")
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !os.SameFile(info, ledger.fileInfo) || !info.Mode().IsRegular() || info.Mode().Perm()&0o077 != 0 || !ownerOnly(info) {
		return errors.New("Docker operation ledger identity changed")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Nlink != 1 {
		return errors.New("Docker operation ledger hard links are forbidden")
	}
	journal, openErr := os.OpenFile(ledger.filePath, os.O_RDONLY|unix.O_NOFOLLOW, 0)
	if openErr != nil {
		return errors.New("reopen Docker operation ledger for validation failed")
	}
	defer journal.Close()
	if err := validateOwnerFile(journal, true); err != nil {
		return err
	}
	openedInfo, _ := journal.Stat()
	if !os.SameFile(openedInfo, ledger.fileInfo) {
		return errors.New("Docker operation ledger identity changed during validation")
	}
	digest, hashErr := operationLedgerFileHash(journal)
	if hashErr != nil || digest != ledger.fileHash {
		return errors.New("Docker operation ledger content changed")
	}
	return nil
}

func operationLedgerFileHash(file *os.File) ([sha256.Size]byte, error) {
	hash := sha256.New()
	written, err := io.Copy(hash, io.LimitReader(file, maxOperationBytes+1))
	if err != nil || written <= 0 || written > maxOperationBytes {
		return [sha256.Size]byte{}, errors.New("hash Docker operation ledger failed")
	}
	var digest [sha256.Size]byte
	copy(digest[:], hash.Sum(nil))
	return digest, nil
}

func validOperationPhase(phase OperationPhase) bool {
	return slices.Contains([]OperationPhase{OperationPhaseSetup, OperationPhaseAttempt, OperationPhaseRetry, OperationPhaseRestart, OperationPhaseRecovery, OperationPhaseVerifier}, phase)
}

func phaseSubsystem(phase OperationPhase) string {
	if phase == OperationPhaseSetup {
		return "installation"
	}
	if phase == OperationPhaseVerifier {
		return "verifier"
	}
	return "managed"
}

func validOperationClass(class string) bool {
	return slices.Contains([]string{"build", "pull", "load", "inspect", "create", "start", "exec", "remove", "list", "context", "version", "other"}, class)
}

func classifyDockerOperation(args []string) (string, *string) {
	if len(args) == 0 {
		return "other", nil
	}
	verb, rest := args[0], args[1:]
	if verb == "image" || verb == "container" || verb == "network" {
		if len(rest) == 0 {
			return "other", nil
		}
		verb, rest = rest[0], rest[1:]
	}
	if (verb == "buildx" || verb == "builder") && len(rest) > 0 && rest[0] == "build" {
		verb, rest = "build", rest[1:]
	}
	class := "other"
	switch verb {
	case "build":
		class = "build"
	case "pull":
		class = "pull"
	case "load":
		class = "load"
	case "inspect":
		class = "inspect"
	case "create":
		class = "create"
	case "start":
		class = "start"
	case "exec":
		class = "exec"
	case "rm", "remove":
		class = "remove"
	case "ps", "ls":
		class = "list"
	case "context":
		class = "context"
	case "version", "info":
		class = "version"
	}
	if !slices.Contains([]string{"build", "pull", "load"}, class) {
		return class, nil
	}
	target := strings.Join(rest, "\x00")
	if class != "load" {
		if len(rest) == 0 {
			return class, nil
		}
		target = rest[len(rest)-1]
		if target == "" || strings.HasPrefix(target, "-") {
			return class, nil
		}
	} else {
		return class, nil
	}
	digest := sha256.Sum256([]byte(target))
	value := hex.EncodeToString(digest[:])
	return class, &value
}

var _ CommandRunner = (*OperationLedger)(nil)
