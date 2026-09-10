package verifier

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
)

type Config struct {
	Policy           Policy
	WorkspaceFactory WorkspaceFactory
	SandboxFactory   SandboxFactory
	Redactor         Redactor
	Now              func() time.Time
}

type Verifier struct {
	policy           Policy
	workspaceFactory WorkspaceFactory
	sandboxFactory   SandboxFactory
	redactor         Redactor
	now              func() time.Time
}

func New(config Config) (*Verifier, error) {
	if err := validatePolicy(config.Policy.document); err != nil || config.Policy.digest == ([32]byte{}) || config.WorkspaceFactory == nil || config.SandboxFactory == nil || config.Redactor == nil || strings.TrimSpace(config.Redactor.Version()) == "" {
		return nil, ErrInvalidPolicy
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	return &Verifier{policy: config.Policy, workspaceFactory: config.WorkspaceFactory, sandboxFactory: config.SandboxFactory, redactor: config.Redactor, now: config.Now}, nil
}

type VerifyRequest struct {
	Contract          speccoding.CoreContract
	Bindings          domain.VerificationBindings
	RunID             string
	VerificationRunID string
	AttemptID         string
	BaselineRoot      string
	BaselineManifest  []byte
	AgentWorkspace    string
	Patch             []byte
}

// Verify produces attempt evidence only. Result aggregation and persistence are
// deliberately outside this package.
func (verifier *Verifier) Verify(ctx context.Context, request VerifyRequest) (AttemptEvidence, error) {
	attemptCtx, cancelAttempt := context.WithTimeout(ctx, time.Duration(verifier.policy.document.AttemptTimeoutSeconds)*time.Second)
	defer cancelAttempt()
	document := request.Contract.Document()
	commands, err := freezeCommands(document.SchemaVersion, document.Acceptance)
	if err != nil {
		return AttemptEvidence{}, err
	}
	identity, err := verifier.authorize(request)
	if err != nil {
		return AttemptEvidence{}, err
	}
	lease, err := verifier.workspaceFactory.Prepare(attemptCtx, WorkspaceRequest{BaselineRoot: request.BaselineRoot, BaselineManifest: slices.Clone(request.BaselineManifest), AgentWorkspace: request.AgentWorkspace,
		ExpectedBaselineDigest: request.Bindings.BaselineDigest, Patch: slices.Clone(request.Patch), ExpectedPatchDigest: request.Bindings.PatchDigest,
		WritableFiles: slices.Clone(document.Execution.Boundary.WritableFiles), VerificationAttemptID: request.AttemptID, PolicyDigest: verifier.policy.digest})
	if err != nil {
		return recovery(AttemptEvidence{}, "workspace preparation rejected: "+err.Error()), nil
	}
	workspaceCleaned := false
	cleanupWorkspace := func() error {
		if workspaceCleaned {
			return nil
		}
		cleanupCtx, cancel := context.WithTimeout(context.Background(), time.Duration(verifier.policy.document.DeathConfirmationSeconds)*time.Second)
		defer cancel()
		err := lease.Cleanup(cleanupCtx)
		if err == nil {
			workspaceCleaned = true
		}
		return err
	}
	if lease.BaselineDigest() != identity.BaselineDigest || lease.PatchDigest() != identity.PatchDigest || samePath(lease.Root(), request.AgentWorkspace) {
		_ = cleanupWorkspace()
		return recovery(AttemptEvidence{}, "workspace identity drift"), nil
	}
	identity.WorkspaceIdentity = workspaceIdentity(lease.Root())
	sandbox, err := verifier.sandboxFactory.Create(attemptCtx, SandboxSpec{WorkspaceRoot: lease.Root(), Policy: verifier.policy, Identity: identity, ArtifactLimit: verifier.policy.document.ArtifactBytes})
	if err != nil || sandbox == nil {
		cleanupErr := cleanupWorkspace()
		reason := "sandbox launch uncertain"
		if err != nil {
			reason += ": " + err.Error()
		}
		if cleanupErr != nil {
			reason += "; workspace cleanup unproven: " + cleanupErr.Error()
		}
		return recovery(AttemptEvidence{}, reason), nil
	}
	var evidence AttemptEvidence
	if verifier.policy.document.Mode == PolicyModeAcceptanceOnly {
		evidence = unavailableCommandEvidence(sandbox, commands, identity, verifier.redactor, verifier.now)
	} else {
		evidence = executeCommands(attemptCtx, sandbox, verifier.policy, commands, identity, verifier.redactor, verifier.now)
	}
	cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), time.Duration(verifier.policy.document.DeathConfirmationSeconds)*time.Second)
	sandboxCleanupErr := sandbox.Cleanup(cleanupCtx)
	cancelCleanup()
	workspaceCleanupErr := cleanupWorkspace()
	if sandboxCleanupErr != nil || workspaceCleanupErr != nil {
		return recovery(evidence, "cleanup unproven: "+errors.Join(sandboxCleanupErr, workspaceCleanupErr).Error()), nil
	}
	evidence.CleanupProven = true
	return evidence, nil
}

