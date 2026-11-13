package codexexec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/codexdiscovery"
	"github.com/Yangyang96/chora/internal/execution"
)

const fixtureSource = "https://example.invalid/public-conformance-fixture"

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
	// Constructor must copy keys, not borrow a mutable caller-owned slice.
	public[0] ^= 1
	return r, private
}

func signReview(request ProtocolReviewRequest, key ed25519.PrivateKey) ProtocolReviewSignature {
	message := ProtocolReviewMessage(request, "reviewer", fixtureSource)
	return ProtocolReviewSignature{Reviewer: "reviewer", SourceReference: fixtureSource, Signature: ed25519.Sign(key, message[:])}
}

func TestMetadataCleanupFailureBlocksPublicQualification(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("permission failure requires an unprivileged process")
	}
	for _, command := range []string{"--version", "exec --help"} {
		t.Run(command, func(t *testing.T) {
			c := versionFixture(t, "codex-cli 0.167.2", "exit 88\n").config
			observed := filepath.Join(filepath.Dir(c.Home), "observed")
			raw, err := os.ReadFile(c.Executable)
			if err != nil {
				t.Fatal(err)
			}
			bootstrap := "if [ \"$*\" = '" + command + "' ]; then\nmkdir \"$HOME/locked\"\necho fixture > \"$HOME/locked/retained\"\nchmod 000 \"$HOME/locked\"\nprintf '%s\\n' \"$HOME\" >> '" + observed + "'\nfi\n"
			if err := os.WriteFile(c.Executable, []byte(strings.Replace(string(raw), "#!/bin/sh\n", "#!/bin/sh\n"+bootstrap, 1)), 0700); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				raw, err := os.ReadFile(observed)
				if err != nil {
					t.Error(err)
					return
				}
				for _, metadata := range strings.Fields(string(raw)) {
					if err := os.Chmod(filepath.Join(metadata, "locked"), 0700); err != nil {
						t.Error(err)
					}
					if err := os.RemoveAll(metadata); err != nil {
						t.Error(err)
					}
				}
			})
			reviews, _ := reviewService(t)
			for _, construct := range []func(context.Context, Config) (*Adapter, error){New, func(ctx context.Context, c Config) (*Adapter, error) {
				return NewReviewed(ctx, c, reviews)
			}} {
				a, err := construct(context.Background(), c)
				if a != nil || !errors.Is(err, ErrVersion) || !errors.Is(err, codexdiscovery.ErrCleanup) || errors.Is(err, ErrManagedPolicy) {
					t.Fatal("cleanup uncertainty was consumed", a, err)
				}
			}
			for _, readiness := range []execution.Readiness{Inspect(context.Background(), c), InspectReviewed(context.Background(), c, reviews)} {
				if readiness.Ready || !strings.Contains(readiness.Failure, codexdiscovery.ErrCleanup.Error()) {
					t.Fatal("cleanup uncertainty lost from readiness", readiness)
				}
				if command == "exec --help" && (readiness.Qualification == nil || evidenceFor(t, readiness.Qualification, "launch_arguments").State != execution.QualificationPending) {
					t.Fatal("unclean help qualified launch arguments", readiness)
				}
			}
			request, err := reviews.Validate(context.Background(), c, true, time.Hour)
			if !errors.Is(err, codexdiscovery.ErrCleanup) || request != (ProtocolReviewRequest{}) {
				t.Fatal("unclean metadata created a review challenge", request, err)
			}
			for _, scope := range []string{c.Home, c.AuthHome} {
				entries, err := os.ReadDir(scope)
				if err != nil || len(entries) != 0 {
					t.Fatal("selected scope changed", entries, err)
				}
			}
		})
	}
}

