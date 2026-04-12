# Run Error Logging and Ticker/FIGI Filter Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix silent failures when subscription lookup fails, add `--ticker`/`--figi` flags to `pvdata run` for per-security debugging across all providers.

**Architecture:** Context-based filter propagation (same pattern as `LookbackKey`). A shared fuzzy-match helper provides "did you mean?" suggestions. Each provider checks the filter at the start of its Fetch function and either restricts its universe or skips with a log message.

**Tech Stack:** Go, zerolog, `github.com/adrg/strutil` (already a dependency, provides Jaro-Winkler), Ginkgo v2 + Gomega for tests.

---

## File Structure

| File | Responsibility |
|------|---------------|
| `provider/filter.go` | Context keys (`TickerFilterKey`, `FigiFilterKey`), `SecurityFilterFromContext()` helper |
| `provider/fuzzy.go` | `SuggestMatch()` helper using Jaro-Winkler |
| `provider/fuzzy_test.go` | Tests for SuggestMatch |
| `provider/filter_test.go` | Tests for SecurityFilterFromContext |
| `library/database.go` | Add name-based fallback to `SubscriptionFromID` |
| `cmd/run.go` | Add `--ticker`/`--figi` flags, fix stderr error output |
| `provider/sec/sec.go` | Filter cikMap before backfill/incremental |
| `provider/tiingo/tiingo.go` | Filter assets list in `downloadTiingoEODQuotes` |
| `provider/sharadar/sharadar_fundamentals.go` | Filter figiMap before API calls |
| `provider/sharadar/sharadar_metrics.go` | Filter figiMap before API calls |
| `provider/zacks/zacks.go` | Filter after screener data load |
| `provider/massive/massive.go` | Filter assets after API fetch |
| `provider/nasdaq/nasdaq.go` | Filter after FIGI map build |
| `provider/fred/fred.go` | Skip with log when filter set |
| `provider/tradingview/tradingview.go` | Skip with log when filter set |
| `provider/ishares/ishares.go` | Skip with log when filter set |
| `provider/pvindex/pvindex.go` | Skip with log when filter set |

---

### Task 1: Add Security Filter Context Helpers

**Files:**
- Create: `provider/filter.go`
- Create: `provider/filter_test.go`

- [ ] **Step 1: Write the failing test**

Create `provider/filter_test.go`:

```go
// Copyright 2024
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SecurityFilterFromContext", func() {
	It("returns empty strings when no filter is set", func() {
		ctx := context.Background()
		ticker, figi := SecurityFilterFromContext(ctx)
		Expect(ticker).To(BeEmpty())
		Expect(figi).To(BeEmpty())
	})

	It("returns ticker when TickerFilterKey is set", func() {
		ctx := context.WithValue(context.Background(), TickerFilterKey, "AAPL")
		ticker, figi := SecurityFilterFromContext(ctx)
		Expect(ticker).To(Equal("AAPL"))
		Expect(figi).To(BeEmpty())
	})

	It("returns figi when FigiFilterKey is set", func() {
		ctx := context.WithValue(context.Background(), FigiFilterKey, "BBG000B9XRY4")
		ticker, figi := SecurityFilterFromContext(ctx)
		Expect(ticker).To(BeEmpty())
		Expect(figi).To(Equal("BBG000B9XRY4"))
	})
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `ginkgo run -race --focus "SecurityFilterFromContext" ./provider/`
Expected: FAIL (SecurityFilterFromContext undefined)

- [ ] **Step 3: Write implementation**

Create `provider/filter.go`:

```go
// Copyright 2024
// SPDX-License-Identifier: Apache-2.0

package provider

import "context"

const TickerFilterKey contextKey = "ticker_filter"
const FigiFilterKey contextKey = "figi_filter"

