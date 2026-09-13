package localweb

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	agentfake "github.com/Yangyang96/chora/internal/agent/fake"
	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/preflight"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestTrustedLocalAcknowledgementAPIRequiresCurrentPolicyAndReplaysExactly(t *testing.T) {
	server := newReadinessTestServer(t)
	handler := server.Handler()
	var current currentTrustedLocalAcknowledgementView
	requestJSON(t, handler, http.MethodGet, "/api/agent-execution/trusted-local-acknowledgements/current", nil, http.StatusOK, &current)
	if current.Acknowledged || current.PolicyVersion != domain.TrustedLocalDisclosurePolicy || current.AcknowledgedAt != "" {
		t.Fatalf("missing current acknowledgement=%#v", current)
	}
	wrongPolicy, err := domain.NewTrustedLocalAcknowledgement(domain.TrustedLocalAcknowledgementRecord{
		PolicyVersion: "chora.trusted-local-disclosure.v2", ActorID: localActor, SessionID: "wrong-policy", AcknowledgedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.store.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
		return tx.InsertTrustedLocalAcknowledgement(context.Background(), wrongPolicy)
	}); err != nil {
		t.Fatal(err)
	}
	requestJSON(t, handler, http.MethodGet, "/api/agent-execution/trusted-local-acknowledgements/current", nil, http.StatusOK, &current)
	if current.Acknowledged {
		t.Fatalf("wrong-policy acknowledgement became current: %#v", current)
	}
	body := map[string]any{"policyVersion": domain.TrustedLocalDisclosurePolicy}
	requestJSON(t, handler, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements", body, http.StatusBadRequest, nil)
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements",
		map[string]any{"policyVersion": "chora.trusted-local-disclosure.v2"}, map[string]string{"Idempotency-Key": "ack-policy-bump"}, http.StatusUnprocessableEntity, nil)
	if _, err := server.store.Reader().GetTrustedLocalAcknowledgement(context.Background(), localActor, domain.TrustedLocalDisclosurePolicy); err != storecontract.ErrNotFound {
		t.Fatalf("rejected acknowledgement persisted: %v", err)
	}
	var first, replay trustedLocalAcknowledgementView
	headers := map[string]string{"Idempotency-Key": "ack-current-policy"}
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements", body, headers, http.StatusOK, &first)
	requestJSONWithHeaders(t, handler, http.MethodPost, "/api/agent-execution/trusted-local-acknowledgements", body, headers, http.StatusOK, &replay)
	if first.PolicyVersion != domain.TrustedLocalDisclosurePolicy || first.ActorID != localActor || first.SessionID != localSession || first.AcknowledgedAt == "" || first.Replayed ||
		!replay.Replayed || replay.PolicyVersion != first.PolicyVersion || replay.ActorID != first.ActorID || replay.SessionID != first.SessionID || replay.AcknowledgedAt != first.AcknowledgedAt {
		t.Fatalf("ack first=%#v replay=%#v", first, replay)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, newLocalRequest(http.MethodGet, "/api/agent-execution/trusted-local-acknowledgements/current", nil))
	if response.Code != http.StatusOK || strings.Contains(response.Body.String(), "actor") || strings.Contains(response.Body.String(), "session") {
		t.Fatalf("current acknowledgement leaked identity: code=%d body=%s", response.Code, response.Body.String())
	}
	if err := json.Unmarshal(response.Body.Bytes(), &current); err != nil || !current.Acknowledged || current.PolicyVersion != domain.TrustedLocalDisclosurePolicy || current.AcknowledgedAt != first.AcknowledgedAt {
		t.Fatalf("current acknowledgement=%#v err=%v", current, err)
	}
}

