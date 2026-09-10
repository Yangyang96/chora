// Package releaseassets defines the immutable Chora v1 offline distribution
// contract. Registry references remain permitted only in build recipes as
// provenance for release construction; installation authority is one exact
// local Docker archive per distinct image.
package releaseassets

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	SpecSchema          = "chora.release-assets-spec.v1"
	ResolutionSchema    = "chora.offline-release-assets-resolution.v1"
	ManifestSchema      = "chora.release-assets-manifest.v1"
	DockerArchiveFormat = "docker-archive"

	RoleManagedPiRuntime    = "managed_pi_runtime"
	RoleNetworkBoundary     = "network_boundary"
	RoleIndependentVerifier = "independent_verifier"
	RoleCapabilityProbe     = "capability_probe"

	maxDocumentBytes = 4 << 20
)

var (
	requiredRoles = []string{
		RoleManagedPiRuntime,
		RoleNetworkBoundary,
		RoleIndependentVerifier,
		RoleCapabilityProbe,
	}
	identifierPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:[._-][a-z0-9]+)*$`)
	buildInputPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]*(?:_[A-Z0-9]+)*$`)
	versionPattern    = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+(?:[-+][0-9A-Za-z.-]+)?$`)
	digestPattern     = regexp.MustCompile(`^[a-f0-9]{64}$`)
	configIDPattern   = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	ociPullPattern    = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?::[0-9]+)?(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*@sha256:[a-f0-9]{64}$`)
)

type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
}

type FileBinding struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type DirectoryBinding struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type PolicyBinding struct {
	ID string `json:"id"`
	FileBinding
}

type RoleBinding struct {
	Role        string        `json:"role"`
	ArtifactID  string        `json:"artifact_id"`
	AliasOfRole string        `json:"alias_of_role,omitempty"`
	Policy      PolicyBinding `json:"policy"`
}

type PackageIdentity struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	NPMIntegrity string `json:"npm_integrity"`
}

type NodeIdentity struct {
	Version       string `json:"version"`
	Executable    string `json:"executable"`
	VersionOutput string `json:"version_output"`
}

type RuntimeIdentity struct {
	Pi   PackageIdentity `json:"pi"`
	Node NodeIdentity    `json:"node"`
}

type BuildInput struct {
	Name             string `json:"name"`
	OCIPullReference string `json:"oci_pull_reference"`
}

type ArtifactSpec struct {
	ID               string           `json:"id"`
	Recipe           FileBinding      `json:"recipe"`
	Context          DirectoryBinding `json:"context"`
	RecipeProvenance []FileBinding    `json:"recipe_provenance"`
	BuildInputs      []BuildInput     `json:"build_inputs"`
	Runtime          *RuntimeIdentity `json:"runtime,omitempty"`
	Entrypoint       []string         `json:"entrypoint"`
	LicenseInventory FileBinding      `json:"license_inventory"`
	SBOM             FileBinding      `json:"sbom"`
}

type Spec struct {
	SchemaVersion string         `json:"schema_version"`
	ReleaseID     string         `json:"release_id"`
	Platform      Platform       `json:"platform"`
	Roles         []RoleBinding  `json:"roles"`
	Artifacts     []ArtifactSpec `json:"artifacts"`
}

