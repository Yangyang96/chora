package localweb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"strings"
	"time"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/productinstall"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

const (
	managedGenerationBindingEvent  = "product_generation.bound"
	managedGenerationBindingSource = "localweb"
	managedGenerationBindingSchema = "chora.product-generation-attempt-binding/v1"
)

var installationGenerationIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type generationReferencePublisher interface {
	PublishReferences(context.Context, productinstall.GenerationReferences) error
	WithInstallationLock(context.Context, func(context.Context) error) error
	Load(context.Context) (productinstall.State, error)
}

type managedGenerationBinding struct {
	SchemaVersion string `json:"schema_version"`
	AttemptID     string `json:"attempt_id"`
	GenerationID  string `json:"generation_id"`
}

func validInstallationGenerationID(value string) bool {
	return installationGenerationIDPattern.MatchString(value)
}

func (server *Server) managesInstalledGeneration() bool {
	return server.product && !server.sourceCheckout
}

func (server *Server) requireManagedGenerationConfiguration() error {
	if !server.managesInstalledGeneration() {
		return nil
	}
	if server.generationConfigErr != nil {
		return server.generationConfigErr
	}
	if server.generationPublisher == nil || !validInstallationGenerationID(server.installationGenerationID) {
		return errors.New("installed generation reference publication is unavailable")
	}
	return nil
}

// prepareAndBindManagedAttempt fences Attempt preparation, immutable generation
// binding, and durable reference publication with the product lifecycle lock.
// The returned Attempt cannot be started by a caller until its reference is
// visible to every GC and Uninstall process sharing the installation root.
func (server *Server) prepareAndBindManagedAttempt(ctx context.Context, prepare func(context.Context) (app.PrepareRunResult, error)) (app.PrepareRunResult, error) {
	if prepare == nil {
		return app.PrepareRunResult{}, errors.New("managed Attempt preparation is unavailable")
	}
	var prepared app.PrepareRunResult
	err := server.withGenerationLifecycleLock(ctx, func(lockCtx context.Context) error {
		var err error
		prepared, err = prepare(lockCtx)
		if err != nil {
			return err
		}
		return server.bindAndPublishManagedAttemptLocked(lockCtx, prepared.Attempt)
	})
	return prepared, err
}

// bindAndPublishManagedAttempt is retained for recovery and focused tests. New
// preparation paths use prepareAndBindManagedAttempt so no durable prepared
// Attempt can become visible before its generation reference is published.
func (server *Server) bindAndPublishManagedAttempt(ctx context.Context, attempt domain.Attempt) error {
	return server.withGenerationLifecycleLock(ctx, func(lockCtx context.Context) error {
		return server.bindAndPublishManagedAttemptLocked(lockCtx, attempt)
	})
}

func (server *Server) bindAndPublishManagedAttemptLocked(ctx context.Context, attempt domain.Attempt) error {
	binding := attempt.AgentExecutionProfileBinding()
	if !server.managesInstalledGeneration() || attempt.AdapterID() != agentpi.AdapterID || binding.ExecutionProvider() != domain.DockerExecutionProvider {
		return nil
	}
	if err := server.requireManagedGenerationConfiguration(); err != nil {
		return err
	}
	if err := server.requireLoadedGenerationCurrent(ctx); err != nil {
		return err
	}
	err := server.store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
		generationID, found, err := managedGenerationForAttempt(ctx, tx, attempt.RunID(), attempt.ID())
		if err != nil {
			return err
		}
		if found {
			if generationID != server.installationGenerationID {
				return errors.New("managed Attempt generation binding conflicts with installed generation")
			}
			return nil
		}
		eventID, err := managedGenerationEventID(attempt.ID())
		if err != nil {
			return err
		}
		payload, err := json.Marshal(managedGenerationBinding{
			SchemaVersion: managedGenerationBindingSchema,
			AttemptID:     attempt.ID().String(),
			GenerationID:  server.installationGenerationID,
		})
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		_, err = tx.AppendRunEvent(ctx, attempt.RunID(), storecontract.EventDraft{
			ID: eventID, Type: managedGenerationBindingEvent, Source: managedGenerationBindingSource,
			OccurredAt: now, RecordedAt: now, NormalizedJSON: payload,
		})
		return err
	})
	if err != nil {
		return err
	}
	return server.publishManagedGenerationReferencesLocked(ctx, &attempt)
}

