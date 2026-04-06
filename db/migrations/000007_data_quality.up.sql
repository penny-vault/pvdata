CREATE TABLE data_quality_issues (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    check_name      TEXT NOT NULL,
    severity        TEXT NOT NULL,
    data_type       TEXT NOT NULL,
    ticker          TEXT,
    composite_figi  TEXT,
    dimension       TEXT,
    event_date      DATE,
    field           TEXT,
    message         TEXT NOT NULL,
    expected        TEXT,
    actual          TEXT,
    subscription_id UUID,
    run_id          UUID,
    detected_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX dqi_ticker_idx ON data_quality_issues (composite_figi, event_date);
CREATE INDEX dqi_check_idx ON data_quality_issues (check_name, detected_at);

CREATE TABLE audit_checkpoints (
    check_name      TEXT PRIMARY KEY,
    last_run        TIMESTAMPTZ NOT NULL,
    last_event_date DATE
);
