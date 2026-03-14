# Migrate Legacy Database Design

## Problem

The old penny-vault database uses flat tables in the `public` schema (`eod`, `assets`, `market_holidays`, `zacks_financials`, etc.). The new pv-data system uses per-subscription tables with published views. We need a CLI command to convert an existing legacy database into the new structure.

## Approach

A `pvdata migrate-legacy` command that:

1. Adds missing values to the `datatype` enum (prerequisite)
2. Creates a `legacy` schema and moves all old tables there
3. Creates proper subscriptions (provider: "legacy") which generates correctly-structured tables
4. Copies and transforms data from `legacy.*` into the new subscription tables
5. Published views are auto-created by the subscription save process

The `legacy` schema remains as a safety net and can be dropped manually later.

## Prerequisites

### Database migration 000003: add missing datatype enum values

The `datatype` PostgreSQL enum is missing several values that are referenced by `DataTypes` in `data/datatype.go`. This must be fixed before the migration can create subscriptions with these data types.

```sql
ALTER TYPE datatype ADD VALUE IF NOT EXISTS 'consensus';
ALTER TYPE datatype ADD VALUE IF NOT EXISTS 'estimate';
ALTER TYPE datatype ADD VALUE IF NOT EXISTS 'index';
ALTER TYPE datatype ADD VALUE IF NOT EXISTS 'quote';
```

This is a standalone database migration (`db/migrations/000003_add_datatype_enum_values.up.sql`) that should be applied independently of the migrate-legacy command, since it fixes a pre-existing gap that also affects the live Zacks provider.

## Tables

### Converted to subscriptions

| Legacy Table | Provider | Dataset | Data Types Created |
|---|---|---|---|
| `legacy.eod` | legacy | eod | eod |
| `legacy.assets` | legacy | assets | asset-description |
| `legacy.market_holidays` | legacy | market-holidays | market-holidays |
| `legacy.zacks_financials` | legacy | Zacks Screener Data | rating, metric, estimate, consensus |

The Zacks dataset name matches the existing Zacks provider (`"Zacks Screener Data"`) so that published views compose cleanly if the user later adds a live Zacks subscription.

### Moved to legacy schema only (no subscription)

- `reported_financials`
- `seeking_alpha`
- `zacks_number_1`
- `trading_days`
- `schema_migrations`
- `activity`
- `announcements`
- `portfolios`
- `portfolio_transactions`
- `portfolio_measurements`
- `profile`

## Data transforms

### EOD

Near-identical structure. Transform:

```sql
INSERT INTO <new_eod_table> (ticker, composite_figi, event_date, open, high, low, close, adj_close, volume, dividend, split_factor)
SELECT ticker, TRIM(composite_figi), event_date, open, high, low, close, COALESCE(adj_close, close), volume, dividend, split_factor
FROM legacy.eod
```

Notes:
- Drop `source` column (datasource enum)
- `TRIM(composite_figi)` cleans padding from the legacy `CHAR(12)` column. The destination is also `CHARACTER(12)` so the value will be re-padded, but trimming ensures clean data entry.
- `COALESCE(adj_close, close)` because new schema has `NOT NULL DEFAULT 0.0` on adj_close. The new table also has an `adj_close_default` trigger that would handle NULL, but COALESCE makes the intent explicit.
- New table is partitioned -- `ManagePartitions()` creates standard partitions (1900-2000, 2000-2005, 2005-2010, then 5-year ranges to current year+1) which cover typical market data date ranges.

### Pre-migration validation

Before copying data, validate that all `composite_figi` values are exactly 12 characters after trimming. The `metric` and `eod` tables have `CHECK (LENGTH(TRIM(BOTH composite_figi)) = 12)` constraints that will reject shorter values.

```sql
SELECT COUNT(*) FROM legacy.eod WHERE LENGTH(TRIM(composite_figi)) != 12;
SELECT COUNT(*) FROM legacy.zacks_financials WHERE LENGTH(TRIM(composite_figi)) != 12;
```

If any rows fail, log them as warnings and exclude them from the migration.

### Assets

