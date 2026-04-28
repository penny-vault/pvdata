# Running Runs + B2-backed Asset Branding Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** (1) Make `run_history` reflect in-progress runs with a live observation count and recover from server crashes, (2) cap Massive's per-run icon/logo fetches so multi-day runs are bounded, (3) upload icon/logo binaries to a public Backblaze B2 bucket and persist their URLs on the asset row, and (4) trickle in coverage by running a missing-logo backfill lane on every Massive run.

**Architecture:** A subscription run inserts a `run_history` row with `status='running'` at start, updates `num_observations` on a 10-second throttle while running, and finalises the row when it completes. On server startup any rows still marked `running` are flipped to `failed` (the goroutine died with the previous process). The Massive provider caps icon/logo binary fetches per run so the bulk of the API budget goes to JSON details, and a missing-branding lane queries the asset table for rows with `icon_url IS NULL OR logo_url IS NULL` so subsequent runs converge on full coverage. Branding bytes are uploaded to `b2://penny-vault/assets/logos/` via the existing `kothar/go-backblaze` SDK; the public URL is stored in two new asset columns and rendered directly by the UI.

**Tech Stack:** Go (pgx/v5, golang-migrate, Ginkgo v2 + Gomega, zerolog, kothar/go-backblaze), Vue 3 + Carbon, Fiber.

---

## File Structure

| File | Responsibility |
|---|---|
| `db/migrations/000012_run_history_running_status.{up,down}.sql` | Allow `'running'` in the run_history status check constraint. |
| `data/datatype.go` | Add `RunRunning` to `StatusType`; add `icon_url` / `logo_url` to the `AssetKey` schema and a per-table migration. |
| `library/run_history.go` | Lifecycle methods (`BeginRun`, `UpdateRunProgress`, `FinalizeRun`, `MarkAbandonedRunsFailed`) and updated `StatusToString`. |
| `library/run_history_test.go` | Specs for `StatusToString`. |
| `library/filer.go` (new) | `FilerFromSpec(spec)` — recognises `file://` and `b2://`, returns a `data.Filer`. Lives in `library` so `data` doesn't import `backblaze`. |
| `library/filer_dispatch_test.go` (new) | Specs for the filer dispatch. |
| `library/database.go` | Use `FilerFromSpec` instead of `data.NewFilerFromString`. |
| `data/asset.go` | Add `IconUrl`/`LogoUrl` fields; refactor `SaveFiles` to capture the URL returned by `Filer.CreateFile`; persist URLs in `SaveDB` UPSERT (with `COALESCE(NULLIF(...),'')`); add columns to `ActiveAssets`/`AllAssets` projections. |
| `data/asset_test.go` (new) | Specs for `SaveFiles` URL capture. |
| `data/data_suite_test.go` (new — only if no existing suite) | Ginkgo test entry point for the `data` package. |
| `web/progress_throttle.go` (new) | Time-based throttle helper. |
| `web/progress_throttle_test.go` (new) | Specs for the throttle. |
| `web/run.go` | Replace single end-of-run insert with `BeginRun` → throttled `UpdateRunProgress` → `FinalizeRun`. |
| `cmd/serve.go` | Call `MarkAbandonedRunsFailed` at startup. |
| `backblaze/filer.go` (new) | `BackblazeFiler` implementing `data.Filer` — uploads via kothar SDK, returns the public URL. |
| `backblaze/filer_test.go` (new) | Specs for URL construction (no live B2 calls). |
| `backblaze/backblaze_suite_test.go` (new — only if no existing suite) | Ginkgo entry point. |
| `provider/massive/massive.go` | Per-run icon/logo binary cap; missing-branding lane in `filterAssetsByLastUpdated`. |
| `provider/massive/massive_test.go` (new) | Specs for the per-run cap. |
| `provider/massive/massive_suite_test.go` (new — only if no existing suite) | Ginkgo entry point. |
| `web/handlers_data.go` (existing — verify path during execution) | Surface `icon_url` / `logo_url` in the asset detail JSON response if not already covered by `SELECT *`. |
| `web/ui/src/pages/SubscriptionDetailPage.vue` | Render `running` status with a live record count from the run_history row; reconnect on SSE drop instead of immediately marking failed. |
| `web/ui/src/...` (asset detail component — locate during execution) | Render `<img :src="asset.icon_url" />` etc. when set. |

---

## Task 1: Schema migration — allow `'running'` in `run_history.status`

**Files:**
- Create: `db/migrations/000012_run_history_running_status.up.sql`
- Create: `db/migrations/000012_run_history_running_status.down.sql`

`run_history.end_time` stays `NOT NULL`; running rows store `end_time = start_time` as a placeholder and the UI computes a live duration from `now() - start_time` while `status = 'running'`.

- [ ] **Step 1: Write the up migration**

```sql
-- db/migrations/000012_run_history_running_status.up.sql
ALTER TABLE run_history DROP CONSTRAINT IF EXISTS run_history_status_check;
ALTER TABLE run_history
    ADD CONSTRAINT run_history_status_check
    CHECK (status IN ('running', 'success', 'failed'));
```

- [ ] **Step 2: Write the down migration**

```sql
-- db/migrations/000012_run_history_running_status.down.sql
UPDATE run_history SET status = 'failed' WHERE status = 'running';

ALTER TABLE run_history DROP CONSTRAINT IF EXISTS run_history_status_check;
ALTER TABLE run_history
    ADD CONSTRAINT run_history_status_check
    CHECK (status IN ('success', 'failed'));
```

- [ ] **Step 3: Lint**

Run: `make lint`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add db/migrations/000012_run_history_running_status.up.sql db/migrations/000012_run_history_running_status.down.sql
git commit -m "feat(db): allow 'running' status in run_history"
```

---

## Task 2: `RunRunning` + `StatusToString`

**Files:**
- Modify: `data/datatype.go:24-30`
- Modify: `library/run_history.go:27-34`
- Modify: `library/run_history_test.go`

- [ ] **Step 1: Write the failing test**

Append to `library/run_history_test.go` inside the existing `Describe("StatusToString", ...)`:

```go
It("converts RunRunning to \"running\"", func() {
    Expect(library.StatusToString(data.RunRunning)).To(Equal("running"))
})
```

- [ ] **Step 2: Run to verify it fails**

Run: `ginkgo run -race --focus "converts RunRunning" ./library/`
Expected: FAIL — `data.RunRunning` undefined.

- [ ] **Step 3: Add the constant**

Edit `data/datatype.go`:

```go
type StatusType int

