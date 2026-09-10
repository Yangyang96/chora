package sqlite_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestInsertRunRejectsClosedTaskWithoutMutatingRoom(t *testing.T) {
	db, seeded := openSeeded(t)
	defer db.Close()
	raw, err := sql.Open("sqlite", seeded.path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.Exec(`UPDATE tasks SET state='closed' WHERE id=?`, seeded.task.ID().String()); err != nil {
		raw.Close()
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	next, err := domain.NewAgentRun(domain.NewRunID(), seeded.task.ID(), seeded.charter.ID(), seeded.now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error { return tx.InsertRun(context.Background(), next) })
	if !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("InsertRun on closed Task err=%v", err)
	}
	if _, err := db.Reader().GetRun(context.Background(), next.ID()); !errors.Is(err, storecontract.ErrNotFound) {
		t.Fatalf("closed Task retained Run err=%v", err)
	}
	room, err := db.Reader().GetRoom(context.Background(), seeded.room.ID())
	if err != nil || room.Version() != seeded.room.Version() {
		t.Fatalf("failed InsertRun mutated Room=%#v err=%v", room, err)
	}
}

func TestSaveRunCASRejectsVersionJumpAndIllegalStateWithoutPartialWrites(t *testing.T) {
	for _, tc := range []struct {
		name    string
		version uint64
		state   domain.RunState
	}{{"version jump", 2, domain.RunStateReady}, {"illegal state", 1, domain.RunStateRunning}} {
		t.Run(tc.name, func(t *testing.T) {
			db, seeded := openSeeded(t)
			defer db.Close()
			next, err := domain.RestoreAgentRun(domain.AgentRunRecord{ID: seeded.run.ID(), TaskID: seeded.run.TaskID(), CharterID: seeded.run.CharterID(), State: tc.state, Version: tc.version, CreatedAt: seeded.now, UpdatedAt: seeded.now.Add(time.Second), StartedAt: func() time.Time {
				if tc.state == domain.RunStateRunning {
					return seeded.now.Add(time.Second)
				}
				return time.Time{}
			}()})
			if err != nil {
				t.Fatal(err)
			}
			observation := storecontract.Observation{ID: domain.NewObservationID(), RunID: seeded.run.ID(), Kind: "note", Body: "must rollback", CreatedAt: seeded.now}
			err = db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
				if err := tx.InsertObservation(context.Background(), observation); err != nil {
					return err
				}
				return tx.SaveRunCAS(context.Background(), 0, next)
			})
			if !errors.Is(err, storecontract.ErrVersionConflict) {
				t.Fatalf("err=%v", err)
			}
			run, err := db.Reader().GetRun(context.Background(), seeded.run.ID())
			if err != nil {
				t.Fatal(err)
			}
			if run.Version() != 0 || run.State() != domain.RunStateDraft {
				t.Fatalf("run mutated: state=%s version=%d", run.State(), run.Version())
			}
			if _, err := db.Reader().GetObservation(context.Background(), observation.ID); !errors.Is(err, storecontract.ErrNotFound) {
				t.Fatalf("partial observation: %v", err)
			}
		})
	}
}

func TestSaveRunCASRejectsTimestampRollback(t *testing.T) {
	db, seeded := openSeeded(t)
	defer db.Close()
	ready, err := seeded.run.Transition(domain.CommandPrepareRun, seeded.now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error { return tx.SaveRunCAS(context.Background(), 0, ready) }); err != nil {
		t.Fatal(err)
	}
	running, err := domain.RestoreAgentRun(domain.AgentRunRecord{ID: ready.ID(), TaskID: ready.TaskID(), CharterID: ready.CharterID(), State: domain.RunStateRunning, Version: 2, CurrentAttemptNumber: 1, CreatedAt: ready.CreatedAt(), UpdatedAt: seeded.now.Add(time.Second), StartedAt: seeded.now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error { return tx.SaveRunCAS(context.Background(), 1, running) })
	if !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("err=%v", err)
	}
}

func TestSaveRunCASRejectsBackdatedNewLifecycleTimestamp(t *testing.T) {
	db, seeded := openSeeded(t)
	defer db.Close()
	ready, err := seeded.run.Transition(domain.CommandPrepareRun, seeded.now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error { return tx.SaveRunCAS(context.Background(), 0, ready) }); err != nil {
		t.Fatal(err)
	}
	running, err := domain.RestoreAgentRun(domain.AgentRunRecord{ID: ready.ID(), TaskID: ready.TaskID(), CharterID: ready.CharterID(), State: domain.RunStateRunning, Version: 2, CurrentAttemptNumber: 1, CreatedAt: ready.CreatedAt(), UpdatedAt: seeded.now.Add(3 * time.Second), StartedAt: seeded.now.Add(time.Second)})
	if err != nil {
		t.Fatal(err)
	}
	err = db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error { return tx.SaveRunCAS(context.Background(), 1, running) })
	if !errors.Is(err, storecontract.ErrVersionConflict) {
		t.Fatalf("err=%v", err)
	}
}

