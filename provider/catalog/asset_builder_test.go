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
package catalog

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/figi"
)

var _ = Describe("AssetBuilder.finalize", func() {
	defaultTracked := func() map[string]struct{} {
		return map[string]struct{}{"CS": {}, "ADRC": {}, "ETF": {}, "CEF": {}, "ETN": {}, "UNK": {}}
	}

	emptyArchive := newEmptyArchive()

	builderWithToday := func(today map[string]struct{}) *AssetBuilder {
		return &AssetBuilder{
			api:      &massiveAssetFetcher{},
			archive:  emptyArchive,
			tracked:  defaultTracked(),
			todayIDs: today,
		}
	}

	It("publishes one closed lifecycle when the ticker is not in today's active snapshot", func() {
		b := builderWithToday(nil)

		p := &proposedAsset{
			ticker: "FOO",
			rng:    dateRange{Start: d("2015-04-10"), End: d("2018-09-20")},
			isLast: true,
			record: massiveStock{
				Ticker:        "FOO",
				Name:          "Foo Industries",
				CompositeFIGI: "BBG000000001",
				Type:          "CS",
				CIK:           "0000001234",
			},
		}

		asset := b.finalize(context.Background(), p, nil)

		Expect(asset).NotTo(BeNil())
		Expect(asset.Ticker).To(Equal("FOO"))
		Expect(asset.CompositeFigi).To(Equal("BBG000000001"))
		Expect(asset.AssetType).To(Equal(data.AssetType("CS")))
		Expect(asset.ListingDate).To(Equal("2015-04-10"))
		Expect(asset.DelistingDate).To(Equal("2018-09-21"))
		Expect(asset.Active).To(BeFalse())
	})

	It("leaves delisted empty and active=true when the last range belongs to today's snapshot", func() {
		b := builderWithToday(map[string]struct{}{"FOO": {}})

		p := &proposedAsset{
			ticker: "FOO",
			rng:    dateRange{Start: d("2015-04-10"), End: d("2026-05-20")},
			isLast: true,
			record: massiveStock{
				Ticker:        "FOO",
				Name:          "Foo Industries",
				CompositeFIGI: "BBG000000001",
				Type:          "CS",
				CIK:           "0000001234",
			},
		}

		asset := b.finalize(context.Background(), p, nil)

		Expect(asset).NotTo(BeNil())
		Expect(asset.ListingDate).To(Equal("2015-04-10"))
		Expect(asset.DelistingDate).To(BeEmpty())
		Expect(asset.Active).To(BeTrue())
	})

	It("never sets active=true on a non-last lifecycle even when the ticker is in today's snapshot", func() {
		b := builderWithToday(map[string]struct{}{"FOO": {}})

		p := &proposedAsset{
			ticker: "FOO",
			rng:    dateRange{Start: d("2002-01-01"), End: d("2005-06-30")},
			isLast: false,
			record: massiveStock{
				Ticker:        "FOO",
				Name:          "Foo Industries",
				CompositeFIGI: "BBG000000001",
				Type:          "CS",
				CIK:           "0000001234",
			},
		}

		asset := b.finalize(context.Background(), p, nil)

		Expect(asset).NotTo(BeNil())
		Expect(asset.DelistingDate).To(Equal("2005-07-01"))
		Expect(asset.Active).To(BeFalse())
	})

	It("mints a synthetic FIGI when OpenFIGI confirms the composite as non-US", func() {
		b := builderWithToday(nil)

		p := &proposedAsset{
			ticker: "AVP",
			rng:    dateRange{Start: d("2008-03-01"), End: d("2020-01-15")},
			isLast: true,
			record: massiveStock{
				Ticker:         "AVP",
				Name:           "Avon Products Inc",
				CompositeFIGI:  "BBG000FOREIGN",
				ShareClassFIGI: "BBG000SHARECLS",
				Type:           "CS",
				CIK:            "0000008868",
			},
		}

		nonUS := map[string]struct{}{"BBG000FOREIGN": {}}

		asset := b.finalize(context.Background(), p, nonUS)

		Expect(asset).NotTo(BeNil())
		Expect(figi.IsSyntheticFIGI(asset.CompositeFigi)).To(BeTrue(),
			"composite must be a synthetic PVG-prefixed FIGI when OpenFIGI says non-US")
		Expect(asset.CompositeFigi).To(Equal(figi.GenerateSyntheticFIGIFromCIKLifecycle("0000008868", "AVP", "2008-03-01")))
		Expect(asset.ShareClassFigi).To(BeEmpty(),
			"share-class must clear when the composite is rewritten to a synthetic")
	})

	It("mints a synthetic FIGI when Massive returns an empty composite", func() {
		b := builderWithToday(nil)

		p := &proposedAsset{
			ticker: "DAL",
			rng:    dateRange{Start: d("2003-11-01"), End: d("2007-04-30")},
			isLast: false,
			record: massiveStock{
				Ticker: "DAL",
				Name:   "Delta Air Lines Inc",
				Type:   "CS",
				CIK:    "0000027904",
				// composite_figi intentionally absent — pre-bankruptcy
				// lifecycle Massive returns without a FIGI.
			},
		}

		asset := b.finalize(context.Background(), p, nil)

		Expect(asset).NotTo(BeNil())
		Expect(figi.IsSyntheticFIGI(asset.CompositeFigi)).To(BeTrue())
		Expect(asset.CompositeFigi).To(Equal(figi.GenerateSyntheticFIGIFromCIKLifecycle("0000027904", "DAL", "2003-11-01")))
		Expect(asset.ShareClassFigi).To(BeEmpty())
	})

	It("falls back to (ticker, name) synthetic when no CIK is available", func() {
		b := builderWithToday(nil)

		p := &proposedAsset{
			ticker: "OLDTKR",
			rng:    dateRange{Start: d("1999-01-01"), End: d("2001-06-30")},
			isLast: false,
			record: massiveStock{
				Ticker: "OLDTKR",
				Name:   "Old Company Inc",
				Type:   "CS",
				// no CIK, no composite_figi
			},
		}

		asset := b.finalize(context.Background(), p, nil)

		Expect(asset).NotTo(BeNil())
		Expect(asset.CompositeFigi).To(Equal(figi.GenerateSyntheticFIGILifecycle("OLDTKR", "Old Company Inc", "1999-01-01")))
	})

	It("drops the lifecycle when no synthetic mint is possible (no CIK, no name)", func() {
		b := builderWithToday(nil)

		p := &proposedAsset{
			ticker: "BAD",
			rng:    dateRange{Start: d("2003-01-01"), End: d("2004-12-31")},
			isLast: false,
			record: massiveStock{
				Ticker: "BAD",
				Type:   "CS",
			},
		}

		asset := b.finalize(context.Background(), p, nil)

		Expect(asset).To(BeNil())
	})

	It("drops the lifecycle when the asset type is not tracked", func() {
		b := builderWithToday(nil)

		p := &proposedAsset{
			ticker: "PFDX",
			rng:    dateRange{Start: d("2005-05-01"), End: d("2010-10-31")},
			isLast: false,
			record: massiveStock{
				Ticker:        "PFDX",
				Name:          "Preferred Sleeve",
				CompositeFIGI: "BBG000000002",
				Type:          "PFD",
				CIK:           "0000005678",
			},
		}

		asset := b.finalize(context.Background(), p, nil)

		Expect(asset).To(BeNil())
	})

	It("passes a US-listed composite through unchanged", func() {
		b := builderWithToday(nil)

		p := &proposedAsset{
			ticker: "AAPL",
			rng:    dateRange{Start: d("2003-09-10"), End: d("2018-04-30")},
			isLast: false,
			record: massiveStock{
				Ticker:         "AAPL",
				Name:           "Apple Inc",
				CompositeFIGI:  "BBG000B9XRY4",
				ShareClassFIGI: "BBG001S5N8V8",
				Type:           "CS",
				CIK:            "0000320193",
			},
		}

		asset := b.finalize(context.Background(), p, nil)

		Expect(asset).NotTo(BeNil())
		Expect(asset.CompositeFigi).To(Equal("BBG000B9XRY4"))
		Expect(asset.ShareClassFigi).To(Equal("BBG001S5N8V8"))
	})
})

