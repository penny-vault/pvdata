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
	"time"
)

// ResolveDirect attempts to find a value for a direct field mapping by searching
// the CompanyFacts for matching XBRL tags. Tags are tried in order; the first
// tag with at least one matching fact wins, and within that tag's matching
// facts the one with the latest Filed date is selected.
//
// For instant (balance sheet) concepts, matches facts where End == periodEnd.
// For duration (income/cash flow) concepts, matches facts where End == periodEnd
// and the filing form matches.
//
// "Latest filed" semantics depend on the caller:
//   - When called directly with a CompanyFacts containing all facts, this picks
//     the most recently reported value across the entire history (i.e. an MR
//     view).
//   - When called via ResolveFieldsForFiling, the cf has already been filtered
//     to facts with Filed <= filedDate, so this picks the latest fact within
//     that window. For an AR resolution the caller passes ARFiledDate (the
//     earliest filing date for the period) and the window collapses to the
//     facts in the original 10-K/10-Q. For an MR resolution the caller passes
//     MRFiledDate (the latest filing date for the period) and the window
//     covers any subsequent restatements as well.
//
// Note that a 10-K/A amendment filed later than the original 10-K but still
// before MRFiledDate will overwrite the AR value when the AR window happens
// to extend to its filing date; this is the desired behavior because the
// amendment is the authoritative record of the period as filed.
func ResolveDirect(cf *CompanyFacts, m FieldMapping, periodEnd time.Time, formType string) (float64, bool) {
	tags := m.XBRLTags
	if m.Type == MappingDerived {
		tags = m.FallbackTags
	}

	// Normalize the target period end once so we match facts whose raw end
	// date differs by a day or two but belongs to the same fiscal period
	// (ghost-period variation across XBRL concepts).
	normalPeriodEnd := NormalizeEventDate(periodEnd, formType)

	for _, tag := range tags {
		facts, ok := cf.Facts[tag]
		if !ok {
			continue
		}

		// Find the best matching fact for this period
		var best *Fact

		for i := range facts {
			f := &facts[i]

			// Must match the normalized period end date
			if !NormalizeEventDate(f.End, formType).Equal(normalPeriodEnd) {
				continue
			}

			// Must match the form type
			if f.Form != formType {
				continue
			}

			// For duration concepts, verify the period length is reasonable.
			if !f.Start.IsZero() {
				days := f.End.Sub(f.Start).Hours() / 24
				if formType == "10-K" && days < 300 {
					continue // Skip quarterly data in an annual filing
				}
			}

			// Prefer single-quarter facts over YTD cumulative regardless of
			// filing date. Comparative filings from later periods often
			// include only the cumulative value (not single-quarter), so
			// preferring the latest filing date would lose the original
			// single-quarter fact. Within the same duration category,
			// prefer the latest filing date (most recent data).
			if best == nil {
				best = f
			} else {
				fDays := f.End.Sub(f.Start).Hours() / 24
				bestDays := best.End.Sub(best.Start).Hours() / 24
				fIsQuarterly := !f.Start.IsZero() && fDays <= ytdThresholdDays
				bestIsQuarterly := !best.Start.IsZero() && bestDays <= ytdThresholdDays

				switch {
				case fIsQuarterly && !bestIsQuarterly:
					// Single-quarter always wins over cumulative.
					best = f
				case !fIsQuarterly && bestIsQuarterly:
					// Keep the single-quarter fact.
				case f.Filed.After(best.Filed):
					// Same duration category: prefer latest filing.
					best = f
				case f.Filed.Equal(best.Filed) && !f.Start.IsZero() && !best.Start.IsZero() && fDays < bestDays:
					// Same filing date: prefer shorter duration.
					best = f
				}
			}
		}

		if best != nil {
			return best.Val, true
		}
	}

	return 0, false
}

// ResolveLongestDuration is like ResolveDirect but prefers the longest-duration
// matching fact instead of the shortest. This resolves YTD cumulative values
// from 10-Q filings for per-share fields where the company-reported cumulative
// value avoids rounding error from summing individually rounded quarterly values.
func ResolveLongestDuration(cf *CompanyFacts, m FieldMapping, periodEnd time.Time, formType string) (float64, bool) {
	tags := m.XBRLTags
	if m.Type == MappingDerived {
		tags = m.FallbackTags
	}

	normalPeriodEnd := NormalizeEventDate(periodEnd, formType)

	for _, tag := range tags {
		facts, ok := cf.Facts[tag]
		if !ok {
			continue
		}

		var best *Fact

		for i := range facts {
			f := &facts[i]

			if !NormalizeEventDate(f.End, formType).Equal(normalPeriodEnd) {
				continue
			}

			if f.Form != formType {
				continue
			}

			// Only duration concepts have cumulative variants.
			if f.Start.IsZero() {
				continue
			}

			if formType == "10-K" {
				days := f.End.Sub(f.Start).Hours() / 24
				if days < 300 {
					continue
				}
			}

			if best == nil || f.Filed.After(best.Filed) {
				best = f
			} else if f.Filed.Equal(best.Filed) && !best.Start.IsZero() {
				fDays := f.End.Sub(f.Start).Hours() / 24
				bestDays := best.End.Sub(best.Start).Hours() / 24

				if fDays > bestDays {
					best = f
				}
			}
		}

		if best != nil {
			return best.Val, true
		}
	}

	return 0, false
}

