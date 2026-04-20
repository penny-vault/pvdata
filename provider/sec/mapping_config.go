// Copyright 2024
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package sec

// MappingType indicates whether a field is read directly from XBRL or derived.
type MappingType string

const (
	MappingDirect  MappingType = "direct"
	MappingDerived MappingType = "derived"
)

// StatementType controls how TTM is computed for this field.
type StatementType string

const (
	StmtFlow          StatementType = "flow"           // Sum 4 quarters for TTM
	StmtPointInTime   StatementType = "point_in_time"  // Use latest quarter for TTM
	StmtPeriodAverage StatementType = "period_average" // Period-average values (e.g., weighted average shares).
	//   TTM: latest quarter's value (like StmtPointInTime).
	//   Q4 synthesis: Q4 = annual*4 - sum(Q1..Q3), since the annual
	//     value is the period-average of 4 quarters.
	//   De-cumulation: skipped (like StmtPointInTime).
	StmtMetric StatementType = "metric" // Recomputed from other fields, not summed
)

// FormulaOp defines the operation for derived fields.
type FormulaOp string

const (
	OpAdd               FormulaOp = "add"                // A + B + ...
	OpSubtract          FormulaOp = "subtract"           // A - B
	OpDivide            FormulaOp = "divide"             // A / B
	OpLinearCombination FormulaOp = "linear_combination" // C0*A + C1*B + ...
)

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
	Op           FormulaOp // Operation to apply
	Operands     []string  // Field names to use as operands
	Coefficients []float64 // Per-operand multipliers (for OpLinearCombination)

	// For derived mappings that also have a direct XBRL fallback
	FallbackTags []string

	// FallbackRequireIfQuarterly gates FallbackTags on a sentinel concept:
	// the FallbackTags are only tried when ANY of the listed concepts appear
	// on a recent 10-Q filing. When none match, the FallbackTags are skipped
	// and the formula is used instead. This allows the same field to use
	// direct resolution for standard companies (with COGS) while falling
	// through to a formula for non-standard companies (insurance/conglomerates
	// that need investment gains added to revenue).
	FallbackRequireIfQuarterly []string

	// FallbackExcludeIfQuarterly is the inverse of FallbackRequireIfQuarterly:
	// the FallbackTags are SKIPPED when ANY of the listed concepts appear on
	// a recent 10-Q filing, forcing the formula path. Use this when a filer
	// reports a dedicated direct tag (e.g. InterestExpenseDebt) but Sharadar
	// computes the value via an alternate formula path driven by different
	// concepts (e.g. WMT: Sharadar uses the net non-operating interest line
	// plus investment interest income, not InterestExpenseDebt directly).
	FallbackExcludeIfQuarterly []string

	// OptionalOperands makes OpAdd treat missing operands as 0 and resolve
	// when at least one operand is present, instead of requiring all.
	OptionalOperands bool

	// RoundDigits rounds OpDivide results to this many decimal places.
	// Zero (the default) means no rounding.
	RoundDigits int

	// RequireQuarterly restricts resolution to companies that file the
	// underlying XBRL concept(s) on 10-Q filings -- not just on 10-K annual
	// disclosures. This distinguishes balance sheet line items (filed
	// quarterly and annually) from supplemental note disclosures (annual
	// only). For example, MSFT files OperatingLeaseLiabilityNoncurrent on
	// 10-Q (it's a balance sheet line) while AAPL only files it on 10-K
	// (supplemental note). Sharadar includes lease liabilities in debt for
	// MSFT but not AAPL, matching this quarterly-availability signal.
	RequireQuarterly bool

	// ExcludeIfQuarterly is the inverse of RequireQuarterly: skip this
	// field entirely if ANY of the listed sentinel concepts appear on a
	// recent 10-Q filing. This detects when a concept is a sub-component
	// of a broader balance sheet line item rather than a separate line.
	// For example, NVDA files AccruedLiabilitiesCurrent (which bundles
	// contract liabilities) so deferred_revenue should be 0; AAPL and
	// MSFT present contract liabilities as a separate line and do not
	// file AccruedLiabilitiesCurrent.
	ExcludeIfQuarterly []string

	// ExcludeIfQuarterlyUnless counteracts ExcludeIfQuarterly: when the
	// ExcludeIfQuarterly sentinel fires, the field is still resolved if
	// ANY of the concepts listed here are ALSO filed on a recent 10-Q.
	// Example: NVDA bundles tax liabilities into AccruedLiabilitiesCurrent
	// (ExcludeIfQuarterly triggers, tax_liab=0); WMT also files
	// AccruedLiabilitiesCurrent but breaks out DeferredIncomeTaxesAndOther
	// separately (the unless sentinel) — so TaxLiabilities still resolves
	// for WMT via the retailer operands.
	ExcludeIfQuarterlyUnless []string

	// ExcludeIfAnnual skips this field if ANY of the listed concepts
	// appear on a recent 10-K filing. It captures the inverse pattern of
	// the usual cross-form fallback: MCD breaks out its current operating
	// lease liability only on 10-Q comparatives (the 10-K omits it), so
	// the concept should contribute to debt_current there. Filers that
	// already tag the concept on their 10-K (AAPL, NVDA) have Sharadar
	// classifying it outside debt_current, and this gate keeps the field
	// off their debt_current computation.
	ExcludeIfAnnual []string

	// RequireIfQuarterly gates resolution on a sentinel concept: the
	// field is only resolved when ANY of the listed concepts appear on
	// a recent 10-Q filing. This is used for cash flow items that some
	// companies bundle into a parent line (and thus should be added to
	// a derived formula) while others present as a standalone line (and
	// should not be added to avoid double-counting).
	RequireIfQuarterly []string

	// AllowCrossFormFallback enables ResolveDirect to fall back to the
	// opposite form type (10-K ↔ 10-Q) at the same period end when no
	// fact of the requested form is found. Used for point-in-time
	// balance-sheet concepts that some filers (e.g. MCD) only tag on
	// 10-Q filings — even for annual year-end dates — leaving the 10-K
	// period silent for that concept.
	AllowCrossFormFallback bool

	// PreferFormulaWhenFallbackZero overrides the usual "FallbackTag wins
	// when it resolves" rule for derived fields: if the FallbackTag
	// resolves to exactly zero but the formula produces a non-zero value,
	// the formula wins. MCD tags us-gaap:DebtCurrent as 0 on its 10-K
	// despite carrying operating-lease-current and commercial-paper
	// balances that Sharadar rolls into debt_current. For filers whose
	// DebtCurrent tag is genuinely zero (no short-term debt) the formula
	// also sums to zero, so this gate is a no-op.
	PreferFormulaWhenFallbackZero bool
}

