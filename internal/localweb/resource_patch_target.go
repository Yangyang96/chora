package localweb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"os"
	"path/filepath"
	"sort"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
)

type resourcePatchTarget struct{}

func newResourcePatchTarget() app.ResourcePatchApplicationTarget { return &resourcePatchTarget{} }

func (target *resourcePatchTarget) Inspect(ctx context.Context, resource domain.TaskRepositoryResource, input app.PatchTargetRequest) (app.PatchTargetInspection, error) {
	inspection := app.PatchTargetInspection{TargetIdentity: resourcePatchTargetIdentity(resource)}
	if target == nil || !input.TaskID.Valid() {
		inspection.Reason = "The frozen Task Patch request is invalid."
		return inspection, fmt.Errorf("%w: invalid resource Patch request", app.ErrPatchTargetConflict)
	}
	review, err := app.BuildResourceReviewablePatch(resource, input.AffectedPaths, input.Raw, input.PatchDigest)
	if err != nil {
		inspection.Reason = "The Patch does not match this repository's frozen writable scope."
		return inspection, fmt.Errorf("%w: invalid resource Patch: %v", app.ErrPatchTargetConflict, err)
	}
	resolved, err := proveResourceApplicationTarget(ctx, resource)
	if err != nil {
		inspection.Reason = "The original repository no longer matches its frozen identity, base, or branch."
		return inspection, fmt.Errorf("%w: resource target authority drifted: %v", app.ErrPatchTargetConflict, err)
	}
	inspection.TargetIdentity = resolved.identity
	inspection.BaseRevision = resource.BaseCommit

	indexDigest, err := resourceGitIndexDigest(ctx, resource.Checkout, resource.CommonGitDir)
	if err != nil {
		inspection.Reason = "The original repository index cannot be proven."
		return inspection, fmt.Errorf("%w: resource target index is unavailable", app.ErrPatchTargetConflict)
	}
	indexAtBase, err := resourceAffectedIndexAtBase(ctx, resource.Checkout, resource.BaseCommit, input.AffectedPaths)
	if err != nil {
		inspection.Reason = "The affected repository index entries cannot be proven."
		return inspection, fmt.Errorf("%w: affected index state is unavailable", app.ErrPatchTargetConflict)
	}
	currentAffected, err := resourceFilesystemStateDigest(resource.Checkout, input.AffectedPaths)
	if err != nil {
		inspection.Reason = "An affected target path is unavailable, oversized, or unsafe."
		return inspection, fmt.Errorf("%w: affected target paths are unsafe", app.ErrPatchTargetConflict)
	}
	inspection.StateDigest = resourceCompositeStateDigest(resource, currentAffected, indexDigest)
	baseAffected, err := resourceBaseStateDigest(ctx, resource.Checkout, resource.BaseCommit, input.AffectedPaths)
	if err != nil {
		inspection.Reason = "The affected files cannot be proven against the frozen base."
		return inspection, fmt.Errorf("%w: frozen affected state is unavailable", app.ErrPatchTargetConflict)
	}
	baseState := resourceCompositeStateDigest(resource, baseAffected, indexDigest)

	if input.PriorPreStateDigest == ([32]byte{}) {
		if !indexAtBase || inspection.StateDigest != baseState {
			inspection.Reason = "An affected file or index entry differs from the frozen Task base."
			return inspection, fmt.Errorf("%w: affected target state differs from the frozen base", app.ErrPatchTargetConflict)
		}
		if err := resolved.target.applyCheck(ctx, input.Raw, false); err != nil {
			inspection.Reason = "The exact Patch conflicts with the frozen target contents."
			return inspection, fmt.Errorf("%w: exact Patch does not apply", app.ErrPatchTargetConflict)
		}
		inspection.Applicable = true
		return inspection, nil
	}

	// A durable writing marker may be reconciled only from the exact original
	// pre-state. Including the whole index in StateDigest detects staging changes
	// between the recorded intent and recovery.
	if !indexAtBase || input.PriorPreStateDigest != baseState {
		inspection.Reason = "The recorded pre-state no longer matches the frozen base and repository index."
		return inspection, fmt.Errorf("%w: prior resource pre-state cannot be proven", app.ErrPatchTargetRecovery)
	}
	if inspection.StateDigest == input.PriorPreStateDigest {
		if err := resolved.target.applyCheck(ctx, input.Raw, false); err != nil {
			inspection.Reason = "The prior write did not occur, but the exact Patch now conflicts."
			return inspection, fmt.Errorf("%w: exact pre-state rejects the Patch", app.ErrPatchTargetConflict)
		}
		inspection.Applicable = true
		return inspection, nil
	}
	expectedAffected, err := expectedResourceAffectedState(ctx, resource, review)
	if err != nil {
		inspection.Reason = "The exact expected post-state cannot be derived from the frozen base."
		return inspection, fmt.Errorf("%w: expected resource post-state is unavailable", app.ErrPatchTargetRecovery)
	}
	expectedPost := resourceCompositeStateDigest(resource, expectedAffected, indexDigest)
	if inspection.StateDigest != expectedPost || resolved.target.applyCheck(ctx, input.Raw, true) != nil {
		inspection.Reason = "The repository is neither the exact recorded pre-state nor the exact expected post-state."
		return inspection, fmt.Errorf("%w: prior resource write cannot be reconciled", app.ErrPatchTargetRecovery)
	}
	inspection.AlreadyApplied = true
	return inspection, nil
}

