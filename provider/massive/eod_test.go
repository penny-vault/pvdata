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
package massive

import (
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Flat-files EOD helpers", func() {
	Describe("dayAggsKey", func() {
		It("zero-pads single-digit months", func() {
			d := time.Date(2003, 9, 9, 0, 0, 0, 0, time.UTC)
			Expect(dayAggsKey(d)).To(Equal("us_stocks_sip/day_aggs_v1/2003/09/2003-09-09.csv.gz"))
		})

		It("renders two-digit months without extra padding", func() {
			d := time.Date(2025, 11, 5, 0, 0, 0, 0, time.UTC)
			Expect(dayAggsKey(d)).To(Equal("us_stocks_sip/day_aggs_v1/2025/11/2025-11-05.csv.gz"))
		})
	})

	Describe("isWeekend", func() {
		It("flags Saturday and Sunday", func() {
			Expect(isWeekend(time.Date(2025, 11, 8, 0, 0, 0, 0, time.UTC))).To(BeTrue())
			Expect(isWeekend(time.Date(2025, 11, 9, 0, 0, 0, 0, time.UTC))).To(BeTrue())
		})

		It("does not flag weekdays", func() {
			Expect(isWeekend(time.Date(2025, 11, 5, 0, 0, 0, 0, time.UTC))).To(BeFalse())
		})
	})

	Describe("readEODRateLimit", func() {
		It("falls back to the default for empty or invalid input", func() {
			Expect(readEODRateLimit("")).To(Equal(defaultRateLimit))
			Expect(readEODRateLimit("not-a-number")).To(Equal(defaultRateLimit))
			Expect(readEODRateLimit("0")).To(Equal(defaultRateLimit))
			Expect(readEODRateLimit("-7")).To(Equal(defaultRateLimit))
		})

		It("honours explicit positive values", func() {
			Expect(readEODRateLimit("100")).To(Equal(100))
		})
	})

	Describe("buildMassiveEodEvent", func() {
		It("places the timestamp at NYSE close", func() {
			ts, err := buildMassiveEodEvent(time.Date(2025, 3, 14, 0, 0, 0, 0, time.UTC))
			Expect(err).NotTo(HaveOccurred())
			Expect(ts.Hour()).To(Equal(16))
			Expect(ts.Minute()).To(Equal(0))
			Expect(ts.Location().String()).To(Equal("America/New_York"))
		})
	})

	Describe("parseAggs", func() {
		It("parses the documented column layout", func() {
			body := "ticker,volume,open,close,high,low,window_start,transactions\n" +
				"BCC,248274,61.68,61.99,62.565,61.41,1680033600000000000,4073\n"

			rows, err := parseAggs(strings.NewReader(body))
			Expect(err).NotTo(HaveOccurred())
			Expect(rows).To(HaveLen(1))
			Expect(rows[0].Ticker).To(Equal("BCC"))
			Expect(rows[0].Open).To(Equal(61.68))
			Expect(rows[0].Close).To(Equal(61.99))
			Expect(rows[0].High).To(Equal(62.565))
			Expect(rows[0].Low).To(Equal(61.41))
			Expect(rows[0].Volume).To(Equal(248274.0))
		})

		It("tolerates extra trailing columns and reorderings", func() {
			body := "open,close,high,low,volume,ticker,extra\n" +
				"10,11,12,9,5000,AAPL,ignored\n"

			rows, err := parseAggs(strings.NewReader(body))
			Expect(err).NotTo(HaveOccurred())
			Expect(rows).To(HaveLen(1))
			Expect(rows[0].Ticker).To(Equal("AAPL"))
			Expect(rows[0].Open).To(Equal(10.0))
			Expect(rows[0].Close).To(Equal(11.0))
		})

		It("returns an error when a required column is missing", func() {
			body := "ticker,open,close,high,low\n" +
				"AAPL,10,11,12,9\n"

			_, err := parseAggs(strings.NewReader(body))
			Expect(err).To(MatchError(ContainSubstring(`missing column "volume"`)))
		})

		It("treats blank numeric cells as zero", func() {
			body := "ticker,volume,open,close,high,low\n" +
				"AAPL,,,,,\n"

			rows, err := parseAggs(strings.NewReader(body))
			Expect(err).NotTo(HaveOccurred())
			Expect(rows).To(HaveLen(1))
			Expect(rows[0].Ticker).To(Equal("AAPL"))
			Expect(rows[0].Open).To(Equal(0.0))
		})
	})

	Describe("corporateActions", func() {
		It("returns zero for unknown ticker/date pairs", func() {
			c := newCorporateActions(0)
			Expect(c.lookup("AAPL", "2025-01-01")).To(Equal(0.0))
		})

		It("stores and retrieves split factors per (ticker, date)", func() {
			c := newCorporateActions(0)
			c.set("AAPL", "2020-08-31", 4.0)
			c.set("AAPL", "2014-06-09", 7.0)
			c.set("MSFT", "2003-02-18", 2.0)

			Expect(c.lookup("AAPL", "2020-08-31")).To(Equal(4.0))
			Expect(c.lookup("AAPL", "2014-06-09")).To(Equal(7.0))
			Expect(c.lookup("MSFT", "2003-02-18")).To(Equal(2.0))
			Expect(c.lookup("AAPL", "2099-01-01")).To(Equal(0.0))
			Expect(c.tickerCount()).To(Equal(2))
		})

		It("is safe to lookup against a nil receiver", func() {
			var c corporateActions
			Expect(c.lookup("AAPL", "2025-01-01")).To(Equal(0.0))
		})
	})
})