var _ = Describe("AssetBuilder.finalize left-edge listed override", func() {
	defaultTracked := func() map[string]struct{} {
		return map[string]struct{}{"CS": {}, "ADRC": {}, "ETF": {}, "CEF": {}, "ETN": {}, "UNK": {}}
	}

	// archiveWithCoverage returns an EODArchive whose overall coverage
	// is [coverageStart, coverageEnd]. Tests only consult Coverage() so
	// the per-ticker map is intentionally left empty.
	archiveWithCoverage := func(coverageStart, coverageEnd time.Time) *EODArchive {
		a := newEmptyArchive()
		a.coverageStart = coverageStart
		a.coverageEnd = coverageEnd

		return a
	}

	builderOn := func(archive *EODArchive, today map[string]struct{}) *AssetBuilder {
		return &AssetBuilder{
			api:      &massiveAssetFetcher{},
			archive:  archive,
			tracked:  defaultTracked(),
			todayIDs: today,
		}
	}

	It("substitutes Massive list_date for listed when the EOD range Start equals the archive's coverage Start (BBI shape)", func() {
		archive := archiveWithCoverage(d("2003-09-10"), d("2026-05-20"))
		b := builderOn(archive, nil)

		p := &proposedAsset{
			ticker: "BBI",
			rng:    dateRange{Start: d("2003-09-10"), End: d("2010-07-06")},
			isLast: false,
			record: massiveStock{
				Ticker:        "BBI",
				Name:          "BLOCKBUSTER INC CL-A",
				CompositeFIGI: "BBG000BGN354",
				Type:          "CS",
				CIK:           "0001085734",
				ListDate:      "1993-03-16",
			},
		}

		asset := b.finalize(context.Background(), p, nil)

		Expect(asset).NotTo(BeNil())
		Expect(asset.ListingDate).To(Equal("1993-03-16"))
		Expect(asset.DelistingDate).To(Equal("2010-07-07"))
	})

	It("keeps the EOD range Start when the lifecycle starts inside coverage (not on the left edge)", func() {
		archive := archiveWithCoverage(d("2003-09-10"), d("2026-05-20"))
		b := builderOn(archive, nil)

		p := &proposedAsset{
			ticker: "FOO",
			rng:    dateRange{Start: d("2015-04-10"), End: d("2018-09-20")},
			isLast: false,
			record: massiveStock{
				Ticker:        "FOO",
				Name:          "Foo Industries",
				CompositeFIGI: "BBG000000001",
				Type:          "CS",
				CIK:           "0000001234",
				ListDate:      "1990-01-01",
			},
		}

		asset := b.finalize(context.Background(), p, nil)

		Expect(asset).NotTo(BeNil())
		Expect(asset.ListingDate).To(Equal("2015-04-10"),
			"a lifecycle whose first bar is well inside coverage trusts EOD over Massive's list_date")
	})

	It("keeps the EOD range Start when Massive's list_date is a known sentinel (1899-12-30, 1972-06-01)", func() {
		archive := archiveWithCoverage(d("2003-09-10"), d("2026-05-20"))
		b := builderOn(archive, nil)

		for _, sentinel := range []string{"1899-12-30", "1972-06-01"} {
			p := &proposedAsset{
				ticker: "BFA",
				rng:    dateRange{Start: d("2003-09-10"), End: d("2010-12-31")},
				isLast: false,
				record: massiveStock{
					Ticker:        "BF/A",
					Name:          "Brown-Forman Class A",
					CompositeFIGI: "BBG000BD2GF7",
					Type:          "CS",
					CIK:           "0000014693",
					ListDate:      sentinel,
				},
			}

			asset := b.finalize(context.Background(), p, nil)

			Expect(asset).NotTo(BeNil(), "sentinel %s should not block publish", sentinel)
			Expect(asset.ListingDate).To(Equal("2003-09-10"),
				"sentinel %s should be rejected; listed falls back to EOD range Start", sentinel)
		}
	})

	It("keeps the EOD range Start when Massive's list_date is missing", func() {
		archive := archiveWithCoverage(d("2003-09-10"), d("2026-05-20"))
		b := builderOn(archive, nil)

		p := &proposedAsset{
			ticker: "BBI",
			rng:    dateRange{Start: d("2003-09-10"), End: d("2010-07-06")},
			isLast: false,
			record: massiveStock{
				Ticker:        "BBI",
				Name:          "BLOCKBUSTER INC CL-A",
				CompositeFIGI: "BBG000BGN354",
				Type:          "CS",
				CIK:           "0001085734",
				// no ListDate
			},
		}

		asset := b.finalize(context.Background(), p, nil)

		Expect(asset).NotTo(BeNil())
		Expect(asset.ListingDate).To(Equal("2003-09-10"))
	})

	It("keeps the EOD range Start when Massive's list_date is at or after the range (no pre-coverage signal to recover)", func() {
		archive := archiveWithCoverage(d("2003-09-10"), d("2026-05-20"))
		b := builderOn(archive, nil)

		p := &proposedAsset{
			ticker: "BAR",
			rng:    dateRange{Start: d("2003-09-10"), End: d("2010-07-06")},
			isLast: false,
			record: massiveStock{
				Ticker:        "BAR",
				Name:          "Bar Inc",
				CompositeFIGI: "BBG000000002",
				Type:          "CS",
				CIK:           "0000002222",
				ListDate:      "2005-01-15",
			},
		}

		asset := b.finalize(context.Background(), p, nil)

		Expect(asset).NotTo(BeNil())
		Expect(asset.ListingDate).To(Equal("2003-09-10"),
			"a list_date inside the bar window contradicts trading evidence and must not be used")
	})
})

