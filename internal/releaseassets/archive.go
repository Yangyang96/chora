package releaseassets

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
)

const maxArchiveMetadataBytes = 4 << 20

const (
	ociImageIndexMediaType    = "application/vnd.oci.image.index.v1+json"
	ociImageManifestMediaType = "application/vnd.oci.image.manifest.v1+json"
	ociImageConfigMediaType   = "application/vnd.oci.image.config.v1+json"
	ociImageLayoutVersion     = "1.0.0"
)

type dockerArchiveManifestEntry struct {
	Config   string   `json:"Config"`
	RepoTags []string `json:"RepoTags"`
	Layers   []string `json:"Layers"`
}

type dockerImageConfig struct {
	Architecture string `json:"architecture"`
	OS           string `json:"os"`
	RootFS       struct {
		Type    string   `json:"type"`
		DiffIDs []string `json:"diff_ids"`
	} `json:"rootfs"`
}

type archiveRegularMember struct {
	Size   int64
	SHA256 string
	Data   []byte
}

type ociDescriptor struct {
	MediaType string    `json:"mediaType"`
	Digest    string    `json:"digest"`
	Size      int64     `json:"size"`
	Platform  *Platform `json:"platform,omitempty"`
}

type ociImageIndex struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Manifests     []ociDescriptor `json:"manifests"`
}

type ociImageManifest struct {
	SchemaVersion int             `json:"schemaVersion"`
	MediaType     string          `json:"mediaType"`
	Config        ociDescriptor   `json:"config"`
	Layers        []ociDescriptor `json:"layers"`
}

type ociLayout struct {
	ImageLayoutVersion string `json:"imageLayoutVersion"`
}

// OpenValidatedDockerArchive securely opens and validates the exact activated
// archive, rewinds that same descriptor, and returns it to the caller. The
// minimal ActivatedImage archive projection is sufficient; role and policy
// metadata are deliberately not required by this byte-identity boundary.
func OpenValidatedDockerArchive(image ActivatedImage) (*os.File, error) {
	if image.ArchiveFormat != DockerArchiveFormat || image.ArchiveSize <= 0 ||
		!digestPattern.MatchString(image.ArchiveSHA256) ||
		!configIDPattern.MatchString(image.LocalDockerConfigImageID) ||
		image.ArchivePath == "" || !filepath.IsAbs(image.ArchivePath) || filepath.Clean(image.ArchivePath) != image.ArchivePath {
		return nil, errors.New("activated Docker archive identity is invalid")
	}
	secured, err := secureAbsoluteArchivePath(image.ArchivePath)
	if err != nil {
		return nil, err
	}
	file, err := openAndValidateArchive(secured, image.ArchiveSize, image.ArchiveSHA256, image.LocalDockerConfigImageID)
	if err != nil {
		return nil, err
	}
	return file, nil
}

func validateArchiveAtRoot(root string, binding ArchiveBinding, expectedConfigID string) error {
	if !validArchiveBinding(binding) || !configIDPattern.MatchString(expectedConfigID) {
		return errors.New("Docker archive binding is invalid")
	}
	secured, err := secureBoundPath(root, binding.Path, false)
	if err != nil {
		return err
	}
	file, err := openAndValidateArchive(secured, binding.Size, binding.SHA256, expectedConfigID)
	if err != nil {
		return err
	}
	return file.Close()
}

