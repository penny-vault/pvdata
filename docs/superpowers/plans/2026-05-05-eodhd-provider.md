# EODHD Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add EODHD ([eodhd.com](https://eodhd.com)) as a data provider with three datasets:

1. **Stock Tickers** — asset catalog from `/api/exchange-symbol-list/{EXCHANGE}` (delisted optional).
2. **EOD** — daily OHLCV from `/api/eod-bulk-last-day/{EXCHANGE}` (incremental) and `/api/eod/{TICKER}.{EXCHANGE_ID}` (per-ticker backfill), with splits and dividends merged in.
3. **Intraday 1m** — minute-bar OHLCV from `/api/intraday/{TICKER}.{EXCHANGE_ID}?interval=1m`.

A new `IntradayKey` data type ("intraday-bar") is introduced to hold OHLCV minute bars; existing `QuoteKey` carries `price/change/change_pct` and is not a fit. The new type is partitioned **monthly** to match the row-volume target (~20–100M per partition).

**Tech Stack:** Go, resty (HTTP), zerolog (logging), Ginkgo/Gomega (testing). EODHD requires an `api_token` query param. Free tier is 20 calls/day; paid All-World+Intraday is 100k calls/day, 1000 RPM. Per-call costs: bulk-last-day = 100, intraday = 5, everything else = 1.

**Ticker format:** EODHD uses `BRK.A` (dot for share class); pv-data uses `BRK/A` (slash). Bidirectional translation lives in the provider only — internal state stays in pv-data form. EODHD does not provide FIGIs; resolution happens via the existing OpenFIGI helper, with a fall-back to synthetic `PVG…` FIGIs (`figi.GenerateSyntheticFIGI`) for tickers OpenFIGI cannot resolve.

---

### Task 1: Add the `intraday-bar` data type

**Files:**
- Modify: `data/datatype.go`
- Create: `data/intraday.go`
- Modify: `data/datatype.go` (add `IntradayKey` constant + `DataTypes[IntradayKey]` entry)
- Modify: `library/database.go` (Observation dispatch)
- Modify: `data/datatype.go` (Observation struct field)
- Create: `db/migrations/000014_intraday_datatype.up.sql` and `.down.sql`
- Modify: `library/subscription.go` if `monthlyPartitionStartYear` needs to be earlier than 2020 to support intraday backfill (decision in Step 1).

- [ ] **Step 1: Decide partition start year**

`library/subscription.go:389` hard-codes `monthlyPartitionStartYear = 2020`. EODHD has 1m bars since 2004 for US, but the realistic loader scope (see Task 4) starts well after that. For now keep `2020` and call out in the loader docs that earlier dates are out of range; revisit only if a user explicitly asks. Document this decision in the loader's config description.

- [ ] **Step 2: Define the `data.IntradayBar` Go type**

Create `data/intraday.go` mirroring `data/eod.go` but with `event_date TIMESTAMP` (not DATE). Fields: `Date time.Time`, `Ticker string`, `CompositeFigi string`, `Open/High/Low/Close float64`, `Volume float64`. No dividend, split, or adj_close — adjustment is out of scope for v1. `SaveDB(ctx, tbl, conn)` does an upsert keyed by `(composite_figi, event_date)`.

- [ ] **Step 3: Register the new data type**

In `data/datatype.go`:

```go
const (
    // ... existing keys ...
    IntradayKey = "intraday-bar"
)
```

Add to `DataTypes`:

```go
IntradayKey: {
    Name:       IntradayKey,
    ViewName:   "intraday_bars",
    DateColumn: "event_date",
    Schema: `CREATE TABLE %[1]s (
        ticker         CHARACTER VARYING(10) NOT NULL,
        composite_figi CHARACTER(12)         NOT NULL,
        event_date     TIMESTAMP             NOT NULL,
        open           NUMERIC(12, 4)        NOT NULL DEFAULT 0.0,
        high           NUMERIC(12, 4)        NOT NULL DEFAULT 0.0,
        low            NUMERIC(12, 4)        NOT NULL DEFAULT 0.0,
        close          NUMERIC(12, 4)        NOT NULL DEFAULT 0.0,
        volume         BIGINT                NOT NULL DEFAULT 0,
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
```

- [ ] **Step 4: Wire the Observation field and dispatch**

Add `IntradayBar *IntradayBar` to `data.Observation` (in `data/datatype.go`). Add the matching dispatch block in `library/database.go` next to the other `SaveDB` cases.

- [ ] **Step 5: Migration to extend the `datatype` enum**

Mirror `db/migrations/000003_add_datatype_enum_values.up.sql`:

```sql
ALTER TYPE datatype ADD VALUE IF NOT EXISTS 'intraday-bar';
```

The `down.sql` is a no-op (Postgres cannot drop enum values).

- [ ] **Step 6: Tests**

`data/datatype_test.go` (or a new `data/intraday_test.go`): Ginkgo specs for `IntradayBar.SaveDB` upsert behavior using a synthetic in-memory model — no live DB. Confirm: schema string formats with table name, primary-key conflict path is exercised in unit logic, and `event_date` is rendered as TIMESTAMP not DATE.

- [ ] **Step 7: Build, lint, commit**

```
go build ./... && ginkgo run -race ./data/
golangci-lint run --fix ./data/... ./library/...
git add data/ library/ db/migrations/
git commit -m "feat(data): add intraday-bar data type with monthly partitioning"
```

---

### Task 2: EODHD provider scaffolding + Stock Tickers (asset loader)

**Files:**
- Create: `provider/eodhd/eodhd.go`
- Create: `provider/eodhd/eodhd_suite_test.go`
- Create: `provider/eodhd/eodhd_test.go`
- Create: `provider/eodhd/ratelimit.go` (mirrors tiingo)
- Modify: `cmd/providers_register.go` (blank-import the new package)

The scaffolding (`Name`, `ConfigDescription`, `Description`, `Datasets`, init-time `provider.Register`) follows the tiingo template.

- [ ] **Step 1: Write the provider skeleton**

`provider/eodhd/eodhd.go` declares `type EODHD struct{}`, registers in `init()`, and exposes `Datasets()` returning all three (`Stock Tickers`, `EOD`, `Intraday 1m`) — but only `Stock Tickers` is wired in this task. The other two `Fetch` fields point to placeholder functions returning a `RunFailed` summary so the dataset list is visible end-to-end before the loaders land.

`ConfigDescription`:

| Key | Description |
|---|---|
| `apiKey` | EODHD API token. |
| `rateLimit` | Max requests per minute (default 1000). |
| `exchanges` | Comma-separated EODHD exchange codes for asset/EOD scope (default `US`). |
| `assetTypes` | Comma-separated pv-data asset types to keep (CS, ETF, MF, ADRC, …). Empty = all. |
| `includeDelisted` | `true` to also fetch delisted tickers via `delisted=1`. Default `false`. |
| `intradayTickers` | Comma-separated `TICKER` or `TICKER.EXCHANGE` entries for intraday loader scope. Empty = none. |
| `intradayLookbackDays` | Days of history to keep current per intraday run (default 5; max single request 120). |
| `workers` | Per-ticker EOD/intraday concurrency (default 10, capped by `rateLimit`). |

- [ ] **Step 2: Implement rate-limit helper**

`provider/eodhd/ratelimit.go` mirrors `provider/tiingo/ratelimit.go`: a `doWithRateLimit` that retries on 429, honors `Retry-After`, sleeps until the next minute boundary if missing, returns `errDailyRateLimit` once the second 429 lands, capped at `rateLimitWaitCap = 5 * time.Minute`. (EODHD's quota is per-day with per-minute throttling, so the cap is shorter than tiingo's hour.)

Keep tests for `computeRateLimitWait` / `parseRetryAfter` in line with the tiingo suite.

- [ ] **Step 3: Tests for ticker normalization and asset-type mapping**

`provider/eodhd/eodhd_test.go`:

- `normalizeTicker` round-trips `BRK.A ↔ BRK/A`, `BRK.B ↔ BRK/B`, leaves plain `AAPL` alone.
- `mapAssetType` maps EODHD `Type` strings to `data.AssetType`:
  - `"Common Stock"` → `CommonStock`
  - `"ETF"` → `ETF`
  - `"Fund"` / `"Mutual Fund"` → `MutualFund`
  - `"Preferred Stock"` → `CommonStock` *(documented decision; flag it in code comment)*
  - other → `UnknownAsset`
- `mapExchange` maps EODHD short names to `data.Exchange` (`NYSE → XNYS`, `NASDAQ → XNAS`, `BATS → BATS`, `NYSE MKT → XASE`, `NYSE ARCA → ARCX`, `OTC → OTC`).
- A small fixture-driven test parses an EODHD `exchange-symbol-list` JSON sample (committed under `provider/eodhd/testdata/`) and asserts the resulting `[]*data.Asset` contents (including ISIN passthrough into `Isins`).

Run: `ginkgo run -race ./provider/eodhd/` — expect FAIL until Step 4.

- [ ] **Step 4: Implement `downloadEodhdAssets`**

Rough shape, modeled on `downloadTiingoAssets`:

1. Bail out if `tickerFilter` or `figiFilter` is set (asset catalog scope is bulk-only, same as tiingo).
2. Parse `exchanges` config (comma-separated, default `US`); for each exchange:
   1. `GET /api/exchange-symbol-list/{EX}?api_token=&fmt=json` (1 call). Append rows.
   2. If `includeDelisted=true`, `GET /api/exchange-symbol-list/{EX}?api_token=&fmt=json&delisted=1` (1 call). Append rows; mark `Active=false`.
3. Convert each EODHD row to `*data.Asset`:
   - `Ticker = normalizeTicker(row.Code)`
   - `Name = row.Name`
   - `PrimaryExchange = mapExchange(row.Exchange)`
   - `AssetType = mapAssetType(row.Type)` (skip if `UnknownAsset` and not in `assetTypes` filter)
   - `Isins = []string{row.Isin}` if non-empty
   - `LastUpdated = time.Now()`
   - For active rows: `Active = true`, `DelistingDate = ""`. Delisted rows: `Active = false`, `DelistingDate = time.Now().Format(RFC3339)` (EODHD does not give a precise delist date in this endpoint).
4. Apply `assetTypes` filter.
5. Resolve FIGIs:
   1. `figi.Enrich(commonAssets...)` — fills `CompositeFigi` for whatever OpenFIGI knows.
   2. For each asset still missing `CompositeFigi`, mint one with `figi.GenerateSyntheticFIGI(asset.Ticker, asset.Name)` and log it. Pattern matches `provider/sharadar/sharadar_import.go:1051-1063`.
6. Reconcile the in-DB universe (active assets in `subscription.DataTablesMap[data.AssetKey]`) and emit `Active=false` for anything no longer present (gated by `assetTypes` filter to avoid cross-typing). Mirror the loop in `provider/tiingo/tiingo.go:605-621`.
7. Stream each asset onto `out` as a `data.Observation{ AssetObject: ... }`.

- [ ] **Step 5: Register the provider**

Add `_ "github.com/penny-vault/pvdata/provider/eodhd"` to `cmd/providers_register.go` (alphabetical, after `discover`).

- [ ] **Step 6: Build, lint, manual smoke test**

```
make build && golangci-lint run --fix ./provider/eodhd/...
ginkgo run -race ./provider/eodhd/
```

Manual: `pvdata subscribe` → eodhd → Stock Tickers; `pvdata run <sub-id>`; verify rows land in `eodhd_asset_description_*`.

- [ ] **Step 7: Commit**

```
git add provider/eodhd/ cmd/providers_register.go
git commit -m "feat(eodhd): add provider with Stock Tickers (asset) loader"
```

---

### Task 3: EOD loader

**Files:**
- Modify: `provider/eodhd/eodhd.go` (replace `EOD` placeholder)
- Create: `provider/eodhd/eod.go`
- Create: `provider/eodhd/eod_test.go`

Two modes, controlled by `LookbackFromContext`:

- **Incremental** (default): one bulk call per exchange per day in the lookback window. `GET /api/eod-bulk-last-day/{EX}?api_token=&date=YYYY-MM-DD&fmt=json`. Costs 100 calls per request — cheap when run nightly.
- **Backfill** (when lookback is large enough that the bulk window would exceed, say, 10 days OR when explicit ticker/FIGI filter is set): per-ticker `GET /api/eod/{TICKER}.{EXCHANGE_ID}?from=&to=&fmt=json` with the worker pool pattern from `downloadTiingoEODQuotes`.

Splits and dividends are not in the bulk EOD response. Two extra bulk calls per exchange per date (`type=splits`, `type=dividends`) merge in `Eod.Split` and `Eod.Dividend`. For per-ticker backfill, the `/api/eod` response is OHLC + adjusted_close + volume only; splits/dividends have to come from `/api/splits/{TICKER}.{EX}` and `/api/div/{TICKER}.{EX}` (1 call each). Document this and fold the merge into the per-ticker path so callers don't need to think about it.

- [ ] **Step 1: Tests for response parsing**

Fixture-driven tests in `provider/eodhd/eod_test.go`:

- `parseBulkEODResponse` parses one row per ticker into `(ticker, *data.Eod)` pairs; ticker normalized (`BRK.A` → `BRK/A`); `event_date` parsed as `YYYY-MM-DD` at NYSE close (16:00 America/New_York), matching the tiingo convention.
- `parsePerTickerEODResponse` parses an array of `{date, open, high, low, close, adjusted_close, volume}` records. Dates are `YYYY-MM-DD`.
- `parseSplitsResponse` returns `map[date]splitFactor`; `parseDividendsResponse` returns `map[date]cashAmount`.
- `mergeSplitsDividends` overlays split/dividend maps onto a slice of `*data.Eod` keyed by date.

- [ ] **Step 2: Implement bulk-mode fetch**

`fetchBulkEOD(ctx, client, exchange, date)` fires three requests in parallel (eod, splits, dividends), parses, merges, and emits one `*data.Observation{ EodQuote: ... }` per ticker. Resolves `CompositeFigi` from an in-process map populated by querying `data.ActiveAssets` for the subscription's asset table.

The dataset's `PostFetch` must include `provider.AdjustEodPrices` to mirror tiingo.

- [ ] **Step 3: Implement per-ticker fetch**

Worker pool over `data.ActiveAssets` (filtered by `assetTypes`, `tickerFilter`, `figiFilter` like tiingo), calling `GET /api/eod/{ticker}.{ex}` plus splits and dividends. Honor `LookbackFromContext` for `from`. Skip assets whose ticker the asset loader marked synthetic-only — they exist in our system but EODHD won't have them.

- [ ] **Step 4: Mode selector**

```
lookback := provider.LookbackFromContext(ctx, 7*24*time.Hour)
if tickerFilter != "" || figiFilter != "" || lookback > 30*24*time.Hour {
    runPerTicker(...)
} else {
    runBulk(...)
}
```

- [ ] **Step 5: Build, lint, manual test**

```
make build && golangci-lint run --fix ./provider/eodhd/...
ginkgo run -race ./provider/eodhd/
```

Manual: subscribe to EOD, run with default lookback, confirm rows in `eodhd_eod_*` partitions; run again with `--lookback 60d` to exercise the per-ticker path.

- [ ] **Step 6: Commit**

```
git add provider/eodhd/
git commit -m "feat(eodhd): add EOD loader (bulk + per-ticker backfill)"
```

---

### Task 4: Intraday 1m loader

**Files:**
- Modify: `provider/eodhd/eodhd.go` (replace `Intraday 1m` placeholder)
- Create: `provider/eodhd/intraday.go`
- Create: `provider/eodhd/intraday_test.go`

EODHD intraday is per-ticker only (no bulk endpoint). 5 calls per request, 120-day max range per request. `from`/`to` are Unix timestamps in UTC. Response: `{timestamp, gmtoffset, datetime, open, high, low, close, volume}`.

- [ ] **Step 1: Tests for response parsing and chunking**

`provider/eodhd/intraday_test.go`:

- `parseIntradayResponse` converts JSON rows to `[]*data.IntradayBar` with `event_date` set to `time.Unix(row.Timestamp, 0).UTC()`.
- `chunkRange(from, to, max=120*24h)` splits a wide range into 120-day windows, inclusive on both ends, returning `[]struct{From, To time.Time}`.

- [ ] **Step 2: Resolve intraday scope**

Source assets:

1. If `intradayTickers` config is non-empty, parse it. Each entry is `TICKER` (assumed to live on the configured `exchanges` list, defaulting to `US`) or `TICKER.EXCHANGE` (explicit override).
2. Otherwise: empty universe → log a warning, mark run successful with 0 observations, exit. The dataset must run cheaply when not configured; do not auto-scope to the active asset universe.

For each entry, look up `CompositeFigi` from `data.ActiveAssets` keyed by ticker; skip entries that have no FIGI (synthetic or otherwise) and warn — those won't satisfy the foreign key in any downstream join even though there's no FK constraint on `intraday-bar` itself.

- [ ] **Step 3: Implement worker pool**

Mirror `downloadTiingoEODQuotes`'s pool. For each `(ticker, exchange)`:

1. Compute the requested window: `from = now - intradayLookbackDays`, `to = now`.
2. Split into 120-day chunks via `chunkRange`.
3. For each chunk: `GET /api/intraday/{TICKER}.{EX}?api_token=&interval=1m&from=<unix>&to=<unix>&fmt=json` (5 calls each).
4. Parse, set `CompositeFigi` and `Ticker` on each bar, emit `*data.Observation{ IntradayBar: ... }`.

Honor cancellation via `workerCtx`, mirror the tiingo abort-on-daily-quota pattern using `errDailyRateLimit` from the shared ratelimit helper.

- [ ] **Step 4: Wire the dataset**

In `Datasets()`:

```go
"Intraday 1m": {
    Name:        "Intraday 1m",
    Description: "1-minute OHLCV bars for configured tickers (per-ticker fetch).",
    DataTypes:   []*data.DataType{data.DataTypes[data.IntradayKey]},
    DateRange: func() (time.Time, time.Time) {
        return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
    },
    Fetch: downloadEodhdIntraday,
    // ExpectedDuration left at default; varies wildly by configured universe size.
},
```

The `2020` lower bound matches `monthlyPartitionStartYear`. Document in `ConfigDescription` for `intradayLookbackDays` that earlier dates are not supported until partitions are extended.

- [ ] **Step 5: Build, lint, manual test**

```
make build && golangci-lint run --fix ./provider/eodhd/...
ginkgo run -race ./provider/eodhd/
```

Manual: configure `intradayTickers=AAPL,MSFT`, `intradayLookbackDays=2`, run, confirm ~780 rows per ticker per regular trading day land in `eodhd_intraday_bar_YYYY_MM`.

- [ ] **Step 6: Commit**

```
git add provider/eodhd/
git commit -m "feat(eodhd): add 1-minute intraday bar loader"
```

---

### Task 5: Documentation and changelog

**Files:**
- Modify: `CHANGELOG.md`

- [ ] **Step 1: Add a user-focused changelog entry**

Lead with what users can do, not internals. Example:

```
- Added EODHD as a data provider. Subscribe to `Stock Tickers`, `EOD`,
  or `Intraday 1m` to pull asset descriptions, daily OHLCV, and 1-minute
  bars from eodhd.com. Configure your API token and (for intraday) the
  ticker list under the eodhd provider in `pvdata subscribe`.
```

- [ ] **Step 2: Commit**

```
git add CHANGELOG.md
git commit -m "docs: changelog entry for EODHD provider"
```

---

## Out of scope (future work)

- Adjusted intraday prices (EODHD's `/api/intraday` returns raw OHLC; adjustment requires applying split/dividend history downstream).
- Fundamentals, dividends, splits, earnings, and economic data endpoints — separate datasets later if useful.
- Realtime / websocket endpoints.
- Backfill earlier than `monthlyPartitionStartYear = 2020` — would need partition logic to be parameterized per data type.
- Curated-universe / index-driven intraday scope (e.g. "follow SP500"). v1 requires an explicit ticker list so quota burn is predictable.
