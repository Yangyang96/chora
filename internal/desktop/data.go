package desktop

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
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Yangyang96/chora/migrations"
	_ "modernc.org/sqlite"
)

const ManifestSchema = "chora.desktop-backup.v1"
const SupportedSchema = 43

type SchemaInspection struct {
	DataRoot      string `json:"dataRoot"`
	Database      string `json:"database"`
	Schema        int    `json:"schema"`
	Supported     int    `json:"supported"`
	Compatibility string `json:"compatibility"`
}

func InspectSchema(ctx context.Context, dataRoot string, supported int) (SchemaInspection, error) {
	if supported != SupportedSchema {
		return SchemaInspection{}, errors.New("supported schema must match this build")
	}
	root, err := exactAbsolute(dataRoot)
	if err != nil {
		return SchemaInspection{}, err
	}
	lock, err := AcquireOwner(root)
	if err != nil {
		return SchemaInspection{}, err
	}
	defer lock.Close()
	return inspectSchemaUnlocked(ctx, root, supported)
}

func inspectSchemaUnlocked(ctx context.Context, root string, supported int) (SchemaInspection, error) {
	result := SchemaInspection{DataRoot: root, Database: "missing", Supported: supported, Compatibility: "missing"}
	snapshot, cleanup, err := snapshotDatabase(root)
	if err != nil {
		return result, err
	}
	defer cleanup()
	dbPath := filepath.Join(snapshot, "chora.db")
	if info, statErr := os.Lstat(dbPath); statErr != nil {
		if os.IsNotExist(statErr) {
			return result, nil
		}
		return result, statErr
	} else if !info.Mode().IsRegular() {
		return result, errors.New("database is not a regular file")
	}
	dsn := (&url.URL{Scheme: "file", Path: dbPath, RawQuery: "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(1000)"}).String()
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return result, err
	}
	defer db.Close()
	queryCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var integrity string
	if err = db.QueryRowContext(queryCtx, "PRAGMA quick_check(1)").Scan(&integrity); err != nil || integrity != "ok" {
		return result, errors.New("database integrity check failed")
	}
	rows, err := db.QueryContext(queryCtx, "SELECT version,name,checksum FROM schema_migrations ORDER BY version")
	if err != nil {
		return result, fmt.Errorf("read migration ledger: %w", err)
	}
	defer rows.Close()
	expectedEntries, _ := fs.ReadDir(migrations.Files, ".")
	expected := map[int]struct {
		name string
		sum  [32]byte
	}{}
	for _, entry := range expectedEntries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		prefix := strings.SplitN(entry.Name(), "_", 2)[0]
		version, parseErr := strconv.Atoi(prefix)
		if parseErr != nil {
			continue
		}
		body, _ := migrations.Files.ReadFile(entry.Name())
		expected[version] = struct {
			name string
			sum  [32]byte
		}{entry.Name(), sha256.Sum256(body)}
	}
	for rows.Next() {
		var version int
		var name string
		var checksum []byte
		if err = rows.Scan(&version, &name, &checksum); err != nil {
			return result, err
		}
		if version > supported {
			result.Schema = version
			result.Database = "available"
			result.Compatibility = "newer_than_supported"
			return result, nil
		}
		want, ok := expected[version]
		if !ok || name != want.name || !bytes.Equal(checksum, want.sum[:]) {
			result.Compatibility = "unknown"
			return result, nil
		}
		if version != result.Schema+1 {
			result.Compatibility = "unknown"
			return result, nil
		}
		result.Schema = version
	}
	if err = rows.Err(); err != nil {
		return result, err
	}
	if result.Schema == 0 {
		result.Compatibility = "unknown"
		return result, nil
	}
	result.Database = "available"
	switch {
	case result.Schema == supported:
		result.Compatibility = "current"
	case result.Schema < supported:
		result.Compatibility = "upgrade_required"
	default:
		result.Compatibility = "newer_than_supported"
	}
	return result, nil
}

