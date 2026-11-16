// Package workflow implements the narrow JSON subprocess boundary to the PG
// research worker. Python owns graph state; Go owns commands and business state.
package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

type Bridge interface {
	Describe(context.Context) (json.RawMessage, error)
	Execute(context.Context, domain.WorkflowCommand) (json.RawMessage, error)
}

type Process struct{ Python string }

// Do not echo stdout, stderr, DSN, provider URLs or process environment on error.
// The explicitly configured interpreter must have chora_workflow installed.
func (p Process) call(ctx context.Context, input any) (json.RawMessage, error) {
	if !filepath.IsAbs(p.Python) {
		return nil, errors.New("configure an absolute workflow Python executable")
	}
	ctx, cancel := context.WithTimeout(ctx, 130*time.Second)
	defer cancel()
	b, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, p.Python, "-m", "chora_workflow.product")
	cmd.Stdin = bytes.NewReader(b)
	var out limitedBuffer
	cmd.Stdout = &out
	cmd.Stderr = io.Discard
	if err = cmd.Run(); err != nil {
		return nil, errors.New("research worker unavailable; reconcile the same operation")
	}
	if !json.Valid(out.Bytes()) {
		return nil, errors.New("invalid research worker receipt")
	}
	return append(json.RawMessage(nil), out.Bytes()...), nil
}
func (p Process) Describe(ctx context.Context) (json.RawMessage, error) {
	return p.call(ctx, map[string]string{"kind": "describe"})
}
func (p Process) Execute(ctx context.Context, c domain.WorkflowCommand) (json.RawMessage, error) {
	return p.call(ctx, c)
}

type limitedBuffer struct{ bytes.Buffer }

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 1<<20 {
		return 0, errors.New("research receipt exceeds limit")
	}
	return b.Buffer.Write(p)
}
