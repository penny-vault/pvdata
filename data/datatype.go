// Copyright 2024
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package data

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

type StatusType int

const (
	StatusUnknown StatusType = iota
	RunFailed
	RunSuccess
	RunInProgress
)

type RunSummary struct {
	StartTime        time.Time
	EndTime          time.Time
	NumObservations  int
	Status           StatusType
	SubscriptionID   uuid.UUID
	SubscriptionName string
}

type Observation struct {
	AssetObject       *Asset
	Consensus         *Consensus
	CustomObject      *Custom
	EconomicIndicator *EconomicIndicator
	EodQuote          *Eod
	Estimate          *Estimate
	Fundamental       *Fundamental
	IndexChange       *IndexChange
	IndexSnapshot     *IndexSnapshot
	MarketHoliday     *MarketHoliday
	Metric            *Metric
	Rating            *AnalystRating

	ObservationDate  time.Time
	SubscriptionID   uuid.UUID
	SubscriptionName string
}

// ViewGenerator produces the SELECT ... FROM <table> portion of a published-view
// leg. When a DataType has a non-nil ViewGenerator, it is used in place of the
// default "SELECT * FROM <table>" — required for types whose published columns
// differ from the underlying storage (e.g. lookup-table foreign keys that must
// be joined back to their textual values).
type ViewGenerator interface {
	SelectFrom(tableName string) string
}

type DataType struct {
	Name              string
	ViewName          string
	Schema            string
	Migrations        []string
	Version           int
	IsPartitioned     bool
	PartitionInterval string
	ViewGenerator     ViewGenerator

	// DateColumn names the column used for FromDate/UntilDate WHERE bounds in
	// a published view leg. An empty string disables date bounds entirely
	// (asset descriptions have no date axis). Examples: "event_date" for most
	// time-series types; "snapshot_date" for IndexSnapshotKey.
	DateColumn string

	// DedupKeys, when non-empty, switches the published-view generator from
	// a plain UNION ALL into a priority-dedup form that emits each unique
	// key tuple exactly once, taking the row from the highest-priority
	// source (sources[0] is highest priority). The keys are column names
	// referenced in NOT EXISTS anti-joins between legs.
	DedupKeys []string
}

const (
	PartitionInterval5Year   = ""
	PartitionIntervalMonthly = "monthly"
)

const (
	AssetKey             = "asset-description"
	ConsensusKey         = "consensus"
	CustomKey            = "custom"
	EconomicIndicatorKey = "economic-indicator"
	EODKey               = "eod"
	EstimateKey          = "estimate"
	FundamentalsKey      = "fundamental"
	IndexSnapshotKey     = "index-snapshot"
	IndexChangelogKey    = "index-changelog"
	MarketHolidaysKey    = "market-holidays"
	MetricKey            = "metric"
	QuoteKey             = "quote"
	RatingKey            = "rating"
)