// ytdThresholdDays is the maximum duration (in days) of a single-quarter fact.
// Facts longer than this in a 10-Q filing are treated as YTD cumulative values
// that need de-cumulation. A standard quarter is ~90 days; 120 provides margin
// for unusual fiscal calendars while cleanly excluding 2-quarter YTD (~180 days).
const ytdThresholdDays = 120

// needsDecumulation reports whether a flow field's best-matching fact for the
// given 10-Q period is a YTD cumulative value. It mirrors ResolveDirect's tag
// priority: the first tag with matching facts determines the answer.
//
// Returns true when the matching tag has duration facts for the period end but
// none of them are single-quarter (all > ytdThresholdDays). This indicates the
// SEC filing only reported YTD cumulative values for this concept, as is
// standard for cash flow statement items.
func needsDecumulation(cf *CompanyFacts, m FieldMapping, periodEnd time.Time, formType string) bool {
	if formType != "10-Q" {
		return false
	}

	if m.StatementType != StmtFlow {
		return false
	}

	normalPeriodEnd := NormalizeEventDate(periodEnd, formType)

	tags := m.XBRLTags
	if m.Type == MappingDerived {
		tags = m.FallbackTags
	}

	for _, tag := range tags {
		facts, ok := cf.Facts[tag]
		if !ok {
			continue
		}

		var hasMatch bool

		for i := range facts {
			f := &facts[i]

			if f.Form != formType {
				continue
			}

			if !NormalizeEventDate(f.End, formType).Equal(normalPeriodEnd) {
				continue
			}

			if f.Start.IsZero() {
				continue // instant concept, not applicable
			}

			hasMatch = true

			days := f.End.Sub(f.Start).Hours() / 24
			if days <= ytdThresholdDays {
				return false // found a quarterly fact, no de-cumulation needed
			}
		}

		if hasMatch {
			return true // this tag has only YTD facts
		}
	}

	return false
}

// conceptFiledQuarterly returns true if any of the given XBRL concept names
// has at least one fact filed on a 10-Q form in cf within the last 3 years.
// This distinguishes balance sheet line items (filed quarterly) from
// supplemental disclosures (10-K only). The recency check avoids false
// positives from tags that were filed on 10-Q years ago but have since been
// discontinued (e.g. AAPL's AccruedIncomeTaxesCurrent, last on 10-Q in 2010).
func conceptFiledQuarterly(cf *CompanyFacts, tags []string) bool {
	for _, tag := range tags {
		facts, ok := cf.Facts[tag]
		if !ok {
			continue
		}

		for i := range facts {
			if facts[i].Form == "10-Q" && !facts[i].End.IsZero() {
				// Consider only facts from the last ~3 years
				age := time.Since(facts[i].End)
				if age < 3*365*24*time.Hour {
					return true
				}
			}
		}
	}

	return false
}

// ResolveAllFields resolves all configured field mappings for a given period.
// Direct fields are resolved first, then derived fields are computed from the
// resolved values.
func ResolveAllFields(cf *CompanyFacts, periodEnd time.Time, formType string) map[string]float64 {
	resolved := make(map[string]float64)

	for _, m := range FieldMappings {
		// RequireQuarterly: skip this field entirely if none of its XBRL
		// tags are filed on 10-Q by this company. This ensures we only
		// include balance sheet line items that the company breaks out as
		// separate lines, not items buried in annual note disclosures.
		if m.RequireQuarterly && !conceptFiledQuarterly(cf, m.XBRLTags) {
			continue
		}

		// ExcludeIfQuarterly: skip this field if a sentinel concept is
		// filed on 10-Q, indicating the field's tags are sub-components
		// of a broader line item rather than separate balance sheet lines.
		if len(m.ExcludeIfQuarterly) > 0 && conceptFiledQuarterly(cf, m.ExcludeIfQuarterly) {
			continue
		}

		// RequireIfQuarterly: only resolve when a sentinel concept is
		// filed on 10-Q. The inverse of ExcludeIfQuarterly.
		if len(m.RequireIfQuarterly) > 0 && !conceptFiledQuarterly(cf, m.RequireIfQuarterly) {
			continue
		}

		switch m.Type {
		case MappingDirect:
			if val, ok := ResolveDirect(cf, m, periodEnd, formType); ok {
				if m.Negate {
					val = -val
				}

				resolved[m.FieldName] = val
			}

		case MappingDerived:
			// Try direct XBRL fallback tags first
			if len(m.FallbackTags) > 0 {
				if val, ok := ResolveDirect(cf, m, periodEnd, formType); ok {
					if m.Negate {
						val = -val
					}

					resolved[m.FieldName] = val

					continue
				}
			}

			// Compute from formula
			if val, ok := computeDerived(m, resolved); ok {
				resolved[m.FieldName] = val
			}
		}
	}

	return resolved
}

