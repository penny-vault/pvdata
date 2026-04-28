-- Allow 'running' as a third status value alongside 'success' / 'failed'.
-- The original constraint in 000004 was created inline; Postgres auto-named
-- it so we discover the actual name from pg_constraint rather than assuming.
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
    CHECK (status IN ('running', 'success', 'failed'));
