package gitsource

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func installFakeGit(t *testing.T, body string) {
	t.Helper()
	directory := t.TempDir()
	path := filepath.Join(directory, "git")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
}

func TestGitOutputRejectsMetadataBeforeBufferingBeyondLimit(t *testing.T) {
	installFakeGit(t, "exec /usr/bin/yes x")
	output, err := gitOutput(context.Background(), t.TempDir(), "status")
	if !errors.Is(err, ErrMetadataLimit) {
		t.Fatalf("gitOutput error = %v, want ErrMetadataLimit", err)
	}
	if output != "" {
		t.Fatalf("bounded failure returned %d buffered bytes", len(output))
	}
}

func TestGitOutputHonorsCallerCancellation(t *testing.T) {
	installFakeGit(t, "exec /bin/sleep 10")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	_, err := gitOutput(ctx, t.TempDir(), "status")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("gitOutput error = %v, want context.Canceled", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("cancelled command took %s", elapsed)
	}
}

func TestGitOutputNeverBuffersStderr(t *testing.T) {
	installFakeGit(t, "/usr/bin/head -c 1200000 /dev/zero >&2; printf ok")
	output, err := gitOutput(context.Background(), t.TempDir(), "status")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output) != "ok" {
		t.Fatalf("stdout = %q", output)
	}
}

func TestDeviceAndInodeRejectsMissingFilesystemEvidence(t *testing.T) {
	if _, _, err := deviceAndInode(nil); err == nil {
		t.Fatal("nil file info unexpectedly produced identity")
	}
}
