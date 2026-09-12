// Package isolatedworkspace moves Task worktree bytes through an untrusted sandbox
// without giving the sandbox access to the original Git repositories.
package isolatedworkspace

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"unicode/utf8"

	"github.com/Yangyang96/chora/internal/domain"
)

const manifestVersion = "chora.isolated-workspace.v1"

type entry struct {
	Mode   uint32 `json:"mode"`
	Data   []byte `json:"data"`
	Digest string `json:"digest"`
}
type repoState struct {
	Resource domain.TaskRepositoryResource `json:"resource"`
	Source   map[string]entry              `json:"source"`
	Git      map[string]string             `json:"git"`
}
type manifest struct {
	Version      string      `json:"version"`
	Repositories []repoState `json:"repositories"`
}

// ReadonlyRelativeMountPaths returns the repository-local Git metadata and all
// reference repository paths which a supervisor must mount read-only.
func ReadonlyRelativeMountPaths(resources []domain.TaskRepositoryResource) []string {
	var paths []string
	for _, r := range resources {
		root := r.WorkspaceDirectory()
		paths = append(paths, filepath.Join(root, ".git"))
		if r.Role == "reference" {
			paths = append(paths, root)
		}
	}
	sort.Strings(paths)
	return paths
}

func Prepare(ctx context.Context, sourceRoot, destRoot, stateRoot string, resources []domain.TaskRepositoryResource) error {
	if err := validateRequest(sourceRoot, destRoot, stateRoot, resources); err != nil {
		return err
	}
	if err := emptyDirectory(destRoot); err != nil {
		return fmt.Errorf("destination is unsafe: %w", err)
	}
	if err := emptyDirectory(stateRoot); err != nil {
		return fmt.Errorf("state directory is unsafe: %w", err)
	}
	m := manifest{Version: manifestVersion}
	for _, r := range resources {
		if err := ctx.Err(); err != nil {
			return err
		}
		src := filepath.Join(sourceRoot, r.WorkspaceDirectory())
		dst := filepath.Join(destRoot, r.WorkspaceDirectory())
		if err := proveResource(ctx, src, r); err != nil {
			return fmt.Errorf("repository %s source is not proven: %w", r.RepoID, err)
		}
		files, err := scanTree(src, false)
		if err != nil {
			return fmt.Errorf("repository %s source: %w", r.RepoID, err)
		}
		if err := os.Mkdir(dst, 0700); err != nil {
			return err
		}
		if err := runGit(ctx, dst, "init", "--quiet"); err != nil {
			return err
		}
		if err := runGit(ctx, dst, "config", "core.logAllRefUpdates", "false"); err != nil {
			return err
		}
		if err := runGit(ctx, dst, "-c", "protocol.file.allow=always", "fetch", "--quiet", "--depth=1", "--no-tags", r.CommonGitDir, r.BaseCommit); err != nil {
			return fmt.Errorf("fetch frozen commit: %w", err)
		}
		if err := runGit(ctx, dst, "checkout", "--quiet", "--detach", r.BaseCommit); err != nil {
			return err
		}
		if err := os.Remove(filepath.Join(dst, ".git", "FETCH_HEAD")); err != nil && !os.IsNotExist(err) {
			return err
		}
		if err := replaceWorktree(dst, files); err != nil {
			return err
		}
		git, err := hashGit(filepath.Join(dst, ".git"))
		if err != nil {
			return err
		}
		m.Repositories = append(m.Repositories, repoState{Resource: r, Source: files, Git: git})
	}
	return writeJSONExclusive(filepath.Join(stateRoot, "manifest.json"), m)
}

