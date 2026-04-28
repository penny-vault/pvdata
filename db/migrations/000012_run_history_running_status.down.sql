-- Downgrade: remove 'running' status, convert any running rows to 'failed'.
UPDATE run_history SET status = 'failed' WHERE status = 'running';

ALTER TABLE run_history DROP CONSTRAINT IF EXISTS run_history_status_check;
ALTER TABLE run_history
    ADD CONSTRAINT run_history_status_check
    CHECK (status IN ('success', 'failed'));
