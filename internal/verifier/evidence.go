package verifier

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"hash"
	"io"
	"slices"
	"strings"
	"time"
)

var (
	ErrInvalidAuthority = errors.New("invalid verifier authority")
	ErrIntegrity        = errors.New("verification integrity uncertain")
)

type Redactor interface {
	Version() string
	Redact([]byte) ([]byte, error)
}

type StreamLog struct {
	FullSHA256             [32]byte
	TotalBytes             int64
	RetainedBody           string
	Truncated              bool
	TruncationBoundary     int64
	RedactionPolicyVersion string
}

type streamCapture struct {
	limit    int64
	total    int64
	hash     hash.Hash
	retained bytes.Buffer
	redactor Redactor
}

func newStreamCapture(limit int64, redactor Redactor) *streamCapture {
	return &streamCapture{limit: limit, hash: sha256.New(), redactor: redactor}
}

func (capture *streamCapture) Write(body []byte) (int, error) {
	_, _ = capture.hash.Write(body)
	capture.total += int64(len(body))
	remaining := capture.limit - int64(capture.retained.Len())
	if remaining > 0 {
		keep := int64(len(body))
		if keep > remaining {
			keep = remaining
		}
		_, _ = capture.retained.Write(body[:keep])
	}
	return len(body), nil
}

func (capture *streamCapture) finish() (StreamLog, error) {
	if capture.limit <= 0 || capture.redactor == nil || strings.TrimSpace(capture.redactor.Version()) == "" {
		return StreamLog{}, ErrIntegrity
	}
	redacted, err := capture.redactor.Redact(capture.retained.Bytes())
	if err != nil || len(redacted) != capture.retained.Len() || int64(len(redacted)) > capture.total || int64(len(redacted)) > capture.limit {
		return StreamLog{}, ErrIntegrity
	}
	var digest [32]byte
	copy(digest[:], capture.hash.Sum(nil))
	truncated := capture.total > int64(capture.retained.Len())
	boundary := int64(0)
	if truncated {
		boundary = int64(len(redacted))
	}
	return StreamLog{FullSHA256: digest, TotalBytes: capture.total, RetainedBody: string(redacted), Truncated: truncated,
		TruncationBoundary: boundary, RedactionPolicyVersion: capture.redactor.Version()}, nil
}

type CommandKind string

const (
	CommandExited       CommandKind = "exited"
	CommandTimedOut     CommandKind = "timed_out"
	CommandCancelled    CommandKind = "cancelled"
	CommandLaunchFailed CommandKind = "launch_failed"
	CommandUnavailable  CommandKind = "unavailable"
)

type CommandRequest struct {
	ID           string
	Argv         []string
	CriterionIDs []string
	Timeout      time.Duration
}

type CommandOutcome struct {
	Kind     CommandKind
	ExitCode *int
	Reason   string
}

type Sandbox interface {
	Identity() string
	Execute(context.Context, CommandRequest, io.Writer, io.Writer) CommandOutcome
	Terminate(context.Context) error
	Cleanup(context.Context) error
}

type SandboxSpec struct {
	WorkspaceRoot string
	Policy        Policy
	Identity      EvidenceIdentity
	ArtifactLimit int64
}

type SandboxFactory interface {
	Create(context.Context, SandboxSpec) (Sandbox, error)
}

type EvidenceIdentity struct {
	RunID                    string
	VerificationRunID        string
	AttemptID                string
	TaskID                   string
	WorkspaceIdentity        string
	VerifierImage            string
	VerifierPolicyVersion    string
	BaselineDigest           [32]byte
	PatchDigest              [32]byte
	ContextSnapshotDigest    [32]byte
	AcceptanceContractDigest [32]byte
	VerifierPolicyDigest     [32]byte
}

func (identity EvidenceIdentity) baseValid() bool {
	zero := [32]byte{}
	return strings.TrimSpace(identity.RunID) != "" && strings.TrimSpace(identity.VerificationRunID) != "" && strings.TrimSpace(identity.AttemptID) != "" && strings.TrimSpace(identity.TaskID) != "" &&
		validSHA256Identity(identity.VerifierImage) && strings.TrimSpace(identity.VerifierPolicyVersion) != "" &&
		identity.BaselineDigest != zero && identity.PatchDigest != zero &&
		identity.ContextSnapshotDigest != zero && identity.AcceptanceContractDigest != zero && identity.VerifierPolicyDigest != zero
}

func (identity EvidenceIdentity) valid() bool {
	return identity.baseValid() && validSHA256Identity(identity.WorkspaceIdentity)
}

type CommandEvidence struct {
	CommandID        string
	CriterionIDs     []string
	Argv             []string
	StartedAt        time.Time
	EndedAt          time.Time
	Kind             CommandKind
	ExitCode         *int
	Stdout           StreamLog
	Stderr           StreamLog
	VerifierIdentity string
	Identity         EvidenceIdentity
}

type AttemptClassification string

const (
	AttemptCompleted        AttemptClassification = "completed"
	AttemptCancelled        AttemptClassification = "cancelled"
	AttemptRecoveryRequired AttemptClassification = "recovery_required"
)

type AttemptEvidence struct {
	Classification AttemptClassification
	Commands       []CommandEvidence
	CleanupProven  bool
	Reason         string
}

type frozenCommand struct {
	ID           string
	Argv         []string
	CriterionIDs []string
}

