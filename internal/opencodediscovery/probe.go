// Package opencodediscovery owns bounded, no-model OpenCode metadata probes.
package opencodediscovery

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

var ErrProbe = errors.New("opencode_readiness_probe_failed")
var ErrCleanup = errors.New("opencode_probe_group_cleanup_unconfirmed")

const outputLimit = 32 * 1024

// Probe admits metadata arguments only. Native bootstrap receives disposable
// HOME/XDG/cwd/tmp directories. Selected scope is used only to exclude unsafe
// temporary bases. Authentication needs a separately qualified read-only path.
func Probe(ctx context.Context, executable, home string, environment map[string]string, args ...string) (string, error) {
	if !slices.Equal(args, []string{"--version"}) && !slices.Equal(args, []string{"run", "--help"}) {
		return "", ErrProbe
	}
	if ctx.Err() != nil || !filepath.IsAbs(executable) {
		return "", ErrProbe
	}
	base, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", ErrProbe
	}
	scopes := []string{home}
	for _, key := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME"} {
		if environment[key] != "" {
			scopes = append(scopes, environment[key])
		}
	}
	for _, selected := range scopes {
		scope, err := filepath.EvalSymlinks(selected)
		if err != nil || !filepath.IsAbs(scope) {
			return "", ErrProbe
		}
		relative, err := filepath.Rel(scope, base)
		if err != nil || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			return "", ErrProbe
		}
	}
	root, err := os.MkdirTemp(base, "chora-opencode-metadata-")
	if err != nil {
		return "", ErrProbe
	}
	defer os.RemoveAll(root)
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", ErrProbe
	}
	for _, name := range []string{"home", "data", "config", "cache", "state", "tmp", "work"} {
		if os.Mkdir(filepath.Join(root, name), 0700) != nil {
			return "", ErrProbe
		}
	}
	env := []string{
		"HOME=" + filepath.Join(root, "home"), "XDG_DATA_HOME=" + filepath.Join(root, "data"),
		"XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"),
		"XDG_STATE_HOME=" + filepath.Join(root, "state"), "TMPDIR=" + filepath.Join(root, "tmp"),
		"PATH=/usr/bin:/bin", "LANG=C.UTF-8", "NO_COLOR=1", "OPENCODE_PURE=1",
		"OPENCODE_DISABLE_AUTOUPDATE=1", "OPENCODE_DISABLE_MODELS_FETCH=1",
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return run(ctx, executable, filepath.Join(root, "work"), env, args)
}

// The process-group leader stays alive until cleanup. Its target has fd3 closed,
// so only the custodian reports target exit. Keeping the leader unreaped until
// KILL preserves ownership even when the native parent exits before descendants.
const custodian = `"$@" </dev/null 3>&- &
probe_pid=$!
wait "$probe_pid"
probe_status=$?
printf '%s\n' "$probe_status" >&3
read -r probe_release
`

type outputResult struct {
	data []byte
	err  error
}
type statusResult struct {
	code int
	err  error
}

func run(ctx context.Context, executable, directory string, environment, args []string) (string, error) {
	outputRead, outputWrite, err := os.Pipe()
	if err != nil {
		return "", ErrProbe
	}
	defer outputRead.Close()
	defer outputWrite.Close()
	statusRead, statusWrite, err := os.Pipe()
	if err != nil {
		return "", ErrProbe
	}
	defer statusRead.Close()
	defer statusWrite.Close()
	holdRead, holdWrite, err := os.Pipe()
	if err != nil {
		return "", ErrProbe
	}
	defer holdRead.Close()
	defer holdWrite.Close()
	arguments := append([]string{"-c", custodian, "chora-opencode-metadata", executable}, args...)
	cmd := exec.Command("/bin/sh", arguments...)
	cmd.Dir, cmd.Env, cmd.Stdin = directory, environment, holdRead
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Stdout, cmd.Stderr = outputWrite, outputWrite
	cmd.ExtraFiles = []*os.File{statusWrite}
	if ctx.Err() != nil || cmd.Start() != nil {
		return "", ErrProbe
	}
	outputWrite.Close()
	statusWrite.Close()
	outputDone := make(chan outputResult, 1)
	go func() {
		data, err := io.ReadAll(io.LimitReader(outputRead, outputLimit+1))
		outputDone <- outputResult{data, err}
	}()
	statusDone := make(chan statusResult, 1)
	go func() {
		line, err := bufio.NewReader(io.LimitReader(statusRead, 16)).ReadString('\n')
		code, parseErr := strconv.Atoi(strings.TrimSpace(line))
		statusDone <- statusResult{code, errors.Join(err, parseErr)}
	}()
	var output outputResult
	var status statusResult
	outputReceived, statusReceived := false, false
	interrupted := false
	select {
	case status = <-statusDone:
		statusReceived = true
	case output = <-outputDone:
		outputReceived, interrupted = true, true
	case <-ctx.Done():
		interrupted = true
	}
	// No Wait before KILL: the custodian's PID cannot be reused at this point.
	signalErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	waitErr := cmd.Wait()
	cleanupErr := confirmGroupGone(cmd.Process.Pid, signalErr)
	// Escaped process groups are outside this Local Connected boundary. Close
	// inherited pipes after bounded draining so they cannot retain a Go reader.
	if !statusReceived {
		statusRead.Close()
		status = <-statusDone
	}
	if !outputReceived {
		select {
		case output = <-outputDone:
		case <-time.After(100 * time.Millisecond):
			outputRead.Close()
			output = <-outputDone
			interrupted = true
		}
	}
	// The custodian itself was intentionally killed. Wait reaps it, while its
	// separately reported target exit determines metadata success.
	if waitErr != nil && cmd.ProcessState == nil {
		cleanupErr = ErrCleanup
	}
	if cleanupErr != nil {
		return "", errors.Join(ErrProbe, cleanupErr)
	}
	if interrupted || ctx.Err() != nil || status.err != nil || status.code != 0 || output.err != nil || len(output.data) > outputLimit {
		return "", ErrProbe
	}
	return string(output.data), nil
}

func confirmGroupGone(pgid int, signalErr error) error {
	if signalErr != nil && !errors.Is(signalErr, syscall.ESRCH) {
		return ErrCleanup
	}
	deadline := time.Now().Add(time.Second)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil || time.Now().After(deadline) {
			return ErrCleanup
		}
		time.Sleep(10 * time.Millisecond)
	}
}
