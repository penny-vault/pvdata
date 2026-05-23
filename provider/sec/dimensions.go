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
	"strings"
	"time"

	"github.com/penny-vault/pvdata/data"
)

// TTM span limits accommodate 52/53-week filers (KO's 4-quarter span dips to
// 269 days in some years) while rejecting spans too short or long to represent
// twelve months of activity.
const (
	ttmMinSpanDays = 265
	ttmMaxSpanDays = 410
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

	// First pass: group raw facts by exact (end, form) pair so we collect
	// AR/MR filed dates per raw period end. Only duration concepts drive
	// period identification; instants supplement existing periods during
	// field resolution but must not create new periods on their own.
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

	// Filter out spurious 10-Q periods that collide with a 10-K at the same
	// normalized date, while keeping legitimate 10-Q periods that merely share
	// a normalized date with a 10-K because of calendar-normalization.
	const sameFiscalEndDays = 10

	periods := make([]Period, 0, len(dedupedPeriods))
	for _, p := range dedupedPeriods {
		if p.FormType == "10-Q" {
			normalEnd := NormalizeEventDate(p.PeriodEnd, p.FormType)
			annualKey := periodKey{end: normalEnd, form: "10-K"}

			if annual, hasAnnual := dedupedPeriods[annualKey]; hasAnnual {
				// Case 1: raw PeriodEnd far from the normalized quarter end.
				// Only applies when the 10-K's raw PeriodEnd is itself close
				// to the normalized date (December-fiscal-year filers). For
				// non-December filers (e.g. CALM, May FYE), the 10-K's raw
				// PeriodEnd sits far from its normalized year-end, and a
				// 10-Q at the same normalized date with raw end far from
				// normalized is a legitimate fiscal Q2/Q3 — not a spurious
				// transaction-date collision.
				dist := absDuration(p.PeriodEnd.Sub(normalEnd))
				annualRawDist := absDuration(annual.PeriodEnd.Sub(normalEnd))

				if dist.Hours()/24 > 10 && annualRawDist.Hours()/24 <= 10 {
					continue
				}

				// Case 2: raw PeriodEnd coincides with the 10-K's raw
				// PeriodEnd (same fiscal year-end).
				annualDist := absDuration(p.PeriodEnd.Sub(annual.PeriodEnd))
				if annualDist.Hours()/24 <= sameFiscalEndDays {
					continue
				}
			}
		}

		periods = append(periods, *p)
	}

	sort.Slice(periods, func(i, j int) bool {
		return periods[i].PeriodEnd.Before(periods[j].PeriodEnd)
	})

	return periods
}