// SecurityFilterFromContext returns the ticker and/or FIGI filter values
// from the context. Returns empty strings if no filter is set.
func SecurityFilterFromContext(ctx context.Context) (ticker, figi string) {
	if v := ctx.Value(TickerFilterKey); v != nil {
		ticker, _ = v.(string)
	}

	if v := ctx.Value(FigiFilterKey); v != nil {
		figi, _ = v.(string)
	}

	return
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `ginkgo run -race --focus "SecurityFilterFromContext" ./provider/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add provider/filter.go provider/filter_test.go
git commit -m "feat(provider): add security filter context helpers"
```

---

### Task 2: Add Fuzzy Match Helper

**Files:**
- Create: `provider/fuzzy.go`
- Create: `provider/fuzzy_test.go`

- [ ] **Step 1: Write the failing test**

Create `provider/fuzzy_test.go`:

```go
// Copyright 2024
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SuggestMatch", func() {
	candidates := []string{"AAPL", "AMZN", "MSFT", "GOOGL", "META", "TSLA", "NVDA", "AAPLX"}

	It("returns a suggestion for a close match", func() {
		suggestions := SuggestMatch("APL", candidates)
		Expect(suggestions).To(ContainElement("AAPL"))
	})

	It("returns multiple suggestions sorted by similarity", func() {
		suggestions := SuggestMatch("AAPL", []string{"AAPLX", "AAPL", "AAPLS"})
		Expect(suggestions).NotTo(BeEmpty())
		Expect(suggestions[0]).To(Equal("AAPL"))
	})

	It("returns at most 3 suggestions", func() {
		many := []string{"AAP", "AAPL", "AAPLX", "AAPLS", "AAPLZ"}
		suggestions := SuggestMatch("AAPL", many)
		Expect(len(suggestions)).To(BeNumerically("<=", 3))
	})

	It("returns nil when no candidate is close enough", func() {
		suggestions := SuggestMatch("AAPL", []string{"ZZZZ", "XXXX", "QQQQ"})
		Expect(suggestions).To(BeNil())
	})

	It("is case-insensitive", func() {
		suggestions := SuggestMatch("aapl", []string{"AAPL", "MSFT"})
		Expect(suggestions).To(ContainElement("AAPL"))
	})
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `ginkgo run -race --focus "SuggestMatch" ./provider/`
Expected: FAIL (SuggestMatch undefined)

- [ ] **Step 3: Write implementation**

Create `provider/fuzzy.go`:

```go
// Copyright 2024
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"sort"
	"strings"

	"github.com/adrg/strutil"
	"github.com/adrg/strutil/metrics"
)

const suggestThreshold = 0.85

