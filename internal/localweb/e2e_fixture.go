//go:build chora_e2e

package localweb

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	agentfake "github.com/Yangyang96/chora/internal/agent/fake"
	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/gitsource"
	"github.com/Yangyang96/chora/internal/pidiscovery"
	"github.com/Yangyang96/chora/internal/preflight"
	"github.com/Yangyang96/chora/internal/verifier"
)

type startRunInput struct {
	RevisionID   string `json:"revisionId"`
	PatchFixture string `json:"patchFixture"`
}

func decodeStartRunInput(request *http.Request) (startRunInput, error) {
	var input startRunInput
	if err := decodeOptionalJSON(request, &input); err != nil {
		return startRunInput{}, err
	}
	if input.PatchFixture != "" && input.PatchFixture != "g2-m3-authoritative" && input.PatchFixture != "project-document" {
		return startRunInput{}, errors.New("patchFixture must be g2-m3-authoritative or project-document")
	}
	return input, nil
}

// NewE2E constructs the repository's build-tagged browser-test surface. It
// preserves a Pi-bound Charter/Snapshot while replacing only execution with the
// deterministic Fake used by the Playwright verification fixtures.
func NewE2E(ctx context.Context, databasePath, webRoot string, logger *log.Logger) (*Server, error) {
	return newE2E(ctx, databasePath, webRoot, logger, false)
}

func NewE2EProjectDocument(ctx context.Context, databasePath, webRoot string, logger *log.Logger) (*Server, error) {
	return newE2E(ctx, databasePath, webRoot, logger, true)
}

func newE2E(ctx context.Context, databasePath, webRoot string, logger *log.Logger, documentScenario bool) (*Server, error) {
	// Project/Room board fixtures need the same durable data-root identity as
	// task-first Projects, while retaining the diagnostic execution surface.
	fixtureRoot := filepath.Join(filepath.Dir(databasePath), "fake-pi")
	fixtureHome := filepath.Join(fixtureRoot, "home")
	fixtureExecutable := filepath.Join(fixtureRoot, "pi")
	fixtureSessions := filepath.Join(filepath.Dir(databasePath), "pi-sessions")
	if documentScenario {
		if err := os.MkdirAll(fixtureHome, 0o700); err != nil {
			return nil, err
		}
		if err := os.MkdirAll(fixtureSessions, 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(fixtureHome, "catalog"), []byte("provider model context max-out thinking images\nopenai fixture 128K 16K yes no\n"), 0o600); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(fixtureHome, "settings.json"), []byte(`{"defaultProvider":"openai","defaultModel":"fixture"}`), 0o600); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(fixtureHome, "models-store.json"), []byte(`{}`), 0o600); err != nil {
			return nil, err
		}
		if err := os.WriteFile(fixtureExecutable, []byte("#!/bin/sh\ncase \"$1\" in\n--version) echo 0.85.1;;\nauth) echo ready;;\n--list-models) cat \"$PI_CODING_AGENT_DIR/catalog\";;\n*) exit 1;;\nesac\n"), 0o700); err != nil {
			return nil, err
		}
		discovery, err := pidiscovery.Discover(ctx, pidiscovery.Options{PiHome: fixtureHome, LookPath: func(string) (string, error) { return fixtureExecutable, nil }})
		if err != nil || discovery.State != pidiscovery.StateReady {
			return nil, fmt.Errorf("configure build-tagged E2E Pi discovery: state=%s reason=%s err=%v", discovery.State, discovery.Reason, err)
		}
		options := ProductOptions{
			DataRoot: filepath.Dir(databasePath), PathPiEnabled: true,
			PathPiSessionRoot: fixtureSessions, PathPiHome: fixtureHome,
			PathPiLookPath:   func(string) (string, error) { return fixtureExecutable, nil },
			RepositorySource: gitsource.Default{},
		}
		server, err := newServer(withVerifierSandboxLifecycle(ctx, &e2eVerifierSandboxFactory{}), databasePath, webRoot, logger, options, false)
		if err != nil {
			return nil, err
		}
		if !server.piStatus.Enabled {
			server.Close()
			return nil, fmt.Errorf("configure build-tagged E2E Pi: %s", server.piStatus.Reason)
		}
		return finishE2E(server, webRoot, fixtureSessions, fixtureExecutable, discovery)
	}
	server, err := newServer(withVerifierSandboxLifecycle(ctx, &e2eVerifierSandboxFactory{}), databasePath, webRoot, logger, ProductOptions{DataRoot: filepath.Dir(databasePath)}, false)
	if err != nil {
		return nil, err
	}
	return finishE2E(server, webRoot, "", "", pidiscovery.Result{})
}

