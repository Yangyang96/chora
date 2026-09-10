package taskdelivery

import (
	"context"
	"fmt"
	"path"
	"sort"
	"strings"
	"unicode/utf8"
)

// DraftRepositoryContext is read from immutable Git objects. Repository text is
// context, never executable configuration or authority to run tools.
type DraftRepositoryContext struct {
	Head          string            `json:"head"`
	Base          string            `json:"base"`
	MergeBase     string            `json:"mergeBase,omitempty"`
	Diff          string            `json:"diff,omitempty"`
	Conventions   map[string]string `json:"conventions"`
	Templates     map[string]string `json:"templates"`
	RecentCommits string            `json:"recentCommits"`
	Warnings      []string          `json:"warnings"`
}

// ReadDraftContext never stages, fetches, executes repository config or writes
// refs. PR text is based on the whole merge-base..head range, after proving its
// content is exactly the accepted review tree and its base has no extra changes.
func (Git) ReadDraftContext(ctx context.Context, b Binding, head, base, reviewedTree string) (DraftRepositoryContext, error) {
	v := DraftRepositoryContext{Conventions: map[string]string{}, Templates: map[string]string{}, Warnings: []string{}}
	if err := validateBinding(ctx, b, head == "", false); err != nil {
		return v, err
	}
	v.Head, v.Base = b.BaseCommit, b.BaseCommit
	if head != "" {
		if !isOID(head) || !isOID(base) || !isOID(reviewedTree) {
			return v, conflict("invalid pull request draft range")
		}
		live, err := output(ctx, b.Root, nil, "rev-parse", "HEAD")
		if err != nil || live != head {
			return v, conflict("pull request head changed; refresh delivery")
		}
		mergeBase, err := output(ctx, b.Root, nil, "merge-base", base, head)
		if err != nil {
			return v, conflict("pull request comparison is unavailable locally; refresh delivery")
		}
		baseTree, err := output(ctx, b.Root, nil, "rev-parse", mergeBase+"^{tree}")
		if err != nil || baseTree != b.BaseTree {
			return v, conflict("pull request includes changes outside this Review; complete a new Review")
		}
		tree, err := output(ctx, b.Root, nil, "rev-parse", head+"^{tree}")
		if err != nil || tree != reviewedTree {
			return v, conflict("pull request tree differs from the reviewed commit")
		}
		diff, err := outputBytes(ctx, b.Root, nil, "-c", "core.quotePath=false", "diff", "--binary", "--full-index", "--no-ext-diff", "--no-textconv", "--no-renames", mergeBase, head, "--")
		if err != nil {
			return v, err
		}
		v.Head, v.Base, v.MergeBase, v.Diff = head, base, mergeBase, string(diff)
	}
	// Ask Git only for known convention paths, never recursively enumerate the
	// entire repository or follow worktree symlinks to files outside this Repo.
	paths := []string{"AGENTS.md", "CONTRIBUTING.md", "CONTRIBUTING.rst", "CONTRIBUTING", ".github/CONTRIBUTING.md", "docs/CONTRIBUTING.md", "commitlint.config.js", "commitlint.config.cjs", "commitlint.config.mjs", "commitlint.config.ts", ".commitlintrc", ".commitlintrc.json", ".commitlintrc.yml", ".commitlintrc.yaml", "package.json", "PULL_REQUEST_TEMPLATE.md", "pull_request_template.md", ".github/PULL_REQUEST_TEMPLATE.md", ".github/pull_request_template.md", "docs/PULL_REQUEST_TEMPLATE.md", "docs/pull_request_template.md", ".github/PULL_REQUEST_TEMPLATE", ".github/pull_request_template", "PULL_REQUEST_TEMPLATE", "docs/PULL_REQUEST_TEMPLATE"}
	args := append([]string{"ls-tree", "-r", "-z", v.Base, "--"}, paths...)
	raw, err := outputBytes(ctx, b.Root, nil, args...)
	if err != nil {
		return v, err
	}
	entries := strings.Split(string(raw), "\x00")
	sort.Strings(entries)
	total := 0
	for _, entry := range entries {
		if entry == "" {
			continue
		}
		metadata, name, ok := strings.Cut(entry, "\t")
		fields := strings.Fields(metadata)
		if !ok || len(fields) != 3 || fields[1] != "blob" || (fields[0] != "100644" && fields[0] != "100755") {
			v.Warnings = append(v.Warnings, "Skipped non-regular repository convention: "+name)
			continue
		}
		isTemplate := strings.Contains(strings.ToLower(name), "pull_request_template")
		if isTemplate && !strings.EqualFold(path.Ext(name), ".md") {
			continue
		}
		if total >= 64<<10 || len(v.Conventions)+len(v.Templates) >= 24 {
			v.Warnings = append(v.Warnings, "Repository convention limit reached; some rules or templates were not read.")
			break
		}
		size, err := output(ctx, b.Root, nil, "cat-file", "-s", fields[2])
		if err != nil {
			return v, err
		}
		var count int
		if _, err := fmt.Sscan(size, &count); err != nil || count > 16<<10 || total+count > 64<<10 {
			v.Warnings = append(v.Warnings, "Repository convention is too large: "+name)
			continue
		}
		content, err := outputBytes(ctx, b.Root, nil, "cat-file", "blob", fields[2])
		if err != nil {
			return v, err
		}
		if !utf8.Valid(content) || strings.ContainsRune(string(content), 0) {
			v.Warnings = append(v.Warnings, "Skipped non-text repository convention: "+name)
			continue
		}
		total += len(content)
		if isTemplate {
			v.Templates[name] = string(content)
		} else {
			v.Conventions[name] = string(content)
		}
	}
	v.RecentCommits, err = output(ctx, b.Root, nil, "log", "-6", "--no-merges", "--format=%s", v.Head, "--")
	if err != nil {
		return v, err
	}
	if len(v.RecentCommits) > 8192 {
		v.RecentCommits = ""
		v.Warnings = append(v.Warnings, "Recent commit subjects exceed the context limit.")
	}
	if err := validateBinding(ctx, b, head == "", false); err != nil {
		return v, err
	}
	current, err := output(ctx, b.Root, nil, "rev-parse", "HEAD")
	if err != nil || current != v.Head {
		return v, conflict("repository changed while preparing the draft")
	}
	return v, nil
}
