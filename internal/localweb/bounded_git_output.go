package localweb

import (
	"context"
	"errors"
	"io"
)

var errGitMetadataLimit = errors.New("Git metadata exceeds the supported size limit; narrow the repository or task output")

// Bound allocation before consuming metadata, and stop Git when it exceeds it.
func gitTargetOutputBounded(ctx context.Context, root string, limit int64, arguments ...string) ([]byte, error) {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	command := isolatedGitCommand(ctx, root, arguments...)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return nil, err
	}
	output, readErr := io.ReadAll(io.LimitReader(stdout, limit+1))
	if readErr != nil || int64(len(output)) > limit {
		cancel()
	}
	waitErr := command.Wait()
	if int64(len(output)) > limit {
		return nil, errGitMetadataLimit
	}
	if readErr != nil {
		return nil, readErr
	}
	if waitErr != nil {
		return nil, waitErr
	}
	return output, nil
}
