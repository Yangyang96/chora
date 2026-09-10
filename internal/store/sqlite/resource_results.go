package sqlite

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func (tx *writeTx) InsertResourceResultGroup(ctx context.Context, g domain.ResourceResultGroup) error {
	b, d, e := g.CanonicalJSON()
	if e != nil {
		return e
	}
	runID, _ := domain.ParseRunID(g.RunID)
	run, e := tx.GetRun(ctx, runID)
	if e != nil {
		return e
	}
	if run.TaskID().String() != g.TaskID {
		return storecontract.ErrVerificationConflict
	}
	attempt, e := tx.GetCurrentAttempt(ctx, runID)
	if e != nil {
		return e
	}
	if attempt.ID().String() != g.AttemptID {
		return storecontract.ErrVerificationConflict
	}
	report, e := tx.GetAgentReportForAttempt(ctx, attempt.ID())
	if e != nil {
		return e
	}
	if report.ID().String() != g.AgentReportID {
		return storecontract.ErrVerificationConflict
	}
	events, e := tx.ListRunEvents(ctx, runID)
	if e != nil {
		return e
	}
	final, finalText, complete := domain.CurrentCompleteAssistantEvidence(events)
	if !complete || final != g.FinalAssistant || finalText != report.FinalText() {
		return storecontract.ErrVerificationConflict
	}
	frozen, e := tx.GetTaskResourceSnapshot(ctx, run.TaskID())
	if e != nil {
		return e
	}
	if hex.EncodeToString(frozen.Digest[:]) != g.ResourceSnapshotDigest {
		return storecontract.ErrVerificationConflict
	}
	binding, e := tx.GetSpecCodingBinding(ctx, run.TaskID())
	if e != nil {
		return e
	}
	if hex.EncodeToString(binding.ActiveContractDigest[:]) != g.ContractDigest || hex.EncodeToString(binding.SnapshotDigest[:]) != g.ContextDigest {
		return storecontract.ErrVerificationConflict
	}
	var snapshot domain.TaskResourceSnapshot
	if e = json.Unmarshal(frozen.CanonicalJSON, &snapshot); e != nil {
		return e
	}
	if len(snapshot.Resources) != len(g.Repositories) {
		return storecontract.ErrVerificationConflict
	}
	for i, r := range g.Repositories {
		f := snapshot.Resources[i]
		if r.RepoID != f.RepoID || r.BaseCommit != f.BaseCommit || r.BaseTree != f.BaseTree {
			return storecontract.ErrVerificationConflict
		}
		for _, p := range r.ChangedPaths {
			if f.Role != "write" || !f.Scope.Allows(p) {
				return storecontract.ErrVerificationConflict
			}
		}
	}
	if e = tx.requireActiveRoomForRun(ctx, runID); e != nil {
		return e
	}
	_, e = tx.tx.ExecContext(ctx, `INSERT INTO resource_result_groups(result_id,run_id,task_id,attempt_id,canonical_json,digest,created_at) VALUES(?,?,?,?,?,?,?)`, g.ID, g.RunID, g.TaskID, g.AttemptID, b, d[:], timeText(g.CreatedAt))
	if e != nil {
		return e
	}
	for _, r := range g.Repositories {
		var checks struct {
			Checks      []json.RawMessage
			Invocations []json.RawMessage
		}
		if err := json.Unmarshal(r.Checks, &checks); err != nil {
			return err
		}
		invocations := checks.Invocations
		if invocations == nil {
			invocations = checks.Checks
		}
		seenCalls := map[string]bool{}
		for _, invocation := range invocations {
			var observed struct{ ToolCallID string }
			if err := json.Unmarshal(invocation, &observed); err != nil {
				return err
			}
			if observed.ToolCallID == "" || seenCalls[observed.ToolCallID] {
				continue
			}
			seenCalls[observed.ToolCallID] = true
			if _, err := tx.tx.ExecContext(ctx, `INSERT INTO check_invocations(attempt_id,repo_id,tool_call_id,sequence,canonical_json,created_at) VALUES(?,?,?,1,?,?)`, g.AttemptID, r.RepoID, observed.ToolCallID, []byte(invocation), timeText(g.CreatedAt)); err != nil {
				return err
			}
		}
		raw, _ := json.Marshal(r)
		digest := sha256.Sum256(raw)
		if _, e = tx.tx.ExecContext(ctx, `INSERT INTO result_repository_changes(result_id,repo_id,canonical_json,digest,created_at) VALUES(?,?,?,?,?)`, g.ID, r.RepoID, raw, digest[:], timeText(g.CreatedAt)); e != nil {
			return e
		}
	}
	return nil
}
func (r *reader) GetResourceResultGroup(ctx context.Context, id domain.AttemptID) (domain.ResourceResultGroup, error) {
	var raw, digest []byte
	var result domain.ResourceResultGroup
	e := r.q.QueryRowContext(ctx, `SELECT canonical_json,digest FROM resource_result_groups WHERE attempt_id=?`, id.String()).Scan(&raw, &digest)
	if errors.Is(e, sql.ErrNoRows) {
		return result, storecontract.ErrNotFound
	}
	if e != nil {
		return result, e
	}
	if e = json.Unmarshal(raw, &result); e != nil {
		return result, e
	}
	canonical, d, e := result.CanonicalJSON()
	if e != nil {
		return result, e
	}
	if string(canonical) != string(raw) || string(d[:]) != string(digest) || result.AttemptID != id.String() {
		return domain.ResourceResultGroup{}, storecontract.ErrVerificationConflict
	}
	return result, nil
}
func (tx *writeTx) InsertResourceResultReview(ctx context.Context, result domain.ResultID, digest [32]byte, review domain.ReviewDecisionID) error {
	decision, e := tx.GetReview(ctx, review)
	if e != nil {
		return e
	}
	var stored []byte
	var run string
	if e = tx.tx.QueryRowContext(ctx, `SELECT digest,run_id FROM resource_result_groups WHERE result_id=?`, result.String()).Scan(&stored, &run); e != nil {
		return e
	}
	if string(stored) != string(digest[:]) || run != decision.RunID().String() {
		return storecontract.ErrVerificationConflict
	}
	_, e = tx.tx.ExecContext(ctx, `INSERT INTO resource_result_reviews(result_id,result_digest,review_id) VALUES(?,?,?)`, result.String(), digest[:], review.String())
	return e
}
func (r *reader) GetResourceResultReview(ctx context.Context, result domain.ResultID) (domain.ReviewDecision, [32]byte, error) {
	var text string
	var raw []byte
	var digest [32]byte
	e := r.q.QueryRowContext(ctx, `SELECT review_id,result_digest FROM resource_result_reviews WHERE result_id=?`, result.String()).Scan(&text, &raw)
	if errors.Is(e, sql.ErrNoRows) {
		e = storecontract.ErrNotFound
	}
	if e != nil {
		return domain.ReviewDecision{}, digest, e
	}
	id, e := domain.ParseReviewDecisionID(text)
	if e != nil {
		return domain.ReviewDecision{}, digest, e
	}
	if len(raw) != 32 {
		return domain.ReviewDecision{}, digest, storecontract.ErrVerificationConflict
	}
	copy(digest[:], raw)
	d, e := r.GetReview(ctx, id)
	return d, digest, e
}
