package pi

import (
	"crypto/sha256"
	"embed"
	"encoding/json"
	"errors"

	"github.com/Yangyang96/chora/internal/domain"
)

//go:embed budgeted/provider.mjs budgeted/relay.mjs budgeted/extension.mjs budgeted/wrapper.mjs
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
	b, _ := json.Marshal(identity)
	return BudgetedSource{identity: identity, digest: sha256.Sum256(b)}, nil
}
func (s BudgetedSource) Configured() bool { return s.digest != [32]byte{} }
func (s BudgetedSource) valid() bool {
	restored, err := NewBudgetedSource(s.identity)
	return err == nil && restored == s
}

func BudgetedAssetDigests() map[string]string {
	result := map[string]string{}
	for _, name := range []string{"provider", "relay", "extension", "wrapper"} {
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
	combined, _ := json.Marshal(d)
	if i.Shim != d["extension"] || i.Serializer != d["provider"] || i.Translator != d["provider"] || i.Entry != d["wrapper"] || i.Helper != domain.WorkflowDigest(combined) {
		return errors.New("restricted source installed closure drift")
	}
	return errors.New("bound_unproven: no trusted renderer/billing proof or independently qualified restricted runtime is installed")
}
