# iShares Index Backfill Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add lookback-based backfill to the iShares index provider, with weight-change tracking and correct state reconstruction from snapshots + changelog.

**Architecture:** Extract an `indexMember` type that carries both FIGI and weight. Add `currentIndexMembers` to reconstruct true index state from the last snapshot plus subsequent changelog entries. Add `tradingDays` to query the DB for valid trading dates. Modify `diffSnapshots` to detect adds, removes, and significant weight changes. Modify `shouldTakeSnapshot` to accept a reference date. Wire backfill loop into `downloadSingleISharesETF` using `LookbackFromContext`.

**Tech Stack:** Go, Ginkgo v2 + Gomega, pgx/v5, resty/v2, zerolog

**Spec:** `docs/superpowers/specs/2026-03-30-ishares-backfill-design.md`

---

## File Map

- **Modify:** `provider/index_helpers.go` -- add `indexMember` type, `currentIndexMembers`, `tradingDays`; change `shouldTakeSnapshot` signature; change `diffSnapshots` to use `indexMember` and detect weight changes
- **Modify:** `provider/index_helpers_test.go` -- update all existing tests and add new tests for the above
- **Modify:** `provider/ishares.go` -- backfill loop in `downloadSingleISharesETF`, `asOfDate` URL parameter
- **Modify:** `provider/nasdaq.go:200-214` -- update calls to `diffSnapshots`, `shouldTakeSnapshot` for new signatures
- **Modify:** `provider/hooks_test.go` -- update `diffSnapshots` call for new signature

---

### Task 1: Add `indexMember` type and update `diffSnapshots` signature

**Files:**
- Modify: `provider/index_helpers.go:38-57`
- Modify: `provider/index_helpers_test.go:56-89`

- [ ] **Step 1: Write failing tests for updated `diffSnapshots`**

In `provider/index_helpers_test.go`, replace the existing `diffSnapshots` Describe block with:

```go
var _ = Describe("diffSnapshots", func() {
	It("returns all as added when previous is empty", func() {
		current := map[string]indexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
			"MSFT": {CompositeFigi: "BBG000BPH459", Weight: 0.04},
		}
		adds, removes, weightChanges := diffSnapshots(current, map[string]indexMember{})
		Expect(adds).To(HaveLen(2))
		Expect(removes).To(BeEmpty())
		Expect(weightChanges).To(BeEmpty())
	})

	It("returns all as removed when current is empty", func() {
		previous := map[string]indexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
		}
		adds, removes, weightChanges := diffSnapshots(map[string]indexMember{}, previous)
		Expect(adds).To(BeEmpty())
		Expect(removes).To(HaveLen(1))
		Expect(removes).To(HaveKey("AAPL"))
		Expect(weightChanges).To(BeEmpty())
	})

	It("detects additions and removals", func() {
		current := map[string]indexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
			"GOOG": {CompositeFigi: "BBG009S39JX6", Weight: 0.03},
		}
		previous := map[string]indexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
			"MSFT": {CompositeFigi: "BBG000BPH459", Weight: 0.04},
		}
		adds, removes, weightChanges := diffSnapshots(current, previous)
		Expect(adds).To(HaveLen(1))
		Expect(adds).To(HaveKey("GOOG"))
		Expect(removes).To(HaveLen(1))
		Expect(removes).To(HaveKey("MSFT"))
		Expect(weightChanges).To(BeEmpty())
	})

	It("returns empty when sets are identical", func() {
		current := map[string]indexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
		}
		previous := map[string]indexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
		}
		adds, removes, weightChanges := diffSnapshots(current, previous)
		Expect(adds).To(BeEmpty())
		Expect(removes).To(BeEmpty())
		Expect(weightChanges).To(BeEmpty())
	})

	It("detects significant weight change above 0.01 threshold", func() {
		current := map[string]indexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.065},
		}
		previous := map[string]indexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
		}
		adds, removes, weightChanges := diffSnapshots(current, previous)
		Expect(adds).To(BeEmpty())
		Expect(removes).To(BeEmpty())
		Expect(weightChanges).To(HaveLen(1))
		Expect(weightChanges).To(HaveKey("AAPL"))
		Expect(weightChanges["AAPL"].Weight).To(BeNumerically("~", 0.065, 0.0001))
	})

	It("ignores weight change below 0.01 threshold", func() {
		current := map[string]indexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.055},
		}
		previous := map[string]indexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
		}
		adds, removes, weightChanges := diffSnapshots(current, previous)
		Expect(adds).To(BeEmpty())
		Expect(removes).To(BeEmpty())
		Expect(weightChanges).To(BeEmpty())
	})

	It("detects weight change exactly at 0.01 boundary", func() {
		current := map[string]indexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.06},
		}
		previous := map[string]indexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
		}
		adds, removes, weightChanges := diffSnapshots(current, previous)
		Expect(weightChanges).To(HaveLen(1))
	})

	It("handles simultaneous adds, removes, and weight changes", func() {
		current := map[string]indexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.08},
			"GOOG": {CompositeFigi: "BBG009S39JX6", Weight: 0.03},
		}
		previous := map[string]indexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
			"MSFT": {CompositeFigi: "BBG000BPH459", Weight: 0.04},
		}
		adds, removes, weightChanges := diffSnapshots(current, previous)
		Expect(adds).To(HaveLen(1))
		Expect(adds).To(HaveKey("GOOG"))
		Expect(removes).To(HaveLen(1))
		Expect(removes).To(HaveKey("MSFT"))
		Expect(weightChanges).To(HaveLen(1))
		Expect(weightChanges).To(HaveKey("AAPL"))
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data/.worktrees/feature-backfill && ginkgo run -race --focus "diffSnapshots" ./provider/`
Expected: compilation errors -- `indexMember` type and 3-return `diffSnapshots` don't exist yet.

