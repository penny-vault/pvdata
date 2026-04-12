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
	StmtFlow        StatementType = "flow"          // Sum 4 quarters for TTM
	StmtPointInTime StatementType = "point_in_time" // Use latest quarter for TTM
	StmtMetric      StatementType = "metric"        // Recomputed from other fields, not summed
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

	// OptionalOperands makes OpAdd treat missing operands as 0 and resolve
	// when at least one operand is present, instead of requiring all.
	OptionalOperands bool

	// RoundDigits rounds OpDivide results to this many decimal places.
	// Zero (the default) means no rounding.
	RoundDigits int
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
		FieldName: "AssetsNonCurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"AssetsNoncurrent"},
		// Fallback: Assets - AssetsCurrent (computed in derived pass if missing)
	},
	{
		FieldName: "CashAndEquivalents", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"CashAndCashEquivalentsAtCarryingValue",
			"CashCashEquivalentsAndShortTermInvestments",
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
	// Investments = InvestmentsCurrent + InvestmentsNonCurrent. Sharadar
	// defines this as "total amount of marketable and non-marketable
	// securities, loans receivable and other invested assets." Companies
	// like Apple tag current/noncurrent separately rather than a single
	// "Investments" concept; the sum captures both.
	{
		FieldName: "Investments", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags:     []string{"Investments"},
		Op:               OpAdd,
		Operands:         []string{"InvestmentsCurrent", "InvestmentsNonCurrent"},
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
	{
		FieldName: "Receivables", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags:     []string{"ReceivablesNetCurrent"},
		Op:               OpAdd,
		Operands:         []string{"TradeReceivables", "NonTradeReceivables"},
		OptionalOperands: true,
	},
	{
		FieldName: "Payables", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"AccountsPayableCurrent",
			"AccountsPayableAndAccruedLiabilitiesCurrent",
		},
	},
	{
		FieldName: "Deposits", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"Deposits", "DepositsDomestic", "DepositsTotal"},
	},
	{
		FieldName: "PropertyPlantAndEquipmentNet", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"PropertyPlantAndEquipmentNet"},
	},
	{
		FieldName: "Intangibles", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"IntangibleAssetsNetIncludingGoodwill",
			"IntangibleAssetsNetExcludingGoodwill",
			"Goodwill",
		},
	},
	{
		FieldName: "TaxAssets", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"IncomeTaxesReceivable",
			"IncomeTaxReceivable",
			"PrepaidTaxes",
		},
	},
	{
		FieldName: "TaxLiabilities", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"DeferredTaxLiabilities",
			"DeferredIncomeTaxLiabilitiesNet",
			"DeferredTaxLiabilitiesNoncurrent",
		},
	},
	{
		FieldName: "ShortTermDebt", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"ShortTermBorrowings"},
	},
	{
		FieldName: "LongTermDebtCurrentMaturities", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"LongTermDebtCurrent"},
	},
	{
		FieldName: "CommercialPaperDebt", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"CommercialPaper"},
	},
	// DebtCurrent = ShortTermDebt + LongTermDebtCurrentMaturities +
	// CommercialPaperDebt. Sharadar includes all forms of current debt
	// (bonds, commercial paper, notes payable, credit facilities). Apple
	// tags LongTermDebtCurrent and CommercialPaper separately; the sum
	// captures both.
	{
		FieldName: "DebtCurrent", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		FallbackTags:     []string{"DebtCurrent"},
		Op:               OpAdd,
		Operands:         []string{"ShortTermDebt", "LongTermDebtCurrentMaturities", "CommercialPaperDebt"},
		OptionalOperands: true,
	},
	{
		FieldName: "DebtNonCurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"LongTermDebtNoncurrent",
			"LongTermDebt",
			"LongTermDebtAndCapitalLeaseObligationsIncludingCurrentMaturities",
		},
	},
	{
		FieldName: "TotalDebt", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		Op:       OpAdd,
		Operands: []string{"DebtCurrent", "DebtNonCurrent"},
	},
	{
		FieldName: "DeferredRevenue", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		// Current-specific tags first to match Sharadar, which reports
		// the current portion. Apple tags ContractWithCustomerLiabilityCurrent
		// (8.0B) vs the total ContractWithCustomerLiability (12.6B).
		XBRLTags: []string{
			"DeferredRevenueCurrent",
			"ContractWithCustomerLiabilityCurrent",
			"DeferredRevenue",
			"ContractWithCustomerLiability",
		},
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
		FieldName: "LiabilitiesNonCurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"LiabilitiesNoncurrent"},
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

	{
		FieldName: "Revenues", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"Revenues",
			"RevenueFromContractWithCustomerExcludingAssessedTax",
			"RevenueFromContractWithCustomerIncludingAssessedTax",
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
	{
		FieldName: "CostOfRevenue", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"CostOfGoodsAndServicesSold",
			"CostOfGoodsSold",
			"CostOfRevenue",
			"CostOfGoodsAndServiceExcludingDepreciationDepletionAndAmortization",
		},
	},
	{
		FieldName: "GrossProfit", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"GrossProfit"},
		// Fallback: Revenues - CostOfRevenue
	},
	{
		FieldName: "OperatingExpenses", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"OperatingExpenses",
			"CostsAndExpenses",
		},
	},
	{
		FieldName: "SellingGeneralAndAdministrativeExpense", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"SellingGeneralAndAdministrativeExpense",
			"GeneralAndAdministrativeExpense",
		},
	},
	{
		FieldName: "RandDExpenses", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"ResearchAndDevelopmentExpense"},
	},
	{
		FieldName: "OperatingIncome", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"OperatingIncomeLoss",
		},
	},
	{
		FieldName: "InterestExpense", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		// Do NOT add InterestIncomeExpenseNet here: it is a signed net value
		// (positive when net expense, negative when net income) and feeds
		// directly into EBIT = NetIncome + IncomeTaxExpense + InterestExpense.
		// For cash-rich companies with interest income > expense, using the
		// net value would incorrectly subtract from EBIT.
		XBRLTags: []string{
			"InterestExpense",
			"InterestExpenseDebt",
		},
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
		XBRLTags: []string{
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
	{
		FieldName: "NetIncomeToNonControllingInterests", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"NetIncomeLossAttributableToNoncontrollingInterest",
		},
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
		FieldName: "EPSDiluted", Type: MappingDirect, StatementType: StmtFlow, ValueType: "float64",
		XBRLTags: []string{"EarningsPerShareDiluted"},
	},
	{
		FieldName: "DividendsPerBasicCommonShare", Type: MappingDirect, StatementType: StmtFlow, ValueType: "float64",
		XBRLTags: []string{
			"CommonStockDividendsPerShareDeclared",
			"CommonStockDividendsPerShareCashPaid",
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
		FieldName: "WeightedAverageShares", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"WeightedAverageNumberOfShareOutstandingBasicAndDiluted",
			"WeightedAverageNumberOfSharesOutstandingBasic",
		},
	},
	{
		FieldName: "WeightedAverageSharesDiluted", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
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
	{
		FieldName: "DepreciationAmortizationAndAccretion", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"DepreciationDepletionAndAmortization",
			"DepreciationAmortizationAndAccretionNet",
			"DepreciationAndAmortization",
			"Depreciation",
		},
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
	{
		FieldName: "ShareBasedCompensation", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
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
		},
	},
	{
		FieldName: "NetCashFlowCommon", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		Negate: true,
		XBRLTags: []string{
			"PaymentsForRepurchaseOfCommonStock",
		},
	},
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
	{
		FieldName: "NetCashFlowFx", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"EffectOfExchangeRateOnCashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents",
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
	{
		FieldName: "EBIT", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		FallbackTags: []string{
			"IncomeLossFromContinuingOperationsBeforeIncomeTaxesExtraordinaryItemsNoncontrollingInterest",
		},
		Op:       OpAdd,
		Operands: []string{"NetIncome", "IncomeTaxExpense", "InterestExpense"},
	},
	// EBITDA = EBIT + DepreciationAmortizationAndAccretion
	{
		FieldName: "EBITDA", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op:       OpAdd,
		Operands: []string{"EBIT", "DepreciationAmortizationAndAccretion"},
	},
	// EBT = NetIncome + IncomeTaxExpense
	{
		FieldName: "EBT", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		FallbackTags: []string{
			"IncomeLossFromContinuingOperationsBeforeIncomeTaxesExtraordinaryItemsNoncontrollingInterest",
			"IncomeLossFromContinuingOperationsBeforeIncomeTaxesMinorityInterestAndIncomeLossFromEquityMethodInvestments",
		},
		Op:       OpAdd,
		Operands: []string{"NetIncome", "IncomeTaxExpense"},
	},
	// FreeCashFlow = NetCashFlowFromOperations + CapitalExpenditure
	// (CapitalExpenditure is already negative after Negate, so addition is correct)
	{
		FieldName: "FreeCashFlow", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op:       OpAdd,
		Operands: []string{"NetCashFlowFromOperations", "CapitalExpenditure"},
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
	// - EVtoEBIT, EVtoEBITDA, DividendYield, PayoutRatio, Price
	// - ShareFactor, FxUSD
}
