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
})

type ratingTestVG struct{}

func (ratingTestVG) SelectFrom(tableName string) string {
	return "SELECT t.ticker, t.event_date, a.analyst FROM " + tableName + " t JOIN analyst_lookup a ON t.analyst_id = a.id"
}
