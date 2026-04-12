# Period-Average Fields Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Compute 7 fields the SEC provider is missing (`InvestedCapital`, `AverageAssets`, `EquityAvg`, `InvestedCapitalAverage`, `ROA`, `ROE`, `ROIC`) so they match Sharadar exactly.

**Architecture:** Extend the derivation engine with `OpLinearCombination` to support InvestedCapital's 5-term formula. Add a `ComputePeriodAverages` function that takes current and prior period field maps and produces the 6 average/ratio fields. Wire it into `emitFundamentals` after de-cumulation (quarterly), in a new annuals collection loop (annual), and after `ComputeTTM` (TTM).

**Tech Stack:** Go, Ginkgo v2, Gomega.

---

## File Structure

- **Modify:** `provider/sec/mapping_config.go` -- add `Coefficients` field to `FieldMapping`, add `OpLinearCombination` constant, add `InvestedCapital` mapping entry, remove deferred-fields comments
- **Modify:** `provider/sec/mapping.go` -- add `OpLinearCombination` case to `computeDerived`
- **Modify:** `provider/sec/dimensions.go` -- add `ComputePeriodAverages` function, add 7 field mappings to `BuildFundamental`
- **Modify:** `provider/sec/sec.go` -- wire period averages into quarterly, annual, and TTM emission loops
- **Modify:** `provider/sec/mapping_test.go` -- tests for `OpLinearCombination` and `InvestedCapital`
- **Modify:** `provider/sec/dimensions_test.go` -- tests for `ComputePeriodAverages` and integration tests

---

### Task 1: Add OpLinearCombination to the derivation engine

**Files:**
- Modify: `provider/sec/mapping_config.go:36-42` (FormulaOp constants)
- Modify: `provider/sec/mapping_config.go:44-69` (FieldMapping struct)
- Modify: `provider/sec/mapping.go:226-286` (computeDerived)
- Modify: `provider/sec/mapping_test.go`

- [ ] **Step 1: Write failing tests for OpLinearCombination**

Add to `provider/sec/mapping_test.go` inside the `Describe("Mapping Engine"` block, after the existing `computeDerived with OptionalOperands` describe:

```go
Describe("computeDerived with OpLinearCombination", func() {
    It("computes weighted sum of operands", func() {
        resolved := map[string]float64{
            "A": 100,
            "B": 200,
            "C": 30,
            "D": 20,
            "E": 10,
        }
        m := FieldMapping{
            FieldName:    "Result",
            Type:         MappingDerived,
            Op:           OpLinearCombination,
            Operands:     []string{"A", "B", "C", "D", "E"},
            Coefficients: []float64{1, 1, -1, -1, -1},
        }
        val, ok := computeDerived(m, resolved)
        Expect(ok).To(BeTrue())
        Expect(val).To(Equal(240.0))
    })

    It("returns false when any operand is missing", func() {
        resolved := map[string]float64{
            "A": 100,
            "B": 200,
        }
        m := FieldMapping{
            FieldName:    "Result",
            Type:         MappingDerived,
            Op:           OpLinearCombination,
            Operands:     []string{"A", "B", "C"},
            Coefficients: []float64{1, 1, -1},
        }
        _, ok := computeDerived(m, resolved)
        Expect(ok).To(BeFalse())
    })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race --focus "OpLinearCombination" ./provider/sec/`
Expected: compilation error -- `OpLinearCombination` and `Coefficients` are undefined.

- [ ] **Step 3: Add OpLinearCombination constant and Coefficients field**

In `provider/sec/mapping_config.go`, add the new constant after the existing three:

```go
const (
	OpAdd               FormulaOp = "add"                // A + B + ...
	OpSubtract          FormulaOp = "subtract"           // A - B
	OpDivide            FormulaOp = "divide"             // A / B
	OpLinearCombination FormulaOp = "linear_combination" // C0*A + C1*B + ...
)
```

Add the `Coefficients` field to the `FieldMapping` struct, after the `Operands` field:

```go
	// For derived mappings: formula
	Op           FormulaOp // Operation to apply
	Operands     []string  // Field names to use as operands
	Coefficients []float64 // Per-operand multipliers (for OpLinearCombination)
```

- [ ] **Step 4: Implement OpLinearCombination in computeDerived**

In `provider/sec/mapping.go`, add a new case in the `switch m.Op` block inside `computeDerived`, after the `OpDivide` case:

```go
	case OpLinearCombination:
		if len(vals) != len(m.Coefficients) {
			return 0, false
		}

		sum := 0.0
		for i, v := range vals {
			sum += m.Coefficients[i] * v
		}

		return sum, true
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `ginkgo run -race --focus "OpLinearCombination" ./provider/sec/`
Expected: PASS

- [ ] **Step 6: Run the full test suite**

Run: `ginkgo run -race ./provider/sec/`
Expected: all tests pass.

- [ ] **Step 7: Commit**

```bash
git add provider/sec/mapping_config.go provider/sec/mapping.go provider/sec/mapping_test.go
git commit -m "feat(sec): add OpLinearCombination to derivation engine (#43)"
```

---

### Task 2: Add InvestedCapital field mapping

**Files:**
- Modify: `provider/sec/mapping_config.go:590-594` (replace omission comment with mapping)
- Modify: `provider/sec/mapping_config.go:668-678` (update trailing comment)
- Modify: `provider/sec/dimensions.go:730-735` (add to BuildFundamental)
- Modify: `provider/sec/mapping_test.go`

- [ ] **Step 1: Write failing test for InvestedCapital resolution**

Add to `provider/sec/mapping_test.go` inside `Describe("ResolveAllFields"`:

```go
It("resolves InvestedCapital from component fields", func() {
    periodEnd := time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC)
    filed := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

    icCF := &CompanyFacts{
        CIK: 1, EntityName: "Test Co",
        Facts: map[string][]Fact{
            "LongTermDebtCurrent":                        {{End: periodEnd, Filed: filed, Val: 10_000, Form: "10-Q"}},
            "LongTermDebtNoncurrent":                     {{End: periodEnd, Filed: filed, Val: 90_000, Form: "10-Q"}},
            "Assets":                                     {{End: periodEnd, Filed: filed, Val: 350_000, Form: "10-Q"}},
            "IntangibleAssetsNetIncludingGoodwill":        {{End: periodEnd, Filed: filed, Val: 50_000, Form: "10-Q"}},
            "CashAndCashEquivalentsAtCarryingValue":       {{End: periodEnd, Filed: filed, Val: 25_000, Form: "10-Q"}},
            "LiabilitiesCurrent":                         {{End: periodEnd, Filed: filed, Val: 60_000, Form: "10-Q"}},
        },
    }

    resolved := ResolveAllFields(icCF, periodEnd, "10-Q")

    // TotalDebt = DebtCurrent(10_000) + DebtNonCurrent(90_000) = 100_000
    // InvestedCapital = 100_000 + 350_000 - 50_000 - 25_000 - 60_000 = 315_000
    Expect(resolved).To(HaveKey("InvestedCapital"))
    Expect(resolved["InvestedCapital"]).To(Equal(315_000.0))
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `ginkgo run -race --focus "resolves InvestedCapital" ./provider/sec/`
Expected: FAIL -- `InvestedCapital` key not present.

- [ ] **Step 3: Add InvestedCapital mapping and update comments**

In `provider/sec/mapping_config.go`, replace the omission comment (lines 590-594) with:

```go
	// InvestedCapital = TotalDebt + TotalAssets - Intangibles - CashAndEquivalents - CurrentLiabilities
	{
		FieldName: "InvestedCapital", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		Op:           OpLinearCombination,
		Operands:     []string{"TotalDebt", "TotalAssets", "Intangibles", "CashAndEquivalents", "CurrentLiabilities"},
		Coefficients: []float64{1, 1, -1, -1, -1},
	},
```

Update the trailing comment (lines 668-678) to remove the three lines about ROA/ROE/ROIC and AverageAssets/EquityAvg/InvestedCapitalAverage:

```go
	// Note: The following Sharadar fields require market price data and are
	// NOT computable from SEC filings alone. They are intentionally omitted:
	// - MarketCapitalization, EnterpriseValue, PE, PB, PS, PE1, PS1
	// - EVtoEBIT, EVtoEBITDA, DividendYield, PayoutRatio, Price
	// - ShareFactor, FxUSD
```

- [ ] **Step 4: Add InvestedCapital to BuildFundamental**

In `provider/sec/dimensions.go`, add the following after the `TangibleAssetValue` mapping (after line 733):

```go
	if v, ok := fields["InvestedCapital"]; ok {
		f.InvestedCapital = int64(v)
	}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `ginkgo run -race --focus "resolves InvestedCapital" ./provider/sec/`
Expected: PASS

- [ ] **Step 6: Run the full test suite**

Run: `ginkgo run -race ./provider/sec/`
Expected: all tests pass (including the `BuildFundamental coverage` test which checks all FieldMappings entries are referenced).

- [ ] **Step 7: Run lint**

Run: `make lint`
Expected: clean.

- [ ] **Step 8: Commit**

```bash
git add provider/sec/mapping_config.go provider/sec/dimensions.go provider/sec/mapping_test.go
git commit -m "feat(sec): add InvestedCapital field mapping (#43)"
```

---

### Task 3: Implement ComputePeriodAverages

**Files:**
- Modify: `provider/sec/dimensions.go` (add function after `ComputeTTM`)
- Modify: `provider/sec/dimensions_test.go`

- [ ] **Step 1: Write failing tests for ComputePeriodAverages**

Add to `provider/sec/dimensions_test.go` inside the top-level `Describe("Dimensions"` block:

```go
Describe("ComputePeriodAverages", func() {
    It("computes all 6 fields from complete inputs", func() {
        current := map[string]float64{
            "TotalAssets":          400_000,
            "Equity":              200_000,
            "InvestedCapital":     300_000,
            "NetIncomeCommonStock": 50_000,
            "EBIT":                60_000,
        }
        prior := map[string]float64{
            "TotalAssets":      360_000,
            "Equity":          180_000,
            "InvestedCapital": 280_000,
        }

        result := ComputePeriodAverages(current, prior)

        // Averages
        Expect(result["AverageAssets"]).To(Equal(380_000.0))
        Expect(result["EquityAvg"]).To(Equal(190_000.0))
        Expect(result["InvestedCapitalAverage"]).To(Equal(290_000.0))

        // Ratios
        Expect(result["ROA"]).To(BeNumerically("~", 50_000.0/380_000.0, 1e-10))
        Expect(result["ROE"]).To(BeNumerically("~", 50_000.0/190_000.0, 1e-10))
        Expect(result["ROIC"]).To(BeNumerically("~", 60_000.0/290_000.0, 1e-10))
    })

    It("omits average when prior is missing the balance sheet field", func() {
        current := map[string]float64{
            "TotalAssets":          400_000,
            "Equity":              200_000,
            "NetIncomeCommonStock": 50_000,
            "EBIT":                60_000,
        }
        prior := map[string]float64{
            "TotalAssets": 360_000,
            // Equity missing
        }

        result := ComputePeriodAverages(current, prior)

        Expect(result).To(HaveKey("AverageAssets"))
        Expect(result).To(HaveKey("ROA"))
        Expect(result).NotTo(HaveKey("EquityAvg"))
        Expect(result).NotTo(HaveKey("ROE"))
    })

    It("omits ratio when numerator is missing", func() {
        current := map[string]float64{
            "TotalAssets": 400_000,
            "Equity":     200_000,
            // NetIncomeCommonStock missing
        }
        prior := map[string]float64{
            "TotalAssets": 360_000,
            "Equity":     180_000,
        }

        result := ComputePeriodAverages(current, prior)

        Expect(result).To(HaveKey("AverageAssets"))
        Expect(result).To(HaveKey("EquityAvg"))
        Expect(result).NotTo(HaveKey("ROA"))
        Expect(result).NotTo(HaveKey("ROE"))
    })

    It("omits ratio when average denominator is zero", func() {
        current := map[string]float64{
            "TotalAssets":          100,
            "NetIncomeCommonStock": 50,
        }
        prior := map[string]float64{
            "TotalAssets": -100, // average = 0
        }

        result := ComputePeriodAverages(current, prior)

        Expect(result).To(HaveKey("AverageAssets"))
        Expect(result["AverageAssets"]).To(Equal(0.0))
        Expect(result).NotTo(HaveKey("ROA"))
    })

    It("returns empty map when prior is nil", func() {
        current := map[string]float64{
            "TotalAssets":          400_000,
            "Equity":              200_000,
            "NetIncomeCommonStock": 50_000,
        }

        result := ComputePeriodAverages(current, nil)
        Expect(result).To(BeEmpty())
    })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race --focus "ComputePeriodAverages" ./provider/sec/`
Expected: compilation error -- `ComputePeriodAverages` is undefined.

- [ ] **Step 3: Implement ComputePeriodAverages**

Add to `provider/sec/dimensions.go`, after the `ComputeTTM` function (after line 404):

```go
// ComputePeriodAverages computes period-average balance sheet fields and the
// ratios that depend on them. current is the emit-ready field map for the
// period being computed; prior is the emit-ready field map for the immediately
// preceding period of the same type (prior quarter for quarterly, prior year
// for annual, quarter before the TTM window for TTM).
//
// Returns a map containing only the computed fields; the caller merges these
// into the emit map. Fields whose inputs are missing are silently omitted.
func ComputePeriodAverages(current, prior map[string]float64) map[string]float64 {
	result := make(map[string]float64)

	if prior == nil {
		return result
	}

	// Helper: compute (prior[field] + current[field]) / 2 if both exist.
	avg := func(field string) (float64, bool) {
		cv, cOK := current[field]
		pv, pOK := prior[field]
		if !cOK || !pOK {
			return 0, false
		}

		return (pv + cv) / 2, true
	}

	// Averages
	if v, ok := avg("TotalAssets"); ok {
		result["AverageAssets"] = v
	}

	if v, ok := avg("Equity"); ok {
		result["EquityAvg"] = v
	}

	if v, ok := avg("InvestedCapital"); ok {
		result["InvestedCapitalAverage"] = v
	}

	// Ratios (only when both numerator and denominator are available and non-zero)
	ratio := func(numField, denomField string) (float64, bool) {
		num, nOK := current[numField]
		denom, dOK := result[denomField]
		if !nOK || !dOK || denom == 0 {
			return 0, false
		}

		return num / denom, true
	}

	if v, ok := ratio("NetIncomeCommonStock", "AverageAssets"); ok {
		result["ROA"] = v
	}

	if v, ok := ratio("NetIncomeCommonStock", "EquityAvg"); ok {
		result["ROE"] = v
	}

	if v, ok := ratio("EBIT", "InvestedCapitalAverage"); ok {
		result["ROIC"] = v
	}

	return result
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `ginkgo run -race --focus "ComputePeriodAverages" ./provider/sec/`
Expected: PASS

- [ ] **Step 5: Run the full test suite**

Run: `ginkgo run -race ./provider/sec/`
Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add provider/sec/dimensions.go provider/sec/dimensions_test.go
git commit -m "feat(sec): add ComputePeriodAverages function (#43)"
```

---

### Task 4: Add period-average fields to BuildFundamental

**Files:**
- Modify: `provider/sec/dimensions.go:730-735` (add field mappings)

- [ ] **Step 1: Write failing test**

Add to `provider/sec/dimensions_test.go` inside `Describe("BuildFundamental coverage"`:

```go
It("references all period-average fields", func() {
    src, err := os.ReadFile("dimensions.go")
    Expect(err).NotTo(HaveOccurred())
    srcStr := string(src)

    avgFields := []string{
        "AverageAssets", "EquityAvg", "InvestedCapitalAverage",
        "ROA", "ROE", "ROIC",
    }

    var missing []string
    for _, name := range avgFields {
        pattern := fmt.Sprintf(`fields[%q]`, name)
        if !strings.Contains(srcStr, pattern) {
            missing = append(missing, name)
        }
    }

    Expect(missing).To(BeEmpty(),
        "period-average fields not referenced in BuildFundamental: %v", missing)
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `ginkgo run -race --focus "references all period-average" ./provider/sec/`
Expected: FAIL -- fields not yet present in BuildFundamental.

- [ ] **Step 3: Add the 6 field mappings to BuildFundamental**

In `provider/sec/dimensions.go`, add the following after the `InvestedCapital` mapping (added in Task 2) inside `BuildFundamental`:

```go
	if v, ok := fields["AverageAssets"]; ok {
		f.AverageAssets = int64(v)
	}

	if v, ok := fields["EquityAvg"]; ok {
		f.EquityAvg = int64(v)
	}

	if v, ok := fields["InvestedCapitalAverage"]; ok {
		f.InvestedCapitalAverage = int64(v)
	}

	if v, ok := fields["ROA"]; ok {
		f.ROA = v
	}

	if v, ok := fields["ROE"]; ok {
		f.ROE = v
	}

	if v, ok := fields["ROIC"]; ok {
		f.ROIC = v
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `ginkgo run -race --focus "references all period-average" ./provider/sec/`
Expected: PASS

- [ ] **Step 5: Run the full test suite**

Run: `ginkgo run -race ./provider/sec/`
Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add provider/sec/dimensions.go provider/sec/dimensions_test.go
git commit -m "feat(sec): add period-average fields to BuildFundamental (#43)"
```

---

### Task 5: Wire period averages into quarterly emission

**Files:**
- Modify: `provider/sec/sec.go:602-638` (add average pass after de-cumulation, before quarterly emission)
- Modify: `provider/sec/dimensions_test.go`

- [ ] **Step 1: Write failing integration test for quarterly period averages**

Add to `provider/sec/dimensions_test.go` inside the top-level `Describe("Dimensions"` block:

```go
Describe("quarterly period averages", func() {
    It("computes averages from consecutive quarters", func() {
        cf := &CompanyFacts{
            CIK:        1,
            EntityName: "Avg Co",
            Facts:      make(map[string][]Fact),
        }

        q1End := time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC)
        q2End := time.Date(2024, 6, 30, 0, 0, 0, 0, time.UTC)
        q1Filed := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
        q2Filed := time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC)
        fyStart := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

        // Balance sheet (instant)
        cf.Facts["Assets"] = []Fact{
            {End: q1End, Filed: q1Filed, Val: 1000, Form: "10-Q"},
            {End: q2End, Filed: q2Filed, Val: 1200, Form: "10-Q"},
        }
        cf.Facts["StockholdersEquity"] = []Fact{
            {End: q1End, Filed: q1Filed, Val: 500, Form: "10-Q"},
            {End: q2End, Filed: q2Filed, Val: 600, Form: "10-Q"},
        }

        // Income statement (duration, quarterly facts)
        cf.Facts["NetIncomeLossAvailableToCommonStockholdersBasic"] = []Fact{
            {Start: fyStart, End: q1End, Filed: q1Filed, Val: 100, Form: "10-Q"},
            {Start: q1End.AddDate(0, 0, 1), End: q2End, Filed: q2Filed, Val: 120, Form: "10-Q"},
        }
        cf.Facts["Revenues"] = []Fact{
            {Start: fyStart, End: q1End, Filed: q1Filed, Val: 500, Form: "10-Q"},
            {Start: q1End.AddDate(0, 0, 1), End: q2End, Filed: q2Filed, Val: 600, Form: "10-Q"},
        }

        asset := AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST00", CIK: 1}
        out := make(chan *data.Observation, 256)
        sub := &library.Subscription{Name: "test"}

        done := make(chan struct{})
        var q1Fund, q2Fund *data.Fundamental

        go func() {
            for obs := range out {
                f := obs.Fundamental
                dateKey := obs.ObservationDate.Format("2006-01-02")
                if f.Dimension == "ARQ" && dateKey == "2024-03-31" {
                    q1Fund = f
                }
                if f.Dimension == "ARQ" && dateKey == "2024-06-30" {
                    q2Fund = f
                }
            }
            close(done)
        }()

        numObs := 0
        emitFundamentals(cf, asset, sub, time.Time{}, out, &numObs)
        close(out)
        <-done

        // Q1: no prior quarter, averages should be zero (absent)
        Expect(q1Fund).NotTo(BeNil())
        Expect(q1Fund.AverageAssets).To(Equal(int64(0)))
        Expect(q1Fund.ROA).To(Equal(0.0))

        // Q2: has prior quarter
        Expect(q2Fund).NotTo(BeNil())
        Expect(q2Fund.AverageAssets).To(Equal(int64(1100)))  // (1000+1200)/2
        Expect(q2Fund.EquityAvg).To(Equal(int64(550)))       // (500+600)/2
        Expect(q2Fund.ROA).To(BeNumerically("~", 120.0/1100.0, 1e-10))
        Expect(q2Fund.ROE).To(BeNumerically("~", 120.0/550.0, 1e-10))
    })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `ginkgo run -race --focus "computes averages from consecutive quarters" ./provider/sec/`
Expected: FAIL -- `q2Fund.AverageAssets` is 0.

- [ ] **Step 3: Wire period averages into the quarterly loop**

In `provider/sec/sec.go`, after the de-cumulation loop (after line 602, the closing `}`), add a new loop:

```go
	// Compute period-average fields (AverageAssets, EquityAvg,
	// InvestedCapitalAverage) and derived ratios (ROA, ROE, ROIC) from
	// consecutive quarterly balance sheet values.
	for i := range quarters {
		q := &quarters[i]

		if i == 0 {
			continue
		}

		prev := &quarters[i-1]
		gapDays := q.period.PeriodEnd.Sub(prev.period.PeriodEnd).Hours() / 24

		if gapDays > maxQuarterGapDays {
			continue
		}

		for k, v := range ComputePeriodAverages(q.arEmit, prev.arEmit) {
			q.arEmit[k] = v
		}

		for k, v := range ComputePeriodAverages(q.mrEmit, prev.mrEmit) {
			q.mrEmit[k] = v
		}
	}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `ginkgo run -race --focus "computes averages from consecutive quarters" ./provider/sec/`
Expected: PASS

- [ ] **Step 5: Run the full test suite**

Run: `ginkgo run -race ./provider/sec/`
Expected: all tests pass.

- [ ] **Step 6: Commit**

```bash
git add provider/sec/sec.go provider/sec/dimensions_test.go
git commit -m "feat(sec): wire period averages into quarterly emission (#43)"
```

---

### Task 6: Wire period averages into annual emission

**Files:**
- Modify: `provider/sec/sec.go:536-568` (refactor annual emission to collect then emit)
- Modify: `provider/sec/dimensions_test.go`

- [ ] **Step 1: Write failing integration test for annual period averages**

Add to `provider/sec/dimensions_test.go` inside the top-level `Describe("Dimensions"` block:

```go
Describe("annual period averages", func() {
    It("computes averages from consecutive annual periods", func() {
        cf := &CompanyFacts{
            CIK:        1,
            EntityName: "Annual Co",
            Facts:      make(map[string][]Fact),
        }

        fy1End := time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC)
        fy2End := time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC)
        fy1Filed := time.Date(2023, 11, 1, 0, 0, 0, 0, time.UTC)
        fy2Filed := time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC)

        // Balance sheet (instant)
        cf.Facts["Assets"] = []Fact{
            {End: fy1End, Filed: fy1Filed, Val: 2000, Form: "10-K"},
            {End: fy2End, Filed: fy2Filed, Val: 2400, Form: "10-K"},
        }
        cf.Facts["StockholdersEquity"] = []Fact{
            {End: fy1End, Filed: fy1Filed, Val: 800, Form: "10-K"},
            {End: fy2End, Filed: fy2Filed, Val: 1000, Form: "10-K"},
        }

        // Income statement (duration)
        cf.Facts["NetIncomeLossAvailableToCommonStockholdersBasic"] = []Fact{
            {Start: time.Date(2022, 10, 1, 0, 0, 0, 0, time.UTC), End: fy1End, Filed: fy1Filed, Val: 200, Form: "10-K"},
            {Start: time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC), End: fy2End, Filed: fy2Filed, Val: 250, Form: "10-K"},
        }
        cf.Facts["Revenues"] = []Fact{
            {Start: time.Date(2022, 10, 1, 0, 0, 0, 0, time.UTC), End: fy1End, Filed: fy1Filed, Val: 1000, Form: "10-K"},
            {Start: time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC), End: fy2End, Filed: fy2Filed, Val: 1200, Form: "10-K"},
        }

        asset := AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST00", CIK: 1}
        out := make(chan *data.Observation, 256)
        sub := &library.Subscription{Name: "test"}

        done := make(chan struct{})
        var fy1Fund, fy2Fund *data.Fundamental

        go func() {
            for obs := range out {
                f := obs.Fundamental
                dateKey := obs.ObservationDate.Format("2006-01-02")
                if f.Dimension == "ARY" && dateKey == "2023-12-31" {
                    fy1Fund = f
                }
                if f.Dimension == "ARY" && dateKey == "2024-12-31" {
                    fy2Fund = f
                }
            }
            close(done)
        }()

        numObs := 0
        emitFundamentals(cf, asset, sub, time.Time{}, out, &numObs)
        close(out)
        <-done

        // FY1: no prior year, averages absent
        Expect(fy1Fund).NotTo(BeNil())
        Expect(fy1Fund.AverageAssets).To(Equal(int64(0)))

        // FY2: has prior year
        Expect(fy2Fund).NotTo(BeNil())
        Expect(fy2Fund.AverageAssets).To(Equal(int64(2200)))  // (2000+2400)/2
        Expect(fy2Fund.EquityAvg).To(Equal(int64(900)))       // (800+1000)/2
        Expect(fy2Fund.ROA).To(BeNumerically("~", 250.0/2200.0, 1e-10))
        Expect(fy2Fund.ROE).To(BeNumerically("~", 250.0/900.0, 1e-10))
    })
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `ginkgo run -race --focus "computes averages from consecutive annual" ./provider/sec/`
Expected: FAIL -- `fy2Fund.AverageAssets` is 0.

- [ ] **Step 3: Refactor annual emission to collect, compute averages, then emit**

In `provider/sec/sec.go`, the current annual emission block (inside `for _, p := range periods`, the `if p.FormType == "10-K"` block starting around line 538) emits immediately. Refactor it to collect annuals and emit later.

First, add a new type alias alongside `quarterData` (around line 495). The existing `quarterData` struct works for annuals too -- reuse it:

After the `var quarters []quarterData` line (line 503), add:

```go
	var annuals []quarterData
```

Replace the existing `if p.FormType == "10-K"` block (lines 538-568) with:

```go
		if p.FormType == "10-K" {
			annuals = append(annuals, quarterData{period: p, arFields: arFields, mrFields: mrFields})
		}
```

Then, after the quarterly period-average loop (the one added in Task 5) and before the quarterly emission loop (the `// Emit quarterly observations` comment around line 604), add the annual average + emission block:

```go
	// Compute period averages and emit annual observations.
	const maxAnnualGapDays = 425 // ~14 months, handles fiscal year shifts

	for i := range annuals {
		a := &annuals[i]

		// Annual data is full-year; no de-cumulation needed. Store
		// directly in arEmit/mrEmit.
		a.arEmit = a.arFields
		a.mrEmit = a.mrFields

		if i > 0 {
			prev := &annuals[i-1]
			gapDays := a.period.PeriodEnd.Sub(prev.period.PeriodEnd).Hours() / 24

			if gapDays <= maxAnnualGapDays {
				for k, v := range ComputePeriodAverages(a.arEmit, prev.arEmit) {
					a.arEmit[k] = v
				}

				for k, v := range ComputePeriodAverages(a.mrEmit, prev.mrEmit) {
					a.mrEmit[k] = v
				}
			}
		}

		calendarDate := NormalizeEventDate(a.period.PeriodEnd, a.period.FormType)

		if !since.IsZero() && a.period.MRFiledDate.Before(since) {
			continue
		}

		// ARY
		fundamental := BuildFundamental(a.arEmit, asset.Ticker, asset.CompositeFigi, "ARY",
			a.period.ARFiledDate, calendarDate, a.period.PeriodEnd, a.period.ARFiledDate)
		out <- &data.Observation{
			Fundamental:      fundamental,
			ObservationDate:  calendarDate,
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}

		*numObservations++

		// MRY
		fundamental = BuildFundamental(a.mrEmit, asset.Ticker, asset.CompositeFigi, "MRY",
			a.period.PeriodEnd, calendarDate, a.period.PeriodEnd, a.period.MRFiledDate)
		out <- &data.Observation{
			Fundamental:      fundamental,
			ObservationDate:  calendarDate,
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}

		*numObservations++

		periodsEmitted++
	}
```

Also move the `latestPeriodEnd`/`latestPeriodCoverage` tracking out of the `if p.FormType == "10-K"` block if needed -- it was above the removed block so should still work. Verify that the coverage tracking at lines 531-534 still runs for both 10-K and 10-Q periods (it does, since it's before the FormType check).

- [ ] **Step 4: Run test to verify it passes**

Run: `ginkgo run -race --focus "computes averages from consecutive annual" ./provider/sec/`
Expected: PASS

- [ ] **Step 5: Run the full test suite**

Run: `ginkgo run -race ./provider/sec/`
Expected: all tests pass. The existing `emitFundamentals` tests should still pass because they only use quarterly data (no 10-K periods).

- [ ] **Step 6: Run lint**

Run: `make lint`
Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add provider/sec/sec.go provider/sec/dimensions_test.go
git commit -m "feat(sec): wire period averages into annual emission (#43)"
```

---

### Task 7: Wire period averages into TTM emission

**Files:**
- Modify: `provider/sec/sec.go:640-737` (add averages after ComputeTTM)
- Modify: `provider/sec/dimensions_test.go`

- [ ] **Step 1: Write failing integration test for TTM period averages**

Add to `provider/sec/dimensions_test.go` inside the top-level `Describe("Dimensions"` block:

```go
Describe("TTM period averages", func() {
    It("computes averages using quarter before TTM window", func() {
        cf := &CompanyFacts{
            CIK:        1,
            EntityName: "TTM Co",
            Facts:      make(map[string][]Fact),
        }

        // 5 consecutive quarters: q0 through q4.
        // TTM window for q4 = [q1..q4], prior balance sheet = q0.
        ends := []time.Time{
            time.Date(2023, 3, 31, 0, 0, 0, 0, time.UTC),
            time.Date(2023, 6, 30, 0, 0, 0, 0, time.UTC),
            time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC),
            time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC),
            time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
        }

        for i, end := range ends {
            start := end.AddDate(0, 0, -89)
            filed := end.AddDate(0, 0, 30)
            assets := float64(1000 + i*100) // 1000, 1100, 1200, 1300, 1400
            equity := float64(500 + i*50)   // 500, 550, 600, 650, 700

            cf.Facts["Assets"] = append(cf.Facts["Assets"],
                Fact{End: end, Filed: filed, Val: assets, Form: "10-Q"})
            cf.Facts["StockholdersEquity"] = append(cf.Facts["StockholdersEquity"],
                Fact{End: end, Filed: filed, Val: equity, Form: "10-Q"})
            cf.Facts["Revenues"] = append(cf.Facts["Revenues"],
                Fact{Start: start, End: end, Filed: filed, Val: 200, Form: "10-Q"})
            cf.Facts["NetIncomeLossAvailableToCommonStockholdersBasic"] = append(
                cf.Facts["NetIncomeLossAvailableToCommonStockholdersBasic"],
                Fact{Start: start, End: end, Filed: filed, Val: 50, Form: "10-Q"})
            cf.Facts["NetIncomeLoss"] = append(cf.Facts["NetIncomeLoss"],
                Fact{Start: start, End: end, Filed: filed, Val: 50, Form: "10-Q"})
        }

        asset := AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST00", CIK: 1}
        out := make(chan *data.Observation, 256)
        sub := &library.Subscription{Name: "test"}

        done := make(chan struct{})
        var ttmFund *data.Fundamental

        go func() {
            for obs := range out {
                if obs.Fundamental.Dimension == "ART" {
                    ttmFund = obs.Fundamental
                }
            }
            close(done)
        }()

        numObs := 0
        emitFundamentals(cf, asset, sub, time.Time{}, out, &numObs)
        close(out)
        <-done

        // TTM window = q1..q4, prior = q0
        // AverageAssets = (q0 assets + q4 assets) / 2 = (1000 + 1400) / 2 = 1200
        // EquityAvg = (q0 equity + q4 equity) / 2 = (500 + 700) / 2 = 600
        // TTM NetIncomeCommonStock = 50*4 = 200
        // ROA = 200 / 1200
        Expect(ttmFund).NotTo(BeNil())
        Expect(ttmFund.AverageAssets).To(Equal(int64(1200)))
        Expect(ttmFund.EquityAvg).To(Equal(int64(600)))
        Expect(ttmFund.ROA).To(BeNumerically("~", 200.0/1200.0, 1e-10))
        Expect(ttmFund.ROE).To(BeNumerically("~", 200.0/600.0, 1e-10))
    })

    It("skips TTM averages when fewer than 5 quarters", func() {
        // Only 4 quarters -- enough for TTM sums but no prior for averages
        cf := buildSyntheticQuarterlyFacts([]time.Time{
            time.Date(2023, 6, 30, 0, 0, 0, 0, time.UTC),
            time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC),
            time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC),
            time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
        })

        asset := AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST00", CIK: 1}
        out := make(chan *data.Observation, 256)
        sub := &library.Subscription{Name: "test"}

        done := make(chan struct{})
        var ttmFund *data.Fundamental

        go func() {
            for obs := range out {
                if obs.Fundamental.Dimension == "ART" {
                    ttmFund = obs.Fundamental
                }
            }
            close(done)
        }()

        numObs := 0
        emitFundamentals(cf, asset, sub, time.Time{}, out, &numObs)
        close(out)
        <-done

        // TTM should exist but without averages
        Expect(ttmFund).NotTo(BeNil())
        Expect(ttmFund.AverageAssets).To(Equal(int64(0)))
    })
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race --focus "TTM period averages" ./provider/sec/`
Expected: FAIL -- `ttmFund.AverageAssets` is 0.

- [ ] **Step 3: Wire period averages into TTM computation**

In `provider/sec/sec.go`, inside the TTM loop, after `ComputeTTM` is called for both AR and MR (around lines 706-717 for ART and 725-736 for MRT), add the average computation.

Modify the ART block. Currently it looks like:

```go
		if ttm := ComputeTTM(arQSlice); ttm != nil {
			fundamental := BuildFundamental(ttm, ...)
			...
		}
```

Change it to:

```go
		if ttm := ComputeTTM(arQSlice); ttm != nil {
			if i >= 4 {
				for k, v := range ComputePeriodAverages(ttm, quarters[i-4].arEmit) {
					ttm[k] = v
				}
			}

			fundamental := BuildFundamental(ttm, asset.Ticker, asset.CompositeFigi, "ART",
				q.period.ARFiledDate, calendarDate, q.period.PeriodEnd, latestARFiled)
			out <- &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  calendarDate,
				SubscriptionID:   sub.ID,
				SubscriptionName: sub.Name,
			}

			*numObservations++
		}
```

Apply the same pattern for the MRT block:

```go
		if ttm := ComputeTTM(mrQSlice); ttm != nil {
			if i >= 4 {
				for k, v := range ComputePeriodAverages(ttm, quarters[i-4].mrEmit) {
					ttm[k] = v
				}
			}

			fundamental := BuildFundamental(ttm, asset.Ticker, asset.CompositeFigi, "MRT",
				q.period.PeriodEnd, calendarDate, q.period.PeriodEnd, latestMRFiled)
			out <- &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  calendarDate,
				SubscriptionID:   sub.ID,
				SubscriptionName: sub.Name,
			}

			*numObservations++
		}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `ginkgo run -race --focus "TTM period averages" ./provider/sec/`
Expected: PASS

- [ ] **Step 5: Run the full test suite**

Run: `ginkgo run -race ./provider/sec/`
Expected: all tests pass.

- [ ] **Step 6: Run lint**

Run: `make lint`
Expected: clean.

- [ ] **Step 7: Commit**

```bash
git add provider/sec/sec.go provider/sec/dimensions_test.go
git commit -m "feat(sec): wire period averages into TTM emission (#43)"
```

---

### Task 8: Validate with existing mapping config tests

**Files:**
- Modify: `provider/sec/mapping_test.go` (if needed)

- [ ] **Step 1: Run the full test suite one final time**

Run: `ginkgo run -race ./provider/sec/`
Expected: all tests pass. In particular:
- `BuildFundamental coverage / references every FieldMappings entry` must pass (InvestedCapital is now in FieldMappings and BuildFundamental).
- `derived formula operands reference existing field names` must pass (InvestedCapital's operands all exist).
- `derived mappings have a formula` must pass (InvestedCapital has operands).

- [ ] **Step 2: Run lint**

Run: `make lint`
Expected: clean.

- [ ] **Step 3: Run the full project test suite**

Run: `make test`
Expected: all tests pass.

- [ ] **Step 4: Verify the mapping config validation test accepts Coefficients**

The existing test `derived mappings have a formula` checks `len(m.Operands) > 0`. The `InvestedCapital` mapping has 5 operands, so this passes. No test changes needed.

Optionally, add a test to verify Coefficients length matches Operands length for OpLinearCombination mappings. Add inside `Describe("Mapping Config"`:

```go
It("OpLinearCombination mappings have matching Coefficients length", func() {
    for _, m := range FieldMappings {
        if m.Op == OpLinearCombination {
            Expect(len(m.Coefficients)).To(Equal(len(m.Operands)),
                "mapping %s: Coefficients length (%d) must match Operands length (%d)",
                m.FieldName, len(m.Coefficients), len(m.Operands))
        }
    }
})
```

- [ ] **Step 5: Final commit if validation test was added**

```bash
git add provider/sec/mapping_test.go
git commit -m "test(sec): add Coefficients length validation for OpLinearCombination (#43)"
```
