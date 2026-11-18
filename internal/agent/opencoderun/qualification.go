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
	fp := a.baseFingerprint(a.executable)
	return execution.QualificationIdentity{RuntimeID: AdapterID, Version: a.version, ProtocolVersion: protocolVersion,
		SourceIdentity: fp.SourceIdentity, ConfigurationIdentity: fp.Digest, SecurityIdentity: fp.SecurityFingerprint}
}

func (a *Adapter) qualify(ctx context.Context) error {
	evidence := []execution.CapabilityEvidence{}
	observe := func(capability string, state execution.QualificationState, method, reference string) {
		evidence = append(evidence, execution.CapabilityEvidence{Capability: capability, State: state, Method: method, Reference: reference})
	}
	launch, launchErr := a.protocolEvidence(ctx)
	evidence = append(evidence, launch)
	source, method := "", "reviewed_source_and_adapter_fixture"
	if a.version == "1.19.5" {
		source = sourceReference + "/cli/cmd/run.ts"
	}
	if approval, ok := a.protocolReviews.lookup(a.qualificationIdentity()); ok {
		source = approval.attestation.SourceReference
		method = "signed_native_source_review_and_adapter_corpus"
		if approval.request.Fixture {
			method = "signed_fixture_conformance:adapter_only"
		}
		a.protocolApproval = approval.digest
	}
	if source != "" {
		for _, capability := range []string{"event_frames", "terminal_result"} {
			observe(capability, execution.QualificationVerified, method, source)
		}
	}
	observe("process_supervision", execution.QualificationVerified, "implementation_and_fixture", "trustedhost_process_group:not_authenticated_cancellation")
	var failures []error
	if launchErr != nil {
		failures = append(failures, launchErr)
	}
	if source == "" || launch.State != execution.QualificationVerified {
		failures = append(failures, ErrVersion)
	}
	if a.nativeCheck == nil {
		method, configReference, retryReference := "pre_effect_gate", "native_configuration_sources_unverifiable", "native_retry_control_unavailable"
		if a.version == "1.19.5" {
			method = "reviewed_source"
			configReference = "pure_retains_global_org_managed_config_and_native_resources"
			retryReference = "session_retry_five_retries:no_qualified_disable_interface"
		}
		observe("configuration_sources", execution.QualificationRejected, method, configReference)
		observe("retry_control", execution.QualificationRejected, method, retryReference)
		// User-scope credential metadata remains behind the safe-start gate.
		auth := ErrAuthScope
		observe("authentication_scope", execution.QualificationRejected, "pre_effect_gate", auth.Error())
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

// protocolEvidence observes argument shape through isolated metadata only.
func (a *Adapter) protocolEvidence(ctx context.Context) (execution.CapabilityEvidence, error) {
	help, err := a.probe(ctx, "run", "--help")
	launch := execution.CapabilityEvidence{Capability: "launch_arguments", State: execution.QualificationPending, Method: "isolated_native_help", Reference: "run_help_unavailable"}
	if err != nil {
		launch.Reference = "run_help_unavailable:" + err.Error()
	}
	if err == nil {
		launch.State, launch.Reference = execution.QualificationVerified, "run_help_flags_present"
		for _, flag := range []string{"--pure", "--format", "--model"} {
			if !slices.Contains(strings.Fields(help), flag) {
				launch.State, launch.Reference = execution.QualificationRejected, "missing_flag:"+flag
				break
			}
		}
	}
	if launch.State != execution.QualificationVerified {
		return launch, errors.Join(fmt.Errorf("%w: %s", ErrVersion, launch.Reference), err)
	}
	return launch, nil
}