const (
	StatusUnknown StatusType = iota
	RunFailed
	RunSuccess
	RunRunning
)
```

- [ ] **Step 4: Update `StatusToString`**

```go
func StatusToString(s data.StatusType) string {
	switch s {
	case data.RunSuccess:
		return "success"
	case data.RunRunning:
		return "running"
	default:
		return "failed"
	}
}
```

- [ ] **Step 5: Run all StatusToString tests**

Run: `ginkgo run -race --focus "StatusToString" ./library/`
Expected: 4 specs PASS.

- [ ] **Step 6: Commit**

```bash
git add data/datatype.go library/run_history.go library/run_history_test.go
git commit -m "feat(data): add RunRunning status"
```

---

## Task 3: Library lifecycle methods

**Files:**
- Modify: `library/run_history.go`

The existing `InsertRunHistory` and `SaveRunHistory` stay so `cmd/import.go:111` and `tui/runmanager.go:151` keep working — they insert a final row only.

- [ ] **Step 1: Add `BeginRun`**

Append below `InsertRunHistory`:

```go
// BeginRun inserts a run_history row with status='running' and
// num_observations=0 and returns its id. end_time is initialised
// to start_time as a placeholder; FinalizeRun overwrites it on
// completion.
func (myLibrary *Library) BeginRun(ctx context.Context, summary data.RunSummary) (string, error) {
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Release()

	var runID string

	err = conn.QueryRow(ctx,
		`INSERT INTO run_history (subscription_id, start_time, end_time, num_observations, status)
		VALUES ($1, $2, $2, 0, 'running')
		RETURNING id::text`,
		summary.SubscriptionID, summary.StartTime,
	).Scan(&runID)
	if err != nil {
		return "", err
	}

	log.Info().
		Str("SubscriptionID", summary.SubscriptionID.String()).
		Str("SubscriptionName", summary.SubscriptionName).
		Str("RunID", runID).
		Msg("started run")

	return runID, nil
}
```

- [ ] **Step 2: Add `UpdateRunProgress`**

```go
// UpdateRunProgress overwrites num_observations on a running row.
// No-op when runID is empty so callers don't have to branch when
// BeginRun failed.
func (myLibrary *Library) UpdateRunProgress(ctx context.Context, runID string, numObservations int) error {
	if runID == "" {
		return nil
	}

	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	_, err = conn.Exec(ctx,
		`UPDATE run_history SET num_observations = $1
		 WHERE id = $2 AND status = 'running'`,
		numObservations, runID,
	)

	return err
}
```

- [ ] **Step 3: Add `FinalizeRun`**

```go
// FinalizeRun updates a running run_history row to its terminal
// state. On success it also refreshes subscription stats. When
// runID is empty it falls back to InsertRunHistory so callers
// that didn't use BeginRun still record a row.
func (myLibrary *Library) FinalizeRun(ctx context.Context, runID string, summary data.RunSummary) error {
	if runID == "" {
		_, err := myLibrary.InsertRunHistory(ctx, summary)

		return err
	}

	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	_, err = conn.Exec(ctx,
		`UPDATE run_history
		 SET end_time = $1, num_observations = $2, status = $3
		 WHERE id = $4`,
		summary.EndTime, summary.NumObservations, StatusToString(summary.Status), runID,
	)
	if err != nil {
		return err
	}

	log.Info().
		Str("SubscriptionID", summary.SubscriptionID.String()).
		Str("SubscriptionName", summary.SubscriptionName).
		Int("NumObservations", summary.NumObservations).
		Str("Status", StatusToString(summary.Status)).
		Str("RunID", runID).
		Msg("finalised run")

	if summary.Status == data.RunSuccess {
		if err := myLibrary.updateSubscriptionStats(ctx, conn, summary); err != nil {
			log.Error().Err(err).Str("SubscriptionID", summary.SubscriptionID.String()).Msg("failed to update subscription stats")
		}
	}

	return nil
}
```

- [ ] **Step 4: Add `MarkAbandonedRunsFailed`**

```go
// MarkAbandonedRunsFailed transitions every run_history row still
// in status='running' to 'failed'. Any in-flight goroutines were
// lost with the previous process so these rows are permanently
// abandoned. Returns the number of rows updated.
func (myLibrary *Library) MarkAbandonedRunsFailed(ctx context.Context) (int64, error) {
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()

	tag, err := conn.Exec(ctx,
		`UPDATE run_history
		 SET status = 'failed', end_time = now()
		 WHERE status = 'running'`,
	)
	if err != nil {
		return 0, err
	}

	return tag.RowsAffected(), nil
}
```

- [ ] **Step 5: Build**

Run: `go build ./library/...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add library/run_history.go
git commit -m "feat(library): BeginRun/UpdateRunProgress/FinalizeRun/MarkAbandonedRunsFailed"
```

---

## Task 4: Progress-throttle helper

**Files:**
- Create: `web/progress_throttle.go`
- Create: `web/progress_throttle_test.go`

- [ ] **Step 1: Write the failing test**

```go
// web/progress_throttle_test.go
// SPDX-License-Identifier: Apache-2.0
package web_test

import (
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/web"
)

var _ = Describe("ProgressThrottle", func() {
	It("calls the emit function on the first tick", func() {
		var calls atomic.Int32

		now := time.Unix(0, 0)

		t := web.NewProgressThrottle(time.Second, func(int) { calls.Add(1) })
		t.Tick(now, 5)

		Expect(calls.Load()).To(Equal(int32(1)))
	})

	It("suppresses ticks within the interval", func() {
		var calls atomic.Int32

		now := time.Unix(0, 0)

		t := web.NewProgressThrottle(10*time.Second, func(int) { calls.Add(1) })
		t.Tick(now, 5)
		t.Tick(now.Add(2*time.Second), 7)
		t.Tick(now.Add(5*time.Second), 9)

		Expect(calls.Load()).To(Equal(int32(1)))
	})

	It("emits again once the interval has elapsed", func() {
		var calls atomic.Int32

		now := time.Unix(0, 0)

		t := web.NewProgressThrottle(10*time.Second, func(int) { calls.Add(1) })
		t.Tick(now, 5)
		t.Tick(now.Add(11*time.Second), 9)

		Expect(calls.Load()).To(Equal(int32(2)))
	})

	It("flushes the latest count even when throttled", func() {
		var last int32

		now := time.Unix(0, 0)

		t := web.NewProgressThrottle(10*time.Second, func(n int) { atomic.StoreInt32(&last, int32(n)) })
		t.Tick(now, 5)
		t.Tick(now.Add(1*time.Second), 7)
		t.Flush()

		Expect(atomic.LoadInt32(&last)).To(Equal(int32(7)))
	})
})
```

- [ ] **Step 2: Run to verify it fails**

Run: `ginkgo run -race --focus "ProgressThrottle" ./web/`
Expected: FAIL — `web.NewProgressThrottle` undefined.

- [ ] **Step 3: Implement the throttle**

```go
// web/progress_throttle.go
// SPDX-License-Identifier: Apache-2.0
package web

