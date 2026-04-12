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

var _ = Describe("Mapping Config", func() {
	It("has no duplicate field names", func() {
		seen := make(map[string]bool)
		for _, m := range FieldMappings {
			Expect(seen[m.FieldName]).To(BeFalse(),
				"duplicate field name: %s", m.FieldName)
			seen[m.FieldName] = true
		}
	})

	It("direct mappings have at least one XBRL tag", func() {
		for _, m := range FieldMappings {
			if m.Type == MappingDirect {
				Expect(len(m.XBRLTags)).To(BeNumerically(">", 0),
					"direct mapping %s has no XBRL tags", m.FieldName)
			}
		}
	})

	It("derived mappings have a formula", func() {
		for _, m := range FieldMappings {
			if m.Type == MappingDerived {
				Expect(len(m.Operands)).To(BeNumerically(">", 0),
					"derived mapping %s has no operands", m.FieldName)
			}
		}
	})

	It("derived formula operands reference existing field names", func() {
		fieldSet := make(map[string]bool)
		for _, m := range FieldMappings {
			fieldSet[m.FieldName] = true
		}

		for _, m := range FieldMappings {
			if m.Type == MappingDerived {
				for _, op := range m.Operands {
					Expect(fieldSet[op]).To(BeTrue(),
						"derived mapping %s references unknown field %s", m.FieldName, op)
				}
			}
		}
	})

	It("all mappings have a valid statement type", func() {
		for _, m := range FieldMappings {
			Expect(m.StatementType).To(BeElementOf(StmtFlow, StmtPointInTime, StmtMetric),
				"mapping %s has invalid statement type: %s", m.FieldName, m.StatementType)
		}
	})
})

