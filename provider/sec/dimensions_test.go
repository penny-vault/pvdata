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
	"math"
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

			It("excludes spurious 10-K periods created by short-duration quarterly facts", func() {
				// SEC 10-K filings often include quarterly-duration facts
				// (e.g. current-quarter revenue or comparative quarterly data)
				// alongside full-year data. These short-duration facts share
				// Form="10-K" but their End dates correspond to quarter boundaries.
				// Without filtering, they create spurious 10-K periods that merge
				// with the real annual period during NormalizeEventDate dedup
				// (both snap to Dec 31), shifting PeriodEnd to December.
				shortDurationCF := &CompanyFacts{
					CIK:        320193,
					EntityName: "Apple Inc",
					Facts: map[string][]Fact{
						"Revenues": {
							// Full-year fact (genuine 10-K period)
							{
								Val:   400000,
								Start: time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC),
								End:   time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC),
								Form:  "10-K",
								FY:    2024,
								FP:    "FY",
								Filed: time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC),
							},
							// Q1 quarterly-duration fact within the 10-K filing
							{
								Val:   110000,
								Start: time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC),
								End:   time.Date(2023, 12, 30, 0, 0, 0, 0, time.UTC),
								Form:  "10-K",
								FY:    2024,
								FP:    "Q1",
								Filed: time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC),
							},
						},
					},
				}

				periods := IdentifyPeriods(shortDurationCF)

				// Should have exactly 1 10-K period at the actual fiscal year-end.
				Expect(periods).To(HaveLen(1))
				Expect(periods[0].FormType).To(Equal("10-K"))
				// PeriodEnd must be the annual End date (Sep 28), NOT the
				// quarterly End date (Dec 30) that would result from merging.
				Expect(periods[0].PeriodEnd).To(Equal(
					time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC)))
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
				{Start: fyStart, End: q2End, Filed: q2Filed, Val: 110, Form: "10-Q", FP: "Q2"}, // YTD
				{Start: fyStart, End: q3End, Filed: q3Filed, Val: 180, Form: "10-Q", FP: "Q3"}, // YTD
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

	Describe("ComputeMultiQAverages", func() {
		It("computes all 6 fields from 4 quarterly snapshots", func() {
			current := map[string]float64{
				"TotalAssets":          400_000,
				"Equity":               200_000,
				"InvestedCapital":      300_000,
				"NetIncomeCommonStock": 50_000,
				"EBIT":                 60_000,
			}
			q1 := map[string]float64{"TotalAssets": 340_000, "Equity": 170_000, "InvestedCapital": 260_000}
			q2 := map[string]float64{"TotalAssets": 360_000, "Equity": 180_000, "InvestedCapital": 280_000}
			q3 := map[string]float64{"TotalAssets": 380_000, "Equity": 190_000, "InvestedCapital": 290_000}
			q4 := map[string]float64{"TotalAssets": 400_000, "Equity": 200_000, "InvestedCapital": 300_000}

			result := ComputeMultiQAverages(current, []map[string]float64{q1, q2, q3, q4})

			// Averages: (340+360+380+400)/4=370, (170+180+190+200)/4=185, (260+280+290+300)/4=282.5
			Expect(result["AverageAssets"]).To(Equal(370_000.0))
			Expect(result["EquityAvg"]).To(Equal(185_000.0))
			Expect(result["InvestedCapitalAverage"]).To(Equal(282_500.0))

			// Ratios (rounded to 3 decimal places)
			Expect(result["ROA"]).To(BeNumerically("~", math.Round(50_000.0/370_000.0*1000)/1000, 1e-10))
			Expect(result["ROE"]).To(BeNumerically("~", math.Round(50_000.0/185_000.0*1000)/1000, 1e-10))
			Expect(result["ROIC"]).To(BeNumerically("~", math.Round(60_000.0/282_500.0*1000)/1000, 1e-10))
		})

		It("averages only quarters that contain the field", func() {
			current := map[string]float64{
				"TotalAssets":          400_000,
				"Equity":               200_000,
				"NetIncomeCommonStock": 50_000,
				"EBIT":                 60_000,
			}
			q1 := map[string]float64{"TotalAssets": 360_000}
			q2 := map[string]float64{"TotalAssets": 400_000, "Equity": 200_000}

			result := ComputeMultiQAverages(current, []map[string]float64{q1, q2})

			Expect(result["AverageAssets"]).To(Equal(380_000.0))
			Expect(result).To(HaveKey("ROA"))
			Expect(result["EquityAvg"]).To(Equal(200_000.0)) // only 1 quarter has it
			Expect(result).To(HaveKey("ROE"))
		})

		It("omits ratio when numerator is missing", func() {
			current := map[string]float64{
				"TotalAssets": 400_000,
				"Equity":      200_000,
			}
			q1 := map[string]float64{"TotalAssets": 360_000, "Equity": 180_000}
			q2 := map[string]float64{"TotalAssets": 400_000, "Equity": 200_000}

			result := ComputeMultiQAverages(current, []map[string]float64{q1, q2})

			Expect(result).To(HaveKey("AverageAssets"))
			Expect(result).To(HaveKey("EquityAvg"))
			Expect(result).NotTo(HaveKey("ROA"))
			Expect(result).NotTo(HaveKey("ROE"))
		})

		It("omits ratio when average denominator is zero", func() {
			current := map[string]float64{
				"TotalAssets":          100,
				"NetIncomeCommonStock": 50,
			}
			q1 := map[string]float64{"TotalAssets": 100}
			q2 := map[string]float64{"TotalAssets": -100}

			result := ComputeMultiQAverages(current, []map[string]float64{q1, q2})

			Expect(result).To(HaveKey("AverageAssets"))
			Expect(result["AverageAssets"]).To(Equal(0.0))
			Expect(result).NotTo(HaveKey("ROA"))
		})

		It("returns empty map when quarter slice is empty", func() {
			current := map[string]float64{
				"TotalAssets":          400_000,
				"Equity":               200_000,
				"NetIncomeCommonStock": 50_000,
			}

			result := ComputeMultiQAverages(current, nil)
			Expect(result).To(BeEmpty())
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
				"TradeReceivables":              true,
				"NonTradeReceivables":           true,
				"ShortTermDebt":                 true,
				"LongTermDebtCurrentMaturities": true,
				"CommercialPaperDebt":           true,
				"_proceedsDebt":                 true,
				"_repaymentsDebt":               true,
				"_netShortTermDebt":             true,
				"_paymentsInvest":               true,
				"_proceedsInvest":               true,
				"_proceedsInvestMaturities":     true,
				"_proceedsInvestSales":          true,
				"_generalAndAdministrativeExpense": true,
				"_sellingAndMarketingExpense":      true,
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

		It("references all period-average fields", func() {
			src, err := os.ReadFile("dimensions.go")
			Expect(err).NotTo(HaveOccurred())
			srcStr := string(src)

			avgFields := []string{
				"AverageAssets", "EquityAvg", "InvestedCapitalAverage",
				"ROA", "ROE", "ROIC",
			}

			var missing []string
			for _, name := range avgFields {
				pattern := fmt.Sprintf(`fields[%q]`, name)
				if !strings.Contains(srcStr, pattern) {
					missing = append(missing, name)
				}
			}

			Expect(missing).To(BeEmpty(),
				"period-average fields not referenced in BuildFundamental: %v", missing)
		})
	})

	Describe("quarterly period averages excluded (#56)", func() {
		It("does not compute averages or ratios for ARQ/MRQ", func() {
			cf := &CompanyFacts{
				CIK:        1,
				EntityName: "Avg Co",
				Facts:      make(map[string][]Fact),
			}

			q1End := time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC)
			q2End := time.Date(2024, 6, 30, 0, 0, 0, 0, time.UTC)
			q1Filed := time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)
			q2Filed := time.Date(2024, 8, 1, 0, 0, 0, 0, time.UTC)
			fyStart := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

			// Balance sheet (instant)
			cf.Facts["Assets"] = []Fact{
				{End: q1End, Filed: q1Filed, Val: 1000, Form: "10-Q"},
				{End: q2End, Filed: q2Filed, Val: 1200, Form: "10-Q"},
			}
			cf.Facts["StockholdersEquity"] = []Fact{
				{End: q1End, Filed: q1Filed, Val: 500, Form: "10-Q"},
				{End: q2End, Filed: q2Filed, Val: 600, Form: "10-Q"},
			}

			// Income statement (duration, quarterly facts)
			cf.Facts["NetIncomeLossAvailableToCommonStockholdersBasic"] = []Fact{
				{Start: fyStart, End: q1End, Filed: q1Filed, Val: 100, Form: "10-Q"},
				{Start: q1End.AddDate(0, 0, 1), End: q2End, Filed: q2Filed, Val: 120, Form: "10-Q"},
			}
			cf.Facts["Revenues"] = []Fact{
				{Start: fyStart, End: q1End, Filed: q1Filed, Val: 500, Form: "10-Q"},
				{Start: q1End.AddDate(0, 0, 1), End: q2End, Filed: q2Filed, Val: 600, Form: "10-Q"},
			}
			cf.Facts["OperatingIncomeLoss"] = []Fact{
				{Start: fyStart, End: q1End, Filed: q1Filed, Val: 200, Form: "10-Q"},
				{Start: q1End.AddDate(0, 0, 1), End: q2End, Filed: q2Filed, Val: 250, Form: "10-Q"},
			}

			asset := AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST00", CIK: 1}
			out := make(chan *data.Observation, 256)
			sub := &library.Subscription{Name: "test"}

			done := make(chan struct{})
			var q2Fund *data.Fundamental

			go func() {
				for obs := range out {
					f := obs.Fundamental
					dateKey := obs.ObservationDate.Format("2006-01-02")
					if f.Dimension == "ARQ" && dateKey == "2024-06-30" {
						q2Fund = f
					}
				}
				close(done)
			}()

			numObs := 0
			emitFundamentals(cf, asset, sub, time.Time{}, out, &numObs)
			close(out)
			<-done

			// Q2 has a prior quarter but averages and ratios must still be
			// zero because these fields are excluded for ARQ/MRQ (#56).
			Expect(q2Fund).NotTo(BeNil())
			Expect(q2Fund.AverageAssets).To(Equal(int64(0)))
			Expect(q2Fund.EquityAvg).To(Equal(int64(0)))
			Expect(q2Fund.InvestedCapitalAverage).To(Equal(int64(0)))
			Expect(q2Fund.ROA).To(Equal(0.0))
			Expect(q2Fund.ROE).To(Equal(0.0))
			Expect(q2Fund.ROIC).To(Equal(0.0))
			Expect(q2Fund.AssetTurnover).To(Equal(0.0))
			Expect(q2Fund.ReturnOnSales).To(Equal(0.0))
		})
	})

	Describe("annual period averages", func() {
		It("computes averages from consecutive annual periods", func() {
			cf := &CompanyFacts{
				CIK:        1,
				EntityName: "Annual Co",
				Facts:      make(map[string][]Fact),
			}

			fy1End := time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC)
			fy2End := time.Date(2024, 9, 28, 0, 0, 0, 0, time.UTC)
			fy1Filed := time.Date(2023, 11, 1, 0, 0, 0, 0, time.UTC)
			fy2Filed := time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC)

			// Balance sheet (instant)
			cf.Facts["Assets"] = []Fact{
				{End: fy1End, Filed: fy1Filed, Val: 2000, Form: "10-K"},
				{End: fy2End, Filed: fy2Filed, Val: 2400, Form: "10-K"},
			}
			cf.Facts["StockholdersEquity"] = []Fact{
				{End: fy1End, Filed: fy1Filed, Val: 800, Form: "10-K"},
				{End: fy2End, Filed: fy2Filed, Val: 1000, Form: "10-K"},
			}

			// Income statement (duration)
			cf.Facts["NetIncomeLossAvailableToCommonStockholdersBasic"] = []Fact{
				{Start: time.Date(2022, 10, 1, 0, 0, 0, 0, time.UTC), End: fy1End, Filed: fy1Filed, Val: 200, Form: "10-K"},
				{Start: time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC), End: fy2End, Filed: fy2Filed, Val: 250, Form: "10-K"},
			}
			cf.Facts["Revenues"] = []Fact{
				{Start: time.Date(2022, 10, 1, 0, 0, 0, 0, time.UTC), End: fy1End, Filed: fy1Filed, Val: 1000, Form: "10-K"},
				{Start: time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC), End: fy2End, Filed: fy2Filed, Val: 1200, Form: "10-K"},
			}

			asset := AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST00", CIK: 1}
			out := make(chan *data.Observation, 256)
			sub := &library.Subscription{Name: "test"}

			done := make(chan struct{})
			var fy1Fund, fy2Fund *data.Fundamental

			go func() {
				for obs := range out {
					f := obs.Fundamental
					dateKey := obs.ObservationDate.Format("2006-01-02")
					if f.Dimension == "ARY" && dateKey == "2023-12-31" {
						fy1Fund = f
					}
					if f.Dimension == "ARY" && dateKey == "2024-12-31" {
						fy2Fund = f
					}
				}
				close(done)
			}()

			numObs := 0
			emitFundamentals(cf, asset, sub, time.Time{}, out, &numObs)
			close(out)
			<-done

			// FY1: no prior year, averages absent
			Expect(fy1Fund).NotTo(BeNil())
			Expect(fy1Fund.AverageAssets).To(Equal(int64(0)))

			// FY2: has prior year
			Expect(fy2Fund).NotTo(BeNil())
			Expect(fy2Fund.AverageAssets).To(Equal(int64(2200))) // (2000+2400)/2
			Expect(fy2Fund.EquityAvg).To(Equal(int64(900)))      // (800+1000)/2
			Expect(fy2Fund.ROA).To(BeNumerically("~", math.Round(250.0/2200.0*1000)/1000, 1e-10))
			Expect(fy2Fund.ROE).To(BeNumerically("~", math.Round(250.0/900.0*1000)/1000, 1e-10))
		})
	})

	Describe("TTM period averages", func() {
		It("computes averages using quarter before TTM window", func() {
			cf := &CompanyFacts{
				CIK:        1,
				EntityName: "TTM Co",
				Facts:      make(map[string][]Fact),
			}

			// 5 consecutive quarters: q0 through q4.
			// TTM window for q4 = [q1..q4], prior balance sheet = q0.
			ends := []time.Time{
				time.Date(2023, 3, 31, 0, 0, 0, 0, time.UTC),
				time.Date(2023, 6, 30, 0, 0, 0, 0, time.UTC),
				time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC),
				time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC),
				time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
			}

			for i, end := range ends {
				start := end.AddDate(0, 0, -89)
				filed := end.AddDate(0, 0, 30)
				assets := float64(1000 + i*100) // 1000, 1100, 1200, 1300, 1400
				equity := float64(500 + i*50)   // 500, 550, 600, 650, 700

				cf.Facts["Assets"] = append(cf.Facts["Assets"],
					Fact{End: end, Filed: filed, Val: assets, Form: "10-Q"})
				cf.Facts["StockholdersEquity"] = append(cf.Facts["StockholdersEquity"],
					Fact{End: end, Filed: filed, Val: equity, Form: "10-Q"})
				cf.Facts["Revenues"] = append(cf.Facts["Revenues"],
					Fact{Start: start, End: end, Filed: filed, Val: 200, Form: "10-Q"})
				cf.Facts["NetIncomeLossAvailableToCommonStockholdersBasic"] = append(
					cf.Facts["NetIncomeLossAvailableToCommonStockholdersBasic"],
					Fact{Start: start, End: end, Filed: filed, Val: 50, Form: "10-Q"})
				cf.Facts["NetIncomeLoss"] = append(cf.Facts["NetIncomeLoss"],
					Fact{Start: start, End: end, Filed: filed, Val: 50, Form: "10-Q"})
			}

			asset := AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST00", CIK: 1}
			out := make(chan *data.Observation, 256)
			sub := &library.Subscription{Name: "test"}

			done := make(chan struct{})
			var ttmFund *data.Fundamental

			go func() {
				for obs := range out {
					if obs.Fundamental.Dimension == "ART" {
						ttmFund = obs.Fundamental
					}
				}
				close(done)
			}()

			numObs := 0
			emitFundamentals(cf, asset, sub, time.Time{}, out, &numObs)
			close(out)
			<-done

			// TTM window = q1..q4 (4-quarter average of balance sheets)
			// AverageAssets = (1100+1200+1300+1400)/4 = 1250
			// EquityAvg = (550+600+650+700)/4 = 625
			// TTM NetIncomeCommonStock = 50*4 = 200
			// ROA = 200 / 1250
			Expect(ttmFund).NotTo(BeNil())
			Expect(ttmFund.AverageAssets).To(Equal(int64(1250)))
			Expect(ttmFund.EquityAvg).To(Equal(int64(625)))
			Expect(ttmFund.ROA).To(BeNumerically("~", 200.0/1250.0, 1e-10))
			Expect(ttmFund.ROE).To(BeNumerically("~", 200.0/625.0, 1e-10))
		})

		It("computes TTM averages with exactly 4 quarters", func() {
			// 4 quarters is sufficient for 4-quarter averaging (no extra prior needed)
			cf := buildSyntheticQuarterlyFacts([]time.Time{
				time.Date(2023, 6, 30, 0, 0, 0, 0, time.UTC),
				time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC),
				time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC),
				time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
			})

			asset := AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST00", CIK: 1}
			out := make(chan *data.Observation, 256)
			sub := &library.Subscription{Name: "test"}

			done := make(chan struct{})
			var ttmFund *data.Fundamental

			go func() {
				for obs := range out {
					if obs.Fundamental.Dimension == "ART" {
						ttmFund = obs.Fundamental
					}
				}
				close(done)
			}()

			numObs := 0
			emitFundamentals(cf, asset, sub, time.Time{}, out, &numObs)
			close(out)
			<-done

			// All synthetic quarters have Assets=1000, so 4-quarter avg = 1000
			Expect(ttmFund).NotTo(BeNil())
			Expect(ttmFund.AverageAssets).To(Equal(int64(1000)))
		})
	})

	Describe("AAPL validation against Sharadar", func() {
		// These tests run emitFundamentals on real Apple XBRL data and
		// verify the output matches known Sharadar values.

		var observations map[string]*data.Fundamental // key: "YYYY-MM-DD:DIM"

		BeforeEach(func() {
			jsonData, err := os.ReadFile("testdata/CIK0000320193.json")
			Expect(err).NotTo(HaveOccurred())
			aaplCF, err := ParseCompanyFacts(jsonData)
			Expect(err).NotTo(HaveOccurred())

			asset := AssetInfo{
				Ticker:        "AAPL",
				CompositeFigi: "BBG000B9XRY4",
				CIK:           320193,
			}
			out := make(chan *data.Observation, 4096)
			sub := &library.Subscription{Name: "SEC Fundamentals"}

			done := make(chan struct{})
			observations = make(map[string]*data.Fundamental)

			go func() {
				for obs := range out {
					key := obs.ObservationDate.Format("2006-01-02") + ":" + obs.Fundamental.Dimension
					observations[key] = obs.Fundamental
				}
				close(done)
			}()

			numObs := 0
			emitFundamentals(aaplCF, asset, sub, time.Time{}, out, &numObs)
			close(out)
			<-done
		})

		It("computes NetCashFlowDebt including commercial paper", func() {
			// Q2 FY2025 (2025-03-31 ARQ): Sharadar NCFDEBT = 976,000,000
			// = ProceedsLongTerm(0) - RepaymentsLongTerm(3,000M) + CommercialPaper(3,976M)
			f := observations["2025-03-31:ARQ"]
			Expect(f).NotTo(BeNil())
			Expect(f.NetCashFlowDebt).To(Equal(int64(976_000_000)))

			// Q3 FY2025 (2025-06-30 ARQ): Sharadar NCFDEBT = 2,711,000,000
			f = observations["2025-06-30:ARQ"]
			Expect(f).NotTo(BeNil())
			Expect(f.NetCashFlowDebt).To(Equal(int64(2_711_000_000)))

			// Q4 FY2025 (2025-09-30 ARQ): Sharadar NCFDEBT = -3,217,000,000
			f = observations["2025-09-30:ARQ"]
			Expect(f).NotTo(BeNil())
			Expect(f.NetCashFlowDebt).To(Equal(int64(-3_217_000_000)))
		})

		It("computes NetCashFlowInvest as sum of maturities + sales - purchases", func() {
			// Q2 FY2025 (2025-03-31 ARQ): Sharadar NCFINV = 6,020,000,000
			f := observations["2025-03-31:ARQ"]
			Expect(f).NotTo(BeNil())
			Expect(f.NetCashFlowInvest).To(Equal(int64(6_020_000_000)))

			// Q3 FY2025 (2025-06-30 ARQ): Sharadar NCFINV = 8,875,000,000
			f = observations["2025-06-30:ARQ"]
			Expect(f).NotTo(BeNil())
			Expect(f.NetCashFlowInvest).To(Equal(int64(8_875_000_000)))
		})

		It("computes 4-quarter AverageAssets for ART", func() {
			// 2025-09-30 ART: Sharadar average_assets = 341,513,500,000
			// = (Q1:344,085M + Q2:331,233M + Q3:331,495M + Q4:359,241M) / 4
			f := observations["2025-09-30:ART"]
			Expect(f).NotTo(BeNil())
			Expect(f.AverageAssets).To(Equal(int64(341_513_500_000)))
		})

		It("computes 4-quarter EquityAvg for ART", func() {
			// 2025-03-31 ART: Sharadar equity_avg = 64,303,000,000
			// = (Q3_FY24:66,708M + Q4_FY24:56,950M + Q1_FY25:66,758M + Q2_FY25:66,796M) / 4
			f := observations["2025-03-31:ART"]
			Expect(f).NotTo(BeNil())
			Expect(f.EquityAvg).To(Equal(int64(64_303_000_000)))
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

var _ = Describe("ResolveCumulativePerShareForFiling", func() {
	It("resolves YTD cumulative per-share values from 10-Q facts", func() {
		// Apple Q3 FY2024 10-Q: filed 2024-08-02, period ending 2024-06-29.
		// EarningsPerShareBasic has both single-quarter (90d) and YTD (273d) facts.
		q3End := time.Date(2024, 6, 29, 0, 0, 0, 0, time.UTC)
		fyStart := time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC)
		q3Start := time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC)
		filed := time.Date(2024, 8, 2, 0, 0, 0, 0, time.UTC)

		cf := &CompanyFacts{
			CIK:        320193,
			EntityName: "Apple Inc",
			Facts: map[string][]Fact{
				"EarningsPerShareBasic": {
					// YTD 9-month cumulative
					{Start: fyStart, End: q3End, Val: 5.13, Form: "10-Q", Filed: filed},
					// Single quarter (Q3 only)
					{Start: q3Start, End: q3End, Val: 1.40, Form: "10-Q", Filed: filed},
				},
				"EarningsPerShareDiluted": {
					{Start: fyStart, End: q3End, Val: 5.11, Form: "10-Q", Filed: filed},
					{Start: q3Start, End: q3End, Val: 1.40, Form: "10-Q", Filed: filed},
				},
				"CommonStockDividendsPerShareDeclared": {
					{Start: fyStart, End: q3End, Val: 0.73, Form: "10-Q", Filed: filed},
					{Start: q3Start, End: q3End, Val: 0.25, Form: "10-Q", Filed: filed},
				},
			},
		}

		result := ResolveCumulativePerShareForFiling(cf, q3End, "10-Q", filed)

		Expect(result).NotTo(BeNil())
		Expect(result["EPS"]).To(Equal(5.13),
			"should resolve YTD cumulative EPS (longest duration), not single-quarter (1.40)")
		Expect(result["EPSDiluted"]).To(Equal(5.11))
		Expect(result["DividendsPerBasicCommonShare"]).To(Equal(0.73))
	})

	It("returns nil for 10-K form type", func() {
		cf := &CompanyFacts{CIK: 1, EntityName: "Test"}
		result := ResolveCumulativePerShareForFiling(cf, time.Now(), "10-K", time.Now())
		Expect(result).To(BeNil())
	})

	It("respects filing date filter", func() {
		q3End := time.Date(2024, 6, 29, 0, 0, 0, 0, time.UTC)
		fyStart := time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC)
		q3Start := time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC)
		earlyFiled := time.Date(2024, 8, 2, 0, 0, 0, 0, time.UTC)
		lateFiled := time.Date(2025, 8, 1, 0, 0, 0, 0, time.UTC)

		cf := &CompanyFacts{
			CIK:        320193,
			EntityName: "Apple Inc",
			Facts: map[string][]Fact{
				"EarningsPerShareBasic": {
					// Original filing: YTD = 5.13
					{Start: fyStart, End: q3End, Val: 5.13, Form: "10-Q", Filed: earlyFiled},
					{Start: q3Start, End: q3End, Val: 1.40, Form: "10-Q", Filed: earlyFiled},
					// Later restatement: YTD = 5.15
					{Start: fyStart, End: q3End, Val: 5.15, Form: "10-Q", Filed: lateFiled},
					{Start: q3Start, End: q3End, Val: 1.42, Form: "10-Q", Filed: lateFiled},
				},
			},
		}

		// AR view: only facts filed on or before earlyFiled.
		arResult := ResolveCumulativePerShareForFiling(cf, q3End, "10-Q", earlyFiled)
		Expect(arResult["EPS"]).To(Equal(5.13),
			"AR view should use original filing (5.13)")

		// MR view: include all facts (up to lateFiled).
		mrResult := ResolveCumulativePerShareForFiling(cf, q3End, "10-Q", lateFiled)
		Expect(mrResult["EPS"]).To(Equal(5.15),
			"MR view should use latest restatement (5.15)")
	})
})

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
