package agent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

func TestDecodeResultPreservesV1TerminalProjection(t *testing.T) {
	data := readFixture(t, "result-success.json")
	result, err := DecodeResult(data)
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != execution.TerminalReviewReady || result.Summary == "" || len(result.Outputs) != 1 || len(result.ArtifactCandidates) != 1 || len(result.Checks) != 1 || len(result.Unknowns) != 1 || result.Handoff != nil {
		t.Fatalf("lossy result: %#v", result)
	}
	if result.Outputs[0].Locator != "reports/result.md" || result.Checks[0].CriterionID == "" {
		t.Fatalf("unexpected projection: %#v", result)
	}
}

func TestDecodeResultMapsNeedsRevision(t *testing.T) {
	result, err := DecodeResult(readFixture(t, "result-needs-revision.json"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Kind != execution.TerminalRevisionRequired {
		t.Fatalf("kind=%q", result.Kind)
	}
}

func TestDecodeResultPreservesDurableArtifactIdentity(t *testing.T) {
	document := `{"schema_version":"chora.agent-result.v1","summary":"done","review_ready":true,"outputs":[{"locator":"attempts/a/patch.diff","description":"patch","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","media_type":"text/x-diff"}],"artifact_candidates":[],"checks":[],"unknowns":[],"handoff":{"requested":false,"reason":""}}`
	result, err := DecodeResult([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Outputs) != 1 || result.Outputs[0].SHA256 != strings.Repeat("a", 64) || result.Outputs[0].MediaType != "text/x-diff" {
		t.Fatalf("artifact identity = %#v", result.Outputs)
	}
}

func TestDecodeResultPreservesStructuredContextConsumption(t *testing.T) {
	snapshotID := domain.NewContextSnapshotID().String()
	candidateID := domain.NewContextRevisionID().String()
	excludedID := domain.NewContextRevisionID().String()
	document := fmt.Sprintf(`{"schema_version":"chora.agent-result.v1","summary":"consumed","review_ready":true,"outputs":[],"artifact_candidates":[],"checks":[],"unknowns":[],"handoff":{"requested":false,"reason":""},"context_consumption":{"snapshot_id":%q,"snapshot_digest":"%064x","candidate_revision_id":%q,"included_revision_ids":[%q],"excluded_revision_ids":[%q],"derivation":"sha256:proof","excluded_content_observed":false}}`, snapshotID, 1, candidateID, candidateID, excludedID)
	result, err := DecodeResult([]byte(document))
	if err != nil {
		t.Fatal(err)
	}
	if result.ContextConsumption == nil || result.ContextConsumption.SnapshotID != snapshotID || result.ContextConsumption.CandidateRevisionID != candidateID || result.ContextConsumption.ExcludedRevisionIDs[0] != excludedID {
		t.Fatalf("context evidence = %#v", result.ContextConsumption)
	}
}

func TestDecodeResultRejectsContractViolations(t *testing.T) {
	valid := `{"schema_version":"chora.agent-result.v1","summary":"done","review_ready":true,"outputs":[],"artifact_candidates":[],"checks":[{"criterion_id":"criterion_1","status":"PASS","evidence":"ok"}],"unknowns":[],"handoff":{"requested":false,"reason":""}}`
	cases := map[string]string{
		"unknown version":             strings.Replace(valid, "chora.agent-result.v1", "chora.agent-result.v2", 1),
		"unknown field":               strings.Replace(valid, `"summary":"done"`, `"summary":"done","extra":1`, 1),
		"duplicate criterion":         strings.Replace(valid, `],"unknowns"`, `,{"criterion_id":"criterion_1","status":"FAIL","evidence":"bad"}],"unknowns"`, 1),
		"absolute posix":              strings.Replace(valid, `"outputs":[]`, `"outputs":[{"locator":"/tmp/out","description":"bad"}]`, 1),
		"absolute windows":            strings.Replace(valid, `"outputs":[]`, `"outputs":[{"locator":"C:\\\\tmp\\\\out","description":"bad"}]`, 1),
		"uri":                         strings.Replace(valid, `"outputs":[]`, `"outputs":[{"locator":"file://out","description":"bad"}]`, 1),
		"trailing document":           valid + `{}`,
		"handoff review ready":        strings.Replace(valid, `"handoff":{"requested":false,"reason":""}`, `"handoff":{"requested":true,"reason":"ask"}`, 1),
		"empty summary":               strings.Replace(valid, `"summary":"done"`, `"summary":""`, 1),
		"missing outputs":             strings.Replace(valid, `"outputs":[],`, "", 1),
		"missing handoff":             strings.Replace(valid, `,"handoff":{"requested":false,"reason":""}`, "", 1),
		"null checks":                 strings.Replace(valid, `"checks":[{"criterion_id":"criterion_1","status":"PASS","evidence":"ok"}]`, `"checks":null`, 1),
		"missing check evidence":      strings.Replace(valid, `,"evidence":"ok"`, "", 1),
		"missing handoff requested":   strings.Replace(valid, `"requested":false,`, "", 1),
		"duplicate output locator":    strings.Replace(valid, `"outputs":[]`, `"outputs":[{"locator":"same.txt","description":"one"},{"locator":"same.txt","description":"two"}]`, 1),
		"duplicate candidate locator": strings.Replace(valid, `"artifact_candidates":[]`, `"artifact_candidates":[{"locator":"same.txt","description":"one"},{"locator":"same.txt","description":"two"}]`, 1),
		"cross collection locator":    strings.Replace(valid, `"outputs":[],"artifact_candidates":[]`, `"outputs":[{"locator":"same.txt","description":"one"}],"artifact_candidates":[{"locator":"same.txt","description":"two"}]`, 1),
		"invalid artifact digest":     strings.Replace(valid, `"outputs":[]`, `"outputs":[{"locator":"patch.diff","description":"patch","sha256":"bad","media_type":"text/x-diff"}]`, 1),
		"partial artifact identity":   strings.Replace(valid, `"outputs":[]`, `"outputs":[{"locator":"patch.diff","description":"patch","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}]`, 1),
	}
	for name, data := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeResult([]byte(data)); err == nil {
				t.Fatal("expected strict contract error")
			}
		})
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "agent-result", "v1", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}
