package contextcore

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"

	"github.com/Yangyang96/chora/internal/domain"
)

func newSnapshot(id domain.ContextSnapshotID, document canonicalDocument, included []domain.ContextRevisionID, excluded []ExcludedRevision, selection *domain.TaskRevisionSelection) (Snapshot, error) {
	canonicalJSON, err := json.Marshal(document)
	if err != nil {
		return Snapshot{}, fmt.Errorf("render canonical context JSON: %w", err)
	}
	digest := sha256.Sum256(canonicalJSON)
	canonicalIncluded, _, err := snapshotRows(document)
	if err != nil || !sameRevisionIDSet(canonicalIncluded, included) {
		return Snapshot{}, ErrInvalidAssembly
	}
	included = canonicalIncluded
	excludedEntryIDs := make([]domain.ContextEntryID, 0, len(excluded))
	for _, item := range excluded {
		excludedEntryIDs = append(excludedEntryIDs, item.EntryID())
	}
	domainSnapshot, err := domain.NewRunContextSnapshot(id, digest, included, excludedEntryIDs)
	if err != nil {
		return Snapshot{}, fmt.Errorf("create domain context snapshot: %w", err)
	}
	var frozenSelection *domain.TaskRevisionSelection
	if selection != nil {
		value := *selection
		frozenSelection = &value
	}
	return Snapshot{
		id: id, digest: digest, canonicalJSON: append([]byte(nil), canonicalJSON...), markdown: renderMarkdown(document),
		includedRevisionIDs: append([]domain.ContextRevisionID(nil), included...), excluded: append([]ExcludedRevision(nil), excluded...), domainSnapshot: domainSnapshot, selection: frozenSelection,
	}, nil
}

func sameRevisionIDSet(left, right []domain.ContextRevisionID) bool {
	if len(left) != len(right) {
		return false
	}
	seen := make(map[domain.ContextRevisionID]int, len(left))
	for _, id := range left {
		seen[id]++
	}
	for _, id := range right {
		seen[id]--
		if seen[id] < 0 {
			return false
		}
	}
	return true
}

func renderMarkdown(document canonicalDocument) []byte {
	var buffer bytes.Buffer
	buffer.WriteString("# Context Snapshot\n\n## Task\n\n")
	fmt.Fprintf(&buffer, "- ID: `%s`\n- Room: `%s`\n- Title: %s\n- Goal: %s\n\n### Briefs\n\n", document.Task.ID, document.Task.RoomID, document.Task.Title, document.Task.Goal)
	renderRevisionList(&buffer, document.Task.Briefs)
	renderRunCharter(&buffer, document.Charter)
	buffer.WriteString("\n## Acceptance Criteria\n\n")
	for index, criterion := range document.AcceptanceCriteria {
		fmt.Fprintf(&buffer, "%d. **%s** (`%s`): %s\n", index+1, criterion.Title, criterion.ID, criterion.Description)
	}
	renderSection(&buffer, "Decisions", document.Decisions)
	renderSection(&buffer, "Constraints", document.Constraints)
	renderSection(&buffer, "References", document.References)
	renderSection(&buffer, "Unknowns", document.Unknowns)
	if document.Selection != nil {
		buffer.WriteString("\n## Explicit Revision Selection\n\n### Selected\n\n")
		renderSelectionItems(&buffer, document.Selection.Selected)
		buffer.WriteString("\n### Excluded\n\n")
		renderSelectionItems(&buffer, document.Selection.Excluded)
	}
	buffer.WriteString("\n## Excluded\n\n")
	if len(document.Excluded) == 0 {
		buffer.WriteString("None.\n")
	} else {
		for _, excluded := range document.Excluded {
			fmt.Fprintf(&buffer, "- Entry `%s`, revision `%s`: %s\n", excluded.EntryID, excluded.RevisionID, excluded.Reason)
		}
	}
	buffer.WriteString("\n## Delta\n\n")
	if document.Delta == nil {
		buffer.WriteString("None.\n")
	} else {
		fmt.Fprintf(&buffer, "- Predecessor snapshot: `%s`\n- Predecessor digest: `%s`\n- Reason: %s\n- Instructions: %s\n", document.Delta.PredecessorSnapshotID, document.Delta.PredecessorDigest, document.Delta.Reason, document.Delta.Instructions)
	}
	return buffer.Bytes()
}

func renderSelectionItems(buffer *bytes.Buffer, items []canonicalSelectionItem) {
	if len(items) == 0 {
		buffer.WriteString("None.\n")
		return
	}
	for _, item := range items {
		fmt.Fprintf(buffer, "- Revision `%s`, digest `%s`, provenance `%s`", item.RevisionID, item.Digest, item.Provenance.Kind)
		if item.Reason != "" {
			fmt.Fprintf(buffer, ": %s", item.Reason)
		}
		buffer.WriteByte('\n')
	}
}

func renderRunCharter(buffer *bytes.Buffer, charter canonicalCharter) {
	buffer.WriteString("\n## Run Charter\n\n")
	fmt.Fprintf(buffer, "- ID: `%s`\n- Task ID: `%s`\n- Task Goal: %s\n- Workspace Root: `%s`\n- Adapter ID: `%s`\n- Sandbox Mode: `%s`\n- Expected Output: %s\n- Responsible Human: %s\n- Initiator: %s\n- Created At: `%s`\n- Confirmed Sensitive Revision IDs:\n", charter.ID, charter.TaskID, charter.TaskGoal, charter.WorkspaceRoot, charter.AdapterID, charter.SandboxMode, charter.ExpectedOutput, charter.ResponsibleHuman, charter.Initiator, charter.CreatedAt)
	renderCharterIDs(buffer, charter.ConfirmedSensitiveRevisionIDs)
	buffer.WriteString("- Sensitive Exclusions:\n")
	renderCharterIDs(buffer, charter.SensitiveExclusions)
	buffer.WriteString("- Capabilities:\n")
	for _, capability := range charter.Capabilities {
		status := "denied"
		if capability.Allowed {
			status = "allowed"
		}
		fmt.Fprintf(buffer, "  - `%s`: %s\n", capability.Name, status)
	}
}

func renderCharterIDs(buffer *bytes.Buffer, ids []string) {
	if len(ids) == 0 {
		buffer.WriteString("  - None.\n")
		return
	}
	for _, id := range ids {
		fmt.Fprintf(buffer, "  - `%s`\n", id)
	}
}

func renderSection(buffer *bytes.Buffer, title string, revisions []canonicalRevision) {
	fmt.Fprintf(buffer, "\n## %s\n\n", title)
	renderRevisionList(buffer, revisions)
}

func renderRevisionList(buffer *bytes.Buffer, revisions []canonicalRevision) {
	if len(revisions) == 0 {
		buffer.WriteString("None.\n")
		return
	}
	for _, revision := range revisions {
		fmt.Fprintf(buffer, "- **%s** (`%s`): %s", revision.Title, revision.RevisionID, revision.Body)
		if revision.Locator != "" {
			fmt.Fprintf(buffer, " [%s]", revision.Locator)
		}
		buffer.WriteByte('\n')
	}
}
