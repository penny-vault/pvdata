# Assets Publication: Priority Dedup & UI

## Overview

The `assets` published view is built today as a plain `UNION ALL` over its source tables. When the same asset (same `(ticker, composite_figi)` pair) is provided by more than one source -- e.g. Tiingo and Sharadar both list AAPL -- the view returns it twice with conflicting metadata.

This design changes the assets publication so that, given a priority-ordered list of sources, each `(ticker, composite_figi)` pair appears exactly once, taken whole from the highest-priority source that contains it. Lower-priority sources contribute only rows whose key is absent from every higher-priority source.

The same change also moves view-SQL generation off of `library/published_views.go` and onto the `data.DataType` itself, replacing the type-key `switch` in `dateColumnForType` with per-type fields. Asset-specific dedup behavior becomes a property declared on the `AssetKey` entry, not a hardcoded branch in the view builder.

The web UI gains drag-handle reordering on every publication detail page (since source-array order is the priority axis), and an asset-only branch that suppresses the date-range columns and dialog (date bounds are silently ignored for assets today; the UI accepting them is a UX bug).

## Goals

- One row per `(ticker, composite_figi)` in the `assets` view, taken whole from the highest-priority source containing that key.
- Source array order is the priority axis: index 0 is highest priority.
- View-SQL generation is owned by the `data.DataType`, not by `library/published_views.go`.
- UI reflects priority via row order and lets the user reorder sources by drag.
- Asset publication detail UI hides date-range affordances; non-asset publications keep them.

## Non-goals

- Per-column coalesce (cherry-picking individual columns from different sources). Whole-row priority only.
- Dedup behavior for any non-asset publication. The mechanism generalizes, but no other data type opts in.
- Materialization. The view stays a `CREATE OR REPLACE VIEW`, evaluated at query time.
- New REST endpoints. The existing `PUT /publications/:id` already accepts an ordered `sources` array.

## Architecture

### Module boundaries

`data/` becomes the home of all "what does a published view of this data type look like" knowledge. Specifically it owns:

- `ViewSource` (moved from `library/`)
- `(*DataType).GenerateViewSQL(viewName string, sources []ViewSource) string`
- Two new fields on `DataType`: `DateColumn string`, `DedupKeys []string`
- Private helpers: `buildLeg`, `buildWhereClause`, `buildPlainUnion`, `buildDedupedUnion`

`library/published_views.go` keeps persistence (load/save/delete), source-table existence validation, and overlap-checking. Its `PublishedView.GenerateViewSQL()` becomes a thin delegation:

```go
func (pv *PublishedView) GenerateViewSQL() []string {
    dt := data.DataTypes[pv.DataTypeKey]
    if dt == nil {
        return []string{fmt.Sprintf("DROP VIEW IF EXISTS %s", pv.ViewName)}
    }
    return []string{dt.GenerateViewSQL(pv.ViewName, pv.Sources)}
}
```

The standalone `dateColumnForType` switch is deleted. `library.generateUnionSQL`, `buildLeg`, `buildWhereClause` are deleted; their replacements live in `data/`.

`library.CheckOverlaps` short-circuits to `nil` when the data type has `DateColumn == ""`. Today this only matters for assets, but the rule is general: types with no date axis cannot have overlaps.

`web/handlers_publications.go` is unchanged. `PUT /publications/:id` already accepts an ordered `sources` array, which is all reordering needs.

`web/ui/src/pages/PublicationDetailPage.vue` gains drag-handle reordering on every publication and a `data_type_key === 'asset-description'` branch that hides the From/Until columns and the Edit Date Bounds dialog.

### `DataType` shape

```go
type DataType struct {
    Name              string
    ViewName          string
    Schema            string
    Migrations        []string
    Version           int
    IsPartitioned     bool
    PartitionInterval string
    ViewGenerator     ViewGenerator   // unchanged; per-leg SELECT/FROM customization

    DateColumn        string          // NEW. "" disables WHERE bounds.
    DedupKeys         []string        // NEW. empty = no dedup.
}
```

