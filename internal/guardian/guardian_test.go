package guardian

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"
)

func TestGuardianDeadlineLeaseRollbackAndSuspend(t *testing.T) {
	start := time.Date(2026, 11, 17, 0, 0, 0, 0, time.UTC)
	for _, mode := range []string{"deadline", "lease", "rollback", "boot-regression", "suspend", "heartbeat-replay", "normal"} {
		t.Run(mode, func(t *testing.T) {
			s, err := New(Policy{start, start.Add(10 * time.Second), 2 * time.Second}, Sample{start, time.Second})
			if err != nil {
				t.Fatal(err)
			}
			now := Sample{start.Add(time.Second), 2 * time.Second}
			if err = s.Accept(now, Heartbeat{Sequence: 1}); err != nil {
				t.Fatal(err)
			}
			next := Sample{start.Add(1500 * time.Millisecond), 2500 * time.Millisecond}
			switch mode {
			case "deadline":
				next.Wall = start.Add(10 * time.Second)
			case "lease":
				next = Sample{start.Add(3 * time.Second), 4 * time.Second}
			case "rollback":
				next.Wall = start.Add(-time.Second)
			case "boot-regression":
				next.Boot = time.Second
			case "suspend":
				next.Boot = 11 * time.Second
			}
			if mode == "heartbeat-replay" {
				err = s.Accept(next, Heartbeat{Sequence: 1})
			} else {
				err = s.Check(next)
			}
			if (err == nil) != (mode == "normal") {
				t.Fatalf("mode=%s err=%v", mode, err)
			}
		})
	}
}
func TestGuardianEOFExpiresWithoutHostTimer(t *testing.T) {
	start := time.Now()
	in, out := io.Pipe()
	closed := make(chan error, 1)
	var ready bytes.Buffer
	go func() {
		closed <- Run(context.Background(), Policy{start, start.Add(time.Minute), time.Second}, func() (Sample, error) { return Sample{time.Now(), time.Since(start)}, nil }, in, &ready)
	}()
	_ = out.Close() // independent pipe lifetime, no Pi or Docker runtime
	select {
	case err := <-closed:
		if err == nil {
			t.Fatal("EOF kept guardian alive")
		}
	case <-time.After(time.Second):
		t.Fatal("guardian did not exit on EOF")
	}
}