// startManagedAttempt closes the publication-to-start race. A lifecycle
// mutation cannot change the durable active generation between the final check
// and creation of the managed runtime session.
func (server *Server) startManagedAttempt(ctx context.Context, attempt domain.Attempt, request app.StartAttemptRequest) (app.StartAttemptResult, error) {
	if !server.managesInstalledGeneration() || attempt.AdapterID() != agentpi.AdapterID || attempt.AgentExecutionProfileBinding().ExecutionProvider() != domain.DockerExecutionProvider {
		return server.service.StartAttempt(ctx, request)
	}
	var started app.StartAttemptResult
	err := server.withGenerationLifecycleLock(ctx, func(lockCtx context.Context) error {
		if err := server.requireLoadedGenerationCurrent(lockCtx); err != nil {
			return err
		}
		generationID, found, err := managedGenerationForAttempt(lockCtx, server.store.Reader(), attempt.RunID(), attempt.ID())
		if err != nil {
			return err
		}
		if !found || generationID != server.installationGenerationID {
			return errors.New("managed Attempt generation reference is unavailable")
		}
		started, err = server.service.StartAttempt(lockCtx, request)
		return err
	})
	return started, err
}

func (server *Server) withGenerationLifecycleLock(ctx context.Context, fn func(context.Context) error) error {
	if fn == nil {
		return errors.New("managed generation lifecycle callback is unavailable")
	}
	if !server.managesInstalledGeneration() {
		return fn(ctx)
	}
	if server.generationPublisher == nil {
		return fn(ctx)
	}
	server.generationRefMu.Lock()
	defer server.generationRefMu.Unlock()
	return server.generationPublisher.WithInstallationLock(ctx, fn)
}

func (server *Server) requireLoadedGenerationCurrent(ctx context.Context) error {
	if err := server.requireManagedGenerationConfiguration(); err != nil {
		return err
	}
	state, err := server.generationPublisher.Load(ctx)
	if err != nil {
		return errors.New("installed generation state is unavailable")
	}
	if state.ActiveGenerationID != server.installationGenerationID {
		return errors.New("installed generation changed; restart is required")
	}
	return nil
}

func managedGenerationEventID(attemptID domain.AttemptID) (domain.EventID, error) {
	return domain.ParseEventID("event_" + strings.TrimPrefix(attemptID.String(), "attempt_"))
}

func managedGenerationForAttempt(ctx context.Context, reader storecontract.Reader, runID domain.RunID, attemptID domain.AttemptID) (string, bool, error) {
	events, err := reader.ListRunEvents(ctx, runID)
	if err != nil {
		return "", false, err
	}
	var generationID string
	count := 0
	for _, event := range events {
		if event.Type() != managedGenerationBindingEvent {
			continue
		}
		if event.Source() != managedGenerationBindingSource {
			return "", false, errors.New("managed Attempt generation binding has an invalid source")
		}
		var payload managedGenerationBinding
		decoder := json.NewDecoder(bytes.NewReader(event.NormalizedJSON()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&payload); err != nil {
			return "", false, errors.New("managed Attempt generation binding is malformed")
		}
		var trailing any
		if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
			return "", false, errors.New("managed Attempt generation binding has trailing data")
		}
		if payload.SchemaVersion != managedGenerationBindingSchema || !validInstallationGenerationID(payload.GenerationID) {
			return "", false, errors.New("managed Attempt generation binding is invalid")
		}
		boundAttempt, err := domain.ParseAttemptID(payload.AttemptID)
		if err != nil {
			return "", false, errors.New("managed Attempt generation binding has an invalid Attempt")
		}
		if boundAttempt != attemptID {
			continue
		}
		count++
		if count > 1 || generationID != "" && generationID != payload.GenerationID {
			return "", false, errors.New("managed Attempt generation binding is duplicate or conflicting")
		}
		generationID = payload.GenerationID
	}
	return generationID, count == 1, nil
}

func (server *Server) publishManagedGenerationReferences(ctx context.Context, pending *domain.Attempt) error {
	return server.withGenerationLifecycleLock(ctx, func(lockCtx context.Context) error {
		return server.publishManagedGenerationReferencesLocked(lockCtx, pending)
	})
}

