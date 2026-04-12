# SEC: Negate Cash Outflow Fields to Match Sharadar Sign Convention

**Issue:** #39
**Date:** 2026-04-11

## Problem

Several SEC cash flow fields use XBRL's native sign convention (outflows reported as positive values) instead of Sharadar's convention (outflows reported as negative). The magnitudes match exactly; only the sign differs. This produces ~72 diffs in the AAPL comparison.

## Affected Fields

| Field | Primary XBRL Tag | SEC Sign | Sharadar Sign |
|---|---|---|---|
| `CapitalExpenditure` | `PaymentsToAcquirePropertyPlantAndEquipment` | +1996M | -1996M |
| `NetCashFlowCommon` | `PaymentsForRepurchaseOfCommonStock` | +23205M | -23205M |
| `NetCashFlowDividend` | `PaymentsOfDividendsCommonStock` | +3710M | -3710M |
| `NetCashFlowBusiness` | `PaymentsToAcquireBusinessesNetOfCashAcquired` | +137M | -137M |

## Design

### Mechanism: `Negate` flag on `FieldMapping`

Add a `Negate bool` field to the `FieldMapping` struct in `mapping_config.go`. Set it on the four mappings listed above. `ResolveAllFields` in `mapping.go` applies the negation immediately after resolving each direct field, so all downstream consumers (derived fields, TTM, de-cumulation, `BuildFundamental`) see the correctly signed value with no further changes.

### Derived field adjustment

`FreeCashFlow` is currently defined as `OpSubtract: NetCashFlowFromOperations - CapitalExpenditure`. With CapitalExpenditure now negative, this would produce `Operations - (-CapEx) = Operations + CapEx`, which is incorrect.

Fix: change the formula to `OpAdd: NetCashFlowFromOperations + CapitalExpenditure`. This is the standard formula when both operands carry their natural sign (operating cash flow positive, capex negative).

### What doesn't change

- `ResolveFieldsForFiling` -- calls `ResolveAllFields`, gets negation for free
- `DecumulateYTD` -- operates on already-resolved (correctly signed) values
- `ComputeTTM` -- sums already-resolved values
- `BuildFundamental` -- copies values from the resolved map
- `needsDecumulation` -- inspects fact durations, not values

## Tests

- Verify the four negated fields resolve to negative values for AAPL 10-K and 10-Q periods
- Verify `FreeCashFlow` remains positive (operating cash flow minus the magnitude of capex)
- Existing mapping config and engine tests continue to pass
