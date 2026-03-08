# Run Command TUI Overhaul - Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Replace the run command with a full bubbletea TUI featuring pre-flight config validation, interactive subscription selection, multi-pane dashboard with tabs, real-time progress, dual logging, and run history.

**Architecture:** Monolithic bubbletea application with tab-based sub-models. Pre-flight validation uses `huh` forms before launching the bubbletea app. A `RunManager` bridges existing provider `Fetch` functions with the TUI via event channels. Dual zerolog writer feeds both a log file and the TUI logs pane.

**Tech Stack:** Go, bubbletea, bubbles, lipgloss, huh, zerolog, pgx, gocron, viper

**Design doc:** `docs/plans/2026-03-07-run-tui-design.md`

---

### Task 1: Safety Fix - ActiveAssets Returns Error Instead of Panic

**Files:**
- Modify: `data/asset.go:88-138`
- Modify: `provider/tiingo.go:163` and `provider/tiingo.go:377`
- Modify: `provider/sharadar_metrics.go:75`
- Modify: `provider/sharadar_fundamentals.go:181`
- Modify: `provider/zacks.go:125`
- Modify: `figi/database.go:41-46`

**Step 1: Change ActiveAssets signature to return error**

In `data/asset.go`, change the function to return `([]*Asset, error)`:

```go
func ActiveAssets(ctx context.Context, dbConn *pgxpool.Conn, tables ...string) ([]*Asset, error) {
	var assetTable string
	if len(tables) == 0 {
		assetTable = viper.GetString("default.asset_table")
		if assetTable == "" {
			return nil, errors.New("default.asset_table not set, list of active assets is not possible")
		}
	} else {
		assetTable = tables[0]
	}

	sql := fmt.Sprintf(`SELECT
		ticker,
		composite_figi,
		share_class_figi,
		primary_exchange,
		asset_type,
		active,
		name,
		description,
		corporate_url,
		sector,
		industry,
		sic_code,
		cik,
		cusips,
		isins,
		other_identifiers,
		similar_tickers,
		tags,
		coalesce(to_char(listed, 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '') as listed,
		coalesce(to_char(delisted, 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '') as delisted,
		last_updated
	FROM %s
	WHERE active=true`, assetTable)

	rows, err := dbConn.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("query active assets from %s: %w", assetTable, err)
	}

	var dbActiveAssets []*Asset
	err = pgxscan.ScanAll(&dbActiveAssets, rows)
	if err != nil {
		return nil, fmt.Errorf("scan active assets: %w", err)
	}

	return dbActiveAssets, nil
}
```

Remove the `"github.com/rs/zerolog/log"` import from `data/asset.go` if it is no longer used (check for other usages in the file first; the `zerolog` import for `zerolog.Ctx` may still be needed but `log` likely is not).

**Step 2: Update provider/tiingo.go:163 (downloadTiingoEODQuotes)**

Change:
```go
assets := data.ActiveAssets(ctx, conn)
```
to:
```go
assets, err := data.ActiveAssets(ctx, conn)
if err != nil {
    logger.Error().Err(err).Msg("could not load active assets")
    runSummary.Status = data.RunFailed
    return
}
```

**Step 3: Update provider/tiingo.go:377 (downloadTiingoAssets)**

Change:
```go
activeDBAssets := data.ActiveAssets(ctx, conn, subscription.DataTablesMap[data.AssetKey])
```
to:
```go
activeDBAssets, err := data.ActiveAssets(ctx, conn, subscription.DataTablesMap[data.AssetKey])
if err != nil {
    logger.Error().Err(err).Msg("could not load active assets from database")
    return
}
```

**Step 4: Update provider/sharadar_metrics.go:75**

Change:
```go
assets := data.ActiveAssets(ctx, conn)
```
to:
```go
assets, err := data.ActiveAssets(ctx, conn)
if err != nil {
    logger.Error().Err(err).Msg("could not load active assets")
    runSummary.Status = data.RunFailed
    return
}
```

**Step 5: Update provider/sharadar_fundamentals.go:181**

Change:
```go
assets := data.ActiveAssets(ctx, conn)
```
to:
```go
assets, err := data.ActiveAssets(ctx, conn)
if err != nil {
    logger.Error().Err(err).Msg("could not load active assets")
    return ""
}
```

Note: `downloadSharadarFundamentals` returns a `string` (cursor), so the error return is `""`.

**Step 6: Update provider/zacks.go:125**

Change:
```go
assets := data.ActiveAssets(ctx, conn)
```
to:
```go
assets, err := data.ActiveAssets(ctx, conn)
if err != nil {
    logger.Error().Err(err).Msg("could not load active assets")
    runSummary.Status = data.RunFailed
    return
}
```

**Step 7: Update figi/database.go:41-46**

Change:
```go
func LoadCacheFromDB(ctx context.Context, dbConn *pgxpool.Conn) {
	assetTable := viper.GetString("default.asset_table")
	if assetTable == "" {
		log.Warn().Msg("default.asset_table not set, local figi lookup is disabled")
		return
	}

	sql := fmt.Sprintf("SELECT ticker, composite_figi FROM %s WHERE active=true", assetTable)
```

This function already handles the missing config gracefully (warning instead of panic), but it should use `ActiveAssets` for consistency. However, it only queries two columns, not the full asset set. Leave this as-is for now -- it already does the right thing.

**Step 8: Verify it compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`
Expected: successful compilation

**Step 9: Commit**

```bash
git add data/asset.go provider/tiingo.go provider/sharadar_metrics.go provider/sharadar_fundamentals.go provider/zacks.go
git commit -m "fix: return error from ActiveAssets instead of panicking"
```

---

### Task 2: Promote Bubbletea Dependencies to Direct

**Files:**
- Modify: `go.mod`

**Step 1: Add bubbletea and bubbles as direct dependencies**

Run:
```bash
cd /Users/jdf/Developer/penny-vault/pv-data
go get github.com/charmbracelet/bubbletea@latest
go get github.com/charmbracelet/bubbles@latest
```

**Step 2: Verify go.mod**

Run: `grep -E "bubbletea|bubbles" go.mod`
Expected: both listed without `// indirect`

**Step 3: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: promote bubbletea and bubbles to direct dependencies"
```

---

### Task 3: Shared Lipgloss Styles

**Files:**
- Create: `tui/styles.go`

**Step 1: Create the styles file**

```go
package tui

import "github.com/charmbracelet/lipgloss"

var (
	// Tab bar styles
	ActiveTabStyle = lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("229")).
		Background(lipgloss.Color("57")).
		Padding(0, 2)

	InactiveTabStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Padding(0, 2)

	TabGapStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("238"))

	// Status bar styles
	StatusBarStyle = lipgloss.NewStyle().
		Foreground(lipgloss.Color("229")).
		Background(lipgloss.Color("236")).
		Padding(0, 1)

	// Content area
	ContentStyle = lipgloss.NewStyle().
		Padding(1, 2)

	// Status indicators
	StatusIdle    = lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render("idle")
	StatusRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("running")
	StatusDone    = lipgloss.NewStyle().Foreground(lipgloss.Color("34")).Render("done")
	StatusError   = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("error")

	// Log level colors
	LogDebug = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	LogInfo  = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	LogWarn  = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	LogError = lipgloss.NewStyle().Foreground(lipgloss.Color("196"))

	// Help style
	HelpStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("241"))
)
```

**Step 2: Verify it compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`

