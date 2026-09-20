package nativecapabilities

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Yangyang96/chora/internal/domain"
)

func ReadObservation(root string, projectID domain.ProjectID, attemptID domain.AttemptID) (Observation, error) {
	if !projectID.Valid() || !attemptID.Valid() {
		return Observation{}, errors.New("invalid observation identity")
	}
	raw, err := readPrivate(filepath.Join(root, attemptID.String()+".status.json"))
	if err != nil {
		return Observation{}, err
	}
	var value Observation
	if json.Unmarshal(raw, &value) != nil || value.Version != 1 || value.ProjectID != projectID.String() || value.AttemptID != attemptID.String() {
		return Observation{}, errors.New("observation identity mismatch")
	}
	return value, nil
}

type Observation struct {
	Version       int          `json:"version"`
	ProjectID     string       `json:"projectID"`
	ConfigVersion uint64       `json:"configVersion"`
	AttemptID     string       `json:"attemptID"`
	ObservedAt    time.Time    `json:"observedAt"`
	Skills        []Capability `json:"skills"`
	Servers       []Capability `json:"servers"`
	Bridge        Bridge       `json:"bridge"`
	Running       bool         `json:"running"`
}

// Observations are last-seen native state, not a claim about future executions.
func Observations(root, projectID string) ([]Observation, error) {
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return []Observation{}, nil
	}
	if err != nil {
		return nil, err
	}
	result := []Observation{}
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".status.json") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		raw, e := readPrivate(filepath.Join(root, entry.Name()))
		if e != nil {
			continue
		}
		var observation Observation
		if json.Unmarshal(raw, &observation) != nil || observation.Version != 1 || observation.ProjectID != projectID || entry.Name() != observation.AttemptID+".status.json" {
			continue
		}
		result = append(result, observation)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ObservedAt.After(result[j].ObservedAt) })
	if len(result) > 20 {
		result = result[:20]
	}
	return result, nil
}
