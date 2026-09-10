package dockersupervisor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDockerRunnerPinsExplicitProviderNeutralAuthoritiesAcrossRunAndStart(t *testing.T) {
	tests := []struct{ contextName, endpoint string }{
		{contextName: "desktop-linux", endpoint: "unix:///Users/alice/.docker/run/docker.sock"},
		{contextName: "orbstack", endpoint: "unix:///Users/alice/.orbstack/run/docker.sock"},
	}
	for _, test := range tests {
		t.Run(test.contextName, func(t *testing.T) {
			first := t.TempDir()
			second := t.TempDir()
			writeDockerAuthorityFixture(t, filepath.Join(first, "docker"), "first")
			writeDockerAuthorityFixture(t, filepath.Join(second, "docker"), "second")
			configRoot := filepath.Join(t.TempDir(), "pinned-config")
			fixture := writeDockerConfigAuthorityFixture(t, configRoot, test.contextName, test.endpoint)
			t.Setenv("PATH", first+string(os.PathListSeparator)+second)
			t.Setenv("DOCKER_CONFIG", configRoot)
			t.Setenv("DOCKER_HOST", "tcp://ambient-before.invalid:2375")
			t.Setenv("DOCKER_CONTEXT", "ambient-before")
			runner, err := NewDockerCommandRunner(fixture.authority)
			if err != nil {
				t.Fatal(err)
			}
			resolvedConfigRoot, err := filepath.EvalSymlinks(configRoot)
			if err != nil {
				t.Fatal(err)
			}

			t.Setenv("PATH", second+string(os.PathListSeparator)+first)
			t.Setenv("DOCKER_CONFIG", filepath.Join(t.TempDir(), "drifted-config"))
			t.Setenv("DOCKER_HOST", "tcp://ambient-after.invalid:2375")
			t.Setenv("DOCKER_CONTEXT", "ambient-after")
			result, err := runner.Run(context.Background(), Command{Args: []string{"version"}})
			if err != nil || result.ExitCode != 0 {
				t.Fatalf("Run() = %#v, %v", result, err)
			}
			assertPinnedDockerAuthority(t, string(result.Stdout), "first", resolvedConfigRoot, test.contextName, "")

			var stdout bytes.Buffer
			process, err := runner.Start(context.Background(), Command{Args: []string{"run", "--pull=never"}, Stdout: &stdout})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := process.Write([]byte("started\n")); err != nil {
				t.Fatal(err)
			}
			if err := process.Close(); err != nil {
				t.Fatal(err)
			}
			if exitCode, err := process.Wait(); err != nil || exitCode != 0 {
				t.Fatalf("Wait() = %d, %v", exitCode, err)
			}
			assertPinnedDockerAuthority(t, stdout.String(), "first", resolvedConfigRoot, test.contextName, "started")
		})
	}
}

func TestObserveEngineUsesTheSameFrozenExplicitAuthority(t *testing.T) {
	root := t.TempDir()
	contextName := "desktop-linux"
	endpoint := "unix:///Users/alice/.docker/run/docker.sock"
	logPath := filepath.Join(root, "docker-argv.log")
	executable := filepath.Join(root, "docker")
	writeDockerObservationFixture(t, executable, logPath, contextName, endpoint)
	fixture := writeDockerConfigAuthorityFixture(t, filepath.Join(root, "config"), contextName, endpoint)
	runner, err := newDockerRunner(executable, fixture.root, fixture.authority)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := ObserveEngine(context.Background(), runner)
	if err != nil {
		t.Fatal(err)
	}
	if identity.ContextName() != contextName || identity.ContextEndpointDigest() != fixture.authority.ContextEndpointDigest {
		t.Fatalf("ObserveEngine() = %#v", identity.Record())
	}
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 4 {
		t.Fatalf("observation commands = %q", lines)
	}
	for _, line := range lines {
		if !strings.HasPrefix(line, "--context "+contextName+" ") || strings.Contains(line, " colima ") {
			t.Fatalf("observation escaped frozen authority: %q", line)
		}
	}
}