**Step 3: Commit**

```bash
git add tui/styles.go
git commit -m "feat: add shared lipgloss styles for TUI"
```

---

### Task 4: Dual Log Writer

**Files:**
- Create: `tui/logwriter.go`

**Step 1: Create the dual log writer**

This writer sends log output to both a file and a channel that the TUI logs pane reads from.

```go
package tui

import (
	"os"
	"sync"
)

// LogEntry represents a single log line for the TUI.
type LogEntry struct {
	Line string
}

// DualWriter writes to both a file and a channel for TUI consumption.
type DualWriter struct {
	file    *os.File
	ch      chan LogEntry
	mu      sync.Mutex
}

// NewDualWriter creates a writer that outputs to the given file path and a channel.
// The channel is buffered to avoid blocking the logger.
func NewDualWriter(filePath string) (*DualWriter, error) {
	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return nil, err
	}

	return &DualWriter{
		file: f,
		ch:   make(chan LogEntry, 1000),
	}, nil
}

// Write implements io.Writer. It writes to both the file and the channel.
func (w *DualWriter) Write(p []byte) (n int, err error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	n, err = w.file.Write(p)
	if err != nil {
		return n, err
	}

	// Non-blocking send to channel
	select {
	case w.ch <- LogEntry{Line: string(p)}:
	default:
		// Drop the log entry if the channel is full
	}

	return n, nil
}

// LogChan returns the channel that TUI can read log entries from.
func (w *DualWriter) LogChan() <-chan LogEntry {
	return w.ch
}

// Close closes the file and channel.
func (w *DualWriter) Close() error {
	close(w.ch)
	return w.file.Close()
}
```

**Step 2: Verify it compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`

**Step 3: Commit**

```bash
git add tui/logwriter.go
git commit -m "feat: add dual log writer for file and TUI channel output"
```

---

### Task 5: RunManager and Event Types

**Files:**
- Create: `tui/runmanager.go`

**Step 1: Create RunManager with event types**

```go
package tui

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog/log"
)

type EventType int

const (
	EventStarted EventType = iota
	EventProgress
	EventCompleted
	EventFailed
)

type RunEvent struct {
	SubscriptionID   uuid.UUID
	SubscriptionName string
	Type             EventType
	RecordsCount     int
	Error            error
	Timestamp        time.Time
}

// SubscriptionStatus tracks the current state of a subscription run.
type SubscriptionStatus struct {
	Subscription *library.Subscription
	Status       EventType
	RecordsCount int
	StartTime    time.Time
	EndTime      time.Time
	Error        error
}

// RunManager orchestrates subscription execution and emits events for the TUI.
type RunManager struct {
	subscriptions []*library.Subscription
	myLibrary     *library.Library
	eventCh       chan RunEvent
	statuses      map[uuid.UUID]*SubscriptionStatus
	mu            sync.RWMutex
}

// NewRunManager creates a RunManager for the given subscriptions.
func NewRunManager(myLibrary *library.Library, subscriptions []*library.Subscription) *RunManager {
	statuses := make(map[uuid.UUID]*SubscriptionStatus, len(subscriptions))
	for _, sub := range subscriptions {
		statuses[sub.ID] = &SubscriptionStatus{
			Subscription: sub,
			Status:       EventStarted, // will be overwritten; using as "idle"
		}
	}

	return &RunManager{
		subscriptions: subscriptions,
		myLibrary:     myLibrary,
		eventCh:       make(chan RunEvent, 100),
		statuses:      statuses,
	}
}

// EventChan returns the channel the TUI reads events from.
func (rm *RunManager) EventChan() <-chan RunEvent {
	return rm.eventCh
}

