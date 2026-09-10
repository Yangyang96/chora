package releaseassets

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
)

// ActivationInput identifies one installed, content-pinned release manifest.
// Root and ManifestPath are deliberately separate so the manifest and every
// file it binds are constrained to the same installed release tree.
type ActivationInput struct {
	Root           string
	ManifestPath   string
	ManifestSHA256 string
	ReleaseID      string
}

// ActivatedImage is the complete immutable identity selected for one role.
// It contains values only; callers cannot mutate the activated release by
// changing a returned image.
type ActivatedImage struct {
	Role                     string
	ArtifactID               string
	LocalDockerConfigImageID string
	ArchiveFormat            string
	ArchivePath              string
	ArchiveSize              int64
	ArchiveSHA256            string
	PolicyID                 string
	PolicyPath               string
	PolicySHA256             string
	ReleaseID                string
	SpecSHA256               string
	Platform                 Platform
	RuntimePiName            string
	RuntimePiVersion         string
	RuntimePiNPMIntegrity    string
	RuntimeNodeVersion       string
	RuntimeNodeExecutable    string
	RuntimeNodeVersionOutput string
	EntrypointJSON           string
}

// ActivatedRelease is a validated value projection of an installed manifest.
// Its fixed, private storage prevents aliasing mutable manifest slices.
type ActivatedRelease struct {
	images [4]ActivatedImage
}

// LoadActivatedRelease validates and projects a content-pinned installed
// release without performing network access or changing the installation.
func LoadActivatedRelease(input ActivationInput) (ActivatedRelease, error) {
	if err := validateActivationInput(input); err != nil {
		return ActivatedRelease{}, err
	}

	raw, err := readPinnedManifest(input.Root, input.ManifestPath, input.ManifestSHA256)
	if err != nil {
		return ActivatedRelease{}, fmt.Errorf("load activated release manifest: %w", err)
	}
	manifest, err := ParseManifest(bytes.NewReader(raw))
	if err != nil {
		return ActivatedRelease{}, fmt.Errorf("parse activated release manifest: %w", err)
	}
	if manifest.ReleaseID != input.ReleaseID {
		return ActivatedRelease{}, errors.New("activated release ID does not match requested release")
	}
	if manifest.Platform.OS != "linux" || manifest.Platform.Architecture != "arm64" {
		return ActivatedRelease{}, errors.New("activated release platform must be linux/arm64")
	}
	if err := ValidateManifestFiles(input.Root, manifest); err != nil {
		return ActivatedRelease{}, fmt.Errorf("validate activated release files: %w", err)
	}

	activated, err := projectActivatedRelease(input.Root, manifest)
	if err != nil {
		return ActivatedRelease{}, err
	}
	return activated, nil
}

// ImageForRole returns the exact artifact explicitly bound to role by the
// activated manifest. It never infers or substitutes an alias.
func (release ActivatedRelease) ImageForRole(role string) (ActivatedImage, error) {
	index, ok := activationRoleIndex(role)
	if !ok {
		return ActivatedImage{}, fmt.Errorf("unknown activated release role %q", role)
	}
	image := release.images[index]
	if image.Role != role {
		return ActivatedImage{}, fmt.Errorf("activated release has no image for role %q", role)
	}
	return image, nil
}

