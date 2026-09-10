package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/dockersupervisor"
	"github.com/Yangyang96/chora/internal/productinstall"
)

func TestExactDockerRunnerDoesNotDependOnPATH(t *testing.T) {
	root := canonicalTempRoot(t)
	executable := filepath.Join(root, "private-docker")
	if err := os.WriteFile(executable, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	configRoot := filepath.Join(root, "docker-config")
	writeExactDockerContextFixture(t, configRoot, "chora-local", "unix:///private/run/docker.sock")
	t.Setenv("PATH", "")
	t.Setenv("DOCKER_CONFIG", configRoot)
	endpointDigest, err := dockersupervisor.DigestContextEndpoint("unix:///private/run/docker.sock")
	if err != nil {
		t.Fatal(err)
	}
	target := productinstall.EngineTarget{CLIPath: executable, ContextName: "chora-local", EndpointDigest: endpointDigest}
	runner, selected, err := exactDockerRunner(target)
	if err != nil || runner == nil || selected != executable {
		t.Fatalf("exactDockerRunner() = %T %q %v", runner, selected, err)
	}
}

func TestExactDockerRunnerRejectsEndpointTOCTOUBeforeCommand(t *testing.T) {
	root := canonicalTempRoot(t)
	marker := filepath.Join(root, "command-ran")
	executable := filepath.Join(root, "private-docker")
	script := fmt.Sprintf("#!/bin/sh\nprintf 'started\\n' > %q\n", marker)
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	configRoot := filepath.Join(root, "docker-config")
	writeExactDockerContextFixture(t, configRoot, "chora-local", "unix:///private/run/first.sock")
	t.Setenv("DOCKER_CONFIG", configRoot)
	digest, _ := dockersupervisor.DigestContextEndpoint("unix:///private/run/first.sock")
	runner, err := newExactDockerCLIRunner(productinstall.EngineTarget{CLIPath: executable, ContextName: "chora-local", EndpointDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	writeExactDockerContextFixture(t, configRoot, "chora-local", "unix:///private/run/second.sock")
	if _, err := runner.Run(context.Background(), dockersupervisor.Command{Args: []string{"image", "rm", "sha256:" + strings.Repeat("a", 64)}}); err == nil {
		t.Fatal("endpoint drift reached Docker command")
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("endpoint drift executed command: %v", err)
	}
}

func TestExactDockerRunnerRejectsFrozenContextAuthorityDriftBeforeCommand(t *testing.T) {
	mutations := map[string]func(*testing.T, string, string){
		"client configuration": func(t *testing.T, configRoot, _ string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte("{\"credsStore\":\"changed\"}\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"TLS material": func(t *testing.T, configRoot, contextName string) {
			t.Helper()
			contextID := sha256.Sum256([]byte(contextName))
			tlsRoot := filepath.Join(configRoot, "contexts", "tls", fmt.Sprintf("%x", contextID[:]), "docker")
			if err := os.MkdirAll(tlsRoot, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(tlsRoot, "ca.pem"), []byte("changed\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			root := canonicalTempRoot(t)
			marker := filepath.Join(root, "command-ran")
			executable := filepath.Join(root, "private-docker")
			script := fmt.Sprintf("#!/bin/sh\nprintf 'started\\n' > %q\n", marker)
			if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
				t.Fatal(err)
			}
			contextName := "chora-local"
			endpoint := "unix:///private/run/docker.sock"
			configRoot := filepath.Join(root, "docker-config")
			writeExactDockerContextFixture(t, configRoot, contextName, endpoint)
			t.Setenv("DOCKER_CONFIG", configRoot)
			digest, _ := dockersupervisor.DigestContextEndpoint(endpoint)
			runner, err := newExactDockerCLIRunner(productinstall.EngineTarget{CLIPath: executable, ContextName: contextName, EndpointDigest: digest})
			if err != nil {
				t.Fatal(err)
			}
			mutate(t, configRoot, contextName)
			if _, err := runner.Run(context.Background(), dockersupervisor.Command{Args: []string{"version"}}); err == nil {
				t.Fatalf("%s drift reached Docker command", name)
			}
			if _, err := os.Lstat(marker); !os.IsNotExist(err) {
				t.Fatalf("%s drift executed command: %v", name, err)
			}
		})
	}
}

func TestExactDockerRunnerProcessStartsAreOneToOneWithLedger(t *testing.T) {
	root := canonicalTempRoot(t)
	contextName := "chora-local"
	endpoint := "unix:///private/run/docker.sock"
	processLog := filepath.Join(root, "docker-processes.log")
	executable := filepath.Join(root, "private-docker")
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case " $* " in
  *" version "*) printf '{"api_version":"1.47","server_version":"29.6.1","operating_system":"linux","architecture":"arm64"}\n' ;;
  *" info "*) printf '{"daemon_id":"daemon-1","provider_name":"Colima"}\n' ;;
  *" context show "*) printf '%s\n' ;;
  *" context inspect "*) printf '"%s"\n' ;;
  *" events "*) exit 0 ;;
  *) exit 64 ;;
