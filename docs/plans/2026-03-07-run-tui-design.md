# Run Command TUI Overhaul

## Problem

The `run` command has several UX issues:

- `ActiveAssets` panics when `default.asset_table` is not configured
- No interactive subscription selection -- users must type UUIDs
- No progress visibility during execution
- No dashboard for daemon mode

## Solution

A monolithic bubbletea application replacing the current `run` command, with pre-flight validation using `huh` forms.

## Pre-flight Validation

Before the bubbletea app launches, a pre-flight phase validates configuration using `huh` forms:

1. **Database connection** -- verify connectivity
2. **`default.asset_table`** -- if not set, query available asset tables from subscriptions:
   ```sql
   SELECT DISTINCT unnest(data_tables)
   FROM subscriptions
   WHERE data_types @> ARRAY['asset-description']::datatype[]
   ```
   Present a `huh.Select` for the user to pick one. Save to `~/.pvdata.toml`.
3. **Subscription selection** (one-off mode, no args) -- `huh.MultiSelect` showing name, provider/dataset for each subscription

Flow: `start -> validate DB -> validate asset_table -> select subscriptions (if needed) -> launch bubbletea app`

## Safety Fix

`data.ActiveAssets` signature changes from:
```go
func ActiveAssets(ctx context.Context, dbConn *pgxpool.Conn, tables ...string) []*Asset
```
to:
```go
func ActiveAssets(ctx context.Context, dbConn *pgxpool.Conn, tables ...string) ([]*Asset, error)
```

Returns an error instead of panicking when `default.asset_table` is not set. All callers updated to handle the error.

## Bubbletea App Structure

### Tabs

- **Subscriptions** -- table of all subscriptions: name, provider/dataset, status (idle/running/done/error), progress, last run. Real-time updates.
- **Logs** -- scrollable viewport fed by a zerolog channel. Color-coded by level.
- **History** -- current session runs with detailed stats (duration, records, securities) on top. Persistent history from DB below.
- **Config** -- read-only view of current configuration.

### Layout

```
+-- [Subscriptions] [Logs] [History] [Config] ----------+
|                                                       |
|  Main content area for active tab                     |
|                                                       |
|                                                       |
+-------------------------------------------------------+
| Status bar: running 2/5 | 1,247 records | 00:03:42   |
+-------------------------------------------------------+
```

### Key Bindings

- `tab` / `shift+tab` -- switch tabs
- `j/k` or arrow keys -- scroll within panes
- `q` / `ctrl+c` -- quit (with confirmation if subscriptions running)
- `enter` on a subscription -- expand detail view inline
- `/` -- filter/search within current pane

## RunManager

Bridges existing provider `Fetch` functions and the bubbletea UI.

### Responsibilities

- Holds subscriptions to execute (from pre-flight or daemon schedule)
- Runs each subscription's `Fetch` in a goroutine
- Wraps existing `outChan`/`exitChan` -- intercepts observations to track progress
- Emits `RunEvent` messages on a status channel consumed by the bubbletea app

### Event Types

```go
type RunEvent struct {
    SubscriptionID string
    Type           EventType  // Started, Progress, Completed, Failed
    RecordsCount   int
    Error          error
    Timestamp      time.Time
}
```

### Daemon Mode

Uses `gocron` (existing dependency) to schedule subscriptions. Scheduled jobs emit `Started` events and kick off fetches. The bubbletea app reacts to events without knowing about scheduling vs. one-off.

### Integration

The existing `library.SaveObservations` goroutine stays as-is. `RunManager` sits between provider `Fetch` output and `SaveObservations` input, counting records as they flow through.

## Dual Logging

A custom `zerolog.Writer` writes to both:
- A log file on disk
- A channel that feeds the bubbletea Logs tab

Set up before the bubbletea app starts.

## File Organization

```
cmd/
  run.go              -- rewritten: pre-flight, launches bubbletea app

tui/
  tui.go              -- main bubbletea model, tab management, key bindings
  subscriptions.go    -- subscriptions tab (table view)
  logs.go             -- logs tab (viewport + channel reader)
  history.go          -- history tab (session + DB)
  config.go           -- config tab (read-only)
  statusbar.go        -- bottom status bar
  styles.go           -- shared lipgloss styles
  preflight.go        -- pre-flight validation and huh forms
  runmanager.go       -- RunManager, RunEvent, orchestration
  logwriter.go        -- dual zerolog writer

data/
  asset.go            -- ActiveAssets returns error instead of panic

provider/
  tiingo.go           -- handle ActiveAssets error (2 sites)
  sharadar_metrics.go -- handle ActiveAssets error
  sharadar_fundamentals.go -- handle ActiveAssets error
  zacks.go            -- handle ActiveAssets error
```

## Dependencies

- `bubbletea`, `bubbles`, `lipgloss` promoted from indirect to direct
- `huh` already a direct dependency
- `gocron` already a direct dependency