// FieldMappings defines the complete mapping from XBRL to data.Fundamental fields.
// Order matters for derived fields -- dependencies must come before dependents.
// The edgartools project (MIT license, github.com/dgunning/edgartools) was used
// as a reference for XBRL tag selection, validated against 32,240 real SEC filings.
var FieldMappings = []FieldMapping{
	// ==================== BALANCE SHEET (point-in-time) ====================

	{
		FieldName: "TotalAssets", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"Assets"},
	},
	{
		FieldName: "CurrentAssets", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"AssetsCurrent"},
	},
	{
		FieldName: "AssetsNonCurrent", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags: []string{"AssetsNoncurrent"},
		Op:           OpSubtract,
		Operands:     []string{"TotalAssets", "CurrentAssets"},
	},
	// _cashStandard: default cash resolution used for most filers.
	// Prefers the plain CashAndCashEquivalentsAtCarryingValue tag so that
	// short-term investments reported as a separate balance-sheet line
	// (MSFT, AMZN) stay out of cash.
	{
		FieldName: "_cashStandard", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"CashAndCashEquivalentsAtCarryingValue",
			"CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents", // Banks (JPM) use this
			"Cash",
			"CashEquivalentsAtCarryingValue",
		},
	},
	// CashAndEquivalents: Sharadar rolls short-term investments into
	// cash_and_equivalents when the filer combines them on the balance-sheet
	// face (KO's "Cash, cash equivalents and short-term investments" line).
	// FallbackRequireIfQuarterly gates the combined tag on
	// MarketableSecurities (KO's current-investments tag, unqualified) so
	// filers that break out short-term investments as a separate line with
	// the MarketableSecuritiesCurrent / ShortTermInvestments tag (MSFT, AMZN)
	// keep the cash-only treatment via the _cashStandard fallback.
	{
		FieldName: "CashAndEquivalents", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags:               []string{"CashCashEquivalentsAndShortTermInvestments"},
		FallbackRequireIfQuarterly: []string{"MarketableSecurities"},
		Op:                         OpAdd,
		Operands:                   []string{"_cashStandard"},
		OptionalOperands:           true,
	},
	{
		FieldName: "Inventory", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"InventoryNet", "InventoryFinishedGoodsAndWorkInProcess"},
	},
	{
		FieldName: "InvestmentsCurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"ShortTermInvestments",
			"MarketableSecuritiesCurrent",
			"AvailableForSaleSecuritiesDebtSecuritiesCurrent",
			// KO reports its short-dated marketable securities under the
			// unqualified MarketableSecurities tag (no Current/Noncurrent
			// suffix) with equity-method holdings under a different tag.
			"MarketableSecurities",
		},
	},
	// _equityMethodInvestmentsForKoPattern: KO-pattern filers that report
	// their equity-method affiliate holdings (bottler investments, ~$20B)
	// as a separate balance-sheet line under EquityMethodInvestments and
	// file the unqualified MarketableSecurities tag for current investments.
	// Sharadar rolls this balance into investments_non_current for those
	// filers but NOT for AMZN-style filers that file EquityMethodInvestments
	// only as a footnote disclosure (no standalone balance-sheet line).
	// Gated on MarketableSecurities (unqualified) which AMZN does not file
	// (AMZN uses MarketableSecuritiesCurrent instead).
	{
		FieldName: "_equityMethodInvestmentsForKoPattern",
		Type:     MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireIfQuarterly: []string{"MarketableSecurities"},
		XBRLTags:           []string{"EquityMethodInvestments"},
	},
	{
		FieldName:     "InvestmentsNonCurrent",
		Type:          MappingDerived,
		StatementType: StmtPointInTime,
		ValueType:     "int64",
		// Banks (Deposits) and investment banks / broker-dealers
		// (CustomerAndOtherPayables) report investment portfolios under
		// broker-dealer-specific tags that Sharadar does not classify as
		// non-current investments (investments_non_current = 0 for these
		// filers). Gating here avoids pulling e.g. GS's
		// InvestmentsInAffiliatesSubsidiariesAssociatesAndJointVentures
		// (~140B) into investments_non_current.
		ExcludeIfQuarterly: []string{
			"Deposits", "DepositsDomestic", "DepositsTotal",
			"CustomerAndOtherPayables",
		},
		FallbackTags: []string{
			"LongTermInvestments",
			"MarketableSecuritiesNoncurrent",
			"AvailableForSaleSecuritiesDebtSecuritiesNoncurrent",
			// CALM reports non-current investments under this tag (joint-
			// venture/affiliate holdings) rather than the LongTerm* tags.
			"InvestmentsInAffiliatesSubsidiariesAssociatesAndJointVentures",
		},
		Op:               OpAdd,
		Operands:         []string{"_equityMethodInvestmentsForKoPattern"},
		OptionalOperands: true,
	},
	// Insurance/conglomerate investment sub-fields. These companies (BRK/B)
	// hold equity securities, debt securities, and equity method investments
	// that aren't captured by the standard Current/NonCurrent breakdown.
	// ExcludeIfQuarterly gates these off when the company files standard
	// investment tags (ShortTermInvestments, MarketableSecuritiesCurrent)
	// to avoid double-counting with InvestmentsCurrent/InvestmentsNonCurrent.
	{
		FieldName: "_equitySecuritiesFvNi", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		// RequireQuarterly keeps WMT's 10-K-only EquitySecuritiesFvNi
		// disclosure (footnote-scale marketable equity holdings, ~$3B) out
		// of Investments. BRK/B files the concept every 10-Q (its primary
		// equity portfolio, ~$260B) so the gate still activates there.
		RequireQuarterly:   true,
		ExcludeIfQuarterly: []string{"ShortTermInvestments", "MarketableSecuritiesCurrent"},
		XBRLTags:           []string{"EquitySecuritiesFvNi"},
	},
	{
		FieldName: "_equityMethodInvestments", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		// Also exclude when InvestmentsInAffiliatesSubsidiariesAssociatesAndJointVentures
		// is filed (CALM): the two tags represent overlapping equity-method /
		// JV holdings and the latter is already summed into InvestmentsNonCurrent
		// above. Counting both double-counts at the annual/Q4 row where
		// EquityMethodInvestments (annual-only for CALM) appears.
		// MarketableSecurities is KO's current-investments tag and also
		// gates _equityMethodInvestmentsForKoPattern on InvestmentsNonCurrent
		// below; exclude here to avoid double-counting the balance.
		ExcludeIfQuarterly: []string{
			"ShortTermInvestments",
			"MarketableSecuritiesCurrent",
			"InvestmentsInAffiliatesSubsidiariesAssociatesAndJointVentures",
			"MarketableSecurities",
		},
		XBRLTags: []string{"EquityMethodInvestments"},
	},
	{
		FieldName: "_debtSecurities", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		// Also exclude when AvailableForSaleSecuritiesDebtSecuritiesCurrent is
		// filed (CALM pattern): the generic tag and its -Current sibling report
		// the same value (no non-current AFS debt), so counting both once via
		// InvestmentsCurrent and again via _debtSecurities doubles the total.
		// MarketableSecurities (KO pattern): the unqualified tag rolls up
		// debt and equity marketable securities into a single figure that's
		// already counted via InvestmentsCurrent. Counting the AFS debt
		// subset on top would double-count.
		ExcludeIfQuarterly: []string{"ShortTermInvestments", "MarketableSecuritiesCurrent", "AvailableForSaleSecuritiesDebtSecuritiesCurrent", "MarketableSecurities"},
		XBRLTags:           []string{"AvailableForSaleSecuritiesDebtSecurities"},
	},
	{
		FieldName: "_treasuryBills", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"ShortTermInvestments", "MarketableSecuritiesCurrent"},
		XBRLTags: []string{
			"USTreasuryBills", // BRK/B extension tag for short-term Treasury holdings
		},
	},
	// Loans receivable / notes receivable: insurance/conglomerate invested
	// assets. Sharadar includes these in Investments for BRK/B. Gated to
	// exclude banks (Deposits) and standard companies (ShortTermInvestments).
	{
		FieldName: "_notesReceivable", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{
			"ShortTermInvestments", "MarketableSecuritiesCurrent",
			"Deposits", "DepositsDomestic", "DepositsTotal",
		},
		XBRLTags: []string{"NotesReceivableNet"},
	},
	// Investments = InvestmentsCurrent + InvestmentsNonCurrent + equity/debt
	// securities + equity method investments + Treasury bills + notes receivable.
	// Sharadar defines this as "total amount of marketable and non-marketable
	// securities, loans receivable and other invested assets." Standard companies
	// use Current/NonCurrent; insurance/conglomerates report per-asset-class.
	{
		FieldName: "Investments", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags: []string{"Investments"},
		Op:           OpAdd,
		Operands: []string{
			"InvestmentsCurrent", "InvestmentsNonCurrent",
			"_equitySecuritiesFvNi", "_equityMethodInvestments",
			"_debtSecurities", "_treasuryBills", "_notesReceivable",
		},
		OptionalOperands: true,
	},
	{
		FieldName: "TradeReceivables", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"AccountsReceivableNetCurrent",
			"AccountsReceivableNet",
			// Franchise-heavy filers (e.g. MCD) report trade, notes, and loan
			// receivables on a single line. Sharadar includes the combined
			// balance in its receivables total.
			"AccountsNotesAndLoansReceivableNetCurrent",
		},
	},
	{
		FieldName: "NonTradeReceivables", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"NontradeReceivablesCurrent",
			"OtherReceivablesNetCurrent", // LLY-style (separate "other receivables" line)
		},
	},
	// Insurance/conglomerate receivables sub-fields. FallbackRequireIfQuarterly
	// on CoGS/Deposits on the parent Receivables gates the fallback path for
	// standard filers only; conglomerates like BRK/B without quarterly CoGS
	// fall through to the Operand formula which sums these sub-fields.
	{
		FieldName: "_premiumsReceivables", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"PremiumsAndOtherReceivablesNet"}, // BRK/B Insurance segment
	},
	{
		FieldName: "_railroadReceivables", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"AccountsAndOtherReceivablesNet"}, // BRK/B Railroad/Utilities extension
	},
	// Receivables = TradeReceivables + NonTradeReceivables. Sharadar
	// defines this as "trade and non-trade receivables." Apple tags these
	// separately (AccountsReceivableNetCurrent + NontradeReceivablesCurrent).
	// Banks (JPM) use a combined extension tag for accrued interest + A/R.
	// Insurance conglomerates (BRK/B) sum Premiums + Railroad segment
	// receivables via the _premiumsReceivables and _railroadReceivables
	// sub-fields (single-segment synthesis provides the consolidated values).
	{
		FieldName: "Receivables", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags: []string{
			"ReceivablesNetCurrent",
			"AccruedInterestAndAccountsReceivable", // JPM extension tag
			"NotesReceivableNet",                   // Conglomerates with loan portfolios
		},
		FallbackRequireIfQuarterly: []string{
			"CostOfGoodsAndServicesSold",
			"CostOfRevenue",
			"CostOfGoodsSold",
			"CostOfGoodsAndServiceExcludingDepreciationDepletionAndAmortization",
			"Deposits",
			"DepositsDomestic",
			"DepositsTotal",
		},
		Op: OpAdd,
		Operands: []string{
			"TradeReceivables", "NonTradeReceivables",
			"_premiumsReceivables", "_railroadReceivables",
		},
		OptionalOperands: true,
	},
	{
		FieldName: "Payables", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"AccountsPayableCurrent",
			"AccountsPayableAndAccruedLiabilitiesCurrent",
			"AccountsPayableAndAccruedLiabilitiesCurrentAndNoncurrent", // Banks (JPM) use combined current+noncurrent
		},
	},
	{
		FieldName: "Deposits", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		// Standard bank deposit tags first, then restaurant-filer extension
		// concepts for "security/restricted deposits" balances that Sharadar
		// maps into the deposits column (e.g. TXRH's restricted-stock deposit).
		// Extension tags are captured from inline XBRL by EnrichWithExtensionFacts
		// under their bare local name (no namespace prefix).
		XBRLTags: []string{
			"Deposits", "DepositsDomestic", "DepositsTotal",
			"RestrictedStockAndOtherDepositsNoncurrent",
		},
	},
	{
		FieldName: "_ppneRaw", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		// ExcludeIfQuarterly on CustomerAndOtherPayables: GS-style investment
		// banks bundle PP&E into "Other assets" (see _goodwill for rationale).
		ExcludeIfQuarterly: []string{"CustomerAndOtherPayables"},
		XBRLTags: []string{
			"PropertyPlantAndEquipmentNet",
			// AMZN files a combined PP&E + finance-lease ROU tag on 10-Q instead
			// of the base PropertyPlantAndEquipmentNet. OperatingLeaseRightOfUseAsset
			// is resolved separately and summed in by the PropertyPlantAndEquipmentNet
			// derivation.
			"PropertyPlantAndEquipmentAndFinanceLeaseRightOfUseAssetAfterAccumulatedDepreciationAndAmortization",
		},
	},
	// Operating lease ROU assets are included in PP&E when the company
	// reports them as a separate balance sheet line (filed on 10-Q).
	// Exclude banks: Sharadar reports bank PP&E as just
	// PropertyPlantAndEquipmentNet without operating lease assets.
	// AllowCrossFormFallback covers MCD-style filers whose 10-K omits
	// this concept despite tagging it on 10-Q comparatives.
	{
		FieldName: "_operatingLeaseROU", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly:       true,
		ExcludeIfQuarterly:     []string{"Deposits", "DepositsDomestic", "DepositsTotal"},
		AllowCrossFormFallback: true,
		XBRLTags:               []string{"OperatingLeaseRightOfUseAsset"},
	},
	// Finance lease ROU assets are included in PP&E when the company reports
	// them as a separate quarterly balance sheet line (WMT). Filers that
	// disclose finance leases only in 10-K notes (AAPL, TXRH) do not meet
	// RequireQuarterly and stay out of PP&E — matching Sharadar's treatment.
	// Exclude filers that file the combined PP&E+FinLeaseROU tag (AMZN) to
	// avoid double-counting. Exclude banks via deposit sentinels.
	{
		FieldName: "_financeLeaseROU", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly: true,
		ExcludeIfQuarterly: []string{
			"Deposits", "DepositsDomestic", "DepositsTotal",
			"PropertyPlantAndEquipmentAndFinanceLeaseRightOfUseAssetAfterAccumulatedDepreciationAndAmortization",
		},
		XBRLTags: []string{"FinanceLeaseRightOfUseAsset"},
	},
	// Property subject to operating leases (lessor-side assets like railcars,
	// utility equipment) is included in PP&E for conglomerates like BRK/B.
	// Exclude banks: they report PropertySubjectToOrAvailableForOperatingLeaseNet
	// for leased real estate, which Sharadar does not include in PP&E.
	{
		FieldName: "_propertyHeldForLease", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"Deposits", "DepositsDomestic", "DepositsTotal"},
		XBRLTags:           []string{"PropertySubjectToOrAvailableForOperatingLeaseNet"},
	},
	// PropertyPlantAndEquipmentNet: use the combined extension tag if
	// available (JPM files a combined premises+equipment+ROU tag); otherwise
	// sum the sub-components.
	{
		FieldName: "PropertyPlantAndEquipmentNet", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags: []string{
			"PropertyPlantAndEquipmentAndOperatingLeaseRightOfUseAssetAfterAccumulatedDepreciationAndAmortization", // JPM extension
		},
		Op:               OpAdd,
		Operands:         []string{"_ppneRaw", "_operatingLeaseROU", "_financeLeaseROU", "_propertyHeldForLease"},
		OptionalOperands: true,
	},
	// --- Internal sub-fields for Intangibles derivation ---
	// ExcludeIfQuarterly on CustomerAndOtherPayables: investment banks that use
	// the GS-style balance sheet (payables bundled into "customer and other
	// payables") do not break out intangibles/goodwill as a separate balance
	// sheet line item -- they're rolled into "Other assets". Sharadar reports
	// Intangibles=0 for these filers to match the face-of-balance-sheet
	// presentation, so skip resolution entirely when the GS-pattern marker is
	// present.
	{
		FieldName: "_goodwill", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"CustomerAndOtherPayables"},
		XBRLTags:           []string{"Goodwill"},
	},
	{
		FieldName: "_intangiblesExGoodwill", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		// RequireQuarterly: ensure the company breaks out non-goodwill
		// intangibles as its own balance-sheet line (filed on 10-Q). AMZN
		// discloses intangibles only in 10-K footnotes — the number is not
		// on the face of the balance sheet, so Sharadar excludes it from
		// intangibles. Filers that report it on 10-Q (MSFT, NVDA, LLY)
		// continue to include it.
		RequireQuarterly:   true,
		ExcludeIfQuarterly: []string{"CustomerAndOtherPayables"},
		XBRLTags: []string{
			"IntangibleAssetsNetExcludingGoodwill",
			"FiniteLivedIntangibleAssetsNet",
			"IndefiniteLivedIntangibleAssetsExcludingGoodwill", // BRK/B and other conglomerates with brand/franchise intangibles
			// KO files only us-gaap:IndefiniteLivedTrademarks for the non-
			// goodwill portion of intangibles (no IntangibleAssetsNet...
			// roll-up). Sharadar includes its brand value in intangibles.
			"IndefiniteLivedTrademarks",
		},
	},
	// Intangibles: Sharadar defines this as "all intangible assets and
	// goodwill." Use the combined tag if available; otherwise sum Goodwill +
	// other intangibles. MSFT reports Goodwill and FiniteLivedIntangibleAssetsNet
	// separately; the sum captures both. Banks (JPM) file a combined extension
	// tag that includes goodwill, MSR, and other intangibles.
	{
		FieldName: "Intangibles", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags: []string{
			"IntangibleAssetsNetIncludingGoodwill",
			"GoodwillServicingAssetsAtFairValueAndOtherIntangibleAssets", // JPM 10-Q extension
			"GoodwillServicingAssetsatFairValueandOtherIntangibleAssets", // JPM 10-K extension (different casing)
		},
		Op:               OpAdd,
		Operands:         []string{"_goodwill", "_intangiblesExGoodwill"},
		OptionalOperands: true,
	},
	// TaxAssets: prefer DeferredIncomeTaxAssetsNet (Sharadar's "deferred tax
	// assets") when filed quarterly. _deferredTaxAssets uses RequireQuarterly
	// so AAPL — which only discloses deferred tax assets in 10-K notes — keeps
	// TaxAssets=0 for MR/AR dimensions instead of leaking the annual value
	// across quarters. _prepaidTaxAssets is the secondary path for filers that
	// disclose income-tax receivables/prepayments instead of deferred tax
	// assets; ExcludeIfQuarterly on DeferredIncomeTaxAssetsNet keeps these
	// from double-counting for filers (LLY) that report both.
	{
		FieldName: "_deferredTaxAssets", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly: true,
		XBRLTags:         []string{"DeferredIncomeTaxAssetsNet"},
	},
	{
		FieldName: "_prepaidTaxAssets", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"DeferredIncomeTaxAssetsNet"},
		XBRLTags: []string{
			"IncomeTaxesReceivable",
			"IncomeTaxReceivable",
			"PrepaidTaxes",
		},
	},
	{
		FieldName: "TaxAssets", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		Op:               OpAdd,
		Operands:         []string{"_deferredTaxAssets", "_prepaidTaxAssets"},
		OptionalOperands: true,
	},
	// --- Internal sub-fields for TaxLiabilities derivation ---
	{
		FieldName: "_deferredTaxLiabilities", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		// WMT-style filers that report DeferredIncomeTaxesAndOther as a
		// combined quarterly line use that tag instead; counting the
		// 10-K note-level DeferredTaxLiabilities on top would double-count.
		ExcludeIfQuarterly: []string{"DeferredIncomeTaxesAndOtherLiabilitiesNoncurrent"},
		XBRLTags: []string{
			// Prefer the net liability-side tag (Sharadar's
			// tax_liabilities uses this). KO files both
			// DeferredTaxLiabilities (1150M, net of deferred tax assets)
			// and DeferredIncomeTaxLiabilitiesNet (2469M, liability-side
			// net); the latter matches the balance-sheet line Sharadar
			// picks up.
			"DeferredIncomeTaxLiabilitiesNet",
			"DeferredTaxLiabilitiesNoncurrent",
			"DeferredTaxLiabilities",
		},
	},
	// Accrued income taxes are included when filed as separate quarterly
	// balance sheet line items (RequireQuarterly). MSFT breaks these out;
	// AAPL only reports them in 10-K notes.
	{
		FieldName: "_accruedIncomeTaxesCurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly: true,
		XBRLTags: []string{
			"AccruedIncomeTaxesCurrent",
			"TaxesPayableCurrent", // LLY-style "Income taxes payable" line
		},
	},
	{
		FieldName: "_accruedIncomeTaxesNoncurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly: true,
		XBRLTags:         []string{"AccruedIncomeTaxesNoncurrent"},
	},
	// MCD separates sales, property, and other non-income taxes as a standalone
	// current-liabilities line (~$224-247M for 2024-2025). Sharadar rolls that
	// into tax_liabilities alongside income and deferred tax balances.
	{
		FieldName: "_otherTaxesPayableCurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly: true,
		XBRLTags:         []string{"AccrualForTaxesOtherThanIncomeTaxesCurrent"},
	},
	// Retailer-variant sub-field: WMT-style filers report "Deferred income
	// taxes and other" as a single non-current line. Sharadar includes that
	// combined balance in tax_liabilities. RequireIfQuarterly on the
	// combined tag keeps this operand inert for filers that don't match.
	// The existing _accruedIncomeTaxesCurrent operand already picks up
	// AccruedIncomeTaxesCurrent for WMT (WMT files it quarterly) so no
	// additional retailer operand is needed on the current side.
	{
		FieldName: "_retailerDeferredTaxAndOtherNoncurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireIfQuarterly: []string{"DeferredIncomeTaxesAndOtherLiabilitiesNoncurrent"},
		XBRLTags:           []string{"DeferredIncomeTaxesAndOtherLiabilitiesNoncurrent"},
	},
	{
		FieldName: "TaxLiabilities", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		// When AccruedLiabilitiesCurrent is filed on 10-Q, the company
		// bundles tax liabilities into broader accrued/other line items
		// (NVDA) — skip unless the filer also reports the combined
		// "Deferred income taxes and other" non-current line, in which
		// case the retailer operand carries the Sharadar-compatible value
		// and the standard _accruedIncomeTaxesCurrent operand supplies
		// the current-side accrual.
		// IncomeTaxesPrincipallyDeferred is a BRK/B extension tag for the
		// consolidated deferred tax liability (filed quarterly, unlike the
		// standard DeferredIncomeTaxLiabilitiesNet which BRK only files on 10-K).
		ExcludeIfQuarterly:       []string{"AccruedLiabilitiesCurrent"},
		ExcludeIfQuarterlyUnless: []string{"DeferredIncomeTaxesAndOtherLiabilitiesNoncurrent"},
		FallbackTags:             []string{"IncomeTaxesPrincipallyDeferred"},
		Op:                       OpAdd,
		Operands: []string{
			"_deferredTaxLiabilities", "_accruedIncomeTaxesCurrent", "_accruedIncomeTaxesNoncurrent", "_otherTaxesPayableCurrent",
			"_retailerDeferredTaxAndOtherNoncurrent",
		},
		OptionalOperands: true,
	},
	// Debt sub-components are gated on AssetsCurrent: companies that classify
	// assets as current/non-current (non-banks) also classify debt that way.
	// Banks (JPM) do not file AssetsCurrent and Sharadar reports DebtCurrent=0
	// and DebtNonCurrent=0 for them.
	{
		FieldName: "ShortTermDebt", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags:           []string{"ShortTermBorrowings"},
	},
	{
		FieldName: "LongTermDebtCurrentMaturities", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags: []string{
			"LongTermDebtCurrent",
			// KO files the current portion of long-term debt under
			// LongTermDebtAndCapitalLeaseObligationsCurrent rather than
			// LongTermDebtCurrent.
			"LongTermDebtAndCapitalLeaseObligationsCurrent",
		},
	},
	{
		FieldName: "CommercialPaperDebt", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		// RequireQuarterly excludes MCD-style filers that tag CommercialPaper
		// only on the annual 10-K (their 10-Q comparatives re-tag the same
		// balance-sheet line under ShortTermBorrowings=0). AAPL keeps filing
		// CommercialPaper quarterly, so the gate leaves its debt_current
		// reconstruction unchanged.
		RequireQuarterly:   true,
		RequireIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags: []string{
			// KO files a broader NotesAndLoansPayable that rolls its
			// commercial paper (1.99B) into a larger "Loans and notes
			// payable" balance-sheet line (2.32B = CP + other short-term
			// notes). Prefer the broader tag so the extra short-term
			// borrowings are captured; filers that file only CommercialPaper
			// (AAPL, MCD, WMT, etc.) fall through.
			"NotesAndLoansPayable",
			"CommercialPaper",
		},
	},
	// Current operating lease liabilities are included in debt_current only
	// for filers whose 10-K omits the concept (MCD-style). MCD breaks out
	// "Current portion of operating lease liability" on its 10-Q
	// comparatives but not on the original 10-K — Sharadar rolls that
	// into debt_current. Filers that tag OperatingLeaseLiabilityCurrent on
	// their 10-K (NVDA, AAPL, AMZN) classify it outside debt_current in
	// Sharadar, so ExcludeIfAnnual keeps it off their sum.
	// AllowCrossFormFallback lets the 10-Q value populate the AR/MR
	// resolution for the 10-K period end.
	{
		FieldName: "_operatingLeaseLiabilityCurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly:       true,
		RequireIfQuarterly:     []string{"AssetsCurrent"},
		ExcludeIfAnnual:        []string{"OperatingLeaseLiabilityCurrent"},
		AllowCrossFormFallback: true,
		XBRLTags:               []string{"OperatingLeaseLiabilityCurrent"},
	},
	// Restaurant-filer variant: TXRH-style filers (PreOpeningCosts filed
	// quarterly) have no traditional debt on the balance sheet — their
	// operating lease liability IS their debt — so Sharadar rolls the
	// current portion into debt_current even when it's tagged on both
	// 10-K and 10-Q (where ExcludeIfAnnual would otherwise suppress it
	// for AAPL/NVDA/AMZN, which classify it outside debt_current).
	{
		FieldName: "_operatingLeaseLiabilityCurrentRestaurant", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly:   true,
		RequireIfQuarterly: []string{"PreOpeningCosts"},
		XBRLTags:           []string{"OperatingLeaseLiabilityCurrent"},
	},
	// WMT-style retailer variant: filers that report FinanceLeaseRightOfUseAsset
	// as a separate quarterly balance-sheet line (WMT) have Sharadar rolling
	// BOTH current operating-lease and finance-lease liabilities into
	// debt_current. The RequireIfQuarterly gate distinguishes this pattern
	// from AMZN (files a combined PP&E+FinLease tag, no standalone FinLeaseROU
	// line) and AAPL (FinLeaseROU only in 10-K notes), where Sharadar
	// classifies these leases outside debt_current.
	{
		FieldName: "_operatingLeaseLiabilityCurrentRetailer", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly:   true,
		RequireIfQuarterly: []string{"FinanceLeaseRightOfUseAsset"},
		XBRLTags:           []string{"OperatingLeaseLiabilityCurrent"},
	},
	{
		FieldName: "_financeLeaseLiabilityCurrentRetailer", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly:   true,
		RequireIfQuarterly: []string{"FinanceLeaseRightOfUseAsset"},
		XBRLTags:           []string{"FinanceLeaseLiabilityCurrent"},
	},
	// DebtCurrent = ShortTermDebt + LongTermDebtCurrentMaturities +
	// CommercialPaperDebt + current operating lease liability. Sharadar
	// includes all forms of current debt (bonds, commercial paper, notes
	// payable, credit facilities, current lease liability). Apple tags
	// LongTermDebtCurrent and CommercialPaper separately; the sum captures
	// both. For banks (no AssetsCurrent), all components are gated off so
	// DebtCurrent = 0. The _operatingLeaseLiabilityCurrent* variants are
	// mutually exclusive by gate: only one resolves per filer.
	{
		FieldName: "DebtCurrent", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags: []string{"DebtCurrent"},
		Op:           OpAdd,
		Operands: []string{
			"ShortTermDebt", "LongTermDebtCurrentMaturities", "CommercialPaperDebt",
			"_operatingLeaseLiabilityCurrent", "_operatingLeaseLiabilityCurrentRestaurant",
			"_operatingLeaseLiabilityCurrentRetailer", "_financeLeaseLiabilityCurrentRetailer",
		},
		OptionalOperands:              true,
		PreferFormulaWhenFallbackZero: true,
	},
	{
		FieldName: "_longTermDebtNoncurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags: []string{
			"LongTermDebtNoncurrent",
			"LongTermDebt",
			// KO-style filers file LongTermDebtAndCapitalLeaseObligations as the
			// non-current-only long-term debt (with a separate
			// ...Current counterpart and an ...IncludingCurrentMaturities total).
			// Prefer this over the including-current-maturities tag so the
			// noncurrent field doesn't double-count the current portion.
			"LongTermDebtAndCapitalLeaseObligations",
			"LongTermDebtAndCapitalLeaseObligationsIncludingCurrentMaturities",
		},
	},
	// Operating lease liabilities are included in debt when the company
	// reports them as a separate balance sheet line item (filed on 10-Q).
	// RequireQuarterly ensures we only add them for companies like MSFT
	// (separate line) and not AAPL (10-K note disclosure only).
	// AllowCrossFormFallback covers MCD-style filers whose 10-K omits
	// this concept despite tagging it on 10-Q comparatives.
	{
		FieldName: "_operatingLeaseLiabilityNoncurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly:       true,
		RequireIfQuarterly:     []string{"AssetsCurrent"},
		AllowCrossFormFallback: true,
		XBRLTags:               []string{"OperatingLeaseLiabilityNoncurrent"},
	},
	// Finance lease liabilities: Sharadar includes non-current finance lease
	// liabilities in debt_non_current for filers (AMZN) that break them out
	// as a separate balance sheet line. MSFT doesn't file this tag so
	// OptionalOperands leaves it at 0 there.
	{
		FieldName: "_financeLeaseLiabilityNoncurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly:   true,
		RequireIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags:           []string{"FinanceLeaseLiabilityNoncurrent"},
	},
	{
		FieldName: "DebtNonCurrent", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		Op:               OpAdd,
		Operands:         []string{"_longTermDebtNoncurrent", "_operatingLeaseLiabilityNoncurrent", "_financeLeaseLiabilityNoncurrent"},
		OptionalOperands: true,
	},
	// --- Bank-specific debt sub-fields ---
	// Banks don't classify debt as current/non-current. Sharadar computes
	// TotalDebt for banks as ShortTermBorrowings + LongTermDebt +
	// FederalFundsPurchased. These sub-fields are gated with
	// ExcludeIfQuarterly on AssetsCurrent so they only resolve for banks.
	{
		FieldName: "_bankShortTermDebt", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags:           []string{"ShortTermBorrowings"},
	},
	{
		FieldName: "_bankLongTermDebt", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags: []string{
			"LongTermDebtAndCapitalLeaseObligationsIncludingCurrentMaturities",
			"LongTermDebt",
		},
	},
	{
		FieldName: "_bankFederalFundsPurchased", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags:           []string{"FederalFundsPurchasedAndSecuritiesSoldUnderAgreementsToRepurchase"},
	},
	{
		FieldName: "TotalDebt", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		// DebtAndCapitalLeaseObligations is reported by conglomerates and
		// insurance companies (e.g. BRK/B) that don't classify debt as
		// current/non-current. It captures all debt including capital
		// lease obligations in a single line item.
		FallbackTags: []string{"DebtAndCapitalLeaseObligations"},
		Op:           OpAdd,
		Operands: []string{
			"DebtCurrent", "DebtNonCurrent",
			"_bankShortTermDebt", "_bankLongTermDebt", "_bankFederalFundsPurchased",
		},
		OptionalOperands: true,
	},
	// --- Internal sub-fields for DeferredRevenue derivation ---
	// ExcludeIfQuarterly: when AccruedLiabilitiesCurrent is filed on 10-Q,
	// contract liabilities are a sub-component of that broader line item
	// (NVDA), not a separate balance sheet line. Sharadar reports 0 in
	// that case. AAPL and MSFT present contract liabilities as their own
	// line and do not file AccruedLiabilitiesCurrent.
	{
		FieldName: "_deferredRevenueCurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"AccruedLiabilitiesCurrent"},
		XBRLTags: []string{
			"DeferredRevenueCurrent",
			"ContractWithCustomerLiabilityCurrent",
		},
	},
	{
		FieldName: "_deferredRevenueNoncurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"AccruedLiabilitiesCurrent"},
		XBRLTags: []string{
			"DeferredRevenueNoncurrent",
			"ContractWithCustomerLiabilityNoncurrent",
		},
	},
	// DeferredRevenue = current + noncurrent. Sharadar includes the noncurrent
	// portion when the company reports it separately (MSFT: Short-term + Long-term
	// unearned revenue). When only the current tag exists (AAPL: no noncurrent
	// tag), OptionalOperands yields just the current value.
	// Insurance deferred revenue sub-fields. ExcludeIfQuarterly on
	// AccruedLiabilitiesCurrent prevents activation for NVDA-type companies.
	// PolicyholderFunds (annuity/reserve balances) is intentionally NOT an
	// operand: Sharadar's deferred_revenue excludes reserve liabilities
	// for all life/annuity insurers (LNC, MET report 0).
	{
		FieldName: "_unearnedPremiums", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"AccruedLiabilitiesCurrent"},
		XBRLTags:           []string{"UnearnedPremiums"},
	},
	{
		FieldName: "_aircraftDeferredRevenue", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"AccruedLiabilitiesCurrent"},
		XBRLTags:           []string{"AircraftRepurchaseLiabilitiesAndDeferredRevenueLeasesNet"}, // BRK (brka:) NetJets extension
	},
	{
		FieldName: "DeferredRevenue", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"AccruedLiabilitiesCurrent"},
		FallbackTags:       []string{"DeferredRevenue"},
		Op:                 OpAdd,
		Operands: []string{
			"_deferredRevenueCurrent", "_deferredRevenueNoncurrent",
			"_unearnedPremiums", "_aircraftDeferredRevenue",
		},
		OptionalOperands: true,
	},
	{
		FieldName: "CurrentLiabilities", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"LiabilitiesCurrent"},
	},
	// --- Internal sub-fields for _liabilitiesNoncurrentRaw sum fallback ---
	// AMZN-pattern filers report the individual non-current liability components
	// on the face of the balance sheet (long-term debt, operating lease liability,
	// finance lease liability, other liabilities) without filing either the
	// consolidated us-gaap:Liabilities or us-gaap:LiabilitiesNoncurrent roll-up
	// tags. These sub-fields feed the fallback sum formula on
	// _liabilitiesNoncurrentRaw below.
	{
		FieldName: "_ltDebtNoncurrentForLiab", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"LongTermDebtNoncurrent",
			// KO-style filers roll long-term debt into
			// LongTermDebtAndCapitalLeaseObligations (noncurrent-only) rather
			// than LongTermDebtNoncurrent. Must appear before any
			// ...IncludingCurrentMaturities alternative to avoid pulling in
			// the current portion.
			"LongTermDebtAndCapitalLeaseObligations",
		},
	},
	{
		// AllowCrossFormFallback lets the non-current operating-lease value
		// reach the liabilities sum when the 10-K omits the concept but a
		// later 10-Q comparative tags it (MCD 2024-12-31: 12.888B on the
		// 2025-05-12 10-Q, silent on the 2025-02-25 10-K).
		// RequireQuarterly keeps this off for filers whose 10-K tags
		// OperatingLeaseLiabilityNoncurrent but the 10-Q does not (KO). For
		// those filers Sharadar excludes the concept from liabilities_non_current,
		// treating the 10-K-only disclosure as a footnote rather than a
		// balance-sheet line.
		FieldName: "_opLeaseNoncurrentForLiab", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly:       true,
		AllowCrossFormFallback: true,
		XBRLTags:               []string{"OperatingLeaseLiabilityNoncurrent"},
	},
	{
		FieldName: "_finLeaseNoncurrentForLiab", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		// RequireQuarterly keeps MCD's 10-K-only finance lease disclosure
		// out of the noncurrent liabilities sum. AMZN tags
		// FinanceLeaseLiabilityNoncurrent on every 10-Q (separate BS line)
		// so its value continues to feed the sum.
		RequireQuarterly: true,
		XBRLTags:         []string{"FinanceLeaseLiabilityNoncurrent"},
	},
	{
		FieldName: "_otherLiabilitiesNoncurrentForLiab", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"OtherLiabilitiesNoncurrent"},
	},
	// MCD breaks out deferred revenue, accrued income taxes, and deferred
	// income taxes as standalone non-current balance sheet lines without
	// filing the consolidated us-gaap:Liabilities or LiabilitiesNoncurrent
	// roll-up tags. ExcludeIfAnnual restricts these operands to that
	// MCD-style pattern: filers that file the Liabilities/LiabilitiesNoncurrent
	// roll-up directly (LLY, MSFT, AAPL) already resolve _liabilitiesNoncurrentRaw
	// via the FallbackTag path and would double-count if these operands
	// contributed to the formula recompute triggered by the MR merge step.
	{
		FieldName: "_deferredRevenueNoncurrentForLiab", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly: true,
		ExcludeIfAnnual:  []string{"Liabilities", "LiabilitiesNoncurrent"},
		XBRLTags:         []string{"DeferredRevenueNoncurrent"},
	},
	{
		FieldName: "_accruedIncomeTaxesNoncurrentForLiab", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly: true,
		ExcludeIfAnnual:  []string{"Liabilities", "LiabilitiesNoncurrent"},
		XBRLTags:         []string{"AccruedIncomeTaxesNoncurrent"},
	},
	{
		FieldName: "_deferredTaxLiabilitiesNoncurrentForLiab", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly: true,
		ExcludeIfAnnual:  []string{"Liabilities", "LiabilitiesNoncurrent"},
		XBRLTags: []string{
			"DeferredTaxLiabilitiesNoncurrent",
			"DeferredIncomeTaxLiabilitiesNet",
		},
	},
	// WMT and other retailers file a single combined line
	// "Deferred income taxes and other" on the non-current liabilities section
	// instead of separate deferred-tax and other-liabilities lines. When the
	// combined tag is the sole quarterly source, feed it into the
	// liabilities-noncurrent sum. ExcludeIfQuarterly suppresses this operand
	// for filers that break out the components individually (avoiding
	// double-count).
	{
		FieldName: "_deferredTaxAndOtherNoncurrentCombined", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly: true,
		ExcludeIfAnnual:  []string{"Liabilities", "LiabilitiesNoncurrent"},
		ExcludeIfQuarterly: []string{
			"OtherLiabilitiesNoncurrent",
			"DeferredTaxLiabilitiesNoncurrent",
			"DeferredIncomeTaxLiabilitiesNet",
		},
		XBRLTags: []string{"DeferredIncomeTaxesAndOtherLiabilitiesNoncurrent"},
	},
	// Redeemable NCI sits in the "mezzanine equity" section between
	// liabilities and equity on the balance sheet. Sharadar rolls the
	// carrying amount into total_liabilities (and thus liabilities_non_current)
	// for filers that report it alongside other non-current lines but DO NOT
	// file the consolidated Liabilities / LiabilitiesNoncurrent roll-up tags
	// (WMT). For filers that DO file Liabilities (UNH), our TotalLiabilities
	// resolves via the FallbackTag path and this operand is never summed in,
	// so the treatment matches Sharadar's exclusion there.
	{
		FieldName: "_redeemableNCI", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly: true,
		ExcludeIfAnnual:  []string{"Liabilities", "LiabilitiesNoncurrent"},
		XBRLTags:         []string{"RedeemableNoncontrollingInterestEquityCarryingAmount"},
	},
	// _liabilitiesNoncurrentRaw resolves from the direct LiabilitiesNoncurrent
	// tag when available; otherwise falls back to summing the individual
	// non-current line items. Used both for TotalLiabilities synthesis
	// (when the company doesn't file the consolidated Liabilities tag, e.g.
	// LLY, AMZN, MCD) and as the fallback for LiabilitiesNonCurrent. Filers
	// that file the consolidated us-gaap:Liabilities tag (MSFT, AAPL) resolve
	// TotalLiabilities via that tag and never consult this field's sum.
	// ContractWithCustomerLiabilityNoncurrent is intentionally NOT an operand
	// here: AMZN-style filers roll the non-current contract liability into
	// OtherLiabilitiesNoncurrent, so adding it would double-count.
	{
		FieldName: "_liabilitiesNoncurrentRaw", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags: []string{"LiabilitiesNoncurrent"},
		Op:           OpAdd,
		Operands: []string{
			"_ltDebtNoncurrentForLiab",
			"_opLeaseNoncurrentForLiab",
			"_finLeaseNoncurrentForLiab",
			"_otherLiabilitiesNoncurrentForLiab",
			"_deferredRevenueNoncurrentForLiab",
			"_accruedIncomeTaxesNoncurrentForLiab",
			"_deferredTaxLiabilitiesNoncurrentForLiab",
			"_deferredTaxAndOtherNoncurrentCombined",
			"_redeemableNCI",
		},
		OptionalOperands: true,
	},
	// TotalLiabilities: prefer the consolidated Liabilities tag; otherwise sum
	// CurrentLiabilities + LiabilitiesNoncurrent. LLY-style filers report only the
	// current/noncurrent components without the consolidated total.
	{
		FieldName: "TotalLiabilities", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags: []string{"Liabilities"},
		Op:           OpAdd,
		Operands:     []string{"CurrentLiabilities", "_liabilitiesNoncurrentRaw"},
	},
	{
		FieldName: "LiabilitiesNonCurrent", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags: []string{"LiabilitiesNoncurrent"},
		Op:           OpSubtract,
		Operands:     []string{"TotalLiabilities", "CurrentLiabilities"},
	},
	// MinorityInterest: NCI portion of stockholders' equity. Used as a subtrahend
	// when only the including-NCI equity tag is filed (UNH files
	// StockholdersEquityIncludingPortionAttributableToNoncontrollingInterest but
	// not StockholdersEquity; Sharadar's equity is the parent-only portion).
	{
		FieldName: "_minorityInterest", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"MinorityInterest"},
	},
	// _equityIncludingNCI: the full stockholders' equity including NCI. Used
	// as the fallback operand when the bare StockholdersEquity tag is absent.
	{
		FieldName: "_equityIncludingNCI", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"StockholdersEquityIncludingPortionAttributableToNoncontrollingInterest"},
	},
	// Equity: prefer the parent-only StockholdersEquity tag. Otherwise derive
	// as StockholdersEquityIncludingNCI − MinorityInterest so that filers like
	// UNH which only file the including-NCI variant still match Sharadar's
	// parent-only equity.
	{
		FieldName: "Equity", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags: []string{"StockholdersEquity"},
		Op:           OpLinearCombination,
		Operands:     []string{"_equityIncludingNCI", "_minorityInterest"},
		Coefficients: []float64{1, -1},
	},
	{
		FieldName: "AccumulatedOtherComprehensiveIncome", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"AccumulatedOtherComprehensiveIncomeLossNetOfTax",
		},
	},
	{
		FieldName: "AccumulatedRetainedEarningsDeficit", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"RetainedEarningsAccumulatedDeficit"},
	},
	// ==================== INCOME STATEMENT (flow) ====================

	// --- Internal sub-fields for Revenues derivation ---
	{
		FieldName: "_revenuesDirect", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"Revenues",
			"RevenueFromContractWithCustomerExcludingAssessedTax",
			"RevenueFromContractWithCustomerIncludingAssessedTax",
			"RevenuesNetOfInterestExpense", // Banks (JPM) report this on 10-Q instead of Revenues
			"SalesRevenueNet",
			"SalesRevenueGoodsNet",
			"SalesRevenueServicesNet",
			"InterestAndDividendIncomeOperating",
			"RegulatedAndUnregulatedOperatingRevenue",
			"HealthCareOrganizationRevenue",
			"RevenueMineralSales",
			"OilAndGasRevenue",
			"FinancialServicesRevenue",
			"ElectricUtilityRevenue",
		},
	},
	// Insurance and conglomerate companies (e.g. BRK/B) report investment
	// gains/losses as a separate income statement line below the Revenues
	// tag. Sharadar includes these in revenue because investment activity
	// is core to the business. The ExcludeIfQuarterly gate ensures this
	// only activates for companies WITHOUT a standard COGS breakdown
	// (i.e., non-standard income statements).
	{
		FieldName: "_investmentGains", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		ExcludeIfQuarterly: []string{
			"CostOfGoodsAndServicesSold",
			"CostOfRevenue",
			"CostOfGoodsSold",
			"CostOfGoodsAndServiceExcludingDepreciationDepletionAndAmortization",
			// Exclude for banks: investment gains are already included in
			// NoninterestIncome which flows into bank revenue. Adding them
			// separately would double-count.
			"Deposits",
			"DepositsDomestic",
			"DepositsTotal",
		},
		XBRLTags: []string{"GainLossOnInvestments"},
	},
	// Revenues: prefer the direct XBRL tag when available (preserves the
	// exact resolution path used before the BRK/B revenue restructuring).
	// For companies without COGS/Deposits (insurance/conglomerates like
	// BRK/B), FallbackRequireIfQuarterly skips the FallbackTags so the
	// formula adds investment gains to revenue.
	{
		FieldName: "Revenues", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		FallbackTags: []string{
			"Revenues",
			"RevenueFromContractWithCustomerExcludingAssessedTax",
			"RevenueFromContractWithCustomerIncludingAssessedTax",
			"RevenuesNetOfInterestExpense",
			"SalesRevenueNet",
			"SalesRevenueGoodsNet",
			"SalesRevenueServicesNet",
			"InterestAndDividendIncomeOperating",
			"RegulatedAndUnregulatedOperatingRevenue",
			"HealthCareOrganizationRevenue",
			"RevenueMineralSales",
			"OilAndGasRevenue",
			"FinancialServicesRevenue",
			"ElectricUtilityRevenue",
		},
		FallbackRequireIfQuarterly: []string{
			"CostOfGoodsAndServicesSold",
			"CostOfRevenue",
			"CostOfGoodsSold",
			"CostOfGoodsAndServiceExcludingDepreciationDepletionAndAmortization",
			"Deposits",
			"DepositsDomestic",
			"DepositsTotal",
		},
		Op:               OpAdd,
		Operands:         []string{"_revenuesDirect", "_investmentGains"},
		OptionalOperands: true,
	},
	// Other above-the-line expenses (non-SGA, non-COGS). For insurance/
	// conglomerates like BRK/B, the Railroad segment reports OtherExpenses
	// which Sharadar includes in OperatingExpenses. Used by
	// deriveCostOfRevenueBottomUp to correctly split COGS vs OpEx.
	{
		FieldName: "_otherExpenses", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		ExcludeIfQuarterly: []string{
			"CostOfGoodsAndServicesSold",
			"CostOfRevenue",
			"CostOfGoodsSold",
			"CostOfGoodsAndServiceExcludingDepreciationDepletionAndAmortization",
		},
		XBRLTags: []string{"OtherExpenses"},
	},
	// _cogsRaw: standard cost-of-goods-sold path. Direct, so ResolveDirect picks
	// the first matching XBRL tag.
	{
		FieldName: "_cogsRaw", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"CostOfGoodsAndServicesSold",
			"CostOfGoodsSold",
			"CostOfRevenue",
			"CostOfGoodsAndServiceExcludingDepreciationDepletionAndAmortization",
		},
	},
	// _insuranceBenefitsIncurred: health insurers (UNH) report medical costs
	// under PolicyholderBenefitsAndClaimsIncurredNet. Sharadar includes this
	// in cost_of_revenue alongside any separately-tagged CoGS. BRK-style
	// insurance conglomerates don't file this tag (their insurance costs are
	// rolled into CostsAndExpenses).
	{
		FieldName: "_insuranceBenefitsIncurred", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"PolicyholderBenefitsAndClaimsIncurredNet",
		},
	},
	// CostOfRevenue: sum of CoGS + insurance benefits incurred. For standard
	// filers only _cogsRaw resolves. Because CostOfRevenue is now Derived with
	// an empty FallbackTags, the synthesis Q4 path will use the formula sum of
	// (already de-cumulated) operands rather than a YTD cumulative value from
	// a single XBRL tag that misses the insurance component.
	{
		FieldName: "CostOfRevenue", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op:               OpAdd,
		Operands:         []string{"_cogsRaw", "_insuranceBenefitsIncurred"},
		OptionalOperands: true,
	},
	// Sub-fields for deriveCostOfRevenueForSegmentFiler. MCD tags
	// CostOfGoodsAndServicesSold only for the franchised-occupancy slice
	// (~620M Q1 2025) while the company-operated restaurant expenses are
	// disclosed on the income statement without a consolidating us-gaap
	// tag. Sharadar reconstructs cost_of_revenue as
	// CostsAndExpenses - SG&A - SegmentReportingOtherItemAmount, using the
	// broader SellingGeneralAndAdministrativeExpense (not OtherSG&A) and
	// the SegmentReportingOtherItemAmount corporate-segment disclosure.
	// Only MCD-style restaurant filers use this pattern; no regression
	// ticker files SegmentReportingOtherItemAmount.
	{
		FieldName: "_costsAndExpensesRaw", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"CostsAndExpenses"},
	},
	{
		FieldName: "_segmentReportingOtherItem", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"SegmentReportingOtherItemAmount"},
	},
	{
		FieldName: "_sgaBroad", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"SellingGeneralAndAdministrativeExpense"},
	},
	// Sub-fields for deriveCostOfRevenueForRestaurantFiler (TXRH-style).
	// Restaurant filers that report D&A, PreOpening, and Impairment as
	// separate income-statement lines (not embedded in CoR) and tag rent
	// with a company-specific extension concept. Sharadar treats all of
	// these — plus G&A — as OpEx and leaves only food/labor/other-operating
	// in CoR.
	{
		FieldName: "_preOpeningCosts", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"PreOpeningCosts"},
	},
	{
		FieldName: "_restaurantImpairmentProvisions", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"RestructuringSettlementAndImpairmentProvisions",
			"AssetImpairmentCharges",
			"ImpairmentOfLongLivedAssetsHeldForUse",
		},
	},
	{
		FieldName: "_depreciationDepletionAndAmortization", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"DepreciationDepletionAndAmortization"},
	},
	// _rentInCostOfRevenue: the "Rent" line on restaurant income statements.
	// Companies tag this with an extension concept (e.g. txrh:
	// RentAndLeaseExpenseIncludedInCostOfRevenue). EnrichWithExtensionFacts
	// pulls extension facts from inline XBRL filings under the bare local
	// name, so these match the namespaced filings without the prefix.
	{
		FieldName: "_rentInCostOfRevenue", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"RentAndLeaseExpenseIncludedInCostOfRevenue",
			"RentExpenseIncludedInCostOfRevenue",
		},
	},
	// GrossProfit: use the direct tag if available; otherwise derive from
	// Revenues − CostOfRevenue. Banks (JPM) do not report GrossProfit or
	// CostOfRevenue; with CostOfRevenue absent (treated as 0), the derived
	// value equals Revenues -- matching Sharadar's treatment of bank revenue
	// as 100% gross.
	{
		FieldName: "GrossProfit", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		FallbackTags:     []string{"GrossProfit"},
		Op:               OpLinearCombination,
		Operands:         []string{"Revenues", "CostOfRevenue"},
		Coefficients:     []float64{1, -1},
		OptionalOperands: true,
	},
	// --- Internal sub-fields for SG&A derivation ---
	{
		FieldName: "_generalAndAdministrativeExpense", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags:  []string{"GeneralAndAdministrativeExpense"},
	},
	{
		FieldName: "_sellingAndMarketingExpense", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"SellingAndMarketingExpense",
			"SellingExpense",
			// AMZN files sales-and-marketing under the bare MarketingExpense tag.
			"MarketingExpense",
		},
	},
	// SGA = GeneralAndAdministrativeExpense + SellingAndMarketingExpense.
	// Companies like Apple report a combined SellingGeneralAndAdministrativeExpense;
	// companies like Microsoft report the two components separately. Sharadar
	// always reports the combined total.
	//
	// OtherSellingGeneralAndAdministrativeExpense is tried first for filers
	// (MCD) that split SG&A into a broader bucket plus an income-statement
	// "Other SG&A" line — Sharadar reports the line-item value (OtherSGA),
	// not the broader bucket. Filers that don't file OtherSGA fall through.
	// KO files a small OtherSGA carve-out (~58M/quarter) alongside the main
	// SGA line (3.6B/quarter); a post-resolution override in mapping.go
	// (overrideSGAForSmallOtherSGA) swaps to SGA for that pattern.
	{
		FieldName: "SellingGeneralAndAdministrativeExpense", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		FallbackTags: []string{
			"OtherSellingGeneralAndAdministrativeExpense",
			"SellingGeneralAndAdministrativeExpense",
		},
		Op:               OpAdd,
		Operands:         []string{"_generalAndAdministrativeExpense", "_sellingAndMarketingExpense"},
		OptionalOperands: true,
	},
	{
		FieldName: "RandDExpenses", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		// LLY (and other pharma) report ResearchAndDevelopmentExpenseExcludingAcquiredInProcessCost
		// to break out acquired in-process R&D charges as a separate income-statement line.
		// Sharadar's r_and_d_expenses uses the ex-IPRD value for these filers.
		// AMZN reports its technology spend under the amzn:TechnologyAndInfrastructureExpense
		// extension tag rather than a us-gaap R&D concept; Sharadar maps it into
		// r_and_d_expenses.
		XBRLTags: []string{
			"ResearchAndDevelopmentExpense",
			"ResearchAndDevelopmentExpenseExcludingAcquiredInProcessCost",
			"TechnologyAndInfrastructureExpense",
		},
	},
	// CostsAndExpenses is total income statement costs. Used to derive
	// OperatingIncome for companies without an explicit OperatingIncomeLoss
	// tag (insurance/conglomerates like BRK/B).
	{
		FieldName: "_costsAndExpenses", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"CostsAndExpenses"},
	},
	// _interestExpenseNonoperatingFallback: only used when the company files
	// OperatingIncomeLoss. Sharadar's interestexp is the operating-line interest
	// component; for filers that don't have an OperatingIncomeLoss subtotal
	// (LLY, AAPL post-FY2023), interest is wholly nonoperating and Sharadar
	// reports 0. MSFT files OperatingIncomeLoss and switched to publishing only
	// InterestExpenseNonoperating starting FY2025 — this fallback keeps the
	// MSFT 10-Q value resolving while LLY/AAPL drop to 0.
	{
		FieldName: "_interestExpenseNonoperatingFallback", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		ExcludeIfQuarterly: []string{"Deposits"},
		RequireIfQuarterly: []string{"OperatingIncomeLoss"},
		XBRLTags:           []string{"InterestExpenseNonoperating"},
	},
	// _interestIncomeExpenseNetFallback: CALM-style filers that report only
	// InterestIncomeExpenseNet (positive = net expense, negative = net income)
	// and never file InterestExpense/InterestExpenseNonoperating. Sharadar
	// treats the signed net value as interest_expense for such filers (a
	// cash-rich filer with interest income > expense shows negative
	// interest_expense, which subtracts from EBIT correctly since the net
	// income already includes that interest income).
	//
	// Gated to avoid interfering with filers that have dedicated InterestExpense
	// facts: ExcludeIfQuarterly on InterestExpense/InterestExpenseNonoperating
	// so only filers lacking both tags use this net fallback.
	{
		FieldName: "_interestIncomeExpenseNetFallback", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		ExcludeIfQuarterly: []string{
			"Deposits",
			"InterestExpense",
			"InterestExpenseDebt",
			"InterestExpenseNonoperating",
		},
		RequireIfQuarterly: []string{"OperatingIncomeLoss"},
		// InterestIncomeExpenseNonoperatingNet is the post-2018 successor tag
		// for filers (TXRH) that used InterestIncomeExpenseNet historically;
		// both carry the same signed-net semantics so Sharadar treats them
		// interchangeably for interest_expense.
		XBRLTags: []string{
			"InterestIncomeExpenseNet",
			"InterestIncomeExpenseNonoperatingNet",
		},
	},
	// Retailer-style filers (WMT) report InterestExpenseDebt and
	// FinanceLeaseInterestExpense separately on the income statement, along
	// with InterestIncomeExpenseNonoperatingNet (net expense minus income)
	// and InvestmentIncomeInterest (interest income). Sharadar's
	// interest_expense for this pattern equals the gross interest expense:
	// -InterestIncomeExpenseNonoperatingNet + InvestmentIncomeInterest.
	// This yields the same quarterly values as InterestExpenseDebt + FinLease
	// interest for Q1-Q3 but diverges in Q4 synthesis — Sharadar's annual
	// sums Q1-Q4 of the net-plus-income formula rather than the raw XBRL
	// annual of InterestExpenseDebt + FinLease. Gate on InterestExpenseDebt
	// filed quarterly so only WMT-style filers pick up these operands.
	{
		FieldName: "_retailerInterestNet", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		RequireIfQuarterly: []string{"InterestExpenseDebt"},
		Negate:             true, // flip signed net to positive gross expense
		// WMT rebadged its original InterestIncomeExpenseNet to the newer
		// InterestIncomeExpenseNonoperatingNet starting Q2 FY25; fall through
		// to the legacy tag so the AR window for Q1 FY25 still resolves.
		XBRLTags: []string{
			"InterestIncomeExpenseNonoperatingNet",
			"InterestIncomeExpenseNet",
		},
	},
	{
		FieldName: "_retailerInvestmentIncomeInterest", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		RequireIfQuarterly: []string{"InterestExpenseDebt"},
		XBRLTags:           []string{"InvestmentIncomeInterest"},
	},
	// InterestExpense: must come before OperatingIncome since the derived
	// OperatingIncome formula uses it as an operand.
	{
		FieldName: "InterestExpense", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		// Do NOT add InterestIncomeExpenseNet here: it is a signed net value
		// (positive when net expense, negative when net income) and feeds
		// directly into EBIT = NetIncome + IncomeTaxExpense + InterestExpense.
		// For cash-rich companies with interest income > expense, using the
		// net value would incorrectly subtract from EBIT.
		//
		// ExcludeIfQuarterly on Deposits: banks (JPM) file the generic
		// InterestExpense tag for some quarters but not others, and for banks
		// this is operational interest (cost of deposits) — NOT financing
		// expense. Including it in EBIT breaks Q4 synthesis (annual has 0
		// but one quarter has 24B). Gating on Deposits (a bank-specific
		// liability) excludes real banks while allowing insurance
		// conglomerates like BRK/B that also lack AssetsCurrent.
		//
		// RequireIfQuarterly on OperatingIncomeLoss / CostsAndExpenses:
		// pharma-style filers (LLY) present an income statement without an
		// operating-income subtotal AND without a CostsAndExpenses aggregate.
		// Sharadar reports interestexp = 0 for such filers (interest is wholly
		// nonoperating). Gating here prevents stray InterestExpense facts (e.g.
		// LLY filed it once for Q1 2024) from leaking into ART/MRT windows.
		//
		// FallbackTags resolve directly when the company files InterestExpense
		// or InterestExpenseDebt. Otherwise the formula adds the Nonoperating
		// sub-field, which is itself gated on OperatingIncomeLoss so that
		// pharma-style filers without an operating-income line (LLY) don't
		// pull a phantom interest_expense that Sharadar treats as 0.
		ExcludeIfQuarterly: []string{"Deposits"},
		RequireIfQuarterly: []string{"OperatingIncomeLoss", "CostsAndExpenses"},
		FallbackTags: []string{
			"InterestExpense",
			"InterestExpenseDebt",
		},
		// WMT-style filers report InterestExpenseDebt but Sharadar computes
		// interest_expense via the net non-operating formula instead (adding
		// back interest income). Skip the FallbackTags path when
		// InterestExpenseDebt is filed quarterly so the retailer operands win.
		FallbackExcludeIfQuarterly: []string{"InterestExpenseDebt"},
		Op:                         OpLinearCombination,
		Operands: []string{
			"_interestExpenseNonoperatingFallback",
			"_interestIncomeExpenseNetFallback",
			"_retailerInterestNet",
			"_retailerInvestmentIncomeInterest",
		},
		Coefficients:     []float64{1, -1, 1, 1},
		OptionalOperands: true,
	},
	// _operatingIncomeFromFormula: BRK-style insurance/conglomerate path.
	// Computes Revenues - CostsAndExpenses + InterestExpense to capture
	// investment gains in the operating-income line. Only resolves when
	// CostsAndExpenses is filed (gated implicitly by OpLinearCombination
	// requiring all operands; the gate on CostsAndExpenses also keeps this
	// from competing with the EBT fallback below).
	{
		FieldName: "_operatingIncomeFromFormula", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op:           OpLinearCombination,
		Operands:     []string{"Revenues", "_costsAndExpenses", "InterestExpense"},
		Coefficients: []float64{1, -1, 1},
	},
	// _operatingIncomeEbtFallback: pharma-style filers (LLY) that do not
	// publish an OperatingIncomeLoss subtotal AND do not file CostsAndExpenses.
	// Their income statement runs straight from Revenue through individual
	// expense lines to "Income before income taxes". Sharadar uses that EBT
	// value as opinc since for these filers interest expense is treated as 0
	// (see _interestExpenseNonoperatingFallback) and other-net is netted in.
	// ExcludeIfQuarterly gates ensure this only fires when neither
	// OperatingIncomeLoss nor CostsAndExpenses is available — and not for
	// banks (which use the GrossProfit - OperatingExpenses recompute path
	// in overrideNCFDebtResidual after this resolver runs).
	{
		FieldName: "_operatingIncomeEbtFallback", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		ExcludeIfQuarterly: []string{
			"CostsAndExpenses", "OperatingIncomeLoss",
			"Deposits", "DepositsDomestic", "DepositsTotal",
			"CustomerAndOtherPayables", // broker-dealers (GS)
		},
		XBRLTags: []string{
			"IncomeLossFromContinuingOperationsBeforeIncomeTaxesExtraordinaryItemsNoncontrollingInterest",
			"IncomeLossFromContinuingOperationsBeforeIncomeTaxesMinorityInterestAndIncomeLossFromEquityMethodInvestments",
		},
	},
	// OperatingIncome: use the direct OperatingIncomeLoss tag when available.
	// Otherwise sum the two mutually-exclusive sub-fields above — only one
	// will resolve per company depending on income-statement structure.
	{
		FieldName: "OperatingIncome", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		FallbackTags:     []string{"OperatingIncomeLoss"},
		Op:               OpAdd,
		Operands:         []string{"_operatingIncomeFromFormula", "_operatingIncomeEbtFallback"},
		OptionalOperands: true,
	},
	// OperatingExpenses: use the direct tag if available; otherwise derive
	// from GrossProfit − OperatingIncome. Sharadar defines OpEx as SGA +
	// R&D (excluding CoR). MSFT omits the OperatingExpenses XBRL tag in
	// older filings, but GrossProfit and OperatingIncome are always present.
	// Banks (JPM) report NoninterestExpense which maps to Sharadar OpEx.
	// Must come AFTER OperatingIncome so the dependency is resolved first.
	// For banks that don't report OperatingIncomeLoss, OperatingIncome is
	// recomputed as GrossProfit - OperatingExpenses in overrideNCFDebtResidual.
	{
		FieldName: "OperatingExpenses", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		FallbackTags: []string{"OperatingExpenses", "NoninterestExpense"},
		Op:           OpSubtract,
		Operands:     []string{"GrossProfit", "OperatingIncome"},
	},
	{
		FieldName: "IncomeTaxExpense", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"IncomeTaxExpenseBenefit",
			"IncomeTaxesPaid",
		},
	},
	{
		FieldName: "NetIncomeCommonStock", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"NetIncomeLossAvailableToCommonStockholdersBasic",
			"NetIncomeLoss",
		},
	},
	{
		// Sharadar's `netinc` is "net income attributable to parent, after NCI,
		// before preferred dividends". When the company reports
		// PreferredStockDividendsIncomeStatementImpact as a separate line (GS),
		// NetIncomeLoss already represents that before-preferred value -- use it
		// directly. When the company does NOT report that tag (JPM), the preferred
		// deduction can't be distinguished from NCI reliably, so Sharadar falls
		// back to NetIncomeLossAvailableToCommonStockholdersBasic (= after both).
		// The FallbackRequireIfQuarterly gate triggers on the preferred-impact tag.
		FieldName: "NetIncome", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		FallbackTags: []string{
			"NetIncomeLoss",
			"ProfitLoss",
		},
		FallbackRequireIfQuarterly: []string{
			"PreferredStockDividendsIncomeStatementImpact",
			"PreferredStockDividendsAndOtherAdjustments",
		},
		Op:               OpAdd,
		Operands:         []string{"NetIncomeCommonStock"},
		OptionalOperands: true,
	},
	{
		FieldName: "ConsolidatedIncome", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"ProfitLoss",
			"IncomeLossFromContinuingOperationsIncludingPortionAttributableToNoncontrollingInterest",
			"NetIncomeLoss",
		},
	},
	{
		FieldName: "NetLossIncomeDiscontinuedOperations", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"IncomeLossFromDiscontinuedOperationsNetOfTax",
			"DiscontinuedOperationIncomeLossFromDiscontinuedOperationDuringPhaseOutPeriodNetOfTax",
		},
	},
	// NetIncomeToNonControllingInterests: use the direct tag if available.
	// Banks (JPM) don't report this tag; derive as ConsolidatedIncome -
	// NetIncomeCommonStock - PreferredDividends. NetIncomeCommonStock uses
	// the "available to common" value (after preferred); the residual after
	// subtracting preferred is the NCI portion. Uses NetIncomeCommonStock
	// rather than NetIncome because NetIncome may resolve to a before-preferred
	// value (GS pattern), which would break this identity.
	{
		FieldName: "NetIncomeToNonControllingInterests", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		FallbackTags: []string{
			"NetIncomeLossAttributableToNoncontrollingInterest",
		},
		Op:               OpLinearCombination,
		Operands:         []string{"ConsolidatedIncome", "NetIncomeCommonStock", "PreferredDividendsIncomeStatementImpact"},
		Coefficients:     []float64{1, -1, -1},
		OptionalOperands: true,
	},
	{
		FieldName: "PreferredDividendsIncomeStatementImpact", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"PreferredStockDividendsIncomeStatementImpact",
			"PreferredStockDividendsAndOtherAdjustments",
		},
	},

	// ==================== PER-SHARE DATA (flow) ====================

	{
		FieldName: "EPS", Type: MappingDirect, StatementType: StmtFlow, ValueType: "float64",
		XBRLTags: []string{"EarningsPerShareBasic"},
	},
	{
		// EPSDiluted: use the XBRL tag if available; otherwise derive from
		// NetIncomeCommonStock / WeightedAverageSharesDiluted. Multi-class
		// filers like BRK/B don't report EarningsPerShareDiluted but do
		// report EarningsPerShareBasic for Class B (where basic ≈ diluted).
		FieldName: "EPSDiluted", Type: MappingDerived, StatementType: StmtFlow, ValueType: "float64",
		FallbackTags: []string{"EarningsPerShareDiluted", "EarningsPerShareBasic"},
		Op:           OpDivide,
		Operands:     []string{"NetIncomeCommonStock", "WeightedAverageSharesDiluted"},
		RoundDigits:  6,
	},
	{
		FieldName: "DividendsPerBasicCommonShare", Type: MappingDirect, StatementType: StmtFlow, ValueType: "float64",
		XBRLTags: []string{
			"CommonStockDividendsPerShareDeclared",
			"CommonStockDividendsPerShareCashPaid",
		},
	},
	// Internal: absolute cash dividends paid to common stockholders. Used to
	// compute cash-paid DPS (= _absDividendsPaid / WeightedAverageShares) post
	// de-cumulation. The PaymentsOfDividends fallback covers filers (AAPL, LLY)
	// that don't file the common-only variant. AAPL's PaymentsOfDividends
	// includes dividend equivalents on RSUs (cash-paid runs slightly above
	// declared per share); the cash-paid override is gated in
	// OverrideDPSFromCash on the company exhibiting non-quarterly declaration
	// cadence so AAPL keeps the declared value while LLY (which declares
	// semi-annually) drops to the cash-paid value.
	{
		FieldName: "_absDividendsPaid", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"PaymentsOfDividendsCommonStock",
			"PaymentsOfDividends",
		},
	},

	// ==================== SHARE COUNTS ====================

	{
		FieldName: "SharesBasic", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"EntityCommonStockSharesOutstanding", // DEI cover-page count (matches Sharadar sharesbas)
			"CommonStockSharesOutstanding",       // us-gaap balance sheet fallback
		},
	},
	{
		// Weighted average shares are period-average values. StmtPeriodAverage
		// ensures Q4 synthesis computes Q4 = annual*4 - sum(Q1..Q3) (since the
		// annual value is the average of 4 quarterly values, not a cumulative
		// sum) and TTM uses the latest quarter's value.
		FieldName: "WeightedAverageShares", Type: MappingDirect, StatementType: StmtPeriodAverage, ValueType: "int64",
		XBRLTags: []string{
			"WeightedAverageNumberOfShareOutstandingBasicAndDiluted",
			"WeightedAverageNumberOfSharesOutstandingBasic",
		},
	},
	{
		// See WeightedAverageShares comment above for StatementType rationale.
		FieldName: "WeightedAverageSharesDiluted", Type: MappingDirect, StatementType: StmtPeriodAverage, ValueType: "int64",
		// WeightedAverageNumberOfSharesOutstandingBasic is the final fallback
		// because in loss periods the diluted count is antidilutive and many
		// filers omit the diluted tag entirely; in those cases basic ≡ diluted.
		XBRLTags: []string{
			"WeightedAverageNumberOfDilutedSharesOutstanding",
			"WeightedAverageNumberOfShareOutstandingBasicAndDiluted",
			"WeightedAverageNumberOfSharesOutstandingBasic",
		},
	},

	// ==================== CASH FLOW STATEMENT (flow) ====================

	{
		FieldName: "NetCashFlowFromOperations", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"NetCashProvidedByUsedInOperatingActivities",
			"NetCashProvidedByUsedInOperatingActivitiesContinuingOperations",
		},
	},
	{
		FieldName: "NetCashFlowFromInvesting", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"NetCashProvidedByUsedInInvestingActivities",
			"NetCashProvidedByUsedInInvestingActivitiesContinuingOperations",
		},
	},
	{
		FieldName: "NetCashFlowFromFinancing", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"NetCashProvidedByUsedInFinancingActivities",
			"NetCashProvidedByUsedInFinancingActivitiesContinuingOperations",
		},
	},
	// --- Internal sub-fields for D&A derivation ---
	{
		FieldName: "_depreciation", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"Depreciation"},
	},
	{
		FieldName: "_amortizationOfIntangibles", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"AmortizationOfIntangibleAssets"},
	},
	{
		FieldName: "_financeLeaseAmortization", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"FinanceLeaseRightOfUseAssetAmortization"},
	},
	// D&A: use the combined tag if available (AAPL files
	// DepreciationDepletionAndAmortization); otherwise sum components.
	// MSFT reports Depreciation, AmortizationOfIntangibleAssets, and
	// FinanceLeaseRightOfUseAssetAmortization separately.
	//
	// DepreciationAndAmortization is tried first because some filers (MCD)
	// file DepreciationDepletionAndAmortization as a partial sub-measure
	// while DepreciationAndAmortization holds the full cash-flow-statement
	// total. Filers that only file DepreciationDepletionAndAmortization
	// (AAPL, AMZN, NVDA, LLY, BRK/B) still resolve correctly via the
	// fallthrough because DepreciationAndAmortization is absent for them.
	{
		FieldName: "DepreciationAmortizationAndAccretion", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		FallbackTags: []string{
			"DepreciationAndAmortization",
			"DepreciationDepletionAndAmortization",
			"DepreciationAmortizationAndAccretionNet",
			"DepreciationAmortizationAndOther", // MSFT extension tag for cash flow D&A line
		},
		Op:               OpAdd,
		Operands:         []string{"_depreciation", "_amortizationOfIntangibles", "_financeLeaseAmortization"},
		OptionalOperands: true,
	},
	// --- Internal sub-fields for CapitalExpenditure derivation ---
	{
		FieldName: "_capexGross", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"PaymentsToAcquirePropertyPlantAndEquipment",
			"PaymentsToAcquireProductiveAssets",
			"CapitalExpendituresIncurredButNotYetPaid",
			// LLY tags PP&E capex as PaymentsToAcquireOtherPropertyPlantAndEquipment
			// (with Other in the name even though it is the company's primary capex line).
			"PaymentsToAcquireOtherPropertyPlantAndEquipment",
		},
	},
	// Proceeds from disposing of PP&E are netted against gross capex for
	// companies that report both (e.g. GS sells real estate and back-leases
	// it). Standard companies (AAPL, MSFT, NVDA) don't file this tag so
	// CapitalExpenditure reduces to -_capexGross for them.
	{
		FieldName: "_proceedsPPESale", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"ProceedsFromSaleOfPropertyPlantAndEquipment",
			// AMZN files a combined extension covering PP&E sale proceeds
			// plus vendor incentive reimbursements that reduce capex.
			// Sharadar nets this against gross capex in the same way.
			"ProceedsFromPropertyPlantAndEquipmentSalesAndIncentives",
		},
	},
	// CapitalExpenditure = -(gross capex - proceeds from PP&E sale). Net of
	// proceeds so disposals reduce capex outflow. Sharadar matches this
	// convention for filers that report proceeds (e.g. GS 2024: 2,091M gross,
	// 1,613M proceeds → -478M net).
	{
		FieldName: "CapitalExpenditure", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op:               OpLinearCombination,
		Operands:         []string{"_capexGross", "_proceedsPPESale"},
		Coefficients:     []float64{-1, 1},
		OptionalOperands: true,
	},
	// Sharadar reports 0 for share-based compensation for commercial banks
	// (JPM, BAC, WFC): they include SBC in LaborAndRelatedExpense and Sharadar
	// does not separate it. Investment banks (GS, MS) DO report SBC separately
	// and Sharadar captures it. Gate on AssetsCurrent OR the CustomerAndOther
	// Payables extension (GS-style investment bank marker) -- the OR semantics
	// allow resolution for standard companies AND investment banks without
	// affecting commercial banks.
	{
		FieldName: "ShareBasedCompensation", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		RequireIfQuarterly: []string{"AssetsCurrent", "CustomerAndOtherPayables"},
		XBRLTags: []string{
			"ShareBasedCompensation",
		},
	},
	// --- Internal sub-fields for NetCashFlowBusiness derivation ---
	{
		FieldName: "_paymentsBusinessAcquire", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			// AMZN only files the combined businesses + nonmarketable
			// securities extension on the 10-K. Put it first so the FY
			// value wins; 10-Qs (which don't file it) fall through to the
			// standard us-gaap tag.
			"PaymentsToAcquireBusinessesNetOfCashAcquiredAndPaymentsToAcquireNonmarketableSecuritiesAndOther",
			"PaymentsToAcquireBusinessesNetOfCashAcquired",
			"PaymentsToAcquireBusinessesGross",
			"AcquisitionsNetOfCashAcquiredAndPurchasesOfIntangibleAndOtherAssets", // MSFT extension
			"PaymentsForProceedsFromBusinessesAndInterestInAffiliates",            // broker-dealers (GS) combining acquisitions and affiliate interests
			"OtherPaymentsToAcquireBusinesses",                                    // LLY uses this tag for their primary acquisition spend
			// KO extension: combined "Acquisitions of businesses, equity method
			// investments and nonmarketable securities" line. KO never files the
			// standard us-gaap acquisition tags, so there's no double-count risk.
			// The 10-K and 10-Q use different casing for the same concept.
			"AcquisitionsOfBusinessesEquityMethodInvestmentsAndNonmarketableSecurities", // 10-K casing
			"Acquisitionsofbusinessesequitymethodinvestmentsandnonmarketablesecurities", // 10-Q casing
		},
	},
	// CALM-style joint-venture / equity-method investments included by Sharadar
	// in NCF_business. _paymentsJVAcquire is a cash outflow (like acquisitions);
	// _proceedsJVDivest and _proceedsEquityMethodReturn are inflows.
	{
		FieldName: "_paymentsJVAcquire", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"PaymentsToAcquireInterestInJointVenture"},
	},
	{
		FieldName: "_proceedsJVDivest", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"ProceedsFromDivestitureOfInterestInJointVenture"},
	},
	{
		FieldName: "_proceedsEquityMethodReturn", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"ProceedsFromEquityMethodInvestmentDividendsOrDistributionsReturnOfCapital"},
	},
	{
		FieldName: "_proceedsDivestBusinesses", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"ProceedsFromDivestitureOfBusinesses",
			// KO extension: combined "Proceeds from disposals of businesses,
			// equity method investments and nonmarketable securities" line.
			// The 10-K and 10-Q use different casing for the same concept.
			"ProceedsFromDisposalsOfBusinessesEquityMethodInvestmentsAndNonmarketableSecurities", // 10-K casing
			"ProceedsfromDisposalsofbusinessesequitymethodinvestmentsandnonmarketablesecurities", // 10-Q casing
		},
	},
	// MCD-style filers that don't tag PaymentsToAcquireBusinesses* at all
	// report their franchisee / real-estate acquisitions under the
	// "other productive assets" pair instead. Sharadar folds that pair into
	// NCF_business for those filers (FY2024: 311 - 669 = -358M). Gate with
	// ExcludeIfQuarterly on the standard business-acquisition tags so
	// filers that report both (e.g. LLY, which tags
	// PaymentsToAcquireBusinesses*) don't pick these up and double-count.
	{
		FieldName: "_paymentsOtherProductiveAssets", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"PaymentsToAcquireOtherProductiveAssets"},
		ExcludeIfQuarterly: []string{
			"PaymentsToAcquireBusinessesNetOfCashAcquiredAndPaymentsToAcquireNonmarketableSecuritiesAndOther",
			"PaymentsToAcquireBusinessesNetOfCashAcquired",
			"PaymentsToAcquireBusinessesGross",
			"AcquisitionsNetOfCashAcquiredAndPurchasesOfIntangibleAndOtherAssets",
			"PaymentsForProceedsFromBusinessesAndInterestInAffiliates",
			"OtherPaymentsToAcquireBusinesses",
		},
	},
	{
		FieldName: "_proceedsOtherProductiveAssets", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"ProceedsFromSaleOfOtherProductiveAssets"},
		ExcludeIfQuarterly: []string{
			"PaymentsToAcquireBusinessesNetOfCashAcquiredAndPaymentsToAcquireNonmarketableSecuritiesAndOther",
			"PaymentsToAcquireBusinessesNetOfCashAcquired",
			"PaymentsToAcquireBusinessesGross",
			"AcquisitionsNetOfCashAcquiredAndPurchasesOfIntangibleAndOtherAssets",
			"PaymentsForProceedsFromBusinessesAndInterestInAffiliates",
			"OtherPaymentsToAcquireBusinesses",
		},
	},
	// NetCashFlowBusiness = -acquisitions - jv_payments + divestitures + equity_method_returns
	// + (-other_productive_asset_payments + other_productive_asset_proceeds) for MCD-style filers.
	// Inflows (divestitures, returns) are positive; outflows are negative.
	{
		FieldName: "NetCashFlowBusiness", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op: OpLinearCombination,
		Operands: []string{
			"_paymentsBusinessAcquire",
			"_paymentsJVAcquire",
			"_proceedsJVDivest",
			"_proceedsEquityMethodReturn",
			"_proceedsBusinessEquityMethod",
			"_proceedsDivestBusinesses",
			"_paymentsOtherProductiveAssets",
			"_proceedsOtherProductiveAssets",
		},
		Coefficients:     []float64{-1, -1, 1, 1, 1, 1, -1, 1},
		OptionalOperands: true,
	},
	// --- Internal sub-fields for NetCashFlowCommon derivation ---
	{
		FieldName: "_paymentsRepurchaseCommon", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"PaymentsForRepurchaseOfCommonStock"},
	},
	{
		FieldName: "_proceedsIssuanceCommon", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"ProceedsFromIssuanceOfCommonStock",
			"ProceedsFromStockOptionsExercised",
			"ProceedsFromStockPlans", // NVDA uses this instead of the above
		},
	},
	// Tax withholding for share-based compensation: some companies (NVDA)
	// bundle this into the stock repurchase section of the cash flow
	// statement when they start filing it on 10-Q. RequireQuarterly gates
	// this to only resolve when the tag appears on 10-Q. For companies
	// that always file it on 10-Q (AAPL, MSFT), the tag IS already
	// embedded in PaymentsForRepurchaseOfCommonStock, so adding it would
	// double-count. RequireIfQuarterly on AccruedLiabilitiesCurrent
	// excludes those companies. TXRH files OtherAccruedLiabilitiesCurrent
	// instead of the generic AccruedLiabilitiesCurrent, so both names are
	// listed as alternative signals.
	{
		FieldName: "_taxWithholdingShareComp", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		RequireQuarterly:   true,
		RequireIfQuarterly: []string{"AccruedLiabilitiesCurrent", "OtherAccruedLiabilitiesCurrent"},
		XBRLTags:           []string{"PaymentsRelatedToTaxWithholdingForShareBasedCompensation"},
	},
	// NetCashFlowCommon = −repurchases − taxWithholding + proceeds.
	// The _taxWithholdingShareComp operand only resolves for companies that
	// bundle it (RequireQuarterly + RequireIfQuarterly gate). For annual
	// dimensions (ARY/MRY), the sub-field is stripped before emission to
	// prevent the 10-K value from being included (see emitFundamentals).
	{
		FieldName: "NetCashFlowCommon", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op:               OpLinearCombination,
		Operands:         []string{"_paymentsRepurchaseCommon", "_proceedsIssuanceCommon", "_taxWithholdingShareComp"},
		Coefficients:     []float64{-1, 1, -1},
		OptionalOperands: true,
	},
	// --- Internal sub-fields for NetCashFlowDebt derivation ---
	{
		FieldName: "_proceedsDebt", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"ProceedsFromIssuanceOfLongTermDebt",
			"ProceedsFromIssuanceOfDebt",
			"ProceedsFromDebtNetOfIssuanceCosts",
			"ProceedsFromDebtMaturingInMoreThanThreeMonths",
		},
	},
	{
		FieldName: "_repaymentsDebt", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"RepaymentsOfLongTermDebt",
			"RepaymentsOfDebt",
			"RepaymentsOfLongTermDebtAndCapitalSecurities",
			"RepaymentsOfDebtMaturingInMoreThanThreeMonths",
			// KO reports repayments of long-term debt and finance-lease
			// obligations in a single combined line.
			"RepaymentsOfDebtAndCapitalLeaseObligations",
		},
	},
	// Some companies (NVDA) report payments on financed PP&E as a separate
	// financing line item using an extension tag. This is a debt-like
	// payment that Sharadar includes in NCFDEBT.
	{
		FieldName: "_repaymentsFinancedAssets", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"PaymentsForFinancedPropertyPlantAndEquipmentAndIntangibleAssetsFinancingActivities",
		},
	},
	// Debt issuance costs (credit-facility amendment fees, underwriting
	// fees, etc.) are a financing-activities cash outflow that Sharadar
	// includes in NCFDEBT.
	{
		FieldName: "_paymentsDebtIssuanceCosts", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"PaymentsOfDebtIssuanceCosts"},
	},
	{
		FieldName: "_netShortTermDebt", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"ProceedsFromRepaymentsOfCommercialPaper",
			"ProceedsFromRepaymentsOfShortTermDebt",
			"ProceedsFromRepaymentsOfShortTermDebtMaturingInThreeMonthsOrLess",
		},
	},
	// Gross short-term debt proceeds/repayments: AMZN reports ST debt as
	// separate inflow and outflow tags rather than the net tag captured by
	// _netShortTermDebt. Both are optional operands of NetCashFlowDebt so
	// filers using either presentation resolve correctly.
	{
		FieldName: "_proceedsShortTermDebt", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"ProceedsFromShortTermDebt"},
	},
	{
		FieldName: "_repaymentsShortTermDebt", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"RepaymentsOfShortTermDebt"},
	},
	// Finance lease principal payments: Sharadar treats these as debt
	// repayments in NCFDEBT for filers (AMZN) that report them as a
	// separate cash-flow line item. MSFT also files this tag but Sharadar
	// excludes it from their NCFDEBT (MSFT reports it as a supplemental
	// disclosure rather than a financing-activities line). Gate on
	// ProceedsFromShortTermDebt: AMZN files gross ST debt proceeds on the
	// financing section alongside the finance lease payment; MSFT does not
	// file ProceedsFromShortTermDebt.
	{
		FieldName: "_financeLeasePrincipalPayments", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		RequireIfQuarterly: []string{"ProceedsFromShortTermDebt"},
		XBRLTags:           []string{"FinanceLeasePrincipalPayments"},
	},
	// Financing obligation repayments: AMZN files an extension tag for
	// repayments on long-term financing obligations (sale-leaseback style).
	// Sharadar includes these in NCFDEBT.
	{
		FieldName: "_repaymentsFinancingObligations", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"RepaymentsOfLongTermFinancingObligations"},
	},
	// NetCashFlowDebt = proceeds - repayments - financedAssets + netShortTermDebt
	// + proceedsShortTermDebt - repaymentsShortTermDebt - financeLeasePrincipal
	// - repaymentsFinancingObligations
	// (Sharadar NCFDEBT: "net cash inflow (outflow) from issuance (repayment) of
	// debt securities"). The financedAssets component captures extension-tag
	// payments on financed PP&E (e.g. NVDA).
	{
		FieldName: "NetCashFlowDebt", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op: OpLinearCombination,
		Operands: []string{
			"_proceedsDebt", "_repaymentsDebt", "_repaymentsFinancedAssets", "_netShortTermDebt",
			"_proceedsShortTermDebt", "_repaymentsShortTermDebt",
			"_financeLeasePrincipalPayments", "_repaymentsFinancingObligations",
			"_paymentsDebtIssuanceCosts",
		},
		Coefficients:     []float64{1, -1, -1, 1, 1, -1, -1, -1, -1},
		OptionalOperands: true,
	},
	// --- Bank-specific NCFDEBT sub-fields ---
	// Banks (JPM) have additional debt-related financing activities not
	// captured by the standard debt proceeds/repayments tags. These are
	// gated on absence of AssetsCurrent (bank detection). StmtFlow ensures
	// YTD cumulative values are properly de-cumulated to single-quarter amounts.
	{
		FieldName: "_bankFedFundsChange", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		ExcludeIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags:           []string{"IncreaseDecreaseInFederalFundsPurchasedAndSecuritiesSoldUnderAgreementsToRepurchaseNet"},
	},
	{
		FieldName: "_bankLTDebtProceeds", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		ExcludeIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags:           []string{"ProceedsFromIssuanceOfLongTermDebtAndCapitalSecuritiesNet", "ProceedsFromIssuanceOfLongTermDebt"},
	},
	{
		FieldName: "_bankLTDebtRepayments", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		ExcludeIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags:           []string{"RepaymentsOfLongTermDebtAndCapitalSecurities", "RepaymentsOfLongTermDebt"},
	},
	{
		FieldName: "_bankSTDebtProceeds", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		ExcludeIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags:           []string{"ProceedsFromShortTermDebt", "ProceedsFromRepaymentsOfShortTermDebt"},
	},
	{
		FieldName: "_bankOtherInvesting", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		ExcludeIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags:           []string{"PaymentsForProceedsFromOtherInvestingActivities"},
	},
	{
		FieldName: "_bankOtherNoninterestExpense", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		ExcludeIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags:           []string{"OtherNoninterestExpense"},
	},
	// Provision for credit losses as a separate line item, captured ONLY for
	// GS-style investment banks (identified by the CustomerAndOtherPayables
	// extension) where Sharadar folds it into "Total operating expenses".
	// Commercial banks (JPM, BAC) keep provision as a separate line that
	// Sharadar does NOT bundle into OperatingExpenses. StmtFlow so YTD values
	// are properly de-cumulated per quarter before Q4 synthesis.
	{
		FieldName: "_bdBankProvision", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		RequireIfQuarterly: []string{"CustomerAndOtherPayables"},
		XBRLTags:           []string{"ProvisionForLoanLeaseAndOtherLosses"},
	},
	// GS-style financing cash flow components. The generic _bankLTDebtProceeds
	// /Repayments use ProceedsFromIssuanceOfLongTermDebt (JPM) but GS files
	// both secured and unsecured tranches as separate tags. GS also reports
	// short-term debt changes as net-proceeds extension concepts rather than
	// the standard ProceedsFromShortTermDebt. Each sub-field is gated by the
	// CustomerAndOtherPayables marker; StmtFlow ensures quarterly de-cumulation.
	{
		FieldName: "_bdUnsecuredDebtProceeds", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		RequireIfQuarterly: []string{"CustomerAndOtherPayables"},
		XBRLTags:           []string{"ProceedsFromIssuanceOfUnsecuredDebt"},
	},
	{
		FieldName: "_bdUnsecuredDebtRepayments", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		RequireIfQuarterly: []string{"CustomerAndOtherPayables"},
		XBRLTags:           []string{"RepaymentsOfUnsecuredDebt"},
	},
	{
		FieldName: "_bdSecuredDebtProceeds", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		RequireIfQuarterly: []string{"CustomerAndOtherPayables"},
		XBRLTags:           []string{"ProceedsFromIssuanceOfSecuredDebt"},
	},
	{
		FieldName: "_bdSecuredDebtRepayments", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		RequireIfQuarterly: []string{"CustomerAndOtherPayables"},
		XBRLTags:           []string{"RepaymentsOfSecuredDebt"},
	},
	// GS extension concepts for short-term borrowings changes (net). Captured
	// by parseXBRLInstanceExtensions from the inline XBRL filings. Negate
	// because these concepts are defined with a debit balance orientation
	// (repayments-positive) in GS's taxonomy, so the stored value is opposite
	// the cash-flow direction: GS files val=3,243 sign="-" for a net borrowing
	// inflow of +3,243.
	{
		FieldName: "_bdUnsecuredSTDebtNet", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		RequireIfQuarterly: []string{"CustomerAndOtherPayables"},
		Negate:             true,
		XBRLTags:           []string{"ProceedsFromRepaymentsOfUnsecuredShortTermDebt"},
	},
	{
		FieldName: "_bdOtherSecuredSTDebtNet", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		RequireIfQuarterly: []string{"CustomerAndOtherPayables"},
		Negate:             true,
		XBRLTags:           []string{"ProceedsFromRepaymentsOfOtherSecuredShortTermDebt"},
	},
	// Derivative contracts with a financing element (net). GS classifies these
	// within financing cash flow alongside debt issuances/repayments.
	{
		FieldName: "_bdDerivativeFinancing", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		RequireIfQuarterly: []string{"CustomerAndOtherPayables"},
		XBRLTags:           []string{"PaymentsForProceedsFromDerivativeInstrumentFinancingActivities"},
	},
	// Professional fees. Sharadar's SGA for GS-style filers is
	// NoninterestExpense - D&A - ProfessionalFees (rather than the
	// NIE - OtherNoninterestExpense used for commercial banks).
	{
		FieldName: "_bdProfessionalFees", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		RequireIfQuarterly: []string{"CustomerAndOtherPayables"},
		XBRLTags:           []string{"ProfessionalFees"},
	},
	{
		FieldName: "NetCashFlowDividend", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		Negate: true,
		XBRLTags: []string{
			"PaymentsOfDividendsCommonStock",
			"PaymentsOfDividends",
		},
	},
	// --- Internal sub-fields for NetCashFlowInvest derivation ---
	{
		FieldName: "_paymentsInvest", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"PaymentsToAcquireInvestments",
			// BRK/B extension: bundles T-bill + AFS debt purchases. Must come
			// before the standard AFS-only tag so the combined value is used.
			"PaymentsToAcquireUSTreasuryBillsAndAvailableForSaleSecuritiesDebt",
			// AMZN files both MarketableSecurities (comprehensive) and the
			// AFS-debt-only tag; the comprehensive MarketableSecurities value
			// must win so AMZN's full purchases flow into NCFINV. AAPL files
			// only the AFS-debt tag, which still resolves via the next entry.
			"PaymentsToAcquireMarketableSecurities",
			"PaymentsToAcquireAvailableForSaleSecuritiesDebt",
			// KO extension: "Purchases of investments" line on the cash flow
			// statement. KO's taxonomy uses this extension rather than a
			// standard us-gaap tag.
			"PurchasesofInvestments",
		},
	},
	// LLY-style filers separate investment purchases by horizon: short-term
	// + other (long-term). Both contribute to NetCashFlowInvest.
	{
		FieldName: "_paymentsInvestShortTerm", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"PaymentsToAcquireShortTermInvestments"},
	},
	{
		FieldName: "_paymentsInvestOther", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"PaymentsToAcquireOtherInvestments"},
	},
	// NVDA reports equity security purchases/sales separately from debt
	// securities. Sharadar includes both in NetCashFlowInvest.
	{
		FieldName: "_paymentsInvestEquity", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"PaymentsToAcquireEquitySecuritiesFvNi"},
	},
	{
		FieldName: "_proceedsInvestEquity", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"ProceedsFromSaleOfEquitySecuritiesFvNi"},
	},
	// Some companies report a combined maturities+sales tag; others report them
	// separately. Split into two sub-fields and sum, with the combined tag as a
	// fallback on _proceedsInvest so both cases are handled.
	{
		FieldName: "_proceedsInvestMaturities", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			// BRK/B extension: combined T-bill + AFS debt maturities/redemptions
			"ProceedsFromRedemptionsAndMaturitiesOfUSTreasuryBillsAndAvailableForSaleSecuritiesDebt",
			"ProceedsFromMaturitiesPrepaymentsAndCallsOfAvailableForSaleSecurities",
		},
	},
	{
		FieldName: "_proceedsInvestSales", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			// KO extension: "Proceeds from disposals of investments" covering
			// both sales and maturities of all marketable securities. KO also
			// files the us-gaap AFS-debt-only tag below as a partial sub-measure
			// (e.g. FY2024: us-gaap=709M vs combined=6.59B), so the KO extension
			// must win by list order.
			"ProceedsFromDisposalsOfInvestments",
			// BRK/B extension: combined T-bill + AFS debt sales
			"ProceedsFromSaleOfUSTreasuryBillsAndAvailableForSaleSecuritiesDebt",
			"ProceedsFromSaleOfAvailableForSaleSecuritiesDebt",
			// CALM combines sales + maturities of AFS securities into a
			// single tag rather than splitting them.
			"ProceedsFromSaleAndMaturityOfAvailableForSaleSecurities",
		},
	},
	// MSFT extension: additional investment proceeds not captured by
	// standard maturities/sales tags (e.g. other investment dispositions).
	{
		FieldName: "_proceedsInvestOther", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"ProceedsFromInvestments",
		},
	},
	// LLY-style filers report combined sale+maturity per investment horizon.
	{
		FieldName: "_proceedsInvestShortTerm", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"ProceedsFromSaleOfShortTermInvestments"},
	},
	{
		FieldName: "_proceedsInvestSaleAndMaturityOther", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"ProceedsFromSaleAndMaturityOfOtherInvestments"},
	},
	// Equity-method investments: payments to acquire interests accounted for
	// under the equity method live in NCF_invest per Sharadar (e.g. MCD's
	// FY2024 $1.837B equity-method acquisition). Proceeds from selling such
	// interests live in NCF_business — Sharadar treats a divestiture of an
	// equity-method interest like a partial business disposal (e.g. TXRH
	// selling its non-consolidated joint-venture stake).
	{
		FieldName: "_paymentsInvestEquityMethod", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"PaymentsToAcquireEquityMethodInvestments"},
	},
	{
		FieldName: "_proceedsBusinessEquityMethod", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"ProceedsFromSaleOfEquityMethodInvestments"},
	},
	// Lease receivables: restaurant/franchise filers that sub-lease real estate
	// (e.g. TXRH selling subleases) report the proceeds under this tag.
	// Sharadar classifies it under investing cash flow.
	{
		FieldName: "_proceedsInvestLeaseReceivables", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"ProceedsFromSaleOfLeaseReceivables"},
	},
	{
		FieldName: "_proceedsInvest", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		FallbackTags: []string{
			"ProceedsFromSaleAndMaturityOfMarketableSecurities",
		},
		Op:               OpAdd,
		Operands:         []string{"_proceedsInvestMaturities", "_proceedsInvestSales", "_proceedsInvestOther"},
		OptionalOperands: true,
	},
	// NetCashFlowInvest = -payments + proceeds (Sharadar NCFINV:
	// "net cash inflow (outflow) associated with acquisition & disposal of investments")
	{
		FieldName: "NetCashFlowInvest", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op: OpLinearCombination,
		Operands: []string{
			"_paymentsInvest", "_proceedsInvest",
			"_paymentsInvestEquity", "_proceedsInvestEquity",
			"_paymentsInvestShortTerm", "_proceedsInvestShortTerm",
			"_paymentsInvestOther", "_proceedsInvestSaleAndMaturityOther",
			"_paymentsInvestEquityMethod",
			"_proceedsInvestLeaseReceivables",
		},
		Coefficients:     []float64{-1, 1, -1, 1, -1, 1, -1, 1, -1, 1},
		OptionalOperands: true,
	},
	{
		FieldName: "NetCashFlowFx", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		// MSFT uses the longer IncludingDisposalGroupAndDiscontinuedOperations variant.
		XBRLTags: []string{
			"EffectOfExchangeRateOnCashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents",
			"EffectOfExchangeRateOnCashCashEquivalentsRestrictedCashAndRestrictedCashEquivalentsIncludingDisposalGroupAndDiscontinuedOperations",
			"EffectOfExchangeRateOnCashAndCashEquivalents",
		},
	},
	{
		FieldName: "NetCashFlow", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalentsPeriodIncreaseDecreaseIncludingExchangeRateEffect",
			// Excluding-FX variant used by domestic-only filers (e.g. CALM)
			// that never report an exchange-rate effect. When both exist, the
			// Including variant above wins by list order, so FX-exposed filers
			// are unaffected.
			"CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalentsPeriodIncreaseDecreaseExcludingExchangeRateEffect",
			"CashAndCashEquivalentsPeriodIncreaseDecrease",
			"CashPeriodIncreaseDecrease",
		},
	},

	// ==================== DERIVED FIELDS ====================
	// These must come AFTER their dependencies in the list.

	// EBIT = NetIncome + IncomeTaxExpense + InterestExpense
	// InterestExpense is optional because some companies stop reporting it
	// (e.g. Apple dropped InterestExpense after FY2023). When absent, EBIT = EBT.
	//
	// Note: the previous FallbackTag (IncomeLossFromContinuingOperationsBeforeIncomeTaxesExtraordinaryItemsNoncontrollingInterest)
	// was EBT (pre-tax income), not EBIT. Using it as a fallback caused EBIT
	// to exclude InterestExpense even when it was available.
	{
		FieldName: "EBIT", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op:               OpAdd,
		Operands:         []string{"NetIncome", "IncomeTaxExpense", "InterestExpense"},
		OptionalOperands: true,
	},
	// EBITDA = EBIT + DepreciationAmortizationAndAccretion
	{
		FieldName: "EBITDA", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op:       OpAdd,
		Operands: []string{"EBIT", "DepreciationAmortizationAndAccretion"},
	},
	// EBT = NetIncome + IncomeTaxExpense. No FallbackTags: the direct XBRL tags
	// (IncomeLossFromContinuingOperationsBeforeIncomeTaxes...) report consolidated
	// pre-tax income including NCI. The formula uses the already-corrected
	// NetIncome (parent-only for companies with NCI like JPM).
	{
		FieldName: "EBT", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op:       OpAdd,
		Operands: []string{"NetIncome", "IncomeTaxExpense"},
	},
	// FreeCashFlow = NetCashFlowFromOperations + CapitalExpenditure
	// (CapitalExpenditure is already negative after Negate, so addition is correct).
	// OptionalOperands: banks (JPM) do not report PaymentsToAcquirePropertyPlantAndEquipment
	// so CapitalExpenditure is absent; FCF = NCFOps when capex is missing.
	{
		FieldName: "FreeCashFlow", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op:               OpAdd,
		Operands:         []string{"NetCashFlowFromOperations", "CapitalExpenditure"},
		OptionalOperands: true,
	},
	// WorkingCapital = CurrentAssets - CurrentLiabilities
	{
		FieldName: "WorkingCapital", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		Op:       OpSubtract,
		Operands: []string{"CurrentAssets", "CurrentLiabilities"},
	},
	// TangibleAssetValue = TotalAssets - Intangibles
	// OptionalOperands: companies like Apple have no intangible assets and
	// do not report any Intangibles XBRL tags; treat missing Intangibles as 0.
	{
		FieldName: "TangibleAssetValue", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		Op:               OpLinearCombination,
		Operands:         []string{"TotalAssets", "Intangibles"},
		Coefficients:     []float64{1, -1},
		OptionalOperands: true,
	},
	// InvestedCapital = TotalDebt + TotalAssets - Intangibles - CashAndEquivalents - CurrentLiabilities
	// OptionalOperands: some companies (e.g. Apple) have no intangible assets
	// and do not report the Intangibles XBRL tag; treat missing components as 0.
	{
		FieldName: "InvestedCapital", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		Op:               OpLinearCombination,
		Operands:         []string{"TotalDebt", "TotalAssets", "Intangibles", "CashAndEquivalents", "CurrentLiabilities"},
		Coefficients:     []float64{1, 1, -1, -1, -1},
		OptionalOperands: true,
	},

	// ==================== RATIO METRICS (derived) ====================

	// GrossMargin = GrossProfit / Revenues
	{
		FieldName: "GrossMargin", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:          OpDivide,
		Operands:    []string{"GrossProfit", "Revenues"},
		RoundDigits: 3,
	},
	// ProfitMargin = NetIncomeCommonStock / Revenues
	{
		FieldName: "ProfitMargin", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:          OpDivide,
		Operands:    []string{"NetIncomeCommonStock", "Revenues"},
		RoundDigits: 3,
	},
	// EBITDAMargin = EBITDA / Revenues
	{
		FieldName: "EBITDAMargin", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:          OpDivide,
		Operands:    []string{"EBITDA", "Revenues"},
		RoundDigits: 3,
	},
	// CurrentRatio = CurrentAssets / CurrentLiabilities
	{
		FieldName: "CurrentRatio", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:          OpDivide,
		Operands:    []string{"CurrentAssets", "CurrentLiabilities"},
		RoundDigits: 3,
	},
	// DebtToEquityRatio = TotalLiabilities / Equity
	{
		FieldName: "DebtToEquityRatio", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:          OpDivide,
		Operands:    []string{"TotalLiabilities", "Equity"},
		RoundDigits: 3,
	},
	// AssetTurnover = Revenues / TotalAssets
	{
		FieldName: "AssetTurnover", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:          OpDivide,
		Operands:    []string{"Revenues", "TotalAssets"},
		RoundDigits: 3,
	},
	// ReturnOnSales = EBIT / Revenues
	{
		FieldName: "ReturnOnSales", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:          OpDivide,
		Operands:    []string{"EBIT", "Revenues"},
		RoundDigits: 3,
	},

	// ==================== PER-SHARE METRICS (derived) ====================

	// FreeCashFlowPerShare = FreeCashFlow / WeightedAverageShares
	{
		FieldName: "FreeCashFlowPerShare", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:          OpDivide,
		Operands:    []string{"FreeCashFlow", "WeightedAverageShares"},
		RoundDigits: 3,
	},
	// BookValuePerShare = Equity / WeightedAverageShares
	{
		FieldName: "BookValuePerShare", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:          OpDivide,
		Operands:    []string{"Equity", "WeightedAverageShares"},
		RoundDigits: 3,
	},
	// SalesPerShare = Revenues / WeightedAverageShares
	{
		FieldName: "SalesPerShare", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:          OpDivide,
		Operands:    []string{"Revenues", "WeightedAverageShares"},
		RoundDigits: 3,
	},
	// TangibleAssetsBookValuePerShare = TangibleAssetValue / WeightedAverageShares
	{
		FieldName: "TangibleAssetsBookValuePerShare", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:          OpDivide,
		Operands:    []string{"TangibleAssetValue", "WeightedAverageShares"},
		RoundDigits: 3,
	},

	// Note: The following Sharadar fields require market price data and are
	// NOT computable from SEC filings alone. They are intentionally omitted:
	// - MarketCapitalization, EnterpriseValue, PE, PB, PS, PE1, PS1
	// - EVtoEBIT, EVtoEBITDA, DividendYield, Price
	// - ShareFactor, FxUSD
	// PayoutRatio is also omitted here because it is a derived ratio
	// (DPS / EPSDiluted) computed in EnrichMarketData for all dimensions.
}
