# Split Index Data Type Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the single `IndexKey` data type (which creates two tables via suffixes) with two independent data types `IndexSnapshotKey` and `IndexChangelogKey`, each following the standard 1:1 data-type-to-table convention.

**Architecture:** Each new data type has its own entry in `DataTypes` with a single-table schema. All special-case logic for index suffixes is removed. Providers list both types and look up each table separately from `DataTablesMap`.

**Tech Stack:** Go, PostgreSQL, Ginkgo/Gomega testing

---

### Task 1: Replace IndexKey with two keys in data/datatype.go

**Files:**
- Modify: `data/datatype.go:69-82` (constants) and `data/datatype.go:232-260` (DataTypes map entry)

- [ ] **Step 1: Replace the constant and DataTypes entries**

In `data/datatype.go`, replace the `IndexKey` constant:

```go
// Before:
IndexKey             = "index"

// After:
IndexSnapshotKey  = "index-snapshot"
IndexChangelogKey = "index-changelog"
```

Replace the single `IndexKey` entry in the `DataTypes` map with two entries:

```go
IndexSnapshotKey: {
    Name:     IndexSnapshotKey,
    ViewName: "indices_snapshot",
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
    Name:     IndexChangelogKey,
    ViewName: "indices_changelog",
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
```

- [ ] **Step 2: Verify the project compiles**

Run: `go build ./data/...`
Expected: Compile errors in files that still reference `data.IndexKey` -- this is expected, we fix them in subsequent tasks.

- [ ] **Step 3: Commit**

```bash
git add data/datatype.go
git commit -m "refactor: split IndexKey into IndexSnapshotKey and IndexChangelogKey"
```

---

### Task 2: Update SaveDB methods in data/index.go

**Files:**
- Modify: `data/index.go:56-63` (IndexSnapshot.SaveDB) and `data/index.go:106-117` (IndexChange.SaveDB)

- [ ] **Step 1: Update IndexSnapshot.SaveDB**

Change the SQL from `%[1]s_snapshot` to `%[1]s`:

```go
sql := fmt.Sprintf(`INSERT INTO %[1]s (
    "index_ticker",
    "snapshot_date",
    "constituents"
) VALUES (
    $1, $2, $3
) ON CONFLICT ON CONSTRAINT %[1]s_pkey DO UPDATE SET
    constituents = EXCLUDED.constituents`, tbl)
```

- [ ] **Step 2: Update IndexChange.SaveDB**

Change the SQL from `%[1]s_changelog` to `%[1]s`:

```go
sql := fmt.Sprintf(`INSERT INTO %[1]s (
    "composite_figi",
    "ticker",
    "index_ticker",
    "event_date",
    "action",
    "weight"
) VALUES (
    $1, $2, $3, $4, $5, $6
) ON CONFLICT ON CONSTRAINT %[1]s_pkey DO UPDATE SET
    action = EXCLUDED.action,
    weight = EXCLUDED.weight`, tbl)
```

- [ ] **Step 3: Commit**

```bash
git add data/index.go
git commit -m "refactor: remove table suffix appending from index SaveDB methods"
```

---

### Task 3: Update observation saving in library/database.go

**Files:**
- Modify: `library/database.go:232-242`

- [ ] **Step 1: Replace the two IndexKey lookups with separate keys**

```go
// Before:
if elem.IndexSnapshot != nil && subscription.DataTablesMap[data.IndexKey] != "" {
    if err := elem.IndexSnapshot.SaveDB(ctx, subscription.DataTablesMap[data.IndexKey], conn); err != nil {
        log.Error().Err(err).Msg("cannot save index snapshot to database")
    }
}

if elem.IndexChange != nil && subscription.DataTablesMap[data.IndexKey] != "" {
    if err := elem.IndexChange.SaveDB(ctx, subscription.DataTablesMap[data.IndexKey], conn); err != nil {
        log.Error().Err(err).Msg("cannot save index change to database")
    }
}

// After:
if elem.IndexSnapshot != nil && subscription.DataTablesMap[data.IndexSnapshotKey] != "" {
    if err := elem.IndexSnapshot.SaveDB(ctx, subscription.DataTablesMap[data.IndexSnapshotKey], conn); err != nil {
        log.Error().Err(err).Msg("cannot save index snapshot to database")
    }
}

if elem.IndexChange != nil && subscription.DataTablesMap[data.IndexChangelogKey] != "" {
    if err := elem.IndexChange.SaveDB(ctx, subscription.DataTablesMap[data.IndexChangelogKey], conn); err != nil {
        log.Error().Err(err).Msg("cannot save index change to database")
    }
}
```

