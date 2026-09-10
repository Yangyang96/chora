package repositorycapture

import (
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

func TestCaptureDirectoryGitRepositoryAndReceipt(t *testing.T) {
	paths := newFixture(t, true)
	mustMkdir(t, filepath.Join(paths.source, "docs"), 0o750)
	mustWrite(t, filepath.Join(paths.source, ".git", "config"), []byte("git-config\n"), 0o640)
	mustWrite(t, filepath.Join(paths.source, "docs", "guide.txt"), []byte("guide\n"), 0o600)

	receipt, err := Capture(paths.source, paths.capture, paths.receipt)
	if err != nil {
		t.Fatal(err)
	}
	if receipt.SchemaVersion != ReceiptSchema || receipt.Status != "passed" || receipt.SourceRoot != paths.source ||
		receipt.CaptureRoot != paths.capture || receipt.EntryCount != 4 || receipt.TotalBytes != int64(len("git-config\nguide\n")) || receipt.NetworkUsed {
		t.Fatalf("unexpected receipt: %+v", receipt)
	}
	assertMode(t, paths.capture, 0o700)
	assertMode(t, filepath.Join(paths.capture, "docs"), 0o750)
	assertMode(t, filepath.Join(paths.capture, "docs", "guide.txt"), 0o600)
	assertMode(t, paths.receipt, 0o400)
	data, err := os.ReadFile(paths.receipt)
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil || len(raw) != 9 {
		t.Fatalf("receipt shape: keys=%d err=%v", len(raw), err)
	}
	for _, key := range []string{"sourceRootIdentity", "sourceGitRootIdentity"} {
		identity, ok := raw[key].(map[string]any)
		if !ok || len(identity) != 5 {
			t.Fatalf("identity %s has wrong shape: %#v", key, raw[key])
		}
	}
}

func TestWriteReadonlyRegularNoReplacePublishesExactReadonlyFile(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "evidence.json")
	value := []byte("exact evidence\n")
	identity, err := WriteReadonlyRegularNoReplace(target, value)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != string(value) {
		t.Fatalf("published value drifted: %q %v", got, err)
	}
	assertMode(t, target, 0o400)
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	stat, err := nodeFromFileInfo(info)
	if err != nil || identity != ownedPathIdentity(stat) {
		t.Fatalf("returned identity drifted: got=%+v stat=%+v err=%v", identity, stat, err)
	}
}

func TestWriteReadonlyRegularNoReplaceRetainsPreexistingTargetAndCleansStaging(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "evidence.json")
	mustWrite(t, target, []byte("preexisting\n"), 0o400)
	if _, err := WriteReadonlyRegularNoReplace(target, []byte("new\n")); err == nil {
		t.Fatal("preexisting target was accepted")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "preexisting\n" {
		t.Fatalf("preexisting target changed: %q %v", got, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "evidence.json" {
		t.Fatalf("failed publication left staging residue: %+v", entries)
	}
}

func TestWriteReadonlyRegularNoReplaceRejectsParentSwapAndCleansPinnedStaging(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	parent := filepath.Join(root, "owned")
	mustMkdir(t, parent, 0o700)
	movedParent := filepath.Join(root, "owned-moved")
	target := filepath.Join(parent, "evidence.json")
	var hookErr error
	testHooks := &hooks{beforeReadonlyPublish: func(_ int, _, _ string) {
		if err := os.Rename(parent, movedParent); err != nil {
			hookErr = err
			return
		}
		if err := os.Mkdir(parent, 0o700); err != nil {
			hookErr = err
		}
	}}
	if _, err := writeReadonlyRegularNoReplaceWithHooks(target, []byte("partial\n"), testHooks); err == nil {
		t.Fatal("parent swap was accepted")
	}
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	assertAbsent(t, target)
	entries, err := os.ReadDir(movedParent)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("pinned parent retained exact staging after failure: %+v", entries)
	}
}

func TestWriteReadonlyRegularNoReplaceQuarantinesStagingSwapWithoutDeletingForeignInode(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "evidence.json")
	foreign := "foreign staging inode\n"
	var hookErr error
	testHooks := &hooks{beforeReadonlyPublish: func(parentFD int, stagingName, _ string) {
		if err := unix.Renameat(parentFD, stagingName, parentFD, "owned-staging-moved"); err != nil {
			hookErr = err
			return
		}
		fd, err := unix.Openat(parentFD, stagingName,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o400)
		if err != nil {
			hookErr = err
			return
		}
		handle := os.NewFile(uintptr(fd), "foreign-staging")
		if handle == nil {
			_ = unix.Close(fd)
			hookErr = errors.New("construct foreign staging handle")
			return
		}
		_, writeErr := handle.Write([]byte(foreign))
		hookErr = errors.Join(writeErr, handle.Close())
	}}
	if _, err := writeReadonlyRegularNoReplaceWithHooks(target, []byte("owned output\n"), testHooks); err == nil {
		t.Fatal("staging swap was accepted")
	}
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	assertAbsent(t, target)
	got, err := os.ReadFile(filepath.Join(root, "owned-staging-moved"))
	if err != nil || string(got) != "owned output\n" {
		t.Fatalf("owned staging was not preserved after adversarial move: %q %v", got, err)
	}
	if !treeContainsFile(t, root, foreign) {
		t.Fatal("foreign staging inode was deleted instead of quarantined")
	}
}

func TestWriteReadonlyRegularNoReplaceQuarantinesPostMoveSwapAndHidesFinal(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "evidence.json")
	foreign := "foreign final inode\n"
	var hookErr error
	testHooks := &hooks{afterReadonlyPublishMove: func(parentFD int, targetName string) {
		if err := unix.Renameat(parentFD, targetName, parentFD, "owned-final-moved"); err != nil {
			hookErr = err
			return
		}
		fd, err := unix.Openat(parentFD, targetName,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o400)
		if err != nil {
			hookErr = err
			return
		}
		handle := os.NewFile(uintptr(fd), "foreign-final")
		if handle == nil {
			_ = unix.Close(fd)
			hookErr = errors.New("construct foreign final handle")
			return
		}
		_, writeErr := handle.Write([]byte(foreign))
		hookErr = errors.Join(writeErr, handle.Close())
	}}
	if _, err := writeReadonlyRegularNoReplaceWithHooks(target, []byte("owned output\n"), testHooks); err == nil {
		t.Fatal("post-move target swap was accepted")
	}
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	assertAbsent(t, target)
	got, err := os.ReadFile(filepath.Join(root, "owned-final-moved"))
	if err != nil || string(got) != "owned output\n" {
		t.Fatalf("exact published inode was not preserved after adversarial move: %q %v", got, err)
	}
	if !treeContainsFile(t, root, foreign) {
		t.Fatal("foreign final inode was deleted instead of quarantined")
	}
}

