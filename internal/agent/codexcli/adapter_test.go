package codexcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Yangyang96/chora/internal/agent"
	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/processbound"
	"github.com/Yangyang96/chora/internal/runtimeboundary"
)

const testManagedPath = "/managed/bin:/usr/bin:/bin"

var testParentProxyEnvironmentKeys = []string{
	"HTTP_PROXY", "http_proxy", "HTTPS_PROXY", "https_proxy",
	"ALL_PROXY", "all_proxy", "FTP_PROXY", "ftp_proxy",
	"WS_PROXY", "ws_proxy", "WSS_PROXY", "wss_proxy",
	"NO_PROXY", "no_proxy",
	"YARN_HTTP_PROXY", "YARN_HTTPS_PROXY", "YARN_NO_PROXY",
	"NPM_CONFIG_HTTP_PROXY", "NPM_CONFIG_HTTPS_PROXY", "NPM_CONFIG_PROXY", "NPM_CONFIG_NOPROXY",
	"npm_config_http_proxy", "npm_config_https_proxy", "npm_config_proxy", "npm_config_noproxy",
	"BUNDLE_HTTP_PROXY", "BUNDLE_HTTPS_PROXY", "BUNDLE_NO_PROXY",
	"PIP_PROXY", "DOCKER_HTTP_PROXY", "DOCKER_HTTPS_PROXY",
	"CODEX_NETWORK_PROXY_ACTIVE", "CODEX_NETWORK_ALLOW_LOCAL_BINDING",
	"ELECTRON_GET_USE_PROXY_ENV", "NODE_USE_ENV_PROXY",
}

func expectedManagedNetworkArgs(cacheRoot, tmpRoot string) []string {
	return []string{
		"--strict-config",
		"-c", `sandbox_mode="workspace-write"`,
		"-c", "sandbox_workspace_write.network_access=true",
		"-c", "features.network_proxy.enabled=true",
		"-c", `features.network_proxy.mode="full"`,
		"-c", `features.network_proxy.domains={"registry.npmjs.org"="allow"}`,
		"-c", "features.network_proxy.allow_upstream_proxy=false",
		"-c", "features.network_proxy.allow_local_binding=false",
		"-c", "features.network_proxy.enable_socks5=false",
		"-c", "features.network_proxy.enable_socks5_udp=false",
		"-c", "features.network_proxy.dangerously_allow_non_loopback_proxy=false",
		"-c", "features.network_proxy.dangerously_allow_all_unix_sockets=false",
		"-c", "sandbox_workspace_write.exclude_tmpdir_env_var=true",
		"-c", "sandbox_workspace_write.exclude_slash_tmp=true",
		"-c", `sandbox_workspace_write.writable_roots=["` + cacheRoot + `","` + tmpRoot + `"]`,
	}
}

func expectedSanitizedCodexPrefix() []string {
	prefix := make([]string, 0, 2*len(testParentProxyEnvironmentKeys)+1)
	for _, key := range testParentProxyEnvironmentKeys {
		prefix = append(prefix, "-u", key)
	}
	return append(prefix, InnerCodexExecutable)
}

func TestAdapterConformanceAndFingerprint(t *testing.T) {
	adapter, _ := testAdapter(t, nil)
	if adapter.contract.Schema != "chora.codex-managed-shell.v2" {
		t.Fatalf("managed shell schema = %q, want v2", adapter.contract.Schema)
	}
	if err := agent.CheckConformance(context.Background(), adapter); err != nil {
		t.Fatalf("CheckConformance() error = %v", err)
	}
	first, err := adapter.Fingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	second, err := adapter.Fingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first != second || first.Version != "0.146.0" || !first.Valid() {
		t.Fatalf("fingerprints = %+v / %+v", first, second)
	}
}

func TestFingerprintFailsClosedWhenManagedNetworkGateFieldIsMissing(t *testing.T) {
	for _, field := range []string{
		"managed_network_proxy", "registry_egress", "non_allowlisted_egress_isolation",
		"managed_loopback_isolation", "managed_local_binding_isolation", "upstream_proxy",
		"socks5", "socks5_udp", "non_loopback_proxy", "all_unix_sockets", "unix_socket_allowlist",
		"environment_wrapper", "environment_wrapper_sha256", "codex_executable", "codex_binary_sha256", "allowed_destination_payload_isolation",
	} {
		t.Run(field, func(t *testing.T) {
			config := testConfig(t, nil)
			baseRead := config.ReadFile
			config.ReadFile = func(path string) ([]byte, error) {
				data, err := baseRead(path)
				if err != nil || path != config.GatePath {
					return data, err
				}
				var fields map[string]any
				if err := json.Unmarshal(data, &fields); err != nil {
					return nil, err
				}
				delete(fields, field)
				return json.Marshal(fields)
			}
			adapter, err := New(config)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := adapter.Fingerprint(context.Background()); err == nil {
				t.Fatalf("Fingerprint accepted gate missing %q", field)
			}
		})
	}
}

