package app

import (
	"context"
	"errors"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

func TestProductionTransportRefusesUnprovenBoundBeforeStoreOrNetwork(t *testing.T) {
	transport, err := NewWorkflowChatTransport(&Service{}, "synthetic-noncredential")
	if err != nil {
		t.Fatal(err)
	}
	_, err = transport.Dispatch(context.Background(), "unused", domain.RunID{}, domain.AttemptID{}, "one", []byte(`{"model":"deepseek-v4-pro"}`))
	if !errors.Is(err, ErrBoundUnproven) {
		t.Fatalf("got %v", err)
	}
}

func TestCredentialFreeRestrictedStartupAuthorityRefusesBeforeAnyStore(t *testing.T) {
	p := NewRestrictedExecutionAuthority(&Service{})
	if err := p.Prepare(context.Background(), execution.StartRequest{}); !errors.Is(err, ErrBoundUnproven) {
		t.Fatal(err)
	}
	if _, err := p.Bind(context.Background(), execution.Invocation{}); !errors.Is(err, ErrBoundUnproven) {
		t.Fatal(err)
	}
	if err := p.Admit(context.Background(), domain.RestrictedLaunch{}); !errors.Is(err, ErrBoundUnproven) {
		t.Fatal(err)
	}
	if err := p.Lease(context.Background(), domain.RestrictedLaunch{}); !errors.Is(err, ErrBoundUnproven) {
		t.Fatal(err)
	}
	if _, err := p.Dispatch(context.Background(), domain.RestrictedLaunch{}, "one", []byte(`{}`)); !errors.Is(err, ErrBoundUnproven) {
		t.Fatal(err)
	}
}
