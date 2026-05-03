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
package data_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/data"
)

var _ = Describe("DataType.GenerateViewSQL", func() {
	Describe("plain (non-deduped) types", func() {
		eod := &data.DataType{Name: "eod", ViewName: "eod", DateColumn: "event_date"}

		It("returns DROP VIEW for zero sources", func() {
			Expect(eod.GenerateViewSQL("eod", nil)).To(Equal("DROP VIEW IF EXISTS eod"))
		})

		It("emits a simple SELECT * for one source with no bounds", func() {
			sql := eod.GenerateViewSQL("eod", []data.ViewSource{
				{TableName: "eod_tiingo_abc12"},
			})
			Expect(sql).To(Equal(
				"CREATE OR REPLACE VIEW eod AS SELECT * FROM eod_tiingo_abc12",
			))
		})

		It("emits WHERE bounds when FromDate and UntilDate are set", func() {
			from := time.Date(2022, 6, 1, 0, 0, 0, 0, time.UTC)
			until := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)
			sql := eod.GenerateViewSQL("eod", []data.ViewSource{
				{TableName: "eod_tiingo_abc12", FromDate: &from, UntilDate: &until},
			})
			Expect(sql).To(Equal(
				"CREATE OR REPLACE VIEW eod AS SELECT * FROM eod_tiingo_abc12 WHERE event_date >= '2022-06-01' AND event_date < '2023-06-01'",
			))
		})

		It("joins multiple legs with UNION ALL", func() {
			from := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			until := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			sql := eod.GenerateViewSQL("eod", []data.ViewSource{
				{TableName: "eod_tiingo_abc12", FromDate: &from},
				{TableName: "eod_legacy_def34", UntilDate: &until},
			})
			Expect(sql).To(ContainSubstring("UNION ALL"))
			Expect(sql).To(ContainSubstring("WHERE event_date >= '2023-01-01'"))
			Expect(sql).To(ContainSubstring("WHERE event_date < '2023-01-01'"))
		})

		It("ignores date bounds when DateColumn is empty", func() {
			asset := &data.DataType{Name: "a", ViewName: "assets", DateColumn: ""}
			from := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			sql := asset.GenerateViewSQL("assets", []data.ViewSource{
				{TableName: "ma_assets_abc12", FromDate: &from},
			})
			Expect(sql).NotTo(ContainSubstring("WHERE"))
			Expect(sql).To(Equal(
				"CREATE OR REPLACE VIEW assets AS SELECT * FROM ma_assets_abc12",
			))
		})

		It("uses the configured DateColumn name (snapshot_date)", func() {
			snap := &data.DataType{Name: "s", ViewName: "indices_snapshot", DateColumn: "snapshot_date"}
			from := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			sql := snap.GenerateViewSQL("indices_snapshot", []data.ViewSource{
				{TableName: "tradingview_idx_xyz", FromDate: &from},
			})
			Expect(sql).To(ContainSubstring("WHERE snapshot_date >= '2023-01-01'"))
			Expect(sql).NotTo(ContainSubstring("event_date"))
		})

		It("delegates leg SELECT/FROM to ViewGenerator when set", func() {
			rating := &data.DataType{
				Name:          "rating",
				ViewName:      "ratings",
				DateColumn:    "event_date",
				ViewGenerator: ratingTestVG{},
			}
			sql := rating.GenerateViewSQL("ratings", []data.ViewSource{
				{TableName: "rating_zacks_abc12"},
			})
			Expect(sql).To(Equal(
				"CREATE OR REPLACE VIEW ratings AS SELECT t.ticker, t.event_date, a.analyst FROM rating_zacks_abc12 t JOIN analyst_lookup a ON t.analyst_id = a.id",
			))
		})
	})

	Describe("deduped types (DedupKeys set)", func() {
		asset := &data.DataType{
			Name:      "a",
			ViewName:  "assets",
			DedupKeys: []string{"ticker", "composite_figi"},
		}

		It("emits a single leg for one source", func() {
			sql := asset.GenerateViewSQL("assets", []data.ViewSource{
				{TableName: "tiingo_assets_abc"},
			})
			Expect(sql).To(Equal(
				"CREATE OR REPLACE VIEW assets AS SELECT * FROM tiingo_assets_abc",
			))
		})

		It("anti-joins the second leg against the first on the dedup keys", func() {
			sql := asset.GenerateViewSQL("assets", []data.ViewSource{
				{TableName: "tiingo_assets_abc"},
				{TableName: "sharadar_assets_def"},
			})
			Expect(sql).To(Equal(
				"CREATE OR REPLACE VIEW assets AS " +
					"SELECT * FROM tiingo_assets_abc " +
					"UNION ALL " +
					"SELECT * FROM sharadar_assets_def " +
					"WHERE NOT EXISTS (" +
					"SELECT 1 FROM tiingo_assets_abc " +
					"WHERE tiingo_assets_abc.ticker = sharadar_assets_def.ticker " +
					"AND tiingo_assets_abc.composite_figi = sharadar_assets_def.composite_figi" +
					")",
			))
		})

		It("anti-joins each leg against every higher-priority leg", func() {
			sql := asset.GenerateViewSQL("assets", []data.ViewSource{
				{TableName: "s1"},
				{TableName: "s2"},
				{TableName: "s3"},
			})
			// First leg: no anti-join.
			Expect(sql).To(ContainSubstring("SELECT * FROM s1 UNION ALL"))
			// Second leg: one anti-join (against s1).
			Expect(sql).To(ContainSubstring(
				"SELECT * FROM s2 WHERE NOT EXISTS (SELECT 1 FROM s1 WHERE s1.ticker = s2.ticker AND s1.composite_figi = s2.composite_figi)",
			))
			// Third leg: two anti-joins (against s1 AND s2), combined with AND.
			Expect(sql).To(ContainSubstring(
				"SELECT * FROM s3 WHERE NOT EXISTS (SELECT 1 FROM s1 WHERE s1.ticker = s3.ticker AND s1.composite_figi = s3.composite_figi) AND NOT EXISTS (SELECT 1 FROM s2 WHERE s2.ticker = s3.ticker AND s2.composite_figi = s3.composite_figi)",
			))
		})

		It("ignores FromDate/UntilDate even if present (DateColumn empty)", func() {
			from := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			sql := asset.GenerateViewSQL("assets", []data.ViewSource{
				{TableName: "s1", FromDate: &from},
			})
			Expect(sql).NotTo(ContainSubstring("WHERE"))
		})

		It("DROP VIEW for zero sources still wins over dedup", func() {
			Expect(asset.GenerateViewSQL("assets", nil)).To(Equal("DROP VIEW IF EXISTS assets"))
		})

		It("uses ViewGenerator for the SELECT portion of each leg when set", func() {
			dt := &data.DataType{
				Name:          "a",
				ViewName:      "assets",
				DedupKeys:     []string{"ticker", "composite_figi"},
				ViewGenerator: explicitColsVG{},
			}
			sql := dt.GenerateViewSQL("assets", []data.ViewSource{
				{TableName: "tiingo_assets_abc"},
				{TableName: "sharadar_assets_def"},
			})
			Expect(sql).To(Equal(
				"CREATE OR REPLACE VIEW assets AS " +
					"SELECT ticker, composite_figi, name FROM tiingo_assets_abc " +
					"UNION ALL " +
					"SELECT ticker, composite_figi, name FROM sharadar_assets_def " +
					"WHERE NOT EXISTS (" +
					"SELECT 1 FROM tiingo_assets_abc " +
					"WHERE tiingo_assets_abc.ticker = sharadar_assets_def.ticker " +
					"AND tiingo_assets_abc.composite_figi = sharadar_assets_def.composite_figi" +
					")",
			))
		})
	})
})

type explicitColsVG struct{}

func (explicitColsVG) SelectFrom(tableName string) string {
	return "SELECT ticker, composite_figi, name FROM " + tableName
}

type ratingTestVG struct{}

func (ratingTestVG) SelectFrom(tableName string) string {
	return "SELECT t.ticker, t.event_date, a.analyst FROM " + tableName + " t JOIN analyst_lookup a ON t.analyst_id = a.id"
}
