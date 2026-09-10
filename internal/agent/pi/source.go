package pi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

const (
	sourceContractVersion                    = "chora.pi-runtime-source.v4"
	managedDevelopmentProvenanceKind         = "development_fixture"
	ManagedReleaseProvenanceActivatedRelease = "activated_release"
)

// ManagedReleaseProvenance is the value-only release identity bound to one
// managed Runtime source. It deliberately binds the offline archive bytes and
// expected Docker config identity; an ambient local image ID alone is not
// release provenance.
type ManagedReleaseProvenance struct {
	Kind                     string
	ReleaseID                string
	SpecSHA256               string
	ArtifactID               string
	ArchiveFormat            string
	ArchiveSHA256            string
	ArchiveSize              int64
	DockerConfigImageID      string
	PolicyID                 string
	PolicyPath               string
	PolicySHA256             string
	PlatformOS               string
	PlatformArchitecture     string
	RuntimePiName            string
	RuntimePiVersion         string
	RuntimePiNPMIntegrity    string
	RuntimeNodeVersion       string
	RuntimeNodeExecutable    string
	RuntimeNodeVersionOutput string
	EntrypointJSON           string
}

// ManagedImageSourceParams identifies the one release-pinned Pi image source.
// The values are copied into an immutable ManagedImageSource on construction.
type ManagedImageSourceParams struct {
	RuntimeVersion      string
	Executable          string
	RuntimeConfigSHA256 string
	RuntimeImageID      string
	ReleaseProvenance   ManagedReleaseProvenance
}

// ManagedImageSourceRecord is the persisted/auditable representation of a
// managed source. SourceIdentity detects partial records and field drift.
type ManagedImageSourceRecord struct {
	RuntimeVersion      string
	Executable          string
	RuntimeConfigSHA256 string
	RuntimeImageID      string
	ReleaseProvenance   ManagedReleaseProvenance
	SourceIdentity      [32]byte
}

type ManagedImageSource struct {
	runtimeVersion      string
	executable          string
	runtimeConfigSHA256 string
	runtimeImageID      string
	releaseProvenance   ManagedReleaseProvenance
	sourceIdentity      [32]byte
}

func NewManagedImageSource(params ManagedImageSourceParams) (ManagedImageSource, error) {
	if params.RuntimeVersion != RuntimeVersion || params.Executable != Executable ||
		params.RuntimeConfigSHA256 != RuntimeConfigSHA256 ||
		!validSHA256Identity(params.RuntimeImageID) || !validManagedReleaseProvenance(params.ReleaseProvenance, params.RuntimeVersion, params.RuntimeImageID) {
		return ManagedImageSource{}, errors.New("invalid managed Pi image source identity")
	}
	source := ManagedImageSource{
		runtimeVersion: params.RuntimeVersion, executable: params.Executable,
		runtimeConfigSHA256: params.RuntimeConfigSHA256, runtimeImageID: params.RuntimeImageID,
		releaseProvenance: params.ReleaseProvenance,
	}
	source.sourceIdentity = managedSourceIdentity(source)
	return source, nil
}

func DefaultManagedImageSource() ManagedImageSource {
	source, err := NewManagedImageSource(ManagedImageSourceParams{
		RuntimeVersion: RuntimeVersion, Executable: Executable,
		RuntimeConfigSHA256: RuntimeConfigSHA256, RuntimeImageID: AttemptImageID,
		ReleaseProvenance: ManagedReleaseProvenance{Kind: managedDevelopmentProvenanceKind},
	})
	if err != nil {
		panic(err)
	}
	return source
}

func RestoreManagedImageSource(record ManagedImageSourceRecord) (ManagedImageSource, error) {
	source, err := NewManagedImageSource(ManagedImageSourceParams{
		RuntimeVersion: record.RuntimeVersion, Executable: record.Executable,
		RuntimeConfigSHA256: record.RuntimeConfigSHA256, RuntimeImageID: record.RuntimeImageID,
		ReleaseProvenance: record.ReleaseProvenance,
	})
	if err != nil || record.SourceIdentity == ([32]byte{}) || source.sourceIdentity != record.SourceIdentity {
		return ManagedImageSource{}, errors.New("managed Pi image source identity drift")
	}
	return source, nil
}

