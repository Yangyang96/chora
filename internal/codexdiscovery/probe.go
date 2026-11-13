// Package codexdiscovery owns bounded native CLI readiness probes. Agent
// protocol adapters cannot start, cancel or signal runtime processes here.
package codexdiscovery

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"time"
)

var ErrProbe = errors.New("codex_readiness_probe_failed")

// Probe permits only executable version, exec help and existing login status.
// Metadata uses a disposable HOME/CODEX_HOME/cwd, never the selected scope.
// Native bootstrap may write inside that disposable tree; it is removed on every
// exit. Login status intentionally uses the selected scope and may perform native
// bootstrap writes there. Callers must accept and preflight that separate boundary.
// No probe installs, logs in/out, or starts a model request.
func Probe(ctx context.Context, executable, home, authHome string, args ...string) (string, error) {
	help := reflect.DeepEqual(args, []string{"exec", "--help"})
	if !reflect.DeepEqual(args, []string{"--version"}) && !reflect.DeepEqual(args, []string{"login", "status"}) && !help {
		return "", ErrProbe
	}
	if help || reflect.DeepEqual(args, []string{"--version"}) {
		root, err := os.MkdirTemp("", "chora-codex-metadata-")
		if err != nil {
			return "", ErrProbe
		}
		defer os.RemoveAll(root)
		root, err = filepath.EvalSymlinks(root)
		if err != nil {
			return "", ErrProbe
		}
		home, authHome = root, filepath.Join(root, "codex")
		for _, dir := range []string{authHome, filepath.Join(root, "tmp")} {
			if os.Mkdir(dir, 0700) != nil {
				return "", ErrProbe
			}
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Reap the command before deleting its disposable tree. Kill any remaining
	// group members even when the parent exits without waiting for them.
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	defer func() {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
	}()
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "CODEX_HOME=" + authHome, "PATH=/usr/bin:/bin", "TMPDIR=" + filepath.Join(home, "tmp"), "LANG=C.UTF-8"}
	output := &boundedOutput{}
	if help {
		output.limit = 32 * 1024
	}
	cmd.Stdout = output
	cmd.Stderr = output
	cmd.WaitDelay = time.Second
	if err := cmd.Run(); err != nil || output.exceeded {
		return "", ErrProbe
	}
	return string(output.data), nil
}

type boundedOutput struct {
	data     []byte
	exceeded bool
	limit    int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	limit := b.limit
	if limit == 0 {
		limit = 4096
	}
	if len(b.data)+len(p) > limit {
		b.exceeded = true
		return len(p), nil
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
