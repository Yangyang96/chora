package hermes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/Yangyang96/chora/internal/execution"
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
		"chora.hermes-semantic-review.v1", r, reviewer, reference,
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

// Validate reads selected implementation metadata and exercises only Chora's
// fixed replay corpus. No subprocess, external transcript or credential is used.
// The host reviewer separately audits the source mapping and semantics. This
// service can approve semantic evidence only; startup safety is never approvable.
func (r *ProtocolReviews) Validate(ctx context.Context, config Config, validity time.Duration) (ProtocolReviewRequest, error) {
	if r == nil || r.now == nil || r.keys == nil || r.pending == nil || r.approved == nil || validity <= 0 || validity > 24*time.Hour {
		return ProtocolReviewRequest{}, ErrProtocolReview
	}
	_, identity, err := inspectIdentity(ctx, config)
	if err != nil {
		return ProtocolReviewRequest{}, err
	}
	if err := validateProtocolConformance(); err != nil {
		return ProtocolReviewRequest{}, err
	}
	_, current, err := inspectIdentity(ctx, config)
	if err != nil || current != identity {
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
	if len(r.pending) >= 128 {
		return ProtocolReviewRequest{}, ErrProtocolReview
	}
	request := ProtocolReviewRequest{Identity: identity, Corpus: protocolCorpusDigest(), CreatedAt: now, ExpiresAt: now.Add(validity)}
	if _, err := rand.Read(request.Nonce[:]); err != nil {
		return ProtocolReviewRequest{}, err
	}
	r.pending[request.Nonce] = request
	return request, nil
}

func (r *ProtocolReviews) Register(request ProtocolReviewRequest, signature ProtocolReviewSignature) error {
	if r == nil || r.now == nil {
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

// RevokeReviewer also invalidates existing semantic approvals at inspection. A new service/key set requires fresh review challenges.
func (r *ProtocolReviews) RevokeReviewer(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keys, id)
}

func (r *ProtocolReviews) lookup(identity execution.QualificationIdentity) (string, bool) {
	if r == nil || r.now == nil {
		return "", false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	a, ok := r.approved[identity.Digest()]
	now := r.now()
	if !ok || len(r.keys[a.reviewer]) != ed25519.PublicKeySize || a.request.Identity != identity || a.request.Corpus != protocolCorpusDigest() || now.Before(a.request.CreatedAt) || !now.Before(a.request.ExpiresAt) {
		delete(r.approved, identity.Digest())
		return "", false
	}
	return a.reference, true
}
