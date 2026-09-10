package localweb

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

func TestResultClosureDoesNotInheritAnotherRunOrResultDelivery(t *testing.T) {
	for _, otherRun := range []bool{false, true} {
		t.Run(fmt.Sprintf("otherRun=%v", otherRun), func(t *testing.T) {
			ctx := context.Background()
			fixture := newResourceApplyIntegrationFixture(t, 2, 1)
			service := fixture.service(newRecordingResourceTarget())
			current := fixture.runs[0].run
			sourceRun := current
			if otherRun {
				var err error
				sourceRun, err = domain.RestoreAgentRun(domain.AgentRunRecord{ID: domain.NewRunID(), TaskID: current.TaskID(), CharterID: current.CharterID(), State: domain.RunStateAccepted, Version: 1, CreatedAt: current.CreatedAt(), UpdatedAt: current.UpdatedAt()})
				if err != nil {
					t.Fatal(err)
				}
				if err = fixture.server.store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error { return tx.InsertRun(ctx, sourceRun) }); err != nil {
					t.Fatal(err)
				}
			}
			req := closureRequest(current)
			before, err := service.PreviewResultClosure(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			digest := before.ResultDigest
			if !otherRun {
				digest = fmt.Sprintf("%x", sha256.Sum256([]byte("prior result")))
			}
			preview, _ := json.Marshal(map[string]string{"ResultDigest": digest})
			repo, err := domain.ParseRepositoryID(fixture.resources[0].RepoID)
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			operation := storecontract.DeliveryOperation{ID: "prior-result-commit", TaskID: current.TaskID(), RunID: sourceRun.ID(), RepositoryID: repo, Kind: "commit", State: "preview", Version: 1, RequestDigest: sha256.Sum256(preview), PreviewJSON: preview, OutcomeJSON: []byte(`{}`), CreatedAt: now, UpdatedAt: now}
			for _, state := range []string{"preview", "writing", "succeeded"} {
				operation.State = state
				if state == "succeeded" {
					operation.OutcomeJSON = []byte(`{"Commit":"prior-immutable-commit"}`)
				}
				if err = fixture.server.store.WithinWriteTx(ctx, func(tx storecontract.WriteTx) error {
					return tx.SaveDeliveryOperation(ctx, operation.Version-1, operation)
				}); err != nil {
					t.Fatal(err)
				}
				operation.Version++
			}
			fresh, err := service.PreviewResultClosure(ctx, req)
			if err != nil {
				t.Fatal(err)
			}
			if len(fresh.Entries) != 2 || fresh.Entries[0].Status != "eligible" || fresh.Entries[0].Achievement != "" {
				t.Fatalf("old delivery attributed to current Result: %#v", fresh)
			}
			req.ResultDigest, req.PreviewDigest = fresh.ResultDigest, fresh.PreviewDigest
			closed, err := service.CloseResult(ctx, req)
			if err != nil {
				t.Fatalf("old delivery blocked current closure at durable write: %v", err)
			}
			if closed.Entries[0].Status != "closed" || closed.Entries[1].Status != "closed" {
				t.Fatalf("closure=%#v", closed)
			}
			stored, err := fixture.server.store.Reader().GetDeliveryOperation(ctx, operation.ID)
			if err != nil || stored.State != "succeeded" || string(stored.OutcomeJSON) != `{"Commit":"prior-immutable-commit"}` {
				t.Fatal("prior delivery history changed", err)
			}
		})
	}
}
