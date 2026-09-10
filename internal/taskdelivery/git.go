package taskdelivery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

var (
	ErrConflict    = errors.New("task delivery conflict")
	ErrRecovery    = errors.New("task delivery recovery required")
	ErrUnsupported = errors.New("task delivery unsupported")
)

type Binding struct {
	Root, CommonGitDir, BaseCommit, BaseTree, Branch, TargetRef string
}

type CommitPreview struct {
	Binding                                  Binding
	Head, Tree, IndexDigest, Digest, Message string
	Patch                                    []byte
	Paths                                    []string
}

type CommitResult struct{ Commit, Tree string }

type PushPreview struct {
	Binding                                                      Binding
	Remote, URL, RemoteRef, Head, RemoteHead, TargetHead, Digest string
}

type Git struct{}

const commandTimeout = 20 * time.Second
const remoteCommandTimeout = 60 * time.Second
const commandOutputLimit = domain.TaskPatchBytes

// VerifyReviewedChanges is the read-only acceptance boundary for worktree-first
// development. The target branch may advance; this Task's own branch/base and
// complete uncommitted contents must still equal the immutable reviewed Result.
func (Git) VerifyReviewedChanges(ctx context.Context, b Binding, patch []byte, paths []string) error {
	if err := validateBinding(ctx, b, true, false); err != nil {
		return err
	}
	if err := reviewIndexUnconflicted(ctx, b); err != nil {
		return err
	}
	want := paths
	if len(paths) > 0 {
		var err error
		want, err = validatePaths(paths)
		if err != nil {
			return err
		}
	}
	actual, actualPaths, err := currentPatch(ctx, b, want)
	if err != nil {
		return err
	}
	if !sameStrings(actualPaths, want) || !bytes.Equal(actual, patch) {
		return conflict("live worktree differs from reviewed result")
	}
	if err := validateBinding(ctx, b, true, false); err != nil {
		return err
	}
	return reviewIndexUnconflicted(ctx, b)
}

func (Git) PreviewCommit(ctx context.Context, b Binding, patch []byte, paths []string, message string) (CommitPreview, error) {
	if len(patch) > domain.TaskPatchBytes {
		return CommitPreview{}, unsupported("patch exceeds Task review policy")
	}
	if err := validateBinding(ctx, b, true, true); err != nil {
		return CommitPreview{}, err
	}
	if message == "" || bytes.IndexByte([]byte(message), 0) >= 0 {
		return CommitPreview{}, unsupported("invalid commit message")
	}
	cleanPaths, err := validatePaths(paths)
	if err != nil {
		return CommitPreview{}, err
	}
	if err := reviewIndexUnconflicted(ctx, b); err != nil {
		return CommitPreview{}, err
	}
	head, _ := output(ctx, b.Root, nil, "rev-parse", "HEAD")
	actual, actualPaths, err := currentPatch(ctx, b, cleanPaths)
	if err != nil {
		return CommitPreview{}, err
	}
	if !bytes.Equal(actual, patch) {
		return CommitPreview{}, conflict("worktree content differs from reviewed patch")
	}
	if !sameStrings(actualPaths, cleanPaths) {
		return CommitPreview{}, conflict("reviewed paths are not the complete changed path set")
	}
	tree, _, err := previewTree(ctx, b, patch)
	if err != nil {
		return CommitPreview{}, err
	}
	if err := reviewedStaging(ctx, b, tree, cleanPaths); err != nil {
		return CommitPreview{}, err
	}
	indexDigest, err := taskIndexDigest(ctx, b)
	if err != nil {
		return CommitPreview{}, err
	}
	p := CommitPreview{Binding: b, Head: head, Tree: tree, IndexDigest: indexDigest, Message: message, Patch: append([]byte(nil), patch...), Paths: cleanPaths}
	p.Digest = commitDigest(p)
	return p, nil
}

