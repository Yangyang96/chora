package domain

import (
	"crypto/sha256"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestTaskWorktreeBindingLifecyclePreservesImmutableIdentity(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	taskID := NewTaskID()
	params := TaskWorktreeBindingParams{
		TaskID: taskID.String(), RepositoryIdentity: "chora", PinnedBaseRevision: strings.Repeat("a", 40),
		PinnedBaseTree: strings.Repeat("b", 40), BaseRef: "refs/heads/main", StartPolicy: TaskStartPolicyCurrentHEAD,
		RelativeLocator: "chora-" + taskID.String() + "-fix-apply", ConfiguredRootFingerprint: sha256.Sum256([]byte("configured-root")), CreatedAt: now,
	}
	binding, err := NewTaskWorktreeBinding(params)
	if err != nil {
		t.Fatal(err)
	}
	if binding.State() != TaskWorktreeProvisioning || binding.Version() != 1 || !binding.UpdatedAt().Equal(now) || !binding.ReadyAt().IsZero() {
		t.Fatalf("new binding=%#v", binding)
	}
	recovery, err := binding.RecoveryRequired("  interrupted provisioning  ", now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	retrying, err := recovery.RetryProvisioning(now.Add(2 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ready, err := retrying.Ready(now.Add(3 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Reason() != "interrupted provisioning" || retrying.Version() != 3 || ready.State() != TaskWorktreeReady || ready.Version() != 4 || !ready.ReadyAt().Equal(now.Add(3*time.Second)) {
		t.Fatalf("recovery=%#v retry=%#v ready=%#v", recovery, retrying, ready)
	}
	if ready.TaskID() != binding.TaskID() || ready.RepositoryIdentity() != binding.RepositoryIdentity() || ready.PinnedBaseRevision() != binding.PinnedBaseRevision() || ready.PinnedBaseTree() != binding.PinnedBaseTree() || ready.BaseRef() != binding.BaseRef() || ready.StartPolicy() != binding.StartPolicy() || ready.RelativeLocator() != binding.RelativeLocator() || ready.ConfiguredRootFingerprint() != binding.ConfiguredRootFingerprint() || !ready.CreatedAt().Equal(binding.CreatedAt()) {
		t.Fatal("lifecycle changed immutable identity")
	}
	drifted, err := ready.RecoveryRequired("ready worktree drifted", now.Add(4*time.Second))
	if err != nil || drifted.Version() != 5 || drifted.State() != TaskWorktreeRecoveryRequired || !drifted.ReadyAt().IsZero() {
		t.Fatalf("drifted=%#v err=%v", drifted, err)
	}
	retryingAgain, err := drifted.RetryProvisioning(now.Add(5 * time.Second))
	if err != nil {
		t.Fatal(err)
	}
	readyAgain, err := retryingAgain.Ready(now.Add(6 * time.Second))
	if err != nil || readyAgain.Version() != 7 {
		t.Fatalf("ready again=%#v err=%v", readyAgain, err)
	}
	secondDrift, err := readyAgain.RecoveryRequired("drifted again", now.Add(7*time.Second))
	if err != nil || secondDrift.Version() != 8 || !secondDrift.ReadyAt().IsZero() {
		t.Fatalf("second drift=%#v err=%v", secondDrift, err)
	}
}

func TestTaskWorktreeBindingNormalizesLegacyAndValidatesCurrentBaseAuthority(t *testing.T) {
	now := time.Date(2026, 9, 7, 10, 0, 0, 0, time.UTC)
	taskID := NewTaskID()
	base := TaskWorktreeBindingParams{
		TaskID: taskID.String(), RepositoryIdentity: "chora", PinnedBaseRevision: strings.Repeat("a", 40),
		RelativeLocator: "chora-" + taskID.String() + "-authority", ConfiguredRootFingerprint: sha256.Sum256([]byte("root")), CreatedAt: now,
	}
	legacy, err := NewTaskWorktreeBinding(base)
	if err != nil || legacy.StartPolicy() != TaskStartPolicyLegacy || legacy.PinnedBaseTree() != "" || legacy.BaseRef() != "" {
		t.Fatalf("legacy=%#v err=%v", legacy, err)
	}
	current := base
	current.PinnedBaseTree, current.BaseRef, current.StartPolicy = strings.Repeat("b", 40), "HEAD", TaskStartPolicyCurrentHEAD
	binding, err := NewTaskWorktreeBinding(current)
	if err != nil || binding.PinnedBaseTree() != current.PinnedBaseTree || binding.BaseRef() != "HEAD" || binding.StartPolicy() != TaskStartPolicyCurrentHEAD {
		t.Fatalf("current=%#v err=%v", binding, err)
	}
	for name, mutate := range map[string]func(*TaskWorktreeBindingParams){
		"missing policy":       func(p *TaskWorktreeBindingParams) { p.StartPolicy = "" },
		"current missing tree": func(p *TaskWorktreeBindingParams) { p.PinnedBaseTree = "" },
		"current bad tree":     func(p *TaskWorktreeBindingParams) { p.PinnedBaseTree = strings.Repeat("B", 40) },
		"current symbolic ref": func(p *TaskWorktreeBindingParams) { p.BaseRef = "main" },
		"current unsafe ref":   func(p *TaskWorktreeBindingParams) { p.BaseRef = "refs/heads/topic..bad" },
		"legacy with tree": func(p *TaskWorktreeBindingParams) {
			p.StartPolicy, p.BaseRef = TaskStartPolicyLegacy, ""
		},
	} {
		t.Run(name, func(t *testing.T) {
			invalid := current
			mutate(&invalid)
			if _, err := NewTaskWorktreeBinding(invalid); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestTaskWorktreeBindingRejectsInvalidIdentityPathAndState(t *testing.T) {
	now := time.Date(2026, 8, 24, 10, 0, 0, 0, time.UTC)
	taskID := NewTaskID()
	valid := TaskWorktreeBindingParams{
		TaskID: taskID.String(), RepositoryIdentity: "chora", PinnedBaseRevision: strings.Repeat("b", 40),
		RelativeLocator: "chora-" + taskID.String() + "-topic", ConfiguredRootFingerprint: sha256.Sum256([]byte("root")),
		State: TaskWorktreeProvisioning, Version: 1, CreatedAt: now, UpdatedAt: now,
	}
	tests := map[string]func(*TaskWorktreeBindingParams){
		"task":                      func(p *TaskWorktreeBindingParams) { p.TaskID = "task_bad" },
		"repository traversal":      func(p *TaskWorktreeBindingParams) { p.RepositoryIdentity = "../chora" },
		"symbolic base":             func(p *TaskWorktreeBindingParams) { p.PinnedBaseRevision = "HEAD" },
		"uppercase base":            func(p *TaskWorktreeBindingParams) { p.PinnedBaseRevision = strings.Repeat("A", 40) },
		"zero fingerprint":          func(p *TaskWorktreeBindingParams) { p.ConfiguredRootFingerprint = [32]byte{} },
		"absolute locator":          func(p *TaskWorktreeBindingParams) { p.RelativeLocator = "/chora-task-topic" },
		"traversal locator":         func(p *TaskWorktreeBindingParams) { p.RelativeLocator = "chora/../task-topic" },
		"backslash locator":         func(p *TaskWorktreeBindingParams) { p.RelativeLocator = "chora\\task-topic" },
		"NUL locator":               func(p *TaskWorktreeBindingParams) { p.RelativeLocator += "\x00tail" },
		"foreign root locator":      func(p *TaskWorktreeBindingParams) { p.RelativeLocator = "other-" + taskID.String() + "-topic" },
		"foreign task locator":      func(p *TaskWorktreeBindingParams) { p.RelativeLocator = "chora-" + NewTaskID().String() + "-topic" },
		"zero created":              func(p *TaskWorktreeBindingParams) { p.CreatedAt = time.Time{} },
		"updated rollback":          func(p *TaskWorktreeBindingParams) { p.UpdatedAt = now.Add(-time.Second) },
		"unknown state":             func(p *TaskWorktreeBindingParams) { p.State = "unknown" },
		"provisioning reason":       func(p *TaskWorktreeBindingParams) { p.Reason = "unexpected" },
		"unreachable version":       func(p *TaskWorktreeBindingParams) { p.Version = 2 },
		"mutated initial timestamp": func(p *TaskWorktreeBindingParams) { p.UpdatedAt = now.Add(time.Second) },
		"ready without timestamp":   func(p *TaskWorktreeBindingParams) { p.State, p.Version = TaskWorktreeReady, 2 },
		"recovery without reason":   func(p *TaskWorktreeBindingParams) { p.State, p.Version = TaskWorktreeRecoveryRequired, 2 },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			params := valid
			mutate(&params)
			if _, err := RestoreTaskWorktreeBinding(params); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestTaskWorktreeBindingRejectsIllegalOrBackdatedTransitions(t *testing.T) {
	now := time.Now().UTC()
	taskID := NewTaskID()
	binding, err := NewTaskWorktreeBinding(TaskWorktreeBindingParams{
		TaskID: taskID.String(), RepositoryIdentity: "chora", PinnedBaseRevision: strings.Repeat("c", 40),
		RelativeLocator: "chora-" + taskID.String() + "-topic", ConfiguredRootFingerprint: sha256.Sum256([]byte("root")), CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := binding.Ready(now.Add(-time.Second)); !errors.Is(err, ErrInvalidRunTransition) {
		t.Fatalf("backdated ready err=%v", err)
	}
	if _, err := binding.RecoveryRequired(" ", now); !errors.Is(err, ErrInvalidRunTransition) {
		t.Fatalf("empty recovery err=%v", err)
	}
	if _, err := binding.RetryProvisioning(now); !errors.Is(err, ErrInvalidRunTransition) {
		t.Fatalf("provisioning retry err=%v", err)
	}
	recovery, err := binding.RecoveryRequired("interrupted", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := recovery.RecoveryRequired("again", now); !errors.Is(err, ErrInvalidRunTransition) {
		t.Fatalf("recovery-to-recovery err=%v", err)
	}
}