func openAndValidateArchive(secured securedPath, expectedSize int64, expectedSHA256, expectedConfigID string) (*os.File, error) {
	if !ownerControlledSingleLinkRegularFile(secured.finalInfo) {
		return nil, errors.New("Docker archive must be an owner-controlled single-link regular file")
	}
	if secured.finalInfo.Size() != expectedSize {
		return nil, errors.New("Docker archive size mismatch")
	}
	file, err := os.Open(secured.path)
	if err != nil {
		return nil, fmt.Errorf("open Docker archive: %w", err)
	}
	fail := func(err error) (*os.File, error) {
		_ = file.Close()
		return nil, err
	}
	openedInfo, err := file.Stat()
	if err != nil || !os.SameFile(secured.finalInfo, openedInfo) || !ownerControlledSingleLinkRegularFile(openedInfo) {
		return fail(errors.New("Docker archive identity changed during open"))
	}
	hash := sha256.New()
	written, err := io.Copy(hash, file)
	if err != nil {
		return fail(fmt.Errorf("hash Docker archive: %w", err))
	}
	if written != expectedSize || hex.EncodeToString(hash.Sum(nil)) != expectedSHA256 {
		return fail(errors.New("Docker archive byte identity mismatch"))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(fmt.Errorf("rewind Docker archive: %w", err))
	}
	if err := validateDockerArchiveContents(file, expectedConfigID); err != nil {
		return fail(err)
	}
	if err := secured.verifyStable(); err != nil {
		return fail(err)
	}
	currentInfo, err := file.Stat()
	if err != nil || !os.SameFile(openedInfo, currentInfo) || !ownerControlledSingleLinkRegularFile(currentInfo) || currentInfo.Size() != expectedSize {
		return fail(errors.New("Docker archive identity, link count, ownership, or size drifted during validation"))
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fail(fmt.Errorf("rewind validated Docker archive: %w", err))
	}
	return file, nil
}

func validateDockerArchiveContents(reader io.Reader, expectedConfigID string) error {
	tape := tar.NewReader(reader)
	members := map[string]archiveRegularMember{}
	allNames := map[string]struct{}{}
	for {
		header, err := tape.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read Docker archive: %w", err)
		}
		name := header.Name
		isDirectory := header.Typeflag == tar.TypeDir
		if isDirectory && !validArchiveDirectoryPath(name) || !isDirectory && !validArchiveMemberPath(name) {
			return fmt.Errorf("Docker archive member path %q is unsafe", name)
		}
		if _, exists := allNames[name]; exists {
			return fmt.Errorf("Docker archive member %q is duplicated", name)
		}
		allNames[name] = struct{}{}
		switch header.Typeflag {
		case tar.TypeDir:
			continue
		case tar.TypeReg, tar.TypeRegA:
		default:
			return fmt.Errorf("Docker archive member %q is not a regular file or directory", name)
		}
		if header.Size < 0 {
			return fmt.Errorf("Docker archive member %q has an invalid size", name)
		}
		hash := sha256.New()
		var data []byte
		capture := header.Size <= maxArchiveMetadataBytes &&
			(name == "manifest.json" || name == "index.json" || name == "oci-layout" ||
				strings.HasSuffix(name, ".json") || strings.HasPrefix(name, "blobs/sha256/"))
		var written int64
		if capture {
			var buffer strings.Builder
			buffer.Grow(int(header.Size))
			written, err = io.Copy(io.MultiWriter(hash, &buffer), tape)
			data = []byte(buffer.String())
		} else {
			written, err = io.Copy(hash, tape)
		}
		if err != nil || written != header.Size {
			return fmt.Errorf("read Docker archive member %q", name)
		}
		members[name] = archiveRegularMember{Size: header.Size, SHA256: hex.EncodeToString(hash.Sum(nil)), Data: data}
	}

	manifestMember, ok := members["manifest.json"]
	if !ok {
		return errors.New("Docker archive must contain manifest.json exactly once")
	}
	manifestRaw := manifestMember.Data
	if manifestRaw == nil {
		return errors.New("Docker archive manifest exceeds metadata size limit")
	}
	if err := rejectDuplicateKeys(manifestRaw); err != nil {
		return fmt.Errorf("Docker archive manifest: %w", err)
	}
	var manifest []dockerArchiveManifestEntry
	if err := json.Unmarshal(manifestRaw, &manifest); err != nil || len(manifest) != 1 {
		return errors.New("Docker archive must describe exactly one image")
	}
	entry := manifest[0]
	if len(entry.RepoTags) != 0 {
		return errors.New("Docker archive image must be tagless")
	}
	if !validArchiveMemberPath(entry.Config) || len(entry.Layers) == 0 {
		return errors.New("Docker archive config identity or layer closure is invalid")
	}
	if strings.HasPrefix(entry.Config, "blobs/sha256/") {
		return validateContainerdDockerArchive(members, entry, expectedConfigID)
	}
	return validateClassicDockerArchive(members, entry, expectedConfigID)
}