// Statuses returns a snapshot of all subscription statuses.
func (rm *RunManager) Statuses() []*SubscriptionStatus {
	rm.mu.RLock()
	defer rm.mu.RUnlock()

	result := make([]*SubscriptionStatus, 0, len(rm.statuses))
	for _, s := range rm.statuses {
		result = append(result, s)
	}
	return result
}

// RunAll executes all subscriptions sequentially, emitting events.
// It uses the existing provider Fetch pattern with outChan/exitChan.
func (rm *RunManager) RunAll(ctx context.Context) {
	outChan := make(chan *data.Observation, 1000)
	exitChan := make(chan data.RunSummary, 5)

	var wg sync.WaitGroup
	wg.Add(1)
	go rm.myLibrary.SaveObservations(outChan, &wg)

	// Create a counting channel that wraps outChan to track progress
	countChan := make(chan *data.Observation, 1000)
	go rm.countObservations(countChan, outChan)

	for _, subscription := range rm.subscriptions {
		rm.emit(RunEvent{
			SubscriptionID:   subscription.ID,
			SubscriptionName: subscription.Name,
			Type:             EventStarted,
			Timestamp:        time.Now(),
		})

		rm.mu.Lock()
		rm.statuses[subscription.ID].Status = EventStarted
		rm.statuses[subscription.ID].StartTime = time.Now()
		rm.mu.Unlock()

		// create any needed partitions
		if err := subscription.ManagePartitions(ctx); err != nil {
			log.Error().Err(err).Msg("ManagePartitions returned an error")
		}

		// resolve provider and dataset
		subProvider, ok := provider.Map[subscription.Provider]
		if !ok {
			rm.emitFailed(subscription, "provider not found: "+subscription.Provider)
			continue
		}

		subDataset, ok := subProvider.Datasets()[subscription.Dataset]
		if !ok {
			rm.emitFailed(subscription, "dataset not found: "+subscription.Dataset)
			continue
		}

		fetchLogger := log.With().Str("SubscriptionID", subscription.ID.String()).Logger()
		fetchCtx := fetchLogger.WithContext(ctx)

		subDataset.Fetch(fetchCtx, subscription, countChan, exitChan)

		// read the exit message from exitChan
		summaryMsg := <-exitChan

		rm.mu.Lock()
		status := rm.statuses[subscription.ID]
		status.EndTime = summaryMsg.EndTime
		if summaryMsg.Status == data.RunFailed {
			status.Status = EventFailed
		} else {
			status.Status = EventCompleted
		}
		rm.mu.Unlock()

		eventType := EventCompleted
		if summaryMsg.Status == data.RunFailed {
			eventType = EventFailed
		}

		rm.emit(RunEvent{
			SubscriptionID:   subscription.ID,
			SubscriptionName: subscription.Name,
			Type:             eventType,
			RecordsCount:     summaryMsg.NumObservations,
			Timestamp:        time.Now(),
		})
	}

	close(countChan)
	wg.Wait()
	close(rm.eventCh)
}

func (rm *RunManager) countObservations(in <-chan *data.Observation, out chan<- *data.Observation) {
	for obs := range in {
		rm.mu.Lock()
		if s, ok := rm.statuses[obs.SubscriptionID]; ok {
			s.RecordsCount++
			count := s.RecordsCount
			rm.mu.Unlock()

			// Emit progress every 100 records to avoid flooding
			if count%100 == 0 {
				rm.emit(RunEvent{
					SubscriptionID:   obs.SubscriptionID,
					SubscriptionName: obs.SubscriptionName,
					Type:             EventProgress,
					RecordsCount:     count,
					Timestamp:        time.Now(),
				})
			}
		} else {
			rm.mu.Unlock()
		}

		out <- obs
	}
}

func (rm *RunManager) emit(event RunEvent) {
	select {
	case rm.eventCh <- event:
	default:
	}
}

func (rm *RunManager) emitFailed(sub *library.Subscription, msg string) {
	rm.mu.Lock()
	rm.statuses[sub.ID].Status = EventFailed
	rm.statuses[sub.ID].Error = fmt.Errorf("%s", msg)
	rm.mu.Unlock()

	rm.emit(RunEvent{
		SubscriptionID:   sub.ID,
		SubscriptionName: sub.Name,
		Type:             EventFailed,
		Error:            fmt.Errorf("%s", msg),
		Timestamp:        time.Now(),
	})
}
```

Note: Add `"fmt"` to the imports.

**Step 2: Verify it compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`

**Step 3: Commit**

```bash
git add tui/runmanager.go
git commit -m "feat: add RunManager for subscription execution with event streaming"
```

---

### Task 6: Pre-flight Validation

**Files:**
- Create: `tui/preflight.go`

**Step 1: Create preflight validation with huh forms**

