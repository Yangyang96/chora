package acceptanceauthority

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "modernc.org/sqlite"
)

const (
	testTaskFailure      = "task_018f0000-0000-7002-8000-000000000002"
	testTaskTimeout      = "task_018f0000-0000-7003-8000-000000000003"
	testSnapshotFailure  = "context_snapshot_018f0000-0000-7004-8000-000000000004"
	testSnapshotTimeout  = "context_snapshot_018f0000-0000-7005-8000-000000000005"
	testRunFailure       = "run_018f0000-0000-7006-8000-000000000006"
	testAttemptFailure   = "attempt_018f0000-0000-7007-8000-000000000007"
	testRunTimeout       = "run_018f0000-0000-7008-8000-000000000008"
	testAttemptTimeout   = "attempt_018f0000-0000-7009-8000-000000000009"
	testAttemptDuplicate = "attempt_018f0000-0000-7010-8000-000000000010"
)

func TestLoadIsDefaultDisabledAndPartialIsFatal(t *testing.T) {
	loaded, err := Load(StartupInput{})
	if err != nil || loaded.Enabled() {
		t.Fatalf("disabled load = %#v, %v", loaded, err)
	}
	if _, err := Load(StartupInput{AuthorityPath: "/tmp/authority.json", InstalledProduct: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("partial load error = %v", err)
	}
}

func TestLoadAcceptsOnlyExactCanonicalInstalledBinding(t *testing.T) {
	fixture := newAuthorityFixture(t)
	loaded, err := Load(fixture.input)
	if err != nil || !loaded.Enabled() || loaded.Digest() != fixture.input.AuthoritySHA256 {
		t.Fatalf("Load() = %#v, %v", loaded, err)
	}
	data, err := os.ReadFile(fixture.input.AuthorityPath)
	if err != nil {
		t.Fatal(err)
	}
	unknown := append([]byte(nil), data[:len(data)-2]...)
	unknown = append(unknown, []byte(`,"unknown":true}`)...)
	unknown = append(unknown, '\n')
	if err := os.Chmod(fixture.input.AuthorityPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fixture.input.AuthorityPath, unknown, 0o400); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(fixture.input.AuthorityPath, 0o400); err != nil {
		t.Fatal(err)
	}
	fixture.input.AuthoritySHA256 = hexDigest(unknown)
	if _, err := Load(fixture.input); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown field error = %v", err)
	}
}

func TestBindDatabaseAcceptsExactAcceptedTasksBeforeRunsExist(t *testing.T) {
	fixture, loaded, databasePath := startupDatabaseFixture(t, AgentExecutionProfileStandard)
	controller, err := loaded.BindDatabase(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	if controller == nil || controller.AuthorityDigest() != fixture.input.AuthoritySHA256 || controller.TupleIdentity() != fixture.input.CandidateTuple {
		t.Fatalf("bound controller = %#v", controller)
	}
	assertTableCount(t, databasePath, "runs", 0)
	assertTableCount(t, databasePath, "attempts", 0)
}

func TestBindDatabaseRejectsWrongOrNonlatestProfile(t *testing.T) {
	for _, test := range []struct {
		name   string
		seed   string
		mutate func(*testing.T, string)
	}{
		{name: "wrong profile", seed: "minimal"},
		{name: "nonlatest Standard", seed: AgentExecutionProfileStandard, mutate: func(t *testing.T, path string) {
			execAuthorityDB(t, path, `INSERT INTO agent_execution_profile_preferences(task_id,version,profile) VALUES(?,2,'minimal')`, testTaskFailure)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, loaded, databasePath := startupDatabaseFixture(t, test.seed)
			if test.mutate != nil {
				test.mutate(t, databasePath)
			}
			if _, err := loaded.BindDatabase(context.Background(), databasePath); !errors.Is(err, ErrInvalid) {
				t.Fatalf("profile error = %v", err)
			}
		})
	}
}

func TestBindDatabaseRejectsTaskSnapshotCanonicalOrPreexistingExecutionDrift(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "Task missing", mutate: func(t *testing.T, path string) {
			execAuthorityDB(t, path, `DELETE FROM tasks WHERE id=?`, testTaskFailure)
		}},
		{name: "current Snapshot binding", mutate: func(t *testing.T, path string) {
			execAuthorityDB(t, path, `UPDATE technical_plan_acceptance_bindings SET snapshot_id=? WHERE task_id=?`, testSnapshotTimeout, testTaskFailure)
		}},
		{name: "Snapshot canonical", mutate: func(t *testing.T, path string) {
			execAuthorityDB(t, path, `UPDATE context_snapshots SET canonical_json=? WHERE id=?`, []byte(`{"task":{"id":"`+testTaskTimeout+`"}}`), testSnapshotFailure)
		}},
		{name: "preexisting Run", mutate: func(t *testing.T, path string) {
			execAuthorityDB(t, path, `INSERT INTO runs(id,task_id) VALUES(?,?)`, testRunFailure, testTaskFailure)
		}},
		{name: "preexisting Attempt", mutate: func(t *testing.T, path string) {
			seedExactAttempt(t, path, 0, nil)
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, loaded, databasePath := startupDatabaseFixture(t, AgentExecutionProfileStandard)
			test.mutate(t, databasePath)
			if _, err := loaded.BindDatabase(context.Background(), databasePath); err == nil {
				t.Fatal("startup drift was accepted")
			}
		})
	}
}

