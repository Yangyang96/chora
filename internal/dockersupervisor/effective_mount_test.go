package dockersupervisor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/execution"
)

func TestExactMountsDockerDesktopBindIdentity(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	canonical, err := filepath.EvalSymlinks(source)
	if err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(source, alias); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, actual, expected, kind string
		rw, want                     bool
	}{
		{"exact bind", source, source, "bind", false, true},
		{"desktop VM path", "/host_mnt" + canonical, source, "bind", false, runtime.GOOS == "darwin"},
		{"desktop canonical alias", "/host_mnt" + canonical, alias, "bind", false, runtime.GOOS == "darwin"},
		{"different source", "/host_mnt" + canonical + "-other", source, "bind", false, false},
		{"writable source", "/host_mnt" + canonical, source, "bind", true, false},
		{"wrong type", "/host_mnt" + canonical, source, "volume", false, false},
		{"missing source", "/host_mnt" + canonical + "/missing", source + "/missing", "bind", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body, _ := json.Marshal(map[string]any{"Mounts": []map[string]any{{"Type": tc.kind, "Source": tc.actual, "Name": tc.actual, "Destination": "/input/context", "RW": tc.rw}}})
			var container effectiveContainer
			if err := json.Unmarshal(body, &container); err != nil {
				t.Fatal(err)
			}
			expected := map[string]effectiveMount{"/input/context": {Source: tc.expected, RW: false}}
			if got := exactMounts(container, expected); got != tc.want {
				t.Fatalf("exactMounts=%v want %v", got, tc.want)
			}
			container.Mounts = append(container.Mounts, container.Mounts[0])
			if exactMounts(container, expected) {
				t.Fatal("accepted extra mount")
			}
		})
	}
}

// Keep the number of observations unchanged while hiding a required mount.
type duplicateWorkbenchMountRunner struct{ *fakeRunner }

func (runner duplicateWorkbenchMountRunner) Run(ctx context.Context, command Command) (CommandResult, error) {
	result, err := runner.fakeRunner.Run(ctx, command)
	if err != nil || len(command.Args) != 5 || command.Args[0] != "container" || command.Args[1] != "inspect" || command.Args[3] != "{{json .}}" {
		return result, err
	}
	var container effectiveContainer
	if err := json.Unmarshal(result.Stdout, &container); err != nil {
		return CommandResult{}, err
	}
	for i := range container.Mounts {
		if container.Mounts[i].Destination == "/input/context" {
			for _, mount := range container.Mounts {
				if mount.Destination == "/workspace" {
					container.Mounts[i] = mount
					break
				}
			}
			break
		}
	}
	result.Stdout, err = json.Marshal(container)
	return result, err
}

func TestWorkbenchRejectsDuplicateMountBeforeLoadingWorkspaceOrCredentials(t *testing.T) {
	runner := &fakeRunner{}
	supervisor := newWorkbenchTestSupervisor(t, runner, func(context.Context, execution.Invocation, string) error {
		t.Error("collected workspace after invalid mounts")
		return nil
	})
	supervisor.config.Runner = duplicateWorkbenchMountRunner{runner}
	invocation := testWorkbenchInvocation(t, testSource(t), "duplicate-mount", supervisor.config)
	outcome := supervisor.Start(context.Background(), invocation, &testSink{binding: invocation.LaunchToken()})
	if outcome.Kind != execution.StartProvenNoChild || !strings.Contains(outcome.Diagnostic, "effective Workbench mounts drift") {
		t.Fatalf("Start = %#v", outcome)
	}
	for _, command := range runner.allCommands() {
		if len(command.Args) > 0 && command.Args[0] == "exec" {
			t.Fatalf("executed container command after invalid mounts: %v", command.Args)
		}
	}
}

func TestExactMountsRequiresEveryDestinationExactlyOnce(t *testing.T) {
	expected := map[string]effectiveMount{
		"/workspace":     {Type: "volume", Source: "task-workspace", RW: true},
		"/input/context": {Source: "/private/context", RW: false},
	}
	for _, tc := range []struct {
		name   string
		mounts string
		want   bool
	}{
		{"complete", `[{"Type":"volume","Name":"task-workspace","Destination":"/workspace","RW":true},{"Type":"bind","Source":"/private/context","Destination":"/input/context","RW":false}]`, true},
		{"reordered", `[{"Type":"bind","Source":"/private/context","Destination":"/input/context","RW":false},{"Type":"volume","Name":"task-workspace","Destination":"/workspace","RW":true}]`, true},
		{"duplicate volume hides context", `[{"Type":"volume","Name":"task-workspace","Destination":"/workspace","RW":true},{"Type":"volume","Name":"task-workspace","Destination":"/workspace","RW":true}]`, false},
		{"duplicate bind hides workspace", `[{"Type":"bind","Source":"/private/context","Destination":"/input/context","RW":false},{"Type":"bind","Source":"/private/context","Destination":"/input/context","RW":false}]`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var container effectiveContainer
			if err := json.Unmarshal([]byte(`{"Mounts":`+tc.mounts+`}`), &container); err != nil {
				t.Fatal(err)
			}
			if got := exactMounts(container, expected); got != tc.want {
				t.Fatalf("exactMounts = %v, want %v", got, tc.want)
			}
		})
	}
}