func (source ManagedImageSource) Record() ManagedImageSourceRecord {
	return ManagedImageSourceRecord{
		RuntimeVersion: source.runtimeVersion, Executable: source.executable,
		RuntimeConfigSHA256: source.runtimeConfigSHA256, RuntimeImageID: source.runtimeImageID,
		ReleaseProvenance: source.releaseProvenance,
		SourceIdentity:    source.sourceIdentity,
	}
}

func (source ManagedImageSource) Configured() bool { return source != (ManagedImageSource{}) }

func (source ManagedImageSource) valid() bool {
	restored, err := RestoreManagedImageSource(source.Record())
	return err == nil && restored == source
}

func (source ManagedImageSource) Executable() string     { return source.executable }
func (source ManagedImageSource) RuntimeVersion() string { return source.runtimeVersion }
func (source ManagedImageSource) RuntimeImageID() string { return source.runtimeImageID }
func (source ManagedImageSource) ReleaseProvenance() ManagedReleaseProvenance {
	return source.releaseProvenance
}
func (source ManagedImageSource) SourceIdentity() [32]byte { return source.sourceIdentity }

func managedSourceIdentity(source ManagedImageSource) [32]byte {
	return sha256.Sum256([]byte(strings.Join([]string{
		sourceContractVersion, "managed_image", source.runtimeVersion, source.executable,
		source.runtimeConfigSHA256, source.runtimeImageID,
		source.releaseProvenance.Kind, source.releaseProvenance.ReleaseID, source.releaseProvenance.SpecSHA256,
		source.releaseProvenance.ArtifactID, source.releaseProvenance.ArchiveFormat,
		source.releaseProvenance.ArchiveSHA256, fmt.Sprint(source.releaseProvenance.ArchiveSize), source.releaseProvenance.DockerConfigImageID,
		source.releaseProvenance.PolicyID, source.releaseProvenance.PolicyPath, source.releaseProvenance.PolicySHA256,
		source.releaseProvenance.PlatformOS, source.releaseProvenance.PlatformArchitecture,
		source.releaseProvenance.RuntimePiName, source.releaseProvenance.RuntimePiVersion,
		source.releaseProvenance.RuntimePiNPMIntegrity, source.releaseProvenance.RuntimeNodeVersion,
		source.releaseProvenance.RuntimeNodeExecutable, source.releaseProvenance.RuntimeNodeVersionOutput,
		source.releaseProvenance.EntrypointJSON,
	}, "\x00")))
}

func validManagedReleaseProvenance(provenance ManagedReleaseProvenance, runtimeVersion, runtimeImageID string) bool {
	if provenance.Kind == managedDevelopmentProvenanceKind {
		return provenance == (ManagedReleaseProvenance{Kind: managedDevelopmentProvenanceKind})
	}
	if provenance.Kind != ManagedReleaseProvenanceActivatedRelease || provenance.ReleaseID == "" ||
		!validDigestText(provenance.SpecSHA256) || provenance.ArtifactID == "" ||
		provenance.ArchiveFormat != "docker-archive" || !validDigestText(provenance.ArchiveSHA256) || provenance.ArchiveSize <= 0 ||
		provenance.DockerConfigImageID != runtimeImageID || !validSHA256Identity(provenance.DockerConfigImageID) || provenance.PolicyID == "" ||
		provenance.PolicyPath == "" || filepath.IsAbs(provenance.PolicyPath) || filepath.Clean(provenance.PolicyPath) != provenance.PolicyPath ||
		!validDigestText(provenance.PolicySHA256) || provenance.PlatformOS != "linux" || provenance.PlatformArchitecture != "arm64" ||
		provenance.RuntimePiName == "" || provenance.RuntimePiVersion != runtimeVersion || provenance.RuntimePiNPMIntegrity == "" ||
		provenance.RuntimeNodeVersion == "" || !filepath.IsAbs(provenance.RuntimeNodeExecutable) ||
		provenance.RuntimeNodeVersionOutput == "" || !validEntrypointJSON(provenance.EntrypointJSON) {
		return false
	}
	return true
}