func TestFingerprintFailsClosedForInvalidGateEvidence(t *testing.T) {
	mutations := map[string]func(*gateManifest){
		"gate":                    func(g *gateManifest) { g.RealAdapterGate = "FAIL" },
		"host read":               func(g *gateManifest) { g.HostReadIsolation = "proven" },
		"workspace":               func(g *gateManifest) { g.WorkspaceWriteIsolation = "unavailable" },
		"control":                 func(g *gateManifest) { g.ControlPlaneIsolation = "unavailable" },
		"loopback":                func(g *gateManifest) { g.LoopbackIsolation = "unavailable" },
		"profile":                 func(g *gateManifest) { g.ProfileTemplateSHA256 = strings.Repeat("0", 64) },
		"renderer":                func(g *gateManifest) { g.ProfileRendererVersion = "changed" },
		"codex sha":               func(g *gateManifest) { g.CodexBinarySHA256 = strings.Repeat("0", 64) },
		"sandbox sha":             func(g *gateManifest) { g.SandboxExecSHA256 = strings.Repeat("0", 64) },
		"contract":                func(g *gateManifest) { g.ContractSHA256 = strings.Repeat("0", 64) },
		"os build":                func(g *gateManifest) { g.OSBuild = "changed" },
		"version":                 func(g *gateManifest) { g.CodexVersion = "0.147.0" },
		"managed network":         func(g *gateManifest) { g.ManagedNetworkProxy = "unavailable" },
		"registry egress":         func(g *gateManifest) { g.RegistryEgress = "unavailable" },
		"non-allowlisted egress":  func(g *gateManifest) { g.NonAllowlistedEgressIsolation = "unavailable" },
		"managed loopback":        func(g *gateManifest) { g.ManagedLoopbackIsolation = "unavailable" },
		"managed local binding":   func(g *gateManifest) { g.ManagedLocalBindingIsolation = "unavailable" },
		"upstream proxy":          func(g *gateManifest) { g.UpstreamProxy = "enabled" },
		"socks5":                  func(g *gateManifest) { g.SOCKS5 = "enabled" },
		"socks5 udp":              func(g *gateManifest) { g.SOCKS5UDP = "enabled" },
		"non-loopback proxy":      func(g *gateManifest) { g.NonLoopbackProxy = "enabled" },
		"all unix sockets":        func(g *gateManifest) { g.AllUnixSockets = "enabled" },
		"unix socket allowlist":   func(g *gateManifest) { g.UnixSocketAllowlist = "nonempty" },
		"environment wrapper":     func(g *gateManifest) { g.EnvironmentWrapper = "unavailable" },
		"environment wrapper sha": func(g *gateManifest) { g.EnvironmentWrapperSHA256 = strings.Repeat("0", 64) },
		"Codex executable":        func(g *gateManifest) { g.CodexExecutable = "/tmp/codex" },
		"payload isolation":       func(g *gateManifest) { g.AllowedDestinationPayloadIsolation = "proven" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			adapter, _ := testAdapter(t, mutate)
			if _, err := adapter.Fingerprint(context.Background()); err == nil {
				t.Fatal("Fingerprint() accepted invalid gate evidence")
			}
		})
	}
}

func TestFingerprintRejectsActualButNonFrozenCodexSHA(t *testing.T) {
	config := testConfig(t, nil)
	config.expectedCodexSHA256 = requiredCodexSHA256
	adapter, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Fingerprint(context.Background()); err == nil {
		t.Fatal("Fingerprint accepted an actual but non-frozen Codex SHA-256")
	}
}

func TestFingerprintRejectsEnvironmentBytesThatDoNotMatchExpectedSHA(t *testing.T) {
	config := testConfig(t, nil)
	baseRead := config.ReadFile
	config.ReadFile = func(path string) ([]byte, error) {
		if path == EnvironmentExecutable {
			return []byte("different environment executable"), nil
		}
		return baseRead(path)
	}
	adapter, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Fingerprint(context.Background()); err == nil {
		t.Fatal("Fingerprint accepted environment bytes that did not match the expected SHA-256")
	}
}

func TestNormalizeConfigDefaultsToFrozenEnvironmentSHA(t *testing.T) {
	input := testConfig(t, nil)
	input.expectedEnvironmentSHA256 = ""
	config, err := normalizeConfig(input)
	if err != nil {
		t.Fatal(err)
	}
	if config.expectedEnvironmentSHA256 != requiredEnvironmentSHA256 {
		t.Fatalf("expected environment SHA-256 = %q", config.expectedEnvironmentSHA256)
	}
}

