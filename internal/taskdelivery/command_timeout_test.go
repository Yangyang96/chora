package taskdelivery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func writeExecutable(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "command")
	if err := os.WriteFile(path, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestGitTimeoutDoesNotWaitForInheritedHelperPipes(t *testing.T) {
	root := t.TempDir()
	pidFile := filepath.Join(root, "helper.pid")
	defer func() {
		if raw, err := os.ReadFile(pidFile); err == nil {
			if pid, err := strconv.Atoi(strings.TrimSpace(string(raw))); err == nil {
				if child, err := os.FindProcess(pid); err == nil {
					_ = child.Kill()
				}
			}
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	// The helper holds stdout/stderr after CommandContext kills its Git parent.
	_, err := run(ctx, root, nil, nil, "-c", "alias.slow=!sleep 10 & echo $! > '"+pidFile+"'; wait", "slow")
	if !errors.Is(err, ErrRecovery) {
		t.Fatalf("timeout must require recovery: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("helper pipes kept the request pending for %s", elapsed)
	}
	if _, err := os.Stat(pidFile); err != nil {
		t.Fatalf("helper did not start; timeout scenario was not exercised: %v", err)
	}
}

func TestGitHubCommandReportsWhetherChildStarted(t *testing.T) {
	t.Run("start failure", func(t *testing.T) {
		g := &githubCLI{path: filepath.Join(t.TempDir(), "missing-gh"), available: true}
		if _, started, err := g.runTracked(context.Background(), nil, "api"); err == nil || started {
			t.Fatalf("started=%t error=%v", started, err)
		}
	})

	t.Run("cancellation after start", func(t *testing.T) {
		readyFile := filepath.Join(t.TempDir(), "ready")
		path := writeExecutable(t, "#!/bin/sh\necho ready > \"$READY_FILE\"\nexec sleep 10\n")
		g := &githubCLI{path: path, available: true, env: []string{"READY_FILE=" + readyFile}}
		ctx, cancel := context.WithCancel(context.Background())
		result := make(chan struct {
			started bool
			err     error
		}, 1)
		go func() {
			_, started, err := g.runTracked(ctx, nil, "api")
			result <- struct {
				started bool
				err     error
			}{started: started, err: err}
		}()
		deadline := time.Now().Add(10 * time.Second)
		for {
			if _, err := os.Stat(readyFile); err == nil {
				break
			}
			select {
			case got := <-result:
				t.Fatalf("command exited before reporting ready: started=%t error=%v", got.started, got.err)
			default:
			}
			if time.Now().After(deadline) {
				cancel()
				t.Fatal("command did not report ready")
			}
			time.Sleep(10 * time.Millisecond)
		}
		start := time.Now()
		cancel()
		var got struct {
			started bool
			err     error
		}
		select {
		case got = <-result:
		case <-time.After(2 * time.Second):
			t.Fatal("command did not stop promptly after cancellation")
		}
		started, err := got.started, got.err
		if err == nil || !started {
			t.Fatalf("started=%t error=%v", started, err)
		}
		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Fatalf("helper pipes kept the request pending for %s", elapsed)
		}
	})

	t.Run("output limit after start", func(t *testing.T) {
		path := writeExecutable(t, fmt.Sprintf("#!/bin/sh\n/usr/bin/yes x | /usr/bin/head -c %d\n", commandOutputLimit+(1<<20)))
		g := &githubCLI{path: path, available: true}
		if _, started, err := g.runTracked(context.Background(), nil, "api"); err == nil || !started {
			t.Fatalf("started=%t error=%v", started, err)
		}
	})
}

func TestCappedBufferLimitsIOCopy(t *testing.T) {
	var out cappedBuffer
	out.limit = 4
	n, err := io.Copy(&out, io.LimitReader(strings.NewReader("abcdef"), 6))
	if err != nil {
		t.Fatal(err)
	}
	if n != 6 || out.Len() != 4 || string(out.Bytes()) != "abcd" || !out.exceeded {
		t.Fatalf("copied=%d length=%d output=%q exceeded=%t", n, out.Len(), out.Bytes(), out.exceeded)
	}
}