import "time"

// ProgressThrottle invokes emit at most once per interval. The
// first Tick fires immediately; later Ticks within the interval
// are dropped but the most recent count is remembered so Flush
// can emit it later. Not safe for concurrent Ticks.
type ProgressThrottle struct {
	interval time.Duration
	emit     func(int)
	lastSent time.Time
	pending  int
	hasFirst bool
	dirty    bool
}

func NewProgressThrottle(interval time.Duration, emit func(int)) *ProgressThrottle {
	return &ProgressThrottle{interval: interval, emit: emit}
}

func (p *ProgressThrottle) Tick(now time.Time, count int) {
	p.pending = count
	p.dirty = true

	if !p.hasFirst || now.Sub(p.lastSent) >= p.interval {
		p.emit(count)
		p.lastSent = now
		p.hasFirst = true
		p.dirty = false
	}
}

func (p *ProgressThrottle) Flush() {
	if !p.dirty {
		return
	}

	p.emit(p.pending)
	p.dirty = false
}
```

- [ ] **Step 4: Run tests**

Run: `ginkgo run -race --focus "ProgressThrottle" ./web/`
Expected: 4 specs PASS.

- [ ] **Step 5: Commit**

```bash
git add web/progress_throttle.go web/progress_throttle_test.go
git commit -m "feat(web): add ProgressThrottle helper"
```

---

## Task 5: Asset schema — add `icon_url` and `logo_url`

**Files:**
- Modify: `data/datatype.go` `AssetKey` block

Asset tables are created per-subscription, so we add columns to the template schema and a `Migrations` entry that runs against existing tables when `RunMigrations` fires.

- [ ] **Step 1: Add columns to the AssetKey schema literal**

Insert after `last_updated timestamp,` in `data/datatype.go`:

```sql
last_updated timestamp,
icon_url TEXT,
logo_url TEXT,
PRIMARY KEY (ticker, composite_figi)
```

- [ ] **Step 2: Add a per-table migration**

In the same `AssetKey` block, set `Version: 1` and append:

```go
Migrations: []string{
    `ALTER TABLE %[1]s ADD COLUMN IF NOT EXISTS icon_url TEXT;
     ALTER TABLE %[1]s ADD COLUMN IF NOT EXISTS logo_url TEXT;`,
},
Version: 1,
```

- [ ] **Step 3: Lint**

Run: `make lint`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add data/datatype.go
git commit -m "feat(data): add icon_url/logo_url to asset schema"
```

---

## Task 6: `Asset.IconUrl` / `LogoUrl` fields

**Files:**
- Modify: `data/asset.go:58-85`

- [ ] **Step 1: Add the fields**

Insert after `LogoMimeType` (`data/asset.go:78`):

```go
	IconUrl              string    `json:"icon_url" db:"icon_url"`
	LogoUrl              string    `json:"logo_url" db:"logo_url"`
```

- [ ] **Step 2: Build**

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add data/asset.go
git commit -m "feat(data): add IconUrl/LogoUrl fields to Asset"
```

---

## Task 7: Capture URLs in `SaveFiles`

**Files:**
- Modify: `data/asset.go:186-228`
- Create: `data/asset_test.go`
- Create (only if absent): `data/data_suite_test.go`

- [ ] **Step 1: If missing, create the Ginkgo suite file**

```go
// data/data_suite_test.go
// SPDX-License-Identifier: Apache-2.0
package data_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestData(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Data Suite")
}
```

- [ ] **Step 2: Write the failing test**

```go
// data/asset_test.go
// SPDX-License-Identifier: Apache-2.0
package data_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/data"
)

type recordingFiler struct {
	urls map[string]string
}

func (r *recordingFiler) CreateFile(name string, _ []byte) (string, error) {
	url := "https://example.test/" + name
	if r.urls == nil {
		r.urls = map[string]string{}
	}
	r.urls[name] = url

	return url, nil
}

var _ = Describe("Asset.SaveFiles", func() {
	It("populates IconUrl with the returned URL", func() {
		asset := &data.Asset{
			CompositeFigi: "BBG000FOO",
			Icon:          []byte{1, 2, 3},
			IconMimeType:  "image/png",
		}

		filer := &recordingFiler{}
		Expect(asset.SaveFiles(context.Background(), filer)).To(Succeed())
		Expect(asset.IconUrl).To(Equal("https://example.test/BBG000FOO-icon.png"))
	})

	It("populates LogoUrl with the returned URL", func() {
		asset := &data.Asset{
			CompositeFigi: "BBG000FOO",
			Logo:          []byte{4, 5, 6},
			LogoMimeType:  "image/jpeg",
		}

		filer := &recordingFiler{}
		Expect(asset.SaveFiles(context.Background(), filer)).To(Succeed())
		Expect(asset.LogoUrl).To(Equal("https://example.test/BBG000FOO-logo.jpg"))
	})

	It("leaves URL unchanged when there is no payload", func() {
		asset := &data.Asset{
			CompositeFigi: "BBG000FOO",
			IconUrl:       "previous-url",
		}

		filer := &recordingFiler{}
		Expect(asset.SaveFiles(context.Background(), filer)).To(Succeed())
		Expect(asset.IconUrl).To(Equal("previous-url"))
	})
})
```

- [ ] **Step 3: Run to verify it fails**

Run: `ginkgo run -race --focus "Asset.SaveFiles" ./data/`
Expected: FAIL.

- [ ] **Step 4: Refactor `SaveFiles`**

Replace `data/asset.go:186-228`:

```go
func (asset *Asset) SaveFiles(ctx context.Context, filer Filer) error {
	if url, err := saveAssetFile(filer, asset.CompositeFigi+"-icon", asset.IconMimeType, asset.Icon); err != nil {
		log.Error().Err(err).Str("Name", asset.CompositeFigi+"-icon").Msg("error saving icon")
	} else if url != "" {
		asset.IconUrl = url
	}

	if url, err := saveAssetFile(filer, asset.CompositeFigi+"-logo", asset.LogoMimeType, asset.Logo); err != nil {
		log.Error().Err(err).Str("Name", asset.CompositeFigi+"-logo").Msg("error saving logo")
	} else if url != "" {
		asset.LogoUrl = url
	}

	return nil
}

