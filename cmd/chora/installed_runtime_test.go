package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/productinstall"
	"github.com/Yangyang96/chora/internal/releaseassets"
)

func TestInstalledRuntimeRejectsGenerationTargetQualificationAndReleaseBeforeDocker(t *testing.T) {
	target := productinstall.EngineTarget{CLIPath: "/definitely/missing/private-docker", ContextName: "chora-local", EndpointDigest: strings.Repeat("e", 64)}
	root := filepath.Join(canonicalTempRoot(t), "state")
	active := installedRuntimeGeneration(t, target)
	backend, _ := productinstall.NewFileBackend(root)
	state := productinstall.EmptyState()
	state.Revision = 1
	state.ActiveGenerationID = "g1"
	state.Generations = []productinstall.Generation{active}
	if err := backend.SaveCAS(context.Background(), 0, state); err != nil {
		t.Fatal(err)
	}
	if err := backend.AtomicActivate(context.Background(), "", active.Spec); err != nil {
		t.Fatal(err)
	}
	base := installedRuntimeInput{
		StateRoot: root, GenerationID: "g1", Target: target,
		Release: releaseassets.ActivationInput{Root: "/definitely/missing/release", ManifestPath: "manifest.json", ManifestSHA256: strings.Repeat("c", 64), ReleaseID: "release-1"},
	}
	t.Run("wrong generation", func(t *testing.T) {
		candidate := base
		candidate.GenerationID = "g2"
		if _, err := loadInstalledRuntime(context.Background(), candidate); err == nil || !strings.Contains(err.Error(), "active generation") {
			t.Fatalf("wrong generation error = %v", err)
		}
	})
	t.Run("wrong target", func(t *testing.T) {
		candidate := base
		candidate.Target.ContextName = "other"
		if _, err := loadInstalledRuntime(context.Background(), candidate); err == nil || !strings.Contains(err.Error(), "generation target") {
			t.Fatalf("wrong target error = %v", err)
		}
	})
	t.Run("wrong qualification", func(t *testing.T) {
		loaded, _ := backend.Load(context.Background())
		loaded.Revision++
		loaded.Generations[0].Probe.EngineQualification = json.RawMessage(`{}`)
		if err := backend.SaveCAS(context.Background(), loaded.Revision-1, loaded); err != nil {
			t.Fatal(err)
		}
		if _, err := loadInstalledRuntime(context.Background(), base); err == nil || !strings.Contains(err.Error(), "qualification") {
			t.Fatalf("wrong qualification error = %v", err)
		}
	})
	t.Run("wrong release", func(t *testing.T) {
		loaded, _ := backend.Load(context.Background())
		loaded.Revision++
		loaded.Generations[0].Probe = active.Probe
		if err := backend.SaveCAS(context.Background(), loaded.Revision-1, loaded); err != nil {
			t.Fatal(err)
		}
		if _, err := loadInstalledRuntime(context.Background(), base); err == nil || !strings.Contains(err.Error(), "release evidence") {
			t.Fatalf("wrong release error = %v", err)
		}
	})
}

func installedRuntimeGeneration(t *testing.T, target productinstall.EngineTarget) productinstall.Generation {
	t.Helper()
	identity, err := dockersupervisor.NewEngineIdentity(dockersupervisor.EngineIdentityInput{
		DaemonID: "daemon-1", APIVersion: "1.47", OperatingSystem: "linux", Architecture: "arm64",
		ContextEndpointDigest: target.EndpointDigest, ProviderName: "Colima", EngineVersion: "29.6.1", ContextName: target.ContextName,
	})
	if err != nil {
		t.Fatal(err)
	}
	contract, err := dockersupervisor.NewCapabilityProbeContract("sha256:"+strings.Repeat("a", 64), strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	completed := time.Date(2026, 8, 27, 9, 30, 0, 0, time.UTC).Format(time.RFC3339Nano)
	record := dockersupervisor.EngineQualificationRecord{
		SchemaVersion: "chora.docker-engine-qualification/v1", EngineIdentityDigest: identity.Digest(), ProbeContractDigest: contract.Digest(),
		ProbeImageID: contract.ProbeImageID(), SandboxPolicyDigest: contract.SandboxPolicyDigest(), CompletedAt: completed,
	}
	payload, _ := json.Marshal(struct {
		SchemaVersion        string `json:"schema_version"`
		EngineIdentityDigest string `json:"engine_identity_digest"`
		ProbeContractDigest  string `json:"probe_contract_digest"`
		ProbeImageID         string `json:"probe_image_id"`
		SandboxPolicyDigest  string `json:"sandbox_policy_digest"`
		CompletedAt          string `json:"completed_at"`
	}{record.SchemaVersion, record.EngineIdentityDigest, record.ProbeContractDigest, record.ProbeImageID, record.SandboxPolicyDigest, record.CompletedAt})
	record.QualificationDigest = fmt.Sprintf("%x", sha256.Sum256(payload))
	qualification, err := dockersupervisor.RestoreEngineQualification(record)
	if err != nil {
		t.Fatal(err)
	}
	contractJSON, _ := json.Marshal(contract)
	qualificationJSON, _ := json.Marshal(qualification)
	asset := productinstall.AssetSpec{ID: "agent", Kind: "oci_image", Identity: "sha256:" + strings.Repeat("a", 64), Ownership: productinstall.OwnershipChora,
		AcquisitionKind: productinstall.AcquisitionLocalDockerArchive,
		ArchivePath:     "/private/release/agent.tar", ArchiveSize: 1, ArchiveSHA256: strings.Repeat("b", 64)}
	spec := productinstall.GenerationSpec{SchemaVersion: productinstall.GenerationSchema, GenerationID: "g1", ReleaseID: "release-1", ManifestSHA256: strings.Repeat("c", 64), Assets: []productinstall.AssetSpec{asset}}
	return productinstall.Generation{
		Spec: spec, Status: productinstall.GenerationActive, QualificationDigest: qualification.Digest(), Target: target,
		Probe: productinstall.ProbeResult{Passed: true, CleanupProven: true, QualificationDigest: qualification.Digest(),
			Engine:             productinstall.EngineObservation{DaemonID: identity.DaemonID(), APIVersion: identity.APIVersion(), OperatingSystem: identity.OperatingSystem(), Architecture: identity.Architecture(), ContextName: identity.ContextName(), EndpointDigest: identity.ContextEndpointDigest()},
			CapabilityContract: contractJSON, EngineQualification: qualificationJSON},
		Assets: []productinstall.InstalledAsset{{Spec: asset}}, ActivatedAt: time.Date(2026, 8, 27, 10, 0, 0, 0, time.UTC),
	}
}
