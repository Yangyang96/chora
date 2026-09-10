package pidistribution

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
)

const maxVersionOutputBytes = 128

var errVersionOutputTooLarge = errors.New("Pi version output exceeds limit")

type VersionResult struct {
	Stdout   []byte
	Stderr   []byte
	ExitCode int
}

// VersionRunner is an injection seam for the one allowed subprocess:
// the selected executable invoked directly with exactly "--version".
type VersionRunner func(context.Context, string, ...string) (VersionResult, error)

func defaultVersionRunner(ctx context.Context, executable string, arguments ...string) (VersionResult, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	var stdout, stderr boundedOutput
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	result := VersionResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: 0}
	if err == nil {
		return result, nil
	}
	if ctx.Err() != nil {
		return VersionResult{}, ctx.Err()
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		result.ExitCode = exitError.ExitCode()
		return result, nil
	}
	return VersionResult{}, err
}

type boundedOutput struct{ data []byte }

func (output *boundedOutput) Write(value []byte) (int, error) {
	remaining := maxVersionOutputBytes - len(output.data)
	if remaining <= 0 {
		return 0, errVersionOutputTooLarge
	}
	if len(value) > remaining {
		output.data = append(output.data, value[:remaining]...)
		return remaining, errVersionOutputTooLarge
	}
	output.data = append(output.data, value...)
	return len(value), nil
}

func (output *boundedOutput) Bytes() []byte { return append([]byte(nil), output.data...) }

func verifyVersion(ctx context.Context, runner VersionRunner, executable string) error {
	result, err := runner(ctx, executable, "--version")
	if err != nil {
		return fmt.Errorf("%w: %v", ErrVersionRejected, err)
	}
	if result.ExitCode != 0 || len(result.Stderr) != 0 || !cleanProbeOutput(result.Stdout) {
		return fmt.Errorf("%w: exit=%d stdout/stderr do not match exact contract", ErrVersionRejected, result.ExitCode)
	}
	return nil
}
