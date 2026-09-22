package domain

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const DelegationProposalSchemaV1 = "chora.delegation-plan.v1"
const MaxDelegationProposalBodyBytes = 32 << 10

// DelegationProposal is untrusted proposed work, not execution authorization.
// Callers must independently prove source provenance and human authorization.
type DelegationProposal struct {
	SchemaVersion string                 `json:"schemaVersion"`
	Assignments   []DelegationAssignment `json:"assignments"`
}

// ParseDelegationProposal accepts one exact, standalone triple-backtick plan
// fence, optionally surrounded by Markdown prose. Other fenced code blocks are
// skipped, so quoted examples cannot masquerade as the actual proposal.
func ParseDelegationProposal(markdown string) (DelegationProposal, error) {
	bad := func(reason string) (DelegationProposal, error) {
		return DelegationProposal{}, fmt.Errorf("%w: delegation proposal: %s", ErrInvalidArgument, reason)
	}
	if !utf8.ValidString(markdown) {
		return bad("invalid UTF-8")
	}
	var body strings.Builder
	found, collecting := false, false
	var fence byte
	fenceLength := 0
	for _, raw := range strings.SplitAfter(markdown, "\n") {
		line := strings.TrimSuffix(strings.TrimSuffix(raw, "\n"), "\r")
		if collecting {
			if line == "```" {
				collecting = false
				continue
			}
			if body.Len()+len(raw) > MaxDelegationProposalBodyBytes {
				return bad("plan body exceeds 32 KiB")
			}
			body.WriteString(raw)
			continue
		}
		// Markdown fences permit up to three leading spaces. Only the exact
		// unindented plan opener below is part of this machine contract.
		trimmed := strings.TrimLeft(line, " ")
		if len(line)-len(trimmed) > 3 {
			continue
		}
		if fence != 0 {
			if len(trimmed) >= fenceLength && strings.Trim(trimmed, string(fence)+" \t") == "" && strings.HasPrefix(trimmed, strings.Repeat(string(fence), fenceLength)) {
				fence = 0
			}
			continue
		}
		if line == "```chora-delegation-plan" {
			if found {
				return bad("multiple plan blocks")
			}
			found, collecting = true, true
			continue
		}
		if len(trimmed) >= 3 && (trimmed[0] == '`' || trimmed[0] == '~') {
			n := 0
			for n < len(trimmed) && trimmed[n] == trimmed[0] {
				n++
			}
			if n >= 3 {
				fence, fenceLength = trimmed[0], n
			}
		}
	}
	if !found || collecting {
		return bad("one closed plan block is required")
	}
	decoder := json.NewDecoder(strings.NewReader(body.String()))
	if err := uniqueProposalJSONValue(decoder, 0); err != nil {
		return bad(err.Error())
	}
	if _, err := decoder.Token(); err != io.EOF {
		return bad("plan must contain one JSON value")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(body.String()), &fields); err != nil || !exactProposalFields(fields, "schemaVersion", "assignments") {
		return bad("expected schemaVersion and assignments only")
	}
	var proposal DelegationProposal
	if err := json.Unmarshal(fields["schemaVersion"], &proposal.SchemaVersion); err != nil || proposal.SchemaVersion != DelegationProposalSchemaV1 {
		return bad("unsupported schema version")
	}
	var assignments []map[string]json.RawMessage
	if err := json.Unmarshal(fields["assignments"], &assignments); err != nil || len(assignments) < 1 || len(assignments) > MaxDelegationAssignments {
		return bad("expected one to four assignments")
	}
	seen := map[string]bool{}
	for _, fields := range assignments {
		if !exactProposalFields(fields, "role", "title", "requirement") {
			return bad("assignment requires role, title and requirement only")
		}
		var a DelegationAssignment
		if json.Unmarshal(fields["role"], &a.Role) != nil || json.Unmarshal(fields["title"], &a.Title) != nil || json.Unmarshal(fields["requirement"], &a.Requirement) != nil {
			return bad("assignment fields must be text")
		}
		// Match TaskDelegation's assignment constraints without inventing
		// a Task identity or an authorizing actor for an untrusted proposal.
		role := strings.ToLower(strings.TrimSpace(a.Role))
		if !ValidTrustedContextText(a.Role, 80, true) || !ValidTrustedContextText(a.Title, 256, true) || !ValidTrustedContextText(a.Requirement, 4000, true) || seen[role] {
			return bad("invalid assignment or duplicate role")
		}
		seen[role] = true
		proposal.Assignments = append(proposal.Assignments, a)
	}
	return proposal, nil
}

func exactProposalFields(fields map[string]json.RawMessage, names ...string) bool {
	if len(fields) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := fields[name]; !ok {
			return false
		}
	}
	return true
}

// Token inspection detects duplicate decoded keys, including escaped aliases,
// before encoding/json can silently replace their earlier values.
func uniqueProposalJSONValue(decoder *json.Decoder, depth int) error {
	if depth > 16 {
		return fmt.Errorf("JSON nesting exceeds proposal contract")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delim, container := token.(json.Delim)
	if !container {
		return nil
	}
	seen := map[string]bool{}
	for decoder.More() {
		if delim == '{' {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return fmt.Errorf("duplicate or invalid JSON key")
			}
			seen[name] = true
		}
		if err := uniqueProposalJSONValue(decoder, depth+1); err != nil {
			return err
		}
	}
	_, err = decoder.Token()
	return err
}