// ReadPolicyForRole reads only the exact policy file already bound to role by
// this validated ActivatedRelease. The caller supplies the explicit activated
// release root; source repositories and other ambient trees are never searched
// or used as fallback authority.
func (release ActivatedRelease) ReadPolicyForRole(root, role string) ([]byte, error) {
	if err := validateActivationRoot(root); err != nil {
		return nil, err
	}
	image, err := release.ImageForRole(role)
	if err != nil {
		return nil, err
	}
	secured, err := secureBoundPath(root, image.PolicyPath, false)
	if err != nil {
		return nil, fmt.Errorf("secure activated role policy: %w", err)
	}
	if !singleLinkRegularFile(secured.finalInfo) {
		return nil, errors.New("activated role policy must be a single-link regular file")
	}
	file, err := os.Open(secured.path)
	if err != nil {
		return nil, fmt.Errorf("open activated role policy: %w", err)
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(secured.finalInfo, openedInfo) || !singleLinkRegularFile(openedInfo) {
		return nil, errors.New("activated role policy identity changed during open")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read activated role policy: %w", err)
	}
	if len(raw) > maxDocumentBytes {
		return nil, errors.New("activated role policy exceeds size limit")
	}
	if err := secured.verifyStable(); err != nil {
		return nil, err
	}
	current, err := os.Lstat(secured.path)
	if err != nil || !os.SameFile(secured.finalInfo, current) || !singleLinkRegularFile(current) {
		return nil, errors.New("activated role policy identity or link count drifted")
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != image.PolicySHA256 {
		return nil, errors.New("activated role policy SHA-256 mismatch")
	}
	return bytes.Clone(raw), nil
}

func singleLinkRegularFile(info os.FileInfo) bool {
	if info == nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	metadata := reflect.ValueOf(info.Sys())
	if !metadata.IsValid() {
		return false
	}
	if metadata.Kind() == reflect.Pointer {
		if metadata.IsNil() {
			return false
		}
		metadata = metadata.Elem()
	}
	if metadata.Kind() != reflect.Struct {
		return false
	}
	links := metadata.FieldByName("Nlink")
	if !links.IsValid() {
		return false
	}
	switch links.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return links.Uint() == 1
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return links.Int() == 1
	default:
		return false
	}
}

func validateActivationInput(input ActivationInput) error {
	if err := validateActivationRoot(input.Root); err != nil {
		return err
	}
	if !validRelativePath(input.ManifestPath) {
		return errors.New("activation manifest path must be canonical and root-relative")
	}
	if !digestPattern.MatchString(input.ManifestSHA256) {
		return errors.New("activation manifest SHA-256 must be exactly 64 lowercase hexadecimal characters")
	}
	if input.ReleaseID == "" {
		return errors.New("activation release ID must be nonempty")
	}
	return nil
}

func validateActivationRoot(root string) error {
	if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return errors.New("activation root must be a canonical absolute path")
	}
	volumeRoot := filepath.VolumeName(root) + string(os.PathSeparator)
	if root == volumeRoot {
		return errors.New("activation root must not be a filesystem root")
	}
	resolvedRoot, err := filepath.EvalSymlinks(root)
	if err != nil || resolvedRoot != root {
		return errors.New("activation root must exist as a canonical symlink-free path")
	}
	info, err := os.Lstat(root)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("activation root must be an available real directory")
	}
	return nil
}

