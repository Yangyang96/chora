package codexexec

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

var requiredCapabilities = []string{"launch_arguments", "event_frames", "tool_lifecycle", "terminal_result", "process_supervision", "configuration_sources"}

var nativeVersion = regexp.MustCompile(`^codex-cli [0-9]+\.[0-9]+\.[0-9]+([-+][0-9A-Za-z.-]+)?$`)

// QualificationError carries redacted per-capability evidence on refused
// construction. It cannot be supplied to New as an execution authorization.
type QualificationError struct {
	Report execution.QualificationReport
	cause  error
}

func (e *QualificationError) Error() string { return e.cause.Error() }
func (e *QualificationError) Unwrap() error { return e.cause }

func (a *Adapter) qualificationIdentity() execution.QualificationIdentity {
	fp := a.deriveFingerprint(a.executable)
	return execution.QualificationIdentity{
		RuntimeID: AdapterID, Version: a.version, ProtocolVersion: protocolVersion,
		SourceIdentity: fp.SourceIdentity, SecurityIdentity: fp.SecurityFingerprint,
		ConfigurationIdentity: hash(struct {
			Config     Config
			Protocol   string
			Descriptor [32]byte
			Required   []string
		}{a.config, protocolVersion, a.Describe().Digest(), requiredCapabilities}),
	}
}

// Reviewed releases are evidence anchors for one protocol generation, not a
// SemVer range or permission to launch. Both still require live argv probes and
// configuration-source proof. Other versions stay pending until their semantic
// evidence is reviewed; matching help text alone cannot certify safety.
func reviewedProtocol(version string) string {
	switch version {
	case "codex-cli 0.155.1":
		return "https://github.com/openai/codex/tree/be2951ea34f0d295ed0becf97079f92fa5f6950e/codex-rs/exec/src"
	case "codex-cli 0.167.2":
		return "https://github.com/openai/codex/tree/ce21072c90d32c40af7a55b60b2fb35c84a43a6a/codex-rs/exec/src"
	default:
		return ""
	}
}

func (a *Adapter) qualifyProtocol(ctx context.Context) error {
	identity := a.qualificationIdentity()
	evidence := []execution.CapabilityEvidence{}
	source := reviewedProtocol(a.version)
	// Unknown versions receive only the bounded, no-model CLI help probe.
	launch, _ := a.protocolEvidence(ctx)
	approval, approved := a.protocolReviews.lookup(identity)
	if approved {
		a.protocolApproval = approval.digest
		source = approval.reference
	}
	evidence = append(evidence, launch)
	if source != "" {
		method := "reviewed_source"
		if approved {
			method = "signed_source_review_and_adapter_conformance"
			if approval.request.Fixture {
				method = "signed_fixture_conformance:not_native"
			}
		}
		for _, capability := range []string{"event_frames", "tool_lifecycle", "terminal_result"} {
			evidence = append(evidence, execution.CapabilityEvidence{Capability: capability, State: execution.QualificationVerified, Method: method, Reference: source})
		}
	}
	evidence = append(evidence, execution.CapabilityEvidence{Capability: "process_supervision", State: execution.QualificationVerified, Method: "implementation_and_fixture", Reference: "trustedhost_bound_process_group:not_authenticated_cancellation"})
	report, err := a.qualification.Store(identity, requiredCapabilities, evidence)
	if err != nil {
		return err
	}
	// Recheck bytes after probing: a version/help pair from different binaries
	// must not qualify either. Binding and the supervisor repeat this check.
	current, err := trustedhost.InspectExecutable(a.config.Executable)
	if err != nil || current != a.executable {
		return ErrIdentity
	}
	if source == "" || launch.State != execution.QualificationVerified {
		missing := []string{}
		for _, e := range report.Evidence {
			if e.State != execution.QualificationVerified {
				missing = append(missing, e.Capability+"="+e.Reference)
			}
		}
		return &QualificationError{Report: report, cause: fmt.Errorf("%w: qualification_%s: %s; validate this executable/protocol before starting a new task", ErrVersion, report.State, strings.Join(missing, ","))}
	}
	return nil
}

func (a *Adapter) qualificationFailure(cause error) error {
	report, _ := a.qualification.Lookup(a.qualificationIdentity())
	evidence := append([]execution.CapabilityEvidence(nil), report.Evidence...)
	for i := range evidence {
		if evidence[i].Capability == "configuration_sources" {
			evidence[i] = execution.CapabilityEvidence{Capability: "configuration_sources", State: execution.QualificationRejected, Method: "live_policy_gate", Reference: "configuration_unqualified"}
			if cause == ErrManagedPolicy {
				evidence[i].Reference = "managed_policy_unverifiable:same_config_consumption_required"
			}
		}
	}
	report, _ = a.qualification.Store(a.qualificationIdentity(), requiredCapabilities, evidence)
	return &QualificationError{Report: report, cause: cause}
}

func (a *Adapter) recordPolicyQualification() error {
	if approval, ok := a.protocolReviews.lookup(a.qualificationIdentity()); ok && approval.request.Fixture {
		return ErrProtocolReview
	}
	report, ok := a.qualification.Lookup(a.qualificationIdentity())
	if !ok {
		return ErrIdentity
	}
	for i := range report.Evidence {
		if report.Evidence[i].Capability == "configuration_sources" {
			// Currently reachable only through the package-private fixture checker.
			// Public New has no native same-Config proof and always rejects.
			report.Evidence[i] = execution.CapabilityEvidence{Capability: "configuration_sources", State: execution.QualificationVerified, Method: "fixture", Reference: "synthetic_policy_checker:not_native_qualification"}
		}
	}
	_, err := a.qualification.Store(a.qualificationIdentity(), requiredCapabilities, report.Evidence)
	return err
}

// protocolEvidence checks arguments only; matching help never certifies semantics.
func (a *Adapter) protocolEvidence(ctx context.Context) (execution.CapabilityEvidence, error) {
	help, err := a.probe(ctx, "exec", "--help")
	launch := execution.CapabilityEvidence{Capability: "launch_arguments", State: execution.QualificationVerified, Method: "native_help", Reference: "exec_help_flags_present"}
	if err != nil {
		launch.State, launch.Reference = execution.QualificationPending, "exec_help_probe_unavailable"
	} else {
		flags := strings.Fields(help)
		for _, required := range []string{"--json", "--ephemeral", "--ignore-user-config", "--ignore-rules", "--skip-git-repo-check", "--sandbox", "--color", "--config", "--model"} {
			found := false
			for _, flag := range flags {
				if flag == required {
					found = true
					break
				}
			}
			if !found {
				launch.State, launch.Reference = execution.QualificationRejected, "missing_flag:"+required
				break
			}
		}
	}

	if launch.State != execution.QualificationVerified {
		return launch, fmt.Errorf("%s", launch.Reference)
	}
	return launch, nil
}
