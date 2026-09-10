package contextcore

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

var (
	ErrInvalidAssembly            = errors.New("invalid context assembly")
	ErrRevisionSetMismatch        = errors.New("context revision set does not match charter")
	ErrSensitiveSelectionConflict = errors.New("sensitive context selection conflicts with charter")
	ErrInvalidPromotion           = errors.New("invalid reviewed context promotion")
	ErrPromotionUnauthorized      = errors.New("context promotion is not device-owner authorized")
	ErrPromotionInFlight          = errors.New("context promotion authorization is already in flight")
	ErrPromotionRejected          = errors.New("context promotion was rejected by persisted validation")
	ErrPromotionRetryable         = errors.New("context promotion was not applied and may be retried")
	ErrPromotionCommitUnknown     = errors.New("context promotion commit state is unknown")
	ErrInvalidAuthorizationID     = errors.New("invalid promotion authorization ID")
)

const (
	schemaVersionV1 = "chora.context.snapshot/v1"
	schemaVersion   = "chora.context.snapshot/v2"
)

type AssembleRequest struct {
	SnapshotID domain.ContextSnapshotID
	Task       domain.Task
	Charter    domain.RunCharter
	Delta      *RetryDelta
	Selection  *domain.TaskRevisionSelection
}

type RetryDelta struct {
	PredecessorSnapshotID domain.ContextSnapshotID
	PredecessorDigest     [32]byte
	Reason                string
	Instructions          string
}

type Snapshot struct {
	id                  domain.ContextSnapshotID
	digest              [32]byte
	canonicalJSON       []byte
	markdown            []byte
	includedRevisionIDs []domain.ContextRevisionID
	excluded            []ExcludedRevision
	domainSnapshot      domain.RunContextSnapshot
	selection           *domain.TaskRevisionSelection
}

type ExcludedRevisionRecord struct {
	EntryID    domain.ContextEntryID
	RevisionID domain.ContextRevisionID
	Reason     string
}
type SnapshotRecord struct {
	ID                      domain.ContextSnapshotID
	Digest                  [32]byte
	CanonicalJSON, Markdown []byte
	IncludedRevisionIDs     []domain.ContextRevisionID
	Excluded                []ExcludedRevisionRecord
	Selection               *domain.TaskRevisionSelection
}

func RestoreSnapshot(record SnapshotRecord) (Snapshot, error) {
	if !record.ID.Valid() || record.Digest == ([32]byte{}) || len(record.CanonicalJSON) == 0 || sha256.Sum256(record.CanonicalJSON) != record.Digest || len(record.Markdown) == 0 {
		return Snapshot{}, ErrInvalidAssembly
	}
	var document canonicalDocument
	decoder := json.NewDecoder(bytes.NewReader(record.CanonicalJSON))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return Snapshot{}, ErrInvalidAssembly
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return Snapshot{}, ErrInvalidAssembly
	}
	canonical, err := json.Marshal(document)
	if err != nil || !bytes.Equal(canonical, record.CanonicalJSON) || (document.SchemaVersion != schemaVersionV1 && document.SchemaVersion != schemaVersion) || !bytes.Equal(renderMarkdown(document), record.Markdown) {
		return Snapshot{}, ErrInvalidAssembly
	}
	if document.SchemaVersion == schemaVersionV1 {
		if document.Selection != nil || record.Selection != nil {
			return Snapshot{}, ErrInvalidAssembly
		}
	} else if record.Selection == nil || !selectionMatches(*record.Selection, document.Selection) {
		return Snapshot{}, ErrInvalidAssembly
	}
	included, canonicalExcluded, err := snapshotRows(document)
	if err != nil || len(included) != len(record.IncludedRevisionIDs) || len(canonicalExcluded) != len(record.Excluded) {
		return Snapshot{}, ErrInvalidAssembly
	}
	for i, id := range included {
		if id != record.IncludedRevisionIDs[i] {
			return Snapshot{}, ErrInvalidAssembly
		}
	}
	excluded := make([]ExcludedRevision, 0, len(record.Excluded))
	excludedEntries := make([]domain.ContextEntryID, 0, len(record.Excluded))
	for i, value := range record.Excluded {
		if !value.EntryID.Valid() || !value.RevisionID.Valid() || value.Reason == "" {
			return Snapshot{}, ErrInvalidAssembly
		}
		if value != canonicalExcluded[i] {
			return Snapshot{}, ErrInvalidAssembly
		}
		excluded = append(excluded, ExcludedRevision{entryID: value.EntryID, revisionID: value.RevisionID, reason: value.Reason})
		excludedEntries = append(excludedEntries, value.EntryID)
	}
	domainSnapshot, err := domain.NewRunContextSnapshot(record.ID, record.Digest, record.IncludedRevisionIDs, excludedEntries)
	if err != nil {
		return Snapshot{}, ErrInvalidAssembly
	}
	var selection *domain.TaskRevisionSelection
	if record.Selection != nil {
		value := *record.Selection
		selection = &value
	}
	return Snapshot{id: record.ID, digest: record.Digest, canonicalJSON: append([]byte(nil), record.CanonicalJSON...), markdown: append([]byte(nil), record.Markdown...), includedRevisionIDs: append([]domain.ContextRevisionID(nil), record.IncludedRevisionIDs...), excluded: excluded, domainSnapshot: domainSnapshot, selection: selection}, nil
}

