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
package sharadar

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/tidwall/gjson"
	"golang.org/x/time/rate"
)

type sharadarFundamental struct {
	Ticker                                  string  // 0 = ticker (text)                     [Entity] The ticker is a unique identifier for a security in the database. Where a company is delisted and the ticker subsequently recycled for use by a different company; we utilise that ticker for the currently active company and append a number to the ticker of the delisted company. The ACTIONS table provides a record of historical ticker changes.
	Dimension                               string  // 1 = dimension (text)                  [Entity] The dimension field allows you to take different dimensional views of data over time. ARQ: Quarterly; excluding restatements; MRQ: Quarterly; including restatements; ARY: annual; excluding restatements; MRY: annual; including restatements; ART: trailing-twelve-months; excluding restatements; MRT: trailing-twelve-months; including restatements.
	CalendarDate                            string  // 2 = calendardate (date (YYYY-MM-DD))  [Entity] The Calendar Date represents the normalized [ReportPeriod]. This provides a common date to query for which is necessary due to irregularity in report periods across companies. For example; if the report period is 2015-09-26; the calendar date will be 2015-09-30 for quarterly and trailing-twelve-month dimensions (ARQ;MRQ;ART;MRT); and 2015-12-31 for annual dimensions (ARY;MRY). We also employ offsets in order to maximise comparability of the period across companies. For example consider two companies: one with a quarter ending on 2018-07-24; and the other with a quarter ending on 2018-06-28. A naive normalization process would assign these to differing calendar quarters of 2018-09-30 and 2018-06-30 respectively. However; we assign these both to the 2018-06-30 calendar quarter because this maximises the overlap in the report periods in question and therefore the comparability of this period.
	DateKey                                 string  // 3 = datekey (date (YYYY-MM-DD))       [Entity] The Date Key represents the SEC filing date for AR dimensions (ARQ;ART;ARY); and the [REPORTPERIOD] for MR dimensions (MRQ;MRT;MRY). In addition; this is the observation date used for [Price] based data such as [MarketCap]; [Price] and [PE].
	ReportPeriod                            string  // 4 = reportperiod (date (YYYY-MM-DD))  [Entity] The Report Period represents the end date of the fiscal period.
	LastUpdated                             string  // 5 = lastupdated (date (YYYY-MM-DD))   [Entity] Last Updated represents the last date that this database entry was updated; which is useful to users when updating their local records.
	AccumulatedOtherComprehensiveIncome     int64   // 6 = accoci (currency)                 [Balance Sheet] A component of [Equity] representing the accumulated change in equity from transactions and other events and circumstances from non-owner sources; net of tax effect; at period end. Includes foreign currency translation items; certain pension adjustments; unrealized gains and losses on certain investments in debt and equity securities. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	TotalAssets                             int64   // 7 = assets (currency)                 [Balance Sheet] Sum of the carrying amounts as of the balance sheet date of all assets that are recognized. Major components are [CashnEq]; [Investments];[Intangibles]; [PPNENet];[TaxAssets] and [Receivables].
	AverageAssets                           int64   // 8 = assetsavg (currency)              [Metrics] Average asset value for the period used in calculation of [ROE] and [ROA]; derived from [Assets].
	CurrentAssets                           int64   // 9 = assetsc (currency)                [Balance Sheet] The current portion of [Assets]; reported if a company operates a classified balance sheet that segments current and non-current assets.
	AssetsNonCurrent                        int64   // 10 = assetsnc (currency)              [Balance Sheet] Amount of non-current assets; for companies that operate a classified balance sheet. Calculated as the different between Total Assets [Assets] and Current Assets [AssetsC].
	AssetTurnover                           float64 // 11 = assetturnover (ratio)            [Metrics] Asset turnover is a measure of a firms operating efficiency; calculated by dividing [Revenue] by [AssetsAVG]. Often a component of DuPont ROE analysis.
	BookValuePerShare                       float64 // 12 = bvps (currency/share)            [Metrics] Measures the ratio between [Equity] and [SharesWA] as adjusted by [ShareFactor].
	CapitalExpenditure                      int64   // 13 = capex (currency)                 [Cash Flow Statement] A component of [NCFI] representing the net cash inflow (outflow) associated with the acquisition & disposal of long-lived; physical & intangible assets that are used in the normal conduct of business to produce goods and services and are not intended for resale. Includes cash inflows/outflows to pay for construction of self-constructed assets & software. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	CashAndEquivalents                      int64   // 14 = cashneq (currency)               [Balance Sheet] A component of [Assets] representing the amount of currency on hand as well as demand deposits with banks or financial institutions. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	CashAndEquivalentsUSD                   int64   // 15 = cashnequsd (USD)                 [Balance Sheet] [CashnEq] in USD; converted by [FXUSD]. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	CostOfRevenue                           int64   // 16 = cor (currency)                   [Income Statement] The aggregate cost of goods produced and sold and services rendered during the reporting period. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	ConsolidatedIncome                      int64   // 17 = consolinc (currency)             [Income Statement] The portion of profit or loss for the period; net of income taxes; which is attributable to the consolidated entity; before the deduction of [NetIncNCI].
	CurrentRatio                            float64 // 18 = currentratio (ratio)             [Metrics] The ratio between [AssetsC] and [LiabilitiesC]; for companies that operate a classified balance sheet.
	DebtToEquityRatio                       float64 // 19 = de (ratio)                       [Metrics] Measures the ratio between [Liabilities] and [Equity].
	TotalDebt                               int64   // 20 = debt (currency)                  [Balance Sheet] A component of [Liabilities] representing the total amount of current and non-current debt owed. Includes secured and unsecured bonds issued; commercial paper; notes payable; credit facilities; lines of credit; capital lease obligations; operating lease obligations; and convertible notes. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	DebtCurrent                             int64   // 21 = debtc (currency)                 [Balance Sheet] The current portion of [Debt]; reported if the company operates a classified balance sheet that segments current and non-current liabilities. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	DebtNonCurrent                          int64   // 22 = debtnc (currency)                [Balance Sheet] The non-current portion of [Debt] reported if the company operates a classified balance sheet that segments current and non-current liabilities. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	TotalDebtUSD                            int64   // 23 = debtusd (USD)                    [Balance Sheet] [Debt] in USD; converted by [FXUSD]. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	DeferredRevenue                         int64   // 24 = deferredrev (currency)           [Balance Sheet] A component of [Liabilities] representing the carrying amount of consideration received or receivable on potential earnings that were not recognized as revenue; including sales; license fees; and royalties; but excluding interest income. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	DepreciationAmortizationAndAccretion    int64   // 25 = depamor (currency)               [Cash Flow Statement] A component of operating cash flow representing the aggregate net amount of depreciation; amortization; and accretion recognized during an accounting period. As a non-cash item; the net amount is added back to net income when calculating cash provided by or used in operations using the indirect method. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	Deposits                                int64   // 26 = deposits (currency)              [Balance Sheet] A component of [Liabilities] representing the total of all deposit liabilities held; including foreign and domestic; interest and noninterest bearing. May include demand deposits; saving deposits; Negotiable Order of Withdrawal and time deposits among others. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	DividendYield                           float64 // 27 = divyield (ratio)                 [Metrics] Dividend Yield measures the ratio between a company's [DPS] and its [Price]. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	DividendsPerBasicCommonShare            float64 // 28 = dps (USD/share)                  [Income Statement] Aggregate dividends declared during the period for each split-adjusted share of common stock outstanding. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	EBIT                                    int64   // 29 = ebit (currency)                  [Income Statement] Earnings Before Interest and Tax is calculated by adding [TaxExp] and [IntExp] back to [NetInc].
	EBITDA                                  int64   // 30 = ebitda (currency)                [Metrics] EBITDA is a non-GAAP accounting metric that is widely used when assessing the performance of companies; calculated by adding [DepAmor] back to [EBIT].
	EBITDAMargin                            float64 // 31 = ebitdamargin (ratio)             [Metrics] Measures the ratio between a company's [EBITDA] and [Revenue].
	EBITDAUSD                               int64   // 32 = ebitdausd (USD)                  [Metrics] [EBITDA] in USD; converted by [FXUSD].
	EBITUSD                                 int64   // 33 = ebitusd (USD)                    [Income Statement] [EBIT] in USD; converted by [FXUSD].
	EBT                                     int64   // 34 = ebt (currency)                   [Metrics] Earnings Before Tax is calculated by adding [TaxExp] back to [NetInc].
	EPS                                     float64 // 35 = eps (currency/share)             [Income Statement] Earnings per share as calculated and reported by the company. Approximates to the amount of [NetIncCmn] for the period per each [SharesWA] after adjusting for [ShareFactor].
	EPSDiluted                              float64 // 36 = epsdil (currency/share)          [Income Statement] Earnings per diluted share as calculated and reported by the company. Approximates to the amount of [NetIncCmn] for the period per each [SharesWADil] after adjusting for [ShareFactor]..
	EPSUSD                                  float64 // 37 = epsusd (USD/share)               [Income Statement] [EPS] in USD; converted by [FXUSD].
	Equity                                  int64   // 38 = equity (currency)                [Balance Sheet] A principal component of the balance sheet; in addition to [Liabilities] and [Assets]; that represents the total of all stockholders' equity (deficit) items; net of receivables from officers; directors; owners; and affiliates of the entity which are attributable to the parent.
	EquityAvg                               int64   // 39 = equityavg (currency)             [Metrics] Average equity value for the period used in calculation of [ROE]; derived from [Equity].
	EquityUSD                               int64   // 40 = equityusd (USD)                  [Balance Sheet] [Equity] in USD; converted by [FXUSD].
	EnterpriseValue                         int64   // 41 = ev (USD)                         [Metrics] Enterprise value is a measure of the value of a business as a whole; calculated as [MarketCap] plus [DebtUSD] minus [CashnEqUSD].
	EVtoEBIT                                int64   // 42 = evebit (ratio)                   [Metrics] Measures the ratio between [EV] and [EBITUSD].
	EVtoEBITDA                              float64 // 43 = evebitda (ratio)                 [Metrics] Measures the ratio between [EV] and [EBITDAUSD].
	FreeCashFlow                            int64   // 44 = fcf (currency)                   [Metrics] Free Cash Flow is a measure of financial performance calculated as [NCFO] minus [CapEx].
	FreeCashFlowPerShare                    float64 // 45 = fcfps (currency/share)           [Metrics] Free Cash Flow per Share is a valuation metric calculated by dividing [FCF] by [SharesWA] and [ShareFactor].
	FxUSD                                   float64 // 46 = fxusd (ratio)                    [Metrics] The exchange rate used for the conversion of foreign currency to USD for non-US companies that do not report in USD.
	GrossProfit                             int64   // 47 = gp (currency)                    [Income Statement] Aggregate revenue [Revenue] less cost of revenue [CoR] directly attributable to the revenue generation activity.
	GrossMargin                             float64 // 48 = grossmargin (ratio)              [Metrics] Gross Margin measures the ratio between a company's [GP] and [Revenue].
	Intangibles                             int64   // 49 = intangibles (currency)           [Balance Sheet] A component of [Assets] representing the carrying amounts of all intangible assets and goodwill as of the balance sheet date; net of accumulated amortization and impairment charges. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	InterestExpense                         int64   // 50 = intexp (currency)                [Income Statement] Amount of the cost of borrowed funds accounted for as interest expense. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	InvestedCapital                         int64   // 51 = invcap (currency)                [Metrics] Invested capital is an input into the calculation of [ROIC]; and is calculated as: [Debt] plus [Assets] minus [Intangibles] minus [CashnEq] minus [LiabilitiesC]. Please note this calculation method is subject to change.
	InvestedCapitalAverage                  int64   // 52 = invcapavg (currency)             [Metrics] Average invested capital value for the period used in the calculation of [ROIC]; and derived from [InvCap]. Invested capital is an input into the calculation of [ROIC]; and is calculated as: [Debt] plus [Assets] minus [Intangibles] minus [CashnEq] minus [LiabilitiesC]. Please note this calculation method is subject to change.
	Inventory                               int64   // 53 = inventory (currency)             [Balance Sheet] A component of [Assets] representing the amount after valuation and reserves of inventory expected to be sold; or consumed within one year or operating cycle; if longer. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	Investments                             int64   // 54 = investments (currency)           [Balance Sheet] A component of [Assets] representing the total amount of marketable and non-marketable securties; loans receivable and other invested assets. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	InvestmentsCurrent                      int64   // 55 = investmentsc (currency)          [Balance Sheet] The current portion of [Investments]; reported if the company operates a classified balance sheet that segments current and non-current assets. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	InvestmentsNonCurrent                   int64   // 56 = investmentsnc (currency)         [Balance Sheet] The non-current portion of [Investments]; reported if the company operates a classified balance sheet that segments current and non-current assets. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	TotalLiabilities                        int64   // 57 = liabilities (currency)           [Balance Sheet] Sum of the carrying amounts as of the balance sheet date of all liabilities that are recognized. Principal components are [Debt]; [DeferredRev]; [Payables];[Deposits]; and [TaxLiabilities].
	CurrentLiabilities                      int64   // 58 = liabilitiesc (currency)          [Balance Sheet] The current portion of [Liabilities]; reported if the company operates a classified balance sheet that segments current and non-current liabilities.
	LiabilitiesNonCurrent                   int64   // 59 = liabilitiesnc (currency)         [Balance Sheet] The non-current portion of [Liabilities]; reported if the company operates a classified balance sheet that segments current and non-current liabilities.
	MarketCapitalization                    int64   // 60 = marketcap (USD)                  [Metrics] Represents the product of [SharesBas]; [Price] and [ShareFactor].
	NetCashFlow                             int64   // 61 = ncf (currency)                   [Cash Flow Statement] Principal component of the cash flow statement representing the amount of increase (decrease) in cash and cash equivalents. Includes [NCFO]; investing [NCFI] and financing [NCFF] for continuing and discontinued operations; and the effect of exchange rate changes on cash [NCFX].
	NetCashFlowBusiness                     int64   // 62 = ncfbus (currency)                [Cash Flow Statement] A component of [NCFI] representing the net cash inflow (outflow) associated with the acquisition & disposal of businesses; joint-ventures; affiliates; and other named investments. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	NetCashFlowCommon                       int64   // 63 = ncfcommon (currency)             [Cash Flow Statement] A component of [NCFF] representing the net cash inflow (outflow) from common equity changes. Includes additional capital contributions from share issuances and exercise of stock options; and outflow from share repurchases.  Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	NetCashFlowDebt                         int64   // 64 = ncfdebt (currency)               [Cash Flow Statement] A component of [NCFF] representing the net cash inflow (outflow) from issuance (repayment) of debt securities. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	NetCashFlowDividend                     int64   // 65 = ncfdiv (currency)                [Cash Flow Statement] A component of [NCFF] representing dividends and dividend equivalents paid on common stock and restricted stock units. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	NetCashFlowFromFinancing                int64   // 66 = ncff (currency)                  [Cash Flow Statement] A component of [NCF] representing the amount of cash inflow (outflow) from financing activities; from continuing and discontinued operations. Principal components of financing cash flow are: issuance (purchase) of equity shares; issuance (repayment) of debt securities; and payment of dividends & other cash distributions. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	NetCashFlowFromInvesting                int64   // 67 = ncfi (currency)                  [Cash Flow Statement] A component of [NCF] representing the amount of cash inflow (outflow) from investing activities; from continuing and discontinued operations. Principal components of investing cash flow are: capital (expenditure) disposal of equipment [CapEx]; business (acquisitions) disposition [NCFBus] and investment (acquisition) disposal [NCFInv]. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	NetCashFlowInvest                       int64   // 68 = ncfinv (currency)                [Cash Flow Statement] A component of [NCFI] representing the net cash inflow (outflow) associated with the acquisition & disposal of investments; including marketable securities and loan originations. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	NetCashFlowFromOperations               int64   // 69 = ncfo (currency)                  [Cash Flow Statement] A component of [NCF] representing the amount of cash inflow (outflow) from operating activities; from continuing and discontinued operations.
	NetCashFlowFx                           int64   // 70 = ncfx (currency)                  [Cash Flow Statement] A component of Net Cash Flow [NCF] representing the amount of increase (decrease) from the effect of exchange rate changes on cash and cash equivalent balances held in foreign currencies. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	NetIncome                               int64   // 71 = netinc (currency)                [Income Statement] The portion of profit or loss for the period; net of income taxes; which is attributable to the parent after the deduction of [NetIncNCI] from [ConsolInc]; and before the deduction of [PrefDivIS].
	NetIncomeCommonStock                    int64   // 72 = netinccmn (currency)             [Income Statement] The amount of net income (loss) for the period due to common shareholders. Typically differs from [NetInc] to the parent entity due to the deduction of [PrefDivIS].
	NetIncomeCommonStockUSD                 int64   // 73 = netinccmnusd (USD)               [Income Statement] [NetIncCmn] in USD; converted by [FXUSD].
	NetLossIncomeDiscontinuedOperations     int64   // 74 = netincdis (currency)             [Income Statement] Amount of loss (income) from a disposal group; net of income tax; reported as a separate component of income. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	NetIncomeToNonControllingInterests      int64   // 75 = netincnci (currency)             [Income Statement] The portion of income which is attributable to non-controlling interest shareholders; subtracted from [ConsolInc] in order to obtain [NetInc]. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	ProfitMargin                            float64 // 76 = netmargin (ratio)                [Metrics] Measures the ratio between a company's [NetIncCmn] and [Revenue].
	OperatingExpenses                       int64   // 77 = opex (currency)                  [Income Statement] Operating expenses represent the total expenditure on [SGnA]; [RnD] and other operating expense items; it excludes [CoR].
	OperatingIncome                         int64   // 78 = opinc (currency)                 [Income Statement] Operating income is a measure of financial performance before the deduction of [IntExp]; [TaxExp] and other Non-Operating items. It is calculated as [GP] minus [OpEx].
	Payables                                int64   // 79 = payables (currency)              [Balance Sheet] A component of [Liabilities] representing trade and non-trade payables. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	PayoutRatio                             float64 // 80 = payoutratio (ratio)              [Metrics] The percentage of earnings paid as dividends to common stockholders. Calculated by dividing [DPS] by [EPSUSD].
	PB                                      float64 // 81 = pb (ratio)                       [Metrics] Measures the ratio between [MarketCap] and [EquityUSD].
	PE                                      float64 // 82 = pe (ratio)                       [Metrics] Measures the ratio between [MarketCap] and [NetIncCmnUSD]
	PE1                                     float64 // 83 = pe1 (ratio)                      [Metrics] An alternative to [PE] representing the ratio between [Price] and [EPSUSD].
	PropertyPlantAndEquipmentNet            int64   // 84 = ppnenet (currency)               [Balance Sheet] A component of [Assets] representing the amount after accumulated depreciation; depletion and amortization of physical assets used in the normal conduct of business to produce goods and services and not intended for resale. Includes Operating Right of Use Assets. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	PreferredDividendsIncomeStatementImpact int64   // 85 = prefdivis (currency)             [Income Statement] Income statement item reflecting dividend payments to preferred stockholders. Subtracted from Net Income to Parent [NetInc] to obtain Net Income to Common Stockholders [NetIncCmn]. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	Price                                   float64 // 86 = price (USD/share)                [Entity] The price per common share adjusted for stock splits but not adjusted for dividends; used in the computation of [PE1]; [PS1]; [DivYield] and [SPS].
	PS                                      float64 // 87 = ps (ratio)                       [Metrics] Measures the ratio between [MarketCap] and [RevenueUSD].
	PS1                                     float64 // 88 = ps1 (ratio)                      [Metrics] An alternative calculation method to [PS]; that measures the ratio between a company's [Price] and it's [SPS].
	Receivables                             int64   // 89 = receivables (currency)           [Balance Sheet] A component of [Assets] representing trade and non-trade receivables. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	AccumulatedRetainedEarningsDeficit      int64   // 90 = retearn (currency)               [Balance Sheet] A component of [Equity] representing the cumulative amount of the entities undistributed earnings or deficit. May only be reported annually by certain companies; rather than quarterly.
	Revenues                                int64   // 91 = revenue (currency)               [Income Statement] The amount of Revenue recognised from goods sold; services rendered; insurance premiums; or other activities that constitute an earning process. Interest income for financial institutions is reported net of interest expense and provision for credit losses. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	RevenuesUSD                             int64   // 92 = revenueusd (USD)                 [Income Statement] [Revenue] in USD; converted by [FXUSD]. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	RandDExpenses                           int64   // 93 = rnd (currency)                   [Income Statement] A component of [OpEx] representing the aggregate costs incurred in a planned search or critical investigation aimed at discovery of new knowledge with the hope that such knowledge will be useful in developing a new product or service. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	ROA                                     float64 // 94 = roa (ratio)                      [Metrics] Return on assets measures how profitable a company is [NetIncCmn] relative to its total assets [AssetsAvg].
	ROE                                     float64 // 95 = roe (ratio)                      [Metrics] Return on equity measures a corporation's profitability by calculating the amount of [NetIncCmn] returned as a percentage of [EquityAvg].
	ROIC                                    float64 // 96 = roic (ratio)                     [Metrics] Return on Invested Capital is a ratio estimated by dividing [EBIT] by [InvCapAvg]. [InvCap] is calculated as: [Debt] plus [Assets] minus [Intangibles] minus [CashnEq] minus [LiabilitiesC]. Please note this calculation method is subject to change.
	ReturnOnSales                           float64 // 97 = ros (ratio)                      [Metrics] Return on Sales is a ratio to evaluate a company's operational efficiency; calculated by dividing [EBIT] by [Revenue]. ROS is often a component of DuPont ROE analysis.
	ShareBasedCompensation                  int64   // 98 = sbcomp (currency)                [Cash Flow Statement] A component of [NCFO] representing the total amount of noncash; equity-based employee remuneration. This may include the value of stock or unit options; amortization of restricted stock or units; and adjustment for officers' compensation. As noncash; this element is an add back when calculating net cash generated by operating activities using the indirect method.
	SellingGeneralAndAdministrativeExpense  int64   // 99 = sgna (currency)                  [Income Statement] A component of [OpEx] representing the aggregate total costs related to selling a firm's product and services; as well as all other general and administrative expenses. Direct selling expenses (for example; credit; warranty; and advertising) are expenses that can be directly linked to the sale of specific products. Indirect selling expenses are expenses that cannot be directly linked to the sale of specific products; for example telephone expenses; Internet; and postal charges. General and administrative expenses include salaries of non-sales personnel; rent; utilities; communication; etc. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	ShareFactor                             float64 // 100 = sharefactor (ratio)             [Entity] Share factor is a multiplicant in the calculation of [MarketCap] and is used to adjust for: American Depository Receipts (ADRs) that represent more or less than 1 underlying share; and; companies which have different earnings share for different share classes (eg Berkshire Hathaway - BRK.B).
	SharesBasic                             int64   // 101 = sharesbas (units)               [Entity] The number of shares or other units outstanding of the entity's capital or common stock or other ownership interests; as stated on the cover of related periodic report (10-K/10-Q); after adjustment for stock splits.
	WeightedAverageShares                   int64   // 102 = shareswa (units)                [Income Statement] The weighted average number of shares or units issued and outstanding that are used by the company to calculate [EPS]; determined based on the timing of issuance of shares or units in the period.
	WeightedAverageSharesDiluted            int64   // 103 = shareswadil (units)             [Income Statement] The weighted average number of shares or units issued and outstanding that are used by the company to calculate [EPSDil]; determined based on the timing of issuance of shares or units in the period.
	SalesPerShare                           float64 // 104 = sps (USD/share)                 [Metrics] Sales per Share measures the ratio between [RevenueUSD] and [SharesWA] as adjusted by [ShareFactor].
	TangibleAssetValue                      int64   // 105 = tangibles (currency)            [Metrics] The value of tangibles assets calculated as the difference between [Assets] and [Intangibles].
	TaxAssets                               int64   // 106 = taxassets (currency)            [Balance Sheet] A component of [Assets] representing tax assets and receivables. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	IncomeTaxExpense                        int64   // 107 = taxexp (currency)               [Income Statement] Amount of current income tax expense (benefit) and deferred income tax expense (benefit) pertaining to continuing operations. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	TaxLiabilities                          int64   // 108 = taxliabilities (currency)       [Balance Sheet] A component of [Liabilities] representing outstanding tax liabilities. Where this item is not contained on the company consolidated financial statements and cannot otherwise be imputed the value of 0 is used.
	TangibleAssetsBookValuePerShare         float64 // 109 = tbvps (currency/share)          [Metrics] Measures the ratio between [Tangibles] and [SharesWA] as adjusted by [ShareFactor].
	WorkingCapital                          int64   // 110 = workingcapital (currency)       [Metrics] Working capital measures the difference between [AssetsC] and [LiabilitiesC].
}

