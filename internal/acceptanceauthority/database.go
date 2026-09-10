package acceptanceauthority

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"syscall"

	"github.com/Yangyang96/chora/internal/domain"
	_ "modernc.org/sqlite"
)

type BoundScenario struct {
	Contract  Scenario
	RunID     string
	AttemptID string
}

type databaseBinding struct {
	path   string
	device uint64
	inode  uint64
}

// BindDatabase proves the startup-only facts that exist before the public
// start flow creates a Run: each declared Task has the exact current accepted
// Snapshot, its latest execution-profile preference is Standard, and it has no
// Run, Attempt, or Runtime state. The exact owner-only database inode is then
// retained for the first invocation proof performed by Consume.
func (loaded Loaded) BindDatabase(ctx context.Context, databasePath string) (*Controller, error) {
	if !loaded.Enabled() {
		return nil, nil
	}
	binding, err := captureDatabaseBinding(databasePath)
	if err != nil {
		return nil, fmt.Errorf("%w: database path: %v", ErrInvalid, err)
	}
	database, err := binding.open(ctx)
	if err != nil {
		return nil, err
	}
	defer database.Close()

	bound := make([]BoundScenario, 0, len(loaded.document.Scenarios))
	for _, scenario := range loaded.document.Scenarios {
		if err := validateStartupScenario(ctx, database, scenario); err != nil {
			return nil, err
		}
		bound = append(bound, BoundScenario{Contract: scenario})
	}
	if err := binding.verifyPath(); err != nil {
		return nil, fmt.Errorf("%w: database changed during startup proof", ErrInvalid)
	}
	if err := loaded.proveUnconsumed(); err != nil {
		return nil, err
	}
	root := filepath.Join(loaded.dataRoot, "o4-acceptance-authority-consumptions")
	return newInstalledController(ControllerConfig{
		AuthorityDigest: loaded.digest, TupleIdentity: loaded.document.TupleIdentity,
		ConsumptionRoot: root, Scenarios: bound,
	}, binding.verifyInvocation)
}

func validateStartupScenario(ctx context.Context, database *sql.DB, scenario Scenario) error {
	if err := validateCurrentAcceptedSnapshot(ctx, database, scenario); err != nil {
		return err
	}
	var latestProfile string
	if err := database.QueryRowContext(ctx, `SELECT profile FROM agent_execution_profile_preferences WHERE task_id=? ORDER BY version DESC LIMIT 1`, scenario.TaskID).Scan(&latestProfile); err != nil || latestProfile != AgentExecutionProfileStandard {
		return fmt.Errorf("%w: scenario %s latest execution profile is not Standard", ErrInvalid, scenario.Scenario)
	}
	var runCount, attemptCount, runtimeCount int
	if err := database.QueryRowContext(ctx, `
		SELECT
		  (SELECT count(*) FROM runs WHERE task_id=?),
		  (SELECT count(*) FROM attempts a JOIN runs r ON r.id=a.run_id WHERE r.task_id=?),
		  (SELECT count(*) FROM runtime_sessions session JOIN attempts a ON a.id=session.attempt_id JOIN runs r ON r.id=a.run_id WHERE r.task_id=?)`,
		scenario.TaskID, scenario.TaskID, scenario.TaskID).Scan(&runCount, &attemptCount, &runtimeCount); err != nil {
		return fmt.Errorf("%w: scenario %s startup state unavailable", ErrInvalid, scenario.Scenario)
	}
	if runCount != 0 || attemptCount != 0 || runtimeCount != 0 {
		return fmt.Errorf("%w: scenario %s already has Run, Attempt, or Runtime state", ErrConsumed, scenario.Scenario)
	}
	return nil
}

func validateCurrentAcceptedSnapshot(ctx context.Context, database *sql.DB, scenario Scenario) error {
	var snapshotID string
	var bindingDigest, snapshotDigest, canonical []byte
	err := database.QueryRowContext(ctx, `
		SELECT binding.snapshot_id,binding.snapshot_digest,snapshot.digest,snapshot.canonical_json
		FROM tasks task
		JOIN current_technical_plan_acceptances current ON current.task_id=task.id
		JOIN technical_plan_acceptance_bindings binding ON binding.task_id=current.task_id AND binding.revision_id=current.revision_id
		JOIN context_snapshots snapshot ON snapshot.id=binding.snapshot_id
		WHERE task.id=?`, scenario.TaskID).Scan(&snapshotID, &bindingDigest, &snapshotDigest, &canonical)
	if err != nil || snapshotID != scenario.SnapshotID || len(bindingDigest) != sha256.Size || len(snapshotDigest) != sha256.Size ||
		!bytes.Equal(bindingDigest, snapshotDigest) || hex.EncodeToString(snapshotDigest) != scenario.SnapshotDigest ||
		sha256.Sum256(canonical) != digestArray(snapshotDigest) || !snapshotDeclaresTask(canonical, scenario.TaskID) {
		return fmt.Errorf("%w: scenario %s current accepted Snapshot unavailable", ErrInvalid, scenario.Scenario)
	}
	return nil
}

