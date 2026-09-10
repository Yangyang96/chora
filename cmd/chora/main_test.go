package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/buildinfo"
	"github.com/Yangyang96/chora/internal/preflight"
)

func TestRunVersion(t *testing.T) {
	originalVersion := buildinfo.Version
	buildinfo.Version = "test-version"
	t.Cleanup(func() {
		buildinfo.Version = originalVersion
	})

	var stdout bytes.Buffer
	var stderr bytes.Buffer

	exitCode := run([]string{"version"}, &stdout, &stderr)

	if exitCode != 0 {
		t.Fatalf("exit code = %d, want 0", exitCode)
	}
	if got, want := stdout.String(), "chora test-version\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got := stderr.String(); got != "" {
		t.Fatalf("stderr = %q, want empty", got)
	}
}

func TestRunDoctorJSONReportsFirstUnsafeBoundary(t *testing.T) {
	var stdout, stderr bytes.Buffer
	args := []string{
		"--source", "/source", "--source-manifest", "/source/source-manifest.json", "--bundle-aggregate", strings.Repeat("b", 64), "--install", "/install", "--data", "/data",
		"--auth", "/private/auth.json", "--ca", "/private/ca.pem",
		"--proxy", preflight.FixedProxyURL, "--model-url", preflight.AllowedModelURL,
		"--port", "8787", "--json",
	}
	probes := preflight.Probes{
		Host: func() (string, string) { return "linux", "amd64" },
		Command: func(context.Context, string, ...string) preflight.CommandResult {
			t.Fatal("command ran after host failure")
			return preflight.CommandResult{}
		},
	}

	exitCode := runDoctor(args, &stdout, &stderr, probes)

	if exitCode != 1 || stderr.Len() != 0 {
		t.Fatalf("exit/stderr = %d/%q, want 1/empty", exitCode, stderr.String())
	}
	var report preflight.Report
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("decode report: %v; output %q", err, stdout.String())
	}
	if report.Failure == nil || report.Failure.Boundary != "host.platform" || report.ResourcesCreated || report.InputFingerprint == "" {
		t.Fatalf("report = %#v", report)
	}
	if strings.Contains(stdout.String(), "secret") {
		t.Fatal("doctor JSON leaked secret")
	}
}

func TestRunDoctorTextIncludesExactOperatorAction(t *testing.T) {
	var stdout, stderr bytes.Buffer
	probes := preflight.Probes{
		Host:    func() (string, string) { return "linux", "amd64" },
		Command: func(context.Context, string, ...string) preflight.CommandResult { return preflight.CommandResult{} },
	}
	exitCode := runDoctor([]string{
		"--source", "/source", "--source-manifest", "/source/source-manifest.json", "--bundle-aggregate", strings.Repeat("b", 64), "--install", "/install", "--data", "/data",
		"--auth", "/auth", "--ca", "/ca", "--proxy", preflight.FixedProxyURL,
		"--model-url", preflight.AllowedModelURL, "--port", "8787",
	}, &stdout, &stderr, probes)
	if exitCode != 1 || !strings.Contains(stdout.String(), "Run Chora on a supported macOS Apple Silicon host.") {
		t.Fatalf("exit/output = %d/%q", exitCode, stdout.String())
	}
}

