// Package codexdiscovery owns bounded read-only native CLI probes. Agent
// protocol adapters cannot start, cancel or signal runtime processes here.
package codexdiscovery

import (
	"context"
	"errors"
	"os/exec"
	"reflect"
	"time"
)

var ErrProbe = errors.New("codex_readiness_probe_failed")

// Probe permits only executable version, exec help and existing login status. It never
// installs, logs in/out, starts a model request or returns unbounded diagnostics.
func Probe(ctx context.Context, executable, home, authHome string, args ...string) (string, error) {
	help := reflect.DeepEqual(args, []string{"exec", "--help"})
	if !reflect.DeepEqual(args, []string{"--version"}) && !reflect.DeepEqual(args, []string{"login", "status"}) && !help {
		return "", ErrProbe
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "CODEX_HOME=" + authHome, "PATH=/usr/bin:/bin"}
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