Per-type values:

| Data type           | `DateColumn`     | `DedupKeys`                        |
|---------------------|------------------|------------------------------------|
| `AssetKey`          | `""`             | `[]string{"ticker","composite_figi"}` |
| `IndexSnapshotKey`  | `"snapshot_date"`| nil                                |
| all others          | `"event_date"`   | nil                                |

`ViewSource` moves from `library/published_views.go` into a new `data/view_source.go`:

```go
package data

type ViewSource struct {
    TableName      string     `json:"table_name"`
    SubscriptionID string     `json:"subscription_id"`
    FromDate       *time.Time `json:"from_date,omitempty"`
    UntilDate      *time.Time `json:"until_date,omitempty"`
}
```

JSON tags are unchanged so the on-disk `published_views.sources` JSONB column round-trips identically. The existing `library.ViewSource` is removed; references update to `data.ViewSource`. `library.PublishedView.Sources` becomes `[]data.ViewSource`.

## SQL strategy

For deduped data types, each lower-priority leg is anti-joined against every higher-priority leg on the dedup key:

```sql
CREATE OR REPLACE VIEW assets AS
SELECT * FROM source_1
UNION ALL
SELECT * FROM source_2 s
  WHERE NOT EXISTS (SELECT 1 FROM source_1 p
                    WHERE p.ticker = s.ticker AND p.composite_figi = s.composite_figi)
UNION ALL
SELECT * FROM source_3 s
  WHERE NOT EXISTS (SELECT 1 FROM source_1 p
                    WHERE p.ticker = s.ticker AND p.composite_figi = s.composite_figi)
    AND NOT EXISTS (SELECT 1 FROM source_2 p
                    WHERE p.ticker = s.ticker AND p.composite_figi = s.composite_figi);
```

Properties of this form:

- Each leg keeps `SELECT *`. No column enumeration, no asset-schema duplication. The generated `search` tsvector column flows through naturally.
- Higher-priority rows pass through unfiltered.
- Lower-priority rows are filtered by `NOT EXISTS` against every higher-priority leg.
- The dedup key column list is parameterized by `dt.DedupKeys`; not asset-specific in code.
- For N sources the expression has O(N) legs and the i-th leg has (i-1) `NOT EXISTS` clauses. Realistic N is 2-5; Postgres plans these as hash anti-joins.

For non-deduped types the path is unchanged: each leg is `SELECT * FROM t [WHERE date_col >= '...' AND date_col < '...']` joined with `UNION ALL`.

`buildLeg` for a deduped type ignores `FromDate`/`UntilDate` (asset `DateColumn` is `""`), matching today's silent-ignore behavior.

### Dispatch

```go
func (dt *DataType) GenerateViewSQL(viewName string, sources []ViewSource) string {
    if len(sources) == 0 {
        return fmt.Sprintf("DROP VIEW IF EXISTS %s", viewName)
    }
    if len(dt.DedupKeys) > 0 {
        return dt.buildDedupedUnion(viewName, sources)
    }
    return dt.buildPlainUnion(viewName, sources)
}
```

`buildPlainUnion` is the current `library.generateUnionSQL` body, relocated. `buildDedupedUnion` emits the anti-join form above.

## UI

`web/ui/src/pages/PublicationDetailPage.vue`:

### All publications

Drag-handle row reordering via PrimeVue's `reorderableRows`:

```vue
<DataTable
  :value="publication.sources"
  :reorderableRows="true"
  @row-reorder="onRowReorder">
  <Column rowReorder style="width: 3rem" />
  <Column field="table_name" header="Source Table" />
  <Column field="subscription_name" header="Subscription" />
  <!-- ... -->
</DataTable>
```

`onRowReorder({ value })`:
1. Updates local `publication.value.sources` to the new order.
2. Calls `updatePublication(id, { sources: [...new order...] })`.
3. On error, reverts local state and surfaces a `Message`.

For non-asset publications the date columns and Edit Dates dialog stay exactly as today.

