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
package eodhd

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/data"
)

var _ = Describe("parseSplitFactor", func() {
	It("parses N/M shorthand into a float ratio", func() {
		Expect(parseSplitFactor("2/1")).To(Equal(2.0))
		Expect(parseSplitFactor("3/2")).To(BeNumerically("~", 1.5, 1e-9))
		Expect(parseSplitFactor("1/4")).To(Equal(0.25))
	})

	It("treats empty input as 1.0 (no split)", func() {
		Expect(parseSplitFactor("")).To(Equal(1.0))
	})

	It("falls back to 1.0 for unparseable input", func() {
		Expect(parseSplitFactor("garbage")).To(Equal(1.0))
		Expect(parseSplitFactor("2/0")).To(Equal(1.0))
	})
})

var _ = Describe("parseBulkEOD", func() {
	It("parses bulk-EOD rows into per-ticker records", func() {
		body := []byte(`[
			{"code":"AAPL","exchange_short_name":"NASDAQ","date":"2026-04-30","open":170.0,"high":172.0,"low":169.5,"close":171.5,"adjusted_close":171.5,"volume":1000000},
			{"code":"BRK-A","exchange_short_name":"NYSE","date":"2026-04-30","open":600000.0,"high":605000.0,"low":598000.0,"close":601000.0,"adjusted_close":601000.0,"volume":1500}
		]`)

		records, err := parseBulkEOD(body)
		Expect(err).NotTo(HaveOccurred())
		Expect(records).To(HaveLen(2))
		Expect(records[0].Ticker).To(Equal("AAPL"))
		Expect(records[1].Ticker).To(Equal("BRK/A"))
		Expect(records[0].Close).To(Equal(171.5))
	})
})

var _ = Describe("parseBulkSplits", func() {
	It("parses split rows keyed by normalized ticker + date", func() {
		body := []byte(`[
			{"code":"AAPL","exchange":"NASDAQ","date":"2026-04-30","split":"4/1"}
		]`)

		splits, err := parseBulkSplits(body)
		Expect(err).NotTo(HaveOccurred())
		Expect(splits).To(HaveKey(splitKey{Ticker: "AAPL", Date: "2026-04-30"}))
		Expect(splits[splitKey{Ticker: "AAPL", Date: "2026-04-30"}]).To(Equal(4.0))
	})
})

var _ = Describe("parseBulkDividends", func() {
	It("parses dividend rows keyed by normalized ticker + date", func() {
		body := []byte(`[
			{"code":"AAPL","exchange":"NASDAQ","date":"2026-04-30","value":0.24}
		]`)

		divs, err := parseBulkDividends(body)
		Expect(err).NotTo(HaveOccurred())
		Expect(divs).To(HaveKey(splitKey{Ticker: "AAPL", Date: "2026-04-30"}))
		Expect(divs[splitKey{Ticker: "AAPL", Date: "2026-04-30"}]).To(Equal(0.24))
	})
})

var _ = Describe("parsePerTickerEOD", func() {
	It("parses an array of per-day OHLC + adjusted_close + volume", func() {
		body := []byte(`[
			{"date":"2026-04-29","open":170.0,"high":172.0,"low":169.5,"close":171.5,"adjusted_close":171.5,"volume":1000000},
			{"date":"2026-04-30","open":171.5,"high":173.0,"low":170.0,"close":172.0,"adjusted_close":172.0,"volume":900000}
		]`)

		rows, err := parsePerTickerEOD(body)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(2))
		Expect(rows[0].Date).To(Equal("2026-04-29"))
		Expect(rows[1].Close).To(Equal(172.0))
	})
})

var _ = Describe("parsePerTickerSplits", func() {
	It("returns a date->factor map", func() {
		body := []byte(`[{"date":"2026-04-30","split":"2/1"}]`)

		splits, err := parsePerTickerSplits(body)
		Expect(err).NotTo(HaveOccurred())
		Expect(splits).To(HaveKeyWithValue("2026-04-30", 2.0))
	})
})

var _ = Describe("parsePerTickerDividends", func() {
	It("returns a date->cash map", func() {
		body := []byte(`[{"date":"2026-04-30","value":0.24,"declarationDate":"2026-04-15"}]`)

		divs, err := parsePerTickerDividends(body)
		Expect(err).NotTo(HaveOccurred())
		Expect(divs).To(HaveKeyWithValue("2026-04-30", 0.24))
	})
})

var _ = Describe("buildEodEvent", func() {
	It("converts a date string into a NYSE-close timestamp", func() {
		ts, err := buildEodEvent("2026-04-30")
		Expect(err).NotTo(HaveOccurred())

		nyc, _ := time.LoadLocation("America/New_York")
		Expect(ts.In(nyc).Hour()).To(Equal(16))
		Expect(ts.In(nyc).Minute()).To(Equal(0))
	})
})

var _ = Describe("eodMode", func() {
	It("uses bulk for short lookbacks with no filter", func() {
		Expect(eodMode("", "", 7*24*time.Hour)).To(Equal(modeBulk))
	})

	It("switches to per-ticker when a ticker filter is set", func() {
		Expect(eodMode("AAPL", "", 7*24*time.Hour)).To(Equal(modePerTicker))
	})

	It("switches to per-ticker when a figi filter is set", func() {
		Expect(eodMode("", "BBG000B9XRY4", 7*24*time.Hour)).To(Equal(modePerTicker))
	})

	It("switches to per-ticker for long lookbacks", func() {
		Expect(eodMode("", "", 60*24*time.Hour)).To(Equal(modePerTicker))
	})
})

// Confirm we can construct a *data.Eod with all fields wired up.
var _ = Describe("buildEodRecord", func() {
	It("populates ticker, FIGI, OHLCV, dividend, and split factor", func() {
		eod, err := buildEodRecord("AAPL", "BBG000B9XRY4", perTickerEODRow{
			Date: "2026-04-30", Open: 1, High: 2, Low: 0.5, Close: 1.5, Volume: 10000,
		}, 0.24, 2.0)

		Expect(err).NotTo(HaveOccurred())
		Expect(eod.Ticker).To(Equal("AAPL"))
		Expect(eod.CompositeFigi).To(Equal("BBG000B9XRY4"))
		Expect(eod.Open).To(Equal(1.0))
		Expect(eod.Dividend).To(Equal(0.24))
		Expect(eod.Split).To(Equal(2.0))
	})

	It("defaults split factor to 1.0 when not provided", func() {
		eod, err := buildEodRecord("AAPL", "BBG000B9XRY4", perTickerEODRow{Date: "2026-04-30"}, 0.0, 0.0)
		Expect(err).NotTo(HaveOccurred())
		Expect(eod.Split).To(Equal(1.0))
	})
})

// Match used by Eod equality assertions in tests.
var _ data.Eod = data.Eod{}
