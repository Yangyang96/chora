package speccoding

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

const IsolatedLocalCapability = "execution.isolated_local.v1"

type IsolatedExecutionIdentity struct {
	ImageSHA256  string
	SourceSHA256 string
	PolicySHA256 string
}

func isolatedCapabilities() []string {
	caps := cloneStrings(requiredCapabilitiesLocalConnected)
	for i, c := range caps {
		if c == LocalConnectedNoSandboxCapability {
			caps[i] = IsolatedLocalCapability
		}
	}
	return caps
}

func (e ResourceEnvelope) WithIsolatedExecution(id IsolatedExecutionIdentity) (ResourceEnvelope, error) {
	if !validLowerHex(id.ImageSHA256, 64) || !validLowerHex(id.SourceSHA256, 64) || !validLowerHex(id.PolicySHA256, 64) {
		return ResourceEnvelope{}, fmt.Errorf("%w: incomplete isolated identity", ErrInvalidCoreContract)
	}
	e.template.Execution.RequiredCapabilities = isolatedCapabilities()
	e.template.Candidate.Runtime.Version = "0.85.1"
	e.template.Candidate.Runtime.Config = ConfigReference{Path: "public-pi-image", SHA256: id.SourceSHA256}
	e.template.Candidate.Sandbox = SandboxCandidate{Provider: "docker", Engine: "colima", Version: "isolated-local.v1", SHA256: id.ImageSHA256}
	e.template.Candidate.Policy = ConfigReference{Path: "isolated-local.v1", SHA256: id.PolicySHA256}
	e.template.Candidate.Model = ModelCandidate{Provider: "deepseek", ModelID: "deepseek-v4-flash", IdentityStatus: "observed_at_runtime", AuthenticationType: "task_scoped_api_key"}
	b, _ := json.Marshal(e.template)
	e.templateDigest = sha256.Sum256(b)
	return e, nil
}

func validateIsolatedCandidate(c CandidateCombination) error {
	if c.Runtime.Package != "@earendil-works/pi-coding-agent" || c.Runtime.Version != "0.85.1" || c.Runtime.Config.Path != "public-pi-image" || !validLowerHex(c.Runtime.Config.SHA256, 64) ||
		c.Sandbox.Provider != "docker" || c.Sandbox.Engine != "colima" || c.Sandbox.Version != "isolated-local.v1" || !validLowerHex(c.Sandbox.SHA256, 64) || c.Sandbox.EngineVersion != "" || c.Sandbox.EngineSHA256 != "" ||
		c.Policy.Path != "isolated-local.v1" || !validLowerHex(c.Policy.SHA256, 64) || len(c.BaseImages) != 0 ||
		c.Model.Provider != "deepseek" || c.Model.ModelID != "deepseek-v4-flash" || c.Model.IdentityStatus != "observed_at_runtime" || c.Model.AuthenticationType != "task_scoped_api_key" || c.Model.Endpoint != "" || c.Model.ModelDigest != "" {
		return fmt.Errorf("%w: isolated candidate drift", ErrInvalidCoreContract)
	}
	return nil
}
