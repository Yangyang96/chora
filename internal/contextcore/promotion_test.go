package contextcore

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestPromotionCommandUsesPersistedProvenanceInsteadOfReviewAggregate(t *testing.T) {
	typeOfCommand := reflect.TypeOf(PromotionCommand{})
	if _, exists := typeOfCommand.FieldByName("Review"); exists {
		t.Fatal("PromotionCommand must not accept an in-memory ReviewDecision")
	}
	for _, opaqueType := range []reflect.Type{reflect.TypeOf(DeviceOwnerAuthorization{}), reflect.TypeOf(AuthorizationReceipt{})} {
		for index := 0; index < opaqueType.NumField(); index++ {
			if field := opaqueType.Field(index); field.IsExported() {
				t.Fatalf("%s field %q is exported", opaqueType.Name(), field.Name)
			}
		}
	}
	for _, field := range []string{"ReviewDecisionID", "RunID", "ExpectedAcceptedRunVersion", "ArtifactID", "EventID"} {
		if _, exists := typeOfCommand.FieldByName(field); !exists {
			t.Fatalf("PromotionCommand missing persisted provenance field %q", field)
		}
	}
}

func TestPromoteReviewedAppliedAndAlreadyApplied(t *testing.T) {
	for _, outcome := range []PromotionOutcome{PromotionOutcomeApplied, PromotionOutcomeAlreadyApplied} {
		t.Run(string(outcome), func(t *testing.T) {
			command := promotionCommandFixture(t)
			authorization := newDeviceOwnerAuthorization(command)
			port := &fakePort{}
			port.promoteFn = func(request PromoteReviewedRequest) (PromoteReviewedResult, error) {
				return PromoteReviewedResult{Outcome: outcome, Record: promotionRecordForRequest(t, request)}, nil
			}
			record, err := NewPromoter(port).Promote(context.Background(), command, authorization)
			if err != nil {
				t.Fatalf("Promote() error = %v", err)
			}
			assertPromotionRecordMatchesCommand(t, record, command)
			if len(port.promotions) != 1 {
				t.Fatalf("persisted promotion records = %d, want 1", len(port.promotions))
			}
			request := port.promoteRequests[0]
			if !request.Authorization.ID().Valid() || request.Authorization.Binding() != request.CommandDigest || request.Authorization.AuthorizedAt().IsZero() {
				t.Fatalf("authorization receipt incomplete: %#v", request.Authorization)
			}
			if _, err := NewPromoter(port).Promote(context.Background(), command, authorization); !errors.Is(err, ErrPromotionUnauthorized) {
				t.Fatalf("consumed authorization error = %v", err)
			}
		})
	}
}

func TestPromoteReviewedAlreadyAppliedAcceptsPersistedOriginalAuthorizationAfterRestart(t *testing.T) {
	command := promotionCommandFixture(t)
	authorizationA := newDeviceOwnerAuthorization(command)
	firstPort := &fakePort{promoteFn: func(request PromoteReviewedRequest) (PromoteReviewedResult, error) {
		return PromoteReviewedResult{Outcome: PromotionOutcomeApplied, Record: promotionRecordForRequest(t, request)}, nil
	}}
	persisted, err := NewPromoter(firstPort).Promote(context.Background(), command, authorizationA)
	if err != nil {
		t.Fatal(err)
	}

	hydratedAuthorizationID, err := ParseAuthorizationID(persisted.AuthorizationID().String())
	if err != nil {
		t.Fatalf("ParseAuthorizationID() error = %v", err)
	}
	hydrated, err := NewPromotionRecord(PromotionRecordParams{
		Revision: persisted.Revision(), CommandDigest: persisted.CommandDigest(),
		ReviewDecisionID: persisted.ReviewDecisionID(), ReviewKind: persisted.ReviewKind(), ReviewExpectedRunVersion: persisted.ReviewExpectedRunVersion(),
		RunID: persisted.RunID(), AcceptedRunVersion: persisted.AcceptedRunVersion(), ArtifactID: persisted.ArtifactID(), EventID: persisted.EventID(),
		AuthorizationID: hydratedAuthorizationID, AuthorizationBinding: persisted.AuthorizationBinding(), AuthorizedAt: persisted.AuthorizedAt(),
	})
	if err != nil {
		t.Fatalf("hydrate PromotionRecord error = %v", err)
	}

	authorizationB := newDeviceOwnerAuthorization(command)
	if authorizationB.grant.id == hydrated.AuthorizationID() {
		t.Fatal("restart fixture did not create a new authorization")
	}
	restartPort := &fakePort{promoteFn: func(PromoteReviewedRequest) (PromoteReviewedResult, error) {
		return PromoteReviewedResult{Outcome: PromotionOutcomeAlreadyApplied, Record: hydrated}, nil
	}}
	reconciled, err := NewPromoter(restartPort).Promote(context.Background(), command, authorizationB)
	if err != nil {
		t.Fatalf("already-applied reconciliation error = %v", err)
	}
	if reconciled.AuthorizationID() != persisted.AuthorizationID() || !reconciled.AuthorizedAt().Equal(persisted.AuthorizedAt()) {
		t.Fatalf("reconciled record lost original authorization provenance: %#v", reconciled)
	}
}

