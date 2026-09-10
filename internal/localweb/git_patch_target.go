package localweb

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/speccoding"
)

type gitPatchTarget struct {
	root     string
	identity string
}

const gitPatchTargetFileByteLimit = 8 * 1024 * 1024

func newProductGitPatchTarget(root string, envelope speccoding.InstalledEnvelope) (*gitPatchTarget, error) {
	return newGitPatchTarget(root, envelope.ValidateApplyTarget)
}

func newGitPatchTarget(root string, validate func(string) error) (*gitPatchTarget, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return nil, errors.New("Patch Apply target must be an absolute clean path")
	}
	if validate != nil {
		if err := validate(root); err != nil {
			return nil, errors.New("Patch Apply target does not match the installed Chora repository authority")
		}
	}
	top, err := gitTargetOutput(context.Background(), root, nil, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(strings.TrimSpace(string(top))) != root {
		return nil, errors.New("Patch Apply target must be the exact root of a Git working tree")
	}
	digest := sha256.Sum256([]byte("chora.patch-target.v1\x00" + root))
	return &gitPatchTarget{root: root, identity: "sha256:" + hex.EncodeToString(digest[:])}, nil
}

func (target *gitPatchTarget) Inspect(ctx context.Context, request app.PatchTargetRequest) (app.PatchTargetInspection, error) {
	if target == nil || target.root == "" || request.PatchDigest == ([32]byte{}) || sha256.Sum256(request.Raw) != request.PatchDigest || len(request.AffectedPaths) == 0 {
		return app.PatchTargetInspection{}, app.ErrPatchTargetConflict
	}
	inspection := app.PatchTargetInspection{TargetIdentity: target.identity}
	stateDigest, err := target.stateDigest(request.AffectedPaths)
	if err != nil {
		inspection.Reason = "The target files are unavailable or unsafe."
		return inspection, fmt.Errorf("%w: target files are unavailable or unsafe", app.ErrPatchTargetConflict)
	}
	inspection.StateDigest = stateDigest
	head, err := gitTargetOutput(ctx, target.root, nil, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(string(head)) == "" {
		inspection.Reason = "The target Git identity is unavailable."
		return inspection, fmt.Errorf("%w: target Git identity is unavailable", app.ErrPatchTargetConflict)
	}
	inspection.BaseRevision = strings.TrimSpace(string(head))
	if request.PriorPreStateDigest != ([32]byte{}) && request.PriorPreStateDigest != stateDigest {
		if err := target.applyCheck(ctx, request.Raw, true); err == nil {
			inspection.AlreadyApplied = true
			return inspection, nil
		}
	}
	if err := target.applyCheck(ctx, request.Raw, false); err == nil {
		inspection.Applicable = true
		return inspection, nil
	}
	inspection.Reason = "The target working tree has conflicting content for this exact Patch."
	return inspection, nil
}

func (target *gitPatchTarget) Apply(ctx context.Context, request app.PatchTargetRequest, inspected app.PatchTargetInspection) (app.PatchTargetEvidence, error) {
	current, err := target.Inspect(ctx, app.PatchTargetRequest{Raw: request.Raw, PatchDigest: request.PatchDigest, AffectedPaths: request.AffectedPaths})
	if err != nil || !current.Applicable || current.TargetIdentity != inspected.TargetIdentity || current.BaseRevision != inspected.BaseRevision || current.StateDigest != inspected.StateDigest {
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: target changed after application inspection", app.ErrPatchTargetConflict)
	}
	indexBefore, err := target.indexDigest(ctx)
	if err != nil {
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: target Git index is unavailable", app.ErrPatchTargetConflict)
	}
	if _, err := gitTargetOutput(ctx, target.root, request.Raw, "apply", "--whitespace=nowarn", "-"); err != nil {
		afterFailure, digestErr := target.stateDigest(request.AffectedPaths)
		if digestErr == nil && afterFailure == inspected.StateDigest {
			return app.PatchTargetEvidence{}, fmt.Errorf("%w: target rejected the exact Patch without modification", app.ErrPatchTargetConflict)
		}
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: Patch write completion is uncertain", app.ErrPatchTargetRecovery)
	}
	post, err := target.stateDigest(request.AffectedPaths)
	if err != nil || post == inspected.StateDigest {
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: Patch post-state is not provable", app.ErrPatchTargetRecovery)
	}
	indexAfter, err := target.indexDigest(ctx)
	if err != nil || indexAfter != indexBefore {
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: Git index changed during Patch application", app.ErrPatchTargetRecovery)
	}
	if err := target.applyCheck(ctx, request.Raw, true); err != nil {
		return app.PatchTargetEvidence{}, fmt.Errorf("%w: applied Patch cannot be verified in the target", app.ErrPatchTargetRecovery)
	}
	return app.PatchTargetEvidence{PostStateDigest: post}, nil
}

func (target *gitPatchTarget) applyCheck(ctx context.Context, patch []byte, reverse bool) error {
	arguments := []string{"apply", "--check", "--whitespace=nowarn"}
	if reverse {
		arguments = append(arguments, "--reverse")
	}
	arguments = append(arguments, "-")
	_, err := gitTargetOutput(ctx, target.root, patch, arguments...)
	return err
}

func (target *gitPatchTarget) indexDigest(ctx context.Context) ([32]byte, error) {
	data, err := gitTargetOutput(ctx, target.root, nil, "diff", "--cached", "--binary", "--no-ext-diff", "--")
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(data), nil
}

func (target *gitPatchTarget) stateDigest(paths []string) ([32]byte, error) {
	values := append([]string(nil), paths...)
	sort.Strings(values)
	if len(values) == 0 {
		return [32]byte{}, errors.New("no affected paths")
	}
	hash := sha256.New()
	seen := make(map[string]struct{}, len(values))
	for _, relative := range values {
		if !safePatchTargetPath(relative) {
			return [32]byte{}, errors.New("unsafe affected path")
		}
		if _, duplicate := seen[relative]; duplicate {
			return [32]byte{}, errors.New("duplicate affected path")
		}
		seen[relative] = struct{}{}
		absolute := target.root
		parts := strings.Split(relative, "/")
		missing := false
		var leafInfo fs.FileInfo
		for index, part := range parts {
			absolute = filepath.Join(absolute, part)
			info, err := os.Lstat(absolute)
			if errors.Is(err, os.ErrNotExist) {
				fmt.Fprintf(hash, "%s\x00missing\x00%d\x00", relative, index)
				missing = true
				break
			}
			if err != nil {
				return [32]byte{}, err
			}
			if info.Mode()&fs.ModeSymlink != 0 || (index < len(parts)-1 && !info.IsDir()) || (index == len(parts)-1 && !info.Mode().IsRegular()) {
				return [32]byte{}, errors.New("unsafe target file")
			}
			if index == len(parts)-1 {
				leafInfo = info
			}
		}
		if missing {
			continue
		}
		if leafInfo == nil || leafInfo.Size() < 0 || leafInfo.Size() > gitPatchTargetFileByteLimit {
			return [32]byte{}, errors.New("target file exceeds the supported review size")
		}
		file, err := os.Open(absolute)
		if err != nil {
			return [32]byte{}, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, gitPatchTargetFileByteLimit+1))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil || len(data) > gitPatchTargetFileByteLimit {
			return [32]byte{}, errors.New("target file exceeds the supported review size")
		}
		fmt.Fprintf(hash, "%s\x00%d\x00%d\x00", relative, len(data), fileMode(absolute))
		_, _ = hash.Write(data)
		_, _ = hash.Write([]byte{0})
	}
	var result [32]byte
	copy(result[:], hash.Sum(nil))
	return result, nil
}

func fileMode(path string) uint32 {
	info, err := os.Lstat(path)
	if err != nil {
		return 0
	}
	return uint32(info.Mode().Perm())
}

func safePatchTargetPath(value string) bool {
	if value == "" || strings.HasPrefix(value, "/") || strings.Contains(value, "\\") || strings.ContainsAny(value, "\x00\r\n\t") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." || part == ".git" {
			return false
		}
	}
	return true
}

func gitTargetOutput(ctx context.Context, root string, stdin []byte, arguments ...string) ([]byte, error) {
	command := isolatedGitCommand(ctx, root, arguments...)
	if stdin != nil {
		command.Stdin = bytes.NewReader(stdin)
	}
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &bytes.Buffer{}
	if err := command.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

var _ app.PatchApplicationTarget = (*gitPatchTarget)(nil)
