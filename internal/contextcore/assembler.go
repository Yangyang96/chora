package contextcore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

type Assembler struct {
	port SnapshotPort
}

func NewAssembler(port SnapshotPort) *Assembler {
	return &Assembler{port: port}
}

func (assembler *Assembler) Assemble(ctx context.Context, request AssembleRequest) (Snapshot, error) {
	if assembler == nil || assembler.port == nil || !validAssemblyRequest(request) {
		return Snapshot{}, ErrInvalidAssembly
	}
	pinnedIDs := request.Charter.ContextRevisionIDs()
	revisions, err := assembler.port.LookupRevisions(ctx, request.Task.RoomID(), append([]domain.ContextRevisionID(nil), pinnedIDs...))
	if err != nil {
		return Snapshot{}, fmt.Errorf("lookup context revisions: %w", err)
	}
	ordered, err := orderPinnedRevisions(request.Task.RoomID(), pinnedIDs, revisions)
	if err != nil {
		return Snapshot{}, err
	}
	document, included, excluded, err := buildDocument(request, ordered)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot, err := newSnapshot(request.SnapshotID, document, included, excluded, request.Selection)
	if err != nil {
		return Snapshot{}, err
	}
	if err := assembler.port.SaveSnapshot(ctx, snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("save context snapshot: %w", err)
	}
	return snapshot, nil
}

func validAssemblyRequest(request AssembleRequest) bool {
	if !request.SnapshotID.Valid() || !request.Task.ID().Valid() || !request.Task.RoomID().Valid() || !request.Charter.ID().Valid() || request.Charter.TaskID() != request.Task.ID() || request.Charter.TaskGoal() != request.Task.Goal() {
		return false
	}
	taskCriteria := request.Task.Criteria()
	charterCriteria := request.Charter.Criteria()
	if len(charterCriteria) == 0 || !criteriaEqual(taskCriteria, charterCriteria) || len(request.Charter.ContextRevisionIDs()) == 0 {
		return false
	}
	if request.Delta == nil {
		return validSelection(request)
	}
	return validSelection(request) && request.Delta.PredecessorSnapshotID.Valid() && request.Delta.PredecessorSnapshotID != request.SnapshotID && request.Delta.PredecessorDigest != ([32]byte{}) && strings.TrimSpace(request.Delta.Reason) != "" && strings.TrimSpace(request.Delta.Instructions) != ""
}

func validSelection(request AssembleRequest) bool {
	if request.Selection == nil {
		return true
	}
	selection := *request.Selection
	if selection.TaskID() != request.Task.ID() || selection.RoomID() != request.Task.RoomID() {
		return false
	}
	selected := selection.SelectedRevisionIDs()
	pinned := request.Charter.ContextRevisionIDs()
	if len(selected) != len(pinned) {
		return false
	}
	for index := range selected {
		if selected[index] != pinned[index] {
			return false
		}
	}
	return true
}

func criteriaEqual(taskCriteria, charterCriteria []domain.AcceptanceCriterion) bool {
	if len(taskCriteria) != len(charterCriteria) {
		return false
	}
	for index := range taskCriteria {
		if taskCriteria[index].ID() != charterCriteria[index].ID() || taskCriteria[index].Title() != charterCriteria[index].Title() || taskCriteria[index].Description() != charterCriteria[index].Description() {
			return false
		}
	}
	return true
}

func orderPinnedRevisions(roomID domain.RoomID, pinnedIDs []domain.ContextRevisionID, revisions []domain.RoomContextRevision) ([]domain.RoomContextRevision, error) {
	if len(revisions) != len(pinnedIDs) {
		return nil, ErrRevisionSetMismatch
	}
	byID := make(map[domain.ContextRevisionID]domain.RoomContextRevision, len(revisions))
	entries := make(map[domain.ContextEntryID]struct{}, len(revisions))
	for _, revision := range revisions {
		if !revision.RevisionID().Valid() || revision.RoomID() != roomID {
			return nil, ErrRevisionSetMismatch
		}
		if _, duplicate := byID[revision.RevisionID()]; duplicate {
			return nil, ErrRevisionSetMismatch
		}
		if _, ambiguous := entries[revision.EntryID()]; ambiguous {
			return nil, ErrRevisionSetMismatch
		}
		byID[revision.RevisionID()] = revision
		entries[revision.EntryID()] = struct{}{}
	}
	ordered := make([]domain.RoomContextRevision, 0, len(pinnedIDs))
	seenPins := make(map[domain.ContextRevisionID]struct{}, len(pinnedIDs))
	for _, id := range pinnedIDs {
		if _, duplicate := seenPins[id]; duplicate {
			return nil, ErrRevisionSetMismatch
		}
		seenPins[id] = struct{}{}
		revision, exists := byID[id]
		if !exists {
			return nil, ErrRevisionSetMismatch
		}
		ordered = append(ordered, revision)
	}
	return ordered, nil
}

