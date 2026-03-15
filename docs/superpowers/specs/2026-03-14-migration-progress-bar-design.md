# Migration Progress Bar Design

## Problem

The `pvdata migrate-legacy` command copies potentially millions of rows across multiple tables. Currently it runs silently until each table copy completes, then logs a row count. For large tables (EOD), this means no feedback for an extended period.

## Approach

Add an animated bubbletea progress bar. The migration runs in a background goroutine, sends progress updates over a channel, and a bubbletea program renders the progress.

## Files

| File | Change |
|---|---|
| `cmd/migrate_progress.go` | New file: bubbletea model, message types, view |
| `cmd/migrate_legacy.go` | Modify: launch bubbletea program, batch copyData by year |

## Bubbletea Model

```go
type migrationModel struct {
    progress    progress.Model    // bubbles animated progress bar
    progressCh  chan progressMsg   // receives updates from migration goroutine
    currentStep string            // e.g., "Copying EOD data..."
    currentRows int64
    totalRows   int64
    completed   []completedStep   // finished steps with row counts
    err         error
    done        bool
}

type completedStep struct {
    name string
    rows int64
}
```

## Message Types

- `progressMsg` -- sent by the migration goroutine: step name, rows copied so far, total rows for this step
- `migrationDoneMsg` -- sent when the entire migration finishes, carries an optional error
- `progress.FrameMsg` -- handled by the bubbles progress widget for smooth animation

## Flow

1. `runMigrateLegacy` creates a buffered `chan progressMsg`
2. Launches `executeMigration` in a goroutine, passing the channel
3. Creates a `tea.NewProgram(migrationModel{...})` and calls `Run()`
4. The model's `Init()` returns a command that listens on the channel
5. The goroutine sends `progressMsg` after each batch and a `migrationDoneMsg` when finished
6. `Update()` updates the progress bar percentage and re-listens on the channel
7. On `migrationDoneMsg`, the program quits and displays the final result
8. The `progress.FrameMsg` case calls `m.progress.Update(msg)` for smooth animation

## Batching Strategy

### Tables with event_date (eod, zacks_financials)

1. Query `SELECT MIN(EXTRACT(YEAR FROM event_date)), MAX(EXTRACT(YEAR FROM event_date))` to get the year range
2. Query `SELECT COUNT(*)` to get total rows
3. Loop year by year, appending `AND event_date >= 'YYYY-01-01' AND event_date < 'YYYY+1-01-01'` to the INSERT...SELECT
4. After each year-batch, send `progressMsg{step, rowsSoFar, totalRows}`

### Small tables (assets, market_holidays)

Copy in a single INSERT...SELECT. Send one `progressMsg` at 100% after the copy completes.

### Zacks split tables (rating, metric, estimate, consensus)

Each is derived from `legacy.zacks_financials` which has `event_date`. Batch the source query by year. For estimate (which produces multiple rows per source row due to series expansion), the total is approximate -- count the source rows and update as we go.

## Visual Output

```
  Migrating legacy database...

  [done] Moved tables to legacy schema
  [done] Created subscriptions
  [done] EOD data                1,234,567 rows
  [done] Assets                     12,345 rows
  Zacks ratings   ████████████░░░░░░░░  62%

```

Non-copy steps (schema move, subscription creation) are logged as completed steps without row counts.

## Changes to executeMigration

- Add a `progressCh chan progressMsg` parameter
- The `moveToLegacySchema` and `createLegacySubscriptions` functions send a step-complete message when done
- The `copyData` function and its helpers (`copyZacksRatings`, etc.) are refactored to:
  1. COUNT(*) the source table first
  2. Copy in year-based batches (for date-based tables)
  3. Send `progressMsg` after each batch
- After all copies complete, `updateSubscriptionMetadata` sends a step-complete message
- The goroutine sends `migrationDoneMsg` with the error (or nil) when `executeMigration` returns

## Dry-run Mode

When `--dry-run` is set, skip the bubbletea program entirely. Current behavior (log preflight results and return) is unchanged.

## Error Handling

If the migration fails, the goroutine sends `migrationDoneMsg{err}`. The bubbletea program displays the error and quits. The transaction rollback still happens inside `executeMigration`.
