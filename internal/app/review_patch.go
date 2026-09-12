package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/linediff"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

var (
	ErrReviewEvidenceUnavailable = errors.New("review evidence unavailable")
	// ErrNoReviewableChanges is returned only after the Task's frozen repository,
	// base and writable-scope authority have all been proven and the worktree has
	// no non-ignored change. The terminal workflow maps this distinct outcome to
	// durable completed/no-change semantics.
	ErrNoReviewableChanges = errors.New("no reviewable changes")
)

type PatchLineKind string

const (
	PatchLineContext PatchLineKind = "context"
	PatchLineAdded   PatchLineKind = "added"
	PatchLineRemoved PatchLineKind = "removed"
)

type ReviewablePatchLine struct {
	Kind             PatchLineKind
	OldLine, NewLine int
	Text             string
}

type ReviewablePatchHunk struct {
	Header             string
	OldStart, OldCount int
	NewStart, NewCount int
	Lines              []ReviewablePatchLine
}

type ReviewablePatchFile struct {
	Path                 string
	Kind                 ReviewablePatchFileKind
	Additions, Deletions int
	Hunks                []ReviewablePatchHunk
	Lines                []ReviewablePatchLine
}

type ReviewablePatchFileKind = string

const (
	ReviewablePatchFileAdded    ReviewablePatchFileKind = "added"
	ReviewablePatchFileModified ReviewablePatchFileKind = "modified"
	ReviewablePatchFileDeleted  ReviewablePatchFileKind = "deleted"
)

type ReviewablePatch struct {
	Raw                 []byte
	PatchDigest         [32]byte
	DeclaredFilesDigest [32]byte
	Files               []ReviewablePatchFile
}

type ReviewableChange struct {
	Patch                 ReviewablePatch
	Binding               domain.VerifiedReviewBinding
	Outcome               domain.ResultOutcome
	Checks                []domain.AcceptanceCheck
	EvidenceComplete      bool
	CleanupProven         bool
	WorkspaceIdentity     string
	ContractDigest        [32]byte
	contextSnapshotDigest [32]byte
}

func (s *Service) LoadReviewableChange(ctx context.Context, runID domain.RunID) (ReviewableChange, error) {
	if s == nil || s.deps.Store == nil || s.deps.PatchSource == nil {
		return ReviewableChange{}, reviewEvidenceError("Patch review dependencies are unavailable")
	}
	return s.loadReviewableChange(ctx, s.deps.Store.Reader(), runID)
}

