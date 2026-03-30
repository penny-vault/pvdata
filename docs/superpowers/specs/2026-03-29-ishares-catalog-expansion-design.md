# iShares ETF Catalog Expansion

## Summary

Expand the iShares provider to support all US-listed iShares equity ETFs by moving the hardcoded ETF map to an embedded JSON data file. This makes the catalog easy to maintain and extend without code changes.

## Current State

The iShares provider (`provider/ishares.go`) has a hardcoded `iSharesETFMap` with 19 ETFs covering S&P 500, Russell, and MSCI indices. Each entry contains a `ProductID`, `Slug`, and `IndexName` used to construct the iShares product page URL and label the index in the database.

The parser (`provider/ishares_parser.go`) filters holdings to `Asset Class == "Equity"` only, which remains appropriate since we are expanding to equity ETFs only.

## Design

### Data File

Create `provider/ishares_etfs.json` -- an embedded JSON array of ETF definitions:

```json
[
  {
    "ticker": "IVV",
    "productId": "239726",
    "slug": "ishares-core-s-p-500-etf",
    "indexName": "sp500"
  }
]
```

Fields:
- `ticker`: The ETF ticker symbol (map key)
- `productId`: iShares product ID from the URL path
- `slug`: URL slug from the iShares product page (required for navigation)
- `indexName`: Descriptive slug stored in the database, derived from the underlying index name

### Code Changes

**`provider/ishares.go`**:
- Remove the hardcoded `iSharesETFMap` map literal
- Add `//go:embed ishares_etfs.json` directive
- Add `init()` function that unmarshals the JSON into `iSharesETFMap`
- The `iSharesETF` struct and `iSharesETFMap` variable declaration remain; the map starts empty and is populated in `init()`
- No changes to `downloadISharesHoldings` or `downloadSingleISharesETF`

**`provider/ishares_parser.go`**: No changes. The equity-only filter stays.

**`provider/index_helpers.go`**: No changes.

**`data/index.go`**: No changes.

### Index Naming Convention

Index names follow existing conventions:
- Lowercase, hyphen-separated
- Derived from the underlying index name, not the fund name
- Concise but unambiguous

Examples:
- `IVV` -> `sp500`
- `XLE` -> `sp-energy-sector`
- `QUAL` -> `msci-usa-quality-factor`
- `MTUM` -> `msci-usa-momentum-factor`

### Catalog Population

The JSON file will be populated by researching the iShares US product listing for all equity ETFs. Each entry requires the real product ID and slug from the iShares website URL. The `indexName` is manually assigned following the naming convention.

### Testing

- Add a test verifying `iSharesETFMap` is populated after package init and that a known ETF (e.g., "IVV") resolves correctly
- Existing parser and helper tests are unaffected

## Phase 2: CSV API and Rate Limiting

### CSV API

The iShares website exposes a CSV download endpoint that works with plain HTTP requests (no Playwright/browser needed):

```
https://www.ishares.com/us/products/{productId}/{slug}/1467271812596.ajax?fileType=csv&fileName={ticker}_holdings&dataType=fund
```

The `1467271812596.ajax` path segment is constant across all products. The CSV format has metadata rows at the top (fund name, holdings date), a blank line, then a standard CSV table with headers: `Ticker,Name,Sector,Asset Class,Market Value,Weight (%),Notional Value,Quantity,Price,Location,Exchange,Currency,FX Rate,Market Currency,Accrual Date`.

Switching from Playwright XLS download to direct HTTP CSV download:
- Eliminates Playwright dependency for iShares (Nasdaq provider still uses it)
- Is faster and more reliable (no browser overhead)
- Uses resty (already a project dependency) for HTTP with retry support

### Rate Limiting

With 287 ETFs, a randomized delay between requests prevents hammering the iShares servers. Each request is followed by a random sleep of **5 seconds to 45 seconds** (uniform distribution). This gives an average of ~25 seconds per request, completing a full 287-ETF run in approximately 2 hours.

### Code Changes

**`provider/ishares.go`:**
- Replace Playwright startup/teardown with a resty client (retry on 429/5xx, 60s timeout)
- Replace `downloadSingleISharesETF` signature: drop `playwright.Page`, add `*resty.Client` and `ticker string`
- Build CSV URL from ETF metadata + ticker
- Add randomized delay (5-45s) between requests in the ticker loop
- Remove `playwright_helpers` and `playwright-go` imports

**`provider/ishares_parser.go`:**
- Replace `parseISharesXML` with `parseISharesCSV`
- Parse metadata rows to extract snapshot date (second row: `Fund Holdings as of,"Mar 27, 2026"`)
- Parse CSV data rows using `encoding/csv`
- Same output: `iSharesParseResult` with `SnapshotDate` and `[]iSharesHolding`
- Same equity-only filter on `Asset Class` column
- Remove all XML types (`ssWorkbook`, `ssWorksheet`, `ssTable`, `ssRow`, `ssCell`, `ssData`)
- Remove `sanitizeAmpersands` (XML-specific workaround)

**`provider/ishares_parser_test.go`:**
- Replace XML sample data with CSV sample data
- Same test cases: parse holdings, extract date, extract weight, filter non-equity

**`provider/integration_test.go`:**
- Update `TestISharesDownloadAndParse` to use resty + CSV instead of Playwright + XLS

## Scope

- Equity ETFs only (bond, commodity, and multi-asset ETFs are excluded by the existing parser filter)
- US-listed iShares ETFs only
- Static/manual catalog -- no dynamic discovery from the iShares website