// saveAssetFile dispatches by mime type and writes the bytes via
// filer. Returns the URL/path Filer.CreateFile reports, or "" when
// there is nothing to save (empty mime type or zero-byte payload).
func saveAssetFile(filer Filer, baseName, mimeType string, data []byte) (string, error) {
	if len(data) == 0 || mimeType == "" {
		return "", nil
	}

	var ext string

	switch mimeType {
	case "image/jpeg":
		ext = ".jpg"
	case "image/png":
		ext = ".png"
	case "image/svg+xml", "image/svg":
		ext = ".svg"
	default:
		return "", errors.New("unknown mimetype: " + mimeType)
	}

	return filer.CreateFile(baseName+ext, data)
}
```

- [ ] **Step 5: Run tests**

Run: `ginkgo run -race --focus "Asset.SaveFiles" ./data/`
Expected: 3 specs PASS.

- [ ] **Step 6: Commit**

```bash
git add data/asset.go data/asset_test.go data/data_suite_test.go
git commit -m "feat(data): capture URL from Filer.CreateFile in SaveFiles"
```

---

## Task 8: Persist `icon_url`/`logo_url` in `Asset.SaveDB`

**Files:**
- Modify: `data/asset.go:230-314`, `95-118`, `143-165`

- [ ] **Step 1: Update INSERT and ON CONFLICT clause**

Replace the SQL block in `SaveDB` (lines 259-301):

```go
	sql := fmt.Sprintf(`INSERT INTO %[1]s (
		"ticker",
		"composite_figi",
		"share_class_figi",
		"primary_exchange",
		"asset_type",
		"active",
		"name",
		"description",
		"corporate_url",
		"sector",
		"industry",
		"sic_code",
		"cik",
		"cusips",
		"isins",
		"other_identifiers",
		"similar_tickers",
		"tags",
		"listed",
		"delisted",
		"last_updated",
		"icon_url",
		"logo_url"
	) VALUES (
		$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12,
		$13, $14, $15, $16, $17, $18, $19, $20, $21, $22, $23
	) ON CONFLICT ON CONSTRAINT %[1]s_pkey DO UPDATE SET
		primary_exchange = EXCLUDED.primary_exchange,
		active = EXCLUDED.active,
		name = EXCLUDED.name,
		description = EXCLUDED.description,
		corporate_url = EXCLUDED.corporate_url,
		sector = EXCLUDED.sector,
		industry = EXCLUDED.industry,
		sic_code = EXCLUDED.sic_code,
		cik = EXCLUDED.cik,
		cusips = EXCLUDED.cusips,
		isins = EXCLUDED.isins,
		other_identifiers = EXCLUDED.other_identifiers,
		similar_tickers = EXCLUDED.similar_tickers,
		tags = EXCLUDED.tags,
		listed = EXCLUDED.listed,
		delisted = EXCLUDED.delisted,
		last_updated = EXCLUDED.last_updated,
		icon_url = COALESCE(EXCLUDED.icon_url, %[1]s.icon_url),
		logo_url = COALESCE(EXCLUDED.logo_url, %[1]s.logo_url)`, tbl)
```

`COALESCE(EXCLUDED.x, table.x)` — if this run brought a non-NULL URL, use it; otherwise keep the previously-stored one. Empty-string is treated as NULL by the binding helper below so we don't blow away existing URLs.

- [ ] **Step 2: Update the bind values**

Replace the `tx.Exec` call (lines 303-307):

```go
	_, err = tx.Exec(ctx, sql, asset.Ticker, asset.CompositeFigi, asset.ShareClassFigi,
		asset.PrimaryExchange, asset.AssetType, asset.Active, asset.Name, asset.Description,
		asset.CorporateUrl, asset.Sector, asset.Industry, asset.SIC, asset.CIK,
		asset.CUSIP, asset.ISIN, asset.OtherIdentifiers, asset.SimilarTickers, asset.Tags,
		listingDate, delistingDate, asset.LastUpdated,
		brandingBind(asset.IconUrl), brandingBind(asset.LogoUrl))
```

Add the helper at the bottom of `data/asset.go`:

```go
// brandingBind converts an empty IconUrl/LogoUrl to a SQL NULL so
// the missing-branding lane (which queries WHERE icon_url IS NULL)
// can find assets that haven't been uploaded yet. Non-empty values
// are passed through unchanged.
func brandingBind(url string) any {
	if url == "" {
		return nil
	}

	return url
}
```

- [ ] **Step 3: Add columns to `ActiveAssets` and `AllAssets`**

In `ActiveAssets` (`data/asset.go:95-118`), append to the column list:

```sql
		last_updated,
		coalesce(icon_url, '') as icon_url,
		coalesce(logo_url, '') as logo_url
```

Mirror in `AllAssets` (`data/asset.go:143-165`).

- [ ] **Step 4: Build**

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add data/asset.go
git commit -m "feat(data): persist icon_url/logo_url in asset UPSERT"
```

---

## Task 9: Backblaze filer

**Files:**
- Create: `backblaze/filer.go`
- Create: `backblaze/filer_test.go`
- Create (only if absent): `backblaze/backblaze_suite_test.go`

- [ ] **Step 1: If missing, create the Ginkgo suite file**

```go
// backblaze/backblaze_suite_test.go
// SPDX-License-Identifier: Apache-2.0
package backblaze_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestBackblaze(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Backblaze Suite")
}
```

- [ ] **Step 2: Write the failing test**

```go
// backblaze/filer_test.go
// SPDX-License-Identifier: Apache-2.0
package backblaze_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/backblaze"
)

var _ = Describe("Filer URL", func() {
	It("constructs the public file URL from bucket, prefix, and name", func() {
		f := backblaze.NewFilerForTest(
			"penny-vault",
			"assets/logos",
			"https://f002.backblazeb2.com",
		)

		Expect(f.PublicURL("BBG000FOO-icon.png")).
			To(Equal("https://f002.backblazeb2.com/file/penny-vault/assets/logos/BBG000FOO-icon.png"))
	})

	It("omits the prefix segment when prefix is empty", func() {
		f := backblaze.NewFilerForTest(
			"penny-vault",
			"",
			"https://f002.backblazeb2.com",
		)

		Expect(f.PublicURL("BBG000FOO-icon.png")).
			To(Equal("https://f002.backblazeb2.com/file/penny-vault/BBG000FOO-icon.png"))
	})
})
```

- [ ] **Step 3: Run to verify it fails**

Run: `ginkgo run -race ./backblaze/`
Expected: FAIL.

- [ ] **Step 4: Implement `BackblazeFiler`**