func (Git) Commit(ctx context.Context, p CommitPreview) (CommitResult, error) {
	if p.Digest == "" || p.Digest != commitDigest(p) {
		return CommitResult{}, conflict("commit preview is invalid")
	}
	fresh, err := (Git{}).PreviewCommit(ctx, p.Binding, p.Patch, p.Paths, p.Message)
	if err != nil {
		return CommitResult{}, err
	}
	if fresh.Digest != p.Digest {
		return CommitResult{}, conflict("commit preview is stale")
	}
	msg, err := os.CreateTemp("", "chora-commit-message-*")
	if err != nil {
		return CommitResult{}, recovery("cannot prepare commit")
	}
	msgName := msg.Name()
	defer os.Remove(msgName)
	if _, err = msg.WriteString(p.Message); err != nil {
		msg.Close()
		return CommitResult{}, recovery("cannot prepare commit")
	}
	if err = msg.Close(); err != nil {
		return CommitResult{}, recovery("cannot prepare commit")
	}
	index, err := materializeIndex(ctx, p.Binding, p.Patch)
	if err != nil {
		return CommitResult{}, err
	}
	defer os.Remove(index)
	indexPath, err := taskIndexPath(ctx, p.Binding)
	if err != nil {
		return CommitResult{}, err
	}
	lockPath := indexPath + ".lock"
	lock, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return CommitResult{}, conflict("task index is busy")
	}
	committedIndex := false
	defer func() {
		if !committedIndex {
			lock.Close()
			os.Remove(lockPath)
		}
	}()
	currentDigest, err := taskIndexDigest(ctx, p.Binding)
	if err != nil || currentDigest != p.IndexDigest {
		return CommitResult{}, conflict("task index changed after preview")
	}
	if _, err = outputBytes(ctx, p.Binding.Root, []string{"GIT_INDEX_FILE=" + index}, "commit", "-F", msgName); err != nil {
		return CommitResult{}, recovery("commit outcome requires reconciliation")
	}
	res, err := reconcileCommitContent(ctx, p)
	if err != nil {
		return CommitResult{}, err
	}
	indexBytes, err := os.ReadFile(index)
	if err != nil {
		return CommitResult{}, recovery("commit succeeded but task index requires recovery")
	}
	if _, err = lock.Write(indexBytes); err != nil {
		return CommitResult{}, recovery("commit succeeded but task index requires recovery")
	}
	if err = lock.Sync(); err != nil {
		return CommitResult{}, recovery("commit succeeded but task index requires recovery")
	}
	if err = lock.Close(); err != nil {
		return CommitResult{}, recovery("commit succeeded but task index requires recovery")
	}
	if err = os.Rename(lockPath, indexPath); err != nil {
		return CommitResult{}, recovery("commit succeeded but task index requires recovery")
	}
	committedIndex = true
	return res, nil
}

func (Git) ReconcileCommit(ctx context.Context, p CommitPreview) (CommitResult, error) {
	result, err := reconcileCommitContent(ctx, p)
	if err != nil {
		return CommitResult{}, err
	}
	// Refresh is read-only: a crash after ref publication must not certify a stale
	// or conflicted real index, nor silently repair user staging.
	staged, err := output(ctx, p.Binding.Root, nil, "diff", "--cached", "--name-only", "--no-ext-diff", result.Commit, "--")
	if err != nil || staged != "" {
		return CommitResult{}, recovery("commit exists but task index requires recovery")
	}
	return result, nil
}

func reconcileCommitContent(ctx context.Context, p CommitPreview) (CommitResult, error) {
	if p.Digest == "" || p.Digest != commitDigest(p) {
		return CommitResult{}, conflict("commit preview is invalid")
	}
	if err := validateBinding(ctx, p.Binding, false, false); err != nil {
		return CommitResult{}, err
	}
	head, err := output(ctx, p.Binding.Root, nil, "rev-parse", "refs/heads/"+p.Binding.Branch)
	if err != nil {
		return CommitResult{}, recovery("commit cannot be reconciled")
	}
	if head == p.Head {
		return CommitResult{}, conflict("commit has not been written")
	}
	parent, err := output(ctx, p.Binding.Root, nil, "rev-parse", head+"^")
	if err != nil || parent != p.Head {
		return CommitResult{}, recovery("commit identity does not match preview")
	}
	tree, err := output(ctx, p.Binding.Root, nil, "show", "-s", "--format=%T", head)
	if err != nil || tree != p.Tree {
		return CommitResult{}, recovery("commit tree does not match preview")
	}
	object, err := outputBytes(ctx, p.Binding.Root, nil, "cat-file", "commit", head)
	separator := bytes.Index(object, []byte("\n\n"))
	expected, expectedErr := run(ctx, p.Binding.Root, nil, []byte(p.Message), "stripspace")
	if len(expected) > 0 && expected[len(expected)-1] != '\n' {
		expected = append(expected, '\n')
	}
	if err != nil || expectedErr != nil || separator < 0 || !bytes.Equal(object[separator+2:], expected) {
		return CommitResult{}, recovery("commit message does not match preview")
	}
	actual, paths, err := currentPatch(ctx, p.Binding, p.Paths)
	if err != nil || !sameStrings(paths, p.Paths) || !bytes.Equal(actual, p.Patch) {
		return CommitResult{}, recovery("worktree content changed during commit")
	}
	return CommitResult{Commit: head, Tree: tree}, nil
}

