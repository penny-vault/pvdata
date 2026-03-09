# Published Views Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace preferred views with multi-source published views backed by UNION ALL, add post-fetch hook chain, and provide a TUI for managing published views.

**Architecture:** A new `published_views` database table stores view configurations (name, data type, ordered sources with date ranges). A Go module generates Postgres views from this config. The `Dataset` struct gains a `PostFetch` hook chain replacing `LifeCycleManager`. A `pvdata publish` command opens a bubbletea TUI for managing views.

**Tech Stack:** Go 1.25, PostgreSQL (pgx/v5), bubbletea/lipgloss/huh (TUI), ginkgo/gomega (tests)

**Design doc:** `docs/plans/2026-03-08-published-views-design.md`

---

### Task 1: Database Migration

**Files:**
- Create: `db/migrations/000002_published_views.up.sql`
- Create: `db/migrations/000002_published_views.down.sql`

**Step 1: Write the up migration**

```sql
BEGIN;

DROP TABLE IF EXISTS dataframe;

CREATE TABLE published_views (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    view_name TEXT NOT NULL UNIQUE,
    data_type_key TEXT NOT NULL,
    sources JSONB NOT NULL DEFAULT '[]'
);

COMMIT;
```

**Step 2: Write the down migration**

```sql
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
```

**Step 3: Verify migration compiles**

Run: `go build ./...`
Expected: SUCCESS (migrations are embedded via iofs, no code changes yet)

**Step 4: Commit**

```bash
git add db/migrations/000002_published_views.up.sql db/migrations/000002_published_views.down.sql
git commit -m "feat: add published_views migration, drop dataframe table"
```

---

### Task 2: Published Views Data Model and CRUD

**Files:**
- Create: `library/published_views.go`
- Create: `library/published_views_test.go`

**Step 1: Write the failing test for data model and SQL generation**

Create `library/published_views_test.go` using ginkgo/gomega. The test file needs a suite setup file too -- check if `library/library_suite_test.go` exists, create if not. Tests should cover:

- `ViewSource` struct serialization
- `GenerateViewSQL` with single source, no date bounds -> `CREATE OR REPLACE VIEW x AS SELECT * FROM table1`
- `GenerateViewSQL` with two sources and date bounds -> UNION ALL with WHERE clauses
- `GenerateViewSQL` with zero sources -> `DROP VIEW IF EXISTS x`
- `GenerateViewSQL` for IndexKey -> generates two views (`_snapshot` and `_changelog`)
- `ValidateSources` rejects overlapping date ranges
- `ValidateSources` accepts non-overlapping ranges
- `ValidateSources` accepts single source with no bounds

```go
package library_test

import (
    "testing"
    "time"

    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"
    "github.com/penny-vault/pvdata/library"
)

func TestLibrary(t *testing.T) {
    RegisterFailHandler(Fail)
    RunSpecs(t, "Library Suite")
}

var _ = Describe("PublishedViews", func() {
    Describe("GenerateViewSQL", func() {
        It("generates a simple view for a single source with no date bounds", func() {
            pv := &library.PublishedView{
                ViewName:    "eod",
                DataTypeKey: "eod",
                Sources: []library.ViewSource{
                    {TableName: "eod_tiingo_abc12", SubscriptionID: "sub-uuid-1"},
                },
            }
            sqls := pv.GenerateViewSQL()
            Expect(sqls).To(HaveLen(1))
            Expect(sqls[0]).To(Equal(
                "CREATE OR REPLACE VIEW eod AS SELECT * FROM eod_tiingo_abc12",
            ))
        })

        It("generates UNION ALL with date filters for multiple sources", func() {
            from := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
            until := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
            pv := &library.PublishedView{
                ViewName:    "eod",
                DataTypeKey: "eod",
                Sources: []library.ViewSource{
                    {TableName: "eod_tiingo_abc12", SubscriptionID: "sub-1", FromDate: &from},
                    {TableName: "eod_legacy_def34", SubscriptionID: "sub-2", UntilDate: &until},
                },
            }
            sqls := pv.GenerateViewSQL()
            Expect(sqls).To(HaveLen(1))
            Expect(sqls[0]).To(ContainSubstring("UNION ALL"))
            Expect(sqls[0]).To(ContainSubstring("WHERE event_date >= '2023-01-01'"))
            Expect(sqls[0]).To(ContainSubstring("WHERE event_date < '2023-01-01'"))
        })

        It("generates DROP VIEW for zero sources", func() {
            pv := &library.PublishedView{
                ViewName:    "eod",
                DataTypeKey: "eod",
                Sources:     []library.ViewSource{},
            }
            sqls := pv.GenerateViewSQL()
            Expect(sqls).To(HaveLen(1))
            Expect(sqls[0]).To(Equal("DROP VIEW IF EXISTS eod"))
        })

        It("generates two views for index data type", func() {
            pv := &library.PublishedView{
                ViewName:    "indices",
                DataTypeKey: "index",
                Sources: []library.ViewSource{
                    {TableName: "index_ishares_abc12", SubscriptionID: "sub-1"},
                },
            }
            sqls := pv.GenerateViewSQL()
            Expect(sqls).To(HaveLen(2))
            Expect(sqls[0]).To(ContainSubstring("indices_snapshot"))
            Expect(sqls[0]).To(ContainSubstring("index_ishares_abc12_snapshot"))
            Expect(sqls[1]).To(ContainSubstring("indices_changelog"))
            Expect(sqls[1]).To(ContainSubstring("index_ishares_abc12_changelog"))
        })
    })

    Describe("ValidateSources", func() {
        It("accepts non-overlapping date ranges", func() {
            boundary := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
            pv := &library.PublishedView{
                ViewName:    "eod",
                DataTypeKey: "eod",
                Sources: []library.ViewSource{
                    {TableName: "t1", FromDate: &boundary},
                    {TableName: "t2", UntilDate: &boundary},
                },
            }
            Expect(pv.ValidateSources()).To(Succeed())
        })

        It("rejects overlapping date ranges", func() {
            d1 := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
            d2 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
            d3 := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
            pv := &library.PublishedView{
                ViewName:    "eod",
                DataTypeKey: "eod",
                Sources: []library.ViewSource{
                    {TableName: "t1", FromDate: &d1, UntilDate: &d2},
                    {TableName: "t2", FromDate: &d3},
                },
            }
            Expect(pv.ValidateSources()).NotTo(Succeed())
        })

        It("accepts a single source with no bounds", func() {
            pv := &library.PublishedView{
                ViewName:    "eod",
                DataTypeKey: "eod",
                Sources: []library.ViewSource{
                    {TableName: "t1"},
                },
            }
            Expect(pv.ValidateSources()).To(Succeed())
        })
    })
})
```

