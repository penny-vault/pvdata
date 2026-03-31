# iShares Index Backfill Design

## Problem

The iShares index provider only fetches today's holdings. There is no way to backfill historical index composition data. The EOD provider supports a `--lookback` flag that controls how far back to fetch; the iShares provider should support the same mechanism.

Additionally, `previousSnapshotTickers` only returns the last full snapshot from the database, ignoring changelog entries (adds/removes) that occurred after that snapshot. This means if a ticker was removed via changelog after the last snapshot, it would be reported as removed again on the next run.

## Design

### Backfill Flow

1. `downloadSingleISharesETF` reads `LookbackFromContext(ctx, 14*24*time.Hour)` -- same default as EOD.
2. When lookback > 0, query the database using the existing `trading_days(start, end)` SQL function to get the list of dates from `now - lookback` to yesterday. Today's data is always fetched last without `asOfDate` (current behavior).
3. Load initial index state from the database using a new `currentIndexMembers` helper that reconstructs the true membership: last snapshot before the start date + all changelog adds/removes between that snapshot and the start date.
4. Loop through trading days oldest-first:
   - Fetch CSV with `&asOfDate=YYYYMMDD` appended to the existing URL template.
   - Diff against in-memory state (not DB).
   - Emit changelog entries for adds/removes.
   - Check `shouldTakeSnapshot` using the processing date vs the in-memory last snapshot date (not wall-clock `time.Since`).
   - If snapshot is due, emit snapshot observations and update in-memory last snapshot date.
   - Update in-memory state with adds/removes.
   - Rate-limit delay between requests.
5. After the loop, fetch today's data without `asOfDate` (preserving existing behavior).

### Weight Change Tracking

The changelog currently only records "add" and "remove" actions. A new "weight-change" action is added to capture significant shifts in constituent weights. The existing schema already supports this -- no migration needed.

A weight change is significant when the absolute difference exceeds 0.01 (1 percentage point). For example, a holding moving from 0.05 to 0.065 (delta 0.015) is recorded; a move from 0.05 to 0.055 (delta 0.005) is not.

During diffing, `diffSnapshots` is extended to also compare weights for tickers present in both the current and previous state. The in-memory state tracks weights alongside FIGIs so that weight changes can be detected. `currentIndexMembers` also returns weights so the initial state is complete.

### Changes

#### `index_helpers.go`

**New function: `currentIndexMembers(ctx, pool, table, indexName, asOfDate) map[string]indexMember`**

Where `indexMember` is a struct with `CompositeFigi string` and `Weight float64`.

Reconstructs the true index membership (including weights) as of a given date:
1. Query the most recent snapshot on or before `asOfDate` (tickers, FIGIs, and weights).
2. Query all changelog entries between that snapshot date and `asOfDate`.
3. Apply adds/removes/weight-changes to the snapshot to produce the true state.

This replaces `previousSnapshotTickers` in the iShares flow and fixes the existing bug where changelog entries after the last snapshot were ignored.

**New function: `tradingDays(ctx, pool, start, end) ([]time.Time, error)`**

Calls the existing `trading_days(DATE, DATE)` SQL function to get a list of NYSE trading days in the given range.

**Modified: `shouldTakeSnapshot(lastSnapshotDate, currentDate, frequency) bool`**

Add a `currentDate` parameter instead of using `time.Since()`, so it works correctly during backfill when processing historical dates. All existing callers updated to pass `time.Now()` or the processing date.

#### `ishares.go`

**Modified URL template**: Add `asOfDate` support. The URL becomes:
```
base_url + "&asOfDate=YYYYMMDD"  (for historical)
base_url                          (for today, no change)
```

**Modified `downloadSingleISharesETF`**: Accept `ctx` for lookback. When lookback > 0, run the backfill loop described above. The function signature gains no new parameters -- lookback comes from context, and trading days come from the subscription's DB pool.

### What Does Not Change

- The `snapshotFrequency` config and its semantics.
- The CSV parser (`parseISharesCSV`).
- The `emitChangelog` function.
- The `diffSnapshots` function signature changes to accept and return weight data, and to detect significant weight changes (see above).
- Non-iShares index providers (e.g., Nasdaq).
- The `previousSnapshotTickers` function remains available for other callers, though iShares will use `currentIndexMembers` instead.
