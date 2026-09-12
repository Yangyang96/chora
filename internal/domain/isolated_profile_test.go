package domain

import "testing"

func TestIsolatedLocalBindingNeverRestoresAsHost(t *testing.T) {
	binding, err := NewAgentExecutionProfileBinding(AgentExecutionProfile("isolated_local"))
	if err != nil {
		t.Fatalf("one public isolated profile must be supported: %v", err)
	}
	if binding.ExecutionProvider() != DockerExecutionProvider || binding.RuntimeSource() != "public_pi_image" || binding.TrustDisclosurePolicy() != "" {
		t.Fatalf("isolated binding has host authority: %#v", binding.Record())
	}
	r := binding.Record()
	r.ExecutionProvider = TrustedHostExecutionProvider
	if _, err := RestoreAgentExecutionProfileBinding(r); err == nil {
		t.Fatal("persisted isolated authority silently downgraded to host")
	}
}
