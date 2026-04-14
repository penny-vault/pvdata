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
			// Q4 GrossMargin = GrossProfit / Revenues = 75 / 170 ≈ 0.441 (rounded to 3 decimal places)
			Expect(arResult["GrossMargin"]).To(Equal(0.441))
			Expect(mrResult["GrossMargin"]).To(Equal(0.441))
		})
	})

	Describe("StmtPeriodAverage field computation (AAPL FY2025)", func() {
		It("uses YTD cumulative for day-weighted Q4 period-average (MSFT FY2025)", func() {
			// MSFT FY2025 WeightedAverageNumberOfDilutedSharesOutstanding:
			//   Q1 (91d, ending 2024-09-30): 7,470,000,000
			//   Q2 (92d, ending 2024-12-31): 7,468,000,000
			//   Q3 (90d, ending 2025-03-31): 7,461,000,000
			//   Q3 YTD (273d): 7,466,000,000
			//   Annual (364d): 7,465,000,000
			// Simple formula: Q4 = 7465*4 - (7470+7468+7461) = 7,461,000,000
			// Day-weighted:   Q4 = (7465*365 - 7466*(365-91)) / 91 = 7,462,000,000
			// Sharadar/Yahoo: 7,462,000,000 (day-weighted matches)
			msftAnnualPeriod := Period{
				PeriodEnd:   time.Date(2025, 6, 30, 0, 0, 0, 0, time.UTC),
				FormType:    "10-K",
				ARFiledDate: time.Date(2025, 7, 30, 0, 0, 0, 0, time.UTC),
				MRFiledDate: time.Date(2025, 7, 30, 0, 0, 0, 0, time.UTC),
			}

			q1 := synthesizeInput{
				periodEnd:     time.Date(2024, 9, 30, 0, 0, 0, 0, time.UTC),
				arEmit:        map[string]float64{"WeightedAverageSharesDiluted": 7_470_000_000},
				mrEmit:        map[string]float64{"WeightedAverageSharesDiluted": 7_470_000_000},
				arCumPerShare: nil, // Q1: no prior YTD
				mrCumPerShare: nil,
			}
			q2 := synthesizeInput{
				periodEnd:     time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC),
				arEmit:        map[string]float64{"WeightedAverageSharesDiluted": 7_468_000_000},
				mrEmit:        map[string]float64{"WeightedAverageSharesDiluted": 7_468_000_000},
				arCumPerShare: nil,
				mrCumPerShare: nil,
			}
			q3 := synthesizeInput{
				periodEnd: time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
				arEmit:    map[string]float64{"WeightedAverageSharesDiluted": 7_461_000_000},
				mrEmit:    map[string]float64{"WeightedAverageSharesDiluted": 7_461_000_000},
				// Q3 YTD cumulative: 7,466,000,000 (273-day weighted average)
				arCumPerShare: map[string]float64{"WeightedAverageSharesDiluted": 7_466_000_000},
				mrCumPerShare: map[string]float64{"WeightedAverageSharesDiluted": 7_466_000_000},
			}

			annualAR := map[string]float64{"WeightedAverageSharesDiluted": 7_465_000_000}
			annualMR := map[string]float64{"WeightedAverageSharesDiluted": 7_465_000_000}

			quarters := []synthesizeInput{q1, q2, q3}
			arResult, mrResult := SynthesizeQ4(annualAR, annualMR, msftAnnualPeriod, quarters)

			Expect(arResult).NotTo(BeNil())
			Expect(mrResult).NotTo(BeNil())

			// Day-weighted: (7465*365 - 7466*(365-91)) / 91 = 7,462,000,000
			Expect(arResult["WeightedAverageSharesDiluted"]).To(BeNumerically("~", 7_462_000_000.0, 1))
			Expect(mrResult["WeightedAverageSharesDiluted"]).To(BeNumerically("~", 7_462_000_000.0, 1))
		})

		It("falls back to annual*4 - sum when no YTD cumulative (AAPL FY2025)", func() {
			// AAPL FY2025 WeightedAverageSharesBasic from SEC XBRL:
			//   Q1 (90d): 15,081,724,000
			//   Q2 (90d): 14,994,082,000
			//   Q3 (90d): 14,902,886,000
			//   Annual (363d): 14,948,500,000
			// Without YTD cumulative, falls back: Q4 = annual*4 - sum(Q1..Q3) = 14,815,308,000
			aaplAnnualPeriod := Period{
				PeriodEnd:   time.Date(2025, 9, 27, 0, 0, 0, 0, time.UTC),
				FormType:    "10-K",
				ARFiledDate: time.Date(2025, 10, 31, 0, 0, 0, 0, time.UTC),
				MRFiledDate: time.Date(2025, 10, 31, 0, 0, 0, 0, time.UTC),
			}

			aaplQ1 := synthesizeInput{
				periodEnd: time.Date(2024, 12, 28, 0, 0, 0, 0, time.UTC),
				arEmit:    map[string]float64{"WeightedAverageShares": 15_081_724_000},
				mrEmit:    map[string]float64{"WeightedAverageShares": 15_081_724_000},
			}
			aaplQ2 := synthesizeInput{
				periodEnd: time.Date(2025, 3, 29, 0, 0, 0, 0, time.UTC),
				arEmit:    map[string]float64{"WeightedAverageShares": 14_994_082_000},
				mrEmit:    map[string]float64{"WeightedAverageShares": 14_994_082_000},
			}
			aaplQ3 := synthesizeInput{
				periodEnd: time.Date(2025, 6, 28, 0, 0, 0, 0, time.UTC),
				arEmit:    map[string]float64{"WeightedAverageShares": 14_902_886_000},
				mrEmit:    map[string]float64{"WeightedAverageShares": 14_902_886_000},
			}

			annualAR := map[string]float64{"WeightedAverageShares": 14_948_500_000}
			annualMR := map[string]float64{"WeightedAverageShares": 14_948_500_000}

			quarters := []synthesizeInput{aaplQ1, aaplQ2, aaplQ3}
			arResult, mrResult := SynthesizeQ4(annualAR, annualMR, aaplAnnualPeriod, quarters)

			Expect(arResult).NotTo(BeNil())
			Expect(mrResult).NotTo(BeNil())

			// Q4 = annual*4 - (Q1+Q2+Q3) = 14,948,500,000*4 - 44,978,692,000 = 14,815,308,000
			expectedQ4 := 14_948_500_000.0*4 - 15_081_724_000.0 - 14_994_082_000.0 - 14_902_886_000.0
			Expect(arResult["WeightedAverageShares"]).To(BeNumerically("~", expectedQ4, 1))
			Expect(mrResult["WeightedAverageShares"]).To(BeNumerically("~", expectedQ4, 1))
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

	Describe("Per-share flow fields use YTD cumulative for Q4 synthesis", func() {
		// AAPL FY2024 per-share values from SEC XBRL (period ending Sep 28, 2024):
		//
		// EarningsPerShareBasic:
		//   Q1 single: 2.19   Q2 single: 1.53   Q3 single: 1.40   Annual: 6.11
		//   Q3 YTD cumulative: 5.13
		//   Note: Q1+Q2+Q3 = 5.12 (not 5.13) due to per-quarter rounding.
		//   Correct Q4 = 6.11 - 5.13 = 0.98 (using cumulative)
		//   Wrong   Q4 = 6.11 - 5.12 = 0.99 (summing individual quarters)
		//
		// EarningsPerShareDiluted:
		//   Q1 single: 2.18   Q2 single: 1.53   Q3 single: 1.40   Annual: 6.08
		//   Q3 YTD cumulative: 5.11
		//   Q4 = 6.08 - 5.11 = 0.97 (cumulative == sum here, so no rounding issue)
		//
		// CommonStockDividendsPerShareDeclared:
		//   Q1 single: 0.24   Q2 single: 0.24   Q3 single: 0.25   Annual: 0.98
		//   Q3 YTD cumulative: 0.73
		//   Q4 = 0.98 - 0.73 = 0.25 (cumulative == sum here)

		aaplAnnualPeriod := Period{
			PeriodEnd:   time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC),
			FormType:    "10-K",
			ARFiledDate: time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC),
			MRFiledDate: time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC),
		}

		annualAR := map[string]float64{
			"EPS":                          6.11,
			"EPSDiluted":                   6.08,
			"DividendsPerBasicCommonShare": 0.98,
			"Revenues":                     391_035_000_000,
			"NetIncomeCommonStock":         93_736_000_000,
		}
		annualMR := map[string]float64{
			"EPS":                          6.11,
			"EPSDiluted":                   6.08,
			"DividendsPerBasicCommonShare": 0.98,
			"Revenues":                     391_035_000_000,
			"NetIncomeCommonStock":         93_736_000_000,
		}

		q1Input := synthesizeInput{
			periodEnd: time.Date(2023, 12, 30, 0, 0, 0, 0, time.UTC),
			arEmit: map[string]float64{
				"EPS": 2.19, "EPSDiluted": 2.18, "DividendsPerBasicCommonShare": 0.24,
				"Revenues": 119_580_000_000, "NetIncomeCommonStock": 33_916_000_000,
			},
			mrEmit: map[string]float64{
				"EPS": 2.19, "EPSDiluted": 2.18, "DividendsPerBasicCommonShare": 0.24,
				"Revenues": 119_580_000_000, "NetIncomeCommonStock": 33_916_000_000,
			},
		}
		q2Input := synthesizeInput{
			periodEnd: time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC),
			arEmit: map[string]float64{
				"EPS": 1.53, "EPSDiluted": 1.53, "DividendsPerBasicCommonShare": 0.24,
				"Revenues": 90_753_000_000, "NetIncomeCommonStock": 23_636_000_000,
			},
			mrEmit: map[string]float64{
				"EPS": 1.53, "EPSDiluted": 1.53, "DividendsPerBasicCommonShare": 0.24,
				"Revenues": 90_753_000_000, "NetIncomeCommonStock": 23_636_000_000,
			},
		}
		q3Input := synthesizeInput{
			periodEnd: time.Date(2024, 6, 29, 0, 0, 0, 0, time.UTC),
			arEmit: map[string]float64{
				"EPS": 1.40, "EPSDiluted": 1.40, "DividendsPerBasicCommonShare": 0.25,
				"Revenues": 85_777_000_000, "NetIncomeCommonStock": 21_448_000_000,
			},
			mrEmit: map[string]float64{
				"EPS": 1.40, "EPSDiluted": 1.40, "DividendsPerBasicCommonShare": 0.25,
				"Revenues": 85_777_000_000, "NetIncomeCommonStock": 21_448_000_000,
			},
			// YTD cumulative per-share values from Q3 10-Q.
			// These are the company-reported 9-month cumulative values.
			arCumPerShare: map[string]float64{
				"EPS": 5.13, "EPSDiluted": 5.11, "DividendsPerBasicCommonShare": 0.73,
			},
			mrCumPerShare: map[string]float64{
				"EPS": 5.13, "EPSDiluted": 5.11, "DividendsPerBasicCommonShare": 0.73,
			},
		}

		It("uses Q3 YTD cumulative for EPS, giving 0.98 instead of 0.99", func() {
			quarters := []synthesizeInput{q1Input, q2Input, q3Input}
			arResult, mrResult := SynthesizeQ4(annualAR, annualMR, aaplAnnualPeriod, quarters)

			Expect(arResult).NotTo(BeNil())
			Expect(mrResult).NotTo(BeNil())

			// EPS: 6.11 - 5.13 = 0.98 (cumulative), NOT 6.11 - 5.12 = 0.99 (sum)
			Expect(arResult["EPS"]).To(BeNumerically("~", 0.98, 1e-9),
				"Q4 EPS must use YTD cumulative (6.11 - 5.13 = 0.98)")
			Expect(mrResult["EPS"]).To(BeNumerically("~", 0.98, 1e-9))

			// EPSDiluted: 6.08 - 5.11 = 0.97
			Expect(arResult["EPSDiluted"]).To(BeNumerically("~", 0.97, 1e-9),
				"Q4 EPSDiluted = 6.08 - 5.11 = 0.97")
			Expect(mrResult["EPSDiluted"]).To(BeNumerically("~", 0.97, 1e-9))

			// DividendsPerBasicCommonShare: 0.98 - 0.73 = 0.25
			Expect(arResult["DividendsPerBasicCommonShare"]).To(BeNumerically("~", 0.25, 1e-9),
				"Q4 DPS = 0.98 - 0.73 = 0.25")
			Expect(mrResult["DividendsPerBasicCommonShare"]).To(BeNumerically("~", 0.25, 1e-9))

			// Integer flow fields still use sum of Q1+Q2+Q3 (exact, no rounding issue).
			Expect(arResult["Revenues"]).To(BeNumerically("==", 94_925_000_000),
				"Q4 Revenues = 391035M - (119580M + 90753M + 85777M)")
			Expect(arResult["NetIncomeCommonStock"]).To(BeNumerically("==", 14_736_000_000),
				"Q4 NetIncome = 93736M - (33916M + 23636M + 21448M)")
		})

		It("falls back to sum when cumulative per-share data is nil", func() {
			// Same inputs but without cumulative per-share maps.
			q3NoCum := synthesizeInput{
				periodEnd:     q3Input.periodEnd,
				arEmit:        q3Input.arEmit,
				mrEmit:        q3Input.mrEmit,
				arCumPerShare: nil,
				mrCumPerShare: nil,
			}

			quarters := []synthesizeInput{q1Input, q2Input, q3NoCum}
			arResult, _ := SynthesizeQ4(annualAR, annualMR, aaplAnnualPeriod, quarters)

			Expect(arResult).NotTo(BeNil())

			// Without cumulative, falls back to sum: 6.11 - (2.19+1.53+1.40) = 0.99
			Expect(arResult["EPS"]).To(BeNumerically("~", 0.99, 1e-9),
				"without cumulative data, Q4 EPS falls back to sum method")
		})

		It("falls back to sum when specific per-share field missing from cumulative map", func() {
			// Cumulative map exists but is missing EPSDiluted.
			q3PartialCum := synthesizeInput{
				periodEnd: q3Input.periodEnd,
				arEmit:    q3Input.arEmit,
				mrEmit:    q3Input.mrEmit,
				arCumPerShare: map[string]float64{
					"EPS": 5.13,
					// EPSDiluted deliberately omitted
				},
				mrCumPerShare: map[string]float64{
					"EPS": 5.13,
				},
			}

			quarters := []synthesizeInput{q1Input, q2Input, q3PartialCum}
			arResult, _ := SynthesizeQ4(annualAR, annualMR, aaplAnnualPeriod, quarters)

			Expect(arResult).NotTo(BeNil())

			// EPS uses cumulative: 6.11 - 5.13 = 0.98
			Expect(arResult["EPS"]).To(BeNumerically("~", 0.98, 1e-9))
			// EPSDiluted falls back to sum: 6.08 - (2.18+1.53+1.40) = 0.97
			Expect(arResult["EPSDiluted"]).To(BeNumerically("~", 0.97, 1e-9))
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

	It("synthesizes Q4 per-share fields using YTD cumulative and overrides TTM at fiscal year-end", func() {
		// Extended CompanyFacts with per-share fields including both
		// single-quarter and YTD cumulative facts, mirroring real AAPL XBRL data.
		//
		// EarningsPerShareBasic:
		//   Q1=2.19, Q2=1.53, Q3=1.40, Annual=6.11
		//   Q2 YTD=3.72, Q3 YTD=5.13 (5.13 != 2.19+1.53+1.40=5.12)
		//   => Q4 = 6.11 - 5.13 = 0.98
		//
		// EarningsPerShareDiluted:
		//   Q1=2.18, Q2=1.53, Q3=1.40, Annual=6.08
		//   Q2 YTD=3.71, Q3 YTD=5.11
		//   => Q4 = 6.08 - 5.11 = 0.97
		//
		// CommonStockDividendsPerShareDeclared:
		//   Q1=0.24, Q2=0.24, Q3=0.25, Annual=0.98
		//   Q2 YTD=0.48, Q3 YTD=0.73
		//   => Q4 = 0.98 - 0.73 = 0.25
		//
		// TTM at fiscal year-end (Sep 30 2024):
		//   Sum of quarterly EPS: 2.19+1.53+1.40+0.98 = 6.10
		//   Annual EPS: 6.11 (overrides the TTM sum)
		//
		// TTM crossing fiscal year (Dec 31 2024, using Q1-next-FY):
		//   EPS = Q1next(2.41) + Q4(0.98) + Q3(1.40) + Q2(1.53) = 6.32
		//   (No annual override, so sum is used as-is)

		cf := &CompanyFacts{
			CIK:        12345,
			EntityName: "Test Corp",
			Facts: map[string][]Fact{
				"Revenues": {
					{Start: q1Start, End: q1End, Val: 110000, Form: "10-Q", Filed: q1Filed},
					{Start: q2Start, End: q2End, Val: 95000, Form: "10-Q", Filed: q2Filed},
					{Start: q3Start, End: q3End, Val: 90000, Form: "10-Q", Filed: q3Filed},
					{Start: fyStart, End: fyEnd, Val: 400000, Form: "10-K", Filed: fyFiled},
					{Start: q1NextStart, End: q1NextEnd, Val: 115000, Form: "10-Q", Filed: q1NextFiled},
				},
				"NetIncomeLossAvailableToCommonStockholdersBasic": {
					{Start: q1Start, End: q1End, Val: 30000, Form: "10-Q", Filed: q1Filed},
					{Start: q2Start, End: q2End, Val: 25000, Form: "10-Q", Filed: q2Filed},
					{Start: q3Start, End: q3End, Val: 22000, Form: "10-Q", Filed: q3Filed},
					{Start: fyStart, End: fyEnd, Val: 100000, Form: "10-K", Filed: fyFiled},
					{Start: q1NextStart, End: q1NextEnd, Val: 32000, Form: "10-Q", Filed: q1NextFiled},
				},
				"EarningsPerShareBasic": {
					// Q1 single-quarter
					{Start: q1Start, End: q1End, Val: 2.19, Form: "10-Q", Filed: q1Filed},
					// Q2 YTD cumulative
					{Start: q1Start, End: q2End, Val: 3.72, Form: "10-Q", Filed: q2Filed},
					// Q2 single-quarter
					{Start: q2Start, End: q2End, Val: 1.53, Form: "10-Q", Filed: q2Filed},
					// Q3 YTD cumulative
					{Start: q1Start, End: q3End, Val: 5.13, Form: "10-Q", Filed: q3Filed},
					// Q3 single-quarter
					{Start: q3Start, End: q3End, Val: 1.40, Form: "10-Q", Filed: q3Filed},
					// 10-K annual
					{Start: fyStart, End: fyEnd, Val: 6.11, Form: "10-K", Filed: fyFiled},
					// Q1 next FY
					{Start: q1NextStart, End: q1NextEnd, Val: 2.41, Form: "10-Q", Filed: q1NextFiled},
				},
				"EarningsPerShareDiluted": {
					{Start: q1Start, End: q1End, Val: 2.18, Form: "10-Q", Filed: q1Filed},
					{Start: q1Start, End: q2End, Val: 3.71, Form: "10-Q", Filed: q2Filed},
					{Start: q2Start, End: q2End, Val: 1.53, Form: "10-Q", Filed: q2Filed},
					{Start: q1Start, End: q3End, Val: 5.11, Form: "10-Q", Filed: q3Filed},
					{Start: q3Start, End: q3End, Val: 1.40, Form: "10-Q", Filed: q3Filed},
					{Start: fyStart, End: fyEnd, Val: 6.08, Form: "10-K", Filed: fyFiled},
					{Start: q1NextStart, End: q1NextEnd, Val: 2.40, Form: "10-Q", Filed: q1NextFiled},
				},
				"CommonStockDividendsPerShareDeclared": {
					{Start: q1Start, End: q1End, Val: 0.24, Form: "10-Q", Filed: q1Filed},
					{Start: q1Start, End: q2End, Val: 0.48, Form: "10-Q", Filed: q2Filed},
					{Start: q2Start, End: q2End, Val: 0.24, Form: "10-Q", Filed: q2Filed},
					{Start: q1Start, End: q3End, Val: 0.73, Form: "10-Q", Filed: q3Filed},
					{Start: q3Start, End: q3End, Val: 0.25, Form: "10-Q", Filed: q3Filed},
					{Start: fyStart, End: fyEnd, Val: 0.98, Form: "10-K", Filed: fyFiled},
					{Start: q1NextStart, End: q1NextEnd, Val: 0.25, Form: "10-Q", Filed: q1NextFiled},
				},
			},
		}

		sub := &library.Subscription{Name: "test"}
		out := make(chan *data.Observation, 200)
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

		q4DateKey := d(2024, 9, 30) // synthesized Q4
		q1NextDateKey := d(2024, 12, 31)

		// --- Q4 ARQ: synthesized per-share values ---
		arqQ4 := observations[obsKey{dimension: "ARQ", dateKey: q4DateKey}]
		Expect(arqQ4).NotTo(BeNil(), "expected ARQ at Q4 date")
		Expect(arqQ4.EPS).To(BeNumerically("~", 0.98, 1e-9),
			"Q4 ARQ EPS = 6.11 - 5.13 = 0.98 (YTD cumulative)")
		Expect(arqQ4.EPSDiluted).To(BeNumerically("~", 0.97, 1e-9),
			"Q4 ARQ EPSDiluted = 6.08 - 5.11 = 0.97")
		Expect(arqQ4.DividendsPerBasicCommonShare).To(BeNumerically("~", 0.25, 1e-9),
			"Q4 ARQ DPS = 0.98 - 0.73 = 0.25")
		Expect(arqQ4.Revenues).To(BeNumerically("==", 105000))
		Expect(arqQ4.NetIncomeCommonStock).To(BeNumerically("==", 23000))

		// --- Q4 MRQ: same as ARQ (no restatements) ---
		mrqQ4 := observations[obsKey{dimension: "MRQ", dateKey: q4DateKey}]
		Expect(mrqQ4).NotTo(BeNil(), "expected MRQ at Q4 date")
		Expect(mrqQ4.EPS).To(BeNumerically("~", 0.98, 1e-9))
		Expect(mrqQ4.EPSDiluted).To(BeNumerically("~", 0.97, 1e-9))
		Expect(mrqQ4.DividendsPerBasicCommonShare).To(BeNumerically("~", 0.25, 1e-9))

		// --- ART at fiscal year-end: per-share fields use annual override ---
		artFYEnd := observations[obsKey{dimension: "ART", dateKey: q4DateKey}]
		Expect(artFYEnd).NotTo(BeNil(), "expected ART at fiscal year-end")
		Expect(artFYEnd.EPS).To(Equal(6.11),
			"ART EPS at fiscal year-end should use annual value (6.11), not sum (6.10)")
		Expect(artFYEnd.EPSDiluted).To(Equal(6.08),
			"ART EPSDiluted at fiscal year-end should use annual value (6.08)")
		Expect(artFYEnd.DividendsPerBasicCommonShare).To(Equal(0.98),
			"ART DPS at fiscal year-end should use annual value (0.98)")
		Expect(artFYEnd.Revenues).To(BeNumerically("==", 400000),
			"ART Revenues at fiscal year-end = FY total")
		Expect(artFYEnd.NetIncomeCommonStock).To(BeNumerically("==", 100000),
			"ART NetIncome at fiscal year-end = FY total")

		// --- MRT at fiscal year-end: same overrides ---
		mrtFYEnd := observations[obsKey{dimension: "MRT", dateKey: q4DateKey}]
		Expect(mrtFYEnd).NotTo(BeNil(), "expected MRT at fiscal year-end")
		Expect(mrtFYEnd.EPS).To(Equal(6.11))
		Expect(mrtFYEnd.EPSDiluted).To(Equal(6.08))
		Expect(mrtFYEnd.DividendsPerBasicCommonShare).To(Equal(0.98))

		// --- ARY at calendar year-end: annual values directly ---
		aryDateKey := d(2024, 12, 31) // 10-K normalized to Dec 31
		ary := observations[obsKey{dimension: "ARY", dateKey: aryDateKey}]
		Expect(ary).NotTo(BeNil(), "expected ARY at 2024-12-31")
		Expect(ary.EPS).To(Equal(6.11))
		Expect(ary.EPSDiluted).To(Equal(6.08))
		Expect(ary.DividendsPerBasicCommonShare).To(Equal(0.98))
		Expect(ary.Revenues).To(BeNumerically("==", 400000))
		Expect(ary.NetIncomeCommonStock).To(BeNumerically("==", 100000))

		// --- MRY: same as ARY ---
		mry := observations[obsKey{dimension: "MRY", dateKey: aryDateKey}]
		Expect(mry).NotTo(BeNil(), "expected MRY at 2024-12-31")
		Expect(mry.EPS).To(Equal(6.11))
		Expect(mry.EPSDiluted).To(Equal(6.08))
		Expect(mry.DividendsPerBasicCommonShare).To(Equal(0.98))

		// --- ART crossing fiscal year boundary (Dec 31 2024) ---
		// TTM = Q1next(2.41) + Q4(0.98) + Q3(1.40) + Q2(1.53) = 6.32
		// No annual override (the TTM window crosses the fiscal year boundary).
		artCross := observations[obsKey{dimension: "ART", dateKey: q1NextDateKey}]
		Expect(artCross).NotTo(BeNil(), "expected ART at 2024-12-31")
		Expect(artCross.EPS).To(BeNumerically("~", 6.32, 1e-9),
			"ART EPS crossing FY boundary = 2.41+0.98+1.40+1.53")
		Expect(artCross.EPSDiluted).To(BeNumerically("~", 6.30, 1e-9),
			"ART EPSDiluted crossing FY boundary = 2.40+0.97+1.40+1.53")
		Expect(artCross.DividendsPerBasicCommonShare).To(BeNumerically("~", 0.99, 1e-9),
			"ART DPS crossing FY boundary = Q1next(0.25)+Q4(0.25)+Q3(0.25)+Q2(0.24)")

		// Individual quarterly ARQ values: verify they use single-quarter (not YTD).
		q1ARQ := observations[obsKey{dimension: "ARQ", dateKey: d(2023, 12, 31)}]
		Expect(q1ARQ).NotTo(BeNil(), "expected Q1 ARQ")
		Expect(q1ARQ.EPS).To(Equal(2.19), "Q1 EPS = single-quarter value")
		Expect(q1ARQ.EPSDiluted).To(Equal(2.18))
		Expect(q1ARQ.DividendsPerBasicCommonShare).To(Equal(0.24))

		q2ARQ := observations[obsKey{dimension: "ARQ", dateKey: d(2024, 3, 31)}]
		Expect(q2ARQ).NotTo(BeNil(), "expected Q2 ARQ")
		Expect(q2ARQ.EPS).To(Equal(1.53), "Q2 EPS = single-quarter value (not YTD 3.72)")
		Expect(q2ARQ.EPSDiluted).To(Equal(1.53))
		Expect(q2ARQ.DividendsPerBasicCommonShare).To(Equal(0.24))

		q3ARQ := observations[obsKey{dimension: "ARQ", dateKey: d(2024, 6, 30)}]
		Expect(q3ARQ).NotTo(BeNil(), "expected Q3 ARQ")
		Expect(q3ARQ.EPS).To(Equal(1.40), "Q3 EPS = single-quarter value (not YTD 5.13)")
		Expect(q3ARQ.EPSDiluted).To(Equal(1.40))
		Expect(q3ARQ.DividendsPerBasicCommonShare).To(Equal(0.25))

		q1NextARQ := observations[obsKey{dimension: "ARQ", dateKey: q1NextDateKey}]
		Expect(q1NextARQ).NotTo(BeNil(), "expected Q1-next ARQ")
		Expect(q1NextARQ.EPS).To(Equal(2.41))
		Expect(q1NextARQ.EPSDiluted).To(Equal(2.40))
		Expect(q1NextARQ.DividendsPerBasicCommonShare).To(Equal(0.25))
	})
})
