-- Preserve historical rows and immutable snapshots. Only new short-branch
-- inserts use the exact repository authority already frozen in the snapshot.
DROP TRIGGER task_repository_worktrees_branch_authority_insert;
CREATE TRIGGER task_repository_worktrees_branch_authority_insert BEFORE INSERT ON task_repository_worktrees
WHEN NOT (
 (NEW.delivery_mode='' AND NEW.task_branch='' AND NEW.target_ref='') OR
 (NEW.delivery_mode='task_branch' AND substr(NEW.target_ref,1,11)='refs/heads/' AND length(NEW.target_ref)>11 AND (
  NEW.task_branch='chora/'||NEW.task_id||'/'||NEW.repo_id OR
  EXISTS (
   SELECT 1 FROM task_resource_snapshots s, json_each(s.canonical_json,'$.resources') r
   WHERE s.task_id=NEW.task_id
    AND json_extract(s.canonical_json,'$.taskId')=NEW.task_id
    AND json_extract(s.canonical_json,'$.branchType') IN ('feature','fix','chore','docs','test','refactor','perf','build','ci')
    AND substr(NEW.task_branch,1,length(json_extract(s.canonical_json,'$.branchType'))+1)=json_extract(s.canonical_json,'$.branchType')||'/'
    AND length(NEW.task_branch)=length(json_extract(s.canonical_json,'$.branchType'))+13
    AND substr(NEW.task_branch,length(json_extract(s.canonical_json,'$.branchType'))+2) NOT GLOB '*[^0-9a-f]*'
    AND json_extract(r.value,'$.repoId')=NEW.repo_id
    AND json_extract(r.value,'$.role')='write'
    AND json_extract(r.value,'$.deliveryMode')='task_branch'
    AND json_extract(r.value,'$.taskBranch')=NEW.task_branch
    AND json_extract(r.value,'$.baseRef')=NEW.target_ref
    AND json_extract(r.value,'$.baseCommit')=NEW.base_commit
    AND json_extract(r.value,'$.baseTree')=NEW.base_tree
  )
 ))
)
BEGIN SELECT RAISE(ABORT,'invalid Task branch authority'); END;
