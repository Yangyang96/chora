package pidistribution

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

const defaultVersionTimeout = 5 * time.Second

type LookPathFunc func(string) (string, error)

type Config struct {
	LookPath       LookPathFunc
	RunVersion     VersionRunner
	VersionTimeout time.Duration
	PrivateRoot    string
	PrivateAsset   *PrivateAsset
	RemoveTree     func(string) error

	// Platform fields are fixture seams. Empty means the running platform.
	PlatformOS   string
	PlatformArch string
}

type Resolver struct {
	lookPath       LookPathFunc
	runVersion     VersionRunner
	versionTimeout time.Duration
	privateRoot    string
	privateAsset   *PrivateAsset
	removeTree     func(string) error
	goos           string
	goarch         string
}

func NewResolver(config Config) (*Resolver, error) {
	if config.LookPath == nil {
		config.LookPath = exec.LookPath
	}
	if config.RunVersion == nil {
		config.RunVersion = defaultVersionRunner
	}
	if config.VersionTimeout == 0 {
		config.VersionTimeout = defaultVersionTimeout
	}
	if config.VersionTimeout < 0 || config.VersionTimeout > 30*time.Second {
		return nil, errors.New("Pi version timeout must be positive and no more than 30 seconds")
	}
	if config.PlatformOS == "" {
		config.PlatformOS = runtime.GOOS
	}
	if config.PlatformArch == "" {
		config.PlatformArch = runtime.GOARCH
	}
	if config.RemoveTree == nil {
		config.RemoveTree = os.RemoveAll
	}
	asset := config.PrivateAsset
	if asset != nil {
		copied := *asset
		copied.ManifestBytes = append([]byte(nil), asset.ManifestBytes...)
		asset = &copied
	}
	return &Resolver{
		lookPath: config.LookPath, runVersion: config.RunVersion, versionTimeout: config.VersionTimeout,
		privateRoot: config.PrivateRoot, privateAsset: asset, removeTree: config.RemoveTree,
		goos: config.PlatformOS, goarch: config.PlatformArch,
	}, nil
}

// InspectCompatiblePATH performs only PATH discovery, immutable closure
// inspection, and the version probe. It never installs or selects private Pi.
func (resolver *Resolver) InspectCompatiblePATH(ctx context.Context) (Selection, error) {
	if resolver == nil {
		return Selection{}, ErrNoUsablePi
	}
	return resolver.resolvePATH(ctx)
}

// SelectExisting is the service-start selection boundary. It prefers a
// compatible PATH Pi and falls back only to an already-installed exact private
// closure; it never installs software.
func (resolver *Resolver) SelectExisting(ctx context.Context) (Selection, error) {
	if resolver == nil {
		return Selection{}, ErrNoUsablePi
	}
	if selection, err := resolver.resolvePATH(ctx); err == nil {
		return selection, nil
	}
	selection, present, err := resolver.InspectPrivate(ctx)
	if err != nil || !present {
		return Selection{}, errors.Join(ErrNoUsablePi, err)
	}
	return selection, nil
}

// Resolve checks PATH first. Every PATH rejection is remembered for diagnosis,
// but private fallback is attempted only with an authenticated manifest pin.
func (resolver *Resolver) Resolve(ctx context.Context) (Selection, error) {
	pathSelection, pathErr := resolver.resolvePATH(ctx)
	if pathErr == nil {
		return pathSelection, nil
	}
	privateSelection, privateErr := resolver.resolvePrivate(ctx)
	if privateErr == nil {
		return privateSelection, nil
	}
	return Selection{}, fmt.Errorf("%w: PATH: %w; private: %w", ErrNoUsablePi, pathErr, privateErr)
}

func Resolve(ctx context.Context, config Config) (Selection, error) {
	resolver, err := NewResolver(config)
	if err != nil {
		return Selection{}, err
	}
	return resolver.Resolve(ctx)
}

// ValidateSelection revalidates an already-bound selection with the production
// direct version runner. It never performs PATH discovery, installation, or
// fallback. Call Resolver.Revalidate when an injected runner/timeout is needed.
func ValidateSelection(ctx context.Context, selection Selection) error {
	resolver, err := NewResolver(Config{})
	if err != nil {
		return err
	}
	return resolver.Revalidate(ctx, selection)
}

// Revalidate proves that the selected path, resolved executable, package root,
// executable bytes, closure bytes/metadata, and exact version remain unchanged.
// It is suitable for an atomic pre-spawn prepare gate and has no mutation path.
func (resolver *Resolver) Revalidate(ctx context.Context, selection Selection) error {
	restored, err := RestoreSelection(selection.Record())
	if err != nil || restored.Record() != selection.Record() {
		return ErrSelectionDrift
	}
	switch selection.Kind() {
	case SelectionPATH:
		before, err := inspectPATHPackage(selection.Path())
		if err != nil || !closureMatchesSelection(before, selection) {
			return fmt.Errorf("%w: PATH selection no longer matches", ErrSelectionDrift)
		}
		probeContext, cancel := context.WithTimeout(ctx, resolver.versionTimeout)
		err = verifyVersion(probeContext, resolver.runVersion, selection.ResolvedPath())
		cancel()
		if err != nil {
			return err
		}
		after, err := inspectPATHPackage(selection.Path())
		if err != nil || !sameClosure(before, after) || !closureMatchesSelection(after, selection) {
			return fmt.Errorf("%w: PATH selection changed during revalidation", ErrSelectionDrift)
		}
		return nil
	case SelectionPrivate:
		return resolver.revalidatePrivate(ctx, selection)
	default:
		return ErrSelectionDrift
	}
}

