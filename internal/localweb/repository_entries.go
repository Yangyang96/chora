package localweb

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	pathpkg "path"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/Yangyang96/chora/internal/domain"
	"github.com/Yangyang96/chora/internal/gitsource"
	storecontract "github.com/Yangyang96/chora/internal/store"
)

const (
	repositoryEntryCursorVersion = 1
	// A search may skip many siblings, but one request never scans an
	// unbounded directory tail. The continuation cursor advances over every
	// inspected record, including records that do not match the query.
	repositoryEntryScanLimit = 10_000
	// Git paths created through an ordinary filesystem are much smaller. This
	// also bounds a deliberately malformed tree record before it is retained.
	repositoryEntryRecordBytes = 16 << 10
	// Leave room under the central 1 MiB metadata limit for cursor and JSON
	// framing. Entry strings are the only retained Git output.
	repositoryEntryPayloadBytes = domain.RepositoryMetadataBytes - 128<<10
)

var (
	errRepositoryEntryCursor      = errors.New("repository directory cursor is invalid or stale")
	errRepositoryEntryIdentity    = errors.New("repository identity changed; original association preserved")
	errRepositoryEntryUnborn      = errors.New("repository has no committed revision")
	errRepositoryEntryUnavailable = errors.New("repository directory is unavailable")
	errRepositoryEntryRecord      = errors.New("repository directory entry exceeds the metadata limit")
)

type repositoryEntryView struct {
	Name string `json:"name"`
	Path string `json:"path"`
	Kind string `json:"kind"`
}

type repositoryEntriesView struct {
	Entries    []repositoryEntryView `json:"entries"`
	NextCursor string                `json:"nextCursor"`
	Truncated  bool                  `json:"truncated"`
	Revision   string                `json:"revision"`
}

type repositoryEntryPage struct {
	entries     []repositoryEntryView
	resumeAfter []byte
	hasMore     bool
	truncated   bool
}

type repositoryEntryCursor struct {
	Version  int    `json:"v"`
	RepoID   string `json:"repository"`
	Revision string `json:"revision"`
	Path     string `json:"path"`
	Query    string `json:"query"`
	After    string `json:"after"`
	Digest   string `json:"digest"`
}

func (server *Server) getRepositoryEntries(writer http.ResponseWriter, request *http.Request) {
	ctx, cancel := context.WithTimeout(request.Context(), domain.RepositoryMetadataTimeout)
	defer cancel()
	request = request.WithContext(ctx)

	association, err := server.repositoryFromRoute(request)
	if err != nil {
		writeProjectError(writer, err)
		return
	}
	if association.State != "active" {
		writeProjectError(writer, storecontract.ErrRoomStateForbidden)
		return
	}
	directory, query, limit, err := parseRepositoryEntryQuery(request)
	if err != nil {
		writeRepositoryEntryError(writer, err)
		return
	}
	inspection, err := server.inspectRepositoryEntries(ctx, association)
	if err != nil {
		writeRepositoryEntryError(writer, err)
		return
	}

	var after []byte
	if raw := request.URL.Query().Get("cursor"); raw != "" {
		after, err = decodeRepositoryEntryCursor(raw, association.Repository.ID.String(), inspection.HeadCommit, directory, query)
		if err != nil {
			writeRepositoryEntryError(writer, err)
			return
		}
	}
	page, err := readRepositoryEntryPage(ctx, inspection.CanonicalPath, inspection.HeadCommit, directory, query, after, limit)
	if err != nil {
		writeRepositoryEntryError(writer, err)
		return
	}

	// Detect a checkout replacement or HEAD change during the Git query before
	// any repository names become an HTTP response.
	afterInspection, err := server.inspectRepositoryEntries(ctx, association)
	if err != nil {
		writeRepositoryEntryError(writer, err)
		return
	}
	if afterInspection.CanonicalPath != inspection.CanonicalPath || afterInspection.CommonGitDir != inspection.CommonGitDir ||
		afterInspection.PhysicalIdentity != inspection.PhysicalIdentity ||
		afterInspection.HeadCommit != inspection.HeadCommit {
		writeRepositoryEntryError(writer, errRepositoryEntryIdentity)
		return
	}

	next := ""
	if page.hasMore {
		if len(page.resumeAfter) == 0 {
			writeRepositoryEntryError(writer, errRepositoryEntryRecord)
			return
		}
		next, err = encodeRepositoryEntryCursor(association.Repository.ID.String(), inspection.HeadCommit, directory, query, page.resumeAfter)
		if err != nil {
			writeRepositoryEntryError(writer, err)
			return
		}
	}
	writeJSON(writer, http.StatusOK, repositoryEntriesView{Entries: page.entries, NextCursor: next, Truncated: page.truncated, Revision: inspection.HeadCommit})
}

