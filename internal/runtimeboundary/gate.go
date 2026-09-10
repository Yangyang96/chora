package runtimeboundary

type GateResult struct {
	RealAdapterGate         string `json:"real_adapter_gate"`
	HostReadIsolation       string `json:"host_read_isolation"`
	WorkspaceWriteIsolation string `json:"workspace_write_isolation"`
	ControlPlaneIsolation   string `json:"control_plane_isolation"`
	LoopbackIsolation       string `json:"loopback_isolation"`
}

func EvaluateGate(report ProbeReport) GateResult {
	result := GateResult{
		RealAdapterGate:         "FAIL",
		HostReadIsolation:       "unavailable",
		WorkspaceWriteIsolation: status(report.OutsideWriteDenied && report.WorkspaceReadWriteAllowed),
		ControlPlaneIsolation:   status(report.ControlPlaneDenied),
		LoopbackIsolation:       status(report.LoopbackDenied),
	}
	if result.WorkspaceWriteIsolation == "proven" && result.ControlPlaneIsolation == "proven" && result.LoopbackIsolation == "proven" && report.RuntimeHomeReadAllowed {
		result.RealAdapterGate = "PASS"
	}
	return result
}

func status(proven bool) string {
	if proven {
		return "proven"
	}
	return "unavailable"
}