func TestRunServeFailsPreflightBeforeDatabaseOrListener(t *testing.T) {
	var stdout, stderr bytes.Buffer
	probes := preflight.Probes{
		Host: func() (string, string) { return "linux", "amd64" },
		Command: func(context.Context, string, ...string) preflight.CommandResult {
			t.Fatal("command ran after host failure")
			return preflight.CommandResult{}
		},
	}
	exitCode := runServeWithProbes([]string{
		"--db", "/data/chora.db", "--web", "/install/build/web-workspace/web/dist", "--port", "8787",
		"--source", "/install", "--source-manifest", "/install/source-manifest.json", "--bundle-aggregate", strings.Repeat("b", 64), "--install", "/install", "--data", "/data", "--repository", "/target",
		"--auth", "/auth", "--ca", "/ca", "--proxy", preflight.FixedProxyURL,
		"--model-url", preflight.AllowedModelURL,
		"--preflight-fingerprint", strings.Repeat("a", 64),
		"--installation-state-root", "/installation-state", "--generation", "g1",
		"--docker-cli", "/private/tools/docker-29.6.1", "--docker-context", "chora-local", "--endpoint-digest", strings.Repeat("e", 64),
		"--release-root", "/release", "--release-manifest", "manifest.json", "--manifest-sha256", strings.Repeat("c", 64), "--release-id", "release-1",
		"--private-pi-root", "/private/pi", "--private-pi-source", "/private/pi-source",
		"--private-pi-manifest", "/private/pi-manifest.json", "--private-pi-manifest-sha256", strings.Repeat("d", 64),
		"--probe-runtime-root", "/private/probe", "--colima-tool-root", "/private/tools", "--colima-profile", "chora-local",
		"--colima-source", "/private/sources/colima", "--docker-client-source", "/private/sources/docker",
	}, &stdout, &stderr, probes)
	if exitCode != 1 || !strings.Contains(stderr.String(), "host.platform") {
		t.Fatalf("exit/stderr = %d/%q", exitCode, stderr.String())
	}
}

func TestRunServeRejectsDatabaseOutsideDeclaredDataRoot(t *testing.T) {
	var stdout, stderr bytes.Buffer
	probes := preflight.Probes{Host: func() (string, string) {
		t.Fatal("host probe ran for invalid serve paths")
		return "", ""
	}}
	exitCode := runServeWithProbes([]string{
		"--db", "/outside/chora.db", "--web", "/install/build/web-workspace/web/dist", "--port", "8787",
		"--source", "/install", "--source-manifest", "/install/source-manifest.json", "--bundle-aggregate", strings.Repeat("b", 64), "--install", "/install", "--data", "/data", "--repository", "/target",
		"--auth", "/auth", "--ca", "/ca", "--proxy", preflight.FixedProxyURL,
		"--model-url", preflight.AllowedModelURL, "--preflight-fingerprint", strings.Repeat("a", 64),
	}, &stdout, &stderr, probes)
	if exitCode != 2 {
		t.Fatalf("exit = %d, want 2", exitCode)
	}
}

func TestRunServeRejectsRepositoryOverlappingProductRoots(t *testing.T) {
	var stdout, stderr bytes.Buffer
	probes := preflight.Probes{Host: func() (string, string) {
		t.Fatal("host probe ran for overlapping Apply target")
		return "", ""
	}}
	exitCode := runServeWithProbes([]string{
		"--db", "/data/chora.db", "--web", "/install/build/web-workspace/web/dist", "--port", "8787",
		"--source", "/install", "--source-manifest", "/install/source-manifest.json", "--bundle-aggregate", strings.Repeat("b", 64), "--install", "/install", "--data", "/data", "--repository", "/data/target",
		"--auth", "/auth", "--ca", "/ca", "--proxy", preflight.FixedProxyURL,
		"--model-url", preflight.AllowedModelURL, "--preflight-fingerprint", strings.Repeat("a", 64),
	}, &stdout, &stderr, probes)
	if exitCode != 2 {
		t.Fatalf("exit = %d, want 2", exitCode)
	}
}

func TestRunServeRejectsPartialO4AuthorityBeforeProductSideEffects(t *testing.T) {
	var stdout, stderr bytes.Buffer
	probes := preflight.Probes{Host: func() (string, string) {
		t.Fatal("host probe ran after partial O4 authority")
		return "", ""
	}}
	args := append(validSyntheticServeArgs(), "--o4-acceptance-authority", "/data/o4-authority.json")
	if exit := runServeWithProbes(args, &stdout, &stderr, probes); exit != 1 || !strings.Contains(stderr.String(), "O4 acceptance authority unavailable") {
		t.Fatalf("partial O4 serve = exit=%d stderr=%q", exit, stderr.String())
	}
}

