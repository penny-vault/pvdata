# Migration Progress Bar Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add an animated bubbletea progress bar to `pvdata migrate-legacy` that shows real-time progress during data copy.

**Architecture:** Create a bubbletea model in a new file (`cmd/migrate_progress.go`) that renders progress. Refactor `executeMigration` and `copyData` to accept a progress channel and batch large table copies by year. Launch the bubbletea program from `runMigrateLegacy` with the migration running in a background goroutine.

**Tech Stack:** charm.land/bubbletea/v2, charm.land/bubbles/v2/progress

**Spec:** `docs/superpowers/specs/2026-03-14-migration-progress-bar-design.md`

---

## File Structure

| File | Responsibility |
|---|---|
| `cmd/migrate_progress.go` | New: bubbletea model, message types, Init/Update/View |
| `cmd/migrate_legacy.go` | Modify: add progress channel to executeMigration/copyData, batch copies by year, launch bubbletea program |

---

## Chunk 1: Progress Model and Migration Integration

### Task 1: Create the bubbletea progress model

**Files:**
- Create: `cmd/migrate_progress.go`

- [ ] **Step 1: Create the progress model file**

```go
package cmd

import (
	"fmt"
	"strings"

	"charm.land/bubbles/v2/progress"
	tea "charm.land/bubbletea/v2"
)

// progressMsg is sent by the migration goroutine to report progress on a step.
type progressMsg struct {
	step    string // display name of the current step
	current int64  // rows copied so far in this step
	total   int64  // total rows for this step
	done    bool   // true when this step is complete
}

// migrationDoneMsg is sent when the entire migration finishes.
type migrationDoneMsg struct {
	err error
}

type completedStep struct {
	name string
	rows int64
}

type migrationModel struct {
	progress    progress.Model
	progressCh  <-chan progressMsg
	currentStep string
	currentRows int64
	totalRows   int64
	completed   []completedStep
	err         error
	done        bool
	width       int
}

const (
	progressPadding  = 2
	progressMaxWidth = 72
)

func newMigrationModel(ch <-chan progressMsg) migrationModel {
	return migrationModel{
		progress:   progress.New(progress.WithDefaultBlend()),
		progressCh: ch,
	}
}

func (m migrationModel) Init() tea.Cmd {
	return m.waitForProgress()
}

func (m migrationModel) waitForProgress() tea.Cmd {
	return func() tea.Msg {
		msg, ok := <-m.progressCh
		if !ok {
			return migrationDoneMsg{}
		}

		return msg
	}
}

func (m migrationModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+c" {
			return m, tea.Quit
		}

		return m, nil

	case tea.WindowSizeMsg:
		m.width = msg.Width - progressPadding*2 - 4
		if m.width > progressMaxWidth {
			m.width = progressMaxWidth
		}

		m.progress.SetWidth(m.width)

		return m, nil

	case progressMsg:
		if msg.done {
			m.completed = append(m.completed, completedStep{
				name: msg.step,
				rows: msg.current,
			})
			m.currentStep = ""
			m.currentRows = 0
			m.totalRows = 0

			// Reset progress bar to 0 for next step
			cmd := m.progress.SetPercent(0)

			return m, tea.Batch(m.waitForProgress(), cmd)
		}

		m.currentStep = msg.step
		m.currentRows = msg.current
		m.totalRows = msg.total

		var pct float64
		if msg.total > 0 {
			pct = float64(msg.current) / float64(msg.total)
		}

		cmd := m.progress.SetPercent(pct)

		return m, tea.Batch(m.waitForProgress(), cmd)

	case migrationDoneMsg:
		m.done = true
		m.err = msg.err

		return m, tea.Quit

	case progress.FrameMsg:
		var cmd tea.Cmd
		m.progress, cmd = m.progress.Update(msg)

		return m, cmd

	default:
		return m, nil
	}
}

func (m migrationModel) View() tea.View {
	pad := strings.Repeat(" ", progressPadding)
	var b strings.Builder

	b.WriteString("\n" + pad + "Migrating legacy database...\n\n")

	// Completed steps
	for _, s := range m.completed {
		if s.rows > 0 {
			b.WriteString(fmt.Sprintf("%s  [done] %-25s %s rows\n", pad, s.name, formatNumber(s.rows)))
		} else {
			b.WriteString(fmt.Sprintf("%s  [done] %s\n", pad, s.name))
		}
	}

	// Current step with progress bar
	if m.currentStep != "" && m.totalRows > 0 {
		b.WriteString(fmt.Sprintf("%s  %-25s\n", pad, m.currentStep))
		b.WriteString(pad + "  " + m.progress.View() + "\n")
	} else if m.currentStep != "" {
		b.WriteString(fmt.Sprintf("%s  %s...\n", pad, m.currentStep))
	}

	// Done message
	if m.done {
		b.WriteString("\n")

		if m.err != nil {
			b.WriteString(fmt.Sprintf("%s  Migration failed: %v\n", pad, m.err))
		} else {
			b.WriteString(pad + "  Migration completed successfully.\n")
		}

		b.WriteString("\n")
	}

	return tea.NewView(b.String())
}

func formatNumber(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}

	if n < 1_000_000 {
		return fmt.Sprintf("%d,%03d", n/1000, n%1000)
	}

	return fmt.Sprintf("%d,%03d,%03d", n/1_000_000, (n%1_000_000)/1000, n%1000)
}
```

