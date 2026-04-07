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

import (
	"sort"
	"time"

	"github.com/penny-vault/pvdata/data"
)

// Period represents a unique reporting period identified from CompanyFacts.
type Period struct {
	PeriodEnd   time.Time // End date of the fiscal period
	FormType    string    // "10-K" or "10-Q"
	ARFiledDate time.Time // Earliest filing date for this period (As Reported)
	MRFiledDate time.Time // Latest filing date for this period (Most Recent Reported)
}

// IdentifyPeriods scans all facts in a CompanyFacts to find unique reporting periods
// and their earliest/latest filing dates.
func IdentifyPeriods(cf *CompanyFacts) []Period {
	type periodKey struct {
		end  time.Time
		form string
	}

	periodMap := make(map[periodKey]*Period)

	for _, facts := range cf.Facts {
		for _, f := range facts {
			key := periodKey{end: f.End, form: f.Form}

			p, exists := periodMap[key]
			if !exists {
				p = &Period{
					PeriodEnd:   f.End,
					FormType:    f.Form,
					ARFiledDate: f.Filed,
					MRFiledDate: f.Filed,
				}
				periodMap[key] = p
			} else {
				if f.Filed.Before(p.ARFiledDate) {
					p.ARFiledDate = f.Filed
				}

				if f.Filed.After(p.MRFiledDate) {
					p.MRFiledDate = f.Filed
				}
			}
		}
	}

	periods := make([]Period, 0, len(periodMap))
	for _, p := range periodMap {
		periods = append(periods, *p)
	}

	sort.Slice(periods, func(i, j int) bool {
		return periods[i].PeriodEnd.Before(periods[j].PeriodEnd)
	})

	return periods
}