- [ ] **Step 2: Commit**

```bash
git add library/database.go
git commit -m "refactor: use separate index data type keys for observation saving"
```

---

### Task 4: Remove special cases from published views

**Files:**
- Modify: `library/published_views.go:62-71` and `library/published_views.go:178-183`
- Modify: `library/published_views_test.go:88-102`

- [ ] **Step 1: Simplify GenerateViewSQL**

Remove the `IndexKey` special case. Every data type now goes through the normal path:

```go
func (pv *PublishedView) GenerateViewSQL() []string {
	return []string{generateUnionSQL(pv.ViewName, "", pv.Sources)}
}
```

- [ ] **Step 2: Remove the tableSuffix parameter from generateUnionSQL**

Since `tableSuffix` is always `""` now, remove it from the function signature and all usage within the function. Every occurrence of `s.TableName + tableSuffix` becomes just `s.TableName`. Update the function comment accordingly.

- [ ] **Step 3: Simplify ValidateSourceTables**

Remove the `IndexKey` special case at `library/published_views.go:178-183`. Every data type validates a single table:

```go
for _, src := range pv.Sources {
    var exists bool

    err := q.QueryRow(ctx,
        `SELECT EXISTS (
           SELECT 1 FROM information_schema.tables
           WHERE table_name = $1 AND table_schema = 'public'
         )`, src.TableName).Scan(&exists)
    if err != nil {
        return fmt.Errorf("check table existence for %s: %w", src.TableName, err)
    }

    if !exists {
        return fmt.Errorf("source table %s does not exist", src.TableName)
    }
}
```

- [ ] **Step 4: Update published_views_test.go**

Replace the "generates two views for index data type" test. The old test expected two SQL statements from one view. Now each data type generates one view, so test them separately:

```go
It("generates view for index-snapshot data type", func() {
    pv := &library.PublishedView{
        ViewName:    "indices_snapshot",
        DataTypeKey: "index-snapshot",
        Sources: []library.ViewSource{
            {TableName: "ishares_index_constituents_abc12_index_snapshot", SubscriptionID: "sub-1"},
        },
    }
    sqls := pv.GenerateViewSQL()
    Expect(sqls).To(HaveLen(1))
    Expect(sqls[0]).To(ContainSubstring("indices_snapshot"))
    Expect(sqls[0]).To(ContainSubstring("ishares_index_constituents_abc12_index_snapshot"))
})

It("generates view for index-changelog data type", func() {
    pv := &library.PublishedView{
        ViewName:    "indices_changelog",
        DataTypeKey: "index-changelog",
        Sources: []library.ViewSource{
            {TableName: "ishares_index_constituents_abc12_index_changelog", SubscriptionID: "sub-1"},
        },
    }
    sqls := pv.GenerateViewSQL()
    Expect(sqls).To(HaveLen(1))
    Expect(sqls[0]).To(ContainSubstring("indices_changelog"))
    Expect(sqls[0]).To(ContainSubstring("ishares_index_constituents_abc12_index_changelog"))
})
```

- [ ] **Step 5: Run published views tests**

Run: `ginkgo run -race ./library/`
Expected: All tests pass.

- [ ] **Step 6: Commit**

```bash
git add library/published_views.go library/published_views_test.go
git commit -m "refactor: remove index special cases from published views"
```

---

### Task 5: Remove subtable routing from web handler

