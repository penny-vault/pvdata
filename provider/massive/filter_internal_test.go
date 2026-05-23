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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rs/zerolog"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/figi"
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

	It("drops a delisted phantom row with empty ListingDate when no EOD coverage exists", func() {
		// JATT-style: Massive returns a residual stub after the
		// asset is delisted, with delisting set, listing stripped,
		// and CIK stripped. No EOD bars exist for the ticker. The
		// stub should be dropped at publish so it cannot collide
		// with the real same-FIGI rows on the (ticker, composite_figi)
		// upsert and erase their already-correct listing date.
		Expect(reasonDropShort(api, &data.Asset{Ticker: "PHANTOM", Active: false, ListingDate: "", DelistingDate: "2020-01-31"})).To(BeTrue())
	})

	It("does not drop when DelistingDate is empty (no delisting boundary to evaluate)", func() {
		Expect(reasonDropShort(api, &data.Asset{Ticker: "GHOST", Active: false, ListingDate: "2020-01-30", DelistingDate: ""})).To(BeFalse())
	})

	It("does not drop a delisted phantom row when EOD coverage exists for its ticker", func() {
		// BBI's archive covers [2003-09-10..2010-07-06]. A phantom
		// row with empty listing but a delisting inside that window
		// has trade-data evidence the security existed; the filter
		// must not drop it just because Massive omitted the listing.
		Expect(reasonDropShort(api, &data.Asset{Ticker: "BBI", Active: false, ListingDate: "", DelistingDate: "2010-06-30"})).To(BeFalse())
	})
})

var _ = Describe("dropSyntheticDuplicatesOfRealFigi", func() {
	silent := func() *zerolog.Logger {
		l := zerolog.Nop()
		return &l
	}

	It("drops a synthetic-FIGI asset when a same-(ticker, name) real-FIGI sibling exists in the batch", func() {
		realFigi := "BBG020ZK3PR6"
		syntheticFigi := figi.GenerateSyntheticFIGI("JATT", "JATT II Acquisition Corp Ordinary Shares")

		assets := []*data.Asset{
			{Ticker: "JATT", Name: "JATT II Acquisition Corp Ordinary Shares", CompositeFigi: realFigi, CIK: "0002112446"},
			{Ticker: "JATT", Name: "JATT II Acquisition Corp Ordinary Shares", CompositeFigi: syntheticFigi, DelistingDate: "2026-04-16"},
		}

		got := dropSyntheticDuplicatesOfRealFigi(silent(), assets)

		Expect(got).To(HaveLen(1))
		Expect(got[0].CompositeFigi).To(Equal(realFigi))
	})

	It("preserves a synthetic-FIGI asset when no same-(ticker, name) real-FIGI sibling exists", func() {
		predecessor := &data.Asset{
			Ticker:        "BBI",
			Name:          "Blockbuster Inc",
			CompositeFigi: figi.GenerateSyntheticFIGIFromCIK("0001085734", "BBI"),
			DelistingDate: "2010-07-06",
		}
		successor := &data.Asset{
			Ticker:        "BBI",
			Name:          "Brickell Biotech Inc",
			CompositeFigi: "BBG00R4MBNH6",
		}

		got := dropSyntheticDuplicatesOfRealFigi(silent(), []*data.Asset{predecessor, successor})

		Expect(got).To(HaveLen(2))
	})

	It("returns the input unchanged when no real-FIGI assets are present", func() {
		assets := []*data.Asset{
			{Ticker: "FOO", Name: "Foo Inc", CompositeFigi: figi.GenerateSyntheticFIGI("FOO", "Foo Inc")},
		}

		got := dropSyntheticDuplicatesOfRealFigi(silent(), assets)

		Expect(got).To(HaveLen(1))
	})
})

