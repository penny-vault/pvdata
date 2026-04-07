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
	"os"
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
	emitFundamentals(cf, asset, sub, out, &numObs)
	close(out)
	<-done

	return counts
}
