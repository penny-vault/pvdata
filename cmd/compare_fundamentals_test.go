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
package cmd

import (
	"fmt"
	"strings"

	"github.com/penny-vault/pvdata/data"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("fundamentalFields", func() {
	It("has every entry present in the FundamentalsKey schema with the correct SQL type", func() {
		dt := data.DataTypes[data.FundamentalsKey]
		Expect(dt).NotTo(BeNil(), "FundamentalsKey data type should be registered")

		schema := dt.Schema

		for _, f := range fundamentalFields {
			var want string

			switch f.kind {
			case kindInt:
				want = fmt.Sprintf("%s BIGINT", f.column)
			case kindFloat:
				want = fmt.Sprintf("%s NUMERIC", f.column)
			}

			Expect(strings.Contains(schema, want)).To(BeTrue(),
				"field %q not found in schema with expected type (looking for %q)", f.column, want)
		}
	})
})

var _ = Describe("fieldByName", func() {
	It("returns a known int field", func() {
		f, ok := fieldByName("revenues")
		Expect(ok).To(BeTrue())
		Expect(f.kind).To(Equal(kindInt))
	})

	It("returns a known float field", func() {
		f, ok := fieldByName("eps")
		Expect(ok).To(BeTrue())
		Expect(f.kind).To(Equal(kindFloat))
	})

	It("reports ok=false for an unknown field", func() {
		_, ok := fieldByName("not_a_field")
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("valuesDiffer", func() {
	Context("null handling", func() {
		It("treats two nils as equal", func() {
			Expect(valuesDiffer(nil, nil, 0.0001, 0)).To(BeFalse())
		})

		It("treats nil vs non-nil as different in either order", func() {
			v := 5.0
			Expect(valuesDiffer(&v, nil, 0.0001, 0)).To(BeTrue())
			Expect(valuesDiffer(nil, &v, 0.0001, 0)).To(BeTrue())
		})
	})

	Context("numeric comparison", func() {
		It("returns false for exactly equal values", func() {
			a, b := 100.0, 100.0
			Expect(valuesDiffer(&a, &b, 0.0001, 0)).To(BeFalse())
		})

		It("returns false when relative diff is within tolerance", func() {
			a, b := 1_000_000.0, 1_000_050.0 // 0.005% < 0.01%
			Expect(valuesDiffer(&a, &b, 0.0001, 0)).To(BeFalse())
		})

		It("returns true when relative diff exceeds tolerance", func() {
			a, b := 1_000_000.0, 1_001_000.0 // 0.1% > 0.01%
			Expect(valuesDiffer(&a, &b, 0.0001, 0)).To(BeTrue())
		})

		It("applies the absolute tolerance floor", func() {
			a, b := 0.0, 0.5
			Expect(valuesDiffer(&a, &b, 0.0001, 1.0)).To(BeFalse())
		})

		It("treats zero vs zero as equal", func() {
			a, b := 0.0, 0.0
			Expect(valuesDiffer(&a, &b, 0.0001, 0)).To(BeFalse())
		})

		It("treats zero vs a small non-zero as different when abs-tol is zero", func() {
			a, b := 0.0, 0.0001
			Expect(valuesDiffer(&a, &b, 0.0001, 0)).To(BeTrue())
		})
	})
})