func validateClassicDockerArchive(members map[string]archiveRegularMember, entry dockerArchiveManifestEntry, expectedConfigID string) error {
	expectedConfigName := strings.TrimPrefix(expectedConfigID, "sha256:") + ".json"
	if entry.Config != expectedConfigName {
		return errors.New("Docker archive config identity or layer closure is invalid")
	}
	configMember, ok := members[entry.Config]
	if !ok {
		return errors.New("Docker archive expected config member is missing")
	}
	if "sha256:"+configMember.SHA256 != expectedConfigID || configMember.Data == nil {
		return errors.New("Docker archive config bytes do not match expected Docker config image ID")
	}
	configRaw := configMember.Data
	if err := rejectDuplicateKeys(configRaw); err != nil {
		return fmt.Errorf("Docker archive image config: %w", err)
	}
	var config dockerImageConfig
	if err := json.Unmarshal(configRaw, &config); err != nil {
		return errors.New("Docker archive image config is malformed")
	}
	if config.OS != "linux" || config.Architecture != "arm64" {
		return errors.New("Docker archive image config platform must be linux/arm64")
	}
	if config.RootFS.Type != "layers" || len(config.RootFS.DiffIDs) != len(entry.Layers) {
		return errors.New("Docker archive config and layer closure do not match")
	}
	expectedMembers := []string{"manifest.json", entry.Config}
	seenLayers := map[string]struct{}{}
	for index, layer := range entry.Layers {
		if !validArchiveMemberPath(layer) || layer == "manifest.json" || layer == entry.Config {
			return errors.New("Docker archive layer member path is invalid")
		}
		if _, exists := seenLayers[layer]; exists {
			return errors.New("Docker archive layer member is duplicated")
		}
		seenLayers[layer] = struct{}{}
		if _, exists := members[layer]; !exists {
			return fmt.Errorf("Docker archive layer member %q is missing", layer)
		}
		if !configIDPattern.MatchString(config.RootFS.DiffIDs[index]) {
			return errors.New("Docker archive config layer digest is invalid")
		}
		expectedMembers = append(expectedMembers, layer)
	}
	return requireExactRegularClosure(members, expectedMembers)
}