// computeDerived evaluates a derived field's formula using already-resolved values.
func computeDerived(m FieldMapping, resolved map[string]float64) (float64, bool) {
	// When OptionalOperands is set, missing operands are treated as 0 and the
	// formula resolves if at least one operand is present. This handles fields
	// like Investments = InvestmentsCurrent + InvestmentsNonCurrent where a
	// company may report only one component, and InvestedCapital where
	// Intangibles may be absent (the company has none).
	if m.OptionalOperands && (m.Op == OpAdd || m.Op == OpLinearCombination) {
		sum := 0.0
		found := false

		for i, op := range m.Operands {
			if v, ok := resolved[op]; ok {
				if m.Op == OpLinearCombination {
					sum += m.Coefficients[i] * v
				} else {
					sum += v
				}

				found = true
			}
		}

		if found {
			return sum, true
		}

		return 0, false
	}

	// All operands must be present
	vals := make([]float64, len(m.Operands))
	for i, op := range m.Operands {
		v, ok := resolved[op]
		if !ok {
			return 0, false
		}

		vals[i] = v
	}

	switch m.Op {
	case OpAdd:
		sum := 0.0
		for _, v := range vals {
			sum += v
		}

		return sum, true

	case OpSubtract:
		if len(vals) < 2 {
			return 0, false
		}

		return vals[0] - vals[1], true

	case OpDivide:
		if len(vals) < 2 || vals[1] == 0 {
			return 0, false
		}

		result := vals[0] / vals[1]

		if m.RoundDigits > 0 {
			pow := math.Pow(10, float64(m.RoundDigits))
			result = math.Round(result*pow) / pow
		}

		return result, true

	case OpLinearCombination:
		if len(vals) != len(m.Coefficients) {
			return 0, false
		}

		sum := 0.0
		for i, v := range vals {
			sum += m.Coefficients[i] * v
		}

		return sum, true
	}

	return 0, false
}

// OverrideDPSFromCash computes DividendsPerBasicCommonShare from the cash-paid
// methodology when _absDividendsPaid is present. This matches Sharadar's DPS
// computation: total cash dividends paid / weighted-average shares, rounded to
// 2 decimal places. When _absDividendsPaid is absent (e.g. AAPL which uses the
// broader PaymentsOfDividends tag), the existing declared per-share value is
// preserved.
func OverrideDPSFromCash(fields map[string]float64) {
	cashPaid, hasCash := fields["_absDividendsPaid"]
	shares, hasShares := fields["WeightedAverageShares"]

	if hasCash && hasShares && shares > 0 {
		dps := cashPaid / shares
		// Round to 2 decimal places.
		dps = math.Round(dps*100) / 100
		fields["DividendsPerBasicCommonShare"] = dps
	}
}

// resolveSharesBasicAsOf returns the EntityCommonStockSharesOutstanding
// value from the most recently filed 10-K or 10-Q as of the given date.
// Sharadar's MR dimensions use "latest known cover-page shares as of the
// period end" rather than the shares from the filing that reports the
// period's own data. Returns (0, false) when no suitable fact is found.
func resolveSharesBasicAsOf(cf *CompanyFacts, asOfDate time.Time) (float64, bool) {
	facts, ok := cf.Facts["EntityCommonStockSharesOutstanding"]
	if !ok {
		return 0, false
	}

	var best *Fact

	for i := range facts {
		f := &facts[i]

		if f.Form != "10-K" && f.Form != "10-Q" {
			continue
		}

		if f.Filed.After(asOfDate) {
			continue
		}

		if best == nil || f.Filed.After(best.Filed) {
			best = f
		}
	}

	if best == nil {
		return 0, false
	}

	return best.Val, true
}

