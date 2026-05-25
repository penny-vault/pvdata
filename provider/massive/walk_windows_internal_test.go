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
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/figi"
)

func mustParseDate(s string) time.Time {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		panic(err)
	}

	return t
}

var _ = Describe("updateWalkWindow", func() {
	It("seeds firstSeen and lastSeen on the first observation", func() {
		idx := map[string]walkWindow{}
		updateWalkWindow(idx, "AAPL:BBG000B9XRY4", mustParseDate("2020-06-15"))
		win := idx["AAPL:BBG000B9XRY4"]
		Expect(win.firstSeen).To(Equal(mustParseDate("2020-06-15")))
		Expect(win.lastSeen).To(Equal(mustParseDate("2020-06-15")))
	})

	It("pushes lastSeen forward on a later observation", func() {
		idx := map[string]walkWindow{}
		updateWalkWindow(idx, "AAPL:BBG000B9XRY4", mustParseDate("2020-06-15"))
		updateWalkWindow(idx, "AAPL:BBG000B9XRY4", mustParseDate("2022-01-10"))
		Expect(idx["AAPL:BBG000B9XRY4"].lastSeen).To(Equal(mustParseDate("2022-01-10")))
		Expect(idx["AAPL:BBG000B9XRY4"].firstSeen).To(Equal(mustParseDate("2020-06-15")))
	})

	It("pushes firstSeen backward on an earlier observation", func() {
		idx := map[string]walkWindow{}
		updateWalkWindow(idx, "AAPL:BBG000B9XRY4", mustParseDate("2020-06-15"))
		updateWalkWindow(idx, "AAPL:BBG000B9XRY4", mustParseDate("2015-04-01"))
		Expect(idx["AAPL:BBG000B9XRY4"].firstSeen).To(Equal(mustParseDate("2015-04-01")))
		Expect(idx["AAPL:BBG000B9XRY4"].lastSeen).To(Equal(mustParseDate("2020-06-15")))
	})

	It("keeps separate windows for different keys", func() {
		idx := map[string]walkWindow{}
		updateWalkWindow(idx, "BBI:BBG_BLOCKBUSTER", mustParseDate("2009-06-01"))
		updateWalkWindow(idx, "BBI:BBG_BLOCKBUSTER", mustParseDate("2010-04-15"))
		updateWalkWindow(idx, "BBI:BBG_BRICKELL", mustParseDate("2022-09-01"))
		updateWalkWindow(idx, "BBI:BBG_BRICKELL", mustParseDate("2026-05-01"))
		Expect(idx["BBI:BBG_BLOCKBUSTER"].lastSeen).To(Equal(mustParseDate("2010-04-15")))
		Expect(idx["BBI:BBG_BRICKELL"].firstSeen).To(Equal(mustParseDate("2022-09-01")))
	})
})