var _ = Describe("dropSameCompositeFigiDuplicates", func() {
	silent := func() *zerolog.Logger {
		l := zerolog.Nop()
		return &l
	}

	It("drops the inactive sibling when two assets share the same (ticker, composite FIGI)", func() {
		active := &data.Asset{Ticker: "MSA", CompositeFigi: "BBG000BPDXF8", CIK: "0000066570", Active: true, Name: "Mine Safety Incorporated"}
		inactive := &data.Asset{Ticker: "MSA", CompositeFigi: "BBG000BPDXF8", CIK: "0000932691", Active: false, Name: "MINE SAFETY APPLIANCES"}

		got := dropSameCompositeFigiDuplicates(silent(), []*data.Asset{inactive, active})

		Expect(got).To(HaveLen(1))
		Expect(got[0]).To(Equal(active))
	})

	It("prefers a non-empty CIK among same-active-state siblings", func() {
		hasCIK := &data.Asset{Ticker: "X", CompositeFigi: "BBG000X", CIK: "0001234567", Active: true}
		noCIK := &data.Asset{Ticker: "X", CompositeFigi: "BBG000X", CIK: "", Active: true}

		got := dropSameCompositeFigiDuplicates(silent(), []*data.Asset{noCIK, hasCIK})

		Expect(got).To(HaveLen(1))
		Expect(got[0]).To(Equal(hasCIK))
	})

	It("preserves assets that do not share a composite FIGI with anyone else", func() {
		a := &data.Asset{Ticker: "BBI", CompositeFigi: "BBG000A", Active: true}
		b := &data.Asset{Ticker: "BBI", CompositeFigi: "BBG000B", Active: false}

		got := dropSameCompositeFigiDuplicates(silent(), []*data.Asset{a, b})

		Expect(got).To(HaveLen(2))
	})

	It("preserves caller order on the survivors", func() {
		a1 := &data.Asset{Ticker: "X", CompositeFigi: "BBG000A", Active: true}
		b := &data.Asset{Ticker: "X", CompositeFigi: "BBG000B", Active: true}
		a2 := &data.Asset{Ticker: "X", CompositeFigi: "BBG000A", Active: false}

		got := dropSameCompositeFigiDuplicates(silent(), []*data.Asset{a1, b, a2})

		Expect(got).To(HaveLen(2))
		Expect(got[0]).To(Equal(a1))
		Expect(got[1]).To(Equal(b))
	})

	It("drops a synthetic-FIGI row whose window overlaps a real-FIGI sibling on the same ticker", func() {
		// DAX-style: Global X carries a real OpenFIGI composite and
		// is currently active. A Recon Capital synthetic-FIGI row
		// surfaced from a name-only walk observation, listed one
		// day later. Their windows overlap; the synthetic must yield
		// to the real-FIGI authoritative record.
		real := &data.Asset{Ticker: "DAX", CompositeFigi: "BBG00MVW5W00", Active: true, ListingDate: "2014-10-22", Name: "Global X Funds Global X DAX Germany ETF"}
		synthetic := &data.Asset{Ticker: "DAX", CompositeFigi: "PVG76XCGMSN7", Active: false, ListingDate: "2014-10-23", DelistingDate: "2017-02-28", Name: "Recon Capital Series Trust Recon Capital DAX Germany ETF"}

		got := dropOverlappingSyntheticAgainstRealFigi(silent(), []*data.Asset{real, synthetic})

		Expect(got).To(HaveLen(1))
		Expect(got[0]).To(Equal(real))
	})

	It("does not drop a synthetic-FIGI row whose window does not overlap any real-FIGI sibling", func() {
		// BBI shape: Blockbuster (synthetic) ended 2010, Brickell
		// (real) started 2019. Disjoint windows — both survive.
		blockbuster := &data.Asset{Ticker: "BBI", CompositeFigi: "PVGMD46KH1G1", Active: false, ListingDate: "1999-05-06", DelistingDate: "2010-07-07"}
		brickell := &data.Asset{Ticker: "BBI", CompositeFigi: "BBG000BGN354", Active: false, ListingDate: "2019-09-03", DelistingDate: "2022-09-08"}

		got := dropOverlappingSyntheticAgainstRealFigi(silent(), []*data.Asset{blockbuster, brickell})

		Expect(got).To(HaveLen(2))
	})

	It("prefers the live snapshot (ValidFor zero) over a historical observation when active and CIK match", func() {
		// MSA-style: Massive returns the same composite FIGI twice,
		// once for the historical CIK observation pinned to a past
		// date and once for the current CIK as the live snapshot
		// (ValidFor zero). Both rows are active and carry a CIK; we
		// want the live snapshot to survive so the persisted row's
		// CIK, name, and branding reflect today's entity rather than
		// last decade's.
		historical := &data.Asset{
			Ticker:        "MSA",
			CompositeFigi: "BBG000BPDXF8",
			CIK:           "0000932691",
			Active:        true,
			Name:          "MINE SAFETY APPLIANCES",
			ValidFor:      time.Date(2014, 3, 10, 0, 0, 0, 0, time.UTC),
		}
		live := &data.Asset{
			Ticker:        "MSA",
			CompositeFigi: "BBG000BPDXF8",
			CIK:           "0000066570",
			Active:        true,
			Name:          "Mine Safety Incorporated",
		}

		got := dropSameCompositeFigiDuplicates(silent(), []*data.Asset{historical, live})

		Expect(got).To(HaveLen(1))
		Expect(got[0]).To(Equal(live))
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
