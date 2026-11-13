package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func commandFixture(t *testing.T) []string {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "hermes_cli"), 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "native")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nexit 98\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"install-stamp.json", "pyproject.toml", "hermes_bootstrap.py", "hermes_cli/main.py", "hermes_cli/_parser.py", "hermes_cli/stream_json.py", "hermes_cli/env_loader.py", "hermes_cli/gateway_chat.py", "hermes_cli/gateway_chat_startup.py"} {
		data := []byte("# synthetic\n")
		if name == "install-stamp.json" {
			data = []byte(`{"displayVersion":"0.26.0-next.1"}`)
		}
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return []string{"-executable", path, "-native-launcher", path, "-interpreter", path, "-source-root", root, "-auth-home", root}
}

func TestStaticCommandReportsPendingWithoutNativeExecution(t *testing.T) {
	var output bytes.Buffer
	if err := run(commandFixture(t), bytes.NewReader(nil), &output); err != nil {
		t.Fatal(err)
	}
	var result struct {
		Version, Authentication string
		Qualification           struct{ State string }
	}
	if err := json.Unmarshal(output.Bytes(), &result); err != nil || result.Version != "0.26.0-next.1" || result.Qualification.State != "pending" {
		t.Fatal(result, err)
	}
}

func TestCommandTrustedReviewSequenceCannotEnableLaunch(t *testing.T) {
	args := commandFixture(t)
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	der, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(t.TempDir(), "public.pem")
	if err := os.WriteFile(key, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	args = append(args, "-reviewer-key", key, "-reviewer", "host", "-source-reference", "https://example.invalid/immutable/hermes")
	inputReader, inputWriter := io.Pipe()
	outputReader, outputWriter := io.Pipe()
	defer inputReader.Close()
	defer inputWriter.Close()
	defer outputReader.Close()
	defer outputWriter.Close()
	done := make(chan error, 1)
	go func() {
		err := run(args, inputReader, outputWriter)
		outputWriter.CloseWithError(err)
		inputReader.Close()
		done <- err
	}()
	decoder := json.NewDecoder(outputReader)
	var request struct{ MessageHex string }
	if err := decoder.Decode(&request); err != nil {
		t.Fatal(err)
	}
	message, err := hex.DecodeString(request.MessageHex)
	if err != nil || len(message) != 32 {
		t.Fatal(err)
	}
	sig := ed25519.Sign(private, message)
	if err := json.NewEncoder(inputWriter).Encode(map[string]string{"SignatureHex": hex.EncodeToString(sig)}); err != nil {
		t.Fatal(err)
	}
	inputWriter.Close()
	var result struct {
		Ready         bool
		Failure       string
		Qualification struct {
			Evidence []struct{ Capability, State string }
		}
	}
	if err := decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if result.Ready || result.Failure == "" {
		t.Fatal("signature enabled native execution")
	}
	verified := 0
	for _, e := range result.Qualification.Evidence {
		if e.State == "verified" {
			verified++
		}
	}
	if verified != 3 {
		t.Fatal(result)
	}
}