type Manifest struct {
	Schema          string  `json:"schema"`
	CreatedAt       string  `json:"createdAt"`
	DataRoot        string  `json:"dataRoot"`
	Entries         []Entry `json:"entries"`
	AggregateSHA256 string  `json:"aggregateSha256"`
}
type Entry struct {
	Path       string `json:"path"`
	Kind       string `json:"kind"`
	Mode       uint32 `json:"mode"`
	Size       int64  `json:"size,omitempty"`
	SHA256     string `json:"sha256,omitempty"`
	LinkTarget string `json:"linkTarget,omitempty"`
}

func Backup(dataRoot, output string) (Manifest, error) {
	root, err := exactAbsolute(dataRoot)
	if err != nil {
		return Manifest{}, err
	}
	dst, err := exactAbsolute(output)
	if err != nil {
		return Manifest{}, err
	}
	if root == dst || strings.HasPrefix(dst+string(os.PathSeparator), root+string(os.PathSeparator)) {
		return Manifest{}, errors.New("backup must be outside data root")
	}
	lock, err := AcquireOwner(root)
	if err != nil {
		return Manifest{}, err
	}
	defer lock.Close()
	if err = ensureStopped(root); err != nil {
		return Manifest{}, err
	}
	if err = requireIdleIfDatabase(root); err != nil {
		return Manifest{}, err
	}
	if err = rejectSymlink(root); err != nil {
		return Manifest{}, fmt.Errorf("data root: %w", err)
	}
	if _, err = os.Lstat(dst); !os.IsNotExist(err) {
		return Manifest{}, errors.New("backup output already exists")
	}
	tmp := dst + ".partial"
	if _, err = os.Lstat(tmp); !os.IsNotExist(err) {
		return Manifest{}, errors.New("partial backup already exists")
	}
	if err = os.Mkdir(tmp, 0700); err != nil {
		return Manifest{}, err
	}
	ok := false
	defer func() {
		if !ok {
			_ = os.RemoveAll(tmp)
		}
	}()
	manifest := Manifest{Schema: ManifestSchema, CreatedAt: time.Now().UTC().Format(time.RFC3339Nano), DataRoot: root}
	if err = copyTree(root, filepath.Join(tmp, "data"), &manifest.Entries); err != nil {
		return Manifest{}, err
	}
	manifest.AggregateSHA256 = aggregate(manifest.Entries)
	b, _ := json.MarshalIndent(manifest, "", "  ")
	b = append(b, '\n')
	if err = os.WriteFile(filepath.Join(tmp, "manifest.json"), b, 0600); err != nil {
		return Manifest{}, err
	}
	if err = os.Rename(tmp, dst); err != nil {
		return Manifest{}, err
	}
	ok = true
	return manifest, nil
}

// Restore replaces only the exact root named in the backup manifest. Existing
// data is moved to a timestamped recovery directory and never deleted.
func Restore(dataRoot, backup string) (string, error) {
	root, err := exactAbsolute(dataRoot)
	if err != nil {
		return "", err
	}
	src, err := exactAbsolute(backup)
	if err != nil {
		return "", err
	}
	lock, err := AcquireOwner(root)
	if err != nil {
		return "", err
	}
	defer lock.Close()
	if err = ensureStopped(root); err != nil {
		return "", err
	}
	if err = requireIdleIfDatabase(root); err != nil {
		return "", err
	}
	if err = rejectSymlink(src); err != nil {
		return "", err
	}
	manifest, err := readManifest(src)
	if err != nil {
		return "", err
	}
	if manifest.DataRoot != root {
		return "", errors.New("backup belongs to a different absolute data root")
	}
	if err = verifyTree(filepath.Join(src, "data"), manifest); err != nil {
		return "", err
	}
	recovery := ""
	if _, err = os.Lstat(root); err == nil {
		recovery = fmt.Sprintf("%s.recovery-%s", root, time.Now().UTC().Format("20060102T150405.000000000Z"))
		if err = os.Rename(root, recovery); err != nil {
			return "", err
		}
	} else if !os.IsNotExist(err) {
		return "", err
	}
	tmp, err := os.MkdirTemp(filepath.Dir(root), "."+filepath.Base(root)+".restore-")
	if err != nil {
		if recovery != "" {
			_ = os.Rename(recovery, root)
		}
		return recovery, err
	}
	if err = os.Remove(tmp); err != nil {
		if recovery != "" {
			_ = os.Rename(recovery, root)
		}
		return recovery, err
	}
	if err = copyTreeNoManifest(filepath.Join(src, "data"), tmp); err != nil {
		if recovery != "" {
			_ = os.Rename(recovery, root)
		}
		return recovery, err
	}
	if err = os.Rename(tmp, root); err != nil {
		_ = os.RemoveAll(tmp)
		if recovery != "" {
			_ = os.Rename(recovery, root)
		}
		return recovery, err
	}
	return recovery, nil
}

