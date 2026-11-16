// chora-hermes-review runs an operator-owned protocol review session. It never
// executes a subprocess/model task, reads credentials or grants native startup safety.
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

	"github.com/Yangyang96/chora/internal/agent/hermes"
)

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, input io.Reader, output io.Writer) error {
	flags := flag.NewFlagSet("chora-hermes-review", flag.ContinueOnError)
	var c hermes.Config
	flags.StringVar(&c.Executable, "executable", "", "absolute selected executable")
	flags.StringVar(&c.NativeLauncher, "native-launcher", "", "selected native launcher")
	flags.StringVar(&c.Interpreter, "interpreter", "", "selected Python executable")
	flags.StringVar(&c.SourceRoot, "source-root", "", "selected installed source directory")
	flags.StringVar(&c.AuthHome, "auth-home", "", "selected existing native home (metadata will not enter it)")

	keyPath := flags.String("reviewer-key", "", "operator-selected Ed25519 public key in PKIX PEM format")
	reviewer := flags.String("reviewer", "", "trusted reviewer identifier")
	reference := flags.String("source-reference", "", "immutable public source reviewed for this exact executable")

	if err := flags.Parse(args); err != nil {
		return err
	}
	before, err := hermes.Inspect(context.Background(), c, nil)
	if err != nil {
		return err
	}
	if *keyPath == "" {
		return json.NewEncoder(output).Encode(before)
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
	reviews, err := hermes.NewProtocolReviews(map[string]ed25519.PublicKey{*reviewer: key})
	if err != nil {
		return err
	}
	request, err := reviews.Validate(context.Background(), c, 15*time.Minute)
	if err != nil {
		return err
	}
	message := hermes.ProtocolReviewMessage(request, *reviewer, *reference)
	// The reviewer inspects request, corpus/source and identity, then signs the
	// 32 message bytes. No private signing key is loaded by this process.
	if err := json.NewEncoder(output).Encode(struct {
		Request                   hermes.ProtocolReviewRequest
		MessageHex                string
		Before                    hermes.Inspection
		Reviewer, SourceReference string
	}{request, hex.EncodeToString(message[:]), before, *reviewer, *reference}); err != nil {
		return err
	}
	var signed struct{ SignatureHex string }
	if err := json.NewDecoder(io.LimitReader(input, 4096)).Decode(&signed); err != nil {
		return errors.New("review_signature_required")
	}
	signature, err := hex.DecodeString(signed.SignatureHex)
	if err != nil {
		return hermes.ErrProtocolReview
	}
	if err := reviews.Register(request, hermes.ProtocolReviewSignature{Reviewer: *reviewer, SourceReference: *reference, Signature: signature}); err != nil {
		return err
	}
	_, err = hermes.NewReviewed(context.Background(), c, reviews)
	var qualification *hermes.QualificationError
	if !errors.As(err, &qualification) {
		return fmt.Errorf("protocol_review_did_not_produce_diagnostic: %w", err)
	}
	return json.NewEncoder(output).Encode(struct {
		Ready         bool
		Failure       string
		Qualification any
	}{false, err.Error(), qualification.Report})
}
