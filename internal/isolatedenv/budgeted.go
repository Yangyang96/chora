package isolatedenv

import (
	"errors"
	"strings"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/domain"
)

// BudgetedImageRecipe returns only public build inputs. It neither starts
// Docker nor promotes the parent's qualification to the new image. Flattening
// resets inherited image environment/entrypoint; actual installed closure,
// preserved permission extensions and effective state still require proof.
func BudgetedImageRecipe(parentImageID string) (map[string][]byte, error) {
	if !strings.HasPrefix(parentImageID, "sha256:") || !domain.CanonicalSHA256(strings.TrimPrefix(parentImageID, "sha256:")) {
		return nil, errors.New("exact parent image ID required")
	}
	files := map[string][]byte{}
	for _, name := range []string{"provider", "relay", "extension", "wrapper"} {
		body, err := agentpi.BudgetedAssets.ReadFile("budgeted/" + name + ".mjs")
		if err != nil {
			return nil, err
		}
		files[name+".mjs"] = body
	}
	files["Dockerfile"] = []byte("FROM " + parentImageID + " AS original\nFROM scratch\nCOPY --from=original / /\nCOPY chora-guardian /opt/chora/chora-guardian\nCOPY provider.mjs relay.mjs extension.mjs wrapper.mjs /opt/chora/budgeted/\nRUN [\"/bin/chmod\",\"0555\",\"/opt/chora/chora-guardian\"]\nRUN [\"/bin/chmod\",\"0444\",\"/opt/chora/budgeted/provider.mjs\",\"/opt/chora/budgeted/relay.mjs\",\"/opt/chora/budgeted/extension.mjs\",\"/opt/chora/budgeted/wrapper.mjs\"]\nENV PATH=/usr/local/bin:/usr/bin:/bin\nUSER 0:0\nWORKDIR /workspace/repository\nENTRYPOINT [\"/opt/chora/chora-guardian\"]\n")
	return files, nil
}
