# Run Error Logging and Ticker/FIGI Filter

## Problem

1. When a subscription ID/name is not found during `pvdata run`, the error is swallowed because the logger has already been reconfigured to write to `~/.pvdata.log` + a TUI channel. The user sees nothing on the terminal.

2. There is no way to filter a provider run to a single security (by ticker or FIGI), which makes debugging individual companies (e.g., a specific SEC filing) require processing the entire universe.

## Solution

### 1. Fix Silent Error on Subscription Not Found

**Logger issue:** In `cmd/run.go`, the logger is reconfigured to a `DualWriter` (file + channel) at line 63-64. The `log.Fatal()` for preflight failure at line 68 writes to the file but never to the terminal.

**Fix:** When preflight fails, write the error to stderr directly (`fmt.Fprintf(os.Stderr, "Error: %v\n", err)` + `os.Exit(1)`) so it is always visible regardless of logger configuration.

**Name-based lookup:** `SubscriptionFromID` currently only does UUID prefix matching (`WHERE id::text LIKE '<input>%'`). Add a fallback: if the prefix match returns no rows, try `WHERE name ILIKE '<input>'` (exact, case-insensitive name match). This lets users type `pvdata run "SEC Fundamentals"` instead of pasting a UUID.

### 2. General-Purpose `--ticker` / `--figi` Flags

**New context keys** in `provider/provider.go`:

```go
const TickerFilterKey contextKey = "ticker_filter"
const FigiFilterKey contextKey = "figi_filter"
```

**Helper function:**

```go
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

**Flag parsing in `cmd/run.go`:**

- Add `--ticker` and `--figi` string flags.
- Validate they are mutually exclusive (error if both set).
- Normalize ticker to uppercase (`strings.ToUpper`) before injecting into context.
- Inject the value into context before providers run.

**Provider behavior by category:**

| Category | Providers | Behavior |
|----------|-----------|----------|
| Individual securities | sec, tiingo, sharadar, zacks, massive, nasdaq | Filter universe to only the matching security. If no match, complete with 0 observations and a warning log. |
| Non-applicable | fred, tradingview, ishares, pvindex, legacy | Log "ticker/FIGI filtering not applicable to provider X, skipping" and emit 0 observations with success status. |

**SEC provider specifically:**

- In `runBackfill`: when processing the companyfacts zip, only handle the CIK that maps to the filtered ticker/FIGI. Skip all others.
- In `runIncremental`: only fetch filings for the matching CIK.
- Lookup: match ticker or FIGI against the `cikMap` to find the target CIK.

**Other individual-security providers:**

Each provider checks `SecurityFilterFromContext(ctx)` at the start of its Fetch function. If a filter is set, it restricts its API calls / data processing to only the matching security. The exact mechanism varies by provider (API query parameters, loop filtering, etc.).

## Non-Goals

- Filtering by multiple tickers/FIGIs in a single run (can be added later).
- Persisting the filter in subscription config (this is a runtime debug flag only).
