package domain

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ResourceResultGroup freezes one Attempt and its ordered per-repository evidence.
// It intentionally has no scalar base or patch digest.
type ResourceResultGroup struct {
	SchemaVersion          string                   `json:"schemaVersion"`
	ID                     string                   `json:"id"`
	RunID                  string                   `json:"runId"`
	TaskID                 string                   `json:"taskId"`
	AttemptID              string                   `json:"attemptId"`
	AgentReportID          string                   `json:"agentReportId"`
	FinalAssistant         FinalAssistantEvidence   `json:"finalAssistant"`
	ResourceSnapshotDigest string                   `json:"resourceSnapshotDigest"`
	ContractDigest         string                   `json:"contractDigest"`
	ContextDigest          string                   `json:"contextDigest"`
	Outcome                string                   `json:"outcome"`
	Repositories           []RepositoryResultChange `json:"repositories"`
	CreatedAt              time.Time                `json:"createdAt"`
}
type RepositoryResultChange struct {
	RepoID       string          `json:"repoId"`
	BaseCommit   string          `json:"baseCommit"`
	BaseTree     string          `json:"baseTree"`
	PatchDigest  string          `json:"patchDigest"`
	PatchLocator string          `json:"patchLocator"`
	ChangedPaths []string        `json:"changedPaths"`
	Checks       json.RawMessage `json:"checks"`
}

// FinalAssistantEvidence binds the exact complete Provider response retained for
// this Attempt. It does not elevate the response's claims to verified checks.
type FinalAssistantEvidence struct {
	EventID    string `json:"eventId"`
	Sequence   int64  `json:"sequence"`
	TextDigest string `json:"textDigest"`
	Complete   bool   `json:"complete"`
}

func CurrentCompleteAssistantEvidence(events []RunEvent) (FinalAssistantEvidence, string, bool) {
	var evidence FinalAssistantEvidence
	text := ""
	started := false
	for _, event := range events {
		if event.Type() == "attempt.started" {
			evidence = FinalAssistantEvidence{}
			text = ""
			started = true
			continue
		}
		if !started || event.Source() != "adapter" || event.Type() != "assistant_message" {
			continue
		}
		evidence = FinalAssistantEvidence{}
		text = ""
		var payload struct {
			Text      string `json:"text"`
			Complete  bool   `json:"terminal_complete"`
			Truncated bool   `json:"truncated"`
		}
		if json.Unmarshal(event.NormalizedJSON(), &payload) != nil || !payload.Complete || payload.Truncated || strings.TrimSpace(payload.Text) == "" || len(payload.Text) > 64*1024 {
			continue
		}
		text = payload.Text
		digest := sha256.Sum256([]byte(text))
		evidence = FinalAssistantEvidence{EventID: event.ID().String(), Sequence: event.Sequence(), TextDigest: fmt.Sprintf("%x", digest), Complete: true}
	}
	return evidence, text, evidence.Complete
}

func (g ResourceResultGroup) CanonicalJSON() ([]byte, [32]byte, error) {
	bad := func() ([]byte, [32]byte, error) {
		return nil, [32]byte{}, fmt.Errorf("%w: resource Result group", ErrInvalidArgument)
	}
	if g.SchemaVersion != "chora.result-group.v2" || g.CreatedAt.IsZero() || len(g.Repositories) == 0 || len(g.Repositories) > TaskRepositoryLimit {
		return bad()
	}
	if _, e := ParseResultID(g.ID); e != nil {
		return bad()
	}
	if _, e := ParseRunID(g.RunID); e != nil {
		return bad()
	}
	if _, e := ParseTaskID(g.TaskID); e != nil {
		return bad()
	}
	if _, e := ParseAttemptID(g.AttemptID); e != nil {
		return bad()
	}
	if _, e := ParseAgentReportID(g.AgentReportID); e != nil {
		return bad()
	}
	if _, err := ParseEventID(g.FinalAssistant.EventID); err != nil || !g.FinalAssistant.Complete || g.FinalAssistant.Sequence <= 0 || !resourceHex(g.FinalAssistant.TextDigest, 64) {
		return bad()
	}
	if !resourceHex(g.ResourceSnapshotDigest, 64) || !resourceHex(g.ContractDigest, 64) || !resourceHex(g.ContextDigest, 64) {
		return bad()
	}
	switch g.Outcome {
	case "review_ready", "completed_no_change", "checks_failed", "checks_incomplete":
	default:
		return bad()
	}
	previous := ""
	total := 0
	for _, r := range g.Repositories {
		if _, e := ParseRepositoryID(r.RepoID); e != nil {
			return bad()
		}
		if r.RepoID <= previous || !resourceHex(r.BaseCommit, 40) || !resourceHex(r.BaseTree, 40) || !resourceHex(r.PatchDigest, 64) {
			return bad()
		}
		previous = r.RepoID
		if len(r.ChangedPaths) > 0 && (!validProjectRelativePath(r.PatchLocator, false) || len(r.Checks) == 0) {
			return bad()
		}
		seen := map[string]bool{}
		for _, p := range r.ChangedPaths {
			if !validProjectRelativePath(p, false) || seen[p] {
				return bad()
			}
			seen[p] = true
			total++
		}
		if total > TaskChangedEntryLimit || len(r.Checks) > RepositoryMetadataBytes || len(r.Checks) > 0 && !json.Valid(r.Checks) {
			return bad()
		}
	}
	b, e := json.Marshal(g)
	if len(b) > RepositoryMetadataBytes {
		return bad()
	}
	return b, sha256.Sum256(b), e
}
