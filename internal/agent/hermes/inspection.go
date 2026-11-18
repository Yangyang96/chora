package hermes

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"

	"github.com/Yangyang96/chora/internal/execution"
	"github.com/Yangyang96/chora/internal/trustedhost"
)

// Inspection is static metadata, not a native version probe, authentication
// check, complete Python dependency closure, or a launch authorization.
type Inspection struct {
	Version        string
	Authentication string // absent or present_unverified; never logged_in
	Qualification  execution.QualificationReport
}

var required = []string{"event_frames", "tool_lifecycle", "terminal_result", "configuration_sources", "retry_control", "gateway_custody", "executable_closure", "authentication"}
var versionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+([+\-][0-9A-Za-z.\-]+)?$`)

// These files bind semantic review to the inspected implementation. They are
// intentionally NOT advertised as the complete native executable closure.
var semanticFiles = []string{"install-stamp.json", "pyproject.toml", "hermes_bootstrap.py", "hermes_cli/main.py", "hermes_cli/_parser.py", "hermes_cli/stream_json.py", "hermes_cli/env_loader.py", "hermes_cli/gateway_chat.py", "hermes_cli/gateway_chat_startup.py"}

func hash(value any) [32]byte { raw, _ := json.Marshal(value); return sha256.Sum256(raw) }
func canonicalDir(path string) bool {
	resolved, err := filepath.EvalSymlinks(path)
	info, statErr := os.Stat(path)
	return filepath.IsAbs(path) && filepath.Clean(path) == path && err == nil && statErr == nil && resolved == path && info.IsDir()
}
func readMetadata(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1024*1024 {
		return nil, ErrInstallation
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ErrInstallation
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(info, actual) {
		return nil, ErrIdentity
	}
	raw, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil || len(raw) > 1024*1024 {
		return nil, ErrInstallation
	}
	return raw, nil
}

func inspectIdentity(ctx context.Context, c Config) (Inspection, execution.QualificationIdentity, error) {
	if err := ctx.Err(); err != nil {
		return Inspection{}, execution.QualificationIdentity{}, err
	}
	if !canonicalDir(c.SourceRoot) || !canonicalDir(c.AuthHome) {
		return Inspection{}, execution.QualificationIdentity{}, ErrInstallation
	}
	var chain []trustedhost.ExecutableIdentity
	for _, path := range []string{c.Executable, c.NativeLauncher, c.Interpreter} {
		// Reject special files and unbounded inputs before opening an executable.
		// No subprocess timeout/cleanup is needed because inspection never spawns.
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 128*1024*1024 {
			return Inspection{}, execution.QualificationIdentity{}, ErrInstallation
		}
		if err := ctx.Err(); err != nil {
			return Inspection{}, execution.QualificationIdentity{}, err
		}
		i, err := trustedhost.InspectExecutable(path)
		if err != nil {
			return Inspection{}, execution.QualificationIdentity{}, ErrInstallation
		}
		chain = append(chain, i)
	}
	files := map[string][32]byte{}
	version := ""
	for _, name := range semanticFiles {
		path := filepath.Join(c.SourceRoot, name)
		resolved, err := filepath.EvalSymlinks(path)
		if err != nil || resolved != path {
			return Inspection{}, execution.QualificationIdentity{}, ErrInstallation
		}
		raw, err := readMetadata(path)
		if err != nil {
			return Inspection{}, execution.QualificationIdentity{}, err
		}
		files[name] = sha256.Sum256(raw)
		if name == "install-stamp.json" {
			var stamp struct {
				Version string `json:"displayVersion"`
			}
			if json.Unmarshal(raw, &stamp) != nil || len(stamp.Version) > 128 || !versionPattern.MatchString(stamp.Version) {
				return Inspection{}, execution.QualificationIdentity{}, ErrInstallation
			}
			version = stamp.Version
		}
	}
	// Lstat metadata only: existence is never proof of authentication or permission.
	auth := "absent"
	metadata := map[string]string{}
	for _, name := range []string{"auth.json", ".env", ".op.env", "config.yaml", "active_profile", ".container-mode", "skills", "plugins", "hooks", "mcp.json"} {
		info, err := os.Lstat(filepath.Join(c.AuthHome, name))
		if os.IsNotExist(err) {
			metadata[name] = "absent"
			continue
		}
		if err != nil {
			return Inspection{}, execution.QualificationIdentity{}, ErrInstallation
		}
		metadata[name] = info.Mode().String()
		if name == "auth.json" || name == ".env" {
			auth = "present_unverified"
		}
	}
	if err := ctx.Err(); err != nil {
		return Inspection{}, execution.QualificationIdentity{}, err
	}
	identity := execution.QualificationIdentity{
		RuntimeID: AdapterID, Version: version, ProtocolVersion: protocolVersion,
		SourceIdentity: hash(struct {
			Chain []trustedhost.ExecutableIdentity
			Root  string
			Files map[string][32]byte
		}{chain, c.SourceRoot, files}),
		ConfigurationIdentity: hash(struct {
			Config   Config
			Metadata map[string]string
		}{c, metadata}),
		SecurityIdentity: hash("hermes.static-only.v1:no-launch:no-auth-read:no-daemon:no-retry:no-native-resources"),
	}
	return Inspection{Version: version, Authentication: auth}, identity, nil
}

// Inspect performs no subprocess, write, daemon connection, credential read or
// model request. An unknown version remains pending until a trusted semantic
// review; no version whitelist or implicit range grants execution authority.
func Inspect(ctx context.Context, c Config, reviews *ProtocolReviews) (Inspection, error) {
	r, identity, err := inspectIdentity(ctx, c)
	if err != nil {
		return Inspection{}, err
	}
	evidence := []execution.CapabilityEvidence{}
	if reference, ok := reviews.lookup(identity); ok {
		for _, capability := range []string{"event_frames", "tool_lifecycle", "terminal_result"} {
			evidence = append(evidence, execution.CapabilityEvidence{Capability: capability, State: execution.QualificationVerified, Method: "signed_source_review_and_fixed_replay:not_native_execution", Reference: reference})
		}
	}
	for _, capability := range []string{"configuration_sources", "retry_control", "gateway_custody", "executable_closure"} {
		evidence = append(evidence, execution.CapabilityEvidence{Capability: capability, State: execution.QualificationPending, Method: "static_inspection_only", Reference: capability + "_unverifiable"})
	}
	evidence = append(evidence, execution.CapabilityEvidence{Capability: "authentication", State: execution.QualificationPending, Method: "filesystem_metadata_only", Reference: r.Authentication})
	var cache execution.QualificationCache
	r.Qualification, err = cache.Store(identity, required, evidence)
	return r, err
}