func (s *Service) loadReviewableChange(ctx context.Context, reader storecontract.Reader, runID domain.RunID) (ReviewableChange, error) {
	run, err := reader.GetRun(ctx, runID)
	if err != nil {
		return ReviewableChange{}, err
	}
	switch run.State() {
	case domain.RunStateAwaitingReview, domain.RunStateRevisionRequired, domain.RunStateAccepted:
	default:
		return ReviewableChange{}, reviewEvidenceError("Run has no reviewable Result")
	}
	agentAttempt, err := reader.GetCurrentAttempt(ctx, run.ID())
	if err != nil || agentAttempt.State() != domain.AttemptStateOutputSubmitted {
		return ReviewableChange{}, reviewEvidenceError("terminal Agent Attempt is unavailable")
	}
	report, err := reader.GetAgentReportForAttempt(ctx, agentAttempt.ID())
	if err != nil || report.AttemptID() != agentAttempt.ID() {
		return ReviewableChange{}, reviewEvidenceError("AgentReport does not bind the terminal Agent Attempt")
	}
	projection, err := reader.GetTerminalProjection(ctx, agentAttempt.ID())
	if err != nil {
		return ReviewableChange{}, err
	}
	patchArtifact, err := finalPatchArtifact(projection.Artifacts)
	if err != nil {
		return ReviewableChange{}, err
	}
	binding, err := reader.GetSpecCodingBinding(ctx, run.TaskID())
	if err != nil || binding.Status != storecontract.SpecCodingRegistered || binding.TaskID != run.TaskID() ||
		sha256.Sum256(binding.ActiveContractJSON) != binding.ActiveContractDigest {
		return ReviewableChange{}, reviewEvidenceError("registered contract binding is unavailable or drifted")
	}
	if err := validateSnapshotLineage(ctx, reader, agentAttempt, binding); err != nil {
		return ReviewableChange{}, reviewEvidenceError("registered contract binding is unavailable or drifted")
	}
	contract, err := speccoding.DecodeCoreContract(binding.ActiveContractJSON)
	if err != nil || contract.DigestHex() != fmt.Sprintf("%x", binding.ActiveContractDigest) {
		return ReviewableChange{}, reviewEvidenceError("active contract bytes do not match their digest")
	}
	document := contract.Document()
	if document.Task.ID != run.TaskID().String() || document.Execution.Input.ContextSnapshotID != binding.SnapshotID.String() || document.Execution.Input.ContextSnapshotDigest != fmt.Sprintf("%x", binding.SnapshotDigest) {
		return ReviewableChange{}, reviewEvidenceError("Task or Context binding drifted")
	}
	verificationRun, err := reader.GetVerificationRunForAgentAttempt(ctx, agentAttempt.ID())
	if errors.Is(err, storecontract.ErrNotFound) && localConnectedReviewDocument(document) {
		return s.loadLocalConnectedReviewableChange(ctx, reader, run, agentAttempt, report, patchArtifact, binding, contract, document)
	}
	if err != nil {
		return ReviewableChange{}, reviewEvidenceError("Verification lineage is unavailable")
	}
	result, err := reader.GetVerificationResultForVerificationRun(ctx, verificationRun.ID())
	if err != nil || result.VerificationRunID() != verificationRun.ID() {
		return ReviewableChange{}, reviewEvidenceError("Result does not bind the Verification Run")
	}
	attemptRecord, err := reader.GetVerificationAttempt(ctx, result.VerificationAttemptID())
	if err != nil || attemptRecord.Attempt.VerificationRunID() != verificationRun.ID() || attemptRecord.Attempt.State() != domain.VerificationAttemptCompleted || !attemptRecord.EvidenceComplete || !attemptRecord.CleanupProven || strings.TrimSpace(attemptRecord.WorkspaceIdentity) == "" {
		return ReviewableChange{}, reviewEvidenceError("Verification Attempt lacks complete trusted evidence or cleanup proof")
	}
	bindings := result.Bindings()
	if patchArtifact.Digest == nil || *patchArtifact.Digest != bindings.PatchDigest || bindings.ContextSnapshotDigest != agentAttempt.ContextDigest() || bindings.AcceptanceContractDigest != binding.ActiveContractDigest || bindings != verificationRun.Bindings() || bindings != attemptRecord.Attempt.Bindings() {
		return ReviewableChange{}, reviewEvidenceError("Result authority bindings drifted")
	}
	if run.State() == domain.RunStateAwaitingReview && result.Outcome() != domain.ResultReviewReady ||
		run.State() == domain.RunStateAccepted && result.Outcome() != domain.ResultReviewReady {
		return ReviewableChange{}, reviewEvidenceError("Run state and Result outcome disagree")
	}
	commands, err := reader.ListVerificationCommandEvidence(ctx, result.VerificationAttemptID())
	if err != nil || !validReviewEvidence(document, result.Checks(), commands, attemptRecord.WorkspaceIdentity) {
		return ReviewableChange{}, reviewEvidenceError("Criterion-bound command evidence is incomplete or drifted")
	}
	raw, err := s.deps.PatchSource.ReadReviewPatch(ctx, patchArtifact.Locator)
	if err != nil {
		return ReviewableChange{}, reviewEvidenceError("immutable Patch bytes are unavailable")
	}
	patch, err := BuildReviewablePatch(contract, raw, bindings.PatchDigest)
	if err != nil {
		return ReviewableChange{}, err
	}
	return ReviewableChange{
		Patch: patch, Binding: domain.VerifiedReviewBinding{
			ResultID: result.ID(), AgentAttemptID: agentAttempt.ID(), VerificationAttemptID: result.VerificationAttemptID(),
			PatchArtifactID: patchArtifact.ID, PatchDigest: bindings.PatchDigest, BaselineDigest: bindings.BaselineDigest, DeclaredFilesDigest: patch.DeclaredFilesDigest,
		}, Outcome: result.Outcome(), Checks: result.Checks(), EvidenceComplete: true, CleanupProven: true,
		WorkspaceIdentity: attemptRecord.WorkspaceIdentity, ContractDigest: binding.ActiveContractDigest,
		contextSnapshotDigest: bindings.ContextSnapshotDigest,
	}, nil
}

