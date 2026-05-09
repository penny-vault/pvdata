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
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/data"
)

var _ = Describe("normalizeTicker", func() {
	It("converts EODHD share-class dashes to slashes", func() {
		Expect(normalizeTicker("BRK-A")).To(Equal("BRK/A"))
		Expect(normalizeTicker("BRK-B")).To(Equal("BRK/B"))
	})

	It("leaves plain tickers untouched", func() {
		Expect(normalizeTicker("AAPL")).To(Equal("AAPL"))
		Expect(normalizeTicker("MSFT")).To(Equal("MSFT"))
	})
})

var _ = Describe("mapAssetType", func() {
	It("maps Common Stock to CommonStock", func() {
		Expect(mapAssetType("Common Stock")).To(Equal(data.CommonStock))
	})

	It("maps ETF to ETF", func() {
		Expect(mapAssetType("ETF")).To(Equal(data.ETF))
	})

	It("maps Fund and Mutual Fund to MutualFund", func() {
		Expect(mapAssetType("Fund")).To(Equal(data.MutualFund))
		Expect(mapAssetType("Mutual Fund")).To(Equal(data.MutualFund))
	})

	It("maps Preferred Stock to CommonStock", func() {
		Expect(mapAssetType("Preferred Stock")).To(Equal(data.CommonStock))
	})

	It("returns UnknownAsset for unrecognized types", func() {
		Expect(mapAssetType("Warrant")).To(Equal(data.UnknownAsset))
		Expect(mapAssetType("")).To(Equal(data.UnknownAsset))
	})
})

var _ = Describe("mapExchange", func() {
	DescribeTable("translates EODHD exchange short names to MIC codes",
		func(eodhdExchange string, expected data.Exchange) {
			Expect(mapExchange(eodhdExchange)).To(Equal(expected))
		},
		Entry("NASDAQ", "NASDAQ", data.NasdaqExchange),
		Entry("NYSE", "NYSE", data.NYSEExchange),
		Entry("BATS", "BATS", data.BATSExchange),
		Entry("NYSE MKT", "NYSE MKT", data.NYSEMktExchange),
		Entry("NYSE ARCA", "NYSE ARCA", data.ARCAExchange),
		Entry("NMFQS", "NMFQS", data.NMFQSExchange),
		Entry("OTC", "OTC", data.OTCExchange),
		Entry("unknown returns UnknownExchange", "AMEX-PRIVATE", data.UnknownExchange),
	)
})

