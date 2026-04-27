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
package web

import (
	"testing"
	"time"

	"github.com/penny-vault/pvdata/data"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestObservationSummary(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Observation Summary Suite")
}

var _ = Describe("summarizeObservation", func() {
	It("summarizes an IndexSnapshot", func() {
		obs := &data.Observation{
			IndexSnapshot: &data.IndexSnapshot{
				IndexTicker:  "sp-500",
				SnapshotDate: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
				Constituents: []data.IndexConstituent{
					{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", Weight: 0.0712},
					{Ticker: "MSFT", CompositeFigi: "BBG000BPH459", Weight: 0.0651},
				},
			},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("index_snapshot"))
		Expect(summary).To(Equal("sp-500 2 constituents 2026-04-01"))
	})

	It("summarizes an IndexChange", func() {
		obs := &data.Observation{
			IndexChange: &data.IndexChange{
				IndexTicker: "sp-500",
				Ticker:      "NVDA",
				Action:      "add",
				Weight:      0.0523,
				EventDate:   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("index_change"))
		Expect(summary).To(Equal("sp-500 NVDA add weight=0.0523 2026-04-01"))
	})

	It("summarizes an EodQuote", func() {
		obs := &data.Observation{
			EodQuote: &data.Eod{
				Ticker: "MSFT",
				Close:  425.50,
				Volume: 32000000,
				Date:   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("eod"))
		Expect(summary).To(Equal("MSFT close=425.50 vol=32000000 2026-04-01"))
	})

	It("summarizes a Fundamental", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:       "AAPL",
				Dimension:    "ARQ",
				ReportPeriod: time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("fundamental"))
		Expect(summary).To(Equal("AAPL ARQ 2026-03-31"))
	})

	It("summarizes an EconomicIndicator", func() {
		obs := &data.Observation{
			EconomicIndicator: &data.EconomicIndicator{
				Series:    "GDP",
				Value:     27000.5,
				EventDate: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("economic_indicator"))
		Expect(summary).To(Equal("GDP value=27000.50 2026-04-01"))
	})

	It("summarizes a Rating", func() {
		obs := &data.Observation{
			Rating: &data.AnalystRating{
				Ticker:    "AAPL",
				Analyst:   "zacks-rank",
				Rating:    2,
				EventDate: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("rating"))
		Expect(summary).To(Equal("AAPL zacks-rank rating=2 2026-04-01"))
	})

	It("summarizes a Metric", func() {
		obs := &data.Observation{
			Metric: &data.Metric{
				Ticker:    "AAPL",
				MarketCap: 3_000_000_000_000,
				PE:        28.4,
				EventDate: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("metric"))
		Expect(summary).To(Equal("AAPL mktcap=3000000000000 pe=28.40 2026-04-01"))
	})

	It("summarizes a Consensus", func() {
		obs := &data.Observation{
			Consensus: &data.Consensus{
				Ticker:            "AAPL",
				AvgRecommendation: 1.8,
				NumAnalysts:       42,
				AvgTargetPrice:    250.0,
				EventDate:         time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("consensus"))
		Expect(summary).To(Equal("AAPL rec=1.80 analysts=42 target=250.00 2026-04-01"))
	})

	It("summarizes an Estimate", func() {
		obs := &data.Observation{
			Estimate: &data.Estimate{
				Ticker:      "AAPL",
				Series:      "eps-q1",
				Value:       1.45,
				NumAnalysts: 28,
				EventDate:   time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("estimate"))
		Expect(summary).To(Equal("AAPL eps-q1 value=1.45 analysts=28 2026-04-01"))
	})

	It("summarizes an Asset", func() {
		obs := &data.Observation{
			AssetObject: &data.Asset{
				Ticker:          "AAPL",
				Name:            "Apple Inc.",
				PrimaryExchange: data.NasdaqExchange,
				AssetType:       data.CommonStock,
			},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("asset"))
		Expect(summary).To(Equal("AAPL CS Apple Inc. (XNAS)"))
	})

	It("summarizes a Custom", func() {
		obs := &data.Observation{
			CustomObject: &data.Custom{
				Ticker:    "AAPL",
				Key:       "short_interest",
				Value:     0.025,
				EventDate: time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC),
			},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("custom"))
		Expect(summary).To(Equal("AAPL short_interest=0.025 2026-04-01"))
	})

	It("summarizes a MarketHoliday", func() {
		obs := &data.Observation{
			MarketHoliday: &data.MarketHoliday{
				Name:      "Independence Day",
				Market:    "NYSE",
				EventDate: time.Date(2026, 7, 3, 0, 0, 0, 0, time.UTC),
			},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("market_holiday"))
		Expect(summary).To(Equal("Independence Day NYSE 2026-07-03"))
	})

	It("falls back for unknown types", func() {
		obs := &data.Observation{}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("observation"))
		Expect(summary).To(Equal("observation"))
	})
})
