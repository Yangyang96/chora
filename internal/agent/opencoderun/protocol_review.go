package opencoderun

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

var ErrProtocolReview = errors.New("opencode_protocol_review_invalid_or_expired")

// ProtocolReviewRequest binds a host-generated challenge to actual metadata and
// the fixed adapter corpus. Fixture challenges remain explicitly synthetic.
type ProtocolReviewRequest struct {
	Identity             execution.QualificationIdentity
	Nonce, Corpus        [32]byte
	CreatedAt, ExpiresAt time.Time
	Fixture              bool
}

// ProtocolAttestation is signed by a separately provisioned trusted reviewer.
// EvidenceDigest identifies that reviewer's evidence package. Native review must
// establish binary/source mapping, tool/terminal/exit pairing, authentication
// error semantics and full turn semantics beyond the adapter's partial corpus.
// These attestations never supply native configuration or credential authority.
type ProtocolAttestation struct {
	SourceReference, SourceCommit string
	EvidenceDigest                [32]byte
	NativeSourceMapping           bool
	NativeToolTerminalExit        bool
	NativeAuthenticationSemantics bool
	NativeFullTurnSemantics       bool
}

type ProtocolReviewSignature struct {
	Reviewer    string
	Attestation ProtocolAttestation
	Signature   []byte
}

func ProtocolReviewMessage(request ProtocolReviewRequest, reviewer string, attestation ProtocolAttestation) [32]byte {
	return hash(struct {
		Domain      string
		Request     ProtocolReviewRequest
		Reviewer    string
		Attestation ProtocolAttestation
	}{"chora.opencode-protocol-review.v1", request, reviewer, attestation})
}

type protocolApproval struct {
	request     ProtocolReviewRequest
	reviewer    string
	attestation ProtocolAttestation
	digest      [32]byte
}

// ProtocolReviews is a process-local host trust service. Reviewer keys are
// provisioned by the embedding host or operator. Task/config JSON, diagnostic
// reports and arbitrary transcripts have no registration authority.
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
		if !identifier(id) || len(key) != ed25519.PublicKeySize {
			return nil, ErrProtocolReview
		}
		r.keys[id] = append(ed25519.PublicKey(nil), key...)
	}
	return r, nil
}

// Validate runs isolated version/help probes and a compiled-in synthetic corpus.
// A trusted reviewer must separately establish the native mapping and full
// semantics. No authentication scope or native model task is accessed here.
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
	file, err := os.Open(a.executable.ResolvedPath)
	if err != nil {
		return ProtocolReviewRequest{}, ErrIdentity
	}
	var magic [4]byte
	digest := sha256.New()
	_, readErr := io.ReadFull(io.TeeReader(file, digest), magic[:])
	_, hashErr := io.Copy(digest, file)
	file.Close()
	var actual [32]byte
	copy(actual[:], digest.Sum(nil))
	if readErr != nil || hashErr != nil || actual != a.executable.SHA256 {
		return ProtocolReviewRequest{}, ErrIdentity
	}
	// Scripts and unrecognized wrappers cannot relabel themselves native.
	fixture = fixture || !nativeBinary(magic)
	current, err := trustedhost.InspectExecutable(config.Executable)
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

func nativeBinary(magic [4]byte) bool {
	switch magic {
	case [4]byte{0x7f, 'E', 'L', 'F'}, [4]byte{0xcf, 0xfa, 0xed, 0xfe}, [4]byte{0xfe, 0xed, 0xfa, 0xcf}, [4]byte{0xce, 0xfa, 0xed, 0xfe}, [4]byte{0xfe, 0xed, 0xfa, 0xce}, [4]byte{0xca, 0xfe, 0xba, 0xbe}:
		return true
	}
	return false
}

func validAttestation(request ProtocolReviewRequest, a ProtocolAttestation) bool {
	reference, err := url.Parse(a.SourceReference)
	commit, commitErr := hex.DecodeString(a.SourceCommit)
	if err != nil || reference.Scheme != "https" || reference.Host == "" || reference.User != nil || reference.RawQuery != "" || reference.Fragment != "" || len(a.SourceReference) > 2048 || commitErr != nil || (len(commit) != 20 && len(commit) != 32) || !strings.Contains("/"+strings.Trim(reference.Path, "/")+"/", "/"+a.SourceCommit+"/") || a.EvidenceDigest == ([32]byte{}) {
		return false
	}
	if request.Fixture {
		return !a.NativeSourceMapping && !a.NativeToolTerminalExit && !a.NativeAuthenticationSemantics && !a.NativeFullTurnSemantics
	}
	return a.NativeSourceMapping && a.NativeToolTerminalExit && a.NativeAuthenticationSemantics && a.NativeFullTurnSemantics
}

func (r *ProtocolReviews) Register(request ProtocolReviewRequest, signature ProtocolReviewSignature) error {
	if r == nil || r.keys == nil || r.pending == nil || r.now == nil {
		return ErrProtocolReview
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	expected, ok := r.pending[request.Nonce]
	now := r.now()
	key := r.keys[signature.Reviewer]
	if !ok || expected != request || now.Before(request.CreatedAt) || !now.Before(request.ExpiresAt) || len(key) != ed25519.PublicKeySize || !validAttestation(request, signature.Attestation) {
		return ErrProtocolReview
	}
	message := ProtocolReviewMessage(request, signature.Reviewer, signature.Attestation)
	if !ed25519.Verify(key, message[:], signature.Signature) {
		return ErrProtocolReview
	}
	delete(r.pending, request.Nonce)
	r.approved[request.Identity.Digest()] = protocolApproval{request: request, reviewer: signature.Reviewer, attestation: signature.Attestation, digest: message}
	return nil
}

func (r *ProtocolReviews) RevokeReviewer(id string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.keys, id)
}

func (r *ProtocolReviews) lookup(identity execution.QualificationIdentity) (protocolApproval, bool) {
	if r == nil || r.keys == nil || r.approved == nil || r.now == nil {
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