func validEntrypointJSON(raw string) bool {
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

func validDigestText(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

// LocalPiSourceParams identifies one explicitly configured supported local Pi.
// Discovery and installation are deliberately outside this value contract.
type LocalPiSourceParams struct {
	ExecutablePath         string
	ResolvedExecutablePath string
	PackageRoot            string
	RuntimeVersion         string
	ExecutableSHA256       [32]byte
	ClosureSHA256          [32]byte
}

type LocalPiSourceRecord struct {
	ExecutablePath         string
	ResolvedExecutablePath string
	PackageRoot            string
	RuntimeVersion         string
	ExecutableSHA256       [32]byte
	ClosureSHA256          [32]byte
	SourceIdentity         [32]byte
}

type LocalPiSource struct {
	executablePath         string
	resolvedExecutablePath string
	packageRoot            string
	runtimeVersion         string
	executableSHA256       [32]byte
	closureSHA256          [32]byte
	sourceIdentity         [32]byte
}

func NewLocalPiSource(params LocalPiSourceParams) (LocalPiSource, error) {
	if !validLocalExecutablePath(params.ExecutablePath) || !validLocalExecutablePath(params.ResolvedExecutablePath) ||
		!validLocalExecutablePath(params.PackageRoot) || !pathWithin(params.PackageRoot, params.ResolvedExecutablePath) ||
		params.RuntimeVersion != RuntimeVersion || params.ExecutableSHA256 == ([32]byte{}) || params.ClosureSHA256 == ([32]byte{}) {
		return LocalPiSource{}, errors.New("invalid local Pi source identity")
	}
	source := LocalPiSource{
		executablePath: params.ExecutablePath, resolvedExecutablePath: params.ResolvedExecutablePath,
		packageRoot: params.PackageRoot, runtimeVersion: params.RuntimeVersion,
		executableSHA256: params.ExecutableSHA256, closureSHA256: params.ClosureSHA256,
	}
	source.sourceIdentity = localSourceIdentity(source)
	return source, nil
}

func RestoreLocalPiSource(record LocalPiSourceRecord) (LocalPiSource, error) {
	source, err := NewLocalPiSource(LocalPiSourceParams{
		ExecutablePath: record.ExecutablePath, ResolvedExecutablePath: record.ResolvedExecutablePath,
		PackageRoot: record.PackageRoot, RuntimeVersion: record.RuntimeVersion,
		ExecutableSHA256: record.ExecutableSHA256, ClosureSHA256: record.ClosureSHA256,
	})
	if err != nil || record.SourceIdentity == ([32]byte{}) || source.sourceIdentity != record.SourceIdentity {
		return LocalPiSource{}, errors.New("local Pi source identity drift")
	}
	return source, nil
}

func (source LocalPiSource) Record() LocalPiSourceRecord {
	return LocalPiSourceRecord{
		ExecutablePath: source.executablePath, ResolvedExecutablePath: source.resolvedExecutablePath,
		PackageRoot: source.packageRoot, RuntimeVersion: source.runtimeVersion,
		ExecutableSHA256: source.executableSHA256, ClosureSHA256: source.closureSHA256,
		SourceIdentity: source.sourceIdentity,
	}
}

func (source LocalPiSource) Configured() bool { return source != (LocalPiSource{}) }

func (source LocalPiSource) valid() bool {
	restored, err := RestoreLocalPiSource(source.Record())
	return err == nil && restored == source
}

func (source LocalPiSource) ExecutablePath() string         { return source.executablePath }
func (source LocalPiSource) ResolvedExecutablePath() string { return source.resolvedExecutablePath }
func (source LocalPiSource) PackageRoot() string            { return source.packageRoot }
func (source LocalPiSource) RuntimeVersion() string         { return source.runtimeVersion }
func (source LocalPiSource) ExecutableSHA256() [32]byte     { return source.executableSHA256 }
func (source LocalPiSource) ClosureSHA256() [32]byte        { return source.closureSHA256 }
func (source LocalPiSource) SourceIdentity() [32]byte       { return source.sourceIdentity }

func localSourceIdentity(source LocalPiSource) [32]byte {
	return sha256.Sum256([]byte(strings.Join([]string{
		sourceContractVersion, "local_pi", source.runtimeVersion, source.executablePath,
		source.resolvedExecutablePath, source.packageRoot, hex.EncodeToString(source.executableSHA256[:]),
		hex.EncodeToString(source.closureSHA256[:]),
	}, "\x00")))
}

func validLocalExecutablePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && path != string(filepath.Separator)
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != "." && relative != ".." && !filepath.IsAbs(relative) &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func validSHA256Identity(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil && len(decoded) == sha256.Size
}
