package domain

import (
	"fmt"
	"math/big"
	"time"
)

// WorkflowPriceSchedule is a PROPOSAL for an exact, externally evidenced billing
// contract. Arithmetic never certifies that contract. Cached input must use the
// most expensive applicable rate; all fixed/tool/error fees must be included.
type WorkflowPriceSchedule struct {
	Reference, EvidenceSHA256, Currency                string
	ValidUntil                                         time.Time
	InputPerMillion, OutputPerMillion, FixedPerRequest uint64
	AllFeesCovered                                     bool
}

// WorkflowCostCeiling returns integer minor currency units, rounded UP. Unknown
// coverage/expired evidence/overflow are refusals; zero is never an unknown price.
func WorkflowCostCeiling(p WorkflowPiAuthorization, schedule WorkflowPriceSchedule, now time.Time) (uint64, error) {
	if p.Validate() != nil || !CanonicalSHA256(schedule.EvidenceSHA256) || !validExecutionProfileActor(schedule.Reference) || !validExecutionProfileActor(schedule.Currency) || !schedule.AllFeesCovered || schedule.ValidUntil.Before(p.Deadline) || !now.Before(schedule.ValidUntil) {
		return 0, fmt.Errorf("%w: billing bound unproven or expired", ErrInvalidArgument)
	}
	// Input and output share MaxTokens. This deliberately over-reserves both
	// ceilings rather than optimizing the allocation of unknown future calls.
	i := new(big.Int).Mul(new(big.Int).SetUint64(uint64(p.MaxTokens)), new(big.Int).SetUint64(schedule.InputPerMillion))
	o := new(big.Int).Mul(new(big.Int).SetUint64(uint64(p.MaxOutputTokens)), new(big.Int).SetUint64(schedule.OutputPerMillion))
	i.Add(i, o)
	i.Add(i, big.NewInt(999999))
	i.Div(i, big.NewInt(1000000))
	fixed := new(big.Int).Mul(new(big.Int).SetUint64(uint64(p.MaxRequests)), new(big.Int).SetUint64(schedule.FixedPerRequest))
	i.Add(i, fixed)
	if !i.IsUint64() {
		return 0, fmt.Errorf("%w: billing bound overflow", ErrInvalidArgument)
	}
	return i.Uint64(), nil
}
