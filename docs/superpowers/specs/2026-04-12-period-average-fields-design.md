# SEC: Compute Period-Average Fields

**Issue:** #43
**Date:** 2026-04-12

## Problem

Six fields in the Fundamental struct are populated by Sharadar but not by the SEC provider: `AverageAssets`, `EquityAvg`, `InvestedCapitalAverage`, `ROA`, `ROE`, `ROIC`. These require averaging balance sheet values across consecutive periods. A prerequisite field, `InvestedCapital`, is also missing because the derivation engine only supports two-operand subtraction and the formula has five terms.

## Sharadar Formulas (source of truth)

All formulas come from the Sharadar field definitions documented in `data/fundamental.go`.

### Single-period derived field

| Field | Formula | Type |
|---|---|---|
| `InvestedCapital` | `TotalDebt + TotalAssets - Intangibles - CashAndEquivalents - CurrentLiabilities` | int64 |

### Period-average fields (require prior period balance sheet)

| Field | Formula | Type |
|---|---|---|
| `AverageAssets` | `(prior TotalAssets + current TotalAssets) / 2` | int64 |
| `EquityAvg` | `(prior Equity + current Equity) / 2` | int64 |
| `InvestedCapitalAverage` | `(prior InvestedCapital + current InvestedCapital) / 2` | int64 |

### Ratio fields (derived from averages)

| Field | Numerator | Denominator | Type |
|---|---|---|---|
| `ROA` | `NetIncomeCommonStock` | `AverageAssets` | float64 |
| `ROE` | `NetIncomeCommonStock` | `EquityAvg` | float64 |
| `ROIC` | `EBIT` | `InvestedCapitalAverage` | float64 |

## Design

### 1. Extend the derivation engine

**File:** `provider/sec/mapping.go`

Add a new operation `OpLinearCombination` and a `Coefficients []float64` field to `FieldMapping`. The `computeDerived` function gets a new case that multiplies each operand by its corresponding coefficient and sums the results. Existing operations are untouched.

**File:** `provider/sec/mapping_config.go`

Add the `InvestedCapital` mapping using the new operation:

```go
{
    FieldName: "InvestedCapital", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
    Op:           OpLinearCombination,
    Operands:     []string{"TotalDebt", "TotalAssets", "Intangibles", "CashAndEquivalents", "CurrentLiabilities"},
    Coefficients: []float64{1, 1, -1, -1, -1},
}
```

Remove the comment at line 549-553 about InvestedCapital being intentionally omitted. Remove the trailing comment at lines 632-636 about these fields being deferred.

### 2. ComputePeriodAverages function

**File:** `provider/sec/dimensions.go`

New function alongside `DecumulateYTD` and `ComputeTTM`:

```go
func ComputePeriodAverages(current, prior map[string]float64) map[string]float64
```

Behavior:

1. Compute three averages from balance sheet values present in both maps:
   - `AverageAssets = (prior["TotalAssets"] + current["TotalAssets"]) / 2`
   - `EquityAvg = (prior["Equity"] + current["Equity"]) / 2`
   - `InvestedCapitalAverage = (prior["InvestedCapital"] + current["InvestedCapital"]) / 2`

2. Derive three ratios from averaged denominators and current-period flow values:
   - `ROA = current["NetIncomeCommonStock"] / AverageAssets`
   - `ROE = current["NetIncomeCommonStock"] / EquityAvg`
   - `ROIC = current["EBIT"] / InvestedCapitalAverage`

3. Return a map containing only the computed fields. Caller merges into the emit map.

If either period is missing a required input, dependent fields are omitted from the result. Division by zero produces no output (field omitted).

### 3. Integration into emitFundamentals

**File:** `provider/sec/sec.go`

Three insertion points corresponding to the three dimension types:

#### Quarterly (ARQ/MRQ)

After the existing de-cumulation loop, add a new loop over `quarters`:

```
for i := range quarters:
    if i > 0 and gap <= maxQuarterGapDays:
        merge ComputePeriodAverages(quarters[i].arEmit, quarters[i-1].arEmit) into quarters[i].arEmit
        merge ComputePeriodAverages(quarters[i].mrEmit, quarters[i-1].mrEmit) into quarters[i].mrEmit
```

When `i == 0` or the gap exceeds the threshold, the 6 fields are simply absent from that period.

#### Annual (ARY/MRY)

Currently 10-K periods are emitted immediately with no history tracking. Change to:

1. Collect annual periods in an `annuals` slice using the same `quarterData` struct (no rename -- the struct is internal and a comment suffices).
2. After the periods loop, iterate `annuals` and compute averages using `annuals[i-1]` as the prior period. Gap threshold for consecutive annuals: 425 days (~14 months) to handle fiscal year shifts.
3. Emit annual observations from this loop instead of inside the periods loop.

No de-cumulation is needed for annuals (10-K values are full-year), so the annuals loop is simpler than the quarterly one.

#### TTM (ART/MRT)

After computing the TTM field map via `ComputeTTM`, compute averages using:
- **Prior:** balance sheet values from `quarters[i-4]` (the quarter immediately before the TTM window)
- **Current:** the TTM field map (which uses the latest quarter's balance sheet for point-in-time fields)

Requires `i >= 4` (5 quarters of history). When `i < 4`, the 6 fields are absent from the TTM observation.

### 4. No changes to BuildFundamental

`BuildFundamental` in `dimensions.go` already maps field name strings to `Fundamental` struct fields for all 7 fields (`InvestedCapital`, `AverageAssets`, `EquityAvg`, `InvestedCapitalAverage`, `ROA`, `ROE`, `ROIC`). They just never appear in the input map today. Once the upstream computation produces them, they flow through automatically.

### 5. Testing

**File:** `provider/sec/dimensions_test.go`

- `ComputePeriodAverages` with known inputs: verify all 6 output fields match expected values.
- Missing-input cases: prior lacks Equity, current lacks EBIT, etc. Confirm dependent fields are omitted and independent fields still compute.
- Division by zero: average denominator is zero, confirm ratio is omitted.

**File:** `provider/sec/mapping_test.go`

- `OpLinearCombination` in `computeDerived`: verify InvestedCapital formula produces correct result.
- Missing operand: confirm field is omitted.

**File:** `provider/sec/dimensions_test.go` (emitFundamentals integration)

- Use Apple testdata (`CIK0000320193.json`) to verify a few quarterly and annual periods produce the expected average and ratio values.

**Final validation:** Run `pvdata compare-fundamentals` against real Sharadar data to confirm the ~84 diffs from issue #43 disappear.
