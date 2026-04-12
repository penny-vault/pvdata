# SEC: Reduce Rounding Differences vs Sharadar

**Issue:** #44
**Date:** 2026-04-12

## Problem

`BookValuePerShare` and `TangibleAssetsBookValuePerShare` use `SharesBasic` (point-in-time shares outstanding) as their denominator, but Sharadar computes them as `value / (SharesWA * ShareFactor)`. For standard US equities where `ShareFactor = 1.0`, this means the denominator should be `WeightedAverageShares`.

Additionally, all derived `OpDivide` float64 fields (per-share metrics and ratio metrics) produce full-precision float64 results, while Sharadar delivers values rounded to a fixed number of decimal places. This causes sub-1% comparison diffs even when the formulas and underlying integers agree.

## Changes

### 1. Fix denominators

| Field | Current formula | Corrected formula |
|---|---|---|
| `BookValuePerShare` | `Equity / SharesBasic` | `Equity / WeightedAverageShares` |
| `TangibleAssetsBookValuePerShare` | `TangibleAssetValue / SharesBasic` | `TangibleAssetValue / WeightedAverageShares` |

In `mapping_config.go`, change the second operand from `"SharesBasic"` to `"WeightedAverageShares"` for both fields.

### 2. Add rounding to derived float64 fields

Add an optional `RoundDigits int` field to the `FieldMapping` struct. When `RoundDigits > 0`, `computeDerived` rounds the division result:

```go
result = math.Round(result * math.Pow(10, float64(m.RoundDigits))) / math.Pow(10, float64(m.RoundDigits))
```

Set `RoundDigits` on all `OpDivide` fields that produce `float64` values:

- Per-share metrics: `BookValuePerShare`, `FreeCashFlowPerShare`, `SalesPerShare`, `TangibleAssetsBookValuePerShare`
- Ratio metrics: `GrossMargin`, `ProfitMargin`, `EBITDAMargin`, `CurrentRatio`, `DebtToEquityRatio`, `AssetTurnover`, `ReturnOnSales`

The exact digit count will be determined by inspecting actual Sharadar values. Based on issue examples (`4.816`, `0.466`, `0.26`), 4 decimal places is the likely target.

### 3. Testing

Update existing unit tests for `computeDerived` and `BuildFundamental` to verify:

- `BookValuePerShare` uses `WeightedAverageShares` as its denominator
- `TangibleAssetsBookValuePerShare` uses `WeightedAverageShares` as its denominator
- Rounding is applied when `RoundDigits > 0` (e.g., `1.23456` with `RoundDigits=4` produces `1.2346`)
- Rounding is not applied when `RoundDigits` is 0 (default zero value)

## Files changed

- `provider/sec/mapping_config.go` -- denominator swap + `RoundDigits` field on struct + values on each OpDivide mapping
- `provider/sec/mapping.go` -- rounding logic in `computeDerived`
- `provider/sec/*_test.go` -- updated tests

## Out of scope

- `ShareFactor` computation (not derivable from SEC filings alone)
- XBRL tag selection changes for margins or shares_basic
- Tolerance adjustment in `compare-fundamentals`
