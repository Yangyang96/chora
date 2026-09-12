package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/Yangyang96/chora/internal/isolatedenv"
	"io"
	"path/filepath"
	"time"
)

func runPrepareIsolated(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("workbench prepare-isolated", flag.ContinueOnError)
	flags.SetOutput(stderr)
	source := flags.String("source", "", "absolute public Chora source checkout")
	data := flags.String("data", "", "absolute owner-private Workbench data root")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !canonicalAbsolute(*source) || !canonicalAbsolute(*data) || pathsOverlap(*data, *source) {
		fmt.Fprintln(stderr, "prepare-isolated requires absolute --source and --data outside the source checkout")
		return 2
	}
	if err := ensureSourceCheckoutDataRoot(filepath.Clean(*data)); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Minute)
	defer cancel()
	fmt.Fprintln(stdout, "Preparing public Pi 0.85.1 / Node 22.19.0 Docker environment (no credentials used).")
	record, err := isolatedenv.Prepare(ctx, *source, *data)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Prepared %s. Restart Workbench using the same --data root.\n", record.Source.ImageID())
	return 0
}
