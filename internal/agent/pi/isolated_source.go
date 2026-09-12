package pi

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/execution"
)

const IsolatedPiVersion = "0.85.1"
const IsolatedModelProvider = "deepseek"
const IsolatedModelID = "deepseek-v4-flash"
const IsolatedWorkspaceRoot = "/workspace/repository"
const IsolatedObserverPath = "/opt/chora/resource_check_observer.mjs"
const IsolatedHelperPath = "/usr/local/bin/chora"

type IsolatedSourceParams struct {
	ImageID      string `json:"imageId"`
	HelperSHA256 string `json:"helperSha256"`
	PolicySHA256 string `json:"policySha256"`
}

// IsolatedSource binds public image bytes and the in-container check observer.
// Docker availability and effective policy are separately verified by its supervisor.
type IsolatedSource struct {
	params   IsolatedSourceParams
	identity [32]byte
}

func NewIsolatedSource(p IsolatedSourceParams) (IsolatedSource, error) {
	if !validSHA256Identity(p.ImageID) || !validSHA256Identity("sha256:"+p.HelperSHA256) || !validSHA256Identity("sha256:"+p.PolicySHA256) {
		return IsolatedSource{}, errors.New("incomplete public isolated Runtime identity")
	}
	b, _ := json.Marshal(p)
	return IsolatedSource{p, sha256.Sum256(append([]byte("chora.public-pi.v1\x00"+IsolatedPiVersion+"\x00"+IsolatedModelProvider+"\x00"+IsolatedModelID+"\x00"+ResourceObserverSHA256()+"\x00"), b...))}, nil
}
func (s IsolatedSource) Configured() bool             { return s.identity != ([32]byte{}) }
func (s IsolatedSource) ImageID() string              { return s.params.ImageID }
func (s IsolatedSource) PolicySHA256() string         { return s.params.PolicySHA256 }
func (s IsolatedSource) HelperSHA256() string         { return s.params.HelperSHA256 }
func (s IsolatedSource) SourceIdentity() [32]byte     { return s.identity }
func (s IsolatedSource) Record() IsolatedSourceParams { return s.params }
func (s IsolatedSource) valid() bool {
	restored, err := NewIsolatedSource(s.params)
	return err == nil && restored == s
}
func IsolatedRPCArguments() []string {
	return []string{"--mode", "rpc", "--no-session", "--no-extensions", "--no-skills", "--no-prompt-templates", "--no-themes", "--no-context-files", "--provider", IsolatedModelProvider, "--model", IsolatedModelID, "--extension", IsolatedObserverPath}
}
func (s IsolatedSource) Fingerprint() execution.RuntimeFingerprint {
	return fingerprintForSource(selectedSource{identity: s.identity, version: IsolatedPiVersion})
}
func IsolatedObserverConfig(attemptID domain.AttemptID, contract []byte) ([]byte, error) {
	resources, err := observerResources(contract)
	if err != nil {
		return nil, err
	}
	if len(resources) == 0 {
		resources = json.RawMessage("[]")
	}
	return json.Marshal(struct {
		Schema           string          `json:"schema"`
		AttemptID        string          `json:"attemptId"`
		TaskRoot         string          `json:"taskRoot"`
		Resources        json.RawMessage `json:"resources"`
		RepositoryLayout string          `json:"repositoryLayout"`
	}{"chora.resource-observer-config.v1", attemptID.String(), IsolatedWorkspaceRoot, resources, "isolated_copy"})
}
func (a *Adapter) configureIsolatedObserver(attemptID domain.AttemptID, contract []byte, env map[string]string) error {
	body, err := IsolatedObserverConfig(attemptID, contract)
	if err != nil {
		return err
	}
	s := a.config.IsolatedSource
	env["CHORA_POLICY_DIGEST"] = s.PolicySHA256()
	env["CHORA_ATTEMPT_IMAGE_ID"] = s.ImageID()
	env["CHORA_BOUNDARY_IMAGE_ID"] = ""
	env["CHORA_CAPABILITY_BUNDLE_ID"] = "chora.public-pi-observer.v1"
	env["CHORA_CAPABILITY_BUNDLE_SHA256"] = ResourceObserverSHA256()
	env["CHORA_CHECK_OBSERVER_CONFIG"] = "/input/context/observer.json"
	env["CHORA_CHECK_OBSERVER_CONFIG_SHA256"] = digest(body)
	env["CHORA_CHECK_OBSERVER_HELPER"] = IsolatedHelperPath
	env["CHORA_CHECK_OBSERVER_HELPER_SHA256"] = s.HelperSHA256()
	fp := s.Fingerprint()
	env["CHORA_CHECK_OBSERVER_RUNTIME_FINGERPRINT"] = hex.EncodeToString(fp.Digest[:])
	env["CHORA_CHECK_OBSERVER_SHA256"] = ResourceObserverSHA256()
	return nil
}

func (s IsolatedSource) ValidateContract(contract []byte) error {
	var doc struct {
		Candidate struct {
			Runtime struct {
				Config struct {
					SHA256 string `json:"sha256"`
				} `json:"config"`
			} `json:"runtime"`
			Sandbox struct {
				SHA256 string `json:"sha256"`
			} `json:"sandbox"`
		} `json:"candidate"`
	}
	if json.Unmarshal(contract, &doc) != nil {
		return errors.New("invalid isolated contract")
	}
	if doc.Candidate.Sandbox.SHA256 != strings.TrimPrefix(s.ImageID(), "sha256:") {
		return errors.New("frozen Task image differs from prepared environment")
	}
	if doc.Candidate.Runtime.Config.SHA256 != hex.EncodeToString(s.identity[:]) {
		return errors.New("frozen Task Runtime differs from prepared environment")
	}
	return nil
}

// WithIsolatedSource composes an independent Docker source with an existing
// host source; profile-bound selection still rejects unavailable sources.
func (a *Adapter) WithIsolatedSource(source IsolatedSource) (*Adapter, error) {
	config := a.config
	config.IsolatedSource = source
	return New(config)
}

// Model identity is checked against the persisted profile's exact model before
// projecting any terminal completion or check evidence. Partial progress frames
// may omit identity; a terminal assistant message must report both fields.
func validateIsolatedEventModel(line []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(line, &raw); err != nil {
		return err
	}
	eventType, _ := stringField(raw, "type")
	var identity map[string]json.RawMessage
	required := false
	switch eventType {
	case "message_end":
		identity = objectField(raw, "message")
		role, _ := stringField(identity, "role")
		if role != "assistant" {
			return nil
		}
		required = true
	case "agent_start", "model_identity":
		identity = raw
	case "response":
		command, _ := stringField(raw, "command")
		if command != "get_state" {
			return nil
		}
		identity = objectField(objectField(raw, "data"), "model")
	default:
		return nil
	}
	provider, _ := stringField(identity, "provider")
	model, _ := firstString(identity, "model_id", "model", "id")
	if nested := objectField(identity, "model"); nested != nil {
		model, _ = stringField(nested, "id")
		if nestedProvider, _ := stringField(nested, "provider"); nestedProvider != "" {
			if provider != "" && provider != nestedProvider {
				return errors.New("isolated Pi provider identity mismatch")
			}
			provider = nestedProvider
		}
	}
	if (provider != "" || required) && provider != IsolatedModelProvider || (model != "" || required) && model != IsolatedModelID {
		return errors.New("isolated Pi model identity mismatch")
	}
	return nil
}
