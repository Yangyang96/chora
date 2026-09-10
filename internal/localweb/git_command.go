package localweb

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const isolatedGitExecutable = "/usr/bin/git"

// isolatedGitCommand makes every product-owned Git operation independent of
// ambient repository selectors, host configuration, hooks, helpers, lazy
// fetches, optional index refreshes, and PATH substitution.
func isolatedGitCommand(ctx context.Context, root string, arguments ...string) *exec.Cmd {
	args := []string{
		"--no-replace-objects",
		"-c", "core.hooksPath=/dev/null",
		"-c", "core.fsmonitor=false",
		"-c", "protocol.allow=never",
		"-c", "gc.auto=0",
		"-C", root,
	}
	args = append(args, arguments...)
	command := exec.CommandContext(ctx, isolatedGitExecutable, args...)
	command.Env = isolatedGitEnvironment(root)
	return command
}

func isolatedGitEnvironment(root string) []string {
	environment := make([]string, 0, len(os.Environ())+11)
	for _, entry := range os.Environ() {
		key := entry
		if index := strings.IndexByte(entry, '='); index >= 0 {
			key = entry[:index]
		}
		if strings.HasPrefix(key, "GIT_") || key == "HOME" ||
			key == "XDG_CONFIG_HOME" || key == "SSH_ASKPASS" {
			continue
		}
		environment = append(environment, entry)
	}
	return append(environment,
		"HOME=/var/empty",
		"XDG_CONFIG_HOME=/var/empty",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_ASKPASS=/usr/bin/false",
		"SSH_ASKPASS=/usr/bin/false",
		"GIT_CEILING_DIRECTORIES="+filepath.Dir(filepath.Clean(root)),
	)
}