func TestWriteReadonlyRegularNoReplaceRejectsSwapDuringFinalFDPathVerification(t *testing.T) {
	root := canonicalTempDir(t)
	target := filepath.Join(root, "evidence.json")
	ownedMoved := filepath.Join(root, "owned-final-moved-late")
	value := []byte("owned output\n")
	foreign := []byte("foreign late final\n")
	var hookErr error
	testHooks := &hooks{beforeReadonlyFinalVerify: func(parentFD int, targetName string) {
		hookErr = unix.Renameat(parentFD, targetName, parentFD, filepath.Base(ownedMoved))
		if hookErr != nil {
			return
		}
		fd, err := unix.Openat(parentFD, targetName,
			unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o400)
		if err != nil {
			hookErr = err
			return
		}
		handle := os.NewFile(uintptr(fd), "late-foreign-final")
		if handle == nil {
			_ = unix.Close(fd)
			hookErr = errors.New("construct late foreign final handle")
			return
		}
		_, writeErr := handle.Write(foreign)
		hookErr = errors.Join(writeErr, handle.Close())
	}}
	if _, err := writeReadonlyRegularNoReplaceWithHooks(target, value, testHooks); err == nil {
		t.Fatal("late target swap was accepted")
	}
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	assertAbsent(t, target)
	got, err := os.ReadFile(ownedMoved)
	if err != nil || string(got) != string(value) {
		t.Fatalf("owned published inode was not preserved after late move: %q %v", got, err)
	}
	if !treeContainsFile(t, root, string(foreign)) {
		t.Fatal("foreign late-swap inode was deleted instead of quarantined")
	}
}

func TestWriteReadonlyRegularNoReplaceRejectsSameInodeContentDriftBeforeFinalVerification(t *testing.T) {
	root := canonicalTempDir(t)
	target := filepath.Join(root, "evidence.json")
	value := []byte("owned output\n")
	altered := []byte("evil content\n")
	if len(value) != len(altered) {
		t.Fatal("test requires same-size content")
	}
	var hookErr error
	testHooks := &hooks{beforeReadonlyFinalVerify: func(_ int, _ string) {
		hookErr = os.Chmod(target, 0o600)
		if hookErr == nil {
			hookErr = os.WriteFile(target, altered, 0o600)
		}
		if hookErr == nil {
			hookErr = os.Chmod(target, 0o400)
		}
	}}
	if _, err := writeReadonlyRegularNoReplaceWithHooks(target, value, testHooks); err == nil {
		t.Fatal("same-inode same-size content drift was accepted")
	}
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	assertAbsent(t, target)
}