- [ ] **Step 2: Verify it compiles**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && go build ./cmd/...
```

- [ ] **Step 3: Commit**

```bash
git add cmd/migrate_progress.go
git commit -m "feat: add bubbletea progress model for migrate-legacy"
```

---

### Task 2: Refactor executeMigration to accept a progress channel

**Files:**
- Modify: `cmd/migrate_legacy.go`

- [ ] **Step 1: Add progress channel parameter to executeMigration**

Change the signature of `executeMigration` from:
```go
func executeMigration(ctx context.Context, pool *pgxpool.Pool, myLibrary *library.Library) error {
```
to:
```go
func executeMigration(ctx context.Context, pool *pgxpool.Pool, myLibrary *library.Library, progressCh chan<- progressMsg) error {
```

Add progress notifications after each major step. Insert these sends into the existing function body:

After `moveToLegacySchema` succeeds (after line 268):
```go
progressCh <- progressMsg{step: "Moved tables to legacy schema", done: true}
```

After `createLegacySubscriptions` succeeds (after line 279):
```go
progressCh <- progressMsg{step: "Created subscriptions", done: true}
```

After `updateSubscriptionMetadata` succeeds (after line 289):
```go
progressCh <- progressMsg{step: "Updated metadata", done: true}
```

- [ ] **Step 2: Add progress channel parameter to copyData and helpers**

Change `copyData` signature to:
```go
func copyData(ctx context.Context, tx pgx.Tx, subs *legacySubscriptions, progressCh chan<- progressMsg) error {
```

Change `copyZacksRatings`, `copyZacksMetrics`, `copyZacksEstimates`, `copyZacksConsensus` signatures to also accept `progressCh chan<- progressMsg`.

Update the call in `executeMigration`:
```go
if err := copyData(ctx, tx, subs, progressCh); err != nil {
```

Update the calls within `copyData`:
```go
if err := copyZacksRatings(ctx, tx, subs.zacks, progressCh); err != nil {
if err := copyZacksMetrics(ctx, tx, subs.zacks, progressCh); err != nil {
if err := copyZacksEstimates(ctx, tx, subs.zacks, progressCh); err != nil {
if err := copyZacksConsensus(ctx, tx, subs.zacks, progressCh); err != nil {
```

- [ ] **Step 3: Add progress sends to simple table copies in copyData**

For EOD (after the Exec succeeds):
```go
progressCh <- progressMsg{step: "EOD data", current: result.RowsAffected(), done: true}
```

For Assets:
```go
progressCh <- progressMsg{step: "Assets", current: result.RowsAffected(), done: true}
```

For Market Holidays:
```go
progressCh <- progressMsg{step: "Market holidays", current: result.RowsAffected(), done: true}
```

For Zacks helpers, add a done message at the end of each:

In `copyZacksRatings`, replace the log line with:
```go
progressCh <- progressMsg{step: "Zacks ratings", current: totalRows, done: true}
```

In `copyZacksMetrics`:
```go
progressCh <- progressMsg{step: "Zacks metrics", current: result.RowsAffected(), done: true}
```

In `copyZacksEstimates`:
```go
progressCh <- progressMsg{step: "Zacks estimates", current: totalRows, done: true}
```

In `copyZacksConsensus`:
```go
progressCh <- progressMsg{step: "Zacks consensus", current: result.RowsAffected(), done: true}
```

- [ ] **Step 4: Update runMigrateLegacy to launch bubbletea program**

Replace the direct `executeMigration` call (line 90) with:

```go
	// Run the migration with progress UI
	progressCh := make(chan progressMsg, 100)

	go func() {
		defer close(progressCh)

		err := executeMigration(ctx, pool, myLibrary, progressCh)

		progressCh <- migrationDoneMsg{err: err}
	}()

	model := newMigrationModel(progressCh)
	p := tea.NewProgram(model)

	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	// Check if migration had an error
	if m, ok := finalModel.(migrationModel); ok && m.err != nil {
		return m.err
	}

	return nil
```

Wait -- there's a problem. `migrationDoneMsg` is sent on `progressCh` which is `chan progressMsg`, but `migrationDoneMsg` is a different type. Fix: send the done message directly, and have `waitForProgress` detect channel close as the done signal. The goroutine should instead send the error through a separate mechanism.

Better approach: use a separate error channel or store the error on a shared variable. Simplest: use a closure variable.

```go
	// Run the migration with progress UI
	progressCh := make(chan progressMsg, 100)
	var migrationErr error

	go func() {
		defer close(progressCh)
		migrationErr = executeMigration(ctx, pool, myLibrary, progressCh)
	}()

	model := newMigrationModel(progressCh)
	p := tea.NewProgram(model)

	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return migrationErr
```

And update `waitForProgress` in `migrate_progress.go`: when the channel closes (`ok` is false), return `migrationDoneMsg{}` (which it already does).

Update `migrationDoneMsg` handling in the model to not check `msg.err` since the error comes from the closure. Actually, we can pass the error through the done message by having the goroutine send it before closing:

Actually, the simplest clean approach: the goroutine just closes the channel when done. The model detects channel close and quits. The error is returned via the closure variable `migrationErr`. The model's `migrationDoneMsg` handler just sets `m.done = true` and quits. The `m.err` field is not needed -- remove it from the model.

Update `migrate_progress.go` accordingly:
- Remove `err` field from `migrationModel`
- Remove `err` field from `migrationDoneMsg`
- In `View()`, remove the error display (the error will be printed by the cobra command)

- [ ] **Step 5: Add `tea "charm.land/bubbletea/v2"` import to migrate_legacy.go**

Add to the import block:
```go
tea "charm.land/bubbletea/v2"
```

- [ ] **Step 6: Verify it compiles**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...
```

- [ ] **Step 7: Commit**

```bash
git add cmd/migrate_legacy.go cmd/migrate_progress.go
git commit -m "feat: integrate progress bar into migrate-legacy command

Launch bubbletea program from runMigrateLegacy with migration running
in background goroutine. Send progressMsg after each table copy."
```

---

### Task 3: Batch EOD copy by year for progress updates

The EOD table is the largest and benefits most from year-based batching.

**Files:**
- Modify: `cmd/migrate_legacy.go`

- [ ] **Step 1: Replace the single EOD INSERT with year-based batching**

Replace the EOD copy block (the `if eodTable != ""` block) with:

```go
	// EOD -- batch by year for progress
	eodTable := subs.eod.DataTablesMap[data.EODKey]
	if eodTable != "" {
		var totalCount int64

		if err := tx.QueryRow(ctx,
			`SELECT COUNT(*) FROM legacy.eod WHERE LENGTH(TRIM(composite_figi)) = 12`).Scan(&totalCount); err != nil {
			return fmt.Errorf("count eod: %w", err)
		}

		var minYear, maxYear int

		if err := tx.QueryRow(ctx,
			`SELECT COALESCE(EXTRACT(YEAR FROM MIN(event_date))::int, 0), COALESCE(EXTRACT(YEAR FROM MAX(event_date))::int, 0) FROM legacy.eod`).Scan(&minYear, &maxYear); err != nil {
			return fmt.Errorf("eod date range: %w", err)
		}

		var copiedRows int64

		for year := minYear; year <= maxYear; year++ {
			result, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, open, high, low, close, adj_close, volume, dividend, split_factor)
SELECT ticker, TRIM(composite_figi), event_date, open, high, low, close, COALESCE(adj_close, close), volume, dividend, split_factor
FROM legacy.eod
WHERE LENGTH(TRIM(composite_figi)) = 12 AND event_date >= '%d-01-01' AND event_date < '%d-01-01'`, eodTable, year, year+1))
			if err != nil {
				return fmt.Errorf("copy eod year %d: %w", year, err)
			}

			copiedRows += result.RowsAffected()

			progressCh <- progressMsg{step: "EOD data", current: copiedRows, total: totalCount}
		}

		progressCh <- progressMsg{step: "EOD data", current: copiedRows, done: true}
	}
```

- [ ] **Step 2: Batch Zacks metrics by year**

Replace `copyZacksMetrics` body with year-based batching (same pattern as EOD):

```go
func copyZacksMetrics(ctx context.Context, tx pgx.Tx, sub *library.Subscription, progressCh chan<- progressMsg) error {
	tbl := sub.DataTablesMap[data.MetricKey]
	if tbl == "" {
		return nil
	}

	var totalCount int64

	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM legacy.zacks_financials WHERE LENGTH(TRIM(composite_figi)) = 12`).Scan(&totalCount); err != nil {
		return fmt.Errorf("count zacks metrics: %w", err)
	}

	var minYear, maxYear int

	if err := tx.QueryRow(ctx,
		`SELECT COALESCE(EXTRACT(YEAR FROM MIN(event_date))::int, 0), COALESCE(EXTRACT(YEAR FROM MAX(event_date))::int, 0) FROM legacy.zacks_financials`).Scan(&minYear, &maxYear); err != nil {
		return fmt.Errorf("zacks date range: %w", err)
	}

	var copiedRows int64

	for year := minYear; year <= maxYear; year++ {
		result, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, market_cap, ev, pe, pb, ps, ev_ebit, ev_ebitda, pe_forward, peg, price_to_cash_flow, beta)
SELECT ticker, TRIM(composite_figi), event_date,
  COALESCE(market_cap_mil, 0) * 1000000, 0,
  COALESCE(pe_trailing_12_months, 0), COALESCE(price_to_book, 0), COALESCE(price_to_sales, 0),
  0, 0, COALESCE(pe_f1, 0), COALESCE(peg_ratio, 0), COALESCE(price_to_cash_flow, 0), COALESCE(beta, 0)
FROM legacy.zacks_financials
WHERE LENGTH(TRIM(composite_figi)) = 12 AND event_date >= '%d-01-01' AND event_date < '%d-01-01'`, tbl, year, year+1))
		if err != nil {
			return fmt.Errorf("copy zacks metrics year %d: %w", year, err)
		}

		copiedRows += result.RowsAffected()

		progressCh <- progressMsg{step: "Zacks metrics", current: copiedRows, total: totalCount}
	}

	progressCh <- progressMsg{step: "Zacks metrics", current: copiedRows, done: true}

	return nil
}
```

- [ ] **Step 3: Verify it compiles**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...
```

- [ ] **Step 4: Run tests**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && go test ./...
```

- [ ] **Step 5: Run linter**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && golangci-lint run
```

- [ ] **Step 6: Commit**

```bash
git add cmd/migrate_legacy.go
git commit -m "feat: batch EOD and Zacks metrics copies by year for progress

Large table copies now iterate year-by-year, sending progress
updates after each batch for smooth progress bar animation."
```
