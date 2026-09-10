package app

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

// collectResourceTerminal is reached only after trusted exit and complete stream drain.
// The adapter cannot submit this evidence or choose its target identities.
func (s *Service) collectResourceTerminal(ctx context.Context, runtime runtimeContext, terminal execution.TerminalResult) (execution.TerminalResult, *domain.ResourceResultGroup, error) {
	binding, e := s.deps.Store.Reader().GetSpecCodingBinding(ctx, runtime.run.TaskID())
	if e != nil {
		return terminal, nil, e
	}
	if binding.Status != storecontract.SpecCodingRegistered || sha256.Sum256(binding.ActiveContractJSON) != binding.ActiveContractDigest {
		return terminal, nil, ErrReviewEvidenceUnavailable
	}
	core, e := speccoding.DecodeCoreContract(binding.ActiveContractJSON)
	if e != nil {
		return terminal, nil, e
	}
	doc := core.Document()
	if doc.SchemaVersion != speccoding.CoreContractSchemaVersionV12 {
		return terminal, nil, nil
	}
	if terminal.Kind != execution.TerminalReviewReady {
		return terminal, nil, nil
	}
	if s.deps.ResourcePatchMaterializer == nil {
		return execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "resource_patch_collector_unavailable"}, nil, nil
	}
	patches, e := s.deps.ResourcePatchMaterializer.Materialize(ctx, runtime.run.TaskID(), runtime.attempt.ID(), runtime.session.WorkingRoot)
	if e != nil {
		return execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "resource_patch_materialization_failed"}, nil, nil
	}
	if len(patches) != len(doc.Task.Resources) {
		return terminal, nil, ErrReviewEvidenceUnavailable
	}
	g := &domain.ResourceResultGroup{SchemaVersion: "chora.result-group.v2", ID: s.deps.IDs.ResultID().String(), RunID: runtime.run.ID().String(), TaskID: runtime.run.TaskID().String(), AttemptID: runtime.attempt.ID().String(), ResourceSnapshotDigest: doc.Task.ResourceSnapshotDigest, ContractDigest: hex.EncodeToString(binding.ActiveContractDigest[:]), ContextDigest: hex.EncodeToString(binding.SnapshotDigest[:]), Outcome: "review_ready", CreatedAt: s.deps.Clock.Now()}
	terminal.Outputs = nil
	terminal.Checks = nil // v2 evidence is repository-bound, never global claimed checks.
	events, err := s.deps.Store.Reader().ListRunEvents(ctx, runtime.run.ID())
	if err != nil {
		return terminal, nil, err
	}
	final, _, complete := domain.CurrentCompleteAssistantEvidence(events)
	if !complete {
		return execution.TerminalResult{Kind: execution.TerminalFailed, FailureReason: "result_contract_invalid"}, nil, nil
	}
	g.FinalAssistant = final
	current := []execution.NormalizedEvent{}
	for _, event := range events {
		if event.Type() == "attempt.started" {
			current = nil
			continue
		}
		if event.Source() == "adapter" {
			current = append(current, execution.NormalizedEvent{Type: event.Type(), NormalizedJSON: event.NormalizedJSON()})
		}
	}
	derived, err := DeriveResourceChecks(doc.Task.Resources, current, resourceObserverFingerprints(runtime.attempt.ID(), current, patches, s.deps.ResourceCheckObserverSHA256, runtime.session.RuntimeFingerprint))
	if err != nil {
		return terminal, nil, err
	}
	anyChange, allChecksSatisfied := false, true
	for i, p := range patches {
		r := doc.Task.Resources[i]
		if p.RepoID != r.RepoID {
			return terminal, nil, ErrReviewEvidenceUnavailable
		}
		paths := []string{}
		for _, f := range p.Patch.Files {
			paths = append(paths, f.Path)
		}
		if len(paths) > 0 {
			anyChange = true
			terminal.Outputs = append(terminal.Outputs, p.Artifact)
		}
		checks, _ := json.Marshal(derived[i])
		if r.Checks.Mode != "none" && !derived[i].NoApplicableChecks && derived[i].Status != execution.CheckPass {
			allChecksSatisfied = false
		}
		if len(paths) == 0 {
			p.Patch.PatchDigest = sha256.Sum256(nil)
		}
		g.Repositories = append(g.Repositories, domain.RepositoryResultChange{RepoID: r.RepoID, BaseCommit: r.BaseCommit, BaseTree: r.BaseTree, PatchDigest: fmt.Sprintf("%x", p.Patch.PatchDigest), PatchLocator: p.Artifact.Locator, ChangedPaths: paths, Checks: checks})
	}
	if !anyChange {
		terminal.Kind = execution.TerminalChecksIncomplete
		for _, checks := range derived {
			if checks.Status == execution.CheckFail {
				terminal.Kind = execution.TerminalChecksFailed
			}
		}
		terminal.FailureReason = string(terminal.Kind)
		if allChecksSatisfied {
			terminal.Kind = execution.TerminalCompletedNoChange
			terminal.FailureReason = ""
		}
		g.Outcome = string(terminal.Kind)
	}
	return terminal, g, nil
}
