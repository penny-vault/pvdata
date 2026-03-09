BEGIN;

DROP TABLE IF EXISTS dataframe;

CREATE TABLE published_views (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    view_name TEXT NOT NULL UNIQUE,
    data_type_key TEXT NOT NULL,
    sources JSONB NOT NULL DEFAULT '[]'
);

COMMIT;
