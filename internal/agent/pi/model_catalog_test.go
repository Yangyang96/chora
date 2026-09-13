package pi

import "testing"

func TestSupportedModelsAreExplicitAndStable(t *testing.T) {
	models := SupportedModels()
	if len(models) == 0 {
		t.Fatal("supported model catalog is empty")
	}
	for _, model := range models {
		if model.Provider == "" || model.ModelID == "" || model.Digest == "" {
			t.Fatalf("incomplete model: %#v", model)
		}
		if !IsSupportedModel(model.Provider, model.ModelID) {
			t.Fatalf("catalog model rejected: %#v", model)
		}
	}
	if IsSupportedModel("openai-codex", "unsupported") {
		t.Fatal("unsupported model accepted")
	}
	models[0].ModelID = "mutated"
	if IsSupportedModel("openai-codex", "mutated") {
		t.Fatal("catalog mutation leaked")
	}
}
