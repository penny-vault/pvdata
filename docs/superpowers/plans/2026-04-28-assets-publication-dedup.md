# Assets Publication Priority Dedup Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the `assets` published view emit one row per `(ticker, composite_figi)` pair, taken whole from the highest-priority source containing that key, while moving view-SQL generation onto `data.DataType` and adding drag-handle reorder UI to publication detail pages.

**Architecture:** Move `ViewSource` and view-SQL generation into the `data` package as a method on `*DataType`. Add two new fields on `DataType`: `DateColumn` (replaces the type-key switch in `library.dateColumnForType`) and `DedupKeys` (turns on a NOT-EXISTS-anti-join SQL form). Asset dedup is configured by setting `DedupKeys: []string{"ticker", "composite_figi"}` on the `AssetKey` entry. `library/published_views.go` keeps persistence and overlap-checking; its SQL generator becomes a one-line delegation. UI gets PrimeVue `reorderableRows` for every publication and an asset-only branch that hides date affordances.

**Tech Stack:** Go (pgx/v5, ginkgo v2, gomega), Vue 3 + PrimeVue, golang-migrate (no schema changes here).

**Key files (created or modified):**
- Create: `data/view_source.go` -- `ViewSource` struct (moves from `library`)
- Create: `data/view_sql.go` -- `(*DataType).GenerateViewSQL` and helpers
- Create: `data/view_sql_test.go` -- ginkgo tests for the new method
- Modify: `data/datatype.go` -- add `DateColumn`, `DedupKeys` fields; populate per type; set `DedupKeys` on `AssetKey`
- Modify: `library/published_views.go` -- alias `ViewSource` to `data.ViewSource`; delegate `GenerateViewSQL`; delete `generateUnionSQL`/`buildLeg`/`buildWhereClause`/`dateColumnForType`; short-circuit `CheckOverlaps`; add `RebuildAllPublishedViews`
- Modify: `library/published_views_test.go` -- update the asset case; add `CheckOverlaps` short-circuit test
- Modify: `cmd/serve.go` -- call `RebuildAllPublishedViews` after `MigrateAllSubscriptions`
- Modify: `web/ui/src/pages/PublicationDetailPage.vue` -- drag-handle reorder + asset branch
- (No change) `web/handlers_publications.go`, `web/route.go`, `tui/*.go` -- keep using `library.ViewSource` via type alias

---

### Task 1: Move `ViewSource` to `data/` with a `library` alias

**Files:**
- Create: `data/view_source.go`
- Modify: `library/published_views.go:42-47`

- [ ] **Step 1: Create `data/view_source.go`**

```go
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

import "time"

// ViewSource represents a single table contributing to a published view,
// optionally bounded by a date range. FromDate is inclusive, UntilDate is
// exclusive. The bounds are silently ignored for data types whose DateColumn
// is empty (e.g. asset descriptions).
type ViewSource struct {
	TableName      string     `json:"table_name"`
	SubscriptionID string     `json:"subscription_id"`
	FromDate       *time.Time `json:"from_date,omitempty"`
	UntilDate      *time.Time `json:"until_date,omitempty"`
}
```

- [ ] **Step 2: Replace the struct in `library/published_views.go` with a type alias**

Old (lines 40-47):

```go
// ViewSource represents a single table contributing to a published view,
// optionally bounded by a date range. FromDate is inclusive, UntilDate is exclusive.
type ViewSource struct {
	TableName      string     `json:"table_name"`
	SubscriptionID string     `json:"subscription_id"`
	FromDate       *time.Time `json:"from_date,omitempty"`
	UntilDate      *time.Time `json:"until_date,omitempty"`
}
```

New:

```go
// ViewSource is re-exported from the data package so that existing callers
// (web handlers, TUI, library tests) continue to compile against
// library.ViewSource. New code may use data.ViewSource directly.
type ViewSource = data.ViewSource
```

The `time` import may now be unused inside `library/published_views.go` if no other code in the file references it. Verify and remove only if so. Confirm `data` is already imported (it is; the file already uses `data.DataTypes` and `data.AssetKey`).

- [ ] **Step 3: Build and run all tests**

Run: `go build ./... && ginkgo run -race ./library/ ./data/ ./web/`
Expected: PASS. The type alias keeps every existing call site (`library.ViewSource{...}` literals in `tui/`, `web/`, `library/`) compiling and behaving identically.

- [ ] **Step 4: Commit**

```bash
git add data/view_source.go library/published_views.go
git commit -m "refactor: move ViewSource into data package with library alias"
```

---

### Task 2: Add `DateColumn` and `DedupKeys` fields to `DataType` (no behavior change)

**Files:**
- Modify: `data/datatype.go:70-79` (DataType struct)
- Modify: `data/datatype.go:103-...` (each DataTypes entry)

- [ ] **Step 1: Extend the `DataType` struct**

Edit `data/datatype.go`. Replace the struct (lines 70-79) with:

```go
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
```

- [ ] **Step 2: Set `DateColumn` on every `DataTypes` entry**

Within `var DataTypes = map[string]*DataType{...}` in `data/datatype.go`, add the appropriate value to each entry:

| Entry key             | Value              |
|-----------------------|--------------------|
| `AssetKey`            | `DateColumn: ""`   |
| `ConsensusKey`        | `DateColumn: "event_date"` |
| `CustomKey`           | `DateColumn: "event_date"` |
| `EconomicIndicatorKey`| `DateColumn: "event_date"` |
| `EODKey`              | `DateColumn: "event_date"` |
| `EstimateKey`         | `DateColumn: "event_date"` |
| `FundamentalsKey`     | `DateColumn: "event_date"` (or whatever the `library.dateColumnForType` default would have returned -- confirm by reading the existing switch in `library/published_views.go:132-141`) |
| `IndexSnapshotKey`    | `DateColumn: "snapshot_date"` |
| `IndexChangelogKey`   | `DateColumn: "event_date"` |
| `MarketHolidaysKey`   | `DateColumn: "event_date"` |
| `MetricKey`           | `DateColumn: "event_date"` |
| `QuoteKey`            | `DateColumn: "event_date"` |
| `RatingKey`           | `DateColumn: "event_date"` |

Add the field beneath each entry's existing fields. Example for `IndexSnapshotKey`:

```go
IndexSnapshotKey: {
    Name:       IndexSnapshotKey,
    ViewName:   "indices_snapshot",
    DateColumn: "snapshot_date",
    Schema:     /* ... */,
    /* ... */
},
```

**Verify against the existing switch:** open `library/published_views.go` and read `dateColumnForType` (lines 132-141). For every key that lands in `default` it returns `"event_date"`; `AssetKey` returns `""`; `IndexSnapshotKey` returns `"snapshot_date"`. The table above must match.

Leave `DedupKeys` unset (nil) on every entry for now -- this task changes structure only, not behavior.

- [ ] **Step 3: Build and run tests**

Run: `go build ./... && ginkgo run -race ./data/ ./library/`
Expected: PASS (no behavior change; the new fields are unused).

- [ ] **Step 4: Commit**

```bash
git add data/datatype.go
git commit -m "refactor: declare DateColumn/DedupKeys on DataType"
```

---

### Task 3: Add `(*DataType).GenerateViewSQL` for plain (non-deduped) types -- TDD

**Files:**
- Create: `data/view_sql.go`
- Create: `data/view_sql_test.go`

- [ ] **Step 1: Write failing tests covering the plain-union behavior**

Create `data/view_sql_test.go`:

```go
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
package data_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/data"
)

var _ = Describe("DataType.GenerateViewSQL", func() {
	Describe("plain (non-deduped) types", func() {
		eod := &data.DataType{Name: "eod", ViewName: "eod", DateColumn: "event_date"}

		It("returns DROP VIEW for zero sources", func() {
			Expect(eod.GenerateViewSQL("eod", nil)).To(Equal("DROP VIEW IF EXISTS eod"))
		})

		It("emits a simple SELECT * for one source with no bounds", func() {
			sql := eod.GenerateViewSQL("eod", []data.ViewSource{
				{TableName: "eod_tiingo_abc12"},
			})
			Expect(sql).To(Equal(
				"CREATE OR REPLACE VIEW eod AS SELECT * FROM eod_tiingo_abc12",
			))
		})

		It("emits WHERE bounds when FromDate and UntilDate are set", func() {
			from := time.Date(2022, 6, 1, 0, 0, 0, 0, time.UTC)
			until := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
			sql := eod.GenerateViewSQL("eod", []data.ViewSource{
				{TableName: "eod_tiingo_abc12", FromDate: &from, UntilDate: &until},
			})
			Expect(sql).To(Equal(
				"CREATE OR REPLACE VIEW eod AS SELECT * FROM eod_tiingo_abc12 WHERE event_date >= '2022-06-01' AND event_date < '2023-06-01'",
			))
		})

		It("joins multiple legs with UNION ALL", func() {
			from := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			until := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			sql := eod.GenerateViewSQL("eod", []data.ViewSource{
				{TableName: "eod_tiingo_abc12", FromDate: &from},
				{TableName: "eod_legacy_def34", UntilDate: &until},
			})
			Expect(sql).To(ContainSubstring("UNION ALL"))
			Expect(sql).To(ContainSubstring("WHERE event_date >= '2023-01-01'"))
			Expect(sql).To(ContainSubstring("WHERE event_date < '2023-01-01'"))
		})

		It("ignores date bounds when DateColumn is empty", func() {
			asset := &data.DataType{Name: "a", ViewName: "assets", DateColumn: ""}
			from := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			sql := asset.GenerateViewSQL("assets", []data.ViewSource{
				{TableName: "ma_assets_abc12", FromDate: &from},
			})
			Expect(sql).NotTo(ContainSubstring("WHERE"))
			Expect(sql).To(Equal(
				"CREATE OR REPLACE VIEW assets AS SELECT * FROM ma_assets_abc12",
			))
		})

		It("uses the configured DateColumn name (snapshot_date)", func() {
			snap := &data.DataType{Name: "s", ViewName: "indices_snapshot", DateColumn: "snapshot_date"}
			from := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			sql := snap.GenerateViewSQL("indices_snapshot", []data.ViewSource{
				{TableName: "tradingview_idx_xyz", FromDate: &from},
			})
			Expect(sql).To(ContainSubstring("WHERE snapshot_date >= '2023-01-01'"))
			Expect(sql).NotTo(ContainSubstring("event_date"))
		})

		It("delegates leg SELECT/FROM to ViewGenerator when set", func() {
			rating := &data.DataType{
				Name:          "rating",
				ViewName:      "ratings",
				DateColumn:    "event_date",
				ViewGenerator: ratingTestVG{},
			}
			sql := rating.GenerateViewSQL("ratings", []data.ViewSource{
				{TableName: "rating_zacks_abc12"},
			})
			Expect(sql).To(Equal(
				"CREATE OR REPLACE VIEW ratings AS SELECT t.ticker, t.event_date, a.analyst FROM rating_zacks_abc12 t JOIN analyst_lookup a ON t.analyst_id = a.id",
			))
		})
	})
})

type ratingTestVG struct{}

func (ratingTestVG) SelectFrom(tableName string) string {
	return "SELECT t.ticker, t.event_date, a.analyst FROM " + tableName + " t JOIN analyst_lookup a ON t.analyst_id = a.id"
}
```

