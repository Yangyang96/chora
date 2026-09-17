package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"

	"github.com/Yangyang96/chora/internal/desktop"
)

func runDesktop(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, desktopUsage)
		return 2
	}
	var result any
	var err error
	switch args[0] {
	case "inspect":
		f := flag.NewFlagSet("desktop inspect", flag.ContinueOnError)
		f.SetOutput(stderr)
		data := f.String("data", "", "absolute data root")
		if f.Parse(args[1:]) != nil || f.NArg() != 0 {
			return 2
		}
		result, err = desktop.InspectSchema(context.Background(), *data, workbenchSchemaVersion)
	case "backup":
		f := flag.NewFlagSet("desktop backup", flag.ContinueOnError)
		f.SetOutput(stderr)
		data := f.String("data", "", "absolute data root")
		output := f.String("output", "", "new absolute backup path")
		if f.Parse(args[1:]) != nil || f.NArg() != 0 {
			return 2
		}
		result, err = desktop.Backup(*data, *output)
	case "restore":
		f := flag.NewFlagSet("desktop restore", flag.ContinueOnError)
		f.SetOutput(stderr)
		data := f.String("data", "", "exact original absolute data root")
		backup := f.String("backup", "", "absolute backup path")
		if f.Parse(args[1:]) != nil || f.NArg() != 0 {
			return 2
		}
		var recovery string
		recovery, err = desktop.Restore(*data, *backup)
		result = map[string]string{"dataRoot": *data, "recoveryPath": recovery}
	case "adopt":
		f := flag.NewFlagSet("desktop adopt", flag.ContinueOnError)
		f.SetOutput(stderr)
		source := f.String("source-data", "", "existing source Workbench data root")
		output := f.String("output", "", "new absolute backup path")
		if f.Parse(args[1:]) != nil || f.NArg() != 0 {
			return 2
		}
		result, err = desktop.Adopt(*source, *output)
	case "verify-update":
		f := flag.NewFlagSet("desktop verify-update", flag.ContinueOnError)
		f.SetOutput(stderr)
		app := f.String("app", "", "candidate app bundle")
		current := f.String("current-app", "", "installed app bundle")
		if f.Parse(args[1:]) != nil || f.NArg() != 0 {
			return 2
		}
		result, err = desktop.VerifyUpdate(*app, *current)
	default:
		fmt.Fprintln(stderr, desktopUsage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "desktop operation failed:", err)
		return 1
	}
	if err = json.NewEncoder(stdout).Encode(result); err != nil {
		return 1
	}
	return 0
}

const desktopUsage = "usage: chora desktop inspect|backup|restore|adopt|verify-update [options]"