### Asset publication only

When `publication.value.data_type_key === 'asset-description'`:

- Hide the **From** column.
- Hide the **Until** column.
- Hide the pencil "Edit Date Bounds" button in the Actions column.
- Show a leading numeric **Priority** column displaying `index + 1`.
- Skip rendering of the per-source overlap warnings (overlaps don't apply when there is no date axis).

Trash/remove and Add Source flows are unchanged.

A small computed `isAssetPublication` drives the v-if branches; no new component split is needed.

## Backward compatibility

Existing rows in `published_views` survive untouched -- the JSON shape is purely additive. Stale `from_date`/`until_date` on asset sources can persist; SQL generation ignores them and the UI no longer surfaces them.

On `pvdata serve` startup, the existing scheduler-bootstrap path gains one call: `library.RebuildAllPublishedViews(ctx, pool)`. This loads every row from `published_views` and runs `ApplyPublishedView` on each. `CREATE OR REPLACE VIEW` is idempotent, so this is safe to run on every startup. Effect: deploying the new binary causes the `assets` view to be rebuilt with dedup semantics on the first server start, with no manual user action.

`RebuildAllPublishedViews` is implemented in `library/published_views.go` next to the existing helpers; it's a thin wrapper around `LoadPublishedViews` + per-row `ApplyPublishedView`, with errors logged and accumulated rather than aborting the whole rebuild.

## Testing

### `data/` (new test file `data/datatype_view_sql_test.go`, ginkgo)

Table-driven cases for `(*DataType).GenerateViewSQL`:

- Empty sources -> `DROP VIEW IF EXISTS <name>`
- 1, 2, 3 sources, no dedup, no date column (asset-old-style) -> plain `UNION ALL`
- 1, 2, 3 sources, dedup keys -> expected anti-join SQL, with the higher-priority `NOT EXISTS` clauses growing on each leg
- Date-bounded sources with `DateColumn = "event_date"` -> WHERE clauses applied per leg
- `DateColumn = "snapshot_date"` -> same, with the alternate column name
- Date-bounded plus `DedupKeys` (hypothetical) -> bounds applied inside each tagged leg
- A `ViewGenerator`-customized type -> custom SELECT/FROM substituted on each leg

### `library/published_views_test.go`

- Reduce to: `PublishedView.GenerateViewSQL` delegates correctly to `dt.GenerateViewSQL`.
- Move existing SQL-shape assertions to the data-package suite.
- Keep `CheckOverlaps` tests; add one asserting overlaps return `nil` when `dt.DateColumn == ""`.
- Add a test that updates the source order and verifies the regenerated SQL reflects the new order.

### `web/handlers_publications_test.go`

- Add a case where `UpdatePublication` is called with a reordered `sources` array and the persisted JSON in `published_views` reflects the new order.

### Manual UI verification

- Asset publication detail page hides From/Until columns and the date-edit dialog; shows the Priority column.
- Drag-reordering a non-asset publication source list persists across reload.
- Non-asset publications still show date columns and the Edit Date Bounds dialog.

## Risks

- **Anti-join performance at high N.** Each leg gains a `NOT EXISTS` per higher-priority leg. At N=10 sources, leg 10 has 9 anti-joins. Realistic N is 2-5; if this grows, switch to a `DISTINCT ON` form with an explicit column list cached on `DataType`. Not implemented now; revisit only if performance is observed to degrade.
- **`SELECT *` schema drift.** All asset source tables are created from the same `data.DataTypes[AssetKey].Schema`, so column shape matches across legs. If a future migration adds a column to one source-table family without bumping the schema for all, the `UNION ALL` will fail at view-creation time. This is the same risk that exists today; not introduced by this change.
- **Forgotten startup rebuild.** If `RebuildAllPublishedViews` is skipped, deployed servers keep the pre-dedup `assets` view until someone touches the publication. Mitigation: wire the call into the same place that registers cron jobs in `pvdata serve` and add a log line confirming the rebuild ran.