- [ ] **Step 2: Run tests to confirm they fail**

Run: `ginkgo run -race ./data/`
Expected: FAIL with a build error like `undefined: (*DataType).GenerateViewSQL`.

- [ ] **Step 3: Create `data/view_sql.go` with the plain-union implementation**

```go
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
	"strings"
)

// GenerateViewSQL produces the single SQL statement (CREATE OR REPLACE VIEW
// or DROP VIEW IF EXISTS) for a published view of this data type backed by
// the given ordered sources. Source order is meaningful only when
// DedupKeys is non-empty; for plain unions the order has no effect on the
// rows returned but is preserved verbatim in the leg sequence for
// readability.
func (dt *DataType) GenerateViewSQL(viewName string, sources []ViewSource) string {
	if len(sources) == 0 {
		return fmt.Sprintf("DROP VIEW IF EXISTS %s", viewName)
	}

	if len(dt.DedupKeys) > 0 {
		return dt.buildDedupedUnion(viewName, sources)
	}

	return dt.buildPlainUnion(viewName, sources)
}

func (dt *DataType) buildPlainUnion(viewName string, sources []ViewSource) string {
	legs := make([]string, len(sources))
	for i, s := range sources {
		legs[i] = dt.buildLeg(s)
	}

	return fmt.Sprintf("CREATE OR REPLACE VIEW %s AS %s", viewName, strings.Join(legs, " UNION ALL "))
}

// buildLeg renders a single SELECT/FROM[/WHERE] leg. The SELECT/FROM portion
// comes from ViewGenerator if set, otherwise defaults to "SELECT * FROM <t>".
// Date bounds are appended only when DateColumn is non-empty.
func (dt *DataType) buildLeg(s ViewSource) string {
	var sel string
	if dt.ViewGenerator != nil {
		sel = dt.ViewGenerator.SelectFrom(s.TableName)
	} else {
		sel = fmt.Sprintf("SELECT * FROM %s", s.TableName)
	}

	where := dt.buildWhereClause(s)
	if where == "" {
		return sel
	}

	return sel + " WHERE " + where
}

func (dt *DataType) buildWhereClause(s ViewSource) string {
	if dt.DateColumn == "" {
		return ""
	}

	var parts []string
	if s.FromDate != nil {
		parts = append(parts, fmt.Sprintf("%s >= '%s'", dt.DateColumn, s.FromDate.Format("2006-01-02")))
	}

	if s.UntilDate != nil {
		parts = append(parts, fmt.Sprintf("%s < '%s'", dt.DateColumn, s.UntilDate.Format("2006-01-02")))
	}

	return strings.Join(parts, " AND ")
}

// buildDedupedUnion is implemented in Task 4.
func (dt *DataType) buildDedupedUnion(viewName string, sources []ViewSource) string {
	// Stub: returns plain union until Task 4 fills it in.
	return dt.buildPlainUnion(viewName, sources)
}
```

- [ ] **Step 4: Run tests; expect PASS**

Run: `ginkgo run -race ./data/`
Expected: PASS for all the plain-union cases.

- [ ] **Step 5: Commit**

```bash
git add data/view_sql.go data/view_sql_test.go
git commit -m "feat(data): add DataType.GenerateViewSQL for plain unions"
```

---

### Task 4: Implement deduped-union SQL generation -- TDD

**Files:**
- Modify: `data/view_sql.go` (replace the `buildDedupedUnion` stub)
- Modify: `data/view_sql_test.go` (add cases)

- [ ] **Step 1: Append failing tests for the dedup form**

Add a new `Describe` block to `data/view_sql_test.go` (inside the top-level container):

