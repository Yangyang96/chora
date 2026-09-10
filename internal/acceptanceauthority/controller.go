package acceptanceauthority

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type InvocationFact struct {
	RunID                 string
	AttemptID             string
	TaskID                string
	SnapshotID            string
	SnapshotDigest        string
	AgentExecutionProfile string
	InvocationDigest      string
}

type Directive struct {
	Scenario          string
	Action            string
	AuthorityDigest   string
	ConsumptionDigest string
}

func (directive Directive) Enabled() bool { return directive.Action != "" }

func (controller *Controller) AuthorityDigest() string {
	if controller == nil {
		return ""
	}
	return controller.authorityDigest
}

func (controller *Controller) TupleIdentity() string {
	if controller == nil {
		return ""
	}
	return controller.tupleIdentity
}

type Controller struct {
	mu               sync.Mutex
	authorityDigest  string
	tupleIdentity    string
	root             string
	scenarios        []BoundScenario
	consumed         map[string]bool
	syncDirectory    func(string) error
	verifyInvocation func(InvocationFact, Scenario) error
}

type ControllerConfig struct {
	AuthorityDigest string
	TupleIdentity   string
	ConsumptionRoot string
	Scenarios       []BoundScenario
	DirectorySync   func(string) error
}

// NewController is the typed unit-test and installed-composition seam. The
// installed CLI obtains the same values only from Load plus BindDatabase.
func NewController(config ControllerConfig) (*Controller, error) {
	return newController(config, nil)
}

func newInstalledController(config ControllerConfig, verifier func(InvocationFact, Scenario) error) (*Controller, error) {
	if verifier == nil {
		return nil, ErrInvalid
	}
	return newController(config, verifier)
}

func newController(config ControllerConfig, verifier func(InvocationFact, Scenario) error) (*Controller, error) {
	if !validDigest(config.AuthorityDigest) || !validDigest(config.TupleIdentity) || !canonicalAbsolute(config.ConsumptionRoot) || len(config.Scenarios) != 2 {
		return nil, ErrInvalid
	}
	want := []struct{ scenario, action string }{{ScenarioFailure, ActionForceManagedNonzero}, {ScenarioTimeout, ActionAcceleratedManagedDeadline}}
	taskIDs := make(map[string]struct{}, len(config.Scenarios))
	snapshotIDs := make(map[string]struct{}, len(config.Scenarios))
	for index, bound := range config.Scenarios {
		contract := bound.Contract
		if contract.Scenario != want[index].scenario || contract.Action != want[index].action ||
			contract.AttemptSequence != 1 || contract.AgentExecutionProfile != AgentExecutionProfileStandard ||
			!validTaskID(contract.TaskID) || !validSnapshotID(contract.SnapshotID) || !validDigest(contract.SnapshotDigest) {
			return nil, ErrInvalid
		}
		if verifier == nil && (bound.RunID == "" || bound.AttemptID == "") {
			return nil, ErrInvalid
		}
		if verifier != nil && (bound.RunID != "" || bound.AttemptID != "") {
			return nil, ErrInvalid
		}
		if _, duplicate := taskIDs[contract.TaskID]; duplicate {
			return nil, ErrInvalid
		}
		if _, duplicate := snapshotIDs[contract.SnapshotID]; duplicate {
			return nil, ErrInvalid
		}
		taskIDs[contract.TaskID] = struct{}{}
		snapshotIDs[contract.SnapshotID] = struct{}{}
	}
	syncDirectory := config.DirectorySync
	if syncDirectory == nil {
		syncDirectory = durableSyncDirectory
	}
	return &Controller{
		authorityDigest: config.AuthorityDigest, tupleIdentity: config.TupleIdentity,
		root: config.ConsumptionRoot, scenarios: append([]BoundScenario(nil), config.Scenarios...),
		syncDirectory: syncDirectory, verifyInvocation: verifier,
	}, nil
}

type consumptionRecord struct {
	SchemaVersion     string `json:"schemaVersion"`
	Status            string `json:"status"`
	AuthoritySha256   string `json:"authoritySha256"`
	TupleIdentity     string `json:"tupleIdentity"`
	Scenario          string `json:"scenario"`
	Action            string `json:"action"`
	RunID             string `json:"runId"`
	AttemptID         string `json:"attemptId"`
	InvocationSha256  string `json:"invocationSha256"`
	ConsumptionSha256 string `json:"consumptionSha256"`
}

