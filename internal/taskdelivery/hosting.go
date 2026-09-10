package taskdelivery

import (
	"context"
	"errors"
)

// ErrHostingMutationNotStarted is returned only when the hosting adapter proves
// that no provider-mutating child was started and no provider mutation could
// have been submitted. Read-only preflight failures qualify; any unproven
// outcome after a mutating child starts must remain ErrRecovery.
var ErrHostingMutationNotStarted = errors.New("task delivery hosting mutation not started")

// HostingPreview is the immutable provider mutation authority persisted by the
// application. Marker is a Chora-owned idempotency witness, never a credential.
type HostingPreview struct {
	Kind, Repository, HeadBranch, BaseBranch, Head, BaseHead string
	Title, Body, Marker, Digest, MergeMethod                 string
	Number                                                   int
}

type PullRequest struct {
	Repository, HeadBranch, BaseBranch, Head, BaseHead string
	URL, State, MergeCommit                            string
	Number                                             int
}

type Hosting interface {
	Available() bool
	PreviewPR(context.Context, PushPreview, string, string, string) (HostingPreview, error)
	PreviewMerge(context.Context, HostingPreview, int) (HostingPreview, error)
	Confirm(context.Context, HostingPreview) (PullRequest, error)
	Observe(context.Context, HostingPreview) (PullRequest, error)
}
