package pi

import (
	"encoding/json"
	"errors"
	"path"
	"reflect"
	"regexp"
	"slices"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
)

type BudgetedExtension struct {
	Path      string `json:"path"`
	SHA256    string `json:"sha256"`
	Closure   string `json:"closure"`
	Semantics string `json:"semantics"`
}

// Both inventories must be explicit (including []); no missing discovery is
// interpreted as permission to disable a required extension or widen tools.
// Matching records are structural evidence, not runtime qualification.
type BudgetedPermissionPlan struct {
	Tools            []string            `json:"tools"`
	OriginalRequired []BudgetedExtension `json:"originalRequired"`
	Installed        []BudgetedExtension `json:"installed"`
	ReadOnlyPaths    []string            `json:"readOnlyPaths"`
}

func (p BudgetedPermissionPlan) Validate() error {
	if p.Tools == nil || len(p.Tools) == 0 || len(p.Tools) > 7 || len(p.OriginalRequired) > 64 || len(p.ReadOnlyPaths) > 1024 || p.OriginalRequired == nil || p.Installed == nil || p.ReadOnlyPaths == nil || !reflect.DeepEqual(p.OriginalRequired, p.Installed) {
		return errors.New("permission_contract_unsupported: explicit unchanged permission inventory required")
	}
	seen := map[string]bool{}
	for _, tool := range p.Tools {
		if !slices.Contains([]string{"read", "bash", "edit", "write", "grep", "find", "ls"}, tool) || seen[tool] {
			return errors.New("permission_contract_unsupported: tool set drift")
		}
		seen[tool] = true
	}
	seen = map[string]bool{}
	for _, e := range p.OriginalRequired {
		if !regexp.MustCompile(`^/opt/chora/permissions/[a-zA-Z0-9_-]+\.mjs$`).MatchString(e.Path) || !domain.CanonicalSHA256(e.SHA256) || !domain.CanonicalSHA256(e.Closure) || !domain.CanonicalSHA256(e.Semantics) || seen[e.Path] {
			return errors.New("permission_contract_unsupported: extension closure or semantics missing")
		}
		seen[e.Path] = true
	}
	seen = map[string]bool{}
	for _, p := range p.ReadOnlyPaths {
		if p == "" || p == "." || path.IsAbs(p) || path.Clean(p) != p || strings.HasPrefix(p, "../") || strings.ContainsAny(p, ",\n\r\\") || seen[p] {
			return errors.New("permission_contract_unsupported: read-only mask drift")
		}
		seen[p] = true
	}
	return nil
}
func (p BudgetedPermissionPlan) Canonical() ([]byte, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	return json.Marshal(p)
}
func (p BudgetedPermissionPlan) Digest() string {
	b, err := p.Canonical()
	if err != nil {
		return ""
	}
	return domain.WorkflowDigest(b)
}
func (p BudgetedPermissionPlan) Arguments() ([]string, error) {
	if err := p.Validate(); err != nil {
		return nil, err
	}
	a := append(BudgetedArguments(), "--tools", strings.Join(p.Tools, ","))
	for _, e := range p.OriginalRequired {
		a = append(a, "--extension", e.Path)
	}
	return a, nil
}

// ValidateBudgetedStructure is usable without a model call or Docker. Only the
// separate compiled admission policy can permit an actual execution.
func ValidateBudgetedStructure(i domain.RestrictedPiIdentity, p BudgetedPermissionPlan) error {
	if err := i.Validate(); err != nil {
		return err
	}
	if err := p.Validate(); err != nil {
		return err
	}
	d := BudgetedAssetDigests()
	tools, _ := json.Marshal(p.Tools)
	args, _ := p.Arguments()
	argv, _ := json.Marshal(args)
	if i.PermissionExtensions != p.Digest() || i.AllowedTools != domain.WorkflowDigest(tools) || i.Arguments != domain.WorkflowDigest(argv) {
		return errors.New("permission_contract_unsupported: frozen tools, masks or required extension inventory changed")
	}
	if i.Shim != d["extension"] || i.Serializer != d["provider"] || i.Translator != d["provider"] || i.Entry != BudgetedEntryDigest() || i.Relay != d["relay"] {
		return errors.New("restricted source installed closure drift")
	}
	return nil
}

func (p BudgetedPermissionPlan) Clone() BudgetedPermissionPlan {
	p.Tools = slices.Clone(p.Tools)
	p.OriginalRequired = slices.Clone(p.OriginalRequired)
	p.Installed = slices.Clone(p.Installed)
	p.ReadOnlyPaths = slices.Clone(p.ReadOnlyPaths)
	return p
}
