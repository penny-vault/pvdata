# Zacks Field Analysis

Analysis of fields available in Zacks fundamentals data (ZACKS-FC and ZACKS-FE tables) that are not currently tracked in the `Fundamental` struct. The existing struct has 97 numeric fields. Zacks FC has 249 columns and FE has 635 columns.

## Data Sources

- **ZACKS-FC** -- Fundamentals Combined: income statement, balance sheet, cash flow in a single table (~190 financial columns after stripping metadata)
- **ZACKS-FE** -- Fundamentals Extended: same data with far more granular line items, industry-specific fields, and pro-forma EPS adjustments (~550 financial columns)

## Candidate Fields

### Normalized / Adjusted Earnings

Zacks pre-computes earnings adjusted for non-recurring items. We currently have no equivalent.

| Zacks Field | Description | Source |
|-------------|-------------|--------|
| `norm_pre_tax_income` | Pre-tax income excluding non-recurring items | FC |
| `norm_aft_tax_income` | After-tax income excluding non-recurring items | FC |
| `eps_diluted_street_def` | Diluted EPS per street/analyst definition | FE |
| `eps_diluted_bnri` | Diluted EPS before non-recurring items | FE |

The FE file also provides per-share adjustment line items used to arrive at street EPS:
`acq`, `amort_intang_goodwill`, `gain_loss_sale_asset_per_share`, `gain_loss_sale_invst_per_share`, `asset_wdown_impair_per_share`, `charge_reversal`, `fgn_currency_gain_loss`, `in_proc_res_dev_exp_per_share`, `litig_per_share`, `merger_acq_income_per_share`, `restruct_gain_loss`, `severance_exp`, `startup_cost`, `write_off`, `tot_pro_forma_adj`.

### FFO (Funds From Operations)

Critical REIT valuation metric. Completely absent from our data.

| Zacks Field | Description | Source |
|-------------|-------------|--------|
| `ffo` | Funds from operations | FE |
| `ffo_per_share_diluted` | FFO per diluted share | FE |

### Non-Recurring / Special Items

Individual line items that are currently lumped into operating or non-operating totals.

| Zacks Field | Description | Source |
|-------------|-------------|--------|
| `asset_wdown_impair_aggr` | Asset writedowns and impairments (non-goodwill) | FC |
| `impair_goodwill` | Goodwill impairment | FC |
| `restruct_charge` | Restructuring charges | FC |
| `merger_acq_income_aggr` | Merger/acquisition related income or expense | FC |
| `litig_aggr` | Litigation charges or settlements | FC |
| `gain_loss_sale_asset_aggr` | Gain/loss on sale of assets | FC |
| `gain_loss_sale_invst_aggr` | Gain/loss on sale of investments | FC |
| `spcl_unusual_charge` | Special or unusual charges | FC |
| `income_loss_equity_invst_other` | Income/loss from equity method investments | FC |

### Goodwill vs Intangibles

We store these combined as `intangibles`. Zacks FE separates them.

| Zacks Field | Description | Source |
|-------------|-------------|--------|
| `goodwill` | Goodwill only | FE |
| `intang_asset` | Intangible assets excluding goodwill | FE |

### Employee Data

| Zacks Field | Description | Source |
|-------------|-------------|--------|
| `emp_cnt` | Total employee count | FC |
| `emp_ft_cnt` | Full-time employees | FC |
| `emp_pt_cnt` | Part-time employees | FC |

### Cash Flow Decomposition

We carry net amounts for several cash flow items. Zacks FE provides gross components.

| Zacks Field | Description | Source |
|-------------|-------------|--------|
| `cap_expense` | Gross capital expenditure (purchases of PP&E) | FE |
| `sale_prop_plant_equip` | Proceeds from sale of PP&E | FE |
| `acq_cash_flow` | Cash spent on acquisitions | FE |
| `divst` | Cash received from divestitures | FE |
| `lterm_debt_issued` | Gross long-term debt issuance | FE |
| `lterm_debt_repaid` | Gross long-term debt repayment | FE |
| `sterm_debt_issued` | Gross short-term debt issuance | FE |
| `sterm_debt_repaid` | Gross short-term debt repayment | FE |
| `comm_shares_issued` | Gross common share issuance | FE |
| `comm_shares_repurch` | Gross common share repurchases | FE |

### D&A Decomposition

We store total D&A. Zacks FE breaks it into components.

| Zacks Field | Description | Source |
|-------------|-------------|--------|
| `deprec_cash_flow` | Depreciation (from cash flow statement) | FE |
| `amort_goodwill` | Amortization of goodwill | FE |
| `amort_intang_asset` | Amortization of intangible assets | FE |
| `amort_def_charge_cash_flow` | Amortization of deferred charges | FE |

