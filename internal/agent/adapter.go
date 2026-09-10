package agent

import (
	"fmt"

	"github.com/Yangyang96/chora/internal/execution"
)

func ValidateExplicitResume(capabilities execution.AdapterCapabilities, request execution.ResumeRequest, fingerprint execution.RuntimeFingerprint) error {
	if capabilities.ResumeMode != execution.ResumeExplicitSession {
		return fmt.Errorf("explicit session resume unsupported")
	}
	if request.Binding.ExternalSession == "" || request.Binding.RuntimeFingerprint != fingerprint || request.Binding.WorkingRoot == "" || request.Binding.SecurityFingerprint != request.SecurityFingerprint || !request.LaunchToken.Valid() {
		return fmt.Errorf("resume binding mismatch")
	}
	return nil
}