func Collect(ctx context.Context, sourceRoot, destRoot, stateRoot string, resources []domain.TaskRepositoryResource) error {
	if err := validateRequest(sourceRoot, destRoot, stateRoot, resources); err != nil {
		return err
	}
	manifestPath := filepath.Join(stateRoot, "manifest.json")
	var m manifest
	if err := readJSONRegular(manifestPath, &m); err != nil || m.Version != manifestVersion || len(m.Repositories) != len(resources) {
		return errors.New("isolated workspace state is unavailable or invalid")
	}
	journal := filepath.Join(stateRoot, "import-journal.json")
	if _, err := os.Lstat(journal); err == nil {
		if err := rollback(sourceRoot, m); err != nil {
			return fmt.Errorf("unresolved previous import rollback: %w", err)
		}
		if err := os.Remove(journal); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	type planned struct {
		root  string
		files map[string]entry
	}
	plans := make([]planned, 0, len(resources))
	changed := 0
	total := 0
	for i, r := range resources {
		if err := ctx.Err(); err != nil {
			return err
		}
		rs := m.Repositories[i]
		if !sameResource(rs.Resource, r) {
			return errors.New("resource authority differs from prepared state")
		}
		src := filepath.Join(sourceRoot, r.WorkspaceDirectory())
		dst := filepath.Join(destRoot, r.WorkspaceDirectory())
		if err := proveResource(ctx, src, r); err != nil {
			return fmt.Errorf("repository %s source Git baseline drifted: %w", r.RepoID, err)
		}
		current, err := scanTree(src, false)
		if err != nil || !equalExactEntries(current, rs.Source) {
			return fmt.Errorf("repository %s source drifted since preparation", r.RepoID)
		}
		git, err := hashGit(filepath.Join(dst, ".git"))
		if err != nil || !equalStrings(git, rs.Git) {
			return fmt.Errorf("repository %s Git metadata was modified", r.RepoID)
		}
		result, err := scanTree(dst, false)
		if err != nil {
			return fmt.Errorf("repository %s result: %w", r.RepoID, err)
		}
		result = normalizeResultModes(rs.Source, result)
		paths := changedPaths(rs.Source, result)
		changed += len(paths)
		if changed > domain.TaskChangedEntryLimit {
			return errors.New("result changed too many paths")
		}
		if len(paths) > 0 && r.Role != "write" {
			return fmt.Errorf("reference repository %s was modified", r.RepoID)
		}
		for _, p := range paths {
			if !r.Scope.Allows(p) {
				return fmt.Errorf("repository %s changed out-of-scope path %q", r.RepoID, p)
			}
			if e, ok := result[p]; ok {
				total += len(e.Data)
				if !utf8.Valid(e.Data) || bytes.IndexByte(e.Data, 0) >= 0 {
					return fmt.Errorf("repository %s changed non-text file %q", r.RepoID, p)
				}
			}
		}
		if total > domain.TaskPatchBytes {
			return errors.New("result exceeds byte limit")
		}
		plans = append(plans, planned{src, result})
	}
	if err := writeJSONExclusive(journal, map[string]string{"state": "applying"}); err != nil {
		return err
	}
	for _, p := range plans {
		if err := replaceWorktree(p.root, p.files); err != nil {
			rb := rollback(sourceRoot, m)
			return errors.Join(err, func() error {
				if rb != nil {
					return fmt.Errorf("unresolved rollback: %w", rb)
				}
				return nil
			}())
		}
	}
	if err := os.Remove(journal); err != nil {
		return fmt.Errorf("import completed but journal cleanup failed: %w", err)
	}
	return nil
}

func validateRequest(source, dest, state string, resources []domain.TaskRepositoryResource) error {
	for _, p := range []string{source, dest, state} {
		if !filepath.IsAbs(p) || filepath.Clean(p) != p {
			return errors.New("workspace roots must be clean absolute paths")
		}
	}
	if source == dest || source == state || dest == state || len(resources) == 0 || len(resources) > domain.TaskRepositoryLimit {
		return errors.New("invalid isolated workspace request")
	}
	seen := map[string]bool{}
	for _, r := range resources {
		loc := r.WorkspaceDirectory()
		if !domain.ValidRepositoryWorkspaceLocator(r.RepoID, loc) || seen[loc] || (r.Role != "write" && r.Role != "reference") {
			return errors.New("invalid or duplicate repository resource")
		}
		seen[loc] = true
	}
	return nil
}

func proveResource(ctx context.Context, root string, r domain.TaskRepositoryResource) error {
	info, e := os.Lstat(root)
	if e != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe root")
	}
	checks := [][]string{{"rev-parse", "--path-format=absolute", "--git-common-dir"}, {"rev-parse", "HEAD^{commit}"}, {"rev-parse", "HEAD^{tree}"}}
	wants := []string{r.CommonGitDir, r.BaseCommit, r.BaseTree}
	for i, a := range checks {
		b, e := gitOutput(ctx, root, a...)
		if e != nil || strings.TrimSpace(string(b)) != wants[i] {
			return errors.New("Git baseline mismatch")
		}
	}
	return nil
}

func scanTree(root string, includeGit bool) (map[string]entry, error) {
	out := map[string]entry{}
	count := 0
	total := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, e := filepath.Rel(root, path)
		if e != nil {
			return e
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if !includeGit && (rel == ".git" || strings.HasPrefix(rel, ".git/")) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink %q is forbidden", rel)
		}
		if d.IsDir() {
			return nil
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("special file %q is forbidden", rel)
		}
		if info.Sys() != nil && hardlinkCount(info) > 1 {
			return fmt.Errorf("hard-linked file %q is forbidden", rel)
		}
		count++
		if count > domain.TaskChangedEntryLimit {
			return errors.New("too many files")
		}
		b, e := os.ReadFile(path)
		if e != nil {
			return e
		}
		total += len(b)
		if total > domain.TaskPatchBytes {
			return errors.New("tree exceeds byte limit")
		}
		h := sha256.Sum256(b)
		out[rel] = entry{Mode: uint32(info.Mode().Perm()), Data: b, Digest: hex.EncodeToString(h[:])}
		return nil
	})
	return out, err
}

func hardlinkCount(info fs.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Nlink)
	}
	return 1
}

