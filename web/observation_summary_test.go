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

	It("falls back for unknown types", func() {
		obs := &data.Observation{
			MarketHoliday: &data.MarketHoliday{},
		}
		typ, summary := summarizeObservation(obs)
		Expect(typ).To(Equal("observation"))
		Expect(summary).To(Equal("observation"))
	})
})
