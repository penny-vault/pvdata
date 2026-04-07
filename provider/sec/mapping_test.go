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
