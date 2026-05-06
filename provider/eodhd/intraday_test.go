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
)

var _ = Describe("parseIntradayResponse", func() {
	It("decodes UTC timestamps and OHLCV fields", func() {
		body := []byte(`[
			{"timestamp":1714465800,"gmtoffset":0,"datetime":"2024-04-30 09:30:00","open":170.0,"high":170.5,"low":169.9,"close":170.2,"volume":12345},
			{"timestamp":1714465860,"gmtoffset":0,"datetime":"2024-04-30 09:31:00","open":170.2,"high":170.4,"low":170.1,"close":170.3,"volume":2345}
		]`)

		bars, err := parseIntradayResponse(body, "AAPL", "BBG000B9XRY4")
		Expect(err).NotTo(HaveOccurred())
		Expect(bars).To(HaveLen(2))

		Expect(bars[0].Date).To(Equal(time.Unix(1714465800, 0).UTC()))
		Expect(bars[0].Ticker).To(Equal("AAPL"))
		Expect(bars[0].CompositeFigi).To(Equal("BBG000B9XRY4"))
		Expect(bars[0].Open).To(Equal(170.0))
		Expect(bars[0].Volume).To(Equal(12345.0))
	})

	It("skips rows with a zero timestamp", func() {
		body := []byte(`[{"timestamp":0,"open":1,"high":1,"low":1,"close":1,"volume":1}]`)

		bars, err := parseIntradayResponse(body, "AAPL", "BBG000B9XRY4")
		Expect(err).NotTo(HaveOccurred())
		Expect(bars).To(BeEmpty())
	})
})

var _ = Describe("chunkRange", func() {
	now := time.Date(2026, 5, 5, 12, 0, 0, 0, time.UTC)

	It("returns one chunk when the range fits", func() {
		from := now.AddDate(0, 0, -30)
		chunks := chunkRange(from, now, 120*24*time.Hour)
		Expect(chunks).To(HaveLen(1))
		Expect(chunks[0].From).To(Equal(from))
		Expect(chunks[0].To).To(Equal(now))
	})

	It("splits a year into 120-day windows with a final remainder", func() {
		from := now.AddDate(-1, 0, 0)
		chunks := chunkRange(from, now, 120*24*time.Hour)
		Expect(len(chunks)).To(BeNumerically(">=", 4))

		Expect(chunks[0].From).To(Equal(from))
		Expect(chunks[len(chunks)-1].To).To(Equal(now))

		for i := 1; i < len(chunks); i++ {
			Expect(chunks[i].From).To(Equal(chunks[i-1].To))
		}
	})

	It("returns no chunks when from is after to", func() {
		Expect(chunkRange(now, now.Add(-time.Hour), 120*24*time.Hour)).To(BeEmpty())
	})
})

var _ = Describe("parseIntradayTickers", func() {
	It("splits a plain ticker list and applies the default exchange", func() {
		entries := parseIntradayTickers("AAPL,MSFT", "US")
		Expect(entries).To(HaveLen(2))
		Expect(entries[0].Ticker).To(Equal("AAPL"))
		Expect(entries[0].Exchange).To(Equal("US"))
		Expect(entries[1].Ticker).To(Equal("MSFT"))
		Expect(entries[1].Exchange).To(Equal("US"))
	})

	It("honors per-entry exchange overrides", func() {
		entries := parseIntradayTickers("AAPL,BMW.XETRA", "US")
		Expect(entries).To(HaveLen(2))
		Expect(entries[0].Exchange).To(Equal("US"))
		Expect(entries[1].Ticker).To(Equal("BMW"))
		Expect(entries[1].Exchange).To(Equal("XETRA"))
	})

	It("returns nothing when the config is blank", func() {
		Expect(parseIntradayTickers("", "US")).To(BeEmpty())
	})

	It("trims whitespace and skips empty entries", func() {
		entries := parseIntradayTickers(" AAPL , , MSFT ", "US")
		Expect(entries).To(HaveLen(2))
		Expect(entries[0].Ticker).To(Equal("AAPL"))
		Expect(entries[1].Ticker).To(Equal("MSFT"))
	})
})

var _ = Describe("readIntradayLookback", func() {
	It("falls back to the default when blank or invalid", func() {
		Expect(readIntradayLookback("")).To(Equal(5))
		Expect(readIntradayLookback("not-a-number")).To(Equal(5))
		Expect(readIntradayLookback("0")).To(Equal(5))
		Expect(readIntradayLookback("-3")).To(Equal(5))
	})

	It("parses positive integers", func() {
		Expect(readIntradayLookback("10")).To(Equal(10))
	})
})
