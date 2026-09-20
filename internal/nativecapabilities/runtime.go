package nativecapabilities

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

//go:embed discovery.mjs
var discoverySource []byte

//go:embed runtime.mjs
var runtimeSource []byte

type Capability struct {
	Name      string `json:"name"`
	Path      string `json:"path,omitempty"`
	Source    string `json:"source"`
	Enabled   bool   `json:"enabled"`
	Status    string `json:"status"`
	ToolCount int    `json:"toolCount,omitempty"`
}
type Bridge struct {
	Status  string `json:"status"`
	Path    string `json:"path"`
	Version string `json:"version"`
}
type Inventory struct {
	ConfigurationDigest string       `json:"configurationDigest,omitempty"`
	ResourceDigest      string       `json:"resourceDigest,omitempty"`
	Skills              []Capability `json:"skills"`
	Servers             []Capability `json:"servers"`
	Bridge              Bridge       `json:"bridge"`
	MissingPackages     int          `json:"missingPackages"`
	Diagnostics         []struct {
		Path   string `json:"path"`
		Status string `json:"status"`
	} `json:"diagnostics"`
	ExtensionPaths []string `json:"extensionPaths,omitempty"`
}
type Manifest struct {
	ProjectID string    `json:"projectID"`
	Config    Config    `json:"config"`
	PiRoot    string    `json:"piRoot"`
	Cwd       string    `json:"cwd"`
	AgentDir  string    `json:"agentDir"`
	Inventory Inventory `json:"inventory"`
	Arguments []string  `json:"arguments"`
}

func packageRoot(executable string) (string, error) {
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return "", err
	}
	for root, depth := filepath.Dir(resolved), 0; depth < 6; root, depth = filepath.Dir(root), depth+1 {
		var metadata struct {
			Name    string
			Version string
		}
		raw, e := os.ReadFile(filepath.Join(root, "package.json"))
		if e == nil && json.Unmarshal(raw, &metadata) == nil && metadata.Name == "@earendil-works/pi-coding-agent" && metadata.Version == "0.85.1" {
			return root, nil
		}
	}
	return "", errors.New("capability management requires the qualified Pi 0.85.1 package")
}