func (target *resourcePatchTarget) Apply(ctx context.Context, resource domain.TaskRepositoryResource, input app.PatchTargetRequest, inspected app.PatchTargetInspection) (app.PatchTargetEvidence, error) {
	current, err := target.Inspect(ctx, resource, input)
	if err != nil {
		if errors.Is(err, app.ErrPatchTargetRecovery) {
			return app.PatchTargetEvidence{}, err
		}
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: target changed after resource preflight", app.ErrPatchTargetConflict)
	}
	if !current.Applicable || current.AlreadyApplied || current.TargetIdentity != inspected.TargetIdentity ||
		current.BaseRevision != inspected.BaseRevision || current.StateDigest != inspected.StateDigest {
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: target changed after resource preflight", app.ErrPatchTargetConflict)
	}
	review, err := app.BuildResourceReviewablePatch(resource, input.AffectedPaths, input.Raw, input.PatchDigest)
	if err != nil {
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: resource Patch binding drifted", app.ErrPatchTargetConflict)
	}
	resolved, err := proveResourceApplicationTarget(ctx, resource)
	if err != nil || resolved.identity != inspected.TargetIdentity {
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: target authority changed before resource write", app.ErrPatchTargetConflict)
	}
	indexBefore, err := resourceGitIndexDigest(ctx, resource.Checkout, resource.CommonGitDir)
	if err != nil {
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: target index is unavailable before resource write", app.ErrPatchTargetConflict)
	}
	indexAtBase, err := resourceAffectedIndexAtBase(ctx, resource.Checkout, resource.BaseCommit, input.AffectedPaths)
	if err != nil || !indexAtBase {
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: affected index changed before resource write", app.ErrPatchTargetConflict)
	}
	affectedBefore, err := resourceFilesystemStateDigest(resource.Checkout, input.AffectedPaths)
	if err != nil || resourceCompositeStateDigest(resource, affectedBefore, indexBefore) != inspected.StateDigest {
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: affected target changed before resource write", app.ErrPatchTargetConflict)
	}
	expectedAffected, err := expectedResourceAffectedState(ctx, resource, review)
	if err != nil {
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: expected resource post-state cannot be proven", app.ErrPatchTargetConflict)
	}
	expectedPost := resourceCompositeStateDigest(resource, expectedAffected, indexBefore)

	if _, err := gitTargetOutput(ctx, resource.Checkout, input.Raw, "apply", "--whitespace=nowarn", "-"); err != nil {
		if resourceTargetStillAtState(ctx, resource, input.AffectedPaths, inspected.StateDigest, indexBefore) {
			return app.PatchTargetEvidence{}, fmt.Errorf("%w: target rejected the exact Patch without modification", app.ErrPatchTargetConflict)
		}
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: resource Patch write completion is uncertain", app.ErrPatchTargetRecovery)
	}

	postAuthority, authorityErr := proveResourceApplicationTarget(ctx, resource)
	indexAfter, indexErr := resourceGitIndexDigest(ctx, resource.Checkout, resource.CommonGitDir)
	indexAtBase, affectedIndexErr := resourceAffectedIndexAtBase(ctx, resource.Checkout, resource.BaseCommit, input.AffectedPaths)
	affectedAfter, affectedErr := resourceFilesystemStateDigest(resource.Checkout, input.AffectedPaths)
	postState := resourceCompositeStateDigest(resource, affectedAfter, indexAfter)
	if authorityErr != nil || postAuthority.identity != inspected.TargetIdentity || indexErr != nil || indexAfter != indexBefore ||
		affectedIndexErr != nil || !indexAtBase || affectedErr != nil || postState != expectedPost || postState == inspected.StateDigest ||
		postAuthority.target.applyCheck(ctx, input.Raw, true) != nil {
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: exact resource Patch post-state is not provable", app.ErrPatchTargetRecovery)
	}
	return app.PatchTargetEvidence{PostStateDigest: postState}, nil
}

type provenResourceApplicationTarget struct {
	target   *gitPatchTarget
	identity string
}

