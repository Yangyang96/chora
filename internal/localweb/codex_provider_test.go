package localweb

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"github.com/Yangyang96/chora/internal/execution"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/agent/codexexec"
)

func TestCodexCompositionRejectsUnverifiableManagedPolicy(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	for _, name := range []string{"home", "auth"} {
		if err := os.Mkdir(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	executable := filepath.Join(root, "codex-fixture")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\ncase \"$1\" in --version) echo 'codex-cli 0.155.1';; exec) echo '--json --ephemeral --ignore-user-config --ignore-rules --skip-git-repo-check --sandbox --color --config --model';; login) echo 'Logged in using ChatGPT';; *) exit 1;; esac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	registration, err := NewCodexRuntimeProvider(context.Background(), codexexec.Config{Executable: executable, AuthHome: filepath.Join(root, "auth"), Home: filepath.Join(root, "home")}, filepath.Join(root, "runtime"))
	if !errors.Is(err, codexexec.ErrManagedPolicy) || registration.Provider != nil || len(registration.Supervisors) != 0 {
		t.Fatalf("unqualified registration: %+v, %v", registration, err)
	}
}

func TestReviewedCodexCompositionStillRejectsPolicy(t *testing.T) {
	root, _ := filepath.EvalSymlinks(t.TempDir())
	home, auth, binary := filepath.Join(root, "home"), filepath.Join(root, "auth"), filepath.Join(root, "fixture")
	for _, p := range []string{home, auth} {
		if err := os.Mkdir(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(binary, []byte("#!/bin/sh\ncase \"$*\" in\n--version) echo codex-cli 0.168.0;;\n'exec --help') echo '--json --ephemeral --ignore-user-config --ignore-rules --skip-git-repo-check --sandbox --color --config --model';;\n*) exit 88;;\nesac\n"), 0700); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	reviews, err := codexexec.NewProtocolReviews(map[string]ed25519.PublicKey{"reviewer": public})
	if err != nil {
		t.Fatal(err)
	}
	c := codexexec.Config{Executable: binary, Home: home, AuthHome: auth}
	request, err := reviews.Validate(context.Background(), c, true, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	reference := "https://example.invalid/fixture"
	message := codexexec.ProtocolReviewMessage(request, "reviewer", reference)
	if err := reviews.Register(request, codexexec.ProtocolReviewSignature{Reviewer: "reviewer", SourceReference: reference, Signature: ed25519.Sign(private, message[:])}); err != nil {
		t.Fatal(err)
	}
	registration, err := NewCodexRuntimeProviderWithProtocolReviews(context.Background(), c, filepath.Join(root, "runtime"), reviews)
	var qualification *codexexec.QualificationError
	if !errors.Is(err, codexexec.ErrManagedPolicy) || !errors.As(err, &qualification) || registration.Provider != nil || len(registration.Supervisors) != 0 {
		t.Fatal(registration, err)
	}
	for _, e := range qualification.Report.Evidence {
		if e.Capability == "event_frames" && e.State != execution.QualificationVerified {
			t.Fatal(e)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "runtime")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("refused registration created a supervisor", err)
	}
}
