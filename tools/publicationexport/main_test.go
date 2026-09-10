package main

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestExportUsesDescriptorRelativeExclusiveWrites(t *testing.T) {
	root, target, external := fixture(t)
	got, err := export(root, target, []string{"docs/guide.md", "README.md"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 2 || got.Entries[0].Path != "README.md" || got.Entries[1].Path != "docs/guide.md" {
		t.Fatalf("unexpected entries: %#v", got.Entries)
	}
	if body, err := os.ReadFile(filepath.Join(target, "docs", "guide.md")); err != nil || string(body) != "guide\n" {
		t.Fatalf("unexpected exported file: %q, %v", body, err)
	}
	if body, err := os.ReadFile(external); err != nil || string(body) != "outside\n" {
		t.Fatalf("external file changed: %q, %v", body, err)
	}
}

func TestExportRejectsDestinationSymlinkSwapWithoutTouchingExternalFile(t *testing.T) {
	root, target, external := fixture(t)
	_, err := export(root, target, []string{"README.md"}, &hooks{
		beforeDestinationOpen: func(_ string, parentFD int, base string) {
			if err := unix.Symlinkat(external, parentFD, base); err != nil {
				t.Fatal(err)
			}
		},
	})
	if err == nil {
		t.Fatal("expected destination swap rejection")
	}
	assertExternalAndNoTarget(t, external, target)
}

func TestExportRejectsExclusivePublishSwapAndCleansStaging(t *testing.T) {
	root, target, external := fixture(t)
	_, err := export(root, target, []string{"README.md"}, &hooks{
		beforePublish: func(parentFD int, _, targetName string) {
			if err := unix.Symlinkat(external, parentFD, targetName); err != nil {
				t.Fatal(err)
			}
		},
	})
	if err == nil {
		t.Fatal("expected exclusive publish rejection")
	}
	if body, readErr := os.ReadFile(external); readErr != nil || string(body) != "outside\n" {
		t.Fatalf("external file changed: %q, %v", body, readErr)
	}
	entries, readErr := os.ReadDir(filepath.Dir(target))
	if readErr != nil {
		t.Fatal(readErr)
	}
	for _, item := range entries {
		if len(item.Name()) >= len(".chora-public-export-") && item.Name()[:len(".chora-public-export-")] == ".chora-public-export-" {
			t.Fatalf("staging residue remains: %s", item.Name())
		}
	}
}

func TestExportRejectsStagingNameSwapAndRemovesSubstituteTarget(t *testing.T) {
	root, target, external := fixture(t)
	parent := filepath.Dir(target)
	stolen := filepath.Join(parent, "stolen-staging")
	defer os.RemoveAll(stolen)
	_, err := export(root, target, []string{"README.md"}, &hooks{
		beforePublish: func(parentFD int, stagingName, _ string) {
			if err := unix.Renameat(parentFD, stagingName, parentFD, filepath.Base(stolen)); err != nil {
				t.Fatal(err)
			}
			if err := unix.Mkdirat(parentFD, stagingName, 0o700); err != nil {
				t.Fatal(err)
			}
			fd, err := unix.Openat(parentFD, stagingName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
			if err != nil {
				t.Fatal(err)
			}
			leaf, err := unix.Openat(fd, "attacker.txt", unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
			_ = unix.Close(fd)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := unix.Write(leaf, []byte("substitute\n")); err != nil {
				t.Fatal(err)
			}
			_ = unix.Close(leaf)
		},
	})
	if err == nil {
		t.Fatal("expected staging identity rejection")
	}
	assertExternalAndNoTarget(t, external, target)
}

func TestExportRejectsTargetParentSwapAndRemovesPublishedTree(t *testing.T) {
	root, _, external := fixture(t)
	parent := filepath.Join(filepath.Dir(root), "target-parent")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "candidate")
	moved := parent + "-moved"
	defer os.RemoveAll(moved)
	_, err := export(root, target, []string{"README.md"}, &hooks{
		afterPublish: func(_ int, _ string) {
			if err := os.Rename(parent, moved); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
		},
	})
	if err == nil {
		t.Fatal("expected target parent identity rejection")
	}
	assertExternalAndNoTarget(t, external, target)
	if _, statErr := os.Lstat(filepath.Join(moved, filepath.Base(target))); !os.IsNotExist(statErr) {
		t.Fatalf("published tree should be cleaned from moved parent, got %v", statErr)
	}
}

func TestExportRejectsSourceDriftAndCleansTarget(t *testing.T) {
	root, target, external := fixture(t)
	_, err := export(root, target, []string{"README.md"}, &hooks{
		afterFirstRead: func(relative string) {
			if err := os.WriteFile(filepath.Join(root, relative), []byte("changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	})
	if err == nil {
		t.Fatal("expected source drift rejection")
	}
	assertExternalAndNoTarget(t, external, target)
}

func fixture(t *testing.T) (string, string, string) {
	t.Helper()
	temporary := t.TempDir()
	root := filepath.Join(temporary, "source")
	if err := os.MkdirAll(filepath.Join(root, "docs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("hello\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "docs", "guide.md"), []byte("guide\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	external := filepath.Join(temporary, "external.txt")
	if err := os.WriteFile(external, []byte("outside\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(temporary, "candidate"), external
}

func assertExternalAndNoTarget(t *testing.T, external, target string) {
	t.Helper()
	if body, err := os.ReadFile(external); err != nil || string(body) != "outside\n" {
		t.Fatalf("external file changed: %q, %v", body, err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatalf("target should be absent, got %v", err)
	}
}
