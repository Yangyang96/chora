package domain

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"
)

// RestrictedPiIdentity is non-secret. Each digest describes actual installed or
// final effective bytes, not a package name, desired image tag, or secret hash.
// There are no defaults, including for permission extensions and empty lists.
type RestrictedPiIdentity struct {
	Schema                                                               string `json:"schema"`
	Source                                                               string `json:"source"`
	PiVersion                                                            string `json:"piVersion"`
	NodeVersion                                                          string `json:"nodeVersion"`
	Endpoint                                                             string `json:"endpoint"`
	CredentialScope                                                      string `json:"credentialScope"`
	Image, RootFS, PiArchive, Installation, Node, System, Tools, Browser string
	Helper, Guardian, Adapter, GoBuild, Catalog, ModelSubset             string
	Shim, Relay, Serializer, Translator, Transport, Policy               string
	Arguments, Environment, Entry, Mounts, Snapshot, Contract            string
	AllowedTools, PermissionExtensions, BoundProof, BillingProof         string
}

func CanonicalSHA256(value string) bool {
	b, err := hex.DecodeString(value)
	return err == nil && len(b) == 32 && hex.EncodeToString(b) == value
}

func (i RestrictedPiIdentity) Validate() error {
	if i.Schema != "chora.restricted-pi.v2" || i.Source != BudgetedPiRuntimeSource || !validExecutionProfileActor(i.PiVersion) || !validExecutionProfileActor(i.NodeVersion) || i.Endpoint != "deepseek.chat-completions.v1" || !validExecutionProfileActor(i.CredentialScope) {
		return fmt.Errorf("%w: restricted source identity incomplete", ErrInvalidArgument)
	}
	for _, v := range []string{i.Image, i.RootFS, i.PiArchive, i.Installation, i.Node, i.System, i.Tools, i.Browser, i.Helper, i.Guardian, i.Adapter, i.GoBuild, i.Catalog, i.ModelSubset, i.Shim, i.Relay, i.Serializer, i.Translator, i.Transport, i.Policy, i.Arguments, i.Environment, i.Entry, i.Mounts, i.Snapshot, i.Contract, i.AllowedTools, i.PermissionExtensions, i.BoundProof, i.BillingProof} {
		if !CanonicalSHA256(v) {
			return fmt.Errorf("%w: restricted source digest missing or noncanonical", ErrInvalidArgument)
		}
	}
	return nil
}
func (i RestrictedPiIdentity) Digest() string { b, _ := json.Marshal(i); return WorkflowDigest(b) }

// Task documents refer to runtime identity, while the full authorization also
// freezes those documents. Excluding only these two scope fields breaks that
// hash cycle; Digest and the Go authority still bind them at launch/dispatch.
func (i RestrictedPiIdentity) RuntimeDigest() string {
	i.Snapshot = ""
	i.Contract = ""
	b, _ := json.Marshal(i)
	return WorkflowDigest(b)
}

// Per-attempt metadata and source-derived fingerprints are retained in the
// immutable launch config digest, not recursively hashed into source identity.
func RestrictedStableEnvironment(env map[string]string) map[string]string {
	stable := make(map[string]string, len(env))
	for k, v := range env {
		switch k {
		case "CHORA_RUN_ID", "CHORA_ATTEMPT_ID", "CHORA_CONTEXT_DIGEST", "CHORA_CONTRACT_DIGEST", "CHORA_CHECK_OBSERVER_CONFIG_SHA256", "CHORA_RUNTIME_SOURCE_IDENTITY", "CHORA_CHECK_OBSERVER_RUNTIME_FINGERPRINT":
			continue
		}
		stable[k] = v
	}
	return stable
}

// RestrictedLaunch is minted by Go after matching final launch observations.
// Frames cannot supply/replace these fields. Epoch revocation is append-only.
type RestrictedLaunch struct {
	Schema                                                        string `json:"schema"`
	Authorization, Workflow, Project, Task, Charter, Run, Attempt string
	Actor, Gate, Operation, InputDigest, AuthorityDigest          string
	LaunchToken, Channel, SourceDigest, ConfigDigest              string
	OutputCap                                                     int `json:"outputCap,omitempty"`
	Epoch                                                         uint64
	AuthorizedAt, Deadline, ObservedAt                            time.Time
}

func (b RestrictedLaunch) Validate() error {
	if b.Schema != "chora.restricted-launch.v2" || b.Epoch != 1 || b.OutputCap < 0 || b.OutputCap > 2048 || b.AuthorizedAt.IsZero() || b.ObservedAt.Before(b.AuthorizedAt) || !b.Deadline.After(b.ObservedAt) || b.Deadline.Sub(b.AuthorizedAt) > 20*time.Minute {
		return fmt.Errorf("%w: invalid restricted launch time or epoch", ErrInvalidArgument)
	}
	for _, v := range []string{b.Authorization, b.Workflow, b.Project, b.Task, b.Charter, b.Run, b.Attempt, b.Actor, b.Gate, b.Operation, b.LaunchToken, b.Channel} {
		if !validExecutionProfileActor(v) {
			return fmt.Errorf("%w: incomplete restricted launch", ErrInvalidArgument)
		}
	}
	for _, v := range []string{b.InputDigest, b.AuthorityDigest, b.SourceDigest, b.ConfigDigest} {
		if !CanonicalSHA256(v) {
			return fmt.Errorf("%w: incomplete restricted launch digest", ErrInvalidArgument)
		}
	}
	return nil
}