var _ = Describe("AssetBuilder.finalize asset-type filter", func() {
	defaultTracked := func() map[string]struct{} {
		return map[string]struct{}{"CS": {}, "ADRC": {}, "ETF": {}, "CEF": {}, "ETN": {}, "UNK": {}}
	}

	emptyArchive := newEmptyArchive()
	builder := func() *AssetBuilder {
		return &AssetBuilder{
			api:     &massiveAssetFetcher{},
			archive: emptyArchive,
			tracked: defaultTracked(),
		}
	}

	Context("when Massive sets the type field (the authoritative path)", func() {
		It("keeps a row whose type is CS when the ticker has a warrant suffix (Massive's type wins over ticker shape)", func() {
			// Massive types this as CS; the /W suffix would normally
			// flag it as a warrant, but type is the authoritative
			// signal and the ticker-shape heuristic is empty-type only.
			p := &proposedAsset{
				ticker: "FOO/W",
				rng:    dateRange{Start: d("2018-01-15"), End: d("2019-06-30")},
				isLast: false,
				record: massiveStock{
					Ticker:        "FOO.W",
					Name:          "Foo Holdings",
					CompositeFIGI: "BBG000000010",
					Type:          "CS",
					CIK:           "0000010101",
				},
			}

			Expect(builder().finalize(context.Background(), p, nil)).NotTo(BeNil())
		})

		It("drops a row whose name marks it as a test symbol even when type is CS (IPOO case)", func() {
			// Massive types its own test symbols as CS; the name
			// pattern check has to run regardless of type or the
			// "IPOO Test Symbol" placeholder lands in the catalog.
			p := &proposedAsset{
				ticker: "IPOO",
				rng:    dateRange{Start: d("2020-01-15"), End: d("2020-01-20")},
				isLast: false,
				record: massiveStock{
					Ticker:        "IPOO",
					Name:          "IPOO Test Symbol",
					CompositeFIGI: "BBG000000050",
					Type:          "CS",
					CIK:           "0000080808",
				},
			}

			Expect(builder().finalize(context.Background(), p, nil)).To(BeNil())
		})

		It("drops a row whose name says 'preferred' even when type is CS", func() {
			p := &proposedAsset{
				ticker: "PFDX",
				rng:    dateRange{Start: d("2010-01-15"), End: d("2015-06-30")},
				isLast: false,
				record: massiveStock{
					Ticker:        "PFDX",
					Name:          "American Finance Trust 7.375% Series C Cumulative Redeemable Preferred Stock",
					CompositeFIGI: "BBG000000011",
					Type:          "CS",
					CIK:           "0000020202",
				},
			}

			Expect(builder().finalize(context.Background(), p, nil)).To(BeNil())
		})

		It("drops a row whose name says 'when-issued' even when type is CS", func() {
			p := &proposedAsset{
				ticker: "ABCD",
				rng:    dateRange{Start: d("2020-03-01"), End: d("2020-03-15")},
				isLast: false,
				record: massiveStock{
					Ticker:        "ABCD",
					Name:          "Aebi Schmidt Holding AG Common Stock When-Issued",
					CompositeFIGI: "BBG000000012",
					Type:          "CS",
					CIK:           "0000030303",
				},
			}

			Expect(builder().finalize(context.Background(), p, nil)).To(BeNil())
		})

		It("drops a row whose type is set but not in the tracked set (PFD, WARRANT, etc.)", func() {
			for _, untracked := range []string{"PFD", "WARRANT", "UNIT", "RIGHT"} {
				p := &proposedAsset{
					ticker: "OK",
					rng:    dateRange{Start: d("2010-01-15"), End: d("2015-06-30")},
					isLast: false,
					record: massiveStock{
						Ticker:        "OK",
						Name:          "Some Issuer",
						CompositeFIGI: "BBG000000011",
						Type:          untracked,
						CIK:           "0000020202",
					},
				}

				Expect(builder().finalize(context.Background(), p, nil)).To(BeNil(),
					"type=%s should be dropped because it is not in the tracked set", untracked)
			}
		})

		It("keeps a clean common-stock row", func() {
			p := &proposedAsset{
				ticker: "AAPL",
				rng:    dateRange{Start: d("2003-09-10"), End: d("2018-04-30")},
				isLast: false,
				record: massiveStock{
					Ticker:        "AAPL",
					Name:          "Apple Inc.",
					CompositeFIGI: "BBG000B9XRY4",
					Type:          "CS",
					CIK:           "0000320193",
				},
			}

			asset := builder().finalize(context.Background(), p, nil)
			Expect(asset).NotTo(BeNil())
			Expect(asset.Ticker).To(Equal("AAPL"))
		})

		It("promotes Massive type=FUND to CEF because the lifecycle came from the EOD archive", func() {
			// AFB (AllianceBernstein National Municipal Income Fund) is a
			// closed-end fund: it trades intraday on NYSE and has
			// continuous EOD bars from 2003-09-10 onward. Massive types
			// it FUND today, but reaching finalize means EOD bars exist,
			// which is the defining signal of a CEF vs an open-end mutual.
			p := &proposedAsset{
				ticker: "AFB",
				rng:    dateRange{Start: d("2003-09-10"), End: d("2026-05-22")},
				isLast: true,
				record: massiveStock{
					Ticker:        "AFB",
					Name:          "AllianceBernstein National Municipal Income Fund",
					CompositeFIGI: "BBG000000020",
					Type:          "FUND",
					CIK:           "0001162027",
				},
			}

			asset := builder().finalize(context.Background(), p, nil)
			Expect(asset).NotTo(BeNil())
			Expect(asset.AssetType).To(Equal(data.CEF))
		})
	})

	Context("when Massive's type field is empty (the heuristic fallback path)", func() {
		It("drops a row whose ticker suffix marks it as a warrant / unit / right", func() {
			for _, ticker := range []string{"FOO/W", "FOO/WS", "FOO/U", "FOO/UN", "FOO/R", "FOO/RT", "FOO/WD"} {
				p := &proposedAsset{
					ticker: ticker,
					rng:    dateRange{Start: d("2018-01-15"), End: d("2019-06-30")},
					isLast: false,
					record: massiveStock{
						Ticker:        ticker,
						Name:          "Foo Holdings",
						CompositeFIGI: "BBG000000020",
						// no Type
						CIK: "0000050505",
					},
				}

				Expect(builder().finalize(context.Background(), p, nil)).To(BeNil(),
					"untyped row with ticker=%s should be dropped by the suffix heuristic", ticker)
			}
		})

		It("drops a row with a lowercase placeholder marker (ADNTw etc.) when type is empty", func() {
			p := &proposedAsset{
				ticker: "ADNTw",
				rng:    dateRange{Start: d("2017-09-01"), End: d("2017-12-31")},
				isLast: false,
				record: massiveStock{
					Ticker:        "ADNTw",
					Name:          "Adient plc",
					CompositeFIGI: "BBG000000013",
					// no Type
					CIK: "0000040404",
				},
			}

			Expect(builder().finalize(context.Background(), p, nil)).To(BeNil())
		})

		It("drops a row whose name contains a non-tradable pattern (preferred, warrant, when-issued) when type is empty", func() {
			names := []string{
				"American Finance Trust Series C Preferred Stock",
				"Aebi Schmidt Holding AG Common Stock When-Issued",
				"AFC Gamma, Inc. Common Stock Ex-distribution When-Issued",
				"HEALTHSOUTH CORP WT EXP 01/17/2017 PUR COM warrant",
			}

			for _, name := range names {
				p := &proposedAsset{
					ticker: "OK",
					rng:    dateRange{Start: d("2010-01-15"), End: d("2015-06-30")},
					isLast: false,
					record: massiveStock{
						Ticker:        "OK",
						Name:          name,
						CompositeFIGI: "BBG000000030",
						// no Type
						CIK: "0000060606",
					},
				}

				Expect(builder().finalize(context.Background(), p, nil)).To(BeNil(),
					"untyped row with name %q should be dropped by the name heuristic", name)
			}
		})

		It("publishes a row with type=UNK when nothing classifies it but it passed the name and ticker filters", func() {
			// SEC client is not configured in tests, so the fallback
			// returns ok=false. Massive did not supply a type. The
			// ticker is clean and the name passes the pattern filter.
			// The builder publishes with UNK so the row is in the
			// catalog for later review rather than dropped silently.
			p := &proposedAsset{
				ticker: "OBSCURE",
				rng:    dateRange{Start: d("1998-01-15"), End: d("2000-06-30")},
				isLast: false,
				record: massiveStock{
					Ticker:        "OBSCURE",
					Name:          "Obscure Issuer",
					CompositeFIGI: "BBG000000040",
				},
			}

			asset := builder().finalize(context.Background(), p, nil)
			Expect(asset).NotTo(BeNil())
			Expect(asset.AssetType).To(Equal(data.UnknownAsset))
		})
	})
})

