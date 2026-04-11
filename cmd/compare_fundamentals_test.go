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
	"github.com/penny-vault/pvdata/library"

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

var _ = Describe("discoverFundamentalsSubscriptions", func() {
	It("returns the sec and sharadar tables when exactly one of each exists", func() {
		subs := []*library.Subscription{
			{
				Name: "sec-fundamentals", Provider: "sec", Active: true,
				DataTypes:     []string{data.FundamentalsKey},
				DataTables:    []string{"sec_fundamentals_v1"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "sec_fundamentals_v1"},
			},
			{
				Name: "sharadar-fundamentals", Provider: "sharadar", Active: true,
				DataTypes:     []string{data.FundamentalsKey},
				DataTables:    []string{"sharadar_fundamentals_v1"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "sharadar_fundamentals_v1"},
			},
			{
				Name: "tiingo-eod", Provider: "tiingo", Active: true,
				DataTypes: []string{data.EODKey},
			},
		}

		secTbl, sharadarTbl, err := discoverFundamentalsSubscriptions(subs)
		Expect(err).NotTo(HaveOccurred())
		Expect(secTbl).To(Equal("sec_fundamentals_v1"))
		Expect(sharadarTbl).To(Equal("sharadar_fundamentals_v1"))
	})

	It("errors when the sec subscription is missing", func() {
		subs := []*library.Subscription{
			{
				Name: "sharadar-fundamentals", Provider: "sharadar", Active: true,
				DataTypes:     []string{data.FundamentalsKey},
				DataTables:    []string{"sharadar_fundamentals_v1"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "sharadar_fundamentals_v1"},
			},
		}

		_, _, err := discoverFundamentalsSubscriptions(subs)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("sec"))
	})

	It("errors when multiple sec subscriptions match", func() {
		subs := []*library.Subscription{
			{
				Name: "sec-a", Provider: "sec", Active: true,
				DataTypes: []string{data.FundamentalsKey}, DataTables: []string{"a"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "a"},
			},
			{
				Name: "sec-b", Provider: "sec", Active: true,
				DataTypes: []string{data.FundamentalsKey}, DataTables: []string{"b"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "b"},
			},
			{
				Name: "sharadar-a", Provider: "sharadar", Active: true,
				DataTypes: []string{data.FundamentalsKey}, DataTables: []string{"c"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "c"},
			},
		}

		_, _, err := discoverFundamentalsSubscriptions(subs)
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("multiple"))
	})

	It("ignores inactive subscriptions when picking a match", func() {
		subs := []*library.Subscription{
			{
				Name: "sec-old", Provider: "sec", Active: false,
				DataTypes: []string{data.FundamentalsKey}, DataTables: []string{"old"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "old"},
			},
			{
				Name: "sec-new", Provider: "sec", Active: true,
				DataTypes: []string{data.FundamentalsKey}, DataTables: []string{"new"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "new"},
			},
			{
				Name: "sharadar", Provider: "sharadar", Active: true,
				DataTypes: []string{data.FundamentalsKey}, DataTables: []string{"sh"},
				DataTablesMap: map[string]string{data.FundamentalsKey: "sh"},
			},
		}

		secTbl, _, err := discoverFundamentalsSubscriptions(subs)
		Expect(err).NotTo(HaveOccurred())
		Expect(secTbl).To(Equal("new"))
	})
})
