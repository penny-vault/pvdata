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
	"math"
	"sort"
	"time"

	"github.com/penny-vault/pvdata/data"
)

// TTM span limits: a valid trailing-twelve-month aggregation should cover roughly
// one calendar year. We allow some slack for fiscal calendar variations (53-week
// retailers, leap years, fiscal-year boundary shifts) but reject anything that
// could not plausibly represent twelve months of activity.
const (
	ttmMinSpanDays = 270
	ttmMaxSpanDays = 410
)

// Period represents a unique reporting period identified from CompanyFacts.
//
// ARFiledDate is the earliest Filed date observed across every concept that
// reported on this period. Because ParseCompanyFacts only retains facts from
// 10-K and 10-Q filings, ARFiledDate is effectively "earliest 10-K or 10-Q
// filing date that reported on this period" -- not a guarantee that the
// filing on that date was the original 10-K. In practice, for most companies
// the earliest filing for a fiscal year-end period IS the original 10-K, and
// for a quarter-end period IS the original 10-Q, so ARFiledDate corresponds
// to the "as-reported" view the names suggests.
//
// MRFiledDate is the latest Filed date observed across every concept for
// this period, i.e. the most recent restatement or amendment that touched
// any fact in the period.
type Period struct {
	PeriodEnd   time.Time // End date of the fiscal period
	FormType    string    // "10-K" or "10-Q"
	ARFiledDate time.Time // Earliest filing date for this period (As Reported)
	MRFiledDate time.Time // Latest filing date for this period (Most Recent Reported)
}

// IdentifyPeriods scans all facts in a CompanyFacts to find unique reporting periods
// and their earliest/latest filing dates.
//
// SEC XBRL filings frequently contain "ghost periods": the same logical fiscal
// quarter is reported with subtly different end dates across filings (e.g.
// 2018-09-29 in one 10-Q and 2018-09-30 in a later amendment). To collapse
// these together, periods are deduplicated by (NormalizeEventDate, FormType):
//   - PeriodEnd is the latest raw end date in the group (the company's most
//     recently used canonical end date)
//   - ARFiledDate is the earliest filing date across the group
//   - MRFiledDate is the latest filing date across the group
//
// Invariant: because ParseCompanyFacts drops every fact whose form is not
// 10-K or 10-Q, ARFiledDate is effectively "earliest 10-K or 10-Q filing
// date that reported on this period". A 10-K/A amendment or other form
// cannot contribute to ARFiledDate (or MRFiledDate) because its facts never
// enter the CompanyFacts in the first place.
//
// The returned slice is sorted by PeriodEnd ascending.
func IdentifyPeriods(cf *CompanyFacts) []Period {
	type periodKey struct {
		end  time.Time
		form string
	}

	// First pass: group raw facts by exact (end, form) pair so we collect
	// AR/MR filed dates per raw period end.
	//
	// Only duration concepts (those with a non-zero Start date) drive period
	// identification. Instant concepts (balance sheet items like Assets that
	// have no Start date) supplement existing periods during field resolution
	// but must not create new periods on their own. Without this filter,
	// comparative balance sheet data included in 10-Q filings (which reports
	// instant values as of the prior fiscal year-end) would create spurious
	// 10-Q periods at fiscal year-end dates. Those fake periods resolve only
	// balance sheet fields, leaving revenues and shares at zero, which trips
	// inline data quality checks.
	rawPeriods := make(map[periodKey]*Period)

	for _, facts := range cf.Facts {
		for _, f := range facts {
			if f.Start.IsZero() {
				continue
			}

			// 10-K filings often include quarterly-duration facts alongside
			// the full-year data (e.g. current-quarter revenue breakdowns,
			// or comparative quarterly data from prior years). These short-
			// duration facts share Form="10-K" but their End dates correspond
			// to quarter boundaries, not the fiscal year-end. If allowed into
			// period identification they create spurious 10-K periods at those
			// quarter-end dates that later merge with the real annual period
			// during NormalizeEventDate dedup (both snap to Dec 31). The merge
			// picks the latest raw PeriodEnd — typically the Q1 end in December
			// — which shifts the synthesized Q4 to the wrong calendar quarter.
			// Filtering to >= 300 days mirrors ResolveDirect's logic and ensures
			// only genuine annual-duration facts drive 10-K period creation.
			if f.Form == "10-K" {
				days := f.End.Sub(f.Start).Hours() / 24
				if days < 300 {
					continue
				}
			}

			key := periodKey{end: f.End, form: f.Form}

			p, exists := rawPeriods[key]
			if !exists {
				p = &Period{
					PeriodEnd:   f.End,
					FormType:    f.Form,
					ARFiledDate: f.Filed,
					MRFiledDate: f.Filed,
				}
				rawPeriods[key] = p

				continue
			}

			if f.Filed.Before(p.ARFiledDate) {
				p.ARFiledDate = f.Filed
			}

			if f.Filed.After(p.MRFiledDate) {
				p.MRFiledDate = f.Filed
			}
		}
	}

	// Second pass: collapse ghost periods by (NormalizeEventDate, FormType).
	// NormalizeEventDate snaps mid-quarter ends to the nearest calendar quarter
	// end (and annual ends to 12/31), so 2018-09-29 and 2018-09-30 fall into
	// the same bucket but 2018-06-30 and 2018-09-30 do not.
	dedupedPeriods := make(map[periodKey]*Period)

	for _, p := range rawPeriods {
		key := periodKey{
			end:  NormalizeEventDate(p.PeriodEnd, p.FormType),
			form: p.FormType,
		}

		canonical, exists := dedupedPeriods[key]
		if !exists {
			// Copy so we don't mutate the rawPeriods entry.
			cp := *p
			dedupedPeriods[key] = &cp

			continue
		}

		// Use the latest raw period end as canonical PeriodEnd.
		if p.PeriodEnd.After(canonical.PeriodEnd) {
			canonical.PeriodEnd = p.PeriodEnd
		}

		// Aggregate filing date bounds across the group.
		if p.ARFiledDate.Before(canonical.ARFiledDate) {
			canonical.ARFiledDate = p.ARFiledDate
		}

		if p.MRFiledDate.After(canonical.MRFiledDate) {
			canonical.MRFiledDate = p.MRFiledDate
		}
	}

	periods := make([]Period, 0, len(dedupedPeriods))
	for _, p := range dedupedPeriods {
		periods = append(periods, *p)
	}

	sort.Slice(periods, func(i, j int) bool {
		return periods[i].PeriodEnd.Before(periods[j].PeriodEnd)
	})

	return periods
}

