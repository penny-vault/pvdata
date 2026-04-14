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
package data

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

type Fundamental struct {
	// [Entity] The Event Date represents the SEC filing date for AR dimensions
	// (ARQ;ART;ARY); and the [REPORTPERIOD] for MR dimensions (MRQ;MRT;MRY).
	// In addition; this is the observation date used for [Price] based data
	// such as [MarketCap]; [Price] and [PE].
	EventDate time.Time

	// [Entity] The ticker is a unique identifier for a security in the
	// database. Where a company is delisted and the ticker subsequently
	// recycled for use by a different company; we utilise that ticker for the
	// currently active company and append a number to the ticker of the
	// delisted company. The ACTIONS table provides a record of historical
	// ticker changes.
	Ticker string

	// [Entity] The composite FIGI is a globally unique identifier assigned
	// by openfigi.com
	CompositeFigi string

	// [Entity] The dimension field allows you to take different dimensional
	// views of data over time. ARQ: Quarterly; excluding restatements; MRQ:
	// Quarterly; including restatements; ARY: annual; excluding restatements;
	// MRY: annual; including restatements; ART: trailing-twelve-months;
	// excluding restatements; MRT: trailing-twelve-months; including
	// restatements.
	Dimension string

	// [Entity] The Date Key represents the normalized [ReportPeriod]. This
	// provides a common date to query for which is necessary due to
	// irregularity in report periods across companies. For example; if the
	// report period is 2015-09-26; the date key will be 2015-09-30 for
	// quarterly and trailing-twelve-month dimensions (ARQ;MRQ;ART;MRT); and
	// 2015-12-31 for annual dimensions (ARY;MRY). We also employ offsets in
	// order to maximise comparability of the period across companies. For
	// example consider two companies: one with a quarter ending on 2018-07-24;
	// and the other with a quarter ending on 2018-06-28. A naive normalization
	// process would assign these to differing calendar quarters of 2018-09-30
	// and 2018-06-30 respectively. However; we assign these both to the
	// 2018-06-30 calendar quarter because this maximises the overlap in the
	// report periods in question and therefore the comparability of this
	// period.
	DateKey time.Time

	// [Entity] The Report Period represents the end date of the fiscal period.
	ReportPeriod time.Time // YYYY-MM-DD

	// [Entity] Last Updated represents the last date that this database entry
	// was updated; which is useful to users when updating their local records.
	LastUpdated time.Time // YYYY-MM-DD

	// [Balance Sheet] A component of [Equity] representing the accumulated
	// change in equity from transactions and other events and circumstances
	// from non-owner sources; net of tax effect; at period end. Includes
	// foreign currency translation items; certain pension adjustments;
	// unrealized gains and losses on certain investments in debt and equity
	// securities. Where this item is not contained on the company consolidated
	// financial statements and cannot otherwise be imputed the value of 0 is
	// used.
	AccumulatedOtherComprehensiveIncome int64 // currency

	// [Balance Sheet] Sum of the carrying amounts as of the balance sheet date
	// of all assets that are recognized. Major components are [CashnEq];
	// [Investments];[Intangibles]; [PPNENet];[TaxAssets] and [Receivables].
	TotalAssets int64 // currency

	// [Metrics] Average asset value for the period used in calculation of [ROE]
	// and [ROA]; derived from [Assets].
	AverageAssets int64 // currency

	// [Balance Sheet] The current portion of [Assets]; reported if a company
	// operates a classified balance sheet that segments current and non-current
	// assets.
	CurrentAssets int64 // currency

	// [Balance Sheet] Amount of non-current assets; for companies that operate
	// a classified balance sheet. Calculated as the different between Total
	// Assets [Assets] and Current Assets [AssetsC].
	AssetsNonCurrent int64 // currency

	// [Metrics] Asset turnover is a measure of a firms operating efficiency;
	// calculated by dividing [Revenue] by [AssetsAVG]. Often a component of
	// DuPont ROE analysis.
	AssetTurnover float64 // ratio

	// [Metrics] Measures the ratio between [Equity] and [SharesWA] as adjusted
	// by [ShareFactor].
	BookValuePerShare float64 // currency/share

	// [Cash Flow Statement] A component of [NCFI] representing the net cash
	// inflow/outflow associated with the acquisition & disposal of long-lived;
	// physical & intangible assets that are used in the normal conduct of
	// business to produce goods and services and are not intended for resale.
	// Includes cash inflows/outflows to pay for construction of
	// self-constructed assets & software. Where this item is not contained on
	// the company consolidated financial statements and cannot otherwise be
	// imputed the value of 0 is used.
	CapitalExpenditure int64 // currency

	// [Balance Sheet] A component of [Assets] representing the amount of
	// currency on hand as well as demand deposits with banks or financial
	// institutions. Where this item is not contained on the company
	// consolidated financial statements and cannot otherwise be imputed the
	// value of 0 is used.
	CashAndEquivalents int64 // currency

	// [Income Statement] The aggregate cost of goods produced and sold and
	// services rendered during the reporting period. Where this item is not
	// contained on the company consolidated financial statements and cannot
	// otherwise be imputed the value of 0 is used.
	CostOfRevenue int64 // currency

	// [Income Statement] The portion of profit or loss for the period; net of
	// income taxes; which is attributable to the consolidated entity; before
	// the deduction of [NetIncNCI].
	ConsolidatedIncome int64 // currency

	// [Metrics] The ratio between [AssetsC] and [LiabilitiesC]; for companies
	// that operate a classified balance sheet.
	CurrentRatio float64 // ratio

	// [Metrics] Measures the ratio between [Liabilities] and [Equity].
	DebtToEquityRatio float64 // ratio

	// [Balance Sheet] A component of [Liabilities] representing the total
	// amount of current and non-current debt owed. Includes secured and
	// unsecured bonds issued; commercial paper; notes payable; credit
	// facilities; lines of credit; capital lease obligations; operating lease
	// obligations; and convertible notes. Where this item is not contained on
	// the company consolidated financial statements and cannot otherwise be
	// imputed the value of 0 is used.
	TotalDebt int64 // currency

	// [Balance Sheet] The current portion of [Debt]; reported if the company
	// operates a classified balance sheet that segments current and non-current
	// liabilities. Where this item is not contained on the company consolidated
	// financial statements and cannot otherwise be imputed the value of 0 is
	// used.
	DebtCurrent int64 // currency

	// [Balance Sheet] The non-current portion of [Debt] reported if the company
	// operates a classified balance sheet that segments current and non-current
	// liabilities. Where this item is not contained on the company consolidated
	// financial statements and cannot otherwise be imputed the value of 0 is
	// used.
	DebtNonCurrent int64 // currency

	// [Balance Sheet] A component of [Liabilities] representing the carrying
	// amount of consideration received or receivable on potential earnings that
	// were not recognized as revenue; including sales; license fees; and
	// royalties; but excluding interest income. Where this item is not
	// contained on the company consolidated financial statements and cannot
	// otherwise be imputed the value of 0 is used.
	DeferredRevenue int64 // currency

	// [Cash Flow Statement] A component of operating cash flow representing the
	// aggregate net amount of depreciation; amortization; and accretion
	// recognized during an accounting period. As a non-cash item; the net
	// amount is added back to net income when calculating cash provided by or
	// used in operations using the indirect method. Where this item is not
	// contained on the company consolidated financial statements and cannot
	// otherwise be imputed the value of 0 is used.
	DepreciationAmortizationAndAccretion int64 // currency

	// [Balance Sheet] A component of [Liabilities] representing the total of
	// all deposit liabilities held; including foreign and domestic; interest
	// and noninterest bearing. May include demand deposits; saving deposits;
	// Negotiable Order of Withdrawal and time deposits among others. Where this
	// item is not contained on the company consolidated financial statements
	// and cannot otherwise be imputed the value of 0 is used.
	Deposits int64 // currency

	// [Metrics] Dividend Yield measures the ratio between a company's [DPS] and
	// its [Price]. Where this item is not contained on the company consolidated
	// financial statements and cannot otherwise be imputed the value of 0 is
	// used.
	DividendYield float64 // ratio

	// [Income Statement] Aggregate dividends declared during the period for
	// each split-adjusted share of common stock outstanding. Where this item is
	// not contained on the company consolidated financial statements and cannot
	// otherwise be imputed the value of 0 is used.
	DividendsPerBasicCommonShare float64 // USD/share

	// [Income Statement] Earnings Before Interest and Tax is calculated by
	// adding [TaxExp] and [IntExp] back to [NetInc].
	EBIT int64 // currency

	// [Metrics] EBITDA is a non-GAAP accounting metric that is widely used when
	// assessing the performance of companies; calculated by adding [DepAmor]
	// back to [EBIT].
	EBITDA int64 // currency

	// [Metrics] Measures the ratio between a company's [EBITDA] and [Revenue].
	EBITDAMargin float64 // ratio

	// [Metrics] Earnings Before Tax is calculated by adding [TaxExp] back to
	// [NetInc].
	EBT int64 // currency

	// [Income Statement] Earnings per share as calculated and reported by the
	// company. Approximates to the amount of [NetIncCmn] for the period per
	// each [SharesWA] after adjusting for [ShareFactor].
	EPS float64 // currency/share

	// [Income Statement] Earnings per diluted share as calculated and reported
	// by the company. Approximates to the amount of [NetIncCmn] for the period
	// per each [SharesWADil] after adjusting for [ShareFactor]..
	EPSDiluted float64 // currency/share

	// [Balance Sheet] A principal component of the balance sheet; in addition
	// to [Liabilities] and [Assets]; that represents the total of all
	// stockholders' equity (deficit) items; net of receivables from officers;
	// directors; owners; and affiliates of the entity which are attributable to
	// the parent.
	Equity int64 // currency)

	// [Metrics] Average equity value for the period used in calculation of
	// [ROE]; derived from [Equity].
	EquityAvg int64 // currency

	// [Metrics] Enterprise value is a measure of the value of a business as a
	// whole; calculated as [MarketCap] plus [DebtUSD] minus [CashnEqUSD].
	EnterpriseValue int64 // USD

	// [Metrics] Measures the ratio between [EV] and [EBITUSD].
	EVtoEBIT int64 // ratio

	// [Metrics] Measures the ratio between [EV] and [EBITDAUSD].
	EVtoEBITDA float64 // ratio

	// [Metrics] Free Cash Flow is a measure of financial performance calculated
	// as [NCFO] minus [CapEx].
	FreeCashFlow int64 // currency

	// [Metrics] Free Cash Flow per Share is a valuation metric calculated by
	// dividing [FCF] by [SharesWA] and [ShareFactor].
	FreeCashFlowPerShare float64 // currency/share

	// [Metrics] The exchange rate used for the conversion of foreign currency
	// to USD for non-US companies that do not report in USD.
	FxUSD float64 // ratio

	// [Income Statement] Aggregate revenue [Revenue] less cost of revenue [CoR]
	// directly attributable to the revenue generation activity.
	GrossProfit int64 // currency

	// [Metrics] Gross Margin measures the ratio between a company's [GP] and
	// [Revenue].
	GrossMargin float64 // ratio

	// [Balance Sheet] A component of [Assets] representing the carrying amounts
	// of all intangible assets and goodwill as of the balance sheet date; net
	// of accumulated amortization and impairment charges. Where this item is
	// not contained on the company consolidated financial statements and cannot
	// otherwise be imputed the value of 0 is used.
	Intangibles int64 // currency

	// [Income Statement] Amount of the cost of borrowed funds accounted for as
	// interest expense. Where this item is not contained on the company
	// consolidated financial statements and cannot otherwise be imputed the
	// value of 0 is used.
	InterestExpense int64 // currency

	// [Metrics] Invested capital is an input into the calculation of [ROIC];
	// and is calculated as: [Debt] plus [Assets] minus [Intangibles] minus
	// [CashnEq] minus [LiabilitiesC]. Please note this calculation method is
	// subject to change.
	InvestedCapital int64 // currency

	// [Metrics] Average invested capital value for the period used in the
	// calculation of [ROIC]; and derived from [InvCap]. Invested capital is an
	// input into the calculation of [ROIC]; and is calculated as: [Debt] plus
	// [Assets] minus [Intangibles] minus [CashnEq] minus [LiabilitiesC]. Please
	// note this calculation method is subject to change.
	InvestedCapitalAverage int64 // currency

	// [Balance Sheet] A component of [Assets] representing the amount after
	// valuation and reserves of inventory expected to be sold; or consumed
	// within one year or operating cycle; if longer. Where this item is not
	// contained on the company consolidated financial statements and cannot
	// otherwise be imputed the value of 0 is used.
	Inventory int64 // currency

	// [Balance Sheet] A component of [Assets] representing the total amount of
	// marketable and non-marketable securties; loans receivable and other
	// invested assets. Where this item is not contained on the company
	// consolidated financial statements and cannot otherwise be imputed the
	// value of 0 is used.
	Investments int64 // currency

	// [Balance Sheet] The current portion of [Investments]; reported if the
	// company operates a classified balance sheet that segments current and
	// non-current assets. Where this item is not contained on the company
	// consolidated financial statements and cannot otherwise be imputed the
	// value of 0 is used.
	InvestmentsCurrent int64 // currency

	// [Balance Sheet] The non-current portion of [Investments]; reported if the
	// company operates a classified balance sheet that segments current and
	// non-current assets. Where this item is not contained on the company
	// consolidated financial statements and cannot otherwise be imputed the
	// value of 0 is used.
	InvestmentsNonCurrent int64 // currency

	// [Balance Sheet] Sum of the carrying amounts as of the balance sheet date
	// of all liabilities that are recognized. Principal components are [Debt];
	// [DeferredRev]; [Payables];[Deposits]; and [TaxLiabilities].
	TotalLiabilities int64 // currency

	// [Balance Sheet] The current portion of [Liabilities]; reported if the
	// company operates a classified balance sheet that segments current and
	// non-current liabilities.
	CurrentLiabilities int64 // currency

	// [Balance Sheet] The non-current portion of [Liabilities]; reported if the
	// company operates a classified balance sheet that segments current and
	// non-current liabilities.
	LiabilitiesNonCurrent int64 // currency

	// [Metrics] Represents the product of [SharesBas]; [Price] and
	// [ShareFactor].
	MarketCapitalization int64 // USD

	// [Cash Flow Statement] Principal component of the cash flow statement
	// representing the amount of increase (decrease) in cash and cash
	// equivalents. Includes [NCFO]; investing [NCFI] and financing [NCFF] for
	// continuing and discontinued operations; and the effect of exchange rate
	// changes on cash [NCFX].
	NetCashFlow int64 // currency

	// [Cash Flow Statement] A component of [NCFI] representing the net cash
	// inflow (outflow) associated with the acquisition & disposal of
	// businesses; joint-ventures; affiliates; and other named investments.
	// Where this item is not contained on the company consolidated financial
	// statements and cannot otherwise be imputed the value of 0 is used.
	NetCashFlowBusiness int64 // currency

	// [Cash Flow Statement] A component of [NCFF] representing the net cash
	// inflow (outflow) from common equity changes. Includes additional capital
	// contributions from share issuances and exercise of stock options; and
	// outflow from share repurchases.  Where this item is not contained on the
	// company consolidated financial statements and cannot otherwise be imputed
	// the value of 0 is used.
	NetCashFlowCommon int64 // currency

	// [Cash Flow Statement] A component of [NCFF] representing the net cash
	// inflow (outflow) from issuance (repayment) of debt securities. Where this
	// item is not contained on the company consolidated financial statements
	// and cannot otherwise be imputed the value of 0 is used.
	NetCashFlowDebt int64 // currency

	// [Cash Flow Statement] A component of [NCFF] representing dividends and
	// dividend equivalents paid on common stock and restricted stock units.
	// Where this item is not contained on the company consolidated financial
	// statements and cannot otherwise be imputed the value of 0 is used.
	NetCashFlowDividend int64 // currency

	// [Cash Flow Statement] A component of [NCF] representing the amount of
	// cash inflow (outflow) from financing activities; from continuing and
	// discontinued operations. Principal components of financing cash flow are:
	// issuance (purchase) of equity shares; issuance (repayment) of debt
	// securities; and payment of dividends & other cash distributions. Where
	// this item is not contained on the company consolidated financial
	// statements and cannot otherwise be imputed the value of 0 is used.
	NetCashFlowFromFinancing int64 // currency

	// [Cash Flow Statement] A component of [NCF] representing the amount of
	// cash inflow (outflow) from investing activities; from continuing and
	// discontinued operations. Principal components of investing cash flow are:
	// capital (expenditure) disposal of equipment [CapEx]; business
	// (acquisitions) disposition [NCFBus] and investment (acquisition) disposal
	// [NCFInv]. Where this item is not contained on the company consolidated
	// financial statements and cannot otherwise be imputed the value of 0 is
	// used.
	NetCashFlowFromInvesting int64 // currency

	// [Cash Flow Statement] A component of [NCFI] representing the net cash
	// inflow (outflow) associated with the acquisition & disposal of
	// investments; including marketable securities and loan originations. Where
	// this item is not contained on the company consolidated financial
	// statements and cannot otherwise be imputed the value of 0 is used.
	NetCashFlowInvest int64 // currency

	// [Cash Flow Statement] A component of [NCF] representing the amount of
	// cash inflow (outflow) from operating activities; from continuing and
	// discontinued operations.
	NetCashFlowFromOperations int64 // currency

	// [Cash Flow Statement] A component of Net Cash Flow [NCF] representing the
	// amount of increase (decrease) from the effect of exchange rate changes on
	// cash and cash equivalent balances held in foreign currencies. Where this
	// item is not contained on the company consolidated financial statements
	// and cannot otherwise be imputed the value of 0 is used.
	NetCashFlowFx int64 // currency)

	// [Income Statement] The portion of profit or loss for the period; net of
	// income taxes; which is attributable to the parent after the deduction of
	// [NetIncNCI] from [ConsolInc]; and before the deduction of [PrefDivIS].
	NetIncome int64 // currency

	// [Income Statement] The amount of net income (loss) for the period due to
	// common shareholders. Typically differs from [NetInc] to the parent entity
	// due to the deduction of [PrefDivIS].
	NetIncomeCommonStock int64 // currency

	// [Income Statement] Amount of loss (income) from a disposal group; net of
	// income tax; reported as a separate component of income. Where this item
	// is not contained on the company consolidated financial statements and
	// cannot otherwise be imputed the value of 0 is used.
	NetLossIncomeDiscontinuedOperations int64 // currency)

	// [Income Statement] The portion of income which is attributable to
	// non-controlling interest shareholders; subtracted from [ConsolInc] in
	// order to obtain [NetInc]. Where this item is not contained on the company
	// consolidated financial statements and cannot otherwise be imputed the
	// value of 0 is used.
	NetIncomeToNonControllingInterests int64 // currency

	// [Metrics] Measures the ratio between a company's [NetIncCmn] and
	// [Revenue].
	ProfitMargin float64 // ratio

	// [Income Statement] Operating expenses represent the total expenditure on
	// [SGnA]; [RnD] and other operating expense items; it excludes [CoR].
	OperatingExpenses int64 // currency

	// [Income Statement] Operating income is a measure of financial performance
	// before the deduction of [IntExp]; [TaxExp] and other Non-Operating items.
	// It is calculated as [GP] minus [OpEx].
	OperatingIncome int64 // currency

	// [Balance Sheet] A component of [Liabilities] representing trade and
	// non-trade payables. Where this item is not contained on the company
	// consolidated financial statements and cannot otherwise be imputed the
	// value of 0 is used.
	Payables int64 // currency

	// [Metrics] The percentage of earnings paid as dividends to common
	// stockholders. Calculated by dividing [DPS] by [EPSUSD].
	PayoutRatio float64 // ratio

	// [Metrics] Measures the ratio between [MarketCap] and [EquityUSD].
	PB float64 // ratio

	// [Metrics] Measures the ratio between [MarketCap] and [NetIncCmnUSD]
	PE float64 // ratio

	// [Metrics] An alternative to [PE] representing the ratio between [Price]
	// and [EPSUSD].
	PE1 float64 // ratio

	// [Balance Sheet] A component of [Assets] representing the amount after
	// accumulated depreciation; depletion and amortization of physical assets
	// used in the normal conduct of business to produce goods and services and
	// not intended for resale. Includes Operating Right of Use Assets. Where
	// this item is not contained on the company consolidated financial
	// statements and cannot otherwise be imputed the value of 0 is used.
	PropertyPlantAndEquipmentNet int64 // currency

	// [Income Statement] Income statement item reflecting dividend payments to
	// preferred stockholders. Subtracted from Net Income to Parent [NetInc] to
	// obtain Net Income to Common Stockholders [NetIncCmn]. Where this item is
	// not contained on the company consolidated financial statements and cannot
	// otherwise be imputed the value of 0 is used.
	PreferredDividendsIncomeStatementImpact int64 // currency

	// [Entity] The price per common share adjusted for stock splits but not
	// adjusted for dividends; used in the computation of [PE1]; [PS1];
	// [DivYield] and [SPS].
	Price float64 // USD/share

	// [Metrics] Measures the ratio between [MarketCap] and [RevenueUSD].
	PS float64 // ratio

	// [Metrics] An alternative calculation method to [PS]; that measures the
	// ratio between a company's [Price] and it's [SPS].
	PS1 float64 // ratio

	// [Balance Sheet] A component of [Assets] representing trade and non-trade
	// receivables. Where this item is not contained on the company consolidated
	// financial statements and cannot otherwise be imputed the value of 0 is
	// used.
	Receivables int64 // currency

	// [Balance Sheet] A component of [Equity] representing the cumulative
	// amount of the entities undistributed earnings or deficit. May only be
	// reported annually by certain companies; rather than quarterly.
	AccumulatedRetainedEarningsDeficit int64 // currency

	// [Income Statement] The amount of Revenue recognised from goods sold;
	// services rendered; insurance premiums; or other activities that
	// constitute an earning process. Interest income for financial institutions
	// is reported net of interest expense and provision for credit losses.
	// Where this item is not contained on the company consolidated financial
	// statements and cannot otherwise be imputed the value of 0 is used.
	Revenues int64 // currency

	// [Income Statement] A component of [OpEx] representing the aggregate costs
	// incurred in a planned search or critical investigation aimed at discovery
	// of new knowledge with the hope that such knowledge will be useful in
	// developing a new product or service. Where this item is not contained on
	// the company consolidated financial statements and cannot otherwise be
	// imputed the value of 0 is used.
	RandDExpenses int64 // currency

	// [Metrics] Return on assets measures how profitable a company is
	// [NetIncCmn] relative to its total assets [AssetsAvg].
	ROA float64 // ratio

	// [Metrics] Return on equity measures a corporation's profitability by
	// calculating the amount of [NetIncCmn] returned as a percentage of
	// [EquityAvg].
	ROE float64 // ratio

	// [Metrics] Return on Invested Capital is a ratio estimated by dividing
	// [EBIT] by [InvCapAvg]. [InvCap] is calculated as: [Debt] plus [Assets]
	// minus [Intangibles] minus [CashnEq] minus [LiabilitiesC]. Please note
	// this calculation method is subject to change.
	ROIC float64 // ratio

	// [Metrics] Return on Sales is a ratio to evaluate a company's operational
	// efficiency; calculated by dividing [EBIT] by [Revenue]. ROS is often a
	// component of DuPont ROE analysis.
	ReturnOnSales float64 // ratio

	// [Cash Flow Statement] A component of [NCFO] representing the total amount
	// of noncash; equity-based employee remuneration. This may include the
	// value of stock or unit options; amortization of restricted stock or
	// units; and adjustment for officers' compensation. As noncash; this
	// element is an add back when calculating net cash generated by operating
	// activities using the indirect method.
	ShareBasedCompensation int64 // currency

	// [Income Statement] A component of [OpEx] representing the aggregate total
	// costs related to selling a firm's product and services; as well as all
	// other general and administrative expenses. Direct selling expenses (for
	// example; credit; warranty; and advertising) are expenses that can be
	// directly linked to the sale of specific products. Indirect selling
	// expenses are expenses that cannot be directly linked to the sale of
	// specific products; for example telephone expenses; Internet; and postal
	// charges. General and administrative expenses include salaries of
	// non-sales personnel; rent; utilities; communication; etc. Where this item
	// is not contained on the company consolidated financial statements and
	// cannot otherwise be imputed the value of 0 is used.
	SellingGeneralAndAdministrativeExpense int64 // currency

	// [Entity] Share factor is a multiplicant in the calculation of [MarketCap]
	// and is used to adjust for: American Depository Receipts (ADRs) that
	// represent more or less than 1 underlying share; and; companies which have
	// different earnings share for different share classes (eg Berkshire
	// Hathaway - BRK.B).
	ShareFactor float64 // ratio

	// [Entity] The number of shares or other units outstanding of the entity's
	// capital or common stock or other ownership interests; as stated on the
	// cover of related periodic report (10-K/10-Q); after adjustment for stock
	// splits.
	SharesBasic int64 // units

	// [Income Statement] The weighted average number of shares or units issued
	// and outstanding that are used by the company to calculate [EPS];
	// determined based on the timing of issuance of shares or units in the
	// period.
	WeightedAverageShares int64 // units

	// [Income Statement] The weighted average number of shares or units issued
	// and outstanding that are used by the company to calculate [EPSDil];
	// determined based on the timing of issuance of shares or units in the
	// period.
	WeightedAverageSharesDiluted int64 // units

	// [Metrics] Sales per Share measures the ratio between [RevenueUSD] and
	// [SharesWA] as adjusted by [ShareFactor].
	SalesPerShare float64 // USD/share

	// [Metrics] The value of tangibles assets calculated as the difference
	// between [Assets] and [Intangibles].
	TangibleAssetValue int64 // currency

	// [Balance Sheet] A component of [Assets] representing tax assets and
	// receivables. Where this item is not contained on the company consolidated
	// financial statements and cannot otherwise be imputed the value of 0 is
	// used.
	TaxAssets int64 // currency

	// [Income Statement] Amount of current income tax expense (benefit) and
	// deferred income tax expense (benefit) pertaining to continuing
	// operations. Where this item is not contained on the company consolidated
	// financial statements and cannot otherwise be imputed the value of 0 is
	// used.
	IncomeTaxExpense int64 // currency

	// [Balance Sheet] A component of [Liabilities] representing outstanding tax
	// liabilities. Where this item is not contained on the company consolidated
	// financial statements and cannot otherwise be imputed the value of 0 is
	// used.
	TaxLiabilities int64 // currency

	// [Metrics] Measures the ratio between [Tangibles] and [SharesWA] as
	// adjusted by [ShareFactor].
	TangibleAssetsBookValuePerShare float64 // currency/share

	// [Metrics] Working capital measures the difference between [AssetsC] and
	// [LiabilitiesC].
	WorkingCapital int64 // currency
}

