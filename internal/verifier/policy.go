package verifier

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Yangyang96/chora/internal/speccoding"
)

const PolicyProjectionSchemaVersion = "chora.m1-verifier-policy.v1"

const (
	PolicyModeFull           = "full_verification"
	PolicyModeAcceptanceOnly = "acceptance_only_evidence_unavailable"
)

var (
	ErrInvalidPolicy = errors.New("invalid verifier policy projection")
)

// PolicyDocument is a non-authoritative execution-layer projection of the
// formally decoded, digest-verified speccoding policy. It is not a second policy
// schema or decoder.
type PolicyDocument struct {
	SchemaVersion            string    `json:"schema_version"`
	Version                  string    `json:"version"`
	Mode                     string    `json:"mode"`
	DockerVersion            string    `json:"docker_version"`
	DockerContext            string    `json:"docker_context"`
	ColimaVersion            string    `json:"colima_version"`
	Image                    string    `json:"image"`
	NetworkDisabled          bool      `json:"network_disabled"`
	CredentialsDisabled      bool      `json:"credentials_disabled"`
	NonRootUID               int       `json:"non_root_uid"`
	NonRootGID               int       `json:"non_root_gid"`
	ReadOnlyRoot             bool      `json:"read_only_root"`
	DropCapabilities         [1]string `json:"drop_capabilities"`
	NoNewPrivileges          bool      `json:"no_new_privileges"`
	SeccompProfile           string    `json:"seccomp_profile"`
	Environment              [6]string `json:"environment"`
	CPUs                     int       `json:"cpus"`
	MemoryMiB                int       `json:"memory_mib"`
	MemorySwapMiB            int       `json:"memory_swap_mib"`
	PIDs                     int       `json:"pids"`
	FileDescriptors          int       `json:"file_descriptors"`
	CommandTimeoutSeconds    int       `json:"command_timeout_seconds"`
	AttemptTimeoutSeconds    int       `json:"attempt_timeout_seconds"`
	PersistedLogBytes        int64     `json:"persisted_log_bytes"`
	ArtifactBytes            int64     `json:"artifact_bytes"`
	DeathConfirmationSeconds int       `json:"death_confirmation_seconds"`
}

type Policy struct {
	document PolicyDocument
	digest   [32]byte
}

// DecodePolicyAuthority is the sole product constructor. It first verifies the
// exact formal policy bytes and digest, then derives the smaller execution
// projection without adding authority.
func DecodePolicyAuthority(data []byte) (Policy, error) {
	authority, err := speccoding.DecodeVerifierPolicyV1(data)
	if err != nil {
		return Policy{}, fmt.Errorf("%w: formal authority: %v", ErrInvalidPolicy, err)
	}
	uidText, gidText, ok := strings.Cut(authority.Workspace.User, ":")
	if !ok {
		return Policy{}, ErrInvalidPolicy
	}
	uid, uidErr := strconv.Atoi(uidText)
	gid, gidErr := strconv.Atoi(gidText)
	if uidErr != nil || gidErr != nil {
		return Policy{}, ErrInvalidPolicy
	}
	document := PolicyDocument{
		SchemaVersion:       PolicyProjectionSchemaVersion,
		Version:             authority.SchemaVersion,
		Mode:                authority.PolicyMode,
		DockerVersion:       authority.Provider.EngineVersion,
		DockerContext:       "colima",
		ColimaVersion:       authority.Provider.ColimaVersion,
		Image:               authority.Image.ID,
		NetworkDisabled:     authority.Network.Mode == "none" && !authority.Network.GeneralEgress && !authority.Network.HostNetwork && !authority.Network.PublishedPorts && !authority.Network.NormalBridgeFallback,
		CredentialsDisabled: authority.Credentials.Injection == "none" && len(authority.Credentials.DeclaredNames) == 0 && !authority.Credentials.InheritHostEnvironment && !authority.Credentials.MountSourceFiles,
		NonRootUID:          uid,
		NonRootGID:          gid,
		ReadOnlyRoot:        authority.Isolation.ReadOnlyRoot,
		DropCapabilities:    [1]string{"ALL"},
		NoNewPrivileges:     authority.Isolation.NoNewPrivileges,
		SeccompProfile:      authority.Isolation.SeccompProfile,
		Environment: [6]string{
			"GOCACHE=" + authority.Command.Environment["GOCACHE"],
			"GOPROXY=" + authority.Command.Environment["GOPROXY"],
			"GOSUMDB=" + authority.Command.Environment["GOSUMDB"],
			"GOTOOLCHAIN=" + authority.Command.Environment["GOTOOLCHAIN"],
			"GOTMPDIR=" + authority.Command.Environment["GOTMPDIR"],
			"HOME=" + authority.Command.Environment["HOME"],
		},
		CPUs:                     authority.Resources.CPUs,
		MemoryMiB:                authority.Resources.MemoryMiB,
		MemorySwapMiB:            authority.Resources.MemorySwapMiB,
		PIDs:                     authority.Resources.PIDsLimit,
		FileDescriptors:          authority.Resources.FileDescriptors,
		CommandTimeoutSeconds:    authority.Resources.CommandTimeoutSeconds,
		AttemptTimeoutSeconds:    authority.Resources.AttemptTimeoutSeconds,
		PersistedLogBytes:        authority.Evidence.PersistedLogBytes,
		ArtifactBytes:            authority.Evidence.ArtifactBytes,
		DeathConfirmationSeconds: authority.Termination.DeathConfirmationSeconds,
	}
	return newPolicyProjection(document, sha256.Sum256(data))
}

func newPolicyProjection(document PolicyDocument, verifiedDigest [32]byte) (Policy, error) {
	if verifiedDigest == ([32]byte{}) {
		return Policy{}, ErrInvalidPolicy
	}
	if err := validatePolicy(document); err != nil {
		return Policy{}, err
	}
	return Policy{document: document, digest: verifiedDigest}, nil
}

func validatePolicy(document PolicyDocument) error {
	if document.SchemaVersion != PolicyProjectionSchemaVersion || strings.TrimSpace(document.Version) == "" ||
		document.Mode != PolicyModeFull && document.Mode != PolicyModeAcceptanceOnly ||
		document.DockerVersion != "29.6.1" || document.DockerContext != "colima" || document.ColimaVersion != "0.10.3" ||
		!validSHA256Identity(document.Image) || !document.NetworkDisabled || !document.CredentialsDisabled ||
		document.NonRootUID <= 0 || document.NonRootGID <= 0 || !document.ReadOnlyRoot ||
		document.DropCapabilities != [1]string{"ALL"} || !document.NoNewPrivileges ||
		document.SeccompProfile != "docker_default" || document.Environment != [6]string{"GOCACHE=/workspace/.chora-cache/go-build", "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOTMPDIR=/workspace/.chora-tmp", "HOME=/tmp"} || document.CPUs != 2 ||
		document.MemoryMiB != 4096 || document.MemorySwapMiB != document.MemoryMiB || document.PIDs != 256 ||
		document.FileDescriptors != 1024 || document.CommandTimeoutSeconds != 600 ||
		document.AttemptTimeoutSeconds != 1200 || document.PersistedLogBytes != 10<<20 ||
		document.ArtifactBytes != 100<<20 || document.DeathConfirmationSeconds != 5 {
		return ErrInvalidPolicy
	}
	return nil
}

func validSHA256Identity(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "sha256:"))
	return err == nil
}

func (policy Policy) Document() PolicyDocument { return policy.document }
func (policy Policy) Digest() [32]byte         { return policy.digest }