func TestDockerRunnerRejectsPinnedExecutableContentDriftBeforeUse(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "docker")
	writeDockerAuthorityFixture(t, executable, "original")
	configRoot := filepath.Join(root, "config")
	fixture := writeDockerConfigAuthorityFixture(t, configRoot, "desktop-linux", "unix:///tmp/desktop.sock")
	runner, err := newDockerRunner(executable, configRoot, fixture.authority)
	if err != nil {
		t.Fatal(err)
	}
	writeDockerAuthorityFixture(t, executable, "replacement")
	result, err := runner.Run(context.Background(), Command{Args: []string{"version"}})
	if err == nil || !strings.Contains(err.Error(), "executable identity drift") || result.ExitCode != -1 {
		t.Fatalf("Run() = %#v, %v", result, err)
	}
}

func TestDockerRunnerRejectsExplicitContextEndpointMismatch(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "docker")
	writeDockerAuthorityFixture(t, executable, "unused")
	fixture := writeDockerConfigAuthorityFixture(t, filepath.Join(root, "config"), "desktop-linux", "unix:///tmp/desktop.sock")
	mismatched := fixture.authority
	mismatched.ContextEndpointDigest = endpointDigest(t, "unix:///tmp/orbstack.sock")
	if _, err := newDockerRunner(executable, fixture.root, mismatched); err == nil || !strings.Contains(err.Error(), "does not match the explicit command authority") {
		t.Fatalf("newDockerRunner() error = %v", err)
	}
}

func TestDockerRunnerHasNoCurrentDefaultOrColimaFallback(t *testing.T) {
	root := t.TempDir()
	executable := filepath.Join(root, "docker")
	writeDockerAuthorityFixture(t, executable, "must-not-run")
	configRoot := filepath.Join(root, "config")
	writeDockerConfigAuthorityFixture(t, configRoot, "colima", "unix:///tmp/colima.sock")
	writeAuthorityFile(t, filepath.Join(configRoot, "config.json"), []byte(`{"currentContext":"colima"}`+"\n"))
	explicit := DockerCommandAuthority{
		ContextName: "desktop-linux", ContextEndpointDigest: endpointDigest(t, "unix:///tmp/desktop.sock"),
	}
	if _, err := newDockerRunner(executable, configRoot, explicit); err == nil {
		t.Fatalf("missing explicit context fell back to current/default provider: %v", err)
	}
	if _, err := NewDockerCommandRunner(DockerCommandAuthority{}); err == nil {
		t.Fatal("empty authority implicitly selected a Docker context")
	}
}

func TestDockerRunnerRejectsContextEndpointAndTLSAuthorityDriftBeforeEveryUse(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*testing.T, dockerAuthorityFixture)
		start  bool
	}{
		{name: "context endpoint before Run", mutate: func(t *testing.T, fixture dockerAuthorityFixture) {
			writeContextMetadata(t, fixture.meta, fixture.contextName, "unix:///tmp/drifted.sock")
		}},
		{name: "TLS CA before Run", mutate: func(t *testing.T, fixture dockerAuthorityFixture) {
			writeAuthorityFile(t, fixture.ca, []byte("drifted ca\n"))
		}},
		{name: "TLS key before Start", start: true, mutate: func(t *testing.T, fixture dockerAuthorityFixture) {
			writeAuthorityFile(t, fixture.key, []byte("drifted key\n"))
		}},
		{name: "context metadata symlink before Start", start: true, mutate: func(t *testing.T, fixture dockerAuthorityFixture) {
			if err := os.Remove(fixture.meta); err != nil {
				t.Fatal(err)
			}
			target := filepath.Join(t.TempDir(), "foreign-meta.json")
			writeContextMetadata(t, target, fixture.contextName, "unix:///tmp/foreign.sock")
			if err := os.Symlink(target, fixture.meta); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "context store ancestor symlink before Run", mutate: func(t *testing.T, fixture dockerAuthorityFixture) {
			metaRoot := filepath.Join(fixture.root, "contexts", "meta")
			foreign := filepath.Join(t.TempDir(), "meta")
			if err := copyAuthorityTree(metaRoot, foreign); err != nil {
				t.Fatal(err)
			}
			if err := os.RemoveAll(metaRoot); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(foreign, metaRoot); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			executable := filepath.Join(root, "docker")
			writeDockerAuthorityFixture(t, executable, "pinned")
			fixture := writeDockerConfigAuthorityFixture(t, filepath.Join(root, "config"), "desktop-linux", "unix:///tmp/desktop.sock")
			runner, err := newDockerRunner(executable, fixture.root, fixture.authority)
			if err != nil {
				t.Fatal(err)
			}
			test.mutate(t, fixture)
			if test.start {
				process, err := runner.Start(context.Background(), Command{Args: []string{"run", "--pull=never"}})
				if err == nil || process != nil || !strings.Contains(err.Error(), "Docker endpoint authority drift") {
					t.Fatalf("Start() = %#v, %v", process, err)
				}
				return
			}
			result, err := runner.Run(context.Background(), Command{Args: []string{"version"}})
			if err == nil || result.ExitCode != -1 || !strings.Contains(err.Error(), "Docker endpoint authority drift") {
				t.Fatalf("Run() = %#v, %v", result, err)
			}
		})
	}
}

func copyAuthorityTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}

type dockerAuthorityFixture struct {
	root, meta, ca, key string
	contextName         string
	authority           DockerCommandAuthority
}

func writeDockerConfigAuthorityFixture(t *testing.T, root, contextName, endpoint string) dockerAuthorityFixture {
	t.Helper()
	contextDigest := fmt.Sprintf("%x", sha256.Sum256([]byte(contextName)))
	fixture := dockerAuthorityFixture{
		root: root, contextName: contextName,
		meta:      filepath.Join(root, "contexts", "meta", contextDigest, "meta.json"),
		ca:        filepath.Join(root, "contexts", "tls", contextDigest, "docker", "ca.pem"),
		key:       filepath.Join(root, "contexts", "tls", contextDigest, "docker", "key.pem"),
		authority: DockerCommandAuthority{ContextName: contextName, ContextEndpointDigest: endpointDigest(t, endpoint)},
	}
	for _, path := range []string{filepath.Dir(fixture.meta), filepath.Dir(fixture.ca)} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeAuthorityFile(t, filepath.Join(root, "config.json"), []byte(`{"auths":{}}`+"\n"))
	writeContextMetadata(t, fixture.meta, contextName, endpoint)
	writeAuthorityFile(t, fixture.ca, []byte("pinned ca\n"))
	writeAuthorityFile(t, fixture.key, []byte("pinned key\n"))
	return fixture
}

func endpointDigest(t *testing.T, endpoint string) string {
	t.Helper()
	digest, err := DigestContextEndpoint(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func writeContextMetadata(t *testing.T, path, contextName, endpoint string) {
	t.Helper()
	writeAuthorityFile(t, path, []byte(fmt.Sprintf(`{"Name":%q,"Endpoints":{"docker":{"Host":%q,"SkipTLSVerify":false}}}`, contextName, endpoint)+"\n"))
}

func writeAuthorityFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeDockerAuthorityFixture(t *testing.T, path, marker string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"IFS= read -r input || true\n" +
		"printf 'marker=%s\\n' '" + marker + "'\n" +
		"printf 'args=%s\\n' \"$*\"\n" +
		"printf 'config=%s\\n' \"${DOCKER_CONFIG-unset}\"\n" +
		"printf 'host=%s\\n' \"${DOCKER_HOST-unset}\"\n" +
		"printf 'context=%s\\n' \"${DOCKER_CONTEXT-unset}\"\n" +
		"printf 'input=%s\\n' \"$input\"\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeDockerObservationFixture(t *testing.T, path, logPath, contextName, endpoint string) {
	t.Helper()
	script := fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$3:$4" in
  version:*) printf '{"api_version":"1.53","server_version":"31.0.0","operating_system":"linux","architecture":"arm64"}\n' ;;
  info:*) printf '{"daemon_id":"desktop-daemon","provider_name":"Docker Desktop"}\n' ;;
  context:show) printf '%%s\n' %q ;;
  context:inspect) printf '"%%s"\n' %q ;;
  *) exit 64 ;;
esac
`, logPath, contextName, endpoint)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
}

func assertPinnedDockerAuthority(t *testing.T, output, marker, configRoot, contextName, input string) {
	t.Helper()
	for _, expected := range []string{
		"marker=" + marker,
		"args=--context " + contextName,
		"config=" + configRoot,
		"host=unset",
		"context=unset",
		"input=" + input,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("authority output missing %q: %q", expected, output)
		}
	}
	if strings.Contains(output, "marker=second") {
		t.Fatalf("Runner followed drifted PATH: %q", output)
	}
}