func TestCaptureAllowsSafeGitPointerAndRejectsUnsafeGitNodes(t *testing.T) {
	t.Run("regular pointer", func(t *testing.T) {
		paths := newFixture(t, false)
		mustWrite(t, filepath.Join(paths.source, ".git"), []byte("gitdir: ../admin\n"), 0o644)
		if _, err := Capture(paths.source, paths.capture, paths.receipt); err != nil {
			t.Fatal(err)
		}
	})
	for _, test := range []struct {
		name  string
		setup func(t *testing.T, path string)
	}{
		{name: "symlink", setup: func(t *testing.T, path string) {
			if err := os.Symlink("target", path); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "fifo", setup: func(t *testing.T, path string) {
			if err := unix.Mkfifo(path, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "hardlink", setup: func(t *testing.T, path string) {
			mustWrite(t, path, []byte("gitdir: ../admin\n"), 0o600)
			if err := os.Link(path, path+".second"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := newFixture(t, false)
			test.setup(t, filepath.Join(paths.source, ".git"))
			if _, err := Capture(paths.source, paths.capture, paths.receipt); err == nil {
				t.Fatal("unsafe .git node was captured")
			}
			assertAbsent(t, paths.capture)
			assertAbsent(t, paths.receipt)
		})
	}
}

func TestCaptureRejectsSourceMutationRaces(t *testing.T) {
	for _, test := range []struct {
		name string
		hook func(t *testing.T, paths fixture) *hooks
	}{
		{name: "directory replacement", hook: func(t *testing.T, paths fixture) *hooks {
			return &hooks{afterSourceEntryOpen: func(relative string) {
				if relative != "dir" {
					return
				}
				if err := os.Rename(filepath.Join(paths.source, "dir"), filepath.Join(paths.source, "old-dir")); err != nil {
					t.Fatal(err)
				}
				mustMkdir(t, filepath.Join(paths.source, "dir"), 0o700)
			}}
		}},
		{name: "regular to symlink", hook: func(t *testing.T, paths fixture) *hooks {
			return &hooks{afterSourceEntryOpen: func(relative string) {
				if relative != "file" {
					return
				}
				if err := os.Rename(filepath.Join(paths.source, "file"), filepath.Join(paths.source, "old-file")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink("old-file", filepath.Join(paths.source, "file")); err != nil {
					t.Fatal(err)
				}
			}}
		}},
		{name: "regular to fifo", hook: func(t *testing.T, paths fixture) *hooks {
			return &hooks{beforeSourceEntryOpen: func(relative string) {
				if relative != "file" {
					return
				}
				if err := os.Remove(filepath.Join(paths.source, "file")); err != nil {
					t.Fatal(err)
				}
				if err := unix.Mkfifo(filepath.Join(paths.source, "file"), 0o600); err != nil {
					t.Fatal(err)
				}
			}}
		}},
		{name: "truncate", hook: func(t *testing.T, paths fixture) *hooks {
			return &hooks{afterSourceEntryOpen: func(relative string) {
				if relative == "file" {
					if err := os.Truncate(filepath.Join(paths.source, "file"), 1); err != nil {
						t.Fatal(err)
					}
				}
			}}
		}},
		{name: "append", hook: func(t *testing.T, paths fixture) *hooks {
			return &hooks{afterSourceEntryOpen: func(relative string) {
				if relative == "file" {
					file, err := os.OpenFile(filepath.Join(paths.source, "file"), os.O_APPEND|os.O_WRONLY, 0)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := file.WriteString("append"); err != nil {
						t.Fatal(err)
					}
					_ = file.Close()
				}
			}}
		}},
		{name: "hardlink", hook: func(t *testing.T, paths fixture) *hooks {
			return &hooks{afterSourceEntryOpen: func(relative string) {
				if relative == "file" {
					if err := os.Link(filepath.Join(paths.source, "file"), filepath.Join(paths.source, "second-link")); err != nil {
						t.Fatal(err)
					}
				}
			}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := newFixture(t, true)
			mustMkdir(t, filepath.Join(paths.source, "dir"), 0o700)
			mustWrite(t, filepath.Join(paths.source, "dir", "nested"), []byte("nested"), 0o600)
			mustWrite(t, filepath.Join(paths.source, "file"), []byte("original"), 0o600)
			_, err := captureWithConfig(paths.source, paths.capture, paths.receipt, config{limits: defaultLimits, hooks: test.hook(t, paths)})
			if err == nil {
				t.Fatal("source race was accepted")
			}
			assertAbsent(t, paths.capture)
			assertAbsent(t, paths.receipt)
		})
	}
}

func TestDefaultLimitsAreProtocolConstants(t *testing.T) {
	want := limits{MaxDepth: 64, MaxEntries: 50_000, MaxPathBytes: 4 << 20, MaxFileBytes: 128 << 20, MaxTotal: 512 << 20}
	if defaultLimits != want {
		t.Fatalf("default limits = %+v, want %+v", defaultLimits, want)
	}
}

func TestCaptureEnforcesAllBounds(t *testing.T) {
	tests := []struct {
		name   string
		limits func() limits
		setup  func(t *testing.T, paths fixture)
	}{
		{name: "depth", limits: func() limits { value := defaultLimits; value.MaxDepth = 0; return value }, setup: func(t *testing.T, paths fixture) {
			mustMkdir(t, filepath.Join(paths.source, "nested"), 0o700)
		}},
		{name: "entries", limits: func() limits { value := defaultLimits; value.MaxEntries = 1; return value }, setup: func(t *testing.T, paths fixture) {
			mustWrite(t, filepath.Join(paths.source, "file"), []byte("x"), 0o600)
		}},
		{name: "path bytes", limits: func() limits { value := defaultLimits; value.MaxPathBytes = 3; return value }, setup: func(t *testing.T, paths fixture) {}},
		{name: "file bytes", limits: func() limits { value := defaultLimits; value.MaxFileBytes = 1; return value }, setup: func(t *testing.T, paths fixture) {
			mustWrite(t, filepath.Join(paths.source, "file"), []byte("xx"), 0o600)
		}},
		{name: "total bytes", limits: func() limits { value := defaultLimits; value.MaxTotal = 1; return value }, setup: func(t *testing.T, paths fixture) {
			mustWrite(t, filepath.Join(paths.source, "file"), []byte("xx"), 0o600)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			paths := newFixture(t, true)
			test.setup(t, paths)
			if _, err := captureWithConfig(paths.source, paths.capture, paths.receipt, config{limits: test.limits()}); err == nil {
				t.Fatal("bound was not enforced")
			}
			assertAbsent(t, paths.capture)
			assertAbsent(t, paths.receipt)
		})
	}
}

func TestCaptureTargetAndReceiptRacesFailWithoutDeletingReplacement(t *testing.T) {
	t.Run("capture root replacement", func(t *testing.T) {
		paths := newFixture(t, true)
		moved := paths.capture + "-moved"
		h := &hooks{afterCaptureRootCreated: func() {
			if err := os.Rename(paths.capture, moved); err != nil {
				t.Fatal(err)
			}
			mustMkdir(t, paths.capture, 0o700)
			mustWrite(t, filepath.Join(paths.capture, "foreign"), []byte("preserve"), 0o600)
		}}
		_, err := captureWithConfig(paths.source, paths.capture, paths.receipt, config{limits: defaultLimits, hooks: h})
		if err == nil || !strings.Contains(err.Error(), "replaced") {
			t.Fatalf("unexpected error: %v", err)
		}
		if data, readErr := os.ReadFile(filepath.Join(paths.capture, "foreign")); readErr != nil || string(data) != "preserve" {
			t.Fatalf("replacement was removed or changed: %q %v", data, readErr)
		}
		assertAbsent(t, paths.receipt)
	})

	t.Run("destination file replacement", func(t *testing.T) {
		paths := newFixture(t, true)
		mustWrite(t, filepath.Join(paths.source, "file"), []byte("source"), 0o600)
		h := &hooks{afterDestinationCreate: func(relative string) {
			if relative != "file" {
				return
			}
			if err := os.Rename(filepath.Join(paths.capture, "file"), filepath.Join(paths.capture, "old-file")); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink("old-file", filepath.Join(paths.capture, "file")); err != nil {
				t.Fatal(err)
			}
		}}
		if _, err := captureWithConfig(paths.source, paths.capture, paths.receipt, config{limits: defaultLimits, hooks: h}); err == nil {
			t.Fatal("destination replacement was accepted")
		}
		assertAbsent(t, paths.capture)
		assertAbsent(t, paths.receipt)
	})

	t.Run("receipt replacement", func(t *testing.T) {
		paths := newFixture(t, true)
		moved := paths.receipt + "-moved"
		h := &hooks{afterReceiptCreated: func() {
			assertMode(t, paths.receipt, 0o400)
			if err := os.Rename(paths.receipt, moved); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, paths.receipt, []byte("replacement"), 0o600)
		}}
		_, err := captureWithConfig(paths.source, paths.capture, paths.receipt, config{limits: defaultLimits, hooks: h})
		if err == nil || !strings.Contains(err.Error(), "replaced") {
			t.Fatalf("unexpected error: %v", err)
		}
		if data, readErr := os.ReadFile(paths.receipt); readErr != nil || string(data) != "replacement" {
			t.Fatalf("replacement receipt was removed: %q %v", data, readErr)
		}
		assertAbsent(t, paths.capture)
	})
}

func TestCleanupQuarantinesReplacementsBeforeDeleting(t *testing.T) {
	for _, test := range []struct {
		name     string
		relative string
		setup    func(t *testing.T, paths fixture)
		replace  func(t *testing.T, paths fixture)
	}{
		{
			name:     "regular file",
			relative: "file",
			setup: func(t *testing.T, paths fixture) {
				mustWrite(t, filepath.Join(paths.source, "file"), []byte("owned-file"), 0o600)
			},
			replace: func(t *testing.T, paths fixture) {
				if err := os.Rename(filepath.Join(paths.capture, "file"), filepath.Join(paths.capture, "owned-file-moved")); err != nil {
					t.Fatal(err)
				}
				mustWrite(t, filepath.Join(paths.capture, "file"), []byte("foreign-file-marker"), 0o600)
			},
		},
		{
			name:     "directory",
			relative: "dir",
			setup: func(t *testing.T, paths fixture) {
				mustMkdir(t, filepath.Join(paths.source, "dir"), 0o700)
				mustWrite(t, filepath.Join(paths.source, "dir", "nested"), []byte("owned-nested"), 0o600)
			},
			replace: func(t *testing.T, paths fixture) {
				if err := os.Rename(filepath.Join(paths.capture, "dir"), filepath.Join(paths.capture, "owned-dir-moved")); err != nil {
					t.Fatal(err)
				}
				mustMkdir(t, filepath.Join(paths.capture, "dir"), 0o700)
				mustWrite(t, filepath.Join(paths.capture, "dir", "marker"), []byte("foreign-directory-marker"), 0o600)
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := newFixture(t, true)
			test.setup(t, paths)
			replaced := false
			h := &hooks{
				beforeReceiptCreate: func() {
					mustWrite(t, paths.receipt, []byte("force receipt failure"), 0o600)
				},
				beforeCleanupQuarantine: func(relative string) {
					if relative == test.relative && !replaced {
						replaced = true
						test.replace(t, paths)
					}
				},
			}
			_, err := captureWithConfig(paths.source, paths.capture, paths.receipt, config{limits: defaultLimits, hooks: h})
			if err == nil || !strings.Contains(err.Error(), "preserved quarantined replacement") {
				t.Fatalf("cleanup replacement was not quarantined: %v", err)
			}
			if !replaced {
				t.Fatal("cleanup replacement hook was not reached")
			}
			marker := "foreign-file-marker"
			if test.relative == "dir" {
				marker = "foreign-directory-marker"
			}
			if !treeContainsFile(t, paths.capture, marker) {
				t.Fatalf("foreign replacement marker %q was deleted", marker)
			}
		})
	}
}

func TestCaptureRejectsCTimeDriftWhenMTimeIsRestored(t *testing.T) {
	t.Run("same-size file rewrite", func(t *testing.T) {
		paths := newFixture(t, true)
		path := filepath.Join(paths.source, "file")
		mustWrite(t, path, []byte("original"), 0o600)
		before, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		h := &hooks{afterSourceFileRead: func(relative string) {
			if relative != "file" {
				return
			}
			mustWrite(t, path, []byte("changed!"), 0o600)
			if err := os.Chmod(path, 0o640); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
				t.Fatal(err)
			}
		}}
		if _, err := captureWithConfig(paths.source, paths.capture, paths.receipt, config{limits: defaultLimits, hooks: h}); err == nil {
			t.Fatal("same-size rewrite with restored mtime was accepted")
		}
		assertAbsent(t, paths.capture)
		assertAbsent(t, paths.receipt)
	})

	t.Run("directory mutation", func(t *testing.T) {
		paths := newFixture(t, true)
		path := filepath.Join(paths.source, "dir")
		mustMkdir(t, path, 0o700)
		before, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		h := &hooks{afterSourceDirectoryRead: func(relative string) {
			if relative != "dir" {
				return
			}
			temporary := filepath.Join(path, "temporary")
			mustWrite(t, temporary, []byte("temporary"), 0o600)
			if err := os.Remove(temporary); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(path, before.ModTime(), before.ModTime()); err != nil {
				t.Fatal(err)
			}
		}}
		if _, err := captureWithConfig(paths.source, paths.capture, paths.receipt, config{limits: defaultLimits, hooks: h}); err == nil {
			t.Fatal("directory mutation with restored mtime was accepted")
		}
		assertAbsent(t, paths.capture)
		assertAbsent(t, paths.receipt)
	})
}

func TestCaptureRejectsPreexistingReceiptAndUnsafeModes(t *testing.T) {
	t.Run("receipt exists", func(t *testing.T) {
		paths := newFixture(t, true)
		mustWrite(t, paths.receipt, []byte("existing"), 0o600)
		if _, err := Capture(paths.source, paths.capture, paths.receipt); err == nil {
			t.Fatal("preexisting receipt was overwritten")
		}
		assertAbsent(t, paths.capture)
	})
	t.Run("group writable file", func(t *testing.T) {
		paths := newFixture(t, true)
		mustWrite(t, filepath.Join(paths.source, "unsafe"), []byte("unsafe"), 0o660)
		if _, err := Capture(paths.source, paths.capture, paths.receipt); err == nil {
			t.Fatal("group-writable source file was accepted")
		}
	})
	t.Run("group writable directory", func(t *testing.T) {
		paths := newFixture(t, true)
		mustMkdir(t, filepath.Join(paths.source, "unsafe"), 0o770)
		if _, err := Capture(paths.source, paths.capture, paths.receipt); err == nil {
			t.Fatal("group-writable source directory was accepted")
		}
	})
	t.Run("source symlink ancestor", func(t *testing.T) {
		paths := newFixture(t, true)
		alias := filepath.Join(paths.root, "alias")
		if err := os.Symlink(paths.root, alias); err != nil {
			t.Fatal(err)
		}
		sourceThroughAlias := filepath.Join(alias, filepath.Base(paths.source))
		if _, err := Capture(sourceThroughAlias, paths.capture, paths.receipt); err == nil {
			t.Fatal("source symlink ancestor was accepted")
		}
		assertAbsent(t, paths.capture)
		assertAbsent(t, paths.receipt)
	})
}

func TestRemoveOwnedPathDispositionsAndAbsentIdempotence(t *testing.T) {
	root := canonicalTempDir(t)
	regular := filepath.Join(root, "regular")
	mustWrite(t, regular, []byte("owned"), 0o600)
	if err := RemoveOwnedPath(regular, RegularFile, ownedIdentity(t, regular)); err != nil {
		t.Fatal(err)
	}
	assertAbsent(t, regular)
	if err := RemoveOwnedPath(regular, RegularFile, OwnedPathIdentity{Mode: int(unix.S_IFREG | 0o600), UID: os.Geteuid(), GID: os.Getegid(), Dev: "0", Ino: "1"}); err != nil {
		t.Fatalf("absent cleanup was not idempotent: %v", err)
	}

	empty := filepath.Join(root, "empty")
	mustMkdir(t, empty, 0o700)
	if err := RemoveOwnedPath(empty, EmptyDirectory, ownedIdentity(t, empty)); err != nil {
		t.Fatal(err)
	}
	assertAbsent(t, empty)

	recursive := filepath.Join(root, "recursive")
	mustMkdir(t, recursive, 0o700)
	mustMkdir(t, filepath.Join(recursive, "nested"), 0o750)
	mustWrite(t, filepath.Join(recursive, "nested", "file"), []byte("owned"), 0o640)
	if err := RemoveOwnedPath(recursive, RecursiveDirectory, ownedIdentity(t, recursive)); err != nil {
		t.Fatal(err)
	}
	assertAbsent(t, recursive)
}

func TestRemoveOwnedPathRemovesOwnerReadonlyRegularWithoutQuarantineResidue(t *testing.T) {
	root := canonicalTempDir(t)
	path := filepath.Join(root, "owner-readonly.json")
	mustWrite(t, path, []byte("owned readonly evidence\n"), 0o400)
	expected := ownedIdentity(t, path)
	if err := RemoveOwnedPath(path, RegularFile, expected); err != nil {
		t.Fatalf("remove owner-readonly regular file: %v", err)
	}
	assertAbsent(t, path)
	if got := quarantineCount(t, root); got != 0 {
		t.Fatalf("successful owner-readonly cleanup retained %d quarantine directories", got)
	}
}

func TestRemoveOwnedPathAcceptsOnlyPinnedDarwinVarAlias(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS fixed /var alias contract")
	}
	root := canonicalTempDir(t)
	if !strings.HasPrefix(root, "/private/var/") {
		t.Skip("test temporary directory is not below /private/var")
	}
	aliasRoot := strings.TrimPrefix(root, "/private")

	t.Run("fixed system alias", func(t *testing.T) {
		canonicalPath := filepath.Join(root, "alias-readonly.json")
		aliasPath := filepath.Join(aliasRoot, "alias-readonly.json")
		mustWrite(t, canonicalPath, []byte("owned readonly evidence\n"), 0o400)
		if err := RemoveOwnedPath(aliasPath, RegularFile, ownedIdentity(t, canonicalPath)); err != nil {
			t.Fatalf("remove through fixed /var alias: %v", err)
		}
		assertAbsent(t, canonicalPath)
	})

	t.Run("symlink in suffix", func(t *testing.T) {
		actual := filepath.Join(root, "actual")
		mustMkdir(t, actual, 0o700)
		canonicalPath := filepath.Join(actual, "owned.json")
		mustWrite(t, canonicalPath, []byte("owned\n"), 0o400)
		aliasDirectory := filepath.Join(root, "suffix-link")
		if err := os.Symlink(actual, aliasDirectory); err != nil {
			t.Fatal(err)
		}
		pathThroughAliases := filepath.Join(strings.TrimPrefix(aliasDirectory, "/private"), "owned.json")
		if err := RemoveOwnedPath(pathThroughAliases, RegularFile, ownedIdentity(t, canonicalPath)); err == nil {
			t.Fatal("user-controlled symlink after fixed /var alias was accepted")
		}
		if got, err := os.ReadFile(canonicalPath); err != nil || string(got) != "owned\n" {
			t.Fatalf("suffix symlink rejection mutated owned file: %q %v", got, err)
		}
	})
}

func TestRemoveOwnedPathRejectsArbitrarySymlinkAncestor(t *testing.T) {
	root := canonicalTempDir(t)
	actual := filepath.Join(root, "actual")
	mustMkdir(t, actual, 0o700)
	canonicalPath := filepath.Join(actual, "owned.json")
	mustWrite(t, canonicalPath, []byte("owned\n"), 0o400)
	alias := filepath.Join(root, "user-alias")
	if err := os.Symlink(actual, alias); err != nil {
		t.Fatal(err)
	}
	if err := RemoveOwnedPath(filepath.Join(alias, "owned.json"), RegularFile,
		ownedIdentity(t, canonicalPath)); err == nil {
		t.Fatal("arbitrary symlink ancestor was accepted")
	}
	if got, err := os.ReadFile(canonicalPath); err != nil || string(got) != "owned\n" {
		t.Fatalf("arbitrary symlink rejection mutated owned file: %q %v", got, err)
	}
}

func TestRemoveOwnedUnixSocketAndQuarantineReplacement(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		root := shortCanonicalTempDir(t)
		path := filepath.Join(root, "control.sock")
		listener := mustUnixListener(t, path)
		expected := ownedIdentity(t, path)
		if err := listener.Close(); err != nil {
			t.Fatal(err)
		}
		if err := RemoveOwnedPath(path, UnixSocket, expected); err != nil {
			t.Fatal(err)
		}
		assertAbsent(t, path)
	})

	t.Run("check after swap preserves replacement", func(t *testing.T) {
		root := shortCanonicalTempDir(t)
		path := filepath.Join(root, "control.sock")
		original := mustUnixListener(t, path)
		defer original.Close()
		expected := ownedIdentity(t, path)
		var replacement *net.UnixListener
		h := &hooks{beforeOwnedPathQuarantine: func(relative string) {
			if relative != "." || replacement != nil {
				return
			}
			if err := os.Rename(path, path+"-owned-moved"); err != nil {
				t.Fatal(err)
			}
			replacement = mustUnixListener(t, path)
		}}
		err := removeOwnedPathWithHooks(path, UnixSocket, expected, h)
		if replacement != nil {
			_ = replacement.Close()
		}
		if err == nil || !strings.Contains(err.Error(), "preserved quarantined replacement") {
			t.Fatalf("socket replacement was not preserved: %v", err)
		}
		assertAbsent(t, path)
		if quarantineCount(t, root) == 0 {
			t.Fatal("socket replacement quarantine is absent")
		}
	})
}

func TestRemoveOwnedPathQuarantinesCheckToRenameReplacements(t *testing.T) {
	for _, test := range []struct {
		name        string
		disposition OwnedPathDisposition
		child       bool
		setup       func(t *testing.T, path string)
		replace     func(t *testing.T, path string)
	}{
		{name: "regular root", disposition: RegularFile,
			setup:   func(t *testing.T, path string) { mustWrite(t, path, []byte("owned"), 0o600) },
			replace: func(t *testing.T, path string) { mustWrite(t, path, []byte("foreign-regular"), 0o600) }},
		{name: "empty directory root", disposition: EmptyDirectory,
			setup:   func(t *testing.T, path string) { mustMkdir(t, path, 0o700) },
			replace: func(t *testing.T, path string) { mustMkdir(t, path, 0o700) }},
		{name: "recursive directory root", disposition: RecursiveDirectory,
			setup: func(t *testing.T, path string) {
				mustMkdir(t, path, 0o700)
				mustWrite(t, filepath.Join(path, "owned-child"), []byte("owned"), 0o600)
			},
			replace: func(t *testing.T, path string) {
				mustMkdir(t, path, 0o700)
				mustWrite(t, filepath.Join(path, "foreign-marker"), []byte("foreign-recursive-root"), 0o600)
			}},
		{name: "recursive child", disposition: RecursiveDirectory, child: true,
			setup: func(t *testing.T, path string) {
				mustMkdir(t, path, 0o700)
				mustWrite(t, filepath.Join(path, "child"), []byte("owned"), 0o600)
			},
			replace: func(t *testing.T, path string) { mustWrite(t, path, []byte("foreign-child"), 0o600) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := canonicalTempDir(t)
			path := filepath.Join(root, "owned")
			test.setup(t, path)
			expected := ownedIdentity(t, path)
			replaced := false
			h := &hooks{beforeOwnedPathQuarantine: func(relative string) {
				wanted := "."
				replacementPath := path
				if test.child {
					wanted = "child"
					replacementPath = filepath.Join(path, "child")
				}
				if relative != wanted || replaced {
					return
				}
				replaced = true
				if err := os.Rename(replacementPath, replacementPath+"-owned-moved"); err != nil {
					t.Fatal(err)
				}
				test.replace(t, replacementPath)
			}}
			err := removeOwnedPathWithHooks(path, test.disposition, expected, h)
			if err == nil || !strings.Contains(err.Error(), "preserved quarantined replacement") {
				t.Fatalf("replacement was not preserved: %v", err)
			}
			if !replaced || quarantineCount(t, root) == 0 {
				t.Fatalf("replacement hook/quarantine missing: replaced=%v", replaced)
			}
			if test.name == "recursive directory root" && !treeContainsFile(t, root, "foreign-recursive-root") {
				t.Fatal("foreign recursive root was not retained")
			}
			if test.child && !treeContainsFile(t, root, "foreign-child") {
				t.Fatal("foreign child was not retained")
			}
		})
	}
}

func TestCreateOwnedDirectoryNoReplaceBindsParentAndPreservesUnknownPostMkdirInode(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		parent := canonicalTempDir(t)
		target := filepath.Join(parent, "created")
		identity, err := CreateOwnedDirectoryNoReplace(target, ownedIdentity(t, parent))
		if err != nil {
			t.Fatal(err)
		}
		if identity != ownedIdentity(t, target) {
			t.Fatal("created directory identity drifted")
		}
	})

	t.Run("parent pathname swap", func(t *testing.T) {
		root := canonicalTempDir(t)
		parent := filepath.Join(root, "parent")
		moved := filepath.Join(root, "moved-parent")
		mustMkdir(t, parent, 0o700)
		target := filepath.Join(parent, "created")
		err := func() error {
			_, err := createOwnedDirectoryNoReplaceWithHooks(target, ownedIdentity(t, parent), &hooks{
				beforeOwnedDirectoryCreate: func() {
					if err := os.Rename(parent, moved); err != nil {
						t.Fatal(err)
					}
					mustMkdir(t, parent, 0o700)
				},
			})
			return err
		}()
		if err == nil || !strings.Contains(err.Error(), "path binding drifted") {
			t.Fatalf("parent swap was accepted: %v", err)
		}
		assertAbsent(t, target)
		assertAbsent(t, filepath.Join(moved, "created"))
	})

	t.Run("post mkdir pre identity loss", func(t *testing.T) {
		parent := canonicalTempDir(t)
		target := filepath.Join(parent, "created")
		retained := filepath.Join(parent, "retained-unknown")
		_, err := createOwnedDirectoryNoReplaceWithHooks(target, ownedIdentity(t, parent), &hooks{
			afterOwnedDirectoryCreate: func() {
				if err := os.Rename(target, retained); err != nil {
					t.Fatal(err)
				}
			},
		})
		if err == nil || !strings.Contains(err.Error(), "retained without identity") {
			t.Fatalf("post-mkdir identity loss was accepted: %v", err)
		}
		if _, statErr := os.Lstat(retained); statErr != nil {
			t.Fatalf("unknown created inode was not retained: %v", statErr)
		}
	})
}