```go
// backblaze/filer.go
// SPDX-License-Identifier: Apache-2.0
package backblaze

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"path"
	"sync"

	"github.com/kothar/go-backblaze"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

// Filer uploads files to a Backblaze B2 bucket and returns the
// public URL. The bucket must be public-read for the URLs to
// work without further authentication.
type Filer struct {
	bucketName  string
	prefix      string
	downloadURL string

	mu     sync.Mutex
	bucket *backblaze.Bucket
	client *backblaze.B2
}

// NewFiler returns a Filer for the given bucket and key prefix.
// Credentials are read from viper lazily on first upload.
func NewFiler(bucketName, prefix string) *Filer {
	prefix = path.Clean(prefix)
	if prefix == "." || prefix == "/" {
		prefix = ""
	}

	return &Filer{bucketName: bucketName, prefix: prefix}
}

// NewFilerForTest skips the lazy authorise step so unit tests can
// exercise URL construction without contacting B2.
func NewFilerForTest(bucketName, prefix, downloadURL string) *Filer {
	prefix = path.Clean(prefix)
	if prefix == "." || prefix == "/" {
		prefix = ""
	}

	return &Filer{bucketName: bucketName, prefix: prefix, downloadURL: downloadURL}
}

// PublicURL returns the public-read URL the configured bucket
// serves the named file from.
func (f *Filer) PublicURL(name string) string {
	parts := []string{"file", f.bucketName}
	if f.prefix != "" {
		parts = append(parts, f.prefix)
	}

	parts = append(parts, name)

	joined, _ := url.JoinPath(f.downloadURL, parts...)

	return joined
}

// CreateFile uploads data to <bucket>/<prefix>/<name> and returns
// the public URL.
func (f *Filer) CreateFile(name string, data []byte) (string, error) {
	if err := f.ensureAuthorised(); err != nil {
		return "", err
	}

	key := name
	if f.prefix != "" {
		key = fmt.Sprintf("%s/%s", f.prefix, name)
	}

	if _, err := f.bucket.UploadFile(key, nil, bytes.NewReader(data)); err != nil {
		log.Error().Err(err).Str("Bucket", f.bucketName).Str("Key", key).Msg("backblaze upload failed")

		return "", err
	}

	return f.PublicURL(name), nil
}

func (f *Filer) ensureAuthorised() error {
	f.mu.Lock()
	defer f.mu.Unlock()

	if f.bucket != nil {
		return nil
	}

	keyID := viper.GetString("backblaze.application_id")
	appKey := viper.GetString("backblaze.application_key")

	if keyID == "" || appKey == "" {
		return errors.New("backblaze: application_id / application_key not configured")
	}

	client, err := backblaze.NewB2(backblaze.Credentials{KeyID: keyID, ApplicationKey: appKey})
	if err != nil {
		return fmt.Errorf("backblaze authorise: %w", err)
	}

	bucket, err := client.Bucket(f.bucketName)
	if err != nil {
		return fmt.Errorf("backblaze bucket %q lookup: %w", f.bucketName, err)
	}

	if bucket == nil {
		return fmt.Errorf("backblaze bucket %q not found", f.bucketName)
	}

	f.client = client
	f.bucket = bucket
	f.downloadURL = client.DownloadURL

	return nil
}
```

(`client.DownloadURL` is exposed by the kothar package — verify with `go doc github.com/kothar/go-backblaze.B2`. If absent in the pinned version, fall back to a viper override `backblaze.download_url`.)

- [ ] **Step 5: Run tests**

Run: `ginkgo run -race ./backblaze/`
Expected: 2 specs PASS.

- [ ] **Step 6: Commit**

```bash
git add backblaze/filer.go backblaze/filer_test.go backblaze/backblaze_suite_test.go
git commit -m "feat(backblaze): add Filer that uploads to public B2 bucket"
```

---

## Task 10: Filer dispatch in `library`

**Files:**
- Create: `library/filer.go`
- Create: `library/filer_dispatch_test.go`
- Modify: `library/database.go:191-193`

We dispatch from `library/` (not `data/`) because `data` cannot import `backblaze` (it would cycle: `backblaze` already implements `data.Filer`).

- [ ] **Step 1: Write the failing test**

```go
// library/filer_dispatch_test.go
// SPDX-License-Identifier: Apache-2.0
package library_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/library"
)

var _ = Describe("FilerFromSpec", func() {
	It("returns an FSFiler for file:// specs", func() {
		Expect(library.FilerFromSpec("file:///tmp/icons")).NotTo(BeNil())
	})

	It("returns a BackblazeFiler for b2:// specs", func() {
		Expect(library.FilerFromSpec("b2://penny-vault/assets/logos")).NotTo(BeNil())
	})

	It("returns nil for unknown schemes", func() {
		Expect(library.FilerFromSpec("s3://nope")).To(BeNil())
	})
})
```

- [ ] **Step 2: Run to verify it fails**

Run: `ginkgo run -race --focus "FilerFromSpec" ./library/`
Expected: FAIL.

- [ ] **Step 3: Implement**

```go
// library/filer.go
// SPDX-License-Identifier: Apache-2.0
package library

import (
	"strings"

	"github.com/penny-vault/pvdata/backblaze"
	"github.com/penny-vault/pvdata/data"
)

// FilerFromSpec resolves a per-subscription filer spec to a
// data.Filer. Supported schemes:
//
//	file:///abs/path          → write to local filesystem
//	b2://<bucket>/<prefix>    → upload to Backblaze B2 (public)
//
// Returns nil for an unrecognised scheme; callers treat that as
// "no filer configured".
func FilerFromSpec(spec string) data.Filer {
	switch {
	case strings.HasPrefix(spec, "file://"):
		return data.NewFilerFromString(spec)
	case strings.HasPrefix(spec, "b2://"):
		rest := strings.TrimPrefix(spec, "b2://")
		parts := strings.SplitN(rest, "/", 2)

		bucket := parts[0]
		prefix := ""

		if len(parts) == 2 {
			prefix = parts[1]
		}

		return backblaze.NewFiler(bucket, prefix)
	}

	return nil
}
```

- [ ] **Step 4: Switch `database.go` to use `FilerFromSpec`**

In `library/database.go:191-193`:

```go
		var filer data.Filer
		if filerPath, ok := subscription.Config["filer"]; ok {
			filer = FilerFromSpec(filerPath)
		}
```

- [ ] **Step 5: Run tests**

Run: `ginkgo run -race --focus "FilerFromSpec" ./library/`
Expected: 3 specs PASS.

- [ ] **Step 6: Commit**

```bash
git add library/filer.go library/filer_dispatch_test.go library/database.go
git commit -m "feat(library): dispatch filer spec to FS or Backblaze"
```

---

## Task 11: Wire run lifecycle into `RunSubscription`

**Files:**
- Modify: `web/run.go:62-215`

