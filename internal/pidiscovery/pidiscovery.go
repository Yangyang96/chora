// Package pidiscovery resolves and proves a compatible user-installed Pi for
// the M2-S1 Local Connected execution choice. Discovery is PATH-only and
// read-only: it never installs, copies, bundles, or updates Pi, and it never
// reads credential values.
package pidiscovery

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// DiscoveryState is the user-visible readiness of an installed Pi.
type DiscoveryState string

const (
	StateReady               DiscoveryState = "ready"
	StateMissing             DiscoveryState = "missing"
	StateNotExecutable       DiscoveryState = "not_executable"
	StateIncompatibleVersion DiscoveryState = "incompatible_version"
	StateUnconfigured        DiscoveryState = "unconfigured"
	StateDrifted             DiscoveryState = "drifted"
)

// MinimumVersion is the frozen compatibility floor from the Pi Local Connected
// compatibility contract.
const MinimumVersion = "0.84.2"

// Result is the digest of one discovery probe. Providers are listed by name
// only; no credential value is ever read or stored.
type Result struct {
	State             DiscoveryState
	ExecutablePath    string
	Version           string
	Reason            string
	ExecutableSHA256  [32]byte
	ReadyProviders    []string
	NotReadyProviders []string
}

// Options lets tests and embedding callers override the ambient environment.
type Options struct {
	LookPath func(string) (string, error)
	PiHome   string
}

// Discover resolves pi from PATH and proves version, executable identity, and
// provider readiness. A non-ready outcome is a State on the returned Result,
// not an error; error is reserved for a broken context.
func Discover(ctx context.Context, options Options) (Result, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	lookPath := options.LookPath
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	found, err := lookPath("pi")
	if err != nil {
		return Result{State: StateMissing, Reason: "pi was not found on PATH"}, nil
	}
	resolved, err := filepath.EvalSymlinks(found)
	if err != nil || resolved == "" {
		return Result{State: StateNotExecutable, Reason: "pi on PATH could not be resolved"}, nil
	}
	if !filepath.IsAbs(resolved) {
		resolved = filepath.Clean(resolved)
	}
	identity, err := inspectExecutable(resolved)
	if err != nil {
		return Result{State: StateNotExecutable, ExecutablePath: resolved, Reason: err.Error()}, nil
	}
	version, err := probeVersion(ctx, resolved)
	if err != nil {
		return Result{State: StateNotExecutable, ExecutablePath: resolved, Reason: versionError(err), ExecutableSHA256: identity}, nil
	}
	if semverLess(version, MinimumVersion) {
		return Result{State: StateIncompatibleVersion, ExecutablePath: resolved, Version: version,
			Reason: fmt.Sprintf("Pi version %s is below the required %s", version, MinimumVersion), ExecutableSHA256: identity}, nil
	}
	ready, notReady, err := probeReadiness(ctx, resolved, options.PiHome)
	if err != nil {
		ready, notReady = nil, nil
	}
	state := StateReady
	reason := ""
	if len(ready) == 0 {
		state = StateUnconfigured
		reason = "no configured Pi provider answered ready; start Pi and use `/login` to configure a provider, then `/model` to choose a model"
	}
	return Result{
		State: state, ExecutablePath: resolved, Version: version, Reason: reason,
		ExecutableSHA256: identity, ReadyProviders: ready, NotReadyProviders: notReady,
	}, nil
}

// Revalidate re-probes the executable at the prior result's path and flags
// drift when its identity or version changed.
func Revalidate(ctx context.Context, prior Result) (Result, error) {
	if prior.State == StateMissing || prior.State == StateDrifted || prior.ExecutablePath == "" {
		return Result{State: StateMissing, Reason: "Pi has no prior discovered identity"}, nil
	}
	identity, err := inspectExecutable(prior.ExecutablePath)
	if err != nil {
		return Result{State: StateDrifted, ExecutablePath: prior.ExecutablePath, Reason: "Pi executable is no longer available"}, nil
	}
	version, err := probeVersion(ctx, prior.ExecutablePath)
	if err != nil {
		return Result{State: StateDrifted, ExecutablePath: prior.ExecutablePath, Reason: versionError(err)}, nil
	}
	if semverLess(version, MinimumVersion) {
		return Result{State: StateIncompatibleVersion, ExecutablePath: prior.ExecutablePath, Version: version,
			Reason: fmt.Sprintf("Pi version %s is below the required %s", version, MinimumVersion)}, nil
	}
	if identity != prior.ExecutableSHA256 || version != prior.Version {
		return Result{State: StateDrifted, ExecutablePath: prior.ExecutablePath, Version: version,
			Reason: "Pi executable identity or version changed since discovery", ExecutableSHA256: identity}, nil
	}
	return prior, nil
}