func (Git) PreviewPush(ctx context.Context, b Binding, remote string) (PushPreview, error) {
	if err := validateBinding(ctx, b, false, true); err != nil {
		return PushPreview{}, err
	}
	if remote == "" || strings.HasPrefix(remote, "-") || strings.ContainsAny(remote, "\x00\n\r") {
		return PushPreview{}, unsupported("invalid remote")
	}
	pushURL, err := exactPushURL(ctx, b, remote)
	if err != nil {
		return PushPreview{}, err
	}
	head, _ := output(ctx, b.Root, nil, "rev-parse", "refs/heads/"+b.Branch)
	remoteRef := "refs/heads/" + b.Branch
	remoteHead, err := lsRemote(ctx, b.Root, pushURL, remoteRef)
	if err != nil {
		return PushPreview{}, err
	}
	targetHead, err := lsRemote(ctx, b.Root, pushURL, b.TargetRef)
	if err != nil {
		return PushPreview{}, err
	}
	if targetHead != b.BaseCommit {
		return PushPreview{}, conflict("remote target branch advanced")
	}
	if remoteHead != "" && remoteHead != head {
		return PushPreview{}, conflict("remote task branch differs from local task branch")
	}
	p := PushPreview{Binding: b, Remote: remote, URL: pushURL, RemoteRef: remoteRef, Head: head, RemoteHead: remoteHead, TargetHead: targetHead}
	p.Digest = pushDigest(p)
	return p, nil
}

func (Git) Push(ctx context.Context, p PushPreview) error {
	if p.Digest == "" || p.Digest != pushDigest(p) {
		return conflict("push preview is invalid")
	}
	fresh, err := (Git{}).PreviewPush(ctx, p.Binding, p.Remote)
	if err != nil {
		return err
	}
	if fresh.Digest != p.Digest {
		return conflict("push preview is stale")
	}
	if p.RemoteHead == p.Head {
		return nil
	}
	_, err = outputBytes(ctx, p.Binding.Root, nil, "push", "--porcelain", "--no-follow-tags", "--recurse-submodules=no", "--", p.URL, p.Head+":"+p.RemoteRef)
	if err != nil {
		return recovery("push outcome requires reconciliation")
	}
	ok, err := (Git{}).ReconcilePush(ctx, p)
	if err != nil {
		return err
	}
	if !ok {
		return recovery("pushed ref does not match preview")
	}
	return nil
}

func (Git) ReconcilePush(ctx context.Context, p PushPreview) (bool, error) {
	if p.Digest == "" || p.Digest != pushDigest(p) {
		return false, conflict("push preview is invalid")
	}
	if err := validateBinding(ctx, p.Binding, false, false); err != nil {
		return false, err
	}
	local, err := output(ctx, p.Binding.Root, nil, "rev-parse", "refs/heads/"+p.Binding.Branch)
	if err != nil || local != p.Head {
		return false, recovery("local task ref differs from preview")
	}
	actualURL, err := exactPushURL(ctx, p.Binding, p.Remote)
	if err != nil {
		return false, err
	}
	if actualURL != p.URL || p.RemoteRef != "refs/heads/"+p.Binding.Branch {
		return false, recovery("push destination differs from preview")
	}
	head, err := lsRemote(ctx, p.Binding.Root, p.URL, p.RemoteRef)
	if err != nil {
		return false, err
	}
	return head == p.Head, nil
}

