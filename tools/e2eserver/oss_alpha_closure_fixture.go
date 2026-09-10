//go:build chora_e2e

package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"

	"github.com/Yangyang96/chora/internal/localweb"
	"github.com/Yangyang96/chora/internal/preflight"
	"github.com/Yangyang96/chora/internal/productinstall"
)

const trustedLocalDisclosurePolicy = "chora.trusted-local-disclosure.v1"

type ossAlphaClosureServer struct {
	base       *localweb.Server
	controller *ossAlphaInstallationController
	handler    http.Handler
	ackMu      sync.RWMutex
	ackMode    string
}

func newOSSAlphaClosureServer(ctx context.Context, databasePath, webRoot string, logger *log.Logger) (*ossAlphaClosureServer, error) {
	installationOnlyRoot := filepath.Join(filepath.Dir(databasePath), "oss-alpha-installation-only")
	if err := os.MkdirAll(installationOnlyRoot, 0o700); err != nil {
		return nil, err
	}
	controller := newOSSAlphaInstallationController()
	base, err := localweb.NewProduct(ctx, databasePath, webRoot, logger, localweb.ProductOptions{
		RepoRoot:               installationOnlyRoot,
		RepositoryRoot:         installationOnlyRoot,
		PiAuthFile:             filepath.Join(installationOnlyRoot, "absent-pi-auth.json"),
		InstallationController: controller,
		PiPreflight: func(context.Context) preflight.Report {
			return preflight.Report{Status: preflight.StatusFailed}
		},
	})
	if err != nil {
		return nil, err
	}
	server := &ossAlphaClosureServer{
		base: base, controller: controller, ackMode: "missing",
	}
	server.handler = http.HandlerFunc(server.serveHTTP)
	return server, nil
}

func (server *ossAlphaClosureServer) Close() error {
	return server.base.Close()
}

func (server *ossAlphaClosureServer) Handler() http.Handler {
	return server.handler
}