**Step 2: Run tests to verify they fail**

Run: `go test ./library/ -v -run TestLibrary`
Expected: FAIL -- `PublishedView`, `ViewSource`, `GenerateViewSQL`, `ValidateSources` not defined

**Step 3: Implement the data model and functions**

Create `library/published_views.go`:

```go
package library

import (
    "context"
    "fmt"
    "sort"
    "strings"
    "time"

    "github.com/google/uuid"
    "github.com/jackc/pgx/v5/pgxpool"
    "github.com/penny-vault/pvdata/data"
    "github.com/rs/zerolog/log"
)

// ViewSource represents one subscription table contributing to a published view.
type ViewSource struct {
    TableName      string     `json:"table_name"`
    SubscriptionID string     `json:"subscription_id"`
    FromDate       *time.Time `json:"from_date,omitempty"`
    UntilDate      *time.Time `json:"until_date,omitempty"`
}

// PublishedView defines a database view composed of one or more subscription tables.
type PublishedView struct {
    ID          uuid.UUID    `json:"id"`
    ViewName    string       `json:"view_name"`
    DataTypeKey string       `json:"data_type_key"`
    Sources     []ViewSource `json:"sources"`
}

// GenerateViewSQL produces the SQL statements to create or drop the view.
// For IndexKey, two statements are returned (snapshot + changelog).
// For zero sources, returns a DROP VIEW statement.
func (pv *PublishedView) GenerateViewSQL() []string {
    if len(pv.Sources) == 0 {
        if pv.DataTypeKey == data.IndexKey {
            return []string{
                fmt.Sprintf("DROP VIEW IF EXISTS %s_snapshot", pv.ViewName),
                fmt.Sprintf("DROP VIEW IF EXISTS %s_changelog", pv.ViewName),
            }
        }
        return []string{fmt.Sprintf("DROP VIEW IF EXISTS %s", pv.ViewName)}
    }

    if pv.DataTypeKey == data.IndexKey {
        return []string{
            pv.generateUnionSQL(pv.ViewName+"_snapshot", "_snapshot"),
            pv.generateUnionSQL(pv.ViewName+"_changelog", "_changelog"),
        }
    }

    return []string{pv.generateUnionSQL(pv.ViewName, "")}
}

func (pv *PublishedView) generateUnionSQL(viewName, tableSuffix string) string {
    var legs []string
    for _, src := range pv.Sources {
        tableName := src.TableName + tableSuffix
        leg := fmt.Sprintf("SELECT * FROM %s", tableName)

        var conditions []string
        if src.FromDate != nil {
            conditions = append(conditions, fmt.Sprintf("event_date >= '%s'", src.FromDate.Format("2006-01-02")))
        }
        if src.UntilDate != nil {
            conditions = append(conditions, fmt.Sprintf("event_date < '%s'", src.UntilDate.Format("2006-01-02")))
        }

        if len(conditions) > 0 {
            leg += " WHERE " + strings.Join(conditions, " AND ")
        }

        legs = append(legs, leg)
    }

    body := strings.Join(legs, "\n  UNION ALL\n  ")
    return fmt.Sprintf("CREATE OR REPLACE VIEW %s AS %s", viewName, body)
}

// ValidateSources checks that no two sources have overlapping date ranges.
func (pv *PublishedView) ValidateSources() error {
    if len(pv.Sources) <= 1 {
        return nil
    }

    // Convert each source to an effective range using sentinel dates
    type effectiveRange struct {
        from  time.Time
        until time.Time
        table string
    }

    minDate := time.Date(1800, 1, 1, 0, 0, 0, 0, time.UTC)
    maxDate := time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)

    ranges := make([]effectiveRange, len(pv.Sources))
    for i, src := range pv.Sources {
        r := effectiveRange{from: minDate, until: maxDate, table: src.TableName}
        if src.FromDate != nil {
            r.from = *src.FromDate
        }
        if src.UntilDate != nil {
            r.until = *src.UntilDate
        }
        ranges[i] = r
    }

    // Sort by from date
    sort.Slice(ranges, func(i, j int) bool {
        return ranges[i].from.Before(ranges[j].from)
    })

    // Check for overlaps: each range's from must be >= previous range's until
    for i := 1; i < len(ranges); i++ {
        if ranges[i].from.Before(ranges[i-1].until) {
            return fmt.Errorf("date ranges overlap between %s and %s", ranges[i-1].table, ranges[i].table)
        }
    }

    return nil
}

// ApplyPublishedView executes the generated SQL to create/replace the view(s).
func ApplyPublishedView(ctx context.Context, pool *pgxpool.Pool, pv *PublishedView) error {
    conn, err := pool.Acquire(ctx)
    if err != nil {
        return err
    }
    defer conn.Release()

    for _, sql := range pv.GenerateViewSQL() {
        if _, err := conn.Exec(ctx, sql); err != nil {
            return fmt.Errorf("apply view %s: %w", pv.ViewName, err)
        }
    }

    log.Info().Str("ViewName", pv.ViewName).Int("Sources", len(pv.Sources)).Msg("applied published view")
    return nil
}

// SavePublishedView upserts a published view record to the database and applies it.
func SavePublishedView(ctx context.Context, pool *pgxpool.Pool, pv *PublishedView) error {
    if err := pv.ValidateSources(); err != nil {
        return err
    }

    conn, err := pool.Acquire(ctx)
    if err != nil {
        return err
    }
    defer conn.Release()

    if pv.ID == uuid.Nil {
        pv.ID = uuid.New()
    }

    _, err = conn.Exec(ctx,
        `INSERT INTO published_views (id, view_name, data_type_key, sources)
         VALUES ($1, $2, $3, $4)
         ON CONFLICT (view_name) DO UPDATE SET data_type_key = $3, sources = $4`,
        pv.ID, pv.ViewName, pv.DataTypeKey, pv.Sources)
    if err != nil {
        return fmt.Errorf("save published view %s: %w", pv.ViewName, err)
    }

    return ApplyPublishedView(ctx, pool, pv)
}

// LoadPublishedViews returns all published view configurations from the database.
func LoadPublishedViews(ctx context.Context, pool *pgxpool.Pool) ([]*PublishedView, error) {
    conn, err := pool.Acquire(ctx)
    if err != nil {
        return nil, err
    }
    defer conn.Release()

    rows, err := conn.Query(ctx, "SELECT id, view_name, data_type_key, sources FROM published_views ORDER BY view_name")
    if err != nil {
        return nil, err
    }
    defer rows.Close()

    var views []*PublishedView
    for rows.Next() {
        pv := &PublishedView{}
        if err := rows.Scan(&pv.ID, &pv.ViewName, &pv.DataTypeKey, &pv.Sources); err != nil {
            return nil, err
        }
        views = append(views, pv)
    }

    return views, nil
}

// LoadPublishedView loads a single published view by view name.
func LoadPublishedView(ctx context.Context, pool *pgxpool.Pool, viewName string) (*PublishedView, error) {
    conn, err := pool.Acquire(ctx)
    if err != nil {
        return nil, err
    }
    defer conn.Release()

    pv := &PublishedView{}
    err = conn.QueryRow(ctx,
        "SELECT id, view_name, data_type_key, sources FROM published_views WHERE view_name = $1",
        viewName).Scan(&pv.ID, &pv.ViewName, &pv.DataTypeKey, &pv.Sources)
    if err != nil {
        return nil, err
    }

    return pv, nil
}

// DeletePublishedView removes a published view config and drops the Postgres view(s).
func DeletePublishedView(ctx context.Context, pool *pgxpool.Pool, viewName string) error {
    pv, err := LoadPublishedView(ctx, pool, viewName)
    if err != nil {
        return err
    }

    // Drop the actual view(s)
    pv.Sources = nil // zero sources triggers DROP VIEW
    if err := ApplyPublishedView(ctx, pool, pv); err != nil {
        return err
    }

    conn, err := pool.Acquire(ctx)
    if err != nil {
        return err
    }
    defer conn.Release()

    _, err = conn.Exec(ctx, "DELETE FROM published_views WHERE view_name = $1", viewName)
    return err
}

// PublishedViewReferencesTable checks if any published view references the given table name.
// Returns the view name if found, empty string otherwise.
func PublishedViewReferencesTable(ctx context.Context, pool *pgxpool.Pool, tableName string) (string, error) {
    views, err := LoadPublishedViews(ctx, pool)
    if err != nil {
        return "", err
    }

    for _, pv := range views {
        for _, src := range pv.Sources {
            if src.TableName == tableName {
                return pv.ViewName, nil
            }
        }
    }

    return "", nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./library/ -v -run TestLibrary`
