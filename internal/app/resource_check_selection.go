package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
)

const (
	resourceCheckSelectionFence        = "```chora-check-selection\n"
	resourceCheckSelectionTextLimit    = 64 * 1024
	resourceCheckSelectionCommandLimit = 64
	resourceCheckSelectionNameLimit    = 256
	resourceCheckSelectionReasonLimit  = 2048
)

type resourceCheckSelectionDocument struct {
	Repositories []resourceCheckSelectionRepository `json:"repositories"`
}

type resourceCheckSelectionRepository struct {
	RepoID             string                          `json:"repoId"`
	Checks             []resourceCheckSelectionCommand `json:"checks"`
	NoApplicableChecks bool                            `json:"noApplicableChecks"`
	Explanation        string                          `json:"explanation"`
}

type resourceCheckSelectionCommand struct {
	Name             string `json:"name"`
	Command          string `json:"command"`
	WorkingDirectory string `json:"workingDirectory"`
}

type resourceAutoSelection struct {
	order        map[string][]*resourceCheckAuthority
	noApplicable map[string]bool
	source       map[string]string
	explanation  map[string]string
	invalid      map[string]string
}

func deriveResourceAutoSelection(resources []speccoding.ExecutionRepositoryResource, frozen map[string][]*resourceCheckAuthority, events []execution.NormalizedEvent) resourceAutoSelection {
	result := resourceAutoSelection{
		order: make(map[string][]*resourceCheckAuthority, len(resources)), noApplicable: make(map[string]bool, len(resources)),
		source: make(map[string]string, len(resources)), explanation: make(map[string]string, len(resources)), invalid: make(map[string]string, len(resources)),
	}
	auto := make(map[string]speccoding.ExecutionRepositoryResource, len(resources))
	var autoIDs []string
	for _, resource := range resources {
		switch resource.Checks.Mode {
		case "named":
			result.source[resource.RepoID] = "frozen_named"
		case "none":
			result.source[resource.RepoID] = "explicit_none"
		case "auto":
			auto[resource.RepoID] = resource
			autoIDs = append(autoIDs, resource.RepoID)
		}
	}

	document, found, err := finalResourceCheckSelection(events)
	if err != nil {
		for _, repositoryID := range autoIDs {
			result.invalid[repositoryID] = err.Error()
		}
	} else if found {
		selected, selectionErr := validateResourceCheckSelection(document, auto, frozen)
		if selectionErr != nil {
			for _, repositoryID := range autoIDs {
				result.invalid[repositoryID] = selectionErr.Error()
			}
		} else {
			for _, repository := range document.Repositories {
				result.order[repository.RepoID] = selected[repository.RepoID]
				result.noApplicable[repository.RepoID] = repository.NoApplicableChecks
				result.source[repository.RepoID] = "agent_proposed"
				result.explanation[repository.RepoID] = strings.TrimSpace(repository.Explanation)
			}
		}
	}

	selected := make(map[*resourceCheckAuthority]bool)
	for _, authorities := range result.order {
		for _, authority := range authorities {
			selected[authority] = true
		}
	}
	var candidates []*resourceCheckAuthority
	for repositoryID := range auto {
		candidates = append(candidates, frozen[repositoryID]...)
	}
	for _, event := range events {
		if event.Type != "tool_start" {
			continue
		}
		var value resourceToolEvent
		if json.Unmarshal(event.NormalizedJSON, &value) != nil || !resourceCheckCommandTool(value.ToolName) || strings.TrimSpace(value.ToolCallID) == "" {
			continue
		}
		matches, _ := matchResourceCheckStart(value, candidates)
		if len(matches) > 1 {
			for _, match := range matches {
				result.invalid[match.repositoryID] = "one observed command matched multiple automatic candidates"
				result.noApplicable[match.repositoryID] = false
			}
			continue
		}
		if len(matches) != 1 {
			continue
		}
		authority := matches[0]
		if result.noApplicable[authority.repositoryID] {
			result.invalid[authority.repositoryID] = "the final selection reported no applicable checks after an automatic candidate was executed"
			result.noApplicable[authority.repositoryID] = false
		}
		if selected[authority] {
			continue
		}
		selected[authority] = true
		result.order[authority.repositoryID] = append(result.order[authority.repositoryID], authority)
		if result.source[authority.repositoryID] == "" {
			result.source[authority.repositoryID] = "automatic_candidate_execution"
			result.explanation[authority.repositoryID] = "Selected by an unambiguous Pi tool invocation of a frozen automatic candidate."
		}
	}
	return result
}

