// Package opencodediscovery owns bounded, no-model OpenCode readiness probes.
package opencodediscovery

import (
	"context"
	"errors"
	"os/exec"
	"slices"
	"time"
)

var ErrProbe = errors.New("opencode_readiness_probe_failed")

// Probe never loads an instance, logs in, or sends a prompt. In particular,
// debug config is excluded because it can load plugins and remote configuration.
func Probe(ctx context.Context, executable, home string, environment map[string]string, args ...string) (string, error) {
	if !slices.Equal(args, []string{"--version"}) && !slices.Equal(args, []string{"run", "--help"}) && !slices.Equal(args, []string{"auth", "list"}) {
		return "", ErrProbe
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, executable, args...)
	cmd.Dir = home
	cmd.Env = []string{}
	for key, value := range environment {
		cmd.Env = append(cmd.Env, key+"="+value)
	}
	output := &boundedOutput{}
	cmd.Stdout, cmd.Stderr = output, output
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
	if len(b.data)+len(p) > 32*1024 {
		b.exceeded = true
		return len(p), nil
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
