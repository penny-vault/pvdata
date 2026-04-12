# SEC: Fix TTM by synthesizing Q4 from 10-K annual data

**Issue:** #42

## Problem

The SEC provider's TTM computation sums 4 consecutive 10-Q periods, but no company files a 10-Q for its fiscal year-end quarter (Q4). Q4 data only exists in the 10-K annual filing. This means the `quarters` slice in `emitFundamentals()` has only 3 quarters per fiscal year, and `ComputeTTM` sums 4 consecutive 10-Q periods that skip Q4 entirely.

For AAPL (FY ends September), ART at date_key 2024-12-31:
- SEC computes: Q1FY25 + Q3FY24 + Q2FY24 + Q1FY24 = 115.3B net income
- Correct: Q1FY25 + Q4FY24 + Q3FY24 + Q2FY24 = 96.2B net income

Verified that Q4 = annual - sum(Q1+Q2+Q3) is exact (not approximate) for US 10-K/10-Q filers: 98.2% exact match across 4125 companies in Sharadar data, with the remaining ~2% being foreign filers (20-F/6-K) outside the SEC provider's universe.

## Fix

In `emitFundamentals()`, after collecting 10-Q periods into the `quarters` slice and de-cumulating YTD values, synthesize a Q4 entry for each 10-K period.

### Identifying the fiscal year's 10-Q quarters

Walk backwards through the sorted `quarters` slice from the 10-K's period end. Take consecutive quarters whose period end is within ~365 days of the 10-K period end and within ~120 days of each other (same gap logic as existing de-cumulation). Expect exactly 3 quarters. If fewer are found, skip Q4 synthesis for that fiscal year and log a warning.

### Computing synthetic Q4 fields

- **Flow fields** (income statement, cash flow): Q4 = 10-K annual value - sum(Q1+Q2+Q3 de-cumulated values)
- **Point-in-time fields** (balance sheet): Q4 = 10-K value directly
- **Metric fields** (ratios): recompute from Q4 flow/point-in-time values using existing `computeDerived()`

### Synthetic Q4 period metadata

- `PeriodEnd`: the 10-K's period end (e.g., 2024-09-28 for AAPL FY2024)
- `FormType`: "10-Q" (so existing TTM and emission code treats it as a quarter)
- `ARFiledDate` / `MRFiledDate`: copied from the 10-K period

### What the synthetic Q4 produces

- ARQ and MRQ observations for the fiscal year-end quarter (currently missing)
- Correct TTM: `ComputeTTM` now sees all 4 quarters per fiscal year

## Scope boundary

The `compare_fundamentals` command should also exclude foreign issuers (those without 10-Q filings) to avoid noise from companies the SEC provider cannot process. This is a small separate change included in the same PR.

## Files modified

- `provider/sec/sec.go` -- `emitFundamentals()`: synthesize Q4 entries, insert into quarters slice
- `provider/sec/sec_test.go` or `provider/sec/dimensions_test.go` -- new test cases
- `cmd/compare_fundamentals.go` -- filter out foreign issuers

## Testing

Unit tests with synthetic `CompanyFacts` data:

1. Company with 3 10-Q periods + 1 10-K: verify Q4 ARQ/MRQ emitted with correct single-quarter values, and ART/MRT have correct TTM (sum of all 4 quarters)
2. Company with only 2 10-Q quarters before a 10-K: no synthetic Q4 emitted, warning logged
3. Company whose FY aligns with calendar year (Q4 = Oct-Dec): still works correctly
