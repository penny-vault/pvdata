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

	It("has the expected partitioning configuration", func() {
		dt := data.DataTypes[data.IntradayKey]
		Expect(dt.IsPartitioned).To(BeTrue())
		Expect(dt.PartitionInterval).To(Equal(data.PartitionIntervalMonthly))
		Expect(dt.DateColumn).To(Equal("event_date"))
	})

	It("renders schema SQL with TIMESTAMP event_date", func() {
		dt := data.DataTypes[data.IntradayKey]
		sql := dt.ExpandedSchema("intraday_bar_eodhd_abc12")
		Expect(sql).To(ContainSubstring("intraday_bar_eodhd_abc12"))
		Expect(sql).To(ContainSubstring("event_date     TIMESTAMP"))
		Expect(sql).To(ContainSubstring("PARTITION BY RANGE (event_date)"))
		Expect(sql).To(ContainSubstring("PRIMARY KEY (composite_figi, event_date)"))
		// no adjusted_close, dividend, or split_factor for intraday
		Expect(strings.Contains(sql, "adj_close")).To(BeFalse())
		Expect(strings.Contains(sql, "dividend")).To(BeFalse())
		Expect(strings.Contains(sql, "split_factor")).To(BeFalse())
	})

	It("uses double precision for OHLC and integer for volume", func() {
		dt := data.DataTypes[data.IntradayKey]
		sql := dt.ExpandedSchema("intraday_bar_eodhd_abc12")
		Expect(sql).To(ContainSubstring("open           DOUBLE PRECISION"))
		Expect(sql).To(ContainSubstring("close          DOUBLE PRECISION"))
		Expect(sql).To(ContainSubstring("volume         INTEGER"))
		Expect(strings.Contains(sql, "NUMERIC")).To(BeFalse())
		Expect(strings.Contains(sql, "BIGINT")).To(BeFalse())
	})
})
