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
)

var _ = Describe("CompanyFacts Parser", func() {
	var (
		cf      *CompanyFacts
		rawJSON []byte
	)

	BeforeEach(func() {
		var err error

		rawJSON, err = os.ReadFile("testdata/CIK0000320193.json")
		Expect(err).NotTo(HaveOccurred())

		cf, err = ParseCompanyFacts(rawJSON)
		Expect(err).NotTo(HaveOccurred())
	})

	It("parses the CIK", func() {
		Expect(cf.CIK).To(Equal(320193))
	})

	It("parses the entity name", func() {
		Expect(cf.EntityName).To(Equal("Apple Inc."))
	})

	It("parses US-GAAP concepts", func() {
		Expect(cf.Facts).To(HaveKey("Assets"))
		Expect(cf.Facts).To(HaveKey("Revenues"))
		Expect(cf.Facts).To(HaveKey("NetIncomeLoss"))
		Expect(cf.Facts).To(HaveKey("EarningsPerShareBasic"))
		Expect(cf.Facts).To(HaveKey("CommonStockSharesOutstanding"))
	})

	It("parses DEI namespace concepts", func() {
		Expect(cf.Facts).To(HaveKey("EntityCommonStockSharesOutstanding"))

		deiFacts := cf.Facts["EntityCommonStockSharesOutstanding"]
		Expect(deiFacts).NotTo(BeEmpty())

		// Verify a specific DEI fact (Q2 FY2024 cover page)
		var found *Fact

		for i := range deiFacts {
			if deiFacts[i].Accn == "0000320193-24-000069" {
				found = &deiFacts[i]

				break
			}
		}

		Expect(found).NotTo(BeNil())
		Expect(found.Val).To(Equal(15334082000.0))
		Expect(found.Form).To(Equal("10-Q"))
		Expect(found.Start.IsZero()).To(BeTrue(), "DEI share count is an instant concept")
	})

	It("parses instant facts (balance sheet items with only end date)", func() {
		assets := cf.Facts["Assets"]
		Expect(assets).NotTo(BeEmpty())

		// Find the specific Assets fact identified by accession number. The
		// fact slice is sorted by Filed in ParseCompanyFacts, so we cannot
		// rely on the parse order to disambiguate the (FY,FP,Form) triple
		// when multiple period ends are reported under the same triple.
		var found *Fact

		for i := range assets {
			if assets[i].Accn == "0000320193-24-000123" &&
				assets[i].End.Equal(time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC)) {
				found = &assets[i]

				break
			}
		}

		Expect(found).NotTo(BeNil())
		Expect(found.FY).To(Equal(2024))
		Expect(found.FP).To(Equal("FY"))
		Expect(found.Form).To(Equal("10-K"))
		Expect(found.Val).To(Equal(352583000000.0))
		Expect(found.End).To(Equal(time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC)))
		Expect(found.Start.IsZero()).To(BeTrue(), "instant facts should have zero Start")
		Expect(found.Filed).To(Equal(time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC)))
		Expect(found.Frame).To(Equal("CY2023Q3I"))
	})

	It("parses duration facts (income statement items with start and end dates)", func() {
		netIncome := cf.Facts["NetIncomeLoss"]
		Expect(netIncome).NotTo(BeEmpty())

		// Find the NetIncomeLoss fact for the period ending 2022-12-31. The
		// fact slice is sorted by Filed in ParseCompanyFacts, so identify
		// the fact by its (FY,FP,Form,End) tuple rather than relying on
		// parse order.
		var found *Fact

		for i := range netIncome {
			if netIncome[i].FY == 2024 && netIncome[i].FP == "Q1" && netIncome[i].Form == "10-Q" &&
				netIncome[i].End.Equal(time.Date(2022, 12, 31, 0, 0, 0, 0, time.UTC)) {
				found = &netIncome[i]

				break
			}
		}

		Expect(found).NotTo(BeNil())
		Expect(found.Val).To(Equal(29998000000.0))
		Expect(found.Start).To(Equal(time.Date(2022, 9, 25, 0, 0, 0, 0, time.UTC)))
		Expect(found.End).To(Equal(time.Date(2022, 12, 31, 0, 0, 0, 0, time.UTC)))
		Expect(found.Frame).To(Equal("CY2022Q4"))
	})

	It("filters to only 10-K and 10-Q forms", func() {
		assets := cf.Facts["Assets"]

		for _, f := range assets {
			Expect(f.Form).To(BeElementOf("10-K", "10-Q"),
				"only 10-K and 10-Q forms should be included, got: %s", f.Form)
		}

		// Apple fixture has 142 total Asset entries but only 136 are 10-K/10-Q
		Expect(len(assets)).To(Equal(136))
	})

	It("prefers USD units over other unit types", func() {
		// EarningsPerShareBasic has USD/shares units; it should still parse
		eps := cf.Facts["EarningsPerShareBasic"]
		Expect(eps).NotTo(BeEmpty())

		// CommonStockSharesOutstanding has shares units; it should still parse
		shares := cf.Facts["CommonStockSharesOutstanding"]
		Expect(shares).NotTo(BeEmpty())
	})

	It("parses EPS values correctly from USD/shares units", func() {
		eps := cf.Facts["EarningsPerShareBasic"]

		var found *Fact

		for i := range eps {
			if eps[i].FY == 2024 && eps[i].FP == "FY" && eps[i].Form == "10-K" {
				found = &eps[i]

				break
			}
		}

		Expect(found).NotTo(BeNil())
		Expect(found.Val).To(Equal(6.15))
	})

	It("returns an error for invalid JSON", func() {
		_, err := ParseCompanyFacts([]byte("not json"))
		Expect(err).To(HaveOccurred())
	})
})