func TestBindDatabaseRejectsUnsafeDatabaseModeBeforeController(t *testing.T) {
	_, loaded, databasePath := startupDatabaseFixture(t, AgentExecutionProfileStandard)
	if err := os.Chmod(databasePath, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := loaded.BindDatabase(context.Background(), databasePath); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unsafe startup database error = %v", err)
	}
}

func TestInstalledControllerVerifiesNewAttemptThenConsumesOnce(t *testing.T) {
	fixture, loaded, databasePath := startupDatabaseFixture(t, AgentExecutionProfileStandard)
	controller, err := loaded.BindDatabase(context.Background(), databasePath)
	if err != nil {
		t.Fatal(err)
	}
	seedExactAttempt(t, databasePath, 0, nil)
	fact := installedInvocationFact(0)
	directive, err := controller.Consume(fact)
	if err != nil || directive.Action != ActionForceManagedNonzero || !validDigest(directive.ConsumptionDigest) {
		t.Fatalf("Consume() = %#v, %v", directive, err)
	}
	consumptionPath := filepath.Join(fixture.input.DataRoot, "o4-acceptance-authority-consumptions", ScenarioFailure+".json")
	data, err := os.ReadFile(consumptionPath)
	if err != nil {
		t.Fatal(err)
	}
	var record consumptionRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatal(err)
	}
	if record.RunID != fact.RunID || record.AttemptID != fact.AttemptID || record.InvocationSha256 != fact.InvocationDigest ||
		record.AuthoritySha256 != fixture.input.AuthoritySHA256 || record.ConsumptionSha256 != directive.ConsumptionDigest {
		t.Fatalf("durable consumption = %#v", record)
	}
	if _, err := controller.Consume(fact); !errors.Is(err, ErrConsumed) {
		t.Fatalf("installed directive refired: %v", err)
	}
}

