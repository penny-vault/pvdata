# pvindex Tradable Universe Provider Design

## Overview

A new provider (`provider/pvindex/`) that produces a daily-recomputed investable universe of US-listed common stocks, published as an index named `us-tradable` using the existing `IndexSnapshot` and `IndexChange` schemas.

Unlike every other provider in the codebase, `pvindex` does not fetch from an external source. It is a **derived** provider: it reads from the canonical published views (`eod`, `metrics`, `assets`) and computes membership locally by applying a deterministic filter chain. This makes it the first member of a new provider category — derived/computed providers — but it does not require any change to the `Provider` interface.

The output matches the schema and conventions used by existing index providers (iShares, Nasdaq), so downstream consumers query it identically: `SELECT constituents FROM indices_snapshot WHERE index_ticker='us-tradable' AND snapshot_date=$1`.

## Goals

- Provide a single canonical "investable universe" that backtests and live strategies can use as a starting screen.
- Adapt to market conditions over time — no fixed dollar thresholds that go stale.
- Backfill historically as far back as the source data supports (~1999, bounded by `metrics` coverage).
- Match the existing index provider pattern (annual snapshots + daily changelog) so consumers and tooling work without modification.
- Capture distressed-recovery and large IPO names that strict filters historically exclude.

## Non-Goals

- Real-time intraday recomputation — this is end-of-day only.
- Tracking exact daily cap-weights via snapshots — weights are emitted via changelog `weight-change` events between annual snapshots; consumers needing exact daily weights should recompute from `metrics` at query time.
- Multiple universe variants in the initial release — only `us-tradable` ships. The provider is designed so additional universes can be added later without rework.

## Data Sources

The provider reads exclusively from canonical published views, never from subscription-specific tables. This insulates it from vendor changes and lets the published-view machinery handle multi-vendor multiplexing transparently.

- `assets` — security master (ticker, name, asset_type, primary_exchange, cik, composite_figi, share_class_figi, active)
- `eod` — daily OHLCV (close, volume, event_date, composite_figi)
- `metrics` — daily valuation metrics, used for `market_cap` (event_date, composite_figi, market_cap)

The provider writes to per-subscription tables produced for the `index-snapshot` and `index-changelog` data types, exactly like every other index provider. After the subscription is created and backfilled, an operator may register the snapshot/changelog tables as additional sources on the canonical `indices_snapshot` and `indices_changelog` published views, alongside other index sources (e.g., the existing Sharadar SPX entries). Since `index_ticker='us-tradable'` is unique, no row-level conflicts occur.

## Universe Definition

For each evaluation date `D`, the universe is computed as follows. The order matters — filters that are cheap or that establish denominators run first.

### 1. Asset master filter

Apply once per backfill chunk against `assets`:

- `active = true`
- `asset_type = 'CS'` — common stock only, by inclusion. (Excluding by enumerating `PS`/`ETF`/`ETN`/`CEF`/`MF`/`ADRC`/`SYNTH`/`INDEX` would be brittle as new asset types are added.)
- `primary_exchange IN ('NASDAQ', 'NYSE', 'NYSE MKT', 'NYSE ARCA', 'BATS', 'AMEX', 'XNAS', 'XNYS', 'XASE', 'ARCX')` — see "Known limitation: exchange field inconsistency" below.
- `name` does **not** end in any of: `LP`, `L.P.`, `L P`, `LLP`, `Limited Partnership` (case-insensitive, suffix match). LPs are stored as `CS` in the asset master and must be excluded explicitly. Suffix matching avoids false positives like "Marsh & McLennan Companies" while reliably catching "Enterprise Products LP".

### 2. Load EOD and metric data for the chunk window

Single bulk query per chunk; details in "Data Flow" below.

### 3. Compute rolling-window stats per FIGI

For each candidate, compute over the trailing window ending `D - 1 trading day`:

- `day_count` — number of EOD rows present in the trailing window.
- `median_dv` — median of `close * volume` over those rows.

The trailing window is `min(day_count, 200)` trading days. For stocks with full history, this is the trailing 200. For early-entry IPOs (see step 5), it is the available 30–199 days. Median is robust to the IPO-pop outlier days, so even a 30-day window gives a usable signal. Computed once per FIGI per chunk and reused by downstream filters.

### 4. Define the percentile baseline

The "broad CS pool on `D`" used by the percentile filters in steps 5 and 7 is computed once per `D` and consists of:

- All assets passing the structural filter from step 1, AND
- having a `metrics.market_cap` row dated ≤ `D`, AND
- after share-class deduplication (step 6 logic, applied to this set without the percentile filters yet).