func (s *Service) loadLocalConnectedReviewableChange(ctx context.Context, reader storecontract.Reader, run domain.AgentRun, agentAttempt domain.Attempt, report domain.AgentReport, patchArtifact storecontract.Artifact, binding storecontract.SpecCodingBinding, contract speccoding.CoreContract, document speccoding.CoreContractDocument) (ReviewableChange, error) {
	result, err := reader.GetLocalReviewResultForAgentAttempt(ctx, agentAttempt.ID())
	if err != nil || result.RunID() != run.ID() || result.AgentAttemptID() != agentAttempt.ID() || result.AgentReportID() != report.ID() ||
		result.PatchArtifactID() != patchArtifact.ID || patchArtifact.Digest == nil || result.PatchDigest() != *patchArtifact.Digest ||
		result.ContextSnapshotDigest() != agentAttempt.ContextDigest() || result.AcceptanceContractDigest() != binding.ActiveContractDigest ||
		result.BaselineDigest() != localConnectedBaselineDigest(document) || result.Outcome() != domain.ResultReviewReady {
		return ReviewableChange{}, reviewEvidenceError("Agent-reported Local Connected Result binding is unavailable or drifted")
	}
	raw, err := s.deps.PatchSource.ReadReviewPatch(ctx, patchArtifact.Locator)
	if err != nil {
		return ReviewableChange{}, reviewEvidenceError("immutable Patch bytes are unavailable")
	}
	patch, err := BuildReviewablePatch(contract, raw, result.PatchDigest())
	if err != nil {
		return ReviewableChange{}, err
	}
	return ReviewableChange{
		Patch: patch,
		Binding: domain.VerifiedReviewBinding{
			EvidenceKind: domain.ReviewEvidenceAgentReport, ResultID: result.ID(), AgentAttemptID: agentAttempt.ID(), AgentReportID: report.ID(),
			PatchArtifactID: patchArtifact.ID, PatchDigest: result.PatchDigest(), BaselineDigest: result.BaselineDigest(), DeclaredFilesDigest: patch.DeclaredFilesDigest,
		},
		Outcome: result.Outcome(), EvidenceComplete: false, CleanupProven: false,
		WorkspaceIdentity: "Task worktree · Agent-reported", ContractDigest: binding.ActiveContractDigest,
		contextSnapshotDigest: result.ContextSnapshotDigest(),
	}, nil
}

func localConnectedReviewDocument(document speccoding.CoreContractDocument) bool {
	if document.SchemaVersion != speccoding.CoreContractSchemaVersionV11 {
		return false
	}
	for _, capability := range document.Execution.RequiredCapabilities {
		if capability == speccoding.LocalConnectedNoSandboxCapability || capability == speccoding.IsolatedLocalCapability {
			return true
		}
	}
	return false
}

func localConnectedBaselineDigest(document speccoding.CoreContractDocument) [32]byte {
	return sha256.Sum256([]byte("chora.local-connected-base.v1\x00" + document.Task.Repository.BaseRevision))
}

func finalPatchArtifact(artifacts []storecontract.Artifact) (storecontract.Artifact, error) {
	var patch *storecontract.Artifact
	for index := range artifacts {
		artifact := &artifacts[index]
		if artifact.Kind != "patch" || artifact.Role != "output" {
			continue
		}
		if patch != nil {
			return storecontract.Artifact{}, reviewEvidenceError("multiple final Patch artifacts exist")
		}
		patch = artifact
	}
	if patch == nil || patch.Digest == nil || strings.TrimSpace(patch.Locator) == "" {
		return storecontract.Artifact{}, reviewEvidenceError("digest-bound final Patch artifact is unavailable")
	}
	return *patch, nil
}