func executeCommands(parent context.Context, sandbox Sandbox, policy Policy, commands []frozenCommand, identity EvidenceIdentity, redactor Redactor, now func() time.Time) AttemptEvidence {
	evidence := AttemptEvidence{Classification: AttemptCompleted}
	if sandbox == nil || strings.TrimSpace(sandbox.Identity()) == "" || !identity.valid() || redactor == nil || now == nil || len(commands) == 0 {
		return recovery(evidence, "invalid execution boundary")
	}
	attemptCtx, cancelAttempt := context.WithTimeout(parent, time.Duration(policy.document.AttemptTimeoutSeconds)*time.Second)
	defer cancelAttempt()
	streamLimit := policy.document.PersistedLogBytes / 2
	for _, command := range commands {
		started := now()
		stdout := newStreamCapture(streamLimit, redactor)
		stderr := newStreamCapture(streamLimit, redactor)
		commandCtx, cancelCommand := context.WithTimeout(attemptCtx, time.Duration(policy.document.CommandTimeoutSeconds)*time.Second)
		outcomeChannel := make(chan CommandOutcome, 1)
		go func() {
			outcomeChannel <- sandbox.Execute(commandCtx, CommandRequest{ID: command.ID, Argv: slices.Clone(command.Argv), CriterionIDs: slices.Clone(command.CriterionIDs), Timeout: time.Duration(policy.document.CommandTimeoutSeconds) * time.Second}, stdout, stderr)
		}()
		var outcome CommandOutcome
		forcedStop := false
		select {
		case outcome = <-outcomeChannel:
		case <-commandCtx.Done():
			forcedStop = true
			if parent.Err() != nil {
				outcome = CommandOutcome{Kind: CommandCancelled, Reason: parent.Err().Error()}
			} else {
				outcome = CommandOutcome{Kind: CommandTimedOut, Reason: context.DeadlineExceeded.Error()}
			}
		}
		contextErr := commandCtx.Err()
		cancelCommand()
		if parent.Err() != nil {
			outcome = CommandOutcome{Kind: CommandCancelled, Reason: parent.Err().Error()}
		} else if errors.Is(contextErr, context.DeadlineExceeded) || errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
			outcome = CommandOutcome{Kind: CommandTimedOut, Reason: context.DeadlineExceeded.Error()}
		}
		needsTermination := forcedStop || outcome.Kind == CommandTimedOut || outcome.Kind == CommandCancelled
		if needsTermination {
			deathCtx, cancelDeath := context.WithTimeout(context.Background(), time.Duration(policy.document.DeathConfirmationSeconds)*time.Second)
			terminateErr := sandbox.Terminate(deathCtx)
			if terminateErr == nil && forcedStop {
				select {
				case <-outcomeChannel:
				case <-deathCtx.Done():
					terminateErr = errors.New("executor did not stop after termination")
				}
			}
			cancelDeath()
			if terminateErr != nil {
				return recovery(evidence, "process death unproven: "+terminateErr.Error())
			}
		}
		ended := now()
		stdoutLog, stdoutErr := stdout.finish()
		stderrLog, stderrErr := stderr.finish()
		commandEvidence := CommandEvidence{CommandID: command.ID, CriterionIDs: slices.Clone(command.CriterionIDs), Argv: slices.Clone(command.Argv),
			StartedAt: started, EndedAt: ended, Kind: outcome.Kind, ExitCode: cloneInt(outcome.ExitCode), Stdout: stdoutLog, Stderr: stderrLog,
			VerifierIdentity: sandbox.Identity(), Identity: identity}
		evidence.Commands = append(evidence.Commands, commandEvidence)
		if stdoutErr != nil || stderrErr != nil || !validOutcome(outcome) || ended.Before(started) {
			return recovery(evidence, "invalid command evidence")
		}
		switch outcome.Kind {
		case CommandExited:
			continue
		case CommandTimedOut, CommandCancelled:
			if outcome.Kind == CommandCancelled {
				evidence.Classification = AttemptCancelled
				return evidence
			}
			if errors.Is(attemptCtx.Err(), context.DeadlineExceeded) {
				return evidence
			}
		case CommandLaunchFailed:
			return recovery(evidence, "command launch uncertain")
		}
	}
	return evidence
}

func unavailableCommandEvidence(sandbox Sandbox, commands []frozenCommand, identity EvidenceIdentity, redactor Redactor, now func() time.Time) AttemptEvidence {
	evidence := AttemptEvidence{Classification: AttemptCompleted, Reason: "evidence deterministically unavailable by accepted verifier policy"}
	if sandbox == nil || strings.TrimSpace(sandbox.Identity()) == "" || !identity.valid() || redactor == nil || strings.TrimSpace(redactor.Version()) == "" || now == nil || len(commands) == 0 {
		return recovery(evidence, "invalid unavailable-evidence boundary")
	}
	emptyDigest := sha256.Sum256(nil)
	for _, command := range commands {
		at := now()
		log := StreamLog{FullSHA256: emptyDigest, RedactionPolicyVersion: redactor.Version()}
		evidence.Commands = append(evidence.Commands, CommandEvidence{
			CommandID: command.ID, CriterionIDs: slices.Clone(command.CriterionIDs), Argv: slices.Clone(command.Argv),
			StartedAt: at, EndedAt: at, Kind: CommandUnavailable, Stdout: log, Stderr: log,
			VerifierIdentity: sandbox.Identity(), Identity: identity,
		})
	}
	return evidence
}

func validOutcome(outcome CommandOutcome) bool {
	switch outcome.Kind {
	case CommandExited:
		return outcome.ExitCode != nil
	case CommandTimedOut, CommandCancelled, CommandLaunchFailed:
		return outcome.ExitCode == nil
	default:
		return false
	}
}

func cloneInt(value *int) *int {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func recovery(evidence AttemptEvidence, reason string) AttemptEvidence {
	evidence.Classification = AttemptRecoveryRequired
	evidence.CleanupProven = false
	evidence.Reason = reason
	return evidence
}
