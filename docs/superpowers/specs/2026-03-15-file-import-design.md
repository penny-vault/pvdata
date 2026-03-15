# File Import Command Design

## Problem

Historical Sharadar data exists in local parquet and CSV files that need to be imported into the pv-data database. The current system only supports fetching from the Nasdaq Data Link API. Users may not have an active API subscription but still want to load historical data from files.

## Solution

Add a `pvdata import` CLI command that reads local files and routes data through the existing observation pipeline, reusing the same parsing and persistence logic as the API fetchers.

## Scope

Three Sharadar datasets, mapping to existing data types:

| File Pattern (case-insensitive) | Dataset | Data Type |
|---|---|---|
| `*sf1*` | Fundamentals | `fundamental` |
| `*metrics*` or `*daily*` | Metrics | `metric` |
| `*tickers*` | Stock Tickers | `asset-description` |

Supported file formats: `.parquet`, `.csv`, `.csv.zst`, `.csv.zip`

Out of scope: SEP (EOD prices), Actions, Events, SF2, SF3, SF3A, SF3B, SFP.

## Interface Design

A new optional `FileImporter` interface on the Provider level:

```go
// In provider/provider.go
type FileImporter interface {
    ImportFiles(ctx context.Context, sub *library.Subscription,
        files []string, out chan<- *data.Observation, exit chan<- data.RunSummary)
}
```

- Lives alongside the existing `Provider` interface
- Not all providers need to implement it
- The import command type-asserts: `if fi, ok := provider.(FileImporter); ok { ... }`
- The dataset is read from `sub.Dataset` inside the implementation (not passed separately)
- Signature mirrors `Dataset.Fetch` but adds `files []string`

The `Sharadar` struct implements `FileImporter` by adding an `ImportFiles` method. No changes to `Provider` interface or `Dataset` struct.

## CLI Command

```
pvdata import --subscription <name-or-id> <file1> [file2] [file3...]
```

### Flags

- `--subscription` (required): name or UUID of an existing subscription

### Workflow

1. Load library from DB
2. Look up subscription by name or ID
3. Look up provider from `provider.Map[subscription.Provider]`
4. Type-assert provider to `FileImporter`; error if not implemented
5. Validate that all files exist and their filename patterns match the subscription's dataset
6. Call `subscription.ManagePartitions(ctx)` and `subscription.RunMigrations(ctx)`
7. Create observation channel (buffered, capacity 1000) and exit channel
8. Spawn `myLibrary.SaveObservations(outChan, &wg)` goroutine (`SaveObservations` is a method on `*Library`)
9. Call `provider.ImportFiles(ctx, subscription, files, outChan, exitChan)`
10. Wait for `RunSummary` on exit channel
11. Close outChan, wait for SaveObservations to finish
12. Look up `Dataset` from `provider.Datasets()[subscription.Dataset]` and run its `PostFetch` hooks on success
13. Log results (success/failure, observation count)

### Error Cases

- Subscription not found: exit with error
- Provider doesn't implement `FileImporter`: exit with error message listing which providers support file import
- File doesn't exist: exit with error before any DB work
- File pattern doesn't match subscription dataset: exit with error (e.g., "file sharadar_sep_20231228.parquet looks like SEP data but subscription 'My Fundamentals' is for dataset Fundamentals")

### Subscription Prerequisite

Users must create a subscription before importing. For historical-only imports without API access:

```
pvdata subscribe sharadar    # create subscription, can skip API key
pvdata import --subscription <id> <files>
```

Note: there is currently only a `pvdata enable` command (no `disable`). New subscriptions default to active. A `pvdata disable` command should be added (mirrors enable, calls `sub.Deactivate()`) for users who want to prevent daemon-scheduled runs. This is a small addition (~20 lines in `cmd/disable.go`).

The import command never calls `Fetch`, so empty API key config is fine.

## Sharadar ImportFiles Implementation

### File Format Detection

Based on file extension:
- `.parquet` -> parquet reader
- `.csv` -> CSV reader
- `.csv.zst` -> zstd-decompressed CSV reader
- `.csv.zip` -> zip-extracted CSV reader

### Dataset Detection from Filename

Case-insensitive match on the base filename (not full path), used to validate files match the subscription's dataset:
- `*sf1*` -> "Fundamentals"
- `*metrics*` or `*daily*` -> "Metrics"
- `*tickers*` -> "Stock Tickers"

### Per-Dataset Parsing