```go
package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/huh"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pelletier/go-toml/v2"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

// PreflightResult holds the results of pre-flight validation.
type PreflightResult struct {
	Subscriptions []*library.Subscription
}

// RunPreflight validates configuration and prompts for missing values.
// Returns the list of subscriptions to run.
func RunPreflight(ctx context.Context, myLibrary *library.Library, subscriptionIDs []string) (*PreflightResult, error) {
	// Step 1: Validate database connection
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not connect to database: %w", err)
	}
	conn.Release()

	// Step 2: Validate default.asset_table
	if err := validateAssetTable(ctx, myLibrary.Pool); err != nil {
		return nil, err
	}

	// Step 3: Resolve subscriptions
	var subscriptions []*library.Subscription
	if len(subscriptionIDs) > 0 {
		// Load specified subscriptions
		for _, id := range subscriptionIDs {
			sub, err := myLibrary.SubscriptionFromID(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("could not load subscription %s: %w", id, err)
			}
			subscriptions = append(subscriptions, sub)
		}
	} else {
		// Interactive selection
		subscriptions, err = selectSubscriptions(ctx, myLibrary)
		if err != nil {
			return nil, err
		}
	}

	if len(subscriptions) == 0 {
		return nil, fmt.Errorf("no subscriptions selected")
	}

	return &PreflightResult{
		Subscriptions: subscriptions,
	}, nil
}

func validateAssetTable(ctx context.Context, pool *pgxpool.Pool) error {
	assetTable := viper.GetString("default.asset_table")
	if assetTable != "" {
		return nil
	}

	// Query available asset tables from subscriptions
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("could not acquire connection: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx,
		`SELECT DISTINCT unnest(data_tables)
		 FROM subscriptions
		 WHERE data_types @> ARRAY['asset-description']::datatype[]`)
	if err != nil {
		return fmt.Errorf("could not query asset tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return fmt.Errorf("could not scan asset table: %w", err)
		}
		tables = append(tables, table)
	}

	if len(tables) == 0 {
		return fmt.Errorf("no asset tables found; create an asset subscription first")
	}

	if len(tables) == 1 {
		// Only one option, use it automatically
		assetTable = tables[0]
		log.Info().Str("AssetTable", assetTable).Msg("auto-selected asset table")
	} else {
		// Present selection
		options := make([]huh.Option[string], len(tables))
		for i, t := range tables {
			options[i] = huh.NewOption(t, t)
		}

		form := huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title("Select the default asset table:").
					Description("This table is used to look up active assets for data providers.").
					Options(options...).
					Value(&assetTable),
			),
		)

		if err := form.Run(); err != nil {
			return fmt.Errorf("asset table selection cancelled: %w", err)
		}
	}

	// Save to viper and config file
	viper.Set("default.asset_table", assetTable)

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not determine home directory: %w", err)
	}

	configFN := filepath.Join(home, ".pvdata.toml")

	// Read existing config, merge, and write back
	existingData, err := os.ReadFile(configFN)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not read config file: %w", err)
	}

	configMap := make(map[string]any)
	if len(existingData) > 0 {
		if err := toml.Unmarshal(existingData, &configMap); err != nil {
			return fmt.Errorf("could not parse config file: %w", err)
		}
	}

	defaultSection, ok := configMap["default"].(map[string]any)
	if !ok {
		defaultSection = make(map[string]any)
	}
	defaultSection["asset_table"] = assetTable
	configMap["default"] = defaultSection

	newData, err := toml.Marshal(configMap)
	if err != nil {
		return fmt.Errorf("could not marshal config: %w", err)
	}

	if err := os.WriteFile(configFN, newData, 0644); err != nil {
		return fmt.Errorf("could not write config file: %w", err)
	}

	log.Info().Str("AssetTable", assetTable).Str("ConfigFile", configFN).Msg("saved default asset table to config")

	return nil
}

func selectSubscriptions(ctx context.Context, myLibrary *library.Library) ([]*library.Subscription, error) {
	allSubs, err := myLibrary.Subscriptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not load subscriptions: %w", err)
	}

	if len(allSubs) == 0 {
		return nil, fmt.Errorf("no subscriptions found; use 'pvdata subscribe' to create one")
	}

	options := make([]huh.Option[string], len(allSubs))
	for i, sub := range allSubs {
		label := fmt.Sprintf("%s (%s/%s)", sub.Name, sub.Provider, sub.Dataset)
		options[i] = huh.NewOption(label, sub.ID.String())
	}

	var selectedIDs []string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Select subscriptions to run:").
				Options(options...).
				Value(&selectedIDs),
		),
	)

	if err := form.Run(); err != nil {
		return nil, fmt.Errorf("subscription selection cancelled: %w", err)
	}

	// Map selected IDs back to subscription objects
	idMap := make(map[string]*library.Subscription, len(allSubs))
	for _, sub := range allSubs {
		idMap[sub.ID.String()] = sub
	}

	selected := make([]*library.Subscription, 0, len(selectedIDs))
	for _, id := range selectedIDs {
		if sub, ok := idMap[id]; ok {
			selected = append(selected, sub)
		}
	}

	return selected, nil
}
```

**Step 2: Verify it compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`

**Step 3: Commit**

```bash
git add tui/preflight.go
git commit -m "feat: add pre-flight validation with asset table selection and subscription picker"
```

---

### Task 7: Logs Tab Sub-model

**Files:**
- Create: `tui/logs.go`

**Step 1: Create the logs tab model**

Uses `bubbles/viewport` for scrollable content. Reads from the `DualWriter` channel.

```go
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// LogLineMsg is sent when a new log line arrives.
type LogLineMsg struct {
	Line string
}

// LogsModel is the sub-model for the Logs tab.
type LogsModel struct {
	viewport viewport.Model
	lines    []string
	maxLines int
	ready    bool
	logCh    <-chan LogEntry
}

func NewLogsModel(logCh <-chan LogEntry) LogsModel {
	return LogsModel{
		lines:    make([]string, 0, 1000),
		maxLines: 10000,
		logCh:    logCh,
	}
}

// WaitForLog returns a command that waits for the next log entry.
func (m LogsModel) WaitForLog() tea.Cmd {
	return func() tea.Msg {
		entry, ok := <-m.logCh
		if !ok {
			return nil
		}
		return LogLineMsg{Line: entry.Line}
	}
}

