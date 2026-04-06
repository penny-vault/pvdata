# Streaming File Import

## Overview

Refactor Sharadar file import to stream rows through a channel instead of loading the entire file into memory. Currently `readFileRows` returns `[]map[string]string` containing all rows (36M+ for large imports), causing excessive memory usage. The fix streams rows from readers to consumers via a buffered channel so memory is bounded by the channel buffer size rather than file size.

## Current Problem

`readFileRows` calls `readParquetRows` or `parseCSV`, both of which accumulate every row into a `[]map[string]string` slice before returning. For a 36M row metrics file, this means 36M `map[string]string` allocations live simultaneously. The `importMetricsRows`/`importFundamentalsRows` functions then iterate the slice, converting and sending to the observation channel -- but the original slice isn't freed until the function returns.

## Design

### Row Channel Type

```go
type RowResult struct {
    Row map[string]string
    Err error
}
```

Errors are sent on the same channel as rows. When an error is sent, the reader closes the channel immediately after. Consumers check each `RowResult.Err` and abort if non-nil.

### Reader Functions

All reader functions change from returning `[]map[string]string` to accepting a `chan<- RowResult` and running as goroutines. They close the channel when done.

**`streamCSV(r io.Reader, out chan<- RowResult)`** -- reads headers, then sends one `RowResult` per CSV row. On error, sends `RowResult{Err: err}` and returns (deferred close handles the channel).

**`streamParquetRows(path string, out chan<- RowResult)`** -- opens parquet reader, reads in 10k batches (existing behavior), sends each row to the channel as it's converted to `map[string]string`. At most 10k maps are being converted at a time.

**`streamCSVRows`, `streamCSVZstRows`, `streamCSVZipRows`** -- open the file/decompressor, call `streamCSV` with the appropriate `io.Reader`. These run as the goroutine body (handle file open/close within the goroutine).

**`streamFileRows(path string) <-chan RowResult`** -- replaces `readFileRows`. Creates a buffered channel (capacity 10000), detects format, launches the appropriate stream function as a goroutine, returns the receive-only channel.

### Consumer Functions

**`importMetricsRows`** and **`importFundamentalsRows`** change signature from `rows []map[string]string` to `rows <-chan RowResult`. They range over the channel. Progress logging changes from `len(rows)` total to just reporting rows processed every 10k (no total available upfront, which is fine).

**`importTickersRows`** -- this function does a single pass over rows, accumulating `allAssets` and batching FIGI enrichment every 5000 active assets. It changes to ranging over the channel. The `allAssets` slice is still needed because assets are emitted to the output channel only after all FIGI enrichment is complete. This is acceptable because ticker files are small (thousands of rows, not millions).

**`importSP500Rows`** -- this function makes two passes over the rows: first to collect unique tickers, then to build snapshots and changes. To avoid loading all rows into memory, restructure into a single pass: collect unique tickers and accumulate row data simultaneously. Alternatively, since SP500 files are small (~500 constituents * ~25 years = ~12.5k rows for snapshots), it can read from the channel into a local slice. This is acceptable because SP500 files are never large.

### ImportFiles Orchestrator

Changes from:
```go
rows, err := readFileRows(filePath)
// ... log len(rows)
switch sub.Dataset {
case "Fundamentals":
    n, err = importFundamentalsRows(ctx, sub, rows, out)
```

To:
```go
rowChan := streamFileRows(filePath)
switch sub.Dataset {
case "Fundamentals":
    n, err = importFundamentalsRows(ctx, sub, rowChan, out)
```

The row count log line moves into each import function, logged at completion with the total count.

### Error Handling

If a reader goroutine encounters an error (corrupt file, I/O failure), it sends `RowResult{Err: err}` on the channel and closes it. The consumer detects this, stops processing, and returns the error. The goroutine cleans up its file handles via defers.

If the consumer encounters an error (e.g., mapping failure), it stops reading from the channel. The reader goroutine will block on a full channel send, then exit when the channel is garbage collected. To handle this cleanly, the reader should use `select` with a context cancellation to avoid leaking the goroutine.

Updated signatures:

```go
func streamFileRows(ctx context.Context, path string) <-chan RowResult
```

All stream functions accept `ctx context.Context` and check `ctx.Done()` between sends:

```go
select {
case out <- RowResult{Row: row}:
case <-ctx.Done():
    return
}
```

The `ImportFiles` function already has a context it can pass through.

### Channel Buffer Size

10,000 rows. This matches the parquet internal batch size and provides enough buffering for the reader to stay ahead of the consumer without holding significant memory. At ~10 fields per metrics row, 10k maps is roughly 1-2 MB -- negligible compared to the current 36M rows in memory.

### What Does NOT Change

- `mapRowToSharadarMetric`, `mapRowToSharadarFundamental`, etc. -- these still accept `map[string]string` and return structs. No change.
- `parseFloat`, `parseInt` -- no change.
- `detectFileFormat`, `countFileRows` -- no change.
- The observation output channel pattern -- no change.
- Test structure -- existing tests that call import functions with slices will need to be updated to send rows through a channel instead.