func TestPrepareStartUsesFrozenEnvelopeAndPerAttemptResult(t *testing.T) {
	adapter, config := testAdapter(t, nil)
	run, attempt := runAndAttempt(t)
	workspace := filepath.Join(t.TempDir(), "workspace")
	request := execution.StartRequest{
		Run: run, Attempt: attempt, WorkspaceRoot: workspace,
		SnapshotDocument: []byte(`{"snapshot":"frozen"}`),
		LaunchToken:      execution.LaunchToken{Value: "launch-start"},
	}
	invocation, err := adapter.PrepareStart(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(config.RuntimeRoot, "runs", run.ID().String(), "attempts", attempt.ID().String(), resultFileName)
	attemptRoot := filepath.Dir(resultPath)
	cacheRoot := filepath.Join(attemptRoot, "npm-cache")
	tmpRoot := filepath.Join(attemptRoot, "tmp")
	wantArgs := append(expectedSanitizedCodexPrefix(), "exec", "--ignore-user-config", "--ignore-rules", "--json", "--output-schema", config.SchemaPath, "-o", resultPath, "--skip-git-repo-check", "-C", workspace)
	wantArgs = append(wantArgs, expectedManagedNetworkArgs(cacheRoot, tmpRoot)...)
	wantArgs = append(wantArgs, "-")
	if invocation.Executable() != CodexExecutable || !slices.Equal(invocation.Arguments(), wantArgs) {
		t.Fatalf("invocation = %q %#v", invocation.Executable(), invocation.Arguments())
	}
	environment := invocation.Environment()
	if environment["HOME"] != filepath.Join(config.RuntimeRoot, "runs", run.ID().String(), "home") ||
		environment["CODEX_HOME"] != filepath.Join(config.RuntimeRoot, "runs", run.ID().String(), "codex-home") ||
		environment["PATH"] != testManagedPath ||
		environment["CHORA_MANAGED_PATH"] != testManagedPath ||
		environment["BASH_ENV"] != filepath.Join(filepath.Dir(resultPath), adapter.contract.BootstrapFileName) ||
		environment["NPM_CONFIG_CACHE"] != cacheRoot ||
		environment["TMPDIR"] != tmpRoot ||
		environment[TerminalResultEnvironment] != resultPath {
		t.Fatalf("environment = %#v", environment)
	}
	stdin := string(invocation.Stdin())
	openingTag := "<chora_frozen_context_snapshot>\n"
	closingTag := "</chora_frozen_context_snapshot>\n"
	openingIndex := strings.Index(stdin, openingTag)
	if openingIndex < 0 || strings.Count(stdin, openingTag) != 1 || strings.Count(stdin, closingTag) != 1 {
		t.Fatalf("stdin must contain exactly one frozen snapshot envelope: %q", stdin)
	}
	snapshotStart := openingIndex + len(openingTag)
	closingOffset := strings.Index(stdin[snapshotStart:], closingTag)
	if closingOffset < 0 {
		t.Fatalf("closing frozen snapshot delimiter must follow opening delimiter: %q", stdin)
	}
	closingIndex := snapshotStart + closingOffset
	promptInstructions := stdin[:openingIndex]
	frozenEnvelope := stdin[openingIndex : closingIndex+len(closingTag)]
	frozenSnapshot := stdin[snapshotStart:closingIndex]
	wantFrozenSnapshot := string(request.SnapshotDocument)
	if !strings.HasSuffix(wantFrozenSnapshot, "\n") {
		wantFrozenSnapshot += "\n"
	}
	if frozenSnapshot != wantFrozenSnapshot {
		t.Fatalf("frozen snapshot = %q, want exact supplied bytes with normalized trailing newline %q", frozenSnapshot, wantFrozenSnapshot)
	}
	if trailing := stdin[closingIndex+len(closingTag):]; trailing != "" {
		t.Fatalf("unexpected content after frozen snapshot envelope: %q", trailing)
	}
	for _, checkRule := range []string{
		"Every checks[].criterion_id must exactly match one of the frozen acceptance_criteria[].id values; use only frozen criterion IDs.",
		"Do not emit extra checks for scope or any other non-criterion concept.",
		"Put non-criterion observations in unknowns without inventing a criterion_id.",
		"Emit exactly one check for every frozen acceptance_criteria[].id, in the same order; use UNKNOWN when evidence is insufficient instead of omitting a criterion.",
	} {
		if !strings.Contains(promptInstructions, checkRule) {
			t.Fatalf("start prompt instructions missing check rule %q before frozen snapshot: %q", checkRule, stdin)
		}
		if strings.Contains(frozenEnvelope, checkRule) {
			t.Fatalf("check rule %q was placed inside frozen snapshot: %q", checkRule, stdin)
		}
	}
	for _, terminalRule := range []string{
		"review_ready is true only when handoff.requested is false",
		"handoff.requested is true only when review_ready is false",
		"When both are false, revision is required",
	} {
		if !strings.Contains(promptInstructions, terminalRule) {
			t.Fatalf("start prompt instructions missing terminal rule %q before frozen snapshot: %q", terminalRule, stdin)
		}
		if strings.Contains(frozenEnvelope, terminalRule) {
			t.Fatalf("terminal rule %q was placed inside frozen snapshot: %q", terminalRule, stdin)
		}
	}
	if _, err := os.Stat(filepath.Dir(resultPath)); err != nil {
		t.Fatalf("attempt result directory was not created: %v", err)
	}
}

func TestWritableRootsRendererEscapesPathsAndRejectsThirdRoot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "quote\"back\\slash\nline")
	config := testConfig(t, nil)
	config.RuntimeRoot = root
	adapter, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	run, attempt := runAndAttempt(t)
	invocation, err := adapter.PrepareStart(context.Background(), startRequest(run, attempt, filepath.Join(t.TempDir(), "workspace")))
	if err != nil {
		t.Fatal(err)
	}
	attemptRoot := filepath.Join(root, "runs", run.ID().String(), "attempts", attempt.ID().String())
	want := "sandbox_workspace_write.writable_roots=[" + strconv.Quote(filepath.Join(attemptRoot, "npm-cache")) + "," + strconv.Quote(filepath.Join(attemptRoot, "tmp")) + "]"
	if !slices.Contains(invocation.Arguments(), want) {
		t.Fatalf("writable roots were not TOML escaped: %#v", invocation.Arguments())
	}
	adapter.contract.ManagedNetworkArguments = append(adapter.contract.ManagedNetworkArguments,
		"-c", `sandbox_workspace_write.writable_roots=["/third"]`)
	if _, err := adapter.PrepareStart(context.Background(), startRequest(run, attempt, filepath.Join(t.TempDir(), "workspace"))); err == nil {
		t.Fatal("PrepareStart accepted a third writable root")
	}
}

func TestPrepareStartRejectsAttemptAncestorSymlinkDrift(t *testing.T) {
	adapter, config := testAdapter(t, nil)
	run, attempt := runAndAttempt(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(config.RuntimeRoot, "runs")); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.PrepareStart(context.Background(), startRequest(run, attempt, filepath.Join(t.TempDir(), "workspace"))); err == nil {
		t.Fatal("PrepareStart accepted a symlinked attempt ancestor")
	}
}

func TestManagedNetworkContractIsIdenticalForFreshAndResume(t *testing.T) {
	adapter, config := testAdapter(t, nil)
	run, startAttempt := runAndAttempt(t)
	workspace := filepath.Join(t.TempDir(), "workspace")
	start, err := adapter.PrepareStart(context.Background(), startRequest(run, startAttempt, workspace))
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := adapter.Fingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	resumeAttempt, err := domain.NewAttempt(domain.AttemptParams{
		ID: domain.NewAttemptID(), RunID: run.ID(), Sequence: 2,
		Predecessor:       attemptIDPointer(startAttempt.ID()),
		ContextSnapshotID: domain.NewContextSnapshotID(), ContextDigest: sha256.Sum256([]byte("resume-snapshot")),
		AdapterID: AdapterID, CreatedAt: time.Unix(1_700_000_001, 0).UTC(),
	})
	if err != nil {
		t.Fatal(err)
	}
	security := sha256.Sum256([]byte("security"))
	resume, err := adapter.PrepareResume(context.Background(), execution.ResumeRequest{
		Run: run, Attempt: resumeAttempt,
		Binding:             execution.ResumeBinding{ExternalSession: "019f-explicit", RuntimeFingerprint: fingerprint, WorkingRoot: workspace, SecurityFingerprint: security},
		SecurityFingerprint: security, DeltaInstruction: []byte("continue"), LaunchToken: execution.LaunchToken{Value: "resume"},
	})
	if err != nil {
		t.Fatal(err)
	}

	assertInvocation := func(t *testing.T, invocation execution.Invocation, attempt domain.Attempt) []string {
		t.Helper()
		if invocation.Executable() != "/usr/bin/env" {
			t.Fatalf("executable = %q, want /usr/bin/env", invocation.Executable())
		}
		args := invocation.Arguments()
		prefix := expectedSanitizedCodexPrefix()
		if !slices.Equal(args[:len(prefix)], prefix) {
			t.Fatalf("sanitizer prefix = %#v, want %#v", args[:len(prefix)], prefix)
		}
		joined := strings.Join(args[len(prefix):], " ")
		for _, forbidden := range []string{" -s ", " --add-dir ", "proxy_url", "features.network_proxy.unix_sockets=", "danger-full-access"} {
			if strings.Contains(" "+joined+" ", forbidden) {
				t.Fatalf("managed argv contains forbidden %q: %s", forbidden, joined)
			}
		}
		attemptRoot := filepath.Join(config.RuntimeRoot, "runs", run.ID().String(), "attempts", attempt.ID().String())
		wantNetwork := expectedManagedNetworkArgs(filepath.Join(attemptRoot, "npm-cache"), filepath.Join(attemptRoot, "tmp"))
		startIndex := slices.Index(args, "--strict-config")
		if startIndex < 0 || !slices.Equal(args[startIndex:startIndex+len(wantNetwork)], wantNetwork) {
			t.Fatalf("managed network argv = %#v, want %#v", args, wantNetwork)
		}
		return wantNetwork[:len(wantNetwork)-2]
	}
	startStatic := assertInvocation(t, start, startAttempt)
	resumeStatic := assertInvocation(t, resume, resumeAttempt)
	if !slices.Equal(startStatic, resumeStatic) {
		t.Fatalf("fresh/resume static network contract differs:\nfresh=%#v\nresume=%#v", startStatic, resumeStatic)
	}
}