Note this baseline is **not contingent on EOD availability** — it uses market cap data only. This avoids a circular dependency between the data availability check and the percentile threshold it consults. The percentile is the canonical "size of all US-listed common stocks on `D`" that both the early-entry rule (top 20%) and the size filter (bottom 25%) reference.

### 5. Data availability check

A stock qualifies for the universe on `D` if it satisfies one of:

- **Standard path**: `day_count >= 200` (200 contiguous trading days of EOD data ending `D - 1`), AND a `metrics.market_cap` row dated ≤ `D`.
- **Early-entry path** (for recent IPOs): `30 <= day_count < 200`, AND a `metrics.market_cap` row dated ≤ `D`, AND market cap on `D` is in the **top 20% (80th percentile)** of the broad CS pool on `D` (per step 4 baseline).

The early-entry path lets large recent IPOs (Meta, Snowflake, Uber, DoorDash, Airbnb, Palantir, Reddit) enter the universe roughly six weeks after listing instead of waiting ~10 months. The 80th-percentile threshold was empirically validated: every major IPO from 2004–2024 in our dataset clears it; smaller IPOs do not.

The contiguity check is exact: `day_count == count(trading_days(window))`. No fuzzing — even one missing day disqualifies a stock for that day.

### 6. Share-class deduplication

Group survivors by `cik`. Within each group, keep the row with the highest median dollar volume (from step 3). For the ~7% of CS rows missing CIK, use `composite_figi` as the grouping key (yielding singletons that cannot collide).

This step is necessary because multi-class companies (GOOG/GOOGL, BRK/A/BRK/B, BF/A/BF/B, FOX/FOXA) appear as separate rows in `assets` with the same CIK. We keep the most-traded class per company. Note: `share_class_figi` does **not** group multi-class companies in OpenFIGI's scheme — it groups same-class-across-countries. CIK is the correct grouping key.

### 7. Market cap percentile filter

Apply the 25th percentile of the broad CS pool baseline (from step 4) to the survivors of steps 1–6. Drop everything strictly below that threshold.

Using a percentile rather than a fixed dollar threshold means the cutoff adapts to market conditions over time: the threshold in 1999 dollars naturally differs from 2026 dollars without manual recalibration.

### 8. Liquidity filter

Median dollar volume (from step 3) `≥ $2,500,000`.

### 9. Price guard rail

`prior_close ≥ $2.00`. The `$2` floor was chosen empirically — see "Price floor: $2" in the appendix. It excludes the broken sub-dollar cohort and the smallest distressed names while still admitting recognizable multi-billion-dollar companies that trade at low nominal prices (BlackBerry, Newell Brands, Coty, Transocean, etc.).

### 10. Cap-weight assignment

For each survivor `i`: `weight_i = market_cap_i / Σ market_cap_j`. Weights sum to 1.0 within float tolerance.

## Output Format

The provider emits two observation types using the existing `IndexSnapshot` and `IndexChange` schemas. No new data types or migrations are required.

### Annual snapshots

On the first trading day of each calendar year (and on the very first day of any backfill range), the provider emits a single `IndexSnapshot` observation containing the full constituent list with cap-weights as of that day. Snapshot frequency is `"yearly"` via the existing `ShouldTakeSnapshot` helper, matching the pvbt convention used by iShares and Nasdaq.

```go
data.IndexSnapshot{
    IndexTicker:  "us-tradable",
    SnapshotDate: D,
    Constituents: []data.IndexConstituent{
        {Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", Weight: 0.0451},
        ...
    },
}
```

### Daily changelog

For every trading day `D` (including snapshot days), the provider compares the freshly-computed universe against the prior-day reconstructed state and emits `IndexChange` observations:

- **Adds**: stocks in the new universe that were not in the prior state. Action `"add"`. Weight = cap-weight on `D`.
- **Removes**: stocks in the prior state that are not in the new universe. Action `"remove"`. Weight unset (0).
- **Weight changes**: stocks present in both, whose new weight differs from the previously-recorded weight by `≥ 25% relative` (i.e., `|new - prev| ≥ 0.25 * prev`). Action `"weight-change"`. Weight = cap-weight on `D`.

The 25% relative threshold replaces the existing absolute `WeightChangeThreshold = 0.01` for this provider. For a ~3,000-name universe where each weight is ~0.03%, the absolute threshold would essentially never fire; the relative threshold scales naturally to universe size.

### Snapshot weight semantics

