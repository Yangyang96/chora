package isolatedenv

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/dockersupervisor"
)

func TestBrowserQualificationRequiresEvidenceAndCleanup(t *testing.T) {
	for _, test := range []struct {
		name, output string
		exit         int
		cleanupError bool
		wantError    bool
	}{
		{name: "success", output: `{"schema":"chora.browser-probe.v1","chromium":"149.0","passed":true}`},
		{name: "missing evidence", output: `{}`, wantError: true},
		{name: "failed browser", exit: 1, wantError: true},
		{name: "false evidence", output: `{"schema":"chora.browser-probe.v1","chromium":"149.0","passed":false}`, wantError: true},
		{name: "cleanup failure", output: `{"schema":"chora.browser-probe.v1","chromium":"149.0","passed":true}`, cleanupError: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			var container string
			runner := catalogRunner{run: func(ctx context.Context, cmd dockersupervisor.Command) (dockersupervisor.CommandResult, error) {
				calls++
				if _, ok := ctx.Deadline(); !ok {
					t.Fatal("unbounded probe or cleanup")
				}
				if calls == 1 {
					args := strings.Join(cmd.Args, " ")
					for _, required := range []string{"--network none", "--user 1000:1000", "--read-only", "--cap-drop ALL", "no-new-privileges:true", "--pids-limit 256", "--cpus 2", "--memory 4096m", "--memory-swap 4096m", "--ulimit nofile=1024:1024", "/opt/chora-browser/probe.mjs"} {
						if !strings.Contains(args, required) {
							t.Fatalf("missing boundary %s", required)
						}
					}
					for _, forbidden := range []string{"--privileged", "--ipc", "--mount", "--publish"} {
						if strings.Contains(args, forbidden) {
							t.Fatalf("unexpected authority %s", forbidden)
						}
					}
					container = cmd.Args[3]
					return dockersupervisor.CommandResult{Stdout: []byte(test.output), ExitCode: test.exit}, nil
				}
				if len(cmd.Args) != 3 || cmd.Args[0] != "rm" || cmd.Args[2] != container {
					t.Fatalf("wrong cleanup: %#v", cmd.Args)
				}
				if test.cleanupError {
					return dockersupervisor.CommandResult{}, errors.New("daemon unavailable")
				}
				return dockersupervisor.CommandResult{}, nil
			}}
			err := verifyBrowser(context.Background(), runner, catalogImage)
			if (err != nil) != test.wantError || calls != 2 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}