func TestAgentExecutionProjectionFreezesAllThreeProfilesAndTrustedLocalDisclosure(t *testing.T) {
	status := piRuntimeStatus{Provider: "docker-colima", Image: "sha256:managed", PolicyFingerprint: "sha256:sandbox"}
	for _, profile := range domain.SupportedAgentExecutionProfiles() {
		binding, err := domain.NewAgentExecutionProfileBinding(profile)
		if err != nil {
			t.Fatal(err)
		}
		view := agentExecutionViewOf(binding)
		if view == nil || view.Profile != profile || view.RuntimeSource != binding.RuntimeSource() || view.ExecutionProvider != binding.ExecutionProvider() || view.CapabilityPolicy != binding.CapabilityPolicy() || view.TrustDisclosurePolicy != binding.TrustDisclosurePolicy() {
			t.Fatalf("profile %q projection=%#v", profile, view)
		}
		sandbox := sandboxIdentityViewOf(binding, status)
		if profile == domain.AgentExecutionProfileTrustedLocal {
			if view.Sandboxed || view.DisclosureLabel != "Trusted Local · No Sandbox" || sandbox.Status != "not_applicable" || sandbox.Provider != domain.TrustedHostExecutionProvider || sandbox.Mode != "No Sandbox" || sandbox.Image != "not_applicable" || sandbox.PolicyFingerprint != "not_applicable" {
				t.Fatalf("Trusted Local disclosure=%#v sandbox=%#v", view, sandbox)
			}
			encoded, _ := json.Marshal(struct {
				AgentExecution *agentExecutionView `json:"agentExecution"`
				Sandbox        sandboxIdentityView `json:"sandbox"`
			}{view, sandbox})
			if strings.Contains(string(encoded), "sha256:managed") || strings.Contains(string(encoded), "sha256:sandbox") || strings.Contains(string(encoded), "docker-colima") {
				t.Fatalf("Trusted Local leaked managed claims: %s", encoded)
			}
		} else if !view.Sandboxed || sandbox.Status != "adopted" {
			t.Fatalf("managed profile %q disclosure=%#v sandbox=%#v", profile, view, sandbox)
		}
	}
}

func TestExactProfileReadinessDoesNotUseGlobalPiStatusOrFallback(t *testing.T) {
	server := &Server{registry: newAdapterRegistry(), supervisor: newRoutingSupervisor(map[string]execution.ProcessSupervisor{"fake": newFakeSupervisor()})}
	terminal, err := fakeTerminalResult(nil, false)
	if err != nil {
		t.Fatal(err)
	}
	server.registry.Set(agentfake.NewAdapter(agentfake.Plan{AdapterID: agentpi.AdapterID, Executable: "/fake", Arguments: []string{"run"}, TerminalResult: terminal}))
	trustedTarget, _ := execution.NewExecutionTarget(agentpi.AdapterID, domain.TrustedHostExecutionProvider)
	if err := server.supervisor.registerTarget(trustedTarget, newFakeSupervisor()); err != nil {
		t.Fatal(err)
	}
	server.piStatus = piRuntimeStatus{Enabled: false, Reason: "token sk-super-secret-value Docker unavailable"}
	server.product = true
	server.generationConfigErr = errors.New("managed generation unavailable")
	preflightCalls := 0
	server.piPreflight = func(context.Context) preflight.Report {
		preflightCalls++
		return preflight.Report{Status: preflight.StatusPassed}
	}
	trusted, _ := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileTrustedLocal)
	trustedRecorder := httptest.NewRecorder()
	if !server.requireAgentExecutionRoute(trustedRecorder, newLocalRequest(http.MethodPost, "/trusted", nil), trusted) || preflightCalls != 0 {
		t.Fatalf("Trusted Local was blocked by Docker/global Pi status: code=%d body=%s calls=%d", trustedRecorder.Code, trustedRecorder.Body.String(), preflightCalls)
	}
	server.product = false
	managed, _ := domain.NewAgentExecutionProfileBinding(domain.AgentExecutionProfileStandard)
	managedRecorder := httptest.NewRecorder()
	if server.requireAgentExecutionRoute(managedRecorder, newLocalRequest(http.MethodPost, "/managed", nil), managed) || managedRecorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("missing managed route did not fail closed: code=%d body=%s", managedRecorder.Code, managedRecorder.Body.String())
	}
	publicError := managedRecorder.Body.String()
	for _, forbidden := range []string{"sk-super-secret-value", "trusted_local", "Trusted Local", "fallback"} {
		if strings.Contains(publicError, forbidden) {
			t.Fatalf("managed route error leaked or suggested fallback: %s", publicError)
		}
	}
	if preflightCalls != 0 {
		t.Fatalf("missing exact managed route invoked Preflight: %d", preflightCalls)
	}
	dockerTarget, _ := execution.NewExecutionTarget(agentpi.AdapterID, domain.DockerExecutionProvider)
	if err := server.supervisor.registerTarget(dockerTarget, newFakeSupervisor()); err != nil {
		t.Fatal(err)
	}
	readyRecorder := httptest.NewRecorder()
	if !server.requireAgentExecutionRoute(readyRecorder, newLocalRequest(http.MethodPost, "/managed", nil), managed) || preflightCalls != 1 {
		t.Fatalf("exact managed route readiness failed: code=%d body=%s calls=%d", readyRecorder.Code, readyRecorder.Body.String(), preflightCalls)
	}
}
