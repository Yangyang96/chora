package agent

import (
	"context"
	"fmt"

	"github.com/Yangyang96/chora/internal/execution"
)

func CheckConformance(ctx context.Context, adapter execution.AgentAdapter) error {
	if adapter == nil || adapter.ID() == "" {
		return fmt.Errorf("adapter identity required")
	}
	capabilities := adapter.Capabilities()
	if capabilities.ResumeMode != execution.ResumeUnsupported && capabilities.ResumeMode != execution.ResumeExplicitSession {
		return fmt.Errorf("invalid resume mode %q", capabilities.ResumeMode)
	}
	fingerprint, err := adapter.Fingerprint(ctx)
	if err != nil {
		return err
	}
	if fingerprint.Digest == ([32]byte{}) || fingerprint.Version == "" {
		return fmt.Errorf("runtime fingerprint incomplete")
	}
	return nil
}
