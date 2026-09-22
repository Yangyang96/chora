package isolatedenv

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/dockersupervisor"
)

//go:embed browser/package.json browser/package-lock.json browser/browser.mjs browser/probe.mjs
var browserInputs embed.FS

func browserInputDigest() string {
	var contents []byte
	for _, name := range browserInputNames {
		data, _ := browserInputs.ReadFile("browser/" + name)
		contents = append(contents, []byte(name+"\x00")...)
		contents = append(contents, data...)
	}
	return digestBytes(contents)
}

var browserInputNames = []string{"package.json", "package-lock.json", "browser.mjs", "probe.mjs"}

func writeBrowserInputs(stage string) error {
	root := filepath.Join(stage, "browser")
	if err := os.Mkdir(root, 0700); err != nil {
		return err
	}
	for _, name := range browserInputNames {
		data, err := browserInputs.ReadFile("browser/" + name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			return err
		}
	}
	return nil
}

// Verify the installed browser against the execution limits, without credentials,
// published ports, host mounts, extra capabilities or host IPC.
func verifyBrowser(ctx context.Context, runner dockersupervisor.CommandRunner, imageID string) (returnErr error) {
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	name := "chora-browser-probe-" + hex.EncodeToString(nonce[:])
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		result, err := runner.Run(cleanupCtx, dockersupervisor.Command{Args: []string{"rm", "-f", name}})
		if err != nil || result.ExitCode != 0 && !strings.Contains(strings.ToLower(string(result.Stderr)), "no such container") {
			returnErr = errors.Join(returnErr, fmt.Errorf("browser probe cleanup failed: exit=%d error=%v", result.ExitCode, err))
		}
	}()
	probeCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	result, err := runner.Run(probeCtx, dockersupervisor.Command{Args: []string{
		"run", "--rm", "--name", name, "--pull=never", "--network", "none",
		"--user", "1000:1000", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true",
		"--pids-limit", "256", "--cpus", "2", "--memory", "4096m", "--memory-swap", "4096m", "--ulimit", "nofile=1024:1024",
		"--tmpfs", "/tmp:rw,nosuid,nodev,noexec,uid=1000,gid=1000,size=64m",
		"--tmpfs", "/run/chora/pi:rw,nosuid,nodev,noexec,uid=1000,gid=1000,size=16m",
		"--env", "HOME=/run/chora/pi", "--entrypoint", "node", imageID, "/opt/chora-browser/probe.mjs",
	}})
	var evidence struct {
		Schema   string `json:"schema"`
		Chromium string `json:"chromium"`
		Passed   bool   `json:"passed"`
	}
	if err != nil || result.ExitCode != 0 || json.Unmarshal(result.Stdout, &evidence) != nil || evidence.Schema != "chora.browser-probe.v1" || !evidence.Passed || evidence.Chromium == "" {
		return fmt.Errorf("qualify isolated browser: exit=%d error=%v stderr=%s", result.ExitCode, err, boundedDiagnostic(result.Stderr))
	}
	return nil
}