func readPinnedManifest(root, relative, expectedSHA256 string) ([]byte, error) {
	secured, err := secureBoundPath(root, relative, false)
	if err != nil {
		return nil, err
	}
	file, err := os.Open(secured.path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	openedInfo, err := file.Stat()
	if err != nil || !openedInfo.Mode().IsRegular() || !os.SameFile(secured.finalInfo, openedInfo) {
		return nil, errors.New("activation manifest identity changed during open")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxDocumentBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maxDocumentBytes {
		return nil, errors.New("activation manifest exceeds size limit")
	}
	if err := secured.verifyStable(); err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	if hex.EncodeToString(digest[:]) != expectedSHA256 {
		return nil, errors.New("activation manifest SHA-256 mismatch")
	}
	return raw, nil
}

func projectActivatedRelease(root string, manifest Manifest) (ActivatedRelease, error) {
	if err := ValidateManifest(manifest); err != nil {
		return ActivatedRelease{}, err
	}
	artifacts := make(map[string]ManifestArtifact, len(manifest.Artifacts))
	for _, artifact := range manifest.Artifacts {
		if _, exists := artifacts[artifact.ID]; exists {
			return ActivatedRelease{}, fmt.Errorf("ambiguous activated artifact %q", artifact.ID)
		}
		artifacts[artifact.ID] = artifact
	}

	var release ActivatedRelease
	var assigned [4]bool
	for _, binding := range manifest.Roles {
		index, ok := activationRoleIndex(binding.Role)
		if !ok || assigned[index] {
			return ActivatedRelease{}, fmt.Errorf("unknown or ambiguous activated role %q", binding.Role)
		}
		artifact, ok := artifacts[binding.ArtifactID]
		if !ok {
			return ActivatedRelease{}, fmt.Errorf("activated role %q references unknown artifact", binding.Role)
		}
		entrypoint, err := json.Marshal(artifact.Entrypoint)
		if err != nil {
			return ActivatedRelease{}, fmt.Errorf("activated role %q entrypoint: %w", binding.Role, err)
		}
		archive, err := secureBoundPath(root, artifact.Image.Archive.Path, false)
		if err != nil {
			return ActivatedRelease{}, fmt.Errorf("activated role %q archive: %w", binding.Role, err)
		}
		image := ActivatedImage{
			Role:                     binding.Role,
			ArtifactID:               binding.ArtifactID,
			LocalDockerConfigImageID: artifact.Image.LocalDockerConfigImageID,
			ArchiveFormat:            artifact.Image.Archive.Format,
			ArchivePath:              archive.path,
			ArchiveSize:              artifact.Image.Archive.Size,
			ArchiveSHA256:            artifact.Image.Archive.SHA256,
			PolicyID:                 binding.Policy.ID,
			PolicyPath:               binding.Policy.Path,
			PolicySHA256:             binding.Policy.SHA256,
			ReleaseID:                manifest.ReleaseID,
			SpecSHA256:               manifest.SpecSHA256,
			Platform:                 manifest.Platform,
			EntrypointJSON:           string(entrypoint),
		}
		if artifact.Runtime != nil {
			image.RuntimePiName = artifact.Runtime.Pi.Name
			image.RuntimePiVersion = artifact.Runtime.Pi.Version
			image.RuntimePiNPMIntegrity = artifact.Runtime.Pi.NPMIntegrity
			image.RuntimeNodeVersion = artifact.Runtime.Node.Version
			image.RuntimeNodeExecutable = artifact.Runtime.Node.Executable
			image.RuntimeNodeVersionOutput = artifact.Runtime.Node.VersionOutput
		}
		if err := validateActivatedImage(image); err != nil {
			return ActivatedRelease{}, fmt.Errorf("activated role %q: %w", binding.Role, err)
		}
		release.images[index] = image
		assigned[index] = true
	}
	for index, present := range assigned {
		if !present {
			return ActivatedRelease{}, fmt.Errorf("activated release is missing role %q", requiredRoles[index])
		}
	}
	return release, nil
}

func validateActivatedImage(image ActivatedImage) error {
	if _, ok := activationRoleIndex(image.Role); !ok || !validIdentifier(image.ArtifactID) ||
		!configIDPattern.MatchString(image.LocalDockerConfigImageID) || image.ArchiveFormat != DockerArchiveFormat ||
		image.ArchivePath == "" || !filepath.IsAbs(image.ArchivePath) || filepath.Clean(image.ArchivePath) != image.ArchivePath ||
		image.ArchiveSize <= 0 || !digestPattern.MatchString(image.ArchiveSHA256) ||
		!validIdentifier(image.PolicyID) || !validRelativePath(image.PolicyPath) || !digestPattern.MatchString(image.PolicySHA256) ||
		!validIdentifier(image.ReleaseID) || !digestPattern.MatchString(image.SpecSHA256) ||
		image.Platform.OS != "linux" || image.Platform.Architecture != "arm64" || !validActivatedEntrypoint(image.EntrypointJSON) {
		return errors.New("activated image identity is invalid")
	}
	runtimeDeclared := image.RuntimePiName != "" || image.RuntimePiVersion != "" || image.RuntimePiNPMIntegrity != "" ||
		image.RuntimeNodeVersion != "" || image.RuntimeNodeExecutable != "" || image.RuntimeNodeVersionOutput != ""
	if image.Role == RoleNetworkBoundary {
		if runtimeDeclared {
			return errors.New("network Boundary image must not declare Pi Runtime identity")
		}
		return nil
	}
	if image.RuntimePiName == "" || image.RuntimePiVersion == "" || image.RuntimePiNPMIntegrity == "" ||
		image.RuntimeNodeVersion == "" || !filepath.IsAbs(image.RuntimeNodeExecutable) || image.RuntimeNodeVersionOutput == "" {
		return errors.New("activated managed image identity is incomplete")
	}
	return nil
}

func validActivatedEntrypoint(raw string) bool {
	var entrypoint []string
	if err := json.Unmarshal([]byte(raw), &entrypoint); err != nil || len(entrypoint) == 0 {
		return false
	}
	for _, argument := range entrypoint {
		if argument == "" {
			return false
		}
	}
	canonical, err := json.Marshal(entrypoint)
	return err == nil && string(canonical) == raw
}

func activationRoleIndex(role string) (int, bool) {
	switch role {
	case RoleManagedPiRuntime:
		return 0, true
	case RoleNetworkBoundary:
		return 1, true
	case RoleIndependentVerifier:
		return 2, true
	case RoleCapabilityProbe:
		return 3, true
	default:
		return 0, false
	}
}
