package pidistribution

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateLifecycleNeverSelectsOrRemovesPATHPiOrSibling(t *testing.T) {
	fixture := newPackageFixture(t)
	asset := fixture.privateAsset(t)
	privateRoot := canonicalFuturePath(t, filepath.Join(t.TempDir(), "private"))
	resolver, err := NewResolver(Config{
		LookPath: func(string) (string, error) { return fixture.executable, nil },
		RunVersion: func(_ context.Context, executable string, _ ...string) (VersionResult, error) {
			return VersionResult{Stdout: []byte(SupportedVersion)}, nil
		},
		PrivateRoot: privateRoot, PrivateAsset: asset,
	})
	if err != nil {
		t.Fatal(err)
	}
	binding, err := BindPrivateAsset(asset, "", "")
	if err != nil || binding.ManifestSHA256 == "" || binding.ClosureSHA256 == "" {
		t.Fatalf("BindPrivateAsset() = (%+v, %v)", binding, err)
	}
	selection, err := resolver.InstallPrivate(context.Background())
	if err != nil || selection.Kind() != SelectionPrivate || filepath.Base(selection.PackageRoot()) != binding.ClosureSHA256 {
		t.Fatalf("InstallPrivate() = (%+v, %v)", selection.Record(), err)
	}
	sibling := filepath.Join(privateRoot, "unrelated-private-sibling")
	if err := os.Mkdir(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(sibling, "keep"), []byte("keep"), 0o600)
	worktree := filepath.Join(filepath.Dir(privateRoot), "worktree")
	evidence := filepath.Join(filepath.Dir(privateRoot), "evidence")
	for _, preserved := range []string{worktree, evidence} {
		if err := os.Mkdir(preserved, 0o700); err != nil {
			t.Fatal(err)
		}
		mustWriteFile(t, filepath.Join(preserved, "keep"), []byte("keep"), 0o600)
	}
	observed, present, err := resolver.InspectPrivate(context.Background())
	if err != nil || !present || observed.Record() != selection.Record() {
		t.Fatalf("InspectPrivate() = (%+v, %t, %v)", observed.Record(), present, err)
	}
	if err := resolver.RemovePrivate(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(selection.PackageRoot()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private closure remains: %v", err)
	}
	if data, err := os.ReadFile(fixture.executable); err != nil || string(data) != "fixture executable" {
		t.Fatalf("PATH Pi changed: %q / %v", data, err)
	}
	if data, err := os.ReadFile(filepath.Join(sibling, "keep")); err != nil || string(data) != "keep" {
		t.Fatalf("private sibling changed: %q / %v", data, err)
	}
	for _, preserved := range []string{worktree, evidence} {
		if data, err := os.ReadFile(filepath.Join(preserved, "keep")); err != nil || string(data) != "keep" {
			t.Fatalf("external product data changed at %s: %q / %v", preserved, data, err)
		}
	}
	if _, present, err := resolver.InspectPrivate(context.Background()); err != nil || present {
		t.Fatalf("post-remove InspectPrivate() = present=%t err=%v", present, err)
	}
}

func TestPrivateBindingRejectsChecksumMismatchWithoutMutation(t *testing.T) {
	fixture := newPackageFixture(t)
	asset := fixture.privateAsset(t)
	asset.ExpectedManifestSHA256[0] ^= 1
	if _, err := BindPrivateAsset(asset, "", ""); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("BindPrivateAsset() error = %v", err)
	}
}