func buildDocument(request AssembleRequest, revisions []domain.RoomContextRevision) (canonicalDocument, []domain.ContextRevisionID, []ExcludedRevision, error) {
	document := canonicalDocument{
		SchemaVersion: schemaVersionV1,
		Task:          canonicalTask{ID: request.Task.ID().String(), RoomID: request.Task.RoomID().String(), Title: request.Task.Title(), Goal: request.Task.Goal(), Briefs: []canonicalRevision{}},
		Charter: canonicalCharter{
			ID: request.Charter.ID().String(), TaskID: request.Charter.TaskID().String(), TaskGoal: request.Charter.TaskGoal(), WorkspaceRoot: request.Charter.WorkspaceRoot(),
			AdapterID: request.Charter.AdapterID(), SandboxMode: request.Charter.SandboxMode(), ExpectedOutput: request.Charter.ExpectedOutput(), ResponsibleHuman: request.Charter.ResponsibleHuman(),
			Capabilities: canonicalCapabilities(request.Charter.CapabilityEnvelope()), Initiator: request.Charter.Initiator(), CreatedAt: request.Charter.CreatedAt().UTC().Format(time.RFC3339Nano),
			ConfirmedSensitiveRevisionIDs: contextRevisionIDStrings(request.Charter.ConfirmedSensitiveRevisionIDs()), SensitiveExclusions: contextEntryIDStrings(request.Charter.SensitiveExclusions()),
		},
		AcceptanceCriteria: canonicalCriteria(request.Charter.Criteria()), Decisions: []canonicalRevision{}, Constraints: []canonicalRevision{}, References: []canonicalRevision{}, Unknowns: []canonicalRevision{}, Excluded: []canonicalExcludedRevision{},
	}
	if request.Selection != nil {
		document.SchemaVersion = schemaVersion
		selection := *request.Selection
		manifest := canonicalSelectionManifest{TaskID: selection.TaskID().String(), RoomID: selection.RoomID().String(), CreatedAt: selection.CreatedAt().UTC().Format(time.RFC3339Nano), Selected: []canonicalSelectionItem{}, Excluded: []canonicalSelectionItem{}}
		for _, item := range selection.Selected() {
			manifest.Selected = append(manifest.Selected, canonicalSelectionItem{RevisionID: item.RevisionID.String(), Digest: fmt.Sprintf("%x", item.Digest), Provenance: item.Provenance})
		}
		for _, item := range selection.Excluded() {
			manifest.Excluded = append(manifest.Excluded, canonicalSelectionItem{RevisionID: item.RevisionID.String(), Digest: fmt.Sprintf("%x", item.Digest), Provenance: item.Provenance, Reason: item.Reason})
		}
		document.Selection = &manifest
	}
	if request.Delta != nil {
		document.Delta = &canonicalRetryDelta{PredecessorSnapshotID: request.Delta.PredecessorSnapshotID.String(), PredecessorDigest: fmt.Sprintf("%x", request.Delta.PredecessorDigest), Reason: request.Delta.Reason, Instructions: request.Delta.Instructions}
	}
	confirmed := revisionIDSet(request.Charter.ConfirmedSensitiveRevisionIDs())
	exclusions := entryIDSet(request.Charter.SensitiveExclusions())
	selectedRevisionIDs := make(map[domain.ContextRevisionID]struct{}, len(revisions))
	selectedEntryIDs := make(map[domain.ContextEntryID]struct{}, len(revisions))
	for _, revision := range revisions {
		selectedRevisionIDs[revision.RevisionID()] = struct{}{}
		selectedEntryIDs[revision.EntryID()] = struct{}{}
	}
	for id := range confirmed {
		if _, selected := selectedRevisionIDs[id]; !selected {
			return canonicalDocument{}, nil, nil, ErrSensitiveSelectionConflict
		}
	}
	for id := range exclusions {
		if _, selected := selectedEntryIDs[id]; !selected {
			return canonicalDocument{}, nil, nil, ErrSensitiveSelectionConflict
		}
	}

	included := make([]domain.ContextRevisionID, 0, len(revisions))
	excluded := make([]ExcludedRevision, 0)
	for _, revision := range revisions {
		_, isConfirmed := confirmed[revision.RevisionID()]
		_, isExcluded := exclusions[revision.EntryID()]
		if isConfirmed && (!revision.Sensitive() || isExcluded) {
			return canonicalDocument{}, nil, nil, ErrSensitiveSelectionConflict
		}
		if isExcluded || revision.Sensitive() && !isConfirmed {
			reason := "charter_exclusion"
			if revision.Sensitive() && !isConfirmed {
				reason = "sensitive_not_confirmed"
			}
			excluded = append(excluded, ExcludedRevision{entryID: revision.EntryID(), revisionID: revision.RevisionID(), reason: reason})
			document.Excluded = append(document.Excluded, canonicalExcludedRevision{EntryID: revision.EntryID().String(), RevisionID: revision.RevisionID().String(), Reason: reason})
			continue
		}
		included = append(included, revision.RevisionID())
		canonical := canonicalizeRevision(revision)
		switch revision.Kind() {
		case domain.ContextKindBrief:
			document.Task.Briefs = append(document.Task.Briefs, canonical)
		case domain.ContextKindDecision:
			document.Decisions = append(document.Decisions, canonical)
		case domain.ContextKindConstraint:
			document.Constraints = append(document.Constraints, canonical)
		case domain.ContextKindSourceRef:
			document.References = append(document.References, canonical)
		case domain.ContextKindUnknown:
			document.Unknowns = append(document.Unknowns, canonical)
		default:
			return canonicalDocument{}, nil, nil, ErrInvalidAssembly
		}
	}
	return document, included, excluded, nil
}