func snapshotRows(document canonicalDocument) ([]domain.ContextRevisionID, []ExcludedRevisionRecord, error) {
	var included []domain.ContextRevisionID
	groups := [][]canonicalRevision{document.Task.Briefs, document.Decisions, document.Constraints, document.References, document.Unknowns}
	for _, group := range groups {
		for _, revision := range group {
			id, err := domain.ParseContextRevisionID(revision.RevisionID)
			if err != nil {
				return nil, nil, err
			}
			included = append(included, id)
		}
	}
	excluded := make([]ExcludedRevisionRecord, 0, len(document.Excluded))
	for _, value := range document.Excluded {
		entryID, err := domain.ParseContextEntryID(value.EntryID)
		if err != nil {
			return nil, nil, err
		}
		revisionID, err := domain.ParseContextRevisionID(value.RevisionID)
		if err != nil {
			return nil, nil, err
		}
		excluded = append(excluded, ExcludedRevisionRecord{EntryID: entryID, RevisionID: revisionID, Reason: value.Reason})
	}
	return included, excluded, nil
}

func (snapshot Snapshot) ID() domain.ContextSnapshotID { return snapshot.id }
func (snapshot Snapshot) Digest() [32]byte             { return snapshot.digest }
func (snapshot Snapshot) CanonicalJSON() []byte {
	return append([]byte(nil), snapshot.canonicalJSON...)
}
func (snapshot Snapshot) Markdown() []byte { return append([]byte(nil), snapshot.markdown...) }
func (snapshot Snapshot) IncludedRevisionIDs() []domain.ContextRevisionID {
	return append([]domain.ContextRevisionID(nil), snapshot.includedRevisionIDs...)
}
func (snapshot Snapshot) Excluded() []ExcludedRevision {
	return append([]ExcludedRevision(nil), snapshot.excluded...)
}
func (snapshot Snapshot) DomainSnapshot() domain.RunContextSnapshot { return snapshot.domainSnapshot }
func (snapshot Snapshot) Selection() (domain.TaskRevisionSelection, bool) {
	if snapshot.selection == nil {
		return domain.TaskRevisionSelection{}, false
	}
	return *snapshot.selection, true
}

type ExcludedRevision struct {
	entryID    domain.ContextEntryID
	revisionID domain.ContextRevisionID
	reason     string
}

func (excluded ExcludedRevision) EntryID() domain.ContextEntryID       { return excluded.entryID }
func (excluded ExcludedRevision) RevisionID() domain.ContextRevisionID { return excluded.revisionID }
func (excluded ExcludedRevision) Reason() string                       { return excluded.reason }

