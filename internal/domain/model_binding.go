package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
)

// ModelCatalog contains only identity metadata declared by the coding runtime.
// Discovery adapters must discard credentials, endpoints and raw responses.
type ModelIdentity struct {
	Provider string `json:"provider"`
	ModelID  string `json:"modelId"`
}
type ModelCatalog struct {
	AgentID         string          `json:"agentId"`
	RuntimeIdentity string          `json:"runtimeIdentity"`
	RuntimeVersion  string          `json:"runtimeVersion"`
	Models          []ModelIdentity `json:"models"`
	Digest          string          `json:"digest"`
}

func modelIdentifier(s string) bool {
	return s != "" && s == strings.TrimSpace(s) && len(s) <= 256 && !strings.ContainsFunc(s, unicode.IsControl)
}
func NewModelCatalog(agent, identity, version string, models []ModelIdentity) (ModelCatalog, error) {
	if !modelIdentifier(agent) || !modelIdentifier(identity) || !modelIdentifier(version) || len(models) == 0 || len(models) > 10000 {
		return ModelCatalog{}, fmt.Errorf("%w: model catalog unavailable", ErrInvalidArgument)
	}
	c := ModelCatalog{AgentID: agent, RuntimeIdentity: identity, RuntimeVersion: version, Models: append([]ModelIdentity(nil), models...)}
	sort.Slice(c.Models, func(i, j int) bool {
		if c.Models[i].Provider == c.Models[j].Provider {
			return c.Models[i].ModelID < c.Models[j].ModelID
		}
		return c.Models[i].Provider < c.Models[j].Provider
	})
	for i, m := range c.Models {
		if !modelIdentifier(m.Provider) || !modelIdentifier(m.ModelID) || (i > 0 && m == c.Models[i-1]) {
			return ModelCatalog{}, fmt.Errorf("%w: invalid model catalog entry", ErrInvalidArgument)
		}
	}
	b, _ := json.Marshal(c)
	sum := sha256.Sum256(b)
	c.Digest = hex.EncodeToString(sum[:])
	return c, nil
}
func (c ModelCatalog) Validate() error {
	expected, err := NewModelCatalog(c.AgentID, c.RuntimeIdentity, c.RuntimeVersion, c.Models)
	if err != nil {
		return err
	}
	if c.Digest != expected.Digest {
		return fmt.Errorf("%w: model catalog digest mismatch", ErrInvalidArgument)
	}
	return nil
}
func (c ModelCatalog) Contains(m ModelIdentity) bool {
	for _, candidate := range c.Models {
		if candidate == m {
			return true
		}
	}
	return false
}

type ModelBindingRecord struct {
	Catalog ModelCatalog `json:"catalog"`
	ModelIdentity
	SelectedAt time.Time `json:"selectedAt"`
	Source     string    `json:"source"`
}

// ModelBinding stores canonical JSON privately so copies cannot mutate history.
// Zero denotes a legacy, unassessed selection, never a default model.
type ModelBinding struct{ canonical string }

func NewModelBinding(c ModelCatalog, m ModelIdentity, at time.Time) (ModelBinding, error) {
	if err := c.Validate(); err != nil {
		return ModelBinding{}, err
	}
	if !c.Contains(m) || at.IsZero() {
		return ModelBinding{}, fmt.Errorf("%w: unsupported model choice", ErrInvalidArgument)
	}
	normalized, _ := NewModelCatalog(c.AgentID, c.RuntimeIdentity, c.RuntimeVersion, c.Models)
	b, _ := json.Marshal(ModelBindingRecord{Catalog: normalized, ModelIdentity: m, SelectedAt: at.UTC(), Source: "coding_agent"})
	return ModelBinding{canonical: string(b)}, nil
}
func ParseModelBinding(raw string) (ModelBinding, error) {
	if raw == "" || raw == "null" {
		return ModelBinding{}, nil
	}
	var r ModelBindingRecord
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return ModelBinding{}, fmt.Errorf("%w: invalid model binding", ErrInvalidArgument)
	}
	if r.Source != "coding_agent" {
		return ModelBinding{}, fmt.Errorf("%w: invalid model binding source", ErrInvalidArgument)
	}
	return NewModelBinding(r.Catalog, r.ModelIdentity, r.SelectedAt)
}
func (b ModelBinding) Configured() bool { return b.canonical != "" }
func (b ModelBinding) JSON() string     { return b.canonical }
func (b ModelBinding) Record() ModelBindingRecord {
	var r ModelBindingRecord
	_ = json.Unmarshal([]byte(b.canonical), &r)
	return r
}
func (b ModelBinding) MarshalJSON() ([]byte, error) {
	if !b.Configured() {
		return []byte("null"), nil
	}
	return []byte(b.canonical), nil
}
func (b *ModelBinding) UnmarshalJSON(raw []byte) error {
	parsed, err := ParseModelBinding(string(raw))
	if err == nil {
		*b = parsed
	}
	return err
}

// ValidateCatalog never accepts a stale cache or substitutes another runtime.
func (b ModelBinding) ValidateCatalog(c ModelCatalog) error {
	if err := c.Validate(); err != nil {
		return err
	}
	r := b.Record()
	if !b.Configured() || r.Catalog.AgentID != c.AgentID || r.Catalog.RuntimeIdentity != c.RuntimeIdentity || r.Catalog.RuntimeVersion != c.RuntimeVersion || !c.Contains(r.ModelIdentity) {
		return fmt.Errorf("%w: model_binding_drift", ErrInvalidArgument)
	}
	return nil
}