```sql
INSERT INTO <new_assets_table> (ticker, composite_figi, share_class_figi, primary_exchange, asset_type, active, name, description, corporate_url, sector, industry, cik, cusips, isins, other_identifiers, similar_tickers, tags, listed, delisted, last_updated)
SELECT ticker, composite_figi, share_class_figi, primary_exchange,
  CASE asset_type
    WHEN 'Common Stock' THEN 'CS'
    WHEN 'Preferred Stock' THEN 'PS'
    WHEN 'Exchange Traded Fund' THEN 'ETF'
    WHEN 'Exchange Traded Note' THEN 'ETN'
    WHEN 'Mutual Fund' THEN 'MF'
    WHEN 'Closed-End Fund' THEN 'CEF'
    WHEN 'American Depository Receipt Common' THEN 'ADRC'
    WHEN 'FRED' THEN 'FRED'
    WHEN 'Synthetic History' THEN 'SYNTH'
  END::assettype,
  active, name, description, corporate_url, sector, industry, cik,
  CASE WHEN cusip IS NOT NULL THEN ARRAY[TRIM(cusip)] ELSE '{}' END,
  CASE WHEN isin IS NOT NULL THEN ARRAY[TRIM(isin)] ELSE '{}' END,
  '{}'::jsonb,
  similar_tickers, tags, listed_utc, delisted_utc, last_updated_utc
FROM legacy.assets
```

Notes:
- Asset type enum values differ between old and new systems (full names vs abbreviations)
- `cusip` (char(9)) and `isin` (char(12)) become single-element arrays `cusips text[]` and `isins text[]`
- `sic_code` is omitted (defaults to NULL) -- not available in legacy data
- `other_identifiers` is set to empty JSONB object -- not available in legacy data
- Drop columns: `source`, `seeking_alpha_id`, `logo_url`, `new`, `updated`
- Primary key is `(ticker, composite_figi)` -- same as old

### Market Holidays

Direct copy, no transforms needed:

```sql
INSERT INTO <new_market_holidays_table> (holiday, event_date, market, early_close, close_time)
SELECT holiday, event_date, market, early_close, close_time
FROM legacy.market_holidays
```

### Zacks Financials -> Rating

The old `zacks_financials` table stores letter grades (A/B/C/D/F) for scores. The new `rating` table uses integers. Each score type becomes a separate row with a different `analyst` value.

Letter-to-int mapping: A=1, B=2, C=3, D=4, F=5

```sql
-- zacks-rank (already integer)
INSERT INTO <new_rating_table> (ticker, composite_figi, event_date, analyst, rating)
SELECT ticker, TRIM(composite_figi), event_date, 'zacks-rank', zacks_rank
FROM legacy.zacks_financials
WHERE zacks_rank IS NOT NULL;

-- zacks-value
INSERT INTO <new_rating_table> (ticker, composite_figi, event_date, analyst, rating)
SELECT ticker, TRIM(composite_figi), event_date, 'zacks-value',
  CASE TRIM(value_score) WHEN 'A' THEN 1 WHEN 'B' THEN 2 WHEN 'C' THEN 3 WHEN 'D' THEN 4 WHEN 'F' THEN 5 END
FROM legacy.zacks_financials
WHERE TRIM(value_score) IN ('A','B','C','D','F');

-- zacks-growth
INSERT INTO <new_rating_table> (ticker, composite_figi, event_date, analyst, rating)
SELECT ticker, TRIM(composite_figi), event_date, 'zacks-growth',
  CASE TRIM(growth_score) WHEN 'A' THEN 1 WHEN 'B' THEN 2 WHEN 'C' THEN 3 WHEN 'D' THEN 4 WHEN 'F' THEN 5 END
FROM legacy.zacks_financials
WHERE TRIM(growth_score) IN ('A','B','C','D','F');

-- zacks-momentum
INSERT INTO <new_rating_table> (ticker, composite_figi, event_date, analyst, rating)
SELECT ticker, TRIM(composite_figi), event_date, 'zacks-momentum',
  CASE TRIM(momentum_score) WHEN 'A' THEN 1 WHEN 'B' THEN 2 WHEN 'C' THEN 3 WHEN 'D' THEN 4 WHEN 'F' THEN 5 END
FROM legacy.zacks_financials
WHERE TRIM(momentum_score) IN ('A','B','C','D','F');

-- zacks-vgm
INSERT INTO <new_rating_table> (ticker, composite_figi, event_date, analyst, rating)
SELECT ticker, TRIM(composite_figi), event_date, 'zacks-vgm',
  CASE TRIM(vgm_score) WHEN 'A' THEN 1 WHEN 'B' THEN 2 WHEN 'C' THEN 3 WHEN 'D' THEN 4 WHEN 'F' THEN 5 END
FROM legacy.zacks_financials
WHERE TRIM(vgm_score) IN ('A','B','C','D','F');
```

### Zacks Financials -> Metric