func TestOwnedPathCleanupRejectsIdentityTypeModeLinksAndUnsafeParent(t *testing.T) {
	t.Run("identity mode", func(t *testing.T) {
		root := canonicalTempDir(t)
		path := filepath.Join(root, "owned")
		mustWrite(t, path, []byte("owned"), 0o600)
		expected := ownedIdentity(t, path)
		expected.Mode ^= 0o040
		if err := RemoveOwnedPath(path, RegularFile, expected); err == nil {
			t.Fatal("mode drift was accepted")
		}
	})
	t.Run("type disposition", func(t *testing.T) {
		root := canonicalTempDir(t)
		path := filepath.Join(root, "owned")
		mustWrite(t, path, []byte("owned"), 0o600)
		if err := RemoveOwnedPath(path, EmptyDirectory, ownedIdentity(t, path)); err == nil {
			t.Fatal("type mismatch was accepted")
		}
	})
	t.Run("hardlink", func(t *testing.T) {
		root := canonicalTempDir(t)
		path := filepath.Join(root, "owned")
		mustWrite(t, path, []byte("owned"), 0o600)
		if err := os.Link(path, filepath.Join(root, "second")); err != nil {
			t.Fatal(err)
		}
		if err := RemoveOwnedPath(path, RegularFile, ownedIdentity(t, path)); err == nil {
			t.Fatal("hardlink was accepted")
		}
	})
	t.Run("unsafe parent", func(t *testing.T) {
		root := canonicalTempDir(t)
		path := filepath.Join(root, "owned")
		mustWrite(t, path, []byte("owned"), 0o600)
		expected := ownedIdentity(t, path)
		if err := os.Chmod(root, 0o1777); err != nil {
			t.Fatal(err)
		}
		if err := RemoveOwnedPath(path, RegularFile, expected); err == nil {
			t.Fatal("non-canonical user-owned 01777 parent was accepted")
		}
	})
}