func TestCompatiblePATHInspectionNeverCreatesPrivateInstall(t *testing.T) {
	fixture := newPackageFixture(t)
	privateRoot := canonicalFuturePath(t, filepath.Join(t.TempDir(), "private"))
	resolver, err := NewResolver(Config{
		LookPath: func(string) (string, error) { return fixture.executable, nil },
		RunVersion: func(context.Context, string, ...string) (VersionResult, error) {
			return VersionResult{Stdout: []byte(SupportedVersion)}, nil
		},
		PrivateRoot: privateRoot, PrivateAsset: fixture.privateAsset(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := resolver.InspectCompatiblePATH(context.Background())
	if err != nil || selection.Kind() != SelectionPATH {
		t.Fatalf("InspectCompatiblePATH() = %+v / %v", selection.Record(), err)
	}
	if _, err := os.Lstat(privateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PATH compatibility inspection created private install: %v", err)
	}
}

func TestPATHDriftInstallsAuthenticatedPrivateFallbackWithoutChangingPATHPackage(t *testing.T) {
	fixture := newPackageFixture(t)
	privateRoot := canonicalFuturePath(t, filepath.Join(t.TempDir(), "private"))
	compatible := true
	resolver, err := NewResolver(Config{
		LookPath: func(string) (string, error) { return fixture.executable, nil },
		RunVersion: func(_ context.Context, executable string, _ ...string) (VersionResult, error) {
			if executable == fixture.executable && !compatible {
				return VersionResult{Stdout: []byte("0.84.1")}, nil
			}
			return VersionResult{Stdout: []byte(SupportedVersion)}, nil
		},
		PrivateRoot: privateRoot, PrivateAsset: fixture.privateAsset(t),
	})
	if err != nil {
		t.Fatal(err)
	}
	initial, err := resolver.InspectCompatiblePATH(context.Background())
	if err != nil || initial.Kind() != SelectionPATH {
		t.Fatalf("initial PATH selection = %+v / %v", initial.Record(), err)
	}
	if _, err := os.Lstat(privateRoot); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("initial compatible PATH eagerly installed private fallback: %v", err)
	}
	compatible = false
	if _, err := resolver.InspectCompatiblePATH(context.Background()); err == nil {
		t.Fatal("drifted PATH remained compatible")
	}
	fallback, err := resolver.InstallPrivate(context.Background())
	if err != nil || fallback.Kind() != SelectionPrivate || !pathWithin(privateRoot, fallback.Path()) {
		t.Fatalf("drift fallback = %+v / %v", fallback.Record(), err)
	}
	if data, err := os.ReadFile(fixture.executable); err != nil || string(data) != "fixture executable" {
		t.Fatalf("PATH package changed while installing fallback: %q / %v", data, err)
	}
}

func TestPrivateRemovalResumesPartiallyDeletedIdentityBoundTombstone(t *testing.T) {
	fixture := newPackageFixture(t)
	asset := fixture.privateAsset(t)
	privateRoot := canonicalFuturePath(t, filepath.Join(t.TempDir(), "private"))
	removeCalls := 0
	resolver, err := NewResolver(Config{
		PrivateRoot: privateRoot, PrivateAsset: asset,
		RunVersion: func(context.Context, string, ...string) (VersionResult, error) {
			return VersionResult{Stdout: []byte(SupportedVersion)}, nil
		},
		RemoveTree: func(path string) error {
			removeCalls++
			if removeCalls > 1 {
				return os.RemoveAll(path)
			}
			removed := false
			err := filepath.WalkDir(path, func(candidate string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil || removed || entry.IsDir() {
					return walkErr
				}
				removed = true
				return os.Remove(candidate)
			})
			if err != nil {
				return err
			}
			return errors.New("injected crash after partial deletion")
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	selection, err := resolver.InstallPrivate(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	sibling := filepath.Join(privateRoot, "keep-sibling")
	if err := os.Mkdir(sibling, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(sibling, "keep"), []byte("keep"), 0o600)
	if err := resolver.RemovePrivate(context.Background()); !errors.Is(err, ErrRemovalInterrupted) {
		t.Fatalf("partial RemovePrivate() error = %v", err)
	}
	binding, _ := BindPrivateAsset(asset, "", "")
	tombstone := filepath.Join(privateRoot, ".remove-"+binding.ClosureSHA256)
	journal := tombstone + ".json"
	if _, err := os.Lstat(selection.PackageRoot()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("original private closure survived tombstoning: %v", err)
	}
	if _, err := os.Lstat(tombstone); err != nil {
		t.Fatalf("partial tombstone missing: %v", err)
	}
	if pending, err := resolver.RemovalPending(context.Background()); err != nil || !pending {
		t.Fatalf("RemovalPending() = %t / %v", pending, err)
	}
	if err := resolver.RemovePrivate(context.Background()); err != nil {
		t.Fatalf("resumed RemovePrivate() = %v", err)
	}
	for _, removed := range []string{selection.PackageRoot(), tombstone, journal} {
		if _, err := os.Lstat(removed); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("removal residue at %s: %v", removed, err)
		}
	}
	if data, err := os.ReadFile(filepath.Join(sibling, "keep")); err != nil || string(data) != "keep" {
		t.Fatalf("sibling changed: %q / %v", data, err)
	}
}