func TestSaveRunCASRejectsInvalidAttemptAndLifecycleSuccessorsWithRollback(t *testing.T) {
	tests := []struct {
		name     string
		commands []domain.CommandKind
		next     func(domain.AgentRun, time.Time) domain.AgentRunRecord
	}{
		{
			name: "draft to ready without attempt increment",
			next: func(current domain.AgentRun, at time.Time) domain.AgentRunRecord {
				return runRecord(current, domain.RunStateReady, current.Version()+1, current.CurrentAttemptNumber(), at)
			},
		},
		{
			name:     "ready to running with extra attempt increment",
			commands: []domain.CommandKind{domain.CommandPrepareRun},
			next: func(current domain.AgentRun, at time.Time) domain.AgentRunRecord {
				record := runRecord(current, domain.RunStateRunning, current.Version()+1, current.CurrentAttemptNumber()+1, at)
				record.StartedAt = at
				return record
			},
		},
		{
			name: "started timestamp before ready to running",
			next: func(current domain.AgentRun, at time.Time) domain.AgentRunRecord {
				record := runRecord(current, domain.RunStateReady, current.Version()+1, current.CurrentAttemptNumber()+1, at)
				record.StartedAt = at
				return record
			},
		},
		{
			name:     "review timestamp before awaiting review",
			commands: []domain.CommandKind{domain.CommandPrepareRun},
			next: func(current domain.AgentRun, at time.Time) domain.AgentRunRecord {
				record := runRecord(current, domain.RunStateRunning, current.Version()+1, current.CurrentAttemptNumber(), at)
				record.StartedAt = at
				record.ReviewRequestedAt = at
				return record
			},
		},
		{
			name:     "terminal timestamp before terminal state",
			commands: []domain.CommandKind{domain.CommandPrepareRun, domain.CommandStartAttempt},
			next: func(current domain.AgentRun, at time.Time) domain.AgentRunRecord {
				record := runRecord(current, domain.RunStateAwaitingReview, current.Version()+1, current.CurrentAttemptNumber(), at)
				record.StartedAt = current.StartedAt()
				record.ReviewRequestedAt = at
				record.TerminalAt = at
				return record
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db, seeded := openSeeded(t)
			defer db.Close()
			current := seeded.run
			for index, command := range test.commands {
				next, err := current.Transition(command, seeded.now.Add(time.Duration(index+1)*time.Second))
				if err != nil {
					t.Fatal(err)
				}
				if err := db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
					return tx.SaveRunCAS(context.Background(), current.Version(), next)
				}); err != nil {
					t.Fatal(err)
				}
				current = next
			}
			at := seeded.now.Add(time.Duration(len(test.commands)+1) * time.Second)
			invalid, err := domain.RestoreAgentRun(test.next(current, at))
			if err != nil {
				t.Fatal(err)
			}
			observation := storecontract.Observation{ID: domain.NewObservationID(), RunID: current.ID(), Kind: "note", Body: "must rollback", CreatedAt: at}
			err = db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error {
				if err := tx.InsertObservation(context.Background(), observation); err != nil {
					return err
				}
				return tx.SaveRunCAS(context.Background(), current.Version(), invalid)
			})
			if !errors.Is(err, storecontract.ErrVersionConflict) {
				t.Fatalf("err=%v", err)
			}
			stored, err := db.Reader().GetRun(context.Background(), current.ID())
			if err != nil {
				t.Fatal(err)
			}
			if stored.Version() != current.Version() || stored.State() != current.State() {
				t.Fatalf("run mutated: before=%#v after=%#v", current, stored)
			}
			if _, err := db.Reader().GetObservation(context.Background(), observation.ID); !errors.Is(err, storecontract.ErrNotFound) {
				t.Fatalf("partial observation: %v", err)
			}
		})
	}
}

func runRecord(current domain.AgentRun, state domain.RunState, version uint64, attempt int, updatedAt time.Time) domain.AgentRunRecord {
	return domain.AgentRunRecord{ID: current.ID(), TaskID: current.TaskID(), CharterID: current.CharterID(), State: state, Version: version, CurrentAttemptNumber: attempt, CreatedAt: current.CreatedAt(), UpdatedAt: updatedAt, StartedAt: current.StartedAt(), ReviewRequestedAt: current.ReviewRequestedAt(), TerminalAt: current.TerminalAt()}
}
