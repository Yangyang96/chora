package app

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
)

// ResourceContentFingerprintObservation proves repository contents after the
// first AfterEvent events. Callers derive it from the proven Task worktree, not
// from Agent text. AfterEvent ranges from zero through len(events).
type ResourceContentFingerprintObservation struct {
	AfterEvent   int
	RepositoryID string
	Fingerprint  string
}

// DerivedResourceCheck preserves the provider observation separately from the
// final-content conclusion. A successful invocation remains UNKNOWN until the
// same exact content fingerprint is proven both at its end and at Attempt end.
type DerivedResourceCheck struct {
	CheckID                 string
	Name                    string
	Version                 uint64
	Source                  string
	Argv                    []string
	WorkingDirectory        string
	ToolCallID              string
	CommandDigest           string
	StartEvent              int
	EndEvent                int
	ProviderSucceeded       *bool
	ExitCode                *int64
	ObservedStatus          execution.CheckStatus
	Status                  execution.CheckStatus
	Evidence                string
	ContentFingerprint      string
	FinalContentFingerprint string
	FinalContentVerified    bool
	Stale                   bool
}

// ResourceCheckDerivation is one repository's complete final check view. Mode
// is retained so none and auto-with-no-applicable-command stay distinguishable.
type ResourceCheckDerivation struct {
	RepositoryID         string
	Mode                 string
	SelectionSource      string
	SelectionExplanation string
	NoApplicableChecks   bool
	Status               execution.CheckStatus
	Explanation          string
	FinalContentVerified bool
	Checks               []DerivedResourceCheck
	Invocations          []DerivedResourceCheck
}

type resourceCheckAuthority struct {
	repositoryID   string
	locator        string
	mode           string
	command        domain.TaskCheckCommand
	workingDir     string
	expectedText   string
	expectedDigest string
}

type resourceCheckStart struct {
	authority     *resourceCheckAuthority
	toolCallID    string
	commandDigest string
	startEvent    int
	ambiguous     bool
}

type resourceCheckState struct {
	result   DerivedResourceCheck
	endEvent int
}

type resourceCheckInvocationState struct {
	repositoryID string
	state        resourceCheckState
}

type resourceToolEvent struct {
	ResourceCommandDigest string `json:"resource_command_digest"`
	ToolName              string `json:"tool_name"`
	ToolCallID            string `json:"tool_call_id"`
	Command               string `json:"redacted_command_summary"`
	CommandDigest         string `json:"command_digest"`
	WorkspacePath         string `json:"workspace_relative_path"`
	ExitCode              *int64 `json:"exit_code"`
	ToolError             *bool  `json:"tool_error"`
}

