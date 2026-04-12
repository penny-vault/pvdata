# SEC: Compute market-data fields from EOD table

**Issue:** #38

## Problem

The SEC provider leaves 14 fields at zero because they require market price data not available in the SEC XBRL companyfacts API. The EOD table already has the price data needed to compute them.

## Approach

Enrich during the SEC provider's Fetch function. A new `enrichMarketData` function is called from `emitFundamentals()` after all observations for a company are built but before they're sent to the output channel. It looks up EOD close prices from the published `eod` view and computes the derived fields.

The function needs all dimensions for a given (composite_figi, date_key) at once so it can copy ratio fields from trailing to quarterly dimensions.

## EOD price lookup

- Source: the published `eod` view
- Price = unadjusted `close` (not `adj_close`)
- Date matching: most recent trading day on or before `event_date` (verified against Sharadar -- e.g., event_date Saturday 2024-12-28 uses Friday 2024-12-27 close)
- If no EOD row is found within a reasonable lookback window (5 trading days), leave price at zero

## Formulas

All formulas verified against Sharadar data for AAPL and MSFT.

### Computed for all dimensions

| Field | Formula |
|---|---|
| `price` | EOD close on or before event_date |
| `market_capitalization` | price * shares_basic |
| `enterprise_value` | market_cap + total_debt - cash_and_equivalents |
| `pb` | market_cap / equity |
| `share_factor` | 1.0 |
| `fx_usd` | 1.0 |

### Computed for trailing/annual dimensions (ART, MRT, ARY, MRY)

These use the record's own flow/TTM values. For quarterly dimensions (ARQ, MRQ), copy from the corresponding trailing dimension (ART, MRT) since they share the same event_date and therefore the same price/market_cap.

| Field | Formula |
|---|---|
| `pe` | market_cap / net_income_common_stock |
| `ps` | market_cap / revenues |
| `pe1` | price / eps (basic EPS, not diluted) |
| `ps1` | price / sales_per_share |
| `ev_to_ebit` | round(enterprise_value / ebit) |
| `ev_to_ebitda` | enterprise_value / ebitda |
| `dividend_yield` | dividends_per_basic_common_share / price |
| `payout_ratio` | dividends_per_basic_common_share / eps_diluted |

### Division by zero

When a denominator is zero (e.g., net_income_common_stock = 0), leave the ratio at zero.

### pe1 note

pe1 uses basic EPS (`eps` field), not diluted EPS (`eps_diluted`). Verified empirically: `price / eps` matches Sharadar's pe1 exactly across AAPL and MSFT for all trailing/annual dimensions. `price / eps_diluted` does not match.

## Pipeline integration

`emitFundamentals()` currently builds observations and sends them to the output channel one at a time. To enrich, we need to:

1. Buffer all observations for a company before sending
2. Group by (composite_figi, date_key) to find trailing/quarterly pairs
3. For each group: look up EOD price, compute fields on trailing/annual dims, copy to quarterly dims
4. Send all enriched observations to the output channel

The enrichment function needs a database connection to query the EOD view. The `*library.Subscription` already carries `Library.Pool` which provides DB access. The published `eod` view name needs to be discovered -- query `published_views` for the view with `data_type_key = 'eod'`.

If no published `eod` view exists (e.g., fresh install with no EOD subscription), skip enrichment and log a warning. The fundamentals are still valid, just without market-data fields.

## Files modified

- `provider/sec/market_data.go` -- new file with `enrichMarketData` function and helpers
- `provider/sec/market_data_test.go` -- unit tests with synthetic data
- `provider/sec/sec.go` -- modify `emitFundamentals()` to buffer observations and call enrichment

## Testing

Unit tests with synthetic data (no real database). Mock the EOD price lookup by accepting a function parameter or interface.

Test cases:
1. All 14 fields computed correctly for an ART record with known price and fundamentals
2. Quarterly dims (ARQ, MRQ) get ratio fields copied from trailing dims (ART, MRT)
3. PB computed independently for quarterly dims (uses own equity, not trailing)
4. Division by zero leaves ratio at zero
5. Missing EOD price leaves all market-data fields at zero
6. pe1 uses basic EPS, not diluted