Expected: PASS (all GenerateViewSQL and ValidateSources tests)

**Step 5: Commit**

```bash
git add library/published_views.go library/published_views_test.go
git commit -m "feat: add published views data model, SQL generation, and validation"
```

---

### Task 3: Update Subscription Lifecycle

**Files:**
- Modify: `library/subscription.go` (Save and Delete methods)
- Delete: `library/views.go`
- Delete: `cmd/prefer.go`

**Step 1: Update subscription Delete to block if referenced**

In `library/subscription.go`, replace the preferred view cleanup block in `Delete()` (lines 116-137) with a pre-deletion check:

```go
// Before dropping tables, check published views for references
for _, tblName := range subscription.DataTables {
    viewName, err := PublishedViewReferencesTable(ctx, subscription.Library.Pool, tblName)
    if err != nil {
        return fmt.Errorf("could not check published views: %w", err)
    }
    if viewName != "" {
        return fmt.Errorf("cannot delete subscription: table %s is used by published view %s. Run 'pvdata publish' to remove it first", tblName, viewName)
    }
}
```

Move this check to the beginning of `Delete()`, before the transaction that drops tables.

**Step 2: Update subscription Save to auto-create published views**

In `library/subscription.go`, replace the preferred view auto-creation block in `Save()` (lines 284-304) with:

```go
// auto-create published views for data types that don't have one yet
existingViews, err := LoadPublishedViews(ctx, subscription.Library.Pool)
if err != nil {
    log.Warn().Err(err).Msg("could not query existing published views")
} else {
    existingSet := make(map[string]bool)
    for _, pv := range existingViews {
        existingSet[pv.ViewName] = true
    }

    for _, dataTypeKey := range subscription.DataTypes {
        dt := data.DataTypes[dataTypeKey]
        if dt == nil || dt.ViewName == "" {
            continue
        }
        if existingSet[dt.ViewName] {
            continue
        }
        tableName := subscription.DataTablesMap[dataTypeKey]
        if tableName == "" {
            continue
        }
        pv := &PublishedView{
            ViewName:    dt.ViewName,
            DataTypeKey: dataTypeKey,
            Sources: []ViewSource{
                {TableName: tableName, SubscriptionID: subscription.ID.String()},
            },
        }
        if err := SavePublishedView(ctx, subscription.Library.Pool, pv); err != nil {
            log.Warn().Err(err).Str("DataType", dataTypeKey).Msg("could not auto-create published view")
        }
    }
}
```

**Step 3: Delete old preferred views code**

Delete `library/views.go` and `cmd/prefer.go`. Remove the `preferCmd` registration from any `init()` function.

**Step 4: Fix compilation errors**

Update any remaining references to `SetPreferredView`, `PreferredViews`, `DropPreferredView` in:
- `tui/preflight.go` (update `validatePreferredViews` -- see Task 4)

Run: `go build ./...`
Expected: May have compile errors from preflight -- those are fixed in Task 4.

**Step 5: Commit**

```bash
git add library/subscription.go
git rm library/views.go cmd/prefer.go
git commit -m "feat: integrate published views into subscription lifecycle, remove preferred views"
```

---

### Task 4: Update Preflight Validation

**Files:**
- Modify: `tui/preflight.go`

**Step 1: Rewrite validatePreferredViews as validatePublishedViews**

Replace the entire `validatePreferredViews` function in `tui/preflight.go` with:

```go
func validatePublishedViews(ctx context.Context, myLibrary *library.Library) error {
    existingViews, err := library.LoadPublishedViews(ctx, myLibrary.Pool)
    if err != nil {
        return fmt.Errorf("could not load published views: %w", err)
    }

    existingSet := make(map[string]bool)
    for _, pv := range existingViews {
        existingSet[pv.ViewName] = true
    }

    allSubs, err := myLibrary.Subscriptions(ctx)
    if err != nil {
        return fmt.Errorf("could not load subscriptions: %w", err)
    }

    // Build map: data type key -> list of (subscription, table name)
    type subTable struct {
        sub       *library.Subscription
        tableName string
    }
    dataTypeProviders := make(map[string][]subTable)
    for _, sub := range allSubs {
        if !sub.Active {
            continue
        }
        for _, dtKey := range sub.DataTypes {
            tableName := sub.DataTablesMap[dtKey]
            if tableName != "" {
                dataTypeProviders[dtKey] = append(dataTypeProviders[dtKey], subTable{sub, tableName})
            }
        }
    }

    for dtKey, providers := range dataTypeProviders {
        dt := data.DataTypes[dtKey]
        if dt == nil || dt.ViewName == "" {
            continue
        }

        if existingSet[dt.ViewName] {
            continue
        }

        if len(providers) == 1 {
            pv := &library.PublishedView{
                ViewName:    dt.ViewName,
                DataTypeKey: dtKey,
                Sources: []library.ViewSource{
                    {TableName: providers[0].tableName, SubscriptionID: providers[0].sub.ID.String()},
                },
            }
            if err := library.SavePublishedView(ctx, myLibrary.Pool, pv); err != nil {
                return fmt.Errorf("could not auto-create published view %s: %w", dt.ViewName, err)
            }
            log.Info().Str("View", dt.ViewName).Str("Table", providers[0].tableName).Msg("auto-created published view")
        } else {
            options := make([]huh.Option[string], len(providers))
            for i, p := range providers {
                label := fmt.Sprintf("%s (%s/%s) -> %s", p.sub.Name, p.sub.Provider, p.sub.Dataset, p.tableName)
                options[i] = huh.NewOption(label, p.tableName)
            }

            var selected string
            form := huh.NewForm(
                huh.NewGroup(
                    huh.NewSelect[string]().
                        Title(fmt.Sprintf("Select the initial table for '%s' published view:", dt.ViewName)).
                        Description("Multiple subscriptions provide this data type. Choose one to start. Use 'pvdata publish' to add more sources.").
                        Options(options...).
                        Value(&selected),
                ),
            )

            if err := form.Run(); err != nil {
                return fmt.Errorf("view selection for %s cancelled: %w", dt.ViewName, err)
            }

            // Find the subscription ID for the selected table
            var subID string
            for _, p := range providers {
                if p.tableName == selected {
                    subID = p.sub.ID.String()
                    break
                }
            }

            pv := &library.PublishedView{
                ViewName:    dt.ViewName,
                DataTypeKey: dtKey,
                Sources: []library.ViewSource{
                    {TableName: selected, SubscriptionID: subID},
                },
            }
            if err := library.SavePublishedView(ctx, myLibrary.Pool, pv); err != nil {
                return fmt.Errorf("could not create published view %s: %w", dt.ViewName, err)
            }
        }
    }

    // Re-apply all published views to ensure Postgres views are in sync
    allViews, err := library.LoadPublishedViews(ctx, myLibrary.Pool)
    if err != nil {
        return fmt.Errorf("could not reload published views: %w", err)
    }
    for _, pv := range allViews {
        if err := library.ApplyPublishedView(ctx, myLibrary.Pool, pv); err != nil {
            log.Warn().Err(err).Str("View", pv.ViewName).Msg("could not re-apply published view")
        }
    }

    return nil
}
```

**Step 2: Update the call site in RunPreflight**

Change `validatePreferredViews` to `validatePublishedViews` in the `RunPreflight` function.

**Step 3: Verify compilation**

Run: `go build ./...`
Expected: SUCCESS

**Step 4: Commit**

```bash
git add tui/preflight.go
git commit -m "feat: update preflight validation to use published views"
```

---

### Task 5: Post-Fetch Hook Chain

**Files:**
- Modify: `provider/provider.go`
- Delete: `provider/lifecycle.go`
- Delete: `provider/eod_adjust.go`
- Create: `provider/hooks.go`
- Create: `provider/hooks_test.go`
- Modify: `provider/discover.go` (remove eod_adjust)
- Modify: `cmd/run.go` (call PostFetch after ingestion)
- Modify: all provider files that reference `LifeCycle` field

**Step 1: Write failing test for AdjustEodPrices logic**

Create `provider/hooks_test.go`. The CRSP adjustment algorithm is pure math -- test it without a database. Extract the core calculation into a testable function:

```go
package provider_test

import (
    "testing"

    . "github.com/onsi/ginkgo/v2"
    . "github.com/onsi/gomega"
    "github.com/penny-vault/pvdata/provider"
)

func TestProvider(t *testing.T) {
    RegisterFailHandler(Fail)
    RunSpecs(t, "Provider Suite")
}

var _ = Describe("AdjustEodPrices", func() {
    Describe("ComputeAdjustedClose", func() {
        It("returns unadjusted prices when no dividends or splits", func() {
            rows := []provider.EodRow{
                {Close: 100.0, Dividend: 0, SplitFactor: 1.0},
                {Close: 99.0, Dividend: 0, SplitFactor: 1.0},
                {Close: 98.0, Dividend: 0, SplitFactor: 1.0},
            }
            result := provider.ComputeAdjustedClose(rows)
            Expect(result[0].AdjClose).To(BeNumerically("~", 100.0, 0.01))
            Expect(result[1].AdjClose).To(BeNumerically("~", 99.0, 0.01))
            Expect(result[2].AdjClose).To(BeNumerically("~", 98.0, 0.01))
        })

        It("adjusts for a 2:1 stock split", func() {
            // Rows in reverse chronological order (newest first)
            rows := []provider.EodRow{
                {Close: 50.0, Dividend: 0, SplitFactor: 1.0},  // after split
                {Close: 100.0, Dividend: 0, SplitFactor: 2.0}, // split day
                {Close: 99.0, Dividend: 0, SplitFactor: 1.0},  // before split
            }
            result := provider.ComputeAdjustedClose(rows)
            Expect(result[0].AdjClose).To(BeNumerically("~", 50.0, 0.01))
            Expect(result[1].AdjClose).To(BeNumerically("~", 50.0, 0.01))
            Expect(result[2].AdjClose).To(BeNumerically("~", 49.5, 0.01))
        })

        It("adjusts for a dividend", func() {
            // Rows in reverse chronological order
            rows := []provider.EodRow{
                {Close: 100.0, Dividend: 0, SplitFactor: 1.0},
                {Close: 102.0, Dividend: 2.0, SplitFactor: 1.0}, // $2 dividend
                {Close: 101.0, Dividend: 0, SplitFactor: 1.0},
            }
            result := provider.ComputeAdjustedClose(rows)
            // After the dividend, adjust factor = (1 + 2/102) * 1 = ~1.0196
            Expect(result[0].AdjClose).To(BeNumerically("~", 100.0, 0.01))
            Expect(result[1].AdjClose).To(BeNumerically("~", 100.0, 0.01))
            // 101.0 / 1.0196 = ~99.07
            Expect(result[2].AdjClose).To(BeNumerically("~", 99.07, 0.1))
        })

        It("handles zero close price gracefully", func() {
            rows := []provider.EodRow{
                {Close: 10.0, Dividend: 0, SplitFactor: 1.0},
                {Close: 0.0, Dividend: 0, SplitFactor: 1.0},
                {Close: 9.0, Dividend: 0, SplitFactor: 1.0},
            }
            result := provider.ComputeAdjustedClose(rows)
            Expect(result).To(HaveLen(3))
            // Should not panic; adjust factor resets to 1.0
        })
    })
})
```

**Step 2: Run tests to verify they fail**

Run: `go test ./provider/ -v -run TestProvider`
Expected: FAIL -- `EodRow`, `ComputeAdjustedClose` not defined

**Step 3: Update Dataset struct and create hooks**

Modify `provider/provider.go` -- remove `LifeCycle` field, add `PostFetch`:

```go
type Dataset struct {
    Name        string
    Description string
    DataTypes   []*data.DataType
    DateRange   func() (time.Time, time.Time)
    TTL         time.Duration
    Fetch       func(context.Context, *library.Subscription, chan<- *data.Observation, chan<- data.RunSummary)
    PostFetch   []func(context.Context, *library.Subscription) error
}
```

Create `provider/hooks.go`:

```go
package provider

import (
    "context"
    "fmt"

    "github.com/penny-vault/pvdata/library"
    "github.com/rs/zerolog/log"
)

// EodRow represents a single EOD record for adjustment calculation.
type EodRow struct {
    Close       float64
    Dividend    float64
    SplitFactor float64
    AdjClose    float64
}

// ComputeAdjustedClose implements CRSP dividend/split adjustment.
// Rows must be in reverse chronological order (newest first).
// See: http://crsp.org/products/documentation/crsp-calculations
func ComputeAdjustedClose(rows []EodRow) []EodRow {
    adjustFactor := 1.0
    for i := range rows {
        rows[i].AdjClose = rows[i].Close / adjustFactor
        if rows[i].Close > 0 {
            adjustFactor *= (1 + (rows[i].Dividend / rows[i].Close)) * rows[i].SplitFactor
        } else {
            adjustFactor = 1.0
        }
    }
    return rows
}

// AdjustEodPrices is a PostFetch hook that computes adjusted close prices
// for all EOD data in the subscription's table.
func AdjustEodPrices(ctx context.Context, subscription *library.Subscription) error {
    tableName := subscription.DataTablesMap["eod"]
    if tableName == "" {
        return nil
    }

    log.Info().Str("Table", tableName).Msg("adjusting EOD prices")

    conn, err := subscription.Library.Pool.Acquire(ctx)
    if err != nil {
        return fmt.Errorf("acquire connection for eod adjust: %w", err)
    }
    defer conn.Release()

    // Get distinct composite_figis
    figiRows, err := conn.Query(ctx,
        fmt.Sprintf("SELECT DISTINCT composite_figi FROM %s", tableName))
    if err != nil {
        return fmt.Errorf("query distinct figis: %w", err)
    }

    var figis []string
    for figiRows.Next() {
        var figi string
        if err := figiRows.Scan(&figi); err != nil {
            figiRows.Close()
            return err
        }
        figis = append(figis, figi)
    }
    figiRows.Close()

    for _, figi := range figis {
        rows, err := conn.Query(ctx,
            fmt.Sprintf("SELECT close, dividend, split_factor FROM %s WHERE composite_figi = $1 ORDER BY event_date DESC", tableName),
            figi)
        if err != nil {
            log.Error().Err(err).Str("FIGI", figi).Msg("query eod for adjustment failed")
            continue
        }

        var eodRows []EodRow
        var eventDates []interface{}
        for rows.Next() {
            var r EodRow
            if err := rows.Scan(&r.Close, &r.Dividend, &r.SplitFactor); err != nil {
                rows.Close()
                return err
            }
            eodRows = append(eodRows, r)
        }
        rows.Close()

        if len(eodRows) == 0 {
            continue
        }

        ComputeAdjustedClose(eodRows)

        // Batch update adj_close values
        // Re-query with event_date to match rows for update
        dateRows, err := conn.Query(ctx,
            fmt.Sprintf("SELECT event_date FROM %s WHERE composite_figi = $1 ORDER BY event_date DESC", tableName),
            figi)
        if err != nil {
            log.Error().Err(err).Str("FIGI", figi).Msg("query event dates failed")
            continue
        }

        tx, err := conn.Begin(ctx)
        if err != nil {
            dateRows.Close()
            return err
        }

        i := 0
        for dateRows.Next() {
            var eventDate interface{}
            if err := dateRows.Scan(&eventDate); err != nil {
                dateRows.Close()
                tx.Rollback(ctx)
                return err
            }
            if _, err := tx.Exec(ctx,
                fmt.Sprintf("UPDATE %s SET adj_close = $1 WHERE composite_figi = $2 AND event_date = $3", tableName),
                eodRows[i].AdjClose, figi, eventDate); err != nil {
                dateRows.Close()
                tx.Rollback(ctx)
                return err
            }
            i++
        }
        dateRows.Close()

        if err := tx.Commit(ctx); err != nil {
            return fmt.Errorf("commit adj_close update for %s: %w", figi, err)
        }
    }

    log.Info().Str("Table", tableName).Int("Assets", len(figis)).Msg("EOD price adjustment complete")
    return nil
}
```

**Step 4: Run tests to verify they pass**

Run: `go test ./provider/ -v -run TestProvider`
Expected: PASS

**Step 5: Remove old lifecycle code and eod_adjust provider**

Delete `provider/lifecycle.go` and `provider/eod_adjust.go`.

Remove `LifeCycle` references from all provider Dataset declarations. Search for `LifeCycle` across provider files (tiingo.go, sharadar.go, fred.go, etc.) and remove those field assignments.

Remove the eod_adjust entry from `provider/discover.go` if present (check Map -- it may not be registered there since it was incomplete).

**Step 6: Wire PostFetch into runSubscription**

In `cmd/run.go`, after `wg.Wait()` in `runSubscription()` (line ~203), add:

```go
// Run post-fetch hooks
if summary.Status == data.RunSuccess && len(subDataset.PostFetch) > 0 {
    for _, hook := range subDataset.PostFetch {
        if err := hook(ctx, subscription); err != nil {
            logger.Error().Err(err).Msg("post-fetch hook failed")
            break
        }
    }
}
```

**Step 7: Register AdjustEodPrices on Tiingo EOD dataset**

In `provider/tiingo.go`, add `PostFetch` to the EOD dataset:

```go
PostFetch: []func(context.Context, *library.Subscription) error{
    AdjustEodPrices,
},
```

**Step 8: Verify compilation**

Run: `go build ./...`
Expected: SUCCESS

**Step 9: Commit**

```bash
git add provider/provider.go provider/hooks.go provider/hooks_test.go cmd/run.go provider/tiingo.go provider/discover.go
git rm provider/lifecycle.go provider/eod_adjust.go
git commit -m "feat: add post-fetch hook chain with EOD price adjustment, remove LifeCycleManager"
```

---

### Task 6: Publish Command and TUI

**Files:**
- Create: `cmd/publish.go`
- Create: `tui/publish.go`

**Step 1: Create the cobra command**