func attemptIDPointer(id domain.AttemptID) *domain.AttemptID { return &id }

func TestAttemptLocalCacheAndTmpAreFreshContained0700AndFailClosed(t *testing.T) {
	for _, name := range []string{"npm-cache", "tmp"} {
		t.Run(name+" created", func(t *testing.T) {
			adapter, config := testAdapter(t, nil)
			run, attempt := runAndAttempt(t)
			if _, err := adapter.PrepareStart(context.Background(), startRequest(run, attempt, filepath.Join(t.TempDir(), "workspace"))); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(config.RuntimeRoot, "runs", run.ID().String(), "attempts", attempt.ID().String(), name)
			info, err := os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || strictPermissionMode(info.Mode()) != 0o700 {
				t.Fatalf("%s mode/type = %v", name, info.Mode())
			}
		})
		for drift, arrange := range map[string]func(*testing.T, string){
			"symlink": func(t *testing.T, path string) {
				t.Helper()
				if err := os.Symlink(t.TempDir(), path); err != nil {
					t.Fatal(err)
				}
			},
			"file": func(t *testing.T, path string) {
				t.Helper()
				if err := os.WriteFile(path, []byte("drift"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			"mode": func(t *testing.T, path string) {
				t.Helper()
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatal(err)
				}
			},
		} {
			t.Run(name+" rejects "+drift, func(t *testing.T) {
				adapter, config := testAdapter(t, nil)
				run, attempt := runAndAttempt(t)
				attemptRoot := filepath.Join(config.RuntimeRoot, "runs", run.ID().String(), "attempts", attempt.ID().String())
				if err := os.MkdirAll(attemptRoot, 0o700); err != nil {
					t.Fatal(err)
				}
				arrange(t, filepath.Join(attemptRoot, name))
				if _, err := adapter.PrepareStart(context.Background(), startRequest(run, attempt, filepath.Join(t.TempDir(), "workspace"))); err == nil {
					t.Fatalf("PrepareStart accepted %s %s drift", name, drift)
				}
			})
		}
	}
}

func TestManagedEnvironmentOverridesCacheTmpAndDeletesEveryInheritedProxyKey(t *testing.T) {
	for _, key := range testParentProxyEnvironmentKeys {
		t.Setenv(key, "http://malicious.invalid:9999")
	}
	t.Setenv("NPM_CONFIG_CACHE", filepath.Join(t.TempDir(), "malicious-cache"))
	t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "malicious-tmp"))
	adapter, config := testAdapter(t, nil)
	run, attempt := runAndAttempt(t)
	invocation, err := adapter.PrepareStart(context.Background(), startRequest(run, attempt, filepath.Join(t.TempDir(), "workspace")))
	if err != nil {
		t.Fatal(err)
	}
	attemptRoot := filepath.Join(config.RuntimeRoot, "runs", run.ID().String(), "attempts", attempt.ID().String())
	environment := invocation.Environment()
	if environment["NPM_CONFIG_CACHE"] != filepath.Join(attemptRoot, "npm-cache") || environment["TMPDIR"] != filepath.Join(attemptRoot, "tmp") {
		t.Fatalf("cache/tmp were not overridden: %#v", environment)
	}
	args := invocation.Arguments()
	prefix := expectedSanitizedCodexPrefix()
	if !slices.Equal(args[:len(prefix)], prefix) {
		t.Fatalf("parent proxy deletion list = %#v, want %#v", args[:len(prefix)], prefix)
	}
	for _, key := range testParentProxyEnvironmentKeys {
		if _, exists := environment[key]; exists {
			t.Fatalf("proxy key %q leaked into explicit environment: %#v", key, environment)
		}
	}
}

func TestManagedPathValidationAndConstructionCapture(t *testing.T) {
	t.Run("empty captures PATH once", func(t *testing.T) {
		captured := "/captured/bin:/captured/sbin"
		t.Setenv("PATH", captured)
		config := testConfig(t, nil)
		config.ManagedPath = ""
		adapter, err := New(config)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv("PATH", "/changed/after/construction")
		invocation := prepareStart(t, adapter)
		if got := invocation.Environment()["PATH"]; got != captured {
			t.Fatalf("captured PATH = %q, want %q", got, captured)
		}
	})

	t.Run("non-empty preserves exact string", func(t *testing.T) {
		exact := "/managed//bin:/opt/Managed Path/bin"
		config := testConfig(t, nil)
		config.ManagedPath = exact
		adapter, err := New(config)
		if err != nil {
			t.Fatal(err)
		}
		if got := prepareStart(t, adapter).Environment()["PATH"]; got != exact {
			t.Fatalf("managed PATH = %q, want exact %q", got, exact)
		}
	})

	for name, value := range map[string]string{
		"nul":               "/managed/bin\x00:/bin",
		"carriage return":   "/managed/bin\r:/bin",
		"line feed":         "/managed/bin\n:/bin",
		"leading empty":     ":/bin",
		"trailing empty":    "/bin:",
		"doubled separator": "/bin::/usr/bin",
		"dot relative":      "/bin:.",
		"dotdot relative":   "/bin:..",
		"plain relative":    "/bin:relative/bin",
	} {
		t.Run(name, func(t *testing.T) {
			config := testConfig(t, nil)
			config.ManagedPath = value
			if _, err := New(config); err == nil {
				t.Fatalf("New() accepted invalid ManagedPath %q", value)
			}
		})
	}
}