var _ = Describe("parseMassiveListDate", func() {
	It("parses a valid YYYY-MM-DD string", func() {
		got := parseMassiveListDate("1993-03-16")
		Expect(got).To(Equal(d("1993-03-16")))
	})

	It("returns zero on empty / whitespace input", func() {
		Expect(parseMassiveListDate("").IsZero()).To(BeTrue())
		Expect(parseMassiveListDate("   ").IsZero()).To(BeTrue())
	})

	It("returns zero on unparseable input", func() {
		Expect(parseMassiveListDate("not-a-date").IsZero()).To(BeTrue())
		Expect(parseMassiveListDate("03/16/1993").IsZero()).To(BeTrue())
	})

	It("returns zero on known Massive list_date sentinels", func() {
		Expect(parseMassiveListDate("1899-12-30").IsZero()).To(BeTrue())
		Expect(parseMassiveListDate("1972-06-01").IsZero()).To(BeTrue())
	})
})

var _ = Describe("scoreMassiveRecord", func() {
	DescribeTable("ranks records by classification completeness",
		func(rec massiveStock, want int) {
			Expect(scoreMassiveRecord(rec)).To(Equal(want))
		},
		Entry("type and CIK present is the complete score (3)",
			massiveStock{Type: "CS", CIK: "0000320193"}, massiveRecordCompleteScore),
		Entry("type only is worth 2",
			massiveStock{Type: "CS"}, 2),
		Entry("CIK only is worth 1",
			massiveStock{CIK: "0000320193"}, 1),
		Entry("neither type nor CIK is worth 0 (the ABCW lifecycle-start shape)",
			massiveStock{Name: "ANCHOR BANCORP WISC INC"}, 0),
		Entry("whitespace-only type is treated as empty",
			massiveStock{Type: "  ", CIK: "0000320193"}, 1),
		Entry("whitespace-only CIK is treated as empty",
			massiveStock{Type: "CS", CIK: "  "}, 2),
	)
})