// DeriveResourceChecks derives per-repository final check truth from normalized
// events scoped by the caller to exactly one current Attempt. It performs no IO
// and never correlates by filename, repository display name, or event time.
func DeriveResourceChecks(resources []speccoding.ExecutionRepositoryResource, events []execution.NormalizedEvent, observations []ResourceContentFingerprintObservation) ([]ResourceCheckDerivation, error) {
	authorities, frozenOrder, err := resourceCheckAuthorities(resources)
	if err != nil {
		return nil, err
	}
	fingerprints, err := normalizeResourceFingerprints(resources, len(events), observations)
	if err != nil {
		return nil, err
	}

	selection := deriveResourceAutoSelection(resources, frozenOrder, events)
	selected := make([]*resourceCheckAuthority, 0, len(authorities))
	order := make(map[string][]*resourceCheckAuthority, len(resources))
	for _, authority := range authorities {
		if authority.mode != "named" {
			continue
		}
		selected = append(selected, authority)
		order[authority.repositoryID] = append(order[authority.repositoryID], authority)
	}
	for _, resource := range resources {
		for _, authority := range selection.order[resource.RepoID] {
			selected = append(selected, authority)
			order[resource.RepoID] = append(order[resource.RepoID], authority)
		}
	}

	states := make(map[*resourceCheckAuthority]*resourceCheckState, len(selected))
	for _, authority := range selected {
		states[authority] = &resourceCheckState{result: DerivedResourceCheck{
			CheckID: authority.command.ID, Name: authority.command.Name, Version: authority.command.Version,
			Source: authority.command.Source,
			Argv:   append([]string(nil), authority.command.Argv...), WorkingDirectory: authority.workingDir,
			StartEvent: -1, EndEvent: -1,
			ObservedStatus: execution.CheckUnknown, Status: execution.CheckUnknown,
			Evidence: authority.command.ID + " was not observed in this Attempt",
		}, endEvent: -1}
	}

	active := make(map[string]resourceCheckStart)
	invocations := make([]resourceCheckInvocationState, 0)
	ambiguousToolCalls := ambiguousResourceToolCallIDs(events)
	for eventIndex, event := range events {
		var value resourceToolEvent
		if json.Unmarshal(event.NormalizedJSON, &value) != nil {
			continue
		}
		switch event.Type {
		case "tool_start":
			if !resourceCheckCommandTool(value.ToolName) || strings.TrimSpace(value.ToolCallID) == "" {
				continue
			}
			matches, digest := matchResourceCheckStart(value, selected)
			if ambiguousToolCalls[value.ToolCallID] {
				if len(matches) == 1 {
					authority := matches[0]
					states[authority].result = DerivedResourceCheck{
						CheckID: authority.command.ID, Name: authority.command.Name, Version: authority.command.Version, Source: authority.command.Source,
						Argv: append([]string(nil), authority.command.Argv...), WorkingDirectory: authority.workingDir,
						StartEvent: -1, EndEvent: -1, ObservedStatus: execution.CheckUnknown, Status: execution.CheckUnknown,
						Evidence: authority.command.ID + " matched a duplicated tool call identity",
					}
					states[authority].endEvent = -1
				}
				continue
			}
			if previous, found := active[value.ToolCallID]; found {
				previous.ambiguous = true
				active[value.ToolCallID] = previous
				continue
			}
			if len(matches) != 1 {
				active[value.ToolCallID] = resourceCheckStart{toolCallID: value.ToolCallID, startEvent: eventIndex, ambiguous: true}
				continue
			}
			authority := matches[0]
			state := states[authority]
			state.result = DerivedResourceCheck{
				CheckID: authority.command.ID, Name: authority.command.Name, Version: authority.command.Version,
				Source: authority.command.Source,
				Argv:   append([]string(nil), authority.command.Argv...), WorkingDirectory: authority.workingDir,
				ToolCallID: value.ToolCallID, CommandDigest: digest,
				StartEvent: eventIndex, EndEvent: -1,
				ObservedStatus: execution.CheckUnknown, Status: execution.CheckUnknown,
				Evidence: authority.command.ID + " has no correlatable end event",
			}
			state.endEvent = -1
			active[value.ToolCallID] = resourceCheckStart{authority: authority, toolCallID: value.ToolCallID, commandDigest: digest, startEvent: eventIndex}
		case "tool_end":
			if strings.TrimSpace(value.ToolCallID) == "" {
				continue
			}
			start, found := active[value.ToolCallID]
			delete(active, value.ToolCallID)
			if !found || start.ambiguous || start.authority == nil || eventIndex <= start.startEvent {
				continue
			}
			state := states[start.authority]
			state.endEvent = eventIndex
			state.result.ToolCallID = start.toolCallID
			state.result.CommandDigest = start.commandDigest
			state.result.StartEvent = start.startEvent
			state.result.EndEvent = eventIndex
			state.result.ExitCode = cloneInt64(value.ExitCode)
			state.result.ProviderSucceeded = providerSucceeded(value.ToolError)
			state.result.ObservedStatus, state.result.Evidence = observedResourceCheckOutcome(start.authority.command.ID, value.ExitCode, value.ToolError)
			invocations = append(invocations, resourceCheckInvocationState{
				repositoryID: start.authority.repositoryID,
				state:        resourceCheckState{result: cloneDerivedResourceCheck(state.result), endEvent: eventIndex},
			})
		}
	}

	for authority, state := range states {
		finalizeResourceCheck(authority.repositoryID, state, len(events), events, fingerprints)
	}
	for index := range invocations {
		finalizeResourceCheck(invocations[index].repositoryID, &invocations[index].state, len(events), events, fingerprints)
	}

	result := make([]ResourceCheckDerivation, 0, len(resources))
	for _, resource := range resources {
		derivation := ResourceCheckDerivation{
			RepositoryID: resource.RepoID, Mode: resource.Checks.Mode,
			SelectionSource: selection.source[resource.RepoID], SelectionExplanation: selection.explanation[resource.RepoID],
			NoApplicableChecks: selection.noApplicable[resource.RepoID], Checks: []DerivedResourceCheck{}, Invocations: []DerivedResourceCheck{},
		}
		for _, authority := range order[resource.RepoID] {
			derivation.Checks = append(derivation.Checks, states[authority].result)
		}
		for _, invocation := range invocations {
			if invocation.repositoryID == resource.RepoID {
				derivation.Invocations = append(derivation.Invocations, invocation.state.result)
			}
		}
		deriveResourceCheckAggregate(&derivation)
		if reason := selection.invalid[resource.RepoID]; reason != "" {
			derivation.Status = execution.CheckUnknown
			derivation.FinalContentVerified = false
			derivation.NoApplicableChecks = false
			derivation.Explanation = "Not run / Unverified: automatic check selection is invalid: " + reason
		}
		result = append(result, derivation)
	}
	return result, nil
}