func finishE2E(server *Server, webRoot, fixtureSessions, fixtureExecutable string, discovery pidiscovery.Result) (*Server, error) {
	if err := prepareE2EBaseline(server); err != nil {
		server.Close()
		return nil, err
	}
	if fixtureExecutable == "" {
		repoRoot := filepath.Clean(filepath.Join(webRoot, "..", ".."))
		runtimeConfig, err := os.ReadFile(filepath.Join(repoRoot, "contracts", "g2-m1a", "pi-runtime-config.v6.json"))
		if err != nil {
			server.Close()
			return nil, err
		}
		policy, err := os.ReadFile(filepath.Join(repoRoot, "contracts", "g2-m1a", "sandbox-policy.v4.json"))
		if err != nil {
			server.Close()
			return nil, err
		}
		adapter, err := agentpi.New(agentpi.Config{RuntimeConfig: runtimeConfig, Policy: policy, AttemptImageID: agentpi.AttemptImageID, BoundaryImageID: agentpi.BoundaryImageID})
		if err != nil {
			server.Close()
			return nil, err
		}
		server.registry.Set(adapter)
		runtime := newFakeSupervisor()
		runtime.identityNamespace = "e2e-pi-"
		target, err := execution.NewExecutionTarget(agentpi.AdapterID, domain.DockerExecutionProvider)
		if err != nil {
			server.Close()
			return nil, err
		}
		if err := server.supervisor.registerTarget(target, runtime); err != nil {
			server.Close()
			return nil, err
		}
		server.supervisor.mu.Lock()
		server.supervisor.byAdapter[agentpi.AdapterID] = runtime
		server.supervisor.mu.Unlock()
		server.piStatus = piRuntimeStatus{Enabled: true, Reason: "build-tagged E2E Pi identity verified", HostReadIsolation: "denied", Provider: "docker-colima", Image: agentpi.AttemptImageID, PolicyFingerprint: agentpi.PolicySHA256, DockerVersion: dockersupervisor.DockerEngineVersion, ColimaVersion: dockersupervisor.RequiredColimaVersion}
		server.piPreflight = func(context.Context) preflight.Report {
			return preflight.Report{Status: preflight.StatusPassed, ResourcesCreated: false, InputFingerprint: strings.Repeat("a", 64)}
		}
		return server, nil
	}
	pathSource, err := agentpi.NewPathPiSource(agentpi.PathPiSourceParams{
		ExecutablePath: fixtureExecutable, Version: discovery.Version, ExecutableSHA256: discovery.ExecutableSHA256,
	})
	if err != nil {
		server.Close()
		return nil, err
	}
	adapter, err := agentpi.New(agentpi.Config{PathPiSource: pathSource, SessionRoot: fixtureSessions})
	if err != nil {
		server.Close()
		return nil, err
	}
	server.registry.Set(adapter)
	// The build-tagged browser fixture exercises the production task-resource
	// route, including read-only inspection of test-owned temporary Git
	// repositories, while retaining deterministic execution below. It uses no
	// host Pi, Docker, or model credentials.
	runtime := newFakeSupervisor()
	// Diagnostic Fake runs may remain live while this distinct Pi target starts.
	runtime.identityNamespace = "e2e-pi-"
	target, err := execution.NewExecutionTarget(agentpi.AdapterID, domain.TrustedHostExecutionProvider)
	if err != nil {
		server.Close()
		return nil, err
	}
	if err := server.supervisor.registerTarget(target, runtime); err != nil {
		server.Close()
		return nil, err
	}
	server.supervisor.mu.Lock()
	server.supervisor.byAdapter[agentpi.AdapterID] = runtime
	server.supervisor.mu.Unlock()
	server.piStatus = piRuntimeStatus{Enabled: true, Reason: "build-tagged E2E Pi identity verified", HostReadIsolation: "denied", Provider: "docker-colima", Image: agentpi.AttemptImageID, PolicyFingerprint: agentpi.PolicySHA256, DockerVersion: dockersupervisor.DockerEngineVersion, ColimaVersion: dockersupervisor.RequiredColimaVersion}
	server.verifierStatus = verifierRuntimeStatus{Enabled: true, Reason: "build-tagged E2E verifier ready", Mode: "strict", Network: "none", Credentials: "zero"}
	server.piPreflight = func(context.Context) preflight.Report {
		return preflight.Report{Status: preflight.StatusPassed, ResourcesCreated: false, InputFingerprint: strings.Repeat("a", 64)}
	}
	return server, nil
}

