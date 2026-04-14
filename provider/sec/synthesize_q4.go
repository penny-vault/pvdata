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

// synthesizeInput holds the resolved field maps for a single de-cumulated quarter,
// along with its period end date. arEmit is the as-reported view and mrEmit is the
// most-recently-reported view.
//
// arCumPerShare / mrCumPerShare hold YTD cumulative values for per-share flow
// fields (EPS, EPSDiluted, DividendsPerBasicCommonShare). These may be nil for
// Q1 quarters where the cumulative equals the single-quarter value. SynthesizeQ4
// uses the last preceding quarter's cumulative to avoid rounding error when
// subtracting per-share values.
type synthesizeInput struct {
	periodEnd     time.Time
	arEmit        map[string]float64
	mrEmit        map[string]float64
	arCumPerShare map[string]float64
	mrCumPerShare map[string]float64
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

	arResult := synthesizeFromPreceding(annualAR, annualPeriod.PeriodEnd, preceding,
		func(q synthesizeInput) map[string]float64 { return q.arEmit },
		func(q synthesizeInput) map[string]float64 { return q.arCumPerShare },
	)

	mrResult := synthesizeFromPreceding(annualMR, annualPeriod.PeriodEnd, preceding,
		func(q synthesizeInput) map[string]float64 { return q.mrEmit },
		func(q synthesizeInput) map[string]float64 { return q.mrCumPerShare },
	)

	return arResult, mrResult
}

// synthesizeFromPreceding builds a Q4 field map from an annual field map and
// the 3 preceding de-cumulated quarters. emitFn selects either the AR or MR
// emit map from each quarter. cumPSFn selects the YTD cumulative per-share map
// for per-share flow fields.
func synthesizeFromPreceding(annual map[string]float64, annualPeriodEnd time.Time, preceding []synthesizeInput, emitFn func(synthesizeInput) map[string]float64, cumPSFn func(synthesizeInput) map[string]float64) map[string]float64 {
	result := make(map[string]float64, len(annual))

	for _, m := range FieldMappings {
		switch {
		case m.StatementType == StmtFlow:
			annualVal, hasAnnual := annual[m.FieldName]
			if !hasAnnual {
				continue
			}

			// Per-share flow fields: use the last preceding quarter's YTD
			// cumulative value for the subtraction. The company-reported
			// cumulative avoids rounding error from summing individually
			// rounded quarterly per-share values.
			if m.ValueType == "float64" {
				lastQ := preceding[0] // most recent quarter before the 10-K
				if cumPS := cumPSFn(lastQ); cumPS != nil {
					if cumVal, ok := cumPS[m.FieldName]; ok {
						result[m.FieldName] = annualVal - cumVal

						continue
					}
				}
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
			// Period-average fields (e.g. weighted average shares) are time-
			// weighted averages. When the Q3 YTD cumulative average is
			// available, use it with day-weighted math to avoid rounding
			// error from summing individually rounded quarterly integers:
			//   Q4 = (annual * annualDays - ytdAvg * ytdDays) / q4Days
			annualVal, hasAnnual := annual[m.FieldName]
			if !hasAnnual {
				continue
			}

			// preceding[0] = Q3 (most recent before annual)
			lastQ := preceding[0]
			if cumPS := cumPSFn(lastQ); cumPS != nil {
				if cumVal, ok := cumPS[m.FieldName]; ok {
					q4Days := annualPeriodEnd.Sub(lastQ.periodEnd).Hours() / 24
					d3 := preceding[0].periodEnd.Sub(preceding[1].periodEnd).Hours() / 24
					d2 := preceding[1].periodEnd.Sub(preceding[2].periodEnd).Hours() / 24
					// Estimate total fiscal year days from the known quarter gaps.
					// Q1 length isn't directly available, so approximate it as the
					// average of Q2, Q3, Q4.
					annualDays := math.Round(4.0 * (d2 + d3 + q4Days) / 3.0)
					ytdDays := annualDays - q4Days

					q4 := (annualVal*annualDays - cumVal*ytdDays) / q4Days
					if m.ValueType == "int64" {
						q4 = math.Round(q4)
					}

					result[m.FieldName] = q4

					continue
				}
			}

			// Fallback: simple equal-weight formula.
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
