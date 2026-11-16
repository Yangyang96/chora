// chora-opencode-review runs an operator-owned protocol review session.
package main

import (
	"context"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Yangyang96/chora/internal/agent/opencoderun"
	"github.com/Yangyang96/chora/internal/execution"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("chora-opencode-review", flag.ContinueOnError)
	var c opencoderun.Config
	flags.StringVar(&c.Executable, "executable", "", "selected absolute executable")
	flags.StringVar(&c.Home, "home", "", "selected existing native home, metadata never enters it")
	flags.StringVar(&c.Model, "model", "", "explicit provider/model selection")
	flags.StringVar(&c.DataHome, "data-home", "", "selected existing XDG data directory")
	flags.StringVar(&c.ConfigHome, "config-home", "", "selected existing XDG config directory")
	flags.StringVar(&c.CacheHome, "cache-home", "", "selected existing XDG cache directory")
	flags.StringVar(&c.StateHome, "state-home", "", "selected existing XDG state directory")
	keyPath := flags.String("reviewer-key", "", "host-selected Ed25519 public key in PKIX PEM format")
	reviewer := flags.String("reviewer", "", "trusted reviewer identifier")
	var attestation opencoderun.ProtocolAttestation
	flags.StringVar(&attestation.SourceReference, "source-reference", "", "immutable public source reference")
	flags.StringVar(&attestation.SourceCommit, "source-commit", "", "reviewed immutable source commit")
	evidence := flags.String("evidence-sha256", "", "digest of the trusted reviewer's evidence package")
	native := flags.Bool("native-evidence-reviewed", false, "attest binary/source mapping and native tool/terminal/exit/auth/full-turn semantics")
	fixture := flags.Bool("fixture", false, "restrict all resulting evidence to adapter fixtures")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return opencoderun.ErrProtocolReview
	}
	raw, err := os.ReadFile(*keyPath)
	if err != nil {
		return errors.New("reviewer_public_key_unavailable")
	}
	block, _ := pem.Decode(raw)
	if block == nil {
		return errors.New("reviewer_public_key_invalid")
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return errors.New("reviewer_public_key_invalid")
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		return errors.New("reviewer_public_key_invalid")
	}
	digest, err := hex.DecodeString(*evidence)
	if err != nil || len(digest) != 32 {
		return opencoderun.ErrProtocolReview
	}
	copy(attestation.EvidenceDigest[:], digest)
	reviews, err := opencoderun.NewProtocolReviews(map[string]ed25519.PublicKey{*reviewer: key})
	if err != nil {
		return err
	}
	before := opencoderun.InspectReviewed(context.Background(), c, reviews)
	request, err := reviews.Validate(context.Background(), c, *fixture, 15*time.Minute)
	if err != nil {
		return err
	}
	if request.Fixture && *native {
		return opencoderun.ErrProtocolReview
	}
	attestation.NativeSourceMapping, attestation.NativeToolTerminalExit = *native, *native
	attestation.NativeAuthenticationSemantics, attestation.NativeFullTurnSemantics = *native, *native
	message := opencoderun.ProtocolReviewMessage(request, *reviewer, attestation)
	if err := json.NewEncoder(output).Encode(struct {
		Request     opencoderun.ProtocolReviewRequest
		Attestation opencoderun.ProtocolAttestation
		MessageHex  string
		Before      execution.Readiness
	}{request, attestation, hex.EncodeToString(message[:]), before}); err != nil {
		return err
	}
	var signed struct{ SignatureHex string }
	if err := json.NewDecoder(io.LimitReader(input, 4096)).Decode(&signed); err != nil {
		return errors.New("review_signature_required")
	}
	signature, err := hex.DecodeString(signed.SignatureHex)
	if err != nil {
		return opencoderun.ErrProtocolReview
	}
	if err := reviews.Register(request, opencoderun.ProtocolReviewSignature{Reviewer: *reviewer, Attestation: attestation, Signature: signature}); err != nil {
		return err
	}
	_, err = opencoderun.NewReviewed(context.Background(), c, reviews)
	var qualification *opencoderun.QualificationError
	if !errors.As(err, &qualification) {
		return fmt.Errorf("protocol_review_did_not_produce_diagnostic: %w", err)
	}
	return json.NewEncoder(output).Encode(struct {
		Ready         bool
		Failure       string
		Qualification execution.QualificationReport
	}{false, err.Error(), qualification.Report})
}