var _ = Describe("applyWalkDerivedDates", func() {
	const (
		walkStartStr = "2003-09-10"
		walkEndStr   = "2026-05-11"
	)

	newFetcher := func() *massiveAssetFetcher {
		return &massiveAssetFetcher{
			walkWindowsByFigi: map[string]walkWindow{},
			walkWindowsByCIK:  map[string]walkWindow{},
			walkStart:         mustParseDate(walkStartStr),
			walkEnd:           mustParseDate(walkEndStr),
		}
	}

	It("is a no-op when no walk has been run", func() {
		api := &massiveAssetFetcher{}
		asset := &data.Asset{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4"}
		api.applyWalkDerivedDates(asset)
		Expect(asset.DelistingDate).To(Equal(""))
		Expect(asset.ListingDate).To(Equal(""))
	})

	It("is a no-op when the asset has no matching walk window", func() {
		api := newFetcher()
		asset := &data.Asset{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4"}
		api.applyWalkDerivedDates(asset)
		Expect(asset.DelistingDate).To(Equal(""))
		Expect(asset.ListingDate).To(Equal(""))
	})

	It("derives DelistingDate via the FIGI index when lastSeen is well before walkEnd", func() {
		api := newFetcher()
		api.walkWindowsByFigi["BBI:BBG_BLOCKBUSTER"] = walkWindow{
			firstSeen: mustParseDate("2003-09-10"),
			lastSeen:  mustParseDate("2010-04-15"),
		}
		asset := &data.Asset{Ticker: "BBI", CompositeFigi: "BBG_BLOCKBUSTER"}
		api.applyWalkDerivedDates(asset)
		Expect(asset.DelistingDate).To(Equal("2010-04-16"))
	})

	It("does not derive DelistingDate when lastSeen is within the buffer window", func() {
		api := newFetcher()
		// lastSeen 5 days before walkEnd; buffer is 14 days so this
		// is within the no-flag zone.
		api.walkWindowsByFigi["AAPL:BBG000B9XRY4"] = walkWindow{
			firstSeen: mustParseDate("2003-09-10"),
			lastSeen:  mustParseDate("2026-05-06"),
		}
		asset := &data.Asset{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4"}
		api.applyWalkDerivedDates(asset)
		Expect(asset.DelistingDate).To(Equal(""))
	})

	It("falls back to the CIK index when the FIGI lookup misses", func() {
		// Simulates the case where the walk recorded the asset without
		// a list-response FIGI (so walkWindowsByFigi is empty for
		// this ticker) but assetDetail has since filled in the FIGI.
		// The CIK index, populated from the same walk row's CIK, is
		// the fallback that lets us find the window.
		api := newFetcher()
		api.walkWindowsByCIK["BBI:0001085734"] = walkWindow{
			firstSeen: mustParseDate("2003-09-10"),
			lastSeen:  mustParseDate("2010-04-15"),
		}
		asset := &data.Asset{
			Ticker:        "BBI",
			CompositeFigi: "BBG_FILLED_BY_ASSETDETAIL",
			CIK:           "0001085734",
		}
		api.applyWalkDerivedDates(asset)
		Expect(asset.DelistingDate).To(Equal("2010-04-16"))
	})

	It("does not override an assetDetail-provided DelistingDate", func() {
		api := newFetcher()
		api.walkWindowsByFigi["BBI:BBG_BLOCKBUSTER"] = walkWindow{
			firstSeen: mustParseDate("2003-09-10"),
			lastSeen:  mustParseDate("2010-04-15"),
		}
		asset := &data.Asset{
			Ticker:        "BBI",
			CompositeFigi: "BBG_BLOCKBUSTER",
			DelistingDate: "2010-07-07", // authoritative
		}
		api.applyWalkDerivedDates(asset)
		Expect(asset.DelistingDate).To(Equal("2010-07-07"))
	})

	It("derives ListingDate when firstSeen is well after walkStart", func() {
		api := newFetcher()
		api.walkWindowsByFigi["ATVI:BBG000CVWGS6"] = walkWindow{
			firstSeen: mustParseDate("2008-07-09"),
			lastSeen:  mustParseDate("2023-10-12"),
		}
		asset := &data.Asset{Ticker: "ATVI", CompositeFigi: "BBG000CVWGS6"}
		api.applyWalkDerivedDates(asset)
		Expect(asset.ListingDate).To(Equal("2008-07-09"))
		Expect(asset.DelistingDate).To(Equal("2023-10-13"))
	})

	It("does not derive ListingDate when firstSeen is within the start-side buffer", func() {
		api := newFetcher()
		// firstSeen 10 days after walkStart; buffer 14 days so this
		// is too close to the boundary to assert "first listed
		// during the walk".
		api.walkWindowsByFigi["AAPL:BBG000B9XRY4"] = walkWindow{
			firstSeen: mustParseDate("2003-09-20"),
			lastSeen:  mustParseDate("2026-05-08"),
		}
		asset := &data.Asset{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4"}
		api.applyWalkDerivedDates(asset)
		Expect(asset.ListingDate).To(Equal(""))
	})

	It("treats two same-ticker different-CIK entities as separate windows", func() {
		// BBI = Blockbuster (CIK 1085734) up to 2010, then BBI =
		// Brickell Biotech (CIK 0819050) from 2022. With ticker:cik
		// keys the two coexist; lookups by the live asset's CIK
		// find the correct window.
		api := newFetcher()
		api.walkWindowsByCIK["BBI:0001085734"] = walkWindow{
			firstSeen: mustParseDate("2003-09-10"),
			lastSeen:  mustParseDate("2010-04-15"),
		}
		api.walkWindowsByCIK["BBI:0000819050"] = walkWindow{
			firstSeen: mustParseDate("2022-09-01"),
			lastSeen:  mustParseDate("2026-05-08"),
		}

		blockbuster := &data.Asset{Ticker: "BBI", CIK: "0001085734"}
		api.applyWalkDerivedDates(blockbuster)
		Expect(blockbuster.DelistingDate).To(Equal("2010-04-16"))

		brickell := &data.Asset{Ticker: "BBI", CIK: "0000819050"}
		api.applyWalkDerivedDates(brickell)
		Expect(brickell.DelistingDate).To(Equal(""))
	})
})

var _ = Describe("sanitizeWalkComposites", func() {
	var (
		ctx     context.Context
		assets  map[string]*data.Asset
		windows map[string]walkWindow
	)

	BeforeEach(func() {
		ctx = context.Background()
		assets = map[string]*data.Asset{}
		windows = map[string]walkWindow{}
	})

	addRow := func(ticker, composite, shareClass, cik, firstSeen, lastSeen string) {
		a := &data.Asset{
			Ticker:         ticker,
			CompositeFigi:  composite,
			ShareClassFigi: shareClass,
			CIK:            cik,
		}
		assets[a.ID()] = a
		windows[a.ID()] = walkWindow{
			firstSeen: mustParseDate(firstSeen),
			lastSeen:  mustParseDate(lastSeen),
		}
	}

	confirmer := func(table map[string]string) compositeConfirmer {
		return func(_ context.Context, figis []string) map[string]*figi.OpenFigiAsset {
			out := make(map[string]*figi.OpenFigiAsset, len(figis))
			for _, f := range figis {
				if exch, ok := table[f]; ok {
					out[f] = &figi.OpenFigiAsset{Figi: f, ExchangeCode: exch}
				}
			}

			return out
		}
	}

	It("drops composites OpenFIGI confirms as non-US", func() {
		// ATVI: BBG000CVWGS6 is the real US listing; the other two are
		// Massive substitutions for foreign-exchange composites on
		// isolated dates (the Frankfurt AIY and the Euro ATVIEUR).
		addRow("ATVI", "BBG000CVWGS6", "BBG001S6C009", "0000718877", "2017-06-15", "2023-10-13")
		addRow("ATVI", "BBG000C2CV72", "BBG001S6C009", "0000718877", "2022-02-08", "2022-02-08")
		addRow("ATVI", "BBG00DJ2WYN1", "BBG001S6C009", "", "2019-09-24", "2019-09-24")

		stub := confirmer(map[string]string{
			"BBG000CVWGS6": "US",
			"BBG000C2CV72": "GR",
			"BBG00DJ2WYN1": "EO",
		})

		sanitizeWalkComposites(ctx, assets, windows, stub)

		Expect(assets).To(HaveLen(1))
		Expect(assets).To(HaveKey("ATVI:BBG000CVWGS6"))
		Expect(windows).NotTo(HaveKey("ATVI:BBG000C2CV72"))
		Expect(windows).NotTo(HaveKey("ATVI:BBG00DJ2WYN1"))
	})

	It("keeps composites OpenFIGI does not know about (delisted / evicted)", func() {
		// Long-delisted Blockbuster composite is not in OpenFIGI today.
		// The walk vote is still the best signal we have; do not drop.
		addRow("BBI", "BBG_BLOCKBUSTER", "BBG_BB_SC", "0001085734", "2003-09-10", "2010-04-15")

		stub := confirmer(nil)

		sanitizeWalkComposites(ctx, assets, windows, stub)

		Expect(assets).To(HaveLen(1))
		Expect(assets).To(HaveKey("BBI:BBG_BLOCKBUSTER"))
	})

	It("collapses (ticker, share_class) duplicates to the longest walk-window span", func() {
		// Same ticker and share class, but the second composite was
		// observed on a single day. Vote keeps the long-span composite.
		addRow("FOO", "BBG_LONGUS", "BBG_SC", "0000111111", "2010-01-04", "2023-06-30")
		addRow("FOO", "BBG_SHORTUS", "BBG_SC", "0000111111", "2022-02-08", "2022-02-09")

		stub := confirmer(map[string]string{
			"BBG_LONGUS":  "US",
			"BBG_SHORTUS": "US",
		})

		sanitizeWalkComposites(ctx, assets, windows, stub)

		Expect(assets).To(HaveLen(1))
		Expect(assets).To(HaveKey("FOO:BBG_LONGUS"))
		Expect(windows).NotTo(HaveKey("FOO:BBG_SHORTUS"))
	})

	It("prefers a composite with a non-empty CIK when walk spans tie", func() {
		// Equal span; CIK presence breaks the tie. Massive's dirty rows
		// often have empty CIK.
		addRow("FOO", "BBG_WITHCIK", "BBG_SC", "0000222222", "2020-01-02", "2020-01-02")
		addRow("FOO", "BBG_NOCIK", "BBG_SC", "", "2020-01-02", "2020-01-02")

		stub := confirmer(map[string]string{
			"BBG_WITHCIK": "US",
			"BBG_NOCIK":   "US",
		})

		sanitizeWalkComposites(ctx, assets, windows, stub)

		Expect(assets).To(HaveLen(1))
		Expect(assets).To(HaveKey("FOO:BBG_WITHCIK"))
	})

	It("leaves the map unchanged when nothing is dirty", func() {
		addRow("AAPL", "BBG000B9XRY4", "BBG001S5N8V8", "0000320193", "2010-01-04", "2023-06-30")
		stub := confirmer(map[string]string{
			"BBG000B9XRY4": "US",
		})

		sanitizeWalkComposites(ctx, assets, windows, stub)

		Expect(assets).To(HaveLen(1))
		Expect(assets).To(HaveKey("AAPL:BBG000B9XRY4"))
		Expect(windows).To(HaveKey("AAPL:BBG000B9XRY4"))
	})
})

var _ = Describe("walkHistoricalKey", func() {
	It("uses ticker:composite_figi when composite is set", func() {
		asset := &data.Asset{
			Ticker:        "AAPL",
			CompositeFigi: "BBG000B9XRY4",
			CIK:           "0000320193",
		}
		Expect(walkHistoricalKey(asset)).To(Equal("AAPL:BBG000B9XRY4"))
	})

	It("falls back to ticker:cik when composite is empty", func() {
		// Blockbuster (BBI 1999-2010). Massive's historical-list
		// endpoint serves Blockbuster with composite_figi=null but
		// cik="0001085734"; without the CIK fallback the key would
		// collapse to "BBI:" and collide with any other empty-composite
		// row for the same ticker.
		asset := &data.Asset{
			Ticker: "BBI",
			CIK:    "0001085734",
			Name:   "BLOCKBUSTER INC CL-A",
		}
		Expect(walkHistoricalKey(asset)).To(Equal("BBI:cik:0001085734"))
	})

	It("falls back to ticker:name when both composite and CIK are empty", func() {
		// Massive anomaly: on 2019-09-24 the list endpoint returned
		// BBI with name="Brickell Biotech, Inc. Common Stock" but
		// both composite_figi and cik null. The name fallback keeps
		// this row separable from any other identifier-less BBI row
		// rather than letting it silently overwrite a sibling.
		asset := &data.Asset{
			Ticker: "BBI",
			Name:   "Brickell Biotech, Inc. Common Stock",
		}
		Expect(walkHistoricalKey(asset)).To(Equal("BBI:name:Brickell Biotech, Inc. Common Stock"))
	})

	It("disambiguates a predecessor from a same-ticker successor anomaly", func() {
		// The exact collision the historical-walk used to suffer: a
		// real Blockbuster observation (empty composite, populated
		// CIK) and a same-ticker Brickell observation (empty composite,
		// empty CIK) both used to produce the key "BBI:" and overwrite
		// each other under keep-newest-by-ValidFor. With CIK + name
		// disambiguation they produce different keys and both survive
		// the walk merge.
		blockbuster := &data.Asset{
			Ticker: "BBI",
			CIK:    "0001085734",
			Name:   "BLOCKBUSTER INC CL-A",
		}
		brickellAnomaly := &data.Asset{
			Ticker: "BBI",
			Name:   "Brickell Biotech, Inc. Common Stock",
		}

		Expect(walkHistoricalKey(blockbuster)).NotTo(Equal(walkHistoricalKey(brickellAnomaly)))
	})

	It("disambiguates two same-ticker entities with different CIKs", func() {
		// Different predecessor + successor under one recycled ticker;
		// both have empty composite. CIK alone is enough.
		blockbuster := &data.Asset{
			Ticker: "BBI",
			CIK:    "0001085734",
		}
		brickell := &data.Asset{
			Ticker: "BBI",
			CIK:    "0000819050",
		}

		Expect(walkHistoricalKey(blockbuster)).NotTo(Equal(walkHistoricalKey(brickell)))
	})
})