- [ ] **Step 1: Add the throttle interval constant**

Below the `RunSourceManual` block (`web/run.go:36-39`):

```go
// runProgressInterval bounds how often we UPDATE num_observations
// on a running run_history row. FRED-style providers can emit
// hundreds of thousands of records per minute; this caps the
// write rate regardless of throughput.
const runProgressInterval = 10 * time.Second
```

- [ ] **Step 2: Move the BeginRun call before the fetch**

Replace `web/run.go:88-119` with:

```go
	subProvider, ok := provider.Map[sub.Provider]
	if !ok {
		logger.Error().Str("provider", sub.Provider).Msg("provider not found")
		emitFinal(opts.Run, sub, 0, false, "provider not found")
		pingHealthcheck(sub, healthcheck.PingFail, fmt.Sprintf("provider not found: %s", sub.Provider))

		return
	}

	subDataset, ok := subProvider.Datasets()[sub.Dataset]
	if !ok {
		logger.Error().Str("dataset", sub.Dataset).Msg("dataset not found")
		emitFinal(opts.Run, sub, 0, false, "dataset not found")
		pingHealthcheck(sub, healthcheck.PingFail, fmt.Sprintf("dataset not found: %s", sub.Dataset))

		return
	}

	runID, beginErr := lib.BeginRun(ctx, data.RunSummary{
		StartTime:        time.Now(),
		SubscriptionID:   sub.ID,
		SubscriptionName: sub.Name,
	})
	if beginErr != nil {
		logger.Error().Err(beginErr).Msg("could not insert running run_history row")
	}

	progress := NewProgressThrottle(runProgressInterval, func(n int) {
		if err := lib.UpdateRunProgress(ctx, runID, n); err != nil {
			logger.Warn().Err(err).Msg("could not update run progress")
		}
	})

	observeChan := make(chan *data.Observation, 1000)
	saveChan := make(chan *data.Observation, 1000)
	exitChan := make(chan data.RunSummary, 1)

	var wg sync.WaitGroup

	wg.Add(1)

	go lib.SaveObservations(saveChan, &wg, checks.NewInlineValidator(checks.InlineChecks()))

	emitStarted(opts.Run, sub)

	// Observation interceptor: summarise each record, push throttled
	// progress to the DB, and forward to saveChan.
	go func() {
		count := 0

		for obs := range observeChan {
			count++

			typ, summary := summarizeObservation(obs)

			if opts.Run != nil {
				d, _ := json.Marshal(map[string]interface{}{
					"count":   count,
					"type":    typ,
					"summary": summary,
				})
				opts.Run.publish(sseEvent{Event: "record", Data: string(d)})
			}

			progress.Tick(time.Now(), count)

			saveChan <- obs
		}

		close(saveChan)
	}()
```

- [ ] **Step 3: Replace the end-of-run insert**

Find `web/run.go:156-161`:

```go
	runID, insertErr := lib.InsertRunHistory(ctx, summary)
	if insertErr != nil {
		logger.Error().Err(insertErr).Msg("failed to save run history")
	}
```

Replace with:

```go
	progress.Flush()

	if err := lib.FinalizeRun(ctx, runID, summary); err != nil {
		logger.Error().Err(err).Msg("failed to finalise run history")
	}
```

(`runID` is already in scope from BeginRun.)

- [ ] **Step 4: Run tests**

Run: `ginkgo run -race ./web/`
Expected: existing specs PASS.

- [ ] **Step 5: Build**

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 6: Commit**

```bash
git add web/run.go
git commit -m "feat(web): persist run lifecycle and live progress to run_history"
```

---

## Task 12: Mark abandoned runs at server startup

**Files:**
- Modify: `cmd/serve.go:62-99`

- [ ] **Step 1: Insert the cleanup call**

Immediately after `defer myLibrary.Close()` (currently `cmd/serve.go:61`):

```go
		if cleared, err := myLibrary.MarkAbandonedRunsFailed(ctx); err != nil {
			log.Warn().Err(err).Msg("could not clean up abandoned runs")
		} else if cleared > 0 {
			log.Info().Int64("count", cleared).Msg("marked abandoned runs as failed")
		}
```

- [ ] **Step 2: Build**

Run: `go build ./cmd/...`
Expected: PASS.

- [ ] **Step 3: Commit**

```bash
git add cmd/serve.go
git commit -m "feat(serve): mark abandoned 'running' runs as failed at startup"
```

---

## Task 13: Cap icon and logo binary fetches per Massive run

**Files:**
- Modify: `provider/massive/massive.go` (struct, `downloadMassiveAssets`, `assetDetail`)
- Create: `provider/massive/massive_test.go`
- Create (only if absent): `provider/massive/massive_suite_test.go`

`maxIconLogoFetchesPerRun = 100` (a single asset consumes up to 2 — one for icon, one for logo). With a 5/min plan and 100 cap, that's at most ~24 minutes of API budget per run on branding.

- [ ] **Step 1: If missing, create the Ginkgo suite file**

```go
// provider/massive/massive_suite_test.go
// SPDX-License-Identifier: Apache-2.0
package massive_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestMassive(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Massive Suite")
}
```

- [ ] **Step 2: Write the failing test**

```go
// provider/massive/massive_test.go
// SPDX-License-Identifier: Apache-2.0
package massive_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/provider/massive"
)

var _ = Describe("Branding budget", func() {
	It("permits fetches up to the cap", func() {
		c := massive.NewBrandingBudget(2)
		Expect(c.Allow()).To(BeTrue())
		Expect(c.Allow()).To(BeTrue())
	})

	It("denies further fetches once the cap is reached", func() {
		c := massive.NewBrandingBudget(2)
		c.Allow()
		c.Allow()
		Expect(c.Allow()).To(BeFalse())
	})

	It("treats a non-positive cap as unlimited", func() {
		c := massive.NewBrandingBudget(0)

		for i := 0; i < 10_000; i++ {
			Expect(c.Allow()).To(BeTrue())
		}
	})
})
```

- [ ] **Step 3: Run to verify it fails**

Run: `ginkgo run -race ./provider/massive/`
Expected: FAIL.

- [ ] **Step 4: Add budget type and wire into the fetcher**

Append to `provider/massive/massive.go`:

```go
const maxIconLogoFetchesPerRun = 100

// brandingBudget caps how many icon/logo HTTP fetches a single
// run performs. A non-positive cap means unlimited.
type brandingBudget struct {
	limit     int
	remaining int
}

func NewBrandingBudget(limit int) *brandingBudget {
	return &brandingBudget{limit: limit, remaining: limit}
}

func (b *brandingBudget) Allow() bool {
	if b == nil || b.limit <= 0 {
		return true
	}

	if b.remaining <= 0 {
		return false
	}

	b.remaining--

	return true
}
```