func captureDatabaseBinding(path string) (databaseBinding, error) {
	if !canonicalAbsolute(path) {
		return databaseBinding{}, errors.New("database path is invalid")
	}
	if err := validateOwnerRegularPath(path); err != nil {
		return databaseBinding{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return databaseBinding{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return databaseBinding{}, errors.New("database identity is unavailable")
	}
	return databaseBinding{path: path, device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

func (binding databaseBinding) verifyPath() error {
	current, err := captureDatabaseBinding(binding.path)
	if err != nil {
		return err
	}
	if current.device != binding.device || current.inode != binding.inode {
		return errors.New("database identity changed")
	}
	return nil
}

func (binding databaseBinding) open(ctx context.Context) (*sql.DB, error) {
	if err := binding.verifyPath(); err != nil {
		return nil, fmt.Errorf("%w: database path is no longer exact", ErrInvalid)
	}
	dsn := fmt.Sprintf("file:%s?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)", url.PathEscape(binding.path))
	database, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("%w: open acceptance database", ErrInvalid)
	}
	database.SetMaxOpenConns(1)
	if err := database.PingContext(ctx); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("%w: read acceptance database", ErrInvalid)
	}
	if err := binding.verifyPath(); err != nil {
		_ = database.Close()
		return nil, fmt.Errorf("%w: database changed while opening", ErrInvalid)
	}
	return database, nil
}

func (binding databaseBinding) verifyInvocation(fact InvocationFact, scenario Scenario) error {
	if _, err := domain.ParseRunID(fact.RunID); err != nil {
		return fmt.Errorf("%w: Run identity is invalid", ErrInvalid)
	}
	if _, err := domain.ParseAttemptID(fact.AttemptID); err != nil {
		return fmt.Errorf("%w: Attempt identity is invalid", ErrInvalid)
	}
	ctx := context.Background()
	database, err := binding.open(ctx)
	if err != nil {
		return err
	}
	defer database.Close()
	if err := validateCurrentAcceptedSnapshot(ctx, database, scenario); err != nil {
		return err
	}
	var latestProfile string
	if err := database.QueryRowContext(ctx, `SELECT profile FROM agent_execution_profile_preferences WHERE task_id=? ORDER BY version DESC LIMIT 1`, scenario.TaskID).Scan(&latestProfile); err != nil || latestProfile != AgentExecutionProfileStandard {
		return fmt.Errorf("%w: scenario %s latest execution profile drifted", ErrInvalid, scenario.Scenario)
	}

	var runCount, attemptCount, runtimeCount int
	if err := database.QueryRowContext(ctx, `
		SELECT
		  (SELECT count(*) FROM runs WHERE task_id=?),
		  (SELECT count(*) FROM attempts a JOIN runs r ON r.id=a.run_id WHERE r.task_id=?),
		  (SELECT count(*) FROM runtime_sessions session JOIN attempts a ON a.id=session.attempt_id JOIN runs r ON r.id=a.run_id WHERE r.task_id=?)`,
		scenario.TaskID, scenario.TaskID, scenario.TaskID).Scan(&runCount, &attemptCount, &runtimeCount); err != nil {
		return fmt.Errorf("%w: scenario %s invocation state unavailable", ErrInvalid, scenario.Scenario)
	}
	if runCount != 1 || attemptCount != 1 {
		return fmt.Errorf("%w: scenario %s does not have the sole Task Run/Attempt", ErrInvalid, scenario.Scenario)
	}
	if runtimeCount != 0 {
		return fmt.Errorf("%w: scenario %s already has Runtime state", ErrConsumed, scenario.Scenario)
	}

	var sequence int
	var snapshotID, profile, source, provider, capability, disclosure, adapter, state string
	var snapshotDigest []byte
	err = database.QueryRowContext(ctx, `
		SELECT a.sequence,a.context_snapshot_id,a.context_digest,a.agent_execution_profile,
		       a.agent_runtime_source,a.agent_execution_provider,a.agent_capability_policy,
		       a.agent_trust_disclosure_policy,a.adapter_id,a.state
		FROM runs r JOIN attempts a ON a.run_id=r.id
		WHERE r.task_id=? AND r.id=? AND a.id=?`, scenario.TaskID, fact.RunID, fact.AttemptID).
		Scan(&sequence, &snapshotID, &snapshotDigest, &profile, &source, &provider, &capability, &disclosure, &adapter, &state)
	if err != nil || sequence != scenario.AttemptSequence || snapshotID != scenario.SnapshotID || len(snapshotDigest) != sha256.Size ||
		hex.EncodeToString(snapshotDigest) != scenario.SnapshotDigest || profile != AgentExecutionProfileStandard ||
		source != "managed_pi_image" || provider != "docker" || capability != "chora.standard.v1" || disclosure != "" || adapter != "pi" || state != "created" {
		return fmt.Errorf("%w: scenario %s invocation binding drifted", ErrInvalid, scenario.Scenario)
	}
	if err := binding.verifyPath(); err != nil {
		return fmt.Errorf("%w: database changed during invocation proof", ErrInvalid)
	}
	return nil
}

func digestArray(value []byte) [sha256.Size]byte {
	var digest [sha256.Size]byte
	copy(digest[:], value)
	return digest
}

func snapshotDeclaresTask(canonical []byte, taskID string) bool {
	var document struct {
		Task struct {
			ID string `json:"id"`
		} `json:"task"`
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	if decoder.Decode(&document) != nil || document.Task.ID != taskID {
		return false
	}
	var trailing any
	return errors.Is(decoder.Decode(&trailing), io.EOF)
}

func (loaded Loaded) proveUnconsumed() error {
	root := filepath.Join(loaded.dataRoot, "o4-acceptance-authority-consumptions")
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("%w: inspect consumption root", ErrInvalid)
	}
	if err := validateOwnerDirectory(root); err != nil {
		return fmt.Errorf("%w: unsafe consumption root", ErrInvalid)
	}
	if len(entries) != 0 {
		return fmt.Errorf("%w: durable consumption already exists", ErrConsumed)
	}
	return nil
}