// SuggestMatch returns up to 3 candidates that are similar to the input
// (Jaro-Winkler score >= 0.85), sorted by descending similarity.
// Returns nil if no candidate meets the threshold.
func SuggestMatch(input string, candidates []string) []string {
	type scored struct {
		value string
		score float64
	}

	inputLower := strings.ToLower(input)
	jw := metrics.NewJaroWinkler()

	var matches []scored

	for _, c := range candidates {
		score := strutil.Similarity(inputLower, strings.ToLower(c), jw)
		if score >= suggestThreshold {
			matches = append(matches, scored{value: c, score: score})
		}
	}

	if len(matches) == 0 {
		return nil
	}

	sort.Slice(matches, func(i, j int) bool {
		return matches[i].score > matches[j].score
	})

	limit := 3
	if len(matches) < limit {
		limit = len(matches)
	}

	result := make([]string, limit)
	for i := range limit {
		result[i] = matches[i].value
	}

	return result
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `ginkgo run -race --focus "SuggestMatch" ./provider/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add provider/fuzzy.go provider/fuzzy_test.go
git commit -m "feat(provider): add Jaro-Winkler fuzzy match helper for ticker suggestions"
```

---

### Task 3: Fix Subscription Lookup (Name Fallback + Visible Error)

**Files:**
- Modify: `library/database.go:374-408`
- Modify: `cmd/run.go:66-68`

- [ ] **Step 1: Add name-based fallback to SubscriptionFromID**

In `library/database.go`, modify `SubscriptionFromID` to try name-based lookup when ID prefix match returns no rows:

```go
// SubscriptionFromID fetches a subscription from the library with the given ID.
// It first tries a UUID prefix match, then falls back to an exact case-insensitive
// name match.
func (myLibrary *Library) SubscriptionFromID(ctx context.Context, id string) (*Subscription, error) {
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	subscription := &Subscription{
		Library: myLibrary,
	}

	// Try UUID prefix match first
	rows, err := conn.Query(ctx, fmt.Sprintf(`SELECT id, name, provider, dataset, config,
	data_tables, data_types, total_records, num_records_last_import, total_securities,
	num_securities_last_import, coalesce(first_obs_date, '0001-01-01'::timestamp) as first_obs_date,
	coalesce(last_obs_date, '0001-01-01'::timestamp) as last_obs_date,
	schedule, health_check_id, coalesce(last_run, '0001-01-01'::timestamp) as last_run, active,
	schema_version, created_on, created_by FROM subscriptions WHERE id::text like '%s%%' LIMIT 1`, id))
	if err != nil {
		return nil, err
	}

	err = pgxscan.ScanOne(subscription, rows)
	if err == nil {
		// build DataTablesMap
		subscription.DataTablesMap = make(map[string]string, len(subscription.DataTables))
		for idx, dataType := range subscription.DataTypes {
			subscription.DataTablesMap[dataType] = subscription.DataTables[idx]
		}

		return subscription, nil
	}

	// Fallback: try case-insensitive name match
	rows, err = conn.Query(ctx, `SELECT id, name, provider, dataset, config,
	data_tables, data_types, total_records, num_records_last_import, total_securities,
	num_securities_last_import, coalesce(first_obs_date, '0001-01-01'::timestamp) as first_obs_date,
	coalesce(last_obs_date, '0001-01-01'::timestamp) as last_obs_date,
	schedule, health_check_id, coalesce(last_run, '0001-01-01'::timestamp) as last_run, active,
	schema_version, created_on, created_by FROM subscriptions WHERE lower(name) = lower($1) LIMIT 1`, id)
	if err != nil {
		return nil, err
	}

	err = pgxscan.ScanOne(subscription, rows)
	if err != nil {
		return nil, fmt.Errorf("subscription not found (tried ID prefix and name match): %s", id)
	}

	// build DataTablesMap
	subscription.DataTablesMap = make(map[string]string, len(subscription.DataTables))
	for idx, dataType := range subscription.DataTypes {
		subscription.DataTablesMap[dataType] = subscription.DataTables[idx]
	}

	return subscription, nil
}
```

- [ ] **Step 2: Fix error output in cmd/run.go**

In `cmd/run.go`, change the preflight error handling (line 66-68) to write to stderr:

```go
result, err := tui.RunPreflight(ctx, myLibrary, args)
if err != nil {
    fmt.Fprintf(os.Stderr, "Error: %v\n", err)
    os.Exit(1)
}
```

- [ ] **Step 3: Run build to verify compilation**

Run: `go build ./...`
Expected: success

- [ ] **Step 4: Commit**

```bash
git add library/database.go cmd/run.go
git commit -m "fix(cmd): show error on stderr when subscription not found

SubscriptionFromID now falls back to case-insensitive name match
when UUID prefix lookup fails. The run command writes preflight
errors to stderr so they are visible regardless of logger state."
```

---

### Task 4: Add --ticker and --figi Flags to Run Command

**Files:**
- Modify: `cmd/run.go`

- [ ] **Step 1: Add flag definitions and validation**

In `cmd/run.go`, add the flags in `init()`:

```go
func init() {
	runCmd.Flags().StringP("lookback", "l", "", "Override data lookback period (e.g. 14d, 4w, 6m, 1y)")
	runCmd.Flags().String("ticker", "", "Filter run to a single security by ticker (e.g. AAPL)")
	runCmd.Flags().String("figi", "", "Filter run to a single security by composite FIGI (e.g. BBG000B9XRY4)")

	if err := viper.BindPFlag("lookback", runCmd.Flags().Lookup("lookback")); err != nil {
		log.Fatal().Err(err).Msg("could not bind lookback flag")
	}

	if err := viper.BindPFlag("ticker", runCmd.Flags().Lookup("ticker")); err != nil {
		log.Fatal().Err(err).Msg("could not bind ticker flag")
	}

	if err := viper.BindPFlag("figi", runCmd.Flags().Lookup("figi")); err != nil {
		log.Fatal().Err(err).Msg("could not bind figi flag")
	}

	rootCmd.AddCommand(runCmd)
}
```

- [ ] **Step 2: Add flag validation and context injection in Run function**

At the top of the `Run` func (after lookback handling), add:

```go
// Validate and inject ticker/FIGI filter
tickerFilter := strings.ToUpper(strings.TrimSpace(viper.GetString("ticker")))
figiFilter := strings.TrimSpace(viper.GetString("figi"))

if tickerFilter != "" && figiFilter != "" {
    fmt.Fprintf(os.Stderr, "Error: --ticker and --figi are mutually exclusive\n")
    os.Exit(1)
}

if tickerFilter != "" {
    ctx = context.WithValue(ctx, provider.TickerFilterKey, tickerFilter)
    log.Info().Str("ticker", tickerFilter).Msg("filtering run to single security")
}

if figiFilter != "" {
    ctx = context.WithValue(ctx, provider.FigiFilterKey, figiFilter)
    log.Info().Str("figi", figiFilter).Msg("filtering run to single security")
}
```

Add `"strings"` to the imports if not already present.

- [ ] **Step 3: Run build to verify compilation**

Run: `go build ./...`
Expected: success

- [ ] **Step 4: Commit**

```bash
git add cmd/run.go
git commit -m "feat(cmd): add --ticker and --figi flags to run command

Flags are mutually exclusive. Ticker is normalized to uppercase.
Values are injected into context for providers to read."
```

---

### Task 5: Update SEC Provider to Support Ticker/FIGI Filter

**Files:**
- Modify: `provider/sec/sec.go:74-180`

- [ ] **Step 1: Add filter logic after cikMap is built**

In `fetchFundamentals`, after the `log.Info()` "built combined CIK map" message (line 166) and before `skippedMissingFIGI := 0` (line 168), add:

```go
// Apply ticker/FIGI filter if set
tickerFilter, figiFilter := provider.SecurityFilterFromContext(ctx)
if tickerFilter != "" || figiFilter != "" {
    filtered := make(map[int]AssetInfo)

    for cik, info := range cikMap {
        if tickerFilter != "" && strings.EqualFold(info.Ticker, tickerFilter) {
            filtered[cik] = info
        } else if figiFilter != "" && info.CompositeFigi == figiFilter {
            filtered[cik] = info
        }
    }

    if len(filtered) == 0 {
        // Build candidate list for fuzzy suggestions
        candidates := make([]string, 0, len(cikMap))
        for _, info := range cikMap {
            if tickerFilter != "" {
                candidates = append(candidates, info.Ticker)
            } else {
                candidates = append(candidates, info.CompositeFigi)
            }
        }

        input := tickerFilter
        if input == "" {
            input = figiFilter
        }

        suggestions := provider.SuggestMatch(input, candidates)
        if len(suggestions) > 0 {
            log.Error().Str("input", input).Strs("suggestions", suggestions).Msg("security not found in SEC universe; did you mean one of these?")
        } else {
            log.Error().Str("input", input).Msg("security not found in SEC universe")
        }

        runSummary.Status = data.RunFailed

        return
    }

    cikMap = filtered

    log.Info().Int("filtered_ciks", len(filtered)).Msg("applied security filter to CIK map")
}
```

Add `"strings"` to the imports if not already present.

- [ ] **Step 2: Run build to verify compilation**

Run: `go build ./...`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add provider/sec/sec.go
git commit -m "feat(sec): support --ticker/--figi filter in SEC provider

Filters cikMap to the single matching CIK. Logs error with fuzzy
suggestions if the ticker/FIGI is not found in the universe."
```

---

### Task 6: Update Tiingo Provider to Support Ticker/FIGI Filter

**Files:**
- Modify: `provider/tiingo/tiingo.go`

- [ ] **Step 1: Add filter logic to downloadTiingoEODQuotes**

After the `assets` slice is loaded from `data.ActiveAssets` (after line 192) and before the lookback calculation (line 196), add:

```go
// Apply ticker/FIGI filter if set
tickerFilter, figiFilter := provider.SecurityFilterFromContext(ctx)
if tickerFilter != "" || figiFilter != "" {
    var filtered []*data.Asset

    for _, asset := range assets {
        if tickerFilter != "" && strings.EqualFold(asset.Ticker, tickerFilter) {
            filtered = append(filtered, asset)
        } else if figiFilter != "" && asset.CompositeFigi == figiFilter {
            filtered = append(filtered, asset)
        }
    }

    if len(filtered) == 0 {
        candidates := make([]string, 0, len(assets))
        for _, asset := range assets {
            if tickerFilter != "" {
                candidates = append(candidates, asset.Ticker)
            } else {
                candidates = append(candidates, asset.CompositeFigi)
            }
        }

        input := tickerFilter
        if input == "" {
            input = figiFilter
        }

        suggestions := provider.SuggestMatch(input, candidates)
        if len(suggestions) > 0 {
            log.Error().Str("input", input).Strs("suggestions", suggestions).Msg("security not found in Tiingo universe; did you mean one of these?")
        } else {
            log.Error().Str("input", input).Msg("security not found in Tiingo universe")
        }

        runSummary.Status = data.RunFailed

        return
    }

    assets = filtered

    log.Info().Int("filtered_assets", len(filtered)).Msg("applied security filter")
}
```

- [ ] **Step 2: Add filter logic to downloadTiingoAssets**

In `downloadTiingoAssets`, the function downloads a ZIP of all tickers. Since it processes a bulk file, add the filter AFTER parsing each record, skipping any that don't match. Add at the point where assets are emitted to the output channel -- check `tickerFilter`/`figiFilter` and skip non-matching records. If the loop completes with 0 emitted records, log the error with suggestions using the full list of tickers seen during parsing.

- [ ] **Step 3: Run build to verify compilation**

Run: `go build ./...`
Expected: success

- [ ] **Step 4: Commit**

```bash
git add provider/tiingo/tiingo.go
git commit -m "feat(tiingo): support --ticker/--figi filter in Tiingo provider"
```

---

### Task 7: Update Sharadar Provider to Support Ticker/FIGI Filter

**Files:**
- Modify: `provider/sharadar/sharadar_fundamentals.go`
- Modify: `provider/sharadar/sharadar_metrics.go`
- Modify: `provider/sharadar/sharadar_tickers.go`

- [ ] **Step 1: Add filter to sharadar_fundamentals.go**

After building the `figiMap` from `data.ActiveAssets`, filter it:

```go
tickerFilter, figiFilter := provider.SecurityFilterFromContext(ctx)
if tickerFilter != "" || figiFilter != "" {
    filtered := make(map[string]string) // ticker -> figi

    for ticker, figi := range figiMap {
        if tickerFilter != "" && strings.EqualFold(ticker, tickerFilter) {
            filtered[ticker] = figi
        } else if figiFilter != "" && figi == figiFilter {
            filtered[ticker] = figi
        }
    }

    if len(filtered) == 0 {
        candidates := make([]string, 0, len(figiMap))
        for ticker := range figiMap {
            if tickerFilter != "" {
                candidates = append(candidates, ticker)
            }
        }

        if figiFilter != "" {
            for _, figi := range figiMap {
                candidates = append(candidates, figi)
            }
        }

        input := tickerFilter
        if input == "" {
            input = figiFilter
        }

        suggestions := provider.SuggestMatch(input, candidates)
        if len(suggestions) > 0 {
            log.Error().Str("input", input).Strs("suggestions", suggestions).Msg("security not found in Sharadar universe; did you mean one of these?")
        } else {
            log.Error().Str("input", input).Msg("security not found in Sharadar universe")
        }

        runSummary.Status = data.RunFailed

        return
    }

    figiMap = filtered

    log.Info().Int("filtered_assets", len(filtered)).Msg("applied security filter")
}
```

- [ ] **Step 2: Apply filter to sharadar_metrics.go**

In `downloadAllSharadarMetrics`, after building figiMap from `data.ActiveAssets`, add the identical filter block: call `SecurityFilterFromContext(ctx)`, build a `filtered` map keeping only the matching entry, log error with `SuggestMatch` suggestions if empty, set `runSummary.Status = data.RunFailed` and return. Otherwise reassign `figiMap = filtered`.

- [ ] **Step 3: Apply filter to sharadar_tickers.go**

In `downloadAllSharadarTickers`, this function paginates through the Nasdaq Data Link API fetching all tickers. Since it's a bulk API, apply the filter post-fetch: after parsing each record in the pagination loop, check if the ticker matches before emitting to the output channel. If the entire pagination completes with 0 emitted observations and a filter is set, log the error with `SuggestMatch` suggestions (build candidates from all tickers seen during pagination).

- [ ] **Step 4: Run build to verify compilation**

Run: `go build ./...`
Expected: success

- [ ] **Step 5: Commit**

```bash
git add provider/sharadar/sharadar_fundamentals.go provider/sharadar/sharadar_metrics.go provider/sharadar/sharadar_tickers.go
git commit -m "feat(sharadar): support --ticker/--figi filter in Sharadar provider"
```

---

### Task 8: Update Zacks Provider to Support Ticker/FIGI Filter

**Files:**
- Modify: `provider/zacks/zacks.go`

- [ ] **Step 1: Add filter after figiMap is built**

In `downloadZacksData`, after the figiMap is constructed from `data.ActiveAssets`, add the same filtering pattern: check `SecurityFilterFromContext`, filter figiMap, log error with suggestions if not found.

- [ ] **Step 2: Run build to verify compilation**

Run: `go build ./...`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add provider/zacks/zacks.go
git commit -m "feat(zacks): support --ticker/--figi filter in Zacks provider"
```

---

### Task 9: Update Massive Provider to Support Ticker/FIGI Filter

**Files:**
- Modify: `provider/massive/massive.go`

- [ ] **Step 1: Add filter after assets are fetched**

In `downloadMassiveAssets`, after assets are collected from the API and before `filterAssetsByLastUpdated`, filter the assets slice using the same pattern. Match on `asset.Ticker` or `asset.CompositeFigi`.

- [ ] **Step 2: Run build to verify compilation**

Run: `go build ./...`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add provider/massive/massive.go
git commit -m "feat(massive): support --ticker/--figi filter in Massive provider"
```

---

### Task 10: Update Nasdaq Provider to Support Ticker/FIGI Filter

**Files:**
- Modify: `provider/nasdaq/nasdaq.go`

- [ ] **Step 1: Add filter after figiMap is built**

In `downloadNasdaqHoldings`, after the figiMap is constructed, apply the same filter pattern.

- [ ] **Step 2: Run build to verify compilation**

Run: `go build ./...`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add provider/nasdaq/nasdaq.go
git commit -m "feat(nasdaq): support --ticker/--figi filter in Nasdaq provider"
```

---

### Task 11: Update Non-Applicable Providers to Skip When Filter Is Set

**Files:**
- Modify: `provider/fred/fred.go`
- Modify: `provider/tradingview/tradingview.go`
- Modify: `provider/ishares/ishares.go`
- Modify: `provider/pvindex/pvindex.go`

- [ ] **Step 1: Add skip logic to each Fetch function**

At the top of each provider's Fetch function (after the defer/runSummary setup), add:

```go
tickerFilter, figiFilter := provider.SecurityFilterFromContext(ctx)
if tickerFilter != "" || figiFilter != "" {
    log.Info().Str("provider", "<PROVIDER_NAME>").Msg("ticker/FIGI filtering not applicable to this provider, skipping")
    return
}
```

Replace `<PROVIDER_NAME>` with the actual provider name ("fred", "tradingview", "ishares", "pvindex").

For providers with multiple datasets, add this to each Fetch function.

- [ ] **Step 2: Run build to verify compilation**

Run: `go build ./...`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add provider/fred/fred.go provider/tradingview/tradingview.go provider/ishares/ishares.go provider/pvindex/pvindex.go
git commit -m "feat(providers): skip non-applicable providers when ticker/FIGI filter is set

Fred, TradingView, iShares, and PVIndex providers log a message and
return immediately since they don't operate on individual securities."
```

---

### Task 12: Run Full Test Suite and Lint

- [ ] **Step 1: Run lint**

Run: `make lint`
Expected: no errors

- [ ] **Step 2: Run tests**

Run: `make test`
Expected: all pass

- [ ] **Step 3: Fix any issues found by lint or tests**

If `golangci-lint` reports issues, run `golangci-lint run --fix` to auto-fix.

- [ ] **Step 4: Commit any fixes**

```bash
git add -A
git commit -m "fix: lint and test corrections"
```
