package app

import (
	"context"
	"errors"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/speccoding"
)

var (
	errOrdinaryResourceEnvelope = errors.New("ordinary resource envelope selected")
	errIsolatedResourceEnvelope = errors.New("isolated resource envelope selected")
)

type resourceEnvelopeSelectionResolver struct{}

func (resourceEnvelopeSelectionResolver) ResolveSpecCodingEnvelope(context.Context, domain.Room) (SpecCodingEnvelope, error) {
	return nil, errors.New("room envelope selected")
}

func (resourceEnvelopeSelectionResolver) ResolveResourceSpecCodingEnvelope(context.Context, domain.Room, domain.TaskResourceSnapshot) (SpecCodingEnvelope, error) {
	return nil, errOrdinaryResourceEnvelope
}

func (resourceEnvelopeSelectionResolver) ResolveIsolatedResourceSpecCodingEnvelope(context.Context, domain.Room, domain.TaskResourceSnapshot) (SpecCodingEnvelope, error) {
	return nil, errIsolatedResourceEnvelope
}

func TestResourceEnvelopeSelectionUsesIsolatedResolverOnlyForPersistedIsolatedProfile(t *testing.T) {
	service := NewService(Dependencies{SpecCodingEnvelopeResolver: resourceEnvelopeSelectionResolver{}})
	_, err := service.resolveResourceSpecCodingEnvelope(context.Background(), domain.Room{}, domain.TaskResourceSnapshot{}, domain.AgentExecutionProfileIsolatedLocal)
	if !errors.Is(err, errIsolatedResourceEnvelope) {
		t.Fatalf("isolated profile resolver error = %v", err)
	}
	_, err = service.resolveResourceSpecCodingEnvelope(context.Background(), domain.Room{}, domain.TaskResourceSnapshot{}, domain.AgentExecutionProfileTrustedLocal)
	if !errors.Is(err, errOrdinaryResourceEnvelope) {
		t.Fatalf("Trusted Local resolver error = %v", err)
	}
}

func TestResourceEnvelopeSelectionFailsClosedWithoutIsolatedResolver(t *testing.T) {
	service := NewService(Dependencies{SpecCodingEnvelopeResolver: ordinaryResourceEnvelopeResolver{}})
	_, err := service.resolveResourceSpecCodingEnvelope(context.Background(), domain.Room{}, domain.TaskResourceSnapshot{}, domain.AgentExecutionProfileIsolatedLocal)
	if !errors.Is(err, speccoding.ErrInvalidInstalledEnvelope) {
		t.Fatalf("missing isolated resolver error = %v", err)
	}
}

type ordinaryResourceEnvelopeResolver struct{}

func (ordinaryResourceEnvelopeResolver) ResolveSpecCodingEnvelope(context.Context, domain.Room) (SpecCodingEnvelope, error) {
	return nil, nil
}

func (ordinaryResourceEnvelopeResolver) ResolveResourceSpecCodingEnvelope(context.Context, domain.Room, domain.TaskResourceSnapshot) (SpecCodingEnvelope, error) {
	return nil, nil
}
