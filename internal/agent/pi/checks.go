package pi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/speccoding"
	"strings"
)

func (adapter *Adapter) AgentReportedChecks(raw []byte, events []execution.NormalizedEvent) ([]execution.DeclaredCheck, error) {
	contract, err := speccoding.DecodeCoreContract(raw)
	if err != nil {
		return nil, err
	}
	return DeriveAgentReportedChecks(contract.Document(), events), nil
}

type agentCommandClaim struct {
	status     execution.CheckStatus
	evidence   string
	generation int
}

type activeAgentCommand struct {
	commandID  string
	generation int
}

// derivePiAgentReportedChecks converts only exact, contract-declared
// verification command executions into AgentReport claims. These observations
// are intentionally not verifier evidence: callers place them only in
// TerminalResult.Checks, which is the AgentReport input.
func DeriveAgentReportedChecks(document speccoding.CoreContractDocument, events []execution.NormalizedEvent) []execution.DeclaredCheck {
	type commandAuthority struct {
		id string
	}
	summaryAuthorities := make(map[string][]commandAuthority, len(document.Acceptance.VerificationCommands))
	digestAuthorities := make(map[string][]commandAuthority, len(document.Acceptance.VerificationCommands))
	states := make(map[string]agentCommandClaim, len(document.Acceptance.VerificationCommands))
	for _, command := range document.Acceptance.VerificationCommands {
		text, err := speccoding.CanonicalCommandText(command)
		if err != nil {
			continue
		}
		digest := sha256.Sum256([]byte(text))
		digestHex := hex.EncodeToString(digest[:])
		authority := commandAuthority{id: command.ID}
		summaryAuthorities[text] = append(summaryAuthorities[text], authority)
		digestAuthorities[digestHex] = append(digestAuthorities[digestHex], authority)
		states[command.ID] = agentCommandClaim{status: execution.CheckUnknown, evidence: command.ID + " was not observed"}
	}

	active := make(map[string][]activeAgentCommand)
	for _, event := range events {
		var value struct {
			ToolName      string  `json:"tool_name"`
			ToolCallID    string  `json:"tool_call_id"`
			Command       string  `json:"redacted_command_summary"`
			CommandDigest *string `json:"command_digest"`
			ExitCode      *int64  `json:"exit_code"`
			ToolError     *bool   `json:"tool_error"`
		}
		if json.Unmarshal(event.NormalizedJSON, &value) != nil {
			continue
		}
		switch event.Type {
		case "tool_start":
			if value.ToolName != "bash" {
				continue
			}
			commands := summaryAuthorities[value.Command]
			if value.CommandDigest != nil {
				// A projected digest is authoritative for correlation even when it
				// is malformed or unknown. Falling back to the redacted/truncated
				// summary would let a digest-bearing event claim another command.
				commands = digestAuthorities[*value.CommandDigest]
			}
			if len(commands) == 0 {
				continue
			}
			started := make([]activeAgentCommand, 0, len(commands))
			for _, command := range commands {
				state := states[command.id]
				state.generation++
				state.status = execution.CheckUnknown
				state.evidence = command.id + " has no correlatable end event"
				states[command.id] = state
				started = append(started, activeAgentCommand{commandID: command.id, generation: state.generation})
			}
			if strings.TrimSpace(value.ToolCallID) != "" {
				active[value.ToolCallID] = append(active[value.ToolCallID], started...)
			}
		case "tool_end":
			if strings.TrimSpace(value.ToolCallID) == "" {
				continue
			}
			started := active[value.ToolCallID]
			delete(active, value.ToolCallID)
			for _, executionStart := range started {
				state := states[executionStart.commandID]
				if state.generation != executionStart.generation {
					continue
				}
				switch {
				case value.ToolError != nil && *value.ToolError:
					state.status = execution.CheckFail
					state.evidence = executionStart.commandID + " reported a tool error"
				case value.ExitCode == nil && value.ToolError != nil && !*value.ToolError:
					state.status = execution.CheckPass
					state.evidence = executionStart.commandID + " completed successfully according to Pi's tool result; numeric exit code unavailable"
				case value.ExitCode == nil:
					state.status = execution.CheckUnknown
					state.evidence = executionStart.commandID + " ended without an exit code"
				case *value.ExitCode == 0:
					state.status = execution.CheckPass
					state.evidence = executionStart.commandID + " exited 0"
				default:
					state.status = execution.CheckFail
					state.evidence = fmt.Sprintf("%s exited %d", executionStart.commandID, *value.ExitCode)
				}
				states[executionStart.commandID] = state
			}
		}
	}

	checks := make([]execution.DeclaredCheck, 0, len(document.Acceptance.Criteria))
	for _, criterion := range document.Acceptance.Criteria {
		status := execution.CheckPass
		if len(criterion.VerificationCommandIDs) == 0 {
			status = execution.CheckUnknown
		}
		parts := make([]string, 0, len(criterion.VerificationCommandIDs))
		for _, commandID := range criterion.VerificationCommandIDs {
			state, ok := states[commandID]
			if !ok {
				state = agentCommandClaim{status: execution.CheckUnknown, evidence: commandID + " has no declared command authority"}
			}
			parts = append(parts, state.evidence)
			if state.status == execution.CheckFail {
				status = execution.CheckFail
			} else if state.status == execution.CheckUnknown && status != execution.CheckFail {
				status = execution.CheckUnknown
			}
		}
		if len(criterion.VerificationCommandIDs) == 0 {
			parts = append(parts, "Not run / Unverified: no checks were selected")
		}
		checks = append(checks, execution.DeclaredCheck{
			CriterionID: criterion.ID,
			Status:      status,
			Evidence:    "Agent-reported, non-authoritative: " + strings.Join(parts, "; "),
		})
	}
	return checks
}
