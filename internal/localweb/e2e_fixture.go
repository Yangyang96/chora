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
	if input.PatchFixture != "" && input.PatchFixture != "g2-m3-authoritative" {
		return startRunInput{}, errors.New("patchFixture must be g2-m3-authoritative")
	}
	return input, nil
}

// NewE2E constructs the repository's build-tagged browser-test surface. It
// preserves a Pi-bound Charter/Snapshot while replacing only execution with the
// deterministic Fake used by the Playwright verification fixtures.
func NewE2E(ctx context.Context, databasePath, webRoot string, logger *log.Logger) (*Server, error) {
	server, err := New(withVerifierSandboxLifecycle(ctx, &e2eVerifierSandboxFactory{}), databasePath, webRoot, logger)
	if err != nil {
		return nil, err
	}
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
	current, err := server.registry.Get(agentpi.AdapterID)
	if err != nil {
		return nil, nil
	}
	if _, fixture := current.(*agentfake.Adapter); !fixture {
		return nil, nil
	}
	terminal, err := server.fakeAuthoritativeVerificationTerminal(criteria, runID)
	if err != nil {
		return nil, err
	}
	adapter := agentfake.NewAdapter(agentfake.Plan{AdapterID: agentpi.AdapterID, Executable: "/fake", Arguments: []string{"run", "--json"}, Environment: map[string]string{"CHORA_FAKE": "1"}, ResumeMode: execution.ResumeExplicitSession, TerminalResult: terminal})
	server.registry.Set(adapter)
	return adapter, nil
}

const (
	g2M3AuthoritativePatchSHA256 = "bedbeee71ba1bb7dc9f4d2aa86a90454f45620784d5f995ec89297948f2f39b6"
	g2M3AuthoritativePatchPath   = "normal-attempt1-v6/runtime/artifacts/attempts/3bc10e5e02d3087050f37ed936ca642da0639b4b39efdd6ce0a25f8ff43dce21/patch.diff"
)

func (server *Server) fakeAuthoritativeVerificationTerminal(criteria []domain.AcceptanceCriterion, runID domain.RunID) ([]byte, error) {
	if server.verifier == nil {
		return nil, errors.New("independent Verifier is unavailable")
	}
	g2M3Root := filepath.Dir(filepath.Dir(server.verifier.baselineRoot))
	patch, err := os.ReadFile(filepath.Join(g2M3Root, filepath.FromSlash(g2M3AuthoritativePatchPath)))
	if err != nil {
		return nil, fmt.Errorf("read authoritative G2-M3 Patch: %w", err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(patch))
	if digest != g2M3AuthoritativePatchSHA256 {
		return nil, errors.New("authoritative G2-M3 Patch digest changed")
	}
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
		SchemaVersion: "chora.agent-result.v1", Summary: "Build-tagged E2E Fake replayed the digest-pinned authoritative G2-M3 Pi/Docker Patch.", ReviewReady: true,
		Outputs:            []map[string]string{{"locator": locator, "description": "Authoritative G2-M3 Pi/Docker Patch replay", "sha256": digest, "media_type": "text/x-diff"}},
		ArtifactCandidates: []map[string]string{}, Checks: claims, Unknowns: []string{}, Handoff: map[string]any{"requested": false, "reason": ""},
	}
	return json.Marshal(document)
}