**Files:**
- Modify: `web/handlers_data.go:30-58` and `web/handlers_data.go:87-96`

- [ ] **Step 1: Update indexedColumnsForDataType**

Replace the single `"index"` case with two cases:

```go
case "index-snapshot":
    return []string{"index_ticker", "snapshot_date"}
case "index-changelog":
    return []string{"ticker", "index_ticker", "event_date"}
```

- [ ] **Step 2: Remove the subtable special case**

Delete lines 87-96 in `web/handlers_data.go` (the entire `if datatype == "index"` block).

- [ ] **Step 3: Commit**

```bash
git add web/handlers_data.go
git commit -m "refactor: remove index subtable routing from web handler"
```

---

### Task 6: Remove suffix appending from index_helpers.go

**Files:**
- Modify: `provider/index_helpers.go:100-241`

- [ ] **Step 1: Update LastSnapshotDate**

Change `%s_snapshot` to `%s`:

```go
sql := fmt.Sprintf(`SELECT COALESCE(MAX(snapshot_date), '0001-01-01') FROM %s WHERE index_ticker = $1`, table)
```

- [ ] **Step 2: Update PreviousSnapshotTickers**

Change `%s_snapshot` to `%s` (two occurrences):

```go
sql := fmt.Sprintf(`SELECT ticker, composite_figi FROM %s
    WHERE index_ticker = $1 AND snapshot_date = (
        SELECT MAX(snapshot_date) FROM %s WHERE index_ticker = $1
    )`, table, table)
```

- [ ] **Step 3: Update CurrentIndexMembers signature and body**

Add a second table parameter and remove suffix appending:

```go
func CurrentIndexMembers(ctx context.Context, pool *pgxpool.Pool, snapshotTable, changelogTable, indexTicker string, asOfDate time.Time) map[string]IndexMember {
```

Update the three SQL queries inside:
- `%s_snapshot` becomes `%s` using `snapshotTable` (two occurrences for the snapshot queries)
- `%s_changelog` becomes `%s` using `changelogTable` (one occurrence for the changelog query)

The snapshot date query:
```go
err = conn.QueryRow(ctx,
    fmt.Sprintf(`SELECT COALESCE(MAX(snapshot_date), '0001-01-01') FROM %s WHERE index_ticker = $1 AND snapshot_date <= $2`, snapshotTable),
    indexTicker, asOfDate,
).Scan(&snapshotDate)
```

The constituents query:
```go
err = conn.QueryRow(ctx,
    fmt.Sprintf(`SELECT constituents FROM %s WHERE index_ticker = $1 AND snapshot_date = $2`, snapshotTable),
    indexTicker, snapshotDate,
).Scan(&constituents)
```

The changelog query:
```go
changeRows, err := conn.Query(ctx,
    fmt.Sprintf(`SELECT ticker, composite_figi, action, weight FROM %s WHERE index_ticker = $1 AND event_date > $2 AND event_date <= $3 ORDER BY event_date`, changelogTable),
    indexTicker, snapshotDate, asOfDate,
)
```

- [ ] **Step 4: Commit**

```bash
git add provider/index_helpers.go
git commit -m "refactor: remove suffix appending from index helper functions"
```

---

### Task 7: Update all providers to use both keys

**Files:**
- Modify: `provider/ishares/ishares.go:98,222`
- Modify: `provider/tradingview/tradingview.go:93,290`
- Modify: `provider/nasdaq/nasdaq.go:64,207`
- Modify: `provider/sharadar/sharadar.go:60,80`
- Modify: `provider/sharadar/sharadar_import.go:795`
- Modify: `provider/zacks/zacks.go:71`

- [ ] **Step 1: Update Datasets() DataTypes in all providers**

In each provider's `Datasets()`, replace `data.DataTypes[data.IndexKey]` with both types:

```go
data.DataTypes[data.IndexSnapshotKey],
data.DataTypes[data.IndexChangelogKey],
```