type ArchiveBinding struct {
	Format string `json:"format"`
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type ResolvedArtifact struct {
	ArtifactID               string         `json:"artifact_id"`
	LocalDockerConfigImageID string         `json:"local_docker_config_image_id"`
	Archive                  ArchiveBinding `json:"archive"`
}

type Resolution struct {
	SchemaVersion string             `json:"schema_version"`
	Artifacts     []ResolvedArtifact `json:"artifacts"`
}

type ImageIdentity struct {
	LocalDockerConfigImageID string         `json:"local_docker_config_image_id"`
	Archive                  ArchiveBinding `json:"archive"`
}

type ManifestArtifact struct {
	ArtifactSpec
	Image ImageIdentity `json:"image"`
}

type Manifest struct {
	SchemaVersion string             `json:"schema_version"`
	SpecSHA256    string             `json:"spec_sha256"`
	ReleaseID     string             `json:"release_id"`
	Platform      Platform           `json:"platform"`
	Roles         []RoleBinding      `json:"roles"`
	Artifacts     []ManifestArtifact `json:"artifacts"`
}

func ParseSpec(reader io.Reader) (Spec, error) {
	var spec Spec
	if err := decodeStrict(reader, &spec); err != nil {
		return Spec{}, fmt.Errorf("decode release asset spec: %w", err)
	}
	if err := ValidateSpec(spec); err != nil {
		return Spec{}, err
	}
	return spec, nil
}

func ParseResolution(reader io.Reader) (Resolution, error) {
	var resolution Resolution
	if err := decodeStrict(reader, &resolution); err != nil {
		return Resolution{}, fmt.Errorf("decode release asset resolution: %w", err)
	}
	if err := validateResolutionShape(resolution); err != nil {
		return Resolution{}, err
	}
	return resolution, nil
}

func ParseManifest(reader io.Reader) (Manifest, error) {
	var manifest Manifest
	if err := decodeStrict(reader, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode release asset manifest: %w", err)
	}
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func ValidateSpec(spec Spec) error {
	if spec.SchemaVersion != SpecSchema {
		return fmt.Errorf("release asset spec schema must be %q", SpecSchema)
	}
	if !validIdentifier(spec.ReleaseID) {
		return errors.New("release ID is invalid")
	}
	if spec.Platform.OS != "linux" || spec.Platform.Architecture != "arm64" {
		return errors.New("release platform must be linux/arm64")
	}
	if len(spec.Roles) != len(requiredRoles) {
		return errors.New("release spec must bind exactly four required roles")
	}
	for index, role := range spec.Roles {
		if role.Role != requiredRoles[index] {
			return errors.New("release role order or coverage is invalid")
		}
		if !validIdentifier(role.ArtifactID) || !validIdentifier(role.Policy.ID) || !validFileBinding(role.Policy.FileBinding) {
			return fmt.Errorf("role %q binding is invalid", role.Role)
		}
	}
	if len(spec.Artifacts) == 0 || !sort.SliceIsSorted(spec.Artifacts, func(i, j int) bool { return spec.Artifacts[i].ID < spec.Artifacts[j].ID }) {
		return errors.New("release artifacts must be non-empty and sorted by ID")
	}
	artifacts := make(map[string]ArtifactSpec, len(spec.Artifacts))
	for _, artifact := range spec.Artifacts {
		if _, exists := artifacts[artifact.ID]; exists || !validArtifact(artifact) {
			return fmt.Errorf("artifact %q is invalid or duplicated", artifact.ID)
		}
		artifacts[artifact.ID] = artifact
	}
	if err := validateRoleAliases(spec.Roles, artifacts); err != nil {
		return err
	}
	managed := artifacts[spec.Roles[0].ArtifactID]
	if managed.Runtime == nil || !validRuntime(*managed.Runtime) {
		return errors.New("managed Pi Runtime artifact requires exact Pi/npm and Node identity")
	}
	for _, artifact := range spec.Artifacts {
		if artifact.ID != managed.ID && artifact.Runtime != nil {
			return fmt.Errorf("non-runtime artifact %q must not declare Runtime identity", artifact.ID)
		}
	}
	return nil
}

func ValidateSpecFiles(root string, spec Spec) error {
	if err := ValidateSpec(spec); err != nil {
		return err
	}
	for _, role := range spec.Roles {
		if err := verifyFile(root, role.Policy.FileBinding); err != nil {
			return fmt.Errorf("role %s policy: %w", role.Role, err)
		}
	}
	for _, artifact := range spec.Artifacts {
		bindings := []struct {
			label   string
			binding FileBinding
		}{
			{"recipe", artifact.Recipe},
			{"license inventory", artifact.LicenseInventory},
			{"SBOM", artifact.SBOM},
		}
		for _, bound := range bindings {
			if err := verifyFile(root, bound.binding); err != nil {
				return fmt.Errorf("artifact %s %s: %w", artifact.ID, bound.label, err)
			}
		}
		for _, provenance := range artifact.RecipeProvenance {
			if err := verifyFile(root, provenance); err != nil {
				return fmt.Errorf("artifact %s recipe provenance: %w", artifact.ID, err)
			}
		}
		recipe, err := readBoundFile(root, artifact.Recipe)
		if err != nil {
			return fmt.Errorf("artifact %s recipe: %w", artifact.ID, err)
		}
		if err := validateRecipeContract(artifact, recipe); err != nil {
			return fmt.Errorf("artifact %s recipe: %w", artifact.ID, err)
		}
		actual, err := DirectoryDigest(root, artifact.Context.Path)
		if err != nil {
			return fmt.Errorf("artifact %s context: %w", artifact.ID, err)
		}
		if actual != artifact.Context.SHA256 {
			return fmt.Errorf("artifact %s context digest mismatch", artifact.ID)
		}
	}
	return nil
}

// Generate creates a production manifest from a complete, locally validated
// offline archive resolution. It performs no build, pull, load, or network IO.
func Generate(root string, spec Spec, resolution Resolution) (Manifest, error) {
	return generateFromRoots(root, root, spec, resolution, true)
}

// GenerateFromRoots validates immutable source/build metadata against
// sourceRoot and offline image archives against a distinct releaseRoot.
func GenerateFromRoots(sourceRoot, releaseRoot string, spec Spec, resolution Resolution) (Manifest, error) {
	return generateFromRoots(sourceRoot, releaseRoot, spec, resolution, false)
}

func generateFromRoots(sourceRoot, releaseRoot string, spec Spec, resolution Resolution, allowSameRoot bool) (Manifest, error) {
	if err := validateReleaseRoots(sourceRoot, releaseRoot, allowSameRoot); err != nil {
		return Manifest{}, err
	}
	if err := ValidateSpecFiles(sourceRoot, spec); err != nil {
		return Manifest{}, err
	}
	if err := ValidateResolutionFiles(releaseRoot, spec, resolution); err != nil {
		return Manifest{}, err
	}
	byID := make(map[string]ResolvedArtifact, len(resolution.Artifacts))
	for _, resolved := range resolution.Artifacts {
		byID[resolved.ArtifactID] = resolved
	}
	manifest := Manifest{
		SchemaVersion: ManifestSchema,
		SpecSHA256:    SpecDigest(spec),
		ReleaseID:     spec.ReleaseID,
		Platform:      spec.Platform,
		Roles:         spec.Roles,
		Artifacts:     make([]ManifestArtifact, 0, len(spec.Artifacts)),
	}
	for _, artifact := range spec.Artifacts {
		resolved := byID[artifact.ID]
		manifest.Artifacts = append(manifest.Artifacts, ManifestArtifact{
			ArtifactSpec: artifact,
			Image: ImageIdentity{
				LocalDockerConfigImageID: resolved.LocalDockerConfigImageID,
				Archive:                  resolved.Archive,
			},
		})
	}
	if err := ValidateManifest(manifest); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func ValidateResolutionFiles(root string, spec Spec, resolution Resolution) error {
	if err := validateResolutionShape(resolution); err != nil {
		return err
	}
	if len(resolution.Artifacts) != len(spec.Artifacts) {
		return errors.New("resolution must cover every artifact exactly once")
	}
	archivePaths := make([]string, 0, len(resolution.Artifacts))
	for index, resolved := range resolution.Artifacts {
		if resolved.ArtifactID != spec.Artifacts[index].ID {
			return errors.New("resolution artifacts must match spec artifacts in sorted order")
		}
		if err := validateArchiveAtRoot(root, resolved.Archive, resolved.LocalDockerConfigImageID); err != nil {
			return fmt.Errorf("artifact %s Docker archive: %w", resolved.ArtifactID, err)
		}
		archivePaths = append(archivePaths, resolved.Archive.Path)
	}
	if !sortedUniqueStrings(archivePaths) {
		return errors.New("resolution archive paths must be sorted and unique")
	}
	return nil
}

func ValidateManifest(manifest Manifest) error {
	if manifest.SchemaVersion != ManifestSchema || !digestPattern.MatchString(manifest.SpecSHA256) {
		return errors.New("release asset manifest identity is invalid")
	}
	spec := Spec{SchemaVersion: SpecSchema, ReleaseID: manifest.ReleaseID, Platform: manifest.Platform, Roles: manifest.Roles}
	for _, artifact := range manifest.Artifacts {
		spec.Artifacts = append(spec.Artifacts, artifact.ArtifactSpec)
		if !validImageIdentity(artifact.Image) {
			return fmt.Errorf("artifact %q image identity is invalid", artifact.ID)
		}
	}
	if err := ValidateSpec(spec); err != nil {
		return err
	}
	if SpecDigest(spec) != manifest.SpecSHA256 {
		return errors.New("release asset manifest spec digest mismatch")
	}
	return nil
}

func ValidateManifestFiles(root string, manifest Manifest) error {
	return validateManifestFilesFromRoots(root, root, manifest, true)
}

// ValidateManifestFilesFromRoots verifies source metadata and local archives
// against their explicit, distinct authorities.
func ValidateManifestFilesFromRoots(sourceRoot, releaseRoot string, manifest Manifest) error {
	return validateManifestFilesFromRoots(sourceRoot, releaseRoot, manifest, false)
}

func validateManifestFilesFromRoots(sourceRoot, releaseRoot string, manifest Manifest, allowSameRoot bool) error {
	if err := validateReleaseRoots(sourceRoot, releaseRoot, allowSameRoot); err != nil {
		return err
	}
	if err := ValidateManifest(manifest); err != nil {
		return err
	}
	spec := Spec{SchemaVersion: SpecSchema, ReleaseID: manifest.ReleaseID, Platform: manifest.Platform, Roles: manifest.Roles}
	archivePaths := make([]string, 0, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		spec.Artifacts = append(spec.Artifacts, artifact.ArtifactSpec)
		if err := validateArchiveAtRoot(releaseRoot, artifact.Image.Archive, artifact.Image.LocalDockerConfigImageID); err != nil {
			return fmt.Errorf("artifact %s Docker archive: %w", artifact.ID, err)
		}
		archivePaths = append(archivePaths, artifact.Image.Archive.Path)
	}
	if !sortedUniqueStrings(archivePaths) {
		return errors.New("manifest archive paths must be sorted and unique")
	}
	return ValidateSpecFiles(sourceRoot, spec)
}

func validateReleaseRoots(sourceRoot, releaseRoot string, allowSameRoot bool) error {
	if allowSameRoot {
		return nil
	}
	for label, root := range map[string]string{"source": sourceRoot, "release": releaseRoot} {
		if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return fmt.Errorf("%s root must be a canonical absolute path", label)
		}
		resolved, err := filepath.EvalSymlinks(root)
		if err != nil || resolved != root {
			return fmt.Errorf("%s root must be an existing symlink-free directory", label)
		}
		info, err := os.Lstat(root)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("%s root must be an existing real directory", label)
		}
	}
	sourceInfo, _ := os.Lstat(sourceRoot)
	releaseInfo, _ := os.Lstat(releaseRoot)
	if !allowSameRoot && os.SameFile(sourceInfo, releaseInfo) {
		return errors.New("source root and release root must be distinct directories")
	}
	for _, pair := range [][2]string{{sourceRoot, releaseRoot}, {releaseRoot, sourceRoot}} {
		relative, err := filepath.Rel(pair[0], pair[1])
		if err == nil && relative != "." && !filepath.IsAbs(relative) && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return errors.New("source root and release root must not contain one another")
		}
	}
	return nil
}