func (verifier *Verifier) authorize(request VerifyRequest) (EvidenceIdentity, error) {
	bindings := request.Bindings
	contractDigest, err := digestHex(request.Contract.DigestHex())
	if err != nil || contractDigest != bindings.AcceptanceContractDigest || verifier.policy.digest != bindings.VerifierPolicyDigest ||
		verifier.policy.document.Version != bindings.VerifierPolicyVersion || len(request.Patch) == 0 ||
		len(bytes.TrimSpace(request.BaselineManifest)) == 0 ||
		request.Bindings.PatchDigest != sha256Bytes(request.Patch) || strings.TrimSpace(request.AttemptID) == "" {
		return EvidenceIdentity{}, ErrInvalidAuthority
	}
	document := request.Contract.Document()
	registeredSnapshotDigest, err := digestHex(document.Execution.Input.ContextSnapshotDigest)
	if err != nil || registeredSnapshotDigest == ([32]byte{}) {
		return EvidenceIdentity{}, ErrInvalidAuthority
	}
	identity := EvidenceIdentity{RunID: request.RunID, VerificationRunID: request.VerificationRunID, AttemptID: request.AttemptID, TaskID: document.Task.ID,
		VerifierImage: verifier.policy.document.Image, VerifierPolicyVersion: verifier.policy.document.Version,
		BaselineDigest: bindings.BaselineDigest, PatchDigest: bindings.PatchDigest,
		ContextSnapshotDigest: bindings.ContextSnapshotDigest, AcceptanceContractDigest: bindings.AcceptanceContractDigest, VerifierPolicyDigest: bindings.VerifierPolicyDigest}
	if !identity.baseValid() {
		return EvidenceIdentity{}, ErrInvalidAuthority
	}
	return identity, nil
}

func freezeCommands(schemaVersion string, acceptance speccoding.AcceptanceContract) ([]frozenCommand, error) {
	if (schemaVersion != speccoding.CoreContractSchemaVersionV8 && schemaVersion != speccoding.CoreContractSchemaVersionV9 && schemaVersion != speccoding.CoreContractSchemaVersionV10) || len(acceptance.Criteria) == 0 || len(acceptance.VerificationCommands) == 0 {
		return nil, ErrInvalidAuthority
	}
	criteriaByCommand := map[string][]string{}
	criterionSeen := map[string]struct{}{}
	for _, criterion := range acceptance.Criteria {
		if strings.TrimSpace(criterion.ID) == "" || len(criterion.VerificationCommandIDs) == 0 {
			return nil, ErrInvalidAuthority
		}
		if _, duplicate := criterionSeen[criterion.ID]; duplicate {
			return nil, ErrInvalidAuthority
		}
		criterionSeen[criterion.ID] = struct{}{}
		bound := map[string]struct{}{}
		for _, commandID := range criterion.VerificationCommandIDs {
			if strings.TrimSpace(commandID) == "" {
				return nil, ErrInvalidAuthority
			}
			if _, duplicate := bound[commandID]; duplicate {
				return nil, ErrInvalidAuthority
			}
			bound[commandID] = struct{}{}
			criteriaByCommand[commandID] = append(criteriaByCommand[commandID], criterion.ID)
		}
	}
	declared := map[string]struct{}{}
	commands := make([]frozenCommand, 0, len(criteriaByCommand))
	for _, command := range acceptance.VerificationCommands {
		if strings.TrimSpace(command.ID) == "" || len(command.Argv) == 0 {
			return nil, ErrInvalidAuthority
		}
		if _, duplicate := declared[command.ID]; duplicate {
			return nil, ErrInvalidAuthority
		}
		declared[command.ID] = struct{}{}
		for _, arg := range command.Argv {
			if strings.TrimSpace(arg) == "" || strings.ContainsRune(arg, '\x00') {
				return nil, ErrInvalidAuthority
			}
		}
		criterionIDs, referenced := criteriaByCommand[command.ID]
		if referenced {
			commands = append(commands, frozenCommand{ID: command.ID, Argv: slices.Clone(command.Argv), CriterionIDs: slices.Clone(criterionIDs)})
		}
	}
	for commandID := range criteriaByCommand {
		if _, ok := declared[commandID]; !ok {
			return nil, ErrInvalidAuthority
		}
	}
	if len(commands) == 0 {
		return nil, ErrInvalidAuthority
	}
	return commands, nil
}

func digestHex(value string) ([32]byte, error) {
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != 32 {
		return [32]byte{}, fmt.Errorf("invalid SHA-256")
	}
	var digest [32]byte
	copy(digest[:], decoded)
	return digest, nil
}

func sha256Bytes(value []byte) [32]byte {
	return sha256.Sum256(value)
}