func TestAuthorizationIDRoundTripAndRejectsNonCanonicalValues(t *testing.T) {
	command := promotionCommandFixture(t)
	id := newDeviceOwnerAuthorization(command).grant.id
	parsed, err := ParseAuthorizationID(id.String())
	if err != nil || parsed != id {
		t.Fatalf("roundtrip parsed=%#v err=%v, want %#v", parsed, err, id)
	}
	payload := id.String()[len("promotion_authorization_"):]
	invalid := []string{
		"",
		"authorization_" + payload,
		"promotion_authorization_" + payload[:31],
		"promotion_authorization_" + payload + "0",
		"promotion_authorization_ABCDEF0123456789ABCDEF0123456789",
		"promotion_authorization_zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		"promotion_authorization_00000000000000000000000000000000",
	}
	for _, value := range invalid {
		if parsed, err := ParseAuthorizationID(value); err == nil || parsed.Valid() {
			t.Fatalf("ParseAuthorizationID(%q) = %#v, %v; want invalid", value, parsed, err)
		}
	}
}

func TestPromoteReviewedRejectedIdempotencyConflictConsumesAuthorization(t *testing.T) {
	command := promotionCommandFixture(t)
	authorization := newDeviceOwnerAuthorization(command)
	port := &fakePort{promoteFn: func(PromoteReviewedRequest) (PromoteReviewedResult, error) {
		return PromoteReviewedResult{Outcome: PromotionOutcomeRejected, Rejection: PromotionRejectionIdempotencyConflict}, nil
	}}
	if _, err := NewPromoter(port).Promote(context.Background(), command, authorization); !errors.Is(err, ErrPromotionRejected) {
		t.Fatalf("error = %v, want ErrPromotionRejected", err)
	} else {
		var rejected *PromotionRejectedError
		if !errors.As(err, &rejected) || rejected.Rejection != PromotionRejectionIdempotencyConflict {
			t.Fatalf("error = %#v, want typed idempotency rejection", err)
		}
	}
	if _, err := NewPromoter(port).Promote(context.Background(), command, authorization); !errors.Is(err, ErrPromotionUnauthorized) {
		t.Fatalf("reused authorization error = %v", err)
	}
}

func TestPromoteReviewedRejectsContradictoryOutcomeErrorCombinations(t *testing.T) {
	portErr := errors.New("port error")
	tests := []struct {
		name   string
		result func(PromoteReviewedRequest) PromoteReviewedResult
		err    error
	}{
		{"applied with rejection", func(request PromoteReviewedRequest) PromoteReviewedResult {
			return PromoteReviewedResult{Outcome: PromotionOutcomeApplied, Record: promotionRecordForRequest(t, request), Rejection: PromotionRejectionVersionMismatch}
		}, nil},
		{"already applied with rejection", func(request PromoteReviewedRequest) PromoteReviewedResult {
			return PromoteReviewedResult{Outcome: PromotionOutcomeAlreadyApplied, Record: promotionRecordForRequest(t, request), Rejection: PromotionRejectionVersionMismatch}
		}, nil},
		{"retryable with rejection", func(PromoteReviewedRequest) PromoteReviewedResult {
			return PromoteReviewedResult{Outcome: PromotionOutcomeRetryableNotApplied, Rejection: PromotionRejectionVersionMismatch}
		}, portErr},
		{"success with error", func(request PromoteReviewedRequest) PromoteReviewedResult {
			return PromoteReviewedResult{Outcome: PromotionOutcomeApplied, Record: promotionRecordForRequest(t, request)}
		}, portErr},
		{"rejected with record", func(request PromoteReviewedRequest) PromoteReviewedResult {
			return PromoteReviewedResult{Outcome: PromotionOutcomeRejected, Record: promotionRecordForRequest(t, request), Rejection: PromotionRejectionVersionMismatch}
		}, nil},
		{"retryable with record", func(request PromoteReviewedRequest) PromoteReviewedResult {
			return PromoteReviewedResult{Outcome: PromotionOutcomeRetryableNotApplied, Record: promotionRecordForRequest(t, request)}
		}, portErr},
		{"rejected with error", func(PromoteReviewedRequest) PromoteReviewedResult {
			return PromoteReviewedResult{Outcome: PromotionOutcomeRejected, Rejection: PromotionRejectionVersionMismatch}
		}, portErr},
		{"commit unknown with record", func(request PromoteReviewedRequest) PromoteReviewedResult {
			return PromoteReviewedResult{Outcome: PromotionOutcomeCommitUnknown, Record: promotionRecordForRequest(t, request)}
		}, nil},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			command := promotionCommandFixture(t)
			authorization := newDeviceOwnerAuthorization(command)
			port := &fakePort{promoteFn: func(request PromoteReviewedRequest) (PromoteReviewedResult, error) {
				return test.result(request), test.err
			}}
			if _, err := NewPromoter(port).Promote(context.Background(), command, authorization); !errors.Is(err, ErrPromotionCommitUnknown) {
				t.Fatalf("error = %v, want ErrPromotionCommitUnknown", err)
			}
			if state := promotionAuthorizationState(authorization.grant.state.Load()); state != promotionAuthorizationUncertain {
				t.Fatalf("authorization state = %v, want uncertain", state)
			}
		})
	}
}