- [ ] **Step 3: Implement `indexMember` type and updated `diffSnapshots`**

In `provider/index_helpers.go`, add the `indexMember` type after the imports and before `shouldTakeSnapshot`:

```go
// indexMember represents a constituent of an index with its FIGI and weight.
type indexMember struct {
	CompositeFigi string
	Weight        float64
}

const weightChangeThreshold = 0.01
```

Replace the existing `diffSnapshots` function (lines 38-57) with:

```go
// diffSnapshots compares current holdings against previous holdings
// and returns maps of added, removed, and weight-changed tickers.
// A weight change is significant when the absolute difference exceeds weightChangeThreshold.
func diffSnapshots(current, previous map[string]indexMember) (added, removed, weightChanged map[string]indexMember) {
	added = make(map[string]indexMember)
	removed = make(map[string]indexMember)
	weightChanged = make(map[string]indexMember)

	for ticker, member := range current {
		prev, ok := previous[ticker]
		if !ok {
			added[ticker] = member
			continue
		}

		delta := member.Weight - prev.Weight
		if delta < 0 {
			delta = -delta
		}

		if delta >= weightChangeThreshold {
			weightChanged[ticker] = member
		}
	}

	for ticker, member := range previous {
		if _, ok := current[ticker]; !ok {
			removed[ticker] = member
		}
	}

	return
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data/.worktrees/feature-backfill && ginkgo run -race --focus "diffSnapshots" ./provider/`
Expected: all 8 diffSnapshots specs PASS.

- [ ] **Step 5: Commit**

```bash
git add provider/index_helpers.go provider/index_helpers_test.go
git commit -m "feat: add indexMember type and weight-change detection to diffSnapshots"
```

---

### Task 2: Update `shouldTakeSnapshot` to accept a reference date

**Files:**
- Modify: `provider/index_helpers.go:15-36`
- Modify: `provider/index_helpers_test.go:10-54`

- [ ] **Step 1: Update tests for new `shouldTakeSnapshot` signature**

In `provider/index_helpers_test.go`, replace the existing `shouldTakeSnapshot` Describe block with:

