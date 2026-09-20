package domain

import (
	"strings"
	"testing"
	"time"
)

func TestExecutionSettingsValidation(t *testing.T) {
	now := time.Now().UTC()
	project := ProjectExecutionSettings{ProjectID: NewProjectID(), Version: 1, AgentExecutionProfile: AgentExecutionProfileTrustedLocal, UpdatedAt: now}
	if err := project.Validate(); err != nil {
		t.Fatal(err)
	}
	model := ModelIdentity{Provider: "provider", ModelID: "model"}
	project.Model = &model
	if err := project.Validate(); err != nil {
		t.Fatal(err)
	}
	project.AgentExecutionProfile = AgentExecutionProfileStandard
	if err := project.Validate(); err == nil {
		t.Fatal("internal comparison profile accepted as a Project default")
	}

	task := TaskExecutionSettings{
		TaskID: NewTaskID(), ProjectID: NewProjectID(), AgentExecutionProfile: AgentExecutionProfileIsolatedLocal,
		EnvironmentSource: ExecutionSettingsSourceDefault, ModelSource: ExecutionSettingsSourceProject,
		NativeCapabilitiesJSON: `{}`, CreatedAt: now,
	}
	if err := task.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "[]", "null", "{", `{"capability":true} trailing`, `{"value":"` + strings.Repeat("x", 64*1024) + `"}`} {
		task.NativeCapabilitiesJSON = invalid
		if err := task.Validate(); err == nil {
			t.Fatalf("native capability snapshot %q accepted", invalid[:min(len(invalid), 32)])
		}
	}
}
