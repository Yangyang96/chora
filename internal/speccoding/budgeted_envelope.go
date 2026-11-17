package speccoding

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
)

const BudgetedCapability = "execution.isolated_budgeted.v1"

func budgetedCapabilities() []string {
	caps := cloneStrings(requiredCapabilitiesLocalConnected)
	for n, c := range caps {
		if c == LocalConnectedNoSandboxCapability {
			caps[n] = BudgetedCapability
		}
	}
	return caps
}
func (e ResourceEnvelope) WithBudgetedExecution(id IsolatedExecutionIdentity) (ResourceEnvelope, error) {
	if !validLowerHex(id.ImageSHA256, 64) || !validLowerHex(id.SourceSHA256, 64) || !validLowerHex(id.PolicySHA256, 64) {
		return ResourceEnvelope{}, fmt.Errorf("%w: incomplete budgeted identity", ErrInvalidCoreContract)
	}
	e.template.Execution.RequiredCapabilities = budgetedCapabilities()
	e.template.Candidate.Runtime.Version = "0.85.1"
	e.template.Candidate.Runtime.Config = ConfigReference{Path: "public-pi-budgeted", SHA256: id.SourceSHA256}
	e.template.Candidate.Sandbox = SandboxCandidate{Provider: "docker", Engine: "qualified", Version: "isolated-budgeted.v1", SHA256: id.ImageSHA256}
	e.template.Candidate.Policy = ConfigReference{Path: "isolated-budgeted.v1", SHA256: id.PolicySHA256}
	e.template.Candidate.Model = ModelCandidate{Provider: "deepseek", ModelID: "deepseek-v4-pro", IdentityStatus: "frozen_catalog", AuthenticationType: "go_budget_channel"}
	b, _ := json.Marshal(e.template)
	e.templateDigest = sha256.Sum256(b)
	return e, nil
}
func validateBudgetedCandidate(c CandidateCombination) error {
	if c.Runtime.Package != "@earendil-works/pi-coding-agent" || c.Runtime.Version != "0.85.1" || c.Runtime.Config.Path != "public-pi-budgeted" || !validLowerHex(c.Runtime.Config.SHA256, 64) || c.Sandbox.Provider != "docker" || c.Sandbox.Engine != "qualified" || c.Sandbox.Version != "isolated-budgeted.v1" || !validLowerHex(c.Sandbox.SHA256, 64) || c.Sandbox.EngineVersion != "" || c.Sandbox.EngineSHA256 != "" || c.Policy.Path != "isolated-budgeted.v1" || !validLowerHex(c.Policy.SHA256, 64) || len(c.BaseImages) != 0 || c.Model.Provider != "deepseek" || c.Model.ModelID != "deepseek-v4-pro" || c.Model.IdentityStatus != "frozen_catalog" || c.Model.AuthenticationType != "go_budget_channel" || c.Model.Endpoint != "" || c.Model.ModelDigest != "" {
		return fmt.Errorf("%w: budgeted candidate drift", ErrInvalidCoreContract)
	}
	return nil
}
