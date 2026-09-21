package store

import (
	"context"
	"github.com/Yangyang96/chora/internal/domain"
)

type ProjectDocumentReader interface {
	ListProjectDocumentRevisions(context.Context, domain.TaskID) ([]domain.ProjectDocumentRevision, error)
	ListProjectDocumentReviews(context.Context, domain.TaskID) ([]domain.ProjectDocumentReview, error)
}
type ProjectDocumentWriter interface {
	InsertProjectDocumentRevision(context.Context, domain.ProjectDocumentRevision) error
	InsertProjectDocumentReview(context.Context, domain.ProjectDocumentReview) error
	CloseTaskForAcceptedProjectDocument(context.Context, domain.TaskID) error
}
