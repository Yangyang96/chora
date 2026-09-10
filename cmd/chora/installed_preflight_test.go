package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/preflight"
)

func TestInstalledDoctorAuthoritiesRejectSpliceReplayAndWritableFiles(t *testing.T) {
	root := filepath.Join(canonicalTempRoot(t), "authorities")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	observation, previous := strings.Repeat("2", 64), strings.Repeat("3", 64)
	receipt := setupReceiptAuthority{
		SchemaVersion: setupReceiptSchema, TupleIdentity: strings.Repeat("1", 64), Phase: "setup", Sequence: 4,
		Status: "passed", OperationDigest: strings.Repeat("4", 64), ObservationDigest: &observation, PreviousReceiptDigest: &previous,
	}
	receipt.ReceiptDigest = setupReceiptDigest(receipt)
	model := modelRequestAuthority{
		SchemaVersion: modelRequestAuthoritySchema, Status: "authorized", GenerationID: "generation-1",
		ProviderIdentitySHA256: strings.Repeat("5", 64), ModelIdentitySHA256: strings.Repeat("6", 64), PolicySHA256: strings.Repeat("7", 64),
		ProxyURLSHA256: hashText(preflight.FixedProxyURL), ModelURLSHA256: hashText(preflight.AllowedModelURL),
		AuthFileSHA256: strings.Repeat("8", 64), CAFileSHA256: strings.Repeat("9", 64), AuthenticationRequired: true,
	}
	receiptPath, receiptSHA := writeAuthorityFixture(t, root, "setup.json", receipt)
	modelPath, modelSHA := writeAuthorityFixture(t, root, "model.json", model)
	command, err := parseInstalledDoctorCommand(
		replaceInstalledDoctorArg(replaceInstalledDoctorArg(replaceInstalledDoctorArg(replaceInstalledDoctorArg(
			validInstalledDoctorArgs(), "--setup-receipt", receiptPath), "--setup-receipt-sha256", receiptSHA),
			"--model-request-authority", modelPath), "--model-request-authority-sha256", modelSHA), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := validateInstalledDoctorAuthorities(command); err != nil {
		t.Fatalf("valid authority chain rejected: %v", err)
	}

	spliced := receipt
	otherPrevious := strings.Repeat("a", 64)
	spliced.PreviousReceiptDigest = &otherPrevious
	command.SetupReceiptSHA256 = rewriteAuthorityFixture(t, receiptPath, spliced)
	command.Config.SetupReceiptSHA256 = command.SetupReceiptSHA256
	if _, err := validateInstalledDoctorAuthorities(command); err == nil {
		t.Fatal("Setup receipt splice was accepted after outer digest was updated")
	}

	command.SetupReceiptSHA256 = rewriteAuthorityFixture(t, receiptPath, receipt)
	command.Config.SetupReceiptSHA256 = command.SetupReceiptSHA256
	replayed := model
	replayed.GenerationID = "generation-2"
	command.ModelAuthoritySHA256 = rewriteAuthorityFixture(t, modelPath, replayed)
	command.Config.ModelAuthoritySHA256 = command.ModelAuthoritySHA256
	if _, err := validateInstalledDoctorAuthorities(command); err == nil {
		t.Fatal("model request authority from another generation was accepted")
	}

	command.ModelAuthoritySHA256 = rewriteAuthorityFixture(t, modelPath, model)
	command.Config.ModelAuthoritySHA256 = command.ModelAuthoritySHA256
	if err := os.Chmod(modelPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateInstalledDoctorAuthorities(command); err == nil {
		t.Fatal("writable model request authority was accepted")
	}
}

func TestParseInstalledDoctorCommandBindsPostSetupReadOnlyProof(t *testing.T) {
	command, err := parseInstalledDoctorCommand(validInstalledDoctorArgs(), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if command.Config.Mode != preflight.ModeInstalledDoctor || command.Runtime.AllowPrivatePiInstall ||
		command.Runtime.GenerationID != "generation-1" || command.Runtime.Release.ReleaseID != "release-1" {
		t.Fatalf("installed Doctor command = %#v", command)
	}
	if command.Config.PriorInputFingerprint != "" {
		t.Fatalf("installed Doctor inherited impossible prior fingerprint %q", command.Config.PriorInputFingerprint)
	}
}

func TestParseInstalledDoctorCommandRejectsAmbientOrMutableIdentity(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
	}{
		{name: "relative state", args: replaceInstalledDoctorArg(validInstalledDoctorArgs(), "--state-root", "state")},
		{name: "ambient context", args: replaceInstalledDoctorArg(validInstalledDoctorArgs(), "--docker-context", "")},
		{name: "unpinned model", args: replaceInstalledDoctorArg(validInstalledDoctorArgs(), "--model-url", "https://example.test")},
		{name: "detached installed source", args: replaceInstalledDoctorArg(validInstalledDoctorArgs(), "--source", "/private/source")},
		{name: "nested source manifest", args: replaceInstalledDoctorArg(validInstalledDoctorArgs(), "--source-manifest", "/private/install/source/source-manifest.json")},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseInstalledDoctorCommand(test.args, &bytes.Buffer{}); err == nil {
				t.Fatal("unsafe installed Doctor arguments accepted")
			}
		})
	}
}

