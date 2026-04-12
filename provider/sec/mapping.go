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

			// Prefer the fact with the latest filing date (most recent data).
			// When filing dates are equal, prefer the shorter duration. SEC
			// 10-Q filings report both single-quarter (~90 days) and YTD
			// cumulative (Q2: ~180, Q3: ~270 days) facts for income statement
			// items. Cash flow items only have YTD facts. By preferring the
			// shortest duration among same-filing-date facts, we pick the
			// single-quarter value when available and fall through to the
			// YTD value when it's the only option.
			if best == nil || f.Filed.After(best.Filed) {
				best = f
			} else if f.Filed.Equal(best.Filed) && !f.Start.IsZero() && !best.Start.IsZero() {
				fDays := f.End.Sub(f.Start).Hours() / 24
				bestDays := best.End.Sub(best.Start).Hours() / 24

				if fDays < bestDays {
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

// ResolveAllFields resolves all configured field mappings for a given period.
// Direct fields are resolved first, then derived fields are computed from the
// resolved values.
func ResolveAllFields(cf *CompanyFacts, periodEnd time.Time, formType string) map[string]float64 {
	resolved := make(map[string]float64)

	for _, m := range FieldMappings {
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

		return vals[0] / vals[1], true
	}

	return 0, false
}
