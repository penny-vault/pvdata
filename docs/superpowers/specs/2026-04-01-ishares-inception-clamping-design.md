# iShares Inception Date Clamping

## Problem

The iShares provider supports a `--lookback` flag to backfill historical index composition data. When lookback is large (e.g., 30 years), the provider requests dates before the ETF existed, wasting HTTP requests and potentially returning bad data. The lookback should be clamped to the fund's inception date.

## Solution

### 1. Updated `ishares_etfs.json` schema

Each entry gains `inceptionDate`, `bloombergTicker`, and the existing `indexName` is replaced with the official benchmark name from the iShares product page. Active ETFs (those not tracking an index) are removed from the file.

Before:
```json
{
  "ticker": "IVV",
  "productId": "239726",
  "slug": "ishares-core-sp-500-etf",
  "indexName": "sp-500"
}
```

After:
```json
{
  "ticker": "IVV",
  "productId": "239726",
  "slug": "ishares-core-sp-500-etf",
  "indexName": "S&P 500 Index (USD)",
  "bloombergTicker": "SPTR",
  "inceptionDate": "2000-05-15"
}
```

Inception dates and key facts are scraped from iShares product pages using one-time Python scripts in `scripts/`. These are development-time tools, not part of the built binary.

### 2. Go struct changes

`iSharesETF` gains:
- `InceptionDate time.Time` -- parsed from the JSON `inceptionDate` string during `init()`
- `BloombergTicker string` -- stored for reference

The existing `IndexName` field now holds the official benchmark name instead of the slug-derived name. No DB migration needed -- old and new index names will coexist as separate series.

### 3. Lookback clamping

In `downloadSingleISharesETF`, after computing `startDate` from the lookback duration, clamp it to the ETF's inception date:

```go
if !etf.InceptionDate.IsZero() && startDate.Before(etf.InceptionDate) {
    startDate = etf.InceptionDate
}
```

No other behavioral changes. Everything downstream (trading days query, date loop, state reconstruction) works with whatever `startDate` it receives.

## Files changed

- `provider/ishares_etfs.json` -- updated schema, active ETFs removed
- `provider/ishares.go` -- struct update, inception date parsing in `init()`, clamping in `downloadSingleISharesETF`
- `scripts/scrape_inception_dates.py` -- one-time dev script (already written)
- `scripts/scrape_ishares_keyfacts.py` -- one-time dev script (already written)
