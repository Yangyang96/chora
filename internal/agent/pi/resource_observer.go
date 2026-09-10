package pi

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
	"path/filepath"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

//go:embed resource_check_observer.mjs
var resourceObserverSource []byte

// ResourceObserverBinding adds observation to native Pi; it never suppresses
// user extensions, skills, context, permissions, or provider configuration.
type ResourceObserverBinding struct {
	ExtensionPath string
	HelperPath    string
	HelperSHA256  string
}

func ResourceObserverSHA256() string {
	sum := sha256.Sum256(resourceObserverSource)
	return hex.EncodeToString(sum[:])
}
func observerFileDigest(path string) (string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", errors.New("observer path is not canonical")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return "", errors.New("observer path is unavailable or symlinked")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 512<<20 {
		return "", errors.New("observer file is not bounded regular content")
	}
	hash := sha256.New()
	if _, err = io.Copy(hash, io.LimitReader(file, (512<<20)+1)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}
func InstallResourceObserver(root, helper string) (ResourceObserverBinding, error) {
	if err := os.MkdirAll(root, 0700); err != nil {
		return ResourceObserverBinding{}, err
	}
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != root {
		return ResourceObserverBinding{}, errors.New("observer directory is not canonical")
	}
	info, err := os.Stat(root)
	if err != nil || !info.IsDir() || info.Mode().Perm()&0077 != 0 {
		return ResourceObserverBinding{}, errors.New("observer directory is not private")
	}
	binding := ResourceObserverBinding{ExtensionPath: filepath.Join(root, "resource_check_observer-"+ResourceObserverSHA256()+".mjs"), HelperPath: helper}
	if err := writeObserverImmutable(binding.ExtensionPath, resourceObserverSource); err != nil {
		return binding, err
	}
	binding.HelperSHA256, err = observerFileDigest(helper)
	if err != nil {
		return binding, err
	}
	return binding, binding.Validate(context.Background())
}
func writeObserverImmutable(path string, body []byte) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if errors.Is(err, os.ErrExist) {
		info, e := os.Lstat(path)
		if e != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
			return errors.New("observer artifact is unsafe")
		}
		existing, e := os.ReadFile(path)
		if e != nil || !bytes.Equal(existing, body) {
			return errors.New("observer artifact identity changed")
		}
		return nil
	}
	if err != nil {
		return err
	}
	_, err = file.Write(body)
	closeErr := file.Close()
	return errors.Join(err, closeErr)
}
func (binding ResourceObserverBinding) Validate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	source, err := observerFileDigest(binding.ExtensionPath)
	if err != nil || source != ResourceObserverSHA256() {
		return errors.New("resource observer source identity changed")
	}
	helper, err := observerFileDigest(binding.HelperPath)
	if err != nil || helper != binding.HelperSHA256 {
		return errors.New("resource fingerprint helper identity changed")
	}
	return nil
}
func observerResources(contract []byte) (json.RawMessage, error) {
	var doc struct {
		Task struct {
			Resources json.RawMessage `json:"resources"`
		} `json:"task"`
	}
	if err := json.Unmarshal(contract, &doc); err != nil {
		return nil, err
	}
	if len(doc.Task.Resources) == 0 || bytes.Equal(doc.Task.Resources, []byte("null")) {
		return nil, nil
	}
	var resources []json.RawMessage
	if err := json.Unmarshal(doc.Task.Resources, &resources); err != nil || len(resources) > domain.TaskRepositoryLimit {
		return nil, errors.New("invalid observer resource vector")
	}
	if len(resources) == 0 {
		return nil, nil
	}
	return doc.Task.Resources, nil
}
func (adapter *Adapter) observerFingerprint(ctx context.Context, source selectedSource, contract []byte) (execution.RuntimeFingerprint, error) {
	base := fingerprintForSource(source)
	resources, err := observerResources(contract)
	if err != nil {
		return execution.RuntimeFingerprint{}, err
	}
	if adapter.config.ResourceObserver == nil || len(resources) == 0 || !source.pathPi {
		return base, nil
	}
	binding := *adapter.config.ResourceObserver
	if err := binding.Validate(ctx); err != nil {
		return execution.RuntimeFingerprint{}, err
	}
	canonical := fmt.Sprintf("chora.pi-observer-runtime.v1\x00%x\x00%s\x00%s\x00%s\x00%s", base.Digest, binding.ExtensionPath, ResourceObserverSHA256(), binding.HelperPath, binding.HelperSHA256)
	return execution.RuntimeFingerprint{Digest: sha256.Sum256([]byte(canonical)), Version: base.Version}, nil
}
func (adapter *Adapter) FingerprintForContract(ctx context.Context, binding domain.AgentExecutionProfileBinding, contract []byte) (execution.RuntimeFingerprint, error) {
	source, err := adapter.sourceForBinding(binding)
	if err != nil {
		return execution.RuntimeFingerprint{}, err
	}
	if err := adapter.validateSelectedSource(ctx, source); err != nil {
		return execution.RuntimeFingerprint{}, err
	}
	return adapter.observerFingerprint(ctx, source, contract)
}
func (adapter *Adapter) configureResourceObserver(source selectedSource, attemptID domain.AttemptID, root string, contract []byte, arguments []string, environment map[string]string) ([]string, error) {
	resources, err := observerResources(contract)
	if err != nil {
		return nil, err
	}
	if adapter.config.ResourceObserver == nil || len(resources) == 0 || !source.pathPi {
		return arguments, nil
	}
	binding := *adapter.config.ResourceObserver
	if err := binding.Validate(context.Background()); err != nil {
		return nil, err
	}
	body, err := json.Marshal(struct {
		Schema    string          `json:"schema"`
		AttemptID string          `json:"attemptId"`
		TaskRoot  string          `json:"taskRoot"`
		Resources json.RawMessage `json:"resources"`
	}{"chora.resource-observer-config.v1", attemptID.String(), root, resources})
	if err != nil || len(body) > domain.RepositoryMetadataBytes {
		return nil, errors.New("observer configuration exceeds limit")
	}
	configRoot := filepath.Dir(binding.ExtensionPath)
	configPath := filepath.Join(configRoot, attemptID.String()+".json")
	if err := writeObserverImmutable(configPath, body); err != nil {
		return nil, err
	}
	sum := sha256.Sum256(body)
	environment["CHORA_CHECK_OBSERVER_CONFIG"] = configPath
	environment["CHORA_CHECK_OBSERVER_CONFIG_SHA256"] = hex.EncodeToString(sum[:])
	environment["CHORA_CHECK_OBSERVER_HELPER"] = binding.HelperPath
	environment["CHORA_CHECK_OBSERVER_HELPER_SHA256"] = binding.HelperSHA256
	fingerprint, err := adapter.observerFingerprint(context.Background(), source, contract)
	if err != nil {
		return nil, err
	}
	environment["CHORA_CHECK_OBSERVER_RUNTIME_FINGERPRINT"] = hex.EncodeToString(fingerprint.Digest[:])
	environment["CHORA_CHECK_OBSERVER_SHA256"] = ResourceObserverSHA256()
	// The optional exact observer pair precedes the existing native suffix.
	args := append([]string{}, arguments[:2]...)
	args = append(args, "--extension", binding.ExtensionPath)
	args = append(args, arguments[2:]...)
	return args, nil
}