// NormalizeEventDate snaps a raw period end to Sharadar's EventDate convention.
// Quarterly forms snap to the nearest calendar quarter end (ties forward).
// Annual forms snap to 12/31 of the year determined by the nearest quarter
// end, handling January-ending fiscal years (NVDA FY2025 ending 2025-01-26
// snaps to 2024-12-31).
func NormalizeEventDate(periodEnd time.Time, formType string) time.Time {
	if formType == "10-K" {
		qe := nearestQuarterEnd(periodEnd)
		return time.Date(qe.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
	}

	return nearestQuarterEnd(periodEnd)
}

// nearestQuarterEnd snaps a date to the nearest calendar quarter end
// (3/31, 6/30, 9/30, 12/31). Ties are broken by snapping forward.
func nearestQuarterEnd(periodEnd time.Time) time.Time {
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

// quarterEndCandidates returns the bracketing calendar quarter ends, ordered
// latest first so ties break by snapping forward.
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

// ResolveFieldsForFiling resolves all fields using only facts filed on or
// before filedDate, producing AR or MR views of the same period.
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

	// AllowCrossFormFallback concepts: if the filtered window is silent on
	// the concept at periodEnd, splice in post-filedDate facts at the same
	// period end so the cross-form fallback has something to find. MCD's
	// 2024 10-K omits operating-lease liabilities and ROU; the first 10-Q
	// comparatives that tag them come months after the 10-K filing date,
	// so the strict AR window misses them entirely. Sharadar uses those
	// 10-Q values at the 10-K period end. This only touches concepts whose
	// mapping opts into AllowCrossFormFallback and only adds facts at the
	// requested periodEnd, so it can't leak unrelated restatements into AR.
	// Compare on exact End dates rather than NormalizeEventDate, since the
	// 10-K normalization collapses every quarter-end in the year to Dec 31
	// and would spuriously treat e.g. a 2024-06-30 10-Q fact as matching a
	// 2024-12-31 10-K request.
	for _, m := range FieldMappings {
		if !m.AllowCrossFormFallback {
			continue
		}

		for _, tag := range m.XBRLTags {
			facts, ok := cf.Facts[tag]
			if !ok {
				continue
			}

			// If the filtered window already has a fact at this exact
			// period end, don't extend. Cross-form fallback covers the
			// rest without needing a splice.
			haveMatch := false

			for _, f := range filtered.Facts[tag] {
				if f.End.Equal(periodEnd) {
					haveMatch = true
					break
				}
			}

			if haveMatch {
				continue
			}

			// Find the earliest post-filedDate fact at periodEnd. Facts
			// are sorted by Filed ascending, so the first match is the
			// earliest restatement that carried the concept.
			for i := range facts {
				f := &facts[i]
				if !f.Filed.After(filedDate) {
					continue
				}

				if !f.End.Equal(periodEnd) {
					continue
				}

				// Append a single synthetic fact so ResolveDirect can find
				// it. Copy the slice to avoid aliasing with cf.Facts (which
				// ResolveFieldsForFiling otherwise shares).
				extended := make([]Fact, len(filtered.Facts[tag]), len(filtered.Facts[tag])+1)
				copy(extended, filtered.Facts[tag])
				extended = append(extended, *f)
				filtered.Facts[tag] = extended

				break
			}
		}
	}

	return ResolveAllFields(filtered, periodEnd, formType)
}

// ResolveCumulativePerShareForFiling resolves YTD cumulative per-share flow
// values (EPS, EPSDiluted, DPS) using longest-duration facts. Needed for Q4
// synthesis to avoid rounding error from summing per-share quarterly values.
func ResolveCumulativePerShareForFiling(cf *CompanyFacts, periodEnd time.Time, formType string, filedDate time.Time) map[string]float64 {
	if formType != "10-Q" {
		return nil
	}

	filtered := &CompanyFacts{
		CIK:        cf.CIK,
		EntityName: cf.EntityName,
		Facts:      make(map[string][]Fact, len(cf.Facts)),
	}

	for concept, facts := range cf.Facts {
		idx := sort.Search(len(facts), func(i int) bool {
			return facts[i].Filed.After(filedDate)
		})

		if idx > 0 {
			filtered.Facts[concept] = facts[:idx]
		}
	}

	result := make(map[string]float64)

	for _, m := range FieldMappings {
		// Flow fields: YTD cumulative avoids rounding error from summing
		// individually rounded quarterly values, and captures any cross-
		// quarter restatements (e.g. JPM Q3 cumulative 43,199M vs single-
		// quarter sum 43,197M). Period-average fields: the company-reported
		// YTD average captures day-weighted precision.
		switch m.StatementType {
		case StmtFlow, StmtPeriodAverage:
		default:
			continue
		}

		// Skip derived fields whose FallbackTags were gated off during
		// resolution. ResolveLongestDuration uses FallbackTags, which
		// would resolve the raw XBRL tag without the formula adjustment
		// (e.g. Revenues without GainLossOnInvestments for BRK/B). The
		// Q4 synthesis fallback (sum of de-cumulated quarters) handles
		// these fields correctly.
		if m.Type == MappingDerived && len(m.FallbackRequireIfQuarterly) > 0 &&
			!conceptFiledQuarterly(cf, m.FallbackRequireIfQuarterly) {
			continue
		}

		// FallbackExcludeIfQuarterly: when the gate sentinel is filed
		// quarterly, the field's FallbackTags are disabled in
		// ResolveAllFields and the formula wins. Skip the YTD cumulative
		// lookup here for the same reason (the FallbackTag's YTD would
		// disagree with the formula's annual; see WMT InterestExpense).
		if m.Type == MappingDerived && len(m.FallbackExcludeIfQuarterly) > 0 &&
			conceptFiledQuarterly(cf, m.FallbackExcludeIfQuarterly) {
			continue
		}

		// Gate direct-mapped operands the same way ResolveAllFields does
		// so cumulative YTD values don't contradict the point-in-time
		// resolution. Without this, _interestIncomeExpenseNetFallback
		// would resolve here (its ExcludeIfQuarterly is only checked in
		// ResolveAllFields), then double-count when InterestExpense is
		// recomputed via its formula in the second pass below.
		if m.RequireQuarterly && !conceptFiledQuarterly(cf, m.XBRLTags) {
			continue
		}

		if len(m.ExcludeIfQuarterly) > 0 && conceptFiledQuarterly(cf, m.ExcludeIfQuarterly) {
			continue
		}

		if len(m.ExcludeIfAnnual) > 0 && conceptFiledAnnually(cf, m.ExcludeIfAnnual) {
			continue
		}

		if len(m.RequireIfQuarterly) > 0 && !conceptFiledQuarterly(cf, m.RequireIfQuarterly) {
			continue
		}

		// Require the winning fact to span more than one quarter. Otherwise
		// we'd return the single-quarter value dressed up as "cumulative",
		// which synthesize_q4 would subtract from the annual to produce a
		// wrong Q4 (Annual - Q3_single instead of Annual - Q1Q2Q3_sum).
		// The threshold admits Q2 YTD (~180d) and Q3 YTD (~270d) while
		// rejecting Q1-only 10-Qs where YTD == single quarter.
		if val, ok := resolveLongestDurationBounded(filtered, m, periodEnd, formType, ytdThresholdDays+1); ok {
			if m.Negate {
				val = -val
			}

			result[m.FieldName] = val
		}
	}

	// Second pass: for derived flow fields whose FallbackTags were gated
	// off during the first pass (FallbackRequireIfQuarterly not met),
	// compute the YTD cumulative from operands instead. This preserves
	// cross-quarter adjustments captured by the operand's company-reported
	// YTD value -- e.g. NetIncome for JPM where the formula path uses
	// NetIncomeCommonStock, whose YTD cumulative (43,199M at Q3) differs
	// slightly from the sum of individually rounded quarters (43,197M).
	// Only fields with this gate-off pattern are recomputed; other derived
	// fields (e.g. NetCashFlowCommon for AAPL) intentionally do not appear
	// in the cumulative map so Q4 synthesis falls back to sum-of-quarters.
	for _, m := range FieldMappings {
		if m.Type != MappingDerived || m.StatementType != StmtFlow {
			continue
		}

		if _, exists := result[m.FieldName]; exists {
			continue
		}

		requireGateSkipped := len(m.FallbackRequireIfQuarterly) > 0 &&
			!conceptFiledQuarterly(cf, m.FallbackRequireIfQuarterly)
		excludeGateSkipped := len(m.FallbackExcludeIfQuarterly) > 0 &&
			conceptFiledQuarterly(cf, m.FallbackExcludeIfQuarterly)

		if !requireGateSkipped && !excludeGateSkipped {
			continue
		}

		if val, ok := computeDerived(m, result); ok {
			result[m.FieldName] = val
		}
	}

	// Apply the same SGA override used by ResolveAllFields so the YTD
	// cumulative for SellingGeneralAndAdministrativeExpense agrees with the
	// annual value on KO-style product-sales filers. Without this, Q4
	// synthesis subtracts the OtherSGA YTD (carve-out) from the SGA annual
	// (main line), inflating the Q4 value by the difference.
	overrideCumPSForSmallOtherSGA(cf, result, periodEnd, formType)

	// Apply the preferred-dividends mezzanine-accretion override (same as
	// ResolveAllFields) to the YTD cumulative map so Q4 synthesis subtracts
	// a matching YTD total. Without this, annual = 37.6M (with override) but
	// YTD Q3 = 20.6M (explicit tag only), so Q4 = 17M instead of ~4.6M.
	overridePreferredDividendsForMezzanineAccretion(cf, result)

	// Apply the footnote-only R&D override to the YTD cumulative map so Q4
	// synthesis subtracts a matching zero. Without this, the annual R&D is
	// zeroed but Q3 YTD still reports the tag value, producing a spurious
	// negative Q4 value via Annual - YTD_Q3.
	overrideRandDExpensesForFootnoteOnly(result)

	return result
}

// overrideCumPSForSmallOtherSGA is the YTD-cumulative-map counterpart of
// overrideSGAForSmallOtherSGA. Applied only when YTD is already populated
// and the company isn't a franchisor.
func overrideCumPSForSmallOtherSGA(cf *CompanyFacts, result map[string]float64, periodEnd time.Time, formType string) {
	if _, ok := result["SellingGeneralAndAdministrativeExpense"]; !ok {
		return
	}

	if _, ok := cf.Facts["FranchiseRevenue"]; ok {
		return
	}

	if _, ok := cf.Facts["FranchiseCosts"]; ok {
		return
	}

	if val, ok := resolveLongestDurationBounded(cf, FieldMapping{
		XBRLTags:      []string{"SellingGeneralAndAdministrativeExpense"},
		StatementType: StmtFlow,
	}, periodEnd, formType, ytdThresholdDays+1); ok && val > 0 {
		result["SellingGeneralAndAdministrativeExpense"] = val
	}
}

// identifyStaleMRFields returns field names whose XBRL concepts are no longer
// reported (absent from the latest 10-K and any subsequent 10-Q, plus an
// instant-concept check against the latest 10-Q).
func identifyStaleMRFields(cf *CompanyFacts) map[string]bool {
	// Find the latest 10-K and latest 10-Q filing dates.
	var latest10KFiled, latest10QFiled time.Time

	for _, facts := range cf.Facts {
		for i := range facts {
			if facts[i].Form == "10-K" && facts[i].Filed.After(latest10KFiled) {
				latest10KFiled = facts[i].Filed
			}

			if facts[i].Form == "10-Q" && facts[i].Filed.After(latest10QFiled) {
				latest10QFiled = facts[i].Filed
			}
		}
	}

	if latest10KFiled.IsZero() {
		return nil
	}

	// Concepts present in the latest 10-K filing or in any 10-Q filed on or
	// after that 10-K date. For flow-style concepts, also include any 10-Q
	// filed within the 18 months before the latest 10-K so that non-recurring
	// flows (only tagged in active-event quarters) are not treated as stale.
	flowLookback := latest10KFiled.AddDate(-1, -6, 0)
	activeConcepts := make(map[string]bool)
	latestQConcepts := make(map[string]bool)

	for concept, facts := range cf.Facts {
		for i := range facts {
			f := &facts[i]
			if f.Form == "10-K" && f.Filed.Equal(latest10KFiled) {
				activeConcepts[concept] = true
			}

			if f.Form == "10-Q" && !f.Filed.Before(latest10KFiled) {
				activeConcepts[concept] = true
			}

			// Widened window for flow concepts: any 10-Q with a start/end
			// duration (distinguishes flow from point-in-time) filed
			// within 18 months before the latest 10-K counts as active.
			if f.Form == "10-Q" && !f.Start.IsZero() && !f.End.IsZero() &&
				f.End.After(f.Start) && !f.Filed.Before(flowLookback) {
				activeConcepts[concept] = true
			}

			// Track concepts that appear in the LATEST 10-Q. Balance-sheet
			// instant concepts get an additional staleness check below —
			// if present in older filings but dropped from the most recent
			// 10-Q, Sharadar treats them as stale.
			if f.Form == "10-Q" && f.Filed.Equal(latest10QFiled) {
				latestQConcepts[concept] = true
			}
		}
	}

	// If the latest 10-Q was filed after the latest 10-K, balance-sheet
	// (instant-style, zero Start) concepts must appear in that 10-Q to
	// remain active. Flow/duration concepts are unaffected by this
	// tightening (they follow the wider flowLookback window above).
	if !latest10QFiled.IsZero() && latest10QFiled.After(latest10KFiled) {
		for concept, facts := range cf.Facts {
			if !activeConcepts[concept] {
				continue
			}

			if latestQConcepts[concept] {
				continue
			}

			// Only consider instant-style concepts for tightening — they're
			// the balance-sheet lines Sharadar drops when a more recent 10-Q
			// omits them.
			hasInstant := false
			hasDuration := false

			for i := range facts {
				f := &facts[i]
				if f.Start.IsZero() {
					hasInstant = true
				} else if f.End.After(f.Start) {
					hasDuration = true
				}
			}

			if hasInstant && !hasDuration {
				delete(activeConcepts, concept)
			}
		}
	}

	// byName: field name → mapping, used to walk operand trees for derived fields.
	byName := make(map[string]*FieldMapping, len(FieldMappings))
	for i := range FieldMappings {
		byName[FieldMappings[i].FieldName] = &FieldMappings[i]
	}

	// collectLeafTags walks a derived field's operand tree and collects every
	// underlying us-gaap/extension XBRL tag (its own XBRLTags/FallbackTags plus
	// the operands' tags recursively). This lets staleness check derived
	// fields against the full operand taxonomy, not just the field's own
	// FallbackTags. For example, DeferredRevenue's FallbackTag
	// "DeferredRevenue" may be stale, but the formula still resolves via
	// ContractWithCustomerLiabilityCurrent (an operand's tag) which remains
	// active — so the field is NOT stale.
	var collectLeafTags func(name string, visited map[string]bool) []string

	collectLeafTags = func(name string, visited map[string]bool) []string {
		if visited[name] {
			return nil
		}

		visited[name] = true

		fm, ok := byName[name]
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

	// Map stale concepts (existed before but not in latest 10-K) to field names.
	staleFields := make(map[string]bool)

	for _, m := range FieldMappings {
		var tags []string
		if m.Type == MappingDerived {
			tags = collectLeafTags(m.FieldName, map[string]bool{})
		} else {
			tags = m.XBRLTags
		}

		allTagsStale := true
		anyTagExists := false

		for _, tag := range tags {
			if _, exists := cf.Facts[tag]; !exists {
				continue
			}

			anyTagExists = true

			if activeConcepts[tag] {
				allTagsStale = false

				break
			}
		}

		if anyTagExists && allTagsStale {
			staleFields[m.FieldName] = true
		}
	}

	if len(staleFields) == 0 {
		return nil
	}

	return staleFields
}

// stripStaleAndRecompute removes stale fields and recomputes derived fields
// whose operands changed, leaving fallback-resolved values intact when none
// of their operands moved.
func stripStaleAndRecompute(fields map[string]float64, stale map[string]bool, cf *CompanyFacts) {
	if len(stale) == 0 {
		return
	}

	for field := range stale {
		delete(fields, field)
	}

	// Track which fields have been affected (stale or recomputed) so that
	// downstream derived fields are also recomputed when needed.
	affected := make(map[string]bool, len(stale))
	for k := range stale {
		affected[k] = true
	}

	// Recompute derived fields only when at least one operand was affected
	// by staleness (directly or transitively).
	for _, m := range FieldMappings {
		if m.Type != MappingDerived {
			continue
		}

		// Check if this derived field needs recomputation.
		needsRecompute := affected[m.FieldName]
		if !needsRecompute {
			// For fields with FallbackTags that resolved successfully,
			// only recompute when the field itself is stale. The
			// FallbackTag value is authoritative and should not be
			// overridden just because a formula operand changed.
			hasFallback := len(m.FallbackTags) > 0

			_, wasResolved := fields[m.FieldName]
			if hasFallback && wasResolved && !stale[m.FieldName] {
				// FallbackTag resolved and the field is not stale -- keep it.
				continue
			}

			for _, op := range m.Operands {
				if affected[op] {
					needsRecompute = true
					break
				}
			}
		}

		if !needsRecompute {
			continue
		}

		if val, ok := computeDerived(m, fields); ok {
			fields[m.FieldName] = val
			affected[m.FieldName] = true
		} else if stale[m.FieldName] {
			// Only delete if the field itself was stale. Non-stale fields
			// that were resolved via FallbackTags (not the formula) should
			// be preserved when the formula fails due to absent operands.
			delete(fields, m.FieldName)
		}
	}
}

// DecumulateYTD subtracts the prior YTD field map from current to produce
// single-quarter values. Returns a copy; inputs are not modified.
func DecumulateYTD(cf *CompanyFacts, current, prior map[string]float64, priorPeriodEnd, periodEnd time.Time, formType string, filedDate time.Time) map[string]float64 {
	result := make(map[string]float64, len(current))
	for k, v := range current {
		result[k] = v
	}

	// Build a filtered view of cf limited to facts filed on or before filedDate.
	// needsDecumulation must see only the facts that existed when this filing
	// was made; otherwise a later amendment that adds a single-quarter fact for
	// the same period will flip the decumulation gate, leaving a YTD value
	// untouched and causing Q4 synthesis to produce nonsensical (often negative)
	// results.
	filteredCF := &CompanyFacts{
		CIK:        cf.CIK,
		EntityName: cf.EntityName,
		Facts:      make(map[string][]Fact, len(cf.Facts)),
	}

	for concept, facts := range cf.Facts {
		idx := sort.Search(len(facts), func(i int) bool {
			return facts[i].Filed.After(filedDate)
		})

		if idx > 0 {
			filteredCF.Facts[concept] = facts[:idx]
		}
	}

	// Pass 1: de-cumulate direct and fallback-resolved flow fields.
	for _, m := range FieldMappings {
		if m.StatementType != StmtFlow {
			continue
		}

		if !needsDecumulation(filteredCF, m, periodEnd, formType) {
			continue
		}

		currVal, hasCurr := current[m.FieldName]
		if !hasCurr {
			continue
		}

		// The prior map may hold a single-quarter value when
		// ResolveDirect picked the shortest-duration fact. We need the
		// prior's YTD cumulative for correct subtraction. Try
		// ResolveLongestDuration first; fall back to the prior map.
		priorVal, hasPrior := prior[m.FieldName]

		if hasPrior && m.Type == MappingDirect {
			if cumVal, ok := ResolveLongestDuration(cf, m, priorPeriodEnd, formType); ok {
				if m.Negate {
					cumVal = -cumVal
				}

				priorVal = cumVal
			}
		}

		if hasPrior {
			result[m.FieldName] = currVal - priorVal
		}
	}

	// Pass 2: recompute derived flow fields from (now de-cumulated) components.
	// This handles cases like FreeCashFlow = NetCashFlowFromOperations +
	// CapitalExpenditure (CapEx is negative) where both operands are cash
	// flow YTD values. Derived fields resolved via FallbackTags (e.g. D&A
	// from extension tags) are preserved unless their operands were
	// de-cumulated in pass 1.
	for _, m := range FieldMappings {
		if m.Type != MappingDerived || m.StatementType != StmtFlow {
			continue
		}

		// Preserve FallbackTag-resolved values: if the field has FallbackTags,
		// was already resolved, and none of its operands were de-cumulated,
		// keep the existing value. Skip this preservation when
		// FallbackRequireIfQuarterly prevented FallbackTags from being used
		// during initial resolution (the value came from the formula, not
		// FallbackTags).
		fallbackActive := len(m.FallbackTags) > 0
		if fallbackActive && len(m.FallbackRequireIfQuarterly) > 0 {
			fallbackActive = conceptFiledQuarterly(cf, m.FallbackRequireIfQuarterly)
		}

		if fallbackActive {
			if existingVal, exists := result[m.FieldName]; exists {
				operandChanged := false

				for _, op := range m.Operands {
					if _, changed := result[op]; changed {
						if origVal, hasCurr := current[op]; hasCurr {
							if resultVal, hasResult := result[op]; hasResult && resultVal != origVal {
								operandChanged = true

								break
							}
						}
					}
				}

				if !operandChanged {
					continue
				}

				// When operands changed but the FallbackTag resolved
				// successfully, preserve the FallbackTag value. The fallback
				// concept is the authoritative XBRL tag for this field;
				// recomputing from sub-operands risks losing components the
				// sub-operand mapping doesn't cover (e.g. BRK's D&A where
				// DepreciationDepletionAndAmortization fully reports D&A but
				// _amortizationOfIntangibles and _financeLeaseAmortization
				// are absent). Two cases both preserve:
				//   a) existingVal == origVal AND fallback is single-quarter
				//      (Pass 1 didn't de-cumulate the fallback value)
				//   b) existingVal != origVal (Pass 1 de-cumulated the
				//      fallback's YTD value — the decumulated value is the
				//      correct single-quarter fallback)
				// Only apply to non-internal fields (not "_" prefixed
				// sub-fields like _proceedsInvest whose FallbackTags may
				// have YTD data that needs recomputation from de-cumulated
				// sub-components).
				if !strings.HasPrefix(m.FieldName, "_") {
					fallbackResolved := false

					for _, tag := range m.FallbackTags {
						if _, ok := cf.Facts[tag]; ok {
							fallbackResolved = true

							break
						}
					}

					if fallbackResolved {
						if origVal, hasCurr := current[m.FieldName]; hasCurr {
							if existingVal != origVal {
								// Case (b): Pass 1 decumulated the fallback.
								continue
							}

							// Case (a): fallback must already be single-quarter.
							if !needsDecumulation(filteredCF, m, periodEnd, formType) {
								continue
							}
						}
					}
				}
			}
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

// ComputeTTM sums flow fields and takes the latest point-in-time value across
// 4 quarterly resolved sets. With strictFlow, flow fields require all 4
// quarters to be present (Sharadar MR behavior); otherwise missing = 0.
func ComputeTTM(quarters []map[string]float64, strictFlow bool) map[string]float64 {
	if len(quarters) < 4 {
		return nil
	}

	// Use the 4 most recent quarters
	recent := quarters[len(quarters)-4:]
	result := make(map[string]float64)

	for _, m := range FieldMappings {
		switch m.StatementType {
		case StmtFlow:
			// Sum quarters that report this field, treating missing quarters
			// as 0. This handles fields that a company stops reporting mid-
			// stream (e.g. Apple dropped InterestExpense after FY2023). The
			// trailing sum should still include the quarters that did report
			// the field. If NO quarter reports the field, skip it entirely
			// to avoid creating a spurious zero.
			sum := 0.0
			found := 0

			for _, q := range recent {
				if v, ok := q[m.FieldName]; ok {
					sum += v
					found++
				}
			}

			minRequired := 1
			if strictFlow {
				minRequired = 4
			}

			if found >= minRequired {
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
// annual dimensions). Returns a map containing only the computed fields;
// fields whose inputs are missing are silently omitted.
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

	round3 := func(v float64) float64 {
		return math.Round(v*1000) / 1000
	}

	if v, ok := ratio("NetIncomeCommonStock", "AverageAssets"); ok {
		result["ROA"] = round3(v)
	}

	if v, ok := ratio("NetIncomeCommonStock", "EquityAvg"); ok {
		result["ROE"] = round3(v)
	}

	if v, ok := ratio("EBIT", "InvestedCapitalAverage"); ok {
		result["ROIC"] = round3(v)
	}

	if v, ok := ratio("Revenues", "AverageAssets"); ok {
		result["AssetTurnover"] = round3(v)
	}

	return result
}

// BuildFundamental converts a resolved field map into a data.Fundamental.
// lastUpdated is the filing date so re-runs produce stable values.
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
