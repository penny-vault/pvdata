BEGIN;

-- Enum Types

CREATE TYPE assettype AS ENUM (
    'CS',   -- Common Stock
    'PS',   -- Preferred Stock
    'ETF',  -- Exchange Traded Fund
    'ETN',  -- Exchange Traded Note
    'MF',   -- Mutual Fund
    'CEF',  -- Closed End Fund
    'ADRC', -- American Depository Receipt Common
    'FRED', -- Federal Reserve Economic Data
    'SYNTH' -- Synthetic Data
);

CREATE TYPE datatype AS ENUM (
    'asset-description',
    'analyst-rating',
    'custom',
    'economic-indicator',
    'eod',
    'fundamental',
    'market-holidays',
    'metric',
    'rating'
);

-- Tables

CREATE TABLE library (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name text,
    owner text
);

CREATE TABLE subscriptions (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,

    provider TEXT NOT NULL,
    dataset TEXT NOT NULL,
    config JSONB NOT NULL,

    data_tables TEXT[] NOT NULL,
    data_types datatype[] NOT NULL,

    total_records INTEGER DEFAULT 0,
    num_records_last_import INTEGER DEFAULT 0,

    total_securities INTEGER DEFAULT 0,
    num_securities_last_import INTEGER DEFAULT 0,

    first_obs_date TIMESTAMP, -- First observation date
    last_obs_date TIMESTAMP,  -- Last observation date

    schedule TEXT NOT NULL,
    health_check_id TEXT,
    last_run TIMESTAMP,
    active BOOLEAN DEFAULT true,
    schema_version INTEGER DEFAULT 0,

    created_on TIMESTAMP DEFAULT now(),
    created_by TEXT NOT NULL
);

CREATE TABLE dataframe (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    data_type datatype NOT NULL,
    partitioned BOOLEAN DEFAULT false,
    subscriptions TEXT[],

    UNIQUE(name)
);

-- Functions

CREATE OR REPLACE FUNCTION adj_close_default()
  RETURNS trigger
  LANGUAGE plpgsql AS
$func$
BEGIN
   NEW.adj_close := NEW.close;
   RETURN NEW;
END
$func$;

CREATE VIEW market_holidays AS SELECT generate_series(date'2024-01-01', date'2024-01-01', interval '1 day') AS event_date, 'NYSE'::text AS market WHERE false;

CREATE OR REPLACE FUNCTION trading_days(DATE, DATE)
RETURNS SETOF DATE
LANGUAGE SQL
AS $func$
  SELECT dt::date FROM generate_series($1, $2, interval '1' day) as t(dt) WHERE extract(dow FROM dt) BETWEEN 1 AND 5 AND dt NOT IN (SELECT event_date FROM market_holidays WHERE market='NYSE')
$func$;

DROP VIEW market_holidays;

CREATE OR REPLACE FUNCTION locf_state(FLOAT, FLOAT)
RETURNS FLOAT
LANGUAGE SQL
AS $func$
  SELECT COALESCE($2,$1)
$func$;

CREATE AGGREGATE locf(FLOAT) (
  SFUNC = locf_state,
  STYPE = FLOAT
);

COMMIT;
