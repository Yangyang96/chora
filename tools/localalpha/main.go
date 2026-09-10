package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/Yangyang96/chora/internal/sourcebundle"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		usage(stderr)
		return 2
	}
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	var source, bundle, install, data, stage string
	var expectedAggregate string
	switch args[0] {
	case "check-policy":
		flags.StringVar(&source, "source", "", "absolute product source root")
	case "bundle":
		flags.StringVar(&source, "source", "", "absolute product source root")
		flags.StringVar(&bundle, "bundle", "", "new absolute bundle root")
	case "verify":
		flags.StringVar(&bundle, "bundle", "", "absolute bundle or installation root")
	case "install":
		flags.StringVar(&bundle, "bundle", "", "absolute verified bundle root")
		flags.StringVar(&install, "root", "", "new absolute installation root")
	case "stage-web":
		flags.StringVar(&install, "root", "", "absolute installed product root")
		flags.StringVar(&stage, "stage", "", "new web build workspace")
		flags.StringVar(&expectedAggregate, "expected-aggregate", "", "exact expected source aggregate")
	case "init-data":
		flags.StringVar(&install, "root", "", "absolute installed product root")
		flags.StringVar(&data, "data", "", "new separate data root")
		flags.StringVar(&expectedAggregate, "expected-aggregate", "", "exact expected source aggregate")
	case "cleanup":
		flags.StringVar(&install, "root", "", "absolute owned installation root")
		flags.StringVar(&data, "data", "", "absolute marker-bound data root")
		flags.StringVar(&expectedAggregate, "expected-aggregate", "", "exact expected source aggregate")
	default:
		usage(stderr)
		return 2
	}
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return 2
	}
	var (
		manifest sourcebundle.Manifest
		err      error
	)
	switch args[0] {
	case "check-policy":
		err = sourcebundle.CheckPolicy(source)
		if err == nil {
			fmt.Fprintln(stdout, `{"status":"passed","policyMatches":true}`)
			return 0
		}
	case "bundle":
		manifest, err = sourcebundle.Create(source, bundle)
	case "verify":
		manifest, err = sourcebundle.Verify(bundle)
	case "install":
		manifest, err = sourcebundle.Install(bundle, install)
	case "stage-web":
		var files int
		files, err = sourcebundle.StageWeb(install, stage, expectedAggregate)
		if err == nil {
			fmt.Fprintf(stdout, "{\"status\":\"passed\",\"files\":%d}\n", files)
			return 0
		}
	case "init-data":
		err = sourcebundle.InitData(install, data, expectedAggregate)
		if err == nil {
			fmt.Fprintln(stdout, `{"status":"passed","initialized":true}`)
			return 0
		}
	case "cleanup":
		err = sourcebundle.CleanupInstallation(install, data, expectedAggregate)
		if err == nil {
			fmt.Fprintln(stdout, `{"status":"passed","removed":true}`)
			return 0
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	encoded, err := json.Marshal(map[string]any{
		"status": "passed", "schemaVersion": manifest.SchemaVersion,
		"aggregateSHA256": manifest.AggregateSHA256, "files": len(manifest.Files),
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, string(encoded))
	return 0
}

func usage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: localalpha check-policy --source ABS | bundle --source ABS --bundle ABS | verify --bundle ABS | install --bundle ABS --root ABS | stage-web --root ABS --stage ABS --expected-aggregate SHA256 | init-data --root ABS --data ABS --expected-aggregate SHA256 | cleanup --root ABS --data ABS --expected-aggregate SHA256")
}