func downloadAllSharadarFundamentals(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
	logger := zerolog.Ctx(ctx)

	runSummary := data.RunSummary{
		StartTime:        time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}

	numObs := 0

	defer func() {
		runSummary.EndTime = time.Now()

		runSummary.NumObservations = numObs
		if runSummary.Status != data.RunFailed {
			runSummary.Status = data.RunSuccess
		}

		exitNotification <- runSummary
	}()

	rateLimit, err := strconv.Atoi(subscription.Config["rateLimit"])
	if err != nil {
		logger.Error().Err(err).Str("configRateLimit", subscription.Config["rateLimit"]).Msg("could not convert rateLimit configuration parameter to an integer")

		rateLimit = 5000
	}

	if rateLimit <= 0 {
		rateLimit = 5000
	}

	limiter := rate.NewLimiter(rate.Limit(float64(rateLimit)/float64(61)), 1)

	client := resty.New().
		SetQueryParam("api_key", subscription.Config["apiKey"]).
		SetRetryCount(3).
		SetRetryWaitTime(5 * time.Second).
		SetRetryMaxWaitTime(30 * time.Second).
		AddRetryCondition(func(r *resty.Response, err error) bool {
			return err != nil || r.StatusCode() == 429 || r.StatusCode() >= 500
		}).
		SetTimeout(60 * time.Second)

	cursor := ""
	for {
		log.Info().Str("cursor", cursor).Msg("Fetching next page sharadar fundamentals")

		var n int

		cursor, n = downloadSharadarFundamentals(ctx, subscription, client, limiter, cursor, out)
		numObs += n

		if cursor == "" {
			break
		}
	}
}

