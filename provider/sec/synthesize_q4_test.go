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

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SynthesizeQ4", func() {
	// Use a standard calendar-year fiscal period for clarity.
	annualPeriodEnd := time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC)

	annualPeriod := Period{
		PeriodEnd:   annualPeriodEnd,
		FormType:    "10-K",
		ARFiledDate: time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC),
		MRFiledDate: time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC),
	}

	// Three preceding quarters: Q1, Q2, Q3 (de-cumulated single-quarter values).
	q1 := synthesizeInput{
		periodEnd: time.Date(2023, 3, 31, 0, 0, 0, 0, time.UTC),
		arEmit:    map[string]float64{"Revenues": 100, "TotalAssets": 1000},
		mrEmit:    map[string]float64{"Revenues": 100, "TotalAssets": 1000},
	}
	q2 := synthesizeInput{
		periodEnd: time.Date(2023, 6, 30, 0, 0, 0, 0, time.UTC),
		arEmit:    map[string]float64{"Revenues": 120, "TotalAssets": 1100},
		mrEmit:    map[string]float64{"Revenues": 120, "TotalAssets": 1100},
	}
	q3 := synthesizeInput{
		periodEnd: time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC),
		arEmit:    map[string]float64{"Revenues": 110, "TotalAssets": 1050},
		mrEmit:    map[string]float64{"Revenues": 110, "TotalAssets": 1050},
	}

	// Annual totals: Revenues is a flow field; TotalAssets is point-in-time.
	annualAR := map[string]float64{"Revenues": 500, "TotalAssets": 1200}
	annualMR := map[string]float64{"Revenues": 500, "TotalAssets": 1200}

	Describe("flow field computation", func() {
		It("returns Q4 = annual - sum(Q1+Q2+Q3) for flow fields", func() {
			quarters := []synthesizeInput{q1, q2, q3}
			arResult, mrResult := SynthesizeQ4(annualAR, annualMR, annualPeriod, quarters)

			Expect(arResult).NotTo(BeNil())
			Expect(mrResult).NotTo(BeNil())

			// Revenues is StmtFlow: 500 - (100 + 120 + 110) = 170
			Expect(arResult["Revenues"]).To(BeNumerically("~", 170, 0.001))
			Expect(mrResult["Revenues"]).To(BeNumerically("~", 170, 0.001))
		})
	})

	Describe("point-in-time field computation", func() {
		It("uses the annual value directly for point-in-time fields", func() {
			quarters := []synthesizeInput{q1, q2, q3}
			arResult, mrResult := SynthesizeQ4(annualAR, annualMR, annualPeriod, quarters)

			Expect(arResult).NotTo(BeNil())
			Expect(mrResult).NotTo(BeNil())

			// TotalAssets is StmtPointInTime: use annual value = 1200
			Expect(arResult["TotalAssets"]).To(BeNumerically("~", 1200, 0.001))
			Expect(mrResult["TotalAssets"]).To(BeNumerically("~", 1200, 0.001))
		})
	})

	Describe("insufficient quarters", func() {
		It("returns nil when fewer than 3 quarters precede the 10-K", func() {
			quarters := []synthesizeInput{q1, q2}
			arResult, mrResult := SynthesizeQ4(annualAR, annualMR, annualPeriod, quarters)

			Expect(arResult).To(BeNil())
			Expect(mrResult).To(BeNil())
		})

		It("returns nil when no quarters precede the 10-K", func() {
			arResult, mrResult := SynthesizeQ4(annualAR, annualMR, annualPeriod, []synthesizeInput{})

			Expect(arResult).To(BeNil())
			Expect(mrResult).To(BeNil())
		})
	})

	Describe("quarters too far from 10-K period end", func() {
		It("returns nil when quarters are more than 400 days before the 10-K period end", func() {
			// Place Q3 over 400 days before the annual period end.
			oldQ3 := synthesizeInput{
				periodEnd: annualPeriodEnd.AddDate(0, 0, -410),
				arEmit:    map[string]float64{"Revenues": 110},
				mrEmit:    map[string]float64{"Revenues": 110},
			}
			quarters := []synthesizeInput{q1, q2, oldQ3}
			arResult, mrResult := SynthesizeQ4(annualAR, annualMR, annualPeriod, quarters)

			Expect(arResult).To(BeNil())
			Expect(mrResult).To(BeNil())
		})
	})

	Describe("quarter ordering", func() {
		It("picks the 3 quarters immediately preceding the 10-K even when older quarters are present", func() {
			// Add an old quarter from the prior fiscal year that should be ignored.
			oldQ := synthesizeInput{
				periodEnd: time.Date(2022, 12, 31, 0, 0, 0, 0, time.UTC),
				arEmit:    map[string]float64{"Revenues": 999},
				mrEmit:    map[string]float64{"Revenues": 999},
			}
			quarters := []synthesizeInput{oldQ, q1, q2, q3}
			arResult, mrResult := SynthesizeQ4(annualAR, annualMR, annualPeriod, quarters)

			Expect(arResult).NotTo(BeNil())
			Expect(mrResult).NotTo(BeNil())

			// Should still be 500 - (100+120+110) = 170, ignoring the old quarter.
			Expect(arResult["Revenues"]).To(BeNumerically("~", 170, 0.001))
		})
	})

	Describe("derived metric fields", func() {
		It("recomputes GrossMargin (GrossProfit / Revenues) from synthesized Q4 flow values", func() {
			// Annual totals for flow fields.
			annualARLocal := map[string]float64{"Revenues": 500, "GrossProfit": 225}
			annualMRLocal := map[string]float64{"Revenues": 500, "GrossProfit": 225}

			// Three quarters whose flow values sum to 330 / 150.
			q1gm := synthesizeInput{
				periodEnd: q1.periodEnd,
				arEmit:    map[string]float64{"Revenues": 100, "GrossProfit": 45},
				mrEmit:    map[string]float64{"Revenues": 100, "GrossProfit": 45},
			}
			q2gm := synthesizeInput{
				periodEnd: q2.periodEnd,
				arEmit:    map[string]float64{"Revenues": 120, "GrossProfit": 55},
				mrEmit:    map[string]float64{"Revenues": 120, "GrossProfit": 55},
			}
			q3gm := synthesizeInput{
				periodEnd: q3.periodEnd,
				arEmit:    map[string]float64{"Revenues": 110, "GrossProfit": 50},
				mrEmit:    map[string]float64{"Revenues": 110, "GrossProfit": 50},
			}

			quarters := []synthesizeInput{q1gm, q2gm, q3gm}
			arResult, mrResult := SynthesizeQ4(annualARLocal, annualMRLocal, annualPeriod, quarters)

			Expect(arResult).NotTo(BeNil())
			Expect(mrResult).NotTo(BeNil())

			// Q4 Revenues = 500 - (100+120+110) = 170
			Expect(arResult["Revenues"]).To(BeNumerically("~", 170, 0.001))
			// Q4 GrossProfit = 225 - (45+55+50) = 75
			Expect(arResult["GrossProfit"]).To(BeNumerically("~", 75, 0.001))
			// Q4 GrossMargin = GrossProfit / Revenues = 75 / 170 ≈ 0.4412
			Expect(arResult["GrossMargin"]).To(BeNumerically("~", 75.0/170.0, 0.0001))
			Expect(mrResult["GrossMargin"]).To(BeNumerically("~", 75.0/170.0, 0.0001))
		})
	})

	Describe("AR and MR emit independence", func() {
		It("computes AR and MR independently when their values differ", func() {
			// MR values reflect a restatement: Revenues was restated upward.
			annualARLocal := map[string]float64{"Revenues": 500}
			annualMRLocal := map[string]float64{"Revenues": 520}

			q1mr := synthesizeInput{
				periodEnd: q1.periodEnd,
				arEmit:    map[string]float64{"Revenues": 100},
				mrEmit:    map[string]float64{"Revenues": 105},
			}
			q2mr := synthesizeInput{
				periodEnd: q2.periodEnd,
				arEmit:    map[string]float64{"Revenues": 120},
				mrEmit:    map[string]float64{"Revenues": 125},
			}
			q3mr := synthesizeInput{
				periodEnd: q3.periodEnd,
				arEmit:    map[string]float64{"Revenues": 110},
				mrEmit:    map[string]float64{"Revenues": 115},
			}

			quarters := []synthesizeInput{q1mr, q2mr, q3mr}
			arResult, mrResult := SynthesizeQ4(annualARLocal, annualMRLocal, annualPeriod, quarters)

			Expect(arResult).NotTo(BeNil())
			Expect(mrResult).NotTo(BeNil())

			// AR: 500 - (100+120+110) = 170
			Expect(arResult["Revenues"]).To(BeNumerically("~", 170, 0.001))
			// MR: 520 - (105+125+115) = 175
			Expect(mrResult["Revenues"]).To(BeNumerically("~", 175, 0.001))
		})
	})
})
