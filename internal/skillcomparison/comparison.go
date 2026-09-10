package skillcomparison

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"strconv"
)

const (
	SchemaVersion         = "chora.core-skill-comparison.v1"
	DecisionSchemaVersion = "chora.core-skill-decision.v1"
	LimitsSchemaVersion   = "chora.core-skill-limits.v1"

	CandidateID          = "chora.core-workflow.v1"
	CandidateSkillSHA256 = "a39a78290abaa14c42fb90875611967297db66b6b41dba6040676d7a486ac67c"
	CapabilityBundleID   = "chora.standard-core.v1"
	StandardPolicyID     = "chora.standard.v1"

	VariantNoSkill        = "no_skill"
	VariantCandidateSkill = "candidate_skill"

	StatusPass    = "PASS"
	StatusNoSkill = "NO_SKILL"

	maxEvidenceBytes = 1 << 20
)

var (
	idPattern      = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)
	digestPattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
	imageIDPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// Evidence contains exactly one no-Skill run and one candidate-Skill run.
// Struct fields, rather than maps, make its canonical JSON representation
// deterministic after strict decoding.
type Evidence struct {
	SchemaVersion string `json:"schema_version"`
	ComparisonID  string `json:"comparison_id"`
	Baseline      Run    `json:"baseline"`
	Candidate     Run    `json:"candidate"`
}

type Run struct {
	Variant   string  `json:"variant"`
	RealTask  bool    `json:"real_task"`
	RunID     string  `json:"run_id"`
	AttemptID string  `json:"attempt_id"`
	Binding   Binding `json:"binding"`
	Outcome   Outcome `json:"outcome"`
}

// Binding is the complete matched-run identity. Both sides must be byte-value
// equal; in particular, the baseline binds which candidate was withheld.
type Binding struct {
	TaskID               string `json:"task_id"`
	TaskSHA256           string `json:"task_sha256"`
	SnapshotID           string `json:"snapshot_id"`
	SnapshotSHA256       string `json:"snapshot_sha256"`
	EngineIdentitySHA256 string `json:"engine_identity_sha256"`
	RuntimeImageID       string `json:"runtime_image_id"`
	ModelProviderID      string `json:"model_provider_id"`
	ModelID              string `json:"model_id"`
	ModelSHA256          string `json:"model_sha256"`
	Limits               Limits `json:"limits"`
	ToolBundleID         string `json:"tool_bundle_id"`
	ToolBundleSHA256     string `json:"tool_bundle_sha256"`
	CandidateID          string `json:"candidate_id"`
	CandidateSkillSHA256 string `json:"candidate_skill_sha256"`
}

// Limits uses integer units only. SHA256 must equal ComputeLimitsSHA256 for
// these exact values, preventing a caller from pairing friendly bounds with a
// digest that identifies some other policy.
type Limits struct {
	ID                string `json:"id"`
	SHA256            string `json:"sha256"`
	MaxTotalTokens    int64  `json:"max_total_tokens"`
	MaxToolCalls      int64  `json:"max_tool_calls"`
	MaxElapsedMillis  int64  `json:"max_elapsed_millis"`
	MaxCostMicrounits int64  `json:"max_cost_microunits"`
}

type Outcome struct {
	Success             bool   `json:"success"`
	ManualInterventions int64  `json:"manual_interventions"`
	ToolErrors          int64  `json:"tool_errors"`
	InputTokens         int64  `json:"input_tokens"`
	OutputTokens        int64  `json:"output_tokens"`
	ToolCalls           int64  `json:"tool_calls"`
	ElapsedMillis       int64  `json:"elapsed_millis"`
	CostMicrounits      int64  `json:"cost_microunits"`
	AttemptCount        int64  `json:"attempt_count"`
	HiddenRetryCount    int64  `json:"hidden_retry_count"`
	FrameworkRetryCount int64  `json:"framework_retry_count"`
	ResultSHA256        string `json:"result_sha256"`
	EvidenceSHA256      string `json:"evidence_sha256"`
}