Files to update:
- `provider/ishares/ishares.go:98`
- `provider/tradingview/tradingview.go:93`
- `provider/nasdaq/nasdaq.go:64`
- `provider/sharadar/sharadar.go:60` (Metrics dataset)
- `provider/sharadar/sharadar.go:80` (SP500 dataset)
- `provider/zacks/zacks.go:71`

- [ ] **Step 2: Update DataTablesMap lookups in iShares**

In `provider/ishares/ishares.go:222`, replace:

```go
// Before:
table := subscription.DataTablesMap[data.IndexKey]

// After:
snapshotTable := subscription.DataTablesMap[data.IndexSnapshotKey]
changelogTable := subscription.DataTablesMap[data.IndexChangelogKey]
```

Update the two calls to `provider.CurrentIndexMembers` and `provider.LastSnapshotDate` to use `snapshotTable` (and pass `changelogTable` to `CurrentIndexMembers`):

```go
state := provider.CurrentIndexMembers(ctx, subscription.Library.Pool, snapshotTable, changelogTable, etf.IndexTicker, startDate)
memLastSnapshotDate := provider.LastSnapshotDate(ctx, subscription.Library.Pool, snapshotTable, etf.IndexTicker)
```

- [ ] **Step 3: Update DataTablesMap lookups in TradingView**

In `provider/tradingview/tradingview.go:290`, replace:

```go
// Before:
table := subscription.DataTablesMap[data.IndexKey]

// After:
snapshotTable := subscription.DataTablesMap[data.IndexSnapshotKey]
changelogTable := subscription.DataTablesMap[data.IndexChangelogKey]
```

Update calls to use the correct table variables:
```go
state := provider.CurrentIndexMembers(ctx, subscription.Library.Pool, snapshotTable, changelogTable, idx.Symbol, eventDate)
memLastSnapshotDate := provider.LastSnapshotDate(ctx, subscription.Library.Pool, snapshotTable, idx.Symbol)
```

- [ ] **Step 4: Update DataTablesMap lookups in Nasdaq**

In `provider/nasdaq/nasdaq.go:207`, replace:

```go
// Before:
table := subscription.DataTablesMap[data.IndexKey]

// After:
snapshotTable := subscription.DataTablesMap[data.IndexSnapshotKey]
changelogTable := subscription.DataTablesMap[data.IndexChangelogKey]
```

Update calls:
```go
prevRaw := provider.PreviousSnapshotTickers(ctx, subscription.Library.Pool, snapshotTable, "NDX")
lastDate := provider.LastSnapshotDate(ctx, subscription.Library.Pool, snapshotTable, "NDX")
```

- [ ] **Step 5: Update DataTablesMap lookups in Sharadar SP500 import**

In `provider/sharadar/sharadar_import.go:795`, replace:

```go
// Before:
table := sub.DataTablesMap[data.IndexKey]
lastSnapshotDate := provider.LastSnapshotDate(ctx, sub.Library.Pool, table, sp500IndexTicker)

// After:
snapshotTable := sub.DataTablesMap[data.IndexSnapshotKey]
lastSnapshotDate := provider.LastSnapshotDate(ctx, sub.Library.Pool, snapshotTable, sp500IndexTicker)
```

- [ ] **Step 6: Commit**

```bash
git add provider/ishares/ishares.go provider/tradingview/tradingview.go provider/nasdaq/nasdaq.go provider/sharadar/sharadar.go provider/sharadar/sharadar_import.go provider/zacks/zacks.go
git commit -m "refactor: update all providers to use split index data type keys"
```

---

### Task 8: Build, lint, and test

- [ ] **Step 1: Build the entire project**

Run: `go build ./...`
Expected: Clean build with no errors.

- [ ] **Step 2: Lint**

Run: `golangci-lint run --fix ./...`
Expected: 0 issues.

- [ ] **Step 3: Run all tests**

Run: `ginkgo run -race ./...`
Expected: All tests pass.

- [ ] **Step 4: Fix any failures and commit**

If any tests fail, fix and commit the fixes.
