package releaseassets

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGenerateParseAndValidateDeterministicManifest(t *testing.T) {
	root, spec, resolution := fixture(t)
	first, err := Generate(root, spec, resolution)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Generate(root, spec, resolution)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := EncodeManifest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := EncodeManifest(second)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstJSON, secondJSON) {
		t.Fatal("manifest generation is not deterministic")
	}
	parsed, err := ParseManifest(bytes.NewReader(firstJSON))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateManifestFiles(root, parsed); err != nil {
		t.Fatal(err)
	}
	if parsed.Artifacts[0].Image.LocalDockerConfigImageID != resolution.Artifacts[0].LocalDockerConfigImageID {
		t.Fatal("local Docker config image ID changed")
	}
	if parsed.Artifacts[0].Image.Archive != resolution.Artifacts[0].Archive {
		t.Fatal("offline archive binding changed")
	}
}

func TestStandaloneReleaseRootMaterializesPortableActivationClosure(t *testing.T) {
	sourceRoot, spec, resolution := fixture(t)
	sourceRoot = canonicalTestRoot(t, sourceRoot)
	releaseRoot := filepath.Join(canonicalTestRoot(t, t.TempDir()), "release")
	if err := os.Mkdir(releaseRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	copyResolutionArchives(t, sourceRoot, releaseRoot, resolution)

	manifest, err := GenerateFromRoots(sourceRoot, releaseRoot, spec, resolution)
	if err != nil {
		t.Fatal(err)
	}
	if err := MaterializeManifestSourceClosure(sourceRoot, releaseRoot, manifest); err != nil {
		t.Fatal(err)
	}
	if err := ValidateManifestFiles(releaseRoot, manifest); err != nil {
		t.Fatal(err)
	}
	encoded, err := EncodeManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	manifestPath := "release-manifest.v1.json"
	if err := os.WriteFile(filepath.Join(releaseRoot, manifestPath), encoded, 0o444); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(encoded)
	activated, err := LoadActivatedRelease(ActivationInput{
		Root: releaseRoot, ManifestPath: manifestPath,
		ManifestSHA256: hex.EncodeToString(digest[:]), ReleaseID: spec.ReleaseID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := activated.ReadPolicyForRole(releaseRoot, RoleManagedPiRuntime); err != nil {
		t.Fatal(err)
	}
}

func TestStandaloneReleaseRootRejectsRootAndCopySplicesBeforePartialClosure(t *testing.T) {
	sourceRoot, spec, resolution := fixture(t)
	sourceRoot = canonicalTestRoot(t, sourceRoot)
	releaseRoot := filepath.Join(canonicalTestRoot(t, t.TempDir()), "release")
	if err := os.Mkdir(releaseRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	copyResolutionArchives(t, sourceRoot, releaseRoot, resolution)
	manifest, err := GenerateFromRoots(sourceRoot, releaseRoot, spec, resolution)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := GenerateFromRoots(sourceRoot, sourceRoot, spec, resolution); err == nil || !strings.Contains(err.Error(), "distinct") {
		t.Fatalf("same source/release root error = %v", err)
	}
	contained := filepath.Join(sourceRoot, "nested-release")
	if err := os.Mkdir(contained, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := GenerateFromRoots(sourceRoot, contained, spec, resolution); err == nil || !strings.Contains(err.Error(), "contain") {
		t.Fatalf("contained release root error = %v", err)
	}

	existing := manifest.Roles[0].Policy.Path
	existingPath := filepath.Join(releaseRoot, filepath.FromSlash(existing))
	if err := os.MkdirAll(filepath.Dir(existingPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existingPath, []byte("splice\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := MaterializeManifestSourceClosure(sourceRoot, releaseRoot, manifest); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("preexisting closure target error = %v", err)
	}
	if _, err := os.Lstat(filepath.Join(releaseRoot, "distribution", "managed", "Dockerfile")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("copy preflight left a partial closure: %v", err)
	}
}

func TestStrictParsersRejectUnknownAndTrailingValues(t *testing.T) {
	root, spec, _ := fixture(t)
	encoded, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	unknown := bytes.Replace(encoded, []byte(`"release_id"`), []byte(`"unknown":true,"release_id"`), 1)
	if _, err := ParseSpec(bytes.NewReader(unknown)); err == nil {
		t.Fatal("unknown field passed")
	}
	if _, err := ParseSpec(bytes.NewReader(append(encoded, []byte(`{}`)...))); err == nil {
		t.Fatal("trailing JSON value passed")
	}
	duplicate := bytes.Replace(encoded, []byte(`"release_id":"chora-m1-alpha"`), []byte(`"release_id":"first","release_id":"chora-m1-alpha"`), 1)
	if _, err := ParseSpec(bytes.NewReader(duplicate)); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("duplicate field error = %v", err)
	}
	if err := ValidateSpecFiles(root, spec); err != nil {
		t.Fatal(err)
	}
}

func TestManifestRequiresLinuxARM64(t *testing.T) {
	_, spec, _ := fixture(t)
	spec.Platform.Architecture = "amd64"
	if err := ValidateSpec(spec); err == nil || !strings.Contains(err.Error(), "linux/arm64") {
		t.Fatalf("amd64 error = %v", err)
	}
}

func TestSharedArtifactRequiresExplicitPrimaryAlias(t *testing.T) {
	_, spec, _ := fixture(t)
	spec.Roles[2].AliasOfRole = ""
	if err := ValidateSpec(spec); err == nil || !strings.Contains(err.Error(), "multiple primary") {
		t.Fatalf("implicit sharing error = %v", err)
	}
	spec.Roles[2].AliasOfRole = RoleNetworkBoundary
	if err := ValidateSpec(spec); err == nil || !strings.Contains(err.Error(), "explicitly alias") {
		t.Fatalf("wrong alias error = %v", err)
	}
}

func TestResolutionRejectsLegacyRegistryShape(t *testing.T) {
	legacy := `{"schema_version":"chora.release-assets-resolution.v1","artifacts":[{"artifact_id":"managed","oci_pull_reference":"registry.example/managed@sha256:` + strings.Repeat("1", 64) + `","local_docker_config_image_id":"sha256:` + strings.Repeat("2", 64) + `","registry_verification":{"method":"authenticated_registry_manifest_inspection","evidence":{"path":"evidence.json","sha256":"` + strings.Repeat("3", 64) + `"}}}]}`
	if _, err := ParseResolution(strings.NewReader(legacy)); err == nil {
		t.Fatal("legacy Registry resolution passed the offline parser")
	}
}

func TestGenerationFailsClosedWithoutCompleteOfflineArchives(t *testing.T) {
	root, spec, resolution := fixture(t)
	resolution.Artifacts[0].Archive.Format = "oci-layout"
	if _, err := Generate(root, spec, resolution); err == nil || !strings.Contains(err.Error(), "resolved artifact") {
		t.Fatalf("wrong archive format error = %v", err)
	}
	_, _, missing := fixture(t)
	missing.Artifacts = missing.Artifacts[:1]
	if _, err := Generate(root, spec, missing); err == nil || !strings.Contains(err.Error(), "every artifact") {
		t.Fatalf("missing digest error = %v", err)
	}
}

func TestFileAndContextDriftFailValidation(t *testing.T) {
	root, spec, _ := fixture(t)
	path := filepath.Join(root, filepath.FromSlash(spec.Artifacts[0].Recipe.Path))
	if err := os.WriteFile(path, []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSpecFiles(root, spec); err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("recipe drift error = %v", err)
	}
}

func TestRuntimeRequiresExactPiIntegrityAndNodeIdentity(t *testing.T) {
	_, spec, _ := fixture(t)
	spec.Artifacts[0].Runtime.Pi.NPMIntegrity = "sha512-not-base64"
	if err := ValidateSpec(spec); err == nil || !strings.Contains(err.Error(), "Pi/npm") {
		t.Fatalf("bad npm integrity error = %v", err)
	}
	_, spec, _ = fixture(t)
	spec.Artifacts[0].Runtime.Node.VersionOutput = "v22"
	if err := ValidateSpec(spec); err == nil || !strings.Contains(err.Error(), "Node identity") {
		t.Fatalf("bad Node identity error = %v", err)
	}
}

func TestBuildInputsAreRequiredAndExactlyBoundToRecipe(t *testing.T) {
	root, spec, _ := fixture(t)
	spec.Artifacts[0].BuildInputs = nil
	if err := ValidateSpec(spec); err == nil || !strings.Contains(err.Error(), "artifact") {
		t.Fatalf("missing build input error = %v", err)
	}

	root, spec, _ = fixture(t)
	spec.Artifacts[0].BuildInputs[0].OCIPullReference = "registry.example/base/managed@sha256:" + strings.Repeat("b", 64)
	if err := ValidateSpecFiles(root, spec); err == nil || !strings.Contains(err.Error(), "not pinned") {
		t.Fatalf("mutated build input error = %v", err)
	}
}

func TestNodeIdentityMustMatchRecipeAssertion(t *testing.T) {
	root, spec, _ := fixture(t)
	spec.Artifacts[0].Runtime.Node.Version = "22.20.0"
	spec.Artifacts[0].Runtime.Node.VersionOutput = "v22.20.0"
	if err := ValidateSpecFiles(root, spec); err == nil || !strings.Contains(err.Error(), "Node version identity") {
		t.Fatalf("Node mismatch error = %v", err)
	}
}

func TestDockerArchiveBindsLocalConfigImageID(t *testing.T) {
	root, spec, resolution := fixture(t)
	resolution.Artifacts[0].LocalDockerConfigImageID = "sha256:" + strings.Repeat("9", 64)
	if _, err := Generate(root, spec, resolution); err == nil || !strings.Contains(err.Error(), "config identity") {
		t.Fatalf("config ID mismatch error = %v", err)
	}
}

func TestBoundPathsRejectAncestorAndRootSymlinks(t *testing.T) {
	root, spec, resolution := fixture(t)
	realEvidence := filepath.Join(root, "archives-real")
	if err := os.Rename(filepath.Join(root, "archives"), realEvidence); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realEvidence, filepath.Join(root, "archives")); err != nil {
		t.Fatal(err)
	}
	if _, err := Generate(root, spec, resolution); err == nil || !strings.Contains(err.Error(), "symlink component") {
		t.Fatalf("evidence ancestor symlink error = %v", err)
	}

	realRoot, spec, _ := fixture(t)
	linkParent := t.TempDir()
	linkedRoot := filepath.Join(linkParent, "root-link")
	if err := os.Symlink(realRoot, linkedRoot); err != nil {
		t.Fatal(err)
	}
	if err := ValidateSpecFiles(linkedRoot, spec); err == nil || !strings.Contains(err.Error(), "bound root") {
		t.Fatalf("root symlink error = %v", err)
	}
}

func TestSecuredPathRejectsRootIdentityDrift(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "root")
	write(t, root, "bound/file", "body\n")
	secured, err := secureBoundPath(root, "bound/file", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(root, filepath.Join(parent, "old-root")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := secured.verifyRootStable(); err == nil || !strings.Contains(err.Error(), "root identity drifted") {
		t.Fatalf("root drift error = %v", err)
	}
}

func TestDirectoryDigestRejectsSymlinks(t *testing.T) {
	root := t.TempDir()
	write(t, root, "context/file", "body\n")
	if err := os.Symlink("file", filepath.Join(root, "context", "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := DirectoryDigest(root, "context"); err == nil || !strings.Contains(err.Error(), "unsafe context entry") {
		t.Fatalf("symlink error = %v", err)
	}
}

func fixture(t *testing.T) (string, Spec, Resolution) {
	t.Helper()
	root := t.TempDir()
	for _, path := range []string{
		"distribution/policies/managed.json", "distribution/policies/network.json",
		"distribution/policies/verifier.json", "distribution/policies/probe.json",
		"distribution/managed/Dockerfile", "distribution/managed/LICENSES.json",
		"distribution/managed/sbom.json", "distribution/boundary/Dockerfile",
		"distribution/boundary/main.go", "distribution/boundary/LICENSES.json",
		"distribution/boundary/sbom.json",
	} {
		write(t, root, path, path+"\n")
	}
	managedRuntime := &RuntimeIdentity{
		Pi: PackageIdentity{
			Name:         "@earendil-works/pi-coding-agent",
			Version:      "0.84.2",
			NPMIntegrity: "sha512-l4E+B7hgXKWddRo8bC/eSue2aWZjEgJ9xIpf5p0Og+lq8a2TArCwJ0HCoCPCgaBP/tN4zbYH/wOwvx9pJpeLCA==",
		},
		Node: NodeIdentity{Version: "22.19.0", Executable: "/usr/local/bin/node", VersionOutput: "v22.19.0"},
	}
	spec := Spec{
		SchemaVersion: SpecSchema,
		ReleaseID:     "chora-m1-alpha",
		Platform:      Platform{OS: "linux", Architecture: "arm64"},
		Roles: []RoleBinding{
			{Role: RoleManagedPiRuntime, ArtifactID: "managed", Policy: policy(t, root, "managed", "distribution/policies/managed.json")},
			{Role: RoleNetworkBoundary, ArtifactID: "network", Policy: policy(t, root, "network", "distribution/policies/network.json")},
			{Role: RoleIndependentVerifier, ArtifactID: "managed", AliasOfRole: RoleManagedPiRuntime, Policy: policy(t, root, "verifier", "distribution/policies/verifier.json")},
			{Role: RoleCapabilityProbe, ArtifactID: "managed", AliasOfRole: RoleManagedPiRuntime, Policy: policy(t, root, "probe", "distribution/policies/probe.json")},
		},
		Artifacts: []ArtifactSpec{
			artifact(t, root, "managed", "distribution/managed", []string{"pi"}, managedRuntime),
			artifact(t, root, "network", "distribution/boundary", []string{"/boundary"}, nil),
		},
	}
	resolution := Resolution{
		SchemaVersion: ResolutionSchema,
		Artifacts: []ResolvedArtifact{
			resolved(t, root, "managed", "archives/managed.tar"),
			resolved(t, root, "network", "archives/network.tar"),
		},
	}
	return root, spec, resolution
}

func artifact(t *testing.T, root, id, context string, entrypoint []string, runtime *RuntimeIdentity) ArtifactSpec {
	t.Helper()
	baseReference := "registry.example/base/" + id + "@sha256:" + strings.Repeat("a", 64)
	entrypointJSON, err := json.Marshal(entrypoint)
	if err != nil {
		t.Fatal(err)
	}
	recipe := "ARG BASE_PULL_REFERENCE=" + baseReference + "\n"
	recipe += "FROM ${BASE_PULL_REFERENCE}\n"
	if runtime != nil {
		recipe += "ARG PI_PACKAGE=" + runtime.Pi.Name + "\n"
		recipe += "ARG PI_VERSION=" + runtime.Pi.Version + "\n"
		recipe += "ARG PI_INTEGRITY=" + runtime.Pi.NPMIntegrity + "\n"
		recipe += "RUN test \"$(node --version)\" = \"" + runtime.Node.VersionOutput + "\"; true\n"
	}
	recipe += "ENTRYPOINT " + string(entrypointJSON) + "\n"
	write(t, root, context+"/Dockerfile", recipe)
	contextDigest, err := DirectoryDigest(root, context)
	if err != nil {
		t.Fatal(err)
	}
	return ArtifactSpec{
		ID:               id,
		Recipe:           binding(t, root, context+"/Dockerfile"),
		Context:          DirectoryBinding{Path: context, SHA256: contextDigest},
		RecipeProvenance: []FileBinding{binding(t, root, context+"/Dockerfile")},
		BuildInputs:      []BuildInput{{Name: "BASE_PULL_REFERENCE", OCIPullReference: baseReference}},
		Runtime:          runtime,
		Entrypoint:       entrypoint,
		LicenseInventory: binding(t, root, context+"/LICENSES.json"),
		SBOM:             binding(t, root, context+"/sbom.json"),
	}
}

func policy(t *testing.T, root, id, path string) PolicyBinding {
	t.Helper()
	return PolicyBinding{ID: id, FileBinding: binding(t, root, path)}
}

func resolved(t *testing.T, root, id, archivePath string) ResolvedArtifact {
	t.Helper()
	config := []byte(`{"architecture":"arm64","os":"linux","rootfs":{"type":"layers","diff_ids":["sha256:` + strings.Repeat("5", 64) + `"]}}`)
	configDigest := sha256.Sum256(config)
	configName := hex.EncodeToString(configDigest[:]) + ".json"
	manifest, err := json.Marshal([]dockerArchiveManifestEntry{{Config: configName, Layers: []string{"layer/layer.tar"}}})
	if err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, member := range []struct {
		name string
		body []byte
	}{{"manifest.json", manifest}, {configName, config}, {"layer/layer.tar", []byte("layer-" + id)}} {
		if err := writer.WriteHeader(&tar.Header{Name: member.name, Mode: 0o444, Size: int64(len(member.body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write(member.body); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	writeBytes(t, root, archivePath, archive.Bytes())
	archiveDigest := sha256.Sum256(archive.Bytes())
	return ResolvedArtifact{ArtifactID: id, LocalDockerConfigImageID: "sha256:" + hex.EncodeToString(configDigest[:]), Archive: ArchiveBinding{
		Format: DockerArchiveFormat, Path: archivePath, Size: int64(archive.Len()), SHA256: hex.EncodeToString(archiveDigest[:]),
	}}
}

func binding(t *testing.T, root, relative string) FileBinding {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return FileBinding{Path: relative, SHA256: hex.EncodeToString(digest[:])}
}

func write(t *testing.T, root, relative, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeBytes(t *testing.T, root, relative string, body []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
}

func copyResolutionArchives(t *testing.T, sourceRoot, releaseRoot string, resolution Resolution) {
	t.Helper()
	for _, artifact := range resolution.Artifacts {
		source := filepath.Join(sourceRoot, filepath.FromSlash(artifact.Archive.Path))
		target := filepath.Join(releaseRoot, filepath.FromSlash(artifact.Archive.Path))
		bytes, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, bytes, 0o400); err != nil {
			t.Fatal(err)
		}
	}
}

func canonicalTestRoot(t *testing.T, root string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