var _ = Describe("parseSymbolList", func() {
	var fixture []byte

	BeforeEach(func() {
		var err error

		fixture, err = os.ReadFile(filepath.Join("testdata", "exchange_symbol_list.json"))
		Expect(err).NotTo(HaveOccurred())
	})

	It("parses every row with a recognized type", func() {
		assets, err := parseSymbolList(fixture, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(assets).To(HaveLen(4))
	})

	It("normalizes share-class tickers to slash form", func() {
		assets, err := parseSymbolList(fixture, false)
		Expect(err).NotTo(HaveOccurred())

		var brk *data.Asset
		for _, a := range assets {
			if a.Name == "Berkshire Hathaway Inc" {
				brk = a
			}
		}

		Expect(brk).NotTo(BeNil())
		Expect(brk.Ticker).To(Equal("BRK/A"))
	})

	It("populates ISINs", func() {
		assets, err := parseSymbolList(fixture, false)
		Expect(err).NotTo(HaveOccurred())

		var aapl *data.Asset
		for _, a := range assets {
			if a.Ticker == "AAPL" {
				aapl = a
			}
		}

		Expect(aapl).NotTo(BeNil())
		Expect(aapl.ISIN).To(ConsistOf("US0378331005"))
	})

	It("omits ISINs when EODHD returns empty string", func() {
		fixtureWithEmptyIsin := []byte(`[{"Code":"FOO","Name":"Foo","Exchange":"NYSE","Type":"Common Stock","Isin":""}]`)
		assets, err := parseSymbolList(fixtureWithEmptyIsin, false)
		Expect(err).NotTo(HaveOccurred())
		Expect(assets).To(HaveLen(1))
		Expect(assets[0].ISIN).To(BeEmpty())
	})

	It("marks rows from delisted=true as inactive without inventing a delisting date", func() {
		assets, err := parseSymbolList(fixture, true)
		Expect(err).NotTo(HaveOccurred())

		for _, a := range assets {
			Expect(a.Active).To(BeFalse())
			// EODHD does not return a delisting date in this endpoint,
			// so we leave it empty rather than stamping "now".
			Expect(a.DelistingDate).To(BeEmpty())
		}
	})

	It("marks rows as active when delisted=false", func() {
		assets, err := parseSymbolList(fixture, false)
		Expect(err).NotTo(HaveOccurred())

		for _, a := range assets {
			Expect(a.Active).To(BeTrue())
			Expect(a.DelistingDate).To(BeEmpty())
		}
	})
})

var _ = Describe("applyExistingFigis", func() {
	It("copies CompositeFigi and ShareClassFigi from a DB asset that shares a ticker", func() {
		incoming := []*data.Asset{
			{Ticker: "AAPL", Name: "Apple Inc"},
		}
		dbAssets := []*data.Asset{
			{Ticker: "AAPL", Name: "Apple Inc", CompositeFigi: "BBG000B9XRY4", ShareClassFigi: "BBG001S5N8V8", Active: true},
		}

		reused := applyExistingFigis(incoming, dbAssets)

		Expect(reused).To(Equal(1))
		Expect(incoming[0].CompositeFigi).To(Equal("BBG000B9XRY4"))
		Expect(incoming[0].ShareClassFigi).To(Equal("BBG001S5N8V8"))
	})

	It("leaves an EODHD asset alone when no DB asset shares its ticker", func() {
		incoming := []*data.Asset{
			{Ticker: "NEW", Name: "New Co"},
		}
		dbAssets := []*data.Asset{
			{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", Active: true},
		}

		reused := applyExistingFigis(incoming, dbAssets)

		Expect(reused).To(Equal(0))
		Expect(incoming[0].CompositeFigi).To(BeEmpty())
	})

	It("does not overwrite a CompositeFigi that the EODHD asset already has", func() {
		incoming := []*data.Asset{
			{Ticker: "AAPL", CompositeFigi: "PREEXISTING"},
		}
		dbAssets := []*data.Asset{
			{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", Active: true},
		}

		reused := applyExistingFigis(incoming, dbAssets)

		Expect(reused).To(Equal(0))
		Expect(incoming[0].CompositeFigi).To(Equal("PREEXISTING"))
	})

	It("prefers an active DB row over a delisted one with the same ticker", func() {
		incoming := []*data.Asset{
			{Ticker: "REUSED"},
		}
		dbAssets := []*data.Asset{
			{Ticker: "REUSED", CompositeFigi: "OLD-DELISTED", Active: false, LastUpdated: time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)},
			{Ticker: "REUSED", CompositeFigi: "CURRENT-ACTIVE", Active: true, LastUpdated: time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)},
		}

		reused := applyExistingFigis(incoming, dbAssets)

		Expect(reused).To(Equal(1))
		Expect(incoming[0].CompositeFigi).To(Equal("CURRENT-ACTIVE"))
	})

	It("breaks ties on most-recently-updated when both DB rows share an active state", func() {
		incoming := []*data.Asset{
			{Ticker: "TIE"},
		}
		dbAssets := []*data.Asset{
			{Ticker: "TIE", CompositeFigi: "OLDER", Active: false, LastUpdated: time.Date(2018, 1, 1, 0, 0, 0, 0, time.UTC)},
			{Ticker: "TIE", CompositeFigi: "NEWER", Active: false, LastUpdated: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)},
		}

		reused := applyExistingFigis(incoming, dbAssets)

		Expect(reused).To(Equal(1))
		Expect(incoming[0].CompositeFigi).To(Equal("NEWER"))
	})

	It("ignores DB rows that have no CompositeFigi", func() {
		incoming := []*data.Asset{
			{Ticker: "AAPL"},
		}
		dbAssets := []*data.Asset{
			{Ticker: "AAPL", CompositeFigi: "", Active: true},
		}

		reused := applyExistingFigis(incoming, dbAssets)

		Expect(reused).To(Equal(0))
		Expect(incoming[0].CompositeFigi).To(BeEmpty())
	})
})
