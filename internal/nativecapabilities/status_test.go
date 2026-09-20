package nativecapabilities

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestObservationRejectsWrongExecutionIdentity(t *testing.T) {
	root := t.TempDir()
	project, attempt := domain.NewProjectID(), domain.NewAttemptID()
	valid := Observation{Version: 1, ProjectID: project.String(), AttemptID: attempt.String()}
	for _, name := range []string{"valid", "project", "attempt", "version"} {
		t.Run(name, func(t *testing.T) {
			value := valid
			switch name {
			case "project":
				value.ProjectID = domain.NewProjectID().String()
			case "attempt":
				value.AttemptID = domain.NewAttemptID().String()
			case "version":
				value.Version = 2
			}
			raw, err := json.Marshal(value)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(filepath.Join(root, attempt.String()+".status.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			_, err = ReadObservation(root, project, attempt)
			if (err == nil) != (name == "valid") {
				t.Fatalf("identity validation: %v", err)
			}
		})
	}
}