```go
	Describe("deduped types (DedupKeys set)", func() {
		asset := &data.DataType{
			Name:      "a",
			ViewName:  "assets",
			DedupKeys: []string{"ticker", "composite_figi"},
		}

		It("emits a single leg for one source", func() {
			sql := asset.GenerateViewSQL("assets", []data.ViewSource{
				{TableName: "tiingo_assets_abc"},
			})
			Expect(sql).To(Equal(
				"CREATE OR REPLACE VIEW assets AS SELECT * FROM tiingo_assets_abc",
			))
		})

		It("anti-joins the second leg against the first on the dedup keys", func() {
			sql := asset.GenerateViewSQL("assets", []data.ViewSource{
				{TableName: "tiingo_assets_abc"},
				{TableName: "sharadar_assets_def"},
			})
			Expect(sql).To(Equal(
				"CREATE OR REPLACE VIEW assets AS " +
					"SELECT * FROM tiingo_assets_abc " +
					"UNION ALL " +
					"SELECT * FROM sharadar_assets_def " +
					"WHERE NOT EXISTS (" +
					"SELECT 1 FROM tiingo_assets_abc " +
					"WHERE tiingo_assets_abc.ticker = sharadar_assets_def.ticker " +
					"AND tiingo_assets_abc.composite_figi = sharadar_assets_def.composite_figi" +
					")",
			))
		})

		It("anti-joins each leg against every higher-priority leg", func() {
			sql := asset.GenerateViewSQL("assets", []data.ViewSource{
				{TableName: "s1"},
				{TableName: "s2"},
				{TableName: "s3"},
			})
			// First leg: no anti-join.
			Expect(sql).To(ContainSubstring("SELECT * FROM s1 UNION ALL"))
			// Second leg: one anti-join (against s1).
			Expect(sql).To(ContainSubstring(
				"SELECT * FROM s2 WHERE NOT EXISTS (SELECT 1 FROM s1 WHERE s1.ticker = s2.ticker AND s1.composite_figi = s2.composite_figi)",
			))
			// Third leg: two anti-joins (against s1 AND s2), combined with AND.
			Expect(sql).To(ContainSubstring(
				"SELECT * FROM s3 WHERE NOT EXISTS (SELECT 1 FROM s1 WHERE s1.ticker = s3.ticker AND s1.composite_figi = s3.composite_figi) AND NOT EXISTS (SELECT 1 FROM s2 WHERE s2.ticker = s3.ticker AND s2.composite_figi = s3.composite_figi)",
			))
		})

		It("ignores FromDate/UntilDate even if present (DateColumn empty)", func() {
			from := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			sql := asset.GenerateViewSQL("assets", []data.ViewSource{
				{TableName: "s1", FromDate: &from},
			})
			Expect(sql).NotTo(ContainSubstring("WHERE"))
		})

		It("DROP VIEW for zero sources still wins over dedup", func() {
			Expect(asset.GenerateViewSQL("assets", nil)).To(Equal("DROP VIEW IF EXISTS assets"))
		})
	})
```

- [ ] **Step 2: Run; expect FAIL**

Run: `ginkgo run -race ./data/ --focus "deduped types"`
Expected: FAIL because the stub still emits plain UNION ALL without anti-joins.

- [ ] **Step 3: Replace the `buildDedupedUnion` stub in `data/view_sql.go`**

Replace the stub function with:

```go
// buildDedupedUnion emits a CREATE OR REPLACE VIEW where each lower-priority
// leg excludes rows whose dedup-key tuple already appears in any
// higher-priority leg. The resulting view returns each unique dedup-key
// tuple exactly once, taking the row whole from the highest-priority source
// containing it.
//
// Date bounds (FromDate/UntilDate) are not applied in dedup mode -- there is
// no real-world data type today that uses both DedupKeys and a DateColumn,
// and the asset case explicitly has DateColumn == "". A non-nil
// ViewGenerator is also ignored: dedup mode emits plain "SELECT * FROM <t>"
// per leg so the NOT EXISTS clauses can reference columns by table name
// without alias bookkeeping. AssetKey has no ViewGenerator, so this
// limitation is moot for the only configured dedup type.
func (dt *DataType) buildDedupedUnion(viewName string, sources []ViewSource) string {
	legs := make([]string, len(sources))
	for i, s := range sources {
		legs[i] = dt.buildDedupLeg(s.TableName, sources[:i])
	}

	return fmt.Sprintf("CREATE OR REPLACE VIEW %s AS %s", viewName, strings.Join(legs, " UNION ALL "))
}

func (dt *DataType) buildDedupLeg(table string, higher []ViewSource) string {
	sel := fmt.Sprintf("SELECT * FROM %s", table)
	if len(higher) == 0 {
		return sel
	}

	clauses := make([]string, len(higher))
	for i, h := range higher {
		clauses[i] = fmt.Sprintf(
			"NOT EXISTS (SELECT 1 FROM %s WHERE %s)",
			h.TableName,
			joinKeyEquality(dt.DedupKeys, h.TableName, table),
		)
	}

	return sel + " WHERE " + strings.Join(clauses, " AND ")
}

// joinKeyEquality returns "<higher>.<k1> = <self>.<k1> AND ..." for each
// dedup key, used inside a NOT EXISTS subquery to identify rows already
// present in a higher-priority leg.
func joinKeyEquality(keys []string, higher, self string) string {
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s.%s = %s.%s", higher, k, self, k)
	}

	return strings.Join(parts, " AND ")
}
```

