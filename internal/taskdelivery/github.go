package taskdelivery

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const githubHost = "github.com"

type githubCLI struct {
	mu        sync.RWMutex
	path      string
	available bool
	env       []string
}

// NewGitHub uses the installed GitHub CLI and its native github.com account.
// It never reads, prints, refreshes, or otherwise changes authentication.
func NewGitHub() Hosting {
	g := &githubCLI{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	g.RefreshAvailability(ctx)
	return g
}

func (g *githubCLI) Available() bool {
	if g == nil {
		return false
	}
	g.mu.RLock()
	defer g.mu.RUnlock()
	return g.path != "" && g.available
}

// RefreshAvailability re-discovers gh and re-checks its active github.com
// account. The application calls this only for an explicit delivery refresh;
// ordinary delivery reads remain local and side-effect free.
func (g *githubCLI) RefreshAvailability(ctx context.Context) {
	path, err := exec.LookPath("gh")
	available := false
	if err == nil {
		probe := &githubCLI{path: path}
		_, err = probe.run(ctx, nil, "auth", "status", "--active", "--hostname", githubHost)
		available = err == nil
	}
	g.mu.Lock()
	g.path, g.available = path, available
	g.mu.Unlock()
}

func (g *githubCLI) PreviewPR(ctx context.Context, push PushPreview, title, body, marker string) (HostingPreview, error) {
	if !g.Available() {
		return HostingPreview{}, unsupported("GitHub CLI is unavailable or not authenticated to github.com")
	}
	repository, err := githubRepository(push.URL)
	if err != nil {
		return HostingPreview{}, err
	}
	base := strings.TrimPrefix(push.Binding.TargetRef, "refs/heads/")
	if !validGitHubBranch(push.Binding.Branch) || !validGitHubBranch(base) ||
		push.RemoteRef != "refs/heads/"+push.Binding.Branch || !isOID(push.Head) ||
		!isOID(push.TargetHead) || title == "" || strings.ContainsAny(title, "\x00\n\r") ||
		strings.ContainsRune(body, 0) || len(title) > 256 || len(body) > commandOutputLimit {
		return HostingPreview{}, unsupported("invalid GitHub pull request input")
	}
	renderedMarker, err := githubMarker(marker)
	if err != nil {
		return HostingPreview{}, err
	}
	if err = g.verifyRefs(ctx, repository, push.Binding.Branch, push.Head, base, push.TargetHead); err != nil {
		return HostingPreview{}, err
	}
	p := HostingPreview{Kind: "pr", Repository: repository, HeadBranch: push.Binding.Branch, BaseBranch: base, Head: push.Head, BaseHead: push.TargetHead, Title: title, Body: body, Marker: renderedMarker, MergeMethod: "merge"}
	p.Digest = hostingDigest(p)
	matches, err := g.listPRs(ctx, p)
	if err != nil {
		return HostingPreview{}, err
	}
	if len(matches) != 0 {
		return HostingPreview{}, conflict("a pull request already exists for the exact GitHub branch pair")
	}
	return p, nil
}

func (g *githubCLI) PreviewMerge(ctx context.Context, created HostingPreview, number int) (HostingPreview, error) {
	if !g.Available() {
		return HostingPreview{}, unsupported("GitHub CLI is unavailable or not authenticated to github.com")
	}
	if created.Kind != "pr" || created.Digest != hostingDigest(created) || number <= 0 {
		return HostingPreview{}, conflict("pull request preview is invalid")
	}
	pr, err := g.viewPR(ctx, created.Repository, number)
	if err != nil {
		return HostingPreview{}, err
	}
	if err = exactPRIdentity(created, pr, false); err != nil {
		return HostingPreview{}, err
	}
	if pr.State != "open" {
		return HostingPreview{}, conflict("pull request is not open")
	}
	// Re-read the base, but do not silently adopt an advanced target: immutable
	// review authority belongs to the frozen Task base.
	if pr.BaseHead != created.BaseHead {
		return HostingPreview{}, conflict("GitHub target branch advanced after review")
	}
	p := created
	p.Kind, p.Number, p.MergeMethod = "merge", number, "merge"
	p.Digest = hostingDigest(p)
	return p, nil
}

func (g *githubCLI) Confirm(ctx context.Context, p HostingPreview) (PullRequest, error) {
	if !g.Available() {
		return PullRequest{}, unsupported("GitHub CLI is unavailable or not authenticated to github.com")
	}
	if p.Digest == "" || p.Digest != hostingDigest(p) {
		return PullRequest{}, conflict("GitHub preview is invalid")
	}
	switch p.Kind {
	case "pr":
		if p.Number != 0 {
			return PullRequest{}, conflict("pull request creation preview is invalid")
		}
		if err := g.verifyRefs(ctx, p.Repository, p.HeadBranch, p.Head, p.BaseBranch, p.BaseHead); err != nil {
			return PullRequest{}, beforeHostingMutation(err, "GitHub pull request preflight could not be completed")
		}
		matches, err := g.listPRs(ctx, p)
		if err != nil {
			return PullRequest{}, beforeHostingMutation(err, "GitHub pull request preflight could not be completed")
		}
		if len(matches) == 1 {
			return matches[0], nil
		}
		if len(matches) > 1 {
			return PullRequest{}, hostingMutationNotStarted("GitHub pull request marker is ambiguous")
		}
		body := p.Body
		if body != "" && !strings.HasSuffix(body, "\n") {
			body += "\n"
		}
		body += "\n" + p.Marker + "\n"
		_, started, err := g.runTracked(ctx, []byte(body), "pr", "create", "-R", p.Repository, "--head", p.HeadBranch, "--base", p.BaseBranch, "--title", p.Title, "--body-file", "-")
		if err != nil && !started {
			return PullRequest{}, hostingMutationNotStarted("GitHub pull request creation was not started")
		}
		if err != nil {
			return PullRequest{}, recovery("GitHub pull request outcome requires reconciliation")
		}
		matches, err = g.listPRs(ctx, p)
		if err != nil || len(matches) != 1 {
			return PullRequest{}, recovery("GitHub pull request outcome requires reconciliation")
		}
		return matches[0], nil
	case "merge":
		if p.Number <= 0 || p.MergeMethod != "merge" {
			return PullRequest{}, conflict("GitHub merge preview is invalid")
		}
		before, err := g.viewPR(ctx, p.Repository, p.Number)
		if err != nil {
			return PullRequest{}, beforeHostingMutation(err, "GitHub merge preflight could not be completed")
		}
		if err = exactPRIdentity(p, before, true); err != nil {
			return PullRequest{}, err
		}
		if before.State != "open" {
			return PullRequest{}, conflict("pull request is not open")
		}
		// The REST merge endpoint is synchronous. Supplying the exact head SHA is
		// conditional authority, and merge_method=merge forbids squash/rebase.
		raw, started, err := g.runTracked(ctx, nil, "api", "--method", "PUT", "repos/"+p.Repository+"/pulls/"+strconv.Itoa(p.Number)+"/merge", "-f", "sha="+p.Head, "-f", "merge_method=merge")
		if err != nil && !started {
			return PullRequest{}, hostingMutationNotStarted("GitHub merge was not started")
		}
		if err != nil {
			return PullRequest{}, recovery("GitHub merge outcome requires reconciliation")
		}
		var result struct {
			Merged bool   `json:"merged"`
			SHA    string `json:"sha"`
		}
		if json.Unmarshal(raw, &result) != nil || !result.Merged || !isOID(result.SHA) {
			return PullRequest{}, recovery("GitHub merge outcome requires reconciliation")
		}
		after, err := g.viewPR(ctx, p.Repository, p.Number)
		if err != nil || after.State != "merged" || after.MergeCommit != result.SHA {
			return PullRequest{}, recovery("GitHub merge outcome requires reconciliation")
		}
		if err = exactPRIdentity(p, after, false); err != nil {
			return PullRequest{}, recovery("GitHub merge identity requires reconciliation")
		}
		return after, nil
	default:
		return PullRequest{}, unsupported("unsupported GitHub operation")
	}
}

func (g *githubCLI) Observe(ctx context.Context, p HostingPreview) (PullRequest, error) {
	if !g.Available() {
		return PullRequest{}, unsupported("GitHub CLI is unavailable or not authenticated to github.com")
	}
	if p.Digest == "" || p.Digest != hostingDigest(p) {
		return PullRequest{}, conflict("GitHub preview is invalid")
	}
	if p.Number > 0 {
		pr, err := g.viewPR(ctx, p.Repository, p.Number)
		if err != nil {
			return PullRequest{}, err
		}
		if err = exactPRIdentity(p, pr, p.Kind == "merge" && pr.State != "merged"); err != nil {
			return PullRequest{}, err
		}
		return pr, nil
	}
	matches, err := g.listPRs(ctx, p)
	if err != nil {
		return PullRequest{}, err
	}
	if len(matches) == 0 {
		return PullRequest{}, recovery("GitHub pull request has not been proven")
	}
	if len(matches) != 1 {
		return PullRequest{}, recovery("GitHub pull request marker is ambiguous")
	}
	return matches[0], nil
}

type githubPRJSON struct {
	Number            int     `json:"number"`
	URL               string  `json:"url"`
	State             string  `json:"state"`
	Body              string  `json:"body"`
	HeadRefName       string  `json:"headRefName"`
	HeadRefOID        string  `json:"headRefOid"`
	BaseRefName       string  `json:"baseRefName"`
	BaseRefOID        string  `json:"baseRefOid"`
	IsCrossRepository bool    `json:"isCrossRepository"`
	MergedAt          *string `json:"mergedAt"`
	MergeCommit       *struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
}

func (g *githubCLI) listPRs(ctx context.Context, p HostingPreview) ([]PullRequest, error) {
	raw, err := g.run(ctx, nil, "pr", "list", "-R", p.Repository, "--state", "all", "--head", p.HeadBranch, "--base", p.BaseBranch, "--limit", "100", "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit")
	if err != nil {
		return nil, recovery("GitHub pull request state cannot be read")
	}
	var records []githubPRJSON
	if json.Unmarshal(raw, &records) != nil {
		return nil, recovery("GitHub pull request response is invalid")
	}
	if len(records) >= 100 {
		return nil, recovery("GitHub pull request history exceeds the bounded identity lookup")
	}
	var matches []PullRequest
	for _, record := range records {
		// A matching branch pair with any unexpected identity is an ambiguity,
		// rather than evidence that creating another PR is safe.
		if record.HeadRefName != p.HeadBranch || record.BaseRefName != p.BaseBranch || record.IsCrossRepository || record.HeadRefOID != p.Head {
			return nil, conflict("GitHub pull request identity does not match preview")
		}
		if !strings.Contains(record.Body, p.Marker) {
			return nil, conflict("GitHub branch pair belongs to a different pull request intent")
		}
		pr, err := normalizeGitHubPR(p.Repository, record)
		if err != nil {
			return nil, err
		}
		matches = append(matches, pr)
	}
	return matches, nil
}

func (g *githubCLI) viewPR(ctx context.Context, repository string, number int) (PullRequest, error) {
	raw, err := g.run(ctx, nil, "pr", "view", strconv.Itoa(number), "-R", repository, "--json", "number,url,state,body,headRefName,headRefOid,baseRefName,baseRefOid,isCrossRepository,mergedAt,mergeCommit")
	if err != nil {
		return PullRequest{}, recovery("GitHub pull request state cannot be read")
	}
	var record githubPRJSON
	if json.Unmarshal(raw, &record) != nil {
		return PullRequest{}, recovery("GitHub pull request response is invalid")
	}
	return normalizeGitHubPR(repository, record)
}

func normalizeGitHubPR(repository string, record githubPRJSON) (PullRequest, error) {
	if record.Number <= 0 || record.URL == "" || record.HeadRefName == "" || record.BaseRefName == "" || !isOID(record.HeadRefOID) || !isOID(record.BaseRefOID) || record.IsCrossRepository {
		return PullRequest{}, recovery("GitHub pull request identity is incomplete")
	}
	wantURL := "https://github.com/" + repository + "/pull/" + strconv.Itoa(record.Number)
	if record.URL != wantURL {
		return PullRequest{}, conflict("GitHub pull request URL does not match repository identity")
	}
	state := strings.ToLower(record.State)
	mergeCommit := ""
	if record.MergeCommit != nil {
		mergeCommit = record.MergeCommit.OID
	}
	if record.MergedAt != nil {
		state = "merged"
	}
	if state != "open" && state != "closed" && state != "merged" {
		return PullRequest{}, recovery("GitHub pull request state is unsupported")
	}
	if state == "merged" && !isOID(mergeCommit) {
		return PullRequest{}, recovery("GitHub merged commit identity is incomplete")
	}
	return PullRequest{Repository: repository, HeadBranch: record.HeadRefName, BaseBranch: record.BaseRefName, Head: record.HeadRefOID, BaseHead: record.BaseRefOID, URL: record.URL, State: state, MergeCommit: mergeCommit, Number: record.Number}, nil
}

func exactPRIdentity(p HostingPreview, pr PullRequest, requireBase bool) error {
	if pr.Repository != p.Repository || pr.HeadBranch != p.HeadBranch || pr.BaseBranch != p.BaseBranch || pr.Head != p.Head || (requireBase && pr.BaseHead != p.BaseHead) {
		return conflict("GitHub pull request identity does not match preview")
	}
	return nil
}

func (g *githubCLI) verifyRefs(ctx context.Context, repository, headBranch, head, baseBranch, base string) error {
	actualHead, err := g.refOID(ctx, repository, headBranch)
	if err != nil {
		return err
	}
	actualBase, err := g.refOID(ctx, repository, baseBranch)
	if err != nil {
		return err
	}
	if actualHead != head || actualBase != base {
		return conflict("GitHub branch state differs from preview")
	}
	return nil
}

func (g *githubCLI) refOID(ctx context.Context, repository, branch string) (string, error) {
	endpoint := "repos/" + repository + "/commits/" + url.PathEscape(branch)
	raw, err := g.run(ctx, nil, "api", endpoint, "--jq", ".sha")
	if err != nil {
		return "", recovery("GitHub branch state cannot be read")
	}
	oid := strings.TrimSpace(string(raw))
	if !isOID(oid) {
		return "", recovery("GitHub branch identity is invalid")
	}
	return oid, nil
}

func (g *githubCLI) run(parent context.Context, input []byte, args ...string) ([]byte, error) {
	out, _, err := g.runTracked(parent, input, args...)
	return out, err
}

// runTracked reports whether the child started. Callers may use started=false
// to prove that a hosting mutation was not submitted. Any failure after Start
// remains outcome-unknown to a mutating caller.
func (g *githubCLI) runTracked(parent context.Context, input []byte, args ...string) ([]byte, bool, error) {
	if g == nil {
		return nil, false, errors.New("gh unavailable")
	}
	g.mu.RLock()
	path := g.path
	extraEnv := append([]string(nil), g.env...)
	g.mu.RUnlock()
	if path == "" {
		return nil, false, errors.New("gh unavailable")
	}
	ctx, cancel := context.WithTimeout(parent, remoteCommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = append(os.Environ(), extraEnv...)
	cmd.Env = append(cmd.Env, "GH_PROMPT_DISABLED=1", "GH_HOST="+githubHost, "NO_COLOR=1")
	var out cappedBuffer
	out.limit = commandOutputLimit
	cmd.Stdout = &out
	cmd.Stderr = &cappedBuffer{limit: 64 << 10}
	if err := cmd.Start(); err != nil {
		return nil, false, errors.New("gh command failed")
	}
	err := cmd.Wait()
	if ctx.Err() != nil || out.exceeded {
		return nil, true, errors.New("gh command failed")
	}
	if err != nil {
		return nil, true, errors.New("gh command failed")
	}
	return out.Bytes(), true, nil
}

func beforeHostingMutation(err error, message string) error {
	if errors.Is(err, ErrRecovery) {
		return hostingMutationNotStarted(message)
	}
	return err
}

func hostingMutationNotStarted(message string) error {
	return fmt.Errorf("%w: %s", ErrHostingMutationNotStarted, message)
}

var githubRepoPath = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)

func githubRepository(raw string) (string, error) {
	var repo string
	if strings.HasPrefix(raw, "git@github.com:") {
		repo = strings.TrimPrefix(raw, "git@github.com:")
	} else {
		u, err := url.Parse(raw)
		if err != nil || !strings.EqualFold(u.Hostname(), githubHost) || u.Port() != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "ssh" && u.Scheme != "git") {
			return "", unsupported("push destination is not an exact credential-free github.com repository")
		}
		repo = strings.TrimPrefix(u.Path, "/")
	}
	repo = strings.TrimSuffix(repo, ".git")
	if !githubRepoPath.MatchString(repo) || strings.Contains(repo, "..") {
		return "", unsupported("push destination is not an exact github.com repository")
	}
	return repo, nil
}

func githubMarker(value string) (string, error) {
	if value == "" || len(value) > 512 || strings.ContainsRune(value, 0) {
		return "", unsupported("invalid GitHub reconciliation marker")
	}
	return "<!-- chora-task-delivery:" + hash([]byte(value)) + " -->", nil
}

func validGitHubBranch(branch string) bool {
	return validBranch(branch) && !strings.Contains(branch, "\\") && !strings.HasSuffix(branch, "/") && !strings.Contains(branch, "//")
}

func hostingDigest(p HostingPreview) string {
	return hash([]byte(strings.Join([]string{p.Kind, p.Repository, p.HeadBranch, p.BaseBranch, p.Head, p.BaseHead, p.Title, p.Body, p.Marker, p.MergeMethod, strconv.Itoa(p.Number)}, "\x1f")))
}
