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

		It("omits date WHERE bounds for asset-description views (no event_date column)", func() {
			from := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			until := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			pv := &library.PublishedView{
				ViewName:    "assets",
				DataTypeKey: "asset-description",
				Sources: []library.ViewSource{
					{TableName: "massive_assets_abc12", SubscriptionID: "sub-1", FromDate: &from, UntilDate: &until},
				},
			}
			sqls := pv.GenerateViewSQL()
			Expect(sqls).To(HaveLen(1))
			Expect(sqls[0]).NotTo(ContainSubstring("WHERE"))
			Expect(sqls[0]).NotTo(ContainSubstring("event_date"))
			Expect(sqls[0]).To(Equal(
				"CREATE OR REPLACE VIEW assets AS SELECT * FROM massive_assets_abc12",
			))
		})

		It("emits the priority-dedup anti-join form for asset views with multiple sources", func() {
			pv := &library.PublishedView{
				ViewName:    "assets",
				DataTypeKey: "asset-description",
				Sources: []library.ViewSource{
					{TableName: "tiingo_assets_abc"},
					{TableName: "sharadar_assets_def"},
				},
			}
			sqls := pv.GenerateViewSQL()
			Expect(sqls).To(HaveLen(1))
			Expect(sqls[0]).To(ContainSubstring("UNION ALL"))
			Expect(sqls[0]).To(ContainSubstring(
				"SELECT * FROM sharadar_assets_def WHERE NOT EXISTS (SELECT 1 FROM tiingo_assets_abc WHERE tiingo_assets_abc.ticker = sharadar_assets_def.ticker AND tiingo_assets_abc.composite_figi = sharadar_assets_def.composite_figi)",
			))
		})

		It("uses snapshot_date for index-snapshot views", func() {
			from := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			pv := &library.PublishedView{
				ViewName:    "indices_snapshot",
				DataTypeKey: "index-snapshot",
				Sources: []library.ViewSource{
					{TableName: "tradingview_idx_xyz", SubscriptionID: "sub-1", FromDate: &from},
				},
			}
			sqls := pv.GenerateViewSQL()
			Expect(sqls).To(HaveLen(1))
			Expect(sqls[0]).To(ContainSubstring("WHERE snapshot_date >= '2023-01-01'"))
			Expect(sqls[0]).NotTo(ContainSubstring("event_date"))
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

		It("generates WHERE with both from and until for a single source", func() {
			from := time.Date(2022, 6, 1, 0, 0, 0, 0, time.UTC)
			until := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
			pv := &library.PublishedView{
				ViewName:    "eod",
				DataTypeKey: "eod",
				Sources: []library.ViewSource{
					{TableName: "eod_tiingo_abc12", SubscriptionID: "sub-1", FromDate: &from, UntilDate: &until},
				},
			}
			sqls := pv.GenerateViewSQL()
			Expect(sqls).To(HaveLen(1))
			Expect(sqls[0]).To(Equal(
				"CREATE OR REPLACE VIEW eod AS SELECT * FROM eod_tiingo_abc12 WHERE event_date >= '2022-06-01' AND event_date < '2023-06-01'",
			))
		})

		It("generates view for index-snapshot data type", func() {
			pv := &library.PublishedView{
				ViewName:    "indices_snapshot",
				DataTypeKey: "index-snapshot",
				Sources: []library.ViewSource{
					{TableName: "ishares_index_constituents_abc12_index_snapshot", SubscriptionID: "sub-1"},
				},
			}
			sqls := pv.GenerateViewSQL()
			Expect(sqls).To(HaveLen(1))
			Expect(sqls[0]).To(ContainSubstring("indices_snapshot"))
			Expect(sqls[0]).To(ContainSubstring("ishares_index_constituents_abc12_index_snapshot"))
		})

		It("generates view for index-changelog data type", func() {
			pv := &library.PublishedView{
				ViewName:    "indices_changelog",
				DataTypeKey: "index-changelog",
				Sources: []library.ViewSource{
					{TableName: "ishares_index_constituents_abc12_index_changelog", SubscriptionID: "sub-1"},
				},
			}
			sqls := pv.GenerateViewSQL()
			Expect(sqls).To(HaveLen(1))
			Expect(sqls[0]).To(ContainSubstring("indices_changelog"))
			Expect(sqls[0]).To(ContainSubstring("ishares_index_constituents_abc12_index_changelog"))
		})

		It("generates a join-based view for the rating data type with a single source", func() {
			pv := &library.PublishedView{
				ViewName:    "ratings",
				DataTypeKey: "rating",
				Sources: []library.ViewSource{
					{TableName: "rating_zacks_abc12", SubscriptionID: "sub-1"},
				},
			}
			sqls := pv.GenerateViewSQL()
			Expect(sqls).To(HaveLen(1))
			Expect(sqls[0]).To(Equal(
				"CREATE OR REPLACE VIEW ratings AS SELECT t.ticker, t.composite_figi, t.event_date, a.analyst, t.rating FROM rating_zacks_abc12 t JOIN analyst_lookup a ON t.analyst_id = a.id",
			))
		})

		It("generates a join-based UNION ALL view for the rating data type with multiple sources", func() {
			pv := &library.PublishedView{
				ViewName:    "ratings",
				DataTypeKey: "rating",
				Sources: []library.ViewSource{
					{TableName: "rating_zacks_abc12", SubscriptionID: "sub-1"},
					{TableName: "rating_zacks_def34", SubscriptionID: "sub-2"},
				},
			}
			sqls := pv.GenerateViewSQL()
			Expect(sqls).To(HaveLen(1))
			Expect(sqls[0]).To(ContainSubstring("UNION ALL"))
			Expect(sqls[0]).To(ContainSubstring("SELECT t.ticker, t.composite_figi, t.event_date, a.analyst, t.rating FROM rating_zacks_abc12 t JOIN analyst_lookup a ON t.analyst_id = a.id"))
			Expect(sqls[0]).To(ContainSubstring("SELECT t.ticker, t.composite_figi, t.event_date, a.analyst, t.rating FROM rating_zacks_def34 t JOIN analyst_lookup a ON t.analyst_id = a.id"))
		})

		It("generates a join-based view for the rating data type with date filters", func() {
			from := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			pv := &library.PublishedView{
				ViewName:    "ratings",
				DataTypeKey: "rating",
				Sources: []library.ViewSource{
					{TableName: "rating_zacks_abc12", SubscriptionID: "sub-1", FromDate: &from},
				},
			}
			sqls := pv.GenerateViewSQL()
			Expect(sqls).To(HaveLen(1))
			Expect(sqls[0]).To(Equal(
				"CREATE OR REPLACE VIEW ratings AS SELECT t.ticker, t.composite_figi, t.event_date, a.analyst, t.rating FROM rating_zacks_abc12 t JOIN analyst_lookup a ON t.analyst_id = a.id WHERE event_date >= '2023-01-01'",
			))
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

	Describe("CheckOverlaps", func() {
		It("returns overlap info for overlapping date ranges", func() {
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
			overlaps := pv.CheckOverlaps()
			Expect(overlaps).To(HaveLen(1))
			Expect(overlaps[0]).To(ContainSubstring("t1"))
			Expect(overlaps[0]).To(ContainSubstring("t2"))
		})

		It("returns empty for non-overlapping sources", func() {
			boundary := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			pv := &library.PublishedView{
				ViewName:    "eod",
				DataTypeKey: "eod",
				Sources: []library.ViewSource{
					{TableName: "t1", UntilDate: &boundary},
					{TableName: "t2", FromDate: &boundary},
				},
			}
			overlaps := pv.CheckOverlaps()
			Expect(overlaps).To(BeEmpty())
		})

		It("returns empty for single source", func() {
			pv := &library.PublishedView{
				ViewName:    "eod",
				DataTypeKey: "eod",
				Sources: []library.ViewSource{
					{TableName: "t1"},
				},
			}
			overlaps := pv.CheckOverlaps()
			Expect(overlaps).To(BeEmpty())
		})
	})
})