```go
var _ = Describe("shouldTakeSnapshot", func() {
	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)

	It("returns true when no previous snapshot exists", func() {
		Expect(shouldTakeSnapshot(time.Time{}, now, "weekly")).To(BeTrue())
	})

	It("returns true when daily and last snapshot was yesterday", func() {
		yesterday := now.AddDate(0, 0, -1)
		Expect(shouldTakeSnapshot(yesterday, now, "daily")).To(BeTrue())
	})

	It("returns false when daily and last snapshot was today", func() {
		Expect(shouldTakeSnapshot(now, now, "daily")).To(BeFalse())
	})

	It("returns true when weekly and last snapshot was 8 days ago", func() {
		eightDaysAgo := now.AddDate(0, 0, -8)
		Expect(shouldTakeSnapshot(eightDaysAgo, now, "weekly")).To(BeTrue())
	})

	It("returns false when weekly and last snapshot was 3 days ago", func() {
		threeDaysAgo := now.AddDate(0, 0, -3)
		Expect(shouldTakeSnapshot(threeDaysAgo, now, "weekly")).To(BeFalse())
	})

	It("returns true when monthly and last snapshot was 32 days ago", func() {
		thirtyTwoDaysAgo := now.AddDate(0, 0, -32)
		Expect(shouldTakeSnapshot(thirtyTwoDaysAgo, now, "monthly")).To(BeTrue())
	})

	It("returns false when monthly and last snapshot was 15 days ago", func() {
		fifteenDaysAgo := now.AddDate(0, 0, -15)
		Expect(shouldTakeSnapshot(fifteenDaysAgo, now, "monthly")).To(BeFalse())
	})

	It("returns true when quarterly and last snapshot was 95 days ago", func() {
		ninetyFiveDaysAgo := now.AddDate(0, 0, -95)
		Expect(shouldTakeSnapshot(ninetyFiveDaysAgo, now, "quarterly")).To(BeTrue())
	})

	It("defaults to weekly for unknown frequency", func() {
		eightDaysAgo := now.AddDate(0, 0, -8)
		Expect(shouldTakeSnapshot(eightDaysAgo, now, "bogus")).To(BeTrue())
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data/.worktrees/feature-backfill && ginkgo run -race --focus "shouldTakeSnapshot" ./provider/`
Expected: compilation error -- `shouldTakeSnapshot` takes 2 args, not 3.

- [ ] **Step 3: Update `shouldTakeSnapshot` implementation**

In `provider/index_helpers.go`, replace the existing `shouldTakeSnapshot` function (lines 13-36) with:

```go
// shouldTakeSnapshot returns true if a new snapshot should be taken based on the
// configured frequency, the date of the last snapshot, and the current processing date.
func shouldTakeSnapshot(lastSnapshotDate, currentDate time.Time, frequency string) bool {
	if lastSnapshotDate.IsZero() {
		return true
	}

	var interval time.Duration

	switch frequency {
	case "daily":
		interval = 24 * time.Hour
	case "weekly":
		interval = 7 * 24 * time.Hour
	case "monthly":
		interval = 30 * 24 * time.Hour
	case "quarterly":
		interval = 90 * 24 * time.Hour
	default:
		interval = 7 * 24 * time.Hour
	}

	return currentDate.Sub(lastSnapshotDate) >= interval
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data/.worktrees/feature-backfill && ginkgo run -race --focus "shouldTakeSnapshot" ./provider/`
Expected: all 9 shouldTakeSnapshot specs PASS.

- [ ] **Step 5: Commit**

```bash
git add provider/index_helpers.go provider/index_helpers_test.go
git commit -m "refactor: add currentDate parameter to shouldTakeSnapshot"
```

---

### Task 3: Update callers of `diffSnapshots` and `shouldTakeSnapshot`

This task fixes compilation for all callers that use the old signatures: `ishares.go`, `nasdaq.go`, and `hooks_test.go`.

**Files:**
- Modify: `provider/ishares.go:240-269`
- Modify: `provider/nasdaq.go:190-214`
- Modify: `provider/hooks_test.go`

- [ ] **Step 1: Update `ishares.go` callers**

In `provider/ishares.go`, replace lines 240-269 (from "Build current holdings map" through the `shouldTakeSnapshot` call) with:

