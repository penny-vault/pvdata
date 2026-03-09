# Published Views Design

## Problem

Data for a given type (e.g., EOD prices) may come from multiple sources over
time: a legacy database for historical data, Tiingo for recent data, etc. The
current "preferred view" system maps one view name to one subscription table.
There is no way to compose multiple subscription tables into a single
queryable view.

Additionally, EOD price adjustment (dividend/split-adjusted close) has no
execution path. It is defined as a provider but is not a data fetch -- it is a
post-ingestion transformation.

## Goals

1. Replace preferred views with published views that support multiple sources
   with date-range boundaries.
2. Replace the stubbed LifeCycleManager with a composable post-fetch hook chain.
3. Provide a TUI (`pvdata publish`) for managing published views.

## Non-Goals

- Backward compatibility with the current preferred view system (unreleased).
- Handling overlapping date ranges automatically. Users set non-overlapping
  boundaries by convention.

---

## 1. Database Schema

Drop the unused `dataframe` table. Create `published_views`:

```sql
DROP TABLE IF EXISTS dataframe;

CREATE TABLE published_views (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    view_name TEXT NOT NULL UNIQUE,
    data_type_key TEXT NOT NULL,
    sources JSONB NOT NULL DEFAULT '[]'
);
```

The `sources` JSONB array holds ordered entries:

```json
[
  {
    "table_name": "eod_tiingo_abc12",
    "subscription_id": "uuid-here",
    "from_date": "2023-01-01",
    "until_date": null
  },
  {
    "table_name": "eod_legacy_def34",
    "subscription_id": "uuid-here",
    "from_date": null,
    "until_date": "2023-01-01"
  }
]
```

- `from_date` null = no lower bound.
- `until_date` null = no upper bound.
- Single-source views with both null are the degenerate case, equivalent to
  the old preferred views.

Generated view SQL example:

```sql
CREATE OR REPLACE VIEW eod AS
  SELECT * FROM eod_tiingo_abc12 WHERE event_date >= '2023-01-01'
  UNION ALL
  SELECT * FROM eod_legacy_def34 WHERE event_date < '2023-01-01';
```

### Performance

For queries on recent data (the common case), Postgres pushes the caller's
WHERE clause into each UNION ALL leg. Legs whose date filter contradicts the
query are eliminated at plan time (zero I/O). Partition pruning within each
leg further narrows to the relevant partition. Net cost is identical to
querying a single table directly.

---

## 2. View Generation

A function `GeneratePublishedView` reads a `published_views` row and produces
the SQL.

Logic per source:
- If `from_date` is set: `WHERE event_date >= 'from_date'`
- If `until_date` is set: `WHERE event_date < 'until_date'`
- If both set: `WHERE event_date >= 'from_date' AND event_date < 'until_date'`
- If neither set: bare `SELECT * FROM table`

All legs joined with `UNION ALL`, wrapped in `CREATE OR REPLACE VIEW`.

### Special cases

- **IndexKey:** One `published_views` row generates two views. The row stores
  the base table name (e.g., `index_ishares_abc12`). Generation appends
  `_snapshot` and `_changelog` to build `indices_snapshot` and
  `indices_changelog`, applying the same date filters to both.
- **Zero sources:** Drop the view if it exists.

### When it runs

- After any modification via `pvdata publish`.
- During preflight validation (verify views exist and match config).
- On subscription save (auto-add if first subscription for that data type).

### Validation before generation

- All referenced tables exist.
- Date ranges do not overlap.
- All source tables share the same data type.

---

## 3. Subscription Lifecycle Integration

### Creation (`Save`)

Auto-create a `published_views` row with a single source (no date bounds) if
no published view exists for the subscription's data type. If one already
exists, do nothing.

### Deletion (`Delete`)

Before dropping tables, check `published_views.sources` for references to the
subscription's tables. If found, return an error:

> cannot delete subscription: table X is used by published view Y.
> Run 'pvdata publish' to remove it first.

### Deactivation (`Deactivate`)

