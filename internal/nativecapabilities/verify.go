package nativecapabilities

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

// Verify starts a disposable Pi session, connects configured servers via the
// native extension command, then shuts it down. It never sends a model prompt.
func Verify(ctx context.Context, root, projectID, executable, agentDir, cwd string, config Config) (Observation, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	directory, err := os.MkdirTemp(root, "capability-check-")
	if err != nil {
		return Observation{}, err
	}
	defer os.RemoveAll(directory)
	directory, err = filepath.EvalSymlinks(directory)
	if err != nil {
		return Observation{}, err
	}
	version := config.Version
	if config.Version == 0 {
		config.Version = 1
	}
	attempt := domain.NewAttemptID()
	args, env, err := Prepare(ctx, directory, projectID, executable, agentDir, cwd, attempt, domain.AttemptID{}, config)
	if err != nil {
		return Observation{}, err
	}
	// Verification loads only the Chora wrapper and Skills, not unrelated user
	// hooks/extensions. The normal task path retains all inherited extensions.
	filtered := []string{}
	for index := 0; index < len(args); index++ {
		if args[index] == "--extension" && index+1 < len(args) {
			if index+2 == len(args) {
				filtered = append(filtered, args[index:index+2]...)
			}
			index++
			continue
		}
		filtered = append(filtered, args[index])
	}
	command := exec.CommandContext(ctx, executable, append([]string{"--mode", "rpc", "--no-session"}, filtered...)...)
	command.Dir = cwd
	command.Env = append(os.Environ(), "PI_CODING_AGENT_DIR="+agentDir, "CHORA_ATTEMPT_ID="+attempt.String())
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.WaitDelay = 2 * time.Second
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
	input, err := command.StdinPipe()
	if err != nil {
		return Observation{}, err
	}
	output, err := command.StdoutPipe()
	if err != nil {
		return Observation{}, err
	}
	if err = command.Start(); err != nil {
		return Observation{}, errors.New("Pi capability verification could not start")
	}
	defer func() { input.Close(); _ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL); _ = command.Wait() }()
	encoder := json.NewEncoder(input)
	if err = encoder.Encode(map[string]string{"id": "chora-capabilities-commands", "type": "get_commands"}); err != nil {
		return Observation{}, err
	}
	scanner := bufio.NewScanner(output)
	scanner.Buffer(make([]byte, 4096), 1<<20)
	ready := false
	for scanner.Scan() {
		var response struct {
			ID      string `json:"id"`
			Type    string `json:"type"`
			Success bool   `json:"success"`
			Data    struct {
				Commands []struct {
					Name string `json:"name"`
				} `json:"commands"`
			} `json:"data"`
		}
		if json.Unmarshal(scanner.Bytes(), &response) != nil || response.Type != "response" {
			continue
		}
		if response.ID == "chora-capabilities-commands" {
			hasMCP := false
			for _, item := range response.Data.Commands {
				if item.Name == "mcp" {
					hasMCP = true
				}
			}
			if !hasMCP {
				ready = true
				break
			}
			if err = encoder.Encode(map[string]string{"id": "chora-capabilities-retry", "type": "prompt", "message": "/mcp reconnect"}); err != nil {
				return Observation{}, err
			}
		}
		if response.ID == "chora-capabilities-retry" {
			ready = response.Success
			break
		}
	}
	if !ready {
		return Observation{}, errors.New("Pi capability verification did not complete; inspect the bridge or authentication and retry")
	}
	project, err := domain.ParseProjectID(projectID)
	if err != nil {
		return Observation{}, err
	}
	observation, err := ReadObservation(directory, project, attempt)
	if err != nil {
		return Observation{}, errors.New("Pi did not report capability state for this verification")
	}
	observation.ConfigVersion = version
	observation.Running = false
	return observation, nil
}