type e2eVerifierSandboxFactory struct{}

func (*e2eVerifierSandboxFactory) Create(_ context.Context, spec verifier.SandboxSpec) (verifier.Sandbox, error) {
	return &e2eVerifierSandbox{identity: spec.Identity.VerifierImage}, nil
}

func (*e2eVerifierSandboxFactory) Recover(context.Context) error { return nil }

type e2eVerifierSandbox struct {
	identity string
}

func (sandbox *e2eVerifierSandbox) Identity() string { return sandbox.identity }

func (*e2eVerifierSandbox) Execute(ctx context.Context, _ verifier.CommandRequest, _, _ io.Writer) verifier.CommandOutcome {
	if err := ctx.Err(); err != nil {
		return verifier.CommandOutcome{Kind: verifier.CommandCancelled, Reason: err.Error()}
	}
	exitCode := 0
	return verifier.CommandOutcome{Kind: verifier.CommandExited, ExitCode: &exitCode}
}

func (*e2eVerifierSandbox) Terminate(context.Context) error { return nil }
func (*e2eVerifierSandbox) Cleanup(context.Context) error   { return nil }

func (server *Server) e2eStartRunAdapter(input startRunInput, criteria []domain.AcceptanceCriterion, runID domain.RunID, adapterID string, snapshotEvidence bool) (*agentfake.Adapter, error) {
	if input.PatchFixture == "" {
		return nil, nil
	}
	if input.PatchFixture == "project-document" {
		server.supervisor.mu.RLock()
		runtime, ok := server.supervisor.byAdapter[adapterID].(*fakeSupervisor)
		server.supervisor.mu.RUnlock()
		if !ok {
			return nil, errors.New("project document fixture runtime is unavailable")
		}
		proposal := "# Compatibility proposal\n\nPreserve the public API contract described by the supplied migration notes.\n"
		message, err := json.Marshal(map[string]any{
			"type": "message_end",
			"message": map[string]any{
				"role": "assistant", "stopReason": "stop",
				"content": []map[string]string{{"type": "text", "text": proposal}},
			},
		})
		if err != nil {
			return nil, err
		}
		runtime.mu.Lock()
		runtime.streams[execution.StreamStdout] = append(append(message, '\n'), []byte("{\"type\":\"agent_settled\"}\n")...)
		runtime.streams[execution.StreamStderr] = nil
		runtime.mu.Unlock()
		terminal, err := fakeTerminalResult(criteria, true)
		if err != nil {
			return nil, err
		}
		adapter := agentfake.NewAdapter(agentfake.Plan{AdapterID: adapterID, Executable: "/fake", Arguments: []string{"run", "--json"}, Environment: map[string]string{"CHORA_FAKE": "1"}, ResumeMode: execution.ResumeExplicitSession, TerminalResult: terminal})
		return adapter, nil
	}
	terminal, err := server.fakeAuthoritativeVerificationTerminal(criteria, runID)
	if err != nil {
		return nil, err
	}
	adapter := agentfake.NewAdapter(agentfake.Plan{
		AdapterID: adapterID, Executable: "/fake", Arguments: []string{"run", "--json"},
		Environment: map[string]string{"CHORA_FAKE": "1"}, ResumeMode: execution.ResumeExplicitSession,
		TerminalResult: terminal, SnapshotEvidence: snapshotEvidence,
	})
	server.registry.Set(adapter)
	return adapter, nil
}

