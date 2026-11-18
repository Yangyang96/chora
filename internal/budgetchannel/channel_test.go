package budgetchannel

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

func boundFixture() domain.RestrictedLaunch {
	now := time.Now().UTC()
	d := strings.Repeat("a", 64)
	return domain.RestrictedLaunch{Schema: "chora.restricted-launch.v2", Authorization: "auth", Workflow: "workflow", Project: "project", Task: "task", Charter: "charter", Run: "run", Attempt: "attempt", Actor: "actor", Gate: "gate", Operation: "op", InputDigest: d, AuthorityDigest: d, LaunchToken: "launch", Channel: "channel", SourceDigest: d, ConfigDigest: d, Epoch: 1, AuthorizedAt: now, ObservedAt: now, Deadline: now.Add(time.Minute)}
}

type fixturePort struct {
	got     domain.RestrictedLaunch
	calls   int
	revoked bool
}

func (p *fixturePort) Dispatch(_ context.Context, b domain.RestrictedLaunch, _ string, raw []byte) (json.RawMessage, error) {
	p.calls++
	p.got = b
	return raw, nil
}
func (p *fixturePort) Revoke(_ context.Context, b domain.RestrictedLaunch) error {
	p.revoked = true
	p.got = b
	return nil
}
func TestChannelSeparatesRPCDispatchAndRevokesEOF(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	p := &fixturePort{}
	bound := boundFixture()
	s, err := New(a, bound, p)
	if err != nil {
		t.Fatal(err)
	}
	var rpc bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- s.Receive(context.Background(), &rpc) }()
	_, _ = io.WriteString(b, "{\"type\":\"pi_rpc\",\"payload\":{\"type\":\"agent_start\"}}\n{\"type\":\"dispatch_request\",\"id\":\"one\",\"payload\":{\"messages\":[]}}\n")
	raw := make([]byte, 1024)
	n, err := b.Read(raw)
	if err != nil || !bytes.Contains(raw[:n], []byte(`"type":"dispatch_result"`)) {
		t.Fatalf("reply=%s %v", raw[:n], err)
	}
	_ = b.Close()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	if p.calls != 1 || p.got != bound || !p.revoked || !strings.Contains(rpc.String(), "agent_start") {
		t.Fatalf("port=%+v rpc=%s", p, rpc.String())
	}
}
func TestChannelRejectsIdentityInjectionOversizeAndDepth(t *testing.T) {
	for _, raw := range []string{`{"type":"dispatch_request","id":"one","payload":{},"run":"foreign"}`, `{"type":"dispatch_result","id":"one","payload":{}}`, `{"type":"dispatch_request","id":"../one","payload":{}}`, strings.Repeat("[", 33) + strings.Repeat("]", 33), strings.Repeat("x", MaxFrame+1)} {
		if _, err := Decode([]byte(raw)); err == nil {
			t.Fatal("untrusted frame admitted")
		}
	}
	if !JSONDepth([]byte(`{"text":"[\\\"{}]"}`), 2) {
		t.Fatal("JSON string was interpreted as nesting")
	}
}