func canonicalCapabilities(capabilities domain.CapabilityEnvelope) []canonicalCapability {
	names := make([]string, 0, len(capabilities))
	for name := range capabilities {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]canonicalCapability, 0, len(names))
	for _, name := range names {
		result = append(result, canonicalCapability{Name: name, Allowed: capabilities[name]})
	}
	return result
}

func canonicalCriteria(criteria []domain.AcceptanceCriterion) []canonicalCriterion {
	result := make([]canonicalCriterion, 0, len(criteria))
	for _, criterion := range criteria {
		result = append(result, canonicalCriterion{ID: criterion.ID().String(), Title: criterion.Title(), Description: criterion.Description()})
	}
	return result
}

func contextRevisionIDStrings(ids []domain.ContextRevisionID) []string {
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		result = append(result, id.String())
	}
	return result
}

func contextEntryIDStrings(ids []domain.ContextEntryID) []string {
	result := make([]string, 0, len(ids))
	for _, id := range ids {
		result = append(result, id.String())
	}
	return result
}

func canonicalizeRevision(revision domain.RoomContextRevision) canonicalRevision {
	return canonicalRevision{EntryID: revision.EntryID().String(), RevisionID: revision.RevisionID().String(), RevisionNumber: revision.RevisionNumber(), Title: revision.Title(), Body: revision.Body(), Locator: revision.Locator(), Sensitive: revision.Sensitive()}
}

func revisionIDSet(ids []domain.ContextRevisionID) map[domain.ContextRevisionID]struct{} {
	set := make(map[domain.ContextRevisionID]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}

func entryIDSet(ids []domain.ContextEntryID) map[domain.ContextEntryID]struct{} {
	set := make(map[domain.ContextEntryID]struct{}, len(ids))
	for _, id := range ids {
		set[id] = struct{}{}
	}
	return set
}