The annual snapshot captures cap-weights as of the snapshot day. Throughout the year, `weight-change` events update the canonical state for any constituent whose weight has drifted ≥25% relative since its previously-recorded weight. A stable mega-cap typically emits one `weight-change` per year; a fast-mover may emit several. Storage scales with churn, not membership size.

## Package Structure

```
provider/pvindex/
  pvindex.go              # Provider registration, dataset definition, Fetch entry point
  filter.go               # Filter chain (asset master, contiguity, share-class dedup, percentile, liquidity, price)
  loader.go               # Chunked EOD/metric loading from canonical views
  rolling.go              # In-memory rolling 200-day median computation
  diff.go                 # Cap-weight calculation, snapshot vs prior-state diffing
  pvindex_test.go         # Unit tests
  pvindex_suite_test.go   # Ginkgo suite
  integration_test.go     # Build tag `integration` — DB-backed end-to-end test
  testdata/               # Synthetic EOD/metric fixtures for unit tests
```

The new provider is registered in `cmd/providers_register.go` via blank import:

```go
import _ "github.com/penny-vault/pvdata/provider/pvindex"
```

## Provider Interface

Registers as `"pvindex"` implementing the existing `Provider` interface.

- **`Name()`**: `"pvindex"`
- **`Description()`**: `"Derived index provider that computes investable universes from canonical EOD, metric, and asset views"`
- **`ConfigDescription()`**:
  - `index_ticker` — optional, default `"us-tradable"`. Override only to publish a parallel-tuned universe (e.g., `us-tradable-strict`) without modifying source data.
  - `start_date_override` — optional, default empty. Forces a later start date during testing or selective backfill.
  - `chunk_size_days` — optional, default `63`. Tuning knob; not user-facing in normal operation.
- **`Datasets()`**: one dataset, `"US Tradable Universe"`.

```go
"US Tradable Universe": provider.Dataset{
    Name:        "US Tradable Universe",
    Description: "Daily-recomputed investable universe of US common stocks: structural + liquidity + size + price filters with annual cap-weighted snapshots",
    DataTypes:   []*data.DataType{data.DataTypes[data.IndexSnapshotKey], data.DataTypes[data.IndexChangelogKey]},
    DateRange:   computeDateRange,
    TTL:         0,
    Fetch:       fetchTradableUniverse,
}
```

`computeDateRange` queries the canonical views at fetch time:

- **Start**: 200 trading days after `MIN(event_date) FROM metrics`. With `metrics` starting 1998-07-16, this gives roughly 1999-04-30.
- **End**: `LEAST(MAX(event_date) FROM eod, MAX(event_date) FROM metrics)`. We cannot compute past the date where market cap data ends.

## Data Flow

### Backfill (cold start)

```
Determine date range from canonical views
  -> chunk into 63-trading-day windows
  -> for each chunk [d1..dn]:
     -> single query: SELECT * FROM eod WHERE event_date BETWEEN (d1 - 200d) AND dn AND composite_figi IN candidates
     -> single query: SELECT * FROM metrics WHERE event_date BETWEEN (d1 - 200d) AND dn AND composite_figi IN candidates
     -> hold in memory: map[composite_figi][]eodRow, sorted by date
     -> for each trading day d in [d1..dn]:
        -> compute trailing-200d median dollar volume by rolling window (no DB I/O)
        -> apply filter chain (Section "Universe Definition")
        -> reconstruct prior state from in-memory cursor
        -> diff -> add/remove/weight-change events
        -> emit IndexChange observations
        -> if d is the first trading day of a calendar year (or d == d1 of cold start), emit IndexSnapshot
     -> reload prior state from DB at the start of the next chunk (single point of truth)
```

Memory bound per chunk: ~6,000 candidate stocks × 263 days × ~50 bytes per EOD row ≈ 80 MB. Comfortable.

### Incremental run (scheduled)

```
high water mark = MAX(snapshot_date, last changelog event_date) for index_ticker='us-tradable'
firstDay = high_water_mark + 1 trading day
lastDay = end of computeDateRange()
if firstDay > lastDay:
    emit empty RunSummary, return
otherwise: process [firstDay..lastDay] in chunks (same code path as backfill)
```

This naturally handles three cases identically: cold-start backfill, routine daily run (one new day), and gap recovery (provider was down for a week).

## Helper Changes

Two minimal additions to existing helpers in `provider/index_helpers.go`:

### 1. `DiffSnapshotsWithThreshold`

The existing `DiffSnapshots` is unchanged. A new variant accepts a threshold mode:

