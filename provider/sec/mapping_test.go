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

	It("OpLinearCombination mappings have matching Coefficients length", func() {
		for _, m := range FieldMappings {
			if m.Op == OpLinearCombination {
				Expect(len(m.Coefficients)).To(Equal(len(m.Operands)),
					"mapping %s: Coefficients length (%d) must match Operands length (%d)",
					m.FieldName, len(m.Coefficients), len(m.Operands))
			}
		}
	})

	It("all mappings have a valid statement type", func() {
		for _, m := range FieldMappings {
			Expect(m.StatementType).To(BeElementOf(StmtFlow, StmtPointInTime, StmtMetric),
				"mapping %s has invalid statement type: %s", m.FieldName, m.StatementType)
		}
	})

	It("BookValuePerShare uses WeightedAverageShares as denominator", func() {
		for _, m := range FieldMappings {
			if m.FieldName == "BookValuePerShare" {
				Expect(m.Operands).To(Equal([]string{"Equity", "WeightedAverageShares"}))
				return
			}
		}

		Fail("BookValuePerShare not found in FieldMappings")
	})

	It("TangibleAssetsBookValuePerShare uses WeightedAverageShares as denominator", func() {
		for _, m := range FieldMappings {
			if m.FieldName == "TangibleAssetsBookValuePerShare" {
				Expect(m.Operands).To(Equal([]string{"TangibleAssetValue", "WeightedAverageShares"}))
				return
			}
		}

		Fail("TangibleAssetsBookValuePerShare not found in FieldMappings")
	})

	It("all OpDivide float64 fields have RoundDigits set", func() {
		for _, m := range FieldMappings {
			if m.Type == MappingDerived && m.Op == OpDivide && m.ValueType == "float64" {
				Expect(m.RoundDigits).To(BeNumerically(">", 0),
					"derived OpDivide float64 field %s should have RoundDigits set", m.FieldName)
			}
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

		It("prefers EntityCommonStockSharesOutstanding over CommonStockSharesOutstanding", func() {
			// When both DEI (EntityCommonStockSharesOutstanding) and us-gaap
			// (CommonStockSharesOutstanding) facts are present, the DEI
			// cover-page count should be selected because it matches Sharadar's
			// sharesbas definition. The two values differ slightly because the
			// cover-page date is ~3 weeks after quarter end and ongoing
			// buybacks reduce the count.
			periodEnd := time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC)
			filed := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

			bothCF := &CompanyFacts{
				CIK:        320193,
				EntityName: "Apple Inc",
				Facts: map[string][]Fact{
					"EntityCommonStockSharesOutstanding": {
						// DEI cover-page value (as of ~2024-04-19)
						{End: time.Date(2024, 4, 19, 0, 0, 0, 0, time.UTC), Filed: filed, Val: 15_334_082_000, Form: "10-Q"},
					},
					"CommonStockSharesOutstanding": {
						// us-gaap balance sheet value (as of 2024-03-30)
						{End: periodEnd, Filed: filed, Val: 15_337_686_000, Form: "10-Q"},
					},
				},
			}

			val, ok := ResolveDirect(bothCF, FieldMapping{
				FieldName: "SharesBasic",
				Type:      MappingDirect,
				XBRLTags: []string{
					"EntityCommonStockSharesOutstanding",
					"CommonStockSharesOutstanding",
				},
			}, periodEnd, "10-Q")

			Expect(ok).To(BeTrue())
			Expect(val).To(Equal(15_334_082_000.0),
				"should pick DEI cover-page count, not us-gaap balance sheet count")
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

	Describe("computeDerived with OpLinearCombination", func() {
		It("computes weighted sum of operands", func() {
			resolved := map[string]float64{
				"A": 100,
				"B": 200,
				"C": 30,
				"D": 20,
				"E": 10,
			}
			m := FieldMapping{
				FieldName:    "Result",
				Type:         MappingDerived,
				Op:           OpLinearCombination,
				Operands:     []string{"A", "B", "C", "D", "E"},
				Coefficients: []float64{1, 1, -1, -1, -1},
			}
			val, ok := computeDerived(m, resolved)
			Expect(ok).To(BeTrue())
			Expect(val).To(Equal(240.0))
		})

		It("returns false when any operand is missing", func() {
			resolved := map[string]float64{
				"A": 100,
				"B": 200,
			}
			m := FieldMapping{
				FieldName:    "Result",
				Type:         MappingDerived,
				Op:           OpLinearCombination,
				Operands:     []string{"A", "B", "C"},
				Coefficients: []float64{1, 1, -1},
			}
			_, ok := computeDerived(m, resolved)
			Expect(ok).To(BeFalse())
		})
	})

	Describe("computeDerived rounding", func() {
		It("rounds division result when RoundDigits is set", func() {
			resolved := map[string]float64{
				"A": 100,
				"B": 3,
			}
			m := FieldMapping{
				FieldName:   "Ratio",
				Type:        MappingDerived,
				Op:          OpDivide,
				Operands:    []string{"A", "B"},
				RoundDigits: 4,
			}
			val, ok := computeDerived(m, resolved)
			Expect(ok).To(BeTrue())
			Expect(val).To(Equal(33.3333))
		})

		It("does not round when RoundDigits is zero", func() {
			resolved := map[string]float64{
				"A": 100,
				"B": 3,
			}
			m := FieldMapping{
				FieldName: "Ratio",
				Type:      MappingDerived,
				Op:        OpDivide,
				Operands:  []string{"A", "B"},
			}
			val, ok := computeDerived(m, resolved)
			Expect(ok).To(BeTrue())
			Expect(val).To(Equal(100.0 / 3.0))
		})

		It("rounds to 3 decimal places", func() {
			resolved := map[string]float64{
				"A": 74_236_000_000,
				"B": 15_408_095_000,
			}
			m := FieldMapping{
				FieldName:   "BVPS",
				Type:        MappingDerived,
				Op:          OpDivide,
				Operands:    []string{"A", "B"},
				RoundDigits: 3,
			}
			val, ok := computeDerived(m, resolved)
			Expect(ok).To(BeTrue())
			Expect(val).To(Equal(4.818))
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

		It("resolves InvestedCapital from component fields", func() {
			periodEnd := time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC)
			filed := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

			icCF := &CompanyFacts{
				CIK: 1, EntityName: "Test Co",
				Facts: map[string][]Fact{
					"LongTermDebtCurrent":                   {{End: periodEnd, Filed: filed, Val: 10_000, Form: "10-Q"}},
					"LongTermDebtNoncurrent":                {{End: periodEnd, Filed: filed, Val: 90_000, Form: "10-Q"}},
					"Assets":                                {{End: periodEnd, Filed: filed, Val: 350_000, Form: "10-Q"}},
					"IntangibleAssetsNetIncludingGoodwill":  {{End: periodEnd, Filed: filed, Val: 50_000, Form: "10-Q"}},
					"CashAndCashEquivalentsAtCarryingValue": {{End: periodEnd, Filed: filed, Val: 25_000, Form: "10-Q"}},
					"LiabilitiesCurrent":                    {{End: periodEnd, Filed: filed, Val: 60_000, Form: "10-Q"}},
				},
			}

			resolved := ResolveAllFields(icCF, periodEnd, "10-Q")

			// TotalDebt = DebtCurrent(10_000) + DebtNonCurrent(90_000) = 100_000
			// InvestedCapital = 100_000 + 350_000 - 50_000 - 25_000 - 60_000 = 315_000
			Expect(resolved).To(HaveKey("InvestedCapital"))
			Expect(resolved["InvestedCapital"]).To(Equal(315_000.0))
		})

		It("resolves NetCashFlowDebt as proceeds minus repayments", func() {
			periodEnd := time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC)
			filed := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

			debtCF := &CompanyFacts{
				CIK: 1, EntityName: "Test Co",
				Facts: map[string][]Fact{
					"ProceedsFromIssuanceOfLongTermDebt": {
						{
							Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
							End:   periodEnd,
							Filed: filed,
							Val:   5_000_000_000,
							Form:  "10-Q",
						},
					},
					"RepaymentsOfLongTermDebt": {
						{
							Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
							End:   periodEnd,
							Filed: filed,
							Val:   3_000_000_000,
							Form:  "10-Q",
						},
					},
				},
			}

			resolved := ResolveAllFields(debtCF, periodEnd, "10-Q")
			Expect(resolved).To(HaveKey("NetCashFlowDebt"))
			Expect(resolved["NetCashFlowDebt"]).To(Equal(2_000_000_000.0),
				"NetCashFlowDebt = proceeds(5B) - repayments(3B) = 2B")
		})

		It("resolves NetCashFlowDebt with only repayments present", func() {
			periodEnd := time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC)
			filed := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

			debtCF := &CompanyFacts{
				CIK: 1, EntityName: "Test Co",
				Facts: map[string][]Fact{
					"RepaymentsOfLongTermDebt": {
						{
							Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
							End:   periodEnd,
							Filed: filed,
							Val:   3_000_000_000,
							Form:  "10-Q",
						},
					},
				},
			}

			resolved := ResolveAllFields(debtCF, periodEnd, "10-Q")
			Expect(resolved).To(HaveKey("NetCashFlowDebt"))
			Expect(resolved["NetCashFlowDebt"]).To(Equal(-3_000_000_000.0),
				"NetCashFlowDebt = -repayments(3B) when no proceeds exist")
		})

		It("resolves NetCashFlowInvest as proceeds minus payments", func() {
			periodEnd := time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC)
			filed := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

			investCF := &CompanyFacts{
				CIK: 1, EntityName: "Test Co",
				Facts: map[string][]Fact{
					"PaymentsToAcquireAvailableForSaleSecuritiesDebt": {
						{
							Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
							End:   periodEnd,
							Filed: filed,
							Val:   15_300_000_000,
							Form:  "10-Q",
						},
					},
					"ProceedsFromMaturitiesPrepaymentsAndCallsOfAvailableForSaleSecurities": {
						{
							Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
							End:   periodEnd,
							Filed: filed,
							Val:   13_200_000_000,
							Form:  "10-Q",
						},
					},
				},
			}

			resolved := ResolveAllFields(investCF, periodEnd, "10-Q")
			Expect(resolved).To(HaveKey("NetCashFlowInvest"))
			Expect(resolved["NetCashFlowInvest"]).To(Equal(-2_100_000_000.0),
				"NetCashFlowInvest = -payments(15.3B) + proceeds(13.2B) = -2.1B")
		})

		It("resolves NetCashFlowInvest with only proceeds present", func() {
			periodEnd := time.Date(2024, 3, 30, 0, 0, 0, 0, time.UTC)
			filed := time.Date(2024, 5, 3, 0, 0, 0, 0, time.UTC)

			investCF := &CompanyFacts{
				CIK: 1, EntityName: "Test Co",
				Facts: map[string][]Fact{
					"ProceedsFromSaleOfAvailableForSaleSecuritiesDebt": {
						{
							Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
							End:   periodEnd,
							Filed: filed,
							Val:   7_000_000_000,
							Form:  "10-Q",
						},
					},
				},
			}

			resolved := ResolveAllFields(investCF, periodEnd, "10-Q")
			Expect(resolved).To(HaveKey("NetCashFlowInvest"))
			Expect(resolved["NetCashFlowInvest"]).To(Equal(7_000_000_000.0),
				"NetCashFlowInvest = proceeds(7B) when no payments exist")
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
