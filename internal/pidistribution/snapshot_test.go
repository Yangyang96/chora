package pidistribution

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestLocalPiSnapshotPATHIsContentAddressedAndLossless(t *testing.T) {
	fixture := newPackageFixture(t)
	resolver := snapshotResolver(t, fixture.executable, nil)
	selection, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	root := canonicalFuturePath(t, filepath.Join(t.TempDir(), "snapshots"))
	snapshot, err := resolver.Snapshot(context.Background(), root, selection)
	if err != nil {
		t.Fatal(err)
	}
	data := snapshot.LocalPiSourceData()
	if data.ExecutablePath == selection.Path() || data.ResolvedPath == selection.ResolvedPath() || data.PackageRoot == selection.PackageRoot() {
		t.Fatalf("snapshot retained mutable source paths: %#v", data)
	}
	if !pathWithin(snapshot.Root(), data.ExecutablePath) || data.ExecutablePath != data.ResolvedPath ||
		data.RuntimeVersion != selection.Version() || data.ExecutableSHA256 != selection.ExecutableSHA256() ||
		data.ClosureSHA256 != selection.ClosureSHA256() || data.SelectionIdentity != snapshot.Selection().Identity() {
		t.Fatalf("lossless snapshot data = %#v", data)
	}
	if got, want := filepath.Base(snapshot.Root()), digestText(snapshot.ContentSHA256()); got != want {
		t.Fatalf("content-addressed root = %q, want %q", got, want)
	}
	if content, err := snapshot.Revalidate(); err != nil || content != snapshot.ContentSHA256() {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(snapshot.Root(), ".snapshot")); err != nil {
		t.Fatal(err)
	}
}

func TestLocalPiSnapshotExactReuseDoesNotReadDriftedSource(t *testing.T) {
	fixture := newPackageFixture(t)
	resolver := snapshotResolver(t, fixture.executable, nil)
	selection, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	root := canonicalFuturePath(t, filepath.Join(t.TempDir(), "snapshots"))
	first, err := resolver.Snapshot(context.Background(), root, selection)
	if err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(fixture.root, "lib", "dependency.js"), []byte("source drift"), 0o600)
	second, err := resolver.Snapshot(context.Background(), root, selection)
	if err != nil {
		t.Fatalf("exact immutable target was not reused: %v", err)
	}
	if first.Root() != second.Root() || first.Selection().Record() != second.Selection().Record() {
		t.Fatalf("reuse mismatch: %#v %#v", first.Selection().Record(), second.Selection().Record())
	}
}