func (server *Server) e2eVerifiedRetryAdapter(criteria []domain.AcceptanceCriterion, runID domain.RunID) (*agentfake.Adapter, error) {
	if server.piStatus.Reason != "build-tagged E2E Pi identity verified" {
		return nil, nil
	}
	server.supervisor.mu.RLock()
	_, fakeRuntime := server.supervisor.byAdapter[agentpi.AdapterID].(*fakeSupervisor)
	server.supervisor.mu.RUnlock()
	if !fakeRuntime {
		return nil, nil
	}
	current, err := server.registry.Get(agentpi.AdapterID)
	if err != nil {
		return nil, nil
	}
	if _, documentFixture := current.(*agentpi.Adapter); documentFixture {
		terminal, terminalErr := fakeTerminalResult(criteria, true)
		if terminalErr != nil {
			return nil, terminalErr
		}
		return agentfake.NewAdapter(agentfake.Plan{AdapterID: agentpi.AdapterID, Executable: "/fake", Arguments: []string{"run", "--json"}, Environment: map[string]string{"CHORA_FAKE": "1"}, ResumeMode: execution.ResumeExplicitSession, TerminalResult: terminal}), nil
	}
	terminal, err := server.fakeAuthoritativeVerificationTerminal(criteria, runID)
	if err != nil {
		return nil, err
	}
	adapter := agentfake.NewAdapter(agentfake.Plan{AdapterID: agentpi.AdapterID, Executable: "/fake", Arguments: []string{"run", "--json"}, Environment: map[string]string{"CHORA_FAKE": "1"}, ResumeMode: execution.ResumeExplicitSession, TerminalResult: terminal})
	server.registry.Set(adapter)
	return adapter, nil
}

func (server *Server) fakeAuthoritativeVerificationTerminal(criteria []domain.AcceptanceCriterion, runID domain.RunID) ([]byte, error) {
	if server.verifier == nil {
		return nil, errors.New("independent Verifier is unavailable")
	}
	patch := []byte(e2eVerificationPatch)
	digest := fmt.Sprintf("%x", sha256.Sum256(patch))
	locator := filepath.ToSlash(filepath.Join("g2-m3-authoritative-replay", runID.String(), "patch.diff"))
	path := filepath.Join(server.runtimeRoot, "artifacts", filepath.FromSlash(locator))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if err := os.WriteFile(path, patch, 0o600); err != nil {
		return nil, err
	}
	type check struct {
		CriterionID string `json:"criterion_id"`
		Status      string `json:"status"`
		Evidence    string `json:"evidence"`
	}
	claims := make([]check, 0, len(criteria))
	for _, criterion := range criteria {
		claims = append(claims, check{CriterionID: criterion.ID().String(), Status: string(execution.CheckPass), Evidence: "E2E Fake replay claim; independent verification is authoritative."})
	}
	document := struct {
		SchemaVersion      string              `json:"schema_version"`
		Summary            string              `json:"summary"`
		ReviewReady        bool                `json:"review_ready"`
		Outputs            []map[string]string `json:"outputs"`
		ArtifactCandidates []map[string]string `json:"artifact_candidates"`
		Checks             []check             `json:"checks"`
		Unknowns           []string            `json:"unknowns"`
		Handoff            map[string]any      `json:"handoff"`
	}{
		SchemaVersion: "chora.agent-result.v1", Summary: "Build-tagged E2E Fake supplied a synthetic public patch for workflow verification.", ReviewReady: true,
		Outputs:            []map[string]string{{"locator": locator, "description": "Synthetic public E2E patch", "sha256": digest, "media_type": "text/x-diff"}},
		ArtifactCandidates: []map[string]string{}, Checks: claims, Unknowns: []string{}, Handoff: map[string]any{"requested": false, "reason": ""},
	}
	return json.Marshal(document)
}
