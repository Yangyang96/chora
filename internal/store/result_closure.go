package store

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

// ResultClosure ends only the listed entries' future delivery eligibility.
// It never substitutes for a Review or changes an existing delivery fact.
type ResultClosure struct {
	ResultID           domain.ResultID
	RunID              domain.RunID
	TaskID             domain.TaskID
	AttemptID          domain.AttemptID
	ResultDigest       [32]byte
	PreviewDigest      [32]byte
	ReviewID           string
	RepositoryIDs      []string // An empty ID denotes the retained scalar Result contract.
	ActorID, SessionID string
	CreatedAt          time.Time
}

type ResultClosureReader interface {
	GetResultClosure(context.Context, domain.ResultID) (ResultClosure, error)
	ListResultClosuresForRun(context.Context, domain.RunID) ([]ResultClosure, error)
}

type ResultClosureWriter interface {
	InsertResultClosure(context.Context, ResultClosure) error
}

// TerminalResultForClosure proves a scalar no-change Result from its canonical
// terminal event. Such results have no patch and therefore no local-review row.
func TerminalResultForClosure(ctx context.Context, reader Reader, run domain.AgentRun, attempt domain.Attempt) (domain.ResultID, [32]byte, error) {
	fail := func(err error) (domain.ResultID, [32]byte, error) { return domain.ResultID{}, [32]byte{}, err }
	if attempt.AgentExecutionProfileBinding().Profile() != domain.AgentExecutionProfileTrustedLocal || attempt.RunID() != run.ID() || (run.State() != domain.RunStateCompleted && run.State() != domain.RunStateRecoveryRequired) {
		return fail(ErrNotFound)
	}
	binding, err := reader.GetSpecCodingBinding(ctx, run.TaskID())
	if err != nil {
		return fail(err)
	}
	if binding.Status != SpecCodingRegistered || sha256.Sum256(binding.ActiveContractJSON) != binding.ActiveContractDigest || binding.SnapshotDigest != attempt.ContextDigest() {
		return fail(ErrVerificationConflict)
	}
	report, err := reader.GetAgentReportForAttempt(ctx, attempt.ID())
	if err != nil {
		return fail(err)
	}
	events, err := reader.ListRunEvents(ctx, run.ID())
	if err != nil {
		return fail(err)
	}
	var found domain.ResultID
	var digest [32]byte
	for _, event := range events {
		if event.Type() != "run.completed_no_change" && event.Type() != "attempt.failed" {
			continue
		}
		var terminal struct {
			ResultID       string `json:"result_id"`
			AttemptID      string `json:"attempt_id"`
			ReportID       string `json:"agent_report_id"`
			Outcome        string `json:"outcome"`
			ContractDigest string `json:"contract_digest"`
			SnapshotDigest string `json:"context_snapshot_digest"`
		}
		if err := json.Unmarshal(event.NormalizedJSON(), &terminal); err != nil {
			return fail(err)
		}
		if terminal.AttemptID != attempt.ID().String() || terminal.ResultID == "" {
			continue
		}
		valid := run.State() == domain.RunStateCompleted && terminal.Outcome == "completed_no_change" && event.Type() == "run.completed_no_change" && attempt.State() == domain.AttemptStateOutputSubmitted
		valid = valid || run.State() == domain.RunStateRecoveryRequired && (terminal.Outcome == "checks_failed" || terminal.Outcome == "checks_incomplete") && event.Type() == "attempt.failed" && attempt.State() == domain.AttemptStateFailed
		if !valid || terminal.ReportID != report.ID().String() || report.RunID() != run.ID() || terminal.ContractDigest != hex.EncodeToString(binding.ActiveContractDigest[:]) || terminal.SnapshotDigest != hex.EncodeToString(binding.SnapshotDigest[:]) {
			return fail(ErrVerificationConflict)
		}
		id, err := domain.ParseResultID(terminal.ResultID)
		if err != nil || found.Valid() {
			return fail(ErrVerificationConflict)
		}
		found, digest = id, sha256.Sum256(event.NormalizedJSON())
	}
	if !found.Valid() {
		return fail(ErrNotFound)
	}
	return found, digest, nil
}