func replaceWorktree(root string, files map[string]entry) error {
	current, e := scanTree(root, false)
	if e != nil {
		return e
	}
	for p := range current {
		if _, ok := files[p]; !ok {
			if e := os.Remove(filepath.Join(root, filepath.FromSlash(p))); e != nil {
				return e
			}
		}
	}
	for p, v := range files {
		target := filepath.Join(root, filepath.FromSlash(p))
		if e := os.MkdirAll(filepath.Dir(target), 0755); e != nil {
			return e
		}
		if e := os.WriteFile(target, v.Data, fs.FileMode(v.Mode)); e != nil {
			return e
		}
		if e := os.Chmod(target, fs.FileMode(v.Mode)); e != nil {
			return e
		}
	}
	return removeEmptyDirs(root)
}
func removeEmptyDirs(root string) error {
	var dirs []string
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e == nil && d.IsDir() && p != root && filepath.Base(p) != ".git" && !strings.Contains(filepath.ToSlash(p), "/.git/") {
			dirs = append(dirs, p)
		}
		return nil
	})
	sort.Slice(dirs, func(i, j int) bool { return len(dirs[i]) > len(dirs[j]) })
	for _, d := range dirs {
		_ = os.Remove(d)
	}
	return nil
}
func hashGit(root string) (map[string]string, error) {
	files, e := scanTree(root, true)
	if e != nil {
		return nil, e
	}
	out := map[string]string{}
	for p, v := range files {
		out[p] = fmt.Sprintf("%t:%s", executable(v.Mode), v.Digest)
	}
	return out, nil
}
func changedPaths(a, b map[string]entry) []string {
	set := map[string]bool{}
	for p, v := range a {
		if w, ok := b[p]; !ok || executable(v.Mode) != executable(w.Mode) || v.Digest != w.Digest {
			set[p] = true
		}
	}
	for p := range b {
		if _, ok := a[p]; !ok {
			set[p] = true
		}
	}
	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}
	sort.Strings(out)
	return out
}
func equalExactEntries(a, b map[string]entry) bool {
	if len(a) != len(b) {
		return false
	}
	for path, value := range a {
		other, ok := b[path]
		if !ok || value.Mode != other.Mode || value.Digest != other.Digest {
			return false
		}
	}
	return true
}
func executable(mode uint32) bool { return mode&0111 != 0 }
func normalizeResultModes(source, result map[string]entry) map[string]entry {
	for path, value := range result {
		if before, ok := source[path]; ok && executable(before.Mode) == executable(value.Mode) {
			value.Mode = before.Mode
		} else if executable(value.Mode) {
			value.Mode = 0755
		} else {
			value.Mode = 0644
		}
		result[path] = value
	}
	return result
}
func equalStrings(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
func sameResource(a, b domain.TaskRepositoryResource) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return bytes.Equal(x, y)
}
func rollback(source string, m manifest) error {
	var errs []error
	for _, r := range m.Repositories {
		if e := replaceWorktree(filepath.Join(source, r.Resource.WorkspaceDirectory()), r.Source); e != nil {
			errs = append(errs, e)
		}
	}
	return errors.Join(errs...)
}
func emptyDirectory(path string) error {
	info, e := os.Lstat(path)
	if e != nil {
		return e
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("not a directory")
	}
	ents, e := os.ReadDir(path)
	if e != nil {
		return e
	}
	if len(ents) != 0 {
		return errors.New("directory is not empty")
	}
	return nil
}
func writeJSONExclusive(path string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	return errors.Join(e, ce)
}
func readJSONRegular(path string, v any) error {
	info, e := os.Lstat(path)
	if e != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("unsafe state file")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	return d.Decode(v)
}
func gitOutput(ctx context.Context, root string, args ...string) ([]byte, error) {
	c := gitCommand(ctx, root, args...)
	return c.Output()
}
func runGit(ctx context.Context, root string, args ...string) error {
	c := gitCommand(ctx, root, args...)
	b, e := c.CombinedOutput()
	if e != nil {
		return fmt.Errorf("git %s: %w: %s", args[0], e, strings.TrimSpace(string(b)))
	}
	return nil
}
func gitCommand(ctx context.Context, root string, args ...string) *exec.Cmd {
	base := []string{"--no-replace-objects", "-c", "core.hooksPath=/dev/null", "-c", "core.fsmonitor=false", "-c", "gc.auto=0", "-C", root}
	c := exec.CommandContext(ctx, "/usr/bin/git", append(base, args...)...)
	env := []string{"HOME=/var/empty", "XDG_CONFIG_HOME=/var/empty", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=/usr/bin/false", "SSH_ASKPASS=/usr/bin/false", "GIT_OPTIONAL_LOCKS=0", "GIT_NO_LAZY_FETCH=1"}
	for _, x := range os.Environ() {
		k := strings.SplitN(x, "=", 2)[0]
		if k == "PATH" || k == "TMPDIR" || k == "LANG" || k == "LC_ALL" {
			env = append(env, x)
		}
	}
	c.Env = env
	return c
}
