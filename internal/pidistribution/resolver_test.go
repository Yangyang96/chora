package pidistribution

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestPATHCompatiblePackageAlwaysWins(t *testing.T) {
	fixture := newPackageFixture(t)
	privateRoot := filepath.Join(t.TempDir(), "private")
	var calls atomic.Int32
	selection, err := Resolve(context.Background(), Config{
		LookPath: func(name string) (string, error) {
			if name != ExecutableName {
				t.Fatalf("lookup name = %q", name)
			}
			return fixture.executable, nil
		},
		RunVersion: func(_ context.Context, executable string, arguments ...string) (VersionResult, error) {
			calls.Add(1)
			if executable != fixture.executable || len(arguments) != 1 || arguments[0] != "--version" {
				t.Fatalf("probe = %q %#v", executable, arguments)
			}
			return VersionResult{Stdout: []byte(SupportedVersion + "\n")}, nil
		},
		PrivateRoot: privateRoot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Kind() != SelectionPATH || selection.PackageRoot() != fixture.root || selection.Path() != fixture.executable || selection.ResolvedPath() != fixture.executable {
		t.Fatalf("selection = %#v", selection.Record())
	}
	if calls.Load() != 1 {
		t.Fatalf("version calls = %d", calls.Load())
	}
	if _, err := os.Lstat(privateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private fallback was touched: %v", err)
	}
	restored, err := RestoreSelection(selection.Record())
	if err != nil || restored.Record() != selection.Record() || restored.LocalPiSourceData().ClosureSHA256 != selection.ClosureSHA256() {
		t.Fatalf("restore/data = %#v, %v", restored.Record(), err)
	}
	tampered := selection.Record()
	tampered.ClosureSHA256[0] ^= 1
	if _, err := RestoreSelection(tampered); !errors.Is(err, ErrSelectionDrift) {
		t.Fatalf("tampered restore err = %v", err)
	}
}

func TestPATHRejectionsFailClosedWithoutPinnedAsset(t *testing.T) {
	fixture := newPackageFixture(t)
	tests := map[string]VersionRunner{
		"wrong version": func(context.Context, string, ...string) (VersionResult, error) {
			return VersionResult{Stdout: []byte("0.84.1\n")}, nil
		},
		"malformed extra newline": func(context.Context, string, ...string) (VersionResult, error) {
			return VersionResult{Stdout: []byte(SupportedVersion + "\n\n")}, nil
		},
		"stderr": func(context.Context, string, ...string) (VersionResult, error) {
			return VersionResult{Stdout: []byte(SupportedVersion), Stderr: []byte("warning")}, nil
		},
		"exit": func(context.Context, string, ...string) (VersionResult, error) {
			return VersionResult{Stdout: []byte(SupportedVersion), ExitCode: 2}, nil
		},
		"timeout": func(ctx context.Context, _ string, _ ...string) (VersionResult, error) {
			<-ctx.Done()
			return VersionResult{}, ctx.Err()
		},
	}
	for name, runner := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Resolve(context.Background(), Config{
				LookPath:   func(string) (string, error) { return fixture.executable, nil },
				RunVersion: runner, VersionTimeout: time.Millisecond,
			})
			if !errors.Is(err, ErrNoUsablePi) || !errors.Is(err, ErrPrivateAssetUnavailable) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestPATHClosureDriftFails(t *testing.T) {
	fixture := newPackageFixture(t)
	_, err := Resolve(context.Background(), Config{
		LookPath: func(string) (string, error) { return fixture.executable, nil },
		RunVersion: func(context.Context, string, ...string) (VersionResult, error) {
			mustWriteFile(t, filepath.Join(fixture.root, "lib", "dependency.js"), []byte("drift"), 0o600)
			return VersionResult{Stdout: []byte(SupportedVersion)}, nil
		},
	})
	if !errors.Is(err, ErrNoUsablePi) || !errors.Is(err, ErrSelectionDrift) {
		t.Fatalf("err = %v", err)
	}
}

func TestRevalidateDoesNotDiscoverOrFallbackAndDetectsDrift(t *testing.T) {
	fixture := newPackageFixture(t)
	var lookups atomic.Int32
	resolver, err := NewResolver(Config{
		LookPath: func(string) (string, error) {
			lookups.Add(1)
			return fixture.executable, nil
		},
		RunVersion: func(context.Context, string, ...string) (VersionResult, error) {
			return VersionResult{Stdout: []byte(SupportedVersion)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.Revalidate(context.Background(), selection); err != nil {
		t.Fatal(err)
	}
	if lookups.Load() != 1 {
		t.Fatalf("revalidation performed discovery: lookups = %d", lookups.Load())
	}
	mustWriteFile(t, filepath.Join(fixture.root, "lib", "dependency.js"), []byte("drift"), 0o600)
	if err := resolver.Revalidate(context.Background(), selection); !errors.Is(err, ErrSelectionDrift) {
		t.Fatalf("drift err = %v", err)
	}
}

func TestRevalidatePrivateRequiresUnchangedCompleteMarker(t *testing.T) {
	fixture := newPackageFixture(t)
	asset := fixture.privateAsset(t)
	resolver, err := NewResolver(privateConfig(t, asset, filepath.Join(t.TempDir(), "private")))
	if err != nil {
		t.Fatal(err)
	}
	selection, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := resolver.Revalidate(context.Background(), selection); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(selection.PackageRoot(), ".complete"), []byte("{}\n"), 0o600)
	if err := resolver.Revalidate(context.Background(), selection); !errors.Is(err, ErrSelectionDrift) {
		t.Fatalf("marker drift err = %v", err)
	}
}

func TestPrivateFallbackRequiresExactManifestPin(t *testing.T) {
	fixture := newPackageFixture(t)
	asset := fixture.privateAsset(t)
	asset.ExpectedManifestSHA256 = [32]byte{}
	_, err := Resolve(context.Background(), privateConfig(t, asset, filepath.Join(t.TempDir(), "private")))
	if !errors.Is(err, ErrPrivateAssetUnavailable) {
		t.Fatalf("zero pin err = %v", err)
	}
	asset = fixture.privateAsset(t)
	asset.ExpectedManifestSHA256[0] ^= 1
	_, err = Resolve(context.Background(), privateConfig(t, asset, filepath.Join(t.TempDir(), "private")))
	if !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("wrong pin err = %v", err)
	}
}

func TestIncompatiblePATHUsesAuthenticatedPrivateFallback(t *testing.T) {
	fixture := newPackageFixture(t)
	asset := fixture.privateAsset(t)
	privateRoot := canonicalFuturePath(t, filepath.Join(t.TempDir(), "private"))
	selection, err := Resolve(context.Background(), Config{
		LookPath: func(string) (string, error) { return fixture.executable, nil },
		RunVersion: func(_ context.Context, executable string, arguments ...string) (VersionResult, error) {
			if len(arguments) != 1 || arguments[0] != "--version" {
				t.Fatalf("arguments = %#v", arguments)
			}
			if executable == fixture.executable {
				return VersionResult{Stdout: []byte("0.84.1")}, nil
			}
			return VersionResult{Stdout: []byte(SupportedVersion)}, nil
		},
		PrivateRoot: privateRoot, PrivateAsset: asset,
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Kind() != SelectionPrivate || !pathWithin(privateRoot, selection.Path()) {
		t.Fatalf("selection = %#v", selection.Record())
	}
	if data, err := os.ReadFile(fixture.executable); err != nil || string(data) != "fixture executable" {
		t.Fatalf("PATH executable was mutated: %q, %v", data, err)
	}
}

func TestPrivateInstallCompleteReuseAndCrashedStage(t *testing.T) {
	fixture := newPackageFixture(t)
	asset := fixture.privateAsset(t)
	parent := t.TempDir()
	privateRoot := filepath.Join(parent, "private")
	if err := os.Mkdir(privateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	crashed := filepath.Join(privateRoot, ".stage-crashed")
	if err := os.Mkdir(crashed, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(crashed, "partial"), []byte("keep"), 0o600)
	config := privateConfig(t, asset, privateRoot)
	first, err := Resolve(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	if first.Kind() != SelectionPrivate || filepath.Base(first.PackageRoot()) != digestText(first.ClosureSHA256()) {
		t.Fatalf("first = %#v", first.Record())
	}
	marker := filepath.Join(first.PackageRoot(), ".complete")
	if info, err := os.Stat(marker); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("complete marker = %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(crashed, "partial")); err != nil {
		t.Fatalf("crashed stage was not preserved: %v", err)
	}
	second, err := Resolve(context.Background(), config)
	if err != nil || second.Record() != first.Record() {
		t.Fatalf("reuse = %#v, %v", second.Record(), err)
	}
}

func TestConcurrentPrivateInstallConverges(t *testing.T) {
	fixture := newPackageFixture(t)
	asset := fixture.privateAsset(t)
	privateRoot := filepath.Join(t.TempDir(), "private")
	config := privateConfig(t, asset, privateRoot)
	var wait sync.WaitGroup
	wait.Add(2)
	selections := make([]Selection, 2)
	errorsSeen := make([]error, 2)
	for index := range selections {
		go func() {
			defer wait.Done()
			selections[index], errorsSeen[index] = Resolve(context.Background(), config)
		}()
	}
	wait.Wait()
	if errorsSeen[0] != nil || errorsSeen[1] != nil || selections[0].Record() != selections[1].Record() {
		t.Fatalf("concurrent = %#v / %#v, errors %v / %v", selections[0].Record(), selections[1].Record(), errorsSeen[0], errorsSeen[1])
	}
}

func TestUnknownFinalTargetIsPreserved(t *testing.T) {
	fixture := newPackageFixture(t)
	asset := fixture.privateAsset(t)
	manifest, err := authenticateManifest(asset, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatal(err)
	}
	privateRoot := filepath.Join(t.TempDir(), "private")
	if err := os.Mkdir(privateRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(privateRoot, digestText(manifest.closure))
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(target, "unknown")
	mustWriteFile(t, unknown, []byte("preserve"), 0o600)
	_, err = Resolve(context.Background(), privateConfig(t, asset, privateRoot))
	if err == nil {
		t.Fatal("unknown target was accepted")
	}
	if data, readErr := os.ReadFile(unknown); readErr != nil || string(data) != "preserve" {
		t.Fatalf("unknown target changed: %q, %v", data, readErr)
	}
}

func TestCompletedTargetWithUnknownFileFailsClosedAndIsPreserved(t *testing.T) {
	fixture := newPackageFixture(t)
	asset := fixture.privateAsset(t)
	privateRoot := filepath.Join(t.TempDir(), "private")
	config := privateConfig(t, asset, privateRoot)
	selection, err := Resolve(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(selection.PackageRoot(), "unknown")
	mustWriteFile(t, unknown, []byte("preserve"), 0o600)
	if _, err := Resolve(context.Background(), config); err == nil {
		t.Fatal("target with extra file was reused")
	}
	if data, err := os.ReadFile(unknown); err != nil || string(data) != "preserve" {
		t.Fatalf("unknown file changed: %q, %v", data, err)
	}
}

func TestManifestStrictJSON(t *testing.T) {
	fixture := newPackageFixture(t)
	asset := fixture.privateAsset(t)
	for name, mutate := range map[string]func([]byte) []byte{
		"trailing": func(value []byte) []byte { return append(append([]byte(nil), value...), []byte(` {}`)...) },
		"unknown": func(value []byte) []byte {
			return append(append([]byte(nil), value[:len(value)-1]...), []byte(`,"unknown":true}`)...)
		},
		"duplicate": func(value []byte) []byte {
			return append([]byte(`{"schema_version":"duplicate",`), value[1:]...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			copyAsset := *asset
			copyAsset.ManifestBytes = mutate(asset.ManifestBytes)
			copyAsset.ExpectedManifestSHA256 = sha256.Sum256(copyAsset.ManifestBytes)
			if _, err := authenticateManifest(&copyAsset, runtime.GOOS, runtime.GOARCH); !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func privateConfig(t *testing.T, asset *PrivateAsset, privateRoot string) Config {
	t.Helper()
	privateRoot = canonicalFuturePath(t, privateRoot)
	return Config{
		LookPath: func(string) (string, error) { return "", exec.ErrNotFound },
		RunVersion: func(_ context.Context, executable string, arguments ...string) (VersionResult, error) {
			if !pathWithin(privateRoot, executable) || len(arguments) != 1 || arguments[0] != "--version" {
				t.Fatalf("private probe = %q %#v", executable, arguments)
			}
			return VersionResult{Stdout: []byte(SupportedVersion)}, nil
		},
		PrivateRoot: privateRoot, PrivateAsset: asset,
	}
}

type packageFixture struct {
	root       string
	executable string
}

func newPackageFixture(t *testing.T) packageFixture {
	t.Helper()
	root := filepath.Join(canonicalExistingPath(t, t.TempDir()), "package")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "lib"), 0o700); err != nil {
		t.Fatal(err)
	}
	packageJSON, _ := json.Marshal(map[string]any{"name": ExpectedPackageName, "version": SupportedVersion, "private": true})
	mustWriteFile(t, filepath.Join(root, "package.json"), packageJSON, 0o600)
	executable := filepath.Join(root, "bin", "pi")
	mustWriteFile(t, executable, []byte("fixture executable"), 0o700)
	mustWriteFile(t, filepath.Join(root, "lib", "dependency.js"), []byte("fixture dependency"), 0o600)
	return packageFixture{root: root, executable: executable}
}

func canonicalExistingPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func canonicalFuturePath(t *testing.T, path string) string {
	t.Helper()
	parent := canonicalExistingPath(t, filepath.Dir(path))
	return filepath.Join(parent, filepath.Base(path))
}

func (fixture packageFixture) privateAsset(t *testing.T) *PrivateAsset {
	t.Helper()
	observed, err := inspectClosure(fixture.root, nil, "")
	if err != nil {
		t.Fatal(err)
	}
	manifest := Manifest{
		SchemaVersion: ManifestSchema,
		Platform:      ManifestPlatform{OS: runtime.GOOS, Architecture: runtime.GOARCH},
		PackageName:   ExpectedPackageName, Version: SupportedVersion,
		Executable: "bin/pi", Files: observed.files, ClosureSHA256: digestText(observed.digest),
	}
	document, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return &PrivateAsset{ManifestBytes: document, ExpectedManifestSHA256: sha256.Sum256(document), SourceRoot: fixture.root}
}

func mustWriteFile(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}
