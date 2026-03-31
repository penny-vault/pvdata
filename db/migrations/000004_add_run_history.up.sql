CREATE TABLE run_history (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    subscription_id UUID NOT NULL REFERENCES subscriptions(id) ON DELETE CASCADE,
    start_time TIMESTAMP NOT NULL,
    end_time TIMESTAMP NOT NULL,
    num_observations INTEGER NOT NULL DEFAULT 0,
    status TEXT NOT NULL CHECK (status IN ('success', 'failed')),
    created_on TIMESTAMP NOT NULL DEFAULT now()
);

CREATE INDEX idx_run_history_subscription_id ON run_history(subscription_id);
CREATE INDEX idx_run_history_start_time ON run_history(start_time);
