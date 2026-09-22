package domain

import (
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
)

const validProposalJSON = `{"schemaVersion":"chora.delegation-plan.v1","assignments":[{"role":"Researcher","title":"Compare designs","requirement":"Use supplied material; report uncertainties."}]}`

func proposalFence(body string) string {
	return "```chora-delegation-plan\n" + body + "\n```"
}

func TestDelegationProposalParsesMarkdownWithoutGrantingAuthority(t *testing.T) {
	for _, markdown := range []string{
		proposalFence(validProposalJSON),
		"# Proposed work\n\n" + proposalFence(validProposalJSON) + "\n\nThese findings need human review.",
		"```json\n{\"example\":true}\n```\n" + proposalFence(validProposalJSON),
		"````markdown\n" + proposalFence(validProposalJSON) + "\n````\n" + proposalFence(validProposalJSON),
		strings.ReplaceAll(proposalFence(validProposalJSON), "\n", "\r\n"),
	} {
		got, err := ParseDelegationProposal(markdown)
		want := DelegationProposal{SchemaVersion: DelegationProposalSchemaV1, Assignments: []DelegationAssignment{{Role: "Researcher", Title: "Compare designs", Requirement: "Use supplied material; report uncertainties."}}}
		if err != nil || !reflect.DeepEqual(got, want) {
			t.Fatalf("proposal=%#v err=%v", got, err)
		}
	}
}

func TestDelegationProposalRejectsAmbiguousOrUnboundedInput(t *testing.T) {
	cases := map[string]string{
		"missing fence":            validProposalJSON,
		"quoted example only":      "````markdown\n" + proposalFence(validProposalJSON) + "\n````",
		"unclosed block":           "```chora-delegation-plan\n" + validProposalJSON,
		"inline opening":           "Plan: " + proposalFence(validProposalJSON),
		"closing suffix":           proposalFence(validProposalJSON) + " extra",
		"multiple blocks":          proposalFence(validProposalJSON) + "\n" + proposalFence(validProposalJSON),
		"trailing JSON":            proposalFence(validProposalJSON + " {}"),
		"trailing garbage":         proposalFence(validProposalJSON + " garbage"),
		"invalid UTF8":             proposalFence(validProposalJSON) + string([]byte{0xff}),
		"unsupported version":      proposalFence(strings.Replace(validProposalJSON, ".v1", ".v2", 1)),
		"unknown authority":        proposalFence(strings.Replace(validProposalJSON, `"assignments":`, `"allowDelivery":true,"assignments":`, 1)),
		"wrong field case":         proposalFence(strings.Replace(validProposalJSON, "schemaVersion", "SchemaVersion", 1)),
		"duplicate version":        proposalFence(strings.Replace(validProposalJSON, `"assignments":`, `"schemaVersion":"chora.delegation-plan.v1","assignments":`, 1)),
		"escaped duplicate key":    proposalFence(strings.Replace(validProposalJSON, `"title":`, `"\u0072ole":"Other","title":`, 1)),
		"duplicate nested key":     proposalFence(strings.Replace(validProposalJSON, `"title":`, `"role":"Other","title":`, 1)),
		"unknown nested field":     proposalFence(strings.Replace(validProposalJSON, `"title":`, `"resources":["private"],"title":`, 1)),
		"missing assignment field": proposalFence(strings.Replace(validProposalJSON, `"role":"Researcher",`, "", 1)),
		"null assignment field":    proposalFence(strings.Replace(validProposalJSON, `"Researcher"`, "null", 1)),
		"non text field":           proposalFence(strings.Replace(validProposalJSON, `"Researcher"`, "123", 1)),
		"null document":            proposalFence("null"),
		"array document":           proposalFence("[]"),
		"no assignments":           proposalFence(`{"schemaVersion":"chora.delegation-plan.v1","assignments":[]}`),
		"null assignments":         proposalFence(`{"schemaVersion":"chora.delegation-plan.v1","assignments":null}`),
		"oversized body":           proposalFence(validProposalJSON + strings.Repeat(" ", MaxDelegationProposalBodyBytes)),
		"malformed JSON":           proposalFence(strings.TrimSuffix(validProposalJSON, "}")),
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := ParseDelegationProposal(input)
			if !errors.Is(err, ErrInvalidArgument) || !reflect.DeepEqual(got, DelegationProposal{}) {
				t.Fatalf("invalid proposal accepted or partial proposal leaked: %#v err=%v", got, err)
			}
		})
	}
}

func TestDelegationProposalAssignmentBounds(t *testing.T) {
	valid := DelegationProposal{SchemaVersion: DelegationProposalSchemaV1, Assignments: []DelegationAssignment{
		{Role: "Researcher", Title: "研究", Requirement: "Use supplied material."},
	}}
	cases := []struct {
		name  string
		edit  func(*DelegationProposal)
		valid bool
	}{
		{"four assignments", func(p *DelegationProposal) {
			for _, role := range []string{"Designer", "Reviewer", "Editor"} {
				p.Assignments = append(p.Assignments, DelegationAssignment{Role: role, Title: role, Requirement: "Review material."})
			}
		}, true},
		{"five assignments", func(p *DelegationProposal) {
			for _, role := range []string{"A", "B", "C", "D"} {
				p.Assignments = append(p.Assignments, DelegationAssignment{Role: role, Title: role, Requirement: "Review material."})
			}
		}, false},
		{"duplicate roles", func(p *DelegationProposal) {
			p.Assignments = append(p.Assignments, DelegationAssignment{Role: " researcher ", Title: "Other", Requirement: "Other"})
		}, false},
		{"empty requirement", func(p *DelegationProposal) { p.Assignments[0].Requirement = " " }, false},
		{"long role", func(p *DelegationProposal) { p.Assignments[0].Role = strings.Repeat("中", 81) }, false},
		{"long title", func(p *DelegationProposal) { p.Assignments[0].Title = strings.Repeat("中", 257) }, false},
		{"long requirement", func(p *DelegationProposal) { p.Assignments[0].Requirement = strings.Repeat("中", 4001) }, false},
		{"control text", func(p *DelegationProposal) { p.Assignments[0].Title = "title\x00" }, false},
		{"field limits", func(p *DelegationProposal) {
			p.Assignments[0] = DelegationAssignment{Role: strings.Repeat("中", 80), Title: strings.Repeat("中", 256), Requirement: strings.Repeat("中", 4000)}
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := valid
			p.Assignments = append([]DelegationAssignment(nil), valid.Assignments...)
			tc.edit(&p)
			raw, err := json.Marshal(p)
			if err != nil {
				t.Fatal(err)
			}
			_, err = ParseDelegationProposal(proposalFence(string(raw)))
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestDelegationProposalBodyByteLimitIncludesWhitespace(t *testing.T) {
	body := validProposalJSON + strings.Repeat(" ", MaxDelegationProposalBodyBytes-len(validProposalJSON)-1)
	if _, err := ParseDelegationProposal(proposalFence(body)); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseDelegationProposal(proposalFence(body + " ")); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("oversized body accepted: %v", err)
	}
}