func (server *Server) publishManagedGenerationReferencesLocked(ctx context.Context, pending *domain.Attempt) error {
	if !server.managesInstalledGeneration() {
		return nil
	}
	candidates, err := server.store.Reader().ListStartupCandidates(ctx)
	if err != nil {
		return errors.New("managed Attempt candidate query failed")
	}
	active := make(map[string]struct{})
	recoverable := make(map[string]struct{})
	serving := make(map[string]struct{})
	if validInstallationGenerationID(server.installationGenerationID) {
		retired, retireErr := server.processServingGenerationRetired(ctx)
		if retireErr != nil {
			return retireErr
		}
		if !retired {
			// Serving references are distinct from Attempt ownership: GC always
			// protects them, while authenticated active/full Uninstall may retire
			// one only after exact durable deactivation under install.lock.
			serving[server.installationGenerationID] = struct{}{}
		}
	}
	seenAttempts := make(map[domain.AttemptID]struct{})
	for _, candidate := range candidates {
		if candidate.Attempt == nil {
			return errors.New("managed Attempt candidate is incomplete")
		}
		attempt := *candidate.Attempt
		if attempt.AdapterID() != agentpi.AdapterID {
			continue
		}
		binding := attempt.AgentExecutionProfileBinding()
		if !binding.Bound() {
			return errors.New("Pi Attempt candidate has no execution profile binding")
		}
		if binding.ExecutionProvider() == domain.TrustedHostExecutionProvider {
			continue
		}
		if binding.ExecutionProvider() != domain.DockerExecutionProvider {
			return errors.New("Pi Attempt candidate has an unsupported execution provider")
		}
		generationID, found, err := managedGenerationForAttempt(ctx, server.store.Reader(), attempt.RunID(), attempt.ID())
		if err != nil {
			return err
		}
		if !found {
			return errors.New("managed Attempt candidate has no generation binding")
		}
		seenAttempts[attempt.ID()] = struct{}{}
		if candidate.Run.State() == domain.RunStateRecoveryRequired || attempt.State() == domain.AttemptStateInterrupted || attempt.State() == domain.AttemptStateFailed {
			recoverable[generationID] = struct{}{}
		} else {
			active[generationID] = struct{}{}
		}
	}
	if pending != nil {
		if _, seen := seenAttempts[pending.ID()]; !seen && pending.AdapterID() == agentpi.AdapterID && pending.AgentExecutionProfileBinding().ExecutionProvider() == domain.DockerExecutionProvider {
			generationID, found, err := managedGenerationForAttempt(ctx, server.store.Reader(), pending.RunID(), pending.ID())
			if err != nil {
				return err
			}
			if !found {
				return errors.New("pending managed Attempt has no generation binding")
			}
			active[generationID] = struct{}{}
		}
	}
	if len(active) > 0 || len(recoverable) > 0 {
		if err := server.requireManagedGenerationConfiguration(); err != nil {
			return err
		}
	}
	if server.generationPublisher == nil {
		return nil
	}
	refs := productinstall.GenerationReferences{
		ServingGenerationIDs:            sortedGenerationIDs(serving),
		ActiveAttemptGenerationIDs:      sortedGenerationIDs(active),
		RecoverableAttemptGenerationIDs: sortedGenerationIDs(recoverable),
	}
	if err := server.generationPublisher.PublishReferences(ctx, refs); err != nil {
		return errors.New("publish managed generation references failed")
	}
	return nil
}

func (server *Server) processServingGenerationRetired(ctx context.Context) (bool, error) {
	state, err := server.generationPublisher.Load(ctx)
	if err != nil {
		return false, errors.New("installed generation state is unavailable")
	}
	if state.ActiveGenerationID == server.installationGenerationID {
		return false, nil
	}
	for _, operation := range state.Operations {
		if operation.Action != productinstall.ActionUninstall || operation.PriorActiveGeneration != server.installationGenerationID ||
			!slices.Contains(operation.TargetGenerationIDs, server.installationGenerationID) ||
			operation.RetiredServingGenerationID != server.installationGenerationID {
			continue
		}
		return true, nil
	}
	return false, nil
}

// publishStartupGenerationReferences preserves Trusted Local availability when
// only managed-generation configuration is broken, while refusing to recover
// any managed candidate until its exact durable references are published.
func (server *Server) publishStartupGenerationReferences(ctx context.Context) error {
	err := server.publishManagedGenerationReferences(ctx, nil)
	if err == nil {
		return nil
	}
	hasManaged, candidateErr := server.hasManagedStartupCandidate(ctx)
	if candidateErr != nil {
		return candidateErr
	}
	if hasManaged {
		return err
	}
	server.generationConfigErr = errors.New("installed generation reference publication is unavailable")
	return nil
}

func (server *Server) hasManagedStartupCandidate(ctx context.Context) (bool, error) {
	candidates, err := server.store.Reader().ListStartupCandidates(ctx)
	if err != nil {
		return false, errors.New("managed Attempt candidate query failed")
	}
	for _, candidate := range candidates {
		if candidate.Attempt == nil {
			return false, errors.New("managed Attempt candidate is incomplete")
		}
		attempt := *candidate.Attempt
		if attempt.AdapterID() != agentpi.AdapterID {
			continue
		}
		binding := attempt.AgentExecutionProfileBinding()
		if !binding.Bound() {
			return false, errors.New("Pi Attempt candidate has no execution profile binding")
		}
		switch binding.ExecutionProvider() {
		case domain.DockerExecutionProvider:
			return true, nil
		case domain.TrustedHostExecutionProvider:
		default:
			return false, errors.New("Pi Attempt candidate has an unsupported execution provider")
		}
	}
	return false, nil
}

func sortedGenerationIDs(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	slices.Sort(result)
	return result
}

func (server *Server) logGenerationReferenceRefresh(context string) {
	if err := server.publishManagedGenerationReferences(server.ctx, nil); err != nil {
		server.logger.Printf("refresh managed generation references after %s failed", context)
	}
}