func finalResourceCheckSelection(events []execution.NormalizedEvent) (resourceCheckSelectionDocument, bool, error) {
	var payload struct {
		EventType        string `json:"event_type"`
		Text             string `json:"text"`
		TerminalComplete bool   `json:"terminal_complete"`
		Truncated        bool   `json:"truncated"`
	}
	foundAssistant := false
	for _, event := range events {
		if event.Type != "assistant_message" {
			continue
		}
		foundAssistant = true
		payload = struct {
			EventType        string `json:"event_type"`
			Text             string `json:"text"`
			TerminalComplete bool   `json:"terminal_complete"`
			Truncated        bool   `json:"truncated"`
		}{}
		if json.Unmarshal(event.NormalizedJSON, &payload) != nil {
			payload = struct {
				EventType        string `json:"event_type"`
				Text             string `json:"text"`
				TerminalComplete bool   `json:"terminal_complete"`
				Truncated        bool   `json:"truncated"`
			}{}
		}
	}
	if !foundAssistant || payload.EventType != "assistant_message" {
		return resourceCheckSelectionDocument{}, false, nil
	}
	mentionsSelection := strings.Contains(payload.Text, "chora-check-selection")
	if !payload.TerminalComplete || payload.Truncated {
		if mentionsSelection {
			return resourceCheckSelectionDocument{}, false, errors.New("selection block must be in the complete, untruncated final assistant message")
		}
		return resourceCheckSelectionDocument{}, false, nil
	}
	if len(payload.Text) > resourceCheckSelectionTextLimit || !utf8.ValidString(payload.Text) {
		if mentionsSelection {
			return resourceCheckSelectionDocument{}, false, errors.New("selection block exceeds the bounded final-message contract")
		}
		return resourceCheckSelectionDocument{}, false, nil
	}
	if !mentionsSelection {
		return resourceCheckSelectionDocument{}, false, nil
	}
	if strings.Count(payload.Text, resourceCheckSelectionFence) != 1 {
		return resourceCheckSelectionDocument{}, false, errors.New("final message must contain exactly one strict chora-check-selection block")
	}
	start := strings.Index(payload.Text, resourceCheckSelectionFence) + len(resourceCheckSelectionFence)
	remainder := payload.Text[start:]
	end := strings.Index(remainder, "\n```")
	if end < 0 {
		return resourceCheckSelectionDocument{}, false, errors.New("chora-check-selection block is not closed")
	}
	after := remainder[end+len("\n```"):]
	if after != "" && !strings.HasPrefix(after, "\n") {
		return resourceCheckSelectionDocument{}, false, errors.New("chora-check-selection closing fence must end its line")
	}
	body := []byte(remainder[:end])
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var document resourceCheckSelectionDocument
	if err := decoder.Decode(&document); err != nil {
		return resourceCheckSelectionDocument{}, false, fmt.Errorf("invalid chora-check-selection JSON: %w", err)
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		return resourceCheckSelectionDocument{}, false, errors.New("chora-check-selection must contain one JSON value")
	}
	return document, true, nil
}

