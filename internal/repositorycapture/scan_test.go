package repositorycapture

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestScanRepositoryIsDeterministicAndHashesRegularFiles(t *testing.T) {
	root := canonicalTempDir(t)
	mustMkdir(t, filepath.Join(root, "z-directory"), 0o700)
	mustWrite(t, filepath.Join(root, "z-directory", "nested.txt"), []byte("nested\n"), 0o600)
	mustWrite(t, filepath.Join(root, "a.txt"), []byte("alpha\n"), 0o400)
	identity := ownedIdentity(t, root)

	first, err := ScanRepository(root, identity)
	if err != nil {
		t.Fatal(err)
	}
	second, err := ScanRepository(root, identity)
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := MarshalRepositoryScanManifest(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := MarshalRepositoryScanManifest(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("repository scan is not deterministic:\n%s\n%s", firstJSON, secondJSON)
	}
	paths := make([]string, 0, len(first.Entries))
	for _, entry := range first.Entries {
		paths = append(paths, entry.Path)
	}
	wantPaths := []string{"a.txt", "z-directory", "z-directory/nested.txt"}
	if !reflect.DeepEqual(paths, wantPaths) {
		t.Fatalf("entry paths = %#v, want %#v", paths, wantPaths)
	}
	if first.EntryCount != 3 || first.TotalBytes != int64(len("alpha\n")+len("nested\n")) || first.ManifestDigest == "" {
		t.Fatalf("unexpected manifest summary: %+v", first)
	}
	assertScanHashes(t, first, "a.txt", []byte("alpha\n"))
}

func TestRepositoryScanDigestMatchesSortedJavaScriptCanonicalJSON(t *testing.T) {
	manifest := RepositoryScanManifest{
		SchemaVersion: "schema", Status: "passed", CaptureRoot: "/capture",
		RootIdentity: OwnedPathIdentity{Mode: 1, UID: 2, GID: 3, Dev: "4", Ino: "5"},
		Entries: []RepositoryScanEntry{{
			Path: "<&>", Kind: "regular_file", Mode: 1, UID: 2, GID: 3, Size: 4,
			SHA256: "h", GitBlobSHA1: "g",
		}},
		EntryCount: 1, TotalBytes: 4,
	}
	digest, err := repositoryScanDigest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	canonical := `{"captureRoot":"/capture","entries":[{"gid":3,"gitBlobSha1":"g","kind":"regular_file","mode":1,"path":"<&>","sha256":"h","size":4,"uid":2}],"entryCount":1,"rootIdentity":{"dev":"4","gid":3,"ino":"5","mode":1,"uid":2},"schemaVersion":"schema","status":"passed","totalBytes":4}`
	want := sha256.Sum256([]byte(canonical))
	if digest != hex.EncodeToString(want[:]) {
		t.Fatalf("canonical digest = %s, want %s", digest, hex.EncodeToString(want[:]))
	}
	encoded, err := marshalCanonicalJSON(repositoryScanValue(manifest, false))
	if err != nil || string(encoded) != canonical {
		t.Fatalf("canonical JSON = %q, want %q (err=%v)", encoded, canonical, err)
	}
}

func TestScanRepositoryRejectsDirectoryEntrySwapToExternal(t *testing.T) {
	container := canonicalTempDir(t)
	root := filepath.Join(container, "capture")
	mustMkdir(t, root, 0o700)
	mustMkdir(t, filepath.Join(root, "directory"), 0o700)
	mustWrite(t, filepath.Join(root, "directory", "owned.txt"), []byte("owned\n"), 0o600)
	external := filepath.Join(container, "external-original")
	var hookErr error
	hooks := &repositoryScanHooks{afterDirectoryOpen: func(relative string) {
		if relative != "directory" || hookErr != nil {
			return
		}
		hookErr = os.Rename(filepath.Join(root, "directory"), external)
		if hookErr == nil {
			hookErr = os.Mkdir(filepath.Join(root, "directory"), 0o700)
		}
		if hookErr == nil {
			hookErr = os.WriteFile(filepath.Join(root, "directory", "foreign.txt"), []byte("foreign\n"), 0o600)
		}
	}}
	_, err := scanRepositoryWithConfig(root, ownedIdentity(t, root), repositoryScanConfig{
		limits: defaultRepositoryScanLimits, hooks: hooks,
	})
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if err == nil {
		t.Fatal("directory entry swap was accepted")
	}
	assertFileValue(t, filepath.Join(external, "owned.txt"), "owned\n")
	assertFileValue(t, filepath.Join(root, "directory", "foreign.txt"), "foreign\n")
}

func TestScanRepositoryRejectsRegularFileSwapBeforeRead(t *testing.T) {
	for _, replacement := range []string{"symlink", "fifo"} {
		t.Run(replacement, func(t *testing.T) {
			root := canonicalTempDir(t)
			path := filepath.Join(root, "file.txt")
			moved := filepath.Join(root, "file-owned.txt")
			mustWrite(t, path, []byte("owned\n"), 0o600)
			readCalls := 0
			var hookErr error
			hooks := &repositoryScanHooks{
				beforeFileOpen: func(relative string) {
					if relative != "file.txt" || hookErr != nil {
						return
					}
					hookErr = os.Rename(path, moved)
					if hookErr != nil {
						return
					}
					if replacement == "symlink" {
						hookErr = os.Symlink(moved, path)
					} else {
						hookErr = unix.Mkfifo(path, 0o600)
					}
				},
				beforeFileRead: func(string) { readCalls++ },
			}
			_, err := scanRepositoryWithConfig(root, ownedIdentity(t, root), repositoryScanConfig{
				limits: defaultRepositoryScanLimits, hooks: hooks,
			})
			if hookErr != nil {
				t.Fatal(hookErr)
			}
			if err == nil {
				t.Fatalf("%s replacement was accepted", replacement)
			}
			if readCalls != 0 {
				t.Fatalf("%s replacement was read %d times", replacement, readCalls)
			}
			assertFileValue(t, moved, "owned\n")
		})
	}
}

func TestScanRepositoryRejectsRegularFileEntrySwapAfterRead(t *testing.T) {
	root := canonicalTempDir(t)
	path := filepath.Join(root, "file.txt")
	moved := filepath.Join(root, "file-owned.txt")
	mustWrite(t, path, []byte("owned\n"), 0o600)
	var hookErr error
	hooks := &repositoryScanHooks{afterFileRead: func(relative string) {
		if relative != "file.txt" || hookErr != nil {
			return
		}
		hookErr = os.Rename(path, moved)
		if hookErr == nil {
			hookErr = os.WriteFile(path, []byte("foreign\n"), 0o600)
		}
	}}
	_, err := scanRepositoryWithConfig(root, ownedIdentity(t, root), repositoryScanConfig{
		limits: defaultRepositoryScanLimits, hooks: hooks,
	})
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if err == nil {
		t.Fatal("post-read regular-file entry swap was accepted")
	}
	assertFileValue(t, moved, "owned\n")
	assertFileValue(t, path, "foreign\n")
}

func TestScanRepositoryRejectsRootMoveAndReplacement(t *testing.T) {
	container := canonicalTempDir(t)
	root := filepath.Join(container, "capture")
	moved := filepath.Join(container, "capture-moved")
	mustMkdir(t, root, 0o700)
	mustWrite(t, filepath.Join(root, "owned.txt"), []byte("owned\n"), 0o600)
	var hookErr error
	hooks := &repositoryScanHooks{afterRootOpen: func() {
		hookErr = os.Rename(root, moved)
		if hookErr == nil {
			hookErr = os.Mkdir(root, 0o700)
		}
	}}
	_, err := scanRepositoryWithConfig(root, ownedIdentity(t, root), repositoryScanConfig{
		limits: defaultRepositoryScanLimits, hooks: hooks,
	})
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if err == nil {
		t.Fatal("root move and replacement was accepted")
	}
	assertFileValue(t, filepath.Join(moved, "owned.txt"), "owned\n")
}

func TestScanRepositoryRejectsRootParentMoveAndReplacement(t *testing.T) {
	container := canonicalTempDir(t)
	parent := filepath.Join(container, "parent")
	movedParent := filepath.Join(container, "parent-moved")
	root := filepath.Join(parent, "capture")
	mustMkdir(t, parent, 0o700)
	mustMkdir(t, root, 0o700)
	mustWrite(t, filepath.Join(root, "owned.txt"), []byte("owned\n"), 0o600)
	identity := ownedIdentity(t, root)
	var hookErr error
	hooks := &repositoryScanHooks{afterRootOpen: func() {
		hookErr = os.Rename(parent, movedParent)
		if hookErr == nil {
			hookErr = os.Mkdir(parent, 0o700)
		}
		if hookErr == nil {
			hookErr = os.Mkdir(root, 0o700)
		}
	}}
	_, err := scanRepositoryWithConfig(root, identity, repositoryScanConfig{
		limits: defaultRepositoryScanLimits, hooks: hooks,
	})
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if err == nil {
		t.Fatal("root parent move and replacement was accepted")
	}
	assertFileValue(t, filepath.Join(movedParent, "capture", "owned.txt"), "owned\n")
}

func TestScanRepositoryRejectsHigherAncestorReplacementEvenWhenRootAndParentStayExact(t *testing.T) {
	container := canonicalTempDir(t)
	ancestor := filepath.Join(container, "ancestor")
	movedAncestor := filepath.Join(container, "ancestor-moved")
	parent := filepath.Join(ancestor, "parent")
	root := filepath.Join(parent, "capture")
	mustMkdir(t, ancestor, 0o700)
	mustMkdir(t, parent, 0o700)
	mustMkdir(t, root, 0o700)
	mustWrite(t, filepath.Join(root, "owned.txt"), []byte("owned\n"), 0o600)
	identity := ownedIdentity(t, root)
	var hookErr error
	hooks := &repositoryScanHooks{afterRootOpen: func() {
		hookErr = os.Rename(ancestor, movedAncestor)
		if hookErr == nil {
			hookErr = os.Mkdir(ancestor, 0o700)
		}
		if hookErr == nil {
			hookErr = os.Rename(filepath.Join(movedAncestor, "parent"), parent)
		}
	}}
	_, err := scanRepositoryWithConfig(root, identity, repositoryScanConfig{
		limits: defaultRepositoryScanLimits, hooks: hooks,
	})
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if err == nil {
		t.Fatal("higher ancestor replacement was accepted despite an exact root and immediate parent")
	}
	if got := ownedIdentity(t, root); got != identity {
		t.Fatalf("test did not preserve exact root identity: got=%+v want=%+v", got, identity)
	}
	assertFileValue(t, filepath.Join(root, "owned.txt"), "owned\n")
}

func TestScanRepositoryRejectsNPlusOneBeforeStat(t *testing.T) {
	root := canonicalTempDir(t)
	mustWrite(t, filepath.Join(root, "one"), []byte("1"), 0o600)
	mustWrite(t, filepath.Join(root, "two"), []byte("2"), 0o600)
	limits := defaultRepositoryScanLimits
	limits.maxEntries = 1
	statCalls := 0
	_, err := scanRepositoryWithConfig(root, ownedIdentity(t, root), repositoryScanConfig{
		limits: limits, hooks: &repositoryScanHooks{beforeEntryStat: func(string) { statCalls++ }},
	})
	if err == nil || !strings.Contains(err.Error(), "entry-count limit") {
		t.Fatalf("N+1 entry error = %v", err)
	}
	if statCalls != 1 {
		t.Fatalf("entry stat calls = %d, want 1", statCalls)
	}
}

func TestScanRepositoryRejectsTotalBudgetBeforeOpenOrRead(t *testing.T) {
	root := canonicalTempDir(t)
	mustWrite(t, filepath.Join(root, "large"), []byte("123456"), 0o600)
	limits := defaultRepositoryScanLimits
	limits.maxTotalBytes = 5
	openCalls, readCalls := 0, 0
	_, err := scanRepositoryWithConfig(root, ownedIdentity(t, root), repositoryScanConfig{
		limits: limits,
		hooks: &repositoryScanHooks{
			beforeFileOpen: func(string) { openCalls++ },
			beforeFileRead: func(string) { readCalls++ },
		},
	})
	if err == nil || !strings.Contains(err.Error(), "before read") {
		t.Fatalf("total budget error = %v", err)
	}
	if openCalls != 0 || readCalls != 0 {
		t.Fatalf("over-budget file reached open/read: open=%d read=%d", openCalls, readCalls)
	}
}

func TestScanRepositoryRejectsManifestBudgetBeforeOpenOrRead(t *testing.T) {
	root := canonicalTempDir(t)
	path := filepath.Join(root, "file")
	mustWrite(t, path, []byte("content"), 0o600)
	identity := ownedIdentity(t, root)
	probe := repositoryScanState{
		config: repositoryScanConfig{limits: defaultRepositoryScanLimits},
		manifest: RepositoryScanManifest{
			SchemaVersion: RepositoryScanSchema, Status: "passed", CaptureRoot: root,
			RootIdentity: identity, Entries: []RepositoryScanEntry{},
		},
	}
	if err := probe.initializeOutputBudget(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	node, err := nodeFromFileInfo(info)
	if err != nil {
		t.Fatal(err)
	}
	placeholder := scanEntryFromNode("file", "regular_file", node, strings.Repeat("0", 64), strings.Repeat("0", 40))
	encoded, err := json.Marshal(placeholder)
	if err != nil {
		t.Fatal(err)
	}
	limits := defaultRepositoryScanLimits
	limits.maxManifestBytes = probe.outputBudget + int64(len(encoded))
	openCalls, readCalls := 0, 0
	_, err = scanRepositoryWithConfig(root, identity, repositoryScanConfig{
		limits: limits,
		hooks: &repositoryScanHooks{
			beforeFileOpen: func(string) { openCalls++ },
			beforeFileRead: func(string) { readCalls++ },
		},
	})
	if err == nil || !strings.Contains(err.Error(), "manifest budget") {
		t.Fatalf("manifest budget error = %v", err)
	}
	if openCalls != 0 || readCalls != 0 {
		t.Fatalf("manifest-over-budget file reached open/read: open=%d read=%d", openCalls, readCalls)
	}
}

func TestReadRepositoryFilesReturnsExactSortedBoundedContent(t *testing.T) {
	root := canonicalTempDir(t)
	mustMkdir(t, filepath.Join(root, ".git"), 0o700)
	mustWrite(t, filepath.Join(root, ".git", "config"), []byte("config\n"), 0o600)
	mustWrite(t, filepath.Join(root, "HEAD"), []byte("ref: refs/heads/main\n"), 0o400)
	paths := []string{".git/config", "HEAD"}
	response, err := ReadRepositoryFiles(root, ownedIdentity(t, root), paths)
	if err != nil {
		t.Fatal(err)
	}
	if response.FileCount != 2 || response.TotalBytes != int64(len("config\n")+len("ref: refs/heads/main\n")) || response.ResponseDigest == "" {
		t.Fatalf("unexpected response summary: %+v", response)
	}
	if got := []string{response.Files[0].Path, response.Files[1].Path}; !reflect.DeepEqual(got, paths) {
		t.Fatalf("response paths = %#v, want %#v", got, paths)
	}
	decoded, err := base64.StdEncoding.DecodeString(response.Files[0].BytesBase64)
	if err != nil || string(decoded) != "config\n" {
		t.Fatalf("decoded content = %q, err=%v", decoded, err)
	}
	if _, err := MarshalRepositoryReadFilesResponse(response); err != nil {
		t.Fatal(err)
	}
}

func TestReadRepositoryFilesRejectsDirectoryAncestorSwap(t *testing.T) {
	container := canonicalTempDir(t)
	root := filepath.Join(container, "capture")
	mustMkdir(t, root, 0o700)
	mustMkdir(t, filepath.Join(root, ".git"), 0o700)
	mustWrite(t, filepath.Join(root, ".git", "config"), []byte("owned\n"), 0o600)
	external := filepath.Join(container, "external-git")
	var hookErr error
	hooks := &repositoryScanHooks{afterDirectoryOpen: func(relative string) {
		if relative != ".git" || hookErr != nil {
			return
		}
		hookErr = os.Rename(filepath.Join(root, ".git"), external)
		if hookErr == nil {
			hookErr = os.Mkdir(filepath.Join(root, ".git"), 0o700)
		}
		if hookErr == nil {
			hookErr = os.WriteFile(filepath.Join(root, ".git", "config"), []byte("foreign\n"), 0o600)
		}
	}}
	_, err := readRepositoryFilesWithConfig(root, ownedIdentity(t, root), []string{".git/config"}, repositoryReadConfig{
		limits: defaultRepositoryReadLimits, hooks: hooks,
	})
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if err == nil {
		t.Fatal("repository read accepted directory ancestor swap")
	}
	assertFileValue(t, filepath.Join(external, "config"), "owned\n")
	assertFileValue(t, filepath.Join(root, ".git", "config"), "foreign\n")
}

func TestReadRepositoryFilesRejectsFinalSymlinkOrFIFOReplacement(t *testing.T) {
	for _, replacement := range []string{"symlink", "fifo"} {
		t.Run(replacement, func(t *testing.T) {
			root := canonicalTempDir(t)
			path := filepath.Join(root, "config")
			moved := filepath.Join(root, "config-owned")
			mustWrite(t, path, []byte("owned\n"), 0o600)
			readCalls := 0
			var hookErr error
			hooks := &repositoryScanHooks{
				beforeFileOpen: func(relative string) {
					if relative != "config" || hookErr != nil {
						return
					}
					hookErr = os.Rename(path, moved)
					if hookErr != nil {
						return
					}
					if replacement == "symlink" {
						hookErr = os.Symlink(moved, path)
					} else {
						hookErr = unix.Mkfifo(path, 0o600)
					}
				},
				beforeFileRead: func(string) { readCalls++ },
			}
			_, err := readRepositoryFilesWithConfig(root, ownedIdentity(t, root), []string{"config"}, repositoryReadConfig{
				limits: defaultRepositoryReadLimits, hooks: hooks,
			})
			if hookErr != nil {
				t.Fatal(hookErr)
			}
			if err == nil {
				t.Fatalf("repository read accepted %s replacement", replacement)
			}
			if readCalls != 0 {
				t.Fatalf("repository read reached %s replacement content", replacement)
			}
			assertFileValue(t, moved, "owned\n")
		})
	}
}

func TestReadRepositoryFilesRejectsFinalEntrySwapAfterRead(t *testing.T) {
	root := canonicalTempDir(t)
	path := filepath.Join(root, "config")
	moved := filepath.Join(root, "config-owned")
	mustWrite(t, path, []byte("owned\n"), 0o600)
	var hookErr error
	hooks := &repositoryScanHooks{afterFileRead: func(relative string) {
		if relative != "config" || hookErr != nil {
			return
		}
		hookErr = os.Rename(path, moved)
		if hookErr == nil {
			hookErr = os.WriteFile(path, []byte("foreign\n"), 0o600)
		}
	}}
	_, err := readRepositoryFilesWithConfig(root, ownedIdentity(t, root), []string{"config"}, repositoryReadConfig{
		limits: defaultRepositoryReadLimits, hooks: hooks,
	})
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if err == nil {
		t.Fatal("repository read accepted post-read entry swap")
	}
	assertFileValue(t, moved, "owned\n")
	assertFileValue(t, path, "foreign\n")
}

func TestReadRepositoryFilesRejectsBudgetBeforeOpenOrRead(t *testing.T) {
	root := canonicalTempDir(t)
	mustWrite(t, filepath.Join(root, "config"), []byte("123456"), 0o600)
	limits := defaultRepositoryReadLimits
	limits.maxTotalBytes = 5
	openCalls, readCalls := 0, 0
	_, err := readRepositoryFilesWithConfig(root, ownedIdentity(t, root), []string{"config"}, repositoryReadConfig{
		limits: limits,
		hooks: &repositoryScanHooks{
			beforeFileOpen: func(string) { openCalls++ },
			beforeFileRead: func(string) { readCalls++ },
		},
	})
	if err == nil || !strings.Contains(err.Error(), "before read") {
		t.Fatalf("repository read total budget error = %v", err)
	}
	if openCalls != 0 || readCalls != 0 {
		t.Fatalf("over-budget repository read reached open/read: open=%d read=%d", openCalls, readCalls)
	}
}

func TestReadRepositoryFilesRejectsOutputBudgetBeforeOpenOrRead(t *testing.T) {
	root := canonicalTempDir(t)
	path := filepath.Join(root, "config")
	value := []byte("content")
	mustWrite(t, path, value, 0o600)
	identity := ownedIdentity(t, root)
	probe := repositoryReadState{
		config: repositoryReadConfig{limits: defaultRepositoryReadLimits},
		response: RepositoryReadFilesResponse{
			SchemaVersion: RepositoryReadFilesSchema, Status: "passed", CaptureRoot: root,
			RootIdentity: identity, Files: []RepositoryReadFile{},
		},
	}
	if err := probe.initializeOutputBudget(); err != nil {
		t.Fatal(err)
	}
	pathJSON, err := json.Marshal("config")
	if err != nil {
		t.Fatal(err)
	}
	itemBudget := int64(len(pathJSON)+base64.StdEncoding.EncodedLen(len(value))) + 160
	limits := defaultRepositoryReadLimits
	limits.maxOutputBytes = probe.outputBudget + itemBudget
	openCalls, readCalls := 0, 0
	_, err = readRepositoryFilesWithConfig(root, identity, []string{"config"}, repositoryReadConfig{
		limits: limits,
		hooks: &repositoryScanHooks{
			beforeFileOpen: func(string) { openCalls++ },
			beforeFileRead: func(string) { readCalls++ },
		},
	})
	if err == nil || !strings.Contains(err.Error(), "output budget") {
		t.Fatalf("repository read output budget error = %v", err)
	}
	if openCalls != 0 || readCalls != 0 {
		t.Fatalf("output-over-budget repository read reached open/read: open=%d read=%d", openCalls, readCalls)
	}
}

func TestReadRepositoryFilesRejectsUnsortedOrDuplicatePathsBeforeRootOpen(t *testing.T) {
	root := canonicalTempDir(t)
	mustWrite(t, filepath.Join(root, "a"), []byte("a"), 0o600)
	for _, paths := range [][]string{{"a", "a"}, {"z", "a"}, {"../a"}, {"a\\b"}} {
		rootOpened := 0
		_, err := readRepositoryFilesWithConfig(root, ownedIdentity(t, root), paths, repositoryReadConfig{
			limits: defaultRepositoryReadLimits,
			hooks:  &repositoryScanHooks{afterRootOpen: func() { rootOpened++ }},
		})
		if err == nil {
			t.Fatalf("unsafe paths %#v were accepted", paths)
		}
		if rootOpened != 0 {
			t.Fatalf("unsafe paths %#v reached root open", paths)
		}
	}
}

func assertScanHashes(t *testing.T, manifest RepositoryScanManifest, path string, content []byte) {
	t.Helper()
	for _, entry := range manifest.Entries {
		if entry.Path != path {
			continue
		}
		sha256Value := sha256.Sum256(content)
		gitInput := append([]byte("blob "+strconv.Itoa(len(content))+"\x00"), content...)
		gitValue := sha1.Sum(gitInput) // Repository format contract.
		if entry.SHA256 != hex.EncodeToString(sha256Value[:]) || entry.GitBlobSHA1 != hex.EncodeToString(gitValue[:]) {
			t.Fatalf("hashes for %s = sha256:%s git:%s", path, entry.SHA256, entry.GitBlobSHA1)
		}
		return
	}
	t.Fatalf("manifest lacks %s", path)
}

func assertFileValue(t *testing.T, path, want string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil || string(got) != want {
		t.Fatalf("%s = %q, want %q (err=%v)", path, got, want, err)
	}
}
