package runtimeboundary

import "testing"

func TestEvaluateGatePassesOnlyWhenRequiredIsolationIsProven(t *testing.T) {
	passing := ProbeReport{
		ForbiddenReadDenied:       true,
		ControlPlaneDenied:        true,
		LoopbackDenied:            true,
		OutsideWriteDenied:        true,
		WorkspaceReadWriteAllowed: true,
		RuntimeHomeReadAllowed:    true,
	}
	if got := EvaluateGate(passing); got.RealAdapterGate != "PASS" {
		t.Fatalf("RealAdapterGate = %q, want PASS", got.RealAdapterGate)
	}
	if got := EvaluateGate(passing); got.HostReadIsolation != "unavailable" {
		t.Fatalf("HostReadIsolation = %q, want unavailable from a single decoy observation", got.HostReadIsolation)
	}

	tests := []struct {
		name   string
		mutate func(*ProbeReport)
	}{
		{"workspace write", func(report *ProbeReport) { report.OutsideWriteDenied = false }},
		{"control plane", func(report *ProbeReport) { report.ControlPlaneDenied = false }},
		{"loopback", func(report *ProbeReport) { report.LoopbackDenied = false }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			report := passing
			test.mutate(&report)
			if got := EvaluateGate(report); got.RealAdapterGate != "FAIL" {
				t.Fatalf("RealAdapterGate = %q, want FAIL", got.RealAdapterGate)
			}
		})
	}
}