var _ = Describe("mintBuilderSynthetic", func() {
	start := d("2003-09-10")

	It("returns the CIK+lifecycle-keyed synthetic when CIK is non-empty", func() {
		got := mintBuilderSynthetic("0000001234", "FOO", "Foo Inc", start)
		Expect(got).To(Equal(figi.GenerateSyntheticFIGIFromCIKLifecycle("0000001234", "FOO", "2003-09-10")))
	})

	It("falls back to (ticker, name, lifecycle-start) when CIK is empty but ticker and name are set", func() {
		got := mintBuilderSynthetic("", "FOO", "Foo Inc", start)
		Expect(got).To(Equal(figi.GenerateSyntheticFIGILifecycle("FOO", "Foo Inc", "2003-09-10")))
	})

	It("returns empty string when neither CIK nor name is available", func() {
		Expect(mintBuilderSynthetic("", "FOO", "", start)).To(BeEmpty())
		Expect(mintBuilderSynthetic("", "", "Foo Inc", start)).To(BeEmpty())
	})

	It("treats whitespace-only inputs as empty", func() {
		Expect(mintBuilderSynthetic("   ", "FOO", "  ", start)).To(BeEmpty())
	})

	It("produces distinct FIGIs for adjacent lifecycles of the same entity", func() {
		// The PILL / ProxyMed scenario: two same-(CIK, ticker, name)
		// lifecycles separated by a real trading gap must get different
		// synthetic FIGIs so they survive the (ticker, composite_figi)
		// upsert without one overwriting the other.
		first := mintBuilderSynthetic("0000906337", "PILL", "PROXYMED INC NEW", d("2003-09-10"))
		second := mintBuilderSynthetic("0000906337", "PILL", "PROXYMED INC NEW", d("2004-12-22"))
		Expect(first).NotTo(BeEmpty())
		Expect(second).NotTo(BeEmpty())
		Expect(first).NotTo(Equal(second))
	})
})

var _ = Describe("EODArchive.AllTickers", func() {
	It("returns tickers in sorted order", func() {
		// absorbFile reads parquet from disk; bypass it here by writing
		// directly into the unexported map so the test stays hermetic.
		archive := newEmptyArchive()
		archive.tickers["ZZZ"] = []dateRange{{Start: d("2020-01-01"), End: d("2020-02-01")}}
		archive.tickers["AAA"] = []dateRange{{Start: d("2020-01-01"), End: d("2020-02-01")}}
		archive.tickers["MMM"] = []dateRange{{Start: d("2020-01-01"), End: d("2020-02-01")}}

		Expect(archive.AllTickers()).To(Equal([]string{"AAA", "MMM", "ZZZ"}))
	})

	It("returns nil for a nil receiver and an empty archive returns an empty slice", func() {
		var nilArchive *EODArchive
		Expect(nilArchive.AllTickers()).To(BeNil())

		empty := newEmptyArchive()
		Expect(empty.AllTickers()).To(BeEmpty())
	})
})
