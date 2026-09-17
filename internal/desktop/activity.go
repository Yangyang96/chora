package desktop

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

type ActivityReport struct {
	Idle    bool             `json:"idle"`
	Reasons []ActivityReason `json:"reasons"`
}
type ActivityReason struct {
	Kind  string `json:"kind"`
	Count int64  `json:"count"`
}

// Activity reads durable state without taking OwnerLock so a running Workbench
// can expose it. Any unreadable or unknown state fails closed.
func Activity(ctx context.Context, dataRoot string) (ActivityReport, error) {
	root, e := exactAbsolute(dataRoot)
	if e != nil {
		return ActivityReport{}, e
	}
	return activityUnlocked(ctx, root)
}
func activityUnlocked(ctx context.Context, root string) (ActivityReport, error) {
	return activityAt(ctx, root, root)
}

func activityAt(ctx context.Context, databaseRoot, root string) (ActivityReport, error) {
	dbPath := filepath.Join(databaseRoot, "chora.db")
	if info, e := os.Lstat(dbPath); e != nil || !info.Mode().IsRegular() {
		return ActivityReport{}, errors.New("activity database is unavailable")
	}
	dsn := (&url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(1000)"}).String()
	db, e := sql.Open("sqlite", dsn)
	if e != nil {
		return ActivityReport{}, e
	}
	defer db.Close()
	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	report := ActivityReport{Idle: true, Reasons: []ActivityReason{}}
	validations := []string{`SELECT count(*) FROM runs WHERE state NOT IN ('draft','ready','running','stopping','awaiting_verification','verifying','awaiting_review','revision_required','recovery_required','verification_recovery_required','accepted','cancelled','completed')`, `SELECT count(*) FROM attempts WHERE state NOT IN ('created','starting','running','output_submitted','failed','interrupted','cancelled')`, `SELECT count(*) FROM verification_runs WHERE state NOT IN ('awaiting','verifying','completed','recovery_required')`, `SELECT count(*) FROM verification_attempts WHERE state NOT IN ('pending','running','completed','cancelled','recovery_required')`, `SELECT count(*) FROM task_delivery_operations WHERE state NOT IN ('preview','writing','succeeded','failed','recovery_required')`, `SELECT count(*) FROM apply_repository_steps WHERE status NOT IN ('planned','preflight','writing','applied','conflict','uncertain')`}
	for _, query := range validations {
		var unknown int64
		if e = db.QueryRowContext(queryCtx, query).Scan(&unknown); e != nil {
			return ActivityReport{}, fmt.Errorf("validate activity states: %w", e)
		}
		if unknown > 0 {
			return ActivityReport{}, errors.New("unknown durable activity state")
		}
	}
	queries := []struct{ kind, q string }{
		{"agent_execution", `SELECT count(*) FROM runs WHERE state IN ('running','stopping','awaiting_verification','verifying')`},
		{"agent_attempt", `SELECT count(*) FROM attempts WHERE state IN ('created','starting','running')`},
		{"verification", `SELECT count(*) FROM verification_runs WHERE state IN ('verifying','recovery_required')`},
		{"verification_attempt", `SELECT count(*) FROM verification_attempts WHERE state IN ('pending','running','recovery_required')`},
		{"delivery", `SELECT count(*) FROM task_delivery_operations WHERE state IN ('writing','recovery_required')`},
		{"resource_apply", `SELECT count(*) FROM apply_repository_steps s WHERE s.sequence=(SELECT max(s2.sequence) FROM apply_repository_steps s2 WHERE s2.operation_id=s.operation_id AND s2.repo_id=s.repo_id) AND s.status IN ('planned','preflight','writing','uncertain')`},
		{"pending_check", `SELECT count(*) FROM checks c JOIN attempts a ON a.id=c.attempt_id WHERE c.status='pending' AND a.state IN ('created','starting','running')`},
		{"check_invocation", `SELECT count(*) FROM check_invocations c JOIN attempts a ON a.id=c.attempt_id WHERE a.state IN ('created','starting','running')`},
	}
	for _, item := range queries {
		var count int64
		if e = db.QueryRowContext(queryCtx, item.q).Scan(&count); e != nil {
			return ActivityReport{}, fmt.Errorf("inspect %s activity: %w", item.kind, e)
		}
		if count > 0 {
			report.Idle = false
			report.Reasons = append(report.Reasons, ActivityReason{Kind: item.kind, Count: count})
		}
	}
	previewCount, e := activePreviews(filepath.Join(root, "runtime", "app-previews"))
	if e != nil {
		return ActivityReport{}, e
	}
	if previewCount > 0 {
		report.Idle = false
		report.Reasons = append(report.Reasons, ActivityReason{Kind: "app_preview", Count: previewCount})
	}
	return report, nil
}

func activePreviews(root string) (int64, error) {
	if _, e := os.Stat(root); os.IsNotExist(e) {
		return 0, nil
	} else if e != nil {
		return 0, e
	}
	var count int64
	e := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || d.Name() != "manifest.json" {
			return nil
		}
		f, e := os.Open(path)
		if e != nil {
			return e
		}
		defer f.Close()
		decoder := json.NewDecoder(io.LimitReader(f, 1<<20))
		var record struct {
			Schema string `json:"schema"`
			Owner  string `json:"owner"`
			Nonce  string `json:"nonce"`
			View   struct {
				State           string `json:"state"`
				CleanupRequired bool   `json:"cleanupRequired"`
			} `json:"view"`
			PID            int    `json:"pid,omitempty"`
			PGID           int    `json:"pgid,omitempty"`
			Birth          string `json:"birth,omitempty"`
			ContainerID    string `json:"containerId,omitempty"`
			ContainerName  string `json:"containerName,omitempty"`
			ImageID        string `json:"imageId,omitempty"`
			EngineIdentity string `json:"engineIdentity,omitempty"`
			SourceSnapshot string `json:"sourceSnapshot,omitempty"`
			DockerLogBytes int64  `json:"dockerLogBytes,omitempty"`
		}
		if e = decoder.Decode(&record); e != nil {
			return fmt.Errorf("invalid app preview manifest: %w", e)
		}
		if decoder.Decode(new(any)) != io.EOF || record.Schema != "chora.app-preview.v1" || record.Owner != "chora:internal/apppreview" {
			return errors.New("unknown app preview manifest")
		}
		switch record.View.State {
		case "idle", "stopped", "failed":
			if record.View.CleanupRequired {
				count++
			}
		case "starting", "running", "recovery_required":
			count++
		default:
			return errors.New("unknown app preview state")
		}
		return nil
	})
	return count, e
}
