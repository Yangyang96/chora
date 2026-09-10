package contextcore

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

type PromotionCommand struct {
	EntryID                    domain.ContextEntryID
	RevisionID                 domain.ContextRevisionID
	RoomID                     domain.RoomID
	Kind                       domain.ContextKind
	Title                      string
	Body                       string
	Locator                    string
	Sensitive                  bool
	ReviewDecisionID           domain.ReviewDecisionID
	RunID                      domain.RunID
	ExpectedAcceptedRunVersion uint64
	ArtifactID                 domain.ArtifactID
	EventID                    domain.EventID
	CreatedAt                  time.Time
}

type AuthorizationID struct {
	value [16]byte
}

func (id AuthorizationID) Valid() bool { return id.value != ([16]byte{}) }
func (id AuthorizationID) String() string {
	if !id.Valid() {
		return ""
	}
	return "promotion_authorization_" + hex.EncodeToString(id.value[:])
}

func ParseAuthorizationID(value string) (AuthorizationID, error) {
	const prefix = "promotion_authorization_"
	if len(value) != len(prefix)+32 || !strings.HasPrefix(value, prefix) {
		return AuthorizationID{}, ErrInvalidAuthorizationID
	}
	payload := strings.TrimPrefix(value, prefix)
	if payload != strings.ToLower(payload) {
		return AuthorizationID{}, ErrInvalidAuthorizationID
	}
	decoded, err := hex.DecodeString(payload)
	if err != nil || len(decoded) != 16 {
		return AuthorizationID{}, ErrInvalidAuthorizationID
	}
	var raw [16]byte
	copy(raw[:], decoded)
	id := AuthorizationID{value: raw}
	if !id.Valid() {
		return AuthorizationID{}, ErrInvalidAuthorizationID
	}
	return id, nil
}

type AuthorizationReceipt struct {
	id           AuthorizationID
	binding      [32]byte
	authorizedAt time.Time
}

func (receipt AuthorizationReceipt) ID() AuthorizationID     { return receipt.id }
func (receipt AuthorizationReceipt) Binding() [32]byte       { return receipt.binding }
func (receipt AuthorizationReceipt) AuthorizedAt() time.Time { return receipt.authorizedAt }

func RestoreAuthorizationReceipt(id AuthorizationID, binding [32]byte, authorizedAt time.Time) (AuthorizationReceipt, error) {
	if !id.Valid() || binding == ([32]byte{}) || authorizedAt.IsZero() {
		return AuthorizationReceipt{}, ErrInvalidPromotion
	}
	return AuthorizationReceipt{id: id, binding: binding, authorizedAt: authorizedAt}, nil
}

// DeviceOwnerAuthorization is an opaque command-bound capability. Task 9 will
// provide the trusted Presence minting path; Task 4 intentionally exposes none.
type DeviceOwnerAuthorization struct {
	grant *promotionGrant
}

type promotionAuthorizationState uint32

const (
	promotionAuthorizationReady promotionAuthorizationState = iota
	promotionAuthorizationInFlight
	promotionAuthorizationConsumed
	promotionAuthorizationUncertain
)

type promotionGrant struct {
	id           AuthorizationID
	binding      [32]byte
	revisionID   domain.ContextRevisionID
	authorizedAt time.Time
	state        atomic.Uint32
}

type PromotionRecordParams struct {
	Revision                 domain.RoomContextRevision
	CommandDigest            [32]byte
	ReviewDecisionID         domain.ReviewDecisionID
	ReviewKind               domain.ReviewDecisionKind
	ReviewExpectedRunVersion uint64
	RunID                    domain.RunID
	AcceptedRunVersion       uint64
	ArtifactID               domain.ArtifactID
	EventID                  domain.EventID
	AuthorizationID          AuthorizationID
	AuthorizationBinding     [32]byte
	AuthorizedAt             time.Time
}

type PromotionRecord struct {
	revision                 domain.RoomContextRevision
	commandDigest            [32]byte
	reviewDecisionID         domain.ReviewDecisionID
	reviewKind               domain.ReviewDecisionKind
	reviewExpectedRunVersion uint64
	runID                    domain.RunID
	acceptedRunVersion       uint64
	artifactID               domain.ArtifactID
	eventID                  domain.EventID
	authorizationID          AuthorizationID
	authorizationBinding     [32]byte
	authorizedAt             time.Time
}

type PromotionRejectedError struct {
	Rejection PromotionRejection
}