```go
type DiffOptions struct {
    AbsoluteThreshold float64 // existing 0.01 default for absolute mode
    RelativeThreshold float64 // 0 = disabled; for pvindex, 0.25 (25%)
}

func DiffSnapshotsWithThreshold(current, previous map[string]IndexMember, opts DiffOptions) (added, removed, weightChanged map[string]IndexMember)
```

Logic: a weight is considered changed when `|delta| >= max(opts.AbsoluteThreshold, prev.Weight * opts.RelativeThreshold)`. Existing iShares/Nasdaq callers continue to use the original `DiffSnapshots` (zero-touch).

### 2. In-process state caching across the chunk

The pvindex loader maintains `prevState map[string]IndexMember` in memory across the chunk's days, applying each day's emitted changes locally instead of round-tripping to the DB. The DB version remains the source of truth across chunk boundaries: each new chunk reloads from DB via `CurrentIndexMembers`.

This is a usage pattern, not a helper change — `CurrentIndexMembers` is called once per chunk start.

## Edge Cases and Known Limitations

### Edge cases

- **EOD data gaps within the 200-day window**: A stock with even one missing trading day inside the trailing 200 fails the contiguity check and is excluded for that day. By design.
- **New IPOs**: Handled by the early-entry path (Section "Universe Definition" step 5). Stocks under 30 days old are always excluded.
- **Stock with valid 200d EOD but no market cap on D**: Excluded silently. Logged at DEBUG level once per chunk; promoted to WARN if exclusion count exceeds 10% of candidates (signals a real metric data gap).
- **CIK collisions across genuinely-different companies**: Rare. Share-class dedup keeps the row with highest median dollar volume per CIK; if two unrelated companies share a CIK, one is dropped. Acceptable trade-off; flagged here as a known limitation.
- **Stock with no CIK**: Falls back to `composite_figi` as the grouping key, becoming a singleton. Cannot collide with other rows.
- **Snapshot table conflicts on re-run**: `IndexSnapshot.SaveDB` already upserts on `(index_ticker, snapshot_date)`. Re-runs are idempotent.
- **Changelog reconciliation on re-run**: `IndexChange.SaveDB` upserts on `(composite_figi, index_ticker, event_date)`. Multiple changes for the same FIGI on the same day collapse to one row (last write wins). The filter chain is deterministic, so re-running on the same date produces identical output.
- **Empty universe day**: Mathematically possible. Emits a snapshot with empty constituents list; consumers should treat as "no data" rather than "universe is empty".
- **Backfill dates before sufficient market cap data**: Excluded by `DateRange.Start`. The provider never attempts to compute the universe for dates outside the date range.

### Known limitations

- **Exchange field inconsistency**: The `assets.primary_exchange` column is mostly populated with display names (`NASDAQ`, `NYSE`, `NYSE MKT`, `NYSE ARCA`, `BATS`, `AMEX`) but the Go constants in `data/asset.go` use MIC codes (`XNAS`, `XNYS`, `XASE`, `ARCX`, `BATS`). The provider's exchange whitelist accepts both formats as a workaround. **The underlying inconsistency should be fixed by a separate cleanup task** that normalizes the field across source providers, after which the whitelist can drop the display-name aliases.
- **ADRs are excluded by design**: Major foreign listings (BABA, JD, BIDU) are classified as `ADRC` and never enter the universe. Per the original spec ("exclude... ADRs"), this is intentional.
- **Sharadar metric data ends 2026-01-08**: Until a newer metric source is published, `DateRange.End` is bounded by the metric data even though `eod` extends further. The provider correctly computes this bound at runtime.

## Testing Strategy

Three layers of tests, following the project's Ginkgo + Gomega convention.

### Unit tests (`provider/pvindex/`)

- LP suffix matcher: table-driven cases including positive matches ("Enterprise Products LP", "Brookfield Infrastructure L.P.") and negatives ("Marsh & McLennan Companies", "MetLife Inc").
- Asset master filter: structural inclusion/exclusion against synthetic asset rows.
- Rolling 200-day median: known-input fixtures with expected median.
- Contiguity check: synthetic EOD slices with and without gaps.
- Share-class dedup: multi-class company fixtures (GOOG/GOOGL with synthetic dollar volumes), CIK-fallback case.
- Market cap percentile filter: known distribution, verify cutoff.
- Cap-weight normalization: weights sum to 1.0 within float tolerance.
- Early-entry path: stock with 50 days of data + top-quintile market cap passes; same stock with bottom-quintile cap is excluded.
- `DiffSnapshotsWithThreshold`: table-driven cases for both absolute and relative threshold modes; verify backwards compat with `DiffSnapshots`.