func (m LogsModel) Init() tea.Cmd {
	return m.WaitForLog()
}

func (m LogsModel) Update(msg tea.Msg) (LogsModel, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.viewport = viewport.New(msg.Width, msg.Height-4) // account for tabs + status bar
		m.viewport.SetContent(strings.Join(m.lines, ""))
		m.ready = true

	case LogLineMsg:
		m.lines = append(m.lines, m.colorize(msg.Line))
		if len(m.lines) > m.maxLines {
			m.lines = m.lines[len(m.lines)-m.maxLines:]
		}
		if m.ready {
			m.viewport.SetContent(strings.Join(m.lines, ""))
			m.viewport.GotoBottom()
		}
		cmds = append(cmds, m.WaitForLog())
	}

	if m.ready {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		cmds = append(cmds, cmd)
	}

	return m, tea.Batch(cmds...)
}

func (m LogsModel) View() string {
	if !m.ready {
		return "Initializing logs..."
	}
	return m.viewport.View()
}

func (m LogsModel) colorize(line string) string {
	// Simple colorization based on log level keywords
	trimmed := strings.TrimSpace(line)
	switch {
	case strings.Contains(trimmed, "ERR") || strings.Contains(trimmed, "error"):
		return LogError.Render(line)
	case strings.Contains(trimmed, "WRN") || strings.Contains(trimmed, "warn"):
		return LogWarn.Render(line)
	case strings.Contains(trimmed, "DBG") || strings.Contains(trimmed, "debug"):
		return LogDebug.Render(line)
	default:
		return LogInfo.Render(line)
	}
}
```

**Step 2: Verify it compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`

**Step 3: Commit**

```bash
git add tui/logs.go
git commit -m "feat: add logs tab sub-model with viewport and color-coded output"
```

---

### Task 8: Subscriptions Tab Sub-model

**Files:**
- Create: `tui/subscriptions.go`

**Step 1: Create the subscriptions tab model**

Renders a table of subscriptions with real-time status updates.

```go
package tui

import (
	"fmt"
	"time"

	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/google/uuid"
)

type SubscriptionsModel struct {
	table       table.Model
	statuses    map[uuid.UUID]*SubscriptionStatus
	expanded    uuid.UUID // ID of expanded subscription, zero if none
	width       int
	height      int
}

func NewSubscriptionsModel(statuses []*SubscriptionStatus) SubscriptionsModel {
	statusMap := make(map[uuid.UUID]*SubscriptionStatus, len(statuses))
	for _, s := range statuses {
		statusMap[s.Subscription.ID] = s
	}

	columns := []table.Column{
		{Title: "Name", Width: 25},
		{Title: "Provider/Dataset", Width: 25},
		{Title: "Status", Width: 10},
		{Title: "Records", Width: 10},
		{Title: "Last Run", Width: 20},
	}

	rows := buildRows(statuses)

	t := table.New(
		table.WithColumns(columns),
		table.WithRows(rows),
		table.WithFocused(true),
		table.WithHeight(10),
	)

	s := table.DefaultStyles()
	s.Header = s.Header.
		BorderStyle(lipgloss.NormalBorder()).
		BorderForeground(lipgloss.Color("240")).
		BorderBottom(true).
		Bold(true)
	s.Selected = s.Selected.
		Foreground(lipgloss.Color("229")).
		Background(lipgloss.Color("57")).
		Bold(false)
	t.SetStyles(s)

	return SubscriptionsModel{
		table:    t,
		statuses: statusMap,
	}
}

func (m SubscriptionsModel) Init() tea.Cmd {
	return nil
}

func (m SubscriptionsModel) Update(msg tea.Msg) (SubscriptionsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height - 4
		m.table.SetHeight(m.height)

	case RunEvent:
		if s, ok := m.statuses[msg.SubscriptionID]; ok {
			s.Status = msg.Type
			s.RecordsCount = msg.RecordsCount
			if msg.Error != nil {
				s.Error = msg.Error
			}
			if msg.Type == EventCompleted || msg.Type == EventFailed {
				s.EndTime = msg.Timestamp
			}
		}
		m.refreshRows()

	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			// Toggle expanded view for selected subscription
			row := m.table.SelectedRow()
			if row != nil {
				id := uuid.MustParse(row[0]) // hidden first column? No, use name lookup
				// For simplicity, skip expand for now; can be enhanced later
			}
		}
	}

	var cmd tea.Cmd
	m.table, cmd = m.table.Update(msg)
	return m, cmd
}

func (m SubscriptionsModel) View() string {
	return m.table.View()
}

func (m *SubscriptionsModel) refreshRows() {
	statuses := make([]*SubscriptionStatus, 0, len(m.statuses))
	for _, s := range m.statuses {
		statuses = append(statuses, s)
	}
	m.table.SetRows(buildRows(statuses))
}

func buildRows(statuses []*SubscriptionStatus) []table.Row {
	rows := make([]table.Row, 0, len(statuses))
	for _, s := range statuses {
		statusStr := "idle"
		switch s.Status {
		case EventStarted:
			statusStr = "running"
		case EventCompleted:
			statusStr = "done"
		case EventFailed:
			statusStr = "error"
		}

		lastRun := "-"
		if !s.Subscription.LastRun.IsZero() {
			lastRun = s.Subscription.LastRun.Format(time.RFC3339)
		}

		rows = append(rows, table.Row{
			s.Subscription.Name,
			fmt.Sprintf("%s/%s", s.Subscription.Provider, s.Subscription.Dataset),
			statusStr,
			fmt.Sprintf("%d", s.RecordsCount),
			lastRun,
		})
	}
	return rows
}
```