func ambiguousResourceToolCallIDs(events []execution.NormalizedEvent) map[string]bool {
	starts := make(map[string]int)
	ends := make(map[string]int)
	for _, event := range events {
		if event.Type != "tool_start" && event.Type != "tool_end" {
			continue
		}
		var value resourceToolEvent
		if json.Unmarshal(event.NormalizedJSON, &value) != nil || strings.TrimSpace(value.ToolCallID) == "" {
			continue
		}
		if event.Type == "tool_start" {
			starts[value.ToolCallID]++
		} else {
			ends[value.ToolCallID]++
		}
	}
	result := make(map[string]bool)
	for toolCallID, count := range starts {
		if count > 1 || ends[toolCallID] > 1 {
			result[toolCallID] = true
		}
	}
	return result
}

func resourceCheckAuthorities(resources []speccoding.ExecutionRepositoryResource) ([]*resourceCheckAuthority, map[string][]*resourceCheckAuthority, error) {
	if len(resources) == 0 || len(resources) > domain.TaskRepositoryLimit {
		return nil, nil, errors.New("invalid repository check resource vector")
	}
	seenRepositories := make(map[string]bool, len(resources))
	all := []*resourceCheckAuthority{}
	order := make(map[string][]*resourceCheckAuthority, len(resources))
	for _, resource := range resources {
		if _, err := domain.ParseRepositoryID(resource.RepoID); err != nil || !domain.ValidRepositoryWorkspaceLocator(resource.RepoID, resource.Locator) || (resource.Role != "write" && resource.Role != "reference") || seenRepositories[resource.RepoID] ||
			(resource.Checks.Mode != "named" && resource.Checks.Mode != "auto" && resource.Checks.Mode != "none") ||
			resource.Checks.Mode == "none" && len(resource.Checks.Commands) != 0 || resource.Checks.Mode == "named" && len(resource.Checks.Commands) == 0 {
			return nil, nil, fmt.Errorf("invalid repository check authority for %q", resource.RepoID)
		}
		seenRepositories[resource.RepoID] = true
		seenCheckIDs := make(map[string]bool, len(resource.Checks.Commands))
		for _, command := range resource.Checks.Commands {
			if command.ID == "" || command.Name == "" || command.Version == 0 || command.Source == "" || command.WorkingDirectory == "" || seenCheckIDs[command.ID] {
				return nil, nil, fmt.Errorf("invalid check definition for %s", resource.RepoID)
			}
			seenCheckIDs[command.ID] = true
			authority, err := newResourceCheckAuthority(resource.RepoID, resource.Checks.Mode, command, resource.Locator)
			if err != nil {
				return nil, nil, err
			}
			all = append(all, authority)
			order[resource.RepoID] = append(order[resource.RepoID], authority)
		}
	}
	return all, order, nil
}

