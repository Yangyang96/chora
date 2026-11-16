package codexexec

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/runtimeenv"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

var ErrProtocolReview = errors.New("protocol_review_invalid_or_expired")

// ProtocolReviewRequest is a proposal, never execution authority. Validation
// exercises the adapter's fixed replay corpus, not a model or the native task
// loop. A trusted reviewer must separately establish that this exact executable
// implements those semantics, and sign the request with a public source reference.
type ProtocolReviewRequest struct {
	Identity             execution.QualificationIdentity
	Nonce                [32]byte
	Corpus               [32]byte
	CreatedAt, ExpiresAt time.Time
	Fixture              bool
}

// ProtocolReviewSignature is accepted only for an outstanding request in the
// registry which produced it. Editing a report or submitting arbitrary evidence
// does not create a request. Keys belong to the trusted embedding host/reviewer,
// never task configuration, native CLI configuration or an HTTP request.
type ProtocolReviewSignature struct {
	Reviewer, SourceReference string
	Signature                 []byte
}

func ProtocolReviewMessage(r ProtocolReviewRequest, reviewer, reference string) [32]byte {
	return hash(struct {
		Domain              string
		Request             ProtocolReviewRequest
		Reviewer, Reference string
	}{
		"chora.codex-protocol-review.v1", r, reviewer, reference,
	})
}

type protocolApproval struct {
	request             ProtocolReviewRequest
	reviewer, reference string
	digest              [32]byte
}

// ProtocolReviews is a process-local trust service, separate from the diagnostic
// QualificationCache. Only the host can provision reviewer keys. No default key,
// self-signed file import, environment override or configuration bypass exists.
// Recreating the service loses outstanding/approved requests, including nonces.
type ProtocolReviews struct {
	mu       sync.Mutex
	keys     map[string]ed25519.PublicKey
	pending  map[[32]byte]ProtocolReviewRequest
	approved map[[32]byte]protocolApproval
	now      func() time.Time
}

func NewProtocolReviews(reviewers map[string]ed25519.PublicKey) (*ProtocolReviews, error) {
	r := &ProtocolReviews{keys: map[string]ed25519.PublicKey{}, pending: map[[32]byte]ProtocolReviewRequest{}, approved: map[[32]byte]protocolApproval{}, now: time.Now}
	for id, key := range reviewers {
		if strings.TrimSpace(id) == "" || len(key) != ed25519.PublicKeySize {
			return nil, ErrProtocolReview
		}
		r.keys[id] = append(ed25519.PublicKey(nil), key...)
	}
	return r, nil
}