func TestUnknownVersionTrustedProtocolTransition(t *testing.T) {
	c := versionFixture(t, "codex-cli 0.167.2", "exit 88\n").config
	raw, _ := os.ReadFile(c.Executable)
	if err := os.WriteFile(c.Executable, []byte(strings.ReplaceAll(string(raw), "0.167.2", "0.168.0")), 0700); err != nil {
		t.Fatal(err)
	}
	r, key := reviewService(t)
	before := InspectReviewed(context.Background(), c, r)
	if before.Ready || before.Qualification == nil || before.Qualification.State != execution.QualificationPending || evidenceFor(t, before.Qualification, "event_frames").State != execution.QualificationPending {
		t.Fatal(before)
	}
	request, err := r.Validate(context.Background(), c, true, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if before := InspectReviewed(context.Background(), c, r); before.Qualification.State != execution.QualificationPending {
		t.Fatal("proposal granted authority", before)
	}
	if err := r.Register(request, signReview(request, key)); err != nil {
		t.Fatal(err)
	}
	ready := InspectReviewed(context.Background(), c, r)
	if ready.Ready || ready.Failure != ErrManagedPolicy.Error() || ready.Qualification.State != execution.QualificationRejected {
		t.Fatal(ready)
	}
	for _, name := range []string{"event_frames", "tool_lifecycle", "terminal_result"} {
		e := evidenceFor(t, ready.Qualification, name)
		if e.State != execution.QualificationVerified || e.Method != "signed_fixture_conformance:not_native" {
			t.Fatal(e)
		}
	}
	if e := evidenceFor(t, ready.Qualification, "configuration_sources"); e.Reference != "managed_policy_unverifiable:same_config_consumption_required" {
		t.Fatal(e)
	}
	if err := r.Register(request, signReview(request, key)); !errors.Is(err, ErrProtocolReview) {
		t.Fatal("replayed challenge", err)
	}
	// Fixture approval must remain incapable of producing a launchable provider,
	// even with the package-private policy injection used by historical tests.
	if a, err := newReviewedAdapter(context.Background(), c, func() error { return nil }, r); a != nil || !errors.Is(err, ErrProtocolReview) {
		t.Fatal("fixture became launch authority", a, err)
	}
	if fresh := Inspect(context.Background(), c); fresh.Qualification.State != execution.QualificationPending {
		t.Fatal("ordinary construction imported approval", fresh)
	}
	t.Log("0.168.0 shell fixture: pending -> signed protocol evidence verified -> independent managed policy rejected; no native/model qualification")
}

func TestProtocolReviewRejectsForgedModifiedExpiredAndReplay(t *testing.T) {
	for _, mode := range []string{"unsigned", "wrong_key", "untrusted_reviewer", "identity", "corpus", "nonce", "fixture_scope", "expiry", "expired", "future", "cross_service", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t, "exit 0\n").config
			r, key := reviewService(t)
			request, err := r.Validate(context.Background(), c, true, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			signature := signReview(request, key)
			switch mode {
			case "unsigned":
				signature.Signature = nil
			case "wrong_key":
				_, other, _ := ed25519.GenerateKey(rand.Reader)
				signature = signReview(request, other)
			case "untrusted_reviewer":
				signature.Reviewer = "intruder"
			case "identity":
				request.Identity.SourceIdentity[0] ^= 1
			case "corpus":
				request.Corpus[0] ^= 1
			case "nonce":
				request.Nonce[0] ^= 1
			case "fixture_scope":
				request.Fixture = false
				signature = signReview(request, key)
			case "expiry":
				request.ExpiresAt = request.ExpiresAt.Add(time.Hour)
				signature = signReview(request, key)
			case "expired":
				r.now = func() time.Time { return request.ExpiresAt }
			case "future":
				r.now = func() time.Time { return request.CreatedAt.Add(-time.Second) }
			case "cross_service":
				r, _ = NewProtocolReviews(map[string]ed25519.PublicKey{"reviewer": key.Public().(ed25519.PublicKey)})
			case "revoked":
				r.RevokeReviewer("reviewer")
			}
			if err := r.Register(request, signature); !errors.Is(err, ErrProtocolReview) {
				t.Fatal("bad review registered", err)
			}
		})
	}
}

func TestProtocolReviewConsumptionIdentityAndValidity(t *testing.T) {
	for _, mode := range []string{"replacement", "configuration", "security", "protocol", "expired", "revoked", "concurrent_registration"} {
		t.Run(mode, func(t *testing.T) {
			c := fixture(t, "exit 0\n").config
			r, key := reviewService(t)
			request, err := r.Validate(context.Background(), c, true, time.Hour)
			if err != nil {
				t.Fatal(err)
			}
			signature := signReview(request, key)
			if mode == "concurrent_registration" {
				var wg sync.WaitGroup
				results := make(chan error, 8)
				for i := 0; i < 8; i++ {
					wg.Add(1)
					go func() { defer wg.Done(); results <- r.Register(request, signature) }()
				}
				wg.Wait()
				close(results)
				success := 0
				for err := range results {
					if err == nil {
						success++
					} else if !errors.Is(err, ErrProtocolReview) {
						t.Fatal(err)
					}
				}
				if success != 1 {
					t.Fatal("challenge consumed more than once", success)
				}
				return
			}
			if err := r.Register(request, signature); err != nil {
				t.Fatal(err)
			}
			identity := request.Identity
			switch mode {
			case "replacement":
				raw, _ := os.ReadFile(c.Executable)
				os.WriteFile(c.Executable, append(raw, []byte("# changed bytes\n")...), 0700)
				a, err := metadataAdapter(context.Background(), c)
				if err != nil {
					t.Fatal(err)
				}
				identity = a.qualificationIdentity()
			case "configuration":
				c.Model = "another-model"
				a, err := metadataAdapter(context.Background(), c)
				if err != nil {
					t.Fatal(err)
				}
				identity = a.qualificationIdentity()
			case "security":
				identity.SecurityIdentity[0] ^= 1
			case "protocol":
				identity.ProtocolVersion = "codex.exec.jsonl.future"
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

func TestProtocolReviewValidationRejectsUnsafeAndCancelled(t *testing.T) {
	c := fixture(t, "exit 0\n").config
	r, _ := reviewService(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Validate(ctx, c, true, time.Hour); err == nil {
		t.Fatal("cancelled validation accepted")
	}
	for _, duration := range []time.Duration{0, -1, 25 * time.Hour} {
		if _, err := r.Validate(context.Background(), c, true, duration); !errors.Is(err, ErrProtocolReview) {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(c.AuthHome+"/.env", []byte("PUBLIC_TEST=1"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Validate(context.Background(), c, true, time.Hour); !errors.Is(err, ErrPolicy) {
		t.Fatal("dotenv reached validator", err)
	}
}
