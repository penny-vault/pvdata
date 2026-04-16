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

	// RequireIfQuarterly gates resolution on a sentinel concept: the
	// field is only resolved when ANY of the listed concepts appear on
	// a recent 10-Q filing. This is used for cash flow items that some
	// companies bundle into a parent line (and thus should be added to
	// a derived formula) while others present as a standalone line (and
	// should not be added to avoid double-counting).
	RequireIfQuarterly []string
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
	{
		FieldName: "CashAndEquivalents", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"CashAndCashEquivalentsAtCarryingValue",
			"CashCashEquivalentsAndShortTermInvestments",
			"CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents", // Banks (JPM) use this
			"Cash",
			"CashEquivalentsAtCarryingValue",
		},
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
		},
	},
	{
		FieldName: "InvestmentsNonCurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"LongTermInvestments",
			"MarketableSecuritiesNoncurrent",
			"AvailableForSaleSecuritiesDebtSecuritiesNoncurrent",
		},
	},
	// Insurance/conglomerate investment sub-fields. These companies (BRK/B)
	// hold equity securities, debt securities, and equity method investments
	// that aren't captured by the standard Current/NonCurrent breakdown.
	// ExcludeIfQuarterly gates these off when the company files standard
	// investment tags (ShortTermInvestments, MarketableSecuritiesCurrent)
	// to avoid double-counting with InvestmentsCurrent/InvestmentsNonCurrent.
	{
		FieldName: "_equitySecuritiesFvNi", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"ShortTermInvestments", "MarketableSecuritiesCurrent"},
		XBRLTags:           []string{"EquitySecuritiesFvNi"},
	},
	{
		FieldName: "_equityMethodInvestments", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"ShortTermInvestments", "MarketableSecuritiesCurrent"},
		XBRLTags:           []string{"EquityMethodInvestments"},
	},
	{
		FieldName: "_debtSecurities", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"ShortTermInvestments", "MarketableSecuritiesCurrent"},
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
		},
	},
	{
		FieldName: "NonTradeReceivables", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"NontradeReceivablesCurrent",
		},
	},
	// Receivables = TradeReceivables + NonTradeReceivables. Sharadar
	// defines this as "trade and non-trade receivables." Apple tags these
	// separately (AccountsReceivableNetCurrent + NontradeReceivablesCurrent).
	// Banks (JPM) use a combined extension tag for accrued interest + A/R.
	{
		FieldName: "Receivables", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags: []string{
			"ReceivablesNetCurrent",
			"PremiumsAndOtherReceivablesNet",       // Insurance companies (BRK/B) — premiums + trade receivables
			"AccruedInterestAndAccountsReceivable", // JPM extension tag
			"NotesReceivableNet",                   // Insurance/conglomerates with loan portfolios
		},
		Op:               OpAdd,
		Operands:         []string{"TradeReceivables", "NonTradeReceivables"},
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
		XBRLTags: []string{"Deposits", "DepositsDomestic", "DepositsTotal"},
	},
	{
		FieldName: "_ppneRaw", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"PropertyPlantAndEquipmentNet"},
	},
	// Operating lease ROU assets are included in PP&E when the company
	// reports them as a separate balance sheet line (filed on 10-Q).
	// Exclude banks: Sharadar reports bank PP&E as just
	// PropertyPlantAndEquipmentNet without operating lease assets.
	{
		FieldName: "_operatingLeaseROU", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly:   true,
		ExcludeIfQuarterly: []string{"Deposits", "DepositsDomestic", "DepositsTotal"},
		XBRLTags:           []string{"OperatingLeaseRightOfUseAsset"},
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
		Operands:         []string{"_ppneRaw", "_operatingLeaseROU", "_propertyHeldForLease"},
		OptionalOperands: true,
	},
	// --- Internal sub-fields for Intangibles derivation ---
	{
		FieldName: "_goodwill", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"Goodwill"},
	},
	{
		FieldName: "_intangiblesExGoodwill", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"IntangibleAssetsNetExcludingGoodwill",
			"FiniteLivedIntangibleAssetsNet",
			"IndefiniteLivedIntangibleAssetsExcludingGoodwill", // BRK/B and other conglomerates with brand/franchise intangibles
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
	// TaxAssets: try prepaid/receivable taxes first (FallbackTags on the
	// derived wrapper). If none resolve, fall through to _deferredTaxAssets
	// which is gated by RequireQuarterly — only companies that present
	// deferred tax assets on 10-Q (NVDA) get the value; companies that
	// only disclose in 10-K notes (AAPL) keep TaxAssets=0.
	{
		FieldName: "_deferredTaxAssets", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly: true,
		XBRLTags:         []string{"DeferredIncomeTaxAssetsNet"},
	},
	{
		FieldName: "TaxAssets", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags: []string{
			"IncomeTaxesReceivable",
			"IncomeTaxReceivable",
			"PrepaidTaxes",
		},
		Op:               OpAdd,
		Operands:         []string{"_deferredTaxAssets"},
		OptionalOperands: true,
	},
	// --- Internal sub-fields for TaxLiabilities derivation ---
	{
		FieldName: "_deferredTaxLiabilities", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"DeferredTaxLiabilities",
			"DeferredIncomeTaxLiabilitiesNet",
			"DeferredTaxLiabilitiesNoncurrent",
		},
	},
	// Accrued income taxes are included when filed as separate quarterly
	// balance sheet line items (RequireQuarterly). MSFT breaks these out;
	// AAPL only reports them in 10-K notes.
	{
		FieldName: "_accruedIncomeTaxesCurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly: true,
		XBRLTags:         []string{"AccruedIncomeTaxesCurrent"},
	},
	{
		FieldName: "_accruedIncomeTaxesNoncurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly: true,
		XBRLTags:         []string{"AccruedIncomeTaxesNoncurrent"},
	},
	{
		FieldName: "TaxLiabilities", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		// When AccruedLiabilitiesCurrent is filed on 10-Q, the company bundles
		// tax liabilities into broader accrued/other line items (NVDA). Only
		// resolve when tax items are separate balance sheet lines (MSFT).
		// IncomeTaxesPrincipallyDeferred is a BRK/B extension tag for the
		// consolidated deferred tax liability (filed quarterly, unlike the
		// standard DeferredIncomeTaxLiabilitiesNet which BRK only files on 10-K).
		ExcludeIfQuarterly: []string{"AccruedLiabilitiesCurrent"},
		FallbackTags:       []string{"IncomeTaxesPrincipallyDeferred"},
		Op:                 OpAdd,
		Operands:           []string{"_deferredTaxLiabilities", "_accruedIncomeTaxesCurrent", "_accruedIncomeTaxesNoncurrent"},
		OptionalOperands:   true,
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
		XBRLTags:           []string{"LongTermDebtCurrent"},
	},
	{
		FieldName: "CommercialPaperDebt", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags:           []string{"CommercialPaper"},
	},
	// DebtCurrent = ShortTermDebt + LongTermDebtCurrentMaturities +
	// CommercialPaperDebt. Sharadar includes all forms of current debt
	// (bonds, commercial paper, notes payable, credit facilities). Apple
	// tags LongTermDebtCurrent and CommercialPaper separately; the sum
	// captures both. For banks (no AssetsCurrent), all components are
	// gated off so DebtCurrent = 0.
	{
		FieldName: "DebtCurrent", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags:     []string{"DebtCurrent"},
		Op:               OpAdd,
		Operands:         []string{"ShortTermDebt", "LongTermDebtCurrentMaturities", "CommercialPaperDebt"},
		OptionalOperands: true,
	},
	{
		FieldName: "_longTermDebtNoncurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags: []string{
			"LongTermDebtNoncurrent",
			"LongTermDebt",
			"LongTermDebtAndCapitalLeaseObligationsIncludingCurrentMaturities",
		},
	},
	// Operating lease liabilities are included in debt when the company
	// reports them as a separate balance sheet line item (filed on 10-Q).
	// RequireQuarterly ensures we only add them for companies like MSFT
	// (separate line) and not AAPL (10-K note disclosure only).
	{
		FieldName: "_operatingLeaseLiabilityNoncurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		RequireQuarterly:   true,
		RequireIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags:           []string{"OperatingLeaseLiabilityNoncurrent"},
	},
	{
		FieldName: "DebtNonCurrent", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		Op:               OpAdd,
		Operands:         []string{"_longTermDebtNoncurrent", "_operatingLeaseLiabilityNoncurrent"},
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
	{
		FieldName: "_unearnedPremiums", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"AccruedLiabilitiesCurrent"},
		XBRLTags:           []string{"UnearnedPremiums"},
	},
	{
		FieldName: "_policyholderFunds", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"AccruedLiabilitiesCurrent"},
		XBRLTags:           []string{"PolicyholderFunds"},
	},
	{
		FieldName: "DeferredRevenue", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		ExcludeIfQuarterly: []string{"AccruedLiabilitiesCurrent"},
		FallbackTags:       []string{"DeferredRevenue"},
		Op:                 OpAdd,
		Operands: []string{
			"_deferredRevenueCurrent", "_deferredRevenueNoncurrent",
			"_unearnedPremiums", "_policyholderFunds",
		},
		OptionalOperands: true,
	},
	{
		FieldName: "TotalLiabilities", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"Liabilities"},
	},
	{
		FieldName: "CurrentLiabilities", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"LiabilitiesCurrent"},
	},
	{
		FieldName: "LiabilitiesNonCurrent", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags: []string{"LiabilitiesNoncurrent"},
		Op:           OpSubtract,
		Operands:     []string{"TotalLiabilities", "CurrentLiabilities"},
	},
	{
		FieldName: "Equity", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"StockholdersEquity",
			"StockholdersEquityIncludingPortionAttributableToNoncontrollingInterest",
		},
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
	{
		FieldName: "CostOfRevenue", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"CostOfGoodsAndServicesSold",
			"CostOfGoodsSold",
			"CostOfRevenue",
			"CostOfGoodsAndServiceExcludingDepreciationDepletionAndAmortization",
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
		XBRLTags: []string{"GeneralAndAdministrativeExpense"},
	},
	{
		FieldName: "_sellingAndMarketingExpense", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"SellingAndMarketingExpense",
			"SellingExpense",
		},
	},
	// SGA = GeneralAndAdministrativeExpense + SellingAndMarketingExpense.
	// Companies like Apple report a combined SellingGeneralAndAdministrativeExpense;
	// companies like Microsoft report the two components separately. Sharadar
	// always reports the combined total.
	{
		FieldName: "SellingGeneralAndAdministrativeExpense", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		FallbackTags:     []string{"SellingGeneralAndAdministrativeExpense"},
		Op:               OpAdd,
		Operands:         []string{"_generalAndAdministrativeExpense", "_sellingAndMarketingExpense"},
		OptionalOperands: true,
	},
	{
		FieldName: "RandDExpenses", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"ResearchAndDevelopmentExpense"},
	},
	// CostsAndExpenses is total income statement costs. Used to derive
	// OperatingIncome for companies without an explicit OperatingIncomeLoss
	// tag (insurance/conglomerates like BRK/B).
	{
		FieldName: "_costsAndExpenses", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"CostsAndExpenses"},
	},
	// InterestExpense: must come before OperatingIncome since the derived
	// OperatingIncome formula uses it as an operand.
	{
		FieldName: "InterestExpense", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		// Do NOT add InterestIncomeExpenseNet here: it is a signed net value
		// (positive when net expense, negative when net income) and feeds
		// directly into EBIT = NetIncome + IncomeTaxExpense + InterestExpense.
		// For cash-rich companies with interest income > expense, using the
		// net value would incorrectly subtract from EBIT.
		// InterestExpenseNonoperating is used by MSFT on 10-Q filings where
		// the generic InterestExpense tag is only filed on 10-K.
		//
		// ExcludeIfQuarterly on Deposits: banks (JPM) file the generic
		// InterestExpense tag for some quarters but not others, and for banks
		// this is operational interest (cost of deposits) — NOT financing
		// expense. Including it in EBIT breaks Q4 synthesis (annual has 0
		// but one quarter has 24B). Gating on Deposits (a bank-specific
		// liability) excludes real banks while allowing insurance
		// conglomerates like BRK/B that also lack AssetsCurrent.
		ExcludeIfQuarterly: []string{"Deposits"},
		XBRLTags: []string{
			"InterestExpense",
			"InterestExpenseDebt",
			"InterestExpenseNonoperating",
		},
	},
	// OperatingIncome: use the direct OperatingIncomeLoss tag when available.
	// For companies without it (non-standard income statements), derive as
	// Revenues - CostsAndExpenses + InterestExpense. This gives income from
	// all operations (including investment gains for insurance companies)
	// before interest and taxes — matching Sharadar's operating income definition.
	{
		FieldName: "OperatingIncome", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		FallbackTags: []string{"OperatingIncomeLoss"},
		Op:           OpLinearCombination,
		Operands:     []string{"Revenues", "_costsAndExpenses", "InterestExpense"},
		Coefficients: []float64{1, -1, 1},
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
		FieldName: "NetIncome", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		// Prefer NetIncomeLossAvailableToCommonStockholdersBasic: Sharadar defines
		// netinc as "net income loss to common shareholders" which deducts NCI and
		// preferred dividends. Banks (JPM) file NetIncomeLoss as the consolidated
		// figure (including NCI); the Available-to-Common variant gives the correct
		// parent-only value. For companies without NCI (AAPL, MSFT), both are equal.
		XBRLTags: []string{
			"NetIncomeLossAvailableToCommonStockholdersBasic",
			"NetIncomeLoss",
			"ProfitLoss",
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
	// NetIncome - PreferredDividends. ConsolidatedIncome uses NetIncomeLoss
	// (consolidated), while NetIncome uses the common-stockholder variant.
	{
		FieldName: "NetIncomeToNonControllingInterests", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		FallbackTags: []string{
			"NetIncomeLossAttributableToNoncontrollingInterest",
		},
		Op:               OpLinearCombination,
		Operands:         []string{"ConsolidatedIncome", "NetIncome", "PreferredDividendsIncomeStatementImpact"},
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
		// filers like BRK/B don't report EarningsPerShareDiluted.
		FieldName: "EPSDiluted", Type: MappingDerived, StatementType: StmtFlow, ValueType: "float64",
		FallbackTags: []string{"EarningsPerShareDiluted"},
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
	// de-cumulation, overriding the declared per-share tag. Only the specific
	// common-stock tag is used; AAPL uses the broader PaymentsOfDividends which
	// gives a different per-share figure, so it falls back to the declared tag.
	{
		FieldName: "_absDividendsPaid", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"PaymentsOfDividendsCommonStock"},
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
	{
		FieldName: "DepreciationAmortizationAndAccretion", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		FallbackTags: []string{
			"DepreciationDepletionAndAmortization",
			"DepreciationAmortizationAndAccretionNet",
			"DepreciationAndAmortization",
			"DepreciationAmortizationAndOther", // MSFT extension tag for cash flow D&A line
		},
		Op:               OpAdd,
		Operands:         []string{"_depreciation", "_amortizationOfIntangibles", "_financeLeaseAmortization"},
		OptionalOperands: true,
	},
	{
		FieldName: "CapitalExpenditure", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		Negate: true,
		XBRLTags: []string{
			"PaymentsToAcquirePropertyPlantAndEquipment",
			"PaymentsToAcquireProductiveAssets",
			"CapitalExpendituresIncurredButNotYetPaid",
		},
	},
	// Sharadar reports 0 for share-based compensation for banks. Banks
	// include SBC in their compensation expense (LaborAndRelatedExpense)
	// and Sharadar does not separate it. Gate on AssetsCurrent to exclude
	// banks (which don't classify assets as current/non-current).
	{
		FieldName: "ShareBasedCompensation", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		RequireIfQuarterly: []string{"AssetsCurrent"},
		XBRLTags: []string{
			"ShareBasedCompensation",
			"AllocatedShareBasedCompensationExpense",
		},
	},
	{
		FieldName: "NetCashFlowBusiness", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		Negate: true,
		XBRLTags: []string{
			"PaymentsToAcquireBusinessesNetOfCashAcquired",
			"PaymentsToAcquireBusinessesGross",
			"AcquisitionsNetOfCashAcquiredAndPurchasesOfIntangibleAndOtherAssets", // MSFT extension
		},
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
	// excludes those companies.
	{
		FieldName: "_taxWithholdingShareComp", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		RequireQuarterly:   true,
		RequireIfQuarterly: []string{"AccruedLiabilitiesCurrent"},
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
	{
		FieldName: "_netShortTermDebt", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"ProceedsFromRepaymentsOfCommercialPaper",
			"ProceedsFromRepaymentsOfShortTermDebt",
			"ProceedsFromRepaymentsOfShortTermDebtMaturingInThreeMonthsOrLess",
		},
	},
	// NetCashFlowDebt = proceeds - repayments - financedAssets + netShortTermDebt
	// (Sharadar NCFDEBT: "net cash inflow (outflow) from issuance (repayment) of
	// debt securities"). The financedAssets component captures extension-tag
	// payments on financed PP&E (e.g. NVDA).
	{
		FieldName: "NetCashFlowDebt", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op:               OpLinearCombination,
		Operands:         []string{"_proceedsDebt", "_repaymentsDebt", "_repaymentsFinancedAssets", "_netShortTermDebt"},
		Coefficients:     []float64{1, -1, -1, 1},
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
			"PaymentsToAcquireAvailableForSaleSecuritiesDebt",
		},
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
			// BRK/B extension: combined T-bill + AFS debt sales
			"ProceedsFromSaleOfUSTreasuryBillsAndAvailableForSaleSecuritiesDebt",
			"ProceedsFromSaleOfAvailableForSaleSecuritiesDebt",
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
		Op:               OpLinearCombination,
		Operands:         []string{"_paymentsInvest", "_proceedsInvest", "_paymentsInvestEquity", "_proceedsInvestEquity"},
		Coefficients:     []float64{-1, 1, -1, 1},
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
