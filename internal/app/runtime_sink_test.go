package app

import (
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

func TestJSONResponseFailsClosedForUnsupportedValue(t *testing.T) {
	if _, err := jsonResponse(make(chan int)); err == nil {
		t.Fatal("unsupported response value was accepted")
	}
}

func TestCleanupRuntimeSignalsDropsCapabilities(t *testing.T) {
	service := &Service{}
	sessionID := domain.NewRuntimeSessionID()
	service.bindRuntimeSink(sessionID, execution.LaunchToken{Value: "launch"})
	if _, ok := service.signals.Load(sessionID.String()); !ok {
		t.Fatal("runtime signal was not registered")
	}
	service.cleanupRuntimeSignals(sessionID)
	if _, ok := service.signals.Load(sessionID.String()); ok {
		t.Fatal("runtime signal was not cleaned up")
	}
}
