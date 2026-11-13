package localweb

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/agent/opencoderun"
)

func TestOpenCodeRegistrationPropagatesNativeRefusal(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "opencode-fixture")
	script := "#!/bin/sh\ncase \"$1\" in --version) echo 1.19.5;; run) test \"$2\" = --help || exit 99; echo '--pure --format --model';; auth) echo '0 credentials';; *) exit 99;; esac\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	registration, err := NewOpenCodeRuntimeProvider(context.Background(), opencoderun.Config{Executable: executable, Home: root, Model: "deepseek/fixture"}, filepath.Join(root, "runtime"))
	var qualification *opencoderun.QualificationError
	if !errors.Is(err, opencoderun.ErrNativeConfig) || !errors.Is(err, opencoderun.ErrNativeRetry) || !errors.As(err, &qualification) || registration.Provider != nil || len(registration.Supervisors) != 0 {
		t.Fatal(registration, err)
	}
	if _, err := os.Stat(filepath.Join(root, "runtime")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("supervisor created before qualification", err)
	}
}

func TestOpenCodeHostReviewDoesNotCreateNativeRoute(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "opencode-fixture")
	script := "#!/bin/sh\ncase \"$1\" in --version) echo 1.20.0;; run) echo '--pure --format --model';; *) exit 99;; esac\n"
	if err := os.WriteFile(executable, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	reviews, err := opencoderun.NewProtocolReviews(map[string]ed25519.PublicKey{"reviewer": public})
	if err != nil {
		t.Fatal(err)
	}
	config := opencoderun.Config{Executable: executable, Home: root, Model: "deepseek/fixture"}
	request, err := reviews.Validate(context.Background(), config, true, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	attestation := opencoderun.ProtocolAttestation{SourceReference: "https://example.invalid/source/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", SourceCommit: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", EvidenceDigest: sha256.Sum256([]byte("synthetic evidence"))}
	message := opencoderun.ProtocolReviewMessage(request, "reviewer", attestation)
	if err := reviews.Register(request, opencoderun.ProtocolReviewSignature{Reviewer: "reviewer", Attestation: attestation, Signature: ed25519.Sign(private, message[:])}); err != nil {
		t.Fatal(err)
	}
	registration, err := NewOpenCodeRuntimeProviderWithProtocolReviews(context.Background(), config, filepath.Join(root, "runtime"), reviews)
	if !errors.Is(err, opencoderun.ErrNativeConfig) || !errors.Is(err, opencoderun.ErrNativeRetry) || !errors.Is(err, opencoderun.ErrAuthScope) || registration.Provider != nil {
		t.Fatal(registration, err)
	}
	if _, err := os.Stat(filepath.Join(root, "runtime")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("route created before native qualification", err)
	}
}
