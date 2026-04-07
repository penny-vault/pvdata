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
// match for the given period end date and form type wins.
//
// For instant (balance sheet) concepts, matches facts where End == periodEnd.
// For duration (income/cash flow) concepts, matches facts where End == periodEnd
// and the filing form matches.
func ResolveDirect(cf *CompanyFacts, m FieldMapping, periodEnd time.Time, formType string) (float64, bool) {
	tags := m.XBRLTags
	if m.Type == MappingDerived {
		tags = m.FallbackTags
	}

	for _, tag := range tags {
		facts, ok := cf.Facts[tag]
		if !ok {
			continue
		}

		// Find the best matching fact for this period
		var best *Fact

		for i := range facts {
			f := &facts[i]

			// Must match the period end date
			if !f.End.Equal(periodEnd) {
				continue
			}

			// Must match the form type
			if f.Form != formType {
				continue
			}

			// For duration concepts, verify the period length is reasonable
			// (roughly a quarter for 10-Q, roughly a year for 10-K)
			if !f.Start.IsZero() {
				days := f.End.Sub(f.Start).Hours() / 24
				if formType == "10-K" && days < 300 {
					continue // Skip quarterly data in an annual filing
				}

				if formType == "10-Q" && days > 200 {
					continue // Skip annual data in a quarterly filing
				}
			}

			// Prefer the fact with the latest filing date (most recent data)
			if best == nil || f.Filed.After(best.Filed) {
				best = f
			}
		}

		if best != nil {
			return best.Val, true
		}
	}

	return 0, false
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
				resolved[m.FieldName] = val
			}

		case MappingDerived:
			// Try direct XBRL fallback tags first
			if len(m.FallbackTags) > 0 {
				if val, ok := ResolveDirect(cf, m, periodEnd, formType); ok {
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