- [ ] **Step 4: Run; expect PASS**

Run: `ginkgo run -race ./data/`
Expected: PASS for every case (plain + deduped).

- [ ] **Step 5: Commit**

```bash
git add data/view_sql.go data/view_sql_test.go
git commit -m "feat(data): add priority dedup form for view SQL"
```

---

### Task 5: Delegate `library.PublishedView.GenerateViewSQL` to `data` and remove obsolete helpers

**Files:**
- Modify: `library/published_views.go` (replace body of `GenerateViewSQL`; delete `generateUnionSQL`, `buildLeg`, `buildWhereClause`, `dateColumnForType`)
- (No test file change yet -- existing tests should continue to pass.)

- [ ] **Step 1: Replace `(pv *PublishedView).GenerateViewSQL` body**

In `library/published_views.go`, replace lines 60-67 (the existing implementation) with:

```go
// GenerateViewSQL produces the SQL statements needed to create (or drop) the
// published view. The shape (plain UNION ALL vs priority-deduped form) is
// determined entirely by the data type's configuration on data.DataTypes.
func (pv *PublishedView) GenerateViewSQL() []string {
	dt, ok := data.DataTypes[pv.DataTypeKey]
	if !ok || dt == nil {
		return []string{fmt.Sprintf("DROP VIEW IF EXISTS %s", pv.ViewName)}
	}

	return []string{dt.GenerateViewSQL(pv.ViewName, pv.Sources)}
}
```

- [ ] **Step 2: Delete the now-unused helpers**

Remove these functions from `library/published_views.go` entirely:
- `generateUnionSQL` (lines ~73-84)
- `buildLeg` (lines ~89-103)
- `buildWhereClause` (lines ~110-125)
- `dateColumnForType` (lines ~132-141)

The `sort` and `strings` imports remain needed by `CheckOverlaps`. Check after deletion whether any imports become unused; remove only if so.

- [ ] **Step 3: Build and run all suites**

Run: `go build ./... && ginkgo run -race ./library/ ./data/ ./web/`
Expected: PASS. The `library/published_views_test.go` "GenerateViewSQL" cases that exercise non-deduped behavior continue to pass because the delegated SQL is byte-identical for those types. The asset case at lines 60-77 of that test file STILL passes -- because we have not yet flipped `DedupKeys` on `AssetKey`.

- [ ] **Step 4: Commit**

```bash
git add library/published_views.go
git commit -m "refactor: delegate published-view SQL generation to data.DataType"
```

---

### Task 6: Turn on dedup for `AssetKey` and update the library test asserting old behavior

**Files:**
- Modify: `data/datatype.go` (the `AssetKey` entry)
- Modify: `library/published_views_test.go:60-77` (the asset-specific case)

- [ ] **Step 1: Set `DedupKeys` on `AssetKey`**

In `data/datatype.go`, find the `AssetKey:` entry inside `DataTypes` and add the field:

```go
AssetKey: {
    Name:       AssetKey,
    ViewName:   "assets",
    DateColumn: "",
    DedupKeys:  []string{"ticker", "composite_figi"},
    Schema:     /* ... unchanged ... */,
    /* ... unchanged ... */
},
```

- [ ] **Step 2: Run the library test suite; observe the asset case fail**

Run: `ginkgo run -race ./library/ --focus "omits date WHERE bounds for asset-description"`
Expected: FAIL. The asserted SQL (`"SELECT * FROM massive_assets_abc12"` plain) no longer matches because asset views now go through dedup form. With one source the dedup form happens to produce identical SQL... so this test may actually pass! Let me verify by reading the dedup logic: with one source there is no anti-join, so the leg is just `SELECT * FROM massive_assets_abc12`. That equals the plain form for one source. So this assertion still passes.

Confirm: re-run the focused test. If it passes, no edit needed.

If you add a second source to that test (or there is one), it will fail. The current test only has one source, so behavior is unchanged.

- [ ] **Step 3: Add a new positive test for asset dedup with two sources**

In `library/published_views_test.go`, add (e.g. after the existing asset case around line 77):

```go
		It("emits the priority-dedup anti-join form for asset views with multiple sources", func() {
			pv := &library.PublishedView{
				ViewName:    "assets",
				DataTypeKey: "asset-description",
				Sources: []library.ViewSource{
					{TableName: "tiingo_assets_abc"},
					{TableName: "sharadar_assets_def"},
				},
			}
			sqls := pv.GenerateViewSQL()
			Expect(sqls).To(HaveLen(1))
			Expect(sqls[0]).To(ContainSubstring("UNION ALL"))
			Expect(sqls[0]).To(ContainSubstring(
				"SELECT * FROM sharadar_assets_def WHERE NOT EXISTS (SELECT 1 FROM tiingo_assets_abc WHERE tiingo_assets_abc.ticker = sharadar_assets_def.ticker AND tiingo_assets_abc.composite_figi = sharadar_assets_def.composite_figi)",
			))
		})
```

