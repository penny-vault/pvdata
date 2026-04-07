# SEC Fundamentals Provider Design

## Overview

A new provider (`provider/sec/`) that extracts fundamental financial data from SEC EDGAR XBRL filings via the companyfacts API. Replicates the fundamental data that Sharadar provides, sourced directly from SEC filings. Sharadar and Zacks data serve as validation references.

## Data Source

- **Primary**: SEC EDGAR companyfacts API (`data.sec.gov/api/xbrl/companyfacts/`)
- **Bulk backfill**: `companyfacts.zip` bulk download (all companies, one-time)
- **Filing discovery**: XBRL RSS feed (`sec.gov/Archives/edgar/xbrl-rss.xml`)
- **Filing types**: 10-K (annual) and 10-Q (quarterly) only
- **History**: 2009-01-01 onward (XBRL mandate start)

The companyfacts API is officially documented by the SEC. It returns all XBRL facts ever filed by a company in a single JSON response, keyed by US-GAAP taxonomy tag. Rate limit is 10 requests/second with a mandatory User-Agent header.

## Field Scope

Initial scope matches Sharadar's ~111 fundamental fields across:

- **Income statement** (~20 fields): Revenue, COGS, gross profit, operating expenses, R&D, SG&A, operating income, interest expense, EBIT, EBITDA, pre-tax income, taxes, net income, EPS variants
- **Balance sheet** (~26 fields): Total assets, current assets, cash, receivables, inventory, PP&E, intangibles, goodwill, total liabilities, current/non-current debt, equity
- **Cash flow** (~15 fields): Operating/investing/financing cash flows, CapEx, free cash flow, D&A
- **Ratios and per-share** (~30 fields): ROA, ROE, ROIC, margins, current ratio, debt/equity, book value/share, FCF/share, EPS diluted
- **Share information**: Shares basic, weighted average shares, diluted shares, share factor

The mapping layer is data-driven (config table), so expanding to Zacks-level granularity (~425 industrial fields) requires only adding entries to the mapping config, not changing code.

## Package Structure

```
provider/sec/
  sec.go                 # Provider registration, datasets, config
  companyfacts.go        # companyfacts API client + bulk zip download
  rss.go                 # XBRL RSS feed polling for new filing discovery
  mapping.go             # XBRL tag -> Sharadar field normalization engine
  mapping_config.go      # Data-driven mapping table (tag lists per field)
  dimensions.go          # AR/MR restatement tracking + TTM computation
  cik.go                 # CIK <-> ticker/FIGI resolution
  sec_test.go            # Tests
  sec_suite_test.go
  testdata/              # Sample companyfacts JSON for test fixtures
```

## Provider Interface

Registers as `"SEC"` implementing the `Provider` interface.

- **Name()**: `"SEC"`
- **Description()**: `"SEC EDGAR fundamentals extracted from 10-K and 10-Q XBRL filings via the companyfacts API"`
- **ConfigDescription()**:
  - `userAgent`: Required. Email address for SEC User-Agent header (e.g., `pvdata/1.0 user@email.com`).
  - `rateLimit`: Requests per second to SEC (default 10).
- **Datasets()**:
  - **Fundamentals**: Data type `data.FundamentalsKey`, date range 2009-01-01 to now.

No FileImporter interface -- API only.

## Data Flow

### Backfill (first run, no LastObsDate)

```
companyfacts.zip (bulk download, ~1GB)
  -> unzip to individual CIK JSON files
  -> for each CIK:
     -> resolve CIK to ticker/FIGI (assets table -> SEC company_tickers.json -> OpenFIGI)
     -> skip if no match (unknown entity)
     -> extract facts using mapping table
     -> group facts into filing periods (quarterly/annual)
     -> determine AR vs MR for each period
     -> compute TTM from quarterly data
     -> emit data.Fundamental observations for all 6 dimensions
```

### Incremental updates (subsequent runs)

```
poll XBRL RSS feed
  -> filter for 10-K and 10-Q form types
  -> extract CIK from each new filing entry
  -> resolve CIK to known asset (skip unknown)
  -> fetch companyfacts/CIK{cik}.json for that company
  -> same normalization pipeline, but only emit new/changed periods
  -> emit data.Fundamental observations
```

### Rate limiting

10 req/sec to SEC enforced via `golang.org/x/time/rate` (same pattern as Sharadar provider).

## XBRL Tag Mapping Engine

### Mapping config structure

Each Sharadar field maps to either direct XBRL tags or a derived formula:

```go
type FieldMapping struct {
    SharadarField string   // target field name, e.g. "revenue"
    Type          string   // "direct" or "derived"
    XBRLTags      []string // ordered fallback list for direct lookup
    Formula       string   // expression referencing other fields, for derived
    StatementType string   // "flow" or "point_in_time" (affects TTM computation)
}
```

### Resolution logic