func TestRunServeRejectsObsoleteAcceptanceEnvironmentBeforeProductSideEffects(t *testing.T) {
	t.Setenv("CHORA_TEST_BOUNDARY_FAULT_SEAM", "legacy")
	var stdout, stderr bytes.Buffer
	probes := preflight.Probes{Host: func() (string, string) {
		t.Fatal("host probe ran with obsolete acceptance environment")
		return "", ""
	}}
	if exit := runServeWithProbes(validSyntheticServeArgs(), &stdout, &stderr, probes); exit != 1 || !strings.Contains(stderr.String(), "obsolete acceptance environment authority is forbidden") {
		t.Fatalf("legacy environment serve = exit=%d stderr=%q", exit, stderr.String())
	}
}

func TestRequireO4AuthorityActiveGenerationRejectsMismatchBeforeSideEffects(t *testing.T) {
	tests := []struct {
		name                string
		enabled             bool
		authorityGeneration string
		activeGeneration    string
		wantError           bool
	}{
		{name: "disabled", enabled: false, authorityGeneration: "gen_authority", activeGeneration: "gen_active"},
		{name: "exact match", enabled: true, authorityGeneration: "gen_exact", activeGeneration: "gen_exact"},
		{name: "mismatch", enabled: true, authorityGeneration: "gen_authority", activeGeneration: "gen_active", wantError: true},
		{name: "no active generation", enabled: true, authorityGeneration: "gen_authority", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := requireO4AuthorityActiveGeneration(test.enabled, test.authorityGeneration, test.activeGeneration)
			if (err != nil) != test.wantError {
				t.Fatalf("requireO4AuthorityActiveGeneration() error = %v, wantError %t", err, test.wantError)
			}
		})
	}
}

func validSyntheticServeArgs() []string {
	return []string{
		"--db", "/data/chora.db", "--web", "/install/build/web-workspace/web/dist", "--port", "8787",
		"--source", "/install", "--source-manifest", "/install/source-manifest.json", "--bundle-aggregate", strings.Repeat("b", 64), "--install", "/install", "--data", "/data", "--repository", "/target",
		"--auth", "/auth", "--ca", "/ca", "--proxy", preflight.FixedProxyURL, "--model-url", preflight.AllowedModelURL,
		"--preflight-fingerprint", strings.Repeat("a", 64), "--installation-state-root", "/installation-state", "--generation", "g1",
		"--docker-cli", "/private/tools/docker-29.6.1", "--docker-context", "chora-local", "--endpoint-digest", strings.Repeat("e", 64),
		"--release-root", "/release", "--release-manifest", "manifest.json", "--manifest-sha256", strings.Repeat("c", 64), "--release-id", "release-1",
		"--private-pi-root", "/private/pi", "--private-pi-source", "/private/pi-source", "--private-pi-manifest", "/private/pi-manifest.json", "--private-pi-manifest-sha256", strings.Repeat("d", 64),
		"--probe-runtime-root", "/private/probe", "--colima-tool-root", "/private/tools", "--colima-profile", "chora-local", "--colima-source", "/private/sources/colima", "--docker-client-source", "/private/sources/docker",
	}
}

func TestRunUsage(t *testing.T) {
	tests := []struct {
		name string
		args []string
	}{
		{name: "no arguments"},
		{name: "invalid argument", args: []string{"unknown"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout bytes.Buffer
			var stderr bytes.Buffer

			exitCode := run(tt.args, &stdout, &stderr)

			if exitCode != 2 {
				t.Fatalf("exit code = %d, want 2", exitCode)
			}
			if got := stdout.String(); got != "" {
				t.Fatalf("stdout = %q, want empty", got)
			}
			if got, want := stderr.String(), usage+"\n"; got != want {
				t.Fatalf("stderr = %q, want %q", got, want)
			}
		})
	}
}
