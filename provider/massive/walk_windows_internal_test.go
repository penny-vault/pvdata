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

		sanitizeWalkComposites(ctx, assets, windows, map[string]walkWindow{}, map[string]walkWindow{}, stub)

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

		sanitizeWalkComposites(ctx, assets, windows, map[string]walkWindow{}, map[string]walkWindow{}, stub)

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

		sanitizeWalkComposites(ctx, assets, windows, map[string]walkWindow{}, map[string]walkWindow{}, stub)

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

		sanitizeWalkComposites(ctx, assets, windows, map[string]walkWindow{}, map[string]walkWindow{}, stub)

		Expect(assets).To(HaveLen(1))
		Expect(assets).To(HaveKey("FOO:BBG_WITHCIK"))
	})

	It("leaves the map unchanged when nothing is dirty", func() {
		addRow("AAPL", "BBG000B9XRY4", "BBG001S5N8V8", "0000320193", "2010-01-04", "2023-06-30")
		stub := confirmer(map[string]string{
			"BBG000B9XRY4": "US",
		})

		sanitizeWalkComposites(ctx, assets, windows, map[string]walkWindow{}, map[string]walkWindow{}, stub)

		Expect(assets).To(HaveLen(1))
		Expect(assets).To(HaveKey("AAPL:BBG000B9XRY4"))
		Expect(windows).To(HaveKey("AAPL:BBG000B9XRY4"))
	})

	It("consolidates a trailing CIK-only entry into the FIGI'd entry for the same lifecycle", func() {
		// Real-world Medley pattern: Massive returns the FIGI for most
		// of the lifecycle (CIK=0001490349, FIGI=BBG00K6RT2X7), then
		// drops the FIGI on the last few months and switches to a
		// wrong CIK (0001009215). The walk creates two historicalMap
		// entries; consolidation should fold the FIGI-less tail into
		// the FIGI'd entry.
		assets["MCC:BBG00K6RT2X7"] = &data.Asset{
			Ticker:        "MCC",
			CompositeFigi: "BBG00K6RT2X7",
			CIK:           "0001490349",
			Name:          "MEDLEY CAPITAL CORPORATION",
		}
		windows["MCC:BBG00K6RT2X7"] = walkWindow{
			firstSeen: mustParseDate("2011-01-20"),
			lastSeen:  mustParseDate("2020-07-24"),
		}

		assets["MCC:cik:0001009215"] = &data.Asset{
			Ticker: "MCC",
			CIK:    "0001009215",
			Name:   "MEDLEY CAPITAL CORPORATION",
		}
		windowsByCIK := map[string]walkWindow{
			"MCC:0001009215": {
				firstSeen: mustParseDate("2020-07-28"),
				lastSeen:  mustParseDate("2021-01-01"),
			},
		}

		stub := confirmer(map[string]string{
			"BBG00K6RT2X7": "US",
		})

		sanitizeWalkComposites(ctx, assets, windows, windowsByCIK, map[string]walkWindow{}, stub)

		Expect(assets).To(HaveLen(1))
		Expect(assets).To(HaveKey("MCC:BBG00K6RT2X7"))
		Expect(windows["MCC:BBG00K6RT2X7"].lastSeen).To(Equal(mustParseDate("2021-01-01")))
		Expect(windowsByCIK).NotTo(HaveKey("MCC:0001009215"))
	})

	It("does not consolidate when the FIGI-less entry's window is too far past the FIGI'd lastSeen", func() {
		// Same ticker, same name, but the gap is years (true ticker
		// reuse, not a Massive data hiccup). Leave both entries.
		assets["BBI:BBG_BLOCK"] = &data.Asset{
			Ticker:        "BBI",
			CompositeFigi: "BBG_BLOCK",
			CIK:           "0001085734",
			Name:          "Blockbuster Inc.",
		}
		windows["BBI:BBG_BLOCK"] = walkWindow{
			firstSeen: mustParseDate("2003-09-10"),
			lastSeen:  mustParseDate("2010-04-15"),
		}

		assets["BBI:cik:0000819050"] = &data.Asset{
			Ticker: "BBI",
			CIK:    "0000819050",
			Name:   "Brickell Biotech Inc.",
		}
		windowsByCIK := map[string]walkWindow{
			"BBI:0000819050": {
				firstSeen: mustParseDate("2019-09-24"),
				lastSeen:  mustParseDate("2022-09-07"),
			},
		}

		stub := confirmer(map[string]string{
			"BBG_BLOCK": "US",
		})

		sanitizeWalkComposites(ctx, assets, windows, windowsByCIK, map[string]walkWindow{}, stub)

		Expect(assets).To(HaveLen(2))
		Expect(assets).To(HaveKey("BBI:BBG_BLOCK"))
		Expect(assets).To(HaveKey("BBI:cik:0000819050"))
	})
})

