package nativecapabilities

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

func TestPreparedArgumentsRejectManifestAndExtensionDrift(t *testing.T) {
	root, rootErr := filepath.EvalSymlinks(t.TempDir())
	if rootErr != nil {
		t.Fatal(rootErr)
	}
	pkg := filepath.Join(root, "pi")
	if err := os.MkdirAll(filepath.Join(pkg, "dist"), 0700); err != nil {
		t.Fatal(err)
	}
	for path, body := range map[string]string{
		"package.json":  `{"name":"@earendil-works/pi-coding-agent","version":"0.85.1","type":"module"}`,
		"dist/cli.js":   "",
		"dist/index.js": `export class SettingsManager{static create(){return {}}} export class DefaultPackageManager{async resolve(){return {skills:[],extensions:[]}}} export function loadSkills(){return {skills:[],diagnostics:[]}}`,
	} {
		if err := os.WriteFile(filepath.Join(pkg, path), []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
	}
	runtimeRoot := filepath.Join(root, "runtime")
	attempt := domain.NewAttemptID()
	project := domain.NewProjectID().String()
	arguments, environment, err := Prepare(context.Background(), runtimeRoot, project, filepath.Join(pkg, "dist/cli.js"), root, root, attempt, domain.AttemptID{}, Config{Version: 1})
	if err != nil {
		t.Fatal(err)
	}
	environment["CHORA_ATTEMPT_ID"] = attempt.String()
	target, _ := execution.NewExecutionTarget("pi", domain.TrustedHostExecutionProvider)
	invocation, err := execution.NewInvocation(execution.InvocationParams{AdapterID: "pi", Executable: filepath.Join(pkg, "dist/cli.js"), Arguments: append([]string{"--mode", "rpc"}, arguments...), Environment: environment, WorkingRoot: root, Target: target, LaunchToken: execution.LaunchToken{Value: "capability-test"}})
	if err != nil {
		t.Fatal(err)
	}
	base, err := StripValidatedArguments(runtimeRoot, invocation)
	if err != nil || len(base) != 2 {
		t.Fatalf("valid arguments: %v %v", base, err)
	}
	resumedAttempt := domain.NewAttemptID()
	resumedArguments, resumedEnvironment, err := Prepare(context.Background(), runtimeRoot, project, filepath.Join(pkg, "dist/cli.js"), root, root, resumedAttempt, attempt, Config{Version: 2, SkillPaths: []string{filepath.Join(root, "new-project-skill")}})
	if err != nil || !reflect.DeepEqual(resumedArguments, arguments) || resumedEnvironment["CHORA_NATIVE_CAPABILITIES_MANIFEST"] != environment["CHORA_NATIVE_CAPABILITIES_MANIFEST"] || resumedEnvironment["CHORA_NATIVE_CAPABILITIES_STATUS"] == environment["CHORA_NATIVE_CAPABILITIES_STATUS"] {
		t.Fatalf("resume must retain original configuration and separate status: %v %#v", err, resumedEnvironment)
	}
	original, err := os.ReadFile(environment["CHORA_NATIVE_CAPABILITIES_MANIFEST"])
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(environment["CHORA_NATIVE_CAPABILITIES_MANIFEST"], append(original, ' '), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = StripValidatedArguments(runtimeRoot, invocation); err == nil {
		t.Fatal("accepted changed manifest")
	}
	if err = os.WriteFile(environment["CHORA_NATIVE_CAPABILITIES_MANIFEST"], original, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(arguments[len(arguments)-1], []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = StripValidatedArguments(runtimeRoot, invocation); err == nil {
		t.Fatal("accepted changed extension")
	}
}

func TestRealPiCapabilityVerification(t *testing.T) {
	executable, bridge := os.Getenv("CHORA_TEST_CAPABILITY_PI"), os.Getenv("CHORA_TEST_CAPABILITY_BRIDGE")
	if executable == "" || bridge == "" {
		t.Skip("set qualified Pi and bridge paths for native integration")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	agentDir := filepath.Join(root, "agent")
	if err := os.MkdirAll(agentDir, 0700); err != nil {
		t.Fatal(err)
	}
	server := filepath.Join(root, "server.mjs")
	if err := os.WriteFile(server, []byte(`import readline from 'node:readline';
for await(const line of readline.createInterface({input:process.stdin})) {
 const m=JSON.parse(line); if(m.id===undefined)continue;
 let result={};
 if(m.method==='initialize')result={protocolVersion:'2024-11-05',capabilities:{tools:{}},serverInfo:{name:'fixture',version:'1'}};
 if(m.method==='tools/list')result={tools:[{name:'read_note',description:'Read the fixture note',inputSchema:{type:'object',properties:{}}}]};
 if(m.method==='resources/list')result={resources:[]};
 if(m.method==='prompts/list')result={prompts:[]};
 if(m.method==='tools/call')result={content:[{type:'text',text:'fixture note'}]};
 process.stdout.write(JSON.stringify({jsonrpc:'2.0',id:m.id,result})+'\n');
}`), 0600); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, "mcp.json")
	raw, _ := json.Marshal(map[string]any{"mcpServers": map[string]any{
		"healthy":  map[string]any{"command": "node", "args": []string{server}},
		"broken":   map[string]any{"command": filepath.Join(root, "missing-command")},
		"disabled": map[string]any{"command": "node", "args": []string{server}, "disabled": true},
	}})
	if err := os.WriteFile(configPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	observation, err := Verify(context.Background(), root, domain.NewProjectID().String(), executable, agentDir, root, Config{Version: 1, BridgePath: bridge, MCPConfigPath: configPath})
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, item := range observation.Servers {
		statuses[item.Name] = item.Status
	}
	if statuses["healthy"] != "connected" || statuses["broken"] != "failed" || statuses["disabled"] != "disabled" {
		t.Fatalf("native server states: %#v, bridge %#v", statuses, observation.Bridge)
	}
}

func TestDiscoveryOutputLimitIncludesIOCopy(t *testing.T) {
	var output limitedOutput
	if _, err := io.Copy(&output, bytes.NewReader(bytes.Repeat([]byte("x"), (1<<20)+1))); err == nil {
		t.Fatal("accepted unbounded output")
	}
	if len(output.Bytes()) > 1<<20 {
		t.Fatal("output exceeded bound")
	}
}