func (fundamental *Fundamental) SaveDB(ctx context.Context, tbl string, dbConn *pgxpool.Conn) error {
	if fundamental.CompositeFigi == "" {
		return nil
	}

	tx, err := dbConn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Commit(ctx); err != nil {
			log.Error().Err(err).Msg("error committing asset transaction to database")
		}
	}()

	sql := fmt.Sprintf(`INSERT INTO %[1]s (
		"event_date",
		"ticker",
		"composite_figi",
		"dimension",
		"date_key",
		"report_period",
		"last_updated",
		"accumulated_other_comprehensive_income",
		"total_assets",
		"average_assets",
		"current_assets",
		"assets_non_current",
		"asset_turnover",
		"book_value_per_share",
		"capital_expenditure",
		"cash_and_equivalents",
		"cost_of_revenue",
		"consolidated_income",
		"current_ratio",
		"debt_to_equity_ratio",
		"total_debt",
		"debt_current",
		"debt_non_current",
		"deferred_revenue",
		"depreciation_amortization_and_accretion",
		"deposits",
		"dividend_yield",
		"dividends_per_basic_common_share",
		"ebit",
		"ebitda",
		"ebitda_margin",
		"ebt",
		"eps",
		"eps_diluted",
		"equity",
		"equity_avg",
		"enterprise_value",
		"ev_to_ebit",
		"ev_to_ebitda",
		"free_cash_flow",
		"free_cash_flow_per_share",
		"fx_usd",
		"gross_profit",
		"gross_margin",
		"intangibles",
		"interest_expense",
		"invested_capital",
		"invested_capital_average",
		"inventory",
		"investments",
		"investments_current",
		"investments_non_current",
		"total_liabilities",
		"current_liabilities",
		"liabilities_non_current",
		"market_capitalization",
		"net_cash_flow",
		"net_cash_flow_business",
		"net_cash_flow_common",
		"net_cash_flow_debt",
		"net_cash_flow_dividend",
		"net_cash_flow_from_financing",
		"net_cash_flow_from_investing",
		"net_cash_flow_invest",
		"net_cash_flow_from_operations",
		"net_cash_flow_fx",
		"net_income",
		"net_income_common_stock",
		"net_loss_income_discontinued_operations",
		"net_income_to_non_controlling_interests",
		"profit_margin",
		"operating_expenses",
		"operating_income",
		"payables",
		"payout_ratio",
		"pb",
		"pe",
		"pe1",
		"property_plant_and_equipment_net",
		"preferred_dividends_income_statement_impact",
		"price",
		"ps",
		"ps1",
		"receivables",
		"accumulated_retained_earnings_deficit",
		"revenues",
		"r_and_d_expenses",
		"roa",
		"roe",
		"roic",
		"return_on_sales",
		"share_based_compensation",
		"selling_general_and_administrative_expense",
		"share_factor",
		"shares_basic",
		"weighted_average_shares",
		"weighted_average_shares_diluted",
		"sales_per_share",
		"tangible_asset_value",
		"tax_assets",
		"income_tax_expense",
		"tax_liabilities",
		"tangible_assets_book_value_per_share",
		"working_capital"
	) VALUES (
	 	$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15, $16,
		$17, $18, $19, $20, $21, $22, $23, $24, $25, $26, $27, $28, $29, $30,
		$31, $32, $33, $34, $35, $36, $37, $38, $39, $40, $41, $42, $43, $44,
		$45, $46, $47, $48, $49, $50, $51, $52, $53, $54, $55, $56, $57, $58,
		$59, $60, $61, $62, $63, $64, $65, $66, $67, $68, $69, $70, $71, $72,
		$73, $74, $75, $76, $77, $78, $79, $80, $81, $82, $83, $84, $85, $86,
		$87, $88, $89, $90, $91, $92, $93, $94, $95, $96, $97, $98, $99, $100,
		$101, $102, $103, $104
	) ON CONFLICT ON CONSTRAINT %[1]s_pkey DO UPDATE SET
		ticker = EXCLUDED.ticker,
		event_date = EXCLUDED.event_date,
		date_key = EXCLUDED.date_key,
		report_period = EXCLUDED.report_period,
		last_updated = EXCLUDED.last_updated,
		accumulated_other_comprehensive_income = EXCLUDED.accumulated_other_comprehensive_income,
		total_assets = EXCLUDED.total_assets,
		average_assets = EXCLUDED.average_assets,
		current_assets = EXCLUDED.current_assets,
		assets_non_current = EXCLUDED.assets_non_current,
		asset_turnover = EXCLUDED.asset_turnover,
		book_value_per_share = EXCLUDED.book_value_per_share,
		capital_expenditure = EXCLUDED.capital_expenditure,
		cash_and_equivalents = EXCLUDED.cash_and_equivalents,
		cost_of_revenue = EXCLUDED.cost_of_revenue,
		consolidated_income = EXCLUDED.consolidated_income,
		current_ratio = EXCLUDED.current_ratio,
		debt_to_equity_ratio = EXCLUDED.debt_to_equity_ratio,
		total_debt = EXCLUDED.total_debt,
		debt_current = EXCLUDED.debt_current,
		debt_non_current = EXCLUDED.debt_non_current,
		deferred_revenue = EXCLUDED.deferred_revenue,
		depreciation_amortization_and_accretion = EXCLUDED.depreciation_amortization_and_accretion,
		deposits = EXCLUDED.deposits,
		dividend_yield = EXCLUDED.dividend_yield,
		dividends_per_basic_common_share = EXCLUDED.dividends_per_basic_common_share,
		ebit = EXCLUDED.ebit,
		ebitda = EXCLUDED.ebitda,
		ebitda_margin = EXCLUDED.ebitda_margin,
		ebt = EXCLUDED.ebt,
		eps = EXCLUDED.eps,
		eps_diluted = EXCLUDED.eps_diluted,
		equity = EXCLUDED.equity,
		equity_avg = EXCLUDED.equity_avg,
		enterprise_value = EXCLUDED.enterprise_value,
		ev_to_ebit = EXCLUDED.ev_to_ebit,
		ev_to_ebitda = EXCLUDED.ev_to_ebitda,
		free_cash_flow = EXCLUDED.free_cash_flow,
		free_cash_flow_per_share = EXCLUDED.free_cash_flow_per_share,
		fx_usd = EXCLUDED.fx_usd,
		gross_profit = EXCLUDED.gross_profit,
		gross_margin = EXCLUDED.gross_margin,
		intangibles = EXCLUDED.intangibles,
		interest_expense = EXCLUDED.interest_expense,
		invested_capital = EXCLUDED.invested_capital,
		invested_capital_average = EXCLUDED.invested_capital_average,
		inventory = EXCLUDED.inventory,
		investments = EXCLUDED.investments,
		investments_current = EXCLUDED.investments_current,
		investments_non_current = EXCLUDED.investments_non_current,
		total_liabilities = EXCLUDED.total_liabilities,
		current_liabilities = EXCLUDED.current_liabilities,
		liabilities_non_current = EXCLUDED.liabilities_non_current,
		market_capitalization = EXCLUDED.market_capitalization,
		net_cash_flow = EXCLUDED.net_cash_flow,
		net_cash_flow_business = EXCLUDED.net_cash_flow_business,
		net_cash_flow_common = EXCLUDED.net_cash_flow_common,
		net_cash_flow_debt = EXCLUDED.net_cash_flow_debt,
		net_cash_flow_dividend = EXCLUDED.net_cash_flow_dividend,
		net_cash_flow_from_financing = EXCLUDED.net_cash_flow_from_financing,
		net_cash_flow_from_investing = EXCLUDED.net_cash_flow_from_investing,
		net_cash_flow_invest = EXCLUDED.net_cash_flow_invest,
		net_cash_flow_from_operations = EXCLUDED.net_cash_flow_from_operations,
		net_cash_flow_fx = EXCLUDED.net_cash_flow_fx,
		net_income = EXCLUDED.net_income,
		net_income_common_stock = EXCLUDED.net_income_common_stock,
		net_loss_income_discontinued_operations = EXCLUDED.net_loss_income_discontinued_operations,
		net_income_to_non_controlling_interests = EXCLUDED.net_income_to_non_controlling_interests,
		profit_margin = EXCLUDED.profit_margin,
		operating_expenses = EXCLUDED.operating_expenses,
		operating_income = EXCLUDED.operating_income,
		payables = EXCLUDED.payables,
		payout_ratio = EXCLUDED.payout_ratio,
		pb = EXCLUDED.pb,
		pe = EXCLUDED.pe,
		pe1 = EXCLUDED.pe1,
		property_plant_and_equipment_net = EXCLUDED.property_plant_and_equipment_net,
		preferred_dividends_income_statement_impact = EXCLUDED.preferred_dividends_income_statement_impact,
		price = EXCLUDED.price,
		ps = EXCLUDED.ps,
		ps1 = EXCLUDED.ps1,
		receivables = EXCLUDED.receivables,
		accumulated_retained_earnings_deficit = EXCLUDED.accumulated_retained_earnings_deficit,
		revenues = EXCLUDED.revenues,
		r_and_d_expenses = EXCLUDED.r_and_d_expenses,
		roa = EXCLUDED.roa,
		roe = EXCLUDED.roe,
		roic = EXCLUDED.roic,
		return_on_sales = EXCLUDED.return_on_sales,
		share_based_compensation = EXCLUDED.share_based_compensation,
		selling_general_and_administrative_expense = EXCLUDED.selling_general_and_administrative_expense,
		share_factor = EXCLUDED.share_factor,
		shares_basic = EXCLUDED.shares_basic,
		weighted_average_shares = EXCLUDED.weighted_average_shares,
		weighted_average_shares_diluted = EXCLUDED.weighted_average_shares_diluted,
		sales_per_share = EXCLUDED.sales_per_share,
		tangible_asset_value = EXCLUDED.tangible_asset_value,
		tax_assets = EXCLUDED.tax_assets,
		income_tax_expense = EXCLUDED.income_tax_expense,
		tax_liabilities = EXCLUDED.tax_liabilities,
		tangible_assets_book_value_per_share = EXCLUDED.tangible_assets_book_value_per_share,
		working_capital = EXCLUDED.working_capital`, tbl)

	_, err = tx.Exec(ctx, sql,
		fundamental.EventDate,
		fundamental.Ticker,
		fundamental.CompositeFigi,
		fundamental.Dimension,
		fundamental.DateKey,
		fundamental.ReportPeriod,
		fundamental.LastUpdated,
		fundamental.AccumulatedOtherComprehensiveIncome,
		fundamental.TotalAssets,
		fundamental.AverageAssets,
		fundamental.CurrentAssets,
		fundamental.AssetsNonCurrent,
		fundamental.AssetTurnover,
		fundamental.BookValuePerShare,
		fundamental.CapitalExpenditure,
		fundamental.CashAndEquivalents,
		fundamental.CostOfRevenue,
		fundamental.ConsolidatedIncome,
		fundamental.CurrentRatio,
		fundamental.DebtToEquityRatio,
		fundamental.TotalDebt,
		fundamental.DebtCurrent,
		fundamental.DebtNonCurrent,
		fundamental.DeferredRevenue,
		fundamental.DepreciationAmortizationAndAccretion,
		fundamental.Deposits,
		fundamental.DividendYield,
		fundamental.DividendsPerBasicCommonShare,
		fundamental.EBIT,
		fundamental.EBITDA,
		fundamental.EBITDAMargin,
		fundamental.EBT,
		fundamental.EPS,
		fundamental.EPSDiluted,
		fundamental.Equity,
		fundamental.EquityAvg,
		fundamental.EnterpriseValue,
		fundamental.EVtoEBIT,
		fundamental.EVtoEBITDA,
		fundamental.FreeCashFlow,
		fundamental.FreeCashFlowPerShare,
		fundamental.FxUSD,
		fundamental.GrossProfit,
		fundamental.GrossMargin,
		fundamental.Intangibles,
		fundamental.InterestExpense,
		fundamental.InvestedCapital,
		fundamental.InvestedCapitalAverage,
		fundamental.Inventory,
		fundamental.Investments,
		fundamental.InvestmentsCurrent,
		fundamental.InvestmentsNonCurrent,
		fundamental.TotalLiabilities,
		fundamental.CurrentLiabilities,
		fundamental.LiabilitiesNonCurrent,
		fundamental.MarketCapitalization,
		fundamental.NetCashFlow,
		fundamental.NetCashFlowBusiness,
		fundamental.NetCashFlowCommon,
		fundamental.NetCashFlowDebt,
		fundamental.NetCashFlowDividend,
		fundamental.NetCashFlowFromFinancing,
		fundamental.NetCashFlowFromInvesting,
		fundamental.NetCashFlowInvest,
		fundamental.NetCashFlowFromOperations,
		fundamental.NetCashFlowFx,
		fundamental.NetIncome,
		fundamental.NetIncomeCommonStock,
		fundamental.NetLossIncomeDiscontinuedOperations,
		fundamental.NetIncomeToNonControllingInterests,
		fundamental.ProfitMargin,
		fundamental.OperatingExpenses,
		fundamental.OperatingIncome,
		fundamental.Payables,
		fundamental.PayoutRatio,
		fundamental.PB,
		fundamental.PE,
		fundamental.PE1,
		fundamental.PropertyPlantAndEquipmentNet,
		fundamental.PreferredDividendsIncomeStatementImpact,
		fundamental.Price,
		fundamental.PS,
		fundamental.PS1,
		fundamental.Receivables,
		fundamental.AccumulatedRetainedEarningsDeficit,
		fundamental.Revenues,
		fundamental.RandDExpenses,
		fundamental.ROA,
		fundamental.ROE,
		fundamental.ROIC,
		fundamental.ReturnOnSales,
		fundamental.ShareBasedCompensation,
		fundamental.SellingGeneralAndAdministrativeExpense,
		fundamental.ShareFactor,
		fundamental.SharesBasic,
		fundamental.WeightedAverageShares,
		fundamental.WeightedAverageSharesDiluted,
		fundamental.SalesPerShare,
		fundamental.TangibleAssetValue,
		fundamental.TaxAssets,
		fundamental.IncomeTaxExpense,
		fundamental.TaxLiabilities,
		fundamental.TangibleAssetsBookValuePerShare,
		fundamental.WorkingCapital,
	)
	if err != nil {
		log.Error().Err(err).Str("SQL", sql).Msg("save fundamental to DB failed")

		if err2 := tx.Rollback(ctx); err2 != nil {
			log.Error().Err(err).Msg("error rollingback tx")
		}
	}

	return err
}