```go
	// Build current holdings map (ticker -> indexMember) and weight map
	currentHoldings := make(map[string]indexMember, len(parseResult.Holdings))
	weightMap := make(map[string]float64, len(parseResult.Holdings))
	for _, holding := range parseResult.Holdings {
		figi := figiMap[holding.Ticker]
		if figi != "" {
			currentHoldings[holding.Ticker] = indexMember{
				CompositeFigi: figi,
				Weight:        holding.Weight,
			}
		}
		weightMap[holding.Ticker] = holding.Weight
	}

	// Get previous snapshot and emit changelog
	table := subscription.DataTablesMap[data.IndexKey]
	previous := previousSnapshotTickers(ctx, subscription.Library.Pool, table, etf.IndexName)
	previousMembers := make(map[string]indexMember, len(previous))
	for t, figi := range previous {
		previousMembers[t] = indexMember{CompositeFigi: figi}
	}
	added, removed, _ := diffSnapshots(currentHoldings, previousMembers)

	eventDate := parseResult.SnapshotDate
	if eventDate.IsZero() {
		eventDate = time.Now().UTC().Truncate(24 * time.Hour)
	}

	addedFigi := make(map[string]string, len(added))
	for t, m := range added {
		addedFigi[t] = m.CompositeFigi
	}
	removedFigi := make(map[string]string, len(removed))
	for t, m := range removed {
		removedFigi[t] = m.CompositeFigi
	}

	emitChangelog(addedFigi, removedFigi, etf.IndexName, eventDate, weightMap, &data.Observation{
		ObservationDate:  time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}, out)
	numObs += len(added) + len(removed)

	// Check if a snapshot should be taken
	lastDate := lastSnapshotDate(ctx, subscription.Library.Pool, table, etf.IndexName)
	if shouldTakeSnapshot(lastDate, eventDate, snapshotFrequency) {
```

Also update the snapshot emission block (lines 271-297) to use `currentHoldings` map:

```go
		for t, member := range currentHoldings {
			out <- &data.Observation{
				IndexSnapshot: &data.IndexSnapshot{
					Ticker:        t,
					CompositeFigi: member.CompositeFigi,
					IndexName:     etf.IndexName,
					SnapshotDate:  eventDate,
					Weight:        member.Weight,
				},
				ObservationDate:  time.Now(),
				SubscriptionID:   subscription.ID,
				SubscriptionName: subscription.Name,
			}

			numObs++
		}

		logger.Info().
			Int("NumSnapshots", len(currentHoldings)).
			Str("IndexName", etf.IndexName).
			Time("SnapshotDate", eventDate).
			Msg("emitted index snapshots")
	}

	return numObs, nil
}
```

- [ ] **Step 2: Update `nasdaq.go` callers**

In `provider/nasdaq.go`, replace lines 190-214 (from "Build current holdings map" through the `shouldTakeSnapshot` call) with:

```go
	// Build current holdings map (ticker -> indexMember)
	currentHoldings := make(map[string]indexMember, len(holdings))
	for _, holding := range holdings {
		if figi, ok := figiMap[holding.Ticker]; ok {
			currentHoldings[holding.Ticker] = indexMember{CompositeFigi: figi}
		}
	}

	// Get previous snapshot and emit changelog
	table := subscription.DataTablesMap[data.IndexKey]
	previous := previousSnapshotTickers(ctx, subscription.Library.Pool, table, "ndx100")
	previousMembers := make(map[string]indexMember, len(previous))
	for t, figi := range previous {
		previousMembers[t] = indexMember{CompositeFigi: figi}
	}
	added, removed, _ := diffSnapshots(currentHoldings, previousMembers)

	eventDate := time.Now().UTC().Truncate(24 * time.Hour)

	addedFigi := make(map[string]string, len(added))
	for t, m := range added {
		addedFigi[t] = m.CompositeFigi
	}
	removedFigi := make(map[string]string, len(removed))
	for t, m := range removed {
		removedFigi[t] = m.CompositeFigi
	}

	emitChangelog(addedFigi, removedFigi, "ndx100", eventDate, nil, &data.Observation{
		ObservationDate:  time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}, out)
	numObs += len(added) + len(removed)

	// Check if a snapshot should be taken
	lastDate := lastSnapshotDate(ctx, subscription.Library.Pool, table, "ndx100")
	if shouldTakeSnapshot(lastDate, eventDate, snapshotFrequency) {
```

Also update the snapshot loop (lines 222-238) to use `currentHoldings`:

```go
		// Build a weight map for quick lookup
		weightMap := make(map[string]float64, len(holdings))
		for _, holding := range holdings {
			weightMap[holding.Ticker] = holding.Weight
		}

		snapshotDate := eventDate

		for ticker, member := range currentHoldings {
			out <- &data.Observation{
				IndexSnapshot: &data.IndexSnapshot{
					Ticker:        ticker,
					CompositeFigi: member.CompositeFigi,
					IndexName:     "ndx100",
					SnapshotDate:  snapshotDate,
					Weight:        weightMap[ticker],
				},
				ObservationDate:  time.Now(),
				SubscriptionID:   subscription.ID,
				SubscriptionName: subscription.Name,
			}

			numObs++
		}
```