// Consume returns no directive for unrelated invocations. A declared Task is
// fail-closed on any drift and can consume its one fixed directive only once.
// The O_EXCL record is fsynced and sealed before this method returns, allowing
// the caller to start or mutate Docker resources only afterwards.
func (controller *Controller) Consume(fact InvocationFact) (Directive, error) {
	if controller == nil {
		return Directive{}, nil
	}
	controller.mu.Lock()
	defer controller.mu.Unlock()
	for _, scenario := range controller.scenarios {
		contract := scenario.Contract
		if fact.TaskID != contract.TaskID {
			continue
		}
		if controller.consumed != nil && controller.consumed[contract.Action] {
			return Directive{}, ErrConsumed
		}
		if fact.SnapshotID != contract.SnapshotID ||
			fact.SnapshotDigest != contract.SnapshotDigest || fact.AgentExecutionProfile != AgentExecutionProfileStandard ||
			!validDigest(fact.InvocationDigest) {
			return Directive{}, fmt.Errorf("%w: declared scenario invocation drifted", ErrInvalid)
		}
		if controller.verifyInvocation != nil {
			if err := controller.verifyInvocation(fact, contract); err != nil {
				return Directive{}, err
			}
		} else if fact.RunID != scenario.RunID || fact.AttemptID != scenario.AttemptID {
			return Directive{}, fmt.Errorf("%w: declared scenario invocation drifted", ErrInvalid)
		}
		if err := os.Mkdir(controller.root, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
			return Directive{}, fmt.Errorf("create consumption root: %w", err)
		}
		if err := validateOwnerDirectory(controller.root); err != nil {
			return Directive{}, fmt.Errorf("secure consumption root: %w", err)
		}
		if err := controller.syncDirectory(filepath.Dir(controller.root)); err != nil {
			return Directive{}, fmt.Errorf("sync consumption root creation: %w", err)
		}
		draft := consumptionRecord{
			SchemaVersion: "chora.m1-o4-fault-consumption.v1", Status: "consumed",
			AuthoritySha256: controller.authorityDigest, TupleIdentity: controller.tupleIdentity,
			Scenario: contract.Scenario, Action: contract.Action, RunID: fact.RunID,
			AttemptID: fact.AttemptID, InvocationSha256: fact.InvocationDigest,
		}
		unsigned, _ := json.Marshal(draft)
		digest := sha256.Sum256(unsigned)
		draft.ConsumptionSha256 = hex.EncodeToString(digest[:])
		data, _ := json.Marshal(draft)
		data = append(data, '\n')
		path := filepath.Join(controller.root, contract.Scenario+".json")
		file, err := openExclusiveOwnerFile(path, 0o600)
		if err != nil {
			if errors.Is(err, os.ErrExist) {
				return Directive{}, ErrConsumed
			}
			return Directive{}, fmt.Errorf("create durable consumption: %w", err)
		}
		created := true
		defer func() {
			if created {
				_ = os.Remove(path)
			}
		}()
		if _, err := file.Write(data); err != nil {
			_ = file.Close()
			return Directive{}, fmt.Errorf("write durable consumption: %w", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return Directive{}, fmt.Errorf("sync durable consumption: %w", err)
		}
		if err := file.Chmod(0o400); err != nil {
			_ = file.Close()
			return Directive{}, fmt.Errorf("seal durable consumption: %w", err)
		}
		if err := file.Sync(); err != nil {
			_ = file.Close()
			return Directive{}, fmt.Errorf("sync sealed durable consumption: %w", err)
		}
		if err := file.Close(); err != nil {
			return Directive{}, fmt.Errorf("close durable consumption: %w", err)
		}
		created = false
		if err := controller.syncDirectory(controller.root); err != nil {
			return Directive{}, fmt.Errorf("sync durable consumption directory: %w", err)
		}
		if controller.consumed == nil {
			controller.consumed = map[string]bool{}
		}
		controller.consumed[contract.Action] = true
		return Directive{Scenario: contract.Scenario, Action: contract.Action, AuthorityDigest: controller.authorityDigest, ConsumptionDigest: draft.ConsumptionSha256}, nil
	}
	return Directive{}, nil
}