func TestOwnedPathProtocolsAllowCanonicalRootOwnedStickyTemporaryParent(t *testing.T) {
	parent := "/private/tmp"
	if _, err := os.Lstat(parent); err != nil {
		parent = "/tmp"
	}
	root, err := os.MkdirTemp(parent, "chora-owned-path-parent-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "child"), []byte("owned"), 0o600)
	if err := RemoveOwnedPath(root, RecursiveDirectory, ownedIdentity(t, root)); err != nil {
		t.Fatalf("cleanup below root-owned sticky temporary parent failed: %v", err)
	}
	assertAbsent(t, root)

	sourceFile, err := os.CreateTemp(parent, "chora-owned-path-publish-")
	if err != nil {
		t.Fatal(err)
	}
	source := sourceFile.Name()
	t.Cleanup(func() { _ = os.Remove(source) })
	if _, err := sourceFile.WriteString("publish"); err != nil {
		_ = sourceFile.Close()
		t.Fatal(err)
	}
	if err := sourceFile.Close(); err != nil {
		t.Fatal(err)
	}
	target := source + "-target"
	t.Cleanup(func() { _ = os.Remove(target) })
	if err := PublishOwnedPath(source, target, RegularFile, ownedIdentity(t, source)); err != nil {
		t.Fatalf("publish below root-owned sticky temporary parent failed: %v", err)
	}
	assertAbsent(t, source)
}

func TestPublishOwnedPathSuccessAndRaces(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		root := canonicalTempDir(t)
		source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
		mustWrite(t, source, []byte("owned"), 0o600)
		expected := ownedIdentity(t, source)
		if err := PublishOwnedPath(source, target, RegularFile, expected); err != nil {
			t.Fatal(err)
		}
		assertAbsent(t, source)
		if got := ownedIdentity(t, target); got != expected {
			t.Fatalf("published identity = %+v, want %+v", got, expected)
		}
	})

	t.Run("source swap is quarantined and target restored absent", func(t *testing.T) {
		root := canonicalTempDir(t)
		source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
		mustWrite(t, source, []byte("owned"), 0o600)
		expected := ownedIdentity(t, source)
		h := &hooks{beforeOwnedPathPublish: func() {
			if err := os.Rename(source, source+"-owned-moved"); err != nil {
				t.Fatal(err)
			}
			mustWrite(t, source, []byte("foreign-publish"), 0o600)
		}}
		err := publishOwnedPathWithHooks(source, target, RegularFile, expected, h)
		if err == nil || !strings.Contains(err.Error(), "changed after validation") {
			t.Fatalf("source swap was accepted: %v", err)
		}
		assertAbsent(t, target)
		if !treeContainsFile(t, root, "foreign-publish") || quarantineCount(t, root) == 0 {
			t.Fatal("foreign source swap was not preserved in quarantine")
		}
	})

	t.Run("target race does not replace target", func(t *testing.T) {
		root := canonicalTempDir(t)
		source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
		mustWrite(t, source, []byte("owned"), 0o600)
		expected := ownedIdentity(t, source)
		h := &hooks{beforeOwnedPathPublish: func() { mustWrite(t, target, []byte("foreign-target"), 0o600) }}
		if err := publishOwnedPathWithHooks(source, target, RegularFile, expected, h); err == nil {
			t.Fatal("target race was accepted")
		}
		if data, err := os.ReadFile(target); err != nil || string(data) != "foreign-target" {
			t.Fatalf("target race was overwritten: %q %v", data, err)
		}
		if data, err := os.ReadFile(source); err != nil || string(data) != "owned" {
			t.Fatalf("source was moved on target race: %q %v", data, err)
		}
	})
}

