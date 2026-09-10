package localweb

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
)

type codexRunWorkspace struct {
	sourceRoot string
	runRoot   string
	worktree  string
	home      string
	codexHome string
}

func prepareCodexRunWorkspace(sourceRoot, runtimeRoot string, runID domain.RunID) (codexRunWorkspace, error) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		return codexRunWorkspace{}, fmt.Errorf("find git: %w", err)
	}
	top, err := runCommand(gitPath, "-C", sourceRoot, "rev-parse", "--show-toplevel")
	if err != nil {
		return codexRunWorkspace{}, fmt.Errorf("resolve source repository: %w", err)
	}
	top = strings.TrimSpace(top)
	if top == "" || !filepath.IsAbs(top) {
		return codexRunWorkspace{}, fmt.Errorf("source repository root is invalid")
	}
	runRoot := filepath.Join(runtimeRoot, "runs", runID.String())
	workspace := codexRunWorkspace{
		sourceRoot: top, runRoot: runRoot, worktree: filepath.Join(runRoot, "workspace"),
		home: filepath.Join(runRoot, "home"), codexHome: filepath.Join(runRoot, "codex-home"),
	}
	for _, path := range []string{workspace.runRoot, workspace.home, workspace.codexHome} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			return codexRunWorkspace{}, fmt.Errorf("create Codex run directory: %w", err)
		}
		if err := os.Chmod(path, 0o700); err != nil {
			return codexRunWorkspace{}, fmt.Errorf("secure Codex run directory: %w", err)
		}
	}
	if _, err := runCommand(gitPath, "-C", top, "worktree", "add", "--detach", workspace.worktree, "HEAD"); err != nil {
		_ = os.RemoveAll(runRoot)
		return codexRunWorkspace{}, fmt.Errorf("create disposable Codex worktree: %w", err)
	}
	if err := copyCodexAuth(workspace.codexHome); err != nil {
		_, _ = runCommand(gitPath, "-C", top, "worktree", "remove", "--force", workspace.worktree)
		_ = os.RemoveAll(runRoot)
		return codexRunWorkspace{}, err
	}
	return workspace, nil
}

func (workspace codexRunWorkspace) cleanup() {
	if workspace.sourceRoot != "" && workspace.worktree != "" {
		if gitPath, err := exec.LookPath("git"); err == nil {
			_, _ = runCommand(gitPath, "-C", workspace.sourceRoot, "worktree", "remove", "--force", workspace.worktree)
		}
	}
	if workspace.runRoot != "" {
		_ = os.RemoveAll(workspace.runRoot)
	}
}

func copyCodexAuth(destination string) error {
	sourceHome := strings.TrimSpace(os.Getenv("CODEX_HOME"))
	if sourceHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return fmt.Errorf("resolve Codex home: %w", err)
		}
		sourceHome = filepath.Join(home, ".codex")
	}
	source := filepath.Join(sourceHome, "auth.json")
	input, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open Codex authentication: %w", err)
	}
	defer input.Close()
	output, err := os.OpenFile(filepath.Join(destination, "auth.json"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return fmt.Errorf("create isolated Codex authentication: %w", err)
	}
	if _, err := io.Copy(output, input); err != nil {
		output.Close()
		return fmt.Errorf("copy isolated Codex authentication: %w", err)
	}
	if err := output.Close(); err != nil {
		return fmt.Errorf("close isolated Codex authentication: %w", err)
	}
	return nil
}

func runCommand(name string, arguments ...string) (string, error) {
	command := exec.Command(name, arguments...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
