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
	"fmt"
	"os"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
)

var _ = Describe("Dimensions", func() {
	var cf *CompanyFacts

	BeforeEach(func() {
		jsonData, err := os.ReadFile("testdata/CIK0000320193.json")
		Expect(err).NotTo(HaveOccurred())
		cf, err = ParseCompanyFacts(jsonData)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("IdentifyPeriods", func() {
		It("finds annual and quarterly periods", func() {
			periods := IdentifyPeriods(cf)
			Expect(len(periods)).To(BeNumerically(">", 0))

			hasAnnual := false
			hasQuarterly := false
			for _, p := range periods {
				if p.FormType == "10-K" {
					hasAnnual = true
				}
				if p.FormType == "10-Q" {
					hasQuarterly = true
				}
			}
			Expect(hasAnnual).To(BeTrue())
			Expect(hasQuarterly).To(BeTrue())
		})

		It("includes AR and MR filing dates for each period", func() {
			periods := IdentifyPeriods(cf)
			for _, p := range periods {
				Expect(p.ARFiledDate.IsZero()).To(BeFalse(),
					"period %v should have an AR filing date", p.PeriodEnd)
				Expect(p.MRFiledDate.IsZero()).To(BeFalse(),
					"period %v should have an MR filing date", p.PeriodEnd)
				Expect(p.MRFiledDate).To(BeTemporally(">=", p.ARFiledDate),
					"MR filed date should be >= AR filed date")
			}
		})

		Describe("ghost period deduplication", func() {
			It("collapses periods with end dates 1 day apart that normalize to the same quarter", func() {
				// Two 10-Q filings for "Q3 2018" with end dates 2018-09-29 and
				// 2018-09-30. Both normalize to 2018-09-30 and should collapse
				// into a single canonical period.
				ghostCF := &CompanyFacts{
					CIK:        1,
					EntityName: "Ghost Co",
					Facts: map[string][]Fact{
						"Revenues": {
							{
								Val:   100,
								Start: time.Date(2018, 7, 1, 0, 0, 0, 0, time.UTC),
								End:   time.Date(2018, 9, 29, 0, 0, 0, 0, time.UTC),
								Form:  "10-Q",
								FY:    2018,
								FP:    "Q3",
								Filed: time.Date(2018, 11, 1, 0, 0, 0, 0, time.UTC),
								Accn:  "0000000000-00-000001",
							},
							{
								Val:   105,
								Start: time.Date(2018, 7, 1, 0, 0, 0, 0, time.UTC),
								End:   time.Date(2018, 9, 30, 0, 0, 0, 0, time.UTC),
								Form:  "10-Q",
								FY:    2018,
								FP:    "Q3",
								Filed: time.Date(2018, 11, 15, 0, 0, 0, 0, time.UTC),
								Accn:  "0000000000-00-000002",
							},
						},
					},
				}

				periods := IdentifyPeriods(ghostCF)

				Expect(periods).To(HaveLen(1),
					"two ghost periods within the same quarter should collapse to one")

				p := periods[0]
				Expect(p.FormType).To(Equal("10-Q"))
				// Canonical PeriodEnd is the latest raw end date in the group.
				Expect(p.PeriodEnd).To(Equal(time.Date(2018, 9, 30, 0, 0, 0, 0, time.UTC)))
				// AR filed date is the earliest across the group.
				Expect(p.ARFiledDate).To(Equal(time.Date(2018, 11, 1, 0, 0, 0, 0, time.UTC)))
				// MR filed date is the latest across the group.
				Expect(p.MRFiledDate).To(Equal(time.Date(2018, 11, 15, 0, 0, 0, 0, time.UTC)))
			})

			It("excludes spurious 10-Q periods created by comparative balance sheet data", func() {
				// Simulate Apple's Q1 10-Q (covering Oct-Dec 2024) which includes
				// comparative balance sheet data (instant concepts like Assets)
				// as of the prior fiscal year-end (2024-09-28). IdentifyPeriods
				// should NOT create a 10-Q period at 2024-09-28 -- that date
				// belongs to the 10-K only.
				comparativeCF := &CompanyFacts{
					CIK:        320193,
					EntityName: "Apple Inc",
					Facts: map[string][]Fact{
						"Revenues": {
							// Real Q1 10-Q revenue (duration concept)
							{
								Val:   100000,
								Start: time.Date(2024, 9, 29, 0, 0, 0, 0, time.UTC),
								End:   time.Date(2024, 12, 28, 0, 0, 0, 0, time.UTC),
								Form:  "10-Q",
								FY:    2025,
								FP:    "Q1",
								Filed: time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
							},
							// Annual revenue (duration concept)
							{
								Val:   400000,
								Start: time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC),
								End:   time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC),
								Form:  "10-K",
								FY:    2024,
								FP:    "FY",
								Filed: time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC),
							},
						},
						"Assets": {
							// Q1 10-Q balance sheet (instant concept, current quarter)
							{
								Val:   500000,
								End:   time.Date(2024, 12, 28, 0, 0, 0, 0, time.UTC),
								Form:  "10-Q",
								FY:    2025,
								FP:    "Q1",
								Filed: time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
							},
							// Q1 10-Q comparative balance sheet (instant concept
							// at PRIOR fiscal year-end) -- this is the problematic
							// fact that was creating a spurious 10-Q period.
							{
								Val:   480000,
								End:   time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC),
								Form:  "10-Q",
								FY:    2025,
								FP:    "Q1",
								Filed: time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
							},
							// Annual balance sheet (instant concept)
							{
								Val:   480000,
								End:   time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC),
								Form:  "10-K",
								FY:    2024,
								FP:    "FY",
								Filed: time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC),
							},
						},
					},
				}

				periods := IdentifyPeriods(comparativeCF)

				// Should have exactly 2 periods: one 10-Q for Q1 2025 and one
				// 10-K for FY2024. The comparative balance sheet data at the
				// fiscal year-end should NOT create a third (spurious) 10-Q period.
				Expect(periods).To(HaveLen(2))

				formTypes := map[string]bool{}
				for _, p := range periods {
					formTypes[p.FormType] = true
				}

				Expect(formTypes).To(HaveKey("10-Q"))
				Expect(formTypes).To(HaveKey("10-K"))

				// The 10-Q period should be for Q1 (Dec 2024), not the fiscal year-end (Sep 2024).
				for _, p := range periods {
					if p.FormType == "10-Q" {
						Expect(p.PeriodEnd).To(Equal(time.Date(2024, 12, 28, 0, 0, 0, 0, time.UTC)),
							"10-Q period should be at Q1 end, not at fiscal year-end")
					}
				}
			})

			It("keeps distinct calendar quarters as separate periods", func() {
				// Q2 2018 (2018-06-30) and Q3 2018 (2018-09-30) normalize to
				// different calendar quarter ends and must remain separate.
				distinctCF := &CompanyFacts{
					CIK:        1,
					EntityName: "Distinct Co",
					Facts: map[string][]Fact{
						"Revenues": {
							{
								Val:   100,
								Start: time.Date(2018, 4, 1, 0, 0, 0, 0, time.UTC),
								End:   time.Date(2018, 6, 30, 0, 0, 0, 0, time.UTC),
								Form:  "10-Q",
								FY:    2018,
								FP:    "Q2",
								Filed: time.Date(2018, 8, 1, 0, 0, 0, 0, time.UTC),
								Accn:  "0000000000-00-000003",
							},
							{
								Val:   200,
								Start: time.Date(2018, 7, 1, 0, 0, 0, 0, time.UTC),
								End:   time.Date(2018, 9, 30, 0, 0, 0, 0, time.UTC),
								Form:  "10-Q",
								FY:    2018,
								FP:    "Q3",
								Filed: time.Date(2018, 11, 1, 0, 0, 0, 0, time.UTC),
								Accn:  "0000000000-00-000004",
							},
						},
					},
				}

				periods := IdentifyPeriods(distinctCF)

				Expect(periods).To(HaveLen(2),
					"two distinct calendar quarters should remain separate")
				Expect(periods[0].PeriodEnd).To(Equal(time.Date(2018, 6, 30, 0, 0, 0, 0, time.UTC)))
				Expect(periods[1].PeriodEnd).To(Equal(time.Date(2018, 9, 30, 0, 0, 0, 0, time.UTC)))
			})
		})
	})

	Describe("NormalizeEventDate", func() {
		It("normalizes quarterly date to quarter end", func() {
			// Apple's fiscal Q1 ends late Dec
			d := NormalizeEventDate(time.Date(2018, 12, 29, 0, 0, 0, 0, time.UTC), "10-Q")
			Expect(d).To(Equal(time.Date(2018, 12, 31, 0, 0, 0, 0, time.UTC)))
		})

		It("normalizes annual date to calendar year end", func() {
			// Apple's fiscal year ends late Sep
			d := NormalizeEventDate(time.Date(2018, 9, 29, 0, 0, 0, 0, time.UTC), "10-K")
			Expect(d).To(Equal(time.Date(2018, 12, 31, 0, 0, 0, 0, time.UTC)))
		})

		// Sharadar comparability examples: a quarter ending mid-period should
		// snap to whichever calendar quarter end is closest, not just the next
		// one forward.
		DescribeTable("snaps quarterly period ends to the nearest calendar quarter",
			func(periodEnd, expected time.Time) {
				Expect(NormalizeEventDate(periodEnd, "10-Q")).To(Equal(expected))
			},
			Entry("2018-07-24 -> 2018-06-30 (closer to previous quarter end)",
				time.Date(2018, 7, 24, 0, 0, 0, 0, time.UTC),
				time.Date(2018, 6, 30, 0, 0, 0, 0, time.UTC)),
			Entry("2018-09-26 -> 2018-09-30 (Apple's fiscal Q-end)",
				time.Date(2018, 9, 26, 0, 0, 0, 0, time.UTC),
				time.Date(2018, 9, 30, 0, 0, 0, 0, time.UTC)),
			Entry("2018-09-29 -> 2018-09-30 (1 day before)",
				time.Date(2018, 9, 29, 0, 0, 0, 0, time.UTC),
				time.Date(2018, 9, 30, 0, 0, 0, 0, time.UTC)),
			Entry("2018-12-29 -> 2018-12-31 (2 days before)",
				time.Date(2018, 12, 29, 0, 0, 0, 0, time.UTC),
				time.Date(2018, 12, 31, 0, 0, 0, 0, time.UTC)),
			Entry("2018-04-04 -> 2018-03-31 (4 days past)",
				time.Date(2018, 4, 4, 0, 0, 0, 0, time.UTC),
				time.Date(2018, 3, 31, 0, 0, 0, 0, time.UTC)),
			Entry("2018-08-15 -> 2018-09-30 (tie, snap forward)",
				time.Date(2018, 8, 15, 0, 0, 0, 0, time.UTC),
				time.Date(2018, 9, 30, 0, 0, 0, 0, time.UTC)),
		)
	})

	Describe("emitFundamentals TTM gap detection", func() {
		It("skips TTM when 4-quarter span is too long (missing quarter)", func() {
			cf := buildSyntheticQuarterlyFacts([]time.Time{
				time.Date(2020, 3, 31, 0, 0, 0, 0, time.UTC),
				time.Date(2020, 6, 30, 0, 0, 0, 0, time.UTC),
				// Q3 2020 missing
				time.Date(2020, 12, 31, 0, 0, 0, 0, time.UTC),
				time.Date(2021, 6, 30, 0, 0, 0, 0, time.UTC),
			})

			asset := AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST00", CIK: 1}
			obs := collectObservations(cf, asset)

			// We should still get ARQ/MRQ for each of the 4 quarters but no
			// ART/MRT because the span (2020-03-31 -> 2021-06-30 = ~457 days)
			// is well outside the 270-410 day window.
			Expect(obs["ARQ"]).To(Equal(4))
			Expect(obs["MRQ"]).To(Equal(4))
			Expect(obs["ART"]).To(Equal(0))
			Expect(obs["MRT"]).To(Equal(0))
		})

		It("emits TTM when 4 quarters span ~12 months", func() {
			cf := buildSyntheticQuarterlyFacts([]time.Time{
				time.Date(2020, 3, 31, 0, 0, 0, 0, time.UTC),
				time.Date(2020, 6, 30, 0, 0, 0, 0, time.UTC),
				time.Date(2020, 9, 30, 0, 0, 0, 0, time.UTC),
				time.Date(2020, 12, 31, 0, 0, 0, 0, time.UTC),
			})

			asset := AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST00", CIK: 1}
			obs := collectObservations(cf, asset)

			Expect(obs["ARQ"]).To(Equal(4))
			Expect(obs["MRQ"]).To(Equal(4))
			Expect(obs["ART"]).To(Equal(1))
			Expect(obs["MRT"]).To(Equal(1))
		})
	})

	Describe("emitFundamentals since filter", func() {
		// Build 5 consecutive quarters so a TTM window can be computed for the
		// last quarter. Each quarter is filed 30 days after its period end.
		var (
			cf    *CompanyFacts
			asset AssetInfo
		)

		BeforeEach(func() {
			cf = buildSyntheticQuarterlyFacts([]time.Time{
				time.Date(2020, 3, 31, 0, 0, 0, 0, time.UTC),  // filed 2020-04-30
				time.Date(2020, 6, 30, 0, 0, 0, 0, time.UTC),  // filed 2020-07-30
				time.Date(2020, 9, 30, 0, 0, 0, 0, time.UTC),  // filed 2020-10-30
				time.Date(2020, 12, 31, 0, 0, 0, 0, time.UTC), // filed 2021-01-30
				time.Date(2021, 3, 31, 0, 0, 0, 0, time.UTC),  // filed 2021-04-30
			})
			asset = AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST00", CIK: 1}
		})

		It("emits all periods when since is zero", func() {
			obs := collectObservationsSince(cf, asset, time.Time{})

			// 5 quarters -> 5 ARQ + 5 MRQ. Two TTM windows: one ending at
			// 2020-12-31 (4 quarters of 2020) and one ending at 2021-03-31.
			Expect(obs["ARQ"]).To(Equal(5))
			Expect(obs["MRQ"]).To(Equal(5))
			Expect(obs["ART"]).To(Equal(2))
			Expect(obs["MRT"]).To(Equal(2))
		})

		It("skips periods filed before since", func() {
			// since = 2021-01-01 -> only the 2020-12-31 (filed 2021-01-30)
			// and 2021-03-31 (filed 2021-04-30) periods should be emitted.
			since := time.Date(2021, 1, 1, 0, 0, 0, 0, time.UTC)
			obs := collectObservationsSince(cf, asset, since)

			Expect(obs["ARQ"]).To(Equal(2))
			Expect(obs["MRQ"]).To(Equal(2))

			// Both TTM windows touch a quarter filed on/after since (the
			// 2020-12-31 window includes 2020-12-31 itself; the 2021-03-31
			// window includes 2020-12-31 and 2021-03-31), so both TTMs are
			// re-emitted.
			Expect(obs["ART"]).To(Equal(2))
			Expect(obs["MRT"]).To(Equal(2))
		})

		It("skips TTM windows whose constituent quarters are all older than since", func() {
			// since = 2021-06-01 -> nothing was filed on/after this date so
			// nothing should be emitted.
			since := time.Date(2021, 6, 1, 0, 0, 0, 0, time.UTC)
			obs := collectObservationsSince(cf, asset, since)

			Expect(obs["ARQ"]).To(Equal(0))
			Expect(obs["MRQ"]).To(Equal(0))
			Expect(obs["ART"]).To(Equal(0))
			Expect(obs["MRT"]).To(Equal(0))
		})
	})

	Describe("YTD cash flow de-cumulation", func() {
		It("de-cumulates YTD cash flow values to single-quarter values", func() {
			// Build 3 consecutive quarters with:
			// - Income statement items: both quarterly and YTD facts (should pick quarterly)
			// - Cash flow items: ONLY YTD facts (should be de-cumulated)
			cf := &CompanyFacts{
				CIK:        1,
				EntityName: "YTD Co",
				Facts:      make(map[string][]Fact),
			}

			q1End := time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC)
			q2End := time.Date(2024, 6, 30, 0, 0, 0, 0, time.UTC)
			q3End := time.Date(2024, 9, 30, 0, 0, 0, 0, time.UTC)
			fyStart := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			q1Filed := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
			q2Filed := time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC)
			q3Filed := time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC)

			// Revenues: quarterly facts (90 days each)
			// Q1=100, Q2=120, Q3=110
			cf.Facts["Revenues"] = []Fact{
				{Start: fyStart, End: q1End, Filed: q1Filed, Val: 100, Form: "10-Q", FP: "Q1"},
				// Q2 has both quarterly and YTD
				{Start: q1End.AddDate(0, 0, 1), End: q2End, Filed: q2Filed, Val: 120, Form: "10-Q", FP: "Q2"},
				{Start: fyStart, End: q2End, Filed: q2Filed, Val: 220, Form: "10-Q", FP: "Q2"}, // YTD
				// Q3 has both quarterly and YTD
				{Start: q2End.AddDate(0, 0, 1), End: q3End, Filed: q3Filed, Val: 110, Form: "10-Q", FP: "Q3"},
				{Start: fyStart, End: q3End, Filed: q3Filed, Val: 330, Form: "10-Q", FP: "Q3"}, // YTD
			}

			// NetIncomeLoss: quarterly facts
			cf.Facts["NetIncomeLoss"] = []Fact{
				{Start: fyStart, End: q1End, Filed: q1Filed, Val: 20, Form: "10-Q", FP: "Q1"},
				{Start: q1End.AddDate(0, 0, 1), End: q2End, Filed: q2Filed, Val: 25, Form: "10-Q", FP: "Q2"},
				{Start: fyStart, End: q2End, Filed: q2Filed, Val: 45, Form: "10-Q", FP: "Q2"}, // YTD
				{Start: q2End.AddDate(0, 0, 1), End: q3End, Filed: q3Filed, Val: 22, Form: "10-Q", FP: "Q3"},
				{Start: fyStart, End: q3End, Filed: q3Filed, Val: 67, Form: "10-Q", FP: "Q3"}, // YTD
			}

			// Cash flow (operations): YTD ONLY (no quarterly facts)
			// Q1=50, cumulative Q2=110 (Q2 alone=60), cumulative Q3=180 (Q3 alone=70)
			cf.Facts["NetCashProvidedByUsedInOperatingActivities"] = []Fact{
				{Start: fyStart, End: q1End, Filed: q1Filed, Val: 50, Form: "10-Q", FP: "Q1"},
				{Start: fyStart, End: q2End, Filed: q2Filed, Val: 110, Form: "10-Q", FP: "Q2"},  // YTD
				{Start: fyStart, End: q3End, Filed: q3Filed, Val: 180, Form: "10-Q", FP: "Q3"},  // YTD
			}

			// CapEx: YTD ONLY
			// Q1=10, cumulative Q2=22 (Q2 alone=12), cumulative Q3=35 (Q3 alone=13)
			cf.Facts["PaymentsToAcquirePropertyPlantAndEquipment"] = []Fact{
				{Start: fyStart, End: q1End, Filed: q1Filed, Val: 10, Form: "10-Q", FP: "Q1"},
				{Start: fyStart, End: q2End, Filed: q2Filed, Val: 22, Form: "10-Q", FP: "Q2"},
				{Start: fyStart, End: q3End, Filed: q3Filed, Val: 35, Form: "10-Q", FP: "Q3"},
			}

			// Balance sheet (instant, for period identification support)
			cf.Facts["Assets"] = []Fact{
				{End: q1End, Filed: q1Filed, Val: 1000, Form: "10-Q", FP: "Q1"},
				{End: q2End, Filed: q2Filed, Val: 1050, Form: "10-Q", FP: "Q2"},
				{End: q3End, Filed: q3Filed, Val: 1100, Form: "10-Q", FP: "Q3"},
			}

			asset := AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST00", CIK: 1}
			out := make(chan *data.Observation, 256)
			sub := &library.Subscription{Name: "test"}

			done := make(chan struct{})
			results := make(map[string]map[string]*data.Fundamental)

			go func() {
				for obs := range out {
					dim := obs.Fundamental.Dimension
					dateKey := obs.ObservationDate.Format("2006-01-02")
					key := dim + ":" + dateKey
					if results[key] == nil {
						results[key] = make(map[string]*data.Fundamental)
					}
					results[key][dim] = obs.Fundamental
				}
				close(done)
			}()

			numObs := 0
			emitFundamentals(cf, asset, sub, time.Time{}, out, &numObs)
			close(out)
			<-done

			// Q1 (2024-03-31): no de-cumulation needed
			q1ARQ := results["ARQ:2024-03-31"]["ARQ"]
			Expect(q1ARQ).NotTo(BeNil())
			Expect(q1ARQ.Revenues).To(Equal(int64(100)))
			Expect(q1ARQ.NetCashFlowFromOperations).To(Equal(int64(50)))
			Expect(q1ARQ.CapitalExpenditure).To(Equal(int64(-10)),
				"cap-ex should be negated: -(10) = -10")

			// Q2 (2024-06-30): should be de-cumulated
			q2ARQ := results["ARQ:2024-06-30"]["ARQ"]
			Expect(q2ARQ).NotTo(BeNil())
			Expect(q2ARQ.Revenues).To(Equal(int64(120)),
				"revenues should be quarterly (shorter-duration preference), not YTD")
			Expect(q2ARQ.NetCashFlowFromOperations).To(Equal(int64(60)),
				"cash flow should be de-cumulated: 110 (YTD) - 50 (Q1) = 60")
			Expect(q2ARQ.CapitalExpenditure).To(Equal(int64(-12)),
				"cap-ex should be de-cumulated then negated: -(22 - 10) = -12")

			// Q3 (2024-09-30): should be de-cumulated using Q2's ORIGINAL YTD
			q3ARQ := results["ARQ:2024-09-30"]["ARQ"]
			Expect(q3ARQ).NotTo(BeNil())
			Expect(q3ARQ.Revenues).To(Equal(int64(110)),
				"revenues should be quarterly")
			Expect(q3ARQ.NetCashFlowFromOperations).To(Equal(int64(70)),
				"cash flow should be de-cumulated: 180 (YTD) - 110 (Q2 YTD) = 70")
			Expect(q3ARQ.CapitalExpenditure).To(Equal(int64(-13)),
				"cap-ex should be de-cumulated then negated: -(35 - 22) = -13")

			// FreeCashFlow = NetCashFlowFromOperations + CapitalExpenditure (CapEx is negative)
			Expect(q2ARQ.FreeCashFlow).To(Equal(int64(48)),
				"free cash flow should be re-derived from de-cumulated components: 60 + (-12) = 48")
			Expect(q3ARQ.FreeCashFlow).To(Equal(int64(57)),
				"free cash flow should be re-derived: 70 + (-13) = 57")
		})
	})

	// BuildFundamental is a long manual switch over fields["X"] keys. Without
	// this guard, adding a new entry to FieldMappings without a corresponding
	// clause in BuildFundamental would silently drop the field from emitted
	// observations. Catch that at test time by static-analyzing the source.
	Describe("BuildFundamental coverage", func() {
		It("references every FieldMappings entry", func() {
			src, err := os.ReadFile("dimensions.go")
			Expect(err).NotTo(HaveOccurred())

			srcStr := string(src)

			// Intermediate fields used only as operands for derived fields;
			// they are intentionally not mapped to Fundamental struct fields.
			intermediate := map[string]bool{
				"TradeReceivables":            true,
				"NonTradeReceivables":         true,
				"ShortTermDebt":               true,
				"LongTermDebtCurrentMaturities": true,
				"CommercialPaperDebt":         true,
			}

			var missing []string

			for _, m := range FieldMappings {
				if intermediate[m.FieldName] {
					continue
				}

				pattern := fmt.Sprintf(`fields[%q]`, m.FieldName)
				if !strings.Contains(srcStr, pattern) {
					missing = append(missing, m.FieldName)
				}
			}

			Expect(missing).To(BeEmpty(),
				"FieldMappings entries not referenced in BuildFundamental: %v", missing)
		})
	})
})

