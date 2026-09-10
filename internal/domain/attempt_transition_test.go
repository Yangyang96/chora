package domain

import (
	"errors"
	"testing"
)

func TestAttemptTransitions(t *testing.T) {
	tests := []struct {
		name  string
		from  AttemptState
		event AttemptEventKind
		want  AttemptState
	}{
		{"start attempt", AttemptStateCreated, AttemptEventStartAttempt, AttemptStateStarting},
		{"attempt started", AttemptStateStarting, AttemptEventAttemptStarted, AttemptStateRunning},
		{"start failed", AttemptStateStarting, AttemptEventStartFailed, AttemptStateFailed},
		{"review output submitted", AttemptStateRunning, AttemptEventOutputSubmitted, AttemptStateOutputSubmitted},
		{"revision output submitted", AttemptStateRunning, AttemptEventOutputSubmitted, AttemptStateOutputSubmitted},
		{"attempt failed while starting", AttemptStateStarting, AttemptEventAttemptFailed, AttemptStateFailed},
		{"attempt failed while running", AttemptStateRunning, AttemptEventAttemptFailed, AttemptStateFailed},
		{"revision stop while starting", AttemptStateStarting, AttemptEventStopConfirmedForRevision, AttemptStateInterrupted},
		{"revision stop while running", AttemptStateRunning, AttemptEventStopConfirmedForRevision, AttemptStateInterrupted},
		{"cancel before start", AttemptStateCreated, AttemptEventStopConfirmedForCancel, AttemptStateCancelled},
		{"cancel while starting", AttemptStateStarting, AttemptEventStopConfirmedForCancel, AttemptStateCancelled},
		{"cancel while running", AttemptStateRunning, AttemptEventStopConfirmedForCancel, AttemptStateCancelled},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := TransitionAttempt(test.from, test.event)
			if err != nil {
				t.Fatalf("TransitionAttempt(%q, %q) error = %v", test.from, test.event, err)
			}
			if got != test.want {
				t.Fatalf("TransitionAttempt(%q, %q) = %q, want %q", test.from, test.event, got, test.want)
			}
		})
	}
}

func TestAttemptTransitionsRejectUnspecifiedAndTerminalMutations(t *testing.T) {
	tests := []struct {
		name  string
		from  AttemptState
		event AttemptEventKind
	}{
		{"unspecified", AttemptStateCreated, AttemptEventAttemptStarted},
		{"output submitted terminal", AttemptStateOutputSubmitted, AttemptEventAttemptFailed},
		{"failed terminal", AttemptStateFailed, AttemptEventStartAttempt},
		{"interrupted terminal", AttemptStateInterrupted, AttemptEventStartAttempt},
		{"cancelled terminal", AttemptStateCancelled, AttemptEventStartAttempt},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := TransitionAttempt(test.from, test.event)
			if !errors.Is(err, ErrInvalidAttemptTransition) {
				t.Fatalf("TransitionAttempt(%q, %q) error = %v, want ErrInvalidAttemptTransition", test.from, test.event, err)
			}
			if got != test.from {
				t.Fatalf("TransitionAttempt(%q, %q) = %q on rejection, want unchanged %q", test.from, test.event, got, test.from)
			}
		})
	}
}
