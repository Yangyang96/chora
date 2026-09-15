package isolatedenv

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/pidiscovery"
)

type catalogRunner struct {
	run func(context.Context, dockersupervisor.Command) (dockersupervisor.CommandResult, error)
}

func (r catalogRunner) Run(ctx context.Context, cmd dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
	return r.run(ctx, cmd)
}
func (r catalogRunner) Start(context.Context, dockersupervisor.Command) (dockersupervisor.Process, error) {
	return nil, errors.New("unexpected process start")
}

var catalogImage = "sha256:" + strings.Repeat("a", 64)

func TestDiscoverModelsUsesCredentialFreeFixedImage(t *testing.T) {
	runner := catalogRunner{run: func(ctx context.Context, cmd dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
		deadline, ok := ctx.Deadline()
		if !ok || time.Until(deadline) > 5*time.Second {
			t.Fatal("model probe has no bounded deadline")
		}
		want := []string{"run", "--rm", "--pull=never", "--network", "none", "--read-only", "--cap-drop=ALL", "--security-opt=no-new-privileges", "--user", "1000:1000", "--env", "PI_OFFLINE=1", "--entrypoint", "node", catalogImage, "--input-type=module", "-e", modelCatalogScript}
		if !reflect.DeepEqual(cmd.Args, want) || cmd.Stdin != nil {
			t.Fatalf("unexpected authority or input: %#v", cmd)
		}
		return dockersupervisor.CommandResult{Stdout: []byte(`[{"provider":"z","modelId":"b"},{"provider":"a","modelId":"z"},{"provider":"z","modelId":"a"},{"provider":"z","modelId":"b"}]`)}, nil
	}}
	models, err := DiscoverModels(context.Background(), runner, catalogImage)
	want := []pidiscovery.ModelOption{{Provider: "a", ModelID: "z"}, {Provider: "z", ModelID: "a"}, {Provider: "z", ModelID: "b"}}
	if err != nil || !reflect.DeepEqual(models, want) {
		t.Fatalf("models=%#v err=%v", models, err)
	}
}

func TestDiscoverModelsRejectsFailedAndInvalidCatalogs(t *testing.T) {
	tooMany := make([]pidiscovery.ModelOption, maxCatalogModels+1)
	for i := range tooMany {
		tooMany[i] = pidiscovery.ModelOption{Provider: "provider", ModelID: "model"}
	}
	tooManyJSON, _ := json.Marshal(tooMany)
	for _, tt := range []struct {
		name   string
		output string
		exit   int
		err    error
	}{
		{name: "runner failure", err: errors.New("failed")},
		{name: "nonzero", output: `[{"provider":"p","modelId":"m"}]`, exit: 1},
		{name: "empty"},
		{name: "no models", output: `[]`},
		{name: "null", output: `null`},
		{name: "diagnostic", output: `No models available`},
		{name: "partial", output: `[{"provider":"p","modelId":"m"}`},
		{name: "missing identity", output: `[{"provider":"p"}]`},
		{name: "unknown field", output: `[{"provider":"p","modelId":"m","secret":"x"}]`},
		{name: "trailing data", output: `[{"provider":"p","modelId":"m"}] []`},
		{name: "whitespace identity", output: `[{"provider":"p","modelId":"two words"}]`},
		{name: "control identity", output: `[{"provider":"p","modelId":"bad\u0000"}]`},
		{name: "oversized identity", output: `[{"provider":"p","modelId":"` + strings.Repeat("m", 513) + `"}]`},
		{name: "too many models", output: string(tooManyJSON)},
		{name: "oversized output", output: strings.Repeat(" ", 1<<20+1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			runner := catalogRunner{run: func(context.Context, dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
				return dockersupervisor.CommandResult{Stdout: []byte(tt.output), ExitCode: tt.exit}, tt.err
			}}
			if models, err := DiscoverModels(context.Background(), runner, catalogImage); err == nil || len(models) != 0 {
				t.Fatalf("accepted invalid catalog %#v: %v", models, err)
			}
		})
	}
}

func TestDiscoverModelsRejectsMutableImageAndHonorsCancellation(t *testing.T) {
	runner := catalogRunner{run: func(ctx context.Context, cmd dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
		<-ctx.Done()
		return dockersupervisor.CommandResult{}, ctx.Err()
	}}
	if _, err := DiscoverModels(context.Background(), runner, "runtime:latest"); err == nil {
		t.Fatal("accepted mutable image")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	if _, err := DiscoverModels(ctx, runner, catalogImage); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
}
