package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestGenerateRequiresResolutionAndOutput(t *testing.T) {
	var output bytes.Buffer
	err := run([]string{"generate"}, &output)
	if err == nil || !strings.Contains(err.Error(), "usage:") {
		t.Fatalf("error = %v", err)
	}
}

func TestUnknownCommandFails(t *testing.T) {
	if err := run([]string{"pull"}, &bytes.Buffer{}); err == nil {
		t.Fatal("image operation command unexpectedly accepted")
	}
}
