package execution

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/Yangyang96/chora/internal/domain"
	"io"
	"strings"
)

const ResourceCheckObservationSchema = "chora.resource-check-observation.v1"
const ResourceCheckFingerprintAlgorithm = "sha256-resource-patch-v1"

type ResourceCheckObservation struct {
	HelperSHA256       string `json:"helperSha256"`
	RuntimeFingerprint string `json:"runtimeFingerprint"`
	Schema             string `json:"schema"`
	Algorithm          string `json:"algorithm"`
	ObserverSHA256     string `json:"observerSha256"`
	AttemptID          string `json:"attemptId"`
	RepoID             string `json:"repoId"`
	ToolCallID         string `json:"toolCallId"`
	CommandDigest      string `json:"commandDigest"`
	WorkingDirectory   string `json:"workingDirectory"`
	StartFingerprint   string `json:"startFingerprint"`
	EndFingerprint     string `json:"endFingerprint"`
	SingleToolBatch    bool   `json:"singleToolBatch"`
}

func DecodeResourceCheckObservation(raw []byte, observerSHA string) (ResourceCheckObservation, error) {
	var value ResourceCheckObservation
	if len(raw) == 0 || len(raw) > 4096 {
		return value, errors.New("observer evidence exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&value); err != nil {
		return value, err
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return value, errors.New("observer evidence has trailing data")
	}
	validDigest := func(value string) bool {
		b, err := hex.DecodeString(value)
		return err == nil && len(b) == 32 && strings.ToLower(value) == value
	}
	if value.Schema != ResourceCheckObservationSchema || value.Algorithm != ResourceCheckFingerprintAlgorithm || !validDigest(value.HelperSHA256) || !validDigest(value.RuntimeFingerprint) || !validDigest(observerSHA) || value.ObserverSHA256 != observerSHA || !value.SingleToolBatch || !validDigest(value.CommandDigest) || !validDigest(value.StartFingerprint) || value.StartFingerprint != value.EndFingerprint {
		return value, errors.New("observer evidence is incomplete or incompatible")
	}
	if _, err := domain.ParseAttemptID(value.AttemptID); err != nil {
		return value, err
	}
	if _, err := domain.ParseRepositoryID(value.RepoID); err != nil {
		return value, err
	}
	if value.ToolCallID == "" || len(value.ToolCallID) > 256 || strings.ContainsAny(value.ToolCallID, "\x00\r\n") {
		return value, errors.New("observer tool identity is invalid")
	}
	cwd := value.WorkingDirectory
	if cwd != "." {
		if cwd == "" || len(cwd) > 4096 || strings.HasPrefix(cwd, "/") || strings.ContainsAny(cwd, "\\\x00\r\n") {
			return value, errors.New("observer directory is invalid")
		}
		for _, part := range strings.Split(cwd, "/") {
			if part == "" || part == "." || part == ".." || part == ".git" {
				return value, errors.New("observer directory is invalid")
			}
		}
	}
	return value, nil
}