// Inspect does not import extension code or connect to MCP servers.
func Inspect(ctx context.Context, executable, agentDir, cwd string, config Config) (Inventory, error) {
	root, err := packageRoot(executable)
	if err != nil {
		return Inventory{}, err
	}
	input := Manifest{Config: config, PiRoot: root, Cwd: cwd, AgentDir: agentDir}
	raw, err := json.Marshal(input)
	if err != nil {
		return Inventory{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "node", "--input-type=module", "-e", string(discoverySource), "--discover")
	// -e consumes the script without a filename, so argv[1] is the first argument.
	command.Args = append(command.Args[:len(command.Args)-1], "chora-discovery", "--discover")
	command.Stdin = bytes.NewReader(raw)
	command.Dir = cwd
	command.Env = append(os.Environ(), "PI_OFFLINE=1", "PI_CODING_AGENT_DIR="+agentDir)
	var output limitedOutput
	command.Stdout = &output
	if err = command.Run(); err != nil {
		return Inventory{}, errors.New("native capability discovery failed; inspect the selected Pi installation and configuration")
	}
	var inventory Inventory
	if err = json.Unmarshal(output.Bytes(), &inventory); err != nil {
		return Inventory{}, errors.New("invalid native capability discovery response")
	}
	return inventory, nil
}

type limitedOutput struct{ buffer bytes.Buffer }

func (output *limitedOutput) Bytes() []byte { return output.buffer.Bytes() }

func (output *limitedOutput) Write(data []byte) (int, error) {
	if output.buffer.Len()+len(data) > 1<<20 {
		return 0, errors.New("capability output exceeds limit")
	}
	return output.buffer.Write(data)
}

// Prepare creates a private immutable configuration reference for an execution.
// An explicit resume reuses its session owner's original Project configuration.
func Prepare(ctx context.Context, root, projectID, executable, agentDir, cwd string, attemptID, ownerID domain.AttemptID, config Config) ([]string, map[string]string, error) {
	if config.Version == 0 && !ownerID.Valid() {
		return nil, nil, nil
	}
	if _, err := domain.ParseProjectID(projectID); err != nil {
		return nil, nil, err
	}
	if !attemptID.Valid() {
		return nil, nil, errors.New("invalid capability attempt")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, nil, err
	}
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil || canonical != root {
		return nil, nil, errors.New("capability directory is not canonical")
	}
	sourceHash := sha256.Sum256(runtimeSource)
	extension := filepath.Join(root, fmt.Sprintf("runtime-%x.mjs", sourceHash))
	if err = writeImmutable(extension, runtimeSource); err != nil {
		return nil, nil, err
	}
	identity := attemptID
	if ownerID.Valid() {
		identity = ownerID
	}
	path := filepath.Join(root, identity.String()+".json")
	var manifest Manifest
	if ownerID.Valid() {
		raw, e := readPrivate(path)
		if os.IsNotExist(e) && config.Version == 0 {
			return nil, nil, nil
		}
		if e != nil {
			return nil, nil, errors.New("original capability configuration unavailable for resume")
		}
		if json.Unmarshal(raw, &manifest) != nil || manifest.ProjectID != projectID || manifest.Cwd != cwd {
			return nil, nil, errors.New("resume capability configuration mismatch")
		}
		current, e := Inspect(ctx, executable, agentDir, cwd, manifest.Config)
		if e != nil || current.ResourceDigest != manifest.Inventory.ResourceDigest {
			return nil, nil, errors.New("native capability resources changed; start a fresh execution instead of resuming this session")
		}
	} else {
		piRoot, e := packageRoot(executable)
		if e != nil {
			return nil, nil, e
		}
		inventory, e := Inspect(ctx, executable, agentDir, cwd, config)
		if e != nil {
			return nil, nil, e
		}
		manifest = Manifest{ProjectID: projectID, Config: config, PiRoot: piRoot, AgentDir: agentDir, Cwd: cwd, Inventory: inventory}
		manifest.Arguments = []string{"--offline", "--no-skills", "--no-extensions"}
		for _, skill := range inventory.Skills {
			if skill.Enabled && skill.Status != "unavailable" {
				manifest.Arguments = append(manifest.Arguments, "--skill", skill.Path)
			}
		}
		for _, extensionPath := range inventory.ExtensionPaths {
			manifest.Arguments = append(manifest.Arguments, "--extension", extensionPath)
		}
		manifest.Arguments = append(manifest.Arguments, "--extension", extension)
		raw, e := json.Marshal(manifest)
		if e != nil {
			return nil, nil, e
		}
		if e = writeImmutable(path, raw); e != nil {
			return nil, nil, e
		}
	}
	raw, err := readPrivate(path)
	if err != nil {
		return nil, nil, err
	}
	sum := sha256.Sum256(raw)
	environment := map[string]string{"CHORA_NATIVE_CAPABILITIES_MANIFEST": path, "CHORA_NATIVE_CAPABILITIES_SHA256": hex.EncodeToString(sum[:]), "CHORA_NATIVE_CAPABILITIES_STATUS": filepath.Join(root, attemptID.String()+".status.json")}
	return manifest.Arguments, environment, nil
}
func writeImmutable(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		previous, e := readPrivate(path)
		if e != nil {
			return e
		}
		if !bytes.Equal(previous, body) {
			return errors.New("capability configuration changed")
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = file.Write(body)
	return errors.Join(err, file.Close())
}
func readPrivate(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return nil, errors.New("unsafe capability file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return io.ReadAll(io.LimitReader(file, (1<<20)+1))
}

// StripValidatedArguments permits only the exact suffix prepared for this child.
// The supervisor still validates the original native Pi argv and observer.
func StripValidatedArguments(root string, invocation execution.Invocation) ([]string, error) {
	env := invocation.Environment()
	path := env["CHORA_NATIVE_CAPABILITIES_MANIFEST"]
	if path == "" {
		return invocation.Arguments(), nil
	}
	if filepath.Dir(path) != root || !strings.HasSuffix(path, ".json") {
		return nil, errors.New("capability manifest escaped runtime root")
	}
	raw, err := readPrivate(path)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) != env["CHORA_NATIVE_CAPABILITIES_SHA256"] {
		return nil, errors.New("capability manifest identity changed")
	}
	var manifest Manifest
	if json.Unmarshal(raw, &manifest) != nil || manifest.Cwd != invocation.WorkingRoot() {
		return nil, errors.New("capability manifest context mismatch")
	}
	sourceHash := sha256.Sum256(runtimeSource)
	expected := filepath.Join(root, fmt.Sprintf("runtime-%x.mjs", sourceHash))
	source, err := readPrivate(expected)
	if err != nil || !bytes.Equal(source, runtimeSource) {
		return nil, errors.New("capability observer identity changed")
	}
	args := invocation.Arguments()
	count := len(manifest.Arguments)
	if count == 0 || len(args) < count || !reflect.DeepEqual(args[len(args)-count:], manifest.Arguments) {
		return nil, errors.New("capability arguments differ from prepared configuration")
	}
	status := env["CHORA_NATIVE_CAPABILITIES_STATUS"]
	if filepath.Dir(status) != root || filepath.Base(status) != env["CHORA_ATTEMPT_ID"]+".status.json" {
		return nil, errors.New("capability status identity mismatch")
	}
	return args[:len(args)-count], nil
}