func (server *ossAlphaClosureServer) serveHTTP(writer http.ResponseWriter, request *http.Request) {
	switch {
	case request.Method == http.MethodGet && request.URL.Path == "/api/agent-execution/trusted-local-acknowledgements/current":
		server.getFixtureAcknowledgement(writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/api/agent-execution/trusted-local-acknowledgements":
		server.recordCurrentAcknowledgement(writer, request)
	case request.Method == http.MethodPost && request.URL.Path == "/__e2e/oss-alpha-closure/acknowledgement-state":
		server.setFixtureAcknowledgement(writer, request)
	case request.Method == http.MethodGet && request.URL.Path == "/__e2e/oss-alpha-closure/audit":
		writeFixtureJSON(writer, http.StatusOK, server.controller.audit())
	default:
		server.base.Handler().ServeHTTP(writer, request)
	}
}

func (server *ossAlphaClosureServer) getFixtureAcknowledgement(writer http.ResponseWriter, request *http.Request) {
	server.ackMu.RLock()
	mode := server.ackMode
	server.ackMu.RUnlock()
	switch mode {
	case "missing":
		writeFixtureJSON(writer, http.StatusOK, map[string]any{
			"acknowledged": false, "policyVersion": trustedLocalDisclosurePolicy,
		})
	case "stale":
		writeFixtureJSON(writer, http.StatusOK, map[string]any{
			"acknowledged": true, "policyVersion": "chora.trusted-local-disclosure.v0", "acknowledgedAt": "2026-08-26T00:00:00Z",
		})
	default:
		server.base.Handler().ServeHTTP(writer, request)
	}
}

func (server *ossAlphaClosureServer) recordCurrentAcknowledgement(writer http.ResponseWriter, request *http.Request) {
	recorder := httptest.NewRecorder()
	server.base.Handler().ServeHTTP(recorder, request)
	if recorder.Code >= http.StatusOK && recorder.Code < http.StatusMultipleChoices {
		server.ackMu.Lock()
		server.ackMode = "stored"
		server.ackMu.Unlock()
	}
	for key, values := range recorder.Header() {
		for _, value := range values {
			writer.Header().Add(key, value)
		}
	}
	writer.WriteHeader(recorder.Code)
	_, _ = writer.Write(recorder.Body.Bytes())
}

func (server *ossAlphaClosureServer) setFixtureAcknowledgement(writer http.ResponseWriter, request *http.Request) {
	defer request.Body.Close()
	var input struct {
		Mode string `json:"mode"`
	}
	decoder := json.NewDecoder(io.LimitReader(request.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil || (input.Mode != "missing" && input.Mode != "stale" && input.Mode != "stored") {
		writeFixtureJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid fixture acknowledgement mode"})
		return
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		writeFixtureJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid fixture acknowledgement mode"})
		return
	}
	server.ackMu.Lock()
	server.ackMode = input.Mode
	server.ackMu.Unlock()
	writeFixtureJSON(writer, http.StatusOK, map[string]string{"mode": input.Mode})
}

func writeFixtureJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

type ossAlphaInstallationController struct {
	mu        sync.Mutex
	view      localweb.ProductInstallationView
	mutations []ossAlphaMutationAudit
	records   map[ossAlphaMutationKey]ossAlphaMutationRecord
	nextSlot  int
}

type ossAlphaMutationAudit struct {
	Action                   string `json:"action"`
	Outcome                  string `json:"outcome"`
	IdempotencySlot          int    `json:"idempotencySlot"`
	IdempotencyKeyPresent    bool   `json:"idempotencyKeyPresent"`
	ConfirmPinnedEngineTools bool   `json:"confirmPinnedEngineTools"`
	ConfirmColimaVM          bool   `json:"confirmColimaVM"`
	Limit                    int    `json:"limit"`
}

type ossAlphaMutationKey struct {
	Action         localweb.ProductInstallationAction
	IdempotencyKey string
}

type ossAlphaRequestIdentity struct {
	ActorID                  string
	SessionID                string
	ConfirmPinnedEngineTools bool
	ConfirmColimaVM          bool
	Limit                    int
}

type ossAlphaMutationRecord struct {
	identity ossAlphaRequestIdentity
	result   localweb.ProductInstallationView
	slot     int
}

type ossAlphaAudit struct {
	Scenario                  string                  `json:"scenario"`
	ExternalOperationCount    int                     `json:"externalOperationCount"`
	ProductionExecutorPresent bool                    `json:"productionExecutorPresent"`
	Mutations                 []ossAlphaMutationAudit `json:"mutations"`
}

func newOSSAlphaInstallationController() *ossAlphaInstallationController {
	return &ossAlphaInstallationController{view: localweb.ProductInstallationView{
		SchemaVersion: localweb.ProductInstallationSchemaVersion,
		Status:        localweb.ProductInstallationStatusBlocked,
		ReasonCode:    localweb.ProductInstallationReasonAuthorityRequired,
		Engine:        localweb.ProductInstallationEngineView{},
		Actions:       localweb.ProductInstallationActionsView{Setup: true},
	}, records: make(map[ossAlphaMutationKey]ossAlphaMutationRecord)}
}

func (controller *ossAlphaInstallationController) Doctor(context.Context) (localweb.ProductInstallationView, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	view := controller.view
	view.Replayed = false
	view.RestartRequired = false
	return view, nil
}

func (controller *ossAlphaInstallationController) Mutate(_ context.Context, request localweb.ProductInstallationMutationRequest) (localweb.ProductInstallationView, error) {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	audit := ossAlphaMutationAudit{
		Action: string(request.Action), IdempotencyKeyPresent: request.IdempotencyKey != "",
		ConfirmPinnedEngineTools: request.ConfirmPinnedEngineTools, ConfirmColimaVM: request.ConfirmColimaVM, Limit: request.Limit,
	}
	key := ossAlphaMutationKey{Action: request.Action, IdempotencyKey: request.IdempotencyKey}
	identity := ossAlphaRequestIdentity{
		ActorID: request.ActorID, SessionID: request.SessionID,
		ConfirmPinnedEngineTools: request.ConfirmPinnedEngineTools, ConfirmColimaVM: request.ConfirmColimaVM, Limit: request.Limit,
	}
	if previous, exists := controller.records[key]; exists {
		audit.IdempotencySlot = previous.slot
		if previous.identity != identity {
			audit.Outcome = "conflict"
			controller.mutations = append(controller.mutations, audit)
			return localweb.ProductInstallationView{}, productinstall.ErrIdempotencyConflict
		}
		audit.Outcome = "replay"
		controller.mutations = append(controller.mutations, audit)
		result := previous.result
		result.Replayed = true
		return result, nil
	}
	controller.nextSlot++
	audit.IdempotencySlot = controller.nextSlot
	audit.Outcome = "first"
	controller.mutations = append(controller.mutations, audit)

	ready := localweb.ProductInstallationView{
		SchemaVersion: localweb.ProductInstallationSchemaVersion,
		Status:        localweb.ProductInstallationStatusReady,
		Engine: localweb.ProductInstallationEngineView{
			Ready: true, APIVersion: "1.47", OperatingSystem: "linux", Architecture: "arm64", ContextName: "chora-e2e",
		},
		ActiveGenerationID: "generation-1",
		Actions: localweb.ProductInstallationActionsView{
			Upgrade: true, GC: true, Uninstall: true,
		},
	}
	switch request.Action {
	case localweb.ProductInstallationActionSetup:
		controller.view = ready
		result := ready
		result.Status = localweb.ProductInstallationStatusComplete
		result.RestartRequired = true
		controller.records[key] = ossAlphaMutationRecord{identity: identity, result: result, slot: audit.IdempotencySlot}
		return result, nil
	case localweb.ProductInstallationActionUpgrade:
		controller.view = ready
		result := ready
		result.Status = localweb.ProductInstallationStatusComplete
		result.RestartRequired = true
		controller.records[key] = ossAlphaMutationRecord{identity: identity, result: result, slot: audit.IdempotencySlot}
		return result, nil
	case localweb.ProductInstallationActionGC:
		controller.view = ready
		result := ready
		result.Status = localweb.ProductInstallationStatusComplete
		controller.records[key] = ossAlphaMutationRecord{identity: identity, result: result, slot: audit.IdempotencySlot}
		return result, nil
	case localweb.ProductInstallationActionUninstall:
		controller.view = localweb.ProductInstallationView{
			SchemaVersion: localweb.ProductInstallationSchemaVersion,
			Status:        localweb.ProductInstallationStatusBlocked,
			ReasonCode:    localweb.ProductInstallationReasonAuthorityRequired,
			Engine:        ready.Engine,
			Actions:       localweb.ProductInstallationActionsView{Setup: true},
		}
		result := controller.view
		result.Status = localweb.ProductInstallationStatusComplete
		result.RestartRequired = true
		controller.records[key] = ossAlphaMutationRecord{identity: identity, result: result, slot: audit.IdempotencySlot}
		return result, nil
	default:
		return localweb.ProductInstallationView{}, errors.New("unsupported fixture action")
	}
}

func (controller *ossAlphaInstallationController) audit() ossAlphaAudit {
	controller.mu.Lock()
	defer controller.mu.Unlock()
	return ossAlphaAudit{
		Scenario: ossAlphaClosureScenario, ExternalOperationCount: 0, ProductionExecutorPresent: false,
		Mutations: append([]ossAlphaMutationAudit(nil), controller.mutations...),
	}
}

var _ localweb.ProductInstallationController = (*ossAlphaInstallationController)(nil)