func parseRepositoryEntryQuery(request *http.Request) (string, string, int, error) {
	directory := request.URL.Query().Get("path")
	if directory == "" {
		directory = "."
	}
	if len(directory) > 4096 || directory != "." && !safeProjectSettingsPath(directory) {
		return "", "", 0, fmt.Errorf("%w: unsafe repository directory", domain.ErrInvalidArgument)
	}
	query := request.URL.Query().Get("query")
	if len(query) > 1024 || !utf8.ValidString(query) || strings.IndexFunc(query, unicode.IsControl) >= 0 {
		return "", "", 0, fmt.Errorf("%w: invalid repository directory query", domain.ErrInvalidArgument)
	}
	limit := domain.RepositoryPageSize
	if raw := request.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > domain.RepositoryMaxPageSize {
			return "", "", 0, fmt.Errorf("%w: invalid repository directory page size", domain.ErrInvalidArgument)
		}
		limit = value
	}
	return directory, query, limit, nil
}

func (server *Server) inspectRepositoryEntries(ctx context.Context, association domain.ProjectRepository) (gitsource.Inspection, error) {
	if server.repositorySource == nil {
		return gitsource.Inspection{}, errRepositoryEntryUnavailable
	}
	repository := association.Repository
	inspection, err := server.repositorySource.Inspect(ctx, repository.Checkout)
	if err != nil {
		if ctx.Err() != nil {
			return gitsource.Inspection{}, ctx.Err()
		}
		return gitsource.Inspection{}, fmt.Errorf("%w: inspection failed", errRepositoryEntryUnavailable)
	}
	if inspection.CanonicalPath != repository.Checkout {
		return gitsource.Inspection{}, errRepositoryEntryIdentity
	}
	switch repository.IdentitySource {
	case "inspected":
		if inspection.CommonGitDir != repository.CommonGitDir || inspection.PhysicalIdentity != repository.PhysicalIdentity {
			return gitsource.Inspection{}, errRepositoryEntryIdentity
		}
	case "legacy_unverified":
		if err := gitsource.ProveRevision(ctx, inspection.CanonicalPath, repository.LegacyCommit, repository.LegacyTree); err != nil {
			if ctx.Err() != nil {
				return gitsource.Inspection{}, ctx.Err()
			}
			return gitsource.Inspection{}, errRepositoryEntryIdentity
		}
	default:
		return gitsource.Inspection{}, errRepositoryEntryIdentity
	}
	if inspection.Unborn {
		return gitsource.Inspection{}, errRepositoryEntryUnborn
	}
	if !validRepositoryEntryRevision(inspection.HeadCommit) {
		return gitsource.Inspection{}, fmt.Errorf("%w: invalid committed revision", errRepositoryEntryUnavailable)
	}
	return inspection, nil
}

