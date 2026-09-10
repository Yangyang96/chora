package localweb

import (
	"context"
	"errors"
	"fmt"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/pidiscovery"
	"github.com/Yangyang96/chora/internal/speccoding"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

// boundSpecCodingEnvelopeResolver resolves a Real Spec Coding envelope per Room.
// A Room with a RepositoryBinding gets a BoundEnvelope bound to that repository
// (the Local Connected path). Only positively classified legacy standalone
// Rooms retain the installed M1 envelope. Missing Project ownership fails closed.
type boundSpecCodingEnvelopeResolver struct {
	reader    storecontract.Reader
	installed *speccoding.InstalledEnvelope
	piVersion string
}

func newBoundSpecCodingEnvelopeResolver(reader storecontract.Reader, installed *speccoding.InstalledEnvelope, discovery pidiscovery.Result) *boundSpecCodingEnvelopeResolver {
	version := ""
	if discovery.State == pidiscovery.StateReady {
		version = discovery.Version
	}
	return &boundSpecCodingEnvelopeResolver{reader: reader, installed: installed, piVersion: version}
}

func (resolver *boundSpecCodingEnvelopeResolver) ResolveSpecCodingEnvelope(ctx context.Context, room domain.Room) (app.SpecCodingEnvelope, error) {
	binding, err := resolver.reader.GetRepositoryBinding(ctx, room.ID())
	if errors.Is(err, storecontract.ErrNotFound) {
		if err := requireQualifiedLegacyRoom(ctx, resolver.reader, room.ID()); err != nil {
			return nil, err
		}
		if resolver.installed == nil {
			return nil, fmt.Errorf("%w: Room has no repository binding and no installed envelope", speccoding.ErrInvalidInstalledEnvelope)
		}
		return resolver.installed, nil
	}
	if err != nil {
		return nil, err
	}
	if resolver.piVersion == "" {
		return nil, fmt.Errorf("%w: a compatible PATH Pi is required to start a Local Connected Task", speccoding.ErrInvalidInstalledEnvelope)
	}
	revision, _, _, err := currentTaskBase(ctx, binding.LocalLocator())
	if err != nil {
		return nil, err
	}
	if binding.State() != domain.RepositoryBindingStateActive {
		return nil, fmt.Errorf("%w: Project is removed", speccoding.ErrInvalidInstalledEnvelope)
	}
	return speccoding.NewBoundEnvelope(binding.LocalLocator(), speccoding.RepositoryIdentity{
		Name:           binding.Name(),
		SourceRevision: revision,
	}, resolver.piVersion)
}

func (resolver *boundSpecCodingEnvelopeResolver) ResolveTaskSpecCodingEnvelope(ctx context.Context, room domain.Room, base domain.TaskWorktreeBinding) (app.SpecCodingEnvelope, error) {
	project, err := resolver.reader.GetRepositoryBinding(ctx, room.ID())
	if errors.Is(err, storecontract.ErrNotFound) {
		if err := requireQualifiedLegacyRoom(ctx, resolver.reader, room.ID()); err != nil {
			return nil, err
		}
		if resolver.installed == nil {
			return nil, speccoding.ErrInvalidInstalledEnvelope
		}
		return resolver.installed, nil
	}
	if err != nil {
		return nil, err
	}
	project, err = repositoryForTaskBase(ctx, project, base)
	if err != nil {
		return nil, err
	}
	return speccoding.NewBoundEnvelope(project.LocalLocator(), speccoding.RepositoryIdentity{Name: project.Name(), SourceRevision: base.PinnedBaseRevision()}, resolver.piVersion)
}

func (resolver *boundSpecCodingEnvelopeResolver) ResolveResourceSpecCodingEnvelope(ctx context.Context, room domain.Room, snapshot domain.TaskResourceSnapshot) (app.SpecCodingEnvelope, error) {
	if room.OwnershipKind() != domain.RoomOwnershipProject || snapshot.RoomID != room.ID().String() || snapshot.ProjectID != room.ProjectID().String() || resolver.piVersion == "" {
		return nil, speccoding.ErrInvalidInstalledEnvelope
	}
	return speccoding.NewResourceEnvelope(room.WorkspaceRoot(), snapshot, resolver.piVersion)
}
