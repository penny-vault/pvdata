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
package pvindex

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pvdata/data"
)

var _ = Describe("isLPName", func() {
	DescribeTable("LP suffix detection",
		func(name string, expected bool) {
			Expect(isLPName(name)).To(Equal(expected))
		},
		Entry("Enterprise Products LP", "Enterprise Products Partners LP", true),
		Entry("Energy Transfer LP", "Energy Transfer LP", true),
		Entry("MPLX LP", "MPLX LP", true),
		Entry("Brookfield Infrastructure L.P.", "Brookfield Infrastructure L.P.", true),
		Entry("L.P. with trailing whitespace", "Some Partnership L.P.   ", true),
		Entry("LLP suffix", "Big Law LLP", true),
		Entry("Limited Partnership full word", "Foo Limited Partnership", true),
		Entry("L P with space", "ABC Investments L P", true),
		Entry("lower case lp", "enterprise products lp", true),
		Entry("Marsh & McLennan Companies (negative - not LP)", "Marsh & McLennan Companies", false),
		Entry("Apple Inc (negative)", "Apple Inc.", false),
		Entry("MetLife Inc (negative)", "MetLife Inc", false),
		Entry("LP in middle of name (negative)", "LP Holdings Corporation", false),
		Entry("Compass Plc (negative - similar shape)", "Compass Group plc", false),
		Entry("Empty name (negative)", "", false),
	)
})

var _ = Describe("filterAssetMaster", func() {
	mkAsset := func(ticker, name, exch string, atype data.AssetType, active bool) *data.Asset {
		return &data.Asset{
			Ticker:          ticker,
			Name:            name,
			PrimaryExchange: data.Exchange(exch),
			AssetType:       atype,
			Active:          active,
		}
	}

	It("keeps active US common stocks on whitelisted exchanges", func() {
		input := []*data.Asset{
			mkAsset("AAPL", "Apple Inc.", "NASDAQ", data.CommonStock, true),
			mkAsset("BAC", "Bank of America Corporation", "NYSE", data.CommonStock, true),
			mkAsset("XOM", "Exxon Mobil Corporation", "XNYS", data.CommonStock, true),
		}
		out := filterAssetMaster(input)
		Expect(out).To(HaveLen(3))
	})

	It("excludes inactive assets", func() {
		input := []*data.Asset{
			mkAsset("DEAD", "Dead Co", "NYSE", data.CommonStock, false),
		}
		out := filterAssetMaster(input)
		Expect(out).To(BeEmpty())
	})

	It("excludes non-CS asset types", func() {
		input := []*data.Asset{
			mkAsset("SPY", "SPDR S&P 500 ETF", "NYSE ARCA", data.ETF, true),
			mkAsset("PCQ", "PIMCO California Muni", "NYSE", data.CEF, true),
			mkAsset("BABA", "Alibaba ADR", "NYSE", data.ADRC, true),
		}
		out := filterAssetMaster(input)
		Expect(out).To(BeEmpty())
	})

	It("excludes OTC and unknown exchanges", func() {
		input := []*data.Asset{
			mkAsset("FOO", "Foo Inc", "OTC", data.CommonStock, true),
			mkAsset("BAR", "Bar Inc", "NMFQS", data.CommonStock, true),
			mkAsset("BAZ", "Baz Inc", "UNK", data.CommonStock, true),
		}
		out := filterAssetMaster(input)
		Expect(out).To(BeEmpty())
	})

	It("accepts both display-name and MIC code formats for whitelisted exchanges", func() {
		input := []*data.Asset{
			mkAsset("A", "Alpha Inc", "NASDAQ", data.CommonStock, true),
			mkAsset("B", "Beta Inc", "XNAS", data.CommonStock, true),
			mkAsset("C", "Gamma Inc", "NYSE", data.CommonStock, true),
			mkAsset("D", "Delta Inc", "XNYS", data.CommonStock, true),
			mkAsset("E", "Epsilon Inc", "NYSE MKT", data.CommonStock, true),
			mkAsset("F", "Zeta Inc", "XASE", data.CommonStock, true),
			mkAsset("G", "Eta Inc", "AMEX", data.CommonStock, true),
			mkAsset("H", "Theta Inc", "NYSE ARCA", data.CommonStock, true),
			mkAsset("I", "Iota Inc", "ARCX", data.CommonStock, true),
			mkAsset("J", "Kappa Inc", "BATS", data.CommonStock, true),
		}
		out := filterAssetMaster(input)
		Expect(out).To(HaveLen(10))
	})

	It("excludes LPs", func() {
		input := []*data.Asset{
			mkAsset("EPD", "Enterprise Products Partners LP", "NYSE", data.CommonStock, true),
			mkAsset("ET", "Energy Transfer LP", "NYSE", data.CommonStock, true),
		}
		out := filterAssetMaster(input)
		Expect(out).To(BeEmpty())
	})
})

var _ = Describe("dedupShareClasses", func() {
	mkAssetWithCIK := func(ticker, cik, figi string) *data.Asset {
		return &data.Asset{
			Ticker:        ticker,
			CompositeFigi: figi,
			CIK:           cik,
		}
	}

	It("keeps the highest-DV share class within a CIK group", func() {
		assets := []*data.Asset{
			mkAssetWithCIK("GOOGL", "0001652044", "BBG009S39JX6"),
			mkAssetWithCIK("GOOG", "0001652044", "BBG009S3NB30"),
		}
		dvByFigi := map[string]float64{
			"BBG009S39JX6": 1_500_000_000, // GOOGL
			"BBG009S3NB30": 2_500_000_000, // GOOG (higher)
		}
		out := dedupShareClasses(assets, dvByFigi)
		Expect(out).To(HaveLen(1))
		Expect(out[0].Ticker).To(Equal("GOOG"))
	})

	It("treats null CIK rows as singleton groups", func() {
		assets := []*data.Asset{
			mkAssetWithCIK("XYZ", "", "BBGXYZ00001"),
			mkAssetWithCIK("ABC", "", "BBGABC00001"),
		}
		dvByFigi := map[string]float64{
			"BBGXYZ00001": 100,
			"BBGABC00001": 200,
		}
		out := dedupShareClasses(assets, dvByFigi)
		Expect(out).To(HaveLen(2))
	})

	It("breaks ties by ticker for determinism", func() {
		assets := []*data.Asset{
			mkAssetWithCIK("AAA", "0001234567", "BBGAAA00001"),
			mkAssetWithCIK("BBB", "0001234567", "BBGBBB00001"),
		}
		dvByFigi := map[string]float64{
			"BBGAAA00001": 1000,
			"BBGBBB00001": 1000,
		}
		out := dedupShareClasses(assets, dvByFigi)
		Expect(out).To(HaveLen(1))
		Expect(out[0].Ticker).To(Equal("AAA"))
	})

	It("handles assets with zero dollar volume", func() {
		assets := []*data.Asset{
			mkAssetWithCIK("ZERO", "0001111111", "BBGZERO0001"),
		}
		out := dedupShareClasses(assets, map[string]float64{})
		Expect(out).To(HaveLen(1))
		Expect(out[0].Ticker).To(Equal("ZERO"))
	})
})
