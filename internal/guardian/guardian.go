// Package guardian is the independent restricted-container PID1 lease guard.
// A valid heartbeat never moves the absolute deadline. Terminating PID1 kills
// its private Linux PID namespace; the host must still inspect exact death.
package guardian

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"
)

type Sample struct {
	Wall time.Time
	Boot time.Duration
}
type Clock func() (Sample, error)
type Policy struct {
	AuthorizedAt, Deadline time.Time
	Lease                  time.Duration
}
type Heartbeat struct {
	Sequence uint64 `json:"sequence"`
	Stop     bool   `json:"stop,omitempty"`
}

// State uses BOOTTIME (including suspend), wall expiry, and rollback checks.
// A resumed/restarted guardian cannot recover a prior session or renew a lease.
type State struct {
	policy      Policy
	first, last Sample
	beat        time.Duration
	sequence    uint64
}

func New(p Policy, s Sample) (*State, error) {
	if p.AuthorizedAt.IsZero() || s.Wall.Before(p.AuthorizedAt) || !p.Deadline.After(s.Wall) || p.Deadline.Sub(p.AuthorizedAt) > 20*time.Minute || p.Lease <= 0 || p.Lease > 5*time.Second || s.Boot < 0 {
		return nil, errors.New("invalid or expired guardian policy")
	}
	return &State{policy: p, first: s, last: s, beat: s.Boot}, nil
}
func (s *State) Check(now Sample) error {
	if now.Wall.Before(s.last.Wall) || now.Boot < s.last.Boot || !now.Wall.Before(s.policy.Deadline) || now.Boot-s.first.Boot >= s.policy.Deadline.Sub(s.first.Wall) || now.Boot-s.beat >= s.policy.Lease {
		return errors.New("guardian expired, clock regressed, or lease lost")
	}
	s.last = now
	return nil
}
func (s *State) Accept(now Sample, b Heartbeat) error {
	if err := s.Check(now); err != nil {
		return err
	}
	if b.Stop || s.sequence == ^uint64(0) || b.Sequence != s.sequence+1 {
		return errors.New("guardian stopped or heartbeat sequence invalid")
	}
	s.sequence = b.Sequence
	s.beat = now.Boot
	return nil
}

func Run(ctx context.Context, p Policy, clock Clock, input io.ReadCloser, output io.Writer) error {
	initial, err := clock()
	if err != nil {
		return err
	}
	state, err := New(p, initial)
	if err != nil {
		return err
	}

	beats := make(chan Heartbeat)
	failure := make(chan error, 1)
	done := make(chan struct{})

	readerDone := make(chan struct{})
	defer func() { close(done); _ = input.Close(); <-readerDone }()
	go func() {
		defer close(readerDone)
		scanner := bufio.NewScanner(input)
		scanner.Buffer(make([]byte, 256), 1024)
		for scanner.Scan() {
			var b Heartbeat
			if e := json.Unmarshal(scanner.Bytes(), &b); e != nil {
				failure <- errors.New("invalid guardian heartbeat")
				return
			}
			select {
			case beats <- b:
			case <-done:
				return
			}
		}
		failure <- errors.New("guardian control EOF")
	}()
	if _, err = fmt.Fprintln(output, `{"type":"guardian_ready","schema":"chora.guardian.v1"}`); err != nil {
		return err
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err = <-failure:
			return err
		case beat := <-beats:
			now, e := clock()
			if e != nil {
				return e
			}
			if e = state.Accept(now, beat); e != nil {
				return e
			}
		case <-ticker.C:
			now, e := clock()
			if e != nil {
				return e
			}
			if e = state.Check(now); e != nil {
				return e
			}
		}
	}
}