func proveResourceApplicationTarget(ctx context.Context, resource domain.TaskRepositoryResource) (provenResourceApplicationTarget, error) {
	inspection, err := (gitsource.Default{}).Inspect(ctx, resource.Checkout)
	if err != nil || inspection.CanonicalPath != resource.Checkout || inspection.CommonGitDir != resource.CommonGitDir ||
		inspection.PhysicalIdentity != resource.PhysicalIdentity || inspection.HeadCommit != resource.BaseCommit || inspection.RootTree != resource.BaseTree {
		return provenResourceApplicationTarget{}, errors.New("repository identity or frozen base differs")
	}
	ref, err := currentTaskRef(ctx, resource.Checkout)
	if err != nil || ref != resource.BaseRef {
		return provenResourceApplicationTarget{}, errors.New("repository branch differs from frozen base")
	}
	narrow, err := newGitPatchTarget(resource.Checkout, nil)
	if err != nil {
		return provenResourceApplicationTarget{}, err
	}
	identity := resourcePatchTargetIdentity(resource)
	narrow.identity = identity
	return provenResourceApplicationTarget{target: narrow, identity: identity}, nil
}

func resourcePatchTargetIdentity(resource domain.TaskRepositoryResource) string {
	digest := sha256.Sum256([]byte("chora.resource-patch-target.v1\x00" + resource.PhysicalIdentity + "\x00" + resource.BaseCommit + "\x00" + resource.BaseTree + "\x00" + resource.BaseRef + "\x00" + resource.Checkout + "\x00" + resource.CommonGitDir))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func resourceTargetStillAtState(ctx context.Context, resource domain.TaskRepositoryResource, paths []string, expected, expectedIndex [32]byte) bool {
	resolved, err := proveResourceApplicationTarget(ctx, resource)
	if err != nil || resolved.identity != resourcePatchTargetIdentity(resource) {
		return false
	}
	indexDigest, err := resourceGitIndexDigest(ctx, resource.Checkout, resource.CommonGitDir)
	if err != nil || indexDigest != expectedIndex {
		return false
	}
	indexAtBase, err := resourceAffectedIndexAtBase(ctx, resource.Checkout, resource.BaseCommit, paths)
	if err != nil || !indexAtBase {
		return false
	}
	affected, err := resourceFilesystemStateDigest(resource.Checkout, paths)
	return err == nil && resourceCompositeStateDigest(resource, affected, indexDigest) == expected
}

func resourceAffectedIndexAtBase(ctx context.Context, root, base string, paths []string) (bool, error) {
	if len(paths) == 0 {
		return false, errors.New("no affected paths")
	}
	for start := 0; start < len(paths); {
		arguments := []string{"diff", "--cached", "--quiet", "--no-ext-diff", base, "--"}
		argumentBytes := 0
		end := start
		for end < len(paths) && end-start < 512 {
			relative := paths[end]
			if !safePatchTargetPath(relative) {
				return false, errors.New("unsafe affected path")
			}
			if end > start && argumentBytes+len(relative) > 64*1024 {
				break
			}
			arguments = append(arguments, ":(literal)"+relative)
			argumentBytes += len(relative)
			end++
		}
		atBase, err := resourceAffectedIndexBatchAtBase(ctx, root, arguments)
		if err != nil || !atBase {
			return atBase, err
		}
		start = end
	}
	return true, nil
}

func resourceAffectedIndexBatchAtBase(ctx context.Context, root string, arguments []string) (bool, error) {
	command := isolatedGitCommand(ctx, root, arguments...)
	command.Stdout, command.Stderr = io.Discard, io.Discard
	err := command.Run()
	if err == nil {
		return true, nil
	}
	if ctx.Err() != nil {
		return false, ctx.Err()
	}
	var status interface{ ExitCode() int }
	if errors.As(err, &status) && status.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}

func resourceGitIndexDigest(ctx context.Context, root, commonGitDir string) ([32]byte, error) {
	common, err := taskWorktreeCommonDirectory(ctx, root)
	if err != nil || common != commonGitDir {
		return [32]byte{}, errors.New("Git index belongs to a different repository")
	}
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := isolatedGitCommand(commandCtx, root, "ls-files", "--stage", "-v", "-z", "--")
	stdout, err := command.StdoutPipe()
	if err != nil {
		return [32]byte{}, err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return [32]byte{}, err
	}
	h := sha256.New()
	_, _ = h.Write([]byte("chora.resource-index.v2\x00"))
	if _, err := io.Copy(h, stdout); err != nil {
		cancel()
		_ = command.Wait()
		return [32]byte{}, err
	}
	if err := command.Wait(); err != nil {
		return [32]byte{}, err
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest, nil
}

func resourceFilesystemStateDigest(root string, paths []string) ([32]byte, error) {
	values := append([]string(nil), paths...)
	sort.Strings(values)
	h := sha256.New()
	_, _ = h.Write([]byte("chora.resource-affected-state.v1\x00"))
	for _, relative := range values {
		mode, data, exists, err := workingTreeFile(root, relative)
		if err != nil {
			return [32]byte{}, err
		}
		writeResourceFileState(h, relative, mode, data, exists)
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest, nil
}

func resourceBaseStateDigest(ctx context.Context, root, base string, paths []string) ([32]byte, error) {
	values := append([]string(nil), paths...)
	sort.Strings(values)
	h := sha256.New()
	_, _ = h.Write([]byte("chora.resource-affected-state.v1\x00"))
	for _, relative := range values {
		mode, data, exists, err := gitBaseFile(ctx, root, base, relative)
		if err != nil {
			return [32]byte{}, err
		}
		writeResourceFileState(h, relative, mode, data, exists)
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest, nil
}

func writeResourceFileState(h hash.Hash, relative, mode string, data []byte, exists bool) {
	if !exists {
		fmt.Fprintf(h, "%s\x00missing\x00", relative)
		return
	}
	fmt.Fprintf(h, "%s\x00%s\x00%d\x00", relative, mode, len(data))
	_, _ = h.Write(data)
	_, _ = h.Write([]byte{0})
}

func resourceCompositeStateDigest(resource domain.TaskRepositoryResource, affected, index [32]byte) [32]byte {
	h := sha256.New()
	_, _ = h.Write([]byte("chora.resource-target-state.v1\x00" + resource.PhysicalIdentity + "\x00" + resource.BaseCommit + "\x00" + resource.BaseTree + "\x00" + resource.BaseRef + "\x00"))
	_, _ = h.Write(affected[:])
	_, _ = h.Write(index[:])
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest
}

func expectedResourceAffectedState(ctx context.Context, resource domain.TaskRepositoryResource, review app.ReviewablePatch) ([32]byte, error) {
	sections, err := resourcePatchSections(review.Raw, review.Files)
	if err != nil {
		return [32]byte{}, err
	}
	paths := make([]string, 0, len(review.Files))
	for _, file := range review.Files {
		paths = append(paths, file.Path)
	}
	sort.Strings(paths)
	h := sha256.New()
	_, _ = h.Write([]byte("chora.resource-affected-state.v1\x00"))
	for _, relative := range paths {
		temporary, err := os.MkdirTemp("", "chora-resource-post-state-*")
		if err != nil {
			return [32]byte{}, err
		}
		proofErr := func() error {
			path := filepath.Join(temporary, filepath.FromSlash(relative))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return err
			}
			mode, data, exists, err := gitBaseFile(ctx, resource.Checkout, resource.BaseCommit, relative)
			if err != nil {
				return err
			}
			if exists {
				if err := os.WriteFile(path, data, 0o600); err != nil {
					return err
				}
				permissions := os.FileMode(0o644)
				if mode == "100755" {
					permissions = 0o755
				}
				if err := os.Chmod(path, permissions); err != nil {
					return err
				}
			}
			if _, err := gitTargetOutput(ctx, temporary, sections[relative], "apply", "--whitespace=nowarn", "-"); err != nil {
				return err
			}
			postMode, postData, postExists, err := workingTreeFile(temporary, relative)
			if err != nil {
				return err
			}
			writeResourceFileState(h, relative, postMode, postData, postExists)
			return nil
		}()
		cleanupErr := os.RemoveAll(temporary)
		if proofErr != nil {
			return [32]byte{}, proofErr
		}
		if cleanupErr != nil {
			return [32]byte{}, cleanupErr
		}
	}
	var digest [32]byte
	copy(digest[:], h.Sum(nil))
	return digest, nil
}

func resourcePatchSections(raw []byte, files []app.ReviewablePatchFile) (map[string][]byte, error) {
	if !bytes.HasPrefix(raw, []byte("diff --git a/")) {
		return nil, errors.New("Patch does not begin with a file section")
	}
	starts := []int{0}
	for offset := 1; offset < len(raw); {
		index := bytes.Index(raw[offset:], []byte("\ndiff --git a/"))
		if index < 0 {
			break
		}
		start := offset + index + 1
		starts = append(starts, start)
		offset = start + 1
	}
	if len(starts) != len(files) {
		return nil, errors.New("Patch section count differs from review projection")
	}
	sections := make(map[string][]byte, len(files))
	for index, file := range files {
		end := len(raw)
		if index+1 < len(starts) {
			end = starts[index+1]
		}
		sections[file.Path] = raw[starts[index]:end]
	}
	return sections, nil
}

var _ app.ResourcePatchApplicationTarget = (*resourcePatchTarget)(nil)