func validateContainerdDockerArchive(members map[string]archiveRegularMember, entry dockerArchiveManifestEntry, expectedImageID string) error {
	for _, required := range []string{"index.json", "oci-layout"} {
		if member, ok := members[required]; !ok || member.Data == nil {
			return fmt.Errorf("containerd Docker archive is missing bounded %s", required)
		}
	}
	var layout ociLayout
	if err := decodeArchiveJSON(members["oci-layout"].Data, &layout); err != nil || layout.ImageLayoutVersion != ociImageLayoutVersion {
		return errors.New("containerd Docker archive OCI layout is invalid")
	}
	var index ociImageIndex
	if err := decodeArchiveJSON(members["index.json"].Data, &index); err != nil ||
		index.SchemaVersion != 2 || index.MediaType != ociImageIndexMediaType || len(index.Manifests) != 1 {
		return errors.New("containerd Docker archive index must describe exactly one OCI image")
	}
	manifestDescriptor := index.Manifests[0]
	if manifestDescriptor.MediaType != ociImageManifestMediaType || manifestDescriptor.Digest != expectedImageID ||
		manifestDescriptor.Size <= 0 || manifestDescriptor.Platform != nil &&
		(manifestDescriptor.Platform.OS != "linux" || manifestDescriptor.Platform.Architecture != "arm64") {
		return errors.New("containerd Docker archive index image identity, media type, or platform is invalid")
	}
	manifestPath, err := ociBlobPath(manifestDescriptor.Digest)
	if err != nil || manifestPath == entry.Config {
		return errors.New("containerd Docker archive image manifest descriptor is invalid")
	}
	manifestMember, err := requireDescriptorMember(members, manifestDescriptor, manifestPath)
	if err != nil || manifestMember.Data == nil {
		return errors.New("containerd Docker archive image manifest bytes do not match its descriptor")
	}
	var imageManifest ociImageManifest
	if err := decodeArchiveJSON(manifestMember.Data, &imageManifest); err != nil || imageManifest.SchemaVersion != 2 ||
		imageManifest.MediaType != ociImageManifestMediaType || imageManifest.Config.MediaType != ociImageConfigMediaType ||
		len(imageManifest.Layers) == 0 {
		return errors.New("containerd Docker archive OCI image manifest is invalid")
	}
	configPath, err := ociBlobPath(imageManifest.Config.Digest)
	if err != nil || configPath != entry.Config || imageManifest.Config.Size <= 0 {
		return errors.New("containerd Docker archive config descriptor does not match manifest.json")
	}
	configMember, err := requireDescriptorMember(members, imageManifest.Config, configPath)
	if err != nil || configMember.Data == nil {
		return errors.New("containerd Docker archive config bytes do not match its descriptor")
	}
	var config dockerImageConfig
	if err := decodeArchiveJSON(configMember.Data, &config); err != nil || config.OS != "linux" || config.Architecture != "arm64" {
		return errors.New("containerd Docker archive image config platform must be linux/arm64")
	}
	if config.RootFS.Type != "layers" || len(config.RootFS.DiffIDs) != len(imageManifest.Layers) || len(entry.Layers) != len(imageManifest.Layers) {
		return errors.New("containerd Docker archive config and layer closure do not match")
	}
	expectedMembers := []string{"manifest.json", "index.json", "oci-layout", manifestPath, configPath}
	seenLayers := map[string]struct{}{}
	for index, descriptor := range imageManifest.Layers {
		if !validOCILayerMediaType(descriptor.MediaType) || descriptor.Size <= 0 || !configIDPattern.MatchString(config.RootFS.DiffIDs[index]) {
			return errors.New("containerd Docker archive layer descriptor is invalid")
		}
		layerPath, err := ociBlobPath(descriptor.Digest)
		if err != nil || layerPath != entry.Layers[index] || layerPath == configPath || layerPath == manifestPath {
			return errors.New("containerd Docker archive layer path does not match the OCI manifest")
		}
		if _, exists := seenLayers[layerPath]; exists {
			return errors.New("containerd Docker archive layer descriptor is duplicated")
		}
		seenLayers[layerPath] = struct{}{}
		if _, err := requireDescriptorMember(members, descriptor, layerPath); err != nil {
			return fmt.Errorf("containerd Docker archive layer bytes: %w", err)
		}
		expectedMembers = append(expectedMembers, layerPath)
	}
	return requireExactRegularClosure(members, expectedMembers)
}

func decodeArchiveJSON(raw []byte, target any) error {
	if raw == nil || len(raw) > maxArchiveMetadataBytes {
		return errors.New("archive JSON metadata exceeds size limit")
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return err
	}
	return json.Unmarshal(raw, target)
}

func ociBlobPath(digest string) (string, error) {
	if !configIDPattern.MatchString(digest) {
		return "", errors.New("OCI descriptor digest is invalid")
	}
	return "blobs/sha256/" + strings.TrimPrefix(digest, "sha256:"), nil
}

func requireDescriptorMember(members map[string]archiveRegularMember, descriptor ociDescriptor, expectedPath string) (archiveRegularMember, error) {
	member, ok := members[expectedPath]
	if !ok || descriptor.Size != member.Size || strings.TrimPrefix(descriptor.Digest, "sha256:") != member.SHA256 {
		return archiveRegularMember{}, errors.New("OCI descriptor digest or size mismatch")
	}
	return member, nil
}