// buildSyntheticQuarterlyFacts constructs a minimal CompanyFacts containing one
// 10-Q fact per requested period end. Each period gets a small set of mapped
// concepts so ResolveFieldsForFiling/ComputeTTM have data to work with.
func buildSyntheticQuarterlyFacts(periodEnds []time.Time) *CompanyFacts {
	cf := &CompanyFacts{
		CIK:        1,
		EntityName: "Test Co",
		Facts:      make(map[string][]Fact),
	}

	concepts := []string{
		"Revenues",
		"NetIncomeLoss",
		"Assets",
		"Liabilities",
		"StockholdersEquity",
	}

	for _, end := range periodEnds {
		// Use a 90-day reporting period ending at `end`.
		start := end.AddDate(0, 0, -89)
		filed := end.AddDate(0, 0, 30)

		for _, concept := range concepts {
			cf.Facts[concept] = append(cf.Facts[concept], Fact{
				Val:   1000,
				Start: start,
				End:   end,
				Form:  "10-Q",
				FY:    end.Year(),
				FP:    "Q1",
				Filed: filed,
				Accn:  "0000000000-00-000000",
			})
		}
	}

	return cf
}

// collectObservations runs emitFundamentals and returns a count per dimension.
func collectObservations(cf *CompanyFacts, asset AssetInfo) map[string]int {
	return collectObservationsSince(cf, asset, time.Time{})
}

// collectObservationsSince runs emitFundamentals with the given since cutoff
// and returns a count per dimension.
func collectObservationsSince(cf *CompanyFacts, asset AssetInfo, since time.Time) map[string]int {
	out := make(chan *data.Observation, 256)
	sub := &library.Subscription{Name: "test"}

	done := make(chan struct{})
	counts := make(map[string]int)

	go func() {
		for obs := range out {
			counts[obs.Fundamental.Dimension]++
		}
		close(done)
	}()

	numObs := 0
	emitFundamentals(cf, asset, sub, since, out, &numObs)
	close(out)
	<-done

	return counts
}
