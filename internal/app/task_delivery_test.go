package app

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	storecontract "github.com/Yangyang96/chora/internal/store"
	"github.com/Yangyang96/chora/internal/taskdelivery"
)

func TestDeliveryProjectionDoesNotDependOnOperationIDOrder(t *testing.T) {
	repo := domain.NewRepositoryID()
	now := time.Now()
	intent, _ := json.Marshal(deliveryIntent{Push: &taskdelivery.PushPreview{Remote: "origin", URL: "/test/remote.git"}})
	commit := storecontract.DeliveryOperation{ID: "z", RepositoryID: repo, State: "succeeded", Kind: "commit", CreatedAt: now, PreviewJSON: []byte(`{}`), OutcomeJSON: []byte(`{"Commit":"abc"}`)}
	push := storecontract.DeliveryOperation{ID: "a", RepositoryID: repo, State: "succeeded", Kind: "push", CreatedAt: now, PreviewJSON: intent, OutcomeJSON: []byte(`{}`)}
	pending := storecontract.DeliveryOperation{ID: "0", RepositoryID: repo, State: "recovery_required", Kind: "push", CreatedAt: now, PreviewJSON: intent, OutcomeJSON: []byte(`{}`)}
	for _, ops := range [][]storecontract.DeliveryOperation{{commit, push}, {push, commit}, {pending, push, commit}, {commit, push, pending}, {push, pending, commit}} {
		got, err := projectDeliveryOperations(RepositoryDeliveryView{RepoID: repo.String(), Status: "uncommitted"}, ops)
		want := "pushed"
		if len(ops) == 3 {
			want = "recovery_required"
		}
		if err != nil || got.Status != want || got.Commit != "abc" || got.Remote != "origin" {
			t.Fatalf("projection=%#v err=%v", got, err)
		}
	}
}

type capabilityHosting struct {
	taskdelivery.Hosting
	available bool
}

func (h capabilityHosting) Available() bool { return h.available }
func TestCleanupCapabilityRequiresFreshHostingObservation(t *testing.T) {
	for _, available := range []bool{false, true} {
		s := NewService(Dependencies{TaskDeliveryHosting: capabilityHosting{available: available}})
		c := s.hostingCapabilities()
		if c.Cleanup != available || c.Hosting != available {
			t.Fatalf("availability %v capabilities %#v", available, c)
		}
	}
	if NewService(Dependencies{}).hostingCapabilities().Cleanup {
		t.Fatal("nil hosting advertises cleanup")
	}
}