func inspectExecutable(path string) ([32]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return [32]byte{}, fmt.Errorf("inspect Pi executable: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return [32]byte{}, errors.New("Pi path is not an executable regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return [32]byte{}, fmt.Errorf("read Pi executable: %w", err)
	}
	return sha256.Sum256(data), nil
}

func probeVersion(ctx context.Context, executable string) (string, error) {
	output, err := runPi(ctx, executable, "--version")
	if err != nil {
		return "", err
	}
	return parseVersion(strings.TrimSpace(string(output)))
}

func parseVersion(raw string) (string, error) {
	match := semverPattern.FindString(raw)
	if match == "" {
		return "", fmt.Errorf("Pi --version output %q is not a semantic version", raw)
	}
	return match, nil
}

var semverPattern = regexp.MustCompile(`\d+\.\d+\.\d+`)

func versionError(err error) string {
	message := err.Error()
	if strings.Contains(strings.ToLower(message), "node") || strings.Contains(message, "exec format") {
		return "Pi could not start: its Node.js runtime prerequisite is unmet; install Node.js >= 22.19.0"
	}
	return fmt.Sprintf("Pi could not report its version: %s", message)
}

// probeReadiness probes each configured provider with the deterministic,
// non-mutating `pi auth check --provider <p> --no-refresh` form.
func probeReadiness(ctx context.Context, executable, piHome string) ([]string, []string, error) {
	providers, err := configuredProviders(piHome)
	if err != nil {
		return nil, nil, err
	}
	var ready, notReady []string
	for _, provider := range providers {
		output, err := runPi(ctx, executable, "auth", "check", "--provider", provider, "--no-refresh")
		if err != nil {
			notReady = append(notReady, provider)
			continue
		}
		if strings.TrimSpace(string(output)) == "ready" {
			ready = append(ready, provider)
		} else {
			notReady = append(notReady, provider)
		}
	}
	sort.Strings(ready)
	sort.Strings(notReady)
	return ready, notReady, nil
}

func configuredProviders(piHome string) ([]string, error) {
	if piHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		piHome = filepath.Join(home, ".pi", "agent")
	}
	providers := map[string]struct{}{}
	if data, err := os.ReadFile(filepath.Join(piHome, "settings.json")); err == nil {
		var settings struct {
			DefaultProvider string `json:"defaultProvider"`
		}
		if json.Unmarshal(data, &settings) == nil && strings.TrimSpace(settings.DefaultProvider) != "" {
			providers[strings.TrimSpace(settings.DefaultProvider)] = struct{}{}
		}
	}
	if data, err := os.ReadFile(filepath.Join(piHome, "models-store.json")); err == nil {
		var store map[string]json.RawMessage
		if json.Unmarshal(data, &store) == nil {
			for name := range store {
				providers[name] = struct{}{}
			}
		}
	}
	names := make([]string, 0, len(providers))
	for name := range providers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

func runPi(ctx context.Context, executable string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, args...)
	var stderr strings.Builder
	command.Stderr = &stderr
	output, err := command.Output()
	if err != nil {
		if message := strings.TrimSpace(stderr.String()); message != "" {
			return nil, fmt.Errorf("%s", message)
		}
		return nil, err
	}
	return output, nil
}

// semverLess reports whether left sorts strictly before right in dotted
// numeric order. Both inputs are assumed to match semverPattern.
func semverLess(left, right string) bool {
	lparts := splitNumeric(left)
	rparts := splitNumeric(right)
	for i := 0; i < len(lparts) && i < len(rparts); i++ {
		if lparts[i] != rparts[i] {
			return lparts[i] < rparts[i]
		}
	}
	return len(lparts) < len(rparts)
}

func splitNumeric(value string) []int {
	fields := strings.Split(value, ".")
	parts := make([]int, len(fields))
	for i, field := range fields {
		number, _ := strconv.Atoi(field)
		parts[i] = number
	}
	return parts
}