func TestPromoteReviewedKnownRollbackAllowsSameAuthorizationRetry(t *testing.T) {
	command := promotionCommandFixture(t)
	authorization := newDeviceOwnerAuthorization(command)
	retryableErr := errors.New("transaction rolled back")
	port := &fakePort{}
	port.promoteFn = func(request PromoteReviewedRequest) (PromoteReviewedResult, error) {
		if port.promoteCalls == 1 {
			return PromoteReviewedResult{Outcome: PromotionOutcomeRetryableNotApplied}, retryableErr
		}
		return PromoteReviewedResult{Outcome: PromotionOutcomeApplied, Record: promotionRecordForRequest(t, request)}, nil
	}
	if _, err := NewPromoter(port).Promote(context.Background(), command, authorization); !errors.Is(err, retryableErr) {
		t.Fatalf("first error = %v", err)
	}
	if _, err := NewPromoter(port).Promote(context.Background(), command, authorization); err != nil {
		t.Fatalf("retry error = %v", err)
	}
}

func TestPromoteReviewedCommitUnknownOnlyAllowsSameBindingRetry(t *testing.T) {
	command := promotionCommandFixture(t)
	authorization := newDeviceOwnerAuthorization(command)
	unknownErr := errors.New("commit acknowledgement lost")
	port := &fakePort{}
	port.promoteFn = func(request PromoteReviewedRequest) (PromoteReviewedResult, error) {
		if port.promoteCalls == 1 {
			return PromoteReviewedResult{Outcome: PromotionOutcomeCommitUnknown}, unknownErr
		}
		return PromoteReviewedResult{Outcome: PromotionOutcomeAlreadyApplied, Record: promotionRecordForRequest(t, request)}, nil
	}
	if _, err := NewPromoter(port).Promote(context.Background(), command, authorization); !errors.Is(err, ErrPromotionCommitUnknown) || !errors.Is(err, unknownErr) {
		t.Fatalf("first error = %v", err)
	}
	changed := command
	changed.Body = "different binding"
	if _, err := NewPromoter(port).Promote(context.Background(), changed, authorization); !errors.Is(err, ErrPromotionUnauthorized) {
		t.Fatalf("changed binding error = %v", err)
	}
	if port.promoteCalls != 1 {
		t.Fatalf("changed binding entered port; calls=%d", port.promoteCalls)
	}
	if _, err := NewPromoter(port).Promote(context.Background(), command, authorization); err != nil {
		t.Fatalf("same binding retry error = %v", err)
	}
}

