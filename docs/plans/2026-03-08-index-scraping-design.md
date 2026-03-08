# Index Scraping Providers Design

## Overview

Add two new providers for scraping index constituent data: iShares (for ETF-based indices like Russell 1000, S&P 500, etc.) and Nasdaq (for NDX-100). Both emit IndexSnapshot and IndexChange observations using the existing IndexKey data type, with a new weight field added to snapshots.

## Providers

### iShares Provider (`provider/ishares.go`)

**Config:**
- `tickers`: Comma-separated ETF tickers (e.g., `IVV,IWD,IWM,IXUS`)
- `snapshotFrequency`: How often to write full snapshots (`daily`, `weekly`, `monthly`, `quarterly`)

**Hardcoded ETF Map:**
A map of ticker -> `{productID, slug, indexName}` is maintained in the provider code. Initial set includes equity index ETFs: IVV, IWB, IWD, IWF, IWM, IJH, IJR, IXUS, IEFA, IEMG, IVW, IVE, ITOT, and others. Product IDs and slugs will be looked up during implementation.

**URL Construction:**
```
https://www.ishares.com/us/products/{productID}/{slug}/{ajaxID}.ajax?fileType=xls&fileName={fileName}&dataType=fund
```
The ajaxID and fileName components need to be discovered from the product page.

**Fetch Method:**
Uses Playwright to navigate to the iShares product page and download the XLS file. The file is XML/SpreadsheetML despite the .xls extension.

**Parsing:**
- Parse the Holdings worksheet from the XML
- Extract the snapshot date from the first row
- Extract ticker and weight for each row where Asset Class is "Equity"
- Filter out non-equity holdings (cash, derivatives, etc.)

**Dataset:** "Index Holdings" with DataTypes: [IndexKey]

### Nasdaq Provider (`provider/nasdaq.go`)

**Config:**
- `snapshotFrequency`: How often to write full snapshots (`daily`, `weekly`, `monthly`, `quarterly`)

**Fetch Method:**
Uses Playwright to scrape the NDX constituents table from `https://www.nasdaq.com/market-activity/quotes/nasdaq-ndx-index`.

**Index Name:** `ndx100`

**Dataset:** "Index Holdings" with DataTypes: [IndexKey]

## Shared Fetch Logic

On each run, both providers follow the same pattern:

1. Download/scrape current holdings (ticker + weight as decimal fraction)
2. Look up composite FIGI for each ticker from the assets table in the database
3. Query the DB for the most recent snapshot of this index
4. Diff current vs. last snapshot to detect membership changes
5. Emit `IndexChange` observations for any adds (action="add") or removes (action="remove")
6. Check if enough time has elapsed since the last snapshot date (per `snapshotFrequency`)
7. If the interval has elapsed, emit `IndexSnapshot` observations for all current holdings with weights

## Data Model Changes

### IndexSnapshot struct (`data/index.go`)

Add `Weight float64` field:

```go
type IndexSnapshot struct {
    Ticker        string
    CompositeFigi string
    IndexName     string
    SnapshotDate  time.Time
    Weight        float64  // NEW: decimal fraction (e.g., 0.0294 for 2.94%)
}
```

### IndexKey schema (`data/datatype.go`)

Add `weight` column to the `_snapshot` table:

```sql
weight REAL NOT NULL DEFAULT 0.0
```

Add a migration (Version 1) for existing tables:

```sql
ALTER TABLE %[1]s_snapshot ADD COLUMN IF NOT EXISTS weight REAL NOT NULL DEFAULT 0.0;
```

### IndexSnapshot.SaveDB (`data/index.go`)

- Include `weight` in the INSERT statement
- Change from `ON CONFLICT DO NOTHING` to `ON CONFLICT DO UPDATE SET weight = EXCLUDED.weight`
- This ensures re-runs update the weight even if the snapshot already exists

### Changelog behavior

The changelog only tracks membership changes (add/remove). Weight changes are not tracked in the changelog; they are visible by comparing snapshots over time. No threshold logic is needed.

## Provider Registration (`provider/discover.go`)

```go
var Map = map[string]Provider{
    "fred":     &Fred{},
    "ishares":  &IShares{},
    "massive":  &Massive{},
    "nasdaq":   &Nasdaq{},
    "polygon":  &Massive{},
    "sharadar": &Sharadar{},
    "tiingo":   &Tiingo{},
    "zacks":    &Zacks{},
}
```

## Impact on Existing Providers

Sharadar and Zacks currently emit IndexSnapshot observations without weight. After the schema change they will write weight=0.0 (the default), which is acceptable since they don't have weight data available.