- [ ] **Step 4: Run all tests**

Run: `ginkgo run -race ./library/ ./data/ ./web/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add data/datatype.go library/published_views_test.go
git commit -m "feat(data): enable priority dedup on assets published view"
```

---

### Task 7: Short-circuit `CheckOverlaps` when the data type has no date axis

**Files:**
- Modify: `library/published_views.go:157-202` (`CheckOverlaps`)
- Modify: `library/published_views_test.go` (add a case)

- [ ] **Step 1: Write a failing test**

Append to the `Describe("CheckOverlaps", ...)` block in `library/published_views_test.go`:

```go
		It("returns empty for asset publications regardless of date fields", func() {
			from := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
			until := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			d3 := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			pv := &library.PublishedView{
				ViewName:    "assets",
				DataTypeKey: "asset-description",
				Sources: []library.ViewSource{
					{TableName: "t1", FromDate: &from, UntilDate: &until},
					{TableName: "t2", FromDate: &d3},
				},
			}
			Expect(pv.CheckOverlaps()).To(BeEmpty())
		})
```

- [ ] **Step 2: Run; expect FAIL**

Run: `ginkgo run -race ./library/ --focus "returns empty for asset publications"`
Expected: FAIL. The current `CheckOverlaps` ignores `DataTypeKey` and reports a fake overlap from the sentinel ranges.

- [ ] **Step 3: Add the short-circuit**

In `library/published_views.go`, at the top of `CheckOverlaps()` (currently around line 157), add:

```go
func (pv *PublishedView) CheckOverlaps() []string {
	// Data types with no date axis (e.g. assets) cannot have overlapping
	// date ranges by construction; FromDate/UntilDate on their sources are
	// silently ignored at SQL-gen time. Skip the check entirely so the UI
	// does not surface spurious overlap warnings.
	if dt, ok := data.DataTypes[pv.DataTypeKey]; ok && dt != nil && dt.DateColumn == "" {
		return nil
	}

	if len(pv.Sources) <= 1 {
		return nil
	}

	/* ... existing body unchanged ... */
}
```

- [ ] **Step 4: Run all tests**

Run: `ginkgo run -race ./library/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add library/published_views.go library/published_views_test.go
git commit -m "fix(library): suppress overlap warnings for date-less data types"
```

---

### Task 8: Add `(*Library).RebuildAllPublishedViews` and call it from `pvdata serve`

**Files:**
- Modify: `library/published_views.go` (add method)
- Modify: `cmd/serve.go:67-83` (insert call after migrations)

- [ ] **Step 1: Add `RebuildAllPublishedViews` as a `*Library` method**

In `library/published_views.go`, append at the end of the file:

```go
// RebuildAllPublishedViews loads every persisted published view and
// re-applies its CREATE OR REPLACE VIEW statement. Intended to run once at
// pvdata serve startup so that code-level changes to view-SQL generation
// (e.g. enabling dedup on assets) take effect on first boot without
// requiring users to touch each publication. CREATE OR REPLACE VIEW is
// idempotent, so safe to run on every startup.
//
// Errors on individual views are logged and accumulated; the function
// returns a non-nil error only if at least one view failed, but does not
// abort the loop early.
func (myLibrary *Library) RebuildAllPublishedViews(ctx context.Context) (rebuilt, total int, err error) {
	views, err := LoadPublishedViews(ctx, myLibrary.Pool)
	if err != nil {
		return 0, 0, fmt.Errorf("load published views: %w", err)
	}

	var failed int
	for _, pv := range views {
		if applyErr := ApplyPublishedView(ctx, myLibrary.Pool, pv); applyErr != nil {
			log.Error().Err(applyErr).Str("view", pv.ViewName).Msg("rebuild published view failed")

			failed++

			continue
		}

		rebuilt++
	}

	if failed > 0 {
		return rebuilt, len(views), fmt.Errorf("%d of %d published views failed to rebuild", failed, len(views))
	}

	return rebuilt, len(views), nil
}
```

If `LoadPublishedViews` is currently a free function (it is, per the existing file), it accepts a `Querier`. `myLibrary.Pool` is `*pgxpool.Pool`, which already satisfies `Querier`. (Verify by reading the existing `MigrateAllSubscriptions` use of `myLibrary.Pool.Acquire(...)` for a similar pattern.) If for any reason the typed pool does not satisfy `Querier`, wrap it: pass `myLibrary.Pool` directly is the expectation.

- [ ] **Step 2: Wire the call into `cmd/serve.go`**

Insert after the existing `MigrateAllSubscriptions` block (after line 83 in `cmd/serve.go`):

```go
		// Rebuild all published views at startup. CREATE OR REPLACE VIEW is
		// idempotent and lets code-level changes to view-SQL generation
		// (e.g. priority dedup on assets) take effect on first boot.
		log.Info().Msg("rebuilding published views")

		if rebuilt, total, err := myLibrary.RebuildAllPublishedViews(ctx); err != nil {
			log.Error().Err(err).Int("rebuilt", rebuilt).Int("total", total).Msg("some published views failed to rebuild")
		} else {
			log.Info().Int("rebuilt", rebuilt).Int("total", total).Msg("published views rebuilt")
		}
```