func readRepositoryEntryPage(ctx context.Context, root, revision, directory, query string, after []byte, limit int) (repositoryEntryPage, error) {
	if err := ctx.Err(); err != nil {
		return repositoryEntryPage{}, err
	}
	if !validRepositoryEntryRevision(revision) || directory != "." && !safeProjectSettingsPath(directory) || limit < 1 || limit > domain.RepositoryMaxPageSize {
		return repositoryEntryPage{}, domain.ErrInvalidArgument
	}
	commandCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	treeish := revision
	if directory != "." {
		treeish += ":" + directory
	}
	command := isolatedGitCommand(commandCtx, root, "ls-tree", "-z", treeish)
	stdout, err := command.StdoutPipe()
	if err != nil {
		return repositoryEntryPage{}, err
	}
	command.Stderr = io.Discard
	if err := command.Start(); err != nil {
		return repositoryEntryPage{}, err
	}

	reader := bufio.NewReaderSize(stdout, 32<<10)
	page := repositoryEntryPage{entries: make([]repositoryEntryView, 0, limit)}
	seeking := len(after) > 0
	queryFolded := strings.ToLower(query)
	scanned := 0
	payloadBytes := 0
	stoppedEarly := false
	var readErr error
	for {
		record, recordErr := readNULRecord(reader, repositoryEntryRecordBytes)
		if errors.Is(recordErr, io.EOF) && len(record) == 0 {
			break
		}
		if recordErr != nil {
			readErr = recordErr
			break
		}
		entry, rawName, parseErr := parseRepositoryTreeEntry(record, directory)
		if parseErr != nil {
			readErr = parseErr
			break
		}
		if seeking {
			if bytes.Equal(rawName, after) {
				seeking = false
				page.resumeAfter = bytes.Clone(rawName)
			}
			continue
		}
		if scanned >= repositoryEntryScanLimit {
			page.hasMore = true
			page.truncated = true
			stoppedEarly = true
			break
		}
		scanned++
		matches := entry.Name != "" && (queryFolded == "" || strings.Contains(strings.ToLower(entry.Name), queryFolded))
		if matches {
			if len(page.entries) >= limit {
				page.hasMore = true
				stoppedEarly = true
				break
			}
			encoded, marshalErr := json.Marshal(entry)
			if marshalErr != nil {
				readErr = marshalErr
				break
			}
			if payloadBytes+len(encoded) > repositoryEntryPayloadBytes {
				page.hasMore = true
				page.truncated = true
				stoppedEarly = true
				break
			}
			payloadBytes += len(encoded)
			page.entries = append(page.entries, entry)
		}
		page.resumeAfter = bytes.Clone(rawName)
	}
	if seeking && readErr == nil {
		readErr = errRepositoryEntryCursor
	}
	if stoppedEarly || readErr != nil {
		cancel()
	}
	waitErr := command.Wait()
	if ctx.Err() != nil {
		return repositoryEntryPage{}, ctx.Err()
	}
	if readErr != nil {
		return repositoryEntryPage{}, readErr
	}
	if !stoppedEarly && waitErr != nil {
		return repositoryEntryPage{}, fmt.Errorf("%w: committed directory cannot be read", errRepositoryEntryUnavailable)
	}
	return page, nil
}

func readNULRecord(reader *bufio.Reader, limit int) ([]byte, error) {
	record := make([]byte, 0, 256)
	exceeded := false
	for {
		fragment, err := reader.ReadSlice(0)
		content := fragment
		if len(content) > 0 && content[len(content)-1] == 0 {
			content = content[:len(content)-1]
		}
		if !exceeded {
			if len(record)+len(content) > limit {
				exceeded = true
				record = nil
			} else {
				record = append(record, content...)
			}
		}
		if err == nil {
			if exceeded {
				return nil, errRepositoryEntryRecord
			}
			return record, nil
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			if exceeded {
				return nil, errRepositoryEntryRecord
			}
			return record, io.EOF
		}
		return nil, err
	}
}

func parseRepositoryTreeEntry(record []byte, directory string) (repositoryEntryView, []byte, error) {
	tab := bytes.IndexByte(record, '\t')
	if tab < 0 || tab == len(record)-1 {
		return repositoryEntryView{}, nil, errRepositoryEntryRecord
	}
	fields := bytes.Fields(record[:tab])
	if len(fields) != 3 {
		return repositoryEntryView{}, nil, errRepositoryEntryRecord
	}
	rawName := record[tab+1:]
	if len(rawName) == 0 {
		return repositoryEntryView{}, nil, errRepositoryEntryRecord
	}
	name := string(rawName)
	entryPath := name
	if directory != "." {
		entryPath = pathpkg.Join(directory, name)
	}
	// Unsafe or non-UTF-8 Git names cannot become a selectable scope. They are
	// still consumed so a continuation cursor always advances.
	if !utf8.Valid(rawName) || !safeProjectSettingsPath(entryPath) {
		return repositoryEntryView{}, rawName, nil
	}
	kind := "unsupported"
	mode, objectType := string(fields[0]), string(fields[1])
	if mode == "040000" && objectType == "tree" {
		kind = "directory"
	} else if (mode == "100644" || mode == "100755") && objectType == "blob" {
		kind = "file"
	}
	return repositoryEntryView{Name: name, Path: entryPath, Kind: kind}, rawName, nil
}