func TestPromoteReviewedConcurrentUseAllowsOnlyOnePortCall(t *testing.T) {
	command := promotionCommandFixture(t)
	authorization := newDeviceOwnerAuthorization(command)
	entered := make(chan PromoteReviewedRequest, 1)
	release := make(chan struct{})
	port := &fakePort{promoteFn: func(request PromoteReviewedRequest) (PromoteReviewedResult, error) {
		entered <- request
		<-release
		return PromoteReviewedResult{Outcome: PromotionOutcomeApplied, Record: promotionRecordForRequest(t, request)}, nil
	}}
	firstDone := make(chan error, 1)
	go func() {
		_, err := NewPromoter(port).Promote(context.Background(), command, authorization)
		firstDone <- err
	}()
	<-entered
	if _, err := NewPromoter(port).Promote(context.Background(), command, authorization); !errors.Is(err, ErrPromotionInFlight) {
		t.Fatalf("concurrent error = %v, want ErrPromotionInFlight", err)
	}
	close(release)
	if err := <-firstDone; err != nil {
		t.Fatalf("first error = %v", err)
	}
	if port.promoteCalls != 1 {
		t.Fatalf("port calls = %d, want 1", port.promoteCalls)
	}
}

func TestPromoteReviewedMalformedSuccessFailsClosedAsCommitUnknown(t *testing.T) {
	command := promotionCommandFixture(t)
	authorization := newDeviceOwnerAuthorization(command)
	port := &fakePort{}
	port.promoteFn = func(request PromoteReviewedRequest) (PromoteReviewedResult, error) {
		if port.promoteCalls == 1 {
			return PromoteReviewedResult{Outcome: PromotionOutcomeApplied}, nil
		}
		return PromoteReviewedResult{Outcome: PromotionOutcomeAlreadyApplied, Record: promotionRecordForRequest(t, request)}, nil
	}
	if _, err := NewPromoter(port).Promote(context.Background(), command, authorization); !errors.Is(err, ErrPromotionCommitUnknown) {
		t.Fatalf("malformed success error = %v", err)
	}
	if _, err := NewPromoter(port).Promote(context.Background(), command, authorization); err != nil {
		t.Fatalf("same binding reconciliation error = %v", err)
	}
}

func TestPromoteReviewedInconsistentSuccessRecordFailsClosed(t *testing.T) {
	command := promotionCommandFixture(t)
	authorization := newDeviceOwnerAuthorization(command)
	port := &fakePort{promoteFn: func(request PromoteReviewedRequest) (PromoteReviewedResult, error) {
		record := promotionRecordForRequest(t, request)
		record.artifactID = domain.NewArtifactID()
		return PromoteReviewedResult{Outcome: PromotionOutcomeApplied, Record: record}, nil
	}}
	if _, err := NewPromoter(port).Promote(context.Background(), command, authorization); !errors.Is(err, ErrPromotionCommitUnknown) {
		t.Fatalf("inconsistent record error = %v, want ErrPromotionCommitUnknown", err)
	}
}