**Step 2: Verify it compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`

**Step 3: Commit**

```bash
git add tui/subscriptions.go
git commit -m "feat: add subscriptions tab with table view and real-time status"
```

---

### Task 9: History Tab Sub-model

**Files:**
- Create: `tui/history.go`

**Step 1: Create the history tab model**

Shows current session runs and persistent history from the database.

```go
package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/google/uuid"
	"github.com/penny-vault/pvdata/data"
)

// SessionRun tracks a completed run in the current session.
type SessionRun struct {
	SubscriptionID   uuid.UUID
	SubscriptionName string
	Status           EventType
	RecordsCount     int
	StartTime        time.Time
	EndTime          time.Time
}

type HistoryModel struct {
	viewport    viewport.Model
	sessionRuns []SessionRun
	ready       bool
}

func NewHistoryModel() HistoryModel {
	return HistoryModel{
		sessionRuns: make([]SessionRun, 0),
	}
}

func (m HistoryModel) Init() tea.Cmd {
	return nil
}

func (m HistoryModel) Update(msg tea.Msg) (HistoryModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.viewport = viewport.New(msg.Width, msg.Height-4)
		m.ready = true
		m.refreshContent()

	case RunEvent:
		if msg.Type == EventCompleted || msg.Type == EventFailed {
			m.sessionRuns = append(m.sessionRuns, SessionRun{
				SubscriptionID:   msg.SubscriptionID,
				SubscriptionName: msg.SubscriptionName,
				Status:           msg.Type,
				RecordsCount:     msg.RecordsCount,
				EndTime:          msg.Timestamp,
			})
			m.refreshContent()
		}
	}

	if m.ready {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m HistoryModel) View() string {
	if !m.ready {
		return "Initializing history..."
	}
	return m.viewport.View()
}

func (m *HistoryModel) refreshContent() {
	if !m.ready {
		return
	}

	var b strings.Builder

	b.WriteString("-- Current Session --\n\n")
	if len(m.sessionRuns) == 0 {
		b.WriteString("  No completed runs yet.\n")
	} else {
		for _, run := range m.sessionRuns {
			status := "done"
			if run.Status == EventFailed {
				status = "FAILED"
			}
			duration := run.EndTime.Sub(run.StartTime).Round(time.Second)
			b.WriteString(fmt.Sprintf("  %s  %-8s  %d records  %s\n",
				run.SubscriptionName, status, run.RecordsCount, duration))
		}
	}

	m.viewport.SetContent(b.String())
}
```

Note: Persistent DB history can be added later by querying the subscriptions table for `last_run`, `total_records`, etc. This keeps the initial implementation focused.

**Step 2: Verify it compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`

**Step 3: Commit**

```bash
git add tui/history.go
git commit -m "feat: add history tab with session run tracking"
```

---

### Task 10: Config Tab Sub-model

**Files:**
- Create: `tui/config.go`

**Step 1: Create the config tab model**

Read-only display of current configuration.

```go
package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/viper"
)

type ConfigModel struct {
	viewport viewport.Model
	ready    bool
}

func NewConfigModel() ConfigModel {
	return ConfigModel{}
}

func (m ConfigModel) Init() tea.Cmd {
	return nil
}

func (m ConfigModel) Update(msg tea.Msg) (ConfigModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.viewport = viewport.New(msg.Width, msg.Height-4)
		m.ready = true
		m.viewport.SetContent(m.renderConfig())
	}

	if m.ready {
		var cmd tea.Cmd
		m.viewport, cmd = m.viewport.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m ConfigModel) View() string {
	if !m.ready {
		return "Initializing config..."
	}
	return m.viewport.View()
}

func (m ConfigModel) renderConfig() string {
	var b strings.Builder

	b.WriteString("-- Configuration --\n\n")

	keys := []struct {
		label string
		key   string
	}{
		{"Database URL", "db.url"},
		{"Default Asset Table", "default.asset_table"},
		{"OpenFIGI API Key", "openfigi.apikey"},
		{"Web Port", "web.port"},
		{"Config File", ""},
	}

	for _, k := range keys {
		if k.key == "" {
			b.WriteString(fmt.Sprintf("  %-25s %s\n", k.label+":", viper.ConfigFileUsed()))
		} else {
			val := viper.GetString(k.key)
			if val == "" {
				val = "(not set)"
			}
			// Mask sensitive values
			if strings.Contains(k.key, "apikey") && val != "(not set)" {
				if len(val) > 4 {
					val = val[:4] + strings.Repeat("*", len(val)-4)
				}
			}
			if strings.Contains(k.key, "url") && val != "(not set)" {
				// Show URL but mask password if present
				val = maskDBPassword(val)
			}
			b.WriteString(fmt.Sprintf("  %-25s %s\n", k.label+":", val))
		}
	}

	return b.String()
}

func maskDBPassword(url string) string {
	// Simple masking: replace password portion of postgres://user:pass@host
	if idx := strings.Index(url, "://"); idx >= 0 {
		rest := url[idx+3:]
		if atIdx := strings.Index(rest, "@"); atIdx >= 0 {
			userPass := rest[:atIdx]
			if colonIdx := strings.Index(userPass, ":"); colonIdx >= 0 {
				return url[:idx+3] + userPass[:colonIdx+1] + "****" + "@" + rest[atIdx+1:]
			}
		}
	}
	return url
}
```