func (err *PromotionRejectedError) Error() string {
	return fmt.Sprintf("%s: %s", ErrPromotionRejected, err.Rejection)
}

func (err *PromotionRejectedError) Unwrap() error { return ErrPromotionRejected }

func NewPromotionRecord(params PromotionRecordParams) (PromotionRecord, error) {
	_, hasSupersedes := params.Revision.Supersedes()
	if !params.Revision.RevisionID().Valid() || params.Revision.RevisionNumber() != 1 || hasSupersedes || params.CommandDigest == ([32]byte{}) || !params.ReviewDecisionID.Valid() || params.ReviewKind != domain.ReviewDecisionAccept || params.ReviewExpectedRunVersion == 0 || !params.RunID.Valid() || params.AcceptedRunVersion <= 1 || params.AcceptedRunVersion != params.ReviewExpectedRunVersion+1 || !params.ArtifactID.Valid() || !params.EventID.Valid() || !params.AuthorizationID.Valid() || params.AuthorizationBinding != params.CommandDigest || params.AuthorizedAt.IsZero() {
		return PromotionRecord{}, ErrInvalidPromotion
	}
	return PromotionRecord{
		revision: params.Revision, commandDigest: params.CommandDigest, reviewDecisionID: params.ReviewDecisionID, reviewKind: params.ReviewKind,
		reviewExpectedRunVersion: params.ReviewExpectedRunVersion, runID: params.RunID, acceptedRunVersion: params.AcceptedRunVersion,
		artifactID: params.ArtifactID, eventID: params.EventID, authorizationID: params.AuthorizationID,
		authorizationBinding: params.AuthorizationBinding, authorizedAt: params.AuthorizedAt,
	}, nil
}

func (record PromotionRecord) Revision() domain.RoomContextRevision { return record.revision }
func (record PromotionRecord) CommandDigest() [32]byte              { return record.commandDigest }
func (record PromotionRecord) ReviewDecisionID() domain.ReviewDecisionID {
	return record.reviewDecisionID
}
func (record PromotionRecord) ReviewKind() domain.ReviewDecisionKind { return record.reviewKind }
func (record PromotionRecord) ReviewExpectedRunVersion() uint64 {
	return record.reviewExpectedRunVersion
}
func (record PromotionRecord) RunID() domain.RunID              { return record.runID }
func (record PromotionRecord) AcceptedRunVersion() uint64       { return record.acceptedRunVersion }
func (record PromotionRecord) ArtifactID() domain.ArtifactID    { return record.artifactID }
func (record PromotionRecord) EventID() domain.EventID          { return record.eventID }
func (record PromotionRecord) AuthorizationID() AuthorizationID { return record.authorizationID }
func (record PromotionRecord) AuthorizationBinding() [32]byte   { return record.authorizationBinding }
func (record PromotionRecord) AuthorizedAt() time.Time          { return record.authorizedAt }

type Promoter struct{ port PromotionPort }

func NewPromoter(port PromotionPort) *Promoter { return &Promoter{port: port} }

func (promoter *Promoter) Promote(ctx context.Context, command PromotionCommand, authorization DeviceOwnerAuthorization) (PromotionRecord, error) {
	if promoter == nil || promoter.port == nil || !validPromotion(command) {
		return PromotionRecord{}, ErrInvalidPromotion
	}
	revision, err := domain.NewRoomContextRevision(domain.RoomContextRevisionParams{
		EntryID: command.EntryID, RevisionID: command.RevisionID, RoomID: command.RoomID, Kind: command.Kind, RevisionNumber: 1,
		Title: command.Title, Body: command.Body, Locator: command.Locator, Sensitive: command.Sensitive, CreatedAt: command.CreatedAt, UpdatedAt: command.CreatedAt,
	})
	if err != nil {
		return PromotionRecord{}, fmt.Errorf("%w: %v", ErrInvalidPromotion, err)
	}
	digest := promotionBinding(command)
	receipt, err := authorization.begin(command.RevisionID, digest)
	if err != nil {
		return PromotionRecord{}, err
	}
	request := PromoteReviewedRequest{
		Revision: revision, ReviewDecisionID: command.ReviewDecisionID, RunID: command.RunID, ExpectedAcceptedRunVersion: command.ExpectedAcceptedRunVersion,
		ArtifactID: command.ArtifactID, EventID: command.EventID, CommandDigest: digest, Authorization: receipt,
	}
	result, portErr := promoter.port.PromoteReviewed(ctx, request)
	return authorization.finish(command, request, result, portErr)
}

