# SEC Cash Outflow Sign Negation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Negate four SEC cash outflow fields so they match Sharadar's negative-outflow sign convention, eliminating ~72 AAPL comparison diffs.

**Architecture:** Add a `Negate bool` field to `FieldMapping`. `ResolveAllFields` applies negation immediately after resolution. Change `FreeCashFlow` formula from subtract to add since CapEx is now negative.

**Tech Stack:** Go, Ginkgo v2 + Gomega

---

### Task 1: Add `Negate` field to `FieldMapping` and set it on affected mappings

**Files:**
- Modify: `provider/sec/mapping_config.go:45-60` (struct definition)
- Modify: `provider/sec/mapping_config.go:426-467` (four field mappings)
- Modify: `provider/sec/mapping_config.go:522-527` (FreeCashFlow derived formula)

- [ ] **Step 1: Add `Negate bool` to the `FieldMapping` struct**

In `provider/sec/mapping_config.go`, add a `Negate` field to `FieldMapping`:

```go
// FieldMapping maps a data.Fundamental field to XBRL tag(s) or a formula.
type FieldMapping struct {
	FieldName     string        // data.Fundamental field name (e.g. "Revenues")
	Type          MappingType   // "direct" or "derived"
	StatementType StatementType // Controls TTM computation
	ValueType     string        // "int64" or "float64" -- matches data.Fundamental field type

	// For direct mappings: ordered list of XBRL concept names to try
	XBRLTags []string

	// Negate flips the sign of the resolved value. Use this for XBRL tags
	// that report cash outflows as positive (e.g. PaymentsToAcquire...)
	// to match Sharadar's negative-outflow convention.
	Negate bool

	// For derived mappings: formula
	Op       FormulaOp // Operation to apply
	Operands []string  // Field names to use as operands

	// For derived mappings that also have a direct XBRL fallback
	FallbackTags []string
}
```

- [ ] **Step 2: Set `Negate: true` on the four affected field mappings**

In `provider/sec/mapping_config.go`, add `Negate: true` to these four entries in `FieldMappings`:

`CapitalExpenditure`:
```go
{
	FieldName: "CapitalExpenditure", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
	Negate: true,
	XBRLTags: []string{
		"PaymentsToAcquirePropertyPlantAndEquipment",
		"PaymentsToAcquireProductiveAssets",
		"CapitalExpendituresIncurredButNotYetPaid",
	},
},
```

`NetCashFlowBusiness`:
```go
{
	FieldName: "NetCashFlowBusiness", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
	Negate: true,
	XBRLTags: []string{
		"PaymentsToAcquireBusinessesNetOfCashAcquired",
		"PaymentsToAcquireBusinessesGross",
	},
},
```

`NetCashFlowCommon`:
```go
{
	FieldName: "NetCashFlowCommon", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
	Negate: true,
	XBRLTags: []string{
		"PaymentsForRepurchaseOfCommonStock",
		"ProceedsFromIssuanceOfCommonStock",
	},
},
```

`NetCashFlowDividend`:
```go
{
	FieldName: "NetCashFlowDividend", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
	Negate: true,
	XBRLTags: []string{
		"PaymentsOfDividendsCommonStock",
		"PaymentsOfDividends",
	},
},
```

- [ ] **Step 3: Change FreeCashFlow from subtract to add**

In `provider/sec/mapping_config.go`, change the FreeCashFlow derived mapping:

From:
```go
// FreeCashFlow = NetCashFlowFromOperations - CapitalExpenditure
{
	FieldName: "FreeCashFlow", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
	Op:       OpSubtract,
	Operands: []string{"NetCashFlowFromOperations", "CapitalExpenditure"},
},
```

To:
```go
// FreeCashFlow = NetCashFlowFromOperations + CapitalExpenditure
// (CapitalExpenditure is already negative after Negate, so addition is correct)
{
	FieldName: "FreeCashFlow", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
	Op:       OpAdd,
	Operands: []string{"NetCashFlowFromOperations", "CapitalExpenditure"},
},
```

- [ ] **Step 4: Commit**

```bash
git add provider/sec/mapping_config.go
git commit -m "feat(sec): add Negate flag to FieldMapping for cash outflow fields (#39)"
```

### Task 2: Apply negation in `ResolveAllFields`

**Files:**
- Modify: `provider/sec/mapping.go:188-215` (ResolveAllFields function)

- [ ] **Step 1: Apply negation after direct field resolution**

In `provider/sec/mapping.go`, modify the `MappingDirect` case in `ResolveAllFields` to negate the value when `m.Negate` is true:

```go
case MappingDirect:
	if val, ok := ResolveDirect(cf, m, periodEnd, formType); ok {
		if m.Negate {
			val = -val
		}

		resolved[m.FieldName] = val
	}
```

- [ ] **Step 2: Apply negation after derived fallback resolution too**

In the same function, the `MappingDerived` case tries `FallbackTags` via `ResolveDirect` before computing the formula. Add negation there as well:

```go
case MappingDerived:
	// Try direct XBRL fallback tags first
	if len(m.FallbackTags) > 0 {
		if val, ok := ResolveDirect(cf, m, periodEnd, formType); ok {
			if m.Negate {
				val = -val
			}

			resolved[m.FieldName] = val
			continue
		}
	}

	// Compute from formula
	if val, ok := computeDerived(m, resolved); ok {
		resolved[m.FieldName] = val
	}
```

- [ ] **Step 3: Commit**

```bash
git add provider/sec/mapping.go
git commit -m "feat(sec): apply Negate sign flip in ResolveAllFields (#39)"
```

### Task 3: Write tests for sign negation

