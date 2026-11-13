package opencoderun

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

var zeroCredentials = regexp.MustCompile(`(?m)^[│└ \t]*0 credentials[ \r]*$`)

var nativeVersion = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`)
var requiredCapabilities = []string{"launch_arguments", "event_frames", "terminal_result", "process_supervision", "configuration_sources", "retry_control", "authentication_scope"}

const sourceReference = "https://github.com/anomalyco/opencode/tree/810c0eba1a7348036f8bdf61e6632d0c36c6876c/packages/opencode/src"

// QualificationError preserves capability diagnostics without raw native output.
type QualificationError struct {
	Report execution.QualificationReport
	cause  error
}

func (e *QualificationError) Error() string { return e.cause.Error() }
func (e *QualificationError) Unwrap() error { return e.cause }

func (a *Adapter) qualificationIdentity() execution.QualificationIdentity {
	fp := a.deriveFingerprint(a.executable)
	return execution.QualificationIdentity{RuntimeID: AdapterID, Version: a.version, ProtocolVersion: protocolVersion,
		SourceIdentity: fp.SourceIdentity, ConfigurationIdentity: fp.Digest, SecurityIdentity: fp.SecurityFingerprint}
}

func (a *Adapter) qualify(ctx context.Context) error {
	evidence := []execution.CapabilityEvidence{}
	observe := func(capability string, state execution.QualificationState, method, reference string) {
		evidence = append(evidence, execution.CapabilityEvidence{Capability: capability, State: state, Method: method, Reference: reference})
	}
	help, err := a.probe(ctx, "run", "--help")
	launchState, launchReference := execution.QualificationPending, "run_help_unavailable"
	if err == nil {
		launchState, launchReference = execution.QualificationVerified, "run_help_flags_present"
		for _, flag := range []string{"--pure", "--format", "--model"} {
			if !slices.Contains(strings.Fields(help), flag) {
				launchState, launchReference = execution.QualificationRejected, "missing_flag:"+flag
				break
			}
		}
	}
	observe("launch_arguments", launchState, "native_help", launchReference)
	// One reviewed source anchor establishes this generation's semantics. It
	// does not authorize every matching or newer SemVer release.
	if a.version == "1.19.5" {
		observe("event_frames", execution.QualificationVerified, "reviewed_source", sourceReference+"/cli/cmd/run.ts")
		observe("terminal_result", execution.QualificationVerified, "reviewed_source_and_fixture", "step_finish_stop:paired_message:trusted_exit_zero")
	}
	observe("process_supervision", execution.QualificationVerified, "implementation_and_fixture", "trustedhost_process_group:not_authenticated_cancellation")
	var failures []error
	if a.version != "1.19.5" || launchState != execution.QualificationVerified {
		failures = append(failures, ErrVersion)
	}
	if a.nativeCheck == nil {
		observe("configuration_sources", execution.QualificationRejected, "reviewed_source", "pure_retains_global_org_managed_config_and_native_resources")
		observe("retry_control", execution.QualificationRejected, "reviewed_source", "session_retry_five_retries:no_qualified_disable_interface")
		auth := a.authenticationDiagnostic(ctx)
		observe("authentication_scope", execution.QualificationRejected, "native_metadata", auth.Error())
		failures = append(failures, ErrNativeConfig, ErrNativeRetry, auth)
	} else {
		// Only package-local fixtures reach this branch. These observations are
		// labelled synthetic and cannot be imported through public construction.
		if err := a.validatePolicy(ctx); err != nil {
			return err
		}
		for _, capability := range []string{"configuration_sources", "retry_control", "authentication_scope"} {
			observe(capability, execution.QualificationVerified, "fixture", "synthetic_native_checker:not_native_qualification")
		}
	}
	report, err := a.qualification.Store(a.qualificationIdentity(), requiredCapabilities, evidence)
	if err != nil {
		return err
	}
	current, err := trustedhost.InspectExecutable(a.config.Executable)
	if err != nil || current != a.executable {
		return ErrIdentity
	}
	if report.State != execution.QualificationVerified {
		return &QualificationError{Report: report, cause: fmt.Errorf("opencode qualification_%s: %w", report.State, errors.Join(failures...))}
	}
	return nil
}