func validPromotion(command PromotionCommand) bool {
	return command.EntryID.Valid() && command.RevisionID.Valid() && command.RoomID.Valid() && validPromotionKind(command.Kind) && strings.TrimSpace(command.Title) != "" && command.ReviewDecisionID.Valid() && command.RunID.Valid() && command.ExpectedAcceptedRunVersion > 1 && command.ArtifactID.Valid() && command.EventID.Valid() && !command.CreatedAt.IsZero()
}

func validPromotionKind(kind domain.ContextKind) bool {
	switch kind {
	case domain.ContextKindBrief, domain.ContextKindDecision, domain.ContextKindConstraint, domain.ContextKindSourceRef, domain.ContextKindUnknown:
		return true
	default:
		return false
	}
}

var authorizationSequence atomic.Uint64

func newDeviceOwnerAuthorization(command PromotionCommand) DeviceOwnerAuthorization {
	binding := promotionBinding(command)
	sequence := authorizationSequence.Add(1)
	var value [16]byte
	copy(value[:8], binding[:8])
	binary.BigEndian.PutUint64(value[8:], sequence)
	grant := &promotionGrant{id: AuthorizationID{value: value}, binding: binding, revisionID: command.RevisionID, authorizedAt: time.Now().UTC()}
	grant.state.Store(uint32(promotionAuthorizationReady))
	return DeviceOwnerAuthorization{grant: grant}
}

func (authorization DeviceOwnerAuthorization) begin(revisionID domain.ContextRevisionID, binding [32]byte) (AuthorizationReceipt, error) {
	if authorization.grant == nil || authorization.grant.binding != binding || authorization.grant.revisionID != revisionID {
		return AuthorizationReceipt{}, ErrPromotionUnauthorized
	}
	for {
		state := promotionAuthorizationState(authorization.grant.state.Load())
		switch state {
		case promotionAuthorizationReady, promotionAuthorizationUncertain:
			if authorization.grant.state.CompareAndSwap(uint32(state), uint32(promotionAuthorizationInFlight)) {
				return AuthorizationReceipt{id: authorization.grant.id, binding: binding, authorizedAt: authorization.grant.authorizedAt}, nil
			}
		case promotionAuthorizationInFlight:
			return AuthorizationReceipt{}, ErrPromotionInFlight
		case promotionAuthorizationConsumed:
			return AuthorizationReceipt{}, ErrPromotionUnauthorized
		default:
			return AuthorizationReceipt{}, ErrPromotionUnauthorized
		}
	}
}

func (authorization DeviceOwnerAuthorization) finish(command PromotionCommand, request PromoteReviewedRequest, result PromoteReviewedResult, portErr error) (PromotionRecord, error) {
	switch result.Outcome {
	case PromotionOutcomeApplied:
		if portErr == nil && result.Rejection == "" && appliedPromotionRecordMatches(result.Record, command, request) {
			authorization.grant.state.Store(uint32(promotionAuthorizationConsumed))
			return result.Record, nil
		}
		authorization.grant.state.Store(uint32(promotionAuthorizationUncertain))
		return PromotionRecord{}, joinPromotionError(ErrPromotionCommitUnknown, portErr)
	case PromotionOutcomeAlreadyApplied:
		if portErr == nil && result.Rejection == "" && alreadyAppliedPromotionRecordMatches(result.Record, command, request) {
			authorization.grant.state.Store(uint32(promotionAuthorizationConsumed))
			return result.Record, nil
		}
		authorization.grant.state.Store(uint32(promotionAuthorizationUncertain))
		return PromotionRecord{}, joinPromotionError(ErrPromotionCommitUnknown, portErr)
	case PromotionOutcomeRejected:
		if portErr == nil && validPromotionRejection(result.Rejection) && promotionRecordIsZero(result.Record) {
			authorization.grant.state.Store(uint32(promotionAuthorizationConsumed))
			return PromotionRecord{}, &PromotionRejectedError{Rejection: result.Rejection}
		}
		authorization.grant.state.Store(uint32(promotionAuthorizationUncertain))
		return PromotionRecord{}, joinPromotionError(ErrPromotionCommitUnknown, portErr)
	case PromotionOutcomeRetryableNotApplied:
		if portErr != nil && result.Rejection == "" && promotionRecordIsZero(result.Record) {
			authorization.grant.state.Store(uint32(promotionAuthorizationReady))
			return PromotionRecord{}, errors.Join(ErrPromotionRetryable, portErr)
		}
		authorization.grant.state.Store(uint32(promotionAuthorizationUncertain))
		return PromotionRecord{}, joinPromotionError(ErrPromotionCommitUnknown, portErr)
	case PromotionOutcomeCommitUnknown:
		authorization.grant.state.Store(uint32(promotionAuthorizationUncertain))
		return PromotionRecord{}, joinPromotionError(ErrPromotionCommitUnknown, portErr)
	default:
		authorization.grant.state.Store(uint32(promotionAuthorizationUncertain))
		return PromotionRecord{}, joinPromotionError(ErrPromotionCommitUnknown, portErr)
	}
}