func newResourceCheckAuthority(repositoryID, mode string, command domain.TaskCheckCommand, locator string) (*resourceCheckAuthority, error) {
	commandText, err := speccoding.CanonicalCheckCommand(command.Argv)
	if err != nil {
		return nil, fmt.Errorf("invalid check %s/%s: %w", repositoryID, command.ID, err)
	}
	workingDir := command.WorkingDirectory
	if workingDir == "" {
		workingDir = "."
	}
	observedDir := locator
	if workingDir != "." {
		observedDir += "/" + workingDir
	}
	cdText, err := speccoding.CanonicalCheckCommand([]string{"cd", observedDir})
	if err != nil {
		return nil, fmt.Errorf("invalid check directory for %s/%s: %w", repositoryID, command.ID, err)
	}
	expected := cdText + " && " + commandText
	normalizedExpected, normalizeErr := speccoding.NormalizeObservedCheckCommand(expected)
	if normalizeErr != nil || !normalizedExpected.ExplicitWorkingDirectory || normalizedExpected.WorkingDirectory != observedDir || !slices.Equal(normalizedExpected.Argv, command.Argv) {
		return nil, fmt.Errorf("invalid check directory for %s/%s", repositoryID, command.ID)
	}
	digest := sha256.Sum256([]byte(expected))
	return &resourceCheckAuthority{repositoryID: repositoryID, locator: locator, mode: mode, command: command, workingDir: workingDir, expectedText: expected, expectedDigest: hex.EncodeToString(digest[:])}, nil
}

func resourceCheckSemanticDigest(authority *resourceCheckAuthority) string {
	cwd := authority.locator
	if authority.workingDir != "." {
		cwd += "/" + authority.workingDir
	}
	semantic, _ := json.Marshal(struct {
		Argv []string `json:"argv"`
		Cwd  string   `json:"cwd"`
	}{authority.command.Argv, cwd})
	computed := sha256.Sum256(semantic)
	return hex.EncodeToString(computed[:])
}

func matchResourceCheckStart(event resourceToolEvent, authorities []*resourceCheckAuthority) ([]*resourceCheckAuthority, string) {
	digest := strings.ToLower(strings.TrimSpace(event.CommandDigest))
	if event.ResourceCommandDigest != "" {
		if !validResourceCheckDigest(event.ResourceCommandDigest) || !validResourceCheckDigest(digest) {
			return nil, digest
		}
		matches := []*resourceCheckAuthority{}
		for _, a := range authorities {
			if resourceCheckSemanticDigest(a) == event.ResourceCommandDigest {
				matches = append(matches, a)
			}
		}
		return matches, digest
	}

	if digest != "" {
		if !validResourceCheckDigest(digest) {
			return nil, digest
		}
		if normalized, err := speccoding.NormalizeObservedCheckCommand(event.Command); err == nil {
			summaryDigest := sha256.Sum256([]byte(event.Command))
			if hex.EncodeToString(summaryDigest[:]) == digest {
				return matchNormalizedResourceCommand(normalized, authorities), digest
			}
		}
		matches := []*resourceCheckAuthority{}
		for _, authority := range authorities {
			if authority.expectedDigest == digest {
				matches = append(matches, authority)
			}
		}
		// A redacted or truncated summary cannot establish semantic equality;
		// only the digest of the exact canonical declared invocation can.
		return matches, digest
	}
	normalized, err := speccoding.NormalizeObservedCheckCommand(event.Command)
	if err != nil {
		return nil, ""
	}
	return matchNormalizedResourceCommand(normalized, authorities), ""
}