**Step 2: Verify it compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`

**Step 3: Commit**

```bash
git add tui/config.go
git commit -m "feat: add config tab with read-only configuration display"
```

---

### Task 11: Status Bar Sub-model

**Files:**
- Create: `tui/statusbar.go`

**Step 1: Create the status bar model**

Shows aggregate stats: subscriptions running, total records, elapsed time.

```go
package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// TickMsg is sent every second to update the status bar timer.
type TickMsg time.Time

type StatusBarModel struct {
	totalSubscriptions int
	runningCount       int
	completedCount     int
	failedCount        int
	totalRecords       int
	startTime          time.Time
	width              int
}

func NewStatusBarModel(totalSubscriptions int) StatusBarModel {
	return StatusBarModel{
		totalSubscriptions: totalSubscriptions,
		startTime:          time.Now(),
	}
}

func (m StatusBarModel) Init() tea.Cmd {
	return tickCmd()
}

func tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return TickMsg(t)
	})
}

func (m StatusBarModel) Update(msg tea.Msg) (StatusBarModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width

	case TickMsg:
		return m, tickCmd()

	case RunEvent:
		switch msg.Type {
		case EventStarted:
			m.runningCount++
		case EventCompleted:
			m.runningCount--
			m.completedCount++
			m.totalRecords += msg.RecordsCount
		case EventFailed:
			m.runningCount--
			m.failedCount++
		case EventProgress:
			// Records are counted on completion
		}
	}

	return m, nil
}

func (m StatusBarModel) View() string {
	elapsed := time.Since(m.startTime).Round(time.Second)

	status := fmt.Sprintf(" running %d/%d", m.runningCount, m.totalSubscriptions)
	if m.completedCount > 0 {
		status += fmt.Sprintf(" | done %d", m.completedCount)
	}
	if m.failedCount > 0 {
		status += fmt.Sprintf(" | failed %d", m.failedCount)
	}
	status += fmt.Sprintf(" | %d records | %s", m.totalRecords, elapsed)

	return StatusBarStyle.Width(m.width).Render(status)
}
```

**Step 2: Verify it compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`

**Step 3: Commit**

```bash
git add tui/statusbar.go
git commit -m "feat: add status bar with running counts and elapsed time"
```

---

### Task 12: Main Bubbletea Model with Tab Management

**Files:**
- Create: `tui/tui.go`

**Step 1: Create the main TUI model**

Composes all sub-models with tab-based navigation.

```go
package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/penny-vault/pvdata/library"
)

type tabID int

const (
	tabSubscriptions tabID = iota
	tabLogs
	tabHistory
	tabConfig
)

var tabNames = []string{"Subscriptions", "Logs", "History", "Config"}

type MainModel struct {
	activeTab     tabID
	subscriptions SubscriptionsModel
	logs          LogsModel
	history       HistoryModel
	config        ConfigModel
	statusBar     StatusBarModel
	runManager    *RunManager
	width         int
	height        int
	quitting      bool
}

func NewMainModel(
	myLibrary *library.Library,
	runManager *RunManager,
	logCh <-chan LogEntry,
) MainModel {
	statuses := runManager.Statuses()

	return MainModel{
		activeTab:     tabSubscriptions,
		subscriptions: NewSubscriptionsModel(statuses),
		logs:          NewLogsModel(logCh),
		history:       NewHistoryModel(),
		config:        NewConfigModel(),
		statusBar:     NewStatusBarModel(len(statuses)),
		runManager:    runManager,
	}
}

func (m MainModel) Init() tea.Cmd {
	return tea.Batch(
		m.logs.Init(),
		m.statusBar.Init(),
		m.listenForEvents(),
	)
}

// listenForEvents returns a command that waits for the next RunEvent.
func (m MainModel) listenForEvents() tea.Cmd {
	return func() tea.Msg {
		event, ok := <-m.runManager.EventChan()
		if !ok {
			return nil
		}
		return event
	}
}

func (m MainModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmds []tea.Cmd

	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q":
			m.quitting = true
			return m, tea.Quit
		case "tab":
			m.activeTab = (m.activeTab + 1) % tabID(len(tabNames))
			return m, nil
		case "shift+tab":
			m.activeTab = (m.activeTab - 1 + tabID(len(tabNames))) % tabID(len(tabNames))
			return m, nil
		}

	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case RunEvent:
		// Forward to all sub-models and re-listen
		cmds = append(cmds, m.listenForEvents())
	}

	// Update all sub-models
	var cmd tea.Cmd

	m.subscriptions, cmd = m.subscriptions.Update(msg)
	cmds = append(cmds, cmd)

	m.logs, cmd = m.logs.Update(msg)
	cmds = append(cmds, cmd)

	m.history, cmd = m.history.Update(msg)
	cmds = append(cmds, cmd)

	m.config, cmd = m.config.Update(msg)
	cmds = append(cmds, cmd)

	m.statusBar, cmd = m.statusBar.Update(msg)
	cmds = append(cmds, cmd)

	return m, tea.Batch(cmds...)
}

func (m MainModel) View() string {
	if m.quitting {
		return ""
	}

	var b strings.Builder

	// Tab bar
	b.WriteString(m.renderTabBar())
	b.WriteString("\n")

	// Active tab content
	switch m.activeTab {
	case tabSubscriptions:
		b.WriteString(ContentStyle.Render(m.subscriptions.View()))
	case tabLogs:
		b.WriteString(ContentStyle.Render(m.logs.View()))
	case tabHistory:
		b.WriteString(ContentStyle.Render(m.history.View()))
	case tabConfig:
		b.WriteString(ContentStyle.Render(m.config.View()))
	}

	b.WriteString("\n")

	// Status bar
	b.WriteString(m.statusBar.View())

	return b.String()
}

func (m MainModel) renderTabBar() string {
	var tabs []string
	for i, name := range tabNames {
		if tabID(i) == m.activeTab {
			tabs = append(tabs, ActiveTabStyle.Render(name))
		} else {
			tabs = append(tabs, InactiveTabStyle.Render(name))
		}
	}
	return lipgloss.JoinHorizontal(lipgloss.Top, tabs...)
}

// Run starts the bubbletea program and the RunManager concurrently.
func Run(ctx context.Context, myLibrary *library.Library, runManager *RunManager, logWriter *DualWriter) error {
	model := NewMainModel(myLibrary, runManager, logWriter.LogChan())

	// Start RunManager in background
	go runManager.RunAll(ctx)

	p := tea.NewProgram(model, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	return nil
}
```

