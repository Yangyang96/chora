package hermes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/agent"
	"github.com/Yangyang96/chora/internal/execution"
)

func fixture(t *testing.T, version string) Config {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	c := Config{Executable: filepath.Join(root, "entry"), NativeLauncher: filepath.Join(root, "native"), Interpreter: filepath.Join(root, "python"), SourceRoot: filepath.Join(root, "source"), AuthHome: filepath.Join(root, "auth")}
	for _, dir := range []string{c.SourceRoot, c.AuthHome, filepath.Join(c.SourceRoot, "hermes_cli")} {
		if err := os.Mkdir(dir, 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{c.Executable, c.NativeLauncher, c.Interpreter} {
		// A would-be metadata subprocess would mutate the selected scope before
		// exiting. Every public path must leave this canary absent.
		if err := os.WriteFile(path, []byte("#!/bin/sh\ntouch '"+filepath.Join(c.AuthHome, "unwanted-write")+"'\nsleep 30\n"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range semanticFiles {
		data := []byte("# synthetic metadata\n")
		if name == "install-stamp.json" {
			data, _ = json.Marshal(map[string]string{"displayVersion": version})
		}
		if err := os.WriteFile(filepath.Join(c.SourceRoot, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return c
}

func TestRefusalBeforeAnyNativeSideEffects(t *testing.T) {
	ctx := context.Background()
	c := fixture(t, "0.23.1")
	r, err := Inspect(ctx, c, nil)
	if err != nil || r.Version != "0.23.1" || r.Authentication != "absent" || r.Qualification.State != execution.QualificationPending {
		t.Fatal(r, err)
	}
	if err := os.WriteFile(filepath.Join(c.AuthHome, ".env"), []byte("PRIVATE_TOKEN=secret\x00\n"), 0600); err != nil {
		t.Fatal(err)
	}
	r, err = Inspect(ctx, c, nil)
	if err != nil || r.Authentication != "present_unverified" {
		t.Fatal(r, err)
	}
	a, err := New(ctx, c)
	var q *QualificationError
	if a != nil || !errors.Is(err, ErrSafety) || !errors.As(err, &q) {
		t.Fatal(a, err)
	}
	entries, _ := os.ReadDir(c.AuthHome)
	if len(entries) != 1 || entries[0].Name() != ".env" {
		t.Fatal("selected scope mutated")
	}
	raw, _ := os.ReadFile(filepath.Join(c.AuthHome, ".env"))
	if string(raw) != "PRIVATE_TOKEN=secret\x00\n" {
		t.Fatal("dotenv normalized before refusal")
	}
	diagnostic, _ := json.Marshal(r)
	if strings.Contains(string(diagnostic), "PRIVATE_TOKEN") || strings.Contains(string(diagnostic), c.AuthHome) {
		t.Fatal("private metadata exposed")
	}
	adapter := &Adapter{}
	if fp, e := adapter.Fingerprint(ctx); e == nil || fp.Valid() {
		t.Fatal("usable fingerprint")
	}
	if _, e := adapter.PrepareStart(ctx, execution.StartRequest{}); !errors.Is(e, ErrSafety) {
		t.Fatal(e)
	}
	if _, e := adapter.PrepareBoundStart(ctx, execution.StartRequest{}); !errors.Is(e, ErrSafety) {
		t.Fatal(e)
	}
	if _, e := adapter.PrepareResume(ctx, execution.ResumeRequest{}); !errors.Is(e, execution.ErrUnsupportedCapability) {
		t.Fatal(e)
	}
	if policy := adapter.Recover(execution.RecoveryRequest{}); policy.FreshRetry || !policy.MissingRouteUncertain {
		t.Fatal(policy)
	}
	registry := agent.NewRegistry()
	if e := registry.Register(adapter); e != nil {
		t.Fatal(e)
	}
	for _, feature := range []string{"resume", "skills", "mcp", "runtime.file_edit", "runtime.command_execution", "runtime.zero_implicit_context"} {
		if adapter.Describe().Require("trusted_host", "code", feature) == nil {
			t.Fatal("unverified capability", feature)
		}
	}
	if adapter.Describe().Require("docker", "code") == nil {
		t.Fatal("unsupported environment")
	}
}

func TestTrustedReviewUnknownVersionsStillCannotLaunch(t *testing.T) {
	for _, version := range []string{"0.23.1", "0.24.0-next.1"} {
		t.Run(version, func(t *testing.T) {
			c := fixture(t, version)
			public, private, _ := ed25519.GenerateKey(rand.Reader)
			reviews, err := NewProtocolReviews(map[string]ed25519.PublicKey{"host": public})
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			reviews.now = func() time.Time { return now }
			request, err := reviews.Validate(context.Background(), c, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			reference := "https://example.invalid/hermes/source/immutable"
			message := ProtocolReviewMessage(request, "host", reference)
			signature := ProtocolReviewSignature{Reviewer: "host", SourceReference: reference, Signature: ed25519.Sign(private, message[:])}
			forged := request
			forged.Identity.Version = "99.0.0"
			if reviews.Register(forged, signature) == nil {
				t.Fatal("forged request")
			}
			bad := signature
			bad.Reviewer = "untrusted"
			if reviews.Register(request, bad) == nil {
				t.Fatal("untrusted key")
			}
			other, _ := NewProtocolReviews(map[string]ed25519.PublicKey{"host": public})
			if other.Register(request, signature) == nil {
				t.Fatal("challenge imported across host registries")
			}
			if err := reviews.Register(request, signature); err != nil {
				t.Fatal(err)
			}
			if reviews.Register(request, signature) == nil {
				t.Fatal("replay accepted")
			}
			r, err := Inspect(context.Background(), c, reviews)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range r.Qualification.Evidence {
				want := execution.QualificationPending
				if e.Capability == "event_frames" || e.Capability == "tool_lifecycle" || e.Capability == "terminal_result" {
					want = execution.QualificationVerified
				}
				if e.State != want {
					t.Fatal(e)
				}
			}
			if _, err := NewReviewed(context.Background(), c, reviews); !errors.Is(err, ErrSafety) {
				t.Fatal(err)
			}
			fresh, _ := Inspect(context.Background(), c, other)
			if fresh.Qualification.Evidence[0].State != execution.QualificationPending {
				t.Fatal("review survived registry restart")
			}
			if err := os.WriteFile(filepath.Join(c.SourceRoot, "hermes_cli/stream_json.py"), []byte("# changed\n"), 0600); err != nil {
				t.Fatal(err)
			}
			drifted, _ := Inspect(context.Background(), c, reviews)
			if drifted.Qualification.Evidence[0].State != execution.QualificationPending {
				t.Fatal("source drift retained review")
			}
			if err := os.WriteFile(filepath.Join(c.SourceRoot, "hermes_cli/stream_json.py"), []byte("# synthetic metadata\n"), 0600); err != nil {
				t.Fatal(err)
			}
			now = now.Add(2 * time.Hour)
			expired, _ := Inspect(context.Background(), c, reviews)
			if expired.Qualification.Evidence[0].State != execution.QualificationPending {
				t.Fatal("expired approval")
			}
			now = request.CreatedAt
			renewed, err := reviews.Validate(context.Background(), c, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			renewedMessage := ProtocolReviewMessage(renewed, "host", reference)
			signature.Signature = ed25519.Sign(private, renewedMessage[:])
			if err := reviews.Register(renewed, signature); err != nil {
				t.Fatal(err)
			}
			beforeRevoke, _ := Inspect(context.Background(), c, reviews)
			if beforeRevoke.Qualification.Evidence[0].State != execution.QualificationVerified {
				t.Fatal("renewed review not consumed")
			}
			reviews.RevokeReviewer("host")
			revoked, _ := Inspect(context.Background(), c, reviews)
			if revoked.Qualification.Evidence[0].State != execution.QualificationPending {
				t.Fatal("revoked review retained")
			}
		})
	}
}

func TestInspectionRejectsMalformedMissingAndCanceledInputs(t *testing.T) {
	c := fixture(t, "0.23.1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := Inspect(ctx, c, nil); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	bad := c
	bad.Executable = filepath.Join(c.SourceRoot, "missing")
	if _, e := Inspect(context.Background(), bad, nil); !errors.Is(e, ErrInstallation) {
		t.Fatal(e)
	}
	if err := os.WriteFile(filepath.Join(c.SourceRoot, "install-stamp.json"), []byte(`{"displayVersion":"invalid/private"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, e := Inspect(context.Background(), c, nil); !errors.Is(e, ErrInstallation) {
		t.Fatal(e)
	}
}

func TestReviewedSourceAndConfigurationIdentityDrift(t *testing.T) {
	for _, drift := range []string{"entry", "native", "interpreter", "auth_metadata", "stamp"} {
		t.Run(drift, func(t *testing.T) {
			c := fixture(t, "0.23.1")
			public, private, _ := ed25519.GenerateKey(rand.Reader)
			reviews, _ := NewProtocolReviews(map[string]ed25519.PublicKey{"host": public})
			request, err := reviews.Validate(context.Background(), c, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			reference := "https://example.invalid/immutable/source"
			message := ProtocolReviewMessage(request, "host", reference)
			if err := reviews.Register(request, ProtocolReviewSignature{Reviewer: "host", SourceReference: reference, Signature: ed25519.Sign(private, message[:])}); err != nil {
				t.Fatal(err)
			}
			path, data := c.Executable, []byte("#!/bin/sh\nexit 99\n")
			switch drift {
			case "native":
				path = c.NativeLauncher
			case "interpreter":
				path = c.Interpreter
			case "auth_metadata":
				path, data = filepath.Join(c.AuthHome, "config.yaml"), []byte("# opaque contents not read\n")
			case "stamp":
				path, data = filepath.Join(c.SourceRoot, "install-stamp.json"), []byte(`{"displayVersion":"0.99.0"}`)
			}
			if err := os.WriteFile(path, data, 0700); err != nil {
				t.Fatal(err)
			}
			r, err := Inspect(context.Background(), c, reviews)
			if err != nil || r.Qualification.Evidence[0].State != execution.QualificationPending {
				t.Fatal("identity retained approval", r, err)
			}
		})
	}
}