func TestAttemptDirectoryAndBootstrapContractFreshAndResume(t *testing.T) {
	adapter, config := testAdapter(t, nil)
	run, attempt := runAndAttempt(t)
	workspace := filepath.Join(t.TempDir(), "workspace")
	start, err := adapter.PrepareStart(context.Background(), startRequest(run, attempt, workspace))
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(config.RuntimeRoot, "runs", run.ID().String(), "attempts", attempt.ID().String(), resultFileName)
	attemptRoot := filepath.Dir(resultPath)
	bootstrapPath := filepath.Join(attemptRoot, adapter.contract.BootstrapFileName)
	assertManagedAttemptFiles(t, attemptRoot, bootstrapPath, adapter.contract)

	fingerprint, err := adapter.Fingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	security := sha256.Sum256([]byte("security"))
	resume, err := adapter.PrepareResume(context.Background(), execution.ResumeRequest{
		Run: run, Attempt: attempt,
		Binding: execution.ResumeBinding{
			ExternalSession: "019f-explicit-thread", RuntimeFingerprint: fingerprint,
			WorkingRoot: workspace, SecurityFingerprint: security,
		},
		SecurityFingerprint: security,
		DeltaInstruction:    []byte("continue"),
		LaunchToken:         execution.LaunchToken{Value: "resume"},
	})
	if err != nil {
		t.Fatal(err)
	}
	assertManagedAttemptFiles(t, attemptRoot, bootstrapPath, adapter.contract)
	for _, invocation := range []execution.Invocation{start, resume} {
		environment := invocation.Environment()
		if environment["PATH"] != testManagedPath || environment["CHORA_MANAGED_PATH"] != testManagedPath || environment["BASH_ENV"] != bootstrapPath {
			t.Fatalf("managed environment = %#v", environment)
		}
	}
}

func TestAttemptDirectoryRejectsSymlinkAndModeDrift(t *testing.T) {
	for name, arrange := range map[string]func(*testing.T, string){
		"symlink": func(t *testing.T, attemptRoot string) {
			t.Helper()
			target := filepath.Join(t.TempDir(), "target")
			if err := os.Mkdir(target, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Dir(attemptRoot), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, attemptRoot); err != nil {
				t.Fatal(err)
			}
		},
		"mode drift": func(t *testing.T, attemptRoot string) {
			t.Helper()
			if err := os.MkdirAll(attemptRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(attemptRoot, 0o755); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			adapter, config := testAdapter(t, nil)
			run, attempt := runAndAttempt(t)
			attemptRoot := filepath.Join(config.RuntimeRoot, "runs", run.ID().String(), "attempts", attempt.ID().String())
			arrange(t, attemptRoot)
			if _, err := adapter.PrepareStart(context.Background(), startRequest(run, attempt, filepath.Join(t.TempDir(), "workspace"))); err == nil {
				t.Fatal("PrepareStart accepted invalid attempt directory")
			}
		})
	}
}

