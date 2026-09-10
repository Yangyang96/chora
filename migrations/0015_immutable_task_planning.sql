DROP TABLE task_plans;

CREATE TABLE technical_plan_drafts (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    edit_version INTEGER NOT NULL CHECK(edit_version >= 1),
    predecessor_revision_id TEXT REFERENCES technical_plan_revisions(id),
    next_revision_number INTEGER NOT NULL CHECK(next_revision_number >= 1),
    base_content_digest BLOB CHECK(base_content_digest IS NULL OR length(base_content_digest) = 32),
    selection_digest BLOB NOT NULL CHECK(length(selection_digest) = 32),
    content_json TEXT NOT NULL CHECK(json_valid(content_json) AND json_type(content_json) = 'object'),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    closed_at TEXT,
    submitted_revision_id TEXT REFERENCES technical_plan_revisions(id),
    CHECK(updated_at >= created_at),
    CHECK((closed_at IS NULL AND submitted_revision_id IS NULL) OR (closed_at = updated_at AND submitted_revision_id IS NOT NULL)),
    CHECK((next_revision_number = 1 AND predecessor_revision_id IS NULL AND base_content_digest IS NULL) OR (next_revision_number > 1 AND predecessor_revision_id IS NOT NULL AND base_content_digest IS NOT NULL))
);

CREATE UNIQUE INDEX one_open_technical_plan_draft_per_task
ON technical_plan_drafts(task_id) WHERE closed_at IS NULL;

CREATE TABLE technical_plan_revisions (
    id TEXT PRIMARY KEY,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    source_draft_id TEXT NOT NULL REFERENCES technical_plan_drafts(id),
    revision_number INTEGER NOT NULL CHECK(revision_number >= 1),
    predecessor_revision_id TEXT REFERENCES technical_plan_revisions(id),
    content_json TEXT NOT NULL CHECK(json_valid(content_json) AND json_type(content_json) = 'object'),
    content_digest BLOB NOT NULL CHECK(length(content_digest) = 32),
    selection_digest BLOB NOT NULL CHECK(length(selection_digest) = 32),
    unchanged INTEGER NOT NULL CHECK(unchanged IN (0,1)),
    submitted_at TEXT NOT NULL,
    UNIQUE(task_id, revision_number),
    UNIQUE(source_draft_id)
);

CREATE TRIGGER technical_plan_revision_lineage
BEFORE INSERT ON technical_plan_revisions
BEGIN
    SELECT CASE WHEN NEW.revision_number != COALESCE((SELECT MAX(revision_number) + 1 FROM technical_plan_revisions WHERE task_id = NEW.task_id), 1)
        THEN RAISE(ABORT, 'technical plan revision number is not monotonic') END;
    SELECT CASE WHEN NEW.revision_number = 1 AND (NEW.predecessor_revision_id IS NOT NULL OR NEW.unchanged != 0)
        THEN RAISE(ABORT, 'initial technical plan revision has invalid predecessor') END;
    SELECT CASE WHEN NEW.revision_number > 1 AND NOT EXISTS (
        SELECT 1 FROM technical_plan_revisions predecessor
        WHERE predecessor.id = NEW.predecessor_revision_id AND predecessor.task_id = NEW.task_id
          AND predecessor.revision_number = NEW.revision_number - 1
          AND predecessor.selection_digest = NEW.selection_digest
          AND ((NEW.unchanged = 1 AND predecessor.content_digest = NEW.content_digest)
            OR (NEW.unchanged = 0 AND predecessor.content_digest != NEW.content_digest))
    ) THEN RAISE(ABORT, 'technical plan revision predecessor mismatch') END;
    SELECT CASE WHEN NOT EXISTS (
        SELECT 1 FROM technical_plan_drafts draft
        WHERE draft.id = NEW.source_draft_id AND draft.task_id = NEW.task_id AND draft.closed_at IS NULL
          AND draft.next_revision_number = NEW.revision_number AND draft.selection_digest = NEW.selection_digest
          AND draft.content_json = NEW.content_json
          AND COALESCE(draft.predecessor_revision_id, '') = COALESCE(NEW.predecessor_revision_id, '')
    ) THEN RAISE(ABORT, 'technical plan revision source draft mismatch') END;
END;

CREATE TRIGGER technical_plan_revisions_no_update BEFORE UPDATE ON technical_plan_revisions
BEGIN SELECT RAISE(ABORT, 'technical plan revisions are immutable'); END;
CREATE TRIGGER technical_plan_revisions_no_delete BEFORE DELETE ON technical_plan_revisions
BEGIN SELECT RAISE(ABORT, 'technical plan revisions are immutable'); END;

CREATE TRIGGER closed_technical_plan_drafts_no_update
BEFORE UPDATE ON technical_plan_drafts WHEN OLD.closed_at IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'closed technical plan drafts are immutable'); END;

