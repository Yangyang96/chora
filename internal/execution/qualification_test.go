package execution

import "testing"

func TestQualificationRequiresCompleteUnambiguousEvidence(t *testing.T) {
	i := QualificationIdentity{RuntimeID: "fixture", Version: "1", ProtocolVersion: "jsonl.v1", SourceIdentity: [32]byte{1}, ConfigurationIdentity: [32]byte{2}, SecurityIdentity: [32]byte{3}}
	e := CapabilityEvidence{Capability: "events", State: QualificationVerified, Method: "fixture", Reference: "synthetic"}
	r, err := EvaluateQualification(i, []string{"events", "policy"}, []CapabilityEvidence{e})
	if err != nil || r.State != QualificationPending || r.Evidence[1].Reference != "evidence_required" {
		t.Fatalf("missing evidence granted qualification: %+v %v", r, err)
	}
	policy := CapabilityEvidence{Capability: "policy", State: QualificationRejected, Method: "live", Reference: "unverifiable"}
	r, err = EvaluateQualification(i, []string{"events", "policy", "cancel"}, []CapabilityEvidence{e, policy})
	if err != nil || r.State != QualificationRejected {
		t.Fatal("pending obscured rejection", r, err)
	}
	for _, evidence := range [][]CapabilityEvidence{{e, e}, {{Capability: "events", State: "success", Method: "fixture", Reference: "invalid"}}, {{Capability: "events", State: QualificationVerified}}} {
		if _, err := EvaluateQualification(i, []string{"events"}, evidence); err == nil {
			t.Fatal("invalid evidence accepted")
		}
	}
	if _, err := EvaluateQualification(i, []string{"events", "events"}, nil); err == nil {
		t.Fatal("duplicate requirement accepted")
	}
}

func TestQualificationCacheEvictsOnEveryIdentityDimension(t *testing.T) {
	i := QualificationIdentity{RuntimeID: "fixture", Version: "1", ProtocolVersion: "jsonl.v1", SourceIdentity: [32]byte{1}, ConfigurationIdentity: [32]byte{2}, SecurityIdentity: [32]byte{3}}
	for _, field := range []string{"runtime", "version", "protocol", "source", "configuration", "security"} {
		t.Run(field, func(t *testing.T) {
			var cache QualificationCache
			evidence := []CapabilityEvidence{{Capability: "events", State: QualificationVerified, Method: "fixture", Reference: "synthetic"}}
			r, err := cache.Store(i, []string{"events"}, evidence)
			if err != nil || r.State != QualificationVerified {
				t.Fatal(r, err)
			}
			evidence[0].State = QualificationRejected
			r.Evidence[0].State = QualificationRejected
			r, ok := cache.Lookup(i)
			if !ok || r.State != QualificationVerified || r.Evidence[0].State != QualificationVerified {
				t.Fatal("aliased cached proof")
			}
			r.Evidence[0].State = QualificationRejected
			if got, _ := cache.Lookup(i); got.Evidence[0].State != QualificationVerified {
				t.Fatal("lookup exposes mutable proof")
			}
			changed := i
			switch field {
			case "runtime":
				changed.RuntimeID += "new"
			case "version":
				changed.Version += "new"
			case "protocol":
				changed.ProtocolVersion += "new"
			case "source":
				changed.SourceIdentity[0]++
			case "configuration":
				changed.ConfigurationIdentity[0]++
			case "security":
				changed.SecurityIdentity[0]++
			}
			if changed.Digest() == i.Digest() {
				t.Fatal("identity dimension omitted")
			}
			if _, ok := cache.Lookup(changed); ok {
				t.Fatal("stale qualification reused")
			}
			if _, ok := cache.Lookup(i); ok {
				t.Fatal("evicted proof resurrected")
			}
		})
	}
	var cache QualificationCache
	_, _ = cache.Store(i, []string{"events"}, nil)
	if _, err := cache.Store(QualificationIdentity{}, []string{"events"}, nil); err == nil {
		t.Fatal("empty identity accepted")
	}
	if _, ok := cache.Lookup(i); ok {
		t.Fatal("failed replacement retained old evidence")
	}
}
