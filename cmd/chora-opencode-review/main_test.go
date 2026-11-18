package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/agent/opencoderun"
	"github.com/Yangyang96/chora/internal/execution"
)

// Uses the actual command input/output boundary. The signing key lives in the
// reviewer, outside the command. No native executable or model task is used.
func TestReviewCommandPendingToProtocolEvidenceAndSafetyRefusal(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, binary := filepath.Join(root, "home"), filepath.Join(root, "fixture")
	for _, p := range []string{home} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	const help = "--pure --format --model"
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ncase \"$*\" in\n--version) echo 1.20.0;;\n'run --help') echo '"+help+"';;\n*) exit 88;;\nesac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	keyPath := filepath.Join(root, "reviewer.pub")
	if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	input, reviewerInput := io.Pipe()
	reviewerOutput, output := io.Pipe()
	defer input.Close()
	defer reviewerInput.Close()
	defer reviewerOutput.Close()
	defer output.Close()
	done := make(chan error, 1)
	go func() {
		done <- run([]string{"-executable", binary, "-home", home, "-model", "deepseek/fixture", "-reviewer-key", keyPath, "-reviewer", "reviewer", "-source-reference", "https://example.invalid/source/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "-source-commit", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "-evidence-sha256", "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}, input, output)
		output.Close()
	}()
	reader := bufio.NewReader(reviewerOutput)
	proposal, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var challenge struct {
		Request     opencoderun.ProtocolReviewRequest
		MessageHex  string
		Before      execution.Readiness
		Attestation opencoderun.ProtocolAttestation
	}
	if err := json.Unmarshal(proposal, &challenge); err != nil {
		t.Fatal(err)
	}
	if challenge.Before.Qualification == nil || challenge.Before.Qualification.State != execution.QualificationRejected || !challenge.Request.Fixture {
		t.Fatal("ordinary flags promoted a shell fixture", string(proposal))
	}
	message := opencoderun.ProtocolReviewMessage(challenge.Request, "reviewer", challenge.Attestation)
	if hex.EncodeToString(message[:]) != challenge.MessageHex {
		t.Fatal("message not bound to reviewed request")
	}
	signature := ed25519.Sign(private, message[:])
	if err := json.NewEncoder(reviewerInput).Encode(struct{ SignatureHex string }{hex.EncodeToString(signature)}); err != nil {
		t.Fatal(err)
	}
	result, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var ready execution.Readiness
	if err := json.Unmarshal(result, &ready); err != nil {
		t.Fatal(err)
	}
	if ready.Ready || !strings.Contains(ready.Failure, opencoderun.ErrNativeConfig.Error()) || !strings.Contains(ready.Failure, opencoderun.ErrNativeRetry.Error()) || ready.Qualification == nil {
		t.Fatal(string(result))
	}
	for _, e := range ready.Qualification.Evidence {
		if e.Capability == "event_frames" && (e.State != execution.QualificationVerified || e.Method != "signed_fixture_conformance:adapter_only") {
			t.Fatal(e)
		}
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("review command did not exit")
	}
	for _, p := range []string{home} {
		entries, err := os.ReadDir(p)
		if err != nil || len(entries) != 0 {
			t.Fatal("review touched selected scope", entries, err)
		}
	}
	if r := opencoderun.Inspect(context.Background(), opencoderun.Config{Executable: binary, Home: home, Model: "deepseek/fixture"}); r.Qualification.State != execution.QualificationRejected {
		t.Fatal("command leaked global approval", r)
	}
	t.Log("command session: adapter fixture evidence registered; native config/retry/auth rejected; ordinary provider retains pending semantics")
}
