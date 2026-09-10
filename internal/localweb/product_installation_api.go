package localweb

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"

	"github.com/Yangyang96/chora/internal/productinstall"
)

const (
	ProductInstallationSchemaVersion  = "chora.product-installation-status/v1"
	ProductInstallationStatusReady    = "ready"
	ProductInstallationStatusBlocked  = "blocked"
	ProductInstallationStatusComplete = "completed"

	ProductInstallationActionSetup     ProductInstallationAction = "setup"
	ProductInstallationActionUpgrade   ProductInstallationAction = "upgrade"
	ProductInstallationActionGC        ProductInstallationAction = "gc"
	ProductInstallationActionUninstall ProductInstallationAction = "uninstall"

	ProductInstallationReasonObservationUnavailable = "observation_unavailable"
	ProductInstallationReasonIdentityMismatch       = "identity_mismatch"
	ProductInstallationReasonAuthorityRequired      = "authority_required"
	ProductInstallationReasonIdempotencyConflict    = "idempotency_conflict"
	ProductInstallationReasonBusy                   = "installation_busy"
	ProductInstallationReasonCapabilityProbeFailed  = "capability_probe_failed"
	ProductInstallationReasonGenerationReferenced   = "generation_referenced"
	ProductInstallationReasonAssetIdentityConflict  = "asset_identity_conflict"
	ProductInstallationReasonInvalidRequest         = "invalid_request"
	ProductInstallationReasonOperationFailed        = "operation_failed"
)

const maxProductInstallationGCLimit = 64

type ProductInstallationAction string

type ProductInstallationMutationRequest struct {
	ActorID                  string
	SessionID                string
	IdempotencyKey           string
	Action                   ProductInstallationAction
	ConfirmPinnedEngineTools bool
	ConfirmColimaVM          bool
	Limit                    int
}

type ProductInstallationEngineView struct {
	Ready           bool   `json:"ready"`
	APIVersion      string `json:"apiVersion"`
	OperatingSystem string `json:"operatingSystem"`
	Architecture    string `json:"architecture"`
	ContextName     string `json:"contextName"`
}

type ProductInstallationActionsView struct {
	Setup     bool `json:"setup"`
	Upgrade   bool `json:"upgrade"`
	GC        bool `json:"gc"`
	Uninstall bool `json:"uninstall"`
}

type ProductInstallationView struct {
	SchemaVersion         string                         `json:"schemaVersion"`
	Status                string                         `json:"status"`
	ReasonCode            string                         `json:"reasonCode"`
	Engine                ProductInstallationEngineView  `json:"engine"`
	ActiveGenerationID    string                         `json:"activeGenerationId"`
	CandidateGenerationID string                         `json:"candidateGenerationId"`
	Actions               ProductInstallationActionsView `json:"actions"`
	Replayed              bool                           `json:"replayed"`
	RestartRequired       bool                           `json:"restartRequired"`
}

// ProductInstallationController is the only installed-lifecycle authority
// exposed to localweb. It deliberately excludes paths, digests, references,
// credentials, commands, and authority grants from the public HTTP boundary.
type ProductInstallationController interface {
	Doctor(context.Context) (ProductInstallationView, error)
	Mutate(context.Context, ProductInstallationMutationRequest) (ProductInstallationView, error)
}

var productInstallationPublicTokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,127}$`)
var productInstallationIdempotencyKeyPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type productInstallationMutationInput struct {
	Confirmation             string `json:"confirmation"`
	ConfirmPinnedEngineTools *bool  `json:"confirmPinnedEngineTools"`
	ConfirmColimaVM          *bool  `json:"confirmColimaVM"`
	Limit                    *int   `json:"limit"`
}

func (server *Server) productInstallationDoctor(writer http.ResponseWriter, request *http.Request) {
	if !server.product {
		http.NotFound(writer, request)
		return
	}
	if server.installationController == nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New("product installation lifecycle is unavailable"))
		return
	}
	view, err := server.installationController.Doctor(request.Context())
	if err != nil {
		writeProductInstallationError(writer, err)
		return
	}
	if err := validateProductInstallationView(view); err != nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New("product installation status is unavailable"))
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func (server *Server) mutateProductInstallation(writer http.ResponseWriter, request *http.Request) {
	if !server.product {
		http.NotFound(writer, request)
		return
	}
	if server.installationController == nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New("product installation lifecycle is unavailable"))
		return
	}
	action, ok := parseProductInstallationAction(request.PathValue("action"))
	if !ok {
		http.NotFound(writer, request)
		return
	}
	input, err := decodeProductInstallationMutation(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, errors.New("invalid product installation request"))
		return
	}
	meta, ok := requireRequestCommandMeta(writer, request, "product-installation-"+string(action))
	if !ok {
		return
	}
	if input.Confirmation != string(action) || !validProductInstallationIdempotencyKey(request.Header.Get("Idempotency-Key")) {
		writeError(writer, http.StatusBadRequest, errors.New("invalid product installation confirmation"))
		return
	}
	command := ProductInstallationMutationRequest{
		ActorID: meta.ActorID, SessionID: meta.SessionID, IdempotencyKey: meta.IdempotencyKey, Action: action,
	}
	if !populateProductInstallationConfirmation(&command, input.ConfirmPinnedEngineTools, input.ConfirmColimaVM, input.Limit) {
		writeError(writer, http.StatusBadRequest, errors.New("invalid product installation confirmation"))
		return
	}
	view, err := server.installationController.Mutate(request.Context(), command)
	if err != nil {
		writeProductInstallationError(writer, err)
		return
	}
	if err := validateProductInstallationView(view); err != nil {
		writeError(writer, http.StatusServiceUnavailable, errors.New("product installation status is unavailable"))
		return
	}
	writeJSON(writer, http.StatusOK, view)
}

func parseProductInstallationAction(value string) (ProductInstallationAction, bool) {
	action := ProductInstallationAction(value)
	switch action {
	case ProductInstallationActionSetup, ProductInstallationActionUpgrade, ProductInstallationActionGC, ProductInstallationActionUninstall:
		return action, true
	default:
		return "", false
	}
}

func decodeProductInstallationMutation(request *http.Request) (productInstallationMutationInput, error) {
	defer request.Body.Close()
	var input productInstallationMutationInput
	decoder := json.NewDecoder(request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return productInstallationMutationInput{}, err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("unexpected trailing JSON value")
		}
		return productInstallationMutationInput{}, err
	}
	return input, nil
}

func populateProductInstallationConfirmation(command *ProductInstallationMutationRequest, engineTools, colimaVM *bool, limit *int) bool {
	switch command.Action {
	case ProductInstallationActionSetup:
		if engineTools == nil || colimaVM == nil || !*engineTools || !*colimaVM || limit != nil {
			return false
		}
		command.ConfirmPinnedEngineTools = true
		command.ConfirmColimaVM = true
	case ProductInstallationActionGC:
		if engineTools != nil || colimaVM != nil || limit == nil || *limit < 1 || *limit > maxProductInstallationGCLimit {
			return false
		}
		command.Limit = *limit
	case ProductInstallationActionUpgrade, ProductInstallationActionUninstall:
		if engineTools != nil || colimaVM != nil || limit != nil {
			return false
		}
	default:
		return false
	}
	return true
}

func validProductInstallationIdempotencyKey(value string) bool {
	return productInstallationIdempotencyKeyPattern.MatchString(value)
}

func validateProductInstallationView(view ProductInstallationView) error {
	if view.SchemaVersion != ProductInstallationSchemaVersion {
		return errors.New("invalid product installation schema")
	}
	switch view.Status {
	case ProductInstallationStatusReady, ProductInstallationStatusBlocked, ProductInstallationStatusComplete:
	default:
		return errors.New("invalid product installation status")
	}
	if !validProductInstallationReason(view.ReasonCode) ||
		!validOptionalProductInstallationToken(view.ActiveGenerationID) ||
		!validOptionalProductInstallationToken(view.CandidateGenerationID) {
		return errors.New("invalid product installation public state")
	}
	engine := view.Engine
	if !validOptionalProductInstallationToken(engine.APIVersion) ||
		!validOptionalProductInstallationToken(engine.OperatingSystem) ||
		!validOptionalProductInstallationToken(engine.Architecture) ||
		!validOptionalProductInstallationToken(engine.ContextName) {
		return errors.New("invalid product installation Engine state")
	}
	if engine.Ready && (engine.APIVersion == "" || engine.OperatingSystem == "" || engine.Architecture == "" || engine.ContextName == "") {
		return errors.New("incomplete product installation Engine state")
	}
	return nil
}

func validOptionalProductInstallationToken(value string) bool {
	return value == "" || productInstallationPublicTokenPattern.MatchString(value)
}

func validProductInstallationReason(value string) bool {
	switch value {
	case "", ProductInstallationReasonObservationUnavailable, ProductInstallationReasonIdentityMismatch,
		ProductInstallationReasonAuthorityRequired, ProductInstallationReasonIdempotencyConflict,
		ProductInstallationReasonBusy, ProductInstallationReasonCapabilityProbeFailed,
		ProductInstallationReasonGenerationReferenced, ProductInstallationReasonAssetIdentityConflict,
		ProductInstallationReasonInvalidRequest, ProductInstallationReasonOperationFailed:
		return true
	default:
		return false
	}
}

func writeProductInstallationError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, productinstall.ErrAuthorityRequired):
		writeError(writer, http.StatusForbidden, errors.New("product installation authority is required"))
	case errors.Is(err, productinstall.ErrIdempotencyConflict), errors.Is(err, productinstall.ErrBusy),
		errors.Is(err, productinstall.ErrConcurrentUpdate), errors.Is(err, productinstall.ErrAssetConflict),
		errors.Is(err, productinstall.ErrGenerationReferenced):
		writeError(writer, http.StatusConflict, errors.New("product installation state conflicts with the request"))
	case errors.Is(err, productinstall.ErrInvalidRequest), errors.Is(err, productinstall.ErrInvalidState):
		writeError(writer, http.StatusUnprocessableEntity, errors.New("product installation request is invalid"))
	default:
		writeError(writer, http.StatusServiceUnavailable, errors.New("product installation operation is unavailable"))
	}
}
