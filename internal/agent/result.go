package agent

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/Yangyang96/chora/internal/execution"
)

const ResultSchemaVersion = "chora.agent-result.v1"

var ErrResultContractInvalid = execution.ErrResultContractInvalid

var relativeWorkspacePath = regexp.MustCompile(`^[A-Za-z0-9_-][A-Za-z0-9._-]*(/[A-Za-z0-9_-][A-Za-z0-9._-]*)*$`)

type resultDocument struct {
	SchemaVersion      string                      `json:"schema_version"`
	Summary            string                      `json:"summary"`
	ReviewReady        bool                        `json:"review_ready"`
	Outputs            []workspaceArtifact         `json:"outputs"`
	ArtifactCandidates []workspaceArtifact         `json:"artifact_candidates"`
	Checks             []declaredCheck             `json:"checks"`
	Unknowns           []string                    `json:"unknowns"`
	Handoff            handoffDocument             `json:"handoff"`
	ContextConsumption *contextConsumptionDocument `json:"context_consumption,omitempty"`
}

type contextConsumptionDocument struct {
	SnapshotID              string   `json:"snapshot_id"`
	SnapshotDigest          string   `json:"snapshot_digest"`
	CandidateRevisionID     string   `json:"candidate_revision_id"`
	IncludedRevisionIDs     []string `json:"included_revision_ids"`
	ExcludedRevisionIDs     []string `json:"excluded_revision_ids"`
	Derivation              string   `json:"derivation"`
	ExcludedContentObserved bool     `json:"excluded_content_observed"`
}

type workspaceArtifact struct {
	Locator     string `json:"locator"`
	Description string `json:"description"`
	SHA256      string `json:"sha256,omitempty"`
	MediaType   string `json:"media_type,omitempty"`
}

type declaredCheck struct {
	CriterionID string `json:"criterion_id"`
	Status      string `json:"status"`
	Evidence    string `json:"evidence"`
}

type handoffDocument struct {
	Requested bool   `json:"requested"`
	Reason    string `json:"reason"`
}

func DecodeResult(data []byte) (execution.TerminalResult, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var document resultDocument
	if err := decoder.Decode(&document); err != nil {
		return execution.TerminalResult{}, contractError(err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON documents")
		}
		return execution.TerminalResult{}, contractError(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return execution.TerminalResult{}, contractError(err)
	}
	for _, required := range []string{"schema_version", "summary", "review_ready", "outputs", "artifact_candidates", "checks", "unknowns", "handoff"} {
		value, ok := fields[required]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return execution.TerminalResult{}, contractError(fmt.Errorf("required field %s is missing or null", required))
		}
	}
	if err := requireObjectFields(fields["handoff"], "requested", "reason"); err != nil {
		return execution.TerminalResult{}, contractError(fmt.Errorf("handoff: %w", err))
	}
	if raw, ok := fields["context_consumption"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		if err := requireObjectFields(raw, "snapshot_id", "snapshot_digest", "candidate_revision_id", "included_revision_ids", "excluded_revision_ids", "derivation", "excluded_content_observed"); err != nil {
			return execution.TerminalResult{}, contractError(fmt.Errorf("context_consumption: %w", err))
		}
	}
	var rawChecks []json.RawMessage
	if err := json.Unmarshal(fields["checks"], &rawChecks); err != nil {
		return execution.TerminalResult{}, contractError(err)
	}
	for index, rawCheck := range rawChecks {
		if err := requireObjectFields(rawCheck, "criterion_id", "status", "evidence"); err != nil {
			return execution.TerminalResult{}, contractError(fmt.Errorf("check %d: %w", index, err))
		}
	}
	if err := validateDocument(document); err != nil {
		return execution.TerminalResult{}, contractError(err)
	}
	result := execution.TerminalResult{Summary: document.Summary}
	for _, artifact := range document.Outputs {
		result.Outputs = append(result.Outputs, execution.WorkspaceArtifact{Locator: artifact.Locator, Description: artifact.Description, SHA256: artifact.SHA256, MediaType: artifact.MediaType})
	}
	for _, artifact := range document.ArtifactCandidates {
		result.ArtifactCandidates = append(result.ArtifactCandidates, execution.WorkspaceArtifact{Locator: artifact.Locator, Description: artifact.Description, SHA256: artifact.SHA256, MediaType: artifact.MediaType})
	}
	for _, check := range document.Checks {
		result.Checks = append(result.Checks, execution.DeclaredCheck{CriterionID: check.CriterionID, Status: execution.CheckStatus(check.Status), Evidence: check.Evidence})
	}
	result.Unknowns = append([]string(nil), document.Unknowns...)
	if document.ContextConsumption != nil {
		result.ContextConsumption = &execution.ContextConsumption{
			SnapshotID: document.ContextConsumption.SnapshotID, SnapshotDigest: document.ContextConsumption.SnapshotDigest,
			CandidateRevisionID: document.ContextConsumption.CandidateRevisionID,
			IncludedRevisionIDs: append([]string(nil), document.ContextConsumption.IncludedRevisionIDs...),
			ExcludedRevisionIDs: append([]string(nil), document.ContextConsumption.ExcludedRevisionIDs...),
			Derivation:          document.ContextConsumption.Derivation, ExcludedContentObserved: document.ContextConsumption.ExcludedContentObserved,
		}
	}
	switch {
	case document.Handoff.Requested:
		result.Kind = execution.TerminalHandoffRequested
		result.Handoff = &execution.HandoffRequest{Reason: document.Handoff.Reason}
	case document.ReviewReady:
		result.Kind = execution.TerminalReviewReady
	default:
		result.Kind = execution.TerminalRevisionRequired
	}
	return result, nil
}

