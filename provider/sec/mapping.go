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
			} else if m.StatementType == StmtFlow && formType == "10-K" {
				// Skip instant-style facts (zero start) for flow concepts on
				// a 10-K. UNH tags CommonStockDividendsPerShareCashPaid at the
				// dividend declaration date (e.g. end=2024-12-17, start=zero,
				// val=$2.10 for the quarterly payment), which normalizes to
				// 2024-12-31 and would be preferred over the valid full-year
				// duration fact (val=$8.18).
				continue
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

// hasNonQuarterlyDPSDeclarationCadence returns true when the company tags
// CommonStockDividendsPerShareDeclared with non-quarterly cadence — i.e., a
// non-Q1 10-Q period where the single-period fact equals the YTD fact. LLY
// declares dividends semi-annually but pays quarterly, so the Q2 single-period
// declared value (start=Apr-1, val=$3.00) duplicates the YTD H1 value
// (start=Jan-1, val=$3.00) instead of representing a real Q2-only $1.50
// declaration. Such filers need cash-paid DPS for Sharadar parity; quarterly
// declarers (AAPL, MSFT) keep the declared per-share value.
func hasNonQuarterlyDPSDeclarationCadence(cf *CompanyFacts) bool {
	facts, ok := cf.Facts["CommonStockDividendsPerShareDeclared"]
	if !ok {
		return false
	}

	type periodKey struct {
		end time.Time
	}

	type periodFacts struct {
		single float64
		ytd    float64
		hasS   bool
		hasY   bool
	}

	groups := make(map[periodKey]*periodFacts)

	for i := range facts {
		f := &facts[i]
		if f.Form != "10-Q" || f.End.IsZero() || f.Start.IsZero() {
			continue
		}

		// Only consider recent facts (last 3 years) to avoid old anomalies
		// — e.g. JPM had a single Q2 2018 with single==YTD that doesn't
		// reflect their current quarterly cadence.
		if time.Since(f.End) > 3*365*24*time.Hour {
			continue
		}

		days := f.End.Sub(f.Start).Hours() / 24
		key := periodKey{end: f.End}

		pf, ok := groups[key]
		if !ok {
			pf = &periodFacts{}
			groups[key] = pf
		}

		// Single quarter ≈ 90 days; YTD H1+ ≥ 150 days.
		switch {
		case days <= 100:
			pf.single = f.Val
			pf.hasS = true
		case days >= 150:
			pf.ytd = f.Val
			pf.hasY = true
		}
	}

	for _, pf := range groups {
		if pf.hasS && pf.hasY && pf.single == pf.ytd && pf.single > 0 {
			return true
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
			// Try direct XBRL fallback tags first.
			// FallbackRequireIfQuarterly gates the attempt: only try
			// FallbackTags when a sentinel concept is filed quarterly
			// (standard companies). Non-standard companies (e.g. BRK/B
			// without COGS) skip FallbackTags and use the formula.
			useFallback := len(m.FallbackTags) > 0
			if useFallback && len(m.FallbackRequireIfQuarterly) > 0 {
				useFallback = conceptFiledQuarterly(cf, m.FallbackRequireIfQuarterly)
			}

			if useFallback {
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

	// Health insurers (UNH) report claims reserves under
	// LiabilityForClaimsAndClaimsAdjustmentExpense. Sharadar includes this in
	// payables alongside the standard accounts-payable line. Detect via
	// PolicyholderBenefitsAndClaimsIncurredNet (the matching income-statement
	// concept); BRK-style insurance conglomerates don't file this pair and
	// aren't affected. CoR is handled via _insuranceBenefitsIncurred in the
	// CostOfRevenue Derived mapping; Payables is patched here because the
	// baseline Payables Direct resolution depends on an unrelated mechanism
	// (dimensional segment synthesis) that currently produces the expected
	// value for BRK, and converting Payables to Derived would break that.
	if conceptFiledQuarterly(cf, []string{"PolicyholderBenefitsAndClaimsIncurredNet"}) {
		if v, ok := ResolveDirect(cf, FieldMapping{
			XBRLTags:      []string{"LiabilityForClaimsAndClaimsAdjustmentExpense"},
			StatementType: StmtPointInTime,
		}, periodEnd, formType); ok {
			resolved["Payables"] += v
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
// 2 decimal places.
//
// _absDividendsPaid resolves from PaymentsOfDividendsCommonStock when filed
// (MSFT, BRK/B, JPM, GS), in which case the override always runs on both AR
// and MR. When it resolves from the broader PaymentsOfDividends fallback, the
// override is restricted for non-quarterly declarers (LLY-style):
//   - MR: always overrides (Sharadar's MR DPS for LLY is cash-paid).
//   - AR: overrides only for periods where the company filed a 10-Q
//     CommonStockDividendsPerShareDeclared fact at periodEnd. Sharadar uses
//     declared values for periods covered by the original 10-K (Q4 fiscal
//     year-end) and cash-paid for in-fiscal-year quarterly periods where the
//     XBRL-declared per-share value is unreliable (LLY's Q2 2025 single fact
//     is tagged equal to the H1 YTD value of $3.00 instead of the actual
//     $1.50 quarterly payment).
//   - AAPL (quarterly declarer): override is skipped entirely because
//     PaymentsOfDividends includes dividend equivalents on RSUs that inflate
//     cash-paid relative to the declared per-share value Sharadar uses.
func OverrideDPSFromCash(cf *CompanyFacts, fields map[string]float64, isMR bool, periodEnd time.Time) {
	cashPaid, hasCash := fields["_absDividendsPaid"]
	shares, hasShares := fields["WeightedAverageShares"]

	if !hasCash || !hasShares || shares <= 0 {
		return
	}

	// When the cash-paid value comes from PaymentsOfDividends (no
	// PaymentsOfDividendsCommonStock filed), gate the override on filer
	// cadence and dimension/period.
	if !conceptFiledQuarterly(cf, []string{"PaymentsOfDividendsCommonStock"}) {
		if !hasNonQuarterlyDPSDeclarationCadence(cf) {
			return
		}

		if !isMR && !hasQuarterlyDPSDeclarationAt(cf, periodEnd) {
			return
		}
	}

	dps := cashPaid / shares
	// Round to 2 decimal places.
	dps = math.Round(dps*100) / 100
	fields["DividendsPerBasicCommonShare"] = dps
}

// hasQuarterlyDPSDeclarationAt returns true when the company filed a 10-Q
// CommonStockDividendsPerShareDeclared fact (any duration) at the given
// periodEnd. For LLY this distinguishes Q1/Q2/Q3 dates (covered by 10-Q
// filings) from Q4 fiscal year-end (covered only by the 10-K). Sharadar uses
// XBRL-declared values for fiscal year-end and cash-paid for in-year quarters
// when the company declares dividends on a non-quarterly cadence.
func hasQuarterlyDPSDeclarationAt(cf *CompanyFacts, periodEnd time.Time) bool {
	facts, ok := cf.Facts["CommonStockDividendsPerShareDeclared"]
	if !ok {
		return false
	}

	for i := range facts {
		f := &facts[i]
		if f.Form == "10-Q" && f.End.Equal(periodEnd) {
			return true
		}
	}

	return false
}

// resolveSharesBasicAsOf returns the shares outstanding value from the most
// recently filed 10-K or 10-Q as of the given date.
//
// It checks two sources in order:
//  1. EntityCommonStockSharesOutstanding (DEI cover-page count)
//  2. CommonStockSharesOutstanding (us-gaap, captured from Class A and
//     Class B dimensional contexts for multi-class filers like BRK/B)
//
// The most recently filed value across both sources is used. For multi-class
// filers, the selected filing may contain one fact per share class; all
// facts from that same concept and filing date are summed so the returned
// value is the total raw share count. Stale values (filed more than 2 years
// before asOfDate) are ignored so that old DEI entries from companies that
// stopped reporting the tag don't persist.
//
// Returns (0, false) when no suitable fact is found.
func resolveSharesBasicAsOf(cf *CompanyFacts, asOfDate time.Time) (float64, bool) {
	staleThreshold := asOfDate.AddDate(-2, 0, 0)

	var (
		bestConcept string
		bestFiled   time.Time
		found       bool
	)

	for _, conceptName := range []string{
		"EntityCommonStockSharesOutstanding",
		"CommonStockSharesOutstanding",
	} {
		facts, ok := cf.Facts[conceptName]
		if !ok {
			continue
		}

		for i := range facts {
			f := &facts[i]

			if f.Form != "10-K" && f.Form != "10-Q" {
				continue
			}

			if f.Filed.After(asOfDate) {
				continue
			}

			if f.Filed.Before(staleThreshold) {
				continue
			}

			if !found || f.Filed.After(bestFiled) {
				bestConcept = conceptName
				bestFiled = f.Filed
				found = true
			}
		}
	}

	if !found {
		return 0, false
	}

	// Sum all facts from the same concept with the same filing date. For a
	// single-class filer this is a single fact; for a multi-class filer the
	// filing reports one fact per class (Class A + Class B), and Sharadar's
	// shares_basic matches the sum of raw counts across classes.
	total := 0.0

	for i := range cf.Facts[bestConcept] {
		f := &cf.Facts[bestConcept][i]
		if f.Filed.Equal(bestFiled) && (f.Form == "10-K" || f.Form == "10-Q") {
			total += f.Val
		}
	}

	return total, true
}

// resolveClassSharesAsOf returns the Class A and Class B raw cover-page share
// counts from the most recently filed 10-K or 10-Q on or before asOfDate.
// Returns (filed, classA, classB, true) when a filing with BOTH class values
// is available, or zero values with false otherwise.
//
// When a filing reports multiple source concepts (e.g. dei:
// EntityCommonStockSharesOutstanding on the cover-page instant and
// us-gaap:CommonStockSharesOutstanding on the period-end balance sheet),
// EntityCommonStockSharesOutstanding is preferred because Sharadar's
// shares_basic matches the cover-page sum, not the balance-sheet instant.
//
// Used by the market-ratio share_factor formula for multi-class filers:
//
//	share_factor = (A*A_price + B*B_price) / ((A+B) * our_price)
//
// where our_price is the price of the traded class being processed.
func resolveClassSharesAsOf(cf *CompanyFacts, asOfDate time.Time) (filed time.Time, classA, classB float64, ok bool) {
	type filingKey struct {
		filed time.Time
		form  string
	}

	// For each filing, prefer EntityCommonStockSharesOutstanding; fall back
	// to CommonStockSharesOutstanding only if Entity data is absent for that
	// class. Track per-class values separately with the preferred concept.
	type classPair struct {
		A, B        float64
		AFromEntity bool
		BFromEntity bool
	}

	pairs := make(map[filingKey]classPair)

	for _, csf := range cf.ClassShares {
		if csf.Form != "10-K" && csf.Form != "10-Q" {
			continue
		}

		if csf.Filed.After(asOfDate) {
			continue
		}

		k := filingKey{filed: csf.Filed, form: csf.Form}
		p := pairs[k]

		isEntity := csf.Concept == "EntityCommonStockSharesOutstanding"

		switch csf.Class {
		case "A":
			if isEntity || !p.AFromEntity {
				p.A = csf.Val
				p.AFromEntity = isEntity
			}
		case "B":
			if isEntity || !p.BFromEntity {
				p.B = csf.Val
				p.BFromEntity = isEntity
			}
		}

		pairs[k] = p
	}

	for k, p := range pairs {
		if p.A <= 0 || p.B <= 0 {
			continue
		}

		if !ok || k.filed.After(filed) {
			filed = k.filed
			classA = p.A
			classB = p.B
			ok = true
		}
	}

	return
}

// applyMRComparativeFilter zeroes out MR values for quarterly flow fields
// where the XBRL concept has NO fact for the given periodEnd filed in a
// subsequent-year filing. This matches Sharadar's "MR quarterly requires a
// comparative in a later-year filing" semantics: if the concept is
// discontinued (e.g. BRK stopped reporting PaymentsToAcquireBusinessesNet
// OfCashAcquired in 2025 10-Qs), the older Q1-Q3 MR values go to 0 rather
// than inheriting from the original 10-Q.
//
// The filter only fires when a subsequent-year filing EXISTS for the
// company (any concept, filed more than 11 months after periodEnd). For
// the most recent quarter where no Y+1 filing has been produced yet, the
// original fact is still used — matching Sharadar's MR = AR behavior for
// the freshest period.
func applyMRComparativeFilter(cf *CompanyFacts, fields map[string]float64, periodEnd time.Time) {
	// Require at least one fact (any concept) filed more than 11 months
	// after periodEnd. Below this threshold, no Y+1 filing has landed and
	// the original fact is still authoritative.
	cutoff := periodEnd.AddDate(0, 11, 0)
	hasSubsequent := false

	for _, facts := range cf.Facts {
		for i := range facts {
			if facts[i].Filed.After(cutoff) {
				hasSubsequent = true

				break
			}
		}

		if hasSubsequent {
			break
		}
	}

	if !hasSubsequent {
		return
	}

	// Build field-by-name lookup for operand resolution.
	byName := make(map[string]*FieldMapping, len(FieldMappings))
	for i := range FieldMappings {
		byName[FieldMappings[i].FieldName] = &FieldMappings[i]
	}

	// collectLeafTags walks a derived field's operand tree and collects
	// every underlying XBRL tag (its own XBRLTags/FallbackTags plus the
	// operands' XBRLTags/FallbackTags, recursively). This lets the filter
	// treat "SGA = G&A + S&M" as having comparatives whenever either
	// G&A or S&M was re-reported in a subsequent filing, even if the
	// company never reports the aggregate SellingGeneralAnd... tag itself.
	var collectLeafTags func(fieldName string, visited map[string]bool) []string

	collectLeafTags = func(fieldName string, visited map[string]bool) []string {
		if visited[fieldName] {
			return nil
		}

		visited[fieldName] = true

		fm, ok := byName[fieldName]
		if !ok {
			return nil
		}

		tags := append([]string(nil), fm.XBRLTags...)
		tags = append(tags, fm.FallbackTags...)

		for _, op := range fm.Operands {
			tags = append(tags, collectLeafTags(op, visited)...)
		}

		return tags
	}

	for _, m := range FieldMappings {
		if m.StatementType != StmtFlow || m.ValueType != "int64" {
			continue
		}

		if _, ok := fields[m.FieldName]; !ok {
			continue
		}

		tags := collectLeafTags(m.FieldName, make(map[string]bool))
		if len(tags) == 0 {
			continue
		}

		hasComparative := false

		for _, tag := range tags {
			if tag == "" {
				continue
			}

			for i := range cf.Facts[tag] {
				f := &cf.Facts[tag][i]
				if f.End.Equal(periodEnd) && f.Filed.After(cutoff) {
					hasComparative = true

					break
				}
			}

			if hasComparative {
				break
			}
		}

		if !hasComparative {
			fields[m.FieldName] = 0
		}
	}
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
	// Banks lack AssetsCurrent (no current/non-current classification) AND
	// report Deposits (a bank-specific liability). Insurance conglomerates
	// like BRK/B also lack AssetsCurrent but don't have Deposits.
	lacksAssetsCurrent := !conceptFiledQuarterly(cf, []string{"AssetsCurrent"})
	isBank := lacksAssetsCurrent &&
		conceptFiledQuarterly(cf, []string{"Deposits", "DepositsDomestic", "DepositsTotal"})
	isInsuranceConglomerate := lacksAssetsCurrent && !isBank
	bundlesFinancing := conceptFiledQuarterly(cf, []string{"AccruedLiabilitiesCurrent"})

	if !isBank && !isInsuranceConglomerate && !bundlesFinancing {
		return
	}

	financing, hasF := fields["NetCashFlowFromFinancing"]
	common, hasC := fields["NetCashFlowCommon"]
	dividend, hasD := fields["NetCashFlowDividend"]

	if hasF && hasC && hasD {
		fields["NetCashFlowDebt"] = financing - common - dividend
	}

	// For banks, recompute derived income fields from their Q4 components.
	// The Q4 synthesis computes each field independently (Annual - Q1-Q2-Q3),
	// but when NetIncome uses the cumulative path (43,199) while the Q1-Q3
	// EBT sum uses single-quarter NI (43,197), derived fields like EBT/EBIT
	// get a 2M inconsistency. Recomputing from formulas ensures consistency.
	if isBank {
		if ni, hasNI := fields["NetIncome"]; hasNI {
			tax := fields["IncomeTaxExpense"]
			intExp := fields["InterestExpense"]
			da := fields["DepreciationAmortizationAndAccretion"]

			fields["EBT"] = ni + tax
			fields["EBIT"] = ni + tax + intExp
			fields["EBITDA"] = ni + tax + intExp + da

			// Recompute NCI from corrected values. Use NetIncomeCommonStock
			// (after preferred) rather than NetIncome: for companies that
			// report PreferredStockDividendsIncomeStatementImpact (GS), NetIncome
			// resolves to NetIncomeLoss (before preferred) and would make the
			// identity ConsolidatedIncome - NetIncome - Preferred double-count
			// the preferred deduction.
			if consolidated, hasCons := fields["ConsolidatedIncome"]; hasCons {
				niCommon, hasNIC := fields["NetIncomeCommonStock"]
				if !hasNIC {
					niCommon = ni
				}

				pref := fields["PreferredDividendsIncomeStatementImpact"]
				fields["NetIncomeToNonControllingInterests"] = consolidated - niCommon - pref
			}
		}
	}

	// For banks, override NCFDEBT with a direct computation from de-cumulated
	// bank-specific fields. The residual approach doesn't work for banks
	// because the financing total includes deposits, preferred stock, and
	// other non-debt items. The sub-fields (_bankFedFundsChange, etc.) are
	// mapped as StmtFlow in FieldMappings so YTD cumulative values are
	// properly de-cumulated before reaching this point.
	if isBank {
		// GS-style banks file their financing cash flows as separate unsecured/
		// secured tranches plus extension short-term-net concepts, rather than
		// the standard ProceedsFromIssuanceOfLongTermDebt + FedFundsChange tags
		// that commercial banks (JPM) use. Detect via CustomerAndOtherPayables.
		_, isGSStyle := cf.Facts["CustomerAndOtherPayables"]

		var bankDebtFields []struct {
			name string
			sign float64
		}

		if isGSStyle {
			// NB: PaymentsForProceedsFromDerivativeInstrumentFinancingActivities
			// is presented in GS's financing section but Sharadar classifies
			// it as "other financing" rather than debt, so it is NOT added
			// here.
			bankDebtFields = []struct {
				name string
				sign float64
			}{
				{"_bdUnsecuredDebtProceeds", 1},
				{"_bdUnsecuredDebtRepayments", -1},
				{"_bdSecuredDebtProceeds", 1},
				{"_bdSecuredDebtRepayments", -1},
				{"_bdUnsecuredSTDebtNet", 1},
				{"_bdOtherSecuredSTDebtNet", 1},
			}
		} else {
			bankDebtFields = []struct {
				name string
				sign float64
			}{
				{"_bankFedFundsChange", 1},
				{"_bankLTDebtProceeds", 1},
				{"_bankLTDebtRepayments", -1},
				{"_bankSTDebtProceeds", 1},
			}
		}

		ncfDebt := 0.0
		found := false

		for _, f := range bankDebtFields {
			if v, ok := fields[f.name]; ok {
				ncfDebt += f.sign * v
				found = true
			}
		}

		if found {
			fields["NetCashFlowDebt"] = ncfDebt
		}
	}

	// Ensure NetCashFlowBusiness is present in the fields map (even as 0) so
	// the strictFlow TTM check passes. Without this, quarters where no
	// acquisitions occurred have the field absent, causing the MRT TTM to
	// skip it entirely (found < 4 with strictFlow=true). For insurance
	// conglomerates like BRK/B, also zero-fill NetCashFlowCommon: Q1 2025
	// had no repurchases/issuances/tax-withholding, so all operands of the
	// derived field were absent and the formula returned NULL.
	if isBank || isInsuranceConglomerate {
		if _, ok := fields["NetCashFlowBusiness"]; !ok {
			fields["NetCashFlowBusiness"] = 0
		}
	}

	if isInsuranceConglomerate {
		if _, ok := fields["NetCashFlowCommon"]; !ok {
			fields["NetCashFlowCommon"] = 0
		}
	}

	// GS-style investment banks present "Total operating expenses" as
	// NoninterestExpense + ProvisionForLoanLeaseAndOtherLosses on the face of
	// the income statement (unlike commercial banks which keep provision as a
	// separate line). Sharadar's OperatingExpenses matches this face-of-the-
	// statement total, so add provision when the _bdBankProvision field was
	// resolved (gated by the CustomerAndOtherPayables marker). Using the
	// resolved field rather than re-reading from cf ensures the quarterly
	// de-cumulated value flows into Q4 synthesis correctly. Set a sentinel so
	// a second call to this function (sec.go now calls it on quarter maps
	// before annual averages AND again before emission) does not double-add.
	if isBank {
		if provision, ok := fields["_bdBankProvision"]; ok {
			if _, alreadyApplied := fields["_bdBankProvisionApplied"]; !alreadyApplied {
				if opex, hasOE := fields["OperatingExpenses"]; hasOE {
					fields["OperatingExpenses"] = opex + provision
					fields["_bdBankProvisionApplied"] = 1
				}
			}
		}
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
			capex := fields["CapitalExpenditure"]     // 0 when absent (banks)
			otherInv := fields["_bankOtherInvesting"] // 0 when absent
			biz := fields["NetCashFlowBusiness"]      // 0 when absent; already negated
			// Add back "other" investing and subtract business acquisitions
			// (both are in the investing total but Sharadar classifies them
			// separately as NCFBIZ and other, not NCFINV).
			fields["NetCashFlowInvest"] = investing - capex + otherInv - biz
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
				"GoodwillServicingAssetsAtFairValueAndOtherIntangibleAssets", // 10-Q
				"GoodwillServicingAssetsatFairValueandOtherIntangibleAssets", // 10-K
			}},
			{"PropertyPlantAndEquipmentNet", []string{
				"PropertyPlantAndEquipmentAndOperatingLeaseRightOfUseAssetAfterAccumulatedDepreciationAndAmortization",
			}},
			{"Receivables", []string{"AccruedInterestAndAccountsReceivable"}},
			// GS reports customer-and-other payables as the balance sheet
			// payables line; us-gaap AccountsPayable* tags are absent.
			{"Payables", []string{"CustomerAndOtherPayables"}},
		}

		for _, ext := range bankExtTags {
			if v, ok := ResolveDirect(cf, FieldMapping{XBRLTags: ext.tags}, periodEnd, formType); ok {
				fields[ext.field] = v
			}
		}

		// GS-style receivables: the "Customer and other receivables" balance
		// sheet line maps to us-gaap:OtherReceivables. The standard chain
		// resolves to NotesReceivableNet (loans receivable), which is a
		// different line for GS. Gate on CustomerAndOtherPayables to avoid
		// affecting other banks that also file OtherReceivables but resolve
		// correctly via the default path.
		if _, ok := cf.Facts["CustomerAndOtherPayables"]; ok {
			if v, rok := ResolveDirect(cf, FieldMapping{XBRLTags: []string{"OtherReceivables"}}, periodEnd, formType); rok {
				fields["Receivables"] = v
			}
		}

		// GS-style total debt: Sharadar sums the interest-bearing wholesale
		// liabilities visible on the balance sheet — unsecured borrowings
		// (short + long), collateralized financings (repo + securities loaned
		// + other secured), and trading liabilities. Deposits are tracked in
		// the separate `deposits` field. The default bank formula
		// (ShortTermBorrowings + LongTermDebt + FedFundsPurchased) doesn't
		// cover GS because GS reports UnsecuredLongTermDebt/UnsecuredDebtCurrent
		// rather than LongTermDebt, and its repo/sec-loaned/trading-liab lines
		// are separate. Gate on CustomerAndOtherPayables so JPM/BAC (which
		// match via the default formula) aren't disturbed.
		if _, ok := cf.Facts["CustomerAndOtherPayables"]; ok {
			gsDebtTags := []string{
				"DebtLongtermAndShorttermCombinedAmount",
				"SecuritiesSoldUnderAgreementsToRepurchase",
				"SecuritiesLoaned",
				"OtherSecuredFinancings",
				"TradingLiabilities",
			}

			total := 0.0
			found := false

			for _, tag := range gsDebtTags {
				if v, tok := ResolveDirect(cf, FieldMapping{XBRLTags: []string{tag}}, periodEnd, formType); tok {
					total += v
					found = true
				}
			}

			if found {
				fields["TotalDebt"] = total

				// Recompute fields that depend on TotalDebt:
				// InvestedCapital = TotalDebt + TotalAssets - Intangibles - Cash - CurrentLiabilities
				// (banks have CurrentLiabilities = 0).
				if ta, hasTA := fields["TotalAssets"]; hasTA {
					intang := fields["Intangibles"]
					cash := fields["CashAndEquivalents"]
					curLiab := fields["CurrentLiabilities"]
					fields["InvestedCapital"] = total + ta - intang - cash - curLiab
				}
			}
		}
	}

	// For banks, compute SGA. Commercial banks (JPM, BAC, WFC): SGA =
	// NoninterestExpense - OtherNoninterestExpense (Sharadar excludes the
	// catch-all "Other" bucket of FDIC assessments, regulatory fees,
	// litigation). GS-style investment banks: SGA = NoninterestExpense -
	// DepreciationAndAmortization - ProfessionalFees -- Sharadar classifies
	// both D&A and professional fees outside SGA for these filers. For GS
	// the OperatingExpenses field has Provision added in the step above, so
	// subtract it back out to get raw NoninterestExpense before applying the
	// GS-specific deductions.
	if isBank {
		nonintExp, hasNIE := fields["OperatingExpenses"] // resolved from NoninterestExpense FallbackTag
		if hasNIE {
			if _, isGSStyle := cf.Facts["CustomerAndOtherPayables"]; isGSStyle {
				rawNIE := nonintExp - fields["_bdBankProvision"] // undo provision addition
				da := fields["DepreciationAmortizationAndAccretion"]
				profFees := fields["_bdProfessionalFees"]
				fields["SellingGeneralAndAdministrativeExpense"] = rawNIE - da - profFees
			} else {
				otherNIE := fields["_bankOtherNoninterestExpense"] // 0 when absent
				fields["SellingGeneralAndAdministrativeExpense"] = nonintExp - otherNIE
			}
		}
	}

	// For banks, compute Investments as the balance sheet residual:
	// TotalAssets - CashAndEquivalents - Receivables - PP&E - Intangibles - OtherAssets.
	// This captures loans, securities, fed funds, and other invested assets.
	if isBank {
		totalAssets, hasTA := fields["TotalAssets"]

		if hasTA {
			cash := fields["CashAndEquivalents"]
			recv := fields["Receivables"]
			ppe := fields["PropertyPlantAndEquipmentNet"]
			intang := fields["Intangibles"]
			oa := 0.0

			if v, ok := ResolveDirect(cf, FieldMapping{XBRLTags: []string{"OtherAssets"}}, periodEnd, formType); ok {
				oa = v
			}

			fields["Investments"] = totalAssets - cash - recv - ppe - intang - oa
		}
	}
}

// deriveCostOfRevenueBottomUp recomputes income statement fields for companies
// that don't report CostOfRevenue or OperatingIncomeLoss directly (insurance
// and conglomerate companies like BRK/B). When CostOfRevenue is missing but
// OperatingIncome and SGA are available, the income statement is reconstructed
// bottom-up:
//
//	OperatingExpenses = SGA + R&D
//	GrossProfit = OperatingIncome + OperatingExpenses
//	CostOfRevenue = Revenues - GrossProfit
func deriveCostOfRevenueBottomUp(fields map[string]float64) {
	// Only apply when CostOfRevenue is missing and OperatingIncome is present.
	if _, hasCOR := fields["CostOfRevenue"]; hasCOR {
		return
	}

	opIncome, hasOI := fields["OperatingIncome"]
	if !hasOI {
		return
	}

	sga := fields["SellingGeneralAndAdministrativeExpense"] // 0 when absent
	rnd := fields["RandDExpenses"]                          // 0 when absent
	otherExp := fields["_otherExpenses"]                    // 0 when absent; Railroad segment for BRK

	// Need at least SGA to derive the income statement.
	if sga == 0 {
		return
	}

	opEx := sga + rnd + otherExp
	grossProfit := opIncome + opEx
	revenue, hasRev := fields["Revenues"]

	if !hasRev || revenue == 0 {
		return
	}

	costOfRevenue := revenue - grossProfit

	// Sanity check: COGS should be positive. For insurance/conglomerate
	// companies where revenue includes investment gains/losses, COGS can
	// legitimately exceed revenue in years with large unrealized losses
	// (e.g. BRK 2022: COGS 238B > Revenue 234B due to -53B unrealized
	// investment losses). So only check the lower bound.
	if costOfRevenue < 0 {
		return
	}

	fields["CostOfRevenue"] = costOfRevenue
	fields["GrossProfit"] = grossProfit
	fields["OperatingExpenses"] = opEx

	// Recompute GrossMargin from the corrected GrossProfit/Revenues.
	// ResolveAllFields computed it earlier using GrossProfit=Revenues
	// (CostOfRevenue defaulted to 0 via OptionalOperands), yielding 1.0.
	if revenue != 0 {
		fields["GrossMargin"] = math.Round(grossProfit/revenue*1000) / 1000
	}
}