func validOCILayerMediaType(value string) bool {
	switch value {
	case "application/vnd.oci.image.layer.v1.tar",
		"application/vnd.oci.image.layer.v1.tar+gzip",
		"application/vnd.oci.image.layer.v1.tar+zstd",
		"application/vnd.oci.image.layer.nondistributable.v1.tar",
		"application/vnd.oci.image.layer.nondistributable.v1.tar+gzip",
		"application/vnd.oci.image.layer.nondistributable.v1.tar+zstd",
		"application/vnd.docker.image.rootfs.diff.tar",
		"application/vnd.docker.image.rootfs.diff.tar.gzip",
		"application/vnd.docker.image.rootfs.foreign.diff.tar",
		"application/vnd.docker.image.rootfs.foreign.diff.tar.gzip":
		return true
	default:
		return false
	}
}

func requireExactRegularClosure(members map[string]archiveRegularMember, expectedMembers []string) error {
	sort.Strings(expectedMembers)
	regularInArchive := make([]string, 0, len(members))
	for name := range members {
		regularInArchive = append(regularInArchive, name)
	}
	sort.Strings(regularInArchive)
	if !equalStrings(regularInArchive, expectedMembers) {
		return errors.New("Docker archive contains an extra or incomplete image closure")
	}
	return nil
}

func validArchiveMemberPath(name string) bool {
	return name != "" && len(name) <= 1024 && !strings.Contains(name, "\\") &&
		!strings.HasPrefix(name, "/") && path.Clean(name) == name && name != "." &&
		name != ".." && !strings.HasPrefix(name, "../")
}

func validArchiveDirectoryPath(name string) bool {
	return strings.HasSuffix(name, "/") && !strings.HasSuffix(name, "//") &&
		validArchiveMemberPath(strings.TrimSuffix(name, "/"))
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func ownerControlledSingleLinkRegularFile(info os.FileInfo) bool {
	if !singleLinkRegularFile(info) || info.Mode().Perm()&0o022 != 0 {
		return false
	}
	metadata := reflect.ValueOf(info.Sys())
	if metadata.Kind() == reflect.Pointer {
		if metadata.IsNil() {
			return false
		}
		metadata = metadata.Elem()
	}
	if metadata.Kind() != reflect.Struct {
		return false
	}
	uid := metadata.FieldByName("Uid")
	if !uid.IsValid() {
		return false
	}
	switch uid.Kind() {
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		return uid.Uint() == uint64(os.Geteuid())
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return uid.Int() == int64(os.Geteuid())
	default:
		return false
	}
}

func secureAbsoluteArchivePath(absolute string) (securedPath, error) {
	if absolute == "" || !filepath.IsAbs(absolute) || filepath.Clean(absolute) != absolute {
		return securedPath{}, errors.New("Docker archive path must be canonical and absolute")
	}
	volumeRoot := filepath.VolumeName(absolute) + string(os.PathSeparator)
	relative, err := filepath.Rel(volumeRoot, absolute)
	if err != nil || relative == "." || strings.HasPrefix(relative, "..") {
		return securedPath{}, errors.New("Docker archive path is invalid")
	}
	rootInfo, err := os.Lstat(volumeRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return securedPath{}, errors.New("Docker archive filesystem root is unavailable")
	}
	current := volumeRoot
	components := strings.Split(relative, string(os.PathSeparator))
	var finalInfo os.FileInfo
	for index, component := range components {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err != nil {
			return securedPath{}, errors.New("Docker archive path component is unavailable")
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return securedPath{}, errors.New("Docker archive path contains a symlink component")
		}
		if index < len(components)-1 && !info.IsDir() {
			return securedPath{}, errors.New("Docker archive path ancestor is not a directory")
		}
		finalInfo = info
	}
	if finalInfo == nil || !finalInfo.Mode().IsRegular() {
		return securedPath{}, errors.New("Docker archive file is unavailable or unsafe")
	}
	return securedPath{root: volumeRoot, path: current, rootInfo: rootInfo, finalInfo: finalInfo}, nil
}
