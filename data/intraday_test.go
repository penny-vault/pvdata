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
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/data"
)

var _ = Describe("IntradayKey DataType", func() {
	It("is registered in DataTypes", func() {
		Expect(data.DataTypes).To(HaveKey(data.IntradayKey))
	})

	It("is routed to the ClickHouse backend", func() {
		dt := data.DataTypes[data.IntradayKey]
		Expect(dt.Backend).To(Equal(data.BackendClickHouse))
		Expect(dt.IsPartitioned).To(BeFalse())
		Expect(dt.DateColumn).To(Equal("event_date"))
	})

	It("renders ClickHouse DDL with ReplacingMergeTree and monthly partitions", func() {
		dt := data.DataTypes[data.IntradayKey]
		sql := dt.ExpandedSchema("intraday_bar_eodhd_abc12")
		Expect(sql).To(ContainSubstring("intraday_bar_eodhd_abc12"))
		Expect(sql).To(ContainSubstring("ENGINE = ReplacingMergeTree"))
		Expect(sql).To(ContainSubstring("PARTITION BY toYYYYMM(event_date)"))
		Expect(sql).To(ContainSubstring("ORDER BY (composite_figi, event_date)"))
		// no Postgres-specific syntax should leak in
		Expect(strings.Contains(sql, "PARTITION BY RANGE")).To(BeFalse())
		Expect(strings.Contains(sql, "DOUBLE PRECISION")).To(BeFalse())
		Expect(strings.Contains(sql, "PRIMARY KEY")).To(BeFalse())
	})

	It("uses Float64 for OHLC and Float64 for volume", func() {
		dt := data.DataTypes[data.IntradayKey]
		sql := dt.ExpandedSchema("intraday_bar_eodhd_abc12")
		Expect(sql).To(ContainSubstring("open            Float64"))
		Expect(sql).To(ContainSubstring("close           Float64"))
		Expect(sql).To(ContainSubstring("volume          Float64"))
		Expect(sql).To(ContainSubstring("composite_figi  FixedString(12)"))
	})

	It("applies column-specialized codecs for time-series compression", func() {
		dt := data.DataTypes[data.IntradayKey]
		sql := dt.ExpandedSchema("intraday_bar_eodhd_abc12")
		Expect(sql).To(ContainSubstring("event_date      DateTime CODEC(DoubleDelta, ZSTD)"))
		Expect(sql).To(ContainSubstring("open            Float64 CODEC(Gorilla, ZSTD)"))
		Expect(sql).To(ContainSubstring("high            Float64 CODEC(Gorilla, ZSTD)"))
		Expect(sql).To(ContainSubstring("low             Float64 CODEC(Gorilla, ZSTD)"))
		Expect(sql).To(ContainSubstring("close           Float64 CODEC(Gorilla, ZSTD)"))
		Expect(sql).To(ContainSubstring("volume          Float64 CODEC(Gorilla, ZSTD)"))
		Expect(sql).To(ContainSubstring("composite_figi  FixedString(12) CODEC(ZSTD)"))
	})
})