// MaterializeManifestSourceClosure copies the exact source-bound files needed
// by LoadActivatedRelease into a new standalone release root. Every target is
// created exclusively; existing files, symlinks, and archive/path collisions
// fail before the first copy.
func MaterializeManifestSourceClosure(sourceRoot, releaseRoot string, manifest Manifest) error {
	if err := ValidateManifestFilesFromRoots(sourceRoot, releaseRoot, manifest); err != nil {
		return err
	}
	targets, err := manifestSourceClosure(sourceRoot, manifest)
	if err != nil {
		return err
	}
	archivePaths := map[string]struct{}{}
	for _, artifact := range manifest.Artifacts {
		archivePaths[artifact.Image.Archive.Path] = struct{}{}
	}
	paths := make([]string, 0, len(targets))
	for relative := range targets {
		if _, collision := archivePaths[relative]; collision {
			return fmt.Errorf("source closure path %q collides with a release archive", relative)
		}
		paths = append(paths, relative)
	}
	sort.Strings(paths)
	for _, relative := range paths {
		if err := ensureReleaseDirectory(releaseRoot, filepath.ToSlash(filepath.Dir(relative)), false); err != nil {
			return err
		}
		target := filepath.Join(releaseRoot, filepath.FromSlash(relative))
		if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return fmt.Errorf("source closure target %q already exists", relative)
			}
			return err
		}
	}
	created := make([]string, 0, len(paths))
	rollback := func() {
		for index := len(created) - 1; index >= 0; index-- {
			_ = os.Remove(created[index])
		}
	}
	for _, relative := range paths {
		target, err := copyReleaseFile(sourceRoot, releaseRoot, relative, targets[relative])
		if err != nil {
			rollback()
			return err
		}
		created = append(created, target)
	}
	if err := ValidateManifestFiles(releaseRoot, manifest); err != nil {
		rollback()
		return fmt.Errorf("validate standalone release closure: %w", err)
	}
	return nil
}

