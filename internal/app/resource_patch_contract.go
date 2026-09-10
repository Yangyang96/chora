package app

import (
	"context"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

type ResourceReviewPatch struct {
	RepoID   string
	Patch    ReviewablePatch
	Artifact execution.WorkspaceArtifact
}
type ResourceReviewPatchMaterializer interface {
	Materialize(context.Context, domain.TaskID, domain.AttemptID, string) ([]ResourceReviewPatch, error)
	Read(context.Context, domain.AttemptID, string, [32]byte) ([]byte, error)
}

// ResourcePatchApplicationTarget binds each write to its frozen physical target.
type ResourcePatchApplicationTarget interface {
	Inspect(context.Context, domain.TaskRepositoryResource, PatchTargetRequest) (PatchTargetInspection, error)
	Apply(context.Context, domain.TaskRepositoryResource, PatchTargetRequest, PatchTargetInspection) (PatchTargetEvidence, error)
}
