package contextcore

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestRestoreAuthorizationReceipt(t *testing.T) {
	id, err := ParseAuthorizationID("promotion_authorization_00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatal(err)
	}
	binding := [32]byte{1}
	now := time.Now().UTC()
	receipt, err := RestoreAuthorizationReceipt(id, binding, now)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.ID() != id || receipt.Binding() != binding || !receipt.AuthorizedAt().Equal(now) {
		t.Fatal("receipt did not round trip")
	}
}

func TestRestoreSnapshotPreservesCanonicalRender(t *testing.T) {
	original := assembledSnapshotForRestore(t)
	snapshot, err := RestoreSnapshot(SnapshotRecord{ID: original.ID(), Digest: original.Digest(), CanonicalJSON: original.CanonicalJSON(), Markdown: original.Markdown(), IncludedRevisionIDs: original.IncludedRevisionIDs()})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshot.CanonicalJSON(), original.CanonicalJSON()) || !bytes.Equal(snapshot.Markdown(), original.Markdown()) || snapshot.Digest() != original.Digest() {
		t.Fatal("snapshot render did not round trip")
	}
}

func TestRestoreSnapshotRejectsMarkdownTamper(t *testing.T) {
	original := assembledSnapshotForRestore(t)
	markdown := original.Markdown()
	markdown = append(markdown, []byte("tampered")...)
	if _, err := RestoreSnapshot(SnapshotRecord{ID: original.ID(), Digest: original.Digest(), CanonicalJSON: original.CanonicalJSON(), Markdown: markdown, IncludedRevisionIDs: original.IncludedRevisionIDs()}); err == nil {
		t.Fatal("tampered markdown accepted")
	}
}

func assembledSnapshotForRestore(t *testing.T) Snapshot {
	t.Helper()
	fixture := newAssemblyFixture(t, false)
	port := &fakePort{revisions: []domain.RoomContextRevision{fixture.brief, fixture.decision, fixture.constraint, fixture.reference, fixture.unknown}}
	snapshot, err := NewAssembler(port).Assemble(context.Background(), AssembleRequest{SnapshotID: fixture.snapshotID, Task: fixture.task, Charter: fixture.charter})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestRestoreAuthorizationReceiptRejectsInvalid(t *testing.T) {
	if _, err := RestoreAuthorizationReceipt(AuthorizationID{}, [32]byte{1}, time.Now()); err == nil {
		t.Fatal("expected invalid ID")
	}
}
