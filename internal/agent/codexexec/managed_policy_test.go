package codexexec

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

func TestPublicConstructionRejectsUnverifiableManagedPolicy(t *testing.T) {
	a := fixture(t, "exit 0\n")
	got, err := New(context.Background(), a.config)
	if got != nil || !errors.Is(err, ErrManagedPolicy) || !errors.Is(err, ErrPolicy) {
		t.Fatalf("%v %v", got, err)
	}
	ready := Inspect(context.Background(), a.config)
	if ready.Ready || ready.Failure != "configuration_unqualified: managed_policy_unverifiable" {
		t.Fatalf("%+v", ready)
	}
}

func TestManagedPolicyRecheckedBeforeBindingAndSpawn(t *testing.T) {
	for _, scenario := range []string{"unavailable", "unreadable", "invalid", "changed"} {
		t.Run(scenario, func(t *testing.T) {
			// Isolated proof-provider injection only. This is not a cloud/MDM probe.
			a := fixture(t, "exit 0\n")
			req := request(t, a)
			prepared, err := a.PrepareBoundStart(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			s, err := trustedhost.New(a.SupervisorConfig(filepath.Join(t.TempDir(), "runtime")))
			if err != nil {
				t.Fatal(err)
			}
			a.managedPolicyCheck = func() error { return errors.New(scenario + ": private policy must not escape") }
			if _, err := a.Binding(context.Background()); !errors.Is(err, ErrManagedPolicy) {
				t.Fatal(err)
			}
			if _, err := a.PrepareBoundStart(context.Background(), req); !errors.Is(err, ErrManagedPolicy) {
				t.Fatal(err)
			}
			got := s.Start(context.Background(), prepared.Invocation, &sink{token: req.LaunchToken, done: make(chan struct{})})
			if got.Kind != execution.StartProvenNoChild {
				t.Fatalf("unqualified policy launched: %+v", got)
			}
		})
	}
}
