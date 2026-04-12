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

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
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

var _ = Describe("emitFundamentals Q4 synthesis", func() {
	// Model a company with FY ending September (like Apple).
	//
	// Periods:
	//   10-Q Q1: Oct-Dec 2023  (period end Dec 30 2023)
	//   10-Q Q2: Jan-Mar 2024  (period end Mar 30 2024)
	//   10-Q Q3: Apr-Jun 2024  (period end Jun 29 2024)
	//   10-K FY: Oct 2023-Sep 2024 (period end Sep 28 2024, annual total)
	//   10-Q Q1 next FY: Oct-Dec 2024 (period end Dec 28 2024)
	//
	// Revenues (flow):
	//   Q1=110000, Q2=95000, Q3=90000, FY=400000 => Q4=105000
	//   Q1 next FY=115000
	//
	// NetIncomeCommonStock (flow, mapped via NetIncomeLossAvailableToCommonStockholdersBasic):
	//   Q1=30000, Q2=25000, Q3=22000, FY=100000 => Q4=23000
	//   Q1 next FY=32000

	d := func(y, m, day int) time.Time {
		return time.Date(y, time.Month(m), day, 0, 0, 0, 0, time.UTC)
	}

	// Period boundaries
	q1End := d(2023, 12, 30)
	q1Start := d(2023, 10, 1)
	q1Filed := d(2024, 1, 15)

	q2End := d(2024, 3, 30)
	q2Start := d(2024, 1, 1)
	q2Filed := d(2024, 4, 15)

	q3End := d(2024, 6, 29)
	q3Start := d(2024, 4, 1)
	q3Filed := d(2024, 7, 15)

	fyEnd := d(2024, 9, 28)
	fyStart := d(2023, 10, 1)
	fyFiled := d(2024, 11, 15)

	q1NextEnd := d(2024, 12, 28)
	q1NextStart := d(2024, 10, 1)
	q1NextFiled := d(2025, 1, 15)

	// Build CompanyFacts with Revenues and NetIncomeLossAvailableToCommonStockholdersBasic
	// facts. Facts must be sorted by Filed within each concept.
	buildCF := func() *CompanyFacts {
		return &CompanyFacts{
			CIK:        12345,
			EntityName: "Test Corp",
			Facts: map[string][]Fact{
				"Revenues": {
					// Q1 single-quarter
					{Start: q1Start, End: q1End, Val: 110000, Form: "10-Q", Filed: q1Filed, FP: "Q1", FY: 2024},
					// Q2 single-quarter
					{Start: q2Start, End: q2End, Val: 95000, Form: "10-Q", Filed: q2Filed, FP: "Q2", FY: 2024},
					// Q3 single-quarter
					{Start: q3Start, End: q3End, Val: 90000, Form: "10-Q", Filed: q3Filed, FP: "Q3", FY: 2024},
					// 10-K full year
					{Start: fyStart, End: fyEnd, Val: 400000, Form: "10-K", Filed: fyFiled, FP: "FY", FY: 2024},
					// Q1 next FY
					{Start: q1NextStart, End: q1NextEnd, Val: 115000, Form: "10-Q", Filed: q1NextFiled, FP: "Q1", FY: 2025},
				},
				"NetIncomeLossAvailableToCommonStockholdersBasic": {
					{Start: q1Start, End: q1End, Val: 30000, Form: "10-Q", Filed: q1Filed, FP: "Q1", FY: 2024},
					{Start: q2Start, End: q2End, Val: 25000, Form: "10-Q", Filed: q2Filed, FP: "Q2", FY: 2024},
					{Start: q3Start, End: q3End, Val: 22000, Form: "10-Q", Filed: q3Filed, FP: "Q3", FY: 2024},
					{Start: fyStart, End: fyEnd, Val: 100000, Form: "10-K", Filed: fyFiled, FP: "FY", FY: 2024},
					{Start: q1NextStart, End: q1NextEnd, Val: 32000, Form: "10-Q", Filed: q1NextFiled, FP: "Q1", FY: 2025},
				},
			},
		}
	}

	It("emits synthesized Q4 ARQ/MRQ observations", func() {
		cf := buildCF()
		sub := &library.Subscription{Name: "test"}
		out := make(chan *data.Observation, 100)
		numObs := 0

		emitFundamentals(cf, AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST", CIK: 12345},
			sub, time.Time{}, out, &numObs)
		close(out)

		// Collect observations by dimension and date_key.
		type obsKey struct {
			dimension string
			dateKey   time.Time
		}

		observations := make(map[obsKey]*data.Fundamental)
		for obs := range out {
			key := obsKey{
				dimension: obs.Fundamental.Dimension,
				dateKey:   obs.Fundamental.DateKey,
			}
			observations[key] = obs.Fundamental
		}

		// Synthesized Q4 should have FormType "10-Q" so NormalizeEventDate snaps
		// Sep 28 2024 to Sep 30 2024.
		q4DateKey := d(2024, 9, 30)

		// Verify Q4 ARQ exists with correct values.
		arqQ4 := observations[obsKey{dimension: "ARQ", dateKey: q4DateKey}]
		Expect(arqQ4).NotTo(BeNil(), "expected ARQ observation for Q4 at 2024-09-30")
		Expect(arqQ4.Revenues).To(BeNumerically("==", 105000),
			"Q4 Revenues = FY 400000 - (Q1 110000 + Q2 95000 + Q3 90000)")
		Expect(arqQ4.NetIncomeCommonStock).To(BeNumerically("==", 23000),
			"Q4 NetIncome = FY 100000 - (Q1 30000 + Q2 25000 + Q3 22000)")

		// Verify Q4 MRQ exists with same values (no restatements in this test).
		mrqQ4 := observations[obsKey{dimension: "MRQ", dateKey: q4DateKey}]
		Expect(mrqQ4).NotTo(BeNil(), "expected MRQ observation for Q4 at 2024-09-30")
		Expect(mrqQ4.Revenues).To(BeNumerically("==", 105000))
		Expect(mrqQ4.NetIncomeCommonStock).To(BeNumerically("==", 23000))
	})

	It("includes synthesized Q4 in TTM computation", func() {
		cf := buildCF()
		sub := &library.Subscription{Name: "test"}
		out := make(chan *data.Observation, 100)
		numObs := 0

		emitFundamentals(cf, AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST", CIK: 12345},
			sub, time.Time{}, out, &numObs)
		close(out)

		type obsKey struct {
			dimension string
			dateKey   time.Time
		}

		observations := make(map[obsKey]*data.Fundamental)
		for obs := range out {
			key := obsKey{
				dimension: obs.Fundamental.Dimension,
				dateKey:   obs.Fundamental.DateKey,
			}
			observations[key] = obs.Fundamental
		}

		// The Q1 next FY (Oct-Dec 2024) normalized date is 2024-12-31.
		// TTM at that date = Q1NextFY + Q4 + Q3 + Q2
		//   Revenues: 115000 + 105000 + 90000 + 95000 = 405000
		//   NetIncome: 32000 + 23000 + 22000 + 25000 = 102000
		q1NextDateKey := d(2024, 12, 31)

		artQ1Next := observations[obsKey{dimension: "ART", dateKey: q1NextDateKey}]
		Expect(artQ1Next).NotTo(BeNil(), "expected ART observation at 2024-12-31")
		Expect(artQ1Next.Revenues).To(BeNumerically("==", 405000),
			"ART Revenues = Q1Next 115000 + Q4 105000 + Q3 90000 + Q2 95000")
		Expect(artQ1Next.NetIncomeCommonStock).To(BeNumerically("==", 102000),
			"ART NetIncome = Q1Next 32000 + Q4 23000 + Q3 22000 + Q2 25000")

		mrtQ1Next := observations[obsKey{dimension: "MRT", dateKey: q1NextDateKey}]
		Expect(mrtQ1Next).NotTo(BeNil(), "expected MRT observation at 2024-12-31")
		Expect(mrtQ1Next.Revenues).To(BeNumerically("==", 405000))
		Expect(mrtQ1Next.NetIncomeCommonStock).To(BeNumerically("==", 102000))
	})
})
