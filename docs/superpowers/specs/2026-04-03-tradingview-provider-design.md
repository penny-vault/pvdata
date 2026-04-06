# TradingView Index Constituents Provider

## Purpose

Fetch definitive index constituent membership from TradingView's screener API and track membership changes over time. Complements the iShares provider (which provides weights but uses ETF holdings as a proxy, causing sampling gaps on larger indices).

## Data Source

TradingView exposes an unauthenticated JSON API at:

```
POST https://screener-facade.tradingview.com/screener-facade/api/v1/screener-table/scan
  ?table_id=symbols.components
  &version=54
  &columnset_id=overview
  &symbol_constituents_id={SYMBOL_ID}
```

Required headers:
- `accept: application/json`
- `content-type: text/plain;charset=UTF-8`
- `origin: https://www.tradingview.com`
- `referer: https://www.tradingview.com/`
- Standard browser `user-agent`

Request body:
```json
{
  "lang": "en",
  "range": [0, 5000],
  "sort": {"sortBy": {"id": "MarketCap", "params": {}}, "sortOrder": "desc", "nullsFirst": false},
  "scanner_product_label": "symbols-components"
}
```

The full constituent list is returned in a single request (no pagination needed). The response includes:
- `totalCount`: number of constituents
- `symbols`: array of `"EXCHANGE:TICKER"` strings (e.g. `"NYSE:MOG.A"`)
- `data[0].rawValues`: array of objects with `name` (ticker), `description` (company name), `exchange`

## Supported Indices

Embedded in `tradingview_indexes.json`:

| Index | Symbol | Symbol ID |
|-------|--------|-----------|
| S&P 500 | SPX | `SYML:SP;SPX` |
| S&P 400 (MidCap) | MID | `SYML:SP;MID` |
| S&P 100 | OEX | `SYML:SP;OEX` |
| Nasdaq Composite | IXIC | `SYML:NASDAQ;IXIC` |
| Nasdaq 100 | NDX | `SYML:NASDAQ;NDX` |
| Russell 1000 | RUI | `SYML:TVC;RUI` |
| Russell 2000 | RUT | `SYML:TVC;RUT` |

## Data Emitted

- **IndexChange** events (add/remove) by diffing current TradingView constituent list against DB state
- **IndexSnapshot** at yearly intervals (using existing `shouldTakeSnapshot`)
- **Asset** observations for tickers resolved via OpenFIGI that are new to the system
- **Weight** is set to 0 for all constituents (TradingView does not provide portfolio weights)

## Ticker Resolution

1. Parse `EXCHANGE:TICKER` from the `symbols` array
2. Normalize share class tickers: dots to slashes (e.g. `MOG.A` -> `MOG/A`)
3. Look up against internal asset DB (`figiMap` built from `data.ActiveAssets`)
4. For unresolved tickers, attempt share class resolution (same `resolveShareClass` logic as iShares, using name from the `data[0].rawValues` `description` field)
5. For still-unresolved tickers, enrich via OpenFIGI and emit as new assets
6. **Abort on any unresolved ticker** -- no weight-based exemption. Every TradingView result is a real index constituent.

## Rate Limiting

Randomized delay between 30 seconds and 2 minutes between index fetches. Context-cancellable via `select` so shutdown is clean.

## Configuration

User specifies which indices to fetch via the `indexes` config key on the subscription (comma-separated symbols, e.g. `SPX,MID,RUT`). Defaults to all supported indices if left empty.

## File Structure

- `provider/tradingview.go` -- provider struct, interface methods, fetch logic, response parsing, ticker resolution
- `provider/tradingview_indexes.json` -- embedded index catalog
- `provider/tradingview_test.go` -- tests for response parsing, ticker normalization, index config parsing

## Shared Infrastructure

All index state management functions are already factored into `provider/index_helpers.go` and shared with iShares:
- `diffSnapshots` -- diff two constituent maps to find adds/removes/weight changes
- `emitChangelog` -- emit IndexChange observations for adds and removes
- `emitWeightChanges` -- emit IndexChange observations for weight changes
- `currentIndexMembers` -- load current index state from DB
- `lastSnapshotDate` -- query most recent snapshot date from DB
- `shouldTakeSnapshot` -- determine if a new snapshot is due
- `indexMember` struct -- shared type for constituent with FIGI and weight

The `resolveShareClass` and `firstWordsMatch` functions from `ishares.go` will need to be moved to a shared file (or `index_helpers.go`) so the TradingView provider can use them.
