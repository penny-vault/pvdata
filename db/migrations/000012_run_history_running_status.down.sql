-- Demote any in-flight 'running' rows so the tighter constraint can be restored.
UPDATE run_history SET status = 'failed' WHERE status = 'running';

DO $$
DECLARE
    name_var TEXT;
BEGIN
    SELECT conname INTO name_var
    FROM pg_constraint
    WHERE conrelid = 'run_history'::regclass
      AND contype = 'c'
      AND pg_get_constraintdef(oid) ILIKE '%status%';

    IF name_var IS NOT NULL THEN
        EXECUTE 'ALTER TABLE run_history DROP CONSTRAINT ' || quote_ident(name_var);
    END IF;
END$$;

ALTER TABLE run_history
    ADD CONSTRAINT run_history_status_check
    CHECK (status IN ('success', 'failed'));