var _ = Describe("normalizeIssuerName", func() {
	It("strips decorative suffixes and lowercases", func() {
		Expect(normalizeIssuerName("MEDLEY CAP CORP COM STK (DE)")).To(Equal("medley cap"))
		Expect(normalizeIssuerName("MEDLEY CAPITAL CORPORATION")).To(Equal("medley capital"))
		// Two different display strings for the same issuer normalize
		// the same core token order; equality is still the caller's
		// concern (see sameLifecycleName).
	})

	It("returns empty for empty or whitespace-only input", func() {
		Expect(normalizeIssuerName("")).To(Equal(""))
		Expect(normalizeIssuerName("   ")).To(Equal(""))
	})

	It("strips Inc / Corp / Ltd / Holdings / etc. uniformly", func() {
		Expect(normalizeIssuerName("Apple Inc.")).To(Equal("apple"))
		Expect(normalizeIssuerName("APPLE INCORPORATED")).To(Equal("apple"))
		Expect(normalizeIssuerName("FOO HOLDINGS LTD")).To(Equal("foo"))
	})
})

var _ = Describe("sameLifecycleName", func() {
	It("returns true when normalized names match", func() {
		Expect(sameLifecycleName("MEDLEY CAPITAL CORPORATION", "Medley Capital Corp.")).To(BeTrue())
	})

	It("returns false when either input is empty", func() {
		Expect(sameLifecycleName("", "Medley Capital")).To(BeFalse())
		Expect(sameLifecycleName("Medley Capital", "")).To(BeFalse())
	})

	It("returns false when normalized names differ", func() {
		Expect(sameLifecycleName("Mestek Inc.", "Medley Capital")).To(BeFalse())
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

var _ = Describe("cleanedListDate", func() {
	ctx := context.Background()

	newFetcher := func() *massiveAssetFetcher {
		return &massiveAssetFetcher{
			walkWindowsByFigi: map[string]walkWindow{},
			walkWindowsByCIK:  map[string]walkWindow{},
			walkWindowsByName: map[string]walkWindow{},
		}
	}

	It("passes the value through when no walk window exists", func() {
		api := newFetcher()
		got := api.cleanedListDate(ctx, massiveStock{ListDate: "2016-10-18"}, "AA", "0000004281")
		Expect(got).To(Equal("2016-10-18"))
	})

	It("passes the value through when list_date is on or before firstSeen", func() {
		api := newFetcher()
		api.walkWindowsByCIK["AA:0001675149"] = walkWindow{
			firstSeen: mustParseDate("2016-11-01"),
			lastSeen:  mustParseDate("2026-05-08"),
		}
		got := api.cleanedListDate(ctx, massiveStock{ListDate: "2016-10-18", CIK: "0001675149"}, "AA", "0001675149")
		Expect(got).To(Equal("2016-10-18"))
	})

	It("rejects a list_date that postdates the walk's firstSeen (predecessor case)", func() {
		// Reproduces the AA predecessor bug: Massive's per-ticker
		// details endpoint at date=2016-10-31 returns the successor's
		// list_date (2016-10-18) alongside the predecessor's CIK
		// (0000004281). The walk has observed AA under that CIK
		// continuously since 2004-06-25, so the list_date cannot be
		// later than 2004-06-25 for this entity.
		api := newFetcher()
		api.walkWindowsByCIK["AA:0000004281"] = walkWindow{
			firstSeen: mustParseDate("2004-06-25"),
			lastSeen:  mustParseDate("2016-10-31"),
		}
		got := api.cleanedListDate(ctx, massiveStock{ListDate: "2016-10-18", CIK: "0000004281"}, "AA", "0000004281")
		Expect(got).To(Equal(""))
	})

	It("falls back to the FIGI index when the CIK index does not match", func() {
		api := newFetcher()
		api.walkWindowsByFigi["AAPL:BBG000B9XRY4"] = walkWindow{
			firstSeen: mustParseDate("2004-06-25"),
			lastSeen:  mustParseDate("2026-05-08"),
		}
		got := api.cleanedListDate(ctx, massiveStock{ListDate: "2016-10-18", CompositeFIGI: "BBG000B9XRY4"}, "AAPL", "")
		Expect(got).To(Equal(""))
	})

	It("returns empty when list_date is empty", func() {
		api := newFetcher()
		got := api.cleanedListDate(ctx, massiveStock{ListDate: ""}, "AAPL", "")
		Expect(got).To(Equal(""))
	})

	It("returns the raw value when list_date is malformed", func() {
		api := newFetcher()
		got := api.cleanedListDate(ctx, massiveStock{ListDate: "not-a-date"}, "AAPL", "")
		Expect(got).To(Equal("not-a-date"))
	})
})