Add a field to `massiveAssetFetcher` (current declaration `provider/massive/massive.go:108-114`):

```go
type massiveAssetFetcher struct {
	subscription *library.Subscription
	client       *resty.Client
	limiter      *rate.Limiter
	publishChan  chan<- *data.Observation
	numPublished int
	branding     *brandingBudget
}
```

Initialise in `downloadMassiveAssets` (`provider/massive/massive.go:167-170`):

```go
	api := &massiveAssetFetcher{
		subscription: subscription,
		publishChan:  out,
		branding:     NewBrandingBudget(maxIconLogoFetchesPerRun),
	}
```

In `assetDetail` (`provider/massive/massive.go:880-913`), gate icon and logo fetches:

```go
	// fetch icon and logo
	var (
		icon         []byte
		iconMimeType string
	)

	if massiveAsset.Branding.IconURL != "" && api.branding.Allow() {
		if err := api.limiter.Wait(ctx); err != nil {
			log.Panic().Err(err).Msg("rate limit failed")
		}

		resp, err := api.client.R().Get(massiveAsset.Branding.IconURL)
		if err != nil {
			logger.Error().Err(err).Msg("error when fetching asset icon")
			return nil, err
		}

		icon = resp.Body()
		iconMimeType = resp.Header().Get("Content-Type")
	}

	var (
		logo         []byte
		logoMimeType string
	)

	if massiveAsset.Branding.LogoURL != "" && api.branding.Allow() {
		if err := api.limiter.Wait(ctx); err != nil {
			log.Panic().Err(err).Msg("rate limit failed")
		}

		resp, err := api.client.R().Get(massiveAsset.Branding.LogoURL)
		if err != nil {
			logger.Error().Err(err).Msg("error when fetching asset logo")
			return nil, err
		}

		logo = resp.Body()
		logoMimeType = resp.Header().Get("Content-Type")
	}
```

- [ ] **Step 5: Run tests**

Run: `ginkgo run -race ./provider/massive/`
Expected: 3 specs PASS.

- [ ] **Step 6: Build**

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 7: Commit**

```bash
git add provider/massive/
git commit -m "feat(massive): cap icon/logo fetches per run"
```

---

## Task 14: Missing-branding lane in `filterAssetsByLastUpdated`

**Files:**
- Modify: `provider/massive/massive.go:531-609`