```sql
INSERT INTO <new_metric_table> (ticker, composite_figi, event_date, market_cap, ev, pe, pb, ps, ev_ebit, ev_ebitda, pe_forward, peg, price_to_cash_flow, beta)
SELECT ticker, TRIM(composite_figi), event_date,
  COALESCE(market_cap_mil, 0) * 1000000,
  0,  -- ev not available in legacy
  COALESCE(pe_trailing_12_months, 0),
  COALESCE(price_to_book, 0),
  COALESCE(price_to_sales, 0),
  0,  -- ev_ebit not available in legacy
  0,  -- ev_ebitda not available in legacy
  COALESCE(pe_f1, 0),
  COALESCE(peg_ratio, 0),
  COALESCE(price_to_cash_flow, 0),
  COALESCE(beta, 0)
FROM legacy.zacks_financials
```

Notes:
- `market_cap_mil` is in millions in legacy, new system stores raw value so multiply by 1e6
- `ev`, `ev_ebit`, `ev_ebitda` not available in legacy zacks data, default to 0
- New metric table is partitioned -- `ManagePartitions()` creates standard partitions

### Zacks Financials -> Estimate

Each estimate series becomes a separate row. Follows the same pattern as the current Zacks provider. The `value` column is `NOT NULL` in the new schema, so all estimate values use COALESCE to default to 0.

```sql
-- eps-q0
INSERT INTO <new_estimate_table> (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-q0',
  COALESCE(q0_consensus_est_last_completed_fiscal_qtr, 0),
  COALESCE(number_of_analysts_in_q0_consensus, 0),
  0
FROM legacy.zacks_financials
WHERE q0_consensus_est_last_completed_fiscal_qtr IS NOT NULL
  OR number_of_analysts_in_q0_consensus IS NOT NULL;

-- eps-q1
INSERT INTO <new_estimate_table> (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-q1',
  COALESCE(q1_consensus_est, 0),
  COALESCE(number_of_analysts_in_q1_consensus, 0),
  COALESCE(stdev_q1_q1_consensus_ratio, 0)
FROM legacy.zacks_financials
WHERE q1_consensus_est IS NOT NULL OR number_of_analysts_in_q1_consensus IS NOT NULL;

-- eps-q2
INSERT INTO <new_estimate_table> (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-q2',
  COALESCE(q2_consensus_est_next_fiscal_qtr, 0),
  COALESCE(number_of_analysts_in_q2_consensus, 0),
  COALESCE(stdev_q2_q2_consensus_ratio, 0)
FROM legacy.zacks_financials
WHERE q2_consensus_est_next_fiscal_qtr IS NOT NULL OR number_of_analysts_in_q2_consensus IS NOT NULL;

-- eps-f0
INSERT INTO <new_estimate_table> (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-f0',
  COALESCE(f0_consensus_est, 0),
  COALESCE(number_of_analysts_in_f0_consensus, 0)::int,
  0
FROM legacy.zacks_financials
WHERE f0_consensus_est IS NOT NULL OR number_of_analysts_in_f0_consensus IS NOT NULL;

-- eps-f1
INSERT INTO <new_estimate_table> (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-f1',
  COALESCE(f1_consensus_est, 0),
  COALESCE(number_of_analysts_in_f1_consensus, 0),
  COALESCE(stdev_f1_f1_consensus_ratio, 0)
FROM legacy.zacks_financials
WHERE f1_consensus_est IS NOT NULL OR number_of_analysts_in_f1_consensus IS NOT NULL;

-- eps-f2
INSERT INTO <new_estimate_table> (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-f2',
  COALESCE(f2_consensus_est, 0),
  COALESCE(number_of_analysts_in_f2_consensus, 0),
  0
FROM legacy.zacks_financials
WHERE f2_consensus_est IS NOT NULL OR number_of_analysts_in_f2_consensus IS NOT NULL;

-- sales-q1
INSERT INTO <new_estimate_table> (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'sales-q1',
  COALESCE(q1_consensus_sales_est_mil, 0), 0, 0
FROM legacy.zacks_financials
WHERE q1_consensus_sales_est_mil IS NOT NULL;

-- sales-f1
INSERT INTO <new_estimate_table> (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'sales-f1',
  COALESCE(f1_consensus_sales_est_mil, 0), 0, 0
FROM legacy.zacks_financials
WHERE f1_consensus_sales_est_mil IS NOT NULL;

-- lt-growth
INSERT INTO <new_estimate_table> (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'lt-growth',
  COALESCE(long_term_growth_consensus_est, 0), 0, 0
FROM legacy.zacks_financials
WHERE long_term_growth_consensus_est IS NOT NULL;

-- earnings-esp
INSERT INTO <new_estimate_table> (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'earnings-esp',
  COALESCE(earnings_esp, 0), 0, 0
FROM legacy.zacks_financials
WHERE earnings_esp IS NOT NULL;

-- eps-surprise-last
INSERT INTO <new_estimate_table> (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-surprise-last',
  COALESCE(last_eps_surprise_percent, 0), 0, 0
FROM legacy.zacks_financials
WHERE last_eps_surprise_percent IS NOT NULL;

-- eps-surprise-prev
INSERT INTO <new_estimate_table> (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-surprise-prev',
  COALESCE(previous_eps_surprise_percent, 0), 0, 0
FROM legacy.zacks_financials
WHERE previous_eps_surprise_percent IS NOT NULL;

-- eps-surprise-avg-4q
INSERT INTO <new_estimate_table> (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-surprise-avg-4q',
  COALESCE(avg_eps_surprise_last_4_qtrs, 0), 0, 0
FROM legacy.zacks_financials
WHERE avg_eps_surprise_last_4_qtrs IS NOT NULL;
```