// NormalizeEventDate converts a raw period end date to a normalized calendar date,
// matching Sharadar's EventDate convention:
// - Quarterly: snaps to the nearest calendar quarter end (3/31, 6/30, 9/30, 12/31)
// - Annual: snaps to the calendar year end (12/31)
func NormalizeEventDate(periodEnd time.Time, formType string) time.Time {
	if formType == "10-K" {
		return time.Date(periodEnd.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
	}

	// Quarterly: snap to nearest quarter end
	month := periodEnd.Month()
	switch {
	case month <= 3:
		return time.Date(periodEnd.Year(), 3, 31, 0, 0, 0, 0, time.UTC)
	case month <= 6:
		return time.Date(periodEnd.Year(), 6, 30, 0, 0, 0, 0, time.UTC)
	case month <= 9:
		return time.Date(periodEnd.Year(), 9, 30, 0, 0, 0, 0, time.UTC)
	default:
		return time.Date(periodEnd.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
	}
}

// ResolveFieldsForFiling resolves all fields using only facts filed on or before
// a specific date. This allows producing AR (earliest filed) vs MR (latest filed)
// views of the same period.
func ResolveFieldsForFiling(cf *CompanyFacts, periodEnd time.Time, formType string, filedDate time.Time) map[string]float64 {
	// Build a filtered CompanyFacts containing only facts filed on or before filedDate
	filtered := &CompanyFacts{
		CIK:        cf.CIK,
		EntityName: cf.EntityName,
		Facts:      make(map[string][]Fact),
	}

	for concept, facts := range cf.Facts {
		var kept []Fact

		for _, f := range facts {
			if !f.Filed.After(filedDate) {
				kept = append(kept, f)
			}
		}

		if len(kept) > 0 {
			filtered.Facts[concept] = kept
		}
	}

	return ResolveAllFields(filtered, periodEnd, formType)
}

// ComputeTTM computes trailing twelve month values from the 4 most recent quarterly
// resolved field sets. Flow items are summed; point-in-time items use the latest value.
func ComputeTTM(quarters []map[string]float64) map[string]float64 {
	if len(quarters) < 4 {
		return nil
	}

	// Use the 4 most recent quarters
	recent := quarters[len(quarters)-4:]
	result := make(map[string]float64)

	for _, m := range FieldMappings {
		switch m.StatementType {
		case StmtFlow:
			// Sum all 4 quarters
			sum := 0.0
			found := 0

			for _, q := range recent {
				if v, ok := q[m.FieldName]; ok {
					sum += v
					found++
				}
			}

			if found == 4 {
				result[m.FieldName] = sum
			}

		case StmtPointInTime:
			// Use the latest quarter's value
			if v, ok := recent[3][m.FieldName]; ok {
				result[m.FieldName] = v
			}

		case StmtMetric:
			// Recompute from TTM values (will be done after flow/point-in-time)
		}
	}

	// Recompute derived metrics from TTM values
	for _, m := range FieldMappings {
		if m.Type == MappingDerived && m.StatementType == StmtMetric {
			if val, ok := computeDerived(m, result); ok {
				result[m.FieldName] = val
			}
		}
	}

	return result
}

// BuildFundamental converts a resolved field map into a data.Fundamental struct.
func BuildFundamental(fields map[string]float64, ticker, compositeFigi, dimension string, eventDate, dateKey, reportPeriod time.Time) *data.Fundamental {
	f := &data.Fundamental{
		EventDate:     eventDate,
		Ticker:        ticker,
		CompositeFigi: compositeFigi,
		Dimension:     dimension,
		DateKey:       dateKey,
		ReportPeriod:  reportPeriod,
		LastUpdated:   time.Now().UTC(),
	}

	// Map resolved values to Fundamental fields
	if v, ok := fields["TotalAssets"]; ok {
		f.TotalAssets = int64(v)
	}

	if v, ok := fields["CurrentAssets"]; ok {
		f.CurrentAssets = int64(v)
	}

	if v, ok := fields["AssetsNonCurrent"]; ok {
		f.AssetsNonCurrent = int64(v)
	}

	if v, ok := fields["CashAndEquivalents"]; ok {
		f.CashAndEquivalents = int64(v)
	}

	if v, ok := fields["Inventory"]; ok {
		f.Inventory = int64(v)
	}

	if v, ok := fields["Investments"]; ok {
		f.Investments = int64(v)
	}

	if v, ok := fields["InvestmentsCurrent"]; ok {
		f.InvestmentsCurrent = int64(v)
	}

	if v, ok := fields["InvestmentsNonCurrent"]; ok {
		f.InvestmentsNonCurrent = int64(v)
	}

	if v, ok := fields["Receivables"]; ok {
		f.Receivables = int64(v)
	}

	if v, ok := fields["Payables"]; ok {
		f.Payables = int64(v)
	}

	if v, ok := fields["Deposits"]; ok {
		f.Deposits = int64(v)
	}

	if v, ok := fields["PropertyPlantAndEquipmentNet"]; ok {
		f.PropertyPlantAndEquipmentNet = int64(v)
	}

	if v, ok := fields["Intangibles"]; ok {
		f.Intangibles = int64(v)
	}

	if v, ok := fields["TaxAssets"]; ok {
		f.TaxAssets = int64(v)
	}

	if v, ok := fields["TaxLiabilities"]; ok {
		f.TaxLiabilities = int64(v)
	}

	if v, ok := fields["TotalDebt"]; ok {
		f.TotalDebt = int64(v)
	}

	if v, ok := fields["DebtCurrent"]; ok {
		f.DebtCurrent = int64(v)
	}

	if v, ok := fields["DebtNonCurrent"]; ok {
		f.DebtNonCurrent = int64(v)
	}

	if v, ok := fields["DeferredRevenue"]; ok {
		f.DeferredRevenue = int64(v)
	}

	if v, ok := fields["TotalLiabilities"]; ok {
		f.TotalLiabilities = int64(v)
	}

	if v, ok := fields["CurrentLiabilities"]; ok {
		f.CurrentLiabilities = int64(v)
	}

	if v, ok := fields["LiabilitiesNonCurrent"]; ok {
		f.LiabilitiesNonCurrent = int64(v)
	}

	if v, ok := fields["Equity"]; ok {
		f.Equity = int64(v)
	}

	if v, ok := fields["AccumulatedOtherComprehensiveIncome"]; ok {
		f.AccumulatedOtherComprehensiveIncome = int64(v)
	}

	if v, ok := fields["AccumulatedRetainedEarningsDeficit"]; ok {
		f.AccumulatedRetainedEarningsDeficit = int64(v)
	}

	// Income Statement
	if v, ok := fields["Revenues"]; ok {
		f.Revenues = int64(v)
	}

	if v, ok := fields["CostOfRevenue"]; ok {
		f.CostOfRevenue = int64(v)
	}

	if v, ok := fields["GrossProfit"]; ok {
		f.GrossProfit = int64(v)
	}

	if v, ok := fields["OperatingExpenses"]; ok {
		f.OperatingExpenses = int64(v)
	}

	if v, ok := fields["SellingGeneralAndAdministrativeExpense"]; ok {
		f.SellingGeneralAndAdministrativeExpense = int64(v)
	}

	if v, ok := fields["RandDExpenses"]; ok {
		f.RandDExpenses = int64(v)
	}

	if v, ok := fields["OperatingIncome"]; ok {
		f.OperatingIncome = int64(v)
	}

	if v, ok := fields["InterestExpense"]; ok {
		f.InterestExpense = int64(v)
	}

	if v, ok := fields["IncomeTaxExpense"]; ok {
		f.IncomeTaxExpense = int64(v)
	}

	if v, ok := fields["NetIncome"]; ok {
		f.NetIncome = int64(v)
	}

	if v, ok := fields["NetIncomeCommonStock"]; ok {
		f.NetIncomeCommonStock = int64(v)
	}

	if v, ok := fields["ConsolidatedIncome"]; ok {
		f.ConsolidatedIncome = int64(v)
	}

	if v, ok := fields["NetLossIncomeDiscontinuedOperations"]; ok {
		f.NetLossIncomeDiscontinuedOperations = int64(v)
	}

	if v, ok := fields["NetIncomeToNonControllingInterests"]; ok {
		f.NetIncomeToNonControllingInterests = int64(v)
	}

	if v, ok := fields["PreferredDividendsIncomeStatementImpact"]; ok {
		f.PreferredDividendsIncomeStatementImpact = int64(v)
	}

	if v, ok := fields["EBIT"]; ok {
		f.EBIT = int64(v)
	}

	if v, ok := fields["EBITDA"]; ok {
		f.EBITDA = int64(v)
	}

	if v, ok := fields["EBT"]; ok {
		f.EBT = int64(v)
	}

	// Per-share
	if v, ok := fields["EPS"]; ok {
		f.EPS = v
	}

	if v, ok := fields["EPSDiluted"]; ok {
		f.EPSDiluted = v
	}

	if v, ok := fields["DividendsPerBasicCommonShare"]; ok {
		f.DividendsPerBasicCommonShare = v
	}

	// Share counts
	if v, ok := fields["SharesBasic"]; ok {
		f.SharesBasic = int64(v)
	}

	if v, ok := fields["WeightedAverageShares"]; ok {
		f.WeightedAverageShares = int64(v)
	}

	if v, ok := fields["WeightedAverageSharesDiluted"]; ok {
		f.WeightedAverageSharesDiluted = int64(v)
	}

	// Cash flow
	if v, ok := fields["NetCashFlowFromOperations"]; ok {
		f.NetCashFlowFromOperations = int64(v)
	}

	if v, ok := fields["NetCashFlowFromInvesting"]; ok {
		f.NetCashFlowFromInvesting = int64(v)
	}

	if v, ok := fields["NetCashFlowFromFinancing"]; ok {
		f.NetCashFlowFromFinancing = int64(v)
	}

	if v, ok := fields["DepreciationAmortizationAndAccretion"]; ok {
		f.DepreciationAmortizationAndAccretion = int64(v)
	}

	if v, ok := fields["CapitalExpenditure"]; ok {
		f.CapitalExpenditure = int64(v)
	}

	if v, ok := fields["ShareBasedCompensation"]; ok {
		f.ShareBasedCompensation = int64(v)
	}

	if v, ok := fields["NetCashFlowBusiness"]; ok {
		f.NetCashFlowBusiness = int64(v)
	}

	if v, ok := fields["NetCashFlowCommon"]; ok {
		f.NetCashFlowCommon = int64(v)
	}

	if v, ok := fields["NetCashFlowDebt"]; ok {
		f.NetCashFlowDebt = int64(v)
	}

	if v, ok := fields["NetCashFlowDividend"]; ok {
		f.NetCashFlowDividend = int64(v)
	}

	if v, ok := fields["NetCashFlowInvest"]; ok {
		f.NetCashFlowInvest = int64(v)
	}

	if v, ok := fields["NetCashFlowFx"]; ok {
		f.NetCashFlowFx = int64(v)
	}

	if v, ok := fields["NetCashFlow"]; ok {
		f.NetCashFlow = int64(v)
	}

	if v, ok := fields["FreeCashFlow"]; ok {
		f.FreeCashFlow = int64(v)
	}

	// Derived metrics
	if v, ok := fields["GrossMargin"]; ok {
		f.GrossMargin = v
	}

	if v, ok := fields["ProfitMargin"]; ok {
		f.ProfitMargin = v
	}

	if v, ok := fields["EBITDAMargin"]; ok {
		f.EBITDAMargin = v
	}

	if v, ok := fields["CurrentRatio"]; ok {
		f.CurrentRatio = v
	}

	if v, ok := fields["DebtToEquityRatio"]; ok {
		f.DebtToEquityRatio = v
	}

	if v, ok := fields["AssetTurnover"]; ok {
		f.AssetTurnover = v
	}

	if v, ok := fields["ReturnOnSales"]; ok {
		f.ReturnOnSales = v
	}

	if v, ok := fields["FreeCashFlowPerShare"]; ok {
		f.FreeCashFlowPerShare = v
	}

	if v, ok := fields["BookValuePerShare"]; ok {
		f.BookValuePerShare = v
	}

	if v, ok := fields["SalesPerShare"]; ok {
		f.SalesPerShare = v
	}

	if v, ok := fields["TangibleAssetsBookValuePerShare"]; ok {
		f.TangibleAssetsBookValuePerShare = v
	}

	if v, ok := fields["WorkingCapital"]; ok {
		f.WorkingCapital = int64(v)
	}

	if v, ok := fields["TangibleAssetValue"]; ok {
		f.TangibleAssetValue = int64(v)
	}

	if v, ok := fields["InvestedCapital"]; ok {
		f.InvestedCapital = int64(v)
	}

	return f
}
