package execution

import (
	"fmt"
	"strings"
	"unicode"
)

// ExecutionTarget is the exact immutable pairing of a Runtime adapter with the
// process provider that is allowed to execute its Invocation. Its private,
// string-only representation keeps it comparable for exact registry lookups.
type ExecutionTarget struct {
	adapterID  string
	providerID string
}

func NewExecutionTarget(adapterID, providerID string) (ExecutionTarget, error) {
	if !validExecutionTargetID(adapterID) || !validExecutionTargetID(providerID) {
		return ExecutionTarget{}, fmt.Errorf("execution target requires exact nonempty adapter and provider IDs")
	}
	return ExecutionTarget{adapterID: adapterID, providerID: providerID}, nil
}

func validExecutionTargetID(value string) bool {
	return value != "" && strings.TrimSpace(value) == value && strings.IndexFunc(value, func(r rune) bool {
		return unicode.IsControl(r) || unicode.IsSpace(r)
	}) == -1
}

func (target ExecutionTarget) Valid() bool {
	return validExecutionTargetID(target.adapterID) && validExecutionTargetID(target.providerID)
}

func (target ExecutionTarget) AdapterID() string  { return target.adapterID }
func (target ExecutionTarget) ProviderID() string { return target.providerID }

// StartPreparation atomically binds the one Invocation that may be started to
// the exact Runtime fingerprint prepared for it.
type StartPreparation struct {
	Invocation         Invocation
	RuntimeFingerprint RuntimeFingerprint
}

func NewStartPreparation(invocation Invocation, fingerprint RuntimeFingerprint) (StartPreparation, error) {
	preparation := StartPreparation{Invocation: invocation, RuntimeFingerprint: fingerprint}
	if !preparation.Valid() {
		return StartPreparation{}, fmt.Errorf("invalid bound start preparation")
	}
	return preparation, nil
}

func (preparation StartPreparation) Valid() bool {
	return preparation.Invocation.Valid() && preparation.RuntimeFingerprint.Valid()
}