func downloadSharadarFundamentals(ctx context.Context, subscription *library.Subscription, client *resty.Client, limiter *rate.Limiter, cursor string, out chan<- *data.Observation) (string, int) {
	logger := zerolog.Ctx(ctx)

	if err := limiter.Wait(ctx); err != nil {
		logger.Error().Err(err).Msg("rate limit wait failed")
		return "", 0
	}

	// Get a list of active assets
	conn, err := subscription.Library.AcquireWithTimeout(ctx)
	if err != nil {
		log.Panic().Msg("could not acquire database connection")
	}

	defer conn.Release()

	// FIGI resolution reads the unified `assets` view so ticker reuse
	// across listings resolves per fundamental event date. AllAssets
	// (not ActiveAssets) is required so since-delisted rows participate.
	assets, err := data.AllAssets(ctx, conn)
	if err != nil {
		logger.Error().Err(err).Msg("could not load assets")
		return "", 0
	}

	tickerFilter, figiFilter := provider.SecurityFilterFromContext(ctx)

	filtered := filterSharadarAssets(assets, tickerFilter, figiFilter)

	if (tickerFilter != "" || figiFilter != "") && len(filtered) == 0 {
		candidates := candidateLabels(assets, tickerFilter)

		input := tickerFilter
		if input == "" {
			input = figiFilter
		}

		suggestions := provider.SuggestMatch(input, candidates)
		if len(suggestions) > 0 {
			log.Error().Str("input", input).Strs("suggestions", suggestions).Msg("security not found in Sharadar universe; did you mean one of these?")
		} else {
			log.Error().Str("input", input).Msg("security not found in Sharadar universe")
		}

		return "", 0
	}

	history := data.NewAssetHistory(filtered)

	if tickerFilter != "" || figiFilter != "" {
		log.Info().Int("filtered_tickers", history.TickerCount()).Msg("applied security filter")
	}

	url := "https://data.nasdaq.com/api/v3/datatables/SHARADAR/SF1"

	req := client.R()
	if cursor != "" {
		req.SetQueryParam("qopts.cursor_id", cursor)
	}

	lastUpdated := time.Now().Add(-1 * time.Hour * 24 * 10)
	req.SetQueryParam("lastupdated.gte", lastUpdated.Format("2006-01-02"))

	resp, err := req.Get(url)
	if err != nil {
		logger.Error().Err(err).Msg("failed to download fundamentals")
		return "", 0
	}

	if resp.StatusCode() >= 400 {
		logger.Error().Int("StatusCode", resp.StatusCode()).Str("Url", url).Bytes("Body", resp.Body()).Msg("error when requesting url")
		return "", 0
	}

	logger.Debug().Str("URL", resp.Request.URL).Send()

	responseBody := string(resp.Body())
	result := gjson.Get(responseBody, "datatable.data")
	count := 0

	for _, val := range result.Array() {
		fundamental := &sharadarFundamental{
			Ticker:                                  val.Get("0").String(),
			Dimension:                               val.Get("1").String(),
			CalendarDate:                            val.Get("2").String(),
			DateKey:                                 val.Get("3").String(),
			ReportPeriod:                            val.Get("4").String(),
			LastUpdated:                             val.Get("5").String(),
			AccumulatedOtherComprehensiveIncome:     val.Get("6").Int(),
			TotalAssets:                             val.Get("7").Int(),
			AverageAssets:                           val.Get("8").Int(),
			CurrentAssets:                           val.Get("9").Int(),
			AssetsNonCurrent:                        val.Get("10").Int(),
			AssetTurnover:                           val.Get("11").Float(),
			BookValuePerShare:                       val.Get("12").Float(),
			CapitalExpenditure:                      val.Get("13").Int(),
			CashAndEquivalents:                      val.Get("14").Int(),
			CashAndEquivalentsUSD:                   val.Get("15").Int(),
			CostOfRevenue:                           val.Get("16").Int(),
			ConsolidatedIncome:                      val.Get("17").Int(),
			CurrentRatio:                            val.Get("18").Float(),
			DebtToEquityRatio:                       val.Get("19").Float(),
			TotalDebt:                               val.Get("20").Int(),
			DebtCurrent:                             val.Get("21").Int(),
			DebtNonCurrent:                          val.Get("22").Int(),
			TotalDebtUSD:                            val.Get("23").Int(),
			DeferredRevenue:                         val.Get("24").Int(),
			DepreciationAmortizationAndAccretion:    val.Get("25").Int(),
			Deposits:                                val.Get("26").Int(),
			DividendYield:                           val.Get("27").Float(),
			DividendsPerBasicCommonShare:            val.Get("28").Float(),
			EBIT:                                    val.Get("29").Int(),
			EBITDA:                                  val.Get("30").Int(),
			EBITDAMargin:                            val.Get("31").Float(),
			EBITDAUSD:                               val.Get("32").Int(),
			EBITUSD:                                 val.Get("33").Int(),
			EBT:                                     val.Get("34").Int(),
			EPS:                                     val.Get("35").Float(),
			EPSDiluted:                              val.Get("36").Float(),
			EPSUSD:                                  val.Get("37").Float(),
			Equity:                                  val.Get("38").Int(),
			EquityAvg:                               val.Get("39").Int(),
			EquityUSD:                               val.Get("40").Int(),
			EnterpriseValue:                         val.Get("41").Int(),
			EVtoEBIT:                                val.Get("42").Int(),
			EVtoEBITDA:                              val.Get("43").Float(),
			FreeCashFlow:                            val.Get("44").Int(),
			FreeCashFlowPerShare:                    val.Get("45").Float(),
			FxUSD:                                   val.Get("46").Float(),
			GrossProfit:                             val.Get("47").Int(),
			GrossMargin:                             val.Get("48").Float(),
			Intangibles:                             val.Get("49").Int(),
			InterestExpense:                         val.Get("50").Int(),
			InvestedCapital:                         val.Get("51").Int(),
			InvestedCapitalAverage:                  val.Get("52").Int(),
			Inventory:                               val.Get("53").Int(),
			Investments:                             val.Get("54").Int(),
			InvestmentsCurrent:                      val.Get("55").Int(),
			InvestmentsNonCurrent:                   val.Get("56").Int(),
			TotalLiabilities:                        val.Get("57").Int(),
			CurrentLiabilities:                      val.Get("58").Int(),
			LiabilitiesNonCurrent:                   val.Get("59").Int(),
			MarketCapitalization:                    val.Get("60").Int(),
			NetCashFlow:                             val.Get("61").Int(),
			NetCashFlowBusiness:                     val.Get("62").Int(),
			NetCashFlowCommon:                       val.Get("63").Int(),
			NetCashFlowDebt:                         val.Get("64").Int(),
			NetCashFlowDividend:                     val.Get("65").Int(),
			NetCashFlowFromFinancing:                val.Get("66").Int(),
			NetCashFlowFromInvesting:                val.Get("67").Int(),
			NetCashFlowInvest:                       val.Get("68").Int(),
			NetCashFlowFromOperations:               val.Get("69").Int(),
			NetCashFlowFx:                           val.Get("70").Int(),
			NetIncome:                               val.Get("71").Int(),
			NetIncomeCommonStock:                    val.Get("72").Int(),
			NetIncomeCommonStockUSD:                 val.Get("73").Int(),
			NetLossIncomeDiscontinuedOperations:     val.Get("74").Int(),
			NetIncomeToNonControllingInterests:      val.Get("75").Int(),
			ProfitMargin:                            val.Get("76").Float(),
			OperatingExpenses:                       val.Get("77").Int(),
			OperatingIncome:                         val.Get("78").Int(),
			Payables:                                val.Get("79").Int(),
			PayoutRatio:                             val.Get("80").Float(),
			PB:                                      val.Get("81").Float(),
			PE:                                      val.Get("82").Float(),
			PE1:                                     val.Get("83").Float(),
			PropertyPlantAndEquipmentNet:            val.Get("84").Int(),
			PreferredDividendsIncomeStatementImpact: val.Get("85").Int(),
			Price:                                   val.Get("86").Float(),
			PS:                                      val.Get("87").Float(),
			PS1:                                     val.Get("88").Float(),
			Receivables:                             val.Get("89").Int(),
			AccumulatedRetainedEarningsDeficit:      val.Get("90").Int(),
			Revenues:                                val.Get("91").Int(),
			RevenuesUSD:                             val.Get("92").Int(),
			RandDExpenses:                           val.Get("93").Int(),
			ROA:                                     val.Get("94").Float(),
			ROE:                                     val.Get("95").Float(),
			ROIC:                                    val.Get("96").Float(),
			ReturnOnSales:                           val.Get("97").Float(),
			ShareBasedCompensation:                  val.Get("98").Int(),
			SellingGeneralAndAdministrativeExpense:  val.Get("99").Int(),
			ShareFactor:                             val.Get("100").Float(),
			SharesBasic:                             val.Get("101").Int(),
			WeightedAverageShares:                   val.Get("102").Int(),
			WeightedAverageSharesDiluted:            val.Get("103").Int(),
			SalesPerShare:                           val.Get("104").Float(),
			TangibleAssetValue:                      val.Get("105").Int(),
			TaxAssets:                               val.Get("106").Int(),
			IncomeTaxExpense:                        val.Get("107").Int(),
			TaxLiabilities:                          val.Get("108").Int(),
			TangibleAssetsBookValuePerShare:         val.Get("109").Float(),
			WorkingCapital:                          val.Get("110").Int(),
		}

		// convert to pv asset type
		pvFundamental := fundamental.PvFundamental(history)

		out <- &data.Observation{
			Fundamental:      pvFundamental,
			ObservationDate:  time.Now(),
			SubscriptionID:   subscription.ID,
			SubscriptionName: subscription.Name,
		}

		count++
	}

	return gjson.Get(responseBody, "meta.next_cursor_id").String(), count
}