esac
`, processLog, contextName, endpoint)
	if err := os.WriteFile(executable, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	configRoot := filepath.Join(root, "docker-config")
	writeExactDockerContextFixture(t, configRoot, contextName, endpoint)
	t.Setenv("DOCKER_CONFIG", configRoot)
	digest, err := dockersupervisor.DigestContextEndpoint(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	runner, err := newExactDockerCLIRunner(productinstall.EngineTarget{CLIPath: executable, ContextName: contextName, EndpointDigest: digest})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(processLog); !os.IsNotExist(err) {
		t.Fatalf("Runner construction started hidden Docker process: %v", err)
	}
	ledger, err := dockersupervisor.OpenOperationLedger(runner, filepath.Join(root, "state"), "g1")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := ledger.Close(); err != nil {
			t.Error(err)
		}
	}()
	scoped := dockersupervisor.RunnerForOperationPhase(ledger, dockersupervisor.OperationPhaseSetup)
	identity, err := dockersupervisor.ObserveEngine(context.Background(), scoped)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ContextName() != contextName || identity.ContextEndpointDigest() != digest {
		t.Fatalf("observed identity = %+v", identity.Record())
	}
	process, err := scoped.Start(context.Background(), dockersupervisor.Command{Args: []string{"events"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := process.Close(); err != nil {
		t.Fatal(err)
	}
	if exitCode, err := process.Wait(); err != nil || exitCode != 0 {
		t.Fatalf("Wait() = %d, %v", exitCode, err)
	}
	snapshot, err := ledger.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(processLog)
	if err != nil {
		t.Fatal(err)
	}
	processes := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(processes) != snapshot.AuditRecordCount || len(processes) != 5 || len(snapshot.CommandAudit) != 5 {
		t.Fatalf("Docker processes=%q ledger=%+v", processes, snapshot)
	}
	contextInspects := 0
	for _, process := range processes {
		if strings.Contains(" "+process+" ", " context inspect ") {
			contextInspects++
		}
	}
	if contextInspects != 1 {
		t.Fatalf("context inspect processes = %d, want only the ledgered ObserveEngine command: %q", contextInspects, processes)
	}
	for index, record := range snapshot.CommandAudit {
		wantInvocation := "run"
		if index == len(snapshot.CommandAudit)-1 {
			wantInvocation = "start"
		}
		if record.Sequence != index+1 || record.Invocation != wantInvocation || record.Result != "succeeded" {
			t.Fatalf("ledger record %d = %+v", index, record)
		}
	}
}

func writeExactDockerContextFixture(t *testing.T, configRoot, contextName, endpoint string) {
	t.Helper()
	contextID := sha256.Sum256([]byte(contextName))
	metadataRoot := filepath.Join(configRoot, "contexts", "meta", fmt.Sprintf("%x", contextID[:]))
	if err := os.MkdirAll(metadataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configRoot, "config.json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	metadata := fmt.Sprintf("{\"Name\":%q,\"Endpoints\":{\"docker\":{\"Host\":%q}}}\n", contextName, endpoint)
	if err := os.WriteFile(filepath.Join(metadataRoot, "meta.json"), []byte(metadata), 0o600); err != nil {
		t.Fatal(err)
	}
}
