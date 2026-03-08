# Zacks Provider Feature Parity Design

## Context

The current pv-data Zacks provider downloads a stock screener CSV with 134+ fields but only publishes 5 rating observations per stock, discarding everything else. The legacy importer at `../importers/zacks-rank` stored all fields in a `zacks_financials` table and ran a separate Playwright scraper for balance sheet data.

The goal is to bring the pv-data provider to feature parity by mapping CSV fields into existing and new data types, without porting the balance sheet scraper (CSV data is sufficient) or creating a Zacks-specific table (parquet archive covers the full dump).

## New Data Types

### `estimate`

Forward-looking consensus data. Normalized rows keyed by series name, generic enough for any provider.

```sql
CREATE TABLE %[1]s (
    ticker         CHARACTER VARYING(10) NOT NULL,
    composite_figi CHARACTER(12)         NOT NULL,
    event_date     DATE                  NOT NULL,
    series         TEXT                  NOT NULL,
    value          REAL                  NOT NULL,
    num_analysts   INT,
    std_dev        REAL,
    PRIMARY KEY (composite_figi, series, event_date)
);
```

Series values from Zacks: `eps-q0`, `eps-q1`, `eps-q2`, `eps-f0`, `eps-f1`, `eps-f2`, `sales-q1`, `sales-f1`, `lt-growth`, `earnings-esp`, `eps-surprise-last`, `eps-surprise-prev`, `eps-surprise-avg-4q`.

### `consensus`

Broker consensus aggregation -- distinct from `rating` which is a single analyst's opinion as an integer.

```sql
CREATE TABLE %[1]s (
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
);
```

### `index`

Index membership tracking using annual snapshots plus a change log. To get members on any date: start from the most recent snapshot before that date, then apply all changes after the snapshot up through that date.

**Snapshot table** (full membership list, taken annually):

```sql
CREATE TABLE %[1]s_snapshot (
    composite_figi CHARACTER(12)         NOT NULL,
    ticker         CHARACTER VARYING(10) NOT NULL,
    index_name     TEXT                  NOT NULL,
    snapshot_date  DATE                  NOT NULL,
    PRIMARY KEY (composite_figi, index_name, snapshot_date)
);
```

**Change log** (additions and removals between snapshots):

```sql
CREATE TABLE %[1]s_changelog (
    composite_figi CHARACTER(12)         NOT NULL,
    ticker         CHARACTER VARYING(10) NOT NULL,
    index_name     TEXT                  NOT NULL,
    event_date     DATE                  NOT NULL,
    action         TEXT                  NOT NULL,
    PRIMARY KEY (composite_figi, index_name, event_date)
);
```

Action values: `add`, `remove`.

## Modified Data Types

### `metric`

Add columns: `pe_forward`, `peg`, `price_to_cash_flow`, `beta`.
Drop column: `sp500` (moves to `index` data type).

## Zacks CSV Field Mapping

| Destination | Fields |
|---|---|
| **rating** | ZacksRank, ValueScore, GrowthScore, MomentumScore, VgmScore (already implemented) |
| **metric** | MarketCapMil, PeTrailing12Mo, PriceToBook, PriceToSales, PeF1, PegRatio, PriceToCashFlow, Beta |
| **estimate** | Q0/Q1/Q2/F0/F1/F2 EPS estimates + analyst counts + std devs, Q1/F1 sales estimates, LtGrowth, EarningsESP, EPS surprises |
| **consensus** | AvgBrokerRec, analyst counts by bucket, upgrades/downgrades, AvgTargetPrice |
| **fundamental** | CurrentAssetsMil, CurrentLiabilitiesMil (working capital derived), plus other balance sheet fields from CSV (ReceivablesMil, IntangiblesMil, InventoryMil, LongTermDebtMil, CommonEquityMil, BookValue, AnnualSalesMil, CostOfGoodsSoldMil, EbitdaMil, EbitMil, NetIncomeMil, CashFlowMil, CurrentRatio, DebtToEquityRatio, DivYieldPercent, CurrentRoeTtm, CurrentRoaTtm, NetMarginPercent) |
| **parquet only** | Price changes (1wk/4wk/12wk/YTD), relative price change, ZacksIndustryRank, ZacksRankChangeIndicator, IndustryRankOfAbr, RankInIndustryOfAbr, ChangeInAvgRec, derivable percentages, MarketValueToNumAnalysts, OperatingMargin, Turnover, InventoryTurnover, AssetUtilization, QuickRatio, CashRatio, PreferredEquityMil, DebtToTotalCapital |

## Architecture

### Multi-data-type datasets

The `Dataset.DataTypes` field is already a slice. The Zacks dataset declaration changes from `[RatingKey]` to `[RatingKey, MetricKey, EstimateKey, ConsensusKey, FundamentalsKey]`. One `Fetch` call emits observations of all types on the same channel. `SaveObservations` in `library/database.go` already routes by observation type to the correct table via `DataTablesMap`.

### Per-subscription tables

Each subscription gets its own tables. The preferred views system (already implemented) points canonical names (`metrics`, `estimates`, etc.) at the user's chosen subscription table.

## What Is NOT Ported

- **Balance sheet web scraper** -- CSV balance sheet fields are sufficient
- **Exclusion tracking** -- only needed for the web scraper
- **`zacks_financials` table** -- parquet archive covers the full 134-field dump