func validateResourceCheckSelection(document resourceCheckSelectionDocument, auto map[string]speccoding.ExecutionRepositoryResource, frozen map[string][]*resourceCheckAuthority) (map[string][]*resourceCheckAuthority, error) {
	if len(document.Repositories) == 0 || len(document.Repositories) > domain.TaskRepositoryLimit {
		return nil, errors.New("selection must contain between one and sixteen automatic repositories")
	}
	result := make(map[string][]*resourceCheckAuthority, len(document.Repositories))
	seenRepositories := make(map[string]bool, len(document.Repositories))
	totalCommands := 0
	for _, repository := range document.Repositories {
		resource, exists := auto[repository.RepoID]
		if !exists || seenRepositories[repository.RepoID] {
			return nil, fmt.Errorf("repository %q is not one unique frozen automatic-check resource", repository.RepoID)
		}
		seenRepositories[repository.RepoID] = true
		explanation := strings.TrimSpace(repository.Explanation)
		if explanation == "" || len(explanation) > resourceCheckSelectionReasonLimit || !utf8.ValidString(explanation) || strings.IndexFunc(explanation, unicode.IsControl) >= 0 {
			return nil, fmt.Errorf("repository %s requires a bounded one-line selection explanation", repository.RepoID)
		}
		if repository.NoApplicableChecks {
			if len(repository.Checks) != 0 {
				return nil, fmt.Errorf("repository %s cannot combine checks with noApplicableChecks", repository.RepoID)
			}
			result[repository.RepoID] = []*resourceCheckAuthority{}
			continue
		}
		if len(repository.Checks) == 0 {
			return nil, fmt.Errorf("repository %s must select checks or explicitly report noApplicableChecks", repository.RepoID)
		}
		totalCommands += len(repository.Checks)
		if totalCommands > resourceCheckSelectionCommandLimit {
			return nil, fmt.Errorf("selection exceeds %d commands", resourceCheckSelectionCommandLimit)
		}
		seenDigests := make(map[string]bool, len(repository.Checks))
		for _, selected := range repository.Checks {
			name := strings.TrimSpace(selected.Name)
			if name == "" || len(name) > resourceCheckSelectionNameLimit || !utf8.ValidString(name) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
				return nil, fmt.Errorf("repository %s has an invalid check name", repository.RepoID)
			}
			if selected.Command != strings.TrimSpace(selected.Command) || selected.WorkingDirectory != strings.TrimSpace(selected.WorkingDirectory) || selected.WorkingDirectory == "" {
				return nil, fmt.Errorf("repository %s has a non-canonical check command or directory", repository.RepoID)
			}
			directoryProven := selected.WorkingDirectory == "."
			for _, frozenCommand := range resource.Checks.Commands {
				if selected.WorkingDirectory == frozenCommand.WorkingDirectory {
					directoryProven = true
				}
			}
			for _, preparation := range resource.Checks.Preparation {
				if selected.WorkingDirectory == preparation.WorkingDirectory {
					directoryProven = true
				}
			}
			if !directoryProven {
				return nil, fmt.Errorf("repository %s proposed check directory %q was not proven in the frozen policy", repository.RepoID, selected.WorkingDirectory)
			}
			argv, err := speccoding.ParseCheckCommand(selected.Command)
			if err != nil {
				return nil, fmt.Errorf("repository %s has an unsafe proposed check: %w", repository.RepoID, err)
			}
			canonical, _ := speccoding.CanonicalCheckCommand(argv)
			command := domain.TaskCheckCommand{Name: name, Version: 1, Command: canonical, Argv: argv, WorkingDirectory: selected.WorkingDirectory, Source: "agent_proposed"}
			probe := command
			probe.ID = "proposal"
			authority, err := newResourceCheckAuthority(resource.RepoID, resource.Checks.Mode, probe, resource.Locator)
			if err != nil {
				return nil, err
			}
			semanticDigest := resourceCheckSemanticDigest(authority)
			if seenDigests[semanticDigest] {
				return nil, fmt.Errorf("repository %s contains duplicate semantic check proposals", repository.RepoID)
			}
			seenDigests[semanticDigest] = true
			var matches []*resourceCheckAuthority
			for _, candidate := range frozen[repository.RepoID] {
				if resourceCheckSemanticDigest(candidate) == semanticDigest {
					matches = append(matches, candidate)
				}
			}
			if len(matches) > 1 {
				return nil, fmt.Errorf("repository %s proposal ambiguously matches multiple frozen candidates", repository.RepoID)
			}
			if len(matches) == 1 {
				result[repository.RepoID] = append(result[repository.RepoID], matches[0])
				continue
			}
			identity := sha256.Sum256([]byte("chora.agent-proposed-check.v1\x00" + repository.RepoID + "\x00" + semanticDigest))
			command.ID = "agent-" + hex.EncodeToString(identity[:])[:24]
			authority, err = newResourceCheckAuthority(resource.RepoID, resource.Checks.Mode, command, resource.Locator)
			if err != nil {
				return nil, err
			}
			result[repository.RepoID] = append(result[repository.RepoID], authority)
		}
	}
	return result, nil
}
