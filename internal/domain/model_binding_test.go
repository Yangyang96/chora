package domain

import (
	"testing"
	"time"
)

func TestModelBindingFreezesCatalogAndRejectsDrift(t *testing.T) {
	catalog, err := NewModelCatalog("agent", "runtime", "1", []ModelIdentity{{Provider: "p", ModelID: "m"}})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := NewModelBinding(catalog, ModelIdentity{Provider: "p", ModelID: "m"}, time.Unix(10, 0))
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.ValidateCatalog(catalog); err != nil {
		t.Fatal(err)
	}
	changed, err := NewModelCatalog("agent", "runtime-2", "1", catalog.Models)
	if err != nil {
		t.Fatal(err)
	}
	if err := binding.ValidateCatalog(changed); err == nil {
		t.Fatal("accepted runtime drift")
	}
	if _, err := NewModelBinding(catalog, ModelIdentity{Provider: "p", ModelID: "other"}, time.Now()); err == nil {
		t.Fatal("accepted unsupported model")
	}
}
