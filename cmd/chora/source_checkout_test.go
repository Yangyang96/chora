package main

import (
	"bytes"
	"errors"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestSourceCheckoutQualificationPreservesProbeErrorAndDistinguishesInvalidQualification(t *testing.T) {
	probeErr := errors.New("safe Probe diagnostic")
	if err := sourceCheckoutQualificationError(probeErr, false); !errors.Is(err, probeErr) {
		t.Fatalf("Probe error was not preserved: %v", err)
	}
	invalidErr := sourceCheckoutQualificationError(nil, false)
	if invalidErr == nil || errors.Is(invalidErr, probeErr) || !strings.Contains(invalidErr.Error(), "invalid qualification") {
		t.Fatalf("invalid qualification error = %v", invalidErr)
	}
	if err := sourceCheckoutQualificationError(nil, true); err != nil {
		t.Fatalf("valid qualification error = %v", err)
	}
}

func TestSourceCheckoutRejectsIncompleteAuthorityBeforeDocker(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := runSourceCheckoutWithListen(nil, &stdout, &stderr, func(*http.Server) error { return nil }); code != 2 {
		t.Fatalf("empty source-checkout code = %d, stderr=%q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "requires absolute") || stdout.Len() != 0 {
		t.Fatalf("unexpected source-checkout diagnostic stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
}

func TestSourceCheckoutRejectsUnsafeOAuthBeforeDocker(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := filepath.Join(root, "source")
	data := filepath.Join(root, "data")
	auth := filepath.Join(root, "auth.json")
	docker := filepath.Join(root, "docker")
	for _, path := range []string{source, filepath.Join(source, "web", "dist")} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(auth, []byte(`{"openai-codex":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(docker, []byte("fixture"), 0o700); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runSourceCheckoutWithListen([]string{
		"--source", source, "--repository", source, "--data", data,
		"--auth", auth, "--docker-cli", docker, "--docker-context", "colima",
	}, &stdout, &stderr, func(*http.Server) error { return nil })
	if code != 1 || !strings.Contains(stderr.String(), "OAuth authority is unavailable") || stdout.Len() != 0 {
		t.Fatalf("unsafe OAuth code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if _, err := os.Lstat(data); !os.IsNotExist(err) {
		t.Fatalf("unsafe OAuth mutated data root: %v", err)
	}
}

func TestWorkbenchDocumentationUsesLocalConnectedEntry(t *testing.T) {
	for _, name := range []string{"README.md", "README.zh-CN.md"} {
		data, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		for _, forbidden := range []string{
			"/private/tmp/chora-source-checkout-data",
			`--repository "$PWD"`,
			"mkdir -m 700 /private/tmp",
			"colima ssh -- test -d",
		} {
			if strings.Contains(text, forbidden) {
				t.Fatalf("%s retains obsolete or unsafe startup command %q", name, forbidden)
			}
		}
		for _, required := range []string{
			`go run ./cmd/chora workbench --source "$(pwd -P)"`,
			"npm ci",
			"/login",
			"/model",
			"Local Connected",
		} {
			if !strings.Contains(text, required) {
				t.Fatalf("%s is missing the supported Workbench contract %q", name, required)
			}
		}
		disclosure := "No Sandbox"
		if name == "README.zh-CN.md" {
			disclosure = "无沙箱"
		}
		if !strings.Contains(text, disclosure) {
			t.Fatalf("%s is missing the local execution disclosure %q", name, disclosure)
		}
	}
}

func TestSourceCheckoutDeveloperGatesBoundConcurrencyAndReportProgress(t *testing.T) {
	makefile, err := os.ReadFile(filepath.Join("..", "..", "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(makefile)
	for _, required := range []string{
		"GO_TEST_TIMEOUT ?= 15m",
		"GO_TEST_PACKAGE_PARALLEL ?= 4",
		"GO_RACE_PACKAGE_PARALLEL ?= 3",
		"GO_TEST_HEARTBEAT_SECONDS ?= 30",
		"GO_TAGGED_LOCALWEB_TEST_PATTERN ?=",
		"GO_REMAINING_PRODUCT_PACKAGES =",
		"define RUN_WITH_PROGRESS",
		"still running",
		"go test -race -p=1 -timeout=$(GO_TEST_TIMEOUT) ./internal/app",
		"go test -race -p=1 -timeout=$(GO_TEST_TIMEOUT) ./internal/localweb",
		"go test -race -p=$(GO_RACE_PACKAGE_PARALLEL) -timeout=$(GO_TEST_TIMEOUT)",
		"-tags chora_e2e -run='$(GO_TAGGED_LOCALWEB_TEST_PATTERN)' ./internal/localweb",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("Makefile is missing bounded visible developer-gate contract %q", required)
		}
	}
	for _, forbidden := range []string{
		"go test $$packages",
		"go test -race $$packages",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("Makefile retains unbounded silent developer-gate command %q", forbidden)
		}
	}

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	const patternPrefix = "GO_TAGGED_LOCALWEB_TEST_PATTERN ?= "
	patternText := ""
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, patternPrefix) {
			patternText = strings.ReplaceAll(strings.TrimPrefix(line, patternPrefix), "$$", "$")
			break
		}
	}
	pattern, err := regexp.Compile(patternText)
	if err != nil || patternText == "" {
		t.Fatalf("tagged localweb test pattern %q is invalid: %v", patternText, err)
	}
	listTests := func(tags ...string) map[string]bool {
		t.Helper()
		args := []string{"test"}
		args = append(args, tags...)
		args = append(args, "./internal/localweb", "-list", "^Test")
		command := exec.Command("go", args...)
		command.Dir = root
		output, err := command.CombinedOutput()
		if err != nil {
			t.Fatalf("list localweb tests with %v: %v\n%s", tags, err, output)
		}
		result := map[string]bool{}
		for _, line := range strings.Split(string(output), "\n") {
			if strings.HasPrefix(line, "Test") {
				result[line] = true
			}
		}
		return result
	}
	plainTests := listTests()
	taggedTests := listTests("-tags", "chora_e2e")
	taggedOnly := 0
	for name := range taggedTests {
		only := !plainTests[name]
		if only {
			taggedOnly++
		}
		if pattern.MatchString(name) != only {
			t.Fatalf("tagged localweb pattern match for %s = %t, want tagged-only=%t", name, pattern.MatchString(name), only)
		}
	}
	if taggedOnly == 0 {
		t.Fatal("tagged localweb suite has no tag-exclusive tests")
	}
	fixture := filepath.Join(t.TempDir(), "progress.mk")
	fixtureText := "include " + filepath.Join(root, "Makefile") + "\n" +
		"FIXTURE_EXIT ?= 0\n" +
		".PHONY: fixture\n" +
		"fixture:\n" +
		"\t$(call RUN_WITH_PROGRESS,fixture progress,sh -c 'sleep 2; exit $(FIXTURE_EXIT)')\n"
	if err := os.WriteFile(fixture, []byte(fixtureText), 0o600); err != nil {
		t.Fatal(err)
	}

	progress := exec.Command("make", "--no-print-directory", "-f", fixture, "fixture", "GO_TEST_HEARTBEAT_SECONDS=1")
	output, err := progress.CombinedOutput()
	if err != nil {
		t.Fatalf("progress wrapper failed: %v\n%s", err, output)
	}
	for _, required := range []string{"fixture progress", "still running", "completed"} {
		if !strings.Contains(string(output), required) {
			t.Fatalf("progress output %q is missing %q", output, required)
		}
	}

	failing := exec.Command("make", "--no-print-directory", "-f", fixture, "fixture", "GO_TEST_HEARTBEAT_SECONDS=1", "FIXTURE_EXIT=7")
	output, err = failing.CombinedOutput()
	exit, ok := err.(*exec.ExitError)
	if !ok || exit.ExitCode() != 2 || !strings.Contains(string(output), "failed (exit 7 after") {
		t.Fatalf("failure wrapper err=%v output=%q", err, output)
	}
}
