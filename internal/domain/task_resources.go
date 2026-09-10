package domain

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

type RepositoryID struct{ idValue }

func NewRepositoryID() RepositoryID { return RepositoryID{newIDValue()} }
func ParseRepositoryID(value string) (RepositoryID, error) {
	id, err := parseIDValue(value, "repo_")
	return RepositoryID{id}, err
}
func (id RepositoryID) String() string { return id.idValue.string("repo_") }
func (id RepositoryID) Valid() bool    { return id.idValue.valid() }

// RepositoryRecord separates physical identity from mutable Git observations.
// Empty physical identity is allowed only for explicitly unverified legacy rows.
type RepositoryRecord struct {
	ID               RepositoryID
	Name             string
	Checkout         string
	CommonGitDir     string
	PhysicalIdentity string
	IdentitySource   string
	LegacyCommit     string
	LegacyTree       string
	CreatedAt        time.Time
}

func (r RepositoryRecord) Validate() error {
	if !r.ID.Valid() || strings.TrimSpace(r.Name) == "" || !canonicalAbsolutePath(r.Checkout) || r.CreatedAt.IsZero() {
		return fmt.Errorf("%w: repository identity", ErrInvalidArgument)
	}
	if r.IdentitySource == "legacy_unverified" && r.PhysicalIdentity == "" && r.CommonGitDir == "" {
		return nil
	}
	if r.IdentitySource != "inspected" || !canonicalAbsolutePath(r.CommonGitDir) || len(r.PhysicalIdentity) != 64 {
		return fmt.Errorf("%w: physical repository identity", ErrInvalidArgument)
	}
	return nil
}

type ProjectRepository struct {
	ProjectID  ProjectID
	Repository RepositoryRecord
	State      string
	Version    uint64
	AddedAt    time.Time
	UpdatedAt  time.Time
}
type RoomRepositoryReferences struct {
	RoomID        RoomID
	Version       uint64
	RepositoryIDs []RepositoryID
	UpdatedAt     time.Time
}

type RepositoryDeliveryDefault struct {
	RepositoryID RepositoryID
	TargetRef    string
	Version      uint64
	UpdatedAt    time.Time
}

func (d RepositoryDeliveryDefault) Validate() error {
	if !d.RepositoryID.Valid() || !validTaskTargetRef(d.TargetRef) || d.Version == 0 || d.UpdatedAt.IsZero() {
		return fmt.Errorf("%w: repository delivery default", ErrInvalidArgument)
	}
	return nil
}

// These JSON contracts are immutable Task authority, not a client declaration.
type TaskResourceSnapshot struct {
	SchemaVersion   string                   `json:"schemaVersion"`
	TaskID          string                   `json:"taskId"`
	ProjectID       string                   `json:"projectId"`
	RoomID          string                   `json:"roomId"`
	SelectionSource string                   `json:"selectionSource"`
	Resources       []TaskRepositoryResource `json:"resources"`
	BranchType      string                   `json:"branchType,omitempty"`
}
type TaskRepositoryResource struct {
	RepoID             string              `json:"repoId"`
	Name               string              `json:"name"`
	Checkout           string              `json:"checkout"`
	CommonGitDir       string              `json:"commonGitDir"`
	PhysicalIdentity   string              `json:"physicalIdentity"`
	Role               string              `json:"role"`
	BaseCommit         string              `json:"baseCommit"`
	BaseTree           string              `json:"baseTree"`
	BaseRef            string              `json:"baseRef"`
	DeliveryMode       string              `json:"deliveryMode,omitempty"`
	TaskBranch         string              `json:"taskBranch,omitempty"`
	AssociationVersion uint64              `json:"associationVersion"`
	Scope              TaskRepositoryScope `json:"scope"`
	Checks             TaskCheckPolicy     `json:"checks"`
	WorkspaceName      string              `json:"workspaceName,omitempty"`
}
type TaskRepositoryScope struct {
	Mode                 string   `json:"mode"`
	WritableFiles        []string `json:"writableFiles"`
	WritableDirectories  []string `json:"writableDirectories"`
	ProtectedDirectories []string `json:"protectedDirectories"`
	MigrationChoice      string   `json:"migrationChoice"`
}
type TaskCheckCommand struct {
	ID               string   `json:"id"`
	Name             string   `json:"name"`
	Version          uint64   `json:"version"`
	Command          string   `json:"command"`
	Argv             []string `json:"argv"`
	WorkingDirectory string   `json:"workingDirectory"`
	Source           string   `json:"source"`
}
type TaskCheckPolicy struct {
	Mode            string             `json:"mode"`
	Commands        []TaskCheckCommand `json:"commands"`
	Preparation     []TaskCheckCommand `json:"preparation"`
	SelectionSource string             `json:"selectionSource"`
}

const TaskResourceSchemaV2 = "chora.task-resources.v2"
const TaskResourceDeliveryTaskBranch = "task_branch"

