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

## Scope

- Equity ETFs only (bond, commodity, and multi-asset ETFs are excluded by the existing parser filter)
- US-listed iShares ETFs only
- Static/manual catalog -- no dynamic discovery from the iShares website
