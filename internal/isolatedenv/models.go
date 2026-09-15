package isolatedenv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/pidiscovery"
)

// The frozen Pi SDK exposes the built-in catalog separately from availability.
// In-memory credentials and models avoid reading any user's auth/configuration;
// skipping refresh avoids the authenticated/network discovery used by --list-models.
const modelCatalogScript = `import { ModelRuntime } from "/opt/pi/node_modules/@earendil-works/pi-coding-agent/dist/core/model-runtime.js";
import { AuthStorage } from "/opt/pi/node_modules/@earendil-works/pi-coding-agent/dist/core/auth-storage.js";
const runtime = await ModelRuntime.create({credentials: AuthStorage.inMemory(), modelsPath: null, refreshOnCreate: false, allowModelNetwork: false});
if (runtime.getError()) throw new Error("invalid runtime catalog");
console.log(JSON.stringify(runtime.getModels().map(model => ({provider: model.provider, modelId: model.id}))));`

const maxCatalogModels = 10000

var imageIdentityPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

// DiscoverModels reads model capabilities from a fixed isolated runtime image.
// These capabilities do not assert provider credential or network readiness.
func DiscoverModels(ctx context.Context, runner dockersupervisor.CommandRunner, imageID string) ([]pidiscovery.ModelOption, error) {
	if runner == nil || !imageIdentityPattern.MatchString(imageID) {
		return nil, errors.New("isolated model discovery requires a runner and immutable image identity")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	result, err := runner.Run(ctx, dockersupervisor.Command{Args: []string{
		"run", "--rm", "--pull=never", "--network", "none", "--read-only",
		"--cap-drop=ALL", "--security-opt=no-new-privileges", "--user", "1000:1000",
		"--env", "PI_OFFLINE=1", "--entrypoint", "node", imageID,
		"--input-type=module", "-e", modelCatalogScript,
	}})
	if ctx.Err() != nil {
		return nil, fmt.Errorf("discover isolated Pi model capabilities: %w", ctx.Err())
	}
	if err != nil {
		return nil, fmt.Errorf("discover isolated Pi model capabilities: %w", err)
	}
	if result.ExitCode != 0 {
		return nil, fmt.Errorf("isolated Pi model discovery exited with status %d", result.ExitCode)
	}
	if len(result.Stdout) == 0 || len(result.Stdout) > 1<<20 {
		return nil, errors.New("isolated Pi model capability output is empty or oversized")
	}
	decoder := json.NewDecoder(bytes.NewReader(result.Stdout))
	decoder.DisallowUnknownFields()
	var models []pidiscovery.ModelOption
	if err := decoder.Decode(&models); err != nil {
		return nil, errors.New("isolated Pi model capability output is invalid")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, errors.New("isolated Pi model capability output contains trailing data")
	}
	return validateModels(models)
}

func validateModels(models []pidiscovery.ModelOption) ([]pidiscovery.ModelOption, error) {
	if len(models) == 0 || len(models) > maxCatalogModels {
		return nil, errors.New("isolated Pi model capability count is invalid")
	}
	out := make([]pidiscovery.ModelOption, 0, len(models))
	seen := make(map[pidiscovery.ModelOption]bool)
	for _, model := range models {
		if !validModelIdentity(model.Provider) || !validModelIdentity(model.ModelID) {
			return nil, errors.New("isolated Pi model capability identity is invalid")
		}
		if !seen[model] {
			out = append(out, model)
			seen[model] = true
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].ModelID < out[j].ModelID
	})
	return out, nil
}

func validModelIdentity(value string) bool {
	return value != "" && len(value) <= 512 && !strings.ContainsFunc(value, func(r rune) bool {
		return unicode.IsSpace(r) || unicode.IsControl(r) || r == unicode.ReplacementChar
	})
}
