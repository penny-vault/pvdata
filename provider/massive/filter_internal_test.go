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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pvdata/data"
)

var _ = Describe("marketIneligibleReason", func() {
	Context("ticker-suffix patterns", func() {
		It("drops units with the /U suffix", func() {
			asset := &data.Asset{Ticker: "GFC/U", AssetType: data.CommonStock, Name: "Gerova Financial Group Ltd Units"}
			reason, drop := marketIneligibleReason(asset)
			Expect(drop).To(BeTrue())
			Expect(reason).To(Equal("ticker_suffix=/U"))
		})

		It("drops units with the /UN suffix", func() {
			asset := &data.Asset{Ticker: "FOO/UN", AssetType: data.CommonStock}
			_, drop := marketIneligibleReason(asset)
			Expect(drop).To(BeTrue())
		})

		It("drops warrants with the /W and /WS suffixes", func() {
			Expect(reasonDrop(&data.Asset{Ticker: "FOO/W"})).To(BeTrue())
			Expect(reasonDrop(&data.Asset{Ticker: "FOO/WS"})).To(BeTrue())
		})

		It("drops rights with the /R and /RT suffixes", func() {
			Expect(reasonDrop(&data.Asset{Ticker: "FOO/R"})).To(BeTrue())
			Expect(reasonDrop(&data.Asset{Ticker: "FOO/RT"})).To(BeTrue())
		})

		It("does NOT drop class-share tickers (/A, /B)", func() {
			Expect(reasonDrop(&data.Asset{Ticker: "BRK/A", AssetType: data.CommonStock})).To(BeFalse())
			Expect(reasonDrop(&data.Asset{Ticker: "BF/B", AssetType: data.CommonStock})).To(BeFalse())
		})
	})

	Context("name patterns", func() {
		It("drops a record whose name contains 'Test Symbol'", func() {
			asset := &data.Asset{Ticker: "IPOO", AssetType: data.CommonStock, Name: "IPOO Test Symbol"}
			reason, drop := marketIneligibleReason(asset)
			Expect(drop).To(BeTrue())
			Expect(reason).To(Equal("name_contains=test symbol"))
		})

		It("matches 'test symbol' case-insensitively", func() {
			Expect(reasonDrop(&data.Asset{Name: "ipoo test symbol"})).To(BeTrue())
			Expect(reasonDrop(&data.Asset{Name: "IPOO TEST SYMBOL"})).To(BeTrue())
		})

		It("does NOT drop a normal company name", func() {
			Expect(reasonDrop(&data.Asset{Ticker: "AAPL", Name: "Apple Inc."})).To(BeFalse())
		})
	})

	Context("lowercase-letter marker", func() {
		It("drops a 'w' suffix (when issued)", func() {
			reason, drop := marketIneligibleReason(&data.Asset{Ticker: "ADNTw", Name: "Adient plc"})
			Expect(drop).To(BeTrue())
			Expect(reason).To(Equal("ticker_has_lowercase_marker"))
		})

		It("drops a 'p' marker (preferred share)", func() {
			Expect(reasonDrop(&data.Asset{Ticker: "AApB", Name: "Alcoa Preferred"})).To(BeTrue())
			Expect(reasonDrop(&data.Asset{Ticker: "AFSIpA"})).To(BeTrue())
		})

		It("drops an 'r' suffix (rights)", func() {
			Expect(reasonDrop(&data.Asset{Ticker: "CPFr"})).To(BeTrue())
		})

		It("drops an 'rw' suffix (rights when-issued)", func() {
			Expect(reasonDrop(&data.Asset{Ticker: "XCOrw"})).To(BeTrue())
		})

		It("keeps all-uppercase tickers regardless of length", func() {
			Expect(reasonDrop(&data.Asset{Ticker: "AAPL", Name: "Apple Inc."})).To(BeFalse())
			Expect(reasonDrop(&data.Asset{Ticker: "BRK/A", Name: "Berkshire"})).To(BeFalse())
		})
	})

	Context("extended name patterns", func() {
		It("drops 'when issued' names", func() {
			Expect(reasonDrop(&data.Asset{Ticker: "FOO", Name: "ACTAVIS PLC COM SHS (IRL) W.I."})).To(BeTrue())
			Expect(reasonDrop(&data.Asset{Ticker: "FOO", Name: "Aebi Schmidt Holding AG Common Stock When-Issued"})).To(BeTrue())
		})

		It("drops 'ex-distribution' names", func() {
			Expect(reasonDrop(&data.Asset{Ticker: "FOO", Name: "AFC Gamma, Inc. Common Stock Ex-distribution When-Issued"})).To(BeTrue())
		})

		It("drops 'pfd' and 'preferred' names", func() {
			Expect(reasonDrop(&data.Asset{Ticker: "FOO", Name: "ARCH COAL INC 5% PERPETUAL CUM CONV PFD"})).To(BeTrue())
			Expect(reasonDrop(&data.Asset{Ticker: "FOO", Name: "American Finance Trust 7.375% Series C Cumulative Redeemable Preferred Stock"})).To(BeTrue())
		})

		It("drops names containing 'warrant'", func() {
			Expect(reasonDrop(&data.Asset{Ticker: "FOO", Name: "HEALTHSOUTH CORP WT EXP 01/17/2017 PUR COM warrant"})).To(BeTrue())
		})
	})

	Context("nil safety", func() {
		It("returns false for a nil asset rather than panicking", func() {
			_, drop := marketIneligibleReason(nil)
			Expect(drop).To(BeFalse())
		})
	})
})