// NormalizeEventDate converts a raw period end date to a normalized calendar date,
// matching Sharadar's EventDate convention:
//   - Quarterly (10-Q, ARQ/MRQ/ART/MRT): snaps to the *nearest* calendar quarter end
//     (3/31, 6/30, 9/30, 12/31). For example, 2018-07-24 maps to 2018-06-30 (closer
//     to the previous quarter end than the next), and 2018-09-29 maps to 2018-09-30
//     (Apple's fiscal Q-end). Ties are broken by snapping forward.
//   - Annual (10-K, ARY/MRY): always snaps to the calendar year end (12/31), so a
//     fiscal year ending 2015-09-26 (Apple's FY2015) is reported as 2015-12-31.
func NormalizeEventDate(periodEnd time.Time, formType string) time.Time {
	if formType == "10-K" {
		return time.Date(periodEnd.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
	}

	// Quarterly: snap to the nearest calendar quarter end. Build the candidate
	// quarter ends bracketing the period end and pick whichever is closer.
	candidates := quarterEndCandidates(periodEnd)

	// Truncate the period end to a date so the comparison is purely calendar-based.
	day := time.Date(periodEnd.Year(), periodEnd.Month(), periodEnd.Day(), 0, 0, 0, 0, time.UTC)

	best := candidates[0]
	bestDist := absDuration(day.Sub(best))

	for _, c := range candidates[1:] {
		dist := absDuration(day.Sub(c))
		// Strictly less so that ties (equal distance) prefer the earlier "best",
		// which is the *forward* candidate because we iterate from latest to earliest.
		if dist < bestDist {
			best = c
			bestDist = dist
		}
	}

	return best
}

// quarterEndCandidates returns the calendar quarter ends that bracket the given date,
// ordered from latest (forward) to earliest (backward) so that tie-breaking favors
// snapping forward.
func quarterEndCandidates(periodEnd time.Time) []time.Time {
	year := periodEnd.Year()
	allEnds := []time.Time{
		time.Date(year-1, 12, 31, 0, 0, 0, 0, time.UTC),
		time.Date(year, 3, 31, 0, 0, 0, 0, time.UTC),
		time.Date(year, 6, 30, 0, 0, 0, 0, time.UTC),
		time.Date(year, 9, 30, 0, 0, 0, 0, time.UTC),
		time.Date(year, 12, 31, 0, 0, 0, 0, time.UTC),
		time.Date(year+1, 3, 31, 0, 0, 0, 0, time.UTC),
	}

	day := time.Date(periodEnd.Year(), periodEnd.Month(), periodEnd.Day(), 0, 0, 0, 0, time.UTC)

	// Find the first quarter end that is >= day; the candidates are it and the one before.
	var forwardIdx int

	for i, qe := range allEnds {
		if !qe.Before(day) {
			forwardIdx = i
			break
		}
	}

	if forwardIdx == 0 {
		forwardIdx = 1
	}

	// Return [forward, backward] so tie-breaking (strict <) keeps the forward one.
	return []time.Time{allEnds[forwardIdx], allEnds[forwardIdx-1]}
}

// absDuration returns the absolute value of a duration.
func absDuration(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}

	return d
}