func TestPromotionRecordAndMatcherRejectSupersedingRevisionOne(t *testing.T) {
	command := promotionCommandFixture(t)
	digest := promotionBinding(command)
	authorization := newDeviceOwnerAuthorization(command)
	receipt := AuthorizationReceipt{id: authorization.grant.id, binding: digest, authorizedAt: authorization.grant.authorizedAt}
	previous := mustContextRevisionID(t, 0x55)
	superseding, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{
		EntryID: command.EntryID, RevisionID: command.RevisionID, RoomID: command.RoomID, Kind: command.Kind, RevisionNumber: 1,
		Title: command.Title, Body: command.Body, Locator: command.Locator, Sensitive: command.Sensitive, Supersedes: &previous,
		CreatedAt: command.CreatedAt, UpdatedAt: command.CreatedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	params := promotionRecordParams(command, superseding, digest, receipt)
	if _, err := NewPromotionRecord(params); !errors.Is(err, ErrInvalidPromotion) {
		t.Fatalf("superseding revision 1 error = %v, want ErrInvalidPromotion", err)
	}

	port := &fakePort{promoteFn: func(request PromoteReviewedRequest) (PromoteReviewedResult, error) {
		record := promotionRecordForRequest(t, request)
		record.revision = superseding
		return PromoteReviewedResult{Outcome: PromotionOutcomeApplied, Record: record}, nil
	}}
	if _, err := NewPromoter(port).Promote(context.Background(), command, authorization); !errors.Is(err, ErrPromotionCommitUnknown) {
		t.Fatalf("superseding success error = %v, want ErrPromotionCommitUnknown", err)
	}
}

func TestPromoteReviewedLocalInvalidCommandDoesNotConsumeAuthorization(t *testing.T) {
	command := promotionCommandFixture(t)
	authorization := newDeviceOwnerAuthorization(command)
	invalid := command
	invalid.ArtifactID = domain.ArtifactID{}
	port := &fakePort{promoteFn: func(request PromoteReviewedRequest) (PromoteReviewedResult, error) {
		return PromoteReviewedResult{Outcome: PromotionOutcomeApplied, Record: promotionRecordForRequest(t, request)}, nil
	}}
	if _, err := NewPromoter(port).Promote(context.Background(), invalid, authorization); !errors.Is(err, ErrInvalidPromotion) {
		t.Fatalf("invalid command error = %v", err)
	}
	if port.promoteCalls != 0 {
		t.Fatalf("invalid command entered port; calls=%d", port.promoteCalls)
	}
	if _, err := NewPromoter(port).Promote(context.Background(), command, authorization); err != nil {
		t.Fatalf("valid retry error = %v", err)
	}
}

func promotionCommandFixture(t *testing.T) PromotionCommand {
	t.Helper()
	return PromotionCommand{
		EntryID: mustContextEntryID(t, 0x42), RevisionID: mustContextRevisionID(t, 0x43), RoomID: mustRoomID(t, 0x01),
		Kind: domain.ContextKindDecision, Title: "Reviewed decision", Body: "Promote only after persisted review.", Locator: "artifact://reviewed",
		ReviewDecisionID: mustReviewDecisionID(t, 0x41), RunID: mustRunID(t, 0x40), ExpectedAcceptedRunVersion: 5,
		ArtifactID: mustArtifactID(t, 0x44), EventID: mustEventID(t, 0x45), CreatedAt: time.Date(2026, 7, 29, 1, 1, 0, 0, time.UTC),
	}
}

func promotionRecordForRequest(t *testing.T, request PromoteReviewedRequest) PromotionRecord {
	t.Helper()
	command := promotionCommandFixture(t)
	command.ReviewDecisionID = request.ReviewDecisionID
	command.RunID = request.RunID
	command.ExpectedAcceptedRunVersion = request.ExpectedAcceptedRunVersion
	command.ArtifactID = request.ArtifactID
	command.EventID = request.EventID
	record, err := NewPromotionRecord(promotionRecordParams(command, request.Revision, request.CommandDigest, request.Authorization))
	if err != nil {
		t.Fatal(err)
	}
	return record
}

func promotionRecordParams(command PromotionCommand, revision domain.RoomContextRevision, digest [32]byte, receipt AuthorizationReceipt) PromotionRecordParams {
	return PromotionRecordParams{
		Revision: revision, CommandDigest: digest,
		ReviewDecisionID: command.ReviewDecisionID, ReviewKind: domain.ReviewDecisionAccept, ReviewExpectedRunVersion: command.ExpectedAcceptedRunVersion - 1,
		RunID: command.RunID, AcceptedRunVersion: command.ExpectedAcceptedRunVersion, ArtifactID: command.ArtifactID, EventID: command.EventID,
		AuthorizationID: receipt.ID(), AuthorizationBinding: receipt.Binding(), AuthorizedAt: receipt.AuthorizedAt(),
	}
}

func assertPromotionRecordMatchesCommand(t *testing.T, record PromotionRecord, command PromotionCommand) {
	t.Helper()
	if record.Revision().RevisionID() != command.RevisionID || record.Revision().RevisionNumber() != 1 || record.CommandDigest() == ([32]byte{}) || record.ReviewDecisionID() != command.ReviewDecisionID || record.ReviewKind() != domain.ReviewDecisionAccept || record.ReviewExpectedRunVersion()+1 != record.AcceptedRunVersion() || record.AcceptedRunVersion() != command.ExpectedAcceptedRunVersion || record.RunID() != command.RunID || record.ArtifactID() != command.ArtifactID || record.EventID() != command.EventID || !record.AuthorizationID().Valid() || record.AuthorizationBinding() != record.CommandDigest() || record.AuthorizedAt().IsZero() {
		t.Fatalf("promotion record mismatch: %#v", record)
	}
}

func mustRunID(t *testing.T, suffix byte) domain.RunID {
	t.Helper()
	value, err := domain.ParseRunID(fixedID("run_", suffix))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func mustEventID(t *testing.T, suffix byte) domain.EventID {
	t.Helper()
	value, err := domain.ParseEventID(fixedID("event_", suffix))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func mustArtifactID(t *testing.T, suffix byte) domain.ArtifactID {
	t.Helper()
	value, err := domain.ParseArtifactID(fixedID("artifact_", suffix))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
func mustReviewDecisionID(t *testing.T, suffix byte) domain.ReviewDecisionID {
	t.Helper()
	value, err := domain.ParseReviewDecisionID(fixedID("review_decision_", suffix))
	if err != nil {
		t.Fatal(err)
	}
	return value
}