var DataTypes = map[string]*DataType{
	AssetKey: {
		Name:       AssetKey,
		ViewName:   "assets",
		DateColumn: "",
		Schema: `CREATE TABLE %[1]s (
ticker TEXT,
composite_figi TEXT,
share_class_figi TEXT,
primary_exchange TEXT,
asset_type assettype,
active BOOLEAN,
name TEXT,
description TEXT,
corporate_url TEXT,
sector TEXT,
industry TEXT,
sic_code INT,
cik TEXT,
cusips text[],
isins text[],
other_identifiers JSONB,
similar_tickers TEXT[],
tags TEXT[],
listed timestamp,
delisted timestamp,
last_updated timestamp,
icon_url TEXT,
logo_url TEXT,
PRIMARY KEY (ticker, composite_figi)
);

CREATE INDEX %[1]s_active ON %[1]s(active);

ALTER TABLE %[1]s
ADD COLUMN search tsvector
	GENERATED ALWAYS AS (
		setweight(to_tsvector('pg_catalog.english', coalesce(ticker,'')), 'A') ||
		setweight(to_tsvector('pg_catalog.english', coalesce(name,'')), 'B') ||
		setweight(to_tsvector('pg_catalog.english', coalesce(composite_figi,'')), 'C')
) STORED;

CREATE INDEX %[1]s_search_idx ON %[1]s USING GIN (search);`,
		Migrations: []string{
			`ALTER TABLE %[1]s ADD COLUMN IF NOT EXISTS icon_url TEXT;
			 ALTER TABLE %[1]s ADD COLUMN IF NOT EXISTS logo_url TEXT;`,
		},
		Version:       1,
		IsPartitioned: false,
	},
	ConsensusKey: {
		Name:       ConsensusKey,
		ViewName:   "consensus",
		DateColumn: "event_date",
		Schema: `CREATE TABLE %[1]s (
	ticker                   CHARACTER VARYING(10) NOT NULL,
	composite_figi           CHARACTER(12)         NOT NULL,
	event_date               DATE                  NOT NULL,
	avg_recommendation       REAL,
	num_analysts             INT,
	num_strong_buy_or_buy    INT,
	num_hold                 INT,
	num_sell_or_strong_sell  INT,
	num_upgrades             INT,
	num_downgrades           INT,
	avg_target_price         REAL,
	PRIMARY KEY (composite_figi, event_date)
) PARTITION BY RANGE (event_date);

CREATE INDEX %[1]s_ticker_idx ON %[1]s(ticker);
CREATE INDEX %[1]s_event_date_idx ON %[1]s(event_date);`,
		Migrations:    []string{},
		Version:       0,
		IsPartitioned: true,
	},
	CustomKey: {
		Name:       CustomKey,
		ViewName:   "custom",
		DateColumn: "event_date",
		Schema: `CREATE TABLE %[1]s (
	ticker         CHARACTER VARYING(10) NOT NULL,
	composite_figi CHARACTER(12)         NOT NULL,
	event_date     DATE                  NOT NULL,
	key            TEXT                  NOT NULL,
	value          JSONB                 NOT NULL,
	PRIMARY KEY (key, composite_figi, event_date)
);

CREATE INDEX %[1]s_key_ticker_event_date_idx ON %[1]s(key, ticker, event_date DESC)`,
		Migrations:    []string{},
		Version:       0,
		IsPartitioned: false,
	},
	EconomicIndicatorKey: {
		Name:       EconomicIndicatorKey,
		ViewName:   "economic_indicators",
		DateColumn: "event_date",
		Schema: `CREATE TABLE %[1]s (
			series     TEXT NOT NULL,
			event_date DATE NOT NULL,
			value      REAL NOT NULL,
			PRIMARY KEY (series, event_date)
		);`,
		Migrations:    []string{},
		Version:       0,
		IsPartitioned: false,
	},
	EODKey: {
		Name:       EODKey,
		ViewName:   "eod",
		DateColumn: "event_date",
		Schema: `CREATE TABLE %[1]s (
ticker         CHARACTER VARYING(10) NOT NULL,
composite_figi CHARACTER(12)         NOT NULL,
event_date     DATE                  NOT NULL,
open           NUMERIC(12, 4)        NOT NULL DEFAULT 0.0,
high           NUMERIC(12, 4)        NOT NULL DEFAULT 0.0,
low            NUMERIC(12, 4)        NOT NULL DEFAULT 0.0,
close          NUMERIC(12, 4)        NOT NULL DEFAULT 0.0,
adj_close      NUMERIC(18, 4)        NOT NULL DEFAULT 0.0,
volume         BIGINT                NOT NULL DEFAULT 0.0,
dividend       NUMERIC(12, 4)        NOT NULL DEFAULT 0.0,
split_factor   NUMERIC(9, 6)         NOT NULL DEFAULT 1.0,
PRIMARY KEY (composite_figi, event_date)
) PARTITION BY RANGE (event_date);

CREATE INDEX %[1]s_event_date_idx ON %[1]s(event_date);
CREATE INDEX %[1]s_ticker_idx ON %[1]s(ticker);

CREATE TRIGGER %[1]s_adj_close_default
BEFORE INSERT ON %[1]s
FOR EACH ROW
WHEN (NEW.adj_close IS NULL AND NEW.close IS NOT NULL)
EXECUTE PROCEDURE adj_close_default();`,
		Migrations:    []string{},
		Version:       0,
		IsPartitioned: true,
	},
	EstimateKey: {
		Name:       EstimateKey,
		ViewName:   "estimates",
		DateColumn: "event_date",
		Schema: `CREATE TABLE %[1]s (
	ticker         CHARACTER VARYING(10) NOT NULL,
	composite_figi CHARACTER(12)         NOT NULL,
	event_date     DATE                  NOT NULL,
	series         estimate_series       NOT NULL,
	value          REAL                  NOT NULL,
	num_analysts   INT,
	std_dev        REAL,
	PRIMARY KEY (composite_figi, series, event_date)
) PARTITION BY RANGE (event_date);

CREATE INDEX %[1]s_ticker_idx ON %[1]s(ticker, series);
CREATE INDEX %[1]s_event_date_idx ON %[1]s(event_date, series);`,
		Migrations: []string{
			`ALTER TABLE %[1]s ALTER COLUMN series TYPE estimate_series USING series::estimate_series;`,
		},
		Version:       1,
		IsPartitioned: true,
	},
	IndexSnapshotKey: {
		Name:       IndexSnapshotKey,
		ViewName:   "indices_snapshot",
		DateColumn: "snapshot_date",
		Schema: `CREATE TABLE %[1]s (
    index_ticker   TEXT   NOT NULL,
    snapshot_date  DATE   NOT NULL,
    constituents   JSONB  NOT NULL,
    PRIMARY KEY (index_ticker, snapshot_date)
);

CREATE INDEX %[1]s_index_ticker_idx ON %[1]s(index_ticker, snapshot_date);`,
		Migrations:    []string{},
		Version:       0,
		IsPartitioned: false,
	},
	IndexChangelogKey: {
		Name:       IndexChangelogKey,
		ViewName:   "indices_changelog",
		DateColumn: "event_date",
		Schema: `CREATE TABLE %[1]s (
    composite_figi CHARACTER(12)         NOT NULL,
    ticker         CHARACTER VARYING(10) NOT NULL,
    index_ticker   TEXT                  NOT NULL,
    event_date     DATE                  NOT NULL,
    action         TEXT                  NOT NULL,
    weight         REAL                  NOT NULL DEFAULT 0.0,
    PRIMARY KEY (composite_figi, index_ticker, event_date)
);

CREATE INDEX %[1]s_index_ticker_idx ON %[1]s(index_ticker, event_date);`,
		Migrations:    []string{},
		Version:       0,
		IsPartitioned: false,
	},
	FundamentalsKey: {
		Name:       FundamentalsKey,
		ViewName:   "fundamentals",
		DateColumn: "event_date",
		Schema: `CREATE TABLE %[1]s (
	event_date DATE,
	ticker TEXT,
	composite_figi TEXT,
	dimension TEXT,
	date_key DATE,
	report_period DATE,
	last_updated DATE,

	accumulated_other_comprehensive_income BIGINT,
	total_assets BIGINT,
	average_assets BIGINT,
	current_assets BIGINT,
	assets_non_current BIGINT,
	asset_turnover NUMERIC,
	book_value_per_share NUMERIC,
	capital_expenditure BIGINT,
	cash_and_equivalents BIGINT,
	cost_of_revenue BIGINT,
	consolidated_income BIGINT,
	current_ratio NUMERIC,
	debt_to_equity_ratio NUMERIC,
	total_debt BIGINT,
	debt_current BIGINT,
	debt_non_current BIGINT,
	deferred_revenue BIGINT,
	depreciation_amortization_and_accretion BIGINT,
	deposits BIGINT,
	dividend_yield NUMERIC,
	dividends_per_basic_common_share NUMERIC,
	ebit BIGINT,
	ebitda BIGINT,
	ebitda_margin NUMERIC,
	ebt BIGINT,
	eps NUMERIC,
	eps_diluted NUMERIC,
	equity BIGINT,
	equity_avg BIGINT,
	enterprise_value BIGINT,
	ev_to_ebit BIGINT,
	ev_to_ebitda NUMERIC,
	free_cash_flow BIGINT,
	free_cash_flow_per_share NUMERIC,
	fx_usd NUMERIC,
	gross_profit BIGINT,
	gross_margin NUMERIC,
	intangibles BIGINT,
	interest_expense BIGINT,
	invested_capital BIGINT,
	invested_capital_average BIGINT,
	inventory BIGINT,
	investments BIGINT,
	investments_current BIGINT,
	investments_non_current BIGINT,
	total_liabilities BIGINT,
	current_liabilities BIGINT,
	liabilities_non_current BIGINT,
	market_capitalization BIGINT,
	net_cash_flow BIGINT,
	net_cash_flow_business BIGINT,
	net_cash_flow_common BIGINT,
	net_cash_flow_debt BIGINT,
	net_cash_flow_dividend BIGINT,
	net_cash_flow_from_financing BIGINT,
	net_cash_flow_from_investing BIGINT,
	net_cash_flow_invest BIGINT,
	net_cash_flow_from_operations BIGINT,
	net_cash_flow_fx BIGINT,
	net_income BIGINT,
	net_income_common_stock BIGINT,
	net_loss_income_discontinued_operations BIGINT,
	net_income_to_non_controlling_interests BIGINT,
	profit_margin NUMERIC,
	operating_expenses BIGINT,
	operating_income BIGINT,
	payables BIGINT,
	payout_ratio NUMERIC,
	pb NUMERIC,
	pe NUMERIC,
	pe1 NUMERIC,
	property_plant_and_equipment_net BIGINT,
	preferred_dividends_income_statement_impact BIGINT,
	price NUMERIC,
	ps NUMERIC,
	ps1 NUMERIC,
	receivables BIGINT,
	accumulated_retained_earnings_deficit BIGINT,
	revenues BIGINT,
	r_and_d_expenses BIGINT,
	roa NUMERIC,
	roe NUMERIC,
	roic NUMERIC,
	return_on_sales NUMERIC,
	share_based_compensation BIGINT,
	selling_general_and_administrative_expense BIGINT,
	share_factor NUMERIC,
	shares_basic BIGINT,
	weighted_average_shares BIGINT,
	weighted_average_shares_diluted BIGINT,
	sales_per_share NUMERIC,
	tangible_asset_value BIGINT,
	tax_assets BIGINT,
	income_tax_expense BIGINT,
	tax_liabilities BIGINT,
	tangible_assets_book_value_per_share NUMERIC,
	working_capital BIGINT,

	PRIMARY KEY (composite_figi, dimension, date_key)
);

CREATE INDEX %[1]s_ticker_idx ON %[1]s(ticker, dimension);
CREATE INDEX %[1]s_event_date_idx ON %[1]s(event_date, dimension);`,
		Migrations:    []string{},
		Version:       0,
		IsPartitioned: false,
	},
	MarketHolidaysKey: {
		Name:       MarketHolidaysKey,
		ViewName:   "market_holidays",
		DateColumn: "event_date",
		Schema: `CREATE TABLE %[1]s (
holiday TEXT NOT NULL,
event_date DATE NOT NULL,
market VARCHAR(25) NOT NULL,
early_close BOOLEAN NOT NULL DEFAULT false,
close_time TIME NOT NULL DEFAULT '16:00:00',
PRIMARY KEY (event_date, market)
);`,
		Migrations:    []string{},
		Version:       0,
		IsPartitioned: false,
	},
	MetricKey: {
		Name:       MetricKey,
		ViewName:   "metrics",
		DateColumn: "event_date",
		Schema: `CREATE TABLE %[1]s (
ticker         CHARACTER VARYING(10) NOT NULL,
composite_figi CHARACTER(12)         NOT NULL,
event_date     DATE                  NOT NULL,
market_cap     BIGINT                NOT NULL DEFAULT 0,
ev             BIGINT                NOT NULL DEFAULT 0,
pe             REAL                  NOT NULL DEFAULT 0.0,
pb             REAL                  NOT NULL DEFAULT 0.0,
ps             REAL                  NOT NULL DEFAULT 0.0,
ev_ebit        REAL                  NOT NULL DEFAULT 0.0,
ev_ebitda      REAL                  NOT NULL DEFAULT 0.0,
pe_forward     REAL                  NOT NULL DEFAULT 0.0,
peg            REAL                  NOT NULL DEFAULT 0.0,
price_to_cash_flow REAL              NOT NULL DEFAULT 0.0,
beta           REAL                  NOT NULL DEFAULT 0.0,
CHECK (LENGTH(TRIM(BOTH composite_figi)) = 12),
PRIMARY KEY (composite_figi, event_date)
) PARTITION BY RANGE (event_date);

CREATE INDEX %[1]s_event_date_idx ON %[1]s(event_date);
CREATE INDEX %[1]s_ticker_idx ON %[1]s(ticker);`,
		Migrations: []string{
			`ALTER TABLE %[1]s ADD COLUMN IF NOT EXISTS pe_forward REAL NOT NULL DEFAULT 0.0;
     ALTER TABLE %[1]s ADD COLUMN IF NOT EXISTS peg REAL NOT NULL DEFAULT 0.0;
     ALTER TABLE %[1]s ADD COLUMN IF NOT EXISTS price_to_cash_flow REAL NOT NULL DEFAULT 0.0;
     ALTER TABLE %[1]s ADD COLUMN IF NOT EXISTS beta REAL NOT NULL DEFAULT 0.0;
     ALTER TABLE %[1]s DROP COLUMN IF EXISTS sp500;`,
		},
		Version:       1,
		IsPartitioned: true,
	},
	QuoteKey: {
		Name:       QuoteKey,
		ViewName:   "quotes",
		DateColumn: "event_date",
		Schema: `CREATE TABLE %[1]s (
ticker         CHARACTER VARYING(10) NOT NULL,
composite_figi CHARACTER(12)         NOT NULL,
event_date     TIMESTAMP             NOT NULL,
price          REAL                  NOT NULL,
change         REAL                  NOT NULL,
change_pct     REAL                  NOT NULL,
CHECK (LENGTH(TRIM(BOTH composite_figi)) = 12),
PRIMARY KEY (composite_figi, event_date)
) PARTITION BY RANGE (event_date);

CREATE INDEX %[1]s_event_date_idx ON %[1]s(event_date);
CREATE INDEX %[1]s_ticker_idx ON %[1]s(ticker);`,
		Migrations:        []string{},
		Version:           0,
		IsPartitioned:     true,
		PartitionInterval: PartitionIntervalMonthly,
	},
	RatingKey: {
		Name:       RatingKey,
		ViewName:   "ratings",
		DateColumn: "event_date",
		Schema: `CREATE TABLE %[1]s (
	ticker         CHARACTER VARYING(10) NOT NULL,
	composite_figi CHARACTER(12)         NOT NULL,
	event_date     DATE                  NOT NULL,
	analyst_id     SMALLINT              NOT NULL REFERENCES analyst_lookup(id),
	rating         INT                   NOT NULL,
	PRIMARY KEY (analyst_id, composite_figi, event_date)
) PARTITION BY RANGE (event_date);

CREATE INDEX %[1]s_ticker_event_date_idx ON %[1]s(ticker, event_date DESC);
CREATE INDEX %[1]s_analyst_id_date_idx ON %[1]s(analyst_id, event_date) INCLUDE (composite_figi, ticker, rating)`,
		Migrations: []string{
			// Wrapped in a DO block so that re-running on a table that
			// has already been (partially or fully) migrated is a no-op
			// instead of erroring with "column already exists" or
			// "constraint already exists". Each step is guarded by a
			// catalog lookup so the migration is safe to retry.
			`DO $do$
DECLARE
    pk_name TEXT;
BEGIN
    -- Step 1: add analyst_id if missing.
    IF NOT EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = '%[1]s' AND column_name = 'analyst_id' AND table_schema = 'public'
    ) THEN
        ALTER TABLE %[1]s ADD COLUMN analyst_id SMALLINT;
    END IF;

    -- Step 2: backfill from analyst (if it still exists) and drop the column.
    IF EXISTS (
        SELECT 1 FROM information_schema.columns
        WHERE table_name = '%[1]s' AND column_name = 'analyst' AND table_schema = 'public'
    ) THEN
        UPDATE %[1]s SET analyst_id = analyst_lookup.id
            FROM analyst_lookup
            WHERE analyst_lookup.analyst = %[1]s.analyst AND %[1]s.analyst_id IS NULL;
        ALTER TABLE %[1]s DROP COLUMN analyst;
    END IF;

    -- Step 3: enforce NOT NULL on analyst_id if it isn't already.
    IF (SELECT is_nullable FROM information_schema.columns
        WHERE table_name = '%[1]s' AND column_name = 'analyst_id' AND table_schema = 'public') = 'YES' THEN
        ALTER TABLE %[1]s ALTER COLUMN analyst_id SET NOT NULL;
    END IF;

    -- Step 4: replace the PK if it's not already keyed by analyst_id.
    SELECT c.conname INTO pk_name
    FROM pg_constraint c
    WHERE c.conrelid = '%[1]s'::regclass AND c.contype = 'p'
      AND NOT EXISTS (
          SELECT 1 FROM pg_attribute a
          WHERE a.attrelid = c.conrelid
            AND a.attnum = ANY(c.conkey)
            AND a.attname = 'analyst_id'
      )
    LIMIT 1;

    IF pk_name IS NOT NULL THEN
        EXECUTE 'ALTER TABLE %[1]s DROP CONSTRAINT ' || quote_ident(pk_name);
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = '%[1]s'::regclass AND contype = 'p'
    ) THEN
        ALTER TABLE %[1]s ADD PRIMARY KEY (analyst_id, composite_figi, event_date);
    END IF;

    -- Step 5: add the FK if missing.
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = '%[1]s'::regclass
          AND contype = 'f'
          AND conname = '%[1]s_analyst_id_fkey'
    ) THEN
        ALTER TABLE %[1]s ADD CONSTRAINT %[1]s_analyst_id_fkey
            FOREIGN KEY (analyst_id) REFERENCES analyst_lookup(id);
    END IF;
END
$do$;
CREATE INDEX IF NOT EXISTS %[1]s_analyst_id_date_idx ON %[1]s(analyst_id, event_date) INCLUDE (composite_figi, ticker, rating);`,
		},
		Version:       1,
		IsPartitioned: true,
		ViewGenerator: ratingViewGenerator{},
	},
}

// Schema returns the schema of the data type. A getter is used to ensure that the value is immutable after construction
func (dt *DataType) ExpandedSchema(tableName string) string {
	return fmt.Sprintf(dt.Schema, tableName)
}
