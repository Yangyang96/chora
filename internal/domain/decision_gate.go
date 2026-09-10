package domain

import (
	"fmt"
	"strings"
	"time"
)

const (
	MaxDecisionGateQuestionRunes       = 500
	MaxDecisionGateContextRunes        = 2_000
	MaxDecisionGateOptionIDRunes       = 100
	MaxDecisionGateOptionLabelRunes    = 200
	MaxDecisionGateOptionImpactRunes   = 1_000
	MaxDecisionGateRecommendationRunes = 1_000
	MaxDecisionGateImpactRunes         = 1_000
	MaxDecisionGateResolutionNoteRunes = 2_000
)

type DecisionGateStatus string

const (
	DecisionGateOpen     DecisionGateStatus = "open"
	DecisionGateResolved DecisionGateStatus = "resolved"
)

type DecisionGateOption struct {
	ID     string
	Label  string
	Impact string
}

type ExecutionDecisionGateParams struct {
	ID             DecisionGateID
	RunID          RunID
	AttemptID      AttemptID
	Question       string
	Context        string
	Options        []DecisionGateOption
	Recommendation string
	Impact         string
	RequestedAt    time.Time
}

type ExecutionDecisionGate struct {
	id               DecisionGateID
	runID            RunID
	attemptID        AttemptID
	question         string
	context          string
	options          []DecisionGateOption
	recommendation   string
	impact           string
	status           DecisionGateStatus
	selectedOptionID string
	note             string
	actorID          string
	sessionID        string
	requestedAt      time.Time
	resolvedAt       time.Time
}

func NewExecutionDecisionGate(params ExecutionDecisionGateParams) (ExecutionDecisionGate, error) {
	if !params.ID.Valid() || !params.RunID.Valid() || !params.AttemptID.Valid() ||
		!ValidTrustedContextText(params.Question, MaxDecisionGateQuestionRunes, true) ||
		!ValidTrustedContextText(params.Context, MaxDecisionGateContextRunes, true) ||
		!ValidTrustedContextText(params.Recommendation, MaxDecisionGateRecommendationRunes, true) ||
		!ValidTrustedContextText(params.Impact, MaxDecisionGateImpactRunes, true) ||
		len(params.Options) < 2 || len(params.Options) > 8 || params.RequestedAt.IsZero() {
		return ExecutionDecisionGate{}, fmt.Errorf("%w: invalid execution decision gate", ErrInvalidArgument)
	}
	seen := make(map[string]struct{}, len(params.Options))
	options := make([]DecisionGateOption, 0, len(params.Options))
	for _, option := range params.Options {
		option.ID = strings.TrimSpace(option.ID)
		option.Label = strings.TrimSpace(option.Label)
		option.Impact = strings.TrimSpace(option.Impact)
		if !ValidTrustedContextText(option.ID, MaxDecisionGateOptionIDRunes, true) ||
			!ValidTrustedContextText(option.Label, MaxDecisionGateOptionLabelRunes, true) ||
			!ValidTrustedContextText(option.Impact, MaxDecisionGateOptionImpactRunes, true) {
			return ExecutionDecisionGate{}, fmt.Errorf("%w: invalid decision option", ErrInvalidArgument)
		}
		if _, exists := seen[option.ID]; exists {
			return ExecutionDecisionGate{}, fmt.Errorf("%w: duplicate decision option", ErrInvalidArgument)
		}
		seen[option.ID] = struct{}{}
		options = append(options, option)
	}
	return ExecutionDecisionGate{
		id: params.ID, runID: params.RunID, attemptID: params.AttemptID,
		question: strings.TrimSpace(params.Question), context: strings.TrimSpace(params.Context), options: options,
		recommendation: strings.TrimSpace(params.Recommendation), impact: strings.TrimSpace(params.Impact),
		status: DecisionGateOpen, requestedAt: params.RequestedAt,
	}, nil
}

func RestoreExecutionDecisionGate(params ExecutionDecisionGateParams, status DecisionGateStatus, selectedOptionID, note, actorID, sessionID string, resolvedAt time.Time) (ExecutionDecisionGate, error) {
	gate, err := NewExecutionDecisionGate(params)
	if err != nil {
		return ExecutionDecisionGate{}, err
	}
	if status == DecisionGateOpen {
		if selectedOptionID != "" || note != "" || actorID != "" || sessionID != "" || !resolvedAt.IsZero() {
			return ExecutionDecisionGate{}, fmt.Errorf("%w: invalid open decision gate", ErrInvalidArgument)
		}
		return gate, nil
	}
	if status != DecisionGateResolved {
		return ExecutionDecisionGate{}, fmt.Errorf("%w: invalid decision gate status", ErrInvalidArgument)
	}
	return gate.Resolve(selectedOptionID, note, actorID, sessionID, resolvedAt)
}

func (gate ExecutionDecisionGate) Resolve(optionID, note, actorID, sessionID string, resolvedAt time.Time) (ExecutionDecisionGate, error) {
	optionID = strings.TrimSpace(optionID)
	if gate.status != DecisionGateOpen || !gate.hasOption(optionID) ||
		!ValidTrustedContextText(note, MaxDecisionGateResolutionNoteRunes, false) ||
		!ValidTrustedContextText(actorID, MaxTrustedContextIdentityRunes, true) ||
		!ValidTrustedContextText(sessionID, MaxTrustedContextIdentityRunes, true) ||
		resolvedAt.IsZero() || resolvedAt.Before(gate.requestedAt) {
		return ExecutionDecisionGate{}, fmt.Errorf("%w: decision gate resolution rejected", ErrInvalidArgument)
	}
	gate.status = DecisionGateResolved
	gate.selectedOptionID = optionID
	gate.note = strings.TrimSpace(note)
	gate.actorID = strings.TrimSpace(actorID)
	gate.sessionID = strings.TrimSpace(sessionID)
	gate.resolvedAt = resolvedAt
	return gate, nil
}

func (gate ExecutionDecisionGate) hasOption(id string) bool {
	for _, option := range gate.options {
		if option.ID == id {
			return true
		}
	}
	return false
}

func (gate ExecutionDecisionGate) ID() DecisionGateID         { return gate.id }
func (gate ExecutionDecisionGate) RunID() RunID               { return gate.runID }
func (gate ExecutionDecisionGate) AttemptID() AttemptID       { return gate.attemptID }
func (gate ExecutionDecisionGate) Question() string           { return gate.question }
func (gate ExecutionDecisionGate) Context() string            { return gate.context }
func (gate ExecutionDecisionGate) Recommendation() string     { return gate.recommendation }
func (gate ExecutionDecisionGate) Impact() string             { return gate.impact }
func (gate ExecutionDecisionGate) Status() DecisionGateStatus { return gate.status }
func (gate ExecutionDecisionGate) SelectedOptionID() string   { return gate.selectedOptionID }
func (gate ExecutionDecisionGate) Note() string               { return gate.note }
func (gate ExecutionDecisionGate) ActorID() string            { return gate.actorID }
func (gate ExecutionDecisionGate) SessionID() string          { return gate.sessionID }
func (gate ExecutionDecisionGate) RequestedAt() time.Time     { return gate.requestedAt }
func (gate ExecutionDecisionGate) ResolvedAt() time.Time      { return gate.resolvedAt }
func (gate ExecutionDecisionGate) Options() []DecisionGateOption {
	return append([]DecisionGateOption(nil), gate.options...)
}