No effect on published views. Deactivated subscriptions stop fetching but
their tables and published view participation remain unchanged.

### Preflight

Rename `validatePreferredViews` to `validatePublishedViews`. Check that actual
Postgres view definitions match `published_views` config. Regenerate if out of
sync. If a data type has subscriptions but no published view, prompt the user.

---

## 4. Post-Fetch Hook Chain

Replace `LifeCycleManager` and its implementations with a hook chain on
`Dataset`:

```go
type Dataset struct {
    Name        string
    Description string
    DataTypes   []*data.DataType
    DateRange   func() (time.Time, time.Time)
    TTL         time.Duration
    Fetch       func(context.Context, *library.Subscription,
                     chan<- *data.Observation, chan<- data.RunSummary)
    PostFetch   []func(context.Context, *library.Subscription) error
}
```

In `runSubscription()`, after fetch completes and observations are saved,
iterate through `PostFetch` in order. Stop on first error.

### Built-in hooks

- **`AdjustEodPrices`** -- CRSP dividend/split adjustment. Reads rows from the
  subscription's EOD table ordered by `(composite_figi, event_date DESC)`.
  Walks backwards per asset, accumulating adjustment factor. Writes `adj_close`
  back via batch UPDATE. Scoped to lookback window (or all rows on first run).

- **`PurgeExpiredData`** -- Deletes rows older than `Dataset.TTL`. Replaces
  the stubbed `TTLLifeCycleManager`.

### Registration example

```go
"EOD": {
    Name:      "EOD",
    DataTypes: []*data.DataType{data.DataTypes[data.EODKey]},
    Fetch:     fetchEod,
    PostFetch: []func(context.Context, *library.Subscription) error{
        AdjustEodPrices,
    },
},
```

### Cleanup

- Delete `lifecycle.go`.
- Delete `eod_adjust.go` as a provider (remove from `discover.go`).
- Move adjust function to `provider/hooks.go`.

---

## 5. TUI: `pvdata publish`

### Main screen

Lists all published views:

```
View Name            Data Type    Sources
eod                  eod          2 sources (tiingo, legacy)
ratings              rating       3 sources (zacks, legacy-zacks, legacy-sa)
assets               asset        1 source (massive)
economic_indicators  econ-ind     1 source (fred)
```

### View detail screen

Shows sources with date ranges:

```
Published View: eod

  Source                     From          Until
  eod_tiingo_abc12           2023-01-01    --
  eod_legacy_def34           --            2023-01-01

  [Add Source]  [Remove Source]  [Edit Boundaries]  [Back]
```

### Add Source flow

1. Show subscription tables matching the data type not already in the view.
2. User picks one.
3. If overlap with existing sources, ask:
   - Prefer newer source for overlap period
   - Prefer denser source (query row counts)
   - Set date boundary manually
4. System computes boundaries, shows result.
5. User can edit before confirming.
6. View is regenerated.

### Edit Boundaries

Select a source, edit from/until via text input. Validates non-overlap.
Regenerates view.

### Remove Source

Select a source, confirm. If last source, warn that view will be dropped.
Regenerates view.

### Create new view

If a data type has subscriptions but no published view, offer to create one.

---

## 6. Migration

Migration file `000002_published_views.up.sql`:

```sql
DROP TABLE IF EXISTS dataframe;

CREATE TABLE published_views (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    view_name TEXT NOT NULL UNIQUE,
    data_type_key TEXT NOT NULL,
    sources JSONB NOT NULL DEFAULT '[]'
);
```

Down migration `000002_published_views.down.sql`:

```sql
DROP TABLE IF EXISTS published_views;

CREATE TABLE dataframe (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name TEXT NOT NULL,
    data_type datatype NOT NULL,
    partitioned BOOLEAN DEFAULT false,
    subscriptions TEXT[],
    UNIQUE(name)
);
```

### Code cleanup

- Delete `library/views.go`.
- Delete `cmd/prefer.go`.
- Update preflight validation to use published views.
- Update subscription `Save` and `Delete` to use published views.
