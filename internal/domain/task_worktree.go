package domain

import (
	"fmt"
	"strings"
	"time"
)

type TaskWorktreeState string

const (
	TaskWorktreeProvisioning     TaskWorktreeState = "provisioning"
	TaskWorktreeReady            TaskWorktreeState = "ready"
	TaskWorktreeRecoveryRequired TaskWorktreeState = "recovery_required"
)

const (
	TaskStartPolicyCurrentHEAD = "current_committed_head"
	TaskStartPolicyLegacy      = "legacy_admitted_base"
)

type TaskWorktreeBindingParams struct {
	TaskID                    string
	RepositoryIdentity        string
	PinnedBaseRevision        string
	PinnedBaseTree            string
	BaseRef                   string
	StartPolicy               string
	RelativeLocator           string
	ConfiguredRootFingerprint [32]byte
	State                     TaskWorktreeState
	Version                   uint64
	Reason                    string
	CreatedAt                 time.Time
	UpdatedAt                 time.Time
	ReadyAt                   time.Time
}

// TaskWorktreeBinding is the durable, host-path-free identity of one Task's
// managed linked worktree. Its identity fields never change after creation.
type TaskWorktreeBinding struct{ params TaskWorktreeBindingParams }

func NewTaskWorktreeBinding(params TaskWorktreeBindingParams) (TaskWorktreeBinding, error) {
	params.State = TaskWorktreeProvisioning
	params.Version = 1
	params.Reason = ""
	params.UpdatedAt = params.CreatedAt
	params.ReadyAt = time.Time{}
	return restoreTaskWorktreeBinding(params)
}

func RestoreTaskWorktreeBinding(params TaskWorktreeBindingParams) (TaskWorktreeBinding, error) {
	return restoreTaskWorktreeBinding(params)
}

func restoreTaskWorktreeBinding(params TaskWorktreeBindingParams) (TaskWorktreeBinding, error) {
	if params.PinnedBaseTree == "" && params.BaseRef == "" && params.StartPolicy == "" {
		params.StartPolicy = TaskStartPolicyLegacy
	}
	taskID, err := ParseTaskID(params.TaskID)
	if err != nil || !validWorktreeRepositoryIdentity(params.RepositoryIdentity) ||
		!validPinnedRevision(params.PinnedBaseRevision) ||
		!validTaskStartAuthority(params.PinnedBaseTree, params.BaseRef, params.StartPolicy) ||
		!validTaskWorktreeLocator(params.RelativeLocator, params.RepositoryIdentity, taskID) ||
		params.ConfiguredRootFingerprint == ([32]byte{}) || params.Version == 0 ||
		params.CreatedAt.IsZero() || params.UpdatedAt.Before(params.CreatedAt) {
		return TaskWorktreeBinding{}, fmt.Errorf("%w: invalid Task worktree binding", ErrInvalidArgument)
	}
	params.Reason = strings.TrimSpace(params.Reason)
	switch params.State {
	case TaskWorktreeProvisioning:
		if params.Version == 2 || params.Reason != "" || !params.ReadyAt.IsZero() ||
			(params.Version == 1 && !params.UpdatedAt.Equal(params.CreatedAt)) {
			return TaskWorktreeBinding{}, fmt.Errorf("%w: invalid provisioning Task worktree", ErrInvalidArgument)
		}
	case TaskWorktreeReady:
		if params.Version < 2 || params.Reason != "" || params.ReadyAt.IsZero() ||
			params.ReadyAt.Before(params.CreatedAt) || !params.ReadyAt.Equal(params.UpdatedAt) {
			return TaskWorktreeBinding{}, fmt.Errorf("%w: invalid ready Task worktree", ErrInvalidArgument)
		}
	case TaskWorktreeRecoveryRequired:
		if params.Version < 2 || params.Reason == "" || !params.ReadyAt.IsZero() {
			return TaskWorktreeBinding{}, fmt.Errorf("%w: invalid recovery-required Task worktree", ErrInvalidArgument)
		}
	default:
		return TaskWorktreeBinding{}, fmt.Errorf("%w: invalid Task worktree state", ErrInvalidArgument)
	}
	return TaskWorktreeBinding{params: params}, nil
}

func validTaskStartAuthority(tree, baseRef, policy string) bool {
	switch policy {
	case TaskStartPolicyLegacy:
		return tree == "" && baseRef == ""
	case TaskStartPolicyCurrentHEAD:
		return validPinnedRevision(tree) && validTaskBaseRef(baseRef)
	default:
		return false
	}
}