func TestInstalledControllerRejectsAttemptDriftBeforeConsumption(t *testing.T) {
	tests := []struct {
		name       string
		mutateFact func(*InvocationFact)
		mutateRow  func(*attemptSeed)
		afterSeed  func(*testing.T, string)
	}{
		{name: "wrong Run", mutateFact: func(fact *InvocationFact) { fact.RunID = testRunTimeout }},
		{name: "wrong Attempt", mutateFact: func(fact *InvocationFact) { fact.AttemptID = testAttemptTimeout }},
		{name: "sequence", mutateRow: func(row *attemptSeed) { row.sequence = 2 }},
		{name: "profile", mutateRow: func(row *attemptSeed) { row.profile = "minimal" }},
		{name: "provider", mutateRow: func(row *attemptSeed) { row.provider = "host" }},
		{name: "source", mutateRow: func(row *attemptSeed) { row.source = "caller" }},
		{name: "capability", mutateRow: func(row *attemptSeed) { row.capability = "chora.minimal.v1" }},
		{name: "adapter", mutateRow: func(row *attemptSeed) { row.adapter = "fake" }},
		{name: "state", mutateRow: func(row *attemptSeed) { row.state = "starting" }},
		{name: "Snapshot", mutateRow: func(row *attemptSeed) { row.snapshotID = testSnapshotTimeout }},
		{name: "Snapshot digest", mutateRow: func(row *attemptSeed) { row.snapshotDigest = strings.Repeat("f", 64) }},
		{name: "duplicate Task Attempt", afterSeed: func(t *testing.T, path string) {
			row := exactAttemptSeed(0)
			row.attemptID = testAttemptDuplicate
			row.sequence = 2
			insertAttempt(t, path, row, false)
		}},
		{name: "existing Runtime session", afterSeed: func(t *testing.T, path string) {
			execAuthorityDB(t, path, `INSERT INTO runtime_sessions(attempt_id) VALUES(?)`, testAttemptFailure)
		}},
		{name: "latest profile drift", afterSeed: func(t *testing.T, path string) {
			execAuthorityDB(t, path, `INSERT INTO agent_execution_profile_preferences(task_id,version,profile) VALUES(?,2,'minimal')`, testTaskFailure)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture, loaded, databasePath := startupDatabaseFixture(t, AgentExecutionProfileStandard)
			controller, err := loaded.BindDatabase(context.Background(), databasePath)
			if err != nil {
				t.Fatal(err)
			}
			seedExactAttempt(t, databasePath, 0, test.mutateRow)
			if test.afterSeed != nil {
				test.afterSeed(t, databasePath)
			}
			fact := installedInvocationFact(0)
			if test.mutateFact != nil {
				test.mutateFact(&fact)
			}
			if _, err := controller.Consume(fact); err == nil {
				t.Fatal("Attempt drift was accepted")
			}
			assertNoConsumption(t, fixture.input.DataRoot)
		})
	}
}

func TestInstalledControllerRejectsDatabaseReplacementOrUnsafeMode(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*testing.T, string)
	}{
		{name: "replacement", mutate: func(t *testing.T, path string) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Rename(path, path+".replaced"); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "unsafe mode", mutate: func(t *testing.T, path string) {
			if err := os.Chmod(path, 0o644); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, loaded, databasePath := startupDatabaseFixture(t, AgentExecutionProfileStandard)
			controller, err := loaded.BindDatabase(context.Background(), databasePath)
			if err != nil {
				t.Fatal(err)
			}
			seedExactAttempt(t, databasePath, 0, nil)
			test.mutate(t, databasePath)
			if _, err := controller.Consume(installedInvocationFact(0)); !errors.Is(err, ErrInvalid) {
				t.Fatalf("database drift error = %v", err)
			}
			assertNoConsumption(t, fixture.input.DataRoot)
		})
	}
}

