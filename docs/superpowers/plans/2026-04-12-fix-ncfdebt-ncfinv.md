# Fix net_cash_flow_debt and net_cash_flow_invest Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Convert `NetCashFlowDebt` and `NetCashFlowInvest` from single-tag direct lookups to derived fields that net proceeds against payments, matching Sharadar's definitions.

**Architecture:** Add four internal-only direct sub-fields (prefixed `_`) to resolve individual XBRL tags for debt proceeds, debt repayments, investment payments, and investment proceeds. Then define `NetCashFlowDebt` and `NetCashFlowInvest` as `MappingDerived` / `OpLinearCombination` entries that combine the sub-fields with appropriate +1/-1 coefficients. The `_`-prefixed names are not in `dimensions.go`'s allowlist, so they never reach the `Fundamental` struct.

**Tech Stack:** Go, Ginkgo v2 + Gomega

**Spec:** `docs/superpowers/specs/2026-04-12-fix-ncfdebt-ncfinv-design.md`

---

### Task 1: Write failing tests for NetCashFlowDebt derivation

**Files:**
- Modify: `provider/sec/mapping_test.go`

- [ ] **Step 1: Write the failing test for NetCashFlowDebt as proceeds minus repayments**

Add inside the `Describe("ResolveAllFields", ...)` block, after the existing `It("resolves InvestedCapital from component fields", ...)` test (around line 416):

```go
It("resolves NetCashFlowDebt as proceeds minus repayments", func() {
    periodEnd := time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC)
    filed := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

    debtCF := &CompanyFacts{
        CIK: 1, EntityName: "Test Co",
        Facts: map[string][]Fact{
            "ProceedsFromIssuanceOfLongTermDebt": {
                {
                    Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
                    End:   periodEnd,
                    Filed: filed,
                    Val:   5_000_000_000,
                    Form:  "10-Q",
                },
            },
            "RepaymentsOfLongTermDebt": {
                {
                    Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
                    End:   periodEnd,
                    Filed: filed,
                    Val:   3_000_000_000,
                    Form:  "10-Q",
                },
            },
        },
    }

    resolved := ResolveAllFields(debtCF, periodEnd, "10-Q")
    Expect(resolved).To(HaveKey("NetCashFlowDebt"))
    Expect(resolved["NetCashFlowDebt"]).To(Equal(2_000_000_000.0),
        "NetCashFlowDebt = proceeds(5B) - repayments(3B) = 2B")
})

It("resolves NetCashFlowDebt with only repayments present", func() {
    periodEnd := time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC)
    filed := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

    debtCF := &CompanyFacts{
        CIK: 1, EntityName: "Test Co",
        Facts: map[string][]Fact{
            "RepaymentsOfLongTermDebt": {
                {
                    Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
                    End:   periodEnd,
                    Filed: filed,
                    Val:   3_000_000_000,
                    Form:  "10-Q",
                },
            },
        },
    }

    resolved := ResolveAllFields(debtCF, periodEnd, "10-Q")
    Expect(resolved).To(HaveKey("NetCashFlowDebt"))
    Expect(resolved["NetCashFlowDebt"]).To(Equal(-3_000_000_000.0),
        "NetCashFlowDebt = -repayments(3B) when no proceeds exist")
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race --focus "resolves NetCashFlowDebt" ./provider/sec/`

Expected: FAIL -- currently `NetCashFlowDebt` is a direct mapping that picks only the first matching tag (`ProceedsFromIssuanceOfLongTermDebt`), so the first test gets 5B instead of 2B, and the second test gets -3B from the wrong mapping path.

---

### Task 2: Write failing tests for NetCashFlowInvest derivation

**Files:**
- Modify: `provider/sec/mapping_test.go`

- [ ] **Step 1: Write the failing test for NetCashFlowInvest as proceeds minus payments**

Add immediately after the NetCashFlowDebt tests from Task 1:

```go
It("resolves NetCashFlowInvest as proceeds minus payments", func() {
    periodEnd := time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC)
    filed := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

    investCF := &CompanyFacts{
        CIK: 1, EntityName: "Test Co",
        Facts: map[string][]Fact{
            "PaymentsToAcquireAvailableForSaleSecuritiesDebt": {
                {
                    Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
                    End:   periodEnd,
                    Filed: filed,
                    Val:   15_300_000_000,
                    Form:  "10-Q",
                },
            },
            "ProceedsFromMaturitiesPrepaymentsAndCallsOfAvailableForSaleSecurities": {
                {
                    Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
                    End:   periodEnd,
                    Filed: filed,
                    Val:   13_200_000_000,
                    Form:  "10-Q",
                },
            },
        },
    }

    resolved := ResolveAllFields(investCF, periodEnd, "10-Q")
    Expect(resolved).To(HaveKey("NetCashFlowInvest"))
    Expect(resolved["NetCashFlowInvest"]).To(Equal(-2_100_000_000.0),
        "NetCashFlowInvest = -payments(15.3B) + proceeds(13.2B) = -2.1B")
})

It("resolves NetCashFlowInvest with only proceeds present", func() {
    periodEnd := time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC)
    filed := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

    investCF := &CompanyFacts{
        CIK: 1, EntityName: "Test Co",
        Facts: map[string][]Fact{
            "ProceedsFromSaleOfAvailableForSaleSecuritiesDebt": {
                {
                    Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
                    End:   periodEnd,
                    Filed: filed,
                    Val:   7_000_000_000,
                    Form:  "10-Q",
                },
            },
        },
    }

    resolved := ResolveAllFields(investCF, periodEnd, "10-Q")
    Expect(resolved).To(HaveKey("NetCashFlowInvest"))
    Expect(resolved["NetCashFlowInvest"]).To(Equal(7_000_000_000.0),
        "NetCashFlowInvest = proceeds(7B) when no payments exist")
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race --focus "resolves NetCashFlowInvest" ./provider/sec/`

Expected: FAIL -- currently picks first matching tag as a direct value instead of netting.

---

### Task 3: Implement the derived mappings in mapping_config.go

**Files:**
- Modify: `provider/sec/mapping_config.go:504-528`

- [ ] **Step 1: Replace the NetCashFlowDebt and NetCashFlowInvest entries**

Replace lines 504-528 (the `NetCashFlowDebt` and `NetCashFlowInvest` entries) with:

```go
	// --- Internal sub-fields for NetCashFlowDebt derivation ---
	{
		FieldName: "_proceedsDebt", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"ProceedsFromIssuanceOfLongTermDebt",
			"ProceedsFromIssuanceOfDebt",
			"ProceedsFromDebtNetOfIssuanceCosts",
		},
	},
	{
		FieldName: "_repaymentsDebt", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"RepaymentsOfLongTermDebt",
			"RepaymentsOfDebt",
			"RepaymentsOfLongTermDebtAndCapitalSecurities",
		},
	},
	// NetCashFlowDebt = proceeds - repayments (Sharadar NCFDEBT:
	// "net cash inflow (outflow) from issuance (repayment) of debt securities")
	{
		FieldName: "NetCashFlowDebt", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op:               OpLinearCombination,
		Operands:         []string{"_proceedsDebt", "_repaymentsDebt"},
		Coefficients:     []float64{1, -1},
		OptionalOperands: true,
	},
	// --- Internal sub-fields for NetCashFlowInvest derivation ---
	{
		FieldName: "_paymentsInvest", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"PaymentsToAcquireInvestments",
			"PaymentsToAcquireAvailableForSaleSecuritiesDebt",
		},
	},
	{
		FieldName: "_proceedsInvest", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"ProceedsFromSaleAndMaturityOfMarketableSecurities",
			"ProceedsFromMaturitiesPrepaymentsAndCallsOfAvailableForSaleSecurities",
			"ProceedsFromSaleOfAvailableForSaleSecuritiesDebt",
		},
	},
	// NetCashFlowInvest = -payments + proceeds (Sharadar NCFINV:
	// "net cash inflow (outflow) associated with acquisition & disposal of investments")
	{
		FieldName: "NetCashFlowInvest", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op:               OpLinearCombination,
		Operands:         []string{"_paymentsInvest", "_proceedsInvest"},
		Coefficients:     []float64{-1, 1},
		OptionalOperands: true,
	},
```

- [ ] **Step 2: Run the new tests to verify they pass**

Run: `ginkgo run -race --focus "resolves NetCashFlow(Debt|Invest)" ./provider/sec/`

Expected: PASS -- all four new tests should pass.

- [ ] **Step 3: Run the full test suite to check for regressions**

Run: `ginkgo run -race ./provider/sec/`

Expected: PASS -- the existing mapping config validation tests (duplicate names, operand references, coefficient lengths) should all still pass.

- [ ] **Step 4: Run the linter**

Run: `golangci-lint run --fix ./provider/sec/...`

Expected: PASS with no issues.

- [ ] **Step 5: Commit**

```bash
git add provider/sec/mapping_config.go provider/sec/mapping_test.go
git commit -m "fix(sec): derive NetCashFlowDebt and NetCashFlowInvest from component sub-fields (#41)

NetCashFlowDebt was picking either proceeds or repayments (never both);
NetCashFlowInvest was picking gross purchases instead of netting against
sales/maturities. Convert both to MappingDerived with OpLinearCombination
using internal _-prefixed sub-fields."
```