The lane queries the asset table for assets missing `icon_url` or `logo_url`, intersects with the current Massive response (so we know they're still active), and merges them into `assetDetail`. The per-run icon/logo cap from Task 13 still bounds how many actually upload.

We accept a small ongoing cost: assets where Massive has no branding URL get re-visited every run (one JSON detail call each, capped at 100 by the lane size). At 5/min that's at most ~20 minutes of API budget.

- [ ] **Step 1: Add the lane constant**

Near `maxIconLogoFetchesPerRun` in `provider/massive/massive.go`:

```go
const maxMissingBrandingPerRun = 100
```

- [ ] **Step 2: Add the missing-branding query**

Append after the existing assetUpdate sort/limit block (current `provider/massive/massive.go:589-606`):

```go
	// Missing-branding lane: surface DB assets whose icon_url /
	// logo_url is NULL — i.e., we never managed to upload binaries
	// for them. Limit per run so first runs don't drown the API.
	apiAssetMap := make(map[string]struct{}, len(assets))
	for _, a := range assets {
		apiAssetMap[fmt.Sprintf("%s:%s", massiveTicker2PvTicker(a.Ticker), a.CompositeFigi)] = struct{}{}
	}

	missingSQL := fmt.Sprintf(`SELECT
			ticker, composite_figi, share_class_figi, primary_exchange,
			asset_type, active, name, description, corporate_url, sector,
			industry, sic_code, cik, cusips, isins, other_identifiers,
			similar_tickers, tags,
			coalesce(to_char(listed, 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '') as listed,
			coalesce(to_char(delisted, 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '') as delisted,
			last_updated
		FROM %s
		WHERE active = true
		  AND (icon_url IS NULL OR logo_url IS NULL)
		LIMIT %d`, api.subscription.DataTablesMap[data.AssetKey], maxMissingBrandingPerRun*2)

	missingRows, missingErr := dbConn.Query(ctx, missingSQL)
	if missingErr != nil {
		logger.Warn().Err(missingErr).Msg("could not query missing-branding assets; skipping lane")
	} else {
		var dbMissing []*data.Asset
		if scanErr := pgxscan.ScanAll(&dbMissing, missingRows); scanErr != nil {
			logger.Warn().Err(scanErr).Msg("could not scan missing-branding assets; skipping lane")
		} else {
			added := 0

			for _, m := range dbMissing {
				if added >= maxMissingBrandingPerRun {
					break
				}

				key := fmt.Sprintf("%s:%s", m.Ticker, m.CompositeFigi)
				if _, present := apiAssetMap[key]; !present {
					continue // delisted or no longer in Massive
				}

				assetDetail = append(assetDetail, m)
				added++
			}

			if added > 0 {
				logger.Info().Int("AddedMissingBranding", added).Msg("queued missing-branding assets for refresh")
			}
		}
	}
```

(`pgxscan` is already imported at `provider/massive/massive.go:27`; `fmt` likewise.)

- [ ] **Step 3: Build**

Run: `go build ./...`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add provider/massive/massive.go
git commit -m "feat(massive): add missing-branding lane to assetDetail filter"
```

---

## Task 15: Confirm asset API surfaces `icon_url` / `logo_url`

**Files:**
- Verified: `web/handlers_data.go:172,175` — `GetSubscriptionData` uses `SELECT * FROM <tableName>` and ships every column the SELECT returns. Once the migration in Task 5 adds `icon_url` / `logo_url`, they will automatically appear in the JSON response under the `columns` and `data` arrays. No handler change required.

This task is a smoke test only — we run it after a successful Massive run to confirm the columns flow through end to end.

- [ ] **Step 1: Curl the endpoint**

```bash
curl -s 'http://localhost:3000/api/v1/subscriptions/<sub-id>/data/asset-description?limit=5' | jq '{columns, sample: (.data[0])}'
```

Expected: `columns` includes `"icon_url"` and `"logo_url"`; the sample row's positional value for those columns is either a `https://f<num>.backblazeb2.com/...` URL or `null`.

- [ ] **Step 2: No commit needed** if Step 1 passes.

If Step 1 fails (columns missing), the migration didn't run on this subscription's asset table. Trigger any run on the subscription so `web/run.go:84-86` (`sub.RunMigrations`) executes, then re-curl.

---

## Task 16: UI — render running runs with live progress

**Files:**
- Modify: `web/ui/src/pages/SubscriptionDetailPage.vue`

- [ ] **Step 1: Map the running status colour**

Find the Run History `<Tag>` cell. Replace:

```vue
<Tag :value="data.status"
     :severity="data.status === 'success' ? 'success' : data.status === 'failed' ? 'danger' : 'secondary'" />
```

with:

```vue
<Tag :value="data.status"
     :severity="data.status === 'success' ? 'success' : data.status === 'failed' ? 'danger' : data.status === 'running' ? 'warning' : 'secondary'">
  <template #default>
    <i v-if="data.status === 'running'" class="pi pi-spin pi-spinner" style="margin-right: 0.4rem; font-size: 11px" />
    {{ data.status }}
  </template>
</Tag>
```

- [ ] **Step 2: Reconnect on SSE drop instead of immediately failing**

Replace the existing `eventSource.onerror` handler (`web/ui/src/pages/SubscriptionDetailPage.vue:239-246`):

```ts
  eventSource.onerror = async () => {
    eventSource?.close()
    eventSource = null

    if (runStatus.value !== 'running') {
      return
    }

    try {
      const status = await getRunStatus(id.value)
      if (status.active) {
        await loadRuns()
        await attachEventSource()
        return
      }
    } catch {
      // fall through to failed
    }

    runStatus.value = 'failed'
    error.value = 'Lost connection to run event stream'
  }
```

- [ ] **Step 3: Build the front-end**

Run: `make build-ui`
Expected: PASS.

- [ ] **Step 4: Commit**

```bash
git add web/ui/src/pages/SubscriptionDetailPage.vue web/ui/dist
git commit -m "feat(ui): render running runs with live progress and reconnect on SSE drop"
```

---

## Task 17: UI — render asset icons and logos from B2 URLs

**Files:**
- Modify: `web/ui/src/components/DataBrowser.vue`

There is no dedicated asset detail page; asset rows render through the generic `DataBrowser.vue` (`<RevoGrid>` backed by `gridColumns` and `gridRows`). Today every cell goes through `formatCell()` which stringifies the value, so a URL would appear as raw text.

We add a `cellTemplate` for the `icon_url` and `logo_url` columns that returns an `<img>` element. RevoGrid `cellTemplate` receives a `createElement` and `props` argument; `props.model[props.prop]` is the cell value.

- [ ] **Step 1: Add an image cell template**

Edit `web/ui/src/components/DataBrowser.vue`. Above the `gridColumns` computed (currently `web/ui/src/components/DataBrowser.vue:73-85`), add:

```ts
function imageCellTemplate(createElement: any, props: any) {
  const url = props.model?.[props.prop]
  if (!url || typeof url !== 'string' || !url.startsWith('http')) {
    return ''
  }
  return createElement('img', {
    src: url,
    alt: props.prop,
    style: 'max-height: 28px; max-width: 80px; vertical-align: middle',
    loading: 'lazy',
    referrerpolicy: 'no-referrer',
  })
}
```

- [ ] **Step 2: Wire it onto the URL columns**

Replace the existing `gridColumns` computed (lines 73-85):

```ts
const gridColumns = computed(() =>
  columns.value.map(col => {
    let size = 120
    if (col === 'composite_figi' || col === 'share_class_figi') size = 150
    else if (col.includes('date') || col.includes('time')) size = 180
    else if (col === 'ticker' || col === 'series') size = 100
    else if (col === 'name' || col === 'description') size = 250
    else if (col === 'icon_url' || col === 'logo_url') size = 100
    else if (col.length > 12) size = col.length * 10

    const indicator = sortField.value === col ? (sortOrder.value === 'asc' ? ' ↑' : ' ↓') : ''

    const column: Record<string, any> = { prop: col, name: col + indicator, size }
    if (col === 'icon_url' || col === 'logo_url') {
      column.cellTemplate = imageCellTemplate
    }
    return column
  })
)
```

- [ ] **Step 3: Skip stringification for URL columns**

In `gridRows` (lines 87-96), `formatCell` currently turns every value into a `string`. We want the URL columns to preserve the raw URL so the cell template can read it. Update the inner forEach:

```ts
const gridRows = computed(() =>
  rows.value.map(row => {
    const obj: Record<string, any> = {}
    columns.value.forEach((col, i) => {
      const val = Array.isArray(row) ? row[i] : row[col]
      if (col === 'icon_url' || col === 'logo_url') {
        obj[col] = val ?? ''
      } else {
        obj[col] = formatCell(val)
      }
    })
    return obj
  })
)
```

- [ ] **Step 4: Build the front-end**

Run: `make build-ui`
Expected: PASS.

- [ ] **Step 5: Smoke test**

1. `make build && ./pvdata serve`.
2. Open Massive subscription → `asset-description` tab.
3. Confirm the `icon_url` / `logo_url` columns render small images for assets that have URLs and stay empty for those that don't.

- [ ] **Step 6: Commit**

```bash
git add web/ui/src/components/DataBrowser.vue web/ui/dist
git commit -m "feat(ui): render icon_url/logo_url cells as images"
```

---

## Task 18: Final verification

- [ ] **Step 1: Lint**

Run: `make lint`
Expected: PASS.

- [ ] **Step 2: Unit + Ginkgo tests**

Run: `make test`
Expected: PASS.

- [ ] **Step 3: Manual end-to-end**

1. In B2 dashboard: confirm `penny-vault` bucket exists, set Files in Bucket to "Public". Create an Application Key with read+write access; set `backblaze.application_id` and `backblaze.application_key` in `.pvdata.toml`.
2. Configure the Massive subscription with `filer = "b2://penny-vault/assets/logos"`.
3. Restart pvdata. Confirm any prior `running` rows in `run_history` were marked `failed` at startup (`marked abandoned runs as failed`).
4. Trigger a small subscription (e.g., FRED) and watch a row appear in `run_history` with `status='running'` whose `num_observations` updates roughly every 10 s. After completion the row flips to `success`.
5. Trigger Massive. Confirm a couple of `<COMPOSITE_FIGI>-icon.<ext>` files appear in B2 (under `assets/logos/`). `psql`: `SELECT ticker, icon_url FROM "<asset_table>" WHERE icon_url IS NOT NULL LIMIT 5;` — URLs match the bucket layout. Open one in a browser; image renders.
6. With Massive still in flight, kill the SSE connection in DevTools. The page should re-attach (no "Lost connection" toast) and the running row should keep climbing.
7. Re-run Massive on a later day; the missing-branding lane should pick up assets without URLs.
