package releaseassets

import (
	"archive/tar"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type archiveFixtureOptions struct {
	os, architecture string
	tags             []string
	secondImage      bool
	duplicateLayer   bool
	omitLayer        bool
	extraMember      bool
	duplicateMember  bool
}

type containerdArchiveFixtureOptions struct {
	os, architecture     string
	tags                 []string
	extraIndexDescriptor bool
	extraMember          bool
	manifestSizeDrift    bool
	layerSizeDrift       bool
	layerByteDrift       bool
	configMediaTypeDrift bool
	layerMediaTypeDrift  bool
}

func TestOpenValidatedDockerArchiveReturnsRewoundExactDescriptor(t *testing.T) {
	image, raw := activatedArchiveFixture(t, archiveFixtureOptions{})
	file, err := OpenValidatedDockerArchive(image)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got := make([]byte, len(raw))
	if _, err := file.Read(got); err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("validated descriptor was not rewound to exact bytes: err=%v", err)
	}
}

func TestDockerArchiveRejectsUnsafeFileIdentityAndByteDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, *ActivatedImage)
	}{
		{name: "size", mutate: func(_ *testing.T, image *ActivatedImage) { image.ArchiveSize++ }},
		{name: "sha256", mutate: func(_ *testing.T, image *ActivatedImage) { image.ArchiveSHA256 = strings.Repeat("f", 64) }},
		{name: "format", mutate: func(_ *testing.T, image *ActivatedImage) { image.ArchiveFormat = "oci-layout" }},
		{name: "config ID", mutate: func(_ *testing.T, image *ActivatedImage) {
			image.LocalDockerConfigImageID = "sha256:" + strings.Repeat("f", 64)
		}},
		{name: "group writable", mutate: func(t *testing.T, image *ActivatedImage) {
			if err := os.Chmod(image.ArchivePath, 0o660); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", mutate: func(t *testing.T, image *ActivatedImage) {
			if err := os.Link(image.ArchivePath, filepath.Join(filepath.Dir(image.ArchivePath), "second-link.tar")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "symlink", mutate: func(t *testing.T, image *ActivatedImage) {
			target := image.ArchivePath
			link := filepath.Join(filepath.Dir(target), "linked.tar")
			if err := os.Symlink(filepath.Base(target), link); err != nil {
				t.Fatal(err)
			}
			image.ArchivePath = link
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			image, _ := activatedArchiveFixture(t, archiveFixtureOptions{})
			test.mutate(t, &image)
			if file, err := OpenValidatedDockerArchive(image); err == nil {
				_ = file.Close()
				t.Fatal("unsafe or drifted archive passed")
			}
		})
	}
}

func TestDockerArchiveRejectsTagPlatformExtraDuplicateAndIncompleteClosure(t *testing.T) {
	for _, test := range []struct {
		name    string
		options archiveFixtureOptions
	}{
		{name: "tagged", options: archiveFixtureOptions{tags: []string{"chora:test"}}},
		{name: "wrong OS", options: archiveFixtureOptions{os: "darwin"}},
		{name: "wrong architecture", options: archiveFixtureOptions{architecture: "amd64"}},
		{name: "second image", options: archiveFixtureOptions{secondImage: true}},
		{name: "duplicate layer reference", options: archiveFixtureOptions{duplicateLayer: true}},
		{name: "missing layer", options: archiveFixtureOptions{omitLayer: true}},
		{name: "extra member", options: archiveFixtureOptions{extraMember: true}},
		{name: "duplicate member", options: archiveFixtureOptions{duplicateMember: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			image, _ := activatedArchiveFixture(t, test.options)
			if file, err := OpenValidatedDockerArchive(image); err == nil {
				_ = file.Close()
				t.Fatal("invalid exact-one archive passed")
			}
		})
	}
}

func TestContainerdDockerArchiveAcceptsExactTaglessOCIClosure(t *testing.T) {
	image, raw := activatedContainerdArchiveFixture(t, containerdArchiveFixtureOptions{})
	file, err := OpenValidatedDockerArchive(image)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	got := make([]byte, len(raw))
	if _, err := file.Read(got); err != nil || !bytes.Equal(got, raw) {
		t.Fatalf("containerd archive descriptor was not rewound to exact bytes: err=%v", err)
	}
}

func TestContainerdDockerArchiveRejectsIdentityDescriptorPlatformTagAndClosureDrift(t *testing.T) {
	for _, test := range []struct {
		name        string
		options     containerdArchiveFixtureOptions
		mutateImage func(*ActivatedImage)
	}{
		{name: "expected image identity", mutateImage: func(image *ActivatedImage) {
			image.LocalDockerConfigImageID = "sha256:" + strings.Repeat("f", 64)
		}},
		{name: "tagged", options: containerdArchiveFixtureOptions{tags: []string{"chora:test"}}},
		{name: "wrong OS", options: containerdArchiveFixtureOptions{os: "darwin"}},
		{name: "wrong architecture", options: containerdArchiveFixtureOptions{architecture: "amd64"}},
		{name: "extra index descriptor", options: containerdArchiveFixtureOptions{extraIndexDescriptor: true}},
		{name: "extra member", options: containerdArchiveFixtureOptions{extraMember: true}},
		{name: "manifest descriptor size", options: containerdArchiveFixtureOptions{manifestSizeDrift: true}},
		{name: "layer descriptor size", options: containerdArchiveFixtureOptions{layerSizeDrift: true}},
		{name: "layer byte digest", options: containerdArchiveFixtureOptions{layerByteDrift: true}},
		{name: "config media type", options: containerdArchiveFixtureOptions{configMediaTypeDrift: true}},
		{name: "layer media type", options: containerdArchiveFixtureOptions{layerMediaTypeDrift: true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			image, _ := activatedContainerdArchiveFixture(t, test.options)
			if test.mutateImage != nil {
				test.mutateImage(&image)
			}
			if file, err := OpenValidatedDockerArchive(image); err == nil {
				_ = file.Close()
				t.Fatal("drifted containerd Docker archive passed")
			}
		})
	}
}

func TestContainerdDockerArchiveAcceptsStandardOCIAndDockerLayerMediaTypes(t *testing.T) {
	for _, mediaType := range []string{
		"application/vnd.oci.image.layer.v1.tar+gzip",
		"application/vnd.docker.image.rootfs.diff.tar.gzip",
	} {
		if !validOCILayerMediaType(mediaType) {
			t.Fatalf("standard layer media type %q was rejected", mediaType)
		}
	}
	if validOCILayerMediaType("application/vnd.in-toto+json") {
		t.Fatal("attestation media type was accepted as a runnable image layer")
	}
}

func activatedArchiveFixture(t *testing.T, options archiveFixtureOptions) (ActivatedImage, []byte) {
	t.Helper()
	if options.os == "" {
		options.os = "linux"
	}
	if options.architecture == "" {
		options.architecture = "arm64"
	}
	layers := []string{"layer/layer.tar"}
	diffIDs := []string{"sha256:" + strings.Repeat("5", 64)}
	if options.duplicateLayer {
		layers = append(layers, layers[0])
		diffIDs = append(diffIDs, diffIDs[0])
	}
	configDocument := struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		RootFS       struct {
			Type    string   `json:"type"`
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}{Architecture: options.architecture, OS: options.os}
	configDocument.RootFS.Type = "layers"
	configDocument.RootFS.DiffIDs = diffIDs
	config, err := json.Marshal(configDocument)
	if err != nil {
		t.Fatal(err)
	}
	configDigest := sha256.Sum256(config)
	configName := hex.EncodeToString(configDigest[:]) + ".json"
	manifest := []dockerArchiveManifestEntry{{Config: configName, RepoTags: options.tags, Layers: layers}}
	if options.secondImage {
		manifest = append(manifest, manifest[0])
	}
	manifestRaw, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	members := []struct {
		name string
		body []byte
	}{{"manifest.json", manifestRaw}, {configName, config}}
	if !options.omitLayer {
		members = append(members, struct {
			name string
			body []byte
		}{"layer/layer.tar", []byte("layer")})
	}
	if options.extraMember {
		members = append(members, struct {
			name string
			body []byte
		}{"extra", []byte("extra")})
	}
	if options.duplicateMember {
		members = append(members, members[1])
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, directory := range []string{"blobs/", "blobs/sha256/"} {
		if err := writer.WriteHeader(&tar.Header{Name: directory, Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
			t.Fatal(err)
		}
	}
	for _, member := range members {
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
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "image.tar")
	if err := os.WriteFile(archivePath, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	archiveDigest := sha256.Sum256(archive.Bytes())
	return ActivatedImage{
		ArchiveFormat: DockerArchiveFormat, ArchivePath: archivePath, ArchiveSize: int64(archive.Len()),
		ArchiveSHA256: hex.EncodeToString(archiveDigest[:]), LocalDockerConfigImageID: "sha256:" + hex.EncodeToString(configDigest[:]),
	}, archive.Bytes()
}

func activatedContainerdArchiveFixture(t *testing.T, options containerdArchiveFixtureOptions) (ActivatedImage, []byte) {
	t.Helper()
	if options.os == "" {
		options.os = "linux"
	}
	if options.architecture == "" {
		options.architecture = "arm64"
	}
	layer := []byte("compressed OCI layer bytes")
	layerDigest := sha256.Sum256(layer)
	layerID := "sha256:" + hex.EncodeToString(layerDigest[:])
	layerPath := "blobs/sha256/" + hex.EncodeToString(layerDigest[:])
	configDocument := struct {
		Architecture string `json:"architecture"`
		OS           string `json:"os"`
		RootFS       struct {
			Type    string   `json:"type"`
			DiffIDs []string `json:"diff_ids"`
		} `json:"rootfs"`
	}{Architecture: options.architecture, OS: options.os}
	configDocument.RootFS.Type = "layers"
	configDocument.RootFS.DiffIDs = []string{"sha256:" + strings.Repeat("5", 64)}
	config, err := json.Marshal(configDocument)
	if err != nil {
		t.Fatal(err)
	}
	configDigest := sha256.Sum256(config)
	configID := "sha256:" + hex.EncodeToString(configDigest[:])
	configPath := "blobs/sha256/" + hex.EncodeToString(configDigest[:])
	configMediaType := ociImageConfigMediaType
	if options.configMediaTypeDrift {
		configMediaType = "application/octet-stream"
	}
	layerMediaType := "application/vnd.oci.image.layer.v1.tar+gzip"
	if options.layerMediaTypeDrift {
		layerMediaType = "application/octet-stream"
	}
	layerSize := int64(len(layer))
	if options.layerSizeDrift {
		layerSize++
	}
	imageManifest := ociImageManifest{
		SchemaVersion: 2, MediaType: ociImageManifestMediaType,
		Config: ociDescriptor{MediaType: configMediaType, Digest: configID, Size: int64(len(config))},
		Layers: []ociDescriptor{{MediaType: layerMediaType, Digest: layerID, Size: layerSize}},
	}
	imageManifestRaw, err := json.Marshal(imageManifest)
	if err != nil {
		t.Fatal(err)
	}
	imageManifestDigest := sha256.Sum256(imageManifestRaw)
	imageID := "sha256:" + hex.EncodeToString(imageManifestDigest[:])
	imageManifestPath := "blobs/sha256/" + hex.EncodeToString(imageManifestDigest[:])
	manifestSize := int64(len(imageManifestRaw))
	if options.manifestSizeDrift {
		manifestSize++
	}
	descriptor := ociDescriptor{
		MediaType: ociImageManifestMediaType, Digest: imageID, Size: manifestSize,
		Platform: &Platform{OS: options.os, Architecture: options.architecture},
	}
	index := ociImageIndex{SchemaVersion: 2, MediaType: ociImageIndexMediaType, Manifests: []ociDescriptor{descriptor}}
	if options.extraIndexDescriptor {
		index.Manifests = append(index.Manifests, descriptor)
	}
	indexRaw, err := json.Marshal(index)
	if err != nil {
		t.Fatal(err)
	}
	layoutRaw, err := json.Marshal(ociLayout{ImageLayoutVersion: ociImageLayoutVersion})
	if err != nil {
		t.Fatal(err)
	}
	dockerManifestRaw, err := json.Marshal([]dockerArchiveManifestEntry{{Config: configPath, RepoTags: options.tags, Layers: []string{layerPath}}})
	if err != nil {
		t.Fatal(err)
	}
	if options.layerByteDrift {
		layer[0] ^= 0xff
	}
	members := []struct {
		name string
		body []byte
	}{
		{"manifest.json", dockerManifestRaw}, {"index.json", indexRaw}, {"oci-layout", layoutRaw},
		{imageManifestPath, imageManifestRaw}, {configPath, config}, {layerPath, layer},
	}
	if options.extraMember {
		members = append(members, struct {
			name string
			body []byte
		}{"blobs/sha256/" + strings.Repeat("e", 64), []byte("extra")})
	}
	var archive bytes.Buffer
	writer := tar.NewWriter(&archive)
	for _, member := range members {
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
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "containerd-image.tar")
	if err := os.WriteFile(archivePath, archive.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	archiveDigest := sha256.Sum256(archive.Bytes())
	return ActivatedImage{
		ArchiveFormat: DockerArchiveFormat, ArchivePath: archivePath, ArchiveSize: int64(archive.Len()),
		ArchiveSHA256: hex.EncodeToString(archiveDigest[:]), LocalDockerConfigImageID: imageID,
	}, archive.Bytes()
}
