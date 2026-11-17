package pi

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

//go:embed budgeted/provider.mjs budgeted/relay.mjs budgeted/extension.mjs budgeted/wrapper.mjs budgeted/launch.mjs
var BudgetedAssets embed.FS

type BudgetedSource struct {
	identity domain.RestrictedPiIdentity
	digest   [32]byte
}

func NewBudgetedSource(identity domain.RestrictedPiIdentity) (BudgetedSource, error) {
	if err := identity.Validate(); err != nil {
		return BudgetedSource{}, err
	}
	if identity.PiVersion != IsolatedPiVersion {
		return BudgetedSource{}, errors.New("restricted provider API version unsupported")
	}
	b, _ := hex.DecodeString(identity.RuntimeDigest())
	var fingerprint [sha256.Size]byte
	copy(fingerprint[:], b)
	return BudgetedSource{identity: identity, digest: fingerprint}, nil
}
func (s BudgetedSource) Configured() bool { return s.digest != [32]byte{} }
func (s BudgetedSource) valid() bool {
	restored, err := NewBudgetedSource(s.identity)
	return err == nil && restored == s
}

func (s BudgetedSource) Record() domain.RestrictedPiIdentity { return s.identity }
func (s BudgetedSource) Fingerprint() execution.RuntimeFingerprint {
	return fingerprintForSource(selectedSource{identity: s.digest, version: s.identity.PiVersion})
}
func (s BudgetedSource) ValidateContract(raw []byte) error {
	var doc struct {
		Candidate struct {
			Runtime struct {
				Config struct {
					SHA256 string `json:"sha256"`
				} `json:"config"`
			} `json:"runtime"`
			Sandbox struct {
				SHA256 string `json:"sha256"`
			} `json:"sandbox"`
		} `json:"candidate"`
	}
	if domain.WorkflowDigest(raw) != s.identity.Contract || json.Unmarshal(raw, &doc) != nil || doc.Candidate.Runtime.Config.SHA256 != s.identity.RuntimeDigest() || doc.Candidate.Sandbox.SHA256 != strings.TrimPrefix(s.identity.Image, "sha256:") {
		return errors.New("frozen restricted Task source or image drift")
	}
	return nil
}
func mustBudgetedArguments(p BudgetedPermissionPlan) []string { a, _ := p.Arguments(); return a }
func (a *Adapter) WithBudgetedSource(s BudgetedSource, p BudgetedPermissionPlan) (*Adapter, error) {
	if err := ValidateBudgetedStructure(s.identity, p); err != nil {
		return nil, err
	}
	config := a.config
	config.BudgetedSource = s
	config.BudgetedPermissions = p.Clone()
	return New(config)
}

func BudgetedAssetDigests() map[string]string {
	result := map[string]string{}
	for _, name := range []string{"provider", "relay", "extension", "wrapper", "launch"} {
		b, _ := BudgetedAssets.ReadFile("budgeted/" + name + ".mjs")
		result[name] = domain.WorkflowDigest(b)
	}
	return result
}
func BudgetedArguments() []string {
	return append(IsolatedRPCArguments(), "--extension", "/opt/chora/budgeted/extension.mjs")
}

// ValidateBudgetedCandidate checks a proposed installed source without probing
// Pi, looking up credentials, or treating a fixture as qualification. It never
// manufactures an input/billing proof. Required permission extensions must be
// fixed and preserved by an independently qualified image before admission.
func ValidateBudgetedCandidate(i domain.RestrictedPiIdentity, permissionContractPreserved bool) error {
	if err := i.Validate(); err != nil {
		return err
	}
	if !permissionContractPreserved {
		return errors.New("permission_contract_unsupported")
	}
	d := BudgetedAssetDigests()

	if i.Shim != d["extension"] || i.Serializer != d["provider"] || i.Translator != d["provider"] || i.Entry != BudgetedEntryDigest() || i.Relay != d["relay"] {
		return errors.New("restricted source installed closure drift")
	}
	return errors.New("bound_unproven: no trusted renderer/billing proof or independently qualified restricted runtime is installed")
}

func BudgetedEntryDigest() string {
	d := BudgetedAssetDigests()
	b, _ := json.Marshal(map[string]string{"wrapper": d["wrapper"], "launch": d["launch"]})
	return domain.WorkflowDigest(b)
}
