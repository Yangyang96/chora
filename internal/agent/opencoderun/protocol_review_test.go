package opencoderun

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

const fixtureCommit = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const fixtureSource = "https://example.invalid/opencode/commit/" + fixtureCommit

func reviewService(t *testing.T) (*ProtocolReviews, ed25519.PrivateKey) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	r, err := NewProtocolReviews(map[string]ed25519.PublicKey{"reviewer": public})
	if err != nil {
		t.Fatal(err)
	}
	public[0] ^= 1
	return r, private
}

func signReview(request ProtocolReviewRequest, key ed25519.PrivateKey) ProtocolReviewSignature {
	a := ProtocolAttestation{SourceReference: fixtureSource, SourceCommit: fixtureCommit, EvidenceDigest: sha256.Sum256([]byte("synthetic review evidence"))}
	if !request.Fixture {
		a.NativeSourceMapping, a.NativeToolTerminalExit, a.NativeAuthenticationSemantics, a.NativeFullTurnSemantics = true, true, true, true
	}
	message := ProtocolReviewMessage(request, "reviewer", a)
	return ProtocolReviewSignature{Reviewer: "reviewer", Attestation: a, Signature: ed25519.Sign(key, message[:])}
}

func unknownConfig(t *testing.T) Config {
	t.Helper()
	c := fixture(t, "exit 99\n").config
	raw, err := os.ReadFile(c.Executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(c.Executable, []byte(strings.ReplaceAll(string(raw), "1.19.5", "1.20.0")), 0700); err != nil {
		t.Fatal(err)
	}
	return c
}

func semanticEvidence(t *testing.T, r execution.Readiness) execution.CapabilityEvidence {
	t.Helper()
	if r.Qualification != nil {
		for _, e := range r.Qualification.Evidence {
			if e.Capability == "event_frames" {
				return e
			}
		}
	}
	t.Fatal("missing event evidence", r)
	return execution.CapabilityEvidence{}
}

func TestUnknownVersionTrustedProtocolTransition(t *testing.T) {
	c := unknownConfig(t)
	r, key := reviewService(t)
	before := InspectReviewed(context.Background(), c, r)
	if before.Ready || semanticEvidence(t, before).State != execution.QualificationPending {
		t.Fatal(before)
	}
	request, err := r.Validate(context.Background(), c, false, time.Hour)
	if err != nil || !request.Fixture {
		t.Fatal("script labelled native", request, err)
	}
	if e := semanticEvidence(t, InspectReviewed(context.Background(), c, r)); e.State != execution.QualificationPending {
		t.Fatal("proposal granted authority", e)
	}
	if err := r.Register(request, signReview(request, key)); err != nil {
		t.Fatal(err)
	}
	after := InspectReviewed(context.Background(), c, r)
	if after.Ready || after.Qualification.State != execution.QualificationRejected || semanticEvidence(t, after).Method != "signed_fixture_conformance:adapter_only" {
		t.Fatal(after)
	}
	_, err = NewReviewed(context.Background(), c, r)
	if !errors.Is(err, ErrNativeConfig) || !errors.Is(err, ErrNativeRetry) || !errors.Is(err, ErrAuthScope) {
		t.Fatal("native gates cleared", err)
	}
	if a, err := newReviewedAdapter(context.Background(), c, func(context.Context) error { return nil }, r); a != nil || !errors.Is(err, ErrProtocolReview) {
		t.Fatal("fixture launched", a, err)
	}
	if e := semanticEvidence(t, Inspect(context.Background(), c)); e.State != execution.QualificationPending {
		t.Fatal("ordinary entry imported approval", e)
	}
	if err := r.Register(request, signReview(request, key)); !errors.Is(err, ErrProtocolReview) {
		t.Fatal("challenge replay accepted", err)
	}
}

func TestProtocolReviewRejectsTamperingAndUntrustedEvidence(t *testing.T) {
	c := unknownConfig(t)
	for _, mode := range []string{"unsigned", "wrong_key", "reviewer", "identity", "runtime", "protocol", "configuration", "security", "nonce", "corpus", "fixture", "expiry", "expired", "future", "cross_service", "revoked", "reference", "commit", "evidence", "fixture_native_claim", "url_only"} {
		t.Run(mode, func(t *testing.T) {
			r, key := reviewService(t)
			request, err := r.Validate(context.Background(), c, true, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			signed := signReview(request, key)
			switch mode {
			case "unsigned":
				signed.Signature = nil
			case "wrong_key":
				_, other, _ := ed25519.GenerateKey(rand.Reader)
				signed = signReview(request, other)
			case "reviewer":
				signed.Reviewer = "intruder"
			case "identity":
				request.Identity.SourceIdentity[0] ^= 1
			case "runtime":
				request.Identity.RuntimeID = "codex-exec"
			case "protocol":
				request.Identity.ProtocolVersion = "future"
			case "configuration":
				request.Identity.ConfigurationIdentity[0] ^= 1
			case "security":
				request.Identity.SecurityIdentity[0] ^= 1
			case "nonce":
				request.Nonce[0] ^= 1
			case "corpus":
				request.Corpus[0] ^= 1
			case "fixture":
				request.Fixture = false
				signed = signReview(request, key)
			case "expiry":
				request.ExpiresAt = request.ExpiresAt.Add(time.Hour)
				signed = signReview(request, key)
			case "expired":
				r.now = func() time.Time { return request.ExpiresAt }
			case "future":
				r.now = func() time.Time { return request.CreatedAt.Add(-time.Second) }
			case "cross_service":
				r, _ = NewProtocolReviews(map[string]ed25519.PublicKey{"reviewer": key.Public().(ed25519.PublicKey)})
			case "revoked":
				r.RevokeReviewer("reviewer")
			case "reference":
				signed.Attestation.SourceReference += "/changed"
			case "commit":
				signed.Attestation.SourceCommit = strings.Repeat("b", 40)
			case "evidence":
				signed.Attestation.EvidenceDigest[0] ^= 1
			case "fixture_native_claim":
				signed.Attestation.NativeSourceMapping = true
				message := ProtocolReviewMessage(request, "reviewer", signed.Attestation)
				signed.Signature = ed25519.Sign(key, message[:])
			case "url_only":
				signed.Attestation.EvidenceDigest = [32]byte{}
				message := ProtocolReviewMessage(request, "reviewer", signed.Attestation)
				signed.Signature = ed25519.Sign(key, message[:])
			}
			if err := r.Register(request, signed); !errors.Is(err, ErrProtocolReview) {
				t.Fatal("untrusted review accepted", err)
			}
		})
	}
}

func TestProtocolReviewConsumptionDriftAndConcurrentReplay(t *testing.T) {
	for _, mode := range []string{"bytes", "symlink", "configuration", "protocol", "security", "corpus", "expired", "revoked", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			c := unknownConfig(t)
			r, key := reviewService(t)
			request, err := r.Validate(context.Background(), c, true, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			signed := signReview(request, key)
			if mode == "concurrent" {
				var wg sync.WaitGroup
				results := make(chan error, 8)
				for i := 0; i < 8; i++ {
					wg.Add(1)
					go func() { defer wg.Done(); results <- r.Register(request, signed) }()
				}
				wg.Wait()
				close(results)
				accepted := 0
				for err := range results {
					if err == nil {
						accepted++
					} else if !errors.Is(err, ErrProtocolReview) {
						t.Fatal(err)
					}
				}
				if accepted != 1 {
					t.Fatal("nonce consumed multiple times", accepted)
				}
				return
			}
			if err := r.Register(request, signed); err != nil {
				t.Fatal(err)
			}
			identity := request.Identity
			switch mode {
			case "bytes":
				raw, _ := os.ReadFile(c.Executable)
				if err := os.WriteFile(c.Executable, append(raw, []byte("# changed\n")...), 0700); err != nil {
					t.Fatal(err)
				}
				a, err := metadataAdapter(context.Background(), c)
				if err != nil {
					t.Fatal(err)
				}
				identity = a.qualificationIdentity()
			case "symlink":
				alias := filepath.Join(filepath.Dir(c.Executable), "alias")
				if err := os.Symlink(c.Executable, alias); err != nil {
					t.Fatal(err)
				}
				c.Executable = alias
				a, err := metadataAdapter(context.Background(), c)
				if err != nil {
					t.Fatal(err)
				}
				identity = a.qualificationIdentity()
			case "configuration":
				c.Model = "deepseek/another"
				a, err := metadataAdapter(context.Background(), c)
				if err != nil {
					t.Fatal(err)
				}
				identity = a.qualificationIdentity()
			case "protocol":
				identity.ProtocolVersion = "opencode.run.future"
			case "security":
				identity.SecurityIdentity[0] ^= 1
			case "corpus":
				approval := r.approved[identity.Digest()]
				approval.request.Corpus[0] ^= 1
				r.approved[identity.Digest()] = approval
			case "expired":
				r.now = func() time.Time { return request.ExpiresAt }
			case "revoked":
				r.RevokeReviewer("reviewer")
			}
			if _, ok := r.lookup(identity); ok {
				t.Fatal("stale approval consumed")
			}
		})
	}
}

// This test injects a synthetic native attestation into package-private state to
// exercise binding freshness. It supplies no real native qualification evidence.
func TestSyntheticReviewBindingAndSpawnFreshness(t *testing.T) {
	for _, mode := range []string{"expired", "revoked", "corpus"} {
		t.Run(mode, func(t *testing.T) {
			a := fixture(t, "exit 0\n")
			r, key := reviewService(t)
			challenge, err := r.Validate(context.Background(), a.config, true, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			challenge.Fixture = false
			r.pending[challenge.Nonce] = challenge
			if err := r.Register(challenge, signReview(challenge, key)); err != nil {
				t.Fatal(err)
			}
			a.protocolReviews = r
			if err := a.qualify(context.Background()); err != nil {
				t.Fatal(err)
			}
			a.fingerprint, err = a.Fingerprint(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			req := request(t, a)
			prepared, err := a.PrepareBoundStart(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			root, _ := filepath.EvalSymlinks(t.TempDir())
			s, err := trustedhost.New(a.SupervisorConfig(filepath.Join(root, "runtime")))
			if err != nil {
				t.Fatal(err)
			}
			if mode == "expired" {
				r.now = func() time.Time { return challenge.ExpiresAt }
			} else if mode == "corpus" {
				approval := r.approved[challenge.Identity.Digest()]
				approval.request.Corpus[0] ^= 1
				r.approved[challenge.Identity.Digest()] = approval
			} else {
				r.RevokeReviewer("reviewer")
			}
			if _, err := a.Binding(context.Background()); !errors.Is(err, ErrIdentity) {
				t.Fatal("stale binding accepted", err)
			}
			if outcome := s.Start(context.Background(), prepared.Invocation, &sink{token: req.LaunchToken, done: make(chan struct{})}); outcome.Kind != execution.StartProvenNoChild {
				t.Fatal("stale review launched", outcome)
			}
		})
	}
}

func TestNativeAttestationRequiresAllReviewDimensions(t *testing.T) {
	request := ProtocolReviewRequest{}
	complete := ProtocolAttestation{SourceReference: fixtureSource, SourceCommit: fixtureCommit, EvidenceDigest: sha256.Sum256([]byte("synthetic evidence")), NativeSourceMapping: true, NativeToolTerminalExit: true, NativeAuthenticationSemantics: true, NativeFullTurnSemantics: true}
	if !validAttestation(request, complete) {
		t.Fatal("complete synthetic trust input rejected")
	}
	for _, mode := range []string{"source_mapping", "tools_terminal_exit", "authentication", "full_turn", "evidence", "commit", "mutable_url"} {
		a := complete
		switch mode {
		case "source_mapping":
			a.NativeSourceMapping = false
		case "tools_terminal_exit":
			a.NativeToolTerminalExit = false
		case "authentication":
			a.NativeAuthenticationSemantics = false
		case "full_turn":
			a.NativeFullTurnSemantics = false
		case "evidence":
			a.EvidenceDigest = [32]byte{}
		case "commit":
			a.SourceCommit = "main"
		case "mutable_url":
			a.SourceReference = "https://example.invalid/opencode/main"
		}
		if validAttestation(request, a) {
			t.Fatal("partial native attestation accepted", mode)
		}
	}
}

func TestProtocolReviewRejectsEmptyServicesAndCancelledValidation(t *testing.T) {
	var imported ProtocolReviews
	raw, err := json.Marshal(execution.QualificationReport{State: execution.QualificationVerified})
	if err != nil || json.Unmarshal(raw, &imported) != nil {
		t.Fatal(err)
	}
	if err := imported.Register(ProtocolReviewRequest{}, ProtocolReviewSignature{}); !errors.Is(err, ErrProtocolReview) {
		t.Fatal("diagnostic JSON provisioned trust", err)
	}
	var empty ProtocolReviews
	if _, err := empty.Validate(context.Background(), Config{}, true, time.Hour); !errors.Is(err, ErrProtocolReview) {
		t.Fatal(err)
	}
	if err := empty.Register(ProtocolReviewRequest{}, ProtocolReviewSignature{}); !errors.Is(err, ErrProtocolReview) {
		t.Fatal(err)
	}
	if _, ok := empty.lookup(execution.QualificationIdentity{}); ok {
		t.Fatal("empty service granted proof")
	}
	c := unknownConfig(t)
	r, _ := reviewService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Validate(ctx, c, true, time.Hour); err == nil {
		t.Fatal("cancelled validation accepted")
	}
	for _, duration := range []time.Duration{0, -time.Second, 25 * time.Hour} {
		if _, err := r.Validate(context.Background(), c, true, duration); !errors.Is(err, ErrProtocolReview) {
			t.Fatal(err)
		}
	}
	raw, _ = os.ReadFile(c.Executable)
	if err := os.WriteFile(c.Executable, []byte(strings.ReplaceAll(string(raw), "--pure --format --model", "--format --model")), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Validate(context.Background(), c, true, time.Hour); !errors.Is(err, ErrVersion) {
		t.Fatal("missing flag admitted", err)
	}
	if len(r.pending) != 0 {
		t.Fatal("failed validation minted a challenge")
	}
}