### Zacks Financials -> Consensus

```sql
INSERT INTO <new_consensus_table> (ticker, composite_figi, event_date, avg_recommendation, num_analysts, num_strong_buy_or_buy, num_hold, num_sell_or_strong_sell, num_upgrades, num_downgrades, avg_target_price)
SELECT ticker, TRIM(composite_figi), event_date,
  current_avg_broker_rec,
  num_brokers_in_rating,
  num_rating_strong_buy_or_buy,
  num_rating_hold,
  num_rating_strong_sell_or_sell,
  number_rating_upgrades,
  number_rating_downgrades,
  average_target_price
FROM legacy.zacks_financials
WHERE current_avg_broker_rec IS NOT NULL OR num_brokers_in_rating IS NOT NULL
```

## Implementation

### CLI command: `pvdata migrate-legacy`

A new cobra command in `cmd/migrate_legacy.go`.

### Flags

- `--db-url` (or uses config): connection string to the database containing legacy tables
- `--dry-run`: print what would be done without making changes

### Execution flow

1. **Pre-flight checks**
   - Verify legacy tables exist in `public` schema (at least `eod` and `assets`)
   - Verify `legacy` schema does not already exist (or offer `--force` to clean up a failed prior run)
   - Verify no subscriptions with provider "legacy" exist
   - Verify the pv-data library tables exist (`subscriptions`, `published_views`)
   - Validate `composite_figi` lengths in legacy tables (warn and exclude bad rows)

2. **Create legacy schema and move tables**
   - `CREATE SCHEMA legacy`
   - `ALTER TABLE public.<table> SET SCHEMA legacy` for each old table
   - Also move old enum types (`datasource`) and functions that belong to the old system

3. **Create subscriptions** (using existing `library.Subscription.Save()`)
   - EOD subscription: provider="legacy", dataset="eod", data_types=["eod"]
   - Assets subscription: provider="legacy", dataset="assets", data_types=["asset-description"]
   - Market holidays subscription: provider="legacy", dataset="market-holidays", data_types=["market-holidays"]
   - Zacks subscription: provider="legacy", dataset="Zacks Screener Data", data_types=["rating", "metric", "estimate", "consensus"]

   Note: `Save()` manages its own transaction internally, so subscription creation is not wrapped in the outer data-copy transaction. If the data copy fails after subscriptions are created, the subscriptions will exist with empty tables. The `--force` flag handles cleanup of this state by deleting existing legacy subscriptions before retrying.

4. **Manage partitions** for partitioned types (eod, metric)
   - Call `ManagePartitions()` which creates standard partitions (1900-2000, 2000-2005, 2005-2010, then 5-year ranges to current year+1)

5. **Copy and transform data** using the SQL statements above
   - Execute the data copy within a transaction (separate from subscription creation)
   - Report row counts for each table after copy
   - Cross-check row counts between legacy and new tables to verify completeness

6. **Update subscription metadata**
   - Set `total_records`, `first_obs_date`, `last_obs_date` from copied data
   - Set `active = false` (legacy subscriptions should not be fetched)
   - Set `schedule = ''` (no automatic fetching)

### Legacy provider registration

Register a "legacy" provider in the provider registry (`provider/discover.go`). It needs only metadata (Name, Description, Datasets, ConfigDescription) -- no Fetch function. The Datasets method returns dataset definitions with the correct DataTypes so that subscription creation works, but the Fetch function is nil or a no-op.

### Error handling

- Schema move and subscription creation happen first; data copy runs in its own transaction
- If the data copy transaction fails, it rolls back cleanly -- subscriptions exist but have empty tables
- `--force` flag: deletes existing legacy subscriptions and legacy schema, allowing a clean retry
- If legacy schema already exists without `--force`, abort with a clear message
- If required legacy tables are missing, list which ones are absent and abort
- Rows with invalid `composite_figi` lengths are logged and excluded

### Testing

- Unit tests for the column mapping SQL (can use string comparison of generated SQL)
- Integration test that creates legacy-structured tables, runs the migration, and verifies data in new tables