### Integration tests (`provider/pvindex/integration_test.go`, build tag `integration`)

- Backfill a single test month (e.g., 2023-01-01 to 2023-01-31) against the real DB.
- Assertions:
  - Snapshot row count = 1 (year boundary at 2023-01-03).
  - Changelog event count is non-zero.
  - All emitted constituents have non-empty `composite_figi`.
  - All snapshot weights sum to 1.0 within float tolerance.
  - A known liquid CS (e.g., AAPL) is in the universe on 2023-01-31.
  - A known LP (e.g., EPD) is NOT in the universe.
  - A known ADRC (e.g., BABA) is NOT in the universe.
- Re-run the same test month and assert idempotency: snapshot/changelog row counts unchanged, no duplicates.

## Configuration and Operations

Subscription configuration is minimal:

- No source-data subscription IDs (reads from canonical views).
- No vendor API keys.
- Optional overrides only (`index_ticker`, `start_date_override`, `chunk_size_days`).

Operationally, the subscription is:

1. Created via the standard subscription UI/CLI, selecting provider `pvindex` and dataset `US Tradable Universe`.
2. Backfilled by running it on-demand once. Initial backfill scans ~6,500 trading days from 1999-04-30 to today.
3. Scheduled for daily incremental runs after market close.
4. Optionally registered as an additional source on the canonical `indices_snapshot` and `indices_changelog` published views, so consumers can query `SELECT * FROM indices_snapshot WHERE index_ticker='us-tradable'` through the canonical interface.

## Appendix: Empirical calibrations

These thresholds were validated against the actual data in the database before being chosen.

### Price floor: $2

Three thresholds were measured against today's data (2026-01-08), four crisis dates (2009-03-09, 2020-03-23, 2022-10-12, 2023-03-13), and the cohort that passes ADV ≥ $2.5M:

- **$5 floor**: excludes 192 names today (~7% of investable universe), including BlackBerry ($2.2B), Newell Brands ($1.7B), Coty ($2.6B), Transocean ($4.5B), Plug Power ($3.1B), and other multi-billion-dollar real companies. Rejected.
- **$1 floor**: excludes only 12 names today, but includes Kosmos Energy ($433M, $0.92), Chegg ($104M, $0.93), and a small cohort of distressed-but-recoverable names. Loses some real distressed-value plays.
- **$2 floor (chosen)**: catches the genuinely-broken sub-dollar cohort plus the AGL/CHGG/MVIS-class distressed names whose fundamentals are usually unsalvageable, while keeping the BlackBerry/Newell/Coty cohort that has strong businesses despite low nominal prices.

### IPO early-entry market cap threshold: top 20% (80th percentile)

Validated against ten major IPOs from 2004–2024:

| Ticker | IPO date | IPO market cap | Percentile rank | Top 10%? | Top 20%? |
|---|---|---|---|---|---|
| GOOGL | 2004-08-19 | $27.2B | — | YES | YES |
| META | 2012-05-18 | $81.7B | — | YES | YES |
| LYFT | 2019-03-29 | $22.2B | — | YES | YES |
| UBER | 2019-05-10 | $69.7B | — | YES | YES |
| SNOW | 2020-09-16 | $70.4B | — | YES | YES |
| PLTR | 2020-09-30 | $15.7B | 88.8% | no | YES |
| DASH | 2020-12-09 | $60.2B | — | YES | YES |
| ABNB | 2020-12-10 | $86.5B | — | YES | YES |
| RDDT | 2024-03-21 | $8.0B | 83.2% | no | YES |

Top decile (90th percentile) misses Palantir and Reddit. Top quintile (80th percentile) catches both while still being demanding enough that it cannot pass mid-cap or smaller IPOs.

### Snapshot frequency: annual

Matches the pvbt convention used by iShares and Nasdaq providers. Daily snapshots were considered and rejected as inconsistent with the project's existing index pattern. Weekly/monthly were considered for fresher cap-weights but rejected in favor of using `weight-change` changelog events with a relative threshold to track drift between annual snapshots.

### Weight-change threshold: 25% relative

The existing `WeightChangeThreshold = 0.01` (1% absolute) is calibrated for SPX-style indices where each constituent is several percent of the index. For a ~3,000-name universe where each weight averages ~0.03%, a 1% absolute threshold would essentially never fire. A 25% relative threshold scales naturally: a constituent's weight emits a `weight-change` when it has drifted by at least 25% of its previously-recorded value. Stable mega-caps emit ~1 event/year; fast-movers emit several. Storage scales with churn, not size.
