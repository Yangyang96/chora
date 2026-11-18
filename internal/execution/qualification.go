package execution

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"sync"
)

// QualificationState describes evidence, not a version-number ordering.
type QualificationState string

const (
	QualificationPending  QualificationState = "pending"
	QualificationVerified QualificationState = "verified"
	QualificationRejected QualificationState = "rejected"
)

// QualificationIdentity binds evidence to the exact executable closure, observed
// version, adapter protocol, configuration and security policy. SourceIdentity
// must include resolved paths and executable content, not just the display version.
// Digests keep machine paths and configuration contents out of diagnostics.
type QualificationIdentity struct {
	RuntimeID, Version, ProtocolVersion                     string
	SourceIdentity, ConfigurationIdentity, SecurityIdentity [32]byte
}

func (i QualificationIdentity) valid() bool {
	return i.RuntimeID != "" && i.Version != "" && i.ProtocolVersion != "" &&
		i.SourceIdentity != ([32]byte{}) && i.ConfigurationIdentity != ([32]byte{}) && i.SecurityIdentity != ([32]byte{})
}

func (i QualificationIdentity) Digest() [32]byte {
	if !i.valid() {
		return [32]byte{}
	}
	raw, _ := json.Marshal(i)
	return sha256.Sum256(raw)
}

// CapabilityEvidence is produced by a provider's controlled probes and reviewed
// protocol implementation. Reference is a public source or diagnostic code, never
// raw native output. Static, fixture and native observations remain distinguishable.
// This type does not certify a probe or authorize execution by itself.
type CapabilityEvidence struct {
	Capability string
	State      QualificationState
	Method     string
	Reference  string
}

type QualificationReport struct {
	Identity QualificationIdentity
	State    QualificationState
	Evidence []CapabilityEvidence
}

var ErrQualificationEvidence = errors.New("invalid_qualification_evidence")

// EvaluateQualification requires exactly one observation for every required
// capability. Missing observations stay pending; a rejection wins over pending.
// Runtime-specific policy freshness must still be rechecked at binding and spawn.
func EvaluateQualification(identity QualificationIdentity, required []string, evidence []CapabilityEvidence) (QualificationReport, error) {
	if !identity.valid() || len(required) == 0 {
		return QualificationReport{}, ErrQualificationEvidence
	}
	observed := make(map[string]CapabilityEvidence, len(evidence))
	for _, e := range evidence {
		if e.Capability == "" || e.Method == "" || e.Reference == "" ||
			(e.State != QualificationPending && e.State != QualificationVerified && e.State != QualificationRejected) {
			return QualificationReport{}, ErrQualificationEvidence
		}
		if _, duplicate := observed[e.Capability]; duplicate {
			return QualificationReport{}, ErrQualificationEvidence
		}
		observed[e.Capability] = e
	}
	r := QualificationReport{Identity: identity, State: QualificationVerified}
	seen := map[string]bool{}
	for _, capability := range required {
		if capability == "" || seen[capability] {
			return QualificationReport{}, ErrQualificationEvidence
		}
		seen[capability] = true
		e, ok := observed[capability]
		if !ok {
			e = CapabilityEvidence{Capability: capability, State: QualificationPending, Method: "none", Reference: "evidence_required"}
		}
		r.Evidence = append(r.Evidence, e)
		if e.State == QualificationRejected || (e.State == QualificationPending && r.State != QualificationRejected) {
			r.State = e.State
		}
	}
	return r, nil
}

// QualificationCache is an optional single-entry, in-memory cache per provider.
// A different identity evicts the old report (including a rejected report).
// It is not a persisted approval store or a substitute for live policy checks.
// Providers own the cache and observations; no product configuration imports proof.
type QualificationCache struct {
	mu     sync.Mutex
	report QualificationReport
}

func (c *QualificationCache) Store(identity QualificationIdentity, required []string, evidence []CapabilityEvidence) (QualificationReport, error) {
	r, err := EvaluateQualification(identity, required, evidence)
	if err != nil {
		c.mu.Lock()
		c.report = QualificationReport{}
		c.mu.Unlock()
		return QualificationReport{}, err
	}
	c.mu.Lock()
	c.report = cloneQualification(r)
	c.mu.Unlock()
	return r, nil
}

func (c *QualificationCache) Lookup(identity QualificationIdentity) (QualificationReport, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !identity.valid() || c.report.Identity != identity {
		c.report = QualificationReport{}
		return QualificationReport{}, false
	}
	return cloneQualification(c.report), true
}

func cloneQualification(r QualificationReport) QualificationReport {
	r.Evidence = append([]CapabilityEvidence(nil), r.Evidence...)
	return r
}
