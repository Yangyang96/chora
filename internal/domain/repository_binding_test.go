package domain

import (
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func bindingParams(roomID RoomID, name string) RepositoryBindingParams {
	return RepositoryBindingParams{
		RoomID:         roomID,
		Name:           name,
		LocalLocator:   filepath.Clean(filepath.Join(string(filepath.Separator), "tmp", "repo")),
		SourceKind:     RepositorySourceKindOpen,
		AdmittedBase:   "0123456789abcdef0123456789abcdef01234567",
		BaseIdentity:   "sha:0123456789abcdef0123456789abcdef01234567:tree:89abcdef0123456789abcdef0123456789abcdef",
		TargetWorktree: filepath.Clean(filepath.Join(string(filepath.Separator), "tmp", "repo")),
		CreatedAt:      time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC),
		UpdatedAt:      time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC),
	}
}

func validBaseIdentityFor(commit string) string {
	return "sha:" + commit + ":tree:89abcdef0123456789abcdef0123456789abcdef"
}

func TestNewRepositoryBindingDefaultsActiveVersionOne(t *testing.T) {
	params := bindingParams(NewRoomID(), "repo")
	binding, err := NewRepositoryBinding(params)
	if err != nil {
		t.Fatal(err)
	}
	if binding.State() != RepositoryBindingStateActive || binding.Version() != 1 {
		t.Fatalf("state=%q version=%d", binding.State(), binding.Version())
	}
}

func TestRestoreRepositoryBindingValidation(t *testing.T) {
	now := time.Date(2026, 9, 3, 0, 0, 0, 0, time.UTC)
	base := RepositoryBindingRecord{
		RoomID:         NewRoomID(),
		Name:           "repo",
		LocalLocator:   filepath.Clean(filepath.Join(string(filepath.Separator), "tmp", "repo")),
		SourceKind:     RepositorySourceKindOpen,
		AdmittedBase:   "0123456789abcdef0123456789abcdef01234567",
		BaseIdentity:   "sha:0123456789abcdef0123456789abcdef01234567:tree:89abcdef0123456789abcdef0123456789abcdef",
		TargetWorktree: filepath.Clean(filepath.Join(string(filepath.Separator), "tmp", "repo")),
		State:          RepositoryBindingStateActive,
		Version:        1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	cases := []struct {
		name   string
		mutate func(*RepositoryBindingRecord)
	}{
		{"empty name", func(r *RepositoryBindingRecord) { r.Name = "  " }},
		{"invalid room", func(r *RepositoryBindingRecord) { r.RoomID = RoomID{} }},
		{"relative locator", func(r *RepositoryBindingRecord) { r.LocalLocator = "tmp/repo" }},
		{"unclean locator", func(r *RepositoryBindingRecord) {
			r.LocalLocator = filepath.Clean(filepath.Join(string(filepath.Separator), "tmp", "repo")) + string(filepath.Separator) + "."
		}},
		{"zero version", func(r *RepositoryBindingRecord) { r.Version = 0 }},
		{"invalid state", func(r *RepositoryBindingRecord) { r.State = RepositoryBindingState("bogus") }},
		{"invalid source kind", func(r *RepositoryBindingRecord) { r.SourceKind = RepositorySourceKind("bogus") }},
		{"empty admitted base", func(r *RepositoryBindingRecord) { r.AdmittedBase = "" }},
		{"empty base identity", func(r *RepositoryBindingRecord) { r.BaseIdentity = "" }},
		{"malformed base identity", func(r *RepositoryBindingRecord) { r.BaseIdentity = "not-sha-prefixed" }},
		{"clone without url", func(r *RepositoryBindingRecord) { r.SourceKind = RepositorySourceKindClone }},
		{"open with url", func(r *RepositoryBindingRecord) { r.CloneURL = "https://example.invalid/repo.git" }},
		{"relative target worktree", func(r *RepositoryBindingRecord) { r.TargetWorktree = "tmp/repo" }},
		{"updated before created", func(r *RepositoryBindingRecord) { r.UpdatedAt = now.Add(-time.Second) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			record := base
			tc.mutate(&record)
			if _, err := RestoreRepositoryBinding(record); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("RestoreRepositoryBinding() error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestRestoreRepositoryBindingAcceptsCloneWithURL(t *testing.T) {
	params := bindingParams(NewRoomID(), "cloned")
	params.SourceKind = RepositorySourceKindClone
	params.CloneURL = "https://example.invalid/repo.git"
	binding, err := NewRepositoryBinding(params)
	if err != nil {
		t.Fatal(err)
	}
	if binding.SourceKind() != RepositorySourceKindClone || binding.CloneURL() != "https://example.invalid/repo.git" {
		t.Fatalf("source kind=%q clone url=%q", binding.SourceKind(), binding.CloneURL())
	}
}

func TestRepositoryBindingRemoveCAS(t *testing.T) {
	params := bindingParams(NewRoomID(), "repo")
	created, err := NewRepositoryBinding(params)
	if err != nil {
		t.Fatal(err)
	}
	removed, err := created.Remove(1, created.UpdatedAt().Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if removed.State() != RepositoryBindingStateRemoved || removed.Version() != 2 || !removed.UpdatedAt().Equal(created.UpdatedAt().Add(time.Second)) {
		t.Fatalf("removed state=%q version=%d updated=%v", removed.State(), removed.Version(), removed.UpdatedAt())
	}
	if _, err := created.Remove(2, created.UpdatedAt().Add(time.Second)); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("stale version Remove() error = %v, want ErrInvalidArgument", err)
	}
	if _, err := created.Remove(1, created.UpdatedAt().Add(-time.Second)); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("non-monotonic Remove() error = %v, want ErrInvalidArgument", err)
	}
	if _, err := removed.Remove(2, removed.UpdatedAt().Add(time.Second)); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("double Remove() error = %v, want ErrInvalidArgument", err)
	}
}
