package app

import (
	"sync"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

type runtimeSignals struct {
	mu            sync.Mutex
	launch        execution.LaunchToken
	notifications map[execution.StreamKind]int64
	exited        bool
}

type trustedRuntimeSink struct{ signals *runtimeSignals }

func (sink *trustedRuntimeSink) Binding() execution.LaunchToken { return sink.signals.launch }
func (sink *trustedRuntimeSink) Notify(kind execution.StreamKind, offset int64) {
	sink.signals.mu.Lock()
	defer sink.signals.mu.Unlock()
	if current, ok := sink.signals.notifications[kind]; !ok || offset > current {
		sink.signals.notifications[kind] = offset
	}
}
func (sink *trustedRuntimeSink) Exited() {
	sink.signals.mu.Lock()
	sink.signals.exited = true
	sink.signals.mu.Unlock()
}

func (s *Service) bindRuntimeSink(sessionID domain.RuntimeSessionID, launch execution.LaunchToken) execution.RuntimeSink {
	signals := &runtimeSignals{launch: launch, notifications: make(map[execution.StreamKind]int64)}
	s.signals.Store(sessionID.String(), signals)
	return &trustedRuntimeSink{signals: signals}
}

func (s *Service) reserveStreamProof(sessionID domain.RuntimeSessionID, launch execution.LaunchToken, kind execution.StreamKind, offset int64) bool {
	value, ok := s.signals.Load(sessionID.String())
	if !ok {
		return false
	}
	signals := value.(*runtimeSignals)
	signals.mu.Lock()
	defer signals.mu.Unlock()
	notified, exists := signals.notifications[kind]
	if signals.launch != launch || signals.exited || !exists || notified < offset {
		return false
	}
	return true
}

func (s *Service) ackStreamProof(sessionID domain.RuntimeSessionID, launch execution.LaunchToken, kind execution.StreamKind, committedOffset int64) {
	value, ok := s.signals.Load(sessionID.String())
	if !ok {
		return
	}
	signals := value.(*runtimeSignals)
	signals.mu.Lock()
	defer signals.mu.Unlock()
	if signals.launch != launch {
		return
	}
	if highWater, exists := signals.notifications[kind]; exists && highWater <= committedOffset {
		delete(signals.notifications, kind)
	}
}

func (s *Service) consumeExitProof(sessionID domain.RuntimeSessionID, launch execution.LaunchToken) bool {
	value, ok := s.signals.Load(sessionID.String())
	if !ok {
		return false
	}
	signals := value.(*runtimeSignals)
	signals.mu.Lock()
	defer signals.mu.Unlock()
	if signals.launch != launch || !signals.exited {
		return false
	}
	return true
}

func (s *Service) cleanupRuntimeSignals(sessionID domain.RuntimeSessionID) {
	s.signals.Delete(sessionID.String())
}