**Files:**
- Modify: `provider/sec/mapping_test.go` (add new test cases)

- [ ] **Step 1: Write test for negated fields using AAPL testdata**

Add a new `Describe` block inside the existing `"Mapping Engine"` describe in `provider/sec/mapping_test.go`:

```go
Describe("Sign negation for cash outflow fields", func() {
	It("negates CapitalExpenditure, NetCashFlowBusiness, NetCashFlowCommon, and NetCashFlowDividend", func() {
		// Apple FY2018 10-K: these XBRL tags report outflows as positive,
		// but after negation they should be negative per Sharadar convention.
		periodEnd := time.Date(2018, 9, 29, 0, 0, 0, 0, time.UTC)
		resolved := ResolveAllFields(cf, periodEnd, "10-K")

		capex, hasCapex := resolved["CapitalExpenditure"]
		Expect(hasCapex).To(BeTrue())
		Expect(capex).To(BeNumerically("<", 0),
			"CapitalExpenditure should be negative (cash outflow)")

		ncfCommon, hasCommon := resolved["NetCashFlowCommon"]
		Expect(hasCommon).To(BeTrue())
		Expect(ncfCommon).To(BeNumerically("<", 0),
			"NetCashFlowCommon should be negative (stock buyback)")

		ncfDiv, hasDiv := resolved["NetCashFlowDividend"]
		Expect(hasDiv).To(BeTrue())
		Expect(ncfDiv).To(BeNumerically("<", 0),
			"NetCashFlowDividend should be negative (dividend payment)")
	})

	It("produces positive FreeCashFlow from negated CapitalExpenditure", func() {
		periodEnd := time.Date(2018, 9, 29, 0, 0, 0, 0, time.UTC)
		resolved := ResolveAllFields(cf, periodEnd, "10-K")

		ops, hasOps := resolved["NetCashFlowFromOperations"]
		Expect(hasOps).To(BeTrue())
		Expect(ops).To(BeNumerically(">", 0))

		capex := resolved["CapitalExpenditure"]
		fcf, hasFCF := resolved["FreeCashFlow"]
		Expect(hasFCF).To(BeTrue())
		// FCF = Operations + CapEx (where CapEx is negative)
		Expect(fcf).To(BeNumerically("~", ops+capex, 1.0))
		Expect(fcf).To(BeNumerically(">", 0),
			"FreeCashFlow should be positive for a profitable company like Apple")
	})
})
```

- [ ] **Step 2: Run the tests**

Run: `ginkgo run -race ./provider/sec/`
Expected: all tests pass including the new sign negation tests.

- [ ] **Step 3: Commit**

```bash
git add provider/sec/mapping_test.go
git commit -m "test(sec): verify cash outflow fields are negated (#39)"
```

### Task 4: Fix YTD de-cumulation test expectations

**Files:**
- Modify: `provider/sec/dimensions_test.go:480-513` (YTD de-cumulation test)

The existing "YTD cash flow de-cumulation" test checks raw CapitalExpenditure values as positive. With negation, these become negative. Update the expectations.

- [ ] **Step 1: Update the YTD de-cumulation test expectations**

In `provider/sec/dimensions_test.go`, inside the `"de-cumulates YTD cash flow values to single-quarter values"` test, update:

Q1 assertions (around line 487):
```go
Expect(q1ARQ.CapitalExpenditure).To(Equal(int64(-10)),
	"cap-ex should be negated: -(10) = -10")
```

Q2 assertions (around line 496):
```go
Expect(q2ARQ.CapitalExpenditure).To(Equal(int64(-12)),
	"cap-ex should be de-cumulated then negated: -(22 - 10) = -12")
```

Q3 assertions (around line 506):
```go
Expect(q3ARQ.CapitalExpenditure).To(Equal(int64(-13)),
	"cap-ex should be de-cumulated then negated: -(35 - 22) = -13")
```

FreeCashFlow expectations (around line 510):
```go
// FreeCashFlow = NetCashFlowFromOperations + CapitalExpenditure (CapEx is negative)
Expect(q2ARQ.FreeCashFlow).To(Equal(int64(48)),
	"free cash flow should be re-derived from de-cumulated components: 60 + (-12) = 48")
Expect(q3ARQ.FreeCashFlow).To(Equal(int64(57)),
	"free cash flow should be re-derived: 70 + (-13) = 57")
```

- [ ] **Step 2: Run the tests**

Run: `ginkgo run -race ./provider/sec/`
Expected: all tests pass.

- [ ] **Step 3: Commit**

```bash
git add provider/sec/dimensions_test.go
git commit -m "test(sec): update YTD de-cumulation expectations for negated capex (#39)"
```

### Task 5: Run lint and verify with compare-fundamentals

**Files:** None (verification only)

- [ ] **Step 1: Run linter**

Run: `make lint`
Expected: no errors.

- [ ] **Step 2: Run full test suite**

Run: `make test`
Expected: all tests pass.

- [ ] **Step 3: Build and run compare-fundamentals for AAPL**

Run:
```bash
make build
./pvdata compare-fundamentals --ticker AAPL --fields capital_expenditure,net_cash_flow_common,net_cash_flow_dividend,net_cash_flow_business,free_cash_flow --format table
```

Expected: the four negated fields and FreeCashFlow should show significantly fewer diffs than before. The ~72 diffs from issue #39 should be eliminated for these fields.

- [ ] **Step 4: Run full compare-fundamentals for AAPL to check for regressions**

Run:
```bash
./pvdata compare-fundamentals --ticker AAPL --format table
```

Expected: total diff count should be reduced by approximately 72 compared to the pre-change baseline. No new diffs introduced.
