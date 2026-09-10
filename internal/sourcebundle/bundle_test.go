package sourcebundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestCreateVerifyInstallDeterministicSourceBundle(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	createFixtureSource(t, source)
	writeFixture(t, filepath.Join(source, "web", "dist", "generated.js"), "excluded")
	writeFixture(t, filepath.Join(source, "web", "cache.tsbuildinfo"), "excluded")
	writeFixture(t, filepath.Join(source, "web", "node_modules", "dep", "index.js"), "excluded")

	firstRoot := filepath.Join(root, "bundle-1")
	secondRoot := filepath.Join(root, "bundle-2")
	first, err := Create(source, firstRoot)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Create(source, secondRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("manifest drift: %#v != %#v", first, second)
	}
	if first.InstallOnlyProjection.Files != 4 || first.ModelReadableProjection.Files != len(first.Files)-4 {
		t.Fatalf("projection classification = %#v %#v", first.InstallOnlyProjection, first.ModelReadableProjection)
	}
	for _, entry := range first.Files {
		if entry.Path == "web/dist/generated.js" || entry.Path == "web/cache.tsbuildinfo" || entry.Path == "web/node_modules/dep/index.js" {
			t.Fatalf("generated path bundled: %s", entry.Path)
		}
	}
	if _, err := Verify(firstRoot); err != nil {
		t.Fatal(err)
	}
	installRoot := filepath.Join(root, "install")
	installed, err := Install(firstRoot, installRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, installed) {
		t.Fatalf("installed manifest drift: %#v != %#v", first, installed)
	}
	info, err := os.Stat(installRoot)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("install root mode = %v, err = %v", info.Mode().Perm(), err)
	}
}

func TestRepositorySourcePathPolicyMatchesDeclaredCoverage(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckPolicy(root); err != nil {
		t.Fatal(err)
	}
}

func TestCheckPolicyRejectsGroupWorldWritableSourceFile(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	createFixtureSource(t, source)
	if err := os.Chmod(filepath.Join(source, "package.json"), 0o666); err != nil {
		t.Fatal(err)
	}

	err := CheckPolicy(source)
	if err == nil || !strings.Contains(err.Error(), "source file is group/world writable: package.json") {
		t.Fatalf("group/world-writable policy error = %v", err)
	}
}

func TestCleanupRequiresExactInstallationIdentity(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	createFixtureSource(t, source)
	bundleRoot := filepath.Join(root, "bundle")
	manifest, err := Create(source, bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	installRoot := filepath.Join(root, "install")
	if _, err := Install(bundleRoot, installRoot); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(installRoot, "data", "chora.db"), "owned data")
	if err := Cleanup(installRoot, strings.Repeat("0", 64)); err == nil {
		t.Fatal("wrong aggregate cleaned installation")
	}
	if _, err := os.Stat(installRoot); err != nil {
		t.Fatalf("failed cleanup removed root: %v", err)
	}
	if err := Cleanup(installRoot, manifest.AggregateSHA256); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(installRoot); !os.IsNotExist(err) {
		t.Fatalf("installation remains after cleanup: %v", err)
	}
}

func TestStageWebAndSeparateDataLifecyclePreserveInstalledSource(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	createFixtureSource(t, source)
	bundleRoot := filepath.Join(root, "bundle")
	manifest, err := Create(source, bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	installRoot := filepath.Join(root, "install")
	if _, err := Install(bundleRoot, installRoot); err != nil {
		t.Fatal(err)
	}
	stageRoot := filepath.Join(installRoot, "build", "web-workspace")
	files, err := StageWeb(installRoot, stageRoot, manifest.AggregateSHA256)
	if err != nil || files < 4 {
		t.Fatalf("StageWeb() files = %d, err = %v", files, err)
	}
	writeFixture(t, filepath.Join(stageRoot, "web", "dist", "generated.js"), "generated")
	if _, err := Verify(installRoot); err != nil {
		t.Fatalf("generated build mutated installed source identity: %v", err)
	}
	dataRoot := filepath.Join(root, "data")
	if err := InitData(installRoot, dataRoot, manifest.AggregateSHA256); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(dataRoot, "runtime", "evidence.json"), "runtime")
	if err := VerifyData(installRoot, dataRoot, manifest.AggregateSHA256); err != nil {
		t.Fatal(err)
	}
	if err := CleanupInstallation(installRoot, dataRoot, manifest.AggregateSHA256); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{installRoot, dataRoot} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("owned root remains after cleanup: %s: %v", path, err)
		}
	}
}

