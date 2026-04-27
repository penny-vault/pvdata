-- Capture per-run log output. Logs are streamed to connected SSE clients
-- in real time and persisted here for retrospection. A background sweep
-- nulls out the column for run_history rows older than 30 days.
ALTER TABLE run_history ADD COLUMN log TEXT;
