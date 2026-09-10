package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Yangyang96/chora/internal/domain"
)

func TestSQLiteTrustedTextValidatorMatchesDomain(t *testing.T) {
	tests := []struct {
		name     string
		value    string
		maxRunes int
		required bool
	}{
		{name: "required text", value: " trusted ", maxRunes: 7, required: true},
		{name: "optional empty", value: "", maxRunes: 1, required: false},
		{name: "ascii whitespace required", value: " \t\n", maxRunes: 3, required: true},
		{name: "nbsp required", value: "\u00a0\u00a0", maxRunes: 2, required: true},
		{name: "fullwidth space required", value: "\u3000\u3000", maxRunes: 2, required: true},
		{name: "unicode exact limit", value: "界界", maxRunes: 2, required: true},
		{name: "unicode over limit", value: "界界界", maxRunes: 2, required: true},
		{name: "nul", value: "a\x00b", maxRunes: 3, required: true},
		{name: "nul before oversized suffix", value: "a\x00" + strings.Repeat("界", 3), maxRunes: 2, required: true},
		{name: "invalid UTF-8", value: string([]byte{0xff}), maxRunes: 1, required: true},
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got int
			required := 0
			if tt.required {
				required = 1
			}
			if err := db.QueryRow(`SELECT chora_valid_trusted_text(CAST(? AS BLOB),?,?)`, tt.value, tt.maxRunes, required).Scan(&got); err != nil {
				t.Fatal(err)
			}
			want := 0
			if domain.ValidTrustedContextText(tt.value, tt.maxRunes, tt.required) {
				want = 1
			}
			if got != want {
				t.Fatalf("SQLite validator = %d, Domain validator = %d", got, want)
			}
		})
	}
}

