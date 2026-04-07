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
	})
})