// Decision is an immutable evaluator result with one of the two exact statuses.
// Its fields are deliberately private: callers can inspect it through methods
// but cannot construct or mutate a PASS projection without Evaluate.
type Decision struct {
	status         string
	evidenceSHA256 string
	reasons        []string
	projection     Projection
}

// Projection is non-zero only for PASS. It is immutable for the same reason as
// Decision and contains the complete later-integration identity.
type Projection struct {
	capabilityBundleID     string
	capabilityBundleSHA256 string
	skillID                string
	skillSHA256            string
	toolBundleID           string
	toolBundleSHA256       string
}

func (decision Decision) Status() string         { return decision.status }
func (decision Decision) EvidenceSHA256() string { return decision.evidenceSHA256 }
func (decision Decision) Reasons() []string      { return append([]string{}, decision.reasons...) }

func (decision Decision) Projection() (Projection, bool) {
	return decision.projection, decision.status == StatusPass
}

func (projection Projection) CapabilityBundleID() string { return projection.capabilityBundleID }
func (projection Projection) CapabilityBundleSHA256() string {
	return projection.capabilityBundleSHA256
}
func (projection Projection) SkillID() string          { return projection.skillID }
func (projection Projection) SkillSHA256() string      { return projection.skillSHA256 }
func (projection Projection) ToolBundleID() string     { return projection.toolBundleID }
func (projection Projection) ToolBundleSHA256() string { return projection.toolBundleSHA256 }

