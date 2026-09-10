package localweb

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/preflight"
)

func TestProjectReadinessReturnsClosedActionableComponents(t *testing.T) {
	checkedAt := time.Date(2026, 8, 17, 13, 0, 0, 0, time.UTC)
	ready := projectReadiness(readinessInputs{
		Report:    preflight.Report{Status: preflight.StatusPassed, ResourcesCreated: false, InputFingerprint: strings.Repeat("a", 64)},
		Pi:        piRuntimeStatus{Enabled: true, Reason: "pinned Pi image and Sandbox are configured", Provider: "docker-colima", Image: "sha256:image", DockerVersion: "29.6.1", ColimaVersion: "0.10.3"},
		Verifier:  verifierRuntimeStatus{Enabled: true, Reason: "pinned independent Verifier configured", BaselineDigest: strings.Repeat("b", 64), Image: "verifier@sha256:digest", Network: "none", Credentials: "zero"},
		CheckedAt: checkedAt,
	})
	wantKeys := []string{"source_baseline", "pi_image", "docker_engine", "colima", "oauth", "ca_proxy_model", "verifier", "task_worktrees", "owned_residue"}
	if !ready.CheckedAt.Equal(checkedAt) || ready.InputFingerprint != strings.Repeat("a", 64) || len(ready.Items) != len(wantKeys) {
		t.Fatalf("readiness = %#v", ready)
	}
	for index, item := range ready.Items {
		if item.Key != wantKeys[index] || item.State != readinessReady || item.Observed == "" || item.Required == "" || item.Action == "" {
			t.Fatalf("item[%d] = %#v", index, item)
		}
	}

	blocked := projectReadiness(readinessInputs{
		Report: preflight.Report{Status: preflight.StatusFailed, ResourcesCreated: false, InputFingerprint: strings.Repeat("c", 64), Failure: &preflight.Failure{
			Boundary: "oauth.provider", Observed: "token secret-value was absent", Required: "owner-only openai-codex OAuth entry", Action: "repair OAuth and Refresh",
		}},
		Pi:        piRuntimeStatus{Enabled: false, Reason: "dependency unavailable"},
		Verifier:  verifierRuntimeStatus{Enabled: true, Reason: "ready", BaselineDigest: strings.Repeat("b", 64)},
		CheckedAt: checkedAt,
	})
	oauth := readinessItemByKey(t, blocked, "oauth")
	if oauth.State != readinessBlocked || !strings.Contains(oauth.Action, "Refresh") || strings.Contains(oauth.Observed, "secret-value") {
		t.Fatalf("OAuth readiness leaked or lost action: %#v", oauth)
	}
	for _, item := range blocked.Items {
		if item.State != readinessReady && item.State != readinessBlocked && item.State != readinessChecking {
			t.Fatalf("open readiness state: %#v", item)
		}
	}

	baselineBlocked := projectReadiness(readinessInputs{
		Report: preflight.Report{Status: preflight.StatusPassed, ResourcesCreated: false, InputFingerprint: strings.Repeat("d", 64)},
		Pi:     piRuntimeStatus{Enabled: true, Reason: "ready"}, Verifier: verifierRuntimeStatus{Enabled: true, Reason: "ready"},
		BaselineErr: errors.New("aggregate mismatch"), CheckedAt: checkedAt,
	})
	if item := readinessItemByKey(t, baselineBlocked, "source_baseline"); item.State != readinessBlocked || !strings.Contains(item.Action, "installation") {
		t.Fatalf("baseline readiness = %#v", item)
	}

	applyBlocked := projectReadiness(readinessInputs{
		Report: preflight.Report{Status: preflight.StatusPassed, ResourcesCreated: false, InputFingerprint: strings.Repeat("f", 64)},
		Pi:     piRuntimeStatus{Enabled: true}, Verifier: verifierRuntimeStatus{Enabled: true}, TaskWorktreesRequired: true,
	})
	if item := readinessItemByKey(t, applyBlocked, "task_worktrees"); item.State != readinessBlocked || !strings.Contains(item.Action, "directly beside") || !strings.Contains(item.Action, "--repository") {
		t.Fatalf("apply target readiness = %#v", item)
	}
}