func manifestSourceClosure(sourceRoot string, manifest Manifest) (map[string]fs.FileMode, error) {
	targets := map[string]fs.FileMode{}
	addBinding := func(binding FileBinding) error {
		secured, err := secureBoundPath(sourceRoot, binding.Path, false)
		if err != nil {
			return err
		}
		targets[binding.Path] = secured.finalInfo.Mode().Perm()
		return nil
	}
	for _, role := range manifest.Roles {
		if err := addBinding(role.Policy.FileBinding); err != nil {
			return nil, fmt.Errorf("role %s policy closure: %w", role.Role, err)
		}
	}
	for _, artifact := range manifest.Artifacts {
		for _, binding := range append([]FileBinding{artifact.Recipe, artifact.LicenseInventory, artifact.SBOM}, artifact.RecipeProvenance...) {
			if err := addBinding(binding); err != nil {
				return nil, fmt.Errorf("artifact %s source closure: %w", artifact.ID, err)
			}
		}
		contextRoot, err := secureBoundPath(sourceRoot, artifact.Context.Path, true)
		if err != nil {
			return nil, fmt.Errorf("artifact %s context closure: %w", artifact.ID, err)
		}
		err = filepath.WalkDir(contextRoot.path, func(filePath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if filePath == contextRoot.path || entry.IsDir() {
				return nil
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("source context contains an unsafe entry")
			}
			relative, err := filepath.Rel(sourceRoot, filePath)
			if err != nil {
				return err
			}
			relative = filepath.ToSlash(relative)
			if !validRelativePath(relative) {
				return errors.New("source context path escaped source root")
			}
			targets[relative] = info.Mode().Perm()
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("artifact %s context closure: %w", artifact.ID, err)
		}
		if err := contextRoot.verifyStable(); err != nil {
			return nil, err
		}
	}
	return targets, nil
}

func ensureReleaseDirectory(root, relative string, create bool) error {
	if relative == "." || relative == "" {
		return nil
	}
	if !validRelativePath(relative) {
		return errors.New("release directory path is invalid")
	}
	current := root
	for _, component := range strings.Split(relative, "/") {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if errors.Is(err, os.ErrNotExist) && create {
			if err := os.Mkdir(current, 0o755); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("release closure path contains a non-directory or symlink ancestor")
		}
	}
	return nil
}

func copyReleaseFile(sourceRoot, releaseRoot, relative string, mode fs.FileMode) (string, error) {
	secured, err := secureBoundPath(sourceRoot, relative, false)
	if err != nil {
		return "", err
	}
	directory := filepath.ToSlash(filepath.Dir(relative))
	if err := ensureReleaseDirectory(releaseRoot, directory, true); err != nil {
		return "", err
	}
	source, err := os.Open(secured.path)
	if err != nil {
		return "", err
	}
	defer source.Close()
	opened, err := source.Stat()
	if err != nil || !os.SameFile(secured.finalInfo, opened) || !opened.Mode().IsRegular() {
		return "", errors.New("source closure file identity changed during open")
	}
	targetPath := filepath.Join(releaseRoot, filepath.FromSlash(relative))
	target, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", err
	}
	completed := false
	defer func() {
		_ = target.Close()
		if !completed {
			_ = os.Remove(targetPath)
		}
	}()
	written, err := io.Copy(target, source)
	if err != nil || written != opened.Size() {
		return "", errors.New("copy source closure file failed or changed size")
	}
	if err := target.Sync(); err != nil {
		return "", err
	}
	if err := target.Chmod(mode.Perm()); err != nil {
		return "", err
	}
	if err := target.Close(); err != nil {
		return "", err
	}
	if err := secured.verifyStable(); err != nil {
		return "", err
	}
	completed = true
	return targetPath, nil
}