Each dataset gets a file reader function that:
1. Opens the file with the appropriate format reader
2. Reads rows into the existing intermediate struct
3. Calls the existing conversion method
4. Emits observations to the output channel

**Fundamentals (SF1):**
- Read rows into `sharadarFundamental` struct
- Call `PvFundamental(figiMap)` to convert
- Emit as `Observation{Fundamental: pvFundamental, ...}`
- FIGI map loaded from DB via `data.ActiveAssets()` before processing

**Metrics (Daily):**
- Read rows into `sharadarMetric` struct
- Call `PvMetric(figiMap, nyc)` to convert
- Emit as `Observation{Metric: pvMetric, ...}`
- FIGI map loaded from DB via `data.ActiveAssets()` before processing
- Note: the Metrics API fetcher also emits SP500 index snapshots; file import skips this (SP500 data is in a separate file that's out of scope). The Metrics subscription has two data types (`metric` and `index`); the import only populates the `metric` table, the `index` table is left untouched.

**Stock Tickers:**
- Filter rows to `table == "SF1"` only (matching the API fetcher's `?table=SF1` query parameter). Rows from other tables (SEP, SFP, etc.) are skipped.
- Read rows into `sharadarTicker` struct
- Call `ToAsset()` to convert
- Enrich active assets with FIGI via `figi.Enrich()`, batched in groups of ~5000 to match API fetcher page-size behavior
- Emit as `Observation{AssetObject: pvAsset, ...}`
- Same filtering as API fetcher: skip OTC, Index, Unknown exchanges/asset types

### FIGI Enrichment

- For Fundamentals and Metrics: load ticker->FIGI map from DB using `data.ActiveAssets()` before processing. Tickers without a FIGI mapping get an empty CompositeFigi (the DB upsert handles this).
- For Tickers: call `figi.Enrich()` on active assets, same as the API fetcher.

### Progress Logging

Log progress every 10,000 rows with current count. No TUI -- console output only (like daemon mode).

### Parquet-to-Struct Field Mapping

The existing API fetchers use positional gjson indices to parse JSON responses. For parquet and CSV files, columns are accessed by name. Each dataset needs a mapping from file column names to the intermediate Go struct fields:

- **Fundamentals:** The parquet column names (e.g., `accoci`, `assets`, `assetsavg`) map to `sharadarFundamental` struct fields. Use a manual mapping function that reads named columns from each row and populates the struct. The column name comments in `sharadarFundamental` already document the mapping (e.g., `// 6 = accoci`).
- **Metrics:** 10 columns, straightforward manual mapping from `ticker`, `date`, `lastupdated`, `ev`, `evebit`, `evebitda`, `marketcap`, `pb`, `pe`, `ps`.
- **Tickers:** ~28 columns, manual mapping from parquet column names to `sharadarTicker` struct fields.

For CSV files, use the header row to determine column positions, then map by column name (not position) for robustness.

### Zip File Handling

For `.csv.zip` files: expect a single CSV entry in the archive. If the zip contains multiple files, read only the first `.csv` entry. Error if the zip contains no CSV files.

### Subscription Lookup

The `--subscription` flag accepts a name or UUID. Resolution order: try UUID parse first; if that fails, do an exact name match. If multiple subscriptions share the same name, exit with an error listing the ambiguous matches.

### Error Handling

- File open/read errors: fail the run, set `RunSummary.Status = RunFailed`
- Individual row parse errors: log warning, skip the row, continue
- Missing FIGI for a ticker: log at debug level, emit observation with empty FIGI

## Files

### New Files

- **`cmd/import.go`** (~80 lines) -- CLI command definition and orchestration
- **`provider/sharadar_import.go`** (~300-400 lines) -- `ImportFiles` method, per-dataset file readers, parquet/CSV row mapping

### Modified Files

- **`provider/provider.go`** -- add `FileImporter` interface (~3 lines)

### Unchanged (Reused)

- `provider/sharadar_fundamentals.go` -- `sharadarFundamental` struct, `PvFundamental()` method
- `provider/sharadar_metrics.go` -- `sharadarMetric` struct, `PvMetric()` method
- `provider/sharadar_tickers.go` -- `sharadarTicker` struct, `ToAsset()`, `figi.Enrich()`
- `library/database.go` -- `SaveObservations()`
- `data/eod.go`, `data/fundamental.go`, `data/metric.go`, `data/asset.go` -- domain types and SaveDB methods

### New Dependencies

- `github.com/parquet-go/parquet-go` -- parquet file reading
- `github.com/klauspost/compress/zstd` -- zstandard decompression for .csv.zst files
