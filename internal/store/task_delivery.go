package store

import (
	"context"
	"github.com/Yangyang96/chora/internal/domain"
	"time"
)

// Delivery intents are durable before Git or hosting mutations. PreviewJSON is
// immutable; mutable outcome fields advance by CAS and never re-arm an intent.
type DeliveryOperation struct {
	ID            string
	TaskID        domain.TaskID
	RunID         domain.RunID
	RepositoryID  domain.RepositoryID
	Kind          string
	State         string
	Version       uint64
	RequestDigest [32]byte
	PreviewJSON   []byte
	OutcomeJSON   []byte
	CreatedAt     time.Time
	UpdatedAt     time.Time
}
type DeliveryReader interface {
	GetRepositoryDeliveryDefault(context.Context, domain.RepositoryID) (domain.RepositoryDeliveryDefault, error)
	GetDeliveryOperation(context.Context, string) (DeliveryOperation, error)
	ListDeliveryOperations(context.Context, domain.TaskID) ([]DeliveryOperation, error)
}
type DeliveryWriter interface {
	SaveRepositoryDeliveryDefault(context.Context, uint64, domain.RepositoryDeliveryDefault) error
	SaveDeliveryOperation(context.Context, uint64, DeliveryOperation) error
}
