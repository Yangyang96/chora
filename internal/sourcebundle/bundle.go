package sourcebundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
)

const (
	ManifestName           = "source-manifest.json"
	InstallName            = "installation.json"
	DataName               = "data-installation.json"
	SourceDirName          = "source"
	SourcePathPolicyPath   = "distribution/v1/policies/source-path-policy.v1.json"
	SchemaVersion          = "chora.local-alpha-source-bundle.v1"
	SourcePathPolicySchema = "chora.source-path-policy.v1"
	InstallSchema          = "chora.local-alpha-installation.v1"
	DataSchema             = "chora.local-alpha-data.v1"
	manifestMode           = 0o444
	installedMode          = 0o700
	bundleRootMode         = 0o700
)

var DefaultPaths = []string{
	".gitignore", ".npmrc", ".nvmrc", "AGENTS.md", "LICENSE", "Makefile", "README.md",
	"README.zh-CN.md", "go.mod", "go.sum", "package.json", "package-lock.json",
	"playwright.config.ts", "playwright.intent-real.config.ts", "playwright.joined.config.ts",
	"playwright.u4-real.config.ts", "cmd", "contracts",
	"distribution", "docs", "e2e", "internal", "migrations", "schemas", "testdata", "tools",
	"vendor", "web",
}

type Entry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	SchemaVersion           string                   `json:"schema_version"`
	AggregateSHA256         string                   `json:"aggregate_sha256"`
	SourcePathPolicy        SourcePathPolicyIdentity `json:"source_path_policy"`
	InstallOnlyProjection   ProjectionIdentity       `json:"install_only_projection"`
	ModelReadableProjection ProjectionIdentity       `json:"model_readable_projection"`
	Files                   []Entry                  `json:"files"`
}

type SourcePathPolicyIdentity struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type ProjectionIdentity struct {
	Files           int    `json:"files"`
	PathsSHA256     string `json:"paths_sha256"`
	AggregateSHA256 string `json:"aggregate_sha256"`
}

type sourcePathPolicy struct {
	SchemaVersion            string   `json:"schema_version"`
	Roots                    []string `json:"roots"`
	ExcludedDirectoryNames   []string `json:"excluded_directory_names"`
	ExcludedFileSuffixes     []string `json:"excluded_file_suffixes"`
	DeclaredFiles            int      `json:"declared_files"`
	DeclaredPathsSHA256      string   `json:"declared_paths_sha256"`
	InstallOnlyRoots         []string `json:"install_only_roots"`
	InstallOnlyPaths         []string `json:"install_only_paths"`
	InstallOnlyFiles         int      `json:"install_only_files"`
	InstallOnlyPathsSHA256   string   `json:"install_only_paths_sha256"`
	ModelReadableFiles       int      `json:"model_readable_files"`
	ModelReadablePathsSHA256 string   `json:"model_readable_paths_sha256"`
}

type Installation struct {
	SchemaVersion   string `json:"schema_version"`
	AggregateSHA256 string `json:"aggregate_sha256"`
	Files           int    `json:"files"`
}

type DataInstallation struct {
	SchemaVersion   string `json:"schema_version"`
	AggregateSHA256 string `json:"aggregate_sha256"`
	InstallRoot     string `json:"install_root"`
}

func Create(sourceRoot, bundleRoot string) (Manifest, error) {
	sourceRoot, bundleRoot, err := validateRoots(sourceRoot, bundleRoot)
	if err != nil {
		return Manifest{}, err
	}
	policy, policyDigest, err := loadSourcePathPolicy(sourceRoot)
	if err != nil {
		return Manifest{}, err
	}
	if err := makeFreshDirectory(bundleRoot, bundleRootMode); err != nil {
		return Manifest{}, err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = os.RemoveAll(bundleRoot)
		}
	}()
	destination := filepath.Join(bundleRoot, SourceDirName)
	if err := os.Mkdir(destination, 0o755); err != nil {
		return Manifest{}, fmt.Errorf("create bundle source root: %w", err)
	}

	entries, err := copyDeclaredFiles(sourceRoot, destination, policy)
	if err != nil {
		return Manifest{}, err
	}
	if err := validateNPMRC(filepath.Join(sourceRoot, ".npmrc")); err != nil {
		return Manifest{}, err
	}
	installOnly, modelReadable := classifyEntries(policy, entries)
	manifest := Manifest{
		SchemaVersion:           SchemaVersion,
		AggregateSHA256:         aggregate(entries),
		SourcePathPolicy:        SourcePathPolicyIdentity{Path: SourcePathPolicyPath, SHA256: policyDigest},
		InstallOnlyProjection:   projectionIdentity(installOnly),
		ModelReadableProjection: projectionIdentity(modelReadable),
		Files:                   entries,
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return Manifest{}, fmt.Errorf("encode source manifest: %w", err)
	}
	encoded = append(encoded, '\n')
	manifestPath := filepath.Join(bundleRoot, ManifestName)
	if err := os.WriteFile(manifestPath, encoded, manifestMode); err != nil {
		return Manifest{}, fmt.Errorf("write source manifest: %w", err)
	}
	if err := os.Chmod(manifestPath, manifestMode); err != nil {
		return Manifest{}, fmt.Errorf("secure source manifest: %w", err)
	}
	succeeded = true
	return manifest, nil
}

