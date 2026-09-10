package trustedhost

import (
	"context"
	"errors"
	"io"
	"os"
	"time"

	"github.com/Yangyang96/chora/internal/execution"
)

func (supervisor *Supervisor) monitorStreams(record *processRecord) {
	ticker := time.NewTicker(supervisor.config.ProbeInterval)
	defer ticker.Stop()
	for {
		select {
		case <-record.done:
			supervisor.captureStreamOffsets(record)
			return
		case <-ticker.C:
			if supervisor.captureStreamOffsets(record) {
				proved, diagnostic := supervisor.terminateAndProve(context.Background(), record, true)
				if !proved {
					supervisor.markUncertain(record, joinDiagnostic("stream capacity exceeded", diagnostic))
				}
				return
			}
		}
	}
}

func (supervisor *Supervisor) captureStreamOffsets(record *processRecord) (overflow bool) {
	for _, kind := range []execution.StreamKind{execution.StreamStdout, execution.StreamStderr} {
		path, _ := record.streamPath(kind)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		size := info.Size()
		if size > supervisor.config.StreamCapacity {
			overflow = true
			record.mu.Lock()
			record.outputLimitExceeded = true
			record.mu.Unlock()
			size = supervisor.config.StreamCapacity
		}
		record.mu.Lock()
		previous := record.notified[kind]
		if size > previous {
			record.notified[kind] = size
		}
		record.mu.Unlock()
		if size > previous {
			safeNotify(record.sink, kind, size)
		}
	}
	return overflow
}

func readBoundedFile(path string, offset int64, limit int, capacity int64) ([]byte, int64, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, 0, 0, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, 0, 0, err
	}
	visible := info.Size()
	if visible > capacity {
		visible = capacity
	}
	if offset < 0 || offset > visible || limit < 0 {
		return nil, 0, 0, errors.New("invalid bounded file range")
	}
	count := int64(limit)
	if count > visible-offset {
		count = visible - offset
	}
	data := make([]byte, int(count))
	if count > 0 {
		n, readErr := file.ReadAt(data, offset)
		data = data[:n]
		if readErr != nil && !errors.Is(readErr, io.EOF) {
			return nil, 0, 0, readErr
		}
	}
	next := offset + int64(len(data))
	return data, next, visible, nil
}

func (supervisor *Supervisor) markTerminal(record *processRecord, diagnostic string) {
	record.mu.Lock()
	if record.terminalProof {
		record.mu.Unlock()
		return
	}
	record.terminalProof = true
	record.state = stateTerminal
	record.diagnostic = joinDiagnostic(record.diagnostic, diagnostic)
	record.mu.Unlock()
	var terminalErr error
	for _, path := range []string{record.stdoutPath, record.stderrPath} {
		if info, err := os.Stat(path); err == nil && info.Size() > supervisor.config.StreamCapacity {
			record.mu.Lock()
			record.outputLimitExceeded = true
			record.mu.Unlock()
			if err := os.Truncate(path, supervisor.config.StreamCapacity); err != nil {
				terminalErr = errors.Join(terminalErr, err)
				record.mu.Lock()
				record.diagnostic = joinDiagnostic(record.diagnostic, "truncate bounded stream: "+err.Error())
				record.mu.Unlock()
			}
		}
	}
	supervisor.captureStreamOffsets(record)
	if err := supervisor.persist(record); err != nil {
		terminalErr = errors.Join(terminalErr, err)
		record.mu.Lock()
		record.diagnostic = joinDiagnostic(record.diagnostic, "persist terminal process proof: "+err.Error())
		record.mu.Unlock()
	}
	record.mu.Lock()
	record.persistErr = terminalErr
	record.terminalPersisted = terminalErr == nil
	record.mu.Unlock()
}

func (supervisor *Supervisor) markUncertain(record *processRecord, diagnostic string) {
	record.mu.Lock()
	if record.terminalProof {
		record.mu.Unlock()
		return
	}
	record.state = stateUncertain
	record.diagnostic = joinDiagnostic(record.diagnostic, diagnostic)
	record.mu.Unlock()
	if err := supervisor.persist(record); err != nil {
		record.mu.Lock()
		record.persistErr = errors.Join(record.persistErr, err)
		record.mu.Unlock()
	}
}