**Step 2: Verify it compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`

**Step 3: Commit**

```bash
git add tui/tui.go
git commit -m "feat: add main bubbletea model with tab navigation and event routing"
```

---

### Task 13: Rewrite cmd/run.go

**Files:**
- Modify: `cmd/run.go`

**Step 1: Rewrite run.go to use pre-flight and TUI**

Replace the entire contents of `cmd/run.go` with:

```go
/*
Copyright 2024
*/
package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/tui"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// runCmd represents the run command
var runCmd = &cobra.Command{
	Use:   "run [subscription-id...]",
	Short: "Run data import subscriptions",
	Long: `The run sub-command executes subscriptions and saves the data they generate. If no
arguments are provided then run will present a subscription picker. If subscription IDs are
provided then each subscription will execute sequentially.`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		// load the library
		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not connect to library")
		}

		// Set up dual logging (file + TUI channel)
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal().Err(err).Msg("could not determine home directory")
		}
		logFile := filepath.Join(home, ".pvdata.log")
		logWriter, err := tui.NewDualWriter(logFile)
		if err != nil {
			log.Fatal().Err(err).Str("LogFile", logFile).Msg("could not create log writer")
		}
		defer logWriter.Close()

		// Configure zerolog to use the dual writer with console formatting
		consoleWriter := zerolog.ConsoleWriter{Out: logWriter}
		log.Logger = zerolog.New(consoleWriter).With().Timestamp().Logger()

		// Run pre-flight validation
		result, err := tui.RunPreflight(ctx, myLibrary, args)
		if err != nil {
			log.Fatal().Err(err).Msg("pre-flight validation failed")
		}

		// Create RunManager
		runManager := tui.NewRunManager(myLibrary, result.Subscriptions)

		// Launch TUI
		if err := tui.Run(ctx, myLibrary, runManager, logWriter); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
}
```

**Step 2: Remove old imports that are no longer needed**

The old `run.go` imported `sync`, `data`, `provider`, `web` -- those are now handled by `tui` package.

**Step 3: Verify it compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`

**Step 4: Commit**

```bash
git add cmd/run.go
git commit -m "feat: rewrite run command with pre-flight validation and bubbletea TUI"
```

---

### Task 14: Integration Testing

**Step 1: Manual test - missing asset table**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go run . run`

Expected: If `default.asset_table` is not set in `~/.pvdata.toml`, a `huh` select form should appear listing available asset tables from the database. Selecting one should save it to the config file and proceed.

**Step 2: Manual test - subscription selection**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go run . run`

Expected: With no subscription IDs, a `huh` multi-select should appear listing all subscriptions. After selection, the bubbletea TUI should launch with tabs.

**Step 3: Manual test - TUI navigation**

In the running TUI:
- Press `tab` to cycle through Subscriptions, Logs, History, Config tabs
- Press `shift+tab` to cycle backwards
- Press `q` to quit

**Step 4: Manual test - with subscription ID**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go run . run <subscription-id>`

Expected: Skips subscription selection, runs pre-flight, launches TUI, executes the specified subscription.

**Step 5: Fix any compilation or runtime issues found during testing**

**Step 6: Final commit**

```bash
git add -A
git commit -m "fix: address issues found during integration testing"
```

---

### Task 15: Daemon Mode with Gocron (follow-up)

This task extends the TUI to support daemon mode. It is intentionally left as a follow-up since the core TUI must work first.

**Files:**
- Modify: `tui/runmanager.go` -- add `RunDaemon(ctx)` method that uses gocron to schedule subscriptions
- Modify: `tui/tui.go` -- accept a `daemon bool` flag
- Modify: `cmd/run.go` -- add `--daemon` flag, choose between `RunAll` and `RunDaemon`

**Outline:**
- Add a `RunDaemon` method to `RunManager` that creates a gocron scheduler
- Each subscription's schedule (cron expression) is registered with gocron
- When a job fires, it emits `EventStarted` and runs `Fetch`
- The TUI stays open until `q` is pressed
- The status bar shows "daemon mode" indicator and next scheduled run time

This is deferred -- implement after Tasks 1-14 are verified working.

Plan complete and saved to `docs/plans/2026-03-07-run-tui-plan.md`. Two execution options:

**1. Subagent-Driven (this session)** - I dispatch a fresh subagent per task, review between tasks, fast iteration

**2. Parallel Session (separate)** - Open a new session with executing-plans, batch execution with checkpoints

Which approach?