// EvaluateJSON strictly decodes evidence and fails closed without returning an
// error channel that a caller could accidentally ignore.
func EvaluateJSON(data []byte) Decision {
	rawDigest := digest(data)
	if len(data) == 0 || len(data) > maxEvidenceBytes || rejectDuplicateKeys(data) != nil {
		return noSkill(rawDigest, "invalid_json")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var evidence Evidence
	if err := decoder.Decode(&evidence); err != nil || requireEOF(decoder) != nil {
		return noSkill(rawDigest, "invalid_json")
	}
	return Evaluate(evidence)
}

// Evaluate applies validation and comparison in a fixed order, yielding stable
// reason ordering and a stable evidence digest.
func Evaluate(evidence Evidence) Decision {
	evidenceDigest := canonicalEvidenceDigest(evidence)
	reasons := make([]string, 0, 12)
	if evidence.SchemaVersion != SchemaVersion || !validID(evidence.ComparisonID) {
		reasons = append(reasons, "invalid_comparison_identity")
	}
	baselineValid := validRun(evidence.Baseline, VariantNoSkill)
	candidateValid := validRun(evidence.Candidate, VariantCandidateSkill)
	if !baselineValid {
		reasons = append(reasons, "invalid_baseline")
	}
	if !candidateValid {
		reasons = append(reasons, "invalid_candidate")
	}
	if baselineValid && candidateValid {
		if evidence.Baseline.Binding != evidence.Candidate.Binding {
			reasons = append(reasons, "unmatched_bindings")
		}
		if evidence.Baseline.RunID == evidence.Candidate.RunID || evidence.Baseline.AttemptID == evidence.Candidate.AttemptID {
			reasons = append(reasons, "duplicate_run_identity")
		}
		if evidence.Baseline.Outcome.EvidenceSHA256 == evidence.Candidate.Outcome.EvidenceSHA256 {
			reasons = append(reasons, "duplicate_run_evidence")
		}
	}

	// Regression comparisons are meaningful only after both run records and
	// their exact binding have validated.
	if len(reasons) == 0 {
		baseline, candidate := evidence.Baseline.Outcome, evidence.Candidate.Outcome
		limits := evidence.Baseline.Binding.Limits
		if !baseline.Success {
			reasons = append(reasons, "baseline_unsuccessful")
		}
		if !candidate.Success {
			reasons = append(reasons, "candidate_success_regression")
		}
		if candidate.ManualInterventions > baseline.ManualInterventions {
			reasons = append(reasons, "manual_intervention_regression")
		}
		if candidate.ToolErrors > baseline.ToolErrors {
			reasons = append(reasons, "tool_error_regression")
		}
		if !withinCostLimits(baseline, limits) {
			reasons = append(reasons, "baseline_cost_out_of_bounds")
		}
		if !withinCostLimits(candidate, limits) {
			reasons = append(reasons, "candidate_cost_out_of_bounds")
		}
		if totalTokens(candidate) > totalTokens(baseline) {
			reasons = append(reasons, "token_cost_regression")
		}
		if candidate.ToolCalls > baseline.ToolCalls {
			reasons = append(reasons, "tool_call_cost_regression")
		}
		if candidate.ElapsedMillis > baseline.ElapsedMillis {
			reasons = append(reasons, "elapsed_cost_regression")
		}
		if candidate.CostMicrounits > baseline.CostMicrounits {
			reasons = append(reasons, "monetary_cost_regression")
		}
	}
	if len(reasons) != 0 {
		return noSkill(evidenceDigest, reasons...)
	}

	binding := evidence.Baseline.Binding
	return Decision{
		status:         StatusPass,
		evidenceSHA256: evidenceDigest,
		reasons:        []string{},
		projection: Projection{
			capabilityBundleID:     CapabilityBundleID,
			capabilityBundleSHA256: capabilityBundleDigest(binding),
			skillID:                CandidateID,
			skillSHA256:            CandidateSkillSHA256,
			toolBundleID:           binding.ToolBundleID,
			toolBundleSHA256:       binding.ToolBundleSHA256,
		},
	}
}

// EncodeDecision emits deterministic JSON only for an internally consistent
// exact PASS or NO_SKILL decision.
func EncodeDecision(decision Decision) ([]byte, error) {
	if !validDecision(decision) {
		return nil, errors.New("invalid core Skill comparison decision")
	}
	type decisionRecord struct {
		SchemaVersion          string   `json:"schema_version"`
		Status                 string   `json:"status"`
		EvidenceSHA256         string   `json:"evidence_sha256"`
		Reasons                []string `json:"reasons"`
		CapabilityBundleID     string   `json:"capability_bundle_id,omitempty"`
		CapabilityBundleSHA256 string   `json:"capability_bundle_sha256,omitempty"`
		SkillID                string   `json:"skill_id,omitempty"`
		SkillSHA256            string   `json:"skill_sha256,omitempty"`
		ToolBundleID           string   `json:"tool_bundle_id,omitempty"`
		ToolBundleSHA256       string   `json:"tool_bundle_sha256,omitempty"`
	}
	record := decisionRecord{
		SchemaVersion: DecisionSchemaVersion, Status: decision.status,
		EvidenceSHA256: decision.evidenceSHA256, Reasons: decision.Reasons(),
	}
	if projection, ok := decision.Projection(); ok {
		record.CapabilityBundleID = projection.capabilityBundleID
		record.CapabilityBundleSHA256 = projection.capabilityBundleSHA256
		record.SkillID = projection.skillID
		record.SkillSHA256 = projection.skillSHA256
		record.ToolBundleID = projection.toolBundleID
		record.ToolBundleSHA256 = projection.toolBundleSHA256
	}
	encoded, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// ComputeLimitsSHA256 binds all integer bounds and the limits policy ID.
func ComputeLimitsSHA256(limits Limits) string {
	return hashFields(
		LimitsSchemaVersion,
		limits.ID,
		strconv.FormatInt(limits.MaxTotalTokens, 10),
		strconv.FormatInt(limits.MaxToolCalls, 10),
		strconv.FormatInt(limits.MaxElapsedMillis, 10),
		strconv.FormatInt(limits.MaxCostMicrounits, 10),
	)
}

func validRun(run Run, variant string) bool {
	return run.Variant == variant && run.RealTask && validID(run.RunID) && validID(run.AttemptID) &&
		validBinding(run.Binding) && validOutcome(run.Outcome)
}

func validBinding(binding Binding) bool {
	return validID(binding.TaskID) && validDigest(binding.TaskSHA256) &&
		validID(binding.SnapshotID) && validDigest(binding.SnapshotSHA256) &&
		validDigest(binding.EngineIdentitySHA256) && imageIDPattern.MatchString(binding.RuntimeImageID) &&
		validID(binding.ModelProviderID) && validID(binding.ModelID) && validDigest(binding.ModelSHA256) &&
		validLimits(binding.Limits) && validID(binding.ToolBundleID) &&
		validDigest(binding.ToolBundleSHA256) && binding.CandidateID == CandidateID &&
		binding.CandidateSkillSHA256 == CandidateSkillSHA256
}

func validLimits(limits Limits) bool {
	return validID(limits.ID) && validDigest(limits.SHA256) &&
		limits.MaxTotalTokens > 0 && limits.MaxToolCalls > 0 &&
		limits.MaxElapsedMillis > 0 && limits.MaxCostMicrounits > 0 &&
		limits.SHA256 == ComputeLimitsSHA256(limits)
}

func validOutcome(outcome Outcome) bool {
	return outcome.ManualInterventions >= 0 && outcome.ToolErrors >= 0 &&
		outcome.InputTokens >= 0 && outcome.OutputTokens >= 0 &&
		outcome.InputTokens <= math.MaxInt64-outcome.OutputTokens &&
		outcome.ToolCalls >= 0 && outcome.ElapsedMillis > 0 && outcome.CostMicrounits >= 0 &&
		outcome.AttemptCount == 1 && outcome.HiddenRetryCount == 0 &&
		outcome.FrameworkRetryCount == 0 && validDigest(outcome.ResultSHA256) &&
		validDigest(outcome.EvidenceSHA256)
}

func withinCostLimits(outcome Outcome, limits Limits) bool {
	return totalTokens(outcome) <= limits.MaxTotalTokens && outcome.ToolCalls <= limits.MaxToolCalls &&
		outcome.ElapsedMillis <= limits.MaxElapsedMillis && outcome.CostMicrounits <= limits.MaxCostMicrounits
}

func totalTokens(outcome Outcome) int64 {
	if outcome.InputTokens > math.MaxInt64-outcome.OutputTokens {
		return math.MaxInt64
	}
	return outcome.InputTokens + outcome.OutputTokens
}

func capabilityBundleDigest(binding Binding) string {
	return capabilityBundleDigestForTools(binding.ToolBundleID, binding.ToolBundleSHA256)
}

func capabilityBundleDigestForTools(toolBundleID, toolBundleSHA256 string) string {
	return hashFields(
		"chora.standard-capability-bundle.v1",
		CapabilityBundleID,
		StandardPolicyID,
		CandidateID,
		CandidateSkillSHA256,
		toolBundleID,
		toolBundleSHA256,
	)
}

func canonicalEvidenceDigest(evidence Evidence) string {
	encoded, err := json.Marshal(evidence)
	if err != nil {
		return digest(nil)
	}
	return digest(encoded)
}

func noSkill(evidenceDigest string, reasons ...string) Decision {
	return Decision{
		status:         StatusNoSkill,
		evidenceSHA256: evidenceDigest,
		reasons:        append([]string(nil), reasons...),
	}
}

func validDecision(decision Decision) bool {
	if !validDigest(decision.evidenceSHA256) {
		return false
	}
	switch decision.status {
	case StatusPass:
		projection := decision.projection
		return len(decision.reasons) == 0 && projection.capabilityBundleID == CapabilityBundleID &&
			projection.capabilityBundleSHA256 == capabilityBundleDigestForTools(projection.toolBundleID, projection.toolBundleSHA256) &&
			projection.skillID == CandidateID && projection.skillSHA256 == CandidateSkillSHA256 &&
			validID(projection.toolBundleID) && validDigest(projection.toolBundleSHA256)
	case StatusNoSkill:
		return len(decision.reasons) > 0 && decision.projection == (Projection{})
	default:
		return false
	}
}

func validID(value string) bool     { return idPattern.MatchString(value) }
func validDigest(value string) bool { return digestPattern.MatchString(value) }
func digest(value []byte) string    { sum := sha256.Sum256(value); return hex.EncodeToString(sum[:]) }

func hashFields(fields ...string) string {
	hash := sha256.New()
	for _, field := range fields {
		fmt.Fprintf(hash, "%d:%s", len(field), field)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func requireEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid JSON object termination")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid JSON array termination")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}