func requireObjectFields(data json.RawMessage, required ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, name := range required {
		if _, ok := fields[name]; !ok {
			return fmt.Errorf("required field %s is missing", name)
		}
	}
	return nil
}

func validateDocument(document resultDocument) error {
	if document.SchemaVersion != ResultSchemaVersion {
		return fmt.Errorf("unsupported schema version %q", document.SchemaVersion)
	}
	if strings.TrimSpace(document.Summary) == "" {
		return errors.New("summary is required")
	}
	if document.Handoff.Requested == document.ReviewReady {
		if document.Handoff.Requested {
			return errors.New("handoff cannot be review ready")
		}
	}
	if document.Handoff.Requested && strings.TrimSpace(document.Handoff.Reason) == "" {
		return errors.New("handoff reason is required")
	}
	if !document.Handoff.Requested && document.Handoff.Reason != "" {
		return errors.New("unrequested handoff reason must be empty")
	}
	seenLocators := make(map[string]struct{}, len(document.Outputs)+len(document.ArtifactCandidates))
	for _, artifact := range append(append([]workspaceArtifact(nil), document.Outputs...), document.ArtifactCandidates...) {
		if !relativeWorkspacePath.MatchString(artifact.Locator) || strings.TrimSpace(artifact.Description) == "" {
			return fmt.Errorf("invalid workspace artifact %q", artifact.Locator)
		}
		if (artifact.SHA256 == "") != (artifact.MediaType == "") || artifact.SHA256 != "" && (!validLowerSHA256(artifact.SHA256) || strings.TrimSpace(artifact.MediaType) != artifact.MediaType || strings.ContainsAny(artifact.MediaType, " \t\r\n")) {
			return fmt.Errorf("invalid workspace artifact identity %q", artifact.Locator)
		}
		if _, exists := seenLocators[artifact.Locator]; exists {
			return fmt.Errorf("duplicate workspace artifact locator %q", artifact.Locator)
		}
		seenLocators[artifact.Locator] = struct{}{}
	}
	seen := make(map[string]struct{}, len(document.Checks))
	for _, check := range document.Checks {
		if strings.TrimSpace(check.CriterionID) == "" {
			return errors.New("criterion id is required")
		}
		if _, exists := seen[check.CriterionID]; exists {
			return fmt.Errorf("duplicate criterion %q", check.CriterionID)
		}
		seen[check.CriterionID] = struct{}{}
		switch check.Status {
		case string(execution.CheckPass), string(execution.CheckFail), string(execution.CheckUnknown):
		default:
			return fmt.Errorf("invalid check status %q", check.Status)
		}
	}
	for _, unknown := range document.Unknowns {
		if strings.TrimSpace(unknown) == "" {
			return errors.New("unknown entry is empty")
		}
	}
	if document.ContextConsumption != nil {
		evidence := execution.ContextConsumption{
			SnapshotID: document.ContextConsumption.SnapshotID, SnapshotDigest: document.ContextConsumption.SnapshotDigest,
			CandidateRevisionID: document.ContextConsumption.CandidateRevisionID,
			IncludedRevisionIDs: document.ContextConsumption.IncludedRevisionIDs, ExcludedRevisionIDs: document.ContextConsumption.ExcludedRevisionIDs,
			Derivation: document.ContextConsumption.Derivation, ExcludedContentObserved: document.ContextConsumption.ExcludedContentObserved,
		}
		if !evidence.Valid() {
			return errors.New("invalid context consumption evidence")
		}
	}
	return nil
}

func validLowerSHA256(value string) bool {
	if len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}
	return true
}

func contractError(err error) error {
	return fmt.Errorf("%w: %v", ErrResultContractInvalid, err)
}