func validReviewEvidence(document speccoding.CoreContractDocument, checks []domain.AcceptanceCheck, commands []domain.VerificationCommandEvidence, workspaceIdentity string) bool {
	if len(checks) != len(document.Acceptance.Criteria) || len(commands) != len(document.Acceptance.VerificationCommands) {
		return false
	}
	commandAuthority := make(map[string]speccoding.BoundedCommand, len(document.Acceptance.VerificationCommands))
	expectedCriteria := make(map[string][]string, len(document.Acceptance.VerificationCommands))
	for _, command := range document.Acceptance.VerificationCommands {
		commandAuthority[command.ID] = command
	}
	criteria := make(map[string]struct{}, len(document.Acceptance.Criteria))
	for _, criterion := range document.Acceptance.Criteria {
		criteria[criterion.ID] = struct{}{}
		for _, commandID := range criterion.VerificationCommandIDs {
			expectedCriteria[commandID] = append(expectedCriteria[commandID], criterion.ID)
		}
	}
	evidenceByID := make(map[domain.VerificationCommandEvidenceID]domain.VerificationCommandEvidence, len(commands))
	seenCommands := make(map[string]struct{}, len(commands))
	for _, evidence := range commands {
		authority, ok := commandAuthority[evidence.CommandID()]
		if !ok || !sameReviewStrings(authority.Argv, evidence.Argv()) || evidence.WorkspaceIdentity() != workspaceIdentity || !sameStringSet(expectedCriteria[evidence.CommandID()], criterionTexts(evidence.CriterionIDs())) {
			return false
		}
		if _, duplicate := seenCommands[evidence.CommandID()]; duplicate {
			return false
		}
		seenCommands[evidence.CommandID()] = struct{}{}
		evidenceByID[evidence.ID()] = evidence
	}
	seenCriteria := make(map[string]struct{}, len(checks))
	for _, check := range checks {
		criterion := check.CriterionID().String()
		if _, ok := criteria[criterion]; !ok || check.Trust() != domain.VerificationEvidenceTrusted {
			return false
		}
		if _, duplicate := seenCriteria[criterion]; duplicate {
			return false
		}
		seenCriteria[criterion] = struct{}{}
		for _, evidenceID := range check.EvidenceIDs() {
			evidence, ok := evidenceByID[evidenceID]
			if !ok || !containsCriterion(evidence.CriterionIDs(), check.CriterionID()) {
				return false
			}
		}
	}
	return len(seenCriteria) == len(criteria)
}

func criterionTexts(ids []domain.CriterionID) []string {
	values := make([]string, len(ids))
	for index, id := range ids {
		values[index] = id.String()
	}
	return values
}

func containsCriterion(ids []domain.CriterionID, target domain.CriterionID) bool {
	for _, id := range ids {
		if id == target {
			return true
		}
	}
	return false
}

func sameReviewStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	counts := make(map[string]int, len(left))
	for _, value := range left {
		counts[value]++
	}
	for _, value := range right {
		counts[value]--
		if counts[value] < 0 {
			return false
		}
	}
	return true
}

var patchHunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

const patchContextLines = 3

// BuildReviewablePatch derives every review projection from the immutable Patch
// byte stream. It rejects digest drift, malformed unified diffs, binary content,
// duplicate file sections, and changes outside the frozen writable-file boundary.
func BuildReviewablePatch(contract speccoding.CoreContract, raw []byte, expectedDigest [32]byte) (ReviewablePatch, error) {
	if len(raw) == 0 || expectedDigest == ([32]byte{}) || sha256.Sum256(raw) != expectedDigest {
		return ReviewablePatch{}, reviewEvidenceError("Patch bytes do not match the frozen digest")
	}
	document := contract.Document()
	if document.SchemaVersion != speccoding.CoreContractSchemaVersionV8 && document.SchemaVersion != speccoding.CoreContractSchemaVersionV9 && document.SchemaVersion != speccoding.CoreContractSchemaVersionV10 && document.SchemaVersion != speccoding.CoreContractSchemaVersionV11 {
		return ReviewablePatch{}, reviewEvidenceError("active contract is not a frozen structured-verification authority")
	}
	files, err := parseUnifiedPatch(raw, document.Execution.Boundary.WritableFiles, document.Execution.Boundary.WritableDirectories)
	if err != nil {
		return ReviewablePatch{}, err
	}
	return ReviewablePatch{
		Raw: append([]byte(nil), raw...), PatchDigest: expectedDigest,
		DeclaredFilesDigest: declaredScopeDigest(document.Execution.Boundary.WritableFiles, document.Execution.Boundary.WritableDirectories), Files: files,
	}, nil
}

