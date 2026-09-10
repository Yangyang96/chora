package trustedhost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	launcherModeKey     = "CHORA_TRUSTEDHOST_LAUNCHER"
	launcherModeValue   = "chora.trusted-host-gate.v1"
	launcherReceiptKey  = "CHORA_TRUSTEDHOST_GATE_RECEIPT"
	launcherNonceKey    = "CHORA_TRUSTEDHOST_GATE_NONCE"
	launcherDeadlineKey = "CHORA_TRUSTEDHOST_GATE_TIMEOUT_MS"
	gateReleaseByte     = byte(0xa7)
	launcherConfigLimit = 1 << 20
	gateProtocol        = "pipe-eof-fail-closed-v1"
)

type processSpec struct {
	ExecutableIdentity ExecutableIdentity
	Arguments          []string
	Environment        []string
	Directory          string
	Stdin              []byte
	Stdout             *os.File
	FilterPiProgress   bool
	Stderr             *os.File
	GateReceiptPath    string
	GateNonce          string
	GateWait           time.Duration
}

type launcherConfig struct {
	Executable  ExecutableIdentity `json:"executable"`
	Arguments   []string           `json:"arguments"`
	Environment []string           `json:"environment"`
}

type gateReceipt struct {
	Protocol string `json:"protocol"`
	Nonce    string `json:"nonce"`
	PID      int    `json:"pid"`
}

type childProcess interface {
	PID() int
	Wait() (int, error)
	Release() error
	AbortGate() error
	CloseStdin() error
}

type processObservation struct {
	PID   int
	PGID  int
	Birth string
}

type processPlatform interface {
	Start(processSpec) (childProcess, error)
	Observe(int) (processObservation, error)
	GroupAlive(int) (bool, error)
	SignalGroup(int, syscall.Signal) error
	WaitGroupGone(context.Context, int, time.Duration, time.Duration) (bool, error)
}

type osPlatform struct {
	launcher ExecutableIdentity
	err      error
}

// init turns the current Chora/test executable into a tiny pre-exec launcher.
// The target cannot execute until its parent durably records PID/PGID/birth and
// writes the one-byte release. Parent death closes the pipe and fails closed.
func init() {
	if os.Getenv(launcherModeKey) != launcherModeValue {
		return
	}
	if err := runGatedLauncher(); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "trusted-host gated launcher:", err)
		os.Exit(126)
	}
	os.Exit(127)
}