For direct fields:
1. Walk the `XBRLTags` list in order
2. For each tag, look for a fact matching the target period end date and USD unit
3. Filter out facts with dimensional qualifiers (use consolidated totals only)
4. First match wins
5. If no tag matches, field is nil

For derived fields:
1. Try direct XBRL tag lookup first (some companies report EBITDA directly)
2. If not found, evaluate the formula using already-resolved fields
3. Topological sort ensures dependencies resolve before dependents

### Examples

Direct field:
```
revenue -> try: Revenues, RevenueFromContractWithCustomerExcludingAssessedTax,
                SalesRevenueNet, RevenueFromContractWithCustomerIncludingAssessedTax
```

Derived field:
```
ebitda -> try XBRL tag first, else compute: operating_income + depreciation_amortization
free_cash_flow -> operating_cash_flow - capital_expenditure
working_capital -> current_assets - current_liabilities
gross_margin -> gross_profit / revenue
```

### Bootstrap

Initial mapping table extracted from edgartools' `concept_mappings.json` and `gaap_mappings.json` (MIT licensed, validated against 32,240 real filings, 2,770 tags mapped to 253 concepts). Filtered down to Sharadar's ~111 fields. Stored as Go data in `mapping_config.go` -- no runtime file dependency.

## Dimension / Restatement Handling

companyfacts returns all reported facts with `filed` (filing date) and `end` (period end date). Six dimensions are produced:

### AR (As Reported)

For each period end date, use facts from the **earliest** `filed` date that reported on that period. Represents what was known at the time of original filing. Critical for backtesting.

### MR (Most Recent Reported)

For each period end date, use facts from the **latest** `filed` date that reported on that period. Captures restatements and amendments.

### Quarterly vs Annual

- **Q (ARQ/MRQ)**: Facts from 10-Q filings. Identified by `form` field = "10-Q".
- **Y (ARY/MRY)**: Facts from 10-K filings. Identified by `form` field = "10-K".

### Trailing Twelve Months (ART/MRT)

Computed from quarterly values:
- **Flow items** (revenue, net income, cash flow, etc.): Sum of 4 most recent quarters
- **Point-in-time items** (total assets, total debt, equity, etc.): Use most recent quarter's value
- ART uses AR quarterly values; MRT uses MR quarterly values

Each field in the mapping config is annotated with `StatementType` ("flow" or "point_in_time") to drive TTM computation.

## CIK Resolution

Three-step lookup in `cik.go`:

1. **Assets table**: Query database for assets with matching CIK. Returns ticker + CompositeFigi directly. Covers everything already populated by Sharadar's ticker data.
2. **SEC company_tickers.json**: SEC-published mapping of CIK to current ticker and exchange. Downloaded once and cached. Fallback for companies not in assets table.
3. **OpenFIGI lookup**: For tickers from step 2 that lack a FIGI, use existing `figi.Enrich()` flow.

**Reverse lookup** (for incremental updates): Build in-memory CIK -> asset map from database at startup. Only fetch companyfacts for CIKs that resolve to a known asset.

**Edge cases**:
- Multiple tickers per CIK (share classes): companyfacts reports at entity level. Use primary ticker.
- Ticker changes: CIK is stable across ticker changes, which is an advantage.

## Validation Strategy

Compare SEC provider output against Sharadar and Zacks for the same companies and periods:

1. **Pick a test set**: Use Zacks sample companies (AAPL, MSFT, JPM, GE, etc.) spanning different industries and time periods.
2. **Field-by-field comparison**: For each Sharadar field, compare SEC-extracted value against Sharadar's value. Allow small tolerance for rounding differences.
3. **Dimension verification**: Confirm AR values match Sharadar's ARQ/ARY (same filing date), MR values match MRQ/MRY.
4. **TTM verification**: Confirm ART/MRT values equal sum of 4 quarters (for flow items) or latest quarter (for point-in-time items).
5. **Cross-reference with Zacks**: For fields that exist in both Sharadar and Zacks, verify all three sources agree.
6. **Coverage reporting**: Track what percentage of Sharadar fields we successfully extract per company, flag companies with low coverage for investigation.

## Configuration

Provider config via subscription:

```toml
[sec]
userAgent = "pvdata/1.0 user@email.com"
rateLimit = 10
```

## Dependencies

- `go-resty/resty` -- HTTP client (already used by Sharadar)
- `golang.org/x/time/rate` -- Rate limiting (already used by Sharadar)
- `tidwall/gjson` -- JSON parsing (already used by Sharadar)
- No new dependencies required

## Out of Scope

- Daily valuation metrics (market cap, PE, PB, etc.) -- separate concern, can be a future provider or post-processing step
- Industry-specific financial statements (banking, insurance, REIT, utility) -- future expansion, the mapping config supports it
- 8-K filings -- Sharadar doesn't include these in SF1
- FileImporter interface -- API-only provider
- Real-time XBRL parsing from filing documents -- companyfacts API is sufficient