// Validate performs only disposable metadata probes and fixed in-process replay
// checks. Fixture records stay explicitly marked; they cannot authorize launch.
// It does not read credential contents or execute caller-supplied transcripts.
func (r *ProtocolReviews) Validate(ctx context.Context, config Config, fixture bool, validity time.Duration) (ProtocolReviewRequest, error) {
	if r == nil || r.keys == nil || r.pending == nil || validity <= 0 || validity > 24*time.Hour {
		return ProtocolReviewRequest{}, ErrProtocolReview
	}
	a, err := metadataAdapter(ctx, config)
	if err != nil {
		return ProtocolReviewRequest{}, err
	}
	if _, err := a.protocolEvidence(ctx); err != nil {
		return ProtocolReviewRequest{}, err
	}
	if err := validateProtocolConformance(); err != nil {
		return ProtocolReviewRequest{}, err
	}
	current, err := trustedhost.InspectExecutable(config.Executable)
	if err != nil || current != a.executable || ctx.Err() != nil {
		return ProtocolReviewRequest{}, ErrIdentity
	}
	// Script fixtures cannot be relabelled native by changing a flag. A
	// trusted source reviewer remains responsible for provenance of native
	// binaries; a conformance replay cannot establish that provenance.
	file, err := os.Open(a.executable.ResolvedPath)
	if err != nil {
		return ProtocolReviewRequest{}, ErrIdentity
	}
	var magic [2]byte
	digest := sha256.New()
	_, readErr := io.ReadFull(io.TeeReader(file, digest), magic[:])
	_, hashErr := io.Copy(digest, file)
	file.Close()
	var actual [32]byte
	copy(actual[:], digest.Sum(nil))
	if readErr != nil || hashErr != nil || actual != a.executable.SHA256 {
		return ProtocolReviewRequest{}, ErrIdentity
	}
	fixture = fixture || string(magic[:]) == "#!"
	current, err = trustedhost.InspectExecutable(config.Executable)
	if err != nil || current != a.executable || ctx.Err() != nil {
		return ProtocolReviewRequest{}, ErrIdentity
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now().UTC()
	for nonce, old := range r.pending {
		if !now.Before(old.ExpiresAt) {
			delete(r.pending, nonce)
		}
	}
	request := ProtocolReviewRequest{Identity: a.qualificationIdentity(), Corpus: protocolCorpusDigest(), CreatedAt: now, ExpiresAt: now.Add(validity), Fixture: fixture}
	if _, err := rand.Read(request.Nonce[:]); err != nil {
		return ProtocolReviewRequest{}, err
	}
	r.pending[request.Nonce] = request
	return request, nil
}

func (r *ProtocolReviews) Register(request ProtocolReviewRequest, signature ProtocolReviewSignature) error {
	if r == nil {
		return ErrProtocolReview
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	expected, ok := r.pending[request.Nonce]
	key := r.keys[signature.Reviewer]
	reference, referenceErr := url.Parse(signature.SourceReference)
	// Source identity and policy identity are part of the signed request. The
	// reviewer attests the native-source mapping; replay checks alone cannot.
	if !ok || expected != request || now.Before(request.CreatedAt) || !now.Before(request.ExpiresAt) ||
		len(key) != ed25519.PublicKeySize || referenceErr != nil || reference.Scheme != "https" || reference.Host == "" || reference.User != nil || reference.RawQuery != "" || reference.Fragment != "" || reference.Path == "" || len(signature.SourceReference) > 2048 {
		return ErrProtocolReview
	}
	message := ProtocolReviewMessage(request, signature.Reviewer, signature.SourceReference)
	if !ed25519.Verify(key, message[:], signature.Signature) {
		return ErrProtocolReview
	}
	delete(r.pending, request.Nonce) // one successful registration per challenge
	r.approved[request.Identity.Digest()] = protocolApproval{request: request, reviewer: signature.Reviewer, reference: signature.SourceReference, digest: message}
	return nil
}

// RevokeReviewer also invalidates existing approvals at the next fingerprint or
// spawn check. A new service/key set requires fresh review challenges.
func (r *ProtocolReviews) RevokeReviewer(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keys, id)
}

func (r *ProtocolReviews) lookup(identity execution.QualificationIdentity) (protocolApproval, bool) {
	if r == nil {
		return protocolApproval{}, false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.approved[identity.Digest()]
	now := r.now()
	if !ok || len(r.keys[a.reviewer]) != ed25519.PublicKeySize || a.request.Identity != identity || a.request.Corpus != protocolCorpusDigest() || now.Before(a.request.CreatedAt) || !now.Before(a.request.ExpiresAt) {
		delete(r.approved, identity.Digest())
		return protocolApproval{}, false
	}
	return a, true
}

// metadataAdapter shares the exact configuration/source preflight with New.
// It intentionally stops before selected-scope authentication or policy proof.
func metadataAdapter(ctx context.Context, config Config) (*Adapter, error) {
	if !canonicalDirectory(config.AuthHome) || !canonicalDirectory(config.Home) || config.AuthHome == config.Home || strings.TrimSpace(config.Model) != config.Model || strings.HasPrefix(config.Model, "-") || strings.ContainsAny(config.Model, "\x00\r\n") {
		return nil, ErrPolicy
	}
	identity, err := trustedhost.InspectExecutable(config.Executable)
	if err != nil {
		return nil, ErrInstallation
	}
	network, err := runtimeenv.New(config.NetworkEnvironment)
	if err != nil {
		return nil, ErrPolicy
	}
	config.NetworkEnvironment = nil
	a := &Adapter{config: config, executable: identity, network: network}
	if err := a.validateSources(); err != nil {
		return nil, err
	}
	version, err := a.probe(ctx, "--version")
	if err != nil || len(strings.TrimSpace(version)) > 128 || !nativeVersion.MatchString(strings.TrimSpace(version)) {
		return nil, ErrVersion
	}
	a.version = strings.TrimSpace(version)
	return a, nil
}
