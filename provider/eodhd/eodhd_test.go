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

	It("marks rows as delisted when delisted=true", func() {
		assets, err := parseSymbolList(fixture, true)
		Expect(err).NotTo(HaveOccurred())

		for _, a := range assets {
			Expect(a.Active).To(BeFalse())
			Expect(a.DelistingDate).NotTo(BeEmpty())
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
