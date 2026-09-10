package domain_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestHumanReviewAuthorizationIsOpaqueAndFailClosedOutsideDomain(t *testing.T) {
	authorizationType := reflect.TypeOf(domain.HumanReviewAuthorization{})
	if authorizationType.Kind() != reflect.Struct {
		t.Fatalf("authorization kind = %v, want opaque struct", authorizationType.Kind())
	}
	for index := 0; index < authorizationType.NumField(); index++ {
		if field := authorizationType.Field(index); field.IsExported() {
			t.Fatalf("authorization field %q is exported", field.Name)
		}
	}

	params := domain.ReviewDecisionParams{
		ID: domain.NewReviewDecisionID(), RunID: domain.NewRunID(), ExpectedRunVersion: 4, Comment: "reviewed", DecidedAt: time.Now().UTC(),
	}
	zero := domain.HumanReviewAuthorization{}
	if _, err := domain.NewAcceptedReviewDecision(params, zero); !errors.Is(err, domain.ErrUnauthorizedReview) {
		t.Fatalf("zero authorization accepted review error = %v, want ErrUnauthorizedReview", err)
	}
	if _, err := domain.NewRejectedReviewDecision(params, zero); !errors.Is(err, domain.ErrUnauthorizedReview) {
		t.Fatalf("zero authorization rejected review error = %v, want ErrUnauthorizedReview", err)
	}
}
