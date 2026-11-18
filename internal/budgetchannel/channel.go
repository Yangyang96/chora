// Package budgetchannel separates untrusted model frames from Pi's RPC stream.
// It opens no listener and carries no credential or general business command.
package budgetchannel

import (
	"bufio"
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

const MaxFrame = 300 << 10
const MaxPayload = 256 << 10

//go:embed channel.go
var protocolSource []byte

func ProtocolIdentity() string { return domain.WorkflowDigest(protocolSource) }

type Frame struct {
	Type    string          `json:"type"`
	ID      string          `json:"id,omitempty"`
	Payload json.RawMessage `json:"payload,omitempty"`
	Error   string          `json:"error,omitempty"`
}
type Port interface {
	Dispatch(context.Context, domain.RestrictedLaunch, string, []byte) (json.RawMessage, error)
	Revoke(context.Context, domain.RestrictedLaunch) error
}

// JSONDepth also rejects excessive nesting before unmarshalling allocates a
// recursive object graph. Strings, escaped quotes and braces remain data.
func JSONDepth(raw []byte, max int) bool {
	depth := 0
	quoted, escaped := false, false
	for _, b := range raw {
		if quoted {
			if escaped {
				escaped = false
			} else if b == '\\' {
				escaped = true
			} else if b == '"' {
				quoted = false
			}
			continue
		}
		if b == '"' {
			quoted = true
		} else if b == '[' || b == '{' {
			depth++
			if depth > max {
				return false
			}
		} else if b == ']' || b == '}' {
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0 && !quoted && json.Valid(raw)
}
func Decode(raw []byte) (f Frame, err error) {
	if len(raw) > MaxFrame || !JSONDepth(raw, 32) {
		return f, errors.New("invalid bounded frame")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err = d.Decode(&f); err != nil {
		return f, errors.New("invalid frame fields")
	}
	if len(f.Payload) > MaxPayload || f.Error != "" {
		return f, errors.New("invalid request frame")
	}
	switch f.Type {
	case "pi_rpc":
		if f.ID != "" || len(f.Payload) == 0 {
			return f, errors.New("invalid RPC frame")
		}
	case "dispatch_request":
		if len(f.ID) == 0 || len(f.ID) > 128 || len(f.Payload) == 0 {
			return f, errors.New("invalid dispatch frame")
		}
		for _, c := range f.ID {
			if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
				return f, errors.New("invalid request ID")
			}
		}
	default:
		return f, errors.New("unsupported frame type")
	}
	return f, nil
}

// Session binds an already-owned Process and immutable Go launch. Writes of
// prompts and replies share the same mutex. Receive handles one dispatch at a
// time; no pending queue or request is replayed after EOF or a partial write.
type Session struct {
	mu      sync.Mutex
	process io.ReadWriteCloser
	bound   domain.RestrictedLaunch
	port    Port
}

func New(process io.ReadWriteCloser, bound domain.RestrictedLaunch, port Port) (*Session, error) {
	if process == nil || port == nil || bound.Validate() != nil {
		return nil, errors.New("complete bound channel required")
	}
	return &Session{process: process, bound: bound, port: port}, nil
}
func (s *Session) write(f Frame) error {
	b, err := json.Marshal(f)
	if err != nil || len(b) > MaxFrame {
		return errors.New("reply exceeds frame limit")
	}
	b = append(b, '\n')
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.process.Write(b)
	if err == nil && n != len(b) {
		err = io.ErrShortWrite
	}
	return err
}
func (s *Session) SendRPC(raw json.RawMessage) error {
	if len(raw) > MaxPayload || !JSONDepth(raw, 32) {
		return errors.New("invalid bounded RPC")
	}
	return s.write(Frame{Type: "pi_rpc", Payload: raw})
}
func (s *Session) Receive(ctx context.Context, rpc io.Writer) (err error) {
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			_ = s.process.Close()
		case <-done:
		}
	}()
	defer func() { close(done); <-stopped }()

	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
		defer cancel()
		_ = s.process.Close()
		err = errors.Join(err, s.port.Revoke(cleanup, s.bound))
	}()
	scanner := bufio.NewScanner(s.process)
	scanner.Buffer(make([]byte, 4096), MaxFrame+1)
	for scanner.Scan() {
		if err = ctx.Err(); err != nil {
			return err
		}
		f, e := Decode(scanner.Bytes())
		if e != nil {
			return e
		}
		if f.Type == "pi_rpc" {
			if _, e = fmt.Fprintf(rpc, "%s\n", f.Payload); e != nil {
				return e
			}
			continue
		}
		raw, e := s.port.Dispatch(ctx, s.bound, f.ID, f.Payload)
		reply := Frame{Type: "dispatch_result", ID: f.ID, Payload: raw}
		if e != nil {
			reply.Payload = nil
			reply.Error = "dispatch_refused_or_unknown"
			if coded, ok := e.(interface{ ChannelError() string }); ok && coded.ChannelError() == "result_unreplayable_no_retransmission" {
				reply.Error = coded.ChannelError()
			}
		}
		if e = s.write(reply); e != nil {
			return e
		}
	}
	return scanner.Err()
}
