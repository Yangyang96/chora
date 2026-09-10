package localweb

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestO4EvidenceBoundaryIsAbsentUnlessExplicitlyConfigured(t *testing.T) {
	server := &Server{webRoot: t.TempDir()}
	request := newLocalRequest(http.MethodPost, "/api/o4/evidence-boundaries", strings.NewReader(`{}`))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusNotFound {
		t.Fatalf("absent evidence endpoint status = %d, want 404", response.Code)
	}
	if boundary, err := newO4EvidenceBoundary(nil, nil); err != nil || boundary != nil {
		t.Fatalf("nil O4 evidence options = %#v, %v", boundary, err)
	}
}

func TestO4RunnerAuthorityAndInstalledDoctorAreRawPinnedOwnerReadonly(t *testing.T) {
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	digest := func(value string) string {
		result := sha256.Sum256([]byte(value))
		return hex.EncodeToString(result[:])
	}
	identity := map[string]any{
		"environmentId": "env_" + strings.Repeat("a", 48), "installId": "ins_" + strings.Repeat("b", 24),
		"generationId": "gen_" + strings.Repeat("c", 24), "platform": map[string]any{"architecture": "arm64", "os": "darwin"},
	}
	authoritySafe := map[string]any{
		"schemaVersion": o4RunnerSessionSchema, "status": "exclusive", "manifestSha256": digest("manifest"),
		"runnerSha256": digest("runner"), "socketPathDigest": digest("socket"), "tupleIdentity": digest("tuple"),
		"identity": identity, "parentPid": 2, "sessionId": digest("session"), "sessionSecret": digest("secret"), "sequenceStart": 0,
	}
	authorityDigest, err := digestCanonicalJSON(authoritySafe)
	if err != nil {
		t.Fatal(err)
	}
	authority := cloneO4Map(authoritySafe)
	authority["authorityDigest"] = authorityDigest
	authorityPath, authorityRawSHA := writeO4TestJSON0400(t, root, "session.json", authority)
	loaded, gotRaw, err := loadO4RunnerAuthority(authorityPath, authorityRawSHA)
	if err != nil || gotRaw != authorityRawSHA || loaded.SessionSecret != digest("secret") {
		t.Fatalf("load authority = %#v, %q, %v", loaded, gotRaw, err)
	}

	roles := map[string]any{}
	for index, role := range []string{"managed_pi_runtime", "network_boundary", "independent_verifier", "capability_probe"} {
		suffix := string(rune('1' + index))
		roles[role] = map[string]any{"artifactId": "artifact-" + role, "archiveSha256": strings.Repeat(suffix, 64), "archiveSize": 1024 + index,
			"dockerConfigImageId": "sha256:" + strings.Repeat(suffix, 64), "policyDigest": strings.Repeat("a", 64)}
	}
	bindings := map[string]any{
		"candidate": map[string]any{"candidateManifestSha256": digest("candidate"), "sourceManifestSha256": digest("source"), "sourceAggregateSha256": digest("aggregate")},
		"product":   map[string]any{"binarySha256": digest("binary"), "webAggregateSha256": digest("web")},
		"release":   map[string]any{"releaseSpecSha256": digest("spec"), "releaseManifestSha256": digest("release"), "offlineBundleEvidenceSha256": digest("bundle")},
		"pi":        map[string]any{"selected": "private", "pathProvenanceSha256": digest("path"), "privateProvenanceSha256": digest("private")},
		"engine":    map[string]any{"qualificationSha256": digest("qualification"), "endpointEvidenceSha256": digest("endpoint")}, "roles": roles,
	}
	bindingDigest, _ := digestCanonicalJSON(bindings)
	reportSafe := map[string]any{
		"schemaVersion": "chora.m1-o4-installed-doctor-report.v2", "status": "passed",
		"environmentId": identity["environmentId"], "installId": identity["installId"], "generationId": identity["generationId"], "platform": identity["platform"],
		"bindings": bindings, "bindingDigest": bindingDigest, "setupReceiptSha256": digest("setup"),
		"modelRequestAuthoritySha256": digest("model-authority"), "engineQualificationSha256": digest("engine-proof"),
		"modelObservationSha256": digest("model-proof"), "inputFingerprint": digest("preflight"),
		"readOnly": true, "modelAuthenticated": true, "resourcesCreated": false, "mutationsAttempted": 0,
	}
	reportDigest, _ := digestCanonicalJSON(reportSafe)
	report := cloneO4Map(reportSafe)
	report["digest"] = reportDigest
	reportPath, reportRawSHA := writeO4TestJSON0400(t, root, "installed-doctor-report.json", report)
	loadedReport, err := loadO4InstalledDoctorReport(reportPath, reportRawSHA)
	if err != nil || loadedReport.BindingDigest != bindingDigest || loadedReport.InputFingerprint != digest("preflight") {
		t.Fatalf("load installed Doctor = %#v, %v", loadedReport, err)
	}
	if _, err := loadO4InstalledDoctorReport(reportPath, strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong raw installed Doctor SHA accepted")
	}
	hardlink := filepath.Join(root, "installed-doctor-hardlink.json")
	if err := os.Link(reportPath, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err := loadO4InstalledDoctorReport(reportPath, reportRawSHA); err == nil {
		t.Fatal("hard-linked installed Doctor report accepted")
	}
	if err := os.Remove(hardlink); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(reportPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadO4InstalledDoctorReport(reportPath, reportRawSHA); err == nil {
		t.Fatal("mutable installed Doctor report accepted")
	}
}

func TestO4HMACUsesSessionSecretTextAndProjectionOmitsDynamicViewFields(t *testing.T) {
	selectors := o4AuthenticatedSelectors{
		AttemptID: "attempt_01900000-0000-7000-8000-000000000001", ObservationDigest: strings.Repeat("a", 64),
		Phase: "C", RunID: "run_01900000-0000-7000-8000-000000000002", Sequence: 1,
		TaskID: "task_01900000-0000-7000-8000-000000000003",
	}
	canonical, err := json.Marshal(selectors)
	if err != nil {
		t.Fatal(err)
	}
	secret := strings.Repeat("f", 64)
	textMAC := hmac.New(sha256.New, []byte(secret))
	_, _ = textMAC.Write(canonical)
	hexKey, _ := hex.DecodeString(secret)
	decodedMAC := hmac.New(sha256.New, hexKey)
	_, _ = decodedMAC.Write(canonical)
	if hmac.Equal(textMAC.Sum(nil), decodedMAC.Sum(nil)) {
		t.Fatal("UTF-8 and decoded-hex HMAC keys unexpectedly alias")
	}
	if err := authenticateO4Selectors(secret, selectors, hex.EncodeToString(textMAC.Sum(nil))); err != nil {
		t.Fatalf("valid selector HMAC rejected: %v", err)
	}
	if err := authenticateO4Selectors(secret, selectors, hex.EncodeToString(decodedMAC.Sum(nil))); err == nil {
		t.Fatal("decoded-hex-key selector HMAC accepted")
	}
	if err := authenticateO4Selectors(secret, selectors, "not-a-mac"); err == nil {
		t.Fatal("malformed selector HMAC accepted")
	}
	baseInput := o4BoundaryInput{Phase: "C", Sequence: 1, ObservationDigest: strings.Repeat("a", 64), TupleIdentity: strings.Repeat("b", 64), SessionID: strings.Repeat("c", 64), GenerationID: "gen_test", MAC: strings.Repeat("d", 64)}
	if _, _, err := validateO4BoundaryInput(baseInput); err != nil {
		t.Fatalf("valid C selector envelope rejected: %v", err)
	}
	baseInput.Sequence = 6
	if _, _, err := validateO4BoundaryInput(baseInput); err == nil {
		t.Fatal("out-of-range C sequence accepted")
	}

	view := runView{ID: selectors.RunID, Status: domain.RunStateAwaitingReview, Version: 7,
		Task: taskView{ID: selectors.TaskID}, AttemptDetail: &attemptDetailView{ID: selectors.AttemptID, Sequence: 1, State: domain.AttemptStateOutputSubmitted,
			AgentExecution: &agentExecutionView{Profile: domain.AgentExecutionProfileStandard}}}
	first := projectO4ProductObservation("C", 1, view)
	firstDigest, _ := digestCanonicalJSON(first)
	view.Activity.ElapsedMS = 999999
	view.Controls.CanRetry = true
	secondDigest, _ := digestCanonicalJSON(projectO4ProductObservation("C", 1, view))
	if firstDigest != secondDigest {
		t.Fatal("dynamic public view fields changed the stable O4 projection")
	}
	view.Version++
	thirdDigest, _ := digestCanonicalJSON(projectO4ProductObservation("C", 1, view))
	if thirdDigest == firstDigest {
		t.Fatal("persisted Run version did not change the O4 projection")
	}
}

func writeO4TestJSON0400(t *testing.T, root, name string, value any) (string, string) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, name)
	if err := os.WriteFile(path, data, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return path, hex.EncodeToString(digest[:])
}

func cloneO4Map(source map[string]any) map[string]any {
	result := make(map[string]any, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}