- [ ] **Step 3: Update `hooks_test.go`**

In `provider/hooks_test.go`, the `ComputeAdjustedClose` tests do not call `diffSnapshots` or `shouldTakeSnapshot`, so no changes are needed. Verify by reading the file.

If any other test files reference the old signatures, update them.

- [ ] **Step 4: Run full provider test suite**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data/.worktrees/feature-backfill && ginkgo run -race ./provider/`
Expected: all specs PASS, no compilation errors.

- [ ] **Step 5: Commit**

```bash
git add provider/ishares.go provider/nasdaq.go
git commit -m "refactor: update ishares and nasdaq to use indexMember and new signatures"
```

---

### Task 4: Add `tradingDays` helper

**Files:**
- Modify: `provider/index_helpers.go`
- Modify: `provider/index_helpers_test.go`

- [ ] **Step 1: Write failing test for `tradingDays`**

This function calls the DB's `trading_days(DATE, DATE)` SQL function. Since unit tests don't have a DB connection, write a test that validates the function exists and returns an error on nil pool. Add to `provider/index_helpers_test.go`:

```go
var _ = Describe("tradingDays", func() {
	It("returns an error when pool is nil", func() {
		start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
		end := time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC)
		_, err := tradingDays(context.Background(), nil, start, end)
		Expect(err).To(HaveOccurred())
	})
})
```

Add `"context"` to the imports at the top of the test file.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data/.worktrees/feature-backfill && ginkgo run -race --focus "tradingDays" ./provider/`
Expected: compilation error -- `tradingDays` not defined.

- [ ] **Step 3: Implement `tradingDays`**

In `provider/index_helpers.go`, add after the `previousSnapshotTickers` function:

```go
// tradingDays returns NYSE trading days between start and end (inclusive)
// by calling the database's trading_days(DATE, DATE) function.
func tradingDays(ctx context.Context, pool *pgxpool.Pool, start, end time.Time) ([]time.Time, error) {
	if pool == nil {
		return nil, fmt.Errorf("database pool is nil")
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not acquire db connection for tradingDays: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx, `SELECT dt FROM trading_days($1::date, $2::date) AS dt ORDER BY dt`, start, end)
	if err != nil {
		return nil, fmt.Errorf("could not query trading days: %w", err)
	}
	defer rows.Close()

	var days []time.Time

	for rows.Next() {
		var dt time.Time
		if err := rows.Scan(&dt); err != nil {
			return nil, fmt.Errorf("error scanning trading day: %w", err)
		}

		days = append(days, dt)
	}

	return days, nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data/.worktrees/feature-backfill && ginkgo run -race --focus "tradingDays" ./provider/`
Expected: 1 spec PASS.

- [ ] **Step 5: Commit**

```bash
git add provider/index_helpers.go provider/index_helpers_test.go
git commit -m "feat: add tradingDays helper to query NYSE trading days from DB"
```

---

### Task 5: Add `currentIndexMembers` helper

**Files:**
- Modify: `provider/index_helpers.go`
- Modify: `provider/index_helpers_test.go`

- [ ] **Step 1: Write failing test for `currentIndexMembers`**

Add to `provider/index_helpers_test.go`:

```go
var _ = Describe("currentIndexMembers", func() {
	It("returns empty map when pool is nil", func() {
		asOf := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
		result := currentIndexMembers(context.Background(), nil, "test_table", "sp500", asOf)
		Expect(result).To(BeEmpty())
	})
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data/.worktrees/feature-backfill && ginkgo run -race --focus "currentIndexMembers" ./provider/`
Expected: compilation error -- `currentIndexMembers` not defined.

- [ ] **Step 3: Implement `currentIndexMembers`**

In `provider/index_helpers.go`, add after the `tradingDays` function:

```go
// currentIndexMembers reconstructs the true index membership as of a given date
// by taking the most recent snapshot on or before asOfDate and applying all
// changelog entries (adds, removes, weight-changes) between that snapshot and asOfDate.
func currentIndexMembers(ctx context.Context, pool *pgxpool.Pool, table, indexName string, asOfDate time.Time) map[string]indexMember {
	if pool == nil {
		return map[string]indexMember{}
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire db connection for currentIndexMembers")
		return map[string]indexMember{}
	}
	defer conn.Release()

	// Get the most recent snapshot on or before asOfDate
	var snapshotDate time.Time

	err = conn.QueryRow(ctx,
		fmt.Sprintf(`SELECT COALESCE(MAX(snapshot_date), '0001-01-01') FROM %s_snapshot WHERE index_name = $1 AND snapshot_date <= $2`, table),
		indexName, asOfDate,
	).Scan(&snapshotDate)
	if err != nil {
		log.Error().Err(err).Msg("could not query snapshot date for currentIndexMembers")
		return map[string]indexMember{}
	}

	result := make(map[string]indexMember)

	if !snapshotDate.IsZero() {
		// Load the snapshot
		rows, err := conn.Query(ctx,
			fmt.Sprintf(`SELECT ticker, composite_figi, weight FROM %s_snapshot WHERE index_name = $1 AND snapshot_date = $2`, table),
			indexName, snapshotDate,
		)
		if err != nil {
			log.Error().Err(err).Msg("could not query snapshot for currentIndexMembers")
			return map[string]indexMember{}
		}
		defer rows.Close()

		for rows.Next() {
			var ticker, figi string
			var weight float64

			if err := rows.Scan(&ticker, &figi, &weight); err != nil {
				log.Error().Err(err).Msg("error scanning snapshot row in currentIndexMembers")
				continue
			}

			result[ticker] = indexMember{CompositeFigi: figi, Weight: weight}
		}

		rows.Close()
	}

	// Apply changelog entries after the snapshot date up to asOfDate
	changeRows, err := conn.Query(ctx,
		fmt.Sprintf(`SELECT ticker, composite_figi, action, weight FROM %s_changelog WHERE index_name = $1 AND event_date > $2 AND event_date <= $3 ORDER BY event_date`, table),
		indexName, snapshotDate, asOfDate,
	)
	if err != nil {
		log.Error().Err(err).Msg("could not query changelog for currentIndexMembers")
		return result
	}
	defer changeRows.Close()

	for changeRows.Next() {
		var ticker, figi, action string
		var weight float64

		if err := changeRows.Scan(&ticker, &figi, &action, &weight); err != nil {
			log.Error().Err(err).Msg("error scanning changelog row in currentIndexMembers")
			continue
		}

		switch action {
		case "add":
			result[ticker] = indexMember{CompositeFigi: figi, Weight: weight}
		case "remove":
			delete(result, ticker)
		case "weight-change":
			if member, ok := result[ticker]; ok {
				member.Weight = weight
				result[ticker] = member
			}
		}
	}

	return result
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data/.worktrees/feature-backfill && ginkgo run -race --focus "currentIndexMembers" ./provider/`
Expected: 1 spec PASS.

- [ ] **Step 5: Commit**

```bash
git add provider/index_helpers.go provider/index_helpers_test.go
git commit -m "feat: add currentIndexMembers to reconstruct index state from snapshot + changelog"
```

---

### Task 6: Add `emitWeightChanges` helper

**Files:**
- Modify: `provider/index_helpers.go`

- [ ] **Step 1: Add `emitWeightChanges` function**

In `provider/index_helpers.go`, add after the existing `emitChangelog` function:

```go
// emitWeightChanges emits IndexChange observations with "weight-change" action.
func emitWeightChanges(changes map[string]indexMember, indexName string, eventDate time.Time, subscription *data.Observation, out chan<- *data.Observation) {
	for ticker, member := range changes {
		out <- &data.Observation{
			IndexChange: &data.IndexChange{
				Ticker:        ticker,
				CompositeFigi: member.CompositeFigi,
				IndexName:     indexName,
				EventDate:     eventDate,
				Action:        "weight-change",
				Weight:        member.Weight,
			},
			ObservationDate:  subscription.ObservationDate,
			SubscriptionID:   subscription.SubscriptionID,
			SubscriptionName: subscription.SubscriptionName,
		}
	}
}
```

