package localweb

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Yangyang96/chora/internal/app"
	"github.com/Yangyang96/chora/internal/domain"
)

// currentTaskBase snapshots the checked-out committed base. Reading the ref and
// HEAD twice detects changes during selection; no dirty files are copied.
func currentTaskBase(ctx context.Context, root string) (revision, tree, ref string, err error) {
	ref, err = currentTaskRef(ctx, root)
	if err != nil {
		return
	}
	raw, readErr := gitTargetOutput(ctx, root, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if readErr != nil {
		err = errors.New("Project has no available committed HEAD; restore the repository or commit externally before creating a Task")
		return
	}
	revision = strings.TrimSpace(string(raw))
	raw, err = gitTargetOutput(ctx, root, nil, "rev-parse", "--verify", revision+"^{tree}")
	if err != nil {
		return
	}
	tree = strings.TrimSpace(string(raw))
	lastRef, checkErr := currentTaskRef(ctx, root)
	lastHead, headErr := gitTargetOutput(ctx, root, nil, "rev-parse", "--verify", "HEAD^{commit}")
	if checkErr != nil || headErr != nil || lastRef != ref || strings.TrimSpace(string(lastHead)) != revision {
		err = fmt.Errorf("%w: Project base changed during Task creation; retry from the selected branch", app.ErrInvalidCommand)
	}
	return
}

func currentTaskRef(ctx context.Context, root string) (string, error) {
	raw, err := gitTargetOutput(ctx, root, nil, "symbolic-ref", "--quiet", "HEAD")
	if err == nil {
		return strings.TrimSpace(string(raw)), nil
	}
	// Detached HEAD is explicit, not a guessed branch name.
	if _, err = gitTargetOutput(ctx, root, nil, "rev-parse", "--verify", "HEAD^{commit}"); err != nil {
		return "", err
	}
	return "HEAD", nil
}

// repositoryForTaskBase is an in-memory projection, never a refreshed Room
// binding. Historical target identities remain byte-identical for legacy tasks.
func repositoryForTaskBase(ctx context.Context, project domain.RepositoryBinding, base domain.TaskWorktreeBinding) (domain.RepositoryBinding, error) {
	if project.State() != domain.RepositoryBindingStateActive {
		return domain.RepositoryBinding{}, errors.New("Project is removed; restore the Project before continuing")
	}
	raw, err := gitTargetOutput(ctx, project.LocalLocator(), nil, "rev-parse", "--verify", base.PinnedBaseRevision()+"^{tree}")
	if err != nil {
		return domain.RepositoryBinding{}, errors.New("Task base is unavailable; restore the original repository and its committed history")
	}
	tree := strings.TrimSpace(string(raw))
	identity := "sha:" + base.PinnedBaseRevision() + ":tree:" + tree
	if base.StartPolicy() == domain.TaskStartPolicyLegacy {
		if base.PinnedBaseRevision() != project.AdmittedBase() || identity != project.BaseIdentity() {
			return domain.RepositoryBinding{}, errors.New("legacy Task base does not match its original repository authority; restore the original history")
		}
	} else if base.StartPolicy() != domain.TaskStartPolicyCurrentHEAD || tree != base.PinnedBaseTree() {
		return domain.RepositoryBinding{}, errors.New("Task commit/tree authority is inconsistent; restore the original history")
	}
	return domain.RestoreRepositoryBinding(domain.RepositoryBindingRecord{
		RoomID: project.RoomID(), Name: project.Name(), LocalLocator: project.LocalLocator(), SourceKind: project.SourceKind(), CloneURL: project.CloneURL(),
		AdmittedBase: base.PinnedBaseRevision(), BaseIdentity: identity, TargetWorktree: project.TargetWorktree(), DirtyAdmitted: project.DirtyAdmitted(),
		State: project.State(), Version: project.Version(), CreatedAt: project.CreatedAt(), UpdatedAt: project.UpdatedAt(),
	})
}