func validTaskBaseRef(value string) bool {
	if value == "HEAD" {
		return true
	}
	const prefix = "refs/heads/"
	if !strings.HasPrefix(value, prefix) {
		return false
	}
	name := strings.TrimPrefix(value, prefix)
	if name == "" || strings.TrimSpace(name) != name || strings.HasPrefix(name, "/") ||
		strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.Contains(name, "//") ||
		strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.ContainsAny(name, "\\ ~^:?*[") {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	for _, char := range name {
		if char < 0x20 || char == 0x7f {
			return false
		}
	}
	return true
}

func validWorktreeRepositoryIdentity(value string) bool {
	if value == "" || len(value) > 100 || strings.TrimSpace(value) != value || value == "." || value == ".." {
		return false
	}
	for index, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || (index > 0 && (char == '.' || char == '_' || char == '-')) {
			continue
		}
		return false
	}
	return true
}

func validPinnedRevision(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func validTaskWorktreeLocator(value, repositoryIdentity string, taskID TaskID) bool {
	if value == "" || len(value) > 512 || strings.TrimSpace(value) != value || strings.HasPrefix(value, "/") ||
		strings.ContainsAny(value, "/\\") || strings.Contains(value, "\x00") {
		return false
	}
	prefix := repositoryIdentity + "-" + taskID.String() + "-"
	if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) || len(value) > 300 {
		return false
	}
	for _, char := range strings.TrimPrefix(value, prefix) {
		if (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || char == '-' {
			continue
		}
		return false
	}
	return true
}

func (binding TaskWorktreeBinding) Ready(at time.Time) (TaskWorktreeBinding, error) {
	if binding.params.State != TaskWorktreeProvisioning || at.Before(binding.params.UpdatedAt) {
		return TaskWorktreeBinding{}, ErrInvalidRunTransition
	}
	params := binding.params
	params.State, params.Version, params.UpdatedAt, params.ReadyAt = TaskWorktreeReady, params.Version+1, at, at
	return restoreTaskWorktreeBinding(params)
}

func (binding TaskWorktreeBinding) RecoveryRequired(reason string, at time.Time) (TaskWorktreeBinding, error) {
	if (binding.params.State != TaskWorktreeProvisioning && binding.params.State != TaskWorktreeReady) || strings.TrimSpace(reason) == "" || at.Before(binding.params.UpdatedAt) {
		return TaskWorktreeBinding{}, ErrInvalidRunTransition
	}
	params := binding.params
	params.State, params.Version, params.Reason, params.UpdatedAt = TaskWorktreeRecoveryRequired, params.Version+1, strings.TrimSpace(reason), at
	params.ReadyAt = time.Time{}
	return restoreTaskWorktreeBinding(params)
}

func (binding TaskWorktreeBinding) RetryProvisioning(at time.Time) (TaskWorktreeBinding, error) {
	if binding.params.State != TaskWorktreeRecoveryRequired || at.Before(binding.params.UpdatedAt) {
		return TaskWorktreeBinding{}, ErrInvalidRunTransition
	}
	params := binding.params
	params.State, params.Version, params.Reason, params.UpdatedAt = TaskWorktreeProvisioning, params.Version+1, "", at
	return restoreTaskWorktreeBinding(params)
}

func (binding TaskWorktreeBinding) TaskID() TaskID {
	id, _ := ParseTaskID(binding.params.TaskID)
	return id
}
func (binding TaskWorktreeBinding) RepositoryIdentity() string {
	return binding.params.RepositoryIdentity
}
func (binding TaskWorktreeBinding) PinnedBaseRevision() string {
	return binding.params.PinnedBaseRevision
}
func (binding TaskWorktreeBinding) PinnedBaseTree() string  { return binding.params.PinnedBaseTree }
func (binding TaskWorktreeBinding) BaseRef() string         { return binding.params.BaseRef }
func (binding TaskWorktreeBinding) StartPolicy() string     { return binding.params.StartPolicy }
func (binding TaskWorktreeBinding) RelativeLocator() string { return binding.params.RelativeLocator }
func (binding TaskWorktreeBinding) ConfiguredRootFingerprint() [32]byte {
	return binding.params.ConfiguredRootFingerprint
}
func (binding TaskWorktreeBinding) State() TaskWorktreeState { return binding.params.State }
func (binding TaskWorktreeBinding) Version() uint64          { return binding.params.Version }
func (binding TaskWorktreeBinding) Reason() string           { return binding.params.Reason }
func (binding TaskWorktreeBinding) CreatedAt() time.Time     { return binding.params.CreatedAt }
func (binding TaskWorktreeBinding) UpdatedAt() time.Time     { return binding.params.UpdatedAt }
func (binding TaskWorktreeBinding) ReadyAt() time.Time       { return binding.params.ReadyAt }