type canonicalDocument struct {
	SchemaVersion      string                      `json:"schema_version"`
	Task               canonicalTask               `json:"task"`
	Charter            canonicalCharter            `json:"charter"`
	AcceptanceCriteria []canonicalCriterion        `json:"acceptance_criteria"`
	Decisions          []canonicalRevision         `json:"decisions"`
	Constraints        []canonicalRevision         `json:"constraints"`
	References         []canonicalRevision         `json:"references"`
	Unknowns           []canonicalRevision         `json:"unknowns"`
	Excluded           []canonicalExcludedRevision `json:"excluded"`
	Delta              *canonicalRetryDelta        `json:"delta"`
	Selection          *canonicalSelectionManifest `json:"selection,omitempty"`
}

type canonicalTask struct {
	ID     string              `json:"id"`
	RoomID string              `json:"room_id"`
	Title  string              `json:"title"`
	Goal   string              `json:"goal"`
	Briefs []canonicalRevision `json:"briefs"`
}

type canonicalCharter struct {
	ID                            string                `json:"id"`
	TaskID                        string                `json:"task_id"`
	TaskGoal                      string                `json:"task_goal"`
	WorkspaceRoot                 string                `json:"workspace_root"`
	AdapterID                     string                `json:"adapter_id"`
	SandboxMode                   string                `json:"sandbox_mode"`
	ExpectedOutput                string                `json:"expected_output"`
	ResponsibleHuman              string                `json:"responsible_human"`
	Capabilities                  []canonicalCapability `json:"capabilities"`
	Initiator                     string                `json:"initiator"`
	CreatedAt                     string                `json:"created_at"`
	ConfirmedSensitiveRevisionIDs []string              `json:"confirmed_sensitive_revision_ids"`
	SensitiveExclusions           []string              `json:"sensitive_exclusions"`
}

type canonicalCapability struct {
	Name    string `json:"name"`
	Allowed bool   `json:"allowed"`
}

type canonicalCriterion struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
}

type canonicalRevision struct {
	EntryID        string `json:"entry_id"`
	RevisionID     string `json:"revision_id"`
	RevisionNumber int    `json:"revision_number"`
	Title          string `json:"title"`
	Body           string `json:"body"`
	Locator        string `json:"locator"`
	Sensitive      bool   `json:"sensitive"`
}

type canonicalExcludedRevision struct {
	EntryID    string `json:"entry_id"`
	RevisionID string `json:"revision_id"`
	Reason     string `json:"reason"`
}

type canonicalRetryDelta struct {
	PredecessorSnapshotID string `json:"predecessor_snapshot_id"`
	PredecessorDigest     string `json:"predecessor_digest"`
	Reason                string `json:"reason"`
	Instructions          string `json:"instructions"`
}

type canonicalSelectionManifest struct {
	TaskID    string                   `json:"task_id"`
	RoomID    string                   `json:"room_id"`
	CreatedAt string                   `json:"created_at"`
	Selected  []canonicalSelectionItem `json:"selected"`
	Excluded  []canonicalSelectionItem `json:"excluded"`
}

type canonicalSelectionItem struct {
	RevisionID string                    `json:"revision_id"`
	Digest     string                    `json:"digest"`
	Provenance domain.RevisionProvenance `json:"provenance"`
	Reason     string                    `json:"reason,omitempty"`
}

func selectionMatches(selection domain.TaskRevisionSelection, canonical *canonicalSelectionManifest) bool {
	if canonical == nil || canonical.TaskID != selection.TaskID().String() || canonical.RoomID != selection.RoomID().String() || canonical.CreatedAt != selection.CreatedAt().UTC().Format(time.RFC3339Nano) {
		return false
	}
	selected := selection.Selected()
	excluded := selection.Excluded()
	if len(selected) != len(canonical.Selected) || len(excluded) != len(canonical.Excluded) {
		return false
	}
	for index, item := range selected {
		want := canonicalSelectionItem{RevisionID: item.RevisionID.String(), Digest: fmt.Sprintf("%x", item.Digest), Provenance: item.Provenance}
		if canonical.Selected[index] != want {
			return false
		}
	}
	for index, item := range excluded {
		want := canonicalSelectionItem{RevisionID: item.RevisionID.String(), Digest: fmt.Sprintf("%x", item.Digest), Provenance: item.Provenance, Reason: item.Reason}
		if canonical.Excluded[index] != want {
			return false
		}
	}
	return true
}