// BindTaskResourceBranches fills Task identity and branch names only after the
// server has allocated the stable Task ID. Callers must never accept TaskBranch
// from a client.
func BindTaskResourceBranches(snapshot TaskResourceSnapshot, taskID TaskID, taskTitle string) (TaskResourceSnapshot, error) {
	if !taskID.Valid() || snapshot.TaskID != "" {
		return TaskResourceSnapshot{}, fmt.Errorf("%w: Task resource identity", ErrInvalidArgument)
	}
	snapshot.TaskID = taskID.String()
	snapshot.BranchType = taskBranchType(taskTitle)
	snapshot.Resources = append([]TaskRepositoryResource(nil), snapshot.Resources...)
	for index := range snapshot.Resources {
		resource := &snapshot.Resources[index]
		resource.WorkspaceName = ShortWorkspaceName(resource.RepoID)
		if resource.Role == "write" {
			resource.DeliveryMode = TaskResourceDeliveryTaskBranch
			resource.TaskBranch = shortTaskBranch(snapshot.BranchType, snapshot.TaskID, resource.RepoID)
		}
	}
	if err := snapshot.Validate(); err != nil {
		return TaskResourceSnapshot{}, err
	}
	return snapshot, nil
}

// BranchType is the discriminator for short-name snapshots. Empty preserves
// historical names and canonical bytes; never rename an already-bound Task.
func shortTaskBranch(kind, taskID, repoID string) string {
	digest := sha256.Sum256([]byte("chora.task-branch.v1\x00" + taskID + "\x00" + repoID))
	return fmt.Sprintf("%s/%x", kind, digest[:6])
}

func validTaskBranchType(kind string) bool {
	switch kind {
	case "feature", "fix", "chore", "docs", "test", "refactor", "perf", "build", "ci":
		return true
	}
	return false
}

func taskBranchType(title string) string {
	title = strings.ToLower(strings.TrimSpace(title))
	first := strings.FieldsFunc(title, func(r rune) bool { return strings.ContainsRune(" /(:：!\t\n", r) })
	if len(first) > 0 {
		if validTaskBranchType(first[0]) {
			return first[0]
		}
		switch first[0] {
		case "feat":
			return "feature"
		case "bugfix", "hotfix", "repair", "resolve":
			return "fix"
		}
	}
	for _, rule := range []struct {
		kind     string
		prefixes []string
	}{
		{"fix", []string{"修复", "修正", "修补", "解决"}},
		{"chore", []string{"维护", "清理", "升级", "更新依赖", "更新配置"}},
		{"docs", []string{"文档", "更新文档", "补充文档"}},
		{"test", []string{"测试", "补充测试", "添加测试", "增加测试"}},
		{"refactor", []string{"重构"}},
		{"perf", []string{"性能优化", "优化性能"}},
	} {
		for _, prefix := range rule.prefixes {
			if strings.HasPrefix(title, prefix) {
				return rule.kind
			}
		}
	}
	return "feature"
}

func (s TaskResourceSnapshot) CanonicalJSON() ([]byte, [32]byte, error) {
	if err := s.Validate(); err != nil {
		return nil, [32]byte{}, err
	}
	b, err := json.Marshal(s)
	return b, sha256.Sum256(b), err
}
func (s TaskResourceSnapshot) Validate() error {
	bad := func() error { return fmt.Errorf("%w: Task resource snapshot", ErrInvalidArgument) }
	if s.SchemaVersion != TaskResourceSchemaV2 || len(s.Resources) == 0 || len(s.Resources) > TaskRepositoryLimit || s.SelectionSource == "" {
		return bad()
	}
	if _, err := ParseTaskID(s.TaskID); err != nil {
		return bad()
	}
	if _, err := ParseProjectID(s.ProjectID); err != nil {
		return bad()
	}
	if _, err := ParseRoomID(s.RoomID); err != nil {
		return bad()
	}
	if s.BranchType != "" && !validTaskBranchType(s.BranchType) {
		return bad()
	}
	previous := ""
	workspaceNames := map[string]bool{}
	for _, r := range s.Resources {
		if (r.WorkspaceName == "") != (s.Resources[0].WorkspaceName == "") || (r.WorkspaceName != "" && r.WorkspaceName != ShortWorkspaceName(r.RepoID)) || workspaceNames[r.WorkspaceDirectory()] {
			return bad()
		}
		workspaceNames[r.WorkspaceDirectory()] = true
		if _, err := ParseRepositoryID(r.RepoID); err != nil {
			return bad()
		}
		if r.RepoID <= previous || r.Name == "" || !canonicalAbsolutePath(r.Checkout) || !canonicalAbsolutePath(r.CommonGitDir) || len(r.PhysicalIdentity) != 64 || !resourceHex(r.PhysicalIdentity, 64) || !resourceHex(r.BaseCommit, 40) || !resourceHex(r.BaseTree, 40) || r.AssociationVersion == 0 || (r.Role != "write" && r.Role != "reference") {
			return bad()
		}
		if r.DeliveryMode == "" {
			// Historical snapshots did not validate BaseRef and must retain their
			// exact canonical bytes and detached behavior.
			if r.TaskBranch != "" || (s.BranchType != "" && r.Role == "write") {
				return bad()
			}
		} else {
			expectedBranch := "chora/" + s.TaskID + "/" + r.RepoID
			if s.BranchType != "" {
				expectedBranch = shortTaskBranch(s.BranchType, s.TaskID, r.RepoID)
			}
			if r.DeliveryMode != TaskResourceDeliveryTaskBranch || r.Role != "write" || !validTaskTargetRef(r.BaseRef) || r.TaskBranch != expectedBranch {
				return bad()
			}
		}
		previous = r.RepoID
		if r.Scope.Mode != "repository" && r.Scope.Mode != "restricted" {
			return bad()
		}
		if r.Scope.Mode == "repository" && (len(r.Scope.WritableFiles) > 0 || len(r.Scope.WritableDirectories) > 0) {
			return bad()
		}
		if r.Scope.Mode == "restricted" && r.Role == "write" && len(r.Scope.WritableFiles)+len(r.Scope.WritableDirectories) == 0 {
			return bad()
		}
		if r.Scope.MigrationChoice != "not_needed" && r.Scope.MigrationChoice != "legacy" && r.Scope.MigrationChoice != "repository" {
			return bad()
		}
		for _, paths := range [][]string{r.Scope.WritableFiles, r.Scope.WritableDirectories, r.Scope.ProtectedDirectories} {
			for _, p := range paths {
				if !validProjectRelativePath(p, false) {
					return bad()
				}
			}
		}
		if r.Checks.Mode != "auto" && r.Checks.Mode != "named" && r.Checks.Mode != "none" {
			return bad()
		}
		if r.Checks.Mode == "none" && len(r.Checks.Commands) > 0 || r.Checks.Mode == "named" && len(r.Checks.Commands) == 0 {
			return bad()
		}
		for _, commands := range [][]TaskCheckCommand{r.Checks.Commands, r.Checks.Preparation} {
			for _, c := range commands {
				if c.ID == "" || c.Name == "" || c.Source == "" || len(c.Argv) == 0 || (c.WorkingDirectory != "." && !validProjectRelativePath(c.WorkingDirectory, true)) {
					return bad()
				}
			}
		}
	}
	return nil
}