// ResolveFieldsForFiling resolves all fields using only facts filed on or before
// a specific date. This allows producing AR (earliest filed) vs MR (latest filed)
// views of the same period.
//
// Each concept's facts slice is pre-sorted by Filed ascending in
// ParseCompanyFacts, so this uses sort.Search to find the prefix of facts
// available at filedDate and re-uses the underlying array via slicing rather
// than copying. The filtered CompanyFacts is read-only by ResolveAllFields, so
// sharing the backing array is safe.
func ResolveFieldsForFiling(cf *CompanyFacts, periodEnd time.Time, formType string, filedDate time.Time) map[string]float64 {
	filtered := &CompanyFacts{
		CIK:        cf.CIK,
		EntityName: cf.EntityName,
		Facts:      make(map[string][]Fact, len(cf.Facts)),
	}

	for concept, facts := range cf.Facts {
		// Binary search for the index of the first fact whose Filed is
		// strictly after filedDate; everything before that index was filed
		// on or before filedDate.
		idx := sort.Search(len(facts), func(i int) bool {
			return facts[i].Filed.After(filedDate)
		})

		if idx > 0 {
			filtered.Facts[concept] = facts[:idx]
		}
	}

	return ResolveAllFields(filtered, periodEnd, formType)
}

// DecumulateYTD converts YTD cumulative values in a 10-Q resolved field map to
// single-quarter values by subtracting the prior quarter's (also YTD) values.
//
// SEC 10-Q filings report cash flow items as year-to-date cumulative values
// only -- no single-quarter fact exists. Income statement items typically have
// both a single-quarter and a YTD fact; ResolveDirect's shorter-duration
// preference already picks the quarterly fact for those. This function handles
// the remaining YTD-only fields.
//
// current and prior are the original resolved field maps (prior may contain YTD
// values). cf is the unfiltered CompanyFacts used to determine which fields are
// YTD via needsDecumulation. The returned map is a copy; current and prior are
// not modified.
//
// After de-cumulating direct fields, derived flow fields are recomputed from the
// de-cumulated components. Metric-type derived fields (ratios) are then
// recomputed from the updated flow/point-in-time values.
func DecumulateYTD(cf *CompanyFacts, current, prior map[string]float64, periodEnd time.Time, formType string) map[string]float64 {
	result := make(map[string]float64, len(current))
	for k, v := range current {
		result[k] = v
	}

	// Pass 1: de-cumulate direct and fallback-resolved flow fields.
	for _, m := range FieldMappings {
		if m.StatementType != StmtFlow {
			continue
		}

		if !needsDecumulation(cf, m, periodEnd, formType) {
			continue
		}

		currVal, hasCurr := current[m.FieldName]
		priorVal, hasPrior := prior[m.FieldName]

		if hasCurr && hasPrior {
			result[m.FieldName] = currVal - priorVal
		}
	}

	// Pass 2: recompute derived flow fields from (now de-cumulated) components.
	// This handles cases like FreeCashFlow = NetCashFlowFromOperations +
	// CapitalExpenditure (CapEx is negative) where both operands are cash
	// flow YTD values.
	for _, m := range FieldMappings {
		if m.Type != MappingDerived || m.StatementType != StmtFlow {
			continue
		}

		if val, ok := computeDerived(m, result); ok {
			result[m.FieldName] = val
		}
	}

	// Pass 3: recompute metric-type derived fields (ratios like EBITDAMargin)
	// since their flow operands may have changed.
	for _, m := range FieldMappings {
		if m.Type != MappingDerived || m.StatementType != StmtMetric {
			continue
		}

		if val, ok := computeDerived(m, result); ok {
			result[m.FieldName] = val
		}
	}

	return result
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

		case StmtPointInTime, StmtPeriodAverage:
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

// ComputeMultiQAverages computes period-average balance sheet fields by
// averaging across multiple quarterly snapshots (typically 4 for TTM and
// annual dimensions). current is the emit-ready field map (TTM aggregate or
// annual) used for ratio numerators; quarterMaps are the individual quarterly
// emit maps whose balance sheet fields are averaged.
//
// Returns a map containing only the computed fields; the caller merges these
// into the emit map. Fields whose inputs are missing are silently omitted.
func ComputeMultiQAverages(current map[string]float64, quarterMaps []map[string]float64) map[string]float64 {
	result := make(map[string]float64)

	if len(quarterMaps) == 0 {
		return result
	}

	// Helper: average field across all quarter maps that contain it.
	avg := func(field string) (float64, bool) {
		sum := 0.0
		count := 0

		for _, qm := range quarterMaps {
			if v, ok := qm[field]; ok {
				sum += v
				count++
			}
		}

		if count == 0 {
			return 0, false
		}

		return sum / float64(count), true
	}

	if v, ok := avg("TotalAssets"); ok {
		result["AverageAssets"] = v
	}

	if v, ok := avg("Equity"); ok {
		result["EquityAvg"] = v
	}

	if v, ok := avg("InvestedCapital"); ok {
		result["InvestedCapitalAverage"] = v
	}

	// Ratios use the aggregate (TTM/annual) numerator and the averaged denominator.
	ratio := func(numField, denomField string) (float64, bool) {
		num, nOK := current[numField]
		denom, dOK := result[denomField]

		if !nOK || !dOK || denom == 0 {
			return 0, false
		}

		return num / denom, true
	}

	if v, ok := ratio("NetIncomeCommonStock", "AverageAssets"); ok {
		result["ROA"] = v
	}

	if v, ok := ratio("NetIncomeCommonStock", "EquityAvg"); ok {
		result["ROE"] = v
	}

	if v, ok := ratio("EBIT", "InvestedCapitalAverage"); ok {
		result["ROIC"] = v
	}

	if v, ok := ratio("Revenues", "AverageAssets"); ok {
		pow := float64(10 * 10 * 10)
		result["AssetTurnover"] = math.Round(v*pow) / pow
	}

	return result
}

// BuildFundamental converts a resolved field map into a data.Fundamental struct.
//
// lastUpdated is the timestamp the caller wants to record as the data's
// freshness marker. SEC fundamentals use the underlying filing date so re-runs
// of the provider produce stable LastUpdated values for the same source data:
//   - AR observations pass the AR (earliest) filing date
//   - MR observations pass the MR (latest) filing date
//   - TTM observations pass the latest MR filing date among the constituent quarters
func BuildFundamental(fields map[string]float64, ticker, compositeFigi, dimension string, eventDate, dateKey, reportPeriod, lastUpdated time.Time) *data.Fundamental {
	f := &data.Fundamental{
		EventDate:     eventDate,
		Ticker:        ticker,
		CompositeFigi: compositeFigi,
		Dimension:     dimension,
		DateKey:       dateKey,
		ReportPeriod:  reportPeriod,
		LastUpdated:   lastUpdated,
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

	if v, ok := fields["AverageAssets"]; ok {
		f.AverageAssets = int64(v)
	}

	if v, ok := fields["EquityAvg"]; ok {
		f.EquityAvg = int64(v)
	}

	if v, ok := fields["InvestedCapitalAverage"]; ok {
		f.InvestedCapitalAverage = int64(v)
	}

	if v, ok := fields["ROA"]; ok {
		f.ROA = v
	}

	if v, ok := fields["ROE"]; ok {
		f.ROE = v
	}

	if v, ok := fields["ROIC"]; ok {
		f.ROIC = v
	}

	return f
}
