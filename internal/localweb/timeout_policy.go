package localweb

import (
	"time"

	"github.com/Yangyang96/chora/internal/acceptanceauthority"
)

const productionAttemptTimeout = 1200 * time.Second

// acceptanceTimeoutPolicy is typed startup composition, not an ambient policy
// source. The Controller chooses the one exact Standard Attempt deadline; all
// ordinary managed and Trusted Local Attempts retain the production limit.
type acceptanceTimeoutPolicy struct {
	AttemptTimeout time.Duration
	Controller     *acceptanceauthority.Controller
}

func acceptanceTimeoutPolicyFor(controller *acceptanceauthority.Controller) acceptanceTimeoutPolicy {
	return acceptanceTimeoutPolicy{AttemptTimeout: productionAttemptTimeout, Controller: controller}
}