func validTaskTargetRef(value string) bool {
	if !strings.HasPrefix(value, "refs/heads/") {
		return false
	}
	name := strings.TrimPrefix(value, "refs/heads/")
	if name == "" || strings.TrimSpace(name) != name || strings.HasPrefix(name, "/") || strings.HasSuffix(name, "/") || strings.HasSuffix(name, ".") || strings.Contains(name, "//") || strings.Contains(name, "..") || strings.Contains(name, "@{") || strings.ContainsAny(name, "\\ ~^:?*[") {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func ValidTaskTargetRef(value string) bool { return validTaskTargetRef(value) }
func resourceHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}
func (scope TaskRepositoryScope) Allows(relative string) bool {
	if !validProjectRelativePath(relative, false) {
		return false
	}
	for _, p := range scope.ProtectedDirectories {
		if relative == p || strings.HasPrefix(relative, p+"/") {
			return false
		}
	}
	if scope.Mode == "repository" {
		return true
	}
	for _, p := range scope.WritableFiles {
		if p == relative {
			return true
		}
	}
	for _, p := range scope.WritableDirectories {
		if relative == p || strings.HasPrefix(relative, p+"/") {
			return true
		}
	}
	return false
}

// ShortWorkspaceName hashes the complete stable identity, including its type.
func ShortWorkspaceName(id string) string {
	digest := sha256.Sum256([]byte("chora.workspace-name.v1\x00" + id))
	return fmt.Sprintf("%x", digest[:6])
}

// WorkspaceDirectory retains the immutable legacy locator when no short name
// was frozen. Display aliases never participate in filesystem authority.
func (r TaskRepositoryResource) WorkspaceDirectory() string {
	if r.WorkspaceName != "" {
		return r.WorkspaceName
	}
	return r.RepoID
}

func (r TaskRepositoryResource) TaskWorkspaceDirectory(taskID TaskID) string {
	if r.WorkspaceName != "" {
		return ShortWorkspaceName(taskID.String())
	}
	return taskID.String()
}

func ValidRepositoryWorkspaceLocator(repoID, locator string) bool {
	if _, err := ParseRepositoryID(repoID); err != nil {
		return false
	}
	return locator == repoID || locator == ShortWorkspaceName(repoID)
}

// RepoWorkspacePath uses the stable ID, never a mutable display alias.
func RepoWorkspacePath(taskRoot string, id RepositoryID) string {
	return filepath.Join(taskRoot, id.String())
}

// TaskRepositoryWorktree is a preparation record, separate from immutable scope.
// RelativePath is anchored below the configured data root; no sibling symlinks.
type TaskRepositoryWorktree struct {
	TaskID          TaskID
	RepositoryID    RepositoryID
	RelativePath    string
	RootFingerprint string
	BaseCommit      string
	BaseTree        string
	DeliveryMode    string
	TaskBranch      string
	TargetRef       string
	State           string
	Version         uint64
	Reason          string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

type RepositoryCheckDefinition struct {
	RepositoryID RepositoryID
	Command      TaskCheckCommand
	CreatedAt    time.Time
}