func runGatedLauncher() error {
	receiptPath := os.Getenv(launcherReceiptKey)
	nonce := os.Getenv(launcherNonceKey)
	deadlineMS, err := strconv.ParseInt(os.Getenv(launcherDeadlineKey), 10, 64)
	if err != nil || deadlineMS <= 0 || deadlineMS > int64(stopWaitCeiling/time.Millisecond) ||
		receiptPath == "" || !filepath.IsAbs(receiptPath) || filepath.Clean(receiptPath) != receiptPath || nonce == "" {
		return errors.New("invalid gate bootstrap")
	}
	gate := os.NewFile(uintptr(3), "trusted-host-gate")
	configFile := os.NewFile(uintptr(4), "trusted-host-config")
	if gate == nil || configFile == nil {
		return errors.New("missing gate descriptors")
	}
	defer gate.Close()
	defer configFile.Close()

	decoder := json.NewDecoder(io.LimitReader(configFile, launcherConfigLimit+1))
	decoder.DisallowUnknownFields()
	var config launcherConfig
	if err := decoder.Decode(&config); err != nil {
		return fmt.Errorf("decode launch contract: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errors.New("launch contract has trailing data")
	}
	if err := validateLauncherConfig(config); err != nil {
		return err
	}
	if err := writeGateReceipt(receiptPath, gateReceipt{Protocol: gateProtocol, Nonce: nonce, PID: os.Getpid()}); err != nil {
		return fmt.Errorf("persist gate receipt: %w", err)
	}

	release := make(chan error, 1)
	go func() {
		var value [1]byte
		_, readErr := io.ReadFull(gate, value[:])
		if readErr == nil && value[0] != gateReleaseByte {
			readErr = errors.New("invalid gate release")
		}
		release <- readErr
	}()
	select {
	case err := <-release:
		if err != nil {
			return fmt.Errorf("gate closed without release: %w", err)
		}
	case <-time.After(time.Duration(deadlineMS) * time.Millisecond):
		return errors.New("gate release deadline exceeded")
	}

	// Revalidate after release and immediately before replacement. exec keeps
	// this gated process's PID, process group, and birth identity unchanged.
	if err := validateExecutableIdentity(config.Executable); err != nil {
		return fmt.Errorf("target executable identity drift: %w", err)
	}
	argv := append([]string{config.Executable.ResolvedPath}, config.Arguments...)
	return syscall.Exec(config.Executable.ResolvedPath, argv, append([]string(nil), config.Environment...))
}

func validateLauncherConfig(config launcherConfig) error {
	if err := validateExecutableIdentity(config.Executable); err != nil {
		return fmt.Errorf("invalid target executable identity: %w", err)
	}
	if err := validateArguments(config.Arguments); err != nil {
		return err
	}
	return validateNativeEnvironment(config.Environment)
}

func writeGateReceipt(path string, receipt gateReceipt) error {
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	var writeErr error
	if _, err := file.Write(encoded); err != nil {
		writeErr = err
	}
	return errors.Join(writeErr, file.Sync(), file.Close())
}

func validateNativeEnvironment(environment []string) error {
	seen := make(map[string]struct{}, len(environment))
	for _, entry := range environment {
		separator := strings.IndexByte(entry, '=')
		if separator <= 0 {
			return errors.New("native environment entry is malformed")
		}
		key, value := entry[:separator], entry[separator+1:]
		if !environmentKey.MatchString(key) || strings.IndexByte(value, 0) >= 0 || forbiddenRuntimeEnvironment(key) {
			return errors.New("native environment contains a malformed or forbidden entry")
		}
		if _, exists := seen[key]; exists {
			return errors.New("native environment contains a duplicate key")
		}
		seen[key] = struct{}{}
	}
	return nil
}

func launcherEnvironment(receiptPath, nonce string, wait time.Duration) []string {
	return []string{
		launcherModeKey + "=" + launcherModeValue,
		launcherReceiptKey + "=" + receiptPath,
		launcherNonceKey + "=" + nonce,
		launcherDeadlineKey + "=" + strconv.FormatInt(wait.Milliseconds(), 10),
	}
}

func newOSPlatform() processPlatform {
	path, err := os.Executable()
	if err != nil {
		return osPlatform{err: err}
	}
	identity, err := InspectExecutable(path)
	return osPlatform{launcher: identity, err: err}
}

func (platform osPlatform) Start(spec processSpec) (childProcess, error) {
	if platform.err != nil {
		return nil, platform.err
	}
	if err := validateExecutableIdentity(platform.launcher); err != nil {
		return nil, fmt.Errorf("trusted-host launcher identity drift: %w", err)
	}
	if err := validateExecutableIdentity(spec.ExecutableIdentity); err != nil {
		return nil, fmt.Errorf("trusted-host target identity drift: %w", err)
	}
	if err := validateArguments(spec.Arguments); err != nil {
		return nil, err
	}
	if err := validateNativeEnvironment(spec.Environment); err != nil {
		return nil, err
	}
	gateRead, gateWrite, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	configRead, configWrite, err := os.Pipe()
	if err != nil {
		_ = gateRead.Close()
		_ = gateWrite.Close()
		return nil, err
	}
	closeAll := func() {
		_ = gateRead.Close()
		_ = gateWrite.Close()
		_ = configRead.Close()
		_ = configWrite.Close()
	}
	stdinRead, stdinWrite, err := os.Pipe()
	if err != nil {
		closeAll()
		return nil, err
	}
	command := exec.Command(platform.launcher.ResolvedPath)
	command.Dir = spec.Directory
	command.Env = launcherEnvironment(spec.GateReceiptPath, spec.GateNonce, spec.GateWait)
	command.Stdin = stdinRead
	command.Stdout = spec.Stdout
	command.Stderr = spec.Stderr
	var captureFile *os.File
	var capture *piProgressWriter
	if spec.FilterPiProgress {
		// Own a duplicate: Start closes its original descriptors immediately.
		fd, duplicateErr := syscall.Dup(int(spec.Stdout.Fd()))
		if duplicateErr != nil {
			closeAll()
			_ = stdinRead.Close()
			_ = stdinWrite.Close()
			return nil, duplicateErr
		}
		syscall.CloseOnExec(fd)
		captureFile = os.NewFile(uintptr(fd), "pi-stdout-capture")
		capture = &piProgressWriter{target: captureFile}
		command.Stdout = capture
		// Descendants must not hold a reaped leader's copy loop indefinitely.
		command.WaitDelay = time.Second
	}
	command.ExtraFiles = []*os.File{gateRead, configRead}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := command.Start(); err != nil {
		if captureFile != nil {
			_ = captureFile.Close()
		}
		closeAll()
		_ = stdinRead.Close()
		_ = stdinWrite.Close()
		return nil, err
	}
	_ = stdinRead.Close()
	// Deliver the prompt asynchronously and hold the write end open. Real Pi RPC
	// runs until its stdin reaches EOF, so the supervisor must keep the pipe open
	// and close it only after the terminal signal (agent_settled) is observed.
	if len(spec.Stdin) > 0 {
		go func() { _, _ = stdinWrite.Write(spec.Stdin) }()
	}
	_ = gateRead.Close()
	_ = configRead.Close()
	config := launcherConfig{Executable: spec.ExecutableIdentity, Arguments: append([]string(nil), spec.Arguments...), Environment: append([]string(nil), spec.Environment...)}
	encodeErr := json.NewEncoder(configWrite).Encode(config)
	closeErr := configWrite.Close()
	if err := errors.Join(encodeErr, closeErr); err != nil {
		_ = gateWrite.Close()
		_ = stdinWrite.Close()
		_, _ = (&osChild{command: command, capture: capture, captureFile: captureFile}).Wait()
		return nil, fmt.Errorf("send gated launcher config: %w", err)
	}
	return &osChild{command: command, gate: gateWrite, stdin: stdinWrite, capture: capture, captureFile: captureFile}, nil
}

type osChild struct {
	command     *exec.Cmd
	capture     *piProgressWriter
	captureFile *os.File
	gate        *os.File
	stdin       *os.File
	gateOnce    sync.Once
	gateErr     error
}

func (child *osChild) PID() int { return child.command.Process.Pid }

func (child *osChild) Wait() (int, error) {
	err := child.command.Wait()
	// Cmd.Wait joins its copy goroutine; flush a final partial record before
	// the supervisor publishes EOF or terminal proof.
	if child.capture != nil {
		err = errors.Join(err, child.capture.Flush(), child.captureFile.Close())
	}
	if child.command.ProcessState != nil {
		code := child.command.ProcessState.ExitCode()
		if code == 0 && err != nil {
			code = -1
		}
		return code, err
	}
	if err == nil {
		return 0, nil
	}
	return -1, err
}

func (child *osChild) Release() error {
	child.gateOnce.Do(func() {
		_, writeErr := child.gate.Write([]byte{gateReleaseByte})
		child.gateErr = errors.Join(writeErr, child.gate.Close())
	})
	return child.gateErr
}

func (child *osChild) AbortGate() error {
	child.gateOnce.Do(func() { child.gateErr = child.gate.Close() })
	return child.gateErr
}

func (child *osChild) CloseStdin() error {
	if child.stdin == nil {
		return nil
	}
	return child.stdin.Close()
}

func (osPlatform) Observe(pid int) (processObservation, error) {
	if pid <= 0 {
		return processObservation{}, os.ErrProcessDone
	}
	if err := unix.Kill(pid, 0); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return processObservation{}, os.ErrProcessDone
		}
		return processObservation{}, err
	}
	pgid, err := unix.Getpgid(pid)
	if err != nil {
		if errors.Is(err, unix.ESRCH) {
			return processObservation{}, os.ErrProcessDone
		}
		return processObservation{}, err
	}
	birth, err := processBirthIdentity(pid)
	if err != nil {
		return processObservation{}, err
	}
	return processObservation{PID: pid, PGID: pgid, Birth: birth}, nil
}

