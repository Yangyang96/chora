CREATE TABLE task_plans (
    task_id TEXT PRIMARY KEY REFERENCES tasks(id) ON DELETE CASCADE,
    version INTEGER NOT NULL CHECK(version >= 1),
    status TEXT NOT NULL CHECK(status IN ('draft','accepted','revision_requested')),
    requirement TEXT NOT NULL CHECK(length(trim(requirement)) > 0 AND instr(requirement,char(0)) = 0),
    desired_behavior_json TEXT NOT NULL CHECK(json_valid(desired_behavior_json) AND json_type(desired_behavior_json) = 'array' AND json_array_length(desired_behavior_json) >= 1),
    out_of_scope_json TEXT NOT NULL CHECK(json_valid(out_of_scope_json) AND json_type(out_of_scope_json) = 'array' AND json_array_length(out_of_scope_json) >= 1),
    acceptance_criteria_json TEXT NOT NULL CHECK(json_valid(acceptance_criteria_json) AND json_type(acceptance_criteria_json) = 'array' AND json_array_length(acceptance_criteria_json) >= 1),
    technical_plan_json TEXT NOT NULL CHECK(json_valid(technical_plan_json) AND json_type(technical_plan_json) = 'array' AND json_array_length(technical_plan_json) >= 1),
    decisions_json TEXT NOT NULL CHECK(json_valid(decisions_json) AND json_type(decisions_json) = 'array' AND json_array_length(decisions_json) >= 1),
    risks_json TEXT NOT NULL CHECK(json_valid(risks_json) AND json_type(risks_json) = 'array' AND json_array_length(risks_json) >= 1),
    unknowns_json TEXT NOT NULL CHECK(json_valid(unknowns_json) AND json_type(unknowns_json) = 'array' AND json_array_length(unknowns_json) >= 1),
    review_note TEXT,
    reviewer TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    reviewed_at TEXT,
    CHECK(updated_at >= created_at),
    CHECK(
        (status = 'draft' AND version = 1 AND review_note IS NULL AND reviewer IS NULL AND reviewed_at IS NULL AND updated_at = created_at)
        OR
        (status IN ('accepted','revision_requested') AND version >= 2 AND length(trim(review_note)) > 0 AND length(trim(reviewer)) > 0 AND reviewed_at IS NOT NULL AND reviewed_at = updated_at)
    )
);

INSERT INTO task_plans(
    task_id, version, status, requirement, desired_behavior_json,
    out_of_scope_json, acceptance_criteria_json, technical_plan_json,
    decisions_json, risks_json, unknowns_json, created_at, updated_at
)
SELECT
    tasks.id,
    1,
    'draft',
    tasks.goal,
    (SELECT json_group_array(title) FROM (SELECT title FROM task_criteria WHERE task_id = tasks.id ORDER BY position)),
    json_array('Undeclared or unselected work'),
    (SELECT json_group_array(json_object('id', criterion_id, 'title', title, 'description', description)) FROM (SELECT criterion_id, title, description FROM task_criteria WHERE task_id = tasks.id ORDER BY position)),
    json_array('Inspect frozen boundary', 'Implement ' || tasks.title, 'Run acceptance checks and report Unknowns'),
    json_array('Use frozen Task scope as implementation authority'),
    json_array('Undeclared dependencies may block implementation'),
    json_array('Implementation details outside frozen Task scope remain unknown'),
    tasks.created_at,
    tasks.created_at
FROM tasks
WHERE EXISTS (SELECT 1 FROM task_criteria WHERE task_id = tasks.id);