func TestPublishOwnedPathRejectsAbsentTypeModeAndHardlink(t *testing.T) {
	root := canonicalTempDir(t)
	absent := filepath.Join(root, "absent")
	target := filepath.Join(root, "target")
	placeholder := OwnedPathIdentity{Mode: int(unix.S_IFREG | 0o600), UID: os.Geteuid(), GID: os.Getegid(), Dev: "0", Ino: "1"}
	if err := PublishOwnedPath(absent, target, RegularFile, placeholder); err == nil {
		t.Fatal("absent source was accepted")
	}
	for _, test := range []struct {
		name   string
		mutate func(t *testing.T, source string, identity *OwnedPathIdentity)
	}{
		{name: "type", mutate: func(t *testing.T, source string, identity *OwnedPathIdentity) {
			identity.Mode = int(unix.S_IFDIR | 0o700)
		}},
		{name: "mode", mutate: func(t *testing.T, source string, identity *OwnedPathIdentity) { identity.Mode ^= 0o040 }},
		{name: "hardlink", mutate: func(t *testing.T, source string, identity *OwnedPathIdentity) {
			if err := os.Link(source, source+"-second"); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			caseRoot := canonicalTempDir(t)
			source := filepath.Join(caseRoot, "source")
			mustWrite(t, source, []byte("owned"), 0o600)
			identity := ownedIdentity(t, source)
			test.mutate(t, source, &identity)
			if err := PublishOwnedPath(source, filepath.Join(caseRoot, "target"), RegularFile, identity); err == nil {
				t.Fatal("unsafe publish source was accepted")
			}
		})
	}
}