func validateBinding(ctx context.Context, b Binding, requireBaseHead, requireTarget bool) error {
	if err := validateBindingStatic(b); err != nil {
		return err
	}
	rootCanonical, rootErr := filepath.EvalSymlinks(filepath.Clean(b.Root))
	top, topErr := output(ctx, b.Root, nil, "rev-parse", "--path-format=absolute", "--show-toplevel")
	topCanonical, canonicalErr := filepath.EvalSymlinks(filepath.Clean(top))
	if rootErr != nil || topErr != nil || canonicalErr != nil || rootCanonical != topCanonical {
		return conflict("repository root changed")
	}
	common, err := output(ctx, b.Root, nil, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if err != nil {
		return err
	}
	want, e1 := filepath.EvalSymlinks(filepath.Clean(b.CommonGitDir))
	got, e2 := filepath.EvalSymlinks(filepath.Clean(common))
	if e1 != nil || e2 != nil || want != got {
		return conflict("repository identity changed")
	}
	head, err := output(ctx, b.Root, nil, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	branch, err := output(ctx, b.Root, nil, "symbolic-ref", "--short", "HEAD")
	if err != nil || branch != b.Branch {
		return conflict("task branch changed")
	}
	if requireTarget {
		target, err := output(ctx, b.Root, nil, "rev-parse", b.TargetRef)
		if err != nil || target != b.BaseCommit {
			return conflict("target branch advanced")
		}
	}
	baseTree, err := output(ctx, b.Root, nil, "show", "-s", "--format=%T", b.BaseCommit)
	if err != nil || baseTree != b.BaseTree {
		return conflict("frozen base changed")
	}
	if requireBaseHead && head != b.BaseCommit {
		return conflict("task branch already changed")
	}
	return nil
}

func validateBindingStatic(b Binding) error {
	if b.Root == "" || b.CommonGitDir == "" || !isOID(b.BaseCommit) || !isOID(b.BaseTree) || !validBranch(b.Branch) || !strings.HasPrefix(b.TargetRef, "refs/heads/") {
		return unsupported("invalid repository binding")
	}
	cleanRoot := filepath.Clean(b.Root)
	if !filepath.IsAbs(cleanRoot) {
		return unsupported("repository root is not canonical")
	}
	return nil
}

func validBranch(s string) bool {
	return s != "" && !strings.HasPrefix(s, "-") && !strings.HasPrefix(s, "refs/") && !strings.ContainsAny(s, "\x00\n\r ") && !strings.Contains(s, "..") && !strings.HasSuffix(s, ".") && !strings.Contains(s, "@{")
}
func isOID(s string) bool {
	if len(s) < 40 || len(s) > 64 {
		return false
	}
	for _, c := range s {
		if !strings.ContainsRune("0123456789abcdef", c) {
			return false
		}
	}
	return true
}

func validatePaths(paths []string) ([]string, error) {
	if len(paths) == 0 {
		return nil, unsupported("no reviewed paths")
	}
	r := append([]string(nil), paths...)
	sort.Strings(r)
	for i, p := range r {
		if p == "" || filepath.IsAbs(p) || p == "." || strings.HasPrefix(filepath.Clean(p), "..") || strings.ContainsRune(p, 0) {
			return nil, unsupported("invalid reviewed path")
		}
		if i > 0 && p == r[i-1] {
			return nil, unsupported("duplicate reviewed path")
		}
	}
	return r, nil
}

func cleanIndex(ctx context.Context, b Binding) error {
	u, err := outputBytes(ctx, b.Root, nil, "ls-files", "-u")
	if err != nil {
		return err
	}
	if len(u) > 0 {
		return unsupported("conflicted index")
	}
	_, err = outputBytes(ctx, b.Root, nil, "diff", "--cached", "--quiet", "HEAD", "--")
	if err != nil {
		if errors.Is(err, ErrConflict) {
			return conflict("task index contains staged changes")
		}
		return err
	}
	return nil
}

func taskIndexPath(ctx context.Context, b Binding) (string, error) {
	p, err := output(ctx, b.Root, nil, "rev-parse", "--path-format=absolute", "--git-path", "index")
	if err != nil {
		return "", err
	}
	if !filepath.IsAbs(p) {
		return "", unsupported("task index path is invalid")
	}
	return filepath.Clean(p), nil
}
func taskIndexDigest(ctx context.Context, b Binding) (string, error) {
	p, err := taskIndexPath(ctx, b)
	if err != nil {
		return "", err
	}
	raw, err := os.ReadFile(p)
	if err != nil {
		return "", recovery("task index cannot be read")
	}
	return hash(raw), nil
}

func previewTree(ctx context.Context, b Binding, patch []byte) (string, string, error) {
	name, err := materializeIndex(ctx, b, patch)
	if err != nil {
		return "", "", err
	}
	defer os.Remove(name)
	env := []string{"GIT_INDEX_FILE=" + name}
	tree, err := output(ctx, b.Root, env, "write-tree")
	if err != nil {
		return "", "", err
	}
	data, err := os.ReadFile(name)
	if err != nil {
		return "", "", recovery("cannot read preview index")
	}
	return tree, hash(data), nil
}

func materializeIndex(ctx context.Context, b Binding, patch []byte) (string, error) {
	f, err := os.CreateTemp("", "chora-index-*")
	if err != nil {
		return "", recovery("cannot prepare delivery index")
	}
	name := f.Name()
	f.Close()
	os.Remove(name)
	fail := func(e error) (string, error) { os.Remove(name); return "", e }
	env := []string{"GIT_INDEX_FILE=" + name}
	if _, err = outputBytes(ctx, b.Root, env, "read-tree", b.BaseCommit); err != nil {
		return fail(err)
	}
	if _, err = run(ctx, b.Root, env, patch, "apply", "--cached", "--binary", "--whitespace=nowarn", "-"); err != nil {
		return fail(conflict("reviewed patch cannot be applied to frozen base"))
	}
	return name, nil
}

func currentPatch(ctx context.Context, b Binding, want []string) ([]byte, []string, error) {
	trackedRaw, err := outputBytes(ctx, b.Root, nil, "-c", "core.quotePath=false", "diff", "--name-only", "-z", "--no-renames", b.BaseCommit, "--")
	if err != nil {
		return nil, nil, err
	}
	untrackedRaw, err := outputBytes(ctx, b.Root, nil, "ls-files", "--others", "--exclude-standard", "-z", "--")
	if err != nil {
		return nil, nil, err
	}
	tracked := splitNUL(trackedRaw)
	untracked := splitNUL(untrackedRaw)
	all := append(append([]string(nil), tracked...), untracked...)
	sort.Strings(all)
	if !sameStrings(all, want) {
		return nil, all, nil
	}
	var result bytes.Buffer
	if len(tracked) > 0 {
		args := []string{"-c", "core.quotePath=false", "diff", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", b.BaseCommit, "--"}
		for _, p := range tracked {
			args = append(args, ":(literal)"+p)
		}
		raw, e := outputBytes(ctx, b.Root, nil, args...)
		if e != nil {
			return nil, nil, e
		}
		result.Write(raw)
	}
	for _, p := range untracked {
		raw, e := diffNoIndex(ctx, b.Root, p)
		if e != nil {
			return nil, nil, e
		}
		result.Write(raw)
	}
	if result.Len() > domain.TaskPatchBytes {
		return nil, nil, unsupported("patch exceeds Task review policy")
	}
	return result.Bytes(), all, nil
}

func diffNoIndex(ctx context.Context, root, path string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/git", safeGitArgs("-c", "core.quotePath=false", "diff", "--no-index", "--full-index", "--no-ext-diff", "--no-textconv", "--", "/dev/null", path)...)
	cmd.Dir = root
	cmd.Env = gitEnv(nil)
	var out cappedBuffer
	out.limit = commandOutputLimit
	cmd.Stdout = &out
	cmd.Stderr = &cappedBuffer{limit: 64 << 10}
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, recovery("git command timed out")
	}
	if out.exceeded {
		return nil, unsupported("git output exceeds delivery limit")
	}
	if err == nil {
		return out.Bytes(), nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) && ee.ExitCode() == 1 {
		return out.Bytes(), nil
	}
	return nil, recovery("git command failed")
}
func splitNUL(raw []byte) []string {
	var out []string
	for _, v := range bytes.Split(raw, []byte{0}) {
		if len(v) > 0 {
			out = append(out, string(v))
		}
	}
	sort.Strings(out)
	return out
}

func exactPushURL(ctx context.Context, b Binding, remote string) (string, error) {
	urls, err := outputBytes(ctx, b.Root, nil, "remote", "get-url", "--push", "--all", remote)
	if err != nil {
		return "", unsupported("remote has no usable push URL")
	}
	lines := nonemptyLines(string(urls))
	if len(lines) != 1 {
		return "", unsupported("remote must have exactly one push URL")
	}
	u := lines[0]
	if unsafePushURL(u) {
		return "", unsupported("credential-bearing push URL")
	}
	keys := []string{"remote." + remote + ".mirror", "remote." + remote + ".push", "remote." + remote + ".pushoption", "remote." + remote + ".receivepack", "remote." + remote + ".vcs", "push.pushoption", "push.followtags", "push.recursesubmodules", "submodule.recursesubmodules"}
	for _, k := range keys {
		v, e := outputBytes(ctx, b.Root, nil, "config", "--get-all", k)
		if e == nil && len(bytes.TrimSpace(v)) > 0 {
			return "", unsupported("remote push configuration widens scope")
		}
	}
	return u, nil
}

var scpPushURL = regexp.MustCompile(`^[A-Za-z0-9._-]+@(?:[A-Za-z0-9._-]+|\[[0-9a-fA-F:]+\]):[^\s]+$`)

func unsafePushURL(s string) bool {
	if s == "" || strings.HasPrefix(s, "-") || strings.ContainsAny(s, "\x00\n\r\t?#") || (strings.Contains(s, "::") && !strings.Contains(s, "://") && !scpPushURL.MatchString(s)) {
		return true
	}
	// SCP-style SSH URLs have a username but no password/query or opaque helper.
	if scpPushURL.MatchString(s) {
		return false
	}
	u, err := url.Parse(s)
	if err != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return true
	}
	switch u.Scheme {
	case "":
		return strings.Contains(s, ":")
	case "file":
		return u.User != nil
	case "http", "https", "git":
		return u.User != nil || u.Host == ""
	case "ssh":
		if u.Host == "" {
			return true
		}
		if u.User != nil {
			_, hasPassword := u.User.Password()
			return hasPassword
		}
		return false
	default:
		return true
	}
}
func lsRemote(ctx context.Context, root, u, ref string) (string, error) {
	raw, err := outputBytes(ctx, root, nil, "ls-remote", "--refs", u, ref)
	if err != nil {
		return "", recovery("remote state cannot be read")
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) != 2 || fields[1] != ref {
		return "", recovery("remote response is ambiguous")
	}
	return fields[0], nil
}

func commitDigest(p CommitPreview) string {
	return hash([]byte(strings.Join([]string{bindingKey(p.Binding), p.Head, p.Tree, p.IndexDigest, p.Message, hash(p.Patch), strings.Join(p.Paths, "\x00")}, "\x1f")))
}
func pushDigest(p PushPreview) string {
	return hash([]byte(strings.Join([]string{bindingKey(p.Binding), p.Remote, p.URL, p.RemoteRef, p.Head, p.RemoteHead, p.TargetHead}, "\x1f")))
}
func bindingKey(b Binding) string {
	return strings.Join([]string{b.Root, b.CommonGitDir, b.BaseCommit, b.BaseTree, b.Branch, b.TargetRef}, "\x1e")
}
func hash(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func sameStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
func nonemptyLines(s string) []string {
	var r []string
	for _, v := range strings.Split(strings.TrimSpace(s), "\n") {
		if strings.TrimSpace(v) != "" {
			r = append(r, strings.TrimSpace(v))
		}
	}
	return r
}

func output(ctx context.Context, root string, env []string, args ...string) (string, error) {
	b, e := outputBytes(ctx, root, env, args...)
	return strings.TrimSpace(string(b)), e
}
func outputBytes(ctx context.Context, root string, env []string, args ...string) ([]byte, error) {
	return run(ctx, root, env, nil, args...)
}
func run(parent context.Context, root string, extra []string, input []byte, args ...string) ([]byte, error) {
	timeout := commandTimeout
	if len(args) > 0 && (args[0] == "ls-remote" || args[0] == "push") {
		// Private remotes may require a second connection for authentication.
		timeout = remoteCommandTimeout
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/usr/bin/git", safeGitArgs(args...)...)
	// Remote helpers can inherit the output pipes and outlive Git after a
	// timeout. Bound pipe draining so the delivery request can report recovery.
	cmd.WaitDelay = 250 * time.Millisecond
	cmd.Dir = root
	cmd.Stdin = bytes.NewReader(input)
	cmd.Env = gitEnv(extra)
	var out cappedBuffer
	out.limit = commandOutputLimit
	cmd.Stdout = &out
	cmd.Stderr = &cappedBuffer{limit: 64 << 10}
	err := cmd.Run()
	if ctx.Err() != nil {
		return nil, recovery("git command timed out")
	}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() == 1 {
			return nil, conflict("git state did not match")
		}
		return nil, recovery("git command failed")
	}
	if out.exceeded {
		return nil, unsupported("git output exceeds delivery limit")
	}
	return out.Bytes(), nil
}

type cappedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *cappedBuffer) Len() int {
	return b.buffer.Len()
}

func (b *cappedBuffer) Bytes() []byte {
	return b.buffer.Bytes()
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.limit - b.Len()
	if remaining > 0 {
		if remaining > n {
			remaining = n
		}
		_, _ = b.buffer.Write(p[:remaining])
	}
	if n > remaining {
		b.exceeded = true
	}
	return n, nil
}
func gitEnv(extra []string) []string {
	env := make([]string, 0, len(os.Environ())+8)
	for _, entry := range os.Environ() {
		key := entry
		if i := strings.IndexByte(key, '='); i >= 0 {
			key = key[:i]
		}
		if unsafeGitEnvironment(key) {
			continue
		}
		env = append(env, entry)
	}
	env = append(env, "GIT_TERMINAL_PROMPT=0", "GCM_INTERACTIVE=never", "GIT_PAGER=cat", "LC_ALL=C", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1", "GIT_ALLOW_PROTOCOL=file:ssh:https:http:git")
	return append(env, extra...)
}
func unsafeGitEnvironment(key string) bool {
	switch key {
	case "GIT_ALLOW_PROTOCOL", "GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE", "GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_CEILING_DIRECTORIES", "GIT_DISCOVERY_ACROSS_FILESYSTEM", "GIT_NAMESPACE", "GIT_SHALLOW_FILE", "GIT_GRAFT_FILE", "GIT_QUARANTINE_PATH", "GIT_EXEC_PATH", "GIT_PREFIX", "GIT_SUPER_PREFIX", "GIT_ATTR_SOURCE", "GIT_REPLACE_REF_BASE", "GIT_EXTERNAL_DIFF", "GIT_DIFF_OPTS", "GIT_LITERAL_PATHSPECS", "GIT_GLOB_PATHSPECS", "GIT_NOGLOB_PATHSPECS", "GIT_ICASE_PATHSPECS":
		return true
	}
	return strings.HasPrefix(key, "GIT_CONFIG_")
}
func safeGitArgs(args ...string) []string {
	return append([]string{"--no-replace-objects", "-c", "core.fsmonitor=false", "-c", "diff.external="}, args...)
}
func conflict(s string) error    { return fmt.Errorf("%w: %s", ErrConflict, s) }
func recovery(s string) error    { return fmt.Errorf("%w: %s", ErrRecovery, s) }
func unsupported(s string) error { return fmt.Errorf("%w: %s", ErrUnsupported, s) }

func reviewIndexUnconflicted(ctx context.Context, b Binding) error {
	unmerged, err := outputBytes(ctx, b.Root, nil, "ls-files", "-u")
	if err != nil {
		return err
	}
	if len(unmerged) > 0 {
		return conflict("Task index has unresolved conflicts")
	}
	return nil
}

// Staging is allowed only where its exact file content is part of the reviewed
// tree; an unrelated or intermediate index version is never discarded.
func reviewedStaging(ctx context.Context, b Binding, tree string, paths []string) error {
	staged, err := outputBytes(ctx, b.Root, nil, "diff", "--cached", "--name-only", "-z", b.BaseCommit, "--")
	if err != nil {
		return err
	}
	allowed := map[string]bool{}
	for _, p := range paths {
		allowed[p] = true
	}
	for _, p := range splitNUL(staged) {
		if !allowed[p] {
			return conflict("index contains unrelated staged changes")
		}
		diff, err := outputBytes(ctx, b.Root, nil, "diff", "--cached", "--no-ext-diff", "--no-textconv", tree, "--", ":(literal)"+p)
		if err != nil {
			return err
		}
		if len(diff) != 0 {
			return conflict("staged content differs from the reviewed tree")
		}
	}
	return nil
}
