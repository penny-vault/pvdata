# SEC Rounding Match Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix BookValuePerShare and TangibleAssetsBookValuePerShare to use WeightedAverageShares as denominator, and add configurable rounding to all derived float64 division fields so SEC output matches Sharadar exactly.

**Architecture:** Add a `RoundDigits int` field to the `FieldMapping` struct. When > 0, `computeDerived` rounds the division result to that many decimal places. Change the two per-share fields to use `WeightedAverageShares` instead of `SharesBasic`.

**Tech Stack:** Go, Ginkgo v2 + Gomega

---

### Task 1: Add RoundDigits field and rounding logic

**Files:**
- Modify: `provider/sec/mapping_config.go:44-69` (FieldMapping struct)
- Modify: `provider/sec/mapping.go:277-282` (OpDivide case in computeDerived)
- Test: `provider/sec/mapping_test.go`

- [ ] **Step 1: Write failing test for rounding in computeDerived**

Add a new `Describe` block in `provider/sec/mapping_test.go` after the existing `computeDerived with OptionalOperands` block (line 213):

```go
Describe("computeDerived rounding", func() {
    It("rounds division result when RoundDigits is set", func() {
        resolved := map[string]float64{
            "A": 100,
            "B": 3,
        }
        m := FieldMapping{
            FieldName:   "Ratio",
            Type:        MappingDerived,
            Op:          OpDivide,
            Operands:    []string{"A", "B"},
            RoundDigits: 4,
        }
        val, ok := computeDerived(m, resolved)
        Expect(ok).To(BeTrue())
        Expect(val).To(Equal(33.3333))
    })

    It("does not round when RoundDigits is zero", func() {
        resolved := map[string]float64{
            "A": 100,
            "B": 3,
        }
        m := FieldMapping{
            FieldName: "Ratio",
            Type:      MappingDerived,
            Op:        OpDivide,
            Operands:  []string{"A", "B"},
        }
        val, ok := computeDerived(m, resolved)
        Expect(ok).To(BeTrue())
        Expect(val).To(Equal(100.0 / 3.0))
    })

    It("rounds to 3 decimal places", func() {
        resolved := map[string]float64{
            "A": 74_236_000_000,
            "B": 15_408_095_000,
        }
        m := FieldMapping{
            FieldName:   "BVPS",
            Type:        MappingDerived,
            Op:          OpDivide,
            Operands:    []string{"A", "B"},
            RoundDigits: 3,
        }
        val, ok := computeDerived(m, resolved)
        Expect(ok).To(BeTrue())
        Expect(val).To(Equal(4.818))
    })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race --focus "computeDerived rounding" ./provider/sec/`

Expected: compilation error -- `RoundDigits` field does not exist on `FieldMapping`

- [ ] **Step 3: Add RoundDigits field to FieldMapping struct**

In `provider/sec/mapping_config.go`, add the `RoundDigits` field after `OptionalOperands` (line 68):

```go
// RoundDigits rounds OpDivide results to this many decimal places.
// Zero (the default) means no rounding.
RoundDigits int
```

- [ ] **Step 4: Add rounding logic to computeDerived**

In `provider/sec/mapping.go`, add a `math` import if not already present, then replace the `OpDivide` case (lines 277-282):

```go
case OpDivide:
    if len(vals) < 2 || vals[1] == 0 {
        return 0, false
    }

    result := vals[0] / vals[1]
    if m.RoundDigits > 0 {
        pow := math.Pow(10, float64(m.RoundDigits))
        result = math.Round(result*pow) / pow
    }

    return result, true
```

- [ ] **Step 5: Run test to verify it passes**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race --focus "computeDerived rounding" ./provider/sec/`

Expected: PASS (all 3 specs)

- [ ] **Step 6: Commit**

```bash
git add provider/sec/mapping_config.go provider/sec/mapping.go provider/sec/mapping_test.go
git commit -m "feat(sec): add RoundDigits field to FieldMapping for derived division rounding (#44)"
```

---

### Task 2: Fix denominators and set RoundDigits on all OpDivide fields

**Files:**
- Modify: `provider/sec/mapping_config.go:598-666` (ratio and per-share metric definitions)
- Test: `provider/sec/mapping_test.go`

- [ ] **Step 1: Write failing test for BookValuePerShare denominator**

Add a test in `provider/sec/mapping_test.go` inside the existing `Describe("Mapping Config", ...)` block (after line 75):

```go
It("BookValuePerShare uses WeightedAverageShares as denominator", func() {
    for _, m := range FieldMappings {
        if m.FieldName == "BookValuePerShare" {
            Expect(m.Operands).To(Equal([]string{"Equity", "WeightedAverageShares"}))
            return
        }
    }
    Fail("BookValuePerShare not found in FieldMappings")
})

It("TangibleAssetsBookValuePerShare uses WeightedAverageShares as denominator", func() {
    for _, m := range FieldMappings {
        if m.FieldName == "TangibleAssetsBookValuePerShare" {
            Expect(m.Operands).To(Equal([]string{"TangibleAssetValue", "WeightedAverageShares"}))
            return
        }
    }
    Fail("TangibleAssetsBookValuePerShare not found in FieldMappings")
})

