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

var _ = Describe("assetsActiveInWindow", func() {
	windowStart := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	It("includes every currently-active asset", func() {
		assets := []*data.Asset{
			{Ticker: "AAPL", Active: true},
			{Ticker: "MSFT", Active: true},
		}
		Expect(assetsActiveInWindow(assets, windowStart)).To(HaveLen(2))
	})

	It("includes a delisted asset when its delisting timestamp is on or after the window start", func() {
		assets := []*data.Asset{
			{Ticker: "FOO", Active: false, DelistingDate: "2026-03-01T00:00:00Z"},
		}
		Expect(assetsActiveInWindow(assets, windowStart)).To(HaveLen(1))
	})

	It("excludes a delisted asset whose delisting timestamp predates the window", func() {
		assets := []*data.Asset{
			{Ticker: "OLD", Active: false, DelistingDate: "2024-06-01T00:00:00Z"},
		}
		Expect(assetsActiveInWindow(assets, windowStart)).To(BeEmpty())
	})

	It("excludes a delisted asset with no delisting timestamp", func() {
		assets := []*data.Asset{
			{Ticker: "OLD", Active: false},
		}
		Expect(assetsActiveInWindow(assets, windowStart)).To(BeEmpty())
	})

	It("excludes a delisted asset whose delisting timestamp does not parse", func() {
		assets := []*data.Asset{
			{Ticker: "OLD", Active: false, DelistingDate: "garbage"},
		}
		Expect(assetsActiveInWindow(assets, windowStart)).To(BeEmpty())
	})
})

var _ = Describe("buildIntradayJobs", func() {
	It("emits one job per asset using the default exchange", func() {
		assets := []*data.Asset{
			{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", AssetType: data.CommonStock, Active: true},
			{Ticker: "MSFT", CompositeFigi: "BBG000BPH459", AssetType: data.CommonStock, Active: true},
		}
		jobs := buildIntradayJobs(assets, "US", nil, "", "")
		Expect(jobs).To(HaveLen(2))
		Expect(jobs[0].Ticker).To(Equal("AAPL"))
		Expect(jobs[0].Exchange).To(Equal("US"))
		Expect(jobs[0].CompositeFigi).To(Equal("BBG000B9XRY4"))
	})

	It("excludes mutual funds because they have no intraday data", func() {
		assets := []*data.Asset{
			{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", AssetType: data.CommonStock},
			{Ticker: "VFINX", CompositeFigi: "BBG000B9XRY5", AssetType: data.MutualFund},
		}
		jobs := buildIntradayJobs(assets, "US", nil, "", "")
		Expect(jobs).To(HaveLen(1))
		Expect(jobs[0].Ticker).To(Equal("AAPL"))
	})

	It("skips assets with no FIGI", func() {
		assets := []*data.Asset{
			{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", AssetType: data.CommonStock},
			{Ticker: "ORPHAN", CompositeFigi: "", AssetType: data.CommonStock},
		}
		jobs := buildIntradayJobs(assets, "US", nil, "", "")
		Expect(jobs).To(HaveLen(1))
	})

	It("honors the assetTypes filter", func() {
		assets := []*data.Asset{
			{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", AssetType: data.CommonStock},
			{Ticker: "SPY", CompositeFigi: "BBG000BDTBL9", AssetType: data.ETF},
		}
		filter := map[data.AssetType]struct{}{data.ETF: {}}
		jobs := buildIntradayJobs(assets, "US", filter, "", "")
		Expect(jobs).To(HaveLen(1))
		Expect(jobs[0].Ticker).To(Equal("SPY"))
	})

	It("honors a tickerFilter", func() {
		assets := []*data.Asset{
			{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", AssetType: data.CommonStock},
			{Ticker: "MSFT", CompositeFigi: "BBG000BPH459", AssetType: data.CommonStock},
		}
		jobs := buildIntradayJobs(assets, "US", nil, "msft", "")
		Expect(jobs).To(HaveLen(1))
		Expect(jobs[0].Ticker).To(Equal("MSFT"))
	})

	It("honors a figiFilter", func() {
		assets := []*data.Asset{
			{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", AssetType: data.CommonStock},
			{Ticker: "MSFT", CompositeFigi: "BBG000BPH459", AssetType: data.CommonStock},
		}
		jobs := buildIntradayJobs(assets, "US", nil, "", "BBG000BPH459")
		Expect(jobs).To(HaveLen(1))
		Expect(jobs[0].Ticker).To(Equal("MSFT"))
	})
})
