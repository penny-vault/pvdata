# Fix net_cash_flow_debt and net_cash_flow_invest Resolution

**Issue:** #41
**Date:** 2026-04-12

## Problem

`NetCashFlowDebt` and `NetCashFlowInvest` are currently `MappingDirect` fields
that pick the first matching XBRL tag. Both fields represent **net** values that
require combining proceeds and payments, but the pick-first behavior grabs only
one side of the equation.

**NetCashFlowDebt** picks either `ProceedsFromIssuanceOfLongTermDebt` or
`RepaymentsOfLongTermDebt` -- never both. This produces sign flips and magnitude
mismatches vs Sharadar across quarters.

**NetCashFlowInvest** picks the first match from a list of five tags (payments
and proceeds mixed together). For AAPL, it grabs
`PaymentsToAcquireAvailableForSaleSecuritiesDebt` (gross purchases = 15.3B)
instead of netting against sales/maturities (Sharadar = 2.1B).

## Approach

Convert both fields from `MappingDirect` to `MappingDerived` using internal-only
sub-fields and `OpLinearCombination`. This follows the existing pattern used by
`Investments`, `Receivables`, and `InvestedCapital`.

Internal sub-fields use a `_` prefix convention and are not listed in
`dimensions.go`, so they never reach the `Fundamental` struct.

## Design

### NetCashFlowDebt

Sharadar definition: "net cash inflow (outflow) from issuance (repayment) of
debt securities."

**Internal sub-fields** (`MappingDirect`, `StmtFlow`, `int64`):

| Sub-field | XBRL Tags (priority order) |
|---|---|
| `_proceedsDebt` | `ProceedsFromIssuanceOfLongTermDebt`, `ProceedsFromIssuanceOfDebt`, `ProceedsFromDebtNetOfIssuanceCosts` |
| `_repaymentsDebt` | `RepaymentsOfLongTermDebt`, `RepaymentsOfDebt`, `RepaymentsOfLongTermDebtAndCapitalSecurities` |

Neither sub-field uses `Negate` -- the raw XBRL values are kept as-is. Proceeds
are reported as positive, repayments as positive. The linear combination applies
the sign:

**Derived field:**
```
NetCashFlowDebt = (+1 * _proceedsDebt) + (-1 * _repaymentsDebt)
Op: OpLinearCombination
Operands: [_proceedsDebt, _repaymentsDebt]
Coefficients: [+1, -1]
OptionalOperands: true
FallbackTags: [] (no direct XBRL fallback)
```

`OptionalOperands: true` so that if a company reports only one side, we still
produce a value.

### NetCashFlowInvest

Sharadar definition: "net cash inflow (outflow) associated with the acquisition
& disposal of investments, including marketable securities and loan originations."

**Internal sub-fields** (`MappingDirect`, `StmtFlow`, `int64`):

| Sub-field | XBRL Tags (priority order) |
|---|---|
| `_paymentsInvest` | `PaymentsToAcquireInvestments`, `PaymentsToAcquireAvailableForSaleSecuritiesDebt` |
| `_proceedsInvest` | `ProceedsFromSaleAndMaturityOfMarketableSecurities`, `ProceedsFromMaturitiesPrepaymentsAndCallsOfAvailableForSaleSecurities`, `ProceedsFromSaleOfAvailableForSaleSecuritiesDebt` |

**Derived field:**
```
NetCashFlowInvest = (-1 * _paymentsInvest) + (+1 * _proceedsInvest)
Op: OpLinearCombination
Operands: [_paymentsInvest, _proceedsInvest]
Coefficients: [-1, +1]
OptionalOperands: true
FallbackTags: [] (no direct XBRL fallback)
```

Payments are negated (outflow) and proceeds are positive (inflow), matching
Sharadar's sign convention.

## Files Changed

- `provider/sec/mapping_config.go` -- Replace the two `MappingDirect` entries
  with four internal sub-fields and two `MappingDerived` entries. The sub-fields
  must appear before the derived fields in `FieldMappings` (ordering requirement).

## No Changes Required

- `data/fundamental.go` -- No struct changes; field names are unchanged.
- `provider/sec/dimensions.go` -- The `_`-prefixed sub-fields are not in the
  allowlist, so they are naturally ignored.
- `provider/sec/mapping.go` -- `OpLinearCombination` with `OptionalOperands` is
  already fully supported.
- `provider/sec/synthesize_q4.go` -- Sub-fields are `StmtFlow`, so Q4
  de-cumulation applies automatically.

## Verification

Run `pvdata compare --ticker AAPL --source sec --source sharadar` and confirm
that `net_cash_flow_debt` and `net_cash_flow_invest` diffs are reduced. The issue
reports ~48 AAPL diffs attributable to these two fields.