func (osPlatform) GroupAlive(pgid int) (bool, error) {
	if pgid <= 0 {
		return false, errors.New("invalid process group")
	}
	err := unix.Kill(-pgid, 0)
	switch {
	case err == nil, errors.Is(err, unix.EPERM):
		return true, nil
	case errors.Is(err, unix.ESRCH):
		return false, nil
	default:
		return false, err
	}
}

func (osPlatform) SignalGroup(pgid int, signal syscall.Signal) error {
	if pgid <= 0 || signal != syscall.SIGTERM && signal != syscall.SIGKILL {
		return errors.New("invalid trusted-host group signal")
	}
	if err := unix.Kill(-pgid, signal); err != nil {
		if errors.Is(err, unix.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	return nil
}

func (platform osPlatform) WaitGroupGone(ctx context.Context, pgid int, timeout, interval time.Duration) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 || interval <= 0 {
		return false, errors.New("invalid process-group death deadline")
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		alive, err := platform.GroupAlive(pgid)
		if err != nil || !alive {
			return !alive, err
		}
		select {
		case <-ctx.Done():
			return false, ctx.Err()
		case <-deadline.C:
			alive, err := platform.GroupAlive(pgid)
			return !alive, err
		case <-ticker.C:
		}
	}
}

type timer interface {
	C() <-chan time.Time
	Stop() bool
}

type timerFactory interface {
	NewTimer(time.Duration) timer
}

type realTimerFactory struct{}

func (realTimerFactory) NewTimer(duration time.Duration) timer {
	return realTimer{Timer: time.NewTimer(duration)}
}

type realTimer struct{ *time.Timer }

func (timer realTimer) C() <-chan time.Time { return timer.Timer.C }