func TestSQLiteRevisionProvenanceValidatorMatchesDomainRestoreBoundary(t *testing.T) {
	validAccepted := fmt.Sprintf(`{"kind":"accepted_run_candidate","candidate_id":%q,"candidate_decision_id":%q,"source_run_id":%q,"source_review_id":%q,"source_artifact_id":%q,"actor":"owner"}`,
		domain.NewCandidateID().String(), domain.NewCandidateDecisionID().String(), domain.NewRunID().String(), domain.NewReviewDecisionID().String(), domain.NewArtifactID().String())
	tests := []struct {
		name  string
		value any
		want  int
	}{
		{name: "human room", value: []byte(`{"kind":"human_room","actor":"owner"}`), want: 1},
		{name: "accepted run candidate", value: []byte(validAccepted), want: 1},
		{name: "numeric actor", value: []byte(`{"kind":"human_room","actor":123}`)},
		{name: "null actor", value: []byte(`{"kind":"human_room","actor":null}`)},
		{name: "boolean actor", value: []byte(`{"kind":"human_room","actor":true}`)},
		{name: "array actor", value: []byte(`{"kind":"human_room","actor":[]}`)},
		{name: "object actor", value: []byte(`{"kind":"human_room","actor":{}}`)},
		{name: "invalid kind", value: []byte(`{"kind":"system","actor":"owner"}`)},
		{name: "human room candidate fields", value: []byte(fmt.Sprintf(`{"kind":"human_room","candidate_id":%q,"actor":"owner"}`, domain.NewCandidateID().String()))},
		{name: "accepted candidate missing IDs", value: []byte(`{"kind":"accepted_run_candidate","actor":"owner"}`)},
		{name: "accepted candidate forged ID", value: []byte(`{"kind":"accepted_run_candidate","candidate_id":"candidate_forged","candidate_decision_id":"candidate_decision_forged","source_run_id":"run_forged","source_review_id":"review_decision_forged","source_artifact_id":"artifact_forged","actor":"owner"}`)},
		{name: "unknown field", value: []byte(`{"kind":"human_room","actor":"owner","authority":"forged"}`)},
		{name: "trailing JSON", value: []byte(`{"kind":"human_room","actor":"owner"}{}`)},
		{name: "not JSON", value: []byte(`not-json`)},
		{name: "invalid UTF-8", value: []byte{'{', '"', 'k', 'i', 'n', 'd', '"', ':', '"', 'h', 'u', 'm', 'a', 'n', '_', 'r', 'o', 'o', 'm', '"', ',', '"', 'a', 'c', 't', 'o', 'r', '"', ':', '"', 0xff, '"', '}'}},
	}
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got int
			if err := db.QueryRow(`SELECT chora_valid_revision_provenance(?)`, tt.value).Scan(&got); err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("chora_valid_revision_provenance() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestTrustedContextSchemaRejectsMaliciousUnicodeAndReopens(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "trusted-text.db")
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	digest := make([]byte, 32)
	oversizedTitleAfterNUL := "x\x00" + strings.Repeat("界", domain.MaxCandidateTitleRunes+1)
	oversizedBodyAfterNUL := "x\x00" + strings.Repeat("界", domain.MaxCandidateBodyRunes+1)
	oversizedNoteAfterNUL := "x\x00" + strings.Repeat("界", domain.MaxCandidateDecisionNoteRunes+1)
	unicodeWhitespace := "\u00a0\u3000"
	badProvenance := []byte(`{"kind":"human_room","actor":"\u00a0\u3000"}`)
	invalidUTF8 := []byte{0xff}

	tests := []struct {
		name  string
		query string
		args  []any
	}{
		{name: "candidate title NUL suffix", query: `INSERT INTO candidates(id,room_id,source_run_id,source_review_id,source_artifact_id,title,body,state,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, args: []any{"candidate-title", "room", "run", "review", "artifact-title", oversizedTitleAfterNUL, "body", "pending", 1, "now", "now"}},
		{name: "candidate title invalid UTF-8", query: `INSERT INTO candidates(id,room_id,source_run_id,source_review_id,source_artifact_id,title,body,state,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, args: []any{"candidate-invalid-utf8", "room", "run", "review", "artifact-invalid-utf8", invalidUTF8, "body", "pending", 1, "now", "now"}},
		{name: "candidate body NUL suffix", query: `INSERT INTO candidates(id,room_id,source_run_id,source_review_id,source_artifact_id,title,body,state,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, args: []any{"candidate-body", "room", "run", "review", "artifact-body", "title", oversizedBodyAfterNUL, "pending", 1, "now", "now"}},
		{name: "candidate title Unicode whitespace", query: `INSERT INTO candidates(id,room_id,source_run_id,source_review_id,source_artifact_id,title,body,state,version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, args: []any{"candidate-space", "room", "run", "review", "artifact-space", unicodeWhitespace, "body", "pending", 1, "now", "now"}},
		{name: "edit previous title NUL suffix", query: `INSERT INTO candidate_edits(candidate_id,from_version,to_version,previous_title,previous_body,edited_title,edited_body,edited_at) VALUES(?,?,?,?,?,?,?,?)`, args: []any{"candidate", 1, 2, oversizedTitleAfterNUL, "body", "title", "body", "now"}},
		{name: "edit previous body NUL suffix", query: `INSERT INTO candidate_edits(candidate_id,from_version,to_version,previous_title,previous_body,edited_title,edited_body,edited_at) VALUES(?,?,?,?,?,?,?,?)`, args: []any{"candidate", 1, 2, "title", oversizedBodyAfterNUL, "title", "body", "now"}},
		{name: "edit title Unicode whitespace", query: `INSERT INTO candidate_edits(candidate_id,from_version,to_version,previous_title,previous_body,edited_title,edited_body,edited_at) VALUES(?,?,?,?,?,?,?,?)`, args: []any{"candidate", 1, 2, "title", "body", unicodeWhitespace, "body", "now"}},
		{name: "edit body NUL suffix", query: `INSERT INTO candidate_edits(candidate_id,from_version,to_version,previous_title,previous_body,edited_title,edited_body,edited_at) VALUES(?,?,?,?,?,?,?,?)`, args: []any{"candidate", 1, 2, "title", "body", "title", oversizedBodyAfterNUL, "now"}},
		{name: "decision note NUL suffix", query: `INSERT INTO candidate_decisions(id,candidate_id,kind,expected_candidate_version,note,actor_id,session_id,decided_at) VALUES(?,?,?,?,?,?,?,?)`, args: []any{"decision-note", "candidate-note", "confirm", 1, oversizedNoteAfterNUL, "actor", "session", "now"}},
		{name: "dismiss Unicode whitespace note", query: `INSERT INTO candidate_decisions(id,candidate_id,kind,expected_candidate_version,note,actor_id,session_id,decided_at) VALUES(?,?,?,?,?,?,?,?)`, args: []any{"decision-dismiss", "candidate-dismiss", "dismiss", 1, unicodeWhitespace, "actor", "session", "now"}},
		{name: "decision actor Unicode whitespace", query: `INSERT INTO candidate_decisions(id,candidate_id,kind,expected_candidate_version,note,actor_id,session_id,decided_at) VALUES(?,?,?,?,?,?,?,?)`, args: []any{"decision-actor", "candidate-actor", "confirm", 1, "", unicodeWhitespace, "session", "now"}},
		{name: "decision session Unicode whitespace", query: `INSERT INTO candidate_decisions(id,candidate_id,kind,expected_candidate_version,note,actor_id,session_id,decided_at) VALUES(?,?,?,?,?,?,?,?)`, args: []any{"decision-session", "candidate-session", "confirm", 1, "", "actor", unicodeWhitespace, "now"}},
		{name: "revision actor Unicode whitespace", query: `INSERT INTO room_revision_records(revision_id,room_id,provenance_kind,actor,digest,confirmed_at) VALUES(?,?,?,?,?,?)`, args: []any{"revision", "room", "human_room", unicodeWhitespace, digest, "now"}},
		{name: "selection provenance actor Unicode whitespace", query: `INSERT INTO task_revision_selections(task_id,room_id,revision_id,position,digest,provenance_json,selected_at) VALUES(?,?,?,?,?,?,?)`, args: []any{"task", "room", "revision", 0, digest, badProvenance, "now"}},
		{name: "exclusion provenance actor Unicode whitespace", query: `INSERT INTO task_revision_exclusions(task_id,room_id,revision_id,position,digest,provenance_json,reason,selected_at) VALUES(?,?,?,?,?,?,?,?)`, args: []any{"task", "room", "revision", 0, digest, badProvenance, "reason", "now"}},
		{name: "exclusion reason Unicode whitespace", query: `INSERT INTO task_revision_exclusions(task_id,room_id,revision_id,position,digest,provenance_json,reason,selected_at) VALUES(?,?,?,?,?,?,?,?)`, args: []any{"task", "room", "revision", 0, digest, []byte(`{"kind":"human_room","actor":"owner"}`), unicodeWhitespace, "now"}},
		{name: "snapshot provenance actor Unicode whitespace", query: `INSERT INTO snapshot_revision_manifest(snapshot_id,revision_id,disposition,position,digest,provenance_json,reason) VALUES(?,?,?,?,?,?,?)`, args: []any{"snapshot", "revision", "selected", 0, digest, badProvenance, ""}},
		{name: "snapshot exclusion reason Unicode whitespace", query: `INSERT INTO snapshot_revision_manifest(snapshot_id,revision_id,disposition,position,digest,provenance_json,reason) VALUES(?,?,?,?,?,?,?)`, args: []any{"snapshot", "revision", "excluded", 0, digest, []byte(`{"kind":"human_room","actor":"owner"}`), unicodeWhitespace}},
	}
	provenanceCases := []struct {
		name  string
		value []byte
	}{
		{name: "numeric actor", value: []byte(`{"kind":"human_room","actor":123}`)},
		{name: "null actor", value: []byte(`{"kind":"human_room","actor":null}`)},
		{name: "boolean actor", value: []byte(`{"kind":"human_room","actor":true}`)},
		{name: "array actor", value: []byte(`{"kind":"human_room","actor":[]}`)},
		{name: "object actor", value: []byte(`{"kind":"human_room","actor":{}}`)},
		{name: "invalid kind", value: []byte(`{"kind":"system","actor":"owner"}`)},
		{name: "human room candidate fields", value: []byte(fmt.Sprintf(`{"kind":"human_room","candidate_id":%q,"actor":"owner"}`, domain.NewCandidateID().String()))},
		{name: "accepted candidate missing IDs", value: []byte(`{"kind":"accepted_run_candidate","actor":"owner"}`)},
		{name: "accepted candidate forged ID", value: []byte(`{"kind":"accepted_run_candidate","candidate_id":"candidate_forged","candidate_decision_id":"candidate_decision_forged","source_run_id":"run_forged","source_review_id":"review_decision_forged","source_artifact_id":"artifact_forged","actor":"owner"}`)},
		{name: "not JSON", value: []byte(`not-json`)},
		{name: "invalid UTF-8", value: invalidUTF8},
	}
	for _, provenance := range provenanceCases {
		tests = append(tests,
			struct {
				name  string
				query string
				args  []any
			}{name: "selection provenance " + provenance.name, query: `INSERT INTO task_revision_selections(task_id,room_id,revision_id,position,digest,provenance_json,selected_at) VALUES(?,?,?,?,?,?,?)`, args: []any{"task", "room", "revision", 0, digest, provenance.value, "now"}},
			struct {
				name  string
				query string
				args  []any
			}{name: "exclusion provenance " + provenance.name, query: `INSERT INTO task_revision_exclusions(task_id,room_id,revision_id,position,digest,provenance_json,reason,selected_at) VALUES(?,?,?,?,?,?,?,?)`, args: []any{"task", "room", "revision", 0, digest, provenance.value, "reason", "now"}},
			struct {
				name  string
				query string
				args  []any
			}{name: "snapshot provenance " + provenance.name, query: `INSERT INTO snapshot_revision_manifest(snapshot_id,revision_id,disposition,position,digest,provenance_json,reason) VALUES(?,?,?,?,?,?,?)`, args: []any{"snapshot", "revision", "selected", 0, digest, provenance.value, ""}},
		)
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := db.ExecContext(ctx, tt.query, tt.args...); err == nil {
				t.Fatalf("malicious trusted text write %d succeeded", index)
			} else if !strings.Contains(err.Error(), "CHECK constraint failed") {
				t.Fatalf("write failed for the wrong reason: %v", err)
			}
		})
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(ctx, path)
	if err != nil {
		t.Fatalf("reopen after rejected writes: %v", err)
	}
	defer store.Close()
	if err := store.CheckIntegrity(ctx); err != nil {
		t.Fatalf("integrity after rejected writes: %v", err)
	}
	for _, table := range []string{"candidates", "candidate_edits", "candidate_decisions", "room_revision_records", "task_revision_selections", "task_revision_exclusions", "snapshot_revision_manifest"} {
		var count int
		if err := store.db.QueryRowContext(ctx, fmt.Sprintf("SELECT count(*) FROM %s", table)).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Fatalf("%s contains %d rows after rejected writes", table, count)
		}
	}
}
