package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Yangyang96/chora/internal/releaseassets"
)

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "releaseassets:", err)
		os.Exit(1)
	}
}

func run(arguments []string, output io.Writer) error {
	if len(arguments) == 0 {
		return usageError()
	}
	switch arguments[0] {
	case "digest-context":
		flags := flag.NewFlagSet("digest-context", flag.ContinueOnError)
		root := flags.String("root", ".", "repository root")
		contextPath := flags.String("path", "", "context directory path")
		flags.SetOutput(io.Discard)
		if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 || *contextPath == "" {
			return usageError()
		}
		digest, err := releaseassets.DirectoryDigest(*root, *contextPath)
		if err != nil {
			return err
		}
		fmt.Fprintln(output, digest)
		return nil
	case "verify-spec":
		flags := flag.NewFlagSet("verify-spec", flag.ContinueOnError)
		root := flags.String("root", ".", "repository root")
		specPath := flags.String("spec", "distribution/v1/release-spec.v1.json", "spec path")
		flags.SetOutput(io.Discard)
		if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 {
			return usageError()
		}
		spec, err := readSpec(*root, *specPath)
		if err != nil {
			return err
		}
		if err := releaseassets.ValidateSpecFiles(*root, spec); err != nil {
			return err
		}
		fmt.Fprintf(output, "spec_sha256=%s platform=linux/arm64 roles=4 artifacts=%d\n", releaseassets.SpecDigest(spec), len(spec.Artifacts))
		return nil
	case "verify-manifest":
		flags := flag.NewFlagSet("verify-manifest", flag.ContinueOnError)
		releaseRoot := flags.String("release-root", ".", "standalone release root")
		manifestPath := flags.String("manifest", "release-manifest.v1.json", "release-root-relative manifest path")
		flags.SetOutput(io.Discard)
		if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 {
			return usageError()
		}
		absoluteReleaseRoot, err := absoluteRoot(*releaseRoot)
		if err != nil {
			return err
		}
		manifest, err := readManifest(absoluteReleaseRoot, *manifestPath)
		if err != nil {
			return err
		}
		if err := releaseassets.ValidateManifestFiles(absoluteReleaseRoot, manifest); err != nil {
			return err
		}
		fmt.Fprintf(output, "spec_sha256=%s platform=linux/arm64 roles=4 artifacts=%d\n", manifest.SpecSHA256, len(manifest.Artifacts))
		return nil
	case "generate":
		flags := flag.NewFlagSet("generate", flag.ContinueOnError)
		sourceRoot := flags.String("source-root", ".", "source repository root")
		releaseRoot := flags.String("release-root", "", "distinct standalone release root")
		specPath := flags.String("spec", "distribution/v1/release-spec.v1.json", "source-root-relative spec path")
		resolutionPath := flags.String("resolution", "", "release-root-relative offline Docker archive resolution path")
		outPath := flags.String("out", "", "release-root-relative new manifest path")
		flags.SetOutput(io.Discard)
		if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 || *releaseRoot == "" || *resolutionPath == "" || *outPath == "" {
			return usageError()
		}
		absoluteSourceRoot, err := absoluteRoot(*sourceRoot)
		if err != nil {
			return err
		}
		absoluteReleaseRoot, err := absoluteRoot(*releaseRoot)
		if err != nil {
			return err
		}
		spec, err := readSpec(absoluteSourceRoot, *specPath)
		if err != nil {
			return err
		}
		resolutionFile, err := openRootFile(absoluteReleaseRoot, *resolutionPath)
		if err != nil {
			return fmt.Errorf("offline manifest blocked: open archive resolution: %w", err)
		}
		defer resolutionFile.Close()
		resolution, err := releaseassets.ParseResolution(resolutionFile)
		if err != nil {
			return fmt.Errorf("offline manifest blocked: %w", err)
		}
		manifest, err := releaseassets.GenerateFromRoots(absoluteSourceRoot, absoluteReleaseRoot, spec, resolution)
		if err != nil {
			return fmt.Errorf("offline manifest blocked: %w", err)
		}
		encoded, err := releaseassets.EncodeManifest(manifest)
		if err != nil {
			return err
		}
		path, err := rootedPath(absoluteReleaseRoot, *outPath)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return errors.New("manifest output already exists")
			}
			return err
		}
		if err := releaseassets.MaterializeManifestSourceClosure(absoluteSourceRoot, absoluteReleaseRoot, manifest); err != nil {
			return fmt.Errorf("offline manifest blocked: materialize standalone source closure: %w", err)
		}
		if err := writeNewFile(path, encoded); err != nil {
			return err
		}
		fmt.Fprintf(output, "wrote %s spec_sha256=%s\n", *outPath, manifest.SpecSHA256)
		return nil
	default:
		return usageError()
	}
}

func writeNewFile(path string, data []byte) (returnErr error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
	if err != nil {
		return err
	}
	completed := false
	defer func() {
		if !completed {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	completed = true
	return nil
}

func readSpec(root, relative string) (releaseassets.Spec, error) {
	file, err := openRootFile(root, relative)
	if err != nil {
		return releaseassets.Spec{}, err
	}
	defer file.Close()
	return releaseassets.ParseSpec(file)
}

func readManifest(root, relative string) (releaseassets.Manifest, error) {
	file, err := openRootFile(root, relative)
	if err != nil {
		return releaseassets.Manifest{}, err
	}
	defer file.Close()
	return releaseassets.ParseManifest(file)
}

func openRootFile(root, relative string) (*os.File, error) {
	path, err := rootedPath(root, relative)
	if err != nil {
		return nil, err
	}
	return os.Open(path)
}

func rootedPath(root, relative string) (string, error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative || filepath.ToSlash(relative) != relative || relative == "." || relative == ".." || len(relative) >= 3 && relative[:3] == "../" {
		return "", errors.New("path must be a clean repository-relative path")
	}
	absoluteRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(filepath.Clean(absoluteRoot), filepath.FromSlash(relative)), nil
}

func absoluteRoot(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	return filepath.Clean(absolute), nil
}

func usageError() error {
	return errors.New("usage: releaseassets digest-context [-root .] -path directory | verify-spec [-root .] [-spec path] | verify-manifest [-release-root .] [-manifest path] | generate [-source-root .] -release-root directory [-spec path] -resolution path -out path")
}
