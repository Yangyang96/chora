package domain

import (
	"testing"
	"time"
)

func validDelegation() TaskDelegation {
	now := time.Now().UTC()
	return TaskDelegation{ParentTaskID: NewTaskID(), Version: 1, State: DelegationRunning, Assignments: []DelegationAssignment{{Role: "Researcher", Title: "Compare options", Requirement: "Use supplied project material."}}, ActorID: "local-human", SessionID: "local-browser", CreatedAt: now, UpdatedAt: now}
}
func TestDelegationBoundsAndDistinctRoles(t *testing.T) {
	for _, test := range []struct {
		name string
		edit func(*TaskDelegation)
	}{
		{"empty", func(d *TaskDelegation) { d.Assignments = nil }},
		{"too many", func(d *TaskDelegation) {
			for i := 0; i < 5; i++ {
				d.Assignments = append(d.Assignments, d.Assignments[0])
			}
		}},
		{"duplicate role", func(d *TaskDelegation) {
			d.Assignments = append(d.Assignments, DelegationAssignment{Role: " researcher ", Title: "Other", Requirement: "Other"})
		}},
		{"empty instructions", func(d *TaskDelegation) { d.Assignments[0].Requirement = " " }},
		{"missing authority", func(d *TaskDelegation) { d.ActorID = "" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			d := validDelegation()
			test.edit(&d)
			if d.Validate() == nil {
				t.Fatal("invalid delegation accepted")
			}
		})
	}
}
func TestDelegationStopIntentCannotBecomeLaunchAuthority(t *testing.T) {
	d := validDelegation()
	now := d.CreatedAt.Add(time.Second)
	d, err := d.Transition(DelegationStopping, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Transition(DelegationRunning, "", now); err == nil {
		t.Fatal("stopping became running")
	}
	if _, err = d.Transition(DelegationBlocked, "", now); err == nil {
		t.Fatal("stop intent was erased by an error")
	}
	d, err = d.Transition(DelegationStopping, "runtime ownership is uncertain", now)
	if err != nil {
		t.Fatal(err)
	}
	d, err = d.Transition(DelegationStopped, "", now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = d.Transition(DelegationRunning, "", now); err == nil {
		t.Fatal("stopped authority resumed")
	}
}