It("all OpDivide float64 fields have RoundDigits set", func() {
    for _, m := range FieldMappings {
        if m.Type == MappingDerived && m.Op == OpDivide && m.ValueType == "float64" {
            Expect(m.RoundDigits).To(BeNumerically(">", 0),
                "derived OpDivide float64 field %s should have RoundDigits set", m.FieldName)
        }
    }
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race --focus "BookValuePerShare uses|TangibleAssetsBookValuePerShare uses|all OpDivide float64" ./provider/sec/`

Expected: FAIL -- BookValuePerShare still uses SharesBasic, RoundDigits is 0

- [ ] **Step 3: Fix denominators and set RoundDigits on all OpDivide float64 fields**

In `provider/sec/mapping_config.go`, update these 11 field definitions:

Replace the GrossMargin block (lines 598-603):
```go
// GrossMargin = GrossProfit / Revenues
{
    FieldName: "GrossMargin", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
    Op:       OpDivide,
    Operands: []string{"GrossProfit", "Revenues"},
    RoundDigits: 4,
},
```

Replace the ProfitMargin block (lines 604-609):
```go
// ProfitMargin = NetIncomeCommonStock / Revenues
{
    FieldName: "ProfitMargin", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
    Op:       OpDivide,
    Operands: []string{"NetIncomeCommonStock", "Revenues"},
    RoundDigits: 4,
},
```

Replace the EBITDAMargin block (lines 610-615):
```go
// EBITDAMargin = EBITDA / Revenues
{
    FieldName: "EBITDAMargin", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
    Op:       OpDivide,
    Operands: []string{"EBITDA", "Revenues"},
    RoundDigits: 4,
},
```

Replace the CurrentRatio block (lines 616-621):
```go
// CurrentRatio = CurrentAssets / CurrentLiabilities
{
    FieldName: "CurrentRatio", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
    Op:       OpDivide,
    Operands: []string{"CurrentAssets", "CurrentLiabilities"},
    RoundDigits: 4,
},
```

Replace the DebtToEquityRatio block (lines 622-627):
```go
// DebtToEquityRatio = TotalDebt / Equity
{
    FieldName: "DebtToEquityRatio", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
    Op:       OpDivide,
    Operands: []string{"TotalDebt", "Equity"},
    RoundDigits: 4,
},
```

Replace the AssetTurnover block (lines 628-633):
```go
// AssetTurnover = Revenues / TotalAssets
{
    FieldName: "AssetTurnover", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
    Op:       OpDivide,
    Operands: []string{"Revenues", "TotalAssets"},
    RoundDigits: 4,
},
```

Replace the ReturnOnSales block (lines 634-639):
```go
// ReturnOnSales = EBIT / Revenues
{
    FieldName: "ReturnOnSales", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
    Op:       OpDivide,
    Operands: []string{"EBIT", "Revenues"},
    RoundDigits: 4,
},
```

Replace the FreeCashFlowPerShare block (lines 643-648):
```go
// FreeCashFlowPerShare = FreeCashFlow / WeightedAverageShares
{
    FieldName: "FreeCashFlowPerShare", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
    Op:       OpDivide,
    Operands: []string{"FreeCashFlow", "WeightedAverageShares"},
    RoundDigits: 4,
},
```

Replace the BookValuePerShare block (lines 649-654) -- **denominator changes from SharesBasic to WeightedAverageShares**:
```go
// BookValuePerShare = Equity / WeightedAverageShares
{
    FieldName: "BookValuePerShare", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
    Op:       OpDivide,
    Operands: []string{"Equity", "WeightedAverageShares"},
    RoundDigits: 4,
},
```

Replace the SalesPerShare block (lines 655-660):
```go
// SalesPerShare = Revenues / WeightedAverageShares
{
    FieldName: "SalesPerShare", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
    Op:       OpDivide,
    Operands: []string{"Revenues", "WeightedAverageShares"},
    RoundDigits: 4,
},
```

Replace the TangibleAssetsBookValuePerShare block (lines 661-666) -- **denominator changes from SharesBasic to WeightedAverageShares**:
```go
// TangibleAssetsBookValuePerShare = TangibleAssetValue / WeightedAverageShares
{
    FieldName: "TangibleAssetsBookValuePerShare", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
    Op:       OpDivide,
    Operands: []string{"TangibleAssetValue", "WeightedAverageShares"},
    RoundDigits: 4,
},
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race --focus "BookValuePerShare uses|TangibleAssetsBookValuePerShare uses|all OpDivide float64" ./provider/sec/`

Expected: PASS (all 3 specs)

- [ ] **Step 5: Run the full test suite to check for regressions**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race ./provider/sec/`

Expected: PASS -- existing tests should still pass. The rounding may cause small value changes in tests that check exact derived float values. If any test fails due to rounding, update the expected value to the rounded result.

- [ ] **Step 6: Run lint**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && golangci-lint run --fix ./provider/sec/...`

Expected: PASS

- [ ] **Step 7: Commit**

```bash
git add provider/sec/mapping_config.go provider/sec/mapping_test.go
git commit -m "feat(sec): fix per-share denominators and add rounding to derived float64 fields (#44)"
```