func TestCleanupInstallationRemovesReadOnlyRuntimeEvidence(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	createFixtureSource(t, source)
	bundleRoot := filepath.Join(root, "bundle")
	manifest, err := Create(source, bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	installRoot := filepath.Join(root, "install")
	if _, err := Install(bundleRoot, installRoot); err != nil {
		t.Fatal(err)
	}
	dataRoot := filepath.Join(root, "data")
	if err := InitData(installRoot, dataRoot, manifest.AggregateSHA256); err != nil {
		t.Fatal(err)
	}
	evidenceRoot := filepath.Join(dataRoot, "runtime", "artifacts", "attempt")
	writeFixture(t, filepath.Join(evidenceRoot, "patch.diff"), "immutable")
	if err := os.Chmod(filepath.Join(evidenceRoot, "patch.diff"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(evidenceRoot, 0o500); err != nil {
		t.Fatal(err)
	}

	if err := CleanupInstallation(installRoot, dataRoot, manifest.AggregateSHA256); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{installRoot, dataRoot} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatalf("owned root remains after cleanup: %s: %v", path, err)
		}
	}
}

func TestCleanupInstallationRejectsSymlinkBeforeRemovingIdentity(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	createFixtureSource(t, source)
	bundleRoot := filepath.Join(root, "bundle")
	manifest, err := Create(source, bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	installRoot := filepath.Join(root, "install")
	if _, err := Install(bundleRoot, installRoot); err != nil {
		t.Fatal(err)
	}
	dataRoot := filepath.Join(root, "data")
	if err := InitData(installRoot, dataRoot, manifest.AggregateSHA256); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(root, "outside")
	writeFixture(t, outside, "must remain")
	if err := os.Symlink(outside, filepath.Join(dataRoot, "unsafe-link")); err != nil {
		t.Fatal(err)
	}

	if err := CleanupInstallation(installRoot, dataRoot, manifest.AggregateSHA256); err == nil {
		t.Fatal("cleanup accepted a symlink in the data tree")
	}
	if _, err := os.Stat(filepath.Join(dataRoot, DataName)); err != nil {
		t.Fatalf("failed cleanup removed data identity: %v", err)
	}
	if encoded, err := os.ReadFile(outside); err != nil || string(encoded) != "must remain" {
		t.Fatalf("failed cleanup changed external target: %q, %v", encoded, err)
	}
}

func TestStageAndDataRejectIdentityDrift(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	createFixtureSource(t, source)
	bundleRoot := filepath.Join(root, "bundle")
	manifest, err := Create(source, bundleRoot)
	if err != nil {
		t.Fatal(err)
	}
	installRoot := filepath.Join(root, "install")
	if _, err := Install(bundleRoot, installRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := StageWeb(installRoot, filepath.Join(installRoot, "source", "stage"), manifest.AggregateSHA256); err == nil {
		t.Fatal("stage inside immutable source passed")
	}
	dataRoot := filepath.Join(root, "data")
	if err := InitData(installRoot, dataRoot, manifest.AggregateSHA256); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dataRoot, DataName)
	encoded, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(marker, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(marker, append(encoded, []byte("{}")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := VerifyData(installRoot, dataRoot, manifest.AggregateSHA256); err == nil {
		t.Fatal("trailing data identity passed")
	}
	if err := CleanupInstallation(installRoot, dataRoot, manifest.AggregateSHA256); err == nil {
		t.Fatal("drifted data identity was removed")
	}
}

func TestVerifyRejectsTrailingManifestValue(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	createFixtureSource(t, source)
	bundleRoot := filepath.Join(root, "bundle")
	if _, err := Create(source, bundleRoot); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(bundleRoot, ManifestName)
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(manifestPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(encoded, []byte("{}")...), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(bundleRoot); err == nil {
		t.Fatal("trailing manifest value passed")
	}
}

func TestVerifyRejectsChangedAndExtraFiles(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	createFixtureSource(t, source)
	bundleRoot := filepath.Join(root, "bundle")
	if _, err := Create(source, bundleRoot); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(bundleRoot, SourceDirName, "README.md")
	if err := os.WriteFile(path, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(bundleRoot); err == nil {
		t.Fatal("changed file passed verification")
	}

	otherBundle := filepath.Join(root, "bundle-extra")
	if _, err := Create(source, otherBundle); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(otherBundle, SourceDirName, "extra"), "extra")
	if _, err := Verify(otherBundle); err == nil {
		t.Fatal("extra file passed verification")
	}
}

func TestCreateRejectsUndeclaredInnocuousSourceFile(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	createFixtureSource(t, source)
	writeFixture(t, filepath.Join(source, "internal", "undeclared.txt"), "innocuous\n")
	if _, err := Create(source, filepath.Join(root, "bundle")); err == nil || !strings.Contains(err.Error(), "declared source coverage") {
		t.Fatalf("undeclared source file error = %v", err)
	}
}

func TestCreateRejectsNPMAuthenticationConfiguration(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	createFixtureSource(t, source)
	writeFixture(t, filepath.Join(source, ".npmrc"), "engine-strict=true\nregistry=https://registry.npmjs.org/\n//registry.npmjs.org/:_authToken=secret\n")
	if _, err := Create(source, filepath.Join(root, "bundle")); err == nil || !strings.Contains(err.Error(), "unsafe or unknown field") {
		t.Fatalf("npm authentication field error = %v", err)
	}
}

func TestVerifyRejectsProjectionIdentityTamper(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	createFixtureSource(t, source)
	bundleRoot := filepath.Join(root, "bundle")
	if _, err := Create(source, bundleRoot); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(bundleRoot, ManifestName)
	encoded, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	var manifest Manifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.ModelReadableProjection.AggregateSHA256 = strings.Repeat("0", 64)
	encoded, err = json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(manifestPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, append(encoded, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Verify(bundleRoot); err == nil || !strings.Contains(err.Error(), "projection identity") {
		t.Fatalf("projection tamper error = %v", err)
	}
}

func TestCreateRejectsUnsafeSourceAndExistingTarget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	createFixtureSource(t, source)
	if err := os.Chmod(filepath.Join(source, "README.md"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(source, filepath.Join(root, "unsafe")); err == nil {
		t.Fatal("group/world-writable source passed")
	}
	if err := os.Chmod(filepath.Join(source, "README.md"), 0o644); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(root, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := Create(source, existing); err == nil {
		t.Fatal("existing target passed")
	}
	if _, err := Create(source, filepath.Join(source, "nested")); err == nil {
		t.Fatal("bundle inside source passed")
	}
}

func createFixtureSource(t *testing.T, source string) {
	t.Helper()
	files := map[string]string{
		".npmrc":    "engine-strict=true\nregistry=https://registry.npmjs.org/\n",
		"LICENSE":   "fixture license\n",
		"README.md": "fixture\n",
		"contracts/g2-m4/source-baseline-v4/delta/web/src/App.tsx": "const historical = \"/Users/build-user/chora\"\n",
		"contracts/g2-m4/source-baseline-v5/delta/web/src/App.tsx": "const historical = \"/Users/build-user/chora\"\n",
		"contracts/g2-m4/source-baseline-v6/delta/web/src/App.tsx": "const historical = \"/Users/build-user/chora\"\n",
		"go.mod":             "module github.com/Yangyang96/chora\n",
		"internal/safe.go":   "package safe\n",
		"package-lock.json":  "{}\n",
		"package.json":       "{\"name\":\"chora\",\"private\":true}\n",
		"vendor/modules.txt": "fixture vendor metadata\n",
		"web/package.json":   "{\"name\":\"web\",\"private\":true}\n",
	}
	for path, body := range files {
		writeFixture(t, filepath.Join(source, filepath.FromSlash(path)), body)
	}
	policy := sourcePathPolicy{
		SchemaVersion:          SourcePathPolicySchema,
		Roots:                  []string{".npmrc", "LICENSE", "README.md", "contracts", "distribution", "go.mod", "internal", "package-lock.json", "package.json", "vendor", "web"},
		ExcludedDirectoryNames: []string{".vite", "coverage", "dist", "node_modules", "test-results"},
		ExcludedFileSuffixes:   []string{".tsbuildinfo"},
		InstallOnlyRoots:       []string{"vendor"},
		InstallOnlyPaths: []string{
			"contracts/g2-m4/source-baseline-v4/delta/web/src/App.tsx",
			"contracts/g2-m4/source-baseline-v5/delta/web/src/App.tsx",
			"contracts/g2-m4/source-baseline-v6/delta/web/src/App.tsx",
		},
	}
	paths := make([]string, 0, len(files)+1)
	for path := range files {
		paths = append(paths, path)
	}
	paths = append(paths, SourcePathPolicyPath)
	sort.Strings(paths)
	installOnly, modelReadable := classifyPaths(policy, paths)
	policy.DeclaredFiles = len(paths)
	policy.DeclaredPathsSHA256 = pathsDigest(paths)
	policy.InstallOnlyFiles = len(installOnly)
	policy.InstallOnlyPathsSHA256 = pathsDigest(installOnly)
	policy.ModelReadableFiles = len(modelReadable)
	policy.ModelReadablePathsSHA256 = pathsDigest(modelReadable)
	encoded, err := json.MarshalIndent(policy, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, filepath.Join(source, filepath.FromSlash(SourcePathPolicyPath)), string(append(encoded, '\n')))
}

func writeFixture(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}
