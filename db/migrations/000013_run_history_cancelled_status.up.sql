-- Allow 'cancelled' as a fourth status value alongside 'running' / 'success' / 'failed'.
-- See 000012 for the same constraint-discovery pattern (the original constraint
-- name is auto-assigned by Postgres so we look it up via pg_constraint).
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
    CHECK (status IN ('running', 'success', 'failed', 'cancelled'));
