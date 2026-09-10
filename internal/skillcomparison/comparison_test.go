package skillcomparison

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"testing"
)

func TestMatchedRealTaskPassIsDeterministicAndEmitsOnlyBoundIdentities(t *testing.T) {
	evidence := passingEvidence()
	encoded := encodeEvidence(t, evidence)

	first := EvaluateJSON(encoded)
	second := EvaluateJSON(encoded)
	if first.Status() != StatusPass || !reflect.DeepEqual(first, second) {
		t.Fatalf("decision = %#v / %#v", first, second)
	}
	projection, ok := first.Projection()
	if first.Reasons() == nil || len(first.Reasons()) != 0 || !ok || projection.SkillID() != CandidateID ||
		projection.SkillSHA256() != CandidateSkillSHA256 || projection.CapabilityBundleID() != CapabilityBundleID ||
		!validDigest(projection.CapabilityBundleSHA256()) || projection.ToolBundleID() != evidence.Baseline.Binding.ToolBundleID ||
		projection.ToolBundleSHA256() != evidence.Baseline.Binding.ToolBundleSHA256 {
		t.Fatalf("PASS projection = %#v", first)
	}
	pretty, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil || !reflect.DeepEqual(EvaluateJSON(pretty), first) {
		t.Fatalf("equivalent JSON changed decision: %v", err)
	}
	firstJSON, err := EncodeDecision(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := EncodeDecision(second)
	if err != nil || !bytes.Equal(firstJSON, secondJSON) {
		t.Fatalf("decision encoding drifted: %v", err)
	}

	changedTools := evidence
	changedTools.Baseline.Binding.ToolBundleSHA256 = strings.Repeat("9", 64)
	changedTools.Candidate.Binding.ToolBundleSHA256 = strings.Repeat("9", 64)
	changed := Evaluate(changedTools)
	changedProjection, changedOK := changed.Projection()
	if changed.Status() != StatusPass || !changedOK ||
		changedProjection.CapabilityBundleSHA256() == projection.CapabilityBundleSHA256() ||
		changedProjection.SkillSHA256() != projection.SkillSHA256() {
		t.Fatalf("tool-bound bundle projection = %#v", changed)
	}
}

func TestEveryMatchedBindingFieldIsRequiredAndCompared(t *testing.T) {
	tests := []struct {
		name, reason string
		mutate       func(*Binding)
	}{
		{"task ID", "unmatched_bindings", func(value *Binding) { value.TaskID = "another-task" }},
		{"task digest", "unmatched_bindings", func(value *Binding) { value.TaskSHA256 = strings.Repeat("1", 64) }},
		{"snapshot ID", "unmatched_bindings", func(value *Binding) { value.SnapshotID = "another-snapshot" }},
		{"snapshot digest", "unmatched_bindings", func(value *Binding) { value.SnapshotSHA256 = strings.Repeat("2", 64) }},
		{"Engine identity", "unmatched_bindings", func(value *Binding) { value.EngineIdentitySHA256 = strings.Repeat("3", 64) }},
		{"Runtime image", "unmatched_bindings", func(value *Binding) { value.RuntimeImageID = "sha256:" + strings.Repeat("4", 64) }},
		{"model provider", "unmatched_bindings", func(value *Binding) { value.ModelProviderID = "other-provider" }},
		{"model ID", "unmatched_bindings", func(value *Binding) { value.ModelID = "gpt-5.6-terra" }},
		{"model digest", "unmatched_bindings", func(value *Binding) { value.ModelSHA256 = strings.Repeat("5", 64) }},
		{"limits ID", "unmatched_bindings", func(value *Binding) {
			value.Limits.ID = "other-limits"
			value.Limits.SHA256 = ComputeLimitsSHA256(value.Limits)
		}},
		{"limits digest", "invalid_candidate", func(value *Binding) { value.Limits.SHA256 = strings.Repeat("6", 64) }},
		{"token limit", "unmatched_bindings", func(value *Binding) {
			value.Limits.MaxTotalTokens++
			value.Limits.SHA256 = ComputeLimitsSHA256(value.Limits)
		}},
		{"tool call limit", "unmatched_bindings", func(value *Binding) {
			value.Limits.MaxToolCalls++
			value.Limits.SHA256 = ComputeLimitsSHA256(value.Limits)
		}},
		{"elapsed limit", "unmatched_bindings", func(value *Binding) {
			value.Limits.MaxElapsedMillis++
			value.Limits.SHA256 = ComputeLimitsSHA256(value.Limits)
		}},
		{"cost limit", "unmatched_bindings", func(value *Binding) {
			value.Limits.MaxCostMicrounits++
			value.Limits.SHA256 = ComputeLimitsSHA256(value.Limits)
		}},
		{"tool bundle ID", "unmatched_bindings", func(value *Binding) { value.ToolBundleID = "other-tools" }},
		{"tool bundle digest", "unmatched_bindings", func(value *Binding) { value.ToolBundleSHA256 = strings.Repeat("7", 64) }},
		{"candidate ID", "invalid_candidate", func(value *Binding) { value.CandidateID = "other-candidate" }},
		{"candidate digest", "invalid_candidate", func(value *Binding) { value.CandidateSkillSHA256 = strings.Repeat("8", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := passingEvidence()
			test.mutate(&evidence.Candidate.Binding)
			decision := Evaluate(evidence)
			assertNoSkill(t, decision)
			if !slices.Contains(decision.Reasons(), test.reason) {
				t.Fatalf("reasons = %#v, want %q", decision.Reasons(), test.reason)
			}
		})
	}
}

func TestPairedRunsRequireDistinctRunAttemptAndEvidenceIdentities(t *testing.T) {
	tests := []struct {
		name, reason string
		mutate       func(*Evidence)
	}{
		{"run", "duplicate_run_identity", func(value *Evidence) { value.Candidate.RunID = value.Baseline.RunID }},
		{"attempt", "duplicate_run_identity", func(value *Evidence) { value.Candidate.AttemptID = value.Baseline.AttemptID }},
		{"evidence", "duplicate_run_evidence", func(value *Evidence) { value.Candidate.Outcome.EvidenceSHA256 = value.Baseline.Outcome.EvidenceSHA256 }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := passingEvidence()
			test.mutate(&evidence)
			assertReason(t, Evaluate(evidence), test.reason)
		})
	}

	t.Run("equal result is valid when produced by distinct evidence", func(t *testing.T) {
		evidence := passingEvidence()
		evidence.Candidate.Outcome.ResultSHA256 = evidence.Baseline.Outcome.ResultSHA256
		decision := Evaluate(evidence)
		if _, ok := decision.Projection(); decision.Status() != StatusPass || !ok {
			t.Fatalf("equal result rejected: %#v", decision)
		}
	})
}

func TestEveryRegressionFailsClosedToNoSkill(t *testing.T) {
	tests := []struct {
		name, reason string
		mutate       func(*Outcome)
	}{
		{"success", "candidate_success_regression", func(value *Outcome) { value.Success = false }},
		{"manual intervention", "manual_intervention_regression", func(value *Outcome) { value.ManualInterventions++ }},
		{"tool errors", "tool_error_regression", func(value *Outcome) { value.ToolErrors++ }},
		{"tokens", "token_cost_regression", func(value *Outcome) { value.OutputTokens++ }},
		{"tool calls", "tool_call_cost_regression", func(value *Outcome) { value.ToolCalls++ }},
		{"elapsed time", "elapsed_cost_regression", func(value *Outcome) { value.ElapsedMillis++ }},
		{"monetary cost", "monetary_cost_regression", func(value *Outcome) { value.CostMicrounits++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			evidence := passingEvidence()
			test.mutate(&evidence.Candidate.Outcome)
			decision := Evaluate(evidence)
			assertNoSkill(t, decision)
			if !slices.Contains(decision.Reasons(), test.reason) {
				t.Fatalf("reasons = %#v, want %q", decision.Reasons(), test.reason)
			}
		})
	}
}

func TestAbsoluteCostBoundsAndSuccessfulBaselineAreMandatory(t *testing.T) {
	t.Run("unsuccessful baseline", func(t *testing.T) {
		evidence := passingEvidence()
		evidence.Baseline.Outcome.Success = false
		decision := Evaluate(evidence)
		assertReason(t, decision, "baseline_unsuccessful")
	})

	for _, test := range []struct {
		name   string
		mutate func(*Outcome, Limits)
	}{
		{"tokens", func(value *Outcome, limits Limits) { value.InputTokens = limits.MaxTotalTokens; value.OutputTokens = 1 }},
		{"tool calls", func(value *Outcome, limits Limits) { value.ToolCalls = limits.MaxToolCalls + 1 }},
		{"elapsed", func(value *Outcome, limits Limits) { value.ElapsedMillis = limits.MaxElapsedMillis + 1 }},
		{"cost", func(value *Outcome, limits Limits) { value.CostMicrounits = limits.MaxCostMicrounits + 1 }},
	} {
		t.Run(test.name, func(t *testing.T) {
			evidence := passingEvidence()
			test.mutate(&evidence.Candidate.Outcome, evidence.Candidate.Binding.Limits)
			assertReason(t, Evaluate(evidence), "candidate_cost_out_of_bounds")
		})
	}
}

func TestHiddenAndFrameworkRetriesInvalidateEitherRun(t *testing.T) {
	for _, side := range []string{"baseline", "candidate"} {
		for _, test := range []struct {
			name   string
			mutate func(*Outcome)
		}{
			{"attempt count", func(value *Outcome) { value.AttemptCount = 2 }},
			{"hidden retry", func(value *Outcome) { value.HiddenRetryCount = 1 }},
			{"framework retry", func(value *Outcome) { value.FrameworkRetryCount = 1 }},
		} {
			t.Run(side+" "+test.name, func(t *testing.T) {
				evidence := passingEvidence()
				if side == "baseline" {
					test.mutate(&evidence.Baseline.Outcome)
				} else {
					test.mutate(&evidence.Candidate.Outcome)
				}
				decision := Evaluate(evidence)
				assertNoSkill(t, decision)
				if want := "invalid_" + side; !slices.Contains(decision.Reasons(), want) {
					t.Fatalf("reasons = %#v, want %q", decision.Reasons(), want)
				}
			})
		}
	}
}

func TestInvalidMissingAndUnmatchedJSONNeverEmitSkillAuthority(t *testing.T) {
	valid := encodeEvidence(t, passingEvidence())
	unknown := bytes.Replace(valid, []byte(`"comparison_id"`), []byte(`"unknown":true,"comparison_id"`), 1)
	duplicate := bytes.Replace(valid, []byte(`"comparison_id":"synthetic-match"`), []byte(`"comparison_id":"first","comparison_id":"synthetic-match"`), 1)
	oversized := bytes.Repeat([]byte("x"), maxEvidenceBytes+1)
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"empty", nil},
		{"missing", []byte(`{}`)},
		{"unknown", unknown},
		{"duplicate", duplicate},
		{"trailing", append(append([]byte(nil), valid...), []byte(`{}`)...)},
		{"oversized", oversized},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := EvaluateJSON(test.data)
			second := EvaluateJSON(test.data)
			assertNoSkill(t, first)
			if !reflect.DeepEqual(first, second) {
				t.Fatalf("nondeterministic invalid decision: %#v / %#v", first, second)
			}
		})
	}

	fixture, err := os.ReadFile(filepath.Join("testdata", "unmatched.no-skill.v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	decision := EvaluateJSON(fixture)
	assertNoSkill(t, decision)
	if !slices.Contains(decision.Reasons(), "unmatched_bindings") {
		t.Fatalf("negative fixture reasons = %#v", decision.Reasons())
	}
}

func TestCandidateAndSchemaAssetsAreExactInertDistributionInputs(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("caller path unavailable")
	}
	distribution := filepath.Clean(filepath.Join(filepath.Dir(source), "..", "..", "distribution", "v1", "capabilities"))
	skill, err := os.ReadFile(filepath.Join(distribution, "core-skill.v1.md"))
	if err != nil {
		t.Fatal(err)
	}
	if digest(skill) != CandidateSkillSHA256 || len(strings.Split(strings.TrimSuffix(string(skill), "\n"), "\n")) != 10 {
		t.Fatalf("candidate identity/line count drifted: %s", digest(skill))
	}
	schemaBytes := mustReadFile(t, filepath.Join(distribution, "core-skill-comparison.schema.v1.json"))
	var schema map[string]any
	readJSONFile(t, filepath.Join(distribution, "core-skill-comparison.schema.v1.json"), &schema)
	if schema["$id"] != "https://chora.local/schemas/core-skill-comparison.v1.json" {
		t.Fatalf("schema identity = %#v", schema["$id"])
	}
	for _, exact := range []string{
		`"const": "no_skill"`,
		`"const": "candidate_skill"`,
		`"const": "` + CandidateID + `"`,
		`"const": "` + CandidateSkillSHA256 + `"`,
	} {
		if !bytes.Contains(schemaBytes, []byte(exact)) {
			t.Fatalf("schema does not bind %s", exact)
		}
	}
	var manifest struct {
		SchemaVersion        string `json:"schema_version"`
		CandidateID          string `json:"candidate_id"`
		SkillPath            string `json:"skill_path"`
		SkillSHA256          string `json:"skill_sha256"`
		ComparisonSchemaPath string `json:"comparison_schema_path"`
		ComparisonSchemaSHA  string `json:"comparison_schema_sha256"`
		CapabilityBundleID   string `json:"capability_bundle_id"`
		StandardPolicyID     string `json:"standard_policy_id"`
		Activation           string `json:"activation"`
	}
	readJSONFile(t, filepath.Join(distribution, "core-skill-candidate.v1.json"), &manifest)
	if manifest.SchemaVersion != "chora.core-skill-candidate.v1" || manifest.CandidateID != CandidateID ||
		manifest.SkillSHA256 != CandidateSkillSHA256 || manifest.Activation != "conditional_comparison_required" ||
		manifest.SkillPath != "distribution/v1/capabilities/core-skill.v1.md" ||
		manifest.ComparisonSchemaPath != "distribution/v1/capabilities/core-skill-comparison.schema.v1.json" ||
		manifest.ComparisonSchemaSHA != digest(schemaBytes) ||
		manifest.CapabilityBundleID != CapabilityBundleID || manifest.StandardPolicyID != StandardPolicyID {
		t.Fatalf("candidate manifest = %#v", manifest)
	}
}

