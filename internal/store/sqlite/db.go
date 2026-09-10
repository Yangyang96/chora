package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "modernc.org/sqlite"

	storecontract "github.com/Yangyang96/chora/internal/store"
)

type Store struct {
	db           *sql.DB
	path         string
	beforeCommit func() error
}

type OpenOptions struct {
	BusyTimeout time.Duration
}

func Open(ctx context.Context, path string) (*Store, error) {
	return OpenWithOptions(ctx, path, OpenOptions{})
}

func OpenWithOptions(ctx context.Context, path string, options OpenOptions) (*Store, error) {
	return openStore(ctx, path, options, nil)
}

func openWithCommitHook(ctx context.Context, path string, hook func() error) (*Store, error) {
	return openStore(ctx, path, OpenOptions{}, hook)
}

func openStore(ctx context.Context, path string, options OpenOptions, hook func() error) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("open sqlite: empty path")
	}
	path, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("absolute sqlite path: %w", err)
	}
	path, trustedRoot, err := anchorManagedPath(path)
	if err != nil {
		return nil, err
	}
	if err := validateManagedAncestors(trustedRoot, filepath.Dir(path)); err != nil {
		return nil, err
	}
	createdSidecars, err := prepareDataPath(path)
	if err != nil {
		return nil, err
	}
	busyTimeout := options.BusyTimeout
	if busyTimeout <= 0 {
		busyTimeout = 5 * time.Second
	}
	dsn := fmt.Sprintf("file:%s?_pragma=foreign_keys(1)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(%d)&_txlock=immediate", url.PathEscape(path), busyTimeout.Milliseconds())
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(8)
	db.SetMaxIdleConns(8)
	s := &Store{db: db, path: path}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("ping sqlite: %w", err)
	}
	if err := backupBeforeResourceMigration(ctx, db, path); err != nil {
		db.Close()
		return nil, err
	}
	if err := migrate(ctx, db); err != nil {
		db.Close()
		return nil, err
	}
	if err := secureCreatedSidecars(path, createdSidecars); err != nil {
		db.Close()
		return nil, err
	}
	s.beforeCommit = func() error {
		if err := validateManagedFiles(path); err != nil {
			return err
		}
		if hook != nil {
			return hook()
		}
		return nil
	}
	return s, nil
}

func anchorManagedPath(path string) (string, string, error) {
	for _, root := range []string{os.TempDir(), userHomeDir()} {
		if root == "" {
			continue
		}
		absoluteRoot, err := filepath.Abs(root)
		if err != nil {
			return "", "", err
		}
		relative, ok := relativeWithin(absoluteRoot, path)
		if !ok {
			continue
		}
		canonicalRoot, err := filepath.EvalSymlinks(absoluteRoot)
		if err != nil {
			return "", "", fmt.Errorf("resolve trusted sqlite root: %w", err)
		}
		return filepath.Join(canonicalRoot, relative), canonicalRoot, nil
	}
	root := string(filepath.Separator)
	if volume := filepath.VolumeName(path); volume != "" {
		root = volume + string(filepath.Separator)
	}
	return path, root, nil
}

func userHomeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

func relativeWithin(root, path string) (string, bool) {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	return relative, true
}

func validateManagedAncestors(root, target string) error {
	relative, ok := relativeWithin(root, target)
	if !ok {
		return fmt.Errorf("sqlite path escapes trusted root: %s", target)
	}
	current := root
	components := strings.Split(relative, string(filepath.Separator))
	for index, component := range components {
		if component == "" || component == "." {
			continue
		}
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if os.IsNotExist(err) && index == len(components)-1 {
			return nil
		}
		if err != nil {
			return fmt.Errorf("inspect sqlite path ancestor %s: %w", current, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("sqlite path ancestor is not a real directory: %s", current)
		}
	}
	return nil
}

func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}
func (store *Store) Reader() storecontract.Reader { return &reader{q: store.db} }

func (store *Store) WithinWriteTx(ctx context.Context, fn func(storecontract.WriteTx) error) error {
	if store == nil || store.db == nil || fn == nil {
		return fmt.Errorf("write transaction: invalid argument")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	w := &writeTx{reader: reader{q: tx}, tx: tx}
	if err := fn(w); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := store.beforeCommit(); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("%w: %v", storecontract.ErrCommitUnknown, err)
	}
	return nil
}

func validateManagedFiles(path string) error {
	dataInfo, err := os.Lstat(filepath.Dir(path))
	if err != nil {
		return err
	}
	if err := validateOwnedPath(filepath.Dir(path), dataInfo, true, 0); err != nil {
		return err
	}
	dbInfo, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if err := validateOwnedPath(path, dbInfo, false, 0o600); err != nil {
		return err
	}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		info, err := os.Lstat(sidecar)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if err := validateOwnedPath(sidecar, info, false, 0o600); err != nil {
			return err
		}
	}
	return nil
}

