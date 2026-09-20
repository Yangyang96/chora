package nativecapabilities

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestDiscoveryKeepsInheritedMCPAndPrefersProjectOverrides(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	piRoot := filepath.Join(root, "pi")
	bridgeRoot := filepath.Join(root, "bridge")
	if err = os.MkdirAll(filepath.Join(piRoot, "dist"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Join(bridgeRoot, "dist"), 0o700); err != nil {
		t.Fatal(err)
	}
	inheritedSkill := filepath.Join(root, "inherited", "SKILL.md")
	projectSkill := filepath.Join(root, "project", "SKILL.md")
	for path, body := range map[string]string{
		filepath.Join(piRoot, "package.json"):   `{"name":"@earendil-works/pi-coding-agent","version":"0.85.1","type":"module"}`,
		filepath.Join(piRoot, "dist", "cli.js"): "",
		filepath.Join(piRoot, "dist", "index.js"): `
import { readFileSync } from 'node:fs'
export class SettingsManager { static create() { return {} } }
export class DefaultPackageManager { async resolve() { return { skills: [{ enabled: true, path: process.env.CHORA_TEST_INHERITED_SKILL, metadata: { scope: 'user' } }], extensions: [] } } }
export function loadSkills({ skillPaths }) {
  const found = new Map()
	const diagnostics = []
  for (const filePath of skillPaths) {
    const skill = JSON.parse(readFileSync(filePath, 'utf8'))
    if (!found.has(skill.name)) found.set(skill.name, { ...skill, filePath, sourceInfo: { scope: 'user' } })
	else diagnostics.push({ type: 'collision', path: filePath })
  }
	return { skills: [...found.values()], diagnostics }
}`,
		filepath.Join(bridgeRoot, "package.json"):      `{"name":"pi-mcp-adapter","version":"2.34.0","type":"module","files":["index.ts","runtime-helper.ts","dist"]}`,
		filepath.Join(bridgeRoot, "index.ts"):          "export const fixture = true\n",
		filepath.Join(bridgeRoot, "runtime-helper.ts"): "export const runtimeFixture = true\n",
		filepath.Join(bridgeRoot, "dist", "config.js"): `
const inherited = { mcpServers: { global: { command: 'global' }, collision: { command: 'global', disabled: true } }, settings: { inherited: true } }
const project = { mcpServers: { project: { command: 'project' }, collision: { command: 'project' } }, settings: { project: true } }
export function loadMcpConfig(path) {
  if (process.env.PI_MCP_CONFIG_MODE === 'exclusive' && process.env.CHORA_TEST_INVALID_PROJECT_MCP === '1') {
    console.warn('Failed to load MCP config from ' + path + ':', new Error('invalid fixture'))
    return { mcpServers: {} }
  }
  return structuredClone(process.env.PI_MCP_CONFIG_MODE === 'exclusive' ? project : inherited)
}
export function getServerProvenance() { return new Map([['global', { kind: 'user' }], ['collision', { kind: 'user' }]]) }
`,
		inheritedSkill: `{"name":"collision","description":"inherited"}`,
		projectSkill:   `{"name":"collision","description":"project"}`,
	} {
		if err = os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	projectConfig := filepath.Join(root, "project-mcp.json")
	if err = os.WriteFile(projectConfig, []byte(`{"mcpServers":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CHORA_TEST_INHERITED_SKILL", inheritedSkill)

	inventory, err := Inspect(context.Background(), filepath.Join(piRoot, "dist", "cli.js"), root, root, Config{
		Version: 1, SkillPaths: []string{projectSkill}, DisabledSkillPaths: []string{inheritedSkill}, BridgePath: bridgeRoot, MCPConfigPath: projectConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inventory.Skills) != 1 || inventory.Skills[0].Path != projectSkill || inventory.Skills[0].Source != "project" || !inventory.Skills[0].Enabled {
		t.Fatalf("Project Skill did not win inherited name collision: %#v", inventory.Skills)
	}
	servers := make(map[string]Capability, len(inventory.Servers))
	for _, server := range inventory.Servers {
		servers[server.Name] = server
	}
	if servers["global"].Source != "user" || !servers["global"].Enabled {
		t.Fatalf("inherited server missing: %#v", servers)
	}
	if servers["project"].Source != "project" || !servers["project"].Enabled {
		t.Fatalf("Project server missing: %#v", servers)
	}
	if servers["collision"].Source != "project" || !servers["collision"].Enabled {
		t.Fatalf("Project server did not override inherited server: %#v", servers)
	}
	owner := domain.NewAttemptID()
	runtimeRoot := filepath.Join(root, "runtime")
	projectID := domain.NewProjectID().String()
	config := Config{Version: 1, SkillPaths: []string{projectSkill}, DisabledSkillPaths: []string{inheritedSkill}, BridgePath: bridgeRoot, MCPConfigPath: projectConfig}
	if _, _, err = Prepare(context.Background(), runtimeRoot, projectID, filepath.Join(piRoot, "dist", "cli.js"), root, root, owner, domain.AttemptID{}, config); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(bridgeRoot, "runtime-helper.ts"), []byte("export const runtimeFixture = false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = Prepare(context.Background(), runtimeRoot, projectID, filepath.Join(piRoot, "dist", "cli.js"), root, root, domain.NewAttemptID(), owner, config); err == nil {
		t.Fatal("resume accepted changed transitive bridge source")
	}
	t.Setenv("CHORA_TEST_INVALID_PROJECT_MCP", "1")
	invalid, err := Inspect(context.Background(), filepath.Join(piRoot, "dist", "cli.js"), root, root, Config{
		Version: 1, BridgePath: bridgeRoot, MCPConfigPath: projectConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	if invalid.Bridge.Status != "unavailable" {
		t.Fatalf("invalid explicit Project MCP config was silently accepted: %#v", invalid)
	}
	t.Setenv("CHORA_TEST_INVALID_PROJECT_MCP", "0")
	missingBridge, err := Inspect(context.Background(), filepath.Join(piRoot, "dist", "cli.js"), root, root, Config{
		Version: 1, MCPConfigPath: projectConfig,
	})
	if err != nil {
		t.Fatal(err)
	}
	if missingBridge.Bridge.Status != "unavailable" {
		t.Fatalf("explicit MCP config without bridge was not unavailable: %#v", missingBridge.Bridge)
	}
}

func TestRealPiSkillVerificationReportsAvailable(t *testing.T) {
	executable := os.Getenv("CHORA_TEST_CAPABILITY_PI")
	if executable == "" {
		t.Skip("set qualified Pi path for native Skill integration")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	agentDir := filepath.Join(root, "agent")
	skill := filepath.Join(root, "skill", "SKILL.md")
	disabledSkill := filepath.Join(root, "disabled-skill", "SKILL.md")
	missingSkill := filepath.Join(root, "missing-skill", "SKILL.md")
	if err = os.MkdirAll(filepath.Dir(skill), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(filepath.Dir(disabledSkill), 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(skill, []byte("---\nname: chora-verification-fixture\ndescription: Verify one disposable Skill.\n---\n\nFixture.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(disabledSkill, []byte("---\nname: chora-disabled-fixture\ndescription: Keep one disposable Skill disabled.\n---\n\nFixture.\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	observation, err := Verify(context.Background(), root, domain.NewProjectID().String(), executable, agentDir, root, Config{
		Version: 1, SkillPaths: []string{skill, disabledSkill, missingSkill}, DisabledSkillPaths: []string{disabledSkill},
	})
	if err != nil {
		t.Fatal(err)
	}
	statuses := make(map[string]string, len(observation.Skills))
	for _, item := range observation.Skills {
		statuses[item.Path] = item.Status
	}
	if statuses[skill] != "available" || statuses[disabledSkill] != "disabled" || statuses[missingSkill] != "unavailable" {
		t.Fatalf("verified Skill status = %#v", observation.Skills)
	}
}

func TestRealPiMCPVerificationKeepsGlobalAndProjectServers(t *testing.T) {
	executable, bridge := os.Getenv("CHORA_TEST_CAPABILITY_PI"), os.Getenv("CHORA_TEST_CAPABILITY_BRIDGE")
	if executable == "" || bridge == "" {
		t.Skip("set qualified Pi and bridge paths for additive MCP integration")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", root)
	agentDir := filepath.Join(root, "agent")
	if err = os.MkdirAll(agentDir, 0o700); err != nil {
		t.Fatal(err)
	}
	server := filepath.Join(root, "server.mjs")
	if err = os.WriteFile(server, []byte(`import readline from 'node:readline';
for await(const line of readline.createInterface({input:process.stdin})) {
 const m=JSON.parse(line); if(m.id===undefined)continue;
 let result={};
 if(m.method==='initialize')result={protocolVersion:'2024-11-05',capabilities:{tools:{}},serverInfo:{name:'fixture',version:'1'}};
 if(m.method==='tools/list')result={tools:[{name:'read_note',description:'Read the fixture note',inputSchema:{type:'object',properties:{}}}]};
 if(m.method==='resources/list')result={resources:[]};
 if(m.method==='prompts/list')result={prompts:[]};
 process.stdout.write(JSON.stringify({jsonrpc:'2.0',id:m.id,result})+'\n');
}`), 0o600); err != nil {
		t.Fatal(err)
	}
	writeConfig := func(path string, servers map[string]any) {
		raw, encodeErr := json.Marshal(map[string]any{"mcpServers": servers})
		if encodeErr != nil {
			t.Fatal(encodeErr)
		}
		if writeErr := os.WriteFile(path, raw, 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	writeConfig(filepath.Join(agentDir, "mcp.json"), map[string]any{
		"global":    map[string]any{"command": "node", "args": []string{server}},
		"collision": map[string]any{"command": filepath.Join(root, "missing-global-command")},
	})
	projectConfig := filepath.Join(root, "project-mcp.json")
	writeConfig(projectConfig, map[string]any{
		"project":   map[string]any{"command": "node", "args": []string{server}},
		"collision": map[string]any{"command": "node", "args": []string{server}},
	})

	observation, err := Verify(context.Background(), root, domain.NewProjectID().String(), executable, agentDir, root, Config{Version: 1, BridgePath: bridge, MCPConfigPath: projectConfig})
	if err != nil {
		t.Fatal(err)
	}
	servers := make(map[string]Capability, len(observation.Servers))
	for _, server := range observation.Servers {
		servers[server.Name] = server
	}
	for _, name := range []string{"global", "project", "collision"} {
		if servers[name].Status != "connected" {
			t.Fatalf("server %q was not connected: %#v", name, servers)
		}
	}
	if servers["global"].Source != "user" || servers["project"].Source != "project" || servers["collision"].Source != "project" {
		t.Fatalf("server provenance = %#v", servers)
	}
}
