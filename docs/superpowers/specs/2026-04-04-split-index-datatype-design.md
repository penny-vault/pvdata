# Split Index Data Type Into Two Separate Types

## Problem

`IndexKey = "index"` is the only data type that creates two tables (`_snapshot` and `_changelog` suffixes). This breaks the 1:1 convention between data types and tables, causing special-case logic in 6 files:

- `data/datatype.go` -- schema creates two tables via `%[1]s_snapshot` and `%[1]s_changelog`
- `data/index.go` -- `SaveDB` methods append suffixes to the table name
- `library/database.go` -- single `IndexKey` lookup serves both snapshot and change saves
- `library/published_views.go` -- special-cases `IndexKey` to generate two views
- `web/handlers_data.go` -- subtable routing for `index` data type
- `provider/index_helpers.go` -- all SQL queries append `_snapshot`/`_changelog` to the table param

Table naming is also wrong: `sharadar_sp500_index_45455_snapshot` puts the subscription ID before the suffix instead of at the end.

## Design

Replace `IndexKey` with two independent data types that follow the standard pattern.

### Data type definitions (data/datatype.go)

Remove `IndexKey = "index"`. Add:

```
IndexSnapshotKey  = "index-snapshot"
IndexChangelogKey = "index-changelog"
```

Each gets its own `DataType` entry with a single-table schema (no suffixes):

**index-snapshot:**
- ViewName: `"indices_snapshot"`
- Schema:

```sql
CREATE TABLE %[1]s (
    index_ticker   TEXT   NOT NULL,
    snapshot_date  DATE   NOT NULL,
    constituents   JSONB  NOT NULL,
    PRIMARY KEY (index_ticker, snapshot_date)
);
CREATE INDEX %[1]s_index_ticker_idx ON %[1]s(index_ticker, snapshot_date);
```

**index-changelog:**
- ViewName: `"indices_changelog"`
- Schema:

```sql
CREATE TABLE %[1]s (
    composite_figi CHARACTER(12)         NOT NULL,
    ticker         CHARACTER VARYING(10) NOT NULL,
    index_ticker   TEXT                  NOT NULL,
    event_date     DATE                  NOT NULL,
    action         TEXT                  NOT NULL,
    weight         REAL                  NOT NULL DEFAULT 0.0,
    PRIMARY KEY (composite_figi, index_ticker, event_date)
);
CREATE INDEX %[1]s_index_ticker_idx ON %[1]s(index_ticker, event_date);
```

No migrations -- fresh schemas only. Table names follow the standard convention: `sharadar_sp500_index_snapshot_45455` and `sharadar_sp500_index_changelog_45455`.

### SaveDB methods (data/index.go)

`IndexSnapshot.SaveDB` and `IndexChange.SaveDB` use the table name directly:

- `INSERT INTO %[1]s (...)` instead of `INSERT INTO %[1]s_snapshot (...)`
- `ON CONFLICT ON CONSTRAINT %[1]s_pkey` instead of `%[1]s_snapshot_pkey`

### Observation saving (library/database.go)

Two separate lookups instead of one:

```go
elem.IndexSnapshot.SaveDB(ctx, subscription.DataTablesMap[data.IndexSnapshotKey], conn)
elem.IndexChange.SaveDB(ctx, subscription.DataTablesMap[data.IndexChangelogKey], conn)
```

### index_helpers.go

Remove all `_snapshot`/`_changelog` suffix appending from SQL queries. Functions query the table name directly.

Functions that query only one table keep a single table parameter:
- `LastSnapshotDate(ctx, pool, snapshotTable, indexTicker)`
- `PreviousSnapshotTickers(ctx, pool, snapshotTable, indexTicker)`

Functions that query both tables take two parameters:
- `CurrentIndexMembers(ctx, pool, snapshotTable, changelogTable, indexTicker, asOfDate)`

`EmitChangelog` and `EmitWeightChanges` don't query the DB -- unchanged.

### Published views (library/published_views.go)

Remove the `IndexKey` special case from `GenerateViewSQL` and `ValidateSourceTables`. Each data type generates its own single view through the normal path. The ViewName fields (`indices_snapshot`, `indices_changelog`) produce the correct view names.

### Web handler (web/handlers_data.go)

Remove the subtable routing special case for `index`. Each data type has its own table -- the client requests the data type it wants directly.

### Providers

All providers that produce index data (iShares, TradingView, Nasdaq, Sharadar SP500) list both types in `Datasets().DataTypes`:

```go
DataTypes: []*data.DataType{
    data.DataTypes[data.IndexSnapshotKey],
    data.DataTypes[data.IndexChangelogKey],
}
```

Each provider looks up both tables from `DataTablesMap`:

```go
snapshotTable  := subscription.DataTablesMap[data.IndexSnapshotKey]
changelogTable := subscription.DataTablesMap[data.IndexChangelogKey]
```

And passes them to `index_helpers.go` functions as appropriate.

## Files changed

| File | Change |
|------|--------|
| `data/datatype.go` | Replace `IndexKey` with two keys and two `DataType` entries |
| `data/index.go` | Remove suffix appending in `SaveDB` methods |
| `library/database.go` | Two separate `DataTablesMap` lookups |
| `library/published_views.go` | Remove `IndexKey` special cases |
| `library/published_views_test.go` | Update expectations |
| `web/handlers_data.go` | Remove subtable routing |
| `provider/index_helpers.go` | Remove suffix appending, add second table param to `CurrentIndexMembers` |
| `provider/ishares/ishares.go` | Use both keys |
| `provider/tradingview/tradingview.go` | Use both keys |
| `provider/nasdaq/nasdaq.go` | Use both keys |
| `provider/sharadar/sharadar.go` | Use both keys |
| `provider/sharadar/sharadar_import.go` | Use both keys |
| `provider/zacks/zacks.go` | Use both keys |