func TestControllerConsumesTwoExactStaticActionsOnceBeforeReturning(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "consumptions")
	controller, err := NewController(ControllerConfig{
		AuthorityDigest: strings.Repeat("a", 64), TupleIdentity: strings.Repeat("b", 64), ConsumptionRoot: root,
		Scenarios: testBoundScenarios(),
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, bound := range testBoundScenarios() {
		contract := bound.Contract
		fact := installedInvocationFact(index)
		directive, err := controller.Consume(fact)
		if err != nil || directive.Action != contract.Action || !validDigest(directive.ConsumptionDigest) {
			t.Fatalf("Consume(%s) = %#v, %v", contract.Scenario, directive, err)
		}
		path := filepath.Join(root, contract.Scenario+".json")
		info, statErr := os.Lstat(path)
		if statErr != nil || info.Mode().Perm() != 0o400 {
			t.Fatalf("durable consumption %s = %#v, %v", path, info, statErr)
		}
		if _, err := controller.Consume(fact); !errors.Is(err, ErrConsumed) {
			t.Fatalf("scenario %s refired: %v", contract.Scenario, err)
		}
	}
}

func TestControllerPropagatesDirectorySyncFailureAndSealsConsumption(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "consumptions")
	syncFailure := errors.New("injected directory sync failure")
	controller, err := NewController(ControllerConfig{
		AuthorityDigest: strings.Repeat("a", 64), TupleIdentity: strings.Repeat("b", 64), ConsumptionRoot: root, Scenarios: testBoundScenarios(),
		DirectorySync: func(path string) error {
			if path == root {
				return syncFailure
			}
			return durableSyncDirectory(path)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = controller.Consume(installedInvocationFact(0))
	if !errors.Is(err, syncFailure) {
		t.Fatalf("directory sync error = %v", err)
	}
	info, statErr := os.Stat(filepath.Join(root, ScenarioFailure+".json"))
	if statErr != nil || info.Mode().Perm() != 0o400 {
		t.Fatalf("failed sync did not leave a sealed fail-closed record: %#v, %v", info, statErr)
	}
}

func TestAuthorityIDsRequireProductUUIDv7Namespaces(t *testing.T) {
	document := testDocument(strings.Repeat("a", 64), strings.Repeat("b", 64))
	document.Scenarios[0].TaskID = "task_018f0000-0000-4002-8000-000000000002"
	data, _ := CanonicalBytes(document)
	if _, err := decodeDocument(data); !errors.Is(err, ErrInvalid) {
		t.Fatalf("UUIDv4 Task accepted: %v", err)
	}
	document = testDocument(strings.Repeat("a", 64), strings.Repeat("b", 64))
	document.Scenarios[0].SnapshotID = "context_snapshot_018f0000-0000-4004-8000-000000000004"
	data, _ = CanonicalBytes(document)
	if _, err := decodeDocument(data); !errors.Is(err, ErrInvalid) {
		t.Fatalf("UUIDv4 Snapshot accepted: %v", err)
	}
}

func TestControllerRejectsDuplicateTaskOrSnapshotBinding(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func([]BoundScenario){
		func(scenarios []BoundScenario) { scenarios[1].Contract.TaskID = scenarios[0].Contract.TaskID },
		func(scenarios []BoundScenario) { scenarios[1].Contract.SnapshotID = scenarios[0].Contract.SnapshotID },
	} {
		scenarios := testBoundScenarios()
		mutate(scenarios)
		_, err := NewController(ControllerConfig{
			AuthorityDigest: strings.Repeat("a", 64), TupleIdentity: strings.Repeat("b", 64),
			ConsumptionRoot: filepath.Join(base, "consumptions"), Scenarios: scenarios,
		})
		if !errors.Is(err, ErrInvalid) {
			t.Fatalf("duplicate binding error = %v", err)
		}
	}
}

func TestSnapshotMustDeclareTheBoundTask(t *testing.T) {
	canonical := []byte(`{"task":{"id":"` + testTaskTimeout + `"}}`)
	if snapshotDeclaresTask(canonical, testTaskFailure) || !snapshotDeclaresTask(canonical, testTaskTimeout) {
		t.Fatal("Snapshot Task binding was not exact")
	}
}

func TestControllerRejectsDeclaredTaskDriftWithoutConsumption(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(base, 0o700); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(base, "consumptions")
	bound := testBoundScenarios()
	controller, err := NewController(ControllerConfig{AuthorityDigest: strings.Repeat("a", 64), TupleIdentity: strings.Repeat("b", 64), ConsumptionRoot: root, Scenarios: bound})
	if err != nil {
		t.Fatal(err)
	}
	fact := installedInvocationFact(0)
	fact.AttemptID = testAttemptTimeout
	_, err = controller.Consume(fact)
	if !errors.Is(err, ErrInvalid) {
		t.Fatalf("drift error = %v", err)
	}
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("drift created consumption state: %v", err)
	}
}

type authorityFixture struct {
	root  string
	input StartupInput
}

func newAuthorityFixture(t *testing.T) authorityFixture {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	dataRoot := filepath.Join(root, "data")
	if err := os.Mkdir(dataRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(root, "chora")
	binary := []byte("installed candidate binary")
	if err := os.WriteFile(binaryPath, binary, 0o500); err != nil {
		t.Fatal(err)
	}
	document := testDocument(pathDigest(dataRoot), hexDigest(binary))
	data, err := CanonicalBytes(document)
	if err != nil {
		t.Fatal(err)
	}
	authorityPath := filepath.Join(root, "authority.json")
	if err := os.WriteFile(authorityPath, data, 0o400); err != nil {
		t.Fatal(err)
	}
	return authorityFixture{root: root, input: StartupInput{
		AuthorityPath: authorityPath, AuthoritySHA256: hexDigest(data), CandidateTuple: document.TupleIdentity,
		GenerationID: document.GenerationID, DataRoot: dataRoot, BinaryPath: binaryPath,
		SourceAggregate: document.SourceAggregateSHA256, PolicySHA256: document.PolicySHA256, InstalledProduct: true,
	}}
}

func startupDatabaseFixture(t *testing.T, profile string) (authorityFixture, Loaded, string) {
	t.Helper()
	fixture := newAuthorityFixture(t)
	loaded, err := Load(fixture.input)
	if err != nil {
		t.Fatal(err)
	}
	databasePath := filepath.Join(fixture.root, "chora.db")
	seedAuthorityDatabase(t, databasePath, profile)
	return fixture, loaded, databasePath
}

func testDocument(dataRootDigest, binaryDigest string) Document {
	bound := testBoundScenarios()
	return Document{
		SchemaVersion: SchemaVersion, Status: StatusAuthorized, Scope: ScopeO4AcceptanceOnly,
		TupleIdentity: strings.Repeat("1", 64), GenerationID: "gen_123456789012345678901234",
		EnvironmentID: "env_123456789012345678901234", InstallID: "ins_123456789012345678901234",
		BinarySHA256: binaryDigest, SourceAggregateSHA256: strings.Repeat("2", 64), DataRootSHA256: dataRootDigest,
		PolicySHA256: strings.Repeat("3", 64),
		Scenarios:    []Scenario{bound[0].Contract, bound[1].Contract}, ClaimBoundary: append([]string(nil), ClaimBoundary...),
	}
}

func testBoundScenarios() []BoundScenario {
	failureSnapshot := []byte(`{"task":{"id":"` + testTaskFailure + `"}}`)
	timeoutSnapshot := []byte(`{"task":{"id":"` + testTaskTimeout + `"}}`)
	failureDigest := sha256.Sum256(failureSnapshot)
	timeoutDigest := sha256.Sum256(timeoutSnapshot)
	return []BoundScenario{
		{Contract: Scenario{Scenario: ScenarioFailure, Action: ActionForceManagedNonzero, TaskID: testTaskFailure, SnapshotID: testSnapshotFailure, SnapshotDigest: hex.EncodeToString(failureDigest[:]), AttemptSequence: 1, AgentExecutionProfile: AgentExecutionProfileStandard}, RunID: testRunFailure, AttemptID: testAttemptFailure},
		{Contract: Scenario{Scenario: ScenarioTimeout, Action: ActionAcceleratedManagedDeadline, TaskID: testTaskTimeout, SnapshotID: testSnapshotTimeout, SnapshotDigest: hex.EncodeToString(timeoutDigest[:]), AttemptSequence: 1, AgentExecutionProfile: AgentExecutionProfileStandard}, RunID: testRunTimeout, AttemptID: testAttemptTimeout},
	}
}

func installedInvocationFact(index int) InvocationFact {
	bound := testBoundScenarios()[index]
	return InvocationFact{
		RunID: bound.RunID, AttemptID: bound.AttemptID, TaskID: bound.Contract.TaskID,
		SnapshotID: bound.Contract.SnapshotID, SnapshotDigest: bound.Contract.SnapshotDigest,
		AgentExecutionProfile: AgentExecutionProfileStandard, InvocationDigest: strings.Repeat(string(rune('c'+index)), 64),
	}
}

func seedAuthorityDatabase(t *testing.T, path, profile string) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE tasks(id TEXT PRIMARY KEY)`,
		`CREATE TABLE runs(id TEXT PRIMARY KEY,task_id TEXT NOT NULL)`,
		`CREATE TABLE attempts(id TEXT PRIMARY KEY,run_id TEXT NOT NULL,sequence INTEGER NOT NULL,context_snapshot_id TEXT NOT NULL,context_digest BLOB NOT NULL,agent_execution_profile TEXT NOT NULL,agent_runtime_source TEXT NOT NULL,agent_execution_provider TEXT NOT NULL,agent_capability_policy TEXT NOT NULL,agent_trust_disclosure_policy TEXT NOT NULL,adapter_id TEXT NOT NULL,state TEXT NOT NULL)`,
		`CREATE TABLE context_snapshots(id TEXT PRIMARY KEY,digest BLOB NOT NULL,canonical_json BLOB NOT NULL)`,
		`CREATE TABLE runtime_sessions(attempt_id TEXT NOT NULL)`,
		`CREATE TABLE agent_execution_profile_preferences(task_id TEXT NOT NULL,version INTEGER NOT NULL,profile TEXT NOT NULL,PRIMARY KEY(task_id,version))`,
		`CREATE TABLE technical_plan_acceptance_bindings(task_id TEXT NOT NULL,revision_id TEXT NOT NULL,snapshot_id TEXT NOT NULL,snapshot_digest BLOB NOT NULL,PRIMARY KEY(task_id,revision_id))`,
		`CREATE TABLE current_technical_plan_acceptances(task_id TEXT PRIMARY KEY,revision_id TEXT NOT NULL)`,
	}
	for _, statement := range statements {
		if _, err := database.Exec(statement); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
	}
	for index, bound := range testBoundScenarios() {
		canonical := []byte(`{"task":{"id":"` + bound.Contract.TaskID + `"}}`)
		digest := sha256.Sum256(canonical)
		revisionID := "revision-" + string(rune('a'+index))
		for _, insert := range []struct {
			query string
			args  []any
		}{
			{`INSERT INTO tasks(id) VALUES(?)`, []any{bound.Contract.TaskID}},
			{`INSERT INTO context_snapshots(id,digest,canonical_json) VALUES(?,?,?)`, []any{bound.Contract.SnapshotID, digest[:], canonical}},
			{`INSERT INTO technical_plan_acceptance_bindings(task_id,revision_id,snapshot_id,snapshot_digest) VALUES(?,?,?,?)`, []any{bound.Contract.TaskID, revisionID, bound.Contract.SnapshotID, digest[:]}},
			{`INSERT INTO current_technical_plan_acceptances(task_id,revision_id) VALUES(?,?)`, []any{bound.Contract.TaskID, revisionID}},
			{`INSERT INTO agent_execution_profile_preferences(task_id,version,profile) VALUES(?,1,?)`, []any{bound.Contract.TaskID, profile}},
		} {
			if _, err := database.Exec(insert.query, insert.args...); err != nil {
				_ = database.Close()
				t.Fatal(err)
			}
		}
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
}

type attemptSeed struct {
	runID, attemptID, taskID, snapshotID, snapshotDigest string
	sequence                                             int
	profile, source, provider, capability                string
	disclosure, adapter, state                           string
}

func exactAttemptSeed(index int) attemptSeed {
	bound := testBoundScenarios()[index]
	return attemptSeed{
		runID: bound.RunID, attemptID: bound.AttemptID, taskID: bound.Contract.TaskID,
		snapshotID: bound.Contract.SnapshotID, snapshotDigest: bound.Contract.SnapshotDigest,
		sequence: 1, profile: AgentExecutionProfileStandard, source: "managed_pi_image", provider: "docker",
		capability: "chora.standard.v1", disclosure: "", adapter: "pi", state: "created",
	}
}

func seedExactAttempt(t *testing.T, path string, index int, mutate func(*attemptSeed)) {
	t.Helper()
	row := exactAttemptSeed(index)
	if mutate != nil {
		mutate(&row)
	}
	insertAttempt(t, path, row, true)
}

func insertAttempt(t *testing.T, path string, row attemptSeed, includeRun bool) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if includeRun {
		if _, err := database.Exec(`INSERT INTO runs(id,task_id) VALUES(?,?)`, row.runID, row.taskID); err != nil {
			_ = database.Close()
			t.Fatal(err)
		}
	}
	digest, err := hex.DecodeString(row.snapshotDigest)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	_, err = database.Exec(`INSERT INTO attempts(id,run_id,sequence,context_snapshot_id,context_digest,agent_execution_profile,agent_runtime_source,agent_execution_provider,agent_capability_policy,agent_trust_disclosure_policy,adapter_id,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`,
		row.attemptID, row.runID, row.sequence, row.snapshotID, digest, row.profile, row.source, row.provider, row.capability, row.disclosure, row.adapter, row.state)
	if err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func execAuthorityDB(t *testing.T, path, query string, args ...any) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.Exec(query, args...); err != nil {
		_ = database.Close()
		t.Fatal(err)
	}
	if err := database.Close(); err != nil {
		t.Fatal(err)
	}
}

func assertTableCount(t *testing.T, path, table string, want int) {
	t.Helper()
	database, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	var got int
	if err := database.QueryRow(`SELECT count(*) FROM ` + table).Scan(&got); err != nil || got != want {
		t.Fatalf("%s count = %d, %v; want %d", table, got, err, want)
	}
}

func assertNoConsumption(t *testing.T, dataRoot string) {
	t.Helper()
	root := filepath.Join(dataRoot, "o4-acceptance-authority-consumptions")
	if _, err := os.Lstat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed proof created consumption state: %v", err)
	}
}