func matchNormalizedResourceCommand(command speccoding.NormalizedCheckCommand, authorities []*resourceCheckAuthority) []*resourceCheckAuthority {
	if !command.ExplicitWorkingDirectory {
		return nil
	}
	repositoryID, localDirectory, ok := splitResourceCheckDirectory(command.WorkingDirectory)
	if !ok {
		return nil
	}
	matches := []*resourceCheckAuthority{}
	for _, authority := range authorities {
		if authority.locator == repositoryID && authority.workingDir == localDirectory && slices.Equal(authority.command.Argv, command.Argv) {
			matches = append(matches, authority)
		}
	}
	return matches
}

func splitResourceCheckDirectory(directory string) (string, string, bool) {
	components := strings.Split(directory, "/")
	if len(components) == 0 {
		return "", "", false
	}
	if !validResourceDirectoryName(components[0]) {
		return "", "", false
	}
	local := "."
	if len(components) > 1 {
		local = strings.Join(components[1:], "/")
	}
	return components[0], local, true
}

func observedResourceCheckOutcome(checkID string, exitCode *int64, toolError *bool) (execution.CheckStatus, string) {
	switch {
	case toolError != nil && *toolError:
		return execution.CheckFail, checkID + " reported a provider tool error"
	case exitCode != nil && *exitCode == 0:
		return execution.CheckPass, checkID + " exited 0"
	case exitCode != nil:
		return execution.CheckFail, fmt.Sprintf("%s exited %d", checkID, *exitCode)
	case toolError != nil && !*toolError:
		return execution.CheckPass, checkID + " completed successfully according to Pi; numeric exit code unavailable"
	default:
		return execution.CheckUnknown, checkID + " ended without a provider outcome or numeric exit code"
	}
}

func finalizeResourceCheck(repositoryID string, state *resourceCheckState, eventCount int, events []execution.NormalizedEvent, fingerprints map[string]map[int]string) {
	if state.endEvent < 0 {
		return
	}
	state.result.ContentFingerprint = fingerprints[repositoryID][state.endEvent+1]
	state.result.FinalContentFingerprint = fingerprints[repositoryID][eventCount]
	state.result.FinalContentVerified = state.result.ContentFingerprint != "" && state.result.ContentFingerprint == state.result.FinalContentFingerprint
	state.result.Stale = !state.result.FinalContentVerified && (resourceMayHaveChangedAfter(repositoryID, state.endEvent, events) ||
		state.result.ContentFingerprint != "" && state.result.FinalContentFingerprint != "" && state.result.ContentFingerprint != state.result.FinalContentFingerprint)
	switch state.result.ObservedStatus {
	case execution.CheckFail:
		state.result.Status = execution.CheckFail
	case execution.CheckPass:
		if state.result.FinalContentVerified {
			state.result.Status = execution.CheckPass
			state.result.Evidence += "; final repository contents match the check observation"
		} else {
			state.result.Status = execution.CheckUnknown
			if state.result.Stale {
				state.result.Evidence += "; later repository activity or content drift made this result stale"
			} else {
				state.result.Evidence += "; final repository contents were not proven"
			}
		}
	default:
		state.result.Status = execution.CheckUnknown
	}
}

func resourceMayHaveChangedAfter(repositoryID string, endEvent int, events []execution.NormalizedEvent) bool {
	for index := endEvent + 1; index < len(events); index++ {
		if events[index].Type != "tool_start" {
			continue
		}
		var value resourceToolEvent
		if json.Unmarshal(events[index].NormalizedJSON, &value) != nil {
			continue
		}
		if resourceCheckCommandTool(value.ToolName) {
			normalized, err := speccoding.NormalizeObservedCheckCommand(value.Command)
			if err != nil || !normalized.ExplicitWorkingDirectory {
				return true
			}
			repository, _, ok := splitResourceCheckDirectory(normalized.WorkingDirectory)
			if !ok || domain.ValidRepositoryWorkspaceLocator(repositoryID, repository) {
				return true
			}
			continue
		}
		if !resourceFileWriteTool(value.ToolName) {
			continue
		}
		repository, ok := repositoryFromWorkspacePath(value.WorkspacePath)
		if !ok || domain.ValidRepositoryWorkspaceLocator(repositoryID, repository) {
			return true
		}
	}
	return false
}