func TestReplaceOwnedPathSuccess(t *testing.T) {
	root := canonicalTempDir(t)
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	mustWrite(t, source, []byte("new"), 0o600)
	mustWrite(t, target, []byte("old"), 0o600)
	sourceIdentity, targetIdentity := ownedIdentity(t, source), ownedIdentity(t, target)
	if err := ReplaceOwnedPath(source, target, RegularFile, RegularFile, sourceIdentity, targetIdentity); err != nil {
		t.Fatal(err)
	}
	assertAbsent(t, source)
	if data, err := os.ReadFile(target); err != nil || string(data) != "new" {
		t.Fatalf("replacement target = %q, err=%v", data, err)
	}
	if got := ownedIdentity(t, target); got != sourceIdentity {
		t.Fatalf("replacement identity = %+v, want %+v", got, sourceIdentity)
	}
	if quarantineCount(t, root) != 0 {
		t.Fatal("successful replace retained quarantine")
	}
}

func TestReplaceOwnedPathRollsBackRacesAndPreservesForeignNodes(t *testing.T) {
	for _, test := range []struct {
		name  string
		hooks func(t *testing.T, source, target string) *hooks
	}{
		{name: "source swap after both checks", hooks: func(t *testing.T, source, target string) *hooks {
			return &hooks{afterOwnedPathReplaceChecks: func() {
				if err := os.Rename(source, source+"-owned-moved"); err != nil {
					t.Fatal(err)
				}
				mustWrite(t, source, []byte("foreign-source"), 0o600)
			}}
		}},
		{name: "target race after both checks", hooks: func(t *testing.T, source, target string) *hooks {
			return &hooks{afterOwnedPathReplaceChecks: func() {
				mustWrite(t, target, []byte("foreign-target"), 0o600)
			}}
		}},
		{name: "target swap after source move", hooks: func(t *testing.T, source, target string) *hooks {
			return &hooks{afterOwnedPathReplaceMove: func() {
				if err := os.Rename(target, target+"-new-moved"); err != nil {
					t.Fatal(err)
				}
				mustWrite(t, target, []byte("foreign-after-move"), 0o600)
			}}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := canonicalTempDir(t)
			source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
			mustWrite(t, source, []byte("new"), 0o600)
			mustWrite(t, target, []byte("old"), 0o600)
			sourceIdentity, targetIdentity := ownedIdentity(t, source), ownedIdentity(t, target)
			err := replaceOwnedPathWithHooks(source, target, RegularFile, RegularFile,
				sourceIdentity, targetIdentity, test.hooks(t, source, target))
			if err == nil {
				t.Fatal("replace race was accepted")
			}
			if data, readErr := os.ReadFile(target); readErr != nil || string(data) != "old" {
				t.Fatalf("exact old target was not restored: %q %v (replace err %v)", data, readErr, err)
			}
			if got := ownedIdentity(t, target); got != targetIdentity {
				t.Fatalf("restored target identity = %+v, want %+v", got, targetIdentity)
			}
			if quarantineCount(t, root) == 0 {
				t.Fatal("foreign replacement was not retained in quarantine")
			}
			marker := "foreign-source"
			if strings.Contains(test.name, "target race") {
				marker = "foreign-target"
			} else if strings.Contains(test.name, "after source move") {
				marker = "foreign-after-move"
			}
			if !treeContainsFile(t, root, marker) {
				t.Fatalf("foreign marker %q was not preserved", marker)
			}
		})
	}
}