func requireIdleIfDatabase(root string) error {
	if _, err := os.Lstat(filepath.Join(root, "chora.db")); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	snapshot, cleanup, err := snapshotDatabase(root)
	if err != nil {
		return err
	}
	defer cleanup()
	report, err := activityAt(context.Background(), snapshot, root)
	if err != nil {
		return err
	}
	if !report.Idle {
		return errors.New("data root has active work")
	}
	return nil
}

type Adoption struct {
	Schema     string `json:"schema"`
	DataRoot   string `json:"dataRoot"`
	BackupPath string `json:"backupPath"`
	AdoptedAt  string `json:"adoptedAt"`
}

// Adopt records explicit consent to let the packaged app use a source
// Workbench data root in place. It first makes a complete backup; absolute
// repository and worktree references therefore remain unchanged.
func Adopt(source, backup string) (Adoption, error) {
	src, e := exactAbsolute(source)
	if e != nil {
		return Adoption{}, e
	}
	if _, e = Backup(src, backup); e != nil {
		return Adoption{}, e
	}
	lock, e := AcquireOwner(src)
	if e != nil {
		return Adoption{}, e
	}
	defer lock.Close()
	if e = ensureStopped(src); e != nil {
		return Adoption{}, e
	}
	inspection, e := inspectSchemaUnlocked(context.Background(), src, SupportedSchema)
	if e != nil {
		return Adoption{}, e
	}
	if inspection.Compatibility == "newer_than_supported" || inspection.Compatibility == "unknown" {
		return Adoption{}, errors.New("source data schema is incompatible with this Chora version")
	}
	snapshot, cleanup, e := snapshotDatabase(src)
	if e != nil {
		return Adoption{}, e
	}
	defer cleanup()
	activity, e := activityAt(context.Background(), snapshot, src)
	if e != nil || !activity.Idle {
		if e != nil {
			return Adoption{}, e
		}
		return Adoption{}, errors.New("source data has active work")
	}
	a := Adoption{Schema: "chora.desktop-adoption.v1", DataRoot: src, BackupPath: backup, AdoptedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	b, _ := json.MarshalIndent(a, "", "  ")
	b = append(b, '\n')
	tmp := filepath.Join(src, ".desktop-adoption.json.tmp")
	if e = os.WriteFile(tmp, b, 0600); e != nil {
		return Adoption{}, e
	}
	if e = os.Rename(tmp, filepath.Join(src, "desktop-adoption.json")); e != nil {
		_ = os.Remove(tmp)
		return Adoption{}, e
	}
	return a, nil
}

func copyTree(src, dst string, entries *[]Entry) error { return walkCopy(src, dst, entries) }
func copyTreeNoManifest(src, dst string) error {
	var entries []Entry
	return walkCopy(src, dst, &entries)
}
func walkCopy(src, dst string, entries *[]Entry) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, e := filepath.Rel(src, path)
		if e != nil {
			return e
		}
		if rel == "." {
			return os.Mkdir(dst, 0700)
		}
		if !fs.ValidPath(filepath.ToSlash(rel)) {
			return errors.New("invalid backup path")
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		target := filepath.Join(dst, rel)
		entry := Entry{Path: filepath.ToSlash(rel), Mode: uint32(info.Mode().Perm())}
		switch {
		case d.Type()&os.ModeSymlink != 0:
			link, e := os.Readlink(path)
			if e != nil {
				return e
			}
			entry.Kind = "symlink"
			entry.LinkTarget = link
			sum := sha256.Sum256([]byte(link))
			entry.SHA256 = hex.EncodeToString(sum[:])
			if e = os.MkdirAll(filepath.Dir(target), 0700); e == nil {
				e = os.Symlink(link, target)
			}
			if e != nil {
				return e
			}
		case info.IsDir():
			entry.Kind = "directory"
			if e = os.Mkdir(target, info.Mode().Perm()); e != nil {
				return e
			}
		case info.Mode().IsRegular():
			entry.Kind = "file"
			entry.Size = info.Size()
			sum, e := copyFileHash(path, target, info.Mode().Perm())
			if e != nil {
				return e
			}
			entry.SHA256 = sum
		default:
			return fmt.Errorf("unsupported file type at %s", rel)
		}
		*entries = append(*entries, entry)
		return nil
	})
}
func copyFileHash(src, dst string, mode fs.FileMode) (string, error) {
	in, e := os.Open(src)
	if e != nil {
		return "", e
	}
	defer in.Close()
	out, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if e != nil {
		return "", e
	}
	h := sha256.New()
	_, copyErr := io.Copy(io.MultiWriter(out, h), in)
	closeErr := out.Close()
	if copyErr != nil {
		return "", copyErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
func aggregate(entries []Entry) string {
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	h := sha256.New()
	for _, e := range entries {
		fmt.Fprintf(h, "%s\x00%s\x00%d\x00%d\x00%s\x00%s\n", e.Path, e.Kind, e.Mode, e.Size, e.SHA256, e.LinkTarget)
	}
	return hex.EncodeToString(h.Sum(nil))
}
func readManifest(root string) (Manifest, error) {
	b, e := os.ReadFile(filepath.Join(root, "manifest.json"))
	if e != nil {
		return Manifest{}, e
	}
	var m Manifest
	if e = json.Unmarshal(b, &m); e != nil {
		return m, e
	}
	if m.Schema != ManifestSchema {
		return m, errors.New("unsupported backup manifest")
	}
	if m.AggregateSHA256 != aggregate(m.Entries) {
		return m, errors.New("backup manifest aggregate mismatch")
	}
	return m, nil
}
func verifyTree(root string, m Manifest) error {
	var observed []Entry
	parent, e := os.MkdirTemp(filepath.Dir(root), ".verify-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(parent)
	if e = copyTree(root, filepath.Join(parent, "data"), &observed); e != nil {
		return e
	}
	if aggregate(observed) != m.AggregateSHA256 {
		return errors.New("backup content hash mismatch")
	}
	return nil
}

// Query a stopped database copy: SQLite mode=ro may still create WAL/SHM files.
// Keep the original byte-for-byte intact, including uncheckpointed WAL content.
func snapshotDatabase(root string) (string, func(), error) {
	if err := ensureStopped(root); err != nil {
		return "", nil, err
	}
	dir, err := os.MkdirTemp("", "chora-db-inspect-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	for _, name := range []string{"chora.db", "chora.db-wal", "chora.db-shm"} {
		source := filepath.Join(root, name)
		info, err := os.Lstat(source)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			cleanup()
			return "", nil, err
		}
		if !info.Mode().IsRegular() {
			cleanup()
			return "", nil, errors.New("database snapshot requires regular files")
		}
		if _, err = copyFileHash(source, filepath.Join(dir, name), 0600); err != nil {
			cleanup()
			return "", nil, err
		}
	}
	return dir, cleanup, nil
}