Create `cmd/publish.go`:

```go
package cmd

import (
    "context"
    "fmt"
    "os"

    tea "github.com/charmbracelet/bubbletea"
    "github.com/penny-vault/pvdata/library"
    "github.com/penny-vault/pvdata/tui"
    "github.com/rs/zerolog/log"
    "github.com/spf13/cobra"
    "github.com/spf13/viper"
)

var publishCmd = &cobra.Command{
    Use:   "publish",
    Short: "Manage published views",
    Long: `Manage published views that combine multiple subscription tables into
unified queryable views. Published views use UNION ALL with optional date-range
filters to compose data from multiple sources (e.g., legacy data + current provider).

Opens a TUI for adding, removing, and editing view sources.`,
    Run: func(cmd *cobra.Command, args []string) {
        ctx := context.Background()

        myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
        if err != nil {
            log.Fatal().Err(err).Msg("could not connect to library")
        }
        defer myLibrary.Close()

        model := tui.NewPublishModel(ctx, myLibrary)
        p := tea.NewProgram(model, tea.WithAltScreen())
        if _, err := p.Run(); err != nil {
            fmt.Fprintf(os.Stderr, "Error: %v\n", err)
            os.Exit(1)
        }
    },
}

func init() {
    rootCmd.AddCommand(publishCmd)
}
```

**Step 2: Create the TUI model**

Create `tui/publish.go`. This is the largest file. It implements a bubbletea model with multiple screens:

- **List screen:** table of all published views
- **Detail screen:** sources for a selected view with actions
- **Add source screen:** pick subscription, resolve overlaps, set dates
- **Edit boundaries screen:** text inputs for from/until dates

The full implementation should use:
- `github.com/charmbracelet/bubbles/table` for the views list
- `github.com/charmbracelet/bubbles/textinput` for date editing
- `github.com/charmbracelet/huh` for selection prompts
- `github.com/charmbracelet/lipgloss` for styling (reuse styles from `tui/styles.go`)

Key states/screens:

```go
type publishScreen int

const (
    screenList publishScreen = iota
    screenDetail
    screenAddSource
    screenEditBoundary
)
```

The model tracks:
- Current screen
- List of all published views (loaded from DB)
- Currently selected view
- All subscriptions (for finding candidates when adding sources)
- Form state for add/edit flows

For the "add source" flow with overlap resolution:
- When a new source's data range overlaps with existing sources, present three options using `huh.Select`:
  1. "Prefer newer source" -- set boundary at existing source's `last_obs_date`
  2. "Prefer denser source" -- query row counts in the overlap period, set boundary where the denser source starts
  3. "Set date manually" -- show text input for the date

After any change, call `library.SavePublishedView()` to persist and regenerate the Postgres view.

This file will be ~300-400 lines. The implementer should follow the patterns in `tui/tui.go` and `tui/subscriptions.go` for model structure, key handling, and rendering.

**Step 3: Verify compilation**

Run: `go build ./...`
Expected: SUCCESS

**Step 4: Manual testing**

Run: `go run . publish`
Expected: TUI opens showing published views list. Can navigate, add/remove sources, edit boundaries.

**Step 5: Commit**

```bash
git add cmd/publish.go tui/publish.go
git commit -m "feat: add pvdata publish command with TUI for managing published views"
```

---

### Task 7: Integration Test and Cleanup

**Files:**
- Modify: `provider/hooks_test.go` (add more edge cases if needed)
- Review: all modified files for unused imports, dead code

**Step 1: Run full test suite**

Run: `go test ./... -v`
Expected: All tests pass

**Step 2: Run build and vet**

Run: `go build ./... && go vet ./...`
Expected: No errors or warnings

**Step 3: Check for dead code references**

Search for any remaining references to:
- `PreferredViews`
- `SetPreferredView`
- `DropPreferredView`
- `preferCmd`
- `LifeCycleManager`
- `RetainLifeCycleManager`
- `TTLLifeCycleManager`
- `EodAdjust` (the provider struct)

Run: `grep -r "PreferredView\|preferCmd\|LifeCycleManager\|RetainLife\|TTLLife\|EodAdjust" --include='*.go' .`
Expected: No matches

**Step 4: Commit any cleanup**

```bash
git add -A
git commit -m "chore: clean up dead references to preferred views and lifecycle manager"
```

---

## Task Dependency Order

```
Task 1 (migration)
  -> Task 2 (data model + CRUD)
    -> Task 3 (subscription lifecycle)
    -> Task 4 (preflight validation)
      -> Task 5 (post-fetch hooks) [independent of 3/4]
        -> Task 6 (TUI)
          -> Task 7 (integration + cleanup)
```

Tasks 3 and 4 depend on Task 2. Task 5 is independent of 3/4 but should come after 2 so the full build compiles. Task 6 depends on all prior tasks. Task 7 is final validation.