func normalizeResourceFingerprints(resources []speccoding.ExecutionRepositoryResource, eventCount int, observations []ResourceContentFingerprintObservation) (map[string]map[int]string, error) {
	known := make(map[string]bool, len(resources))
	result := make(map[string]map[int]string, len(resources))
	for _, resource := range resources {
		known[resource.RepoID] = true
		result[resource.RepoID] = make(map[int]string)
	}
	for _, observation := range observations {
		fingerprint := strings.ToLower(strings.TrimSpace(observation.Fingerprint))
		if !known[observation.RepositoryID] || observation.AfterEvent < 0 || observation.AfterEvent > eventCount || !validResourceCheckDigest(fingerprint) {
			return nil, errors.New("invalid repository content fingerprint observation")
		}
		if previous := result[observation.RepositoryID][observation.AfterEvent]; previous != "" && previous != fingerprint {
			return nil, errors.New("conflicting repository content fingerprint observations")
		}
		result[observation.RepositoryID][observation.AfterEvent] = fingerprint
	}
	return result, nil
}

func deriveResourceCheckAggregate(result *ResourceCheckDerivation) {
	if len(result.Checks) == 0 {
		result.Status = execution.CheckUnknown
		if result.Mode == "none" {
			result.Explanation = "Not run / Unverified: no checks were selected"
		} else if result.NoApplicableChecks {
			result.Explanation = "Not run / Unverified: the Agent explicitly reported no applicable checks"
		} else {
			result.Explanation = "Not run / Unverified: no automatic check selection was proven"
		}
		return
	}
	result.Status = execution.CheckPass
	result.FinalContentVerified = true
	for _, check := range result.Checks {
		if check.Status == execution.CheckFail {
			result.Status = execution.CheckFail
		} else if check.Status == execution.CheckUnknown && result.Status != execution.CheckFail {
			result.Status = execution.CheckUnknown
		}
		result.FinalContentVerified = result.FinalContentVerified && check.FinalContentVerified
	}
	switch result.Status {
	case execution.CheckPass:
		result.Explanation = "All selected checks passed against proven final repository contents"
	case execution.CheckFail:
		result.Explanation = "At least one selected check failed"
	default:
		result.Explanation = "Selected checks are missing, ambiguous, stale, or lack final-content proof"
	}
}

func repositoryFromWorkspacePath(value string) (string, bool) {
	if value == "" || strings.HasPrefix(value, "/") || path.Clean(value) != value {
		return "", false
	}
	repository := strings.Split(value, "/")[0]
	if !validResourceDirectoryName(repository) {
		return "", false
	}
	return repository, true
}

func resourceCheckCommandTool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "bash", "shell", "exec", "exec_command":
		return true
	default:
		return false
	}
}

func resourceFileWriteTool(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "edit", "write", "str_replace_editor":
		return true
	default:
		return false
	}
}

func validResourceCheckDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func providerSucceeded(toolError *bool) *bool {
	if toolError == nil {
		return nil
	}
	value := !*toolError
	return &value
}

func cloneInt64(value *int64) *int64 {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneDerivedResourceCheck(value DerivedResourceCheck) DerivedResourceCheck {
	value.Argv = append([]string(nil), value.Argv...)
	value.ProviderSucceeded = cloneBool(value.ProviderSucceeded)
	value.ExitCode = cloneInt64(value.ExitCode)
	return value
}

func cloneBool(value *bool) *bool {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func validResourceDirectoryName(value string) bool {
	if _, err := domain.ParseRepositoryID(value); err == nil {
		return true
	}
	raw, err := hex.DecodeString(value)
	return err == nil && len(raw) == 6 && value == strings.ToLower(value)
}