var _ = Describe("shortDelistedNoCoverageReason", func() {
	var api *massiveAssetFetcher

	BeforeEach(func() {
		api = newFetcherWithArchive(buildBBIArchive())
	})

	It("does not drop a currently-active asset, even with no dates and no coverage", func() {
		asset := &data.Asset{Ticker: "NEW", Active: true, ListingDate: "", DelistingDate: ""}
		_, drop := api.shortDelistedNoCoverageReason(asset)
		Expect(drop).To(BeFalse())
	})

	It("does not drop a delisted asset with EOD bars overlapping its window", func() {
		// BBI's archive has [2003-09-10..2010-07-06] for Blockbuster's
		// lifecycle. A delisted asset whose window falls inside that
		// range should NOT be filtered, even though the window is
		// shorter than 180 days.
		asset := &data.Asset{
			Ticker:        "BBI",
			Active:        false,
			ListingDate:   "2004-01-15",
			DelistingDate: "2004-03-15",
		}
		_, drop := api.shortDelistedNoCoverageReason(asset)
		Expect(drop).To(BeFalse())
	})

	It("drops a delisted asset whose window has no overlapping EOD range and is shorter than 180 days", func() {
		// AHI/Avadim-style: a single-day window for a ticker the EOD
		// archive does not cover at all.
		asset := &data.Asset{
			Ticker:        "GHOST",
			Active:        false,
			ListingDate:   "2020-01-30",
			DelistingDate: "2020-01-31",
		}
		reason, drop := api.shortDelistedNoCoverageReason(asset)
		Expect(drop).To(BeTrue())
		Expect(reason).To(Equal("duration=1d_no_eod_coverage"))
	})

	It("does not drop a delisted asset whose window is longer than the threshold", func() {
		// 365-day window, no EOD coverage — but long enough we trust it.
		asset := &data.Asset{
			Ticker:        "GHOST",
			Active:        false,
			ListingDate:   "2019-01-01",
			DelistingDate: "2020-01-01",
		}
		_, drop := api.shortDelistedNoCoverageReason(asset)
		Expect(drop).To(BeFalse())
	})

	It("does not drop when ListingDate or DelistingDate is empty", func() {
		Expect(reasonDropShort(api, &data.Asset{Active: false, ListingDate: "", DelistingDate: "2020-01-31"})).To(BeFalse())
		Expect(reasonDropShort(api, &data.Asset{Active: false, ListingDate: "2020-01-30", DelistingDate: ""})).To(BeFalse())
	})
})

func reasonDrop(asset *data.Asset) bool {
	_, drop := marketIneligibleReason(asset)
	return drop
}

func reasonDropShort(api *massiveAssetFetcher, asset *data.Asset) bool {
	_, drop := api.shortDelistedNoCoverageReason(asset)
	return drop
}