### Tax Detail

We only have total `income_tax_expense`. Zacks FE has a jurisdiction breakdown.

| Zacks Field | Description | Source |
|-------------|-------------|--------|
| `income_tax_cr` | Income tax credits | FE |
| `def_tax` | Deferred tax expense/benefit | FE |
| `fed_income_tax` | Federal income tax | FE |
| `state_income_tax` | State income tax | FE |
| `fgn_income_tax` | Foreign income tax | FE |

### Income Statement Detail

| Zacks Field | Description | Source |
|-------------|-------------|--------|
| `stock_compsn_exp` | Stock compensation on income statement | FE |
| `advert_exp` | Advertising expense | FE |
| `sal_compsn_labor_exp` | Salary/compensation/labor expense | FE |
| `int_invst_income_oper` | Interest/investment income from operations | FC |
| `pension_post_retire_exp` | Pension/post-retirement expense | FC |
| `rental_exp_ind_broker` | Rental/lease expense | FC |
| `dilution_factor` | Ratio of basic to diluted shares | FC |

### Balance Sheet Detail

| Zacks Field | Description | Source |
|-------------|-------------|--------|
| `gross_prop_plant_equip` | Gross PP&E before depreciation | FC |
| `tot_accum_deprec` | Accumulated depreciation | FC |
| `prepaid_expense` | Prepaid expenses | FC |
| `pension_post_retire_asset` | Pension/post-retirement assets | FC |
| `pension_post_retire_liab` | Pension/post-retirement liabilities | FC |
| `conv_debt` | Convertible debt | FE |
| `non_curr_cap_lease` | Non-current capital/finance lease obligations | FE |
| `accrued_exp` | Accrued expenses | FC |
| `note_pay` | Notes payable | FC |
| `div_per_share_comm_stock` | Regular dividends per share | FE |
| `spcl_div_per_share_comm_stock` | Special dividends per share | FE |
| `adr_share_ratio` | ADR share ratio (international companies) | FE |

### Inventory Decomposition (FE only)

| Zacks Field | Description | Source |
|-------------|-------------|--------|
| `raw_material` | Raw materials inventory | FE |
| `wip` | Work-in-progress inventory | FE |
| `finished_good` | Finished goods inventory | FE |

### Banking / Financial Sector (FE only)

These fields are required to properly analyze banks and financial institutions (~15% of public companies). None are currently tracked.

| Zacks Field | Description |
|-------------|-------------|
| `int_income_tot` | Total interest income |
| `tot_int_expense` | Total interest expense |
| `net_int_income` | Net interest income |
| `provsn_loan_cr_loss` | Provision for loan/credit losses |
| `net_int_income_aft_loan_loss` | Net interest income after provisions |
| `tot_non_int_income` | Total non-interest income |
| `gross_loan_made` | Gross loans |
| `loan_loss_allow` | Loan loss allowance |
| `net_loan` | Net loans |
| `non_perform_loan` | Non-performing loans |
| `non_perform_asset` | Non-performing assets |
| `tot_dep` | Total deposits |
| `cust_dep_int_bearing_dom` | Interest-bearing domestic deposits |
| `cust_dep_non_int_bearing_dom` | Non-interest-bearing domestic deposits |

### Insurance Sector (FE only)

Required for insurance companies. None are currently tracked.

| Zacks Field | Description |
|-------------|-------------|
| `gross_prem_written` | Gross premiums written |
| `net_prem_earned` | Net premiums earned |
| `bene_policy_holder_claim` | Benefits and policyholder claims |
| `loss_expense` | Loss expense |
| `policy_acq_underwriting_cost` | Policy acquisition/underwriting costs |
| `policy_bene_loss_reserve` | Policy benefit/loss reserves |
| `unearned_prem_reserve` | Unearned premium reserves |

## What Would NOT Be Worth Adding

- **PP&E gross breakdown** (land, buildings, machinery, etc.) -- Too granular, rarely used in quantitative analysis.
- **Detailed equity decomposition** (additional paid-in capital, treasury stock, deferred compensation) -- Low analytical value, already captured in equity total.
- **Preferred stock decomposition** (redeemable, non-redeemable, convertible) -- Edge case for most companies.
- **Per-share EPS variants** (basic/diluted for continuing ops, discontinued ops, extraordinary items, accounting changes) -- Excessive granularity; street EPS and GAAP EPS cover the important cases.
- **Industry-specific revenue breakdown** (utilities: electric/gas/water; banking: fee types) -- Too specialized for a general-purpose struct.