func declaredScopeDigest(paths, directories []string) [32]byte {
	values := append([]string(nil), paths...)
	sort.Strings(values)
	if len(directories) == 0 {
		return sha256.Sum256([]byte("chora.declared-files.v1\n" + strings.Join(values, "\n") + "\n"))
	}
	dirs := append([]string(nil), directories...)
	sort.Strings(dirs)
	return sha256.Sum256([]byte("chora.declared-scope.v2\nfiles\n" + strings.Join(values, "\n") + "\ndirectories\n" + strings.Join(dirs, "\n") + "\n"))
}

func parseUnifiedPatch(raw []byte, writableFiles, writableDirectories []string) ([]ReviewablePatchFile, error) {
	if bytes.IndexByte(raw, 0) >= 0 || !utf8.Valid(raw) || !bytes.HasSuffix(raw, []byte("\n")) {
		return nil, reviewEvidenceError("Patch is not a complete text unified diff")
	}
	lines := strings.Split(string(raw[:len(raw)-1]), "\n")
	var files []ReviewablePatchFile
	seen := make(map[string]struct{})
	for index := 0; index < len(lines); {
		if !strings.HasPrefix(lines[index], "diff --git a/") {
			return nil, reviewEvidenceError("Patch contains content outside a file section")
		}
		path, err := parseDiffHeader(lines[index])
		if err != nil {
			return nil, err
		}
		if !speccoding.WritableScopeAllows(path, writableFiles, writableDirectories) {
			return nil, reviewEvidenceError("Patch changes an undeclared file: " + path)
		}
		if _, exists := seen[path]; exists {
			return nil, reviewEvidenceError("Patch repeats a file section: " + path)
		}
		seen[path] = struct{}{}
		index++

		kind := ReviewablePatchFileModified
		for index < len(lines) && !strings.HasPrefix(lines[index], "--- ") && !strings.HasPrefix(lines[index], "diff --git ") {
			if strings.HasPrefix(lines[index], "Binary files ") || strings.HasPrefix(lines[index], "GIT binary patch") {
				return nil, reviewEvidenceError("binary Patch is not line-reviewable")
			}
			switch {
			case lines[index] == "new file mode 100644":
				if kind != ReviewablePatchFileModified {
					return nil, reviewEvidenceError("Patch has conflicting file-kind metadata")
				}
				kind = ReviewablePatchFileAdded
			case strings.HasPrefix(lines[index], "new file mode "):
				return nil, reviewEvidenceError("new executable, symlink or special-file mode is unsupported")
			case lines[index] == "deleted file mode 100644" || lines[index] == "deleted file mode 100755":
				if kind != ReviewablePatchFileModified {
					return nil, reviewEvidenceError("Patch has conflicting file-kind metadata")
				}
				kind = ReviewablePatchFileDeleted
			case strings.HasPrefix(lines[index], "deleted file mode "):
				return nil, reviewEvidenceError("deleted symlink or special-file mode is unsupported")
			case strings.HasPrefix(lines[index], "old mode ") || strings.HasPrefix(lines[index], "new mode "):
				return nil, reviewEvidenceError("executable-mode changes are unsupported")
			case strings.HasPrefix(lines[index], "index "):
				// The immutable blob identities are retained in the raw Patch. Apply
				// conflict checks bind the actual target pre-state separately.
				if !supportedPatchIndexMetadata(lines[index]) {
					return nil, reviewEvidenceError("Patch index names a symlink, submodule, or unsupported mode")
				}
			default:
				return nil, reviewEvidenceError("Patch contains unsupported file metadata")
			}
			index++
		}
		if index+1 >= len(lines) || !validPatchFileHeaders(lines[index], lines[index+1], path, kind) {
			return nil, reviewEvidenceError("Patch file headers do not match the declared path")
		}
		index += 2
		file := ReviewablePatchFile{Path: path, Kind: kind}
		hunks := 0
		for index < len(lines) && !strings.HasPrefix(lines[index], "diff --git ") {
			header := lines[index]
			match := patchHunkHeader.FindStringSubmatch(header)
			if match == nil {
				return nil, reviewEvidenceError("Patch contains content outside a hunk")
			}
			oldLine, _ := strconv.Atoi(match[1])
			newLine, _ := strconv.Atoi(match[3])
			oldCount, newCount := 1, 1
			if match[2] != "" {
				oldCount, _ = strconv.Atoi(match[2])
			}
			if match[4] != "" {
				newCount, _ = strconv.Atoi(match[4])
			}
			hunk := ReviewablePatchHunk{Header: header, OldStart: oldLine, OldCount: oldCount, NewStart: newLine, NewCount: newCount, Lines: []ReviewablePatchLine{}}
			index++
			seenOld, seenNew := 0, 0
			for index < len(lines) && !strings.HasPrefix(lines[index], "@@ ") && !strings.HasPrefix(lines[index], "diff --git ") {
				line := lines[index]
				if line == `\ No newline at end of file` {
					index++
					continue
				}
				if line == "" {
					return nil, reviewEvidenceError("unprefixed empty line inside Patch hunk")
				}
				switch line[0] {
				case ' ':
					item := ReviewablePatchLine{Kind: PatchLineContext, OldLine: oldLine, NewLine: newLine, Text: line[1:]}
					file.Lines = append(file.Lines, item)
					hunk.Lines = append(hunk.Lines, item)
					oldLine++
					newLine++
					seenOld++
					seenNew++
				case '-':
					item := ReviewablePatchLine{Kind: PatchLineRemoved, OldLine: oldLine, Text: line[1:]}
					file.Lines = append(file.Lines, item)
					hunk.Lines = append(hunk.Lines, item)
					file.Deletions++
					oldLine++
					seenOld++
				case '+':
					item := ReviewablePatchLine{Kind: PatchLineAdded, NewLine: newLine, Text: line[1:]}
					file.Lines = append(file.Lines, item)
					hunk.Lines = append(hunk.Lines, item)
					file.Additions++
					newLine++
					seenNew++
				default:
					return nil, reviewEvidenceError("invalid unified Patch line prefix")
				}
				index++
			}
			if seenOld != oldCount || seenNew != newCount {
				return nil, reviewEvidenceError("Patch hunk line counts do not match its header")
			}
			file.Hunks = append(file.Hunks, hunk)
			hunks++
		}
		if hunks == 0 || len(file.Lines) == 0 {
			return nil, reviewEvidenceError("Patch file has no reviewable hunk")
		}
		file = normalizeLegacyWholeFileProjection(file)
		files = append(files, file)
	}
	if len(files) == 0 {
		return nil, reviewEvidenceError("Patch has no reviewable files")
	}
	return files, nil
}

