-- Allow 'running' status in run_history. Running rows store end_time = start_time
-- as a placeholder; the UI computes live duration from now() - start_time.
ALTER TABLE run_history DROP CONSTRAINT IF EXISTS run_history_status_check;
ALTER TABLE run_history
    ADD CONSTRAINT run_history_status_check
    CHECK (status IN ('running', 'success', 'failed'));
