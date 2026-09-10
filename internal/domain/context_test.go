package domain

import (
	"errors"
	"reflect"
	"testing"
	"time"
)

func TestRoomContextRevisionIsImmutableAndValidated(t *testing.T) {
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	previous := NewContextRevisionID()
	revision, err := NewRoomContextRevision(RoomContextRevisionParams{
		EntryID: NewContextEntryID(), RevisionID: NewContextRevisionID(), RoomID: NewRoomID(),
		Kind: ContextKindDecision, RevisionNumber: 2, Title: "Runtime", Body: "Use a fake adapter first",
		Locator: "docs/design.md", Sensitive: true, Supersedes: &previous, CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		t.Fatalf("NewRoomContextRevision() error = %v", err)
	}
	if revision.Kind() != ContextKindDecision || revision.RevisionNumber() != 2 || !revision.Sensitive() {
		t.Fatalf("revision fields not preserved: %#v", revision)
	}
	if got, ok := revision.Supersedes(); !ok || got != previous {
		t.Fatalf("Supersedes() = %v, %v; want %v, true", got, ok, previous)
	}

	for _, kind := range []ContextKind{ContextKindBrief, ContextKindDecision, ContextKindConstraint, ContextKindSourceRef, ContextKindUnknown} {
		params := RoomContextRevisionParams{EntryID: NewContextEntryID(), RevisionID: NewContextRevisionID(), RoomID: NewRoomID(), Kind: kind, RevisionNumber: 1, Title: "title", Body: "body", CreatedAt: now, UpdatedAt: now}
		if _, err := NewRoomContextRevision(params); err != nil {
			t.Fatalf("kind %q rejected: %v", kind, err)
		}
	}
}

func TestRoomContextRevisionRejectsInvalidKindAndRevision(t *testing.T) {
	now := time.Now().UTC()
	base := RoomContextRevisionParams{EntryID: NewContextEntryID(), RevisionID: NewContextRevisionID(), RoomID: NewRoomID(), Kind: ContextKindBrief, RevisionNumber: 1, Title: "title", Body: "body", CreatedAt: now, UpdatedAt: now}
	tests := []struct {
		name   string
		mutate func(*RoomContextRevisionParams)
	}{
		{"invalid kind", func(params *RoomContextRevisionParams) { params.Kind = "generic" }},
		{"zero revision", func(params *RoomContextRevisionParams) { params.RevisionNumber = 0 }},
		{"missing title", func(params *RoomContextRevisionParams) { params.Title = "" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			params := base
			test.mutate(&params)
			if _, err := NewRoomContextRevision(params); !errors.Is(err, ErrInvalidArgument) {
				t.Fatalf("error = %v, want ErrInvalidArgument", err)
			}
		})
	}
}

func TestRunContextSnapshotCopiesReferencesAndRejectsZeroDigest(t *testing.T) {
	digest := [32]byte{1, 2, 3}
	included := []ContextRevisionID{NewContextRevisionID()}
	excluded := []ContextEntryID{NewContextEntryID()}
	wantIncluded := included[0]
	wantExcluded := excluded[0]
	snapshot, err := NewRunContextSnapshot(NewContextSnapshotID(), digest, included, excluded)
	if err != nil {
		t.Fatalf("NewRunContextSnapshot() error = %v", err)
	}
	if snapshot.Digest() != digest {
		t.Fatalf("digest = %x, want %x", snapshot.Digest(), digest)
	}
	included[0] = NewContextRevisionID()
	excluded[0] = NewContextEntryID()
	if snapshot.IncludedRevisionIDs()[0] != wantIncluded || snapshot.ExcludedEntryIDs()[0] != wantExcluded {
		t.Fatal("snapshot aliases caller-owned ID slices")
	}
	returnedIncluded := snapshot.IncludedRevisionIDs()
	returnedExcluded := snapshot.ExcludedEntryIDs()
	returnedIncluded[0] = NewContextRevisionID()
	returnedExcluded[0] = NewContextEntryID()
	if snapshot.IncludedRevisionIDs()[0] != wantIncluded || snapshot.ExcludedEntryIDs()[0] != wantExcluded {
		t.Fatal("snapshot getters expose mutable ID slices")
	}
	if _, err := NewRunContextSnapshot(NewContextSnapshotID(), [32]byte{}, []ContextRevisionID{NewContextRevisionID()}, nil); !errors.Is(err, ErrInvalidArgument) {
		t.Fatalf("zero digest error = %v, want ErrInvalidArgument", err)
	}
}

func TestRunContextSnapshotAllowsOnlyExcludedContext(t *testing.T) {
	digest := [32]byte{1}
	excluded := NewContextEntryID()
	snapshot, err := NewRunContextSnapshot(NewContextSnapshotID(), digest, nil, []ContextEntryID{excluded})
	if err != nil {
		t.Fatalf("NewRunContextSnapshot() error = %v", err)
	}
	if len(snapshot.IncludedRevisionIDs()) != 0 || len(snapshot.ExcludedEntryIDs()) != 1 || snapshot.ExcludedEntryIDs()[0] != excluded {
		t.Fatalf("snapshot included=%v excluded=%v", snapshot.IncludedRevisionIDs(), snapshot.ExcludedEntryIDs())
	}
}

func TestRunContextSnapshotAPIContainsNoContentCanonicalization(t *testing.T) {
	constructor := reflect.TypeOf(NewRunContextSnapshot)
	if constructor.NumIn() != 4 {
		t.Fatalf("NewRunContextSnapshot input count = %d, want ID, digest, included IDs, excluded IDs", constructor.NumIn())
	}
	if constructor.In(1) != reflect.TypeOf([32]byte{}) {
		t.Fatalf("NewRunContextSnapshot second input = %v, want [32]byte digest", constructor.In(1))
	}
	snapshotType := reflect.TypeOf(RunContextSnapshot{})
	for _, method := range []string{"CanonicalJSON", "RenderedMarkdown"} {
		if _, exists := snapshotType.MethodByName(method); exists {
			t.Fatalf("RunContextSnapshot unexpectedly exposes %s", method)
		}
	}
}
