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
	OpAdd      FormulaOp = "add"      // A + B + ...
	OpSubtract FormulaOp = "subtract" // A - B
	OpDivide   FormulaOp = "divide"   // A / B
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
	Op       FormulaOp // Operation to apply
	Operands []string  // Field names to use as operands

	// For derived mappings that also have a direct XBRL fallback
	FallbackTags []string
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
		FieldName: "Investments", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"Investments",
			"ShortTermInvestments",
			"LongTermInvestments",
			"MarketableSecuritiesCurrent",
		},
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
	{
		FieldName: "Receivables", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"AccountsReceivableNetCurrent",
			"AccountsReceivableNet",
			"ReceivablesNetCurrent",
		},
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
			"DeferredTaxAssetsNet",
			"DeferredTaxAssetsNetCurrent",
			"DeferredIncomeTaxAssetsNet",
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
		FieldName: "DebtCurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"ShortTermBorrowings",
			"LongTermDebtCurrent",
			"DebtCurrent",
			"CommercialPaper",
		},
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
		XBRLTags: []string{
			"DeferredRevenue",
			"ContractWithCustomerLiability",
			"DeferredRevenueCurrent",
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
			"CommonStockSharesOutstanding",
			"EntityCommonStockSharesOutstanding",
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
	{
		FieldName: "NetCashFlowDebt", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"ProceedsFromIssuanceOfLongTermDebt",
			"RepaymentsOfLongTermDebt",
		},
	},
	{
		FieldName: "NetCashFlowDividend", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		Negate: true,
		XBRLTags: []string{
			"PaymentsOfDividendsCommonStock",
			"PaymentsOfDividends",
		},
	},
	{
		FieldName: "NetCashFlowInvest", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"PaymentsToAcquireInvestments",
			"PaymentsToAcquireAvailableForSaleSecuritiesDebt",
			"ProceedsFromSaleAndMaturityOfMarketableSecurities",
			"ProceedsFromMaturitiesPrepaymentsAndCallsOfAvailableForSaleSecurities",
			"ProceedsFromSaleOfAvailableForSaleSecuritiesDebt",
		},
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
	{
		FieldName: "TangibleAssetValue", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		Op:       OpSubtract,
		Operands: []string{"TotalAssets", "Intangibles"},
	},
	// InvestedCapital is intentionally omitted: the proper formula is
	//   TotalDebt + TotalAssets - Intangibles - CashAndEquivalents - CurrentLiabilities
	// which requires multi-term subtract support that the derivation engine does
	// not yet have. Add it back in a follow-up once the engine can express that
	// formula correctly; computing only TotalDebt + TotalAssets would be wrong.

	// ==================== RATIO METRICS (derived) ====================

	// GrossMargin = GrossProfit / Revenues
	{
		FieldName: "GrossMargin", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"GrossProfit", "Revenues"},
	},
	// ProfitMargin = NetIncomeCommonStock / Revenues
	{
		FieldName: "ProfitMargin", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"NetIncomeCommonStock", "Revenues"},
	},
	// EBITDAMargin = EBITDA / Revenues
	{
		FieldName: "EBITDAMargin", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"EBITDA", "Revenues"},
	},
	// CurrentRatio = CurrentAssets / CurrentLiabilities
	{
		FieldName: "CurrentRatio", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"CurrentAssets", "CurrentLiabilities"},
	},
	// DebtToEquityRatio = TotalLiabilities / Equity
	{
		FieldName: "DebtToEquityRatio", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"TotalLiabilities", "Equity"},
	},
	// AssetTurnover = Revenues / TotalAssets
	{
		FieldName: "AssetTurnover", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"Revenues", "TotalAssets"},
	},
	// ReturnOnSales = EBIT / Revenues
	{
		FieldName: "ReturnOnSales", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"EBIT", "Revenues"},
	},

	// ==================== PER-SHARE METRICS (derived) ====================

	// FreeCashFlowPerShare = FreeCashFlow / WeightedAverageShares
	{
		FieldName: "FreeCashFlowPerShare", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"FreeCashFlow", "WeightedAverageShares"},
	},
	// BookValuePerShare = Equity / SharesBasic
	{
		FieldName: "BookValuePerShare", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"Equity", "SharesBasic"},
	},
	// SalesPerShare = Revenues / WeightedAverageShares
	{
		FieldName: "SalesPerShare", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"Revenues", "WeightedAverageShares"},
	},
	// TangibleAssetsBookValuePerShare = TangibleAssetValue / SharesBasic
	{
		FieldName: "TangibleAssetsBookValuePerShare", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"TangibleAssetValue", "SharesBasic"},
	},

	// Note: The following Sharadar fields require market price data and are
	// NOT computable from SEC filings alone. They are intentionally omitted:
	// - MarketCapitalization, EnterpriseValue, PE, PB, PS, PE1, PS1
	// - EVtoEBIT, EVtoEBITDA, DividendYield, PayoutRatio, Price
	// - ShareFactor, FxUSD
	// - ROA, ROE, ROIC (these need average values across periods)
	// - AverageAssets, EquityAvg, InvestedCapitalAverage (need prior period)
	//
	// These will be computed in a later pass once we have multi-period data
	// available (see dimensions.go for average computations).
}
