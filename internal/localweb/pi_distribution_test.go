package localweb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	agentpi "github.com/Yangyang96/chora/internal/agent/pi"
	"github.com/Yangyang96/chora/internal/pidistribution"
)

func TestLocalPiSourceConversionPreservesCompletePackageClosureIdentity(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "package")
	bin := filepath.Join(root, "bin")
	path := filepath.Join(bin, "pi")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"@earendil-works/pi-coding-agent","version":"0.84.2"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture Pi executable"), 0o700); err != nil {
		t.Fatal(err)
	}

	resolver, err := pidistribution.NewResolver(pidistribution.Config{
		LookPath: func(name string) (string, error) {
			if name != pidistribution.ExecutableName {
				return "", errors.New("unexpected executable lookup")
			}
			return path, nil
		},
		RunVersion: func(_ context.Context, executable string, arguments ...string) (pidistribution.VersionResult, error) {
			if executable != path || len(arguments) != 1 || arguments[0] != "--version" {
				return pidistribution.VersionResult{}, errors.New("unexpected version probe")
			}
			return pidistribution.VersionResult{Stdout: []byte(pidistribution.SupportedVersion + "\n")}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	source, err := localPiSourceFromSelection(selection)
	if err != nil {
		t.Fatal(err)
	}
	data := selection.LocalPiSourceData()
	if source.ExecutablePath() != data.ExecutablePath || source.ResolvedExecutablePath() != data.ResolvedPath ||
		source.PackageRoot() != data.PackageRoot || source.RuntimeVersion() != data.RuntimeVersion ||
		source.ExecutableSHA256() != data.ExecutableSHA256 || source.ClosureSHA256() != data.ClosureSHA256 {
		t.Fatal("distribution selection was converted lossily")
	}
	validator := localPiSelectionValidator(resolver, selection)
	if err := validator(context.Background(), source); err != nil {
		t.Fatalf("unchanged complete package closure failed validation: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"@earendil-works/pi-coding-agent","version":"0.84.2","drift":true}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validator(context.Background(), source); err == nil {
		t.Fatal("package closure drift passed validation")
	}
}

func TestLocalPiSourceConversionRejectsUnavailableSelection(t *testing.T) {
	if _, err := localPiSourceFromSelection(pidistribution.Selection{}); err == nil {
		t.Fatal("zero distribution selection was converted")
	}
	validator := localPiSelectionValidator(nil, pidistribution.Selection{})
	if err := validator(context.Background(), agentpi.LocalPiSource{}); err == nil {
		t.Fatal("unconfigured selection validator passed")
	}
}