func TestManagedBashBootstrapRejectsDrift(t *testing.T) {
	contract := defaultManagedShellContract()
	for name, arrange := range map[string]func(*testing.T, string){
		"symlink": func(t *testing.T, path string) {
			t.Helper()
			target := filepath.Join(t.TempDir(), "target")
			if err := os.WriteFile(target, []byte(contract.BootstrapContent), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		},
		"mode drift": func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, []byte(contract.BootstrapContent), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"content drift": func(t *testing.T, path string) {
			t.Helper()
			if err := os.WriteFile(path, []byte("export PATH=/malicious\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			adapter, config := testAdapter(t, nil)
			run, attempt := runAndAttempt(t)
			attemptRoot := filepath.Join(config.RuntimeRoot, "runs", run.ID().String(), "attempts", attempt.ID().String())
			if err := os.MkdirAll(attemptRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(attemptRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			arrange(t, filepath.Join(attemptRoot, adapter.contract.BootstrapFileName))
			if _, err := adapter.PrepareStart(context.Background(), startRequest(run, attempt, filepath.Join(t.TempDir(), "workspace"))); err == nil {
				t.Fatal("PrepareStart accepted invalid managed Bash bootstrap")
			}
		})
	}
}

func TestManagedEnvironmentOverridesInheritedBashEnvironment(t *testing.T) {
	t.Setenv("BASH_ENV", filepath.Join(t.TempDir(), "malicious-bash-env"))
	adapter, _ := testAdapter(t, nil)
	invocation := prepareStart(t, adapter)
	environment := invocation.Environment()
	if environment["BASH_ENV"] == os.Getenv("BASH_ENV") || filepath.Base(environment["BASH_ENV"]) != adapter.contract.BootstrapFileName {
		t.Fatalf("BASH_ENV was not explicitly overridden: %#v", environment)
	}
}

func TestManagedBashEnvironmentRestoresExactPathAfterLoginProfile(t *testing.T) {
	fakeBin := filepath.Join(t.TempDir(), "managed-bin")
	if err := os.Mkdir(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	fakeTool := filepath.Join(fakeBin, "chora-managed-tool")
	if err := os.WriteFile(fakeTool, []byte("#!/bin/sh\nprintf 'managed-tool-ok\\n'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	managedPath := fakeBin + ":/usr/bin:/bin"
	config := testConfig(t, nil)
	config.ManagedPath = managedPath
	adapter, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	invocation := prepareStart(t, adapter)
	environment := append([]string(nil), os.Environ()...)
	environment = append(environment, "BASH_ENV=/malicious/inherited", "PATH=/malicious/inherited")
	for key, value := range invocation.Environment() {
		environment = append(environment, key+"="+value)
	}
	result, err := processbound.Run(context.Background(), processbound.Spec{
		Name: "/bin/bash", Args: []string{"-lc", `printf '%s\n' "$PATH"; command -v chora-managed-tool; chora-managed-tool`}, Env: environment,
	})
	if err != nil {
		t.Fatalf("bash failed: %v stderr=%q", err, result.Stderr)
	}
	want := managedPath + "\n" + fakeTool + "\nmanaged-tool-ok\n"
	if string(result.Stdout) != want {
		t.Fatalf("bash stdout = %q, want %q (stderr=%q)", result.Stdout, want, result.Stderr)
	}
}

func TestFingerprintBindsManagedPathButNotAttemptPath(t *testing.T) {
	first, _ := testAdapter(t, nil)
	second, _ := testAdapter(t, nil)
	firstFingerprint, err := first.Fingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	secondFingerprint, err := second.Fingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if firstFingerprint != secondFingerprint {
		t.Fatalf("fingerprint changed with runtime/attempt root: %+v / %+v", firstFingerprint, secondFingerprint)
	}

	config := testConfig(t, nil)
	config.ManagedPath = "/different/managed/bin:/usr/bin:/bin"
	different, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	differentFingerprint, err := different.Fingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if firstFingerprint == differentFingerprint {
		t.Fatal("fingerprint did not bind exact ManagedPath")
	}
}

func TestFingerprintBindsEveryManagedShellContractField(t *testing.T) {
	mutations := map[string]func(*managedShellContract){
		"schema":                 func(contract *managedShellContract) { contract.Schema += ".changed" },
		"bootstrap filename":     func(contract *managedShellContract) { contract.BootstrapFileName += ".changed" },
		"attempt directory mode": func(contract *managedShellContract) { contract.AttemptDirectoryMode = 0o750 },
		"bootstrap file mode":    func(contract *managedShellContract) { contract.BootstrapFileMode = 0o640 },
		"PATH key":               func(contract *managedShellContract) { contract.EnvironmentKeys.Path += "_CHANGED" },
		"CHORA_MANAGED_PATH key": func(contract *managedShellContract) { contract.EnvironmentKeys.ManagedPath += "_CHANGED" },
		"BASH_ENV key":           func(contract *managedShellContract) { contract.EnvironmentKeys.BashEnv += "_CHANGED" },
		"HOME key":               func(contract *managedShellContract) { contract.EnvironmentKeys.Home += "_CHANGED" },
		"CODEX_HOME key":         func(contract *managedShellContract) { contract.EnvironmentKeys.CodexHome += "_CHANGED" },
		"bootstrap content":      func(contract *managedShellContract) { contract.BootstrapContent += "# changed\n" },
		"network static argv": func(contract *managedShellContract) {
			contract.ManagedNetworkArguments = append([]string(nil), contract.ManagedNetworkArguments...)
			contract.ManagedNetworkArguments[2] = `sandbox_mode="danger-full-access"`
		},
		"parent environment deletion": func(contract *managedShellContract) {
			contract.ParentEnvironmentUnset = append([]string(nil), contract.ParentEnvironmentUnset...)
			contract.ParentEnvironmentUnset[0] = "CHANGED_PROXY"
		},
		"environment executable":       func(contract *managedShellContract) { contract.EnvironmentExecutable += ".changed" },
		"environment sha":              func(contract *managedShellContract) { contract.EnvironmentSHA256 = strings.Repeat("0", 64) },
		"environment unset option":     func(contract *managedShellContract) { contract.EnvironmentUnsetOption = "--unset" },
		"environment exec replacement": func(contract *managedShellContract) { contract.EnvironmentExecReplace = "changed" },
		"npm cache directory":          func(contract *managedShellContract) { contract.NPMCacheDirectoryName += "-changed" },
		"tmp directory":                func(contract *managedShellContract) { contract.TmpDirectoryName += "-changed" },
		"writable directory mode":      func(contract *managedShellContract) { contract.WritableDirectoryMode = 0o750 },
		"npm cache environment key":    func(contract *managedShellContract) { contract.NPMCacheEnvironmentKey += "_CHANGED" },
		"tmp environment key":          func(contract *managedShellContract) { contract.TmpEnvironmentKey += "_CHANGED" },
		"writable roots config key":    func(contract *managedShellContract) { contract.WritableRoots.ConfigKey += ".changed" },
		"writable roots order": func(contract *managedShellContract) {
			contract.WritableRoots.NPMCacheDirectory, contract.WritableRoots.TmpDirectory = contract.WritableRoots.TmpDirectory, contract.WritableRoots.NPMCacheDirectory
		},
		"writable roots placeholder": func(contract *managedShellContract) { contract.WritableRoots.PlaceholderShape += ".changed" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			adapter, _ := testAdapter(t, nil)
			baseline, err := adapter.Fingerprint(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			mutate(&adapter.contract)
			changed, err := adapter.Fingerprint(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if changed == baseline {
				t.Fatalf("fingerprint did not bind managed-shell contract field %q", name)
			}
		})
	}
}

func TestManagedShellRuntimeUsesAdapterContract(t *testing.T) {
	adapter, config := testAdapter(t, nil)
	adapter.contract.BootstrapFileName = ".custom-managed-bash-env"
	adapter.contract.AttemptDirectoryMode = 0o750
	adapter.contract.BootstrapFileMode = 0o640
	adapter.contract.EnvironmentKeys = managedEnvironmentKeys{
		Path: "CUSTOM_PATH", ManagedPath: "CUSTOM_MANAGED_PATH", BashEnv: "CUSTOM_BASH_ENV",
		Home: "CUSTOM_HOME", CodexHome: "CUSTOM_CODEX_HOME",
	}
	adapter.contract.BootstrapContent = "export CUSTOM_PATH=\"$CUSTOM_MANAGED_PATH\"\n"
	if err := os.Chmod(config.RuntimeRoot, adapter.contract.AttemptDirectoryMode); err != nil {
		t.Fatal(err)
	}

	run, attempt := runAndAttempt(t)
	invocation, err := adapter.PrepareStart(context.Background(), startRequest(run, attempt, filepath.Join(t.TempDir(), "workspace")))
	if err != nil {
		t.Fatal(err)
	}
	attemptRoot := filepath.Join(config.RuntimeRoot, "runs", run.ID().String(), "attempts", attempt.ID().String())
	bootstrapPath := filepath.Join(attemptRoot, adapter.contract.BootstrapFileName)
	assertManagedAttemptFiles(t, attemptRoot, bootstrapPath, adapter.contract)
	environment := invocation.Environment()
	if environment[adapter.contract.EnvironmentKeys.Path] != testManagedPath ||
		environment[adapter.contract.EnvironmentKeys.ManagedPath] != testManagedPath ||
		environment[adapter.contract.EnvironmentKeys.BashEnv] != bootstrapPath ||
		environment[adapter.contract.EnvironmentKeys.Home] == "" ||
		environment[adapter.contract.EnvironmentKeys.CodexHome] == "" {
		t.Fatalf("custom managed environment = %#v", environment)
	}
	for _, defaultKey := range []string{"PATH", "CHORA_MANAGED_PATH", "BASH_ENV", "HOME", "CODEX_HOME"} {
		if _, exists := environment[defaultKey]; exists {
			t.Fatalf("runtime ignored custom contract and emitted default key %q: %#v", defaultKey, environment)
		}
	}
}

func TestPrepareResumeUsesExplicitThreadDeltaAndNeverLast(t *testing.T) {
	adapter, config := testAdapter(t, nil)
	fingerprint, err := adapter.Fingerprint(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	run, attempt := runAndAttempt(t)
	security := sha256.Sum256([]byte("security"))
	request := execution.ResumeRequest{
		Run: run, Attempt: attempt,
		Binding: execution.ResumeBinding{
			ExternalSession: "019f-explicit-thread", RuntimeFingerprint: fingerprint,
			WorkingRoot: "/prepared/workspace", SecurityFingerprint: security,
		},
		SecurityFingerprint: security,
		DeltaInstruction:    []byte("revise only the failing criterion"),
		LaunchToken:         execution.LaunchToken{Value: "launch-resume"},
	}
	invocation, err := adapter.PrepareResume(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	resultPath := filepath.Join(config.RuntimeRoot, "runs", run.ID().String(), "attempts", attempt.ID().String(), resultFileName)
	attemptRoot := filepath.Dir(resultPath)
	wantArgs := append(expectedSanitizedCodexPrefix(), "exec", "resume", "--ignore-user-config", "--ignore-rules", "--json", "--output-schema", config.SchemaPath, "-o", resultPath, "--skip-git-repo-check")
	wantArgs = append(wantArgs, expectedManagedNetworkArgs(filepath.Join(attemptRoot, "npm-cache"), filepath.Join(attemptRoot, "tmp"))...)
	wantArgs = append(wantArgs, request.Binding.ExternalSession, "-")
	if !slices.Equal(invocation.Arguments(), wantArgs) || !reflect.DeepEqual(invocation.Stdin(), request.DeltaInstruction) {
		t.Fatalf("resume invocation = %#v stdin=%q", invocation.Arguments(), invocation.Stdin())
	}
	joined := strings.Join(invocation.Arguments(), " ")
	if strings.Contains(joined, "--last") || strings.Contains(joined, " -C ") || strings.Contains(joined, " -s ") {
		t.Fatalf("resume crossed frozen envelope: %q", joined)
	}
	request.Binding.ExternalSession = ""
	if _, err := adapter.PrepareResume(context.Background(), request); err == nil {
		t.Fatal("PrepareResume accepted an empty explicit thread ID")
	}
}

func TestNormalizeConfigDefaultsToCodex0146Evidence(t *testing.T) {
	root := t.TempDir()
	config, err := normalizeConfig(Config{RepoRoot: root, RuntimeRoot: filepath.Join(root, "runtime")})
	if err != nil {
		t.Fatal(err)
	}
	if config.GatePath != filepath.Join(root, "docs", "validation", "codex-cli-0.146.0-gate.json") ||
		config.ContractPath != filepath.Join(root, "docs", "validation", "codex-cli-0.146.0-contract.md") {
		t.Fatalf("default evidence paths = %q / %q", config.GatePath, config.ContractPath)
	}
}

func TestDecodeEventConsumesCompleteJSONLLinesAndPreservesUnknownEvents(t *testing.T) {
	adapter, _ := testAdapter(t, nil)
	data := []byte("{\"type\":\"thread.started\",\"thread_id\":\"thread-1\"}\n{\"type\":\"future.event\",\"value\":1}\n")
	split := len(data) - 5
	first, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Data: data[:split]})
	if err != nil {
		t.Fatal(err)
	}
	if first.ConsumedBytes == 0 || len(first.Events) != 1 || first.Events[0].ExternalSession != "thread-1" {
		t.Fatalf("first decode = %+v", first)
	}
	replayed := append([]byte(nil), data[first.ConsumedBytes:]...)
	second, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStdout, Offset: int64(first.ConsumedBytes), Data: replayed, EOF: true})
	if err != nil {
		t.Fatal(err)
	}
	if second.ConsumedBytes != len(replayed) || len(second.Events) != 1 || second.Events[0].Type != "future.event" || string(second.Events[0].RawJSON) != strings.TrimSuffix(string(replayed), "\n") {
		t.Fatalf("second decode = %+v", second)
	}
}

func TestDecodeEventRedactsMalformedLines(t *testing.T) {
	adapter, _ := testAdapter(t, nil)
	secret := "malformed bearer super-secret"
	decoded, err := adapter.DecodeEvent(execution.EventChunk{Stream: execution.StreamStderr, Offset: 19, Data: []byte(secret + "\n"), EOF: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Events) != 1 || decoded.Events[0].Type != "adapter.parse_error" {
		t.Fatalf("decoded = %+v", decoded)
	}
	encoded, _ := json.Marshal(decoded.Events[0])
	if strings.Contains(string(encoded), "super-secret") || !json.Valid(decoded.Events[0].RawJSON) {
		t.Fatalf("parse error leaked malformed input: %s", encoded)
	}
}

func TestDecodeTerminalReadsResultPathAndStrictlyDecodesResult(t *testing.T) {
	adapter, _ := testAdapter(t, nil)
	valid := []byte(`{"schema_version":"chora.agent-result.v1","summary":"done","review_ready":true,"outputs":[],"artifact_candidates":[],"checks":[],"unknowns":[],"handoff":{"requested":false,"reason":""}}`)
	resultPath := filepath.Join(t.TempDir(), "result.json")
	if err := os.WriteFile(resultPath, valid, 0o600); err != nil {
		t.Fatal(err)
	}
	result, err := adapter.DecodeTerminal(execution.TerminalFiles{Paths: map[string]string{"result": resultPath}, ExitCode: 0})
	if err != nil || result.Kind != execution.TerminalReviewReady {
		t.Fatalf("DecodeTerminal() = %+v, %v", result, err)
	}
	if err := os.WriteFile(resultPath, append(valid[:len(valid)-1], []byte(`,"unexpected":true}`)...), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.DecodeTerminal(execution.TerminalFiles{Paths: map[string]string{"result": resultPath}}); !errors.Is(err, execution.ErrResultContractInvalid) {
		t.Fatalf("DecodeTerminal() error = %v", err)
	}
	if _, err := adapter.DecodeTerminal(execution.TerminalFiles{}); err == nil {
		t.Fatal("DecodeTerminal accepted missing result path")
	}
}

func testAdapter(t *testing.T, mutate func(*gateManifest)) (*Adapter, Config) {
	t.Helper()
	config := testConfig(t, mutate)
	adapter, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	return adapter, config
}

func testConfig(t *testing.T, mutate func(*gateManifest)) Config {
	t.Helper()
	root := t.TempDir()
	runtimeRoot := filepath.Join(root, "runtime")
	if err := os.Mkdir(runtimeRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	contractPath := filepath.Join(root, "contract.md")
	gatePath := filepath.Join(root, "gate.json")
	schemaPath := filepath.Join(root, "schema.json")
	contract := []byte("frozen contract")
	codexBinary := []byte("fixed Codex binary")
	sandboxBinary := []byte("fixed sandbox-exec")
	environmentBinary, err := os.ReadFile("/usr/bin/env")
	if err != nil {
		t.Fatal(err)
	}
	manifest := gateManifest{
		SchemaVersion: "chora.runtime-gate.v2", CodexVersion: "0.146.0", RealAdapterGate: "PASS",
		HostReadIsolation: "unavailable", WorkspaceWriteIsolation: "proven", ControlPlaneIsolation: "proven", LoopbackIsolation: "proven",
		ProfileTemplateSHA256: runtimeboundary.ProfileTemplateSHA256(), ProfileRendererVersion: runtimeboundary.RendererVersion,
		ProbeBinarySHA256: strings.Repeat("a", 64), CodexBinarySHA256: digestHex(codexBinary), SandboxExecSHA256: digestHex(sandboxBinary),
		OSBuild: "25E246", ContractSHA256: digestHex(contract),
		ManagedNetworkProxy: "proven", RegistryEgress: "proven", NonAllowlistedEgressIsolation: "proven",
		ManagedLoopbackIsolation: "proven", ManagedLocalBindingIsolation: "proven", UpstreamProxy: "disabled",
		SOCKS5: "disabled", SOCKS5UDP: "disabled", NonLoopbackProxy: "disabled", AllUnixSockets: "disabled",
		UnixSocketAllowlist: "empty", EnvironmentWrapper: "proven", EnvironmentWrapperSHA256: digestHex(environmentBinary),
		CodexExecutable:                    InnerCodexExecutable,
		AllowedDestinationPayloadIsolation: "unavailable",
	}
	if mutate != nil {
		mutate(&manifest)
	}
	gateFields := map[string]any{}
	manifestData, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(manifestData, &gateFields); err != nil {
		t.Fatal(err)
	}
	gateData, err := json.Marshal(gateFields)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		gatePath: gateData, contractPath: contract, InnerCodexExecutable: codexBinary, SandboxExecutable: sandboxBinary, "/usr/bin/env": environmentBinary,
	}
	config := Config{
		RuntimeRoot: runtimeRoot, SchemaPath: schemaPath, GatePath: gatePath, ContractPath: contractPath, ManagedPath: testManagedPath,
		expectedCodexSHA256:       digestHex(codexBinary),
		expectedEnvironmentSHA256: digestHex(environmentBinary),
		ReadFile: func(path string) ([]byte, error) {
			if data, ok := files[path]; ok {
				return append([]byte(nil), data...), nil
			}
			return os.ReadFile(path)
		},
		RunCommand: func(_ context.Context, name string, args ...string) ([]byte, error) {
			if name != InnerCodexExecutable || !slices.Equal(args, []string{"--version"}) {
				return nil, errors.New("unexpected command")
			}
			return []byte("codex-cli 0.146.0\n"), nil
		},
		OSBuild: func(context.Context) (string, error) { return "25E246", nil },
	}
	return config
}

func prepareStart(t *testing.T, adapter *Adapter) execution.Invocation {
	t.Helper()
	run, attempt := runAndAttempt(t)
	invocation, err := adapter.PrepareStart(context.Background(), startRequest(run, attempt, filepath.Join(t.TempDir(), "workspace")))
	if err != nil {
		t.Fatal(err)
	}
	return invocation
}

func startRequest(run domain.AgentRun, attempt domain.Attempt, workspace string) execution.StartRequest {
	return execution.StartRequest{
		Run: run, Attempt: attempt, WorkspaceRoot: workspace,
		SnapshotDocument: []byte(`{"snapshot":"frozen"}`),
		LaunchToken:      execution.LaunchToken{Value: "launch-start"},
	}
}

func assertManagedAttemptFiles(t *testing.T, attemptRoot, bootstrapPath string, contract managedShellContract) {
	t.Helper()
	directoryInfo, err := os.Lstat(attemptRoot)
	if err != nil {
		t.Fatal(err)
	}
	if directoryInfo.Mode()&os.ModeSymlink != 0 || !directoryInfo.IsDir() || strictPermissionMode(directoryInfo.Mode()) != contract.AttemptDirectoryMode {
		t.Fatalf("attempt directory mode = %v", directoryInfo.Mode())
	}
	bootstrapInfo, err := os.Lstat(bootstrapPath)
	if err != nil {
		t.Fatal(err)
	}
	if bootstrapInfo.Mode()&os.ModeSymlink != 0 || !bootstrapInfo.Mode().IsRegular() || strictPermissionMode(bootstrapInfo.Mode()) != contract.BootstrapFileMode {
		t.Fatalf("bootstrap mode = %v", bootstrapInfo.Mode())
	}
	content, err := os.ReadFile(bootstrapPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != contract.BootstrapContent {
		t.Fatalf("bootstrap content = %q", content)
	}
}

func runAndAttempt(t *testing.T) (domain.AgentRun, domain.Attempt) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0).UTC()
	run, err := domain.NewAgentRun(domain.NewRunID(), domain.NewTaskID(), domain.NewCharterID(), now)
	if err != nil {
		t.Fatal(err)
	}
	attempt, err := domain.NewAttempt(domain.AttemptParams{
		ID: domain.NewAttemptID(), RunID: run.ID(), Sequence: 1,
		ContextSnapshotID: domain.NewContextSnapshotID(), ContextDigest: sha256.Sum256([]byte("snapshot")),
		AdapterID: AdapterID, CreatedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return run, attempt
}

func digestHex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