// overrideNCFDebtResidual recomputes NetCashFlowDebt as the residual of
// the financing section: debt = financing - common - dividend. This captures
// items that are not separately tagged in XBRL but are included in the
// financing total.
//
// Applied in two cases:
//  1. AccruedLiabilitiesCurrent is filed on 10-Q (NVDA) — the company bundles
//     financing items into broader categories.
//  2. AssetsCurrent is NOT filed on 10-Q (banks like JPM) — bank financing
//     activities include deposits, fed funds, and repo agreements that aren't
//     captured by the standard debt-proceeds/repayments tags.
//
// For companies like AAPL that present debt cash flows as separate lines
// and DO file AssetsCurrent, the direct XBRL-based computation is correct.
func overrideNCFDebtResidual(cf *CompanyFacts, fields map[string]float64, periodEnd time.Time, formType string) {
	isBank := !conceptFiledQuarterly(cf, []string{"AssetsCurrent"})
	bundlesFinancing := conceptFiledQuarterly(cf, []string{"AccruedLiabilitiesCurrent"})

	if !isBank && !bundlesFinancing {
		return
	}

	financing, hasF := fields["NetCashFlowFromFinancing"]
	common, hasC := fields["NetCashFlowCommon"]
	dividend, hasD := fields["NetCashFlowDividend"]

	if hasF && hasC && hasD {
		fields["NetCashFlowDebt"] = financing - common - dividend
	}

	// For banks, derive OperatingIncome = GrossProfit - OperatingExpenses
	// when OperatingIncomeLoss isn't available. OperatingExpenses resolves
	// from NoninterestExpense FallbackTag, and GrossProfit = Revenues.
	if isBank {
		if _, hasOI := fields["OperatingIncome"]; !hasOI {
			gp, hasGP := fields["GrossProfit"]
			opex, hasOE := fields["OperatingExpenses"]

			if hasGP && hasOE {
				fields["OperatingIncome"] = gp - opex
			}
		}
	}

	// For banks, also recompute NetCashFlowInvest as the residual of the
	// investing section: invest = total_investing - capex. Bank investing
	// activities include loan originations, fed funds, and repo transactions
	// that standard investment-security tags don't capture. Business
	// acquisitions are already part of the investing total and are tracked
	// separately as NetCashFlowBusiness.
	if isBank {
		investing, hasI := fields["NetCashFlowFromInvesting"]
		if hasI {
			capex, _ := fields["CapitalExpenditure"] // 0 when absent (banks)
			fields["NetCashFlowInvest"] = investing - capex
		}
	}

	// For banks, override balance sheet fields from extension tags. The
	// mapping-level FallbackTags work for AR dimensions but the MR filing
	// date filter can exclude extension facts from the MR CompanyFacts.
	// Resolving directly from the UNFILTERED cf ensures extension tags are
	// always available.
	if isBank {
		bankExtTags := []struct {
			field string
			tags  []string
		}{
			// JPM 10-K and 10-Q use different casing for extension concepts.
			{"Intangibles", []string{
				"GoodwillServicingAssetsAtFairValueAndOtherIntangibleAssets",   // 10-Q
				"GoodwillServicingAssetsatFairValueandOtherIntangibleAssets",   // 10-K
			}},
			{"PropertyPlantAndEquipmentNet", []string{
				"PropertyPlantAndEquipmentAndOperatingLeaseRightOfUseAssetAfterAccumulatedDepreciationAndAmortization",
			}},
			{"Receivables", []string{"AccruedInterestAndAccountsReceivable"}},
		}

		for _, ext := range bankExtTags {
			if v, ok := ResolveDirect(cf, FieldMapping{XBRLTags: ext.tags}, periodEnd, formType); ok {
				fields[ext.field] = v
			}
		}
	}

	// For banks, compute Investments as the balance sheet residual:
	// TotalAssets - CashAndEquivalents - Receivables - PP&E - Intangibles - OtherAssets.
	// This captures loans, securities, fed funds, and other invested assets.
	if isBank {
		totalAssets, hasTA := fields["TotalAssets"]

		if hasTA {
			cash, _ := fields["CashAndEquivalents"]
			recv, _ := fields["Receivables"]
			ppe, _ := fields["PropertyPlantAndEquipmentNet"]
			intang, _ := fields["Intangibles"]
			oa := 0.0

			if v, ok := ResolveDirect(cf, FieldMapping{XBRLTags: []string{"OtherAssets"}}, periodEnd, formType); ok {
				oa = v
			}

			fields["Investments"] = totalAssets - cash - recv - ppe - intang - oa
		}
	}
}