// Older Chora runtimes serialized any changed file as every old line removed
// followed by every new line added. The immutable Patch remains the authority,
// but that representation is not an honest human review projection: unchanged
// lines look deleted. Recognize only that exact legacy shape and derive minimal
// display hunks from the complete before/after text it contains.
func normalizeLegacyWholeFileProjection(file ReviewablePatchFile) ReviewablePatchFile {
	if len(file.Hunks) != 1 {
		return file
	}
	hunk := file.Hunks[0]
	if hunk.OldStart != 1 || hunk.NewStart != 1 || hunk.OldCount == 0 || hunk.NewCount == 0 || len(hunk.Lines) != hunk.OldCount+hunk.NewCount {
		return file
	}
	before := make([]string, 0, hunk.OldCount)
	after := make([]string, 0, hunk.NewCount)
	for index, line := range hunk.Lines {
		switch {
		case index < hunk.OldCount && line.Kind == PatchLineRemoved:
			before = append(before, line.Text)
		case index >= hunk.OldCount && line.Kind == PatchLineAdded:
			after = append(after, line.Text)
		default:
			return file
		}
	}
	projection, ok := semanticPatchProjection(file.Path, before, after)
	if !ok {
		return file
	}
	projection.Kind = file.Kind
	return projection
}

func semanticPatchProjection(path string, before, after []string) (ReviewablePatchFile, bool) {
	edits := linediff.ShortestEdits(before, after)
	ranges := linediff.ContextRanges(edits, patchContextLines)
	if len(ranges) == 0 {
		return ReviewablePatchFile{}, false
	}
	file := ReviewablePatchFile{Path: path, Kind: ReviewablePatchFileModified, Hunks: make([]ReviewablePatchHunk, 0, len(ranges))}
	oldConsumed, newConsumed, position := 0, 0, 0
	for _, displayRange := range ranges {
		for position < displayRange.Start {
			if edits[position].Kind != linediff.Insert {
				oldConsumed++
			}
			if edits[position].Kind != linediff.Delete {
				newConsumed++
			}
			position++
		}
		oldCount, newCount := 0, 0
		for index := displayRange.Start; index < displayRange.End; index++ {
			if edits[index].Kind != linediff.Insert {
				oldCount++
			}
			if edits[index].Kind != linediff.Delete {
				newCount++
			}
		}
		oldStart, newStart := oldConsumed+1, newConsumed+1
		if oldCount == 0 {
			oldStart = oldConsumed
		}
		if newCount == 0 {
			newStart = newConsumed
		}
		hunk := ReviewablePatchHunk{Header: "@@ -" + reviewPatchRange(oldStart, oldCount) + " +" + reviewPatchRange(newStart, newCount) + " @@", OldStart: oldStart, OldCount: oldCount, NewStart: newStart, NewCount: newCount}
		for position < displayRange.End {
			edit := edits[position]
			line := ReviewablePatchLine{Kind: PatchLineContext, Text: edit.Line}
			switch edit.Kind {
			case linediff.Equal:
				line.OldLine, line.NewLine = oldConsumed+1, newConsumed+1
			case linediff.Delete:
				line.Kind, line.OldLine = PatchLineRemoved, oldConsumed+1
				file.Deletions++
			case linediff.Insert:
				line.Kind, line.NewLine = PatchLineAdded, newConsumed+1
				file.Additions++
			}
			if edit.Kind != linediff.Insert {
				oldConsumed++
			}
			if edit.Kind != linediff.Delete {
				newConsumed++
			}
			hunk.Lines = append(hunk.Lines, line)
			file.Lines = append(file.Lines, line)
			position++
		}
		file.Hunks = append(file.Hunks, hunk)
	}
	return file, true
}