func TestReplaceOwnedPathRejectsNonRegularAndIdentityDrift(t *testing.T) {
	root := canonicalTempDir(t)
	source, target := filepath.Join(root, "source"), filepath.Join(root, "target")
	mustWrite(t, source, []byte("new"), 0o600)
	mustWrite(t, target, []byte("old"), 0o600)
	sourceIdentity, targetIdentity := ownedIdentity(t, source), ownedIdentity(t, target)
	if err := ReplaceOwnedPath(source, target, RecursiveDirectory, RegularFile, sourceIdentity, targetIdentity); err == nil {
		t.Fatal("non-regular source disposition was accepted")
	}
	drifted := sourceIdentity
	drifted.Mode ^= 0o040
	if err := ReplaceOwnedPath(source, target, RegularFile, RegularFile, drifted, targetIdentity); err == nil {
		t.Fatal("source identity drift was accepted")
	}
	if err := os.Link(target, target+"-hardlink"); err != nil {
		t.Fatal(err)
	}
	if err := ReplaceOwnedPath(source, target, RegularFile, RegularFile, sourceIdentity, ownedIdentity(t, target)); err == nil {
		t.Fatal("hard-linked target was accepted")
	}
}

type fixture struct {
	root, source, capture, receipt string
}

func newFixture(t *testing.T, gitDirectory bool) fixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	paths := fixture{root: root, source: filepath.Join(root, "source"), capture: filepath.Join(root, "capture"), receipt: filepath.Join(root, "receipt.json")}
	mustMkdir(t, paths.source, 0o700)
	if gitDirectory {
		mustMkdir(t, filepath.Join(paths.source, ".git"), 0o700)
	}
	return paths
}

func canonicalTempDir(t *testing.T) string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func shortCanonicalTempDir(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("", "s")
	if err != nil {
		t.Fatal(err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	return root
}

func ownedIdentity(t *testing.T, path string) OwnedPathIdentity {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("filesystem identity unavailable")
	}
	return OwnedPathIdentity{
		Mode: int(stat.Mode), UID: int(stat.Uid), GID: int(stat.Gid),
		Dev: strconv.FormatUint(uint64(stat.Dev), 10), Ino: strconv.FormatUint(uint64(stat.Ino), 10),
	}
}

func quarantineCount(t *testing.T, root string) int {
	t.Helper()
	count := 0
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() && strings.HasPrefix(entry.Name(), ".chora-cleanup-") {
			count++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return count
}

func mustUnixListener(t *testing.T, path string) *net.UnixListener {
	t.Helper()
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if errors.Is(err, syscall.EPERM) {
		t.Skip("local sandbox forbids Unix-domain bind")
	}
	if err != nil {
		t.Fatal(err)
	}
	listener.SetUnlinkOnClose(false)
	return listener
}

func mustMkdir(t *testing.T, path string, mode os.FileMode) {
	t.Helper()
	if err := os.Mkdir(path, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func mustWrite(t *testing.T, path string, data []byte, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("%s mode = %04o, want %04o", path, got, want)
	}
}

func assertAbsent(t *testing.T, path string) {
	t.Helper()
	_, err := os.Lstat(path)
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s exists or cannot be inspected: %v", path, err)
	}
}

func treeContainsFile(t *testing.T, root, marker string) bool {
	t.Helper()
	found := false
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type().IsRegular() {
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if string(data) == marker {
				found = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return found
}
