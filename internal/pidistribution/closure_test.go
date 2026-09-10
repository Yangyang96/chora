package pidistribution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestPrivateClosureMutationsRejected(t *testing.T) {
	mutations := map[string]func(*testing.T, packageFixture){
		"content": func(t *testing.T, fixture packageFixture) {
			mustWriteFile(t, filepath.Join(fixture.root, "lib", "dependency.js"), []byte("changed"), 0o600)
		},
		"mode": func(t *testing.T, fixture packageFixture) {
			if err := os.Chmod(filepath.Join(fixture.root, "lib", "dependency.js"), 0o666); err != nil {
				t.Fatal(err)
			}
		},
		"extra": func(t *testing.T, fixture packageFixture) {
			mustWriteFile(t, filepath.Join(fixture.root, "extra"), []byte("extra"), 0o600)
		},
		"symlink": func(t *testing.T, fixture packageFixture) {
			if err := os.Symlink("dependency.js", filepath.Join(fixture.root, "lib", "link")); err != nil {
				t.Fatal(err)
			}
		},
		"hardlink": func(t *testing.T, fixture packageFixture) {
			if err := os.Link(filepath.Join(fixture.root, "lib", "dependency.js"), filepath.Join(fixture.root, "lib", "hard")); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			fixture := newPackageFixture(t)
			asset := fixture.privateAsset(t)
			mutate(t, fixture)
			_, err := Resolve(context.Background(), privateConfig(t, asset, filepath.Join(t.TempDir(), "private")))
			if !errors.Is(err, ErrNoUsablePi) || !errors.Is(err, ErrUnsafeClosure) {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func TestPATHSymlinkLauncherBindsResolvedBytesAndPackage(t *testing.T) {
	fixture := newPackageFixture(t)
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.Mkdir(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(bin, "pi")
	if err := os.Symlink(fixture.executable, launcher); err != nil {
		t.Fatal(err)
	}
	selection, err := Resolve(context.Background(), Config{
		LookPath: func(string) (string, error) { return launcher, nil },
		RunVersion: func(_ context.Context, executable string, _ ...string) (VersionResult, error) {
			if executable != fixture.executable {
				t.Fatalf("resolved probe = %q", executable)
			}
			return VersionResult{Stdout: []byte(SupportedVersion)}, nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if selection.Path() != launcher || selection.ResolvedPath() != fixture.executable || selection.PackageRoot() != fixture.root {
		t.Fatalf("selection = %#v", selection.Record())
	}
}