var _ = Describe("Mapping Engine", func() {
	var cf *CompanyFacts

	BeforeEach(func() {
		jsonData, err := os.ReadFile("testdata/CIK0000320193.json")
		Expect(err).NotTo(HaveOccurred())
		cf, err = ParseCompanyFacts(jsonData)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("ResolveDirect", func() {
		It("resolves a direct field from XBRL facts", func() {
			// Apple's Assets (instant, balance sheet) for a 10-K period
			periodEnd := time.Date(2018, 9, 29, 0, 0, 0, 0, time.UTC)
			val, ok := ResolveDirect(cf, FieldMapping{
				FieldName: "TotalAssets",
				Type:      MappingDirect,
				XBRLTags:  []string{"Assets"},
			}, periodEnd, "10-K")
			Expect(ok).To(BeTrue())
			Expect(val).To(BeNumerically(">", 0))
		})

		It("falls back through tag list when first tag not found", func() {
			periodEnd := time.Date(2018, 9, 29, 0, 0, 0, 0, time.UTC)
			val, ok := ResolveDirect(cf, FieldMapping{
				FieldName: "CashAndEquivalents",
				Type:      MappingDirect,
				XBRLTags:  []string{"NonExistentTag", "CashAndCashEquivalentsAtCarryingValue"},
			}, periodEnd, "10-K")
			Expect(ok).To(BeTrue())
			Expect(val).To(BeNumerically(">", 0))
		})

		It("returns false when no tag matches", func() {
			periodEnd := time.Date(2018, 9, 29, 0, 0, 0, 0, time.UTC)
			_, ok := ResolveDirect(cf, FieldMapping{
				FieldName: "Test",
				Type:      MappingDirect,
				XBRLTags:  []string{"CompletelyFakeTag"},
			}, periodEnd, "10-K")
			Expect(ok).To(BeFalse())
		})

		It("prefers single-quarter facts over YTD cumulative facts in 10-Q", func() {
			// Simulate Apple's Q2 10-Q where both a 90-day single-quarter
			// fact and a 181-day YTD cumulative fact exist for the same
			// period end and filing date. ResolveDirect must pick the
			// single-quarter value.
			q2End := time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC)
			filed := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

			ytdCF := &CompanyFacts{
				CIK:        320193,
				EntityName: "Apple Inc",
				Facts: map[string][]Fact{
					"RevenueFromContractWithCustomerExcludingAssessedTax": {
						// YTD 6-month cumulative (Q1+Q2)
						{
							Start: time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC),
							End:   q2End,
							Filed: filed,
							Val:   210_328_000_000,
							Form:  "10-Q",
							FP:    "Q2",
						},
						// Single quarter (Q2 only)
						{
							Start: time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC),
							End:   q2End,
							Filed: filed,
							Val:   90_753_000_000,
							Form:  "10-Q",
							FP:    "Q2",
						},
					},
				},
			}

			val, ok := ResolveDirect(ytdCF, FieldMapping{
				FieldName: "Revenues",
				Type:      MappingDirect,
				XBRLTags:  []string{"RevenueFromContractWithCustomerExcludingAssessedTax"},
			}, q2End, "10-Q")

			Expect(ok).To(BeTrue())
			Expect(val).To(Equal(90_753_000_000.0),
				"should pick single-quarter revenue, not YTD cumulative")
		})
	})

	Describe("ResolveAllFields", func() {
		It("resolves both direct and derived fields", func() {
			periodEnd := time.Date(2018, 9, 29, 0, 0, 0, 0, time.UTC)
			resolved := ResolveAllFields(cf, periodEnd, "10-K")

			// Direct fields
			_, hasRevenues := resolved["Revenues"]
			Expect(hasRevenues).To(BeTrue())

			_, hasAssets := resolved["TotalAssets"]
			Expect(hasAssets).To(BeTrue())
		})

		It("falls back to basic weighted-average shares when diluted tags are absent", func() {
			// Simulates a loss-quarter filer (e.g. Nordstrom during COVID,
			// Vaxart as a pre-revenue biotech) that reports only the basic
			// weighted-average share count because the diluted figure is
			// antidilutive. The fallback must still produce a positive value
			// so PositiveShares does not reject the observation.
			periodStart := time.Date(2020, 2, 2, 0, 0, 0, 0, time.UTC)
			periodEnd := time.Date(2020, 5, 2, 0, 0, 0, 0, time.UTC)
			filed := time.Date(2020, 6, 10, 0, 0, 0, 0, time.UTC)

			lossCF := &CompanyFacts{
				CIK:        12345,
				EntityName: "Loss Co",
				Facts: map[string][]Fact{
					"WeightedAverageNumberOfSharesOutstandingBasic": {
						{
							Start: periodStart,
							End:   periodEnd,
							Filed: filed,
							Val:   157_000_000,
							Form:  "10-Q",
						},
					},
				},
			}

			resolved := ResolveAllFields(lossCF, periodEnd, "10-Q")

			val, ok := resolved["WeightedAverageSharesDiluted"]
			Expect(ok).To(BeTrue())
			Expect(val).To(Equal(float64(157_000_000)))
		})
	})

	Describe("Sign negation for cash outflow fields", func() {
		It("negates CapitalExpenditure, NetCashFlowCommon, and NetCashFlowDividend for AAPL", func() {
			// Apple FY2018 10-K: these XBRL tags report outflows as positive,
			// but after negation they should be negative per Sharadar convention.
			periodEnd := time.Date(2018, 9, 29, 0, 0, 0, 0, time.UTC)
			resolved := ResolveAllFields(cf, periodEnd, "10-K")

			capex, hasCapex := resolved["CapitalExpenditure"]
			Expect(hasCapex).To(BeTrue())
			Expect(capex).To(BeNumerically("<", 0),
				"CapitalExpenditure should be negative (cash outflow)")

			ncfCommon, hasCommon := resolved["NetCashFlowCommon"]
			Expect(hasCommon).To(BeTrue())
			Expect(ncfCommon).To(BeNumerically("<", 0),
				"NetCashFlowCommon should be negative (stock buyback)")

			ncfDiv, hasDiv := resolved["NetCashFlowDividend"]
			Expect(hasDiv).To(BeTrue())
			Expect(ncfDiv).To(BeNumerically("<", 0),
				"NetCashFlowDividend should be negative (dividend payment)")
		})

		It("negates NetCashFlowBusiness using synthetic data", func() {
			// Apple testdata lacks PaymentsToAcquireBusinessesNetOfCashAcquired,
			// so use a synthetic CompanyFacts to verify negation.
			periodEnd := time.Date(2024, 9, 30, 0, 0, 0, 0, time.UTC)
			filed := time.Date(2024, 11, 1, 0, 0, 0, 0, time.UTC)

			synthCF := &CompanyFacts{
				CIK:        1,
				EntityName: "Test Co",
				Facts: map[string][]Fact{
					"PaymentsToAcquireBusinessesNetOfCashAcquired": {
						{
							Start: time.Date(2023, 10, 1, 0, 0, 0, 0, time.UTC),
							End:   periodEnd,
							Filed: filed,
							Val:   500_000_000,
							Form:  "10-K",
							FP:    "FY",
						},
					},
				},
			}

			resolved := ResolveAllFields(synthCF, periodEnd, "10-K")

			ncfBiz, hasBiz := resolved["NetCashFlowBusiness"]
			Expect(hasBiz).To(BeTrue())
			Expect(ncfBiz).To(Equal(-500_000_000.0),
				"NetCashFlowBusiness should be negated (acquisition outflow)")
		})

		It("produces positive FreeCashFlow from negated CapitalExpenditure", func() {
			periodEnd := time.Date(2018, 9, 29, 0, 0, 0, 0, time.UTC)
			resolved := ResolveAllFields(cf, periodEnd, "10-K")

			ops, hasOps := resolved["NetCashFlowFromOperations"]
			Expect(hasOps).To(BeTrue())
			Expect(ops).To(BeNumerically(">", 0))

			capex := resolved["CapitalExpenditure"]
			fcf, hasFCF := resolved["FreeCashFlow"]
			Expect(hasFCF).To(BeTrue())
			// FCF = Operations + CapEx (where CapEx is negative)
			Expect(fcf).To(BeNumerically("~", ops+capex, 1.0))
			Expect(fcf).To(BeNumerically(">", 0),
				"FreeCashFlow should be positive for a profitable company like Apple")
		})
	})
})