func TestInstalledDoctorDispatchDoesNotRunProofForInvalidArguments(t *testing.T) {
	var stdout, stderr bytes.Buffer
	exit := runWithProductExecutor([]string{"installed-doctor"}, &stdout, &stderr, &fakeProductExecutor{})
	if exit != 2 || stdout.Len() != 0 || !strings.Contains(stderr.String(), "installed-doctor requires") {
		t.Fatalf("exit/stdout/stderr = %d/%q/%q", exit, stdout.String(), stderr.String())
	}
}

func validInstalledDoctorArgs() []string {
	return []string{
		"--state-root", "/private/state", "--generation", "generation-1",
		"--docker-cli", "/private/tools/docker-29.6.1", "--docker-context", "chora-o4", "--endpoint-digest", strings.Repeat("a", 64),
		"--release-root", "/private/release", "--release-manifest", "manifest.json", "--manifest-sha256", strings.Repeat("b", 64), "--release-id", "release-1",
		"--private-pi-root", "/private/pi", "--private-pi-source", "/private/pi-source", "--private-pi-manifest", "/private/pi-manifest.json", "--private-pi-manifest-sha256", strings.Repeat("c", 64),
		"--source", "/private/install", "--source-manifest", "/private/install/source-manifest.json", "--bundle-aggregate", strings.Repeat("d", 64),
		"--install", "/private/install", "--data", "/private/data", "--auth", "/private/auth.json", "--ca", "/private/ca.pem",
		"--proxy", preflight.FixedProxyURL, "--model-url", preflight.AllowedModelURL, "--port", "8787",
		"--setup-receipt", "/private/04-setup.json", "--setup-receipt-sha256", strings.Repeat("e", 64),
		"--model-request-authority", "/private/model-request-authority.json", "--model-request-authority-sha256", strings.Repeat("f", 64),
	}
}

func replaceInstalledDoctorArg(args []string, name, value string) []string {
	copy := append([]string(nil), args...)
	for index := 0; index+1 < len(copy); index++ {
		if copy[index] == name {
			copy[index+1] = value
			return copy
		}
	}
	return copy
}

func writeAuthorityFixture(t *testing.T, root, name string, value any) (string, string) {
	t.Helper()
	path := filepath.Join(root, name)
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o400); err != nil {
		t.Fatal(err)
	}
	return path, hashBytes(data)
}

func rewriteAuthorityFixture(t *testing.T, path string, value any) string {
	t.Helper()
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o400); err != nil {
		t.Fatal(err)
	}
	return hashBytes(data)
}
