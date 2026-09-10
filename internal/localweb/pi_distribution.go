package localweb

import (
	"context"
	"errors"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/pidistribution"
)

func localPiSourceFromSelection(selection pidistribution.Selection) (agentpi.LocalPiSource, error) {
	if !selection.Configured() {
		return agentpi.LocalPiSource{}, errors.New("Pi distribution selection is not configured")
	}
	data := selection.LocalPiSourceData()
	return agentpi.NewLocalPiSource(agentpi.LocalPiSourceParams{
		ExecutablePath:         data.ExecutablePath,
		ResolvedExecutablePath: data.ResolvedPath,
		PackageRoot:            data.PackageRoot,
		RuntimeVersion:         data.RuntimeVersion,
		ExecutableSHA256:       data.ExecutableSHA256,
		ClosureSHA256:          data.ClosureSHA256,
	})
}

func localPiSelectionValidator(resolver *pidistribution.Resolver, selection pidistribution.Selection) func(context.Context, agentpi.LocalPiSource) error {
	return func(ctx context.Context, source agentpi.LocalPiSource) error {
		if resolver == nil || !selection.Configured() {
			return errors.New("Pi distribution selection validator is unavailable")
		}
		data := selection.LocalPiSourceData()
		if source.ExecutablePath() != data.ExecutablePath || source.ResolvedExecutablePath() != data.ResolvedPath ||
			source.PackageRoot() != data.PackageRoot || source.RuntimeVersion() != data.RuntimeVersion ||
			source.ExecutableSHA256() != data.ExecutableSHA256 || source.ClosureSHA256() != data.ClosureSHA256 {
			return errors.New("Pi local source no longer matches the frozen distribution selection")
		}
		return resolver.Revalidate(ctx, selection)
	}
}

func localPiSnapshotValidator(snapshot pidistribution.LocalPiSnapshot) func(context.Context, agentpi.LocalPiSource) error {
	return func(ctx context.Context, source agentpi.LocalPiSource) error {
		selection := snapshot.Selection()
		if !selection.Configured() {
			return errors.New("Pi distribution snapshot validator is unavailable")
		}
		data := snapshot.LocalPiSourceData()
		if source.ExecutablePath() != data.ExecutablePath || source.ResolvedExecutablePath() != data.ResolvedPath ||
			source.PackageRoot() != data.PackageRoot || source.RuntimeVersion() != data.RuntimeVersion ||
			source.ExecutableSHA256() != data.ExecutableSHA256 || source.ClosureSHA256() != data.ClosureSHA256 {
			return errors.New("Pi local source no longer matches the immutable Runtime snapshot")
		}
		_, err := snapshot.Revalidate()
		return err
	}
}