// CheckPolicy verifies that the exact current source path set still matches the
// checked-in source path policy without creating a bundle or any other output.
func CheckPolicy(sourceRoot string) error {
	var err error
	sourceRoot, err = cleanAbsolute(sourceRoot)
	if err != nil {
		return fmt.Errorf("source root: %w", err)
	}
	info, err := os.Stat(sourceRoot)
	if err != nil || !info.IsDir() {
		return errors.New("source root must be an existing directory")
	}
	policy, _, err := loadSourcePathPolicy(sourceRoot)
	if err != nil {
		return err
	}
	_, err = declaredSourcePaths(sourceRoot, policy)
	return err
}

func Verify(bundleRoot string) (Manifest, error) {
	bundleRoot, err := cleanAbsolute(bundleRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("bundle root: %w", err)
	}
	manifestBytes, err := os.ReadFile(filepath.Join(bundleRoot, ManifestName))
	if err != nil {
		return Manifest{}, fmt.Errorf("read source manifest: %w", err)
	}
	var manifest Manifest
	decoder := json.NewDecoder(strings.NewReader(string(manifestBytes)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode source manifest: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Manifest{}, fmt.Errorf("decode source manifest: %w", err)
	}
	if manifest.SchemaVersion != SchemaVersion || len(manifest.Files) == 0 || !validDigest(manifest.AggregateSHA256) ||
		manifest.SourcePathPolicy.Path != SourcePathPolicyPath || !validDigest(manifest.SourcePathPolicy.SHA256) {
		return Manifest{}, errors.New("source manifest identity is invalid")
	}
	if !sort.SliceIsSorted(manifest.Files, func(i, j int) bool { return manifest.Files[i].Path < manifest.Files[j].Path }) {
		return Manifest{}, errors.New("source manifest paths are not sorted")
	}
	for index, entry := range manifest.Files {
		if !validEntry(entry) || (index > 0 && manifest.Files[index-1].Path == entry.Path) {
			return Manifest{}, errors.New("source manifest entry is invalid")
		}
	}
	actual, err := inspectTree(filepath.Join(bundleRoot, SourceDirName))
	if err != nil {
		return Manifest{}, err
	}
	policy, policyDigest, err := loadSourcePathPolicy(filepath.Join(bundleRoot, SourceDirName))
	if err != nil {
		return Manifest{}, err
	}
	if policyDigest != manifest.SourcePathPolicy.SHA256 {
		return Manifest{}, errors.New("source path policy identity mismatch")
	}
	if err := validatePolicyCoverage(policy, entryPaths(actual)); err != nil {
		return Manifest{}, err
	}
	if len(actual) != len(manifest.Files) {
		return Manifest{}, errors.New("source bundle file coverage mismatch")
	}
	for index := range actual {
		if actual[index] != manifest.Files[index] {
			return Manifest{}, fmt.Errorf("source bundle entry mismatch: %s", manifest.Files[index].Path)
		}
	}
	if aggregate(actual) != manifest.AggregateSHA256 {
		return Manifest{}, errors.New("source bundle aggregate mismatch")
	}
	installOnly, modelReadable := classifyEntries(policy, actual)
	if manifest.InstallOnlyProjection != projectionIdentity(installOnly) ||
		manifest.ModelReadableProjection != projectionIdentity(modelReadable) {
		return Manifest{}, errors.New("source bundle projection identity mismatch")
	}
	if err := validateNPMRC(filepath.Join(bundleRoot, SourceDirName, ".npmrc")); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func Install(bundleRoot, installRoot string) (Manifest, error) {
	manifest, err := Verify(bundleRoot)
	if err != nil {
		return Manifest{}, err
	}
	installRoot, err = cleanAbsolute(installRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("installation root: %w", err)
	}
	if err := makeFreshDirectory(installRoot, installedMode); err != nil {
		return Manifest{}, err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = os.RemoveAll(installRoot)
		}
	}()
	if err := os.Mkdir(filepath.Join(installRoot, SourceDirName), 0o755); err != nil {
		return Manifest{}, fmt.Errorf("create installation source root: %w", err)
	}
	for _, entry := range manifest.Files {
		if err := copyFile(filepath.Join(bundleRoot, SourceDirName, filepath.FromSlash(entry.Path)), filepath.Join(installRoot, SourceDirName, filepath.FromSlash(entry.Path)), parseMode(entry.Mode)); err != nil {
			return Manifest{}, err
		}
	}
	if err := copyFile(filepath.Join(bundleRoot, ManifestName), filepath.Join(installRoot, ManifestName), manifestMode); err != nil {
		return Manifest{}, err
	}
	if _, err := Verify(installRoot); err != nil {
		return Manifest{}, fmt.Errorf("verify installed source: %w", err)
	}
	installation := Installation{SchemaVersion: InstallSchema, AggregateSHA256: manifest.AggregateSHA256, Files: len(manifest.Files)}
	encoded, err := json.MarshalIndent(installation, "", "  ")
	if err != nil {
		return Manifest{}, fmt.Errorf("encode installation identity: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(filepath.Join(installRoot, InstallName), encoded, manifestMode); err != nil {
		return Manifest{}, fmt.Errorf("write installation identity: %w", err)
	}
	if err := os.Chmod(filepath.Join(installRoot, InstallName), manifestMode); err != nil {
		return Manifest{}, fmt.Errorf("secure installation identity: %w", err)
	}
	succeeded = true
	return manifest, nil
}

func Cleanup(installRoot, expectedAggregate string) error {
	installRoot, err := cleanAbsolute(installRoot)
	if err != nil {
		return fmt.Errorf("installation root: %w", err)
	}
	if !validDigest(expectedAggregate) {
		return errors.New("expected installation aggregate is invalid")
	}
	info, err := os.Lstat(installRoot)
	if err != nil || !info.IsDir() || info.Mode().Perm() != installedMode {
		return errors.New("installation root identity is unsafe")
	}
	markerBytes, err := os.ReadFile(filepath.Join(installRoot, InstallName))
	if err != nil {
		return errors.New("installation identity is unavailable")
	}
	var installation Installation
	decoder := json.NewDecoder(strings.NewReader(string(markerBytes)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&installation); err != nil || requireJSONEOF(decoder) != nil || installation.SchemaVersion != InstallSchema || installation.AggregateSHA256 != expectedAggregate || installation.Files <= 0 {
		return errors.New("installation identity mismatch")
	}
	manifestBytes, err := os.ReadFile(filepath.Join(installRoot, ManifestName))
	if err != nil {
		return errors.New("installation manifest is unavailable")
	}
	var manifest Manifest
	manifestDecoder := json.NewDecoder(strings.NewReader(string(manifestBytes)))
	manifestDecoder.DisallowUnknownFields()
	if err := manifestDecoder.Decode(&manifest); err != nil || requireJSONEOF(manifestDecoder) != nil || manifest.SchemaVersion != SchemaVersion || manifest.AggregateSHA256 != expectedAggregate || len(manifest.Files) != installation.Files {
		return errors.New("installation manifest identity mismatch")
	}
	if err := os.RemoveAll(installRoot); err != nil {
		return fmt.Errorf("remove installation root: %w", err)
	}
	if _, err := os.Lstat(installRoot); !errors.Is(err, os.ErrNotExist) {
		return errors.New("installation root removal is unproven")
	}
	return nil
}

// StageWeb creates a disposable npm workspace under the installed product root
// without mutating the immutable installed source tree.
func StageWeb(installRoot, stageRoot, expectedAggregate string) (int, error) {
	manifest, err := verifyInstallation(installRoot, expectedAggregate)
	if err != nil {
		return 0, err
	}
	stageRoot, err = cleanAbsolute(stageRoot)
	if err != nil {
		return 0, fmt.Errorf("web stage root: %w", err)
	}
	if !pathWithin(installRoot, stageRoot) || pathWithin(filepath.Join(installRoot, SourceDirName), stageRoot) {
		return 0, errors.New("web stage must be inside the installation and outside immutable source")
	}
	if err := makeFreshDirectory(stageRoot, installedMode); err != nil {
		return 0, err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = os.RemoveAll(stageRoot)
		}
	}()
	copied := 0
	for _, entry := range manifest.Files {
		if entry.Path != ".npmrc" && entry.Path != "package.json" && entry.Path != "package-lock.json" && !strings.HasPrefix(entry.Path, "web/") {
			continue
		}
		if err := copyFile(
			filepath.Join(installRoot, SourceDirName, filepath.FromSlash(entry.Path)),
			filepath.Join(stageRoot, filepath.FromSlash(entry.Path)),
			parseMode(entry.Mode),
		); err != nil {
			return 0, err
		}
		copied++
	}
	if copied == 0 {
		return 0, errors.New("web build inputs are absent")
	}
	succeeded = true
	return copied, nil
}

// InitData creates a separate owner-only data root bound to one exact product
// installation. The marker is later consumed by Preflight and bounded cleanup.
func InitData(installRoot, dataRoot, expectedAggregate string) error {
	installRoot, err := cleanAbsolute(installRoot)
	if err != nil {
		return fmt.Errorf("installation root: %w", err)
	}
	dataRoot, err = cleanAbsolute(dataRoot)
	if err != nil {
		return fmt.Errorf("data root: %w", err)
	}
	if rootsOverlap(installRoot, dataRoot) {
		return errors.New("installation and data roots must be disjoint")
	}
	if _, err := verifyInstallation(installRoot, expectedAggregate); err != nil {
		return err
	}
	if err := makeFreshDirectory(dataRoot, installedMode); err != nil {
		return err
	}
	succeeded := false
	defer func() {
		if !succeeded {
			_ = os.RemoveAll(dataRoot)
		}
	}()
	marker := DataInstallation{SchemaVersion: DataSchema, AggregateSHA256: expectedAggregate, InstallRoot: installRoot}
	encoded, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(filepath.Join(dataRoot, DataName), encoded, manifestMode); err != nil {
		return fmt.Errorf("write data identity: %w", err)
	}
	if err := os.Chmod(filepath.Join(dataRoot, DataName), manifestMode); err != nil {
		return fmt.Errorf("secure data identity: %w", err)
	}
	succeeded = true
	return nil
}

// VerifyData proves that a data root is owner-only and bound to the declared
// installation and source aggregate. Runtime files beneath it are allowed.
func VerifyData(installRoot, dataRoot, expectedAggregate string) error {
	installRoot, err := cleanAbsolute(installRoot)
	if err != nil {
		return err
	}
	dataRoot, err = cleanAbsolute(dataRoot)
	if err != nil {
		return err
	}
	if rootsOverlap(installRoot, dataRoot) {
		return errors.New("installation and data roots must be disjoint")
	}
	if err := safeOwnedRoot(dataRoot); err != nil {
		return fmt.Errorf("data root identity: %w", err)
	}
	encoded, err := os.ReadFile(filepath.Join(dataRoot, DataName))
	if err != nil {
		return errors.New("data identity is unavailable")
	}
	var marker DataInstallation
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&marker); err != nil || requireJSONEOF(decoder) != nil || marker.SchemaVersion != DataSchema || marker.AggregateSHA256 != expectedAggregate || marker.InstallRoot != installRoot {
		return errors.New("data identity mismatch")
	}
	info, err := os.Lstat(filepath.Join(dataRoot, DataName))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != manifestMode {
		return errors.New("data identity mode mismatch")
	}
	return nil
}

// CleanupInstallation removes only the exact, marker-bound data and product
// roots and proves both are absent afterwards.
func CleanupInstallation(installRoot, dataRoot, expectedAggregate string) error {
	if _, err := verifyInstallation(installRoot, expectedAggregate); err != nil {
		return err
	}
	if err := VerifyData(installRoot, dataRoot, expectedAggregate); err != nil {
		return err
	}
	if err := prepareOwnedTreeForRemoval(dataRoot); err != nil {
		return fmt.Errorf("prepare data root removal: %w", err)
	}
	entries, err := os.ReadDir(dataRoot)
	if err != nil {
		return fmt.Errorf("read data root for removal: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() == DataName {
			continue
		}
		path := filepath.Join(dataRoot, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("remove data root entry %q: %w", entry.Name(), err)
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("data root entry removal is unproven: %s", entry.Name())
		}
	}
	// Preserve the ownership marker until every runtime child is proven absent.
	// A failed cleanup therefore remains eligible for the same bounded retry.
	if err := os.Remove(filepath.Join(dataRoot, DataName)); err != nil {
		return fmt.Errorf("remove data identity: %w", err)
	}
	if err := os.Remove(dataRoot); err != nil {
		return fmt.Errorf("remove empty data root: %w", err)
	}
	if _, err := os.Lstat(dataRoot); !errors.Is(err, os.ErrNotExist) {
		return errors.New("data root removal is unproven")
	}
	return Cleanup(installRoot, expectedAggregate)
}

// prepareOwnedTreeForRemoval validates the entire marker-bound data tree before
// making its directories owner-writable. Runtime evidence is intentionally
// immutable, so ordinary RemoveAll cannot unlink it from read-only directories.
func prepareOwnedTreeForRemoval(root string) error {
	directories := make([]string, 0, 16)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!info.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("unsupported data entry: %s", path)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != os.Geteuid() {
			return fmt.Errorf("data entry owner mismatch: %s", path)
		}
		if info.Mode().Perm()&0o022 != 0 {
			return fmt.Errorf("group/world-writable data entry: %s", path)
		}
		if info.IsDir() {
			directories = append(directories, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, directory := range directories {
		info, err := os.Lstat(directory)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("data directory identity changed: %s", directory)
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != os.Geteuid() {
			return fmt.Errorf("data directory owner changed: %s", directory)
		}
		if err := os.Chmod(directory, installedMode); err != nil {
			return fmt.Errorf("make data directory removable: %s: %w", directory, err)
		}
	}
	return nil
}

func verifyInstallation(installRoot, expectedAggregate string) (Manifest, error) {
	installRoot, err := cleanAbsolute(installRoot)
	if err != nil {
		return Manifest{}, fmt.Errorf("installation root: %w", err)
	}
	if !validDigest(expectedAggregate) {
		return Manifest{}, errors.New("expected installation aggregate is invalid")
	}
	if err := safeOwnedRoot(installRoot); err != nil {
		return Manifest{}, fmt.Errorf("installation root identity: %w", err)
	}
	manifest, err := Verify(installRoot)
	if err != nil || manifest.AggregateSHA256 != expectedAggregate {
		return Manifest{}, errors.New("installed source identity mismatch")
	}
	markerBytes, err := os.ReadFile(filepath.Join(installRoot, InstallName))
	if err != nil {
		return Manifest{}, errors.New("installation identity is unavailable")
	}
	var installation Installation
	decoder := json.NewDecoder(strings.NewReader(string(markerBytes)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&installation); err != nil || requireJSONEOF(decoder) != nil || installation.SchemaVersion != InstallSchema || installation.AggregateSHA256 != expectedAggregate || installation.Files != len(manifest.Files) {
		return Manifest{}, errors.New("installation identity mismatch")
	}
	return manifest, nil
}

func safeOwnedRoot(root string) error {
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm() != installedMode {
		return errors.New("root is not an owner-only real directory")
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return errors.New("root owner mismatch")
	}
	return nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("trailing JSON value")
	}
	return err
}

func pathWithin(parent, child string) bool {
	relative, err := filepath.Rel(filepath.Clean(parent), filepath.Clean(child))
	return err == nil && relative != "." && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func rootsOverlap(left, right string) bool {
	return left == right || pathWithin(left, right) || pathWithin(right, left)
}

func validateRoots(sourceRoot, bundleRoot string) (string, string, error) {
	sourceRoot, err := cleanAbsolute(sourceRoot)
	if err != nil {
		return "", "", fmt.Errorf("source root: %w", err)
	}
	bundleRoot, err = cleanAbsolute(bundleRoot)
	if err != nil {
		return "", "", fmt.Errorf("bundle root: %w", err)
	}
	info, err := os.Stat(sourceRoot)
	if err != nil || !info.IsDir() {
		return "", "", errors.New("source root must be an existing directory")
	}
	relative, err := filepath.Rel(sourceRoot, bundleRoot)
	if err != nil || relative == "." || (relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
		return "", "", errors.New("bundle root must be outside source root")
	}
	return sourceRoot, bundleRoot, nil
}

func loadSourcePathPolicy(sourceRoot string) (sourcePathPolicy, string, error) {
	path := filepath.Join(sourceRoot, filepath.FromSlash(SourcePathPolicyPath))
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0o022 != 0 {
		return sourcePathPolicy{}, "", errors.New("source path policy is unavailable or unsafe")
	}
	encoded, err := os.ReadFile(path)
	if err != nil {
		return sourcePathPolicy{}, "", fmt.Errorf("read source path policy: %w", err)
	}
	var policy sourcePathPolicy
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return sourcePathPolicy{}, "", fmt.Errorf("decode source path policy: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return sourcePathPolicy{}, "", fmt.Errorf("decode source path policy: %w", err)
	}
	if err := validateSourcePathPolicy(policy); err != nil {
		return sourcePathPolicy{}, "", err
	}
	digest := sha256.Sum256(encoded)
	return policy, hex.EncodeToString(digest[:]), nil
}

func validateSourcePathPolicy(policy sourcePathPolicy) error {
	if policy.SchemaVersion != SourcePathPolicySchema ||
		!validSortedPolicyList(policy.Roots, true) ||
		!validSortedPolicyList(policy.ExcludedDirectoryNames, false) ||
		!validSortedPolicyList(policy.ExcludedFileSuffixes, false) ||
		!validSortedPolicyList(policy.InstallOnlyRoots, true) ||
		!validSortedPolicyList(policy.InstallOnlyPaths, false) ||
		policy.DeclaredFiles <= 0 || policy.InstallOnlyFiles <= 0 || policy.ModelReadableFiles <= 0 ||
		policy.DeclaredFiles != policy.InstallOnlyFiles+policy.ModelReadableFiles ||
		!validDigest(policy.DeclaredPathsSHA256) || !validDigest(policy.InstallOnlyPathsSHA256) ||
		!validDigest(policy.ModelReadablePathsSHA256) {
		return errors.New("source path policy identity is invalid")
	}
	rootSet := make(map[string]struct{}, len(policy.Roots))
	for _, root := range policy.Roots {
		rootSet[root] = struct{}{}
	}
	for _, required := range []string{".npmrc", "distribution", "go.mod", "package.json", "vendor"} {
		if _, ok := rootSet[required]; !ok {
			return fmt.Errorf("source path policy omits required root: %s", required)
		}
	}
	for _, root := range policy.InstallOnlyRoots {
		if _, ok := rootSet[root]; !ok {
			return fmt.Errorf("install-only root is outside source policy: %s", root)
		}
	}
	if !containsString(policy.InstallOnlyRoots, "vendor") {
		return errors.New("vendor must be classified as install-only")
	}
	if !pathDeclaredByPolicy(policy, SourcePathPolicyPath) || policyExcluded(policy, SourcePathPolicyPath, false) {
		return errors.New("source path policy does not declare itself")
	}
	return nil
}

func validSortedPolicyList(values []string, roots bool) bool {
	if len(values) == 0 || !sort.StringsAreSorted(values) {
		return false
	}
	for index, value := range values {
		if value == "" || strings.ContainsAny(value, "\\\x00") || value == "." || value == ".." ||
			strings.HasPrefix(value, "/") || strings.HasPrefix(value, "../") ||
			(index > 0 && values[index-1] == value) {
			return false
		}
		if roots && strings.Contains(value, "/") {
			return false
		}
		if !roots {
			clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
			if clean != value && !strings.HasPrefix(value, ".") {
				return false
			}
		}
	}
	return true
}

func copyDeclaredFiles(sourceRoot, destination string, policy sourcePathPolicy) ([]Entry, error) {
	paths, err := declaredSourcePaths(sourceRoot, policy)
	if err != nil {
		return nil, err
	}
	entries := make([]Entry, 0, len(paths))
	for _, relative := range paths {
		path := filepath.Join(sourceRoot, filepath.FromSlash(relative))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("source path identity changed: %s", relative)
		}
		entry, err := copySourceFile(sourceRoot, destination, path, info)
		if err != nil {
			return nil, err
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func declaredSourcePaths(sourceRoot string, policy sourcePathPolicy) ([]string, error) {
	var paths []string
	for _, declared := range policy.Roots {
		path := filepath.Join(sourceRoot, filepath.FromSlash(declared))
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("required source path %s: %w", declared, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("required source path is a symlink: %s", declared)
		}
		if info.Mode().IsRegular() {
			if err := validateSourceFileMode(declared, info.Mode()); err != nil {
				return nil, err
			}
			paths = append(paths, declared)
			continue
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("required source path has unsupported type: %s", declared)
		}
		err = filepath.WalkDir(path, func(current string, dirEntry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(sourceRoot, current)
			if err != nil {
				return err
			}
			if current != path && policyExcluded(policy, filepath.ToSlash(relative), dirEntry.IsDir()) {
				if dirEntry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if dirEntry.IsDir() {
				return nil
			}
			info, err := dirEntry.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				return fmt.Errorf("source path has unsupported type: %s", filepath.ToSlash(relative))
			}
			relativeSlash := filepath.ToSlash(relative)
			if err := validateSourceFileMode(relativeSlash, info.Mode()); err != nil {
				return err
			}
			paths = append(paths, relativeSlash)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("walk source path %s: %w", declared, err)
		}
	}
	sort.Strings(paths)
	if err := validatePolicyCoverage(policy, paths); err != nil {
		return nil, err
	}
	return paths, nil
}

func copySourceFile(sourceRoot, destination, source string, info os.FileInfo) (Entry, error) {
	relative, err := filepath.Rel(sourceRoot, source)
	if err != nil {
		return Entry{}, err
	}
	relative = filepath.ToSlash(relative)
	mode := info.Mode().Perm()
	if err := validateSourceFileMode(relative, info.Mode()); err != nil {
		return Entry{}, err
	}
	if err := copyFile(source, filepath.Join(destination, filepath.FromSlash(relative)), mode); err != nil {
		return Entry{}, err
	}
	digest, err := fileDigest(source)
	if err != nil {
		return Entry{}, err
	}
	return Entry{Path: relative, Size: info.Size(), Mode: fmt.Sprintf("%04o", mode), SHA256: digest}, nil
}

func validateSourceFileMode(relative string, mode fs.FileMode) error {
	if mode.Perm()&0o022 != 0 {
		return fmt.Errorf("source file is group/world writable: %s", relative)
	}
	return nil
}

func inspectTree(root string) ([]Entry, error) {
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() {
		return nil, errors.New("source bundle root is unavailable")
	}
	var entries []Entry
	err = filepath.WalkDir(root, func(path string, dirEntry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if dirEntry.IsDir() {
			return nil
		}
		info, err := dirEntry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return errors.New("source bundle contains a non-regular file")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		digest, err := fileDigest(path)
		if err != nil {
			return err
		}
		entries = append(entries, Entry{Path: filepath.ToSlash(relative), Size: info.Size(), Mode: fmt.Sprintf("%04o", info.Mode().Perm()), SHA256: digest})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("inspect source bundle: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries, nil
}

func policyExcluded(policy sourcePathPolicy, relative string, directory bool) bool {
	components := strings.Split(filepath.ToSlash(relative), "/")
	limit := len(components)
	if !directory {
		limit--
	}
	for _, component := range components[:limit] {
		if containsString(policy.ExcludedDirectoryNames, component) {
			return true
		}
	}
	if directory {
		return containsString(policy.ExcludedDirectoryNames, components[len(components)-1])
	}
	for _, suffix := range policy.ExcludedFileSuffixes {
		if strings.HasSuffix(relative, suffix) {
			return true
		}
	}
	return false
}

func validatePolicyCoverage(policy sourcePathPolicy, paths []string) error {
	if len(paths) != policy.DeclaredFiles || pathsDigest(paths) != policy.DeclaredPathsSHA256 {
		return errors.New("declared source coverage does not match source path policy")
	}
	for index, path := range paths {
		if (index > 0 && paths[index-1] >= path) || !pathDeclaredByPolicy(policy, path) || policyExcluded(policy, path, false) {
			return fmt.Errorf("source path is outside exact policy: %s", path)
		}
	}
	installOnly, modelReadable := classifyPaths(policy, paths)
	if len(installOnly) != policy.InstallOnlyFiles || pathsDigest(installOnly) != policy.InstallOnlyPathsSHA256 ||
		len(modelReadable) != policy.ModelReadableFiles || pathsDigest(modelReadable) != policy.ModelReadablePathsSHA256 {
		return errors.New("source path policy projection coverage mismatch")
	}
	return nil
}

func pathDeclaredByPolicy(policy sourcePathPolicy, path string) bool {
	for _, root := range policy.Roots {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

func classifyEntries(policy sourcePathPolicy, entries []Entry) ([]Entry, []Entry) {
	installOnly := make([]Entry, 0, policy.InstallOnlyFiles)
	modelReadable := make([]Entry, 0, policy.ModelReadableFiles)
	for _, entry := range entries {
		if installOnlyPath(policy, entry.Path) {
			installOnly = append(installOnly, entry)
		} else {
			modelReadable = append(modelReadable, entry)
		}
	}
	return installOnly, modelReadable
}

func classifyPaths(policy sourcePathPolicy, paths []string) ([]string, []string) {
	installOnly := make([]string, 0, policy.InstallOnlyFiles)
	modelReadable := make([]string, 0, policy.ModelReadableFiles)
	for _, path := range paths {
		if installOnlyPath(policy, path) {
			installOnly = append(installOnly, path)
		} else {
			modelReadable = append(modelReadable, path)
		}
	}
	return installOnly, modelReadable
}

func installOnlyPath(policy sourcePathPolicy, path string) bool {
	if containsString(policy.InstallOnlyPaths, path) {
		return true
	}
	for _, root := range policy.InstallOnlyRoots {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

func projectionIdentity(entries []Entry) ProjectionIdentity {
	return ProjectionIdentity{Files: len(entries), PathsSHA256: pathsDigest(entryPaths(entries)), AggregateSHA256: aggregate(entries)}
}

func entryPaths(entries []Entry) []string {
	paths := make([]string, len(entries))
	for index, entry := range entries {
		paths[index] = entry.Path
	}
	return paths
}

func pathsDigest(paths []string) string {
	hash := sha256.New()
	for _, path := range paths {
		fmt.Fprintf(hash, "%s\n", path)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func containsString(values []string, target string) bool {
	index := sort.SearchStrings(values, target)
	return index < len(values) && values[index] == target
}

func validateNPMRC(path string) error {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return errors.New("read safe npm configuration")
	}
	if len(encoded) == 0 || len(encoded) > 4096 || strings.ContainsRune(string(encoded), '\x00') {
		return errors.New("npm configuration is malformed")
	}
	want := map[string]string{
		"engine-strict": "true",
		"registry":      "https://registry.npmjs.org/",
	}
	seen := make(map[string]struct{}, len(want))
	for _, raw := range strings.Split(string(encoded), "\n") {
		if raw == "" {
			continue
		}
		if strings.TrimSpace(raw) != raw || strings.HasPrefix(raw, "#") || strings.HasPrefix(raw, ";") {
			return errors.New("npm configuration contains unsupported syntax")
		}
		key, value, ok := strings.Cut(raw, "=")
		expected, allowed := want[key]
		if !ok || !allowed || value != expected {
			return errors.New("npm configuration contains an unsafe or unknown field")
		}
		if _, duplicate := seen[key]; duplicate {
			return errors.New("npm configuration contains a duplicate field")
		}
		seen[key] = struct{}{}
	}
	if len(seen) != len(want) {
		return errors.New("npm configuration omits a required safe field")
	}
	return nil
}

func makeFreshDirectory(path string, mode fs.FileMode) error {
	if _, err := os.Lstat(path); err == nil {
		return errors.New("target root already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create target parent: %w", err)
	}
	if err := os.Mkdir(path, mode); err != nil {
		return fmt.Errorf("create target root: %w", err)
	}
	return os.Chmod(path, mode)
}

func copyFile(source, destination string, mode fs.FileMode) error {
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source file: %w", err)
	}
	defer input.Close()
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return fmt.Errorf("create destination directory: %w", err)
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create destination file: %w", err)
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil {
		return fmt.Errorf("copy source file: %w", copyErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close destination file: %w", closeErr)
	}
	if err := os.Chmod(destination, mode); err != nil {
		return fmt.Errorf("set destination mode: %w", err)
	}
	return nil
}

func fileDigest(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func aggregate(entries []Entry) string {
	hash := sha256.New()
	for _, entry := range entries {
		fmt.Fprintf(hash, "%s\x00%s\x00%d\x00%s\n", entry.Path, entry.Mode, entry.Size, entry.SHA256)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func validEntry(entry Entry) bool {
	clean := filepath.Clean(filepath.FromSlash(entry.Path))
	if entry.Path == "" || filepath.IsAbs(clean) || clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) || filepath.ToSlash(clean) != entry.Path || entry.Size < 0 || !validDigest(entry.SHA256) {
		return false
	}
	mode, err := strconv.ParseUint(entry.Mode, 8, 32)
	return err == nil && mode <= 0o777 && fs.FileMode(mode)&0o022 == 0
}

func validDigest(value string) bool {
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size && strings.ToLower(value) == value
}

func parseMode(value string) fs.FileMode {
	mode, _ := strconv.ParseUint(value, 8, 32)
	return fs.FileMode(mode)
}

func cleanAbsolute(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("path must be absolute")
	}
	clean := filepath.Clean(path)
	if clean != path || clean == string(filepath.Separator) {
		return "", errors.New("path must be clean and specific")
	}
	return clean, nil
}