- [ ] **Step 2: Run full suite to verify compilation**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data/.worktrees/feature-backfill && ginkgo run -race ./provider/`
Expected: all specs PASS.

- [ ] **Step 3: Commit**

```bash
git add provider/index_helpers.go
git commit -m "feat: add emitWeightChanges helper for weight-change changelog entries"
```

---

### Task 7: Wire up backfill loop in `downloadSingleISharesETF`

This is the core change. The function gains a backfill loop that fetches historical CSVs using `asOfDate`, diffs against in-memory state, and emits changelog + snapshot observations.

**Files:**
- Modify: `provider/ishares.go:90,194-301`

- [ ] **Step 1: Update URL template**

In `provider/ishares.go`, change the `iSharesHoldingsURLTemplate` constant (line 90) to remove the trailing parameter so we can conditionally append `asOfDate`:

The existing template already works -- we just append `&asOfDate=YYYYMMDD` when needed. No change to the constant is required.

- [ ] **Step 2: Rewrite `downloadSingleISharesETF` with backfill support**

Replace the entire `downloadSingleISharesETF` function (lines 194-301) with:

```go
func downloadSingleISharesETF(
	ctx context.Context,
	client *resty.Client,
	ticker string,
	etf iSharesETF,
	figiMap map[string]string,
	snapshotFrequency string,
	subscription *library.Subscription,
	out chan<- *data.Observation,
) (int, error) {
	logger := zerolog.Ctx(ctx)
	numObs := 0
	table := subscription.DataTablesMap[data.IndexKey]

	lookback := LookbackFromContext(ctx, 14*24*time.Hour)

	// Build the list of dates to fetch.
	// Always includes today (empty asOfDate = current data).
	type fetchDate struct {
		date     time.Time
		asOfDate string // empty means "today / no asOfDate param"
	}

	var dates []fetchDate

	startDate := time.Now().Add(-lookback)
	yesterday := time.Now().AddDate(0, 0, -1).Truncate(24 * time.Hour)

	if startDate.Before(yesterday) {
		days, err := tradingDays(ctx, subscription.Library.Pool, startDate, yesterday)
		if err != nil {
			logger.Warn().Err(err).Msg("could not query trading days, falling back to today only")
		} else {
			for _, d := range days {
				dates = append(dates, fetchDate{
					date:     d,
					asOfDate: d.Format("20060102"),
				})
			}
		}
	}

	// Always fetch today's data last (no asOfDate)
	dates = append(dates, fetchDate{
		date: time.Now().UTC().Truncate(24 * time.Hour),
	})

	// Load initial state from DB: last snapshot + changelog entries up to start
	state := currentIndexMembers(ctx, subscription.Library.Pool, table, etf.IndexName, startDate)
	memLastSnapshotDate := lastSnapshotDate(ctx, subscription.Library.Pool, table, etf.IndexName)

	obsTemplate := &data.Observation{
		ObservationDate:  time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}

	for i, fd := range dates {
		// Build URL
		csvURL := fmt.Sprintf(iSharesHoldingsURLTemplate, etf.ProductID, etf.Slug, ticker)
		if fd.asOfDate != "" {
			csvURL += "&asOfDate=" + fd.asOfDate
		}

		logger.Info().Str("URL", csvURL).Str("IndexName", etf.IndexName).Time("Date", fd.date).Msg("downloading iShares holdings CSV")

		resp, err := client.R().SetContext(ctx).Get(csvURL)
		if err != nil {
			logger.Error().Err(err).Str("IndexName", etf.IndexName).Time("Date", fd.date).Msg("HTTP request failed")
			continue
		}

		if resp.StatusCode() != 200 {
			logger.Error().Int("StatusCode", resp.StatusCode()).Str("IndexName", etf.IndexName).Time("Date", fd.date).Msg("HTTP error")
			continue
		}

		csvData := resp.Body()
		logger.Info().Int("Bytes", len(csvData)).Str("IndexName", etf.IndexName).Msg("downloaded iShares holdings CSV")

		parseResult, err := parseISharesCSV(csvData)
		if err != nil {
			logger.Error().Err(err).Str("IndexName", etf.IndexName).Time("Date", fd.date).Msg("could not parse CSV")
			continue
		}

		if len(parseResult.Holdings) == 0 {
			logger.Warn().Str("IndexName", etf.IndexName).Time("Date", fd.date).Msg("no holdings found")
			continue
		}

		logger.Info().
			Int("NumHoldings", len(parseResult.Holdings)).
			Time("SnapshotDate", parseResult.SnapshotDate).
			Str("IndexName", etf.IndexName).
			Msg("parsed iShares holdings")

		// Build current holdings map
		currentHoldings := make(map[string]indexMember, len(parseResult.Holdings))
		weightMap := make(map[string]float64, len(parseResult.Holdings))

		for _, holding := range parseResult.Holdings {
			figi := figiMap[holding.Ticker]
			if figi != "" {
				currentHoldings[holding.Ticker] = indexMember{
					CompositeFigi: figi,
					Weight:        holding.Weight,
				}
			}

			weightMap[holding.Ticker] = holding.Weight
		}

		// Diff against in-memory state
		added, removed, weightChanged := diffSnapshots(currentHoldings, state)

		eventDate := parseResult.SnapshotDate
		if eventDate.IsZero() {
			eventDate = fd.date
		}

		// Emit changelog: adds and removes
		addedFigi := make(map[string]string, len(added))
		for t, m := range added {
			addedFigi[t] = m.CompositeFigi
		}

		removedFigi := make(map[string]string, len(removed))
		for t, m := range removed {
			removedFigi[t] = m.CompositeFigi
		}

		emitChangelog(addedFigi, removedFigi, etf.IndexName, eventDate, weightMap, obsTemplate, out)
		numObs += len(added) + len(removed)

		// Emit weight changes
		emitWeightChanges(weightChanged, etf.IndexName, eventDate, obsTemplate, out)
		numObs += len(weightChanged)

		// Check if snapshot is due
		if shouldTakeSnapshot(memLastSnapshotDate, eventDate, snapshotFrequency) {
			for t, member := range currentHoldings {
				out <- &data.Observation{
					IndexSnapshot: &data.IndexSnapshot{
						Ticker:        t,
						CompositeFigi: member.CompositeFigi,
						IndexName:     etf.IndexName,
						SnapshotDate:  eventDate,
						Weight:        member.Weight,
					},
					ObservationDate:  obsTemplate.ObservationDate,
					SubscriptionID:   obsTemplate.SubscriptionID,
					SubscriptionName: obsTemplate.SubscriptionName,
				}

				numObs++
			}

			memLastSnapshotDate = eventDate

			logger.Info().
				Int("NumSnapshots", len(currentHoldings)).
				Str("IndexName", etf.IndexName).
				Time("SnapshotDate", eventDate).
				Msg("emitted index snapshots")
		}

		// Update in-memory state
		for t, m := range added {
			state[t] = m
		}

		for t := range removed {
			delete(state, t)
		}

		for t, m := range weightChanged {
			state[t] = m
		}

		// Rate-limit delay between requests (skip after last)
		if i < len(dates)-1 {
			delay := 5*time.Second + time.Duration(rand.IntN(41))*time.Second
			logger.Info().Dur("Delay", delay).Msg("waiting between iShares historical requests")
			time.Sleep(delay)
		}
	}

	return numObs, nil
}
```

- [ ] **Step 3: Run full test suite**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data/.worktrees/feature-backfill && ginkgo run -race ./provider/`
Expected: all specs PASS.

- [ ] **Step 4: Run full project test suite**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data/.worktrees/feature-backfill && ginkgo run -race ./...`
Expected: all suites PASS.

- [ ] **Step 5: Commit**

```bash
git add provider/ishares.go
git commit -m "feat: add lookback-based backfill to iShares index provider"
```

---

### Task 8: Run linter and fix issues

**Files:**
- Possibly: `provider/index_helpers.go`, `provider/ishares.go`, `provider/nasdaq.go`

- [ ] **Step 1: Run linter**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data/.worktrees/feature-backfill && make lint`
Expected: no errors. If there are errors, fix them.

- [ ] **Step 2: Run full test suite one final time**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data/.worktrees/feature-backfill && ginkgo run -race ./...`
Expected: all suites PASS, Test Suite Passed.

- [ ] **Step 3: Commit any lint fixes**

```bash
git add -A
git commit -m "style: fix lint issues from backfill implementation"
```

Only commit if there were lint fixes. Skip if clean.