func TestReadinessBoundaryMappingIsClosedAndExact(t *testing.T) {
	cases := map[string]string{
		"baseline.identity":          "source_baseline",
		"host.platform":              "source_baseline",
		"tool.go":                    "source_baseline",
		"image.agent":                "pi_image",
		"image.boundary":             "pi_image",
		"tool.pi":                    "pi_image",
		"preflight.unconfigured":     "pi_image",
		"docker.version":             "docker_engine",
		"docker.context":             "docker_engine",
		"colima.version":             "colima",
		"colima.capacity":            "colima",
		"oauth.file":                 "oauth",
		"oauth.entry":                "oauth",
		"trust.ca":                   "ca_proxy_model",
		"proxy.reachability":         "ca_proxy_model",
		"model.destination":          "ca_proxy_model",
		"image.verifier":             "verifier",
		"verifier.policy":            "verifier",
		"residue.container":          "owned_residue",
		"residue.verifier_container": "owned_residue",
		"residue.workspace":          "owned_residue",
		"preflight.resource-order":   "owned_residue",
	}
	for boundary, want := range cases {
		if got := readinessKeyForBoundary(boundary); got != want {
			t.Errorf("readinessKeyForBoundary(%q) = %q, want %q", boundary, got, want)
		}
	}
}

func TestProjectReadinessRedactsCredentialLikeDiagnostics(t *testing.T) {
	view := projectReadiness(readinessInputs{
		Report: preflight.Report{Status: preflight.StatusFailed, InputFingerprint: strings.Repeat("E", 64), Failure: &preflight.Failure{
			Boundary: "oauth.entry",
			Observed: `Authorization: Bearer eyJhbGciOiJub25l.eyJzdWIiOiJvd25lciJ9. token secret-value`,
			Required: `access_token=sk-example123456789`,
			Action:   `Refresh credential "opaqueCredentialValue123456789" with pi.`,
		}},
		Pi: piRuntimeStatus{Enabled: true}, Verifier: verifierRuntimeStatus{Enabled: true},
	})
	oauth := readinessItemByKey(t, view, "oauth")
	joined := oauth.Observed + " " + oauth.Required + " " + oauth.Action
	for _, secret := range []string{"eyJhbGci", "secret-value", "sk-example", "opaqueCredentialValue"} {
		if strings.Contains(joined, secret) {
			t.Fatalf("OAuth readiness leaked %q: %#v", secret, oauth)
		}
	}
	if !strings.Contains(oauth.Action, "Refresh") || view.InputFingerprint != strings.Repeat("e", 64) {
		t.Fatalf("redacted readiness lost safe action or fingerprint: %#v", view)
	}
}

func TestProjectReadinessStartupDependenciesFailClosed(t *testing.T) {
	view := projectReadiness(readinessInputs{
		Report:   preflight.Report{Status: preflight.StatusPassed, InputFingerprint: strings.Repeat("a", 64)},
		Pi:       piRuntimeStatus{Enabled: false, Reason: "Bearer should-not-appear"},
		Verifier: verifierRuntimeStatus{Enabled: false, Reason: "token should-not-appear"},
	})
	if got := readinessItemByKey(t, view, "pi_image"); got.State != readinessBlocked || strings.Contains(got.Observed, "should-not-appear") {
		t.Fatalf("Pi readiness = %#v", got)
	}
	if got := readinessItemByKey(t, view, "verifier"); got.State != readinessBlocked || strings.Contains(got.Observed, "should-not-appear") {
		t.Fatalf("Verifier readiness = %#v", got)
	}
	if got := readinessItemByKey(t, view, "source_baseline"); got.State != readinessReady {
		t.Fatalf("Baseline readiness = %#v", got)
	}
}

func readinessItemByKey(t *testing.T, view readinessView, key string) readinessItem {
	t.Helper()
	for _, item := range view.Items {
		if item.Key == key {
			return item
		}
	}
	t.Fatalf("readiness item %q not found in %#v", key, view)
	return readinessItem{}
}
