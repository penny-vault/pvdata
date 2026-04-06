# Fix FIGI Resolution for Index Imports

## Problem

The SP500 import silently loses data. Of 1877 changelog entries in the source, only 780 are saved to the database because:

1. **`importSP500Rows` queries only active assets** via `data.ActiveAssets`, missing all delisted tickers.
2. **`figi.LookupFigi` does not request unlisted equities** from OpenFIGI, so delisted tickers that OpenFIGI knows about are not found.
3. **`figi.Enrich` skips delisted assets** entirely, so even if we passed delisted tickers to it, they'd be ignored.
4. **`IndexChange.SaveDB` silently drops rows** where `CompositeFigi == ""`, discarding data with no error or warning.

The source data is complete and internally consistent -- adds/removes perfectly reconstruct every quarterly snapshot. The import pipeline fails to resolve FIGIs for historical tickers and then silently discards the unresolved entries.

## Design

### 1. AllAssets query (data/asset.go)

Add `AllAssets(ctx context.Context, conn *pgxpool.Conn) ([]*Asset, error)` alongside the existing `ActiveAssets`. Same query without the `WHERE active = true` filter.

### 2. OpenFIGI unlisted equities support (figi/openfigi.go)

Add an `IncludeUnlistedEquities bool` field to `OpenFigiQuery`. When set, the JSON payload includes `"includeUnlistedEquities": true`, which tells OpenFIGI to return results for delisted securities.

Add a new function `LookupFigiWithOptions` (or add an options parameter to `LookupFigi`) that allows the caller to request unlisted equities. The existing `LookupFigi` and `Enrich` functions keep their current behavior -- active-only by default. Callers that need delisted resolution call the new function directly.

### 3. Synthetic FIGI generation (figi/synthetic.go)

New function: `GenerateSyntheticFIGI(ticker, name string) string`

Format follows the FIGI standard:
- Positions 1-2: `PV` (our issuer prefix)
- Position 3: `G` (required by the standard)
- Positions 4-11: 8 characters derived deterministically from a hash of ticker + name, using only valid FIGI characters (consonants and digits, no vowels)
- Position 12: check digit computed per the FIGI modified Luhn algorithm

The function is deterministic -- the same ticker + name always produces the same FIGI.

### 4. IndexChange.SaveDB error on empty FIGI (data/index.go)

Remove the silent `return nil` when `CompositeFigi == ""`. Replace with:

```go
return fmt.Errorf("index change for ticker %s has empty composite FIGI", idx.Ticker)
```

### 5. Import flow update (provider/sharadar/sharadar_import.go)

Update `importSP500Rows` resolution pipeline:

1. Build FIGI map from `AllAssets` (not `ActiveAssets`)
2. Collect all unique tickers from the parquet rows
3. For any ticker not in the FIGI map, call `LookupFigi` with `includeUnlistedEquities: true`
4. For any ticker still unresolved, call `GenerateSyntheticFIGI` and emit an `AssetObject` observation so the synthetic asset is persisted to the database
5. Proceed with building snapshots and changelog entries -- every ticker now has a FIGI

## Files changed

| File | Change |
|------|--------|
| `data/asset.go` | Add `AllAssets` function |
| `data/index.go` | Error on empty FIGI in `IndexChange.SaveDB` |
| `figi/openfigi.go` | Add `IncludeUnlistedEquities` to query struct, add function for unlisted lookups |
| `figi/synthetic.go` | New file: `GenerateSyntheticFIGI` with FIGI-standard format and check digit |
| `figi/synthetic_test.go` | Tests for synthetic FIGI generation (format, determinism, check digit) |
| `provider/sharadar/sharadar_import.go` | Update `importSP500Rows` to use `AllAssets`, OpenFIGI with unlisted, and synthetic fallback |
