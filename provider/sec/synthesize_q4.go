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

// synthesizeInput holds the resolved field maps for a single de-cumulated
// quarter. arCumPerShare/mrCumPerShare hold YTD cumulative per-share flow
// values; SynthesizeQ4 uses the last preceding quarter's cumulative to avoid
// rounding error when subtracting per-share values.
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

// SynthesizeQ4 derives single-quarter Q4 field maps from a 10-K by subtracting
// the three preceding fiscal-year quarters from the annual total. Returns
// (nil, nil) when fewer than 3 quarters fall in the maxQ4QuarterSpanDays
// window before the 10-K period end.
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
		func(q synthesizeInput, _ string) map[string]float64 { return q.arEmit },
		func(q synthesizeInput, _ string) map[string]float64 { return q.arCumPerShare },
	)

	// MR Q4: when the 10-K annual was NOT restated (annualAR == annualMR),
	// use AR preceding quarters — the 10-K only saw original Q1-Q3 values,
	// so synthesizing from later-restated comparatives would be wrong.
	mrResult := synthesizeFromPreceding(annualMR, annualPeriod.PeriodEnd, preceding,
		func(q synthesizeInput, field string) map[string]float64 {
			if arVal, hasAR := annualAR[field]; hasAR {
				if mrVal, hasMR := annualMR[field]; hasMR && arVal == mrVal {
					return q.arEmit
				}
			}

			return q.mrEmit
		},
		func(q synthesizeInput, field string) map[string]float64 {
			if arVal, hasAR := annualAR[field]; hasAR {
				if mrVal, hasMR := annualMR[field]; hasMR && arVal == mrVal {
					return q.arCumPerShare
				}
			}

			return q.mrCumPerShare
		},
	)

	return arResult, mrResult
}

// synthesizeFromPreceding builds a Q4 field map from an annual field map and
// 3 preceding de-cumulated quarters. emitFn/cumPSFn select AR vs MR per-field;
// see SynthesizeQ4 for the AR-vs-MR rule.
func synthesizeFromPreceding(annual map[string]float64, annualPeriodEnd time.Time, preceding []synthesizeInput, emitFn func(synthesizeInput, string) map[string]float64, cumPSFn func(synthesizeInput, string) map[string]float64) map[string]float64 {
	result := make(map[string]float64, len(annual))

	for _, m := range FieldMappings {
		switch {
		case m.StatementType == StmtFlow:
			annualVal, hasAnnual := annual[m.FieldName]
			if !hasAnnual {
				// Some filers report a concept only on 10-Q comparatives
				// and never on the 10-K (WMT tags
				// ProceedsFromDivestitureOfBusinesses on Q2/Q3 10-Qs only).
				// If any preceding quarter has a value, treat the annual as
				// 0 so derived fields can offset the prior-quarter cross-flow.
				hasQuarterlyVal := false

				for _, q := range preceding {
					emit := emitFn(q, m.FieldName)
					if _, ok := emit[m.FieldName]; ok {
						hasQuarterlyVal = true

						break
					}
				}

				if !hasQuarterlyVal {
					continue
				}

				annualVal = 0
			}

			// Prefer the last preceding quarter's YTD cumulative for the
			// subtraction: it captures restatements across quarters and
			// avoids rounding error from summing per-share values.
			// ResolveCumulativePerShareForFiling only populates cumPS when
			// the winning fact spans more than one quarter.
			lastQ := preceding[0] // most recent quarter before the 10-K
			if cumPS := cumPSFn(lastQ, m.FieldName); cumPS != nil {
				if cumVal, ok := cumPS[m.FieldName]; ok {
					result[m.FieldName] = annualVal - cumVal

					continue
				}
			}

			// Sum preceding quarters, treating missing values as 0. Some
			// flow fields are only filed in quarters where the activity
			// happened; the annual still has the full-year total so
			// Q4 = annual - sum(present) is correct.
			sum := 0.0

			for _, q := range preceding {
				emit := emitFn(q, m.FieldName)
				if v, ok := emit[m.FieldName]; ok {
					sum += v
				}
			}

			result[m.FieldName] = annualVal - sum

		case m.StatementType == StmtPointInTime:
			if v, ok := annual[m.FieldName]; ok {
				result[m.FieldName] = v
			}

		case m.StatementType == StmtPeriodAverage:
			// Period-average fields are time-weighted. When the Q3 YTD
			// average is available, use day-weighted math to avoid rounding
			// error: Q4 = (annual * annualDays - ytdAvg * ytdDays) / q4Days.
			annualVal, hasAnnual := annual[m.FieldName]
			if !hasAnnual {
				continue
			}

			// preceding[0] = Q3 (most recent before annual)
			lastQ := preceding[0]
			if cumPS := cumPSFn(lastQ, m.FieldName); cumPS != nil {
				if cumVal, ok := cumPS[m.FieldName]; ok {
					q4Days := annualPeriodEnd.Sub(lastQ.periodEnd).Hours() / 24
					d3 := preceding[0].periodEnd.Sub(preceding[1].periodEnd).Hours() / 24
					d2 := preceding[1].periodEnd.Sub(preceding[2].periodEnd).Hours() / 24
					// Q1 length isn't directly available; approximate the
					// fiscal year as 4 x average(Q2, Q3, Q4).
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
				emit := emitFn(q, m.FieldName)
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
			// Handled in the third pass below.
		}
	}

	// Second pass: recompute derived StmtFlow fields from Q4 operands when
	// they agree with the annual-sum value to within rounding drift. The
	// annual-sum path subtracts individually rounded quarterly values and
	// loses precision; operand-level cumulative subtraction is tighter. A
	// 0.5% relative tolerance keeps the override to rounding drift only.
	for _, m := range FieldMappings {
		if m.Type != MappingDerived || m.StatementType != StmtFlow {
			continue
		}

		if len(m.Operands) == 0 {
			continue
		}

		// Restrict to additive formulas; division/multiplication is left to
		// the annual-sum path to avoid amplifying operand rounding.
		switch m.Op {
		case OpAdd, OpSubtract, OpLinearCombination:
		default:
			continue
		}

		val, ok := computeDerived(m, result)
		if !ok {
			continue
		}

		existing, hasExisting := result[m.FieldName]
		if hasExisting {
			scale := math.Max(math.Abs(val), math.Abs(existing))
			if scale > 0 && math.Abs(val-existing)/scale > 0.005 {
				continue
			}
		}

		result[m.FieldName] = val
	}

	// Third pass: recompute derived metric fields from the Q4 values.
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
