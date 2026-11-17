package app

import (
	"context"
	"errors"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
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
