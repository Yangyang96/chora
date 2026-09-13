package dockersupervisor

import (
	"context"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/isolatedproxy"
	"os"
	"strings"
	"testing"
)

func TestWorkbenchProxyUsesPrivateFileAndExactEffectiveEnvironment(t *testing.T) {
	process := newFakeProcess()
	runner := &fakeRunner{processes: []*fakeProcess{process}, boundaryStopped: true}
	supervisor := newWorkbenchTestSupervisor(t, runner, func(context.Context, execution.Invocation, string) error { return nil })
	proxy := isolatedproxy.Config{HTTPProxy: "http://proxy.example:8080", NoProxy: "localhost"}
	supervisor.config.Workbench.Proxy = proxy
	inv := testWorkbenchInvocation(t, testSource(t), "proxy-lifecycle", supervisor.config)
	outcome := supervisor.Start(context.Background(), inv, &testSink{binding: inv.LaunchToken()})
	if outcome.Kind != execution.Started {
		t.Fatalf("Start: %+v", outcome)
	}
	for _, command := range runner.allCommands() {
		for i, arg := range command.Args {
			if strings.Contains(arg, "proxy.example") {
				t.Fatal("endpoint leaked into argv")
			}
			if arg == "--env-file" {
				if _, err := os.Stat(command.Args[i+1]); !os.IsNotExist(err) {
					t.Fatal("proxy env file retained")
				}
			}
		}
	}
	metadata, err := supervisor.validateInvocation(inv, &testSink{binding: inv.LaunchToken()})
	if err != nil {
		t.Fatal(err)
	}
	env := []string{"HOME=/run/chora/pi", "PI_CODING_AGENT_DIR=/run/chora/pi", "PI_SKIP_VERSION_CHECK=1", "PI_TELEMETRY=0"}
	for key, value := range metadata.environment {
		env = append(env, key+"="+value)
	}
	if !workbenchEnvironmentMatches(env, metadata.environment) {
		t.Fatal("bound environment rejected")
	}
	for _, bad := range []string{"https_proxy=http://other.example:8080", "HTTPS_PROXY=", "all_proxy=socks5://other.example:1080", "no_proxy=*"} {
		if workbenchEnvironmentMatches(append(append([]string{}, env...), bad), metadata.environment) {
			t.Fatalf("accepted routing drift %s", bad)
		}
	}
	process.stdout.Write([]byte("failed at http://proxy.example:8080\n"))
	process.exit(0)
	waitDone(t, supervisor.record(outcome.Handle).done)
	output, err := supervisor.Read(context.Background(), outcome.Handle, execution.StreamStdout, 0, 4096)
	if err != nil || strings.Contains(string(output.Data), "proxy.example") {
		t.Fatal("endpoint leaked into stream")
	}
}