func reviewPatchRange(start, count int) string {
	if count == 1 {
		return strconv.Itoa(start)
	}
	return strconv.Itoa(start) + "," + strconv.Itoa(count)
}

func parseDiffHeader(line string) (string, error) {
	const prefix = "diff --git a/"
	remainder := strings.TrimPrefix(line, prefix)
	// With rename detection disabled Chora accepts only Git headers of the form
	// `a/<path> b/<same-path>`. The separator is therefore at the unique byte
	// midpoint; searching for " b/" is ambiguous for valid paths such as
	// `a b/c.txt`.
	if remainder == line || len(remainder) <= len(" b/") || (len(remainder)-len(" b/"))%2 != 0 {
		return "", reviewEvidenceError("invalid Patch file header")
	}
	separator := (len(remainder) - len(" b/")) / 2
	if separator <= 0 || remainder[separator:separator+len(" b/")] != " b/" {
		return "", reviewEvidenceError("invalid Patch file header")
	}
	oldPath, newPath := remainder[:separator], remainder[separator+3:]
	if oldPath == "" || oldPath != newPath || strings.ContainsRune(oldPath, '\x00') {
		return "", reviewEvidenceError("Patch rename or invalid path is outside the frozen boundary")
	}
	return oldPath, nil
}

func supportedPatchIndexMetadata(line string) bool {
	fields := strings.Fields(line)
	if len(fields) != 2 && len(fields) != 3 || fields[0] != "index" || !strings.Contains(fields[1], "..") {
		return false
	}
	return len(fields) == 2 || fields[2] == "100644" || fields[2] == "100755"
}

func validPatchFileHeaders(oldHeader, newHeader, path string, kind ReviewablePatchFileKind) bool {
	switch kind {
	case ReviewablePatchFileAdded:
		return patchPathHeaderMatches(oldHeader, "--- /dev/null") && patchPathHeaderMatches(newHeader, "+++ b/"+path)
	case ReviewablePatchFileDeleted:
		return patchPathHeaderMatches(oldHeader, "--- a/"+path) && patchPathHeaderMatches(newHeader, "+++ /dev/null")
	default:
		return patchPathHeaderMatches(oldHeader, "--- a/"+path) && patchPathHeaderMatches(newHeader, "+++ b/"+path)
	}
}

func patchPathHeaderMatches(header, expected string) bool {
	return header == expected || strings.HasPrefix(header, expected+"\t")
}

func reviewEvidenceError(detail string) error {
	return fmt.Errorf("%w: %s", ErrReviewEvidenceUnavailable, detail)
}
