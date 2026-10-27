package domain

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// RuntimeBindingV1 is additive to the exact legacy Pi profile. It contains
// identities, never executable paths, credentials, or mutable configuration.
type RuntimeBindingV1 struct {
	SchemaVersion            string `json:"schemaVersion"`
	RuntimeID                string `json:"runtimeId"`
	ProtocolVersion          string `json:"protocolVersion"`
	SourceIdentity           string `json:"sourceIdentity"`
	ConfigurationFingerprint string `json:"configurationFingerprint"`
	ExecutionProvider        string `json:"executionProvider"`
	SecurityFingerprint      string `json:"securityFingerprint"`
	CapabilityDigest         string `json:"capabilityDigest"`
	ModelProvenance          string `json:"modelProvenance"`
}

func (r RuntimeBindingV1) Validate() error {
	if r.SchemaVersion != "chora.runtime-binding.v1" || r.RuntimeID == "pi" || r.RuntimeID == "codex" || r.RuntimeID == "fake" || !modelIdentifier(r.RuntimeID) || r.ExecutionProvider != TrustedHostExecutionProvider || r.ProtocolVersion == "" || (r.ModelProvenance != "native_default" && r.ModelProvenance != "explicit") {
		return fmt.Errorf("%w: invalid runtime binding", ErrInvalidArgument)
	}
	for _, v := range []string{r.SourceIdentity, r.ConfigurationFingerprint, r.SecurityFingerprint, r.CapabilityDigest} {
		if len(v) != 64 || strings.Trim(v, "0123456789abcdef") != "" {
			return fmt.Errorf("%w: runtime binding requires SHA-256 identities", ErrInvalidArgument)
		}
	}
	return nil
}
func ParseRuntimeBinding(raw string) (RuntimeBindingV1, error) {
	var r RuntimeBindingV1
	decoder := json.NewDecoder(bytes.NewBufferString(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&r); err != nil {
		return r, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return r, fmt.Errorf("%w: trailing runtime binding", ErrInvalidArgument)
	}
	return r, r.Validate()
}
func NewRuntimeProfileBinding(r RuntimeBindingV1) (AgentExecutionProfileBinding, error) {
	if err := r.Validate(); err != nil {
		return AgentExecutionProfileBinding{}, err
	}
	raw, _ := json.Marshal(r)
	return AgentExecutionProfileBinding{profile: AgentExecutionProfileTrustedLocal, runtimeSource: r.RuntimeID, executionProvider: r.ExecutionProvider, capabilityPolicy: "chora.runtime-native.v1", trustDisclosurePolicy: TrustedLocalDisclosurePolicy, runtimeBindingJSON: string(raw)}, nil
}
func (binding AgentExecutionProfileBinding) RuntimeBindingJSON() string {
	return binding.runtimeBindingJSON
}
func (binding AgentExecutionProfileBinding) Legacy() bool {
	return binding.Bound() && binding.runtimeBindingJSON == ""
}

// RuntimeID returns the exact bound runtime. An unbound profile has no default.
func (binding AgentExecutionProfileBinding) RuntimeID() string {
	if binding.Legacy() {
		return "pi"
	}
	if binding.runtimeBindingJSON == "" {
		return ""
	}
	runtime, err := ParseRuntimeBinding(binding.runtimeBindingJSON)
	if err != nil {
		return ""
	}
	return runtime.RuntimeID
}