CREATE TABLE technical_plan_reviews (
    id TEXT PRIMARY KEY,
    revision_id TEXT NOT NULL UNIQUE REFERENCES technical_plan_revisions(id),
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK(kind IN ('accept','request_revision')),
    reviewer TEXT NOT NULL CHECK(length(trim(reviewer)) > 0),
    note TEXT NOT NULL CHECK(length(trim(note)) > 0),
    decided_at TEXT NOT NULL
);

CREATE TRIGGER technical_plan_review_exact_revision
BEFORE INSERT ON technical_plan_reviews
WHEN NOT EXISTS (SELECT 1 FROM technical_plan_revisions revision WHERE revision.id = NEW.revision_id AND revision.task_id = NEW.task_id)
BEGIN SELECT RAISE(ABORT, 'technical plan review task mismatch'); END;
CREATE TRIGGER technical_plan_reviews_no_update BEFORE UPDATE ON technical_plan_reviews
BEGIN SELECT RAISE(ABORT, 'technical plan reviews are append-only'); END;
CREATE TRIGGER technical_plan_reviews_no_delete BEFORE DELETE ON technical_plan_reviews
BEGIN SELECT RAISE(ABORT, 'technical plan reviews are append-only'); END;

CREATE TABLE technical_plan_acceptance_bindings (
    review_id TEXT PRIMARY KEY REFERENCES technical_plan_reviews(id),
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    revision_id TEXT NOT NULL UNIQUE REFERENCES technical_plan_revisions(id),
    snapshot_id TEXT NOT NULL REFERENCES context_snapshots(id),
    snapshot_digest BLOB NOT NULL CHECK(length(snapshot_digest) = 32),
    bound_at TEXT NOT NULL,
    UNIQUE(task_id, revision_id)
);

CREATE TRIGGER technical_plan_acceptance_exact
BEFORE INSERT ON technical_plan_acceptance_bindings
WHEN NOT EXISTS (
    SELECT 1 FROM technical_plan_reviews review
    JOIN context_snapshots snapshot ON snapshot.id = NEW.snapshot_id
    WHERE review.id = NEW.review_id AND review.kind = 'accept' AND review.task_id = NEW.task_id
      AND review.revision_id = NEW.revision_id AND snapshot.digest = NEW.snapshot_digest
)
BEGIN SELECT RAISE(ABORT, 'technical plan acceptance binding mismatch'); END;
CREATE TRIGGER technical_plan_acceptance_bindings_no_update BEFORE UPDATE ON technical_plan_acceptance_bindings
BEGIN SELECT RAISE(ABORT, 'technical plan acceptance bindings are immutable'); END;
CREATE TRIGGER technical_plan_acceptance_bindings_no_delete BEFORE DELETE ON technical_plan_acceptance_bindings
BEGIN SELECT RAISE(ABORT, 'technical plan acceptance bindings are immutable'); END;

CREATE TABLE current_technical_plan_acceptances (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    revision_id TEXT NOT NULL,
    FOREIGN KEY(task_id, revision_id) REFERENCES technical_plan_acceptance_bindings(task_id, revision_id)
);

CREATE TABLE technical_plan_run_bindings (
    run_id TEXT PRIMARY KEY REFERENCES runs(id) ON DELETE CASCADE,
    task_id TEXT NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
    revision_id TEXT NOT NULL REFERENCES technical_plan_revisions(id),
    charter_id TEXT NOT NULL REFERENCES run_charters(id),
    snapshot_id TEXT NOT NULL REFERENCES context_snapshots(id),
    snapshot_digest BLOB NOT NULL CHECK(length(snapshot_digest) = 32),
    bound_at TEXT NOT NULL
);

CREATE TRIGGER technical_plan_run_binding_exact
BEFORE INSERT ON technical_plan_run_bindings
WHEN NOT EXISTS (
    SELECT 1 FROM runs run
    JOIN run_charters charter ON charter.id = NEW.charter_id
    JOIN technical_plan_acceptance_bindings accepted ON accepted.task_id = NEW.task_id AND accepted.revision_id = NEW.revision_id
    WHERE run.id = NEW.run_id AND run.task_id = NEW.task_id AND run.charter_id = NEW.charter_id
      AND charter.task_id = NEW.task_id AND accepted.snapshot_id = NEW.snapshot_id
      AND accepted.snapshot_digest = NEW.snapshot_digest
)
BEGIN SELECT RAISE(ABORT, 'technical plan run binding mismatch'); END;
CREATE TRIGGER technical_plan_run_bindings_no_update BEFORE UPDATE ON technical_plan_run_bindings
BEGIN SELECT RAISE(ABORT, 'technical plan run bindings are immutable'); END;
CREATE TRIGGER technical_plan_run_bindings_no_delete BEFORE DELETE ON technical_plan_run_bindings
BEGIN SELECT RAISE(ABORT, 'technical plan run bindings are immutable'); END;
