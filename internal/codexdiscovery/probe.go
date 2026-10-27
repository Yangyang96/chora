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

// Probe permits only executable version and existing login status. It never
// installs, logs in/out, starts a model request or returns unbounded diagnostics.
func Probe(ctx context.Context, executable, home, authHome string, args ...string) (string, error) {
	if !reflect.DeepEqual(args, []string{"--version"}) && !reflect.DeepEqual(args, []string{"login", "status"}) {
		return "", ErrProbe
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = home
	cmd.Env = []string{"HOME=" + home, "CODEX_HOME=" + authHome, "PATH=/usr/bin:/bin"}
	output := &boundedOutput{}
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
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > 4096 {
		b.exceeded = true
		return len(p), nil
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
