BEGIN;

DROP TABLE IF EXISTS published_views;

CREATE TABLE dataframe (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    data_type datatype NOT NULL,
    partitioned BOOLEAN DEFAULT false,
    subscriptions TEXT[],
    UNIQUE(name)
);

COMMIT;
