package main

import (
	"context"
	"fmt"
	"io"

	"github.com/Yangyang96/chora/internal/localweb"
)

func runResourceCheckFingerprint(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) != 0 || stdin == nil || stdout == nil || stderr == nil {
		fmt.Fprintln(stderr, "internal-resource-fingerprint accepts JSON on stdin and no arguments")
		return 2
	}
	if err := localweb.RunResourceCheckFingerprint(context.Background(), stdin, stdout); err != nil {
		fmt.Fprintln(stderr, "resource fingerprint unavailable")
		return 1
	}
	return 0
}
