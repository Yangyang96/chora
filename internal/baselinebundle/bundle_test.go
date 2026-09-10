package baselinebundle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestReconstructUsesSparseDeltaAndIgnoresInstalledExtras(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	installed := filepath.Join(parent, "installed")
	delta := filepath.Join(parent, "delta")
	target := filepath.Join(parent, "baseline")
	mustWrite(t, installed, "a.txt", "current-a", 0o444)
	// Installed-source modes belong to the current product bundle. The frozen
	// manifest supplies the reconstructed historical modes.
	mustWrite(t, installed, "bin/tool", "stable", 0o755)
	mustWrite(t, installed, "not-in-manifest.txt", "ignored", 0o444)
	mustWrite(t, delta, "a.txt", "frozen-a", 0o444)
	manifest := testManifest(t, []testFile{{"a.txt", "frozen-a", "0444"}, {"bin/tool", "stable", "0555"}})

	result, err := Reconstruct(context.Background(), Request{
		InstalledRoot: installed, DeltaRoot: delta, TargetRoot: target, Manifest: manifest,
	})
	if err != nil {
		t.Fatalf("Reconstruct: %v", err)
	}
	if result.EntryCount != 2 || result.AggregateSHA256 == "" {
		t.Fatalf("unexpected result: %#v", result)
	}
	assertFile(t, target, "a.txt", "frozen-a", 0o444)
	assertFile(t, target, "bin/tool", "stable", 0o555)
	if _, err := os.Lstat(filepath.Join(target, "not-in-manifest.txt")); !os.IsNotExist(err) {
		t.Fatalf("installed extra was copied: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("target mode = %v, err = %v", info.Mode().Perm(), err)
	}
	if _, err := Verify(target, manifest); err != nil {
		t.Fatalf("Verify: %v", err)
	}
}

func TestReconstructAllowsPackagedDeltaInsideInstalledSource(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	installed := filepath.Join(parent, "installed")
	delta := filepath.Join(installed, "contracts", "frozen-delta")
	target := filepath.Join(parent, "baseline")
	mustWrite(t, installed, "a.txt", "current", 0o644)
	mustWrite(t, delta, "a.txt", "frozen", 0o444)
	manifest := testManifest(t, []testFile{{"a.txt", "frozen", "0444"}})
	if _, err := Reconstruct(context.Background(), Request{InstalledRoot: installed, DeltaRoot: delta, TargetRoot: target, Manifest: manifest}); err != nil {
		t.Fatal(err)
	}
	assertFile(t, target, "a.txt", "frozen", 0o444)
}

func TestReconstructRejectsDeltaModeDrift(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	installed, delta, target := filepath.Join(parent, "installed"), filepath.Join(parent, "delta"), filepath.Join(parent, "baseline")
	mustWrite(t, installed, "a.txt", "current", 0o644)
	mustWrite(t, delta, "a.txt", "frozen", 0o644)
	manifest := testManifest(t, []testFile{{"a.txt", "frozen", "0444"}})
	if _, err := Reconstruct(context.Background(), Request{InstalledRoot: installed, DeltaRoot: delta, TargetRoot: target, Manifest: manifest}); !errors.Is(err, ErrDeltaIdentity) {
		t.Fatalf("error = %v", err)
	}
}

func TestReconstructRejectsUnsafeInputsWithoutPublishingTarget(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*testing.T, string, string, *Manifest)
	}{
		{"delta extra", func(t *testing.T, _, delta string, _ *Manifest) { mustWrite(t, delta, "extra", "x", 0o444) }},
		{"source symlink", func(t *testing.T, installed, _ string, _ *Manifest) {
			if err := os.Remove(filepath.Join(installed, "a.txt")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("elsewhere", filepath.Join(installed, "a.txt")); err != nil {
				t.Fatal(err)
			}
		}},
		{"duplicate path", func(_ *testing.T, _, _ string, manifest *Manifest) {
			manifest.Entries = append(manifest.Entries, manifest.Entries[0])
			manifest.EntryCount++
		}},
		{"traversal", func(_ *testing.T, _, _ string, manifest *Manifest) { manifest.Entries[0].Path = "../a.txt" }},
		{"bad aggregate", func(_ *testing.T, _, _ string, manifest *Manifest) { manifest.AggregateSHA256 = digest("wrong") }},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			installed, delta := filepath.Join(parent, "installed"), filepath.Join(parent, "delta")
			target := filepath.Join(parent, "baseline")
			mustWrite(t, installed, "a.txt", "frozen", 0o444)
			if err := os.Mkdir(delta, 0o700); err != nil {
				t.Fatal(err)
			}
			manifestBytes := testManifest(t, []testFile{{"a.txt", "frozen", "0444"}})
			var manifest Manifest
			if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, installed, delta, &manifest)
			manifestBytes, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := Reconstruct(context.Background(), Request{InstalledRoot: installed, DeltaRoot: delta, TargetRoot: target, Manifest: manifestBytes}); err == nil {
				t.Fatal("Reconstruct succeeded")
			}
			if _, err := os.Lstat(target); !os.IsNotExist(err) {
				t.Fatalf("partial target exists: %v", err)
			}
		})
	}
}

