ALTER TABLE tasks ADD COLUMN model_binding TEXT;
ALTER TABLE run_charters ADD COLUMN model_binding TEXT;
ALTER TABLE attempts ADD COLUMN model_binding TEXT;
CREATE TRIGGER tasks_model_binding_immutable_update BEFORE UPDATE OF model_binding ON tasks WHEN OLD.model_binding IS NOT NEW.model_binding BEGIN SELECT RAISE(ABORT, 'Task model binding is immutable'); END;
CREATE TRIGGER run_charters_model_binding_immutable_update BEFORE UPDATE OF model_binding ON run_charters WHEN OLD.model_binding IS NOT NEW.model_binding BEGIN SELECT RAISE(ABORT, 'Run Charter model binding is immutable'); END;
CREATE TRIGGER attempts_model_binding_immutable_update BEFORE UPDATE OF model_binding ON attempts WHEN OLD.model_binding IS NOT NEW.model_binding BEGIN SELECT RAISE(ABORT, 'Attempt model binding is immutable'); END;