func (store *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	err := store.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version),0) FROM schema_migrations`).Scan(&version)
	return version, err
}

func (store *Store) JournalMode(ctx context.Context) (string, error) {
	var mode string
	err := store.db.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&mode)
	return strings.ToLower(mode), err
}

func (store *Store) CheckIntegrity(ctx context.Context) error {
	connections := make([]*sql.Conn, 0, 8)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()
	for i := 0; i < 8; i++ {
		connection, err := store.db.Conn(ctx)
		if err != nil {
			return err
		}
		connections = append(connections, connection)
		var enabled int
		if err := connection.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&enabled); err != nil {
			return err
		}
		if enabled != 1 {
			return fmt.Errorf("sqlite foreign_keys disabled on pooled connection %d", i)
		}
	}
	rows, err := connections[0].QueryContext(ctx, `PRAGMA foreign_key_check`)
	if err != nil {
		return err
	}
	defer rows.Close()
	if rows.Next() {
		return fmt.Errorf("sqlite foreign_key_check failed")
	}
	if err := rows.Err(); err != nil {
		return err
	}
	var result string
	if err := connections[0].QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("sqlite integrity_check: %s", result)
	}
	return nil
}

func prepareDataPath(path string) (map[string]bool, error) {
	dataDir := filepath.Dir(path)
	parent := filepath.Dir(dataDir)
	parentInfo, err := os.Lstat(parent)
	if err != nil {
		return nil, fmt.Errorf("inspect sqlite data parent: %w", err)
	}
	if parentInfo.Mode()&os.ModeSymlink != 0 || !parentInfo.IsDir() {
		return nil, fmt.Errorf("sqlite data parent is not a real directory: %s", parent)
	}
	info, err := os.Lstat(dataDir)
	if os.IsNotExist(err) {
		if err := os.Mkdir(dataDir, 0o700); err != nil {
			return nil, fmt.Errorf("create sqlite data directory: %w", err)
		}
	} else if err != nil {
		return nil, err
	} else if err := validateOwnedPath(dataDir, info, true, 0); err != nil {
		return nil, err
	}
	if info, err := os.Lstat(path); os.IsNotExist(err) {
		file, createErr := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0o600)
		if createErr != nil {
			return nil, fmt.Errorf("create sqlite database: %w", createErr)
		}
		if closeErr := file.Close(); closeErr != nil {
			return nil, closeErr
		}
	} else if err != nil {
		return nil, err
	} else if err := validateOwnedPath(path, info, false, 0o600); err != nil {
		return nil, err
	}
	created := map[string]bool{}
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		info, err := os.Lstat(sidecar)
		if os.IsNotExist(err) {
			created[sidecar] = true
			continue
		}
		if err != nil {
			return nil, err
		}
		if err := validateOwnedPath(sidecar, info, false, 0o600); err != nil {
			return nil, err
		}
	}
	return created, nil
}

func validateOwnedPath(path string, info os.FileInfo, directory bool, required os.FileMode) error {
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("sqlite path is symlink: %s", path)
	}
	if directory {
		if !info.IsDir() {
			return fmt.Errorf("sqlite data path is not directory: %s", path)
		}
		if info.Mode().Perm()&0o077 != 0 {
			return fmt.Errorf("sqlite data directory has group/world permissions: %s", path)
		}
	} else {
		if !info.Mode().IsRegular() {
			return fmt.Errorf("sqlite file is not regular: %s", path)
		}
		if info.Mode().Perm() != required {
			return fmt.Errorf("sqlite file mode is %#o, require %#o: %s", info.Mode().Perm(), required, path)
		}
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || int(stat.Uid) != os.Geteuid() {
		return fmt.Errorf("sqlite path is not owned by current user: %s", path)
	}
	return nil
}

func secureCreatedSidecars(path string, created map[string]bool) error {
	for _, sidecar := range []string{path + "-wal", path + "-shm"} {
		info, err := os.Lstat(sidecar)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if !created[sidecar] {
			if err := validateOwnedPath(sidecar, info, false, 0o600); err != nil {
				return err
			}
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("sqlite sidecar is not regular: %s", sidecar)
		}
		if err := os.Chmod(sidecar, 0o600); err != nil {
			return fmt.Errorf("secure sqlite sidecar %s: %w", sidecar, err)
		}
	}
	return nil
}

type queryer interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	QueryRowContext(context.Context, string, ...any) *sql.Row
}
type reader struct{ q queryer }
type writeTx struct {
	reader
	tx *sql.Tx
}

const sqliteTimestampLayout = "2006-01-02T15:04:05.000000000Z07:00"

func timeText(value time.Time) string           { return value.UTC().Format(sqliteTimestampLayout) }
func parseTime(value string) (time.Time, error) { return time.Parse(time.RFC3339Nano, value) }
func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return timeText(value)
}
func parseNullableTime(value sql.NullString) (time.Time, error) {
	if !value.Valid {
		return time.Time{}, nil
	}
	return parseTime(value.String)
}

func mapWriteError(err error) error {
	if err == nil {
		return nil
	}
	message := err.Error()
	switch {
	case strings.Contains(message, "one_active_run_per_task"), strings.Contains(message, "UNIQUE constraint failed: runs.task_id"):
		return fmt.Errorf("%w: %v", storecontract.ErrActiveRunExists, err)
	case strings.Contains(message, "run_events.run_id, run_events.sequence"):
		return fmt.Errorf("%w: %v", storecontract.ErrEventSequenceConflict, err)
	case strings.Contains(message, "repository_bindings.room_id"), strings.Contains(message, "project_repository_resources.project_id"), strings.Contains(message, "project_repository_resources.local_locator"):
		return fmt.Errorf("%w: %v", storecontract.ErrRepositoryBindingConflict, err)
	default:
		return err
	}
}
