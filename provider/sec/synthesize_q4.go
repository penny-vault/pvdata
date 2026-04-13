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

import "time"

// synthesizeInput holds the resolved field maps for a single de-cumulated quarter,
// along with its period end date. arEmit is the as-reported view and mrEmit is the
// most-recently-reported view.
type synthesizeInput struct {
	periodEnd time.Time
	arEmit    map[string]float64
	mrEmit    map[string]float64
}

// maxQ4QuarterSpanDays is the maximum number of days before the 10-K period end
// that a quarter may fall and still be considered part of the same fiscal year.
const maxQ4QuarterSpanDays = 400

// SynthesizeQ4 derives single-quarter Q4 field maps from a 10-K annual filing by
// subtracting the three preceding fiscal-year quarters from the annual total.
//
// annualAR and annualMR are the resolved field maps for the 10-K period in the
// as-reported and most-recently-reported views respectively. annualPeriod is the
// 10-K Period metadata. quarters is an ordered slice (ascending by periodEnd) of
// de-cumulated quarter inputs.
//
// The function walks backwards through quarters to find exactly 3 quarters whose
// periodEnd is strictly before the 10-K period end and within maxQ4QuarterSpanDays
// of it. If fewer than 3 such quarters exist, both return values are nil.
//
// For each field:
//   - StmtFlow: Q4 = annual value - sum(Q1+Q2+Q3 de-cumulated values)
//   - StmtPointInTime: Q4 = annual value directly
//   - StmtMetric with MappingDerived: recomputed from Q4 values via computeDerived
func SynthesizeQ4(annualAR, annualMR map[string]float64, annualPeriod Period, quarters []synthesizeInput) (map[string]float64, map[string]float64) {
	// Walk backwards through quarters to collect the 3 immediately preceding ones.
	var preceding []synthesizeInput

	for i := len(quarters) - 1; i >= 0; i-- {
		q := quarters[i]

		if !q.periodEnd.Before(annualPeriod.PeriodEnd) {
			continue
		}

		daysBefore := annualPeriod.PeriodEnd.Sub(q.periodEnd).Hours() / 24
		if daysBefore > maxQ4QuarterSpanDays {
			continue
		}

		preceding = append(preceding, q)

		if len(preceding) == 3 {
			break
		}
	}

	if len(preceding) < 3 {
		return nil, nil
	}

	arResult := synthesizeFromPreceding(annualAR, preceding, func(q synthesizeInput) map[string]float64 {
		return q.arEmit
	})

	mrResult := synthesizeFromPreceding(annualMR, preceding, func(q synthesizeInput) map[string]float64 {
		return q.mrEmit
	})

	return arResult, mrResult
}

// synthesizeFromPreceding builds a Q4 field map from an annual field map and
// the 3 preceding de-cumulated quarters. emitFn selects either the AR or MR
// emit map from each quarter.
func synthesizeFromPreceding(annual map[string]float64, preceding []synthesizeInput, emitFn func(synthesizeInput) map[string]float64) map[string]float64 {
	result := make(map[string]float64, len(annual))

	for _, m := range FieldMappings {
		switch {
		case m.StatementType == StmtFlow:
			annualVal, hasAnnual := annual[m.FieldName]
			if !hasAnnual {
				continue
			}

			sum := 0.0
			allFound := true

			for _, q := range preceding {
				emit := emitFn(q)
				if v, ok := emit[m.FieldName]; ok {
					sum += v
				} else {
					allFound = false
					break
				}
			}

			if allFound {
				result[m.FieldName] = annualVal - sum
			}

		case m.StatementType == StmtPointInTime:
			if v, ok := annual[m.FieldName]; ok {
				result[m.FieldName] = v
			}

		case m.StatementType == StmtPeriodAverage:
			// Period-average fields (e.g. weighted average shares): the annual
			// value is the average of 4 quarterly values, so
			// Q4 = annual*4 - sum(Q1..Q3).
			annualVal, hasAnnual := annual[m.FieldName]
			if !hasAnnual {
				continue
			}

			sum := 0.0
			allFound := true

			for _, q := range preceding {
				emit := emitFn(q)
				if v, ok := emit[m.FieldName]; ok {
					sum += v
				} else {
					allFound = false
					break
				}
			}

			if allFound {
				result[m.FieldName] = annualVal*4 - sum
			}

		case m.StatementType == StmtMetric && m.Type == MappingDerived:
			// Recompute from Q4 values after all flow and point-in-time fields
			// have been populated; handled in a second pass below.
		}
	}

	// Second pass: recompute derived metric fields from the Q4 values.
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