func (resolver *Resolver) revalidatePrivate(ctx context.Context, selection Selection) error {
	rootInfo, err := os.Lstat(selection.PackageRoot())
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode().Perm() != 0o700 || rootInfo.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%w: private root identity changed", ErrSelectionDrift)
	}
	if err := verifyCompleteForSelection(selection.PackageRoot(), selection.ClosureSHA256()); err != nil {
		return fmt.Errorf("%w: %v", ErrSelectionDrift, err)
	}
	before, err := inspectClosure(selection.PackageRoot(), nil, ".complete")
	if err != nil || !closureMatchesSelection(before, selection) {
		return fmt.Errorf("%w: private selection no longer matches", ErrSelectionDrift)
	}
	if err := verifyPackageIdentityAtRoot(selection.PackageRoot()); err != nil {
		return fmt.Errorf("%w: %v", ErrSelectionDrift, err)
	}
	probeContext, cancel := context.WithTimeout(ctx, resolver.versionTimeout)
	err = verifyVersion(probeContext, resolver.runVersion, selection.ResolvedPath())
	cancel()
	if err != nil {
		return err
	}
	after, err := inspectClosure(selection.PackageRoot(), nil, ".complete")
	if err != nil || before.digest != after.digest || !closureMatchesSelection(after, selection) {
		return fmt.Errorf("%w: private selection changed during revalidation", ErrSelectionDrift)
	}
	if err := verifyCompleteForSelection(selection.PackageRoot(), selection.ClosureSHA256()); err != nil {
		return fmt.Errorf("%w: private completion identity changed", ErrSelectionDrift)
	}
	return nil
}

func closureMatchesSelection(observed closure, selection Selection) bool {
	if observed.root != selection.PackageRoot() || observed.digest != selection.ClosureSHA256() {
		return false
	}
	relative, err := filepath.Rel(observed.root, selection.ResolvedPath())
	if err != nil || !validRelativePath(filepath.ToSlash(relative)) {
		return false
	}
	if observed.executable.path != "" && (observed.executable.path != selection.Path() ||
		observed.executable.resolved != selection.ResolvedPath() || observed.executable.digest != selection.ExecutableSHA256()) {
		return false
	}
	for _, file := range observed.files {
		if file.Path == filepath.ToSlash(relative) && file.Mode&0o100 != 0 {
			digest, decodeErr := hex.DecodeString(file.SHA256)
			executableDigest := selection.ExecutableSHA256()
			return decodeErr == nil && bytes.Equal(digest, executableDigest[:])
		}
	}
	return false
}

func (resolver *Resolver) resolvePATH(ctx context.Context) (Selection, error) {
	pathValue, err := resolver.lookPath(ExecutableName)
	if err != nil {
		return Selection{}, fmt.Errorf("look up %s: %w", ExecutableName, err)
	}
	before, err := inspectPATHPackage(pathValue)
	if err != nil {
		return Selection{}, err
	}
	probeContext, cancel := context.WithTimeout(ctx, resolver.versionTimeout)
	err = verifyVersion(probeContext, resolver.runVersion, before.executable.resolved)
	cancel()
	if err != nil {
		return Selection{}, err
	}
	after, err := inspectPATHPackage(pathValue)
	if err != nil || !sameClosure(before, after) {
		return Selection{}, fmt.Errorf("%w: PATH package changed during validation", ErrSelectionDrift)
	}
	return newSelection(SelectionRecord{
		Kind: SelectionPATH, Path: before.executable.path, ResolvedPath: before.executable.resolved,
		PackageRoot: before.root, Version: SupportedVersion,
		ExecutableSHA256: before.executable.digest, ClosureSHA256: before.digest,
	})
}

func (resolver *Resolver) resolvePrivate(ctx context.Context) (Selection, error) {
	manifest, err := authenticateManifest(resolver.privateAsset, resolver.goos, resolver.goarch)
	if err != nil {
		return Selection{}, err
	}
	if !canonicalAbsolute(resolver.privateRoot) {
		return Selection{}, fmt.Errorf("private root must be a canonical absolute path")
	}
	source, err := inspectClosure(resolver.privateAsset.SourceRoot, manifest.document.Files, "")
	if err != nil || source.digest != manifest.closure {
		return Selection{}, fmt.Errorf("%w: source does not match authenticated manifest", ErrUnsafeClosure)
	}
	if err := verifyPackageIdentityAtRoot(source.root); err != nil {
		return Selection{}, err
	}
	selection, err := resolver.install(ctx, manifest)
	if err != nil {
		return Selection{}, err
	}
	return selection, nil
}

func verifyPackageIdentityAtRoot(root string) error {
	identity, err := readPackageIdentity(normalizedExecutable(root, "package.json"))
	if err != nil || identity.Name != ExpectedPackageName || identity.Version != SupportedVersion {
		return errors.New("private asset package.json identity mismatch")
	}
	return nil
}