Note the choice to log-and-continue rather than `Fatal`. A view-rebuild failure should not block the server from starting; the operator can investigate from the log line.

- [ ] **Step 3: Build and run all tests**

Run: `go build ./... && ginkgo run -race ./...`
Expected: PASS.

- [ ] **Step 4: Manual verification**

1. Start `pvdata serve` against a database that has at least one published view (e.g. `eod`).
2. Confirm the log emits `rebuilding published views` followed by `published views rebuilt rebuilt=N total=N`.
3. In a SQL client, run `\d+ assets` (or equivalent) and verify the view definition reflects the new SQL.

- [ ] **Step 5: Commit**

```bash
git add library/published_views.go cmd/serve.go
git commit -m "feat(serve): rebuild published views at startup"
```

---

### Task 9: UI -- drag-handle row reorder for all publications

**Files:**
- Modify: `web/ui/src/pages/PublicationDetailPage.vue`

- [ ] **Step 1: Add the `rowReorder` column and handler**

In `web/ui/src/pages/PublicationDetailPage.vue`:

1. Add a function alongside the existing source-edit functions (after `confirmRemoveSource`, around line 197):

```typescript
async function onRowReorder(event: { value: any[] }) {
  const previous = publication.value.sources
  publication.value.sources = event.value
  saving.value = true

  const updatedSources = event.value.map((s: any) => {
    const source: any = {
      table_name: s.table_name,
      subscription_id: s.subscription_id,
    }
    if (s.from_date) source.from_date = new Date(s.from_date)
    if (s.until_date) source.until_date = new Date(s.until_date)
    return source
  })

  try {
    publication.value = await updatePublication(id.value, { sources: updatedSources })
  } catch (e: any) {
    error.value = e.message || 'Failed to reorder sources'
    publication.value.sources = previous
  } finally {
    saving.value = false
  }
}
```

2. Update the `<DataTable>` element (currently around line 242) to enable reordering and add the reorder column at the front. Replace:

```vue
<DataTable :value="publication.sources" :loading="saving">
  <Column field="table_name" header="Source Table" />
```

with:

```vue
<DataTable
  :value="publication.sources"
  :loading="saving"
  :reorderableRows="true"
  @row-reorder="onRowReorder"
>
  <Column rowReorder style="width: 3rem" />
  <Column field="table_name" header="Source Table" />
```

- [ ] **Step 2: Build the UI**

Run: `cd web/ui && npm run build` (or `make build-ui` from the repo root).
Expected: build succeeds, no TypeScript errors.

- [ ] **Step 3: Manual verification**

1. Start `pvdata serve`. Open a non-asset publication detail page (e.g. `eod`) in the browser.
2. Confirm a drag handle appears in the leftmost column of the sources table.
3. Drag the second source above the first; verify the order persists across a page reload.
4. Repeat with three sources to confirm multi-step reorders.

- [ ] **Step 4: Commit**

```bash
git add web/ui/src/pages/PublicationDetailPage.vue
git commit -m "feat(ui): drag-handle reorder for publication sources"
```

---

### Task 10: UI -- asset-only branch (hide From/Until columns and Edit Dates dialog; show Priority column)

**Files:**
- Modify: `web/ui/src/pages/PublicationDetailPage.vue`

- [ ] **Step 1: Add the `isAssetPublication` computed**

Below the existing `id = computed(...)` line in the `<script setup>` block of `web/ui/src/pages/PublicationDetailPage.vue`, add:

```typescript
const isAssetPublication = computed(() => publication.value?.data_type_key === 'asset-description')
```

- [ ] **Step 2: Add the Priority column (asset-only) and gate the From/Until columns**

Within the `<DataTable>` element, after the `rowReorder` column added in Task 9, insert:

```vue
<Column
  v-if="isAssetPublication"
  header="Priority"
  style="width: 6rem"
>
  <template #body="{ index }">{{ index + 1 }}</template>
</Column>
```

Then wrap the existing From and Until columns with `v-if="!isAssetPublication"`:

```vue
<Column v-if="!isAssetPublication" field="from_date" header="From">
  <template #body="{ data }">{{ data.from_date || '' }}</template>
</Column>
<Column v-if="!isAssetPublication" field="until_date" header="Until">
  <template #body="{ data }">{{ data.until_date || '' }}</template>
</Column>
```

- [ ] **Step 3: Hide the pencil "Edit Date Bounds" button on the asset page**

In the Actions column (around line 254), wrap the pencil button:

```vue
<Column header="Actions" style="width: 120px">
  <template #body="{ index }">
    <Button
      v-if="!isAssetPublication"
      icon="pi pi-pencil"
      text
      size="small"
      @click="openEditDates(index)"
    />
    <Button icon="pi pi-trash" text size="small" severity="danger" @click="openRemoveSource(index)" />
  </template>
</Column>
```

The Edit Dates `<Dialog>` element itself can stay in the template (it just never opens for asset pages), or wrap it with `v-if="!isAssetPublication"`. Wrap it for cleanliness:

```vue
<Dialog v-if="!isAssetPublication" v-model:visible="showEditDates" ...>
```

- [ ] **Step 4: Build the UI**

Run: `cd web/ui && npm run build`
Expected: build succeeds, no TypeScript errors.

- [ ] **Step 5: Manual verification**

1. Open a non-asset publication detail page (e.g. `eod`). Confirm From/Until columns and the pencil button are still visible. Confirm the Priority column is NOT visible.
2. Open an asset publication detail page (`assets`). Confirm:
   - From and Until columns are hidden.
   - A leading "Priority" column shows `1, 2, 3, ...` matching row order.
   - The pencil button is hidden in the Actions column; the trash button still works.
   - Drag-reorder the rows and confirm the Priority numbers update reactively.
3. Reload the page; confirm the new order persists.
4. Open the SQL Console (button at the bottom) and run `SELECT view_definition FROM information_schema.views WHERE table_name = 'assets';` -- the definition should reflect the new source order through the priority dedup form.

- [ ] **Step 6: Commit**

```bash
git add web/ui/src/pages/PublicationDetailPage.vue
git commit -m "feat(ui): asset-publication branch hides date affordances"
```

---

### Task 11: Final verification -- lint, full test pass, and dedup behavior smoke-test

**Files:** none (verification step)

- [ ] **Step 1: Run the linter and auto-fix**

Run: `golangci-lint run --fix`
Expected: no errors. If any remain, fix manually before continuing.

- [ ] **Step 2: Run the full Go test suite**

Run: `make test`
Expected: PASS across all packages.

- [ ] **Step 3: Build the full binary (Go + UI)**

Run: `make build`
Expected: clean build.

- [ ] **Step 4: Smoke-test the dedup against a real DB**

If a development database with multiple asset sources is available:
1. Start `pvdata serve`.
2. Open the `assets` publication; ensure two or more sources are configured.
3. Reorder them so a known overlapping `(ticker, composite_figi)` pair (e.g. AAPL with FIGI `BBG000B9XRY4`) is provided by both sources.
4. In the SQL Console run:
   ```sql
   SELECT ticker, composite_figi, COUNT(*) AS n
   FROM assets
   GROUP BY ticker, composite_figi
   HAVING COUNT(*) > 1;
   ```
   Expected: zero rows.
5. Drag the second source above the first; confirm the row's source-specific fields (e.g. `name`, `last_updated`) flip to match the now-higher-priority source on the next refresh.

If no such DB is available, document in the PR description that the SQL was verified by reading the generated view definition only.

- [ ] **Step 5: No commit needed for verification**

This task produces no code changes; it gates the merge.

---

## Self-Review

**Spec coverage:**
- "One row per (ticker, composite_figi)" -- Tasks 4, 6.
- "Source array order is the priority axis" -- Tasks 4 (anti-join order), 9 (reorder UI persists order via PUT).
- "View-SQL generation owned by data.DataType" -- Tasks 1-5.
- "UI reflects priority via row order; lets user reorder by drag" -- Task 9.
- "Asset publication detail UI hides date-range affordances" -- Task 10.
- Non-goal: per-column coalesce -- explicitly excluded by the dedup SQL form (whole-row anti-join).
- Non-goal: dedup for non-asset types -- only `AssetKey` gets `DedupKeys` set in Task 6.
- Non-goal: materialization -- still a `CREATE OR REPLACE VIEW`.
- Non-goal: new REST endpoints -- `PUT /publications/:id` is reused, no route or handler signature changes.
- Backward compat: stale `from_date`/`until_date` on asset sources -- preserved in JSON, ignored by SQL gen (Task 4 leg form drops them), hidden by UI (Task 10).
- Startup rebuild -- Task 8.
- Risk: `SELECT *` schema drift -- inherited from existing code; mentioned in spec; not introduced here.
- Tests in spec: data tests covering plain + dedup + bounds + ViewGenerator -- Tasks 3 and 4. `library` overlap test for `DateColumn==""` -- Task 7. Reorder persistence -- not added as automated test (would require a fake `Querier` or DB; manual verification in Task 9 step 3 covers it).

**Placeholder scan:** No "TBD" / "TODO" / "implement later" / "appropriate error handling" / "similar to Task N" / "write tests for the above" patterns appear above. Every code step contains the actual code.

**Type consistency:**
- `ViewSource` defined in Task 1 (`data.ViewSource`), used in Tasks 3, 4, 6.
- `DataType` field names `DateColumn` and `DedupKeys` introduced in Task 2, used in Tasks 3, 4, 6, 7.
- `(*DataType).GenerateViewSQL(viewName string, sources []ViewSource) string` defined in Task 3, called from Task 5.
- `(*Library).RebuildAllPublishedViews(ctx) (rebuilt, total int, err error)` defined in Task 8 step 1, called in Task 8 step 2.
- `isAssetPublication` defined in Task 10 step 1, used in Task 10 steps 2-3.
- `onRowReorder` defined in Task 9 step 1, referenced in Task 9 step 1's template snippet.
- PrimeVue `:reorderableRows` and `rowReorder` column attribute consistent across Tasks 9 and 10.
