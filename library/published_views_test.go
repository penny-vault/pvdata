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
package library_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/library"
)

var _ = Describe("PublishedViews", func() {
	Describe("GenerateViewSQL", func() {
		It("generates a simple view for a single source with no date bounds", func() {
			pv := &library.PublishedView{
				ViewName:    "eod",
				DataTypeKey: "eod",
				Sources: []library.ViewSource{
					{TableName: "eod_tiingo_abc12", SubscriptionID: "sub-uuid-1"},
				},
			}
			sqls := pv.GenerateViewSQL()
			Expect(sqls).To(HaveLen(1))
			Expect(sqls[0]).To(Equal(
				"CREATE OR REPLACE VIEW eod AS SELECT * FROM eod_tiingo_abc12",
			))
		})

		It("generates UNION ALL with date filters for multiple sources", func() {
			from := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			until := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			pv := &library.PublishedView{
				ViewName:    "eod",
				DataTypeKey: "eod",
				Sources: []library.ViewSource{
					{TableName: "eod_tiingo_abc12", SubscriptionID: "sub-1", FromDate: &from},
					{TableName: "eod_legacy_def34", SubscriptionID: "sub-2", UntilDate: &until},
				},
			}
			sqls := pv.GenerateViewSQL()
			Expect(sqls).To(HaveLen(1))
			Expect(sqls[0]).To(ContainSubstring("UNION ALL"))
			Expect(sqls[0]).To(ContainSubstring("WHERE event_date >= '2023-01-01'"))
			Expect(sqls[0]).To(ContainSubstring("WHERE event_date < '2023-01-01'"))
		})

		It("generates DROP VIEW for zero sources", func() {
			pv := &library.PublishedView{
				ViewName:    "eod",
				DataTypeKey: "eod",
				Sources:     []library.ViewSource{},
			}
			sqls := pv.GenerateViewSQL()
			Expect(sqls).To(HaveLen(1))
			Expect(sqls[0]).To(Equal("DROP VIEW IF EXISTS eod"))
		})

		It("generates two views for index data type", func() {
			pv := &library.PublishedView{
				ViewName:    "indices",
				DataTypeKey: "index",
				Sources: []library.ViewSource{
					{TableName: "index_ishares_abc12", SubscriptionID: "sub-1"},
				},
			}
			sqls := pv.GenerateViewSQL()
			Expect(sqls).To(HaveLen(2))
			Expect(sqls[0]).To(ContainSubstring("indices_snapshot"))
			Expect(sqls[0]).To(ContainSubstring("index_ishares_abc12_snapshot"))
			Expect(sqls[1]).To(ContainSubstring("indices_changelog"))
			Expect(sqls[1]).To(ContainSubstring("index_ishares_abc12_changelog"))
		})
	})

	Describe("ValidateSources", func() {
		It("accepts non-overlapping date ranges", func() {
			boundary := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			pv := &library.PublishedView{
				ViewName:    "eod",
				DataTypeKey: "eod",
				Sources: []library.ViewSource{
					{TableName: "t1", FromDate: &boundary},
					{TableName: "t2", UntilDate: &boundary},
				},
			}
			Expect(pv.ValidateSources()).To(Succeed())
		})

		It("rejects overlapping date ranges", func() {
			d1 := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
			d2 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			d3 := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			pv := &library.PublishedView{
				ViewName:    "eod",
				DataTypeKey: "eod",
				Sources: []library.ViewSource{
					{TableName: "t1", FromDate: &d1, UntilDate: &d2},
					{TableName: "t2", FromDate: &d3},
				},
			}
			Expect(pv.ValidateSources()).NotTo(Succeed())
		})

		It("accepts a single source with no bounds", func() {
			pv := &library.PublishedView{
				ViewName:    "eod",
				DataTypeKey: "eod",
				Sources: []library.ViewSource{
					{TableName: "t1"},
				},
			}
			Expect(pv.ValidateSources()).To(Succeed())
		})
	})
})