func TestLocalPiSnapshotRevalidateIsPureAndDetectsClosureDrift(t *testing.T) {
	for _, drift := range []bool{false, true} {
		name := "unchanged"
		if drift {
			name = "dependency drift"
		}
		t.Run(name, func(t *testing.T) {
			fixture := newPackageFixture(t)
			resolver := snapshotResolver(t, fixture.executable, nil)
			selection, err := resolver.Resolve(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := resolver.Snapshot(context.Background(), canonicalFuturePath(t, filepath.Join(t.TempDir(), "snapshots")), selection)
			if err != nil {
				t.Fatal(err)
			}
			var versionCalls atomic.Int32
			snapshot.runVersion = func(context.Context, string, ...string) (VersionResult, error) {
				versionCalls.Add(1)
				return VersionResult{}, errors.New("version runner must not execute at the pure closure gate")
			}
			if drift {
				mustWriteFile(t, filepath.Join(snapshot.Selection().PackageRoot(), "lib", "dependency.js"), []byte("drift"), 0o600)
			}
			content, err := snapshot.Revalidate()
			if versionCalls.Load() != 0 {
				t.Fatalf("pure revalidation invoked version runner %d times", versionCalls.Load())
			}
			if drift {
				if !errors.Is(err, ErrSnapshotDrift) || content != ([32]byte{}) {
					t.Fatalf("closure drift result = %x, %v", content, err)
				}
				return
			}
			if err != nil || content != snapshot.ContentSHA256() {
				t.Fatalf("pure result = %x, %v", content, err)
			}
		})
	}
}

func TestLocalPiSnapshotDetectsSourceDriftDuringCopy(t *testing.T) {
	fixture := newPackageFixture(t)
	resolver := snapshotResolver(t, fixture.executable, func(executable string, call int32) {
		// Resolve probes once; pre-copy revalidation probes second; post-copy
		// source revalidation probes third.
		if executable == fixture.executable && call == 3 {
			mustWriteFile(t, filepath.Join(fixture.root, "lib", "dependency.js"), []byte("concurrent drift"), 0o600)
		}
	})
	selection, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	_, err = resolver.Snapshot(context.Background(), canonicalFuturePath(t, filepath.Join(t.TempDir(), "snapshots")), selection)
	if !errors.Is(err, ErrSelectionDrift) {
		t.Fatalf("source drift err = %v", err)
	}
}

func TestLocalPiSnapshotRejectsDependencyPermissionSymlinkAndHardlinkDrift(t *testing.T) {
	tests := map[string]func(*testing.T, LocalPiSnapshot){
		"dependency bytes": func(t *testing.T, snapshot LocalPiSnapshot) {
			mustWriteFile(t, filepath.Join(snapshot.Selection().PackageRoot(), "lib", "dependency.js"), []byte("drift"), 0o600)
		},
		"file permissions": func(t *testing.T, snapshot LocalPiSnapshot) {
			if err := os.Chmod(filepath.Join(snapshot.Selection().PackageRoot(), "lib", "dependency.js"), 0o400); err != nil {
				t.Fatal(err)
			}
		},
		"directory permissions": func(t *testing.T, snapshot LocalPiSnapshot) {
			path := filepath.Join(snapshot.Selection().PackageRoot(), "lib")
			if err := os.Chmod(path, 0o500); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(path, 0o700) })
		},
		"symlink": func(t *testing.T, snapshot LocalPiSnapshot) {
			path := filepath.Join(snapshot.Selection().PackageRoot(), "lib", "dependency.js")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(filepath.Join(snapshot.Selection().PackageRoot(), "package.json"), path); err != nil {
				t.Fatal(err)
			}
		},
		"hardlink": func(t *testing.T, snapshot LocalPiSnapshot) {
			if runtime.GOOS == "windows" {
				t.Skip("hard links require Unix fixture semantics")
			}
			path := filepath.Join(snapshot.Selection().PackageRoot(), "lib", "dependency.js")
			if err := os.Link(path, filepath.Join(snapshot.Selection().PackageRoot(), "lib", "alias.js")); err != nil {
				t.Fatal(err)
			}
		},
		"special file": func(t *testing.T, snapshot LocalPiSnapshot) {
			path := filepath.Join(snapshot.Selection().PackageRoot(), "lib", "dependency.js")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := newPackageFixture(t)
			resolver := snapshotResolver(t, fixture.executable, nil)
			selection, err := resolver.Resolve(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := resolver.Snapshot(context.Background(), canonicalFuturePath(t, filepath.Join(t.TempDir(), "snapshots")), selection)
			if err != nil {
				t.Fatal(err)
			}
			mutate(t, snapshot)
			if _, err := snapshot.Revalidate(); !errors.Is(err, ErrSnapshotDrift) {
				t.Fatalf("drift err = %v", err)
			}
		})
	}
}

func TestLocalPiSnapshotPrivateSelectionCopiesOnlyPackageClosure(t *testing.T) {
	fixture := newPackageFixture(t)
	asset := fixture.privateAsset(t)
	privateRoot := canonicalFuturePath(t, filepath.Join(t.TempDir(), "private"))
	resolver, err := NewResolver(privateConfig(t, asset, privateRoot))
	if err != nil {
		t.Fatal(err)
	}
	selection, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if selection.Kind() != SelectionPrivate {
		t.Fatalf("selection kind = %q", selection.Kind())
	}
	snapshotRoot := canonicalFuturePath(t, filepath.Join(t.TempDir(), "snapshots"))
	resolver.runVersion = func(_ context.Context, executable string, arguments ...string) (VersionResult, error) {
		if len(arguments) != 1 || arguments[0] != "--version" ||
			(!pathWithin(privateRoot, executable) && !pathWithin(snapshotRoot, executable)) {
			t.Fatalf("snapshot probe = %q %#v", executable, arguments)
		}
		return VersionResult{Stdout: []byte(SupportedVersion)}, nil
	}
	snapshot, err := resolver.Snapshot(context.Background(), snapshotRoot, selection)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(snapshot.Selection().PackageRoot(), ".complete")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("private installer marker leaked into package closure: %v", err)
	}
	if snapshot.LocalPiSourceData().ClosureSHA256 != selection.ClosureSHA256() {
		t.Fatal("private package closure binding was not preserved")
	}
	if content, err := snapshot.Revalidate(); err != nil || content != snapshot.ContentSHA256() {
		t.Fatal(err)
	}
}