func validPromotionRejection(rejection PromotionRejection) bool {
	switch rejection {
	case PromotionRejectionReviewNotAccepted, PromotionRejectionRunNotAccepted, PromotionRejectionProvenanceMismatch, PromotionRejectionRoomMismatch, PromotionRejectionVersionMismatch, PromotionRejectionIdempotencyConflict:
		return true
	default:
		return false
	}
}

func appliedPromotionRecordMatches(record PromotionRecord, command PromotionCommand, request PromoteReviewedRequest) bool {
	return promotionRecordCoreMatches(record, command, request) && record.AuthorizationID() == request.Authorization.ID() && record.AuthorizationBinding() == request.Authorization.Binding() && record.AuthorizedAt().Equal(request.Authorization.AuthorizedAt())
}

func alreadyAppliedPromotionRecordMatches(record PromotionRecord, command PromotionCommand, request PromoteReviewedRequest) bool {
	return promotionRecordCoreMatches(record, command, request)
}

func promotionRecordCoreMatches(record PromotionRecord, command PromotionCommand, request PromoteReviewedRequest) bool {
	revision := record.Revision()
	target := request.Revision
	_, revisionSupersedes := revision.Supersedes()
	_, targetSupersedes := target.Supersedes()
	return !revisionSupersedes && !targetSupersedes && revision.EntryID() == target.EntryID() && revision.RevisionID() == target.RevisionID() && revision.RoomID() == target.RoomID() && revision.Kind() == target.Kind() && revision.RevisionNumber() == 1 && target.RevisionNumber() == 1 && revision.Title() == target.Title() && revision.Body() == target.Body() && revision.Locator() == target.Locator() && revision.Sensitive() == target.Sensitive() && revision.CreatedAt().Equal(target.CreatedAt()) && revision.UpdatedAt().Equal(target.UpdatedAt()) && record.CommandDigest() == request.CommandDigest && record.ReviewDecisionID() == command.ReviewDecisionID && record.ReviewKind() == domain.ReviewDecisionAccept && record.ReviewExpectedRunVersion()+1 == record.AcceptedRunVersion() && record.AcceptedRunVersion() == command.ExpectedAcceptedRunVersion && record.RunID() == command.RunID && record.ArtifactID() == command.ArtifactID && record.EventID() == command.EventID && record.AuthorizationID().Valid() && record.AuthorizationBinding() == request.CommandDigest && !record.AuthorizedAt().IsZero()
}

func promotionRecordIsZero(record PromotionRecord) bool { return record == (PromotionRecord{}) }

func joinPromotionError(classification, cause error) error {
	if cause == nil {
		return classification
	}
	return errors.Join(classification, cause)
}

func promotionBinding(command PromotionCommand) [32]byte {
	type bindingDocument struct {
		EntryID, RevisionID, RoomID, Kind, Title, Body, Locator, ReviewDecisionID, RunID, ArtifactID, EventID, CreatedAt string
		Sensitive                                                                                                        bool
		ExpectedAcceptedRunVersion                                                                                       uint64
	}
	document := bindingDocument{
		EntryID: command.EntryID.String(), RevisionID: command.RevisionID.String(), RoomID: command.RoomID.String(), Kind: string(command.Kind), Title: command.Title,
		Body: command.Body, Locator: command.Locator, Sensitive: command.Sensitive, ReviewDecisionID: command.ReviewDecisionID.String(), RunID: command.RunID.String(),
		ExpectedAcceptedRunVersion: command.ExpectedAcceptedRunVersion, ArtifactID: command.ArtifactID.String(), EventID: command.EventID.String(), CreatedAt: command.CreatedAt.UTC().Format(time.RFC3339Nano),
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		panic(err)
	}
	return sha256.Sum256(encoded)
}
