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
package tradingview

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("parseTradingViewResponse", func() {
	sampleJSON := []byte(`{
		"totalCount": 3,
		"symbols": ["NASDAQ:AAPL", "NYSE:MSFT", "NYSE:MOG.A"],
		"data": [
			{
				"id": "TickerUniversal",
				"rawValues": [
					{"name": "AAPL", "description": "Apple Inc."},
					{"name": "MSFT", "description": "Microsoft Corporation"},
					{"name": "MOG/A", "description": "Moog Inc."}
				]
			}
		]
	}`)

	It("parses symbols and extracts ticker names", func() {
		result, err := parseTradingViewResponse(sampleJSON)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.TotalCount).To(Equal(3))
		Expect(result.Holdings).To(HaveLen(3))
	})

	It("normalizes share class tickers (dots to slashes)", func() {
		result, err := parseTradingViewResponse(sampleJSON)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Holdings[2].Ticker).To(Equal("MOG/A"))
	})

	It("extracts names from the TickerUniversal data block", func() {
		result, err := parseTradingViewResponse(sampleJSON)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Holdings[0].Name).To(Equal("Apple Inc."))
		Expect(result.Holdings[1].Name).To(Equal("Microsoft Corporation"))
		Expect(result.Holdings[2].Name).To(Equal("Moog Inc."))
	})

	It("handles invalid JSON", func() {
		_, err := parseTradingViewResponse([]byte(`not valid json`))
		Expect(err).To(HaveOccurred())
	})

	It("handles empty response", func() {
		result, err := parseTradingViewResponse([]byte(`{"totalCount": 0, "symbols": [], "data": []}`))
		Expect(err).ToNot(HaveOccurred())
		Expect(result.TotalCount).To(Equal(0))
		Expect(result.Holdings).To(BeEmpty())
	})

	It("handles missing TickerUniversal block", func() {
		noTickerJSON := []byte(`{
			"totalCount": 2,
			"symbols": ["NASDAQ:AAPL", "NYSE:MSFT"],
			"data": [
				{
					"id": "SomeOtherBlock",
					"rawValues": [
						{"name": "AAPL", "description": "Apple Inc."}
					]
				}
			]
		}`)

		result, err := parseTradingViewResponse(noTickerJSON)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Holdings).To(HaveLen(2))
		// Names should be empty since TickerUniversal block is missing.
		Expect(result.Holdings[0].Name).To(BeEmpty())
		Expect(result.Holdings[1].Name).To(BeEmpty())
	})
})

var _ = Describe("normalizeTVTicker", func() {
	It("strips the exchange prefix", func() {
		Expect(normalizeTVTicker("NASDAQ:AAPL")).To(Equal("AAPL"))
		Expect(normalizeTVTicker("NYSE:MSFT")).To(Equal("MSFT"))
	})

	It("converts dots to slashes", func() {
		Expect(normalizeTVTicker("NYSE:BRK.B")).To(Equal("BRK/B"))
		Expect(normalizeTVTicker("NYSE:MOG.A")).To(Equal("MOG/A"))
	})

	It("handles input without a prefix", func() {
		Expect(normalizeTVTicker("AAPL")).To(Equal("AAPL"))
	})
})

var _ = Describe("indexMap", func() {
	It("contains all 7 expected indices", func() {
		Expect(indexMap).To(HaveLen(7))
	})

	It("has the correct index names", func() {
		Expect(indexMap).To(HaveKey("SPX"))
		Expect(indexMap["SPX"].Name).To(Equal("S&P 500"))

		Expect(indexMap).To(HaveKey("MID"))
		Expect(indexMap["MID"].Name).To(Equal("S&P MidCap 400"))

		Expect(indexMap).To(HaveKey("OEX"))
		Expect(indexMap["OEX"].Name).To(Equal("S&P 100"))

		Expect(indexMap).To(HaveKey("IXIC"))
		Expect(indexMap["IXIC"].Name).To(Equal("Nasdaq Composite"))

		Expect(indexMap).To(HaveKey("NDX"))
		Expect(indexMap["NDX"].Name).To(Equal("Nasdaq 100"))

		Expect(indexMap).To(HaveKey("RUI"))
		Expect(indexMap["RUI"].Name).To(Equal("Russell 1000"))

		Expect(indexMap).To(HaveKey("RUT"))
		Expect(indexMap["RUT"].Name).To(Equal("Russell 2000"))
	})
})