func TestDecisionEncodingRejectsPartialOrFabricatedAuthority(t *testing.T) {
	noSkill := EvaluateJSON(nil)
	if _, err := EncodeDecision(noSkill); err != nil {
		t.Fatal(err)
	}
	partial := noSkill
	partial.projection.skillSHA256 = CandidateSkillSHA256
	if _, err := EncodeDecision(partial); err == nil {
		t.Fatal("NO_SKILL with Skill projection encoded")
	}
	pass := Evaluate(passingEvidence())
	pass.projection.capabilityBundleSHA256 = strings.Repeat("f", 64)
	if _, err := EncodeDecision(pass); err == nil {
		t.Fatal("fabricated PASS bundle digest encoded")
	}
	reasons := noSkill.Reasons()
	reasons[0] = "mutated"
	if noSkill.Reasons()[0] == "mutated" {
		t.Fatal("caller mutated immutable decision reasons")
	}
}

func passingEvidence() Evidence {
	limits := Limits{
		ID: "synthetic-limits", MaxTotalTokens: 2000, MaxToolCalls: 20,
		MaxElapsedMillis: 60000, MaxCostMicrounits: 2000,
	}
	limits.SHA256 = ComputeLimitsSHA256(limits)
	binding := Binding{
		TaskID: "task-a", TaskSHA256: strings.Repeat("a", 64),
		SnapshotID: "snapshot-a", SnapshotSHA256: strings.Repeat("b", 64),
		EngineIdentitySHA256: strings.Repeat("c", 64), RuntimeImageID: "sha256:" + strings.Repeat("d", 64),
		ModelProviderID: "openai-codex", ModelID: "gpt-5.6-sol", ModelSHA256: strings.Repeat("e", 64), Limits: limits,
		ToolBundleID: "synthetic-tools", ToolBundleSHA256: strings.Repeat("f", 64),
		CandidateID: CandidateID, CandidateSkillSHA256: CandidateSkillSHA256,
	}
	baseline := Outcome{
		Success: true, ManualInterventions: 1, ToolErrors: 1,
		InputTokens: 800, OutputTokens: 400, ToolCalls: 12, ElapsedMillis: 45000, CostMicrounits: 1200,
		AttemptCount: 1, ResultSHA256: strings.Repeat("1", 64), EvidenceSHA256: strings.Repeat("2", 64),
	}
	candidate := Outcome{
		Success: true, ManualInterventions: 1, ToolErrors: 1,
		InputTokens: 800, OutputTokens: 400, ToolCalls: 12, ElapsedMillis: 45000, CostMicrounits: 1200,
		AttemptCount: 1, ResultSHA256: strings.Repeat("3", 64), EvidenceSHA256: strings.Repeat("4", 64),
	}
	return Evidence{
		SchemaVersion: SchemaVersion, ComparisonID: "synthetic-match",
		Baseline: Run{
			Variant: VariantNoSkill, RealTask: true, RunID: "baseline-run", AttemptID: "baseline-attempt",
			Binding: binding, Outcome: baseline,
		},
		Candidate: Run{
			Variant: VariantCandidateSkill, RealTask: true, RunID: "candidate-run", AttemptID: "candidate-attempt",
			Binding: binding, Outcome: candidate,
		},
	}
}

func encodeEvidence(t *testing.T, evidence Evidence) []byte {
	t.Helper()
	encoded, err := json.Marshal(evidence)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func assertNoSkill(t *testing.T, decision Decision) {
	t.Helper()
	_, projected := decision.Projection()
	if decision.Status() != StatusNoSkill || len(decision.Reasons()) == 0 || projected {
		t.Fatalf("NO_SKILL decision leaked authority: %#v", decision)
	}
}

func assertReason(t *testing.T, decision Decision, reason string) {
	t.Helper()
	assertNoSkill(t, decision)
	if !slices.Contains(decision.Reasons(), reason) {
		t.Fatalf("reasons = %#v, want %q", decision.Reasons(), reason)
	}
}

func readJSONFile(t *testing.T, path string, target any) {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil || requireEOF(decoder) != nil {
		t.Fatalf("read %s: %v", path, err)
	}
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
