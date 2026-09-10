-- Preserve historical Task worktree bases while recording immutable authority
-- for newly admitted Tasks at their current committed HEAD.
ALTER TABLE task_worktrees ADD COLUMN pinned_base_tree TEXT NOT NULL DEFAULT '';
ALTER TABLE task_worktrees ADD COLUMN base_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE task_worktrees ADD COLUMN start_policy TEXT NOT NULL DEFAULT 'legacy_admitted_base'
  CHECK(start_policy IN ('current_committed_head','legacy_admitted_base'));

DROP TRIGGER task_worktrees_immutable_binding;

CREATE TRIGGER task_worktrees_base_authority_insert BEFORE INSERT ON task_worktrees
WHEN NOT (
  (NEW.start_policy='legacy_admitted_base' AND NEW.pinned_base_tree='' AND NEW.base_ref='')
  OR
  (NEW.start_policy='current_committed_head'
    AND length(NEW.pinned_base_tree) IN (40,64)
    AND NEW.pinned_base_tree NOT GLOB '*[^0-9a-f]*'
    AND (
      NEW.base_ref='HEAD'
      OR (
        substr(NEW.base_ref,1,11)='refs/heads/' AND length(NEW.base_ref)>11
        AND NEW.base_ref=trim(NEW.base_ref)
        AND instr(NEW.base_ref,' ')=0 AND instr(NEW.base_ref,'~')=0
        AND instr(NEW.base_ref,'^')=0 AND instr(NEW.base_ref,':')=0
        AND instr(NEW.base_ref,'?')=0 AND instr(NEW.base_ref,'*')=0
        AND instr(NEW.base_ref,'[')=0 AND instr(NEW.base_ref,'\')=0
        AND instr(NEW.base_ref,char(0))=0 AND instr(NEW.base_ref,char(1))=0
        AND instr(NEW.base_ref,char(2))=0 AND instr(NEW.base_ref,char(3))=0
        AND instr(NEW.base_ref,char(4))=0 AND instr(NEW.base_ref,char(5))=0
        AND instr(NEW.base_ref,char(6))=0 AND instr(NEW.base_ref,char(7))=0
        AND instr(NEW.base_ref,char(8))=0 AND instr(NEW.base_ref,char(9))=0
        AND instr(NEW.base_ref,char(10))=0 AND instr(NEW.base_ref,char(11))=0
        AND instr(NEW.base_ref,char(12))=0 AND instr(NEW.base_ref,char(13))=0
        AND instr(NEW.base_ref,char(14))=0 AND instr(NEW.base_ref,char(15))=0
        AND instr(NEW.base_ref,char(16))=0 AND instr(NEW.base_ref,char(17))=0
        AND instr(NEW.base_ref,char(18))=0 AND instr(NEW.base_ref,char(19))=0
        AND instr(NEW.base_ref,char(20))=0 AND instr(NEW.base_ref,char(21))=0
        AND instr(NEW.base_ref,char(22))=0 AND instr(NEW.base_ref,char(23))=0
        AND instr(NEW.base_ref,char(24))=0 AND instr(NEW.base_ref,char(25))=0
        AND instr(NEW.base_ref,char(26))=0 AND instr(NEW.base_ref,char(27))=0
        AND instr(NEW.base_ref,char(28))=0 AND instr(NEW.base_ref,char(29))=0
        AND instr(NEW.base_ref,char(30))=0 AND instr(NEW.base_ref,char(31))=0
        AND instr(NEW.base_ref,char(127))=0
        AND instr(NEW.base_ref,'//')=0 AND instr(NEW.base_ref,'..')=0
        AND instr(NEW.base_ref,'@{')=0
        AND substr(NEW.base_ref,12,1)<>'.' AND instr(NEW.base_ref,'/.')=0
        AND NEW.base_ref NOT GLOB '*.lock' AND NEW.base_ref NOT GLOB '*.lock/*'
        AND substr(NEW.base_ref,-1)<>'/' AND substr(NEW.base_ref,-1)<>'.'
      )
    )
  )
)
BEGIN
  SELECT RAISE(ABORT, 'invalid Task worktree base authority');
END;

CREATE TRIGGER task_worktrees_base_authority_update BEFORE UPDATE ON task_worktrees
WHEN NOT (
  (NEW.start_policy='legacy_admitted_base' AND NEW.pinned_base_tree='' AND NEW.base_ref='')
  OR
  (NEW.start_policy='current_committed_head'
    AND length(NEW.pinned_base_tree) IN (40,64)
    AND NEW.pinned_base_tree NOT GLOB '*[^0-9a-f]*'
    AND (NEW.base_ref='HEAD' OR (
      substr(NEW.base_ref,1,11)='refs/heads/' AND length(NEW.base_ref)>11
      AND NEW.base_ref=trim(NEW.base_ref)
      AND instr(NEW.base_ref,' ')=0 AND instr(NEW.base_ref,'~')=0
      AND instr(NEW.base_ref,'^')=0 AND instr(NEW.base_ref,':')=0
      AND instr(NEW.base_ref,'?')=0 AND instr(NEW.base_ref,'*')=0
      AND instr(NEW.base_ref,'[')=0 AND instr(NEW.base_ref,'\')=0
      AND instr(NEW.base_ref,char(0))=0 AND instr(NEW.base_ref,char(1))=0
      AND instr(NEW.base_ref,char(2))=0 AND instr(NEW.base_ref,char(3))=0
      AND instr(NEW.base_ref,char(4))=0 AND instr(NEW.base_ref,char(5))=0
      AND instr(NEW.base_ref,char(6))=0 AND instr(NEW.base_ref,char(7))=0
      AND instr(NEW.base_ref,char(8))=0 AND instr(NEW.base_ref,char(9))=0
      AND instr(NEW.base_ref,char(10))=0 AND instr(NEW.base_ref,char(11))=0
      AND instr(NEW.base_ref,char(12))=0 AND instr(NEW.base_ref,char(13))=0
      AND instr(NEW.base_ref,char(14))=0 AND instr(NEW.base_ref,char(15))=0
      AND instr(NEW.base_ref,char(16))=0 AND instr(NEW.base_ref,char(17))=0
      AND instr(NEW.base_ref,char(18))=0 AND instr(NEW.base_ref,char(19))=0
      AND instr(NEW.base_ref,char(20))=0 AND instr(NEW.base_ref,char(21))=0
      AND instr(NEW.base_ref,char(22))=0 AND instr(NEW.base_ref,char(23))=0
      AND instr(NEW.base_ref,char(24))=0 AND instr(NEW.base_ref,char(25))=0
      AND instr(NEW.base_ref,char(26))=0 AND instr(NEW.base_ref,char(27))=0
      AND instr(NEW.base_ref,char(28))=0 AND instr(NEW.base_ref,char(29))=0
      AND instr(NEW.base_ref,char(30))=0 AND instr(NEW.base_ref,char(31))=0
      AND instr(NEW.base_ref,char(127))=0
      AND instr(NEW.base_ref,'//')=0 AND instr(NEW.base_ref,'..')=0
      AND instr(NEW.base_ref,'@{')=0
      AND substr(NEW.base_ref,12,1)<>'.' AND instr(NEW.base_ref,'/.')=0
      AND NEW.base_ref NOT GLOB '*.lock' AND NEW.base_ref NOT GLOB '*.lock/*'
      AND substr(NEW.base_ref,-1)<>'/' AND substr(NEW.base_ref,-1)<>'.'
    ))
  )
)
BEGIN
  SELECT RAISE(ABORT, 'invalid Task worktree base authority');
END;

CREATE TRIGGER task_worktrees_immutable_binding BEFORE UPDATE ON task_worktrees
WHEN OLD.task_id<>NEW.task_id OR OLD.repository_identity<>NEW.repository_identity
  OR OLD.pinned_base_revision<>NEW.pinned_base_revision OR OLD.pinned_base_tree<>NEW.pinned_base_tree
  OR OLD.base_ref<>NEW.base_ref OR OLD.start_policy<>NEW.start_policy
  OR OLD.relative_locator<>NEW.relative_locator
  OR OLD.configured_root_fingerprint<>NEW.configured_root_fingerprint OR OLD.created_at<>NEW.created_at
BEGIN
  SELECT RAISE(ABORT, 'Task worktree binding is immutable');
END;
