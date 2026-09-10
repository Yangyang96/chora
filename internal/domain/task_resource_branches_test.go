package domain

import (
	"bytes"
	"encoding/json"
	"regexp"
	"testing"
)

func TestTaskResourceBranchBindingAndLegacyCanonicalCompatibility(t *testing.T) {
	taskID := NewTaskID()
	repositoryID := NewRepositoryID()
	legacy := TaskResourceSnapshot{
		SchemaVersion: TaskResourceSchemaV2, TaskID: taskID.String(), ProjectID: NewProjectID().String(),
		RoomID: NewRoomID().String(), SelectionSource: "test",
		Resources: []TaskRepositoryResource{{
			RepoID: repositoryID.String(), Name: "repo", Checkout: "/tmp/repo", CommonGitDir: "/tmp/repo/.git",
			PhysicalIdentity: string(bytes.Repeat([]byte{'a'}, 64)), Role: "write",
			BaseCommit: string(bytes.Repeat([]byte{'b'}, 40)), BaseTree: string(bytes.Repeat([]byte{'c'}, 40)),
			AssociationVersion: 1, Scope: TaskRepositoryScope{Mode: "repository", MigrationChoice: "not_needed"}, Checks: TaskCheckPolicy{Mode: "none"},
		}},
	}
	before, _, err := legacy.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(before, []byte("deliveryMode")) || bytes.Contains(before, []byte("taskBranch")) || bytes.Contains(before, []byte("workspaceName")) {
		t.Fatalf("legacy canonical JSON changed: %s", before)
	}
	legacy.TaskID = ""
	legacy.Resources[0].BaseRef = "refs/heads/main"
	bound, err := BindTaskResourceBranches(legacy, taskID, "新增乘法运算")
	if err != nil {
		t.Fatal(err)
	}
	resource := bound.Resources[0]
	if !regexp.MustCompile(`^[0-9a-f]{12}$`).MatchString(resource.WorkspaceDirectory()) {
		t.Fatal("long worktree name")
	}
	malformed := bound
	malformed.Resources = append([]TaskRepositoryResource(nil), bound.Resources...)
	malformed.Resources[0].WorkspaceName = "000000000000"
	if malformed.Validate() == nil {
		t.Fatal("accepted another repository's workspace name")
	}

	if resource.DeliveryMode != TaskResourceDeliveryTaskBranch || !regexp.MustCompile(`^feature/[0-9a-f]{12}$`).MatchString(resource.TaskBranch) {
		t.Fatalf("invalid short branch: %+v", resource)
	}
	// The persisted long-name format keeps exactly the same canonical bytes.
	legacy.TaskID = taskID.String()
	legacy.Resources[0].DeliveryMode = TaskResourceDeliveryTaskBranch
	legacy.Resources[0].TaskBranch = "chora/" + taskID.String() + "/" + repositoryID.String()
	oldJSON, oldDigest, err := legacy.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(oldJSON, []byte("branchType")) {
		t.Fatal("historical snapshot gained a new field")
	}
	var restored TaskResourceSnapshot
	if err := json.Unmarshal(oldJSON, &restored); err != nil {
		t.Fatal(err)
	}
	newJSON, newDigest, err := restored.CanonicalJSON()
	if err != nil || !bytes.Equal(oldJSON, newJSON) || oldDigest != newDigest {
		t.Fatal("legacy canonical bytes changed")
	}
	// Full task/repository identities, rather than their shared UUID time prefix,
	// distinguish branches. Rebinding the same identities is deterministic.
	legacy.TaskID = ""
	again, err := BindTaskResourceBranches(legacy, taskID, "新增乘法运算")
	if err != nil || again.Resources[0].TaskBranch != resource.TaskBranch {
		t.Fatal("unstable branch name")
	}
	other, err := BindTaskResourceBranches(legacy, NewTaskID(), "新增乘法运算")
	if err != nil || other.Resources[0].TaskBranch == resource.TaskBranch {
		t.Fatal("tasks share a branch")
	}
	legacy.Resources[0].RepoID = NewRepositoryID().String()
	other, err = BindTaskResourceBranches(legacy, taskID, "新增乘法运算")
	if err != nil || other.Resources[0].TaskBranch == resource.TaskBranch {
		t.Fatal("repositories share a branch")
	}
	bound.Resources[0].TaskBranch = "feature/000000000000"
	if bound.Validate() == nil {
		t.Fatal("accepted unrelated short branch")
	}
}

func TestTaskBranchTypeFromTitle(t *testing.T) {
	for title, want := range map[string]string{
		"feat(calculator): add multiply": "feature", "feature/add-multiply": "feature",
		"Fix(parser): reject invalid input": "fix", "fix login": "fix", "修复登录失败": "fix",
		"chore(deps): update packages": "chore", "升级依赖": "chore", "清理无用文件": "chore",
		"docs: document API": "docs", "补充测试覆盖": "test", "refactor: simplify parsing": "refactor",
		"在 calculator.js 新增 multiply": "feature", "新增功能并修复边界问题": "feature",
	} {
		if got := taskBranchType(title); got != want {
			t.Errorf("%q: got %q, want %q", title, got, want)
		}
	}
}