// PvFundamental converts the sharadar
func (fundamental *sharadarFundamental) PvFundamental(history *data.AssetHistory) *data.Fundamental {
	var err error

	// get nyc timezone
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		log.Panic().Err(err).Msg("could not load timezone")
		return nil
	}

	ff := &data.Fundamental{
		Ticker:                                  strings.ReplaceAll(fundamental.Ticker, ".", "/"),
		Dimension:                               fundamental.Dimension,
		AccumulatedOtherComprehensiveIncome:     fundamental.AccumulatedOtherComprehensiveIncome,
		TotalAssets:                             fundamental.TotalAssets,
		AverageAssets:                           fundamental.AverageAssets,
		CurrentAssets:                           fundamental.CurrentAssets,
		AssetsNonCurrent:                        fundamental.AssetsNonCurrent,
		AssetTurnover:                           fundamental.AssetTurnover,
		BookValuePerShare:                       fundamental.BookValuePerShare,
		CapitalExpenditure:                      fundamental.CapitalExpenditure,
		CashAndEquivalents:                      fundamental.CashAndEquivalents,
		CostOfRevenue:                           fundamental.CostOfRevenue,
		ConsolidatedIncome:                      fundamental.ConsolidatedIncome,
		CurrentRatio:                            fundamental.CurrentRatio,
		DebtToEquityRatio:                       fundamental.DebtToEquityRatio,
		TotalDebt:                               fundamental.TotalDebt,
		DebtCurrent:                             fundamental.DebtCurrent,
		DebtNonCurrent:                          fundamental.DebtNonCurrent,
		DeferredRevenue:                         fundamental.DeferredRevenue,
		DepreciationAmortizationAndAccretion:    fundamental.DepreciationAmortizationAndAccretion,
		Deposits:                                fundamental.Deposits,
		DividendYield:                           fundamental.DividendYield,
		DividendsPerBasicCommonShare:            fundamental.DividendsPerBasicCommonShare,
		EBIT:                                    fundamental.EBIT,
		EBITDA:                                  fundamental.EBITDA,
		EBITDAMargin:                            fundamental.EBITDAMargin,
		EBT:                                     fundamental.EBT,
		EPS:                                     fundamental.EPS,
		EPSDiluted:                              fundamental.EPSDiluted,
		Equity:                                  fundamental.Equity,
		EquityAvg:                               fundamental.EquityAvg,
		EnterpriseValue:                         fundamental.EnterpriseValue,
		EVtoEBIT:                                fundamental.EVtoEBIT,
		EVtoEBITDA:                              fundamental.EVtoEBITDA,
		FreeCashFlow:                            fundamental.FreeCashFlow,
		FreeCashFlowPerShare:                    fundamental.FreeCashFlowPerShare,
		FxUSD:                                   fundamental.FxUSD,
		GrossProfit:                             fundamental.GrossProfit,
		GrossMargin:                             fundamental.GrossMargin,
		Intangibles:                             fundamental.Intangibles,
		InterestExpense:                         fundamental.InterestExpense,
		InvestedCapital:                         fundamental.InvestedCapital,
		InvestedCapitalAverage:                  fundamental.InvestedCapitalAverage,
		Inventory:                               fundamental.Inventory,
		Investments:                             fundamental.Investments,
		InvestmentsCurrent:                      fundamental.InvestmentsCurrent,
		InvestmentsNonCurrent:                   fundamental.InvestmentsNonCurrent,
		TotalLiabilities:                        fundamental.TotalLiabilities,
		CurrentLiabilities:                      fundamental.CurrentLiabilities,
		LiabilitiesNonCurrent:                   fundamental.LiabilitiesNonCurrent,
		MarketCapitalization:                    fundamental.MarketCapitalization,
		NetCashFlow:                             fundamental.NetCashFlow,
		NetCashFlowBusiness:                     fundamental.NetCashFlowBusiness,
		NetCashFlowCommon:                       fundamental.NetCashFlowCommon,
		NetCashFlowDebt:                         fundamental.NetCashFlowDebt,
		NetCashFlowDividend:                     fundamental.NetCashFlowDividend,
		NetCashFlowFromFinancing:                fundamental.NetCashFlowFromFinancing,
		NetCashFlowFromInvesting:                fundamental.NetCashFlowFromInvesting,
		NetCashFlowInvest:                       fundamental.NetCashFlowInvest,
		NetCashFlowFromOperations:               fundamental.NetCashFlowFromOperations,
		NetCashFlowFx:                           fundamental.NetCashFlowFx,
		NetIncome:                               fundamental.NetIncome,
		NetIncomeCommonStock:                    fundamental.NetIncomeCommonStock,
		NetLossIncomeDiscontinuedOperations:     fundamental.NetLossIncomeDiscontinuedOperations,
		NetIncomeToNonControllingInterests:      fundamental.NetIncomeToNonControllingInterests,
		ProfitMargin:                            fundamental.ProfitMargin,
		OperatingExpenses:                       fundamental.OperatingExpenses,
		OperatingIncome:                         fundamental.OperatingIncome,
		Payables:                                fundamental.Payables,
		PayoutRatio:                             fundamental.PayoutRatio,
		PB:                                      fundamental.PB,
		PE:                                      fundamental.PE,
		PE1:                                     fundamental.PE1,
		PropertyPlantAndEquipmentNet:            fundamental.PropertyPlantAndEquipmentNet,
		PreferredDividendsIncomeStatementImpact: fundamental.PreferredDividendsIncomeStatementImpact,
		Price:                                   fundamental.Price,
		PS:                                      fundamental.PS,
		PS1:                                     fundamental.PS1,
		Receivables:                             fundamental.Receivables,
		AccumulatedRetainedEarningsDeficit:      fundamental.AccumulatedRetainedEarningsDeficit,
		Revenues:                                fundamental.Revenues,
		RandDExpenses:                           fundamental.RandDExpenses,
		ROA:                                     fundamental.ROA,
		ROE:                                     fundamental.ROE,
		ROIC:                                    fundamental.ROIC,
		ReturnOnSales:                           fundamental.ReturnOnSales,
		ShareBasedCompensation:                  fundamental.ShareBasedCompensation,
		SellingGeneralAndAdministrativeExpense:  fundamental.SellingGeneralAndAdministrativeExpense,
		ShareFactor:                             fundamental.ShareFactor,
		SharesBasic:                             fundamental.SharesBasic,
		WeightedAverageShares:                   fundamental.WeightedAverageShares,
		WeightedAverageSharesDiluted:            fundamental.WeightedAverageSharesDiluted,
		SalesPerShare:                           fundamental.SalesPerShare,
		TangibleAssetValue:                      fundamental.TangibleAssetValue,
		TaxAssets:                               fundamental.TaxAssets,
		IncomeTaxExpense:                        fundamental.IncomeTaxExpense,
		TaxLiabilities:                          fundamental.TaxLiabilities,
		TangibleAssetsBookValuePerShare:         fundamental.TangibleAssetsBookValuePerShare,
		WorkingCapital:                          fundamental.WorkingCapital,
	}

	if fundamental.DateKey != "" {
		ff.EventDate, err = time.Parse("2006-01-02", fundamental.DateKey)
		if err != nil {
			log.Error().Err(err).Str("DateKey", fundamental.DateKey).Msg("could not parse date")
			return nil
		}

		ff.EventDate = time.Date(ff.EventDate.Year(), ff.EventDate.Month(), ff.EventDate.Day(), 0, 0, 0, 0, nyc)
	}

	if fundamental.CalendarDate != "" {
		ff.DateKey, err = time.Parse("2006-01-02", fundamental.CalendarDate)
		if err != nil {
			log.Error().Err(err).Str("CalendarDate", fundamental.CalendarDate).Msg("could not parse date")
			return nil
		}

		ff.DateKey = time.Date(ff.DateKey.Year(), ff.DateKey.Month(), ff.DateKey.Day(), 0, 0, 0, 0, nyc)
	}

	if fundamental.ReportPeriod != "" {
		ff.ReportPeriod, err = time.Parse("2006-01-02", fundamental.ReportPeriod)
		if err != nil {
			log.Error().Err(err).Str("ReportPeriod", fundamental.CalendarDate).Msg("could not parse date")
			return nil
		}

		ff.ReportPeriod = time.Date(ff.ReportPeriod.Year(), ff.ReportPeriod.Month(), ff.ReportPeriod.Day(), 0, 0, 0, 0, nyc)
	}

	if fundamental.LastUpdated != "" {
		ff.LastUpdated, err = time.Parse("2006-01-02", fundamental.LastUpdated)
		if err != nil {
			log.Error().Err(err).Str("LastUpdated", fundamental.CalendarDate).Msg("could not parse date")

			ff.LastUpdated = time.Now().In(nyc)
		}

		ff.LastUpdated = time.Date(ff.LastUpdated.Year(), ff.LastUpdated.Month(), ff.LastUpdated.Day(), 0, 0, 0, 0, nyc)
	}

	// Date-aware FIGI lookup against the unified asset history. Falls
	// back to empty when no window covers the event date, matching the
	// previous snapshot-miss semantics.
	if figi, ok := history.FIGIAt(ff.Ticker, ff.EventDate); ok {
		ff.CompositeFigi = figi
	}

	return ff
}