func SpecDigest(spec Spec) string {
	encoded, _ := json.Marshal(spec)
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}

func EncodeManifest(manifest Manifest) ([]byte, error) {
	if err := ValidateManifest(manifest); err != nil {
		return nil, err
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

// DirectoryDigest hashes a sorted inventory of regular files. Paths, modes,
// sizes, and file digests are length-delimited; symlinks and special files fail.
func DirectoryDigest(root, relative string) (string, error) {
	secured, err := secureBoundPath(root, relative, true)
	if err != nil {
		return "", err
	}
	directory := secured.path
	type entry struct {
		path, mode, digest string
		size               int64
	}
	var entries []entry
	err = filepath.WalkDir(directory, func(path string, item fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == directory {
			return nil
		}
		info, err := item.Info()
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 || (!item.IsDir() && !info.Mode().IsRegular()) {
			return fmt.Errorf("unsafe context entry: %s", path)
		}
		if item.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(directory, path)
		if err != nil {
			return err
		}
		digest, err := fileDigestMatching(path, info)
		if err != nil {
			return err
		}
		entries = append(entries, entry{filepath.ToSlash(rel), fmt.Sprintf("%04o", info.Mode().Perm()), digest, info.Size()})
		return nil
	})
	if err != nil {
		return "", err
	}
	if err := secured.verifyStable(); err != nil {
		return "", err
	}
	if len(entries) == 0 {
		return "", errors.New("context directory is empty")
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].path < entries[j].path })
	hash := sha256.New()
	for _, entry := range entries {
		fmt.Fprintf(hash, "%d:%s%d:%s%d:%d%d:%s", len(entry.path), entry.path, len(entry.mode), entry.mode, len(fmt.Sprint(entry.size)), entry.size, len(entry.digest), entry.digest)
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func decodeStrict(reader io.Reader, target any) error {
	data, err := io.ReadAll(io.LimitReader(reader, maxDocumentBytes+1))
	if err != nil {
		return err
	}
	if len(data) > maxDocumentBytes {
		return errors.New("JSON document exceeds size limit")
	}
	if err := rejectDuplicateKeys(data); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func rejectDuplicateKeys(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := scanJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}

func scanJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		seen := map[string]struct{}{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("JSON object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return fmt.Errorf("duplicate JSON object key %q", key)
			}
			seen[key] = struct{}{}
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim('}') {
			return errors.New("invalid JSON object termination")
		}
	case '[':
		for decoder.More() {
			if err := scanJSONValue(decoder); err != nil {
				return err
			}
		}
		closing, err := decoder.Token()
		if err != nil || closing != json.Delim(']') {
			return errors.New("invalid JSON array termination")
		}
	default:
		return errors.New("invalid JSON delimiter")
	}
	return nil
}

func validateResolutionShape(resolution Resolution) error {
	if resolution.SchemaVersion != ResolutionSchema || len(resolution.Artifacts) == 0 || !sort.SliceIsSorted(resolution.Artifacts, func(i, j int) bool { return resolution.Artifacts[i].ArtifactID < resolution.Artifacts[j].ArtifactID }) {
		return errors.New("release asset resolution identity or order is invalid")
	}
	seen := map[string]struct{}{}
	for _, artifact := range resolution.Artifacts {
		if _, exists := seen[artifact.ArtifactID]; exists || !validIdentifier(artifact.ArtifactID) || !configIDPattern.MatchString(artifact.LocalDockerConfigImageID) || !validArchiveBinding(artifact.Archive) {
			return fmt.Errorf("resolved artifact %q is invalid", artifact.ArtifactID)
		}
		seen[artifact.ArtifactID] = struct{}{}
	}
	return nil
}

func validImageIdentity(identity ImageIdentity) bool {
	return configIDPattern.MatchString(identity.LocalDockerConfigImageID) && validArchiveBinding(identity.Archive)
}

func validArchiveBinding(binding ArchiveBinding) bool {
	return binding.Format == DockerArchiveFormat && validRelativePath(binding.Path) && binding.Size > 0 && digestPattern.MatchString(binding.SHA256)
}

func sortedUniqueStrings(values []string) bool {
	if len(values) == 0 || !sort.StringsAreSorted(values) {
		return false
	}
	for index := 1; index < len(values); index++ {
		if values[index-1] == values[index] {
			return false
		}
	}
	return true
}

func validateRoleAliases(roles []RoleBinding, artifacts map[string]ArtifactSpec) error {
	byArtifact := map[string][]RoleBinding{}
	for _, role := range roles {
		if _, exists := artifacts[role.ArtifactID]; !exists {
			return fmt.Errorf("role %q references unknown artifact", role.Role)
		}
		byArtifact[role.ArtifactID] = append(byArtifact[role.ArtifactID], role)
	}
	if len(byArtifact) != len(artifacts) {
		return errors.New("release spec contains an unbound artifact")
	}
	for artifactID, bindings := range byArtifact {
		rootRole := ""
		for _, binding := range bindings {
			if binding.AliasOfRole == "" {
				if rootRole != "" {
					return fmt.Errorf("shared artifact %q has multiple primary roles", artifactID)
				}
				rootRole = binding.Role
			}
		}
		if rootRole == "" {
			return fmt.Errorf("artifact %q has no primary role", artifactID)
		}
		for _, binding := range bindings {
			if len(bindings) == 1 && binding.AliasOfRole != "" {
				return fmt.Errorf("role %q declares a needless artifact alias", binding.Role)
			}
			if binding.Role != rootRole && binding.AliasOfRole != rootRole {
				return fmt.Errorf("role %q must explicitly alias primary role %q", binding.Role, rootRole)
			}
		}
	}
	return nil
}

func validArtifact(artifact ArtifactSpec) bool {
	if !validIdentifier(artifact.ID) || !validFileBinding(artifact.Recipe) || !validDirectoryBinding(artifact.Context) || !validFileBinding(artifact.LicenseInventory) || !validFileBinding(artifact.SBOM) || len(artifact.RecipeProvenance) == 0 || !sort.SliceIsSorted(artifact.RecipeProvenance, func(i, j int) bool { return artifact.RecipeProvenance[i].Path < artifact.RecipeProvenance[j].Path }) || len(artifact.BuildInputs) == 0 || !sort.SliceIsSorted(artifact.BuildInputs, func(i, j int) bool { return artifact.BuildInputs[i].Name < artifact.BuildInputs[j].Name }) || len(artifact.Entrypoint) == 0 || len(artifact.Entrypoint) > 32 {
		return false
	}
	for index, provenance := range artifact.RecipeProvenance {
		if !validFileBinding(provenance) || index > 0 && artifact.RecipeProvenance[index-1].Path == provenance.Path {
			return false
		}
	}
	for index, input := range artifact.BuildInputs {
		if !buildInputPattern.MatchString(input.Name) || !ociPullPattern.MatchString(input.OCIPullReference) || index > 0 && artifact.BuildInputs[index-1].Name == input.Name {
			return false
		}
	}
	for _, argument := range artifact.Entrypoint {
		if !validText(argument) || strings.ContainsAny(argument, "\r\n\x00") {
			return false
		}
	}
	return true
}

func validateRecipeContract(artifact ArtifactSpec, recipe []byte) error {
	text := string(recipe)
	for _, input := range artifact.BuildInputs {
		if err := requireDockerfileARG(text, input.Name, input.OCIPullReference); err != nil {
			return err
		}
		if err := requireDockerfileFROM(text, input.Name); err != nil {
			return err
		}
	}
	entrypoint, err := json.Marshal(artifact.Entrypoint)
	if err != nil {
		return err
	}
	if !hasExactDockerfileLine(text, "ENTRYPOINT "+string(entrypoint)) {
		return errors.New("configured entrypoint is not exactly bound in recipe")
	}
	if artifact.Runtime != nil {
		for _, expected := range []struct{ name, value string }{
			{"PI_PACKAGE", artifact.Runtime.Pi.Name},
			{"PI_VERSION", artifact.Runtime.Pi.Version},
			{"PI_INTEGRITY", artifact.Runtime.Pi.NPMIntegrity},
		} {
			if err := requireDockerfileARG(text, expected.name, expected.value); err != nil {
				return err
			}
		}
		expectedNodeCheck := `test "$(node --version)" = "` + artifact.Runtime.Node.VersionOutput + `";`
		if !strings.Contains(text, expectedNodeCheck) {
			return errors.New("Node version identity is not exactly enforced by recipe")
		}
	}
	return nil
}

func requireDockerfileARG(recipe, name, value string) error {
	expected := "ARG " + name + "=" + value
	matches := 0
	for _, line := range strings.Split(recipe, "\n") {
		line = strings.TrimSpace(line)
		prefix := "ARG " + name
		if line == prefix || strings.HasPrefix(line, prefix+"=") || strings.HasPrefix(line, prefix+" ") {
			if line != expected {
				return fmt.Errorf("build input %s is not pinned to its contracted OCI pull reference", name)
			}
			matches++
		}
	}
	if matches != 1 {
		return fmt.Errorf("build input %s must appear exactly once in recipe", name)
	}
	return nil
}

func requireDockerfileFROM(recipe, name string) error {
	expected := "FROM ${" + name + "}"
	matches := 0
	for _, line := range strings.Split(recipe, "\n") {
		line = strings.TrimSpace(line)
		if line == expected || strings.HasPrefix(line, expected+" ") {
			matches++
		}
	}
	if matches != 1 {
		return fmt.Errorf("build input %s must be consumed by exactly one FROM instruction", name)
	}
	return nil
}

func hasExactDockerfileLine(recipe, expected string) bool {
	for _, line := range strings.Split(recipe, "\n") {
		if strings.TrimSpace(line) == expected {
			return true
		}
	}
	return false
}

func validRuntime(runtime RuntimeIdentity) bool {
	if !strings.HasPrefix(runtime.Pi.Name, "@") || !strings.Contains(runtime.Pi.Name, "/") || !versionPattern.MatchString(runtime.Pi.Version) || !validNPMIntegrity(runtime.Pi.NPMIntegrity) || !versionPattern.MatchString(runtime.Node.Version) || !filepath.IsAbs(runtime.Node.Executable) {
		return false
	}
	return runtime.Node.VersionOutput == "v"+runtime.Node.Version
}

func validNPMIntegrity(value string) bool {
	if !strings.HasPrefix(value, "sha512-") {
		return false
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(strings.TrimPrefix(value, "sha512-"))
	return err == nil && len(decoded) == sha512Bytes
}

const sha512Bytes = 64

func validIdentifier(value string) bool {
	return len(value) <= 128 && identifierPattern.MatchString(value)
}

func validText(value string) bool {
	return value != "" && len(value) <= 1024 && strings.TrimSpace(value) == value
}

func validFileBinding(binding FileBinding) bool {
	return validRelativePath(binding.Path) && digestPattern.MatchString(binding.SHA256)
}

func validDirectoryBinding(binding DirectoryBinding) bool {
	return validRelativePath(binding.Path) && digestPattern.MatchString(binding.SHA256)
}

func validRelativePath(path string) bool {
	return path != "" && len(path) <= 512 && !strings.Contains(path, "\\") && !filepath.IsAbs(path) && filepath.Clean(path) == path && filepath.ToSlash(path) == path && path != "." && !strings.HasPrefix(path, "../")
}

func verifyFile(root string, binding FileBinding) error {
	_, err := readBoundFile(root, binding)
	return err
}

func readBoundFile(root string, binding FileBinding) ([]byte, error) {
	secured, err := secureBoundPath(root, binding.Path, false)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(secured.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(secured.finalInfo, openedInfo) {
		return nil, errors.New("bound file identity changed during open")
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != binding.SHA256 {
		return nil, errors.New("bound file digest mismatch")
	}
	if err := secured.verifyStable(); err != nil {
		return nil, err
	}
	return data, nil
}

type securedPath struct {
	root      string
	path      string
	rootInfo  fs.FileInfo
	finalInfo fs.FileInfo
}

func secureBoundPath(root, relative string, wantDirectory bool) (securedPath, error) {
	if !validRelativePath(relative) {
		return securedPath{}, errors.New("bound path is invalid")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return securedPath{}, err
	}
	absoluteRoot = filepath.Clean(absoluteRoot)
	rootInfo, err := os.Lstat(absoluteRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return securedPath{}, errors.New("bound root is unavailable, not a directory, or a symlink")
	}
	current := absoluteRoot
	components := strings.Split(relative, "/")
	var finalInfo fs.FileInfo
	for index, component := range components {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return securedPath{}, errors.New("bound path component is unavailable")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return securedPath{}, errors.New("bound path contains a symlink component")
		}
		if index < len(components)-1 && !info.IsDir() {
			return securedPath{}, errors.New("bound path ancestor is not a directory")
		}
		finalInfo = info
	}
	if wantDirectory && !finalInfo.IsDir() {
		return securedPath{}, errors.New("context directory is unavailable")
	}
	if !wantDirectory && !finalInfo.Mode().IsRegular() {
		return securedPath{}, errors.New("bound file is unavailable or unsafe")
	}
	secured := securedPath{root: absoluteRoot, path: current, rootInfo: rootInfo, finalInfo: finalInfo}
	if err := secured.verifyRootStable(); err != nil {
		return securedPath{}, err
	}
	return secured, nil
}

func (secured securedPath) verifyRootStable() error {
	current, err := os.Lstat(secured.root)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(secured.rootInfo, current) {
		return errors.New("bound root identity drifted")
	}
	return nil
}

func (secured securedPath) verifyStable() error {
	if err := secured.verifyRootStable(); err != nil {
		return err
	}
	current, err := os.Lstat(secured.path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !os.SameFile(secured.finalInfo, current) {
		return errors.New("bound path identity drifted")
	}
	return nil
}

func fileDigestMatching(path string, expected fs.FileInfo) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(expected, opened) || !opened.Mode().IsRegular() {
		return "", errors.New("context file identity changed during open")
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
