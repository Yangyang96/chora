package sqlite_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestGetSnapshotRejectsPersistedRenderAndJoinTampering(t *testing.T) {
	for _, tc := range []string{"markdown", "inclusions"} {
		t.Run(tc, func(t *testing.T) {
			db, seeded := openSeeded(t)
			defer db.Close()
			snapshot := assembleSeedSnapshot(t, db, seeded)
			raw, err := sql.Open("sqlite", seeded.path)
			if err != nil {
				t.Fatal(err)
			}
			defer raw.Close()
			switch tc {
			case "markdown":
				_, err = raw.Exec(`UPDATE context_snapshots SET markdown=markdown||'tampered' WHERE id=?`, snapshot.ID().String())
			case "inclusions":
				_, err = raw.Exec(`DELETE FROM snapshot_inclusions WHERE snapshot_id=?`, snapshot.ID().String())
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := db.Reader().GetSnapshot(context.Background(), snapshot.ID()); err == nil {
				t.Fatalf("%s tamper accepted", tc)
			}
		})
	}
}

func TestAttemptRejectsSnapshotDigestMismatch(t *testing.T) {
	db, seeded := openSeeded(t)
	defer db.Close()
	snapshot := assembleSeedSnapshot(t, db, seeded)
	digest := snapshot.Digest()
	digest[0] ^= 0xff
	attempt, err := domain.NewAttempt(domain.AttemptParams{ID: domain.NewAttemptID(), RunID: seeded.run.ID(), Sequence: 1, ContextSnapshotID: snapshot.ID(), ContextDigest: digest, AdapterID: "codex", CreatedAt: seeded.now})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.WithinWriteTx(context.Background(), func(tx storecontract.WriteTx) error { return tx.InsertAttempt(context.Background(), attempt) }); err == nil {
		t.Fatal("snapshot digest mismatch accepted")
	}
}
