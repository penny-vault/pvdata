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

	Describe("computeDerived with OptionalOperands", func() {
		It("sums present operands and ignores missing ones", func() {
			resolved := map[string]float64{
				"A": 100,
				"C": 300,
			}
			m := FieldMapping{
				FieldName:        "Sum",
				Type:             MappingDerived,
				Op:               OpAdd,
				Operands:         []string{"A", "B", "C"},
				OptionalOperands: true,
			}
			val, ok := computeDerived(m, resolved)
			Expect(ok).To(BeTrue())
			Expect(val).To(Equal(400.0))
		})

		It("returns false when no operands are present", func() {
			resolved := map[string]float64{}
			m := FieldMapping{
				FieldName:        "Sum",
				Type:             MappingDerived,
				Op:               OpAdd,
				Operands:         []string{"A", "B"},
				OptionalOperands: true,
			}
			_, ok := computeDerived(m, resolved)
			Expect(ok).To(BeFalse())
		})

		It("does not apply to non-optional OpAdd", func() {
			resolved := map[string]float64{
				"A": 100,
			}
			m := FieldMapping{
				FieldName: "Sum",
				Type:      MappingDerived,
				Op:        OpAdd,
				Operands:  []string{"A", "B"},
			}
			_, ok := computeDerived(m, resolved)
			Expect(ok).To(BeFalse())
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

		// Verify the four fields that were misaligned with Sharadar due to
		// narrow XBRL tag scope. Uses synthetic data modeled on AAPL Q2 2024
		// (period ending 2024-03-30, filed 2024-05-03).
		It("resolves Investments as sum of current + noncurrent", func() {
			periodEnd := time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC)
			filed := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

			investCF := &CompanyFacts{
				CIK: 320193, EntityName: "Apple Inc",
				Facts: map[string][]Fact{
					"MarketableSecuritiesCurrent": {
						{End: periodEnd, Filed: filed, Val: 34_455_000_000, Form: "10-Q"},
					},
					"MarketableSecuritiesNoncurrent": {
						{End: periodEnd, Filed: filed, Val: 95_187_000_000, Form: "10-Q"},
					},
				},
			}

			resolved := ResolveAllFields(investCF, periodEnd, "10-Q")
			Expect(resolved).To(HaveKey("Investments"))
			Expect(resolved["Investments"]).To(Equal(129_642_000_000.0),
				"Investments should sum current + noncurrent marketable securities")
		})

		It("resolves Investments from single component when only one exists", func() {
			periodEnd := time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC)
			filed := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

			investCF := &CompanyFacts{
				CIK: 1, EntityName: "Test Co",
				Facts: map[string][]Fact{
					"ShortTermInvestments": {
						{End: periodEnd, Filed: filed, Val: 50_000_000, Form: "10-Q"},
					},
				},
			}

			resolved := ResolveAllFields(investCF, periodEnd, "10-Q")
			Expect(resolved).To(HaveKey("Investments"))
			Expect(resolved["Investments"]).To(Equal(50_000_000.0),
				"Investments should resolve from single component via OptionalOperands")
		})

		It("resolves Receivables as sum of trade + non-trade", func() {
			periodEnd := time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC)
			filed := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

			recvCF := &CompanyFacts{
				CIK: 320193, EntityName: "Apple Inc",
				Facts: map[string][]Fact{
					"AccountsReceivableNetCurrent": {
						{End: periodEnd, Filed: filed, Val: 21_837_000_000, Form: "10-Q"},
					},
					"NontradeReceivablesCurrent": {
						{End: periodEnd, Filed: filed, Val: 19_313_000_000, Form: "10-Q"},
					},
				},
			}

			resolved := ResolveAllFields(recvCF, periodEnd, "10-Q")
			Expect(resolved).To(HaveKey("Receivables"))
			Expect(resolved["Receivables"]).To(Equal(41_150_000_000.0),
				"Receivables should sum trade + non-trade receivables")
		})

		It("resolves DeferredRevenue as current portion", func() {
			periodEnd := time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC)
			filed := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

			defRevCF := &CompanyFacts{
				CIK: 320193, EntityName: "Apple Inc",
				Facts: map[string][]Fact{
					"ContractWithCustomerLiabilityCurrent": {
						{End: periodEnd, Filed: filed, Val: 8_012_000_000, Form: "10-Q"},
					},
					"ContractWithCustomerLiability": {
						{End: periodEnd, Filed: filed, Val: 12_600_000_000, Form: "10-Q"},
					},
				},
			}

			resolved := ResolveAllFields(defRevCF, periodEnd, "10-Q")
			Expect(resolved).To(HaveKey("DeferredRevenue"))
			Expect(resolved["DeferredRevenue"]).To(Equal(8_012_000_000.0),
				"DeferredRevenue should pick current-specific tag over total")
		})

		It("resolves DebtCurrent as sum of components", func() {
			periodEnd := time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC)
			filed := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

			debtCF := &CompanyFacts{
				CIK: 320193, EntityName: "Apple Inc",
				Facts: map[string][]Fact{
					"LongTermDebtCurrent": {
						{End: periodEnd, Filed: filed, Val: 10_762_000_000, Form: "10-Q"},
					},
					"CommercialPaper": {
						{End: periodEnd, Filed: filed, Val: 1_997_000_000, Form: "10-Q"},
					},
					"LongTermDebtNoncurrent": {
						{End: periodEnd, Filed: filed, Val: 91_831_000_000, Form: "10-Q"},
					},
				},
			}

			resolved := ResolveAllFields(debtCF, periodEnd, "10-Q")

			Expect(resolved).To(HaveKey("DebtCurrent"))
			Expect(resolved["DebtCurrent"]).To(Equal(12_759_000_000.0),
				"DebtCurrent should sum current maturities + commercial paper")

			Expect(resolved).To(HaveKey("TotalDebt"))
			Expect(resolved["TotalDebt"]).To(Equal(104_590_000_000.0),
				"TotalDebt should cascade from corrected DebtCurrent + DebtNonCurrent")
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
})