func TestVerifyRejectsExtrasAndMutation(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{"extra file", func(t *testing.T, root string) { mustWrite(t, root, "extra", "x", 0o444) }},
		{"extra directory", func(t *testing.T, root string) {
			if err := os.Mkdir(filepath.Join(root, "extra-dir"), 0o700); err != nil {
				t.Fatal(err)
			}
		}},
		{"content", func(t *testing.T, root string) {
			if err := os.Chmod(filepath.Join(root, "a.txt"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "a.txt"), []byte("changed"), 0o444); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(filepath.Join(root, "a.txt"), 0o444); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			parent := t.TempDir()
			installed, delta, target := filepath.Join(parent, "installed"), filepath.Join(parent, "delta"), filepath.Join(parent, "baseline")
			mustWrite(t, installed, "a.txt", "frozen", 0o444)
			if err := os.Mkdir(delta, 0o700); err != nil {
				t.Fatal(err)
			}
			manifest := testManifest(t, []testFile{{"a.txt", "frozen", "0444"}})
			if _, err := Reconstruct(context.Background(), Request{InstalledRoot: installed, DeltaRoot: delta, TargetRoot: target, Manifest: manifest}); err != nil {
				t.Fatal(err)
			}
			test.mutate(t, target)
			if _, err := Verify(target, manifest); err == nil {
				t.Fatal("Verify succeeded")
			}
		})
	}
}

func TestReconstructCancellationRemovesStagingAndTarget(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	installed, delta, target := filepath.Join(parent, "installed"), filepath.Join(parent, "delta"), filepath.Join(parent, "baseline")
	mustWrite(t, installed, "a.txt", "frozen", 0o444)
	if err := os.Mkdir(delta, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := testManifest(t, []testFile{{"a.txt", "frozen", "0444"}})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Reconstruct(ctx, Request{InstalledRoot: installed, DeltaRoot: delta, TargetRoot: target, Manifest: manifest}); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("partial target exists: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(parent, ".baseline.partial-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("staging residue = %v, err = %v", matches, err)
	}
}

func TestReconstructRejectsManifestFileDirectoryConflict(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	installed, delta, target := filepath.Join(parent, "installed"), filepath.Join(parent, "delta"), filepath.Join(parent, "baseline")
	if err := os.Mkdir(installed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(delta, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := testManifest(t, []testFile{{"a", "one", "0444"}, {"a/b", "two", "0444"}})
	if _, err := Reconstruct(context.Background(), Request{InstalledRoot: installed, DeltaRoot: delta, TargetRoot: target, Manifest: manifest}); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("error = %v", err)
	}
}

func TestManifestSchemaCompatibility(t *testing.T) {
	t.Parallel()
	manifestBytes := testManifest(t, []testFile{{"a.txt", "frozen", "0444"}})
	var manifest Manifest
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, schema := range []string{SchemaVersionV4, SchemaVersionV5, SchemaVersionV6} {
		manifest.SchemaVersion = schema
		encoded, err := json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := decodeManifest(encoded); err != nil {
			t.Fatalf("schema %q rejected: %v", schema, err)
		}
	}
	manifest.SchemaVersion = "chora.source-baseline.v7"
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := decodeManifest(encoded); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("unsupported schema error = %v", err)
	}
}

type testFile struct{ path, body, mode string }

func testManifest(t *testing.T, files []testFile) []byte {
	t.Helper()
	manifest := Manifest{SchemaVersion: SchemaVersion, SourceRevision: "revision", TaskID: "task", ContextSnapshotID: "context", ContextSnapshotDigest: digest("context")}
	for _, file := range files {
		manifest.Entries = append(manifest.Entries, Entry{Path: file.path, SHA256: digest(file.body), Mode: file.mode})
	}
	manifest.EntryCount = len(manifest.Entries)
	manifest.AggregateSHA256 = testAggregate(manifest.Entries)
	encoded, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func mustWrite(t *testing.T, root, relative, body string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertFile(t *testing.T, root, relative, body string, mode os.FileMode) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != body || info.Mode().Perm() != mode {
		t.Fatalf("%s = %q mode %04o", relative, data, info.Mode().Perm())
	}
}

func digest(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func testAggregate(entries []Entry) string {
	hash := sha256.New()
	for _, entry := range entries {
		_, _ = fmt.Fprintf(hash, "%s\x00%s\x00%s\n", entry.Path, entry.SHA256, entry.Mode)
	}
	return hex.EncodeToString(hash.Sum(nil))
}