func encodeRepositoryEntryCursor(repoID, revision, directory, query string, after []byte) (string, error) {
	if !validRepositoryEntryRevision(revision) || len(after) == 0 || len(after) > repositoryEntryRecordBytes {
		return "", errRepositoryEntryCursor
	}
	cursor := repositoryEntryCursor{Version: repositoryEntryCursorVersion, RepoID: repoID, Revision: revision, Path: directory, Query: query, After: base64.RawURLEncoding.EncodeToString(after)}
	unsigned, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(append([]byte("chora.repository-entry-cursor.v1\x00"), unsigned...))
	cursor.Digest = hex.EncodeToString(digest[:])
	encoded, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(encoded), nil
}

func decodeRepositoryEntryCursor(raw, repoID, revision, directory, query string) ([]byte, error) {
	if len(raw) > repositoryEntryRecordBytes*3 {
		return nil, errRepositoryEntryCursor
	}
	encoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return nil, errRepositoryEntryCursor
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var cursor repositoryEntryCursor
	if err := decoder.Decode(&cursor); err != nil {
		return nil, errRepositoryEntryCursor
	}
	if err := ensureRepositoryEntryCursorEOF(decoder); err != nil {
		return nil, errRepositoryEntryCursor
	}
	digest := cursor.Digest
	cursor.Digest = ""
	unsigned, err := json.Marshal(cursor)
	if err != nil {
		return nil, errRepositoryEntryCursor
	}
	expected := sha256.Sum256(append([]byte("chora.repository-entry-cursor.v1\x00"), unsigned...))
	provided, err := hex.DecodeString(digest)
	if err != nil || len(provided) != sha256.Size || subtle.ConstantTimeCompare(provided, expected[:]) != 1 ||
		cursor.Version != repositoryEntryCursorVersion || cursor.RepoID != repoID || cursor.Revision != revision || cursor.Path != directory || cursor.Query != query {
		return nil, errRepositoryEntryCursor
	}
	after, err := base64.RawURLEncoding.DecodeString(cursor.After)
	if err != nil || len(after) == 0 || len(after) > repositoryEntryRecordBytes {
		return nil, errRepositoryEntryCursor
	}
	return after, nil
}

func ensureRepositoryEntryCursorEOF(decoder *json.Decoder) error {
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errRepositoryEntryCursor
	}
	return nil
}

func validRepositoryEntryRevision(revision string) bool {
	if len(revision) != 40 {
		return false
	}
	for _, character := range revision {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func writeRepositoryEntryError(writer http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrInvalidArgument), errors.Is(err, errRepositoryEntryCursor):
		writeError(writer, http.StatusBadRequest, err)
	case errors.Is(err, errRepositoryEntryIdentity):
		writeError(writer, http.StatusConflict, err)
	case errors.Is(err, errRepositoryEntryUnborn):
		writeError(writer, http.StatusUnprocessableEntity, err)
	case errors.Is(err, errRepositoryEntryRecord), errors.Is(err, errGitMetadataLimit):
		writeError(writer, http.StatusRequestEntityTooLarge, err)
	case errors.Is(err, context.DeadlineExceeded):
		writeError(writer, http.StatusGatewayTimeout, err)
	case errors.Is(err, context.Canceled):
		writeError(writer, http.StatusRequestTimeout, err)
	case errors.Is(err, errRepositoryEntryUnavailable):
		writeError(writer, http.StatusServiceUnavailable, err)
	default:
		writeError(writer, http.StatusInternalServerError, err)
	}
}