func TestLocalPiSnapshotConcurrentCallsPublishOnce(t *testing.T) {
	fixture := newPackageFixture(t)
	resolver := snapshotResolver(t, fixture.executable, nil)
	selection, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	root := canonicalFuturePath(t, filepath.Join(t.TempDir(), "snapshots"))
	const count = 8
	results := make([]LocalPiSnapshot, count)
	errs := make([]error, count)
	var wait sync.WaitGroup
	start := make(chan struct{})
	for index := range results {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			<-start
			results[index], errs[index] = resolver.Snapshot(context.Background(), root, selection)
		}(index)
	}
	close(start)
	wait.Wait()
	for index, err := range errs {
		if err != nil {
			t.Fatalf("snapshot %d: %v", index, err)
		}
		if results[index].Root() != results[0].Root() {
			t.Fatalf("roots differ: %q != %q", results[index].Root(), results[0].Root())
		}
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 { // lock plus one published content target
		t.Fatalf("snapshot root entries = %d", len(entries))
	}
}

func TestLocalPiSnapshotPreservesPartialAndUnknownTargets(t *testing.T) {
	fixture := newPackageFixture(t)
	resolver := snapshotResolver(t, fixture.executable, nil)
	selection, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	root := canonicalFuturePath(t, filepath.Join(t.TempDir(), "snapshots"))
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	relative, _ := filepath.Rel(selection.PackageRoot(), selection.ResolvedPath())
	content := snapshotContentIdentity(selection, filepath.ToSlash(relative))
	target := filepath.Join(root, digestText(content))
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	unknown := filepath.Join(target, "do-not-remove")
	mustWriteFile(t, unknown, []byte("preserve"), 0o600)
	if _, err := resolver.Snapshot(context.Background(), root, selection); err == nil {
		t.Fatal("partial target was accepted")
	}
	if data, err := os.ReadFile(unknown); err != nil || string(data) != "preserve" {
		t.Fatalf("unknown target was modified: %q, %v", data, err)
	}
}

func TestLocalPiSnapshotRejectsUnsafeSourceAndPathEscape(t *testing.T) {
	fixture := newPackageFixture(t)
	resolver := snapshotResolver(t, fixture.executable, nil)
	selection, err := resolver.Resolve(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Snapshot(context.Background(), filepath.Join(fixture.root, "snapshots"), selection); err == nil {
		t.Fatal("snapshot root inside source was accepted")
	}
	dependency := filepath.Join(fixture.root, "lib", "dependency.js")
	if err := os.Link(dependency, filepath.Join(fixture.root, "lib", "alias.js")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolver.Snapshot(context.Background(), canonicalFuturePath(t, filepath.Join(t.TempDir(), "snapshots")), selection); err == nil {
		t.Fatal("hard-linked source was accepted")
	}
}

func snapshotResolver(t *testing.T, executable string, hook func(string, int32)) *Resolver {
	t.Helper()
	var calls atomic.Int32
	resolver, err := NewResolver(Config{
		LookPath: func(string) (string, error) { return executable, nil },
		RunVersion: func(_ context.Context, path string, arguments ...string) (VersionResult, error) {
			call := calls.Add(1)
			if len(arguments) != 1 || arguments[0] != "--version" {
				t.Fatalf("version arguments = %#v", arguments)
			}
			if hook != nil {
				hook(path, call)
			}
			return VersionResult{Stdout: []byte(SupportedVersion)}, nil
		},
		VersionTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return resolver
}
