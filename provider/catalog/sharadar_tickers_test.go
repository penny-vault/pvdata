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
	"os"
	"path/filepath"
	"strings"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/klauspost/compress/zstd"

	"github.com/penny-vault/pvdata/data"
)

var _ = Describe("splitSharadarSuffix", func() {
	It("returns the bare ticker unchanged when there is no trailing digit", func() {
		base, suffix := splitSharadarSuffix("AAC")
		Expect(base).To(Equal("AAC"))
		Expect(suffix).To(Equal(0))
	})

	It("splits a trailing single-digit suffix", func() {
		base, suffix := splitSharadarSuffix("ABI1")
		Expect(base).To(Equal("ABI"))
		Expect(suffix).To(Equal(1))
	})

	It("splits a trailing multi-digit suffix", func() {
		base, suffix := splitSharadarSuffix("FOO42")
		Expect(base).To(Equal("FOO"))
		Expect(suffix).To(Equal(42))
	})

	It("treats an all-digit input as a bare ticker (no base, no split)", func() {
		base, suffix := splitSharadarSuffix("12345")
		Expect(base).To(Equal("12345"))
		Expect(suffix).To(Equal(0))
	})

	It("returns ('', 0) for an empty string", func() {
		base, suffix := splitSharadarSuffix("")
		Expect(base).To(Equal(""))
		Expect(suffix).To(Equal(0))
	})
})

var _ = Describe("sharadarNormalizeTicker", func() {
	It("maps Sharadar's class-share separator . to pvdata's /", func() {
		Expect(sharadarNormalizeTicker("BRK.B")).To(Equal("BRK/B"))
		Expect(sharadarNormalizeTicker("BF.A")).To(Equal("BF/A"))
	})

	It("leaves bare tickers alone", func() {
		Expect(sharadarNormalizeTicker("AAPL")).To(Equal("AAPL"))
	})

	It("trims surrounding whitespace", func() {
		Expect(sharadarNormalizeTicker("  AAPL  ")).To(Equal("AAPL"))
	})
})

var _ = Describe("extractSharadarCIK", func() {
	It("pulls the CIK from a Sharadar SEC filings URL and strips leading zeros", func() {
		url := "https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&CIK=0000320193"
		Expect(extractSharadarCIK(url)).To(Equal("320193"))
	})

	It("returns an empty string when the URL has no CIK parameter", func() {
		Expect(extractSharadarCIK("https://example.com/")).To(Equal(""))
	})

	It("returns an empty string for an empty input", func() {
		Expect(extractSharadarCIK("")).To(Equal(""))
	})
})

var _ = Describe("splitSpaceSeparated", func() {
	It("splits a space-separated list", func() {
		Expect(splitSpaceSeparated("037833100 037833200")).To(Equal([]string{"037833100", "037833200"}))
	})

	It("returns nil for empty input", func() {
		Expect(splitSpaceSeparated("")).To(BeNil())
		Expect(splitSpaceSeparated("   ")).To(BeNil())
	})
})

var _ = Describe("buildSharadarIndex", func() {
	It("groups records by base ticker and sorts by suffix ascending", func() {
		idx := buildSharadarIndex([]sharadarRecord{
			{Ticker: "ABI2", BaseTicker: "ABI", Suffix: 2, LastUpdated: d("2023-01-01")},
			{Ticker: "ABI1", BaseTicker: "ABI", Suffix: 1, LastUpdated: d("2023-01-01")},
			{Ticker: "AAC", BaseTicker: "AAC", Suffix: 0, LastUpdated: d("2023-01-01")},
		})

		Expect(idx.byBase["ABI"]).To(HaveLen(2))
		Expect(idx.byBase["ABI"][0].Suffix).To(Equal(1))
		Expect(idx.byBase["ABI"][1].Suffix).To(Equal(2))
		Expect(idx.byBase["AAC"]).To(HaveLen(1))
	})

	It("collapses duplicate (base, suffix) rows keeping the most recently updated", func() {
		idx := buildSharadarIndex([]sharadarRecord{
			{Ticker: "A", BaseTicker: "A", Suffix: 0, Name: "Old", LastUpdated: d("2022-01-01")},
			{Ticker: "A", BaseTicker: "A", Suffix: 0, Name: "New", LastUpdated: d("2024-01-01")},
		})

		Expect(idx.byBase["A"]).To(HaveLen(1))
		Expect(idx.byBase["A"][0].Name).To(Equal("New"))
	})
})

var _ = Describe("(*sharadarTickerIndex).lookupByLifecycle", func() {
	build := func(records ...sharadarRecord) *sharadarTickerIndex {
		return buildSharadarIndex(records)
	}

	It("returns nil for an unknown ticker", func() {
		idx := build()
		Expect(idx.lookupByLifecycle("AAPL", dateRange{Start: d("2020-01-01"), End: d("2021-01-01")})).To(BeNil())
	})

	It("picks the suffixed record whose price-date window best matches the lifecycle", func() {
		idx := build(
			sharadarRecord{Ticker: "ABI1", BaseTicker: "ABI", Suffix: 1, FirstPriceDate: d("1986-01-01"), LastPriceDate: d("2008-11-21"), Name: "Applied Biosystems"},
			sharadarRecord{Ticker: "ABI2", BaseTicker: "ABI", Suffix: 2, FirstPriceDate: d("1986-01-01"), LastPriceDate: d("1999-08-18"), Name: "American Bankers"},
		)

		rec := idx.lookupByLifecycle("ABI", dateRange{Start: d("2003-01-01"), End: d("2008-09-30")})
		Expect(rec).NotTo(BeNil())
		Expect(rec.Name).To(Equal("Applied Biosystems"))

		rec = idx.lookupByLifecycle("ABI", dateRange{Start: d("1995-01-01"), End: d("1999-08-18")})
		Expect(rec).NotTo(BeNil())
		Expect(rec.Name).To(Equal("American Bankers"))
	})

	It("returns nil when the lifecycle does not overlap any record", func() {
		idx := build(
			sharadarRecord{Ticker: "AAC1", BaseTicker: "AAC", Suffix: 1, FirstPriceDate: d("1992-01-30"), LastPriceDate: d("2000-04-03")},
		)

		Expect(idx.lookupByLifecycle("AAC", dateRange{Start: d("2010-01-01"), End: d("2015-01-01")})).To(BeNil())
	})

	It("treats a zero LastPriceDate as open-ended (still trading)", func() {
		idx := build(
			sharadarRecord{Ticker: "AAPL", BaseTicker: "AAPL", FirstPriceDate: d("1980-12-12")},
		)

		rec := idx.lookupByLifecycle("AAPL", dateRange{Start: d("2025-01-01"), End: d("2026-01-01")})
		Expect(rec).NotTo(BeNil())
		Expect(rec.Ticker).To(Equal("AAPL"))
	})
})

var _ = Describe("applySharadarEnrichment", func() {
	rec := &sharadarRecord{
		PermaTicker:    "196290",
		CUSIPs:         []string{"00846U101"},
		CIK:            "1090872",
		SICCode:        3826,
		Sector:         "Healthcare",
		Industry:       "Diagnostics & Research",
		SimilarTickers: []string{"AGN"},
	}

	It("fills empty CUSIP, Sector, Industry, SIC, and CIK", func() {
		asset := &data.Asset{Ticker: "A"}
		applySharadarEnrichment(context.Background(), asset, rec)
		Expect(asset.CUSIP).To(Equal([]string{"00846U101"}))
		Expect(asset.Sector).To(Equal("Healthcare"))
		Expect(asset.Industry).To(Equal("Diagnostics & Research"))
		Expect(asset.SIC).NotTo(BeNil())
		Expect(*asset.SIC).To(Equal(3826))
		Expect(asset.CIK).To(Equal("1090872"))
		Expect(asset.OtherIdentifiers).To(HaveKeyWithValue("sharadar", "196290"))
	})

	It("does not override a non-empty CIK", func() {
		asset := &data.Asset{Ticker: "A", CIK: "0000077551"}
		applySharadarEnrichment(context.Background(), asset, rec)
		Expect(asset.CIK).To(Equal("0000077551"))
	})

	It("does not override a non-empty Sector", func() {
		asset := &data.Asset{Ticker: "A", Sector: "Technology"}
		applySharadarEnrichment(context.Background(), asset, rec)
		Expect(asset.Sector).To(Equal("Technology"))
	})

	It("does not override a non-zero SIC", func() {
		existing := 1234
		asset := &data.Asset{Ticker: "A", SIC: &existing}
		applySharadarEnrichment(context.Background(), asset, rec)
		Expect(*asset.SIC).To(Equal(1234))
	})

	It("does not override an existing CUSIP list", func() {
		asset := &data.Asset{Ticker: "A", CUSIP: []string{"EXISTING"}}
		applySharadarEnrichment(context.Background(), asset, rec)
		Expect(asset.CUSIP).To(Equal([]string{"EXISTING"}))
	})

	It("is a no-op when the record is nil", func() {
		asset := &data.Asset{Ticker: "A"}
		applySharadarEnrichment(context.Background(), asset, nil)
		Expect(asset.CUSIP).To(BeNil())
		Expect(asset.Sector).To(Equal(""))
	})
})

var _ = Describe("parseSharadarTickersCSV", func() {
	const header = "table,permaticker,ticker,name,exchange,isdelisted,category,cusips,siccode,sicsector,sicindustry,famasector,famaindustry,sector,industry,scalemarketcap,scalerevenue,relatedtickers,currency,location,lastupdated,firstadded,firstpricedate,lastpricedate,firstquarter,lastquarter,secfilings,companysite"

	It("parses one row and populates the documented fields", func() {
		body := header + "\n" +
			`SF1,196290,A,AGILENT TECHNOLOGIES INC,NYSE,N,Domestic Common Stock,00846U101,3826,Manufacturing,Laboratory Analytical Instruments,,Measuring and Control Equipment,Healthcare,Diagnostics & Research,5 - Large,5 - Large,,USD,California; U.S.A,2023-12-20,2014-09-26,1999-11-18,2023-12-28,1997-06-30,2023-09-30,https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&CIK=0001090872,https://www.agilent.com` + "\n"

		records, err := parseSharadarTickersCSV(strings.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		Expect(records).To(HaveLen(1))

		r := records[0]
		Expect(r.Ticker).To(Equal("A"))
		Expect(r.BaseTicker).To(Equal("A"))
		Expect(r.Suffix).To(Equal(0))
		Expect(r.PermaTicker).To(Equal("196290"))
		Expect(r.Name).To(Equal("AGILENT TECHNOLOGIES INC"))
		Expect(r.CUSIPs).To(Equal([]string{"00846U101"}))
		Expect(r.CIK).To(Equal("1090872"))
		Expect(r.SICCode).To(Equal(3826))
		Expect(r.Sector).To(Equal("Healthcare"))
		Expect(r.Industry).To(Equal("Diagnostics & Research"))
		Expect(r.IsDelisted).To(BeFalse())
		Expect(r.FirstPriceDate).To(Equal(d("1999-11-18")))
		Expect(r.LastPriceDate).To(Equal(d("2023-12-28")))
	})

	It("normalizes a Sharadar class-share ticker and its related tickers", func() {
		body := header + "\n" +
			`SF1,1,BRK.B,Berkshire Hathaway Inc,NYSE,N,Domestic Common Stock,,,,,,,,,,,BRK.A,USD,,2024-01-01,1996-05-09,1996-05-09,2024-01-01,,,https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&CIK=0001067983,https://www.berkshirehathaway.com` + "\n"

		records, err := parseSharadarTickersCSV(strings.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		Expect(records).To(HaveLen(1))
		Expect(records[0].Ticker).To(Equal("BRK/B"))
		Expect(records[0].BaseTicker).To(Equal("BRK/B"))
		Expect(records[0].SimilarTickers).To(Equal([]string{"BRK/A"}))
	})

	It("splits a suffixed Sharadar ticker into base and suffix", func() {
		body := header + "\n" +
			`SF1,173408,ABI1,APPLIED BIOSYSTEMS INC,NYSE,Y,Domestic Common Stock,038149100,3826,Manufacturing,Laboratory Analytical Instruments,,Measuring and Control Equipment,Healthcare,Diagnostics & Research,5 - Large,4 - Mid,,USD,Connecticut; U.S.A,2018-12-17,2015-07-18,1986-01-01,2008-11-21,,,https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&CIK=0000077551,` + "\n"

		records, err := parseSharadarTickersCSV(strings.NewReader(body))
		Expect(err).NotTo(HaveOccurred())
		Expect(records).To(HaveLen(1))
		Expect(records[0].Ticker).To(Equal("ABI1"))
		Expect(records[0].BaseTicker).To(Equal("ABI"))
		Expect(records[0].Suffix).To(Equal(1))
		Expect(records[0].IsDelisted).To(BeTrue())
	})
})

var _ = Describe("loadSharadarTickerIndex", func() {
	const header = "table,permaticker,ticker,name,exchange,isdelisted,category,cusips,siccode,sicsector,sicindustry,famasector,famaindustry,sector,industry,scalemarketcap,scalerevenue,relatedtickers,currency,location,lastupdated,firstadded,firstpricedate,lastpricedate,firstquarter,lastquarter,secfilings,companysite"

	writeZstd := func(dir, name, body string) string {
		path := filepath.Join(dir, name)

		f, err := os.Create(path)
		Expect(err).NotTo(HaveOccurred())

		defer f.Close()

		w, err := zstd.NewWriter(f)
		Expect(err).NotTo(HaveOccurred())

		_, err = w.Write([]byte(body))
		Expect(err).NotTo(HaveOccurred())
		Expect(w.Close()).To(Succeed())

		return path
	}

	It("returns (nil, nil) when the directory config is empty", func() {
		idx, err := loadSharadarTickerIndex(context.Background(), "")
		Expect(err).NotTo(HaveOccurred())
		Expect(idx).To(BeNil())
	})

	It("returns (nil, nil) when the directory is missing", func() {
		idx, err := loadSharadarTickerIndex(context.Background(), filepath.Join(GinkgoT().TempDir(), "does-not-exist"))
		Expect(err).NotTo(HaveOccurred())
		Expect(idx).To(BeNil())
	})

	It("returns (nil, nil) when no *TICKERS*.zst file is found in the directory", func() {
		dir := GinkgoT().TempDir()
		Expect(os.WriteFile(filepath.Join(dir, "unrelated.txt"), []byte("noise"), 0o644)).To(Succeed())

		idx, err := loadSharadarTickerIndex(context.Background(), dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(idx).To(BeNil())
	})

	It("loads the lexically latest *TICKERS*.zst file and parses it", func() {
		dir := GinkgoT().TempDir()

		_ = writeZstd(dir, "SHARADAR_TICKERS_2024.csv.zst", header+"\n"+
			`SF1,1,OLD,Old Co,NYSE,Y,Domestic Common Stock,,,,,,,,,,,,,2024-01-01,,1990-01-01,1995-01-01,,,,`+"\n")

		_ = writeZstd(dir, "SHARADAR_TICKERS_2025.csv.zst", header+"\n"+
			`SF1,2,NEW,New Co,NYSE,N,Domestic Common Stock,,,,,,,,,,,,,2025-01-01,,2000-01-01,,,,,`+"\n")

		idx, err := loadSharadarTickerIndex(context.Background(), dir)
		Expect(err).NotTo(HaveOccurred())
		Expect(idx).NotTo(BeNil())

		Expect(idx.byBase).To(HaveKey("NEW"))
		Expect(idx.byBase).NotTo(HaveKey("OLD"))
	})
})

var _ = Describe("sharadarListDateUsable", func() {
	rngStart := d("2010-06-15")

	It("rejects a zero date", func() {
		Expect(sharadarListDateUsable(time.Time{}, rngStart)).To(BeFalse())
	})

	It("rejects a date on or after the lifecycle start", func() {
		Expect(sharadarListDateUsable(d("2010-06-15"), rngStart)).To(BeFalse())
		Expect(sharadarListDateUsable(d("2011-01-01"), rngStart)).To(BeFalse())
	})

	It("rejects the 1986-01-01 Sharadar data-start sentinel", func() {
		Expect(sharadarListDateUsable(d("1986-01-01"), rngStart)).To(BeFalse())
	})

	It("accepts a real date before the lifecycle start", func() {
		Expect(sharadarListDateUsable(d("2008-04-22"), rngStart)).To(BeTrue())
	})
})

var _ = Describe("AssetBuilder.finalize Sharadar listing-date backstop", func() {
	It("uses Sharadar FirstPriceDate when at the EOD left edge and Massive list_date is empty", func() {
		archive := newEmptyArchive()
		archive.tickers["FOO"] = []dateRange{{Start: d("2010-01-04"), End: d("2018-09-20")}}
		archive.coverageStart = d("2010-01-04")
		archive.coverageEnd = d("2018-09-20")

		idx := buildSharadarIndex([]sharadarRecord{
			{
				Ticker:         "FOO",
				BaseTicker:     "FOO",
				FirstPriceDate: d("2005-03-15"),
				LastPriceDate:  d("2018-09-20"),
			},
		})

		b := &AssetBuilder{
			api:      &massiveAssetFetcher{},
			archive:  archive,
			tracked:  map[string]struct{}{"CS": {}, "ADRC": {}, "ETF": {}, "CEF": {}, "ETN": {}, "UNK": {}},
			todayIDs: nil,
			sharadar: idx,
		}

		p := &proposedAsset{
			ticker: "FOO",
			rng:    dateRange{Start: d("2010-01-04"), End: d("2018-09-20")},
			isLast: true,
			record: massiveStock{Ticker: "FOO", Name: "Foo", CompositeFIGI: "BBG000000001", Type: "CS"},
		}

		asset := b.finalize(context.Background(), p, nil)
		Expect(asset).NotTo(BeNil())
		Expect(asset.ListingDate).To(Equal("2005-03-15"))
	})

	It("prefers Massive list_date over Sharadar when both are present and usable", func() {
		archive := newEmptyArchive()
		archive.tickers["FOO"] = []dateRange{{Start: d("2010-01-04"), End: d("2018-09-20")}}
		archive.coverageStart = d("2010-01-04")
		archive.coverageEnd = d("2018-09-20")

		idx := buildSharadarIndex([]sharadarRecord{
			{
				Ticker:         "FOO",
				BaseTicker:     "FOO",
				FirstPriceDate: d("2005-03-15"),
				LastPriceDate:  d("2018-09-20"),
			},
		})

		b := &AssetBuilder{
			api:      &massiveAssetFetcher{},
			archive:  archive,
			tracked:  map[string]struct{}{"CS": {}, "ADRC": {}, "ETF": {}, "CEF": {}, "ETN": {}, "UNK": {}},
			todayIDs: nil,
			sharadar: idx,
		}

		p := &proposedAsset{
			ticker: "FOO",
			rng:    dateRange{Start: d("2010-01-04"), End: d("2018-09-20")},
			isLast: true,
			record: massiveStock{Ticker: "FOO", Name: "Foo", CompositeFIGI: "BBG000000001", Type: "CS", ListDate: "2007-09-12"},
		}

		asset := b.finalize(context.Background(), p, nil)
		Expect(asset).NotTo(BeNil())
		Expect(asset.ListingDate).To(Equal("2007-09-12"))
	})

	It("does not override the EOD range start when the lifecycle is not at the left edge of coverage", func() {
		archive := newEmptyArchive()
		archive.tickers["FOO"] = []dateRange{{Start: d("2010-01-04"), End: d("2018-09-20")}}
		archive.coverageStart = d("2003-09-10")
		archive.coverageEnd = d("2018-09-20")

		idx := buildSharadarIndex([]sharadarRecord{
			{
				Ticker:         "FOO",
				BaseTicker:     "FOO",
				FirstPriceDate: d("2005-03-15"),
				LastPriceDate:  d("2018-09-20"),
			},
		})

		b := &AssetBuilder{
			api:      &massiveAssetFetcher{},
			archive:  archive,
			tracked:  map[string]struct{}{"CS": {}, "ADRC": {}, "ETF": {}, "CEF": {}, "ETN": {}, "UNK": {}},
			todayIDs: nil,
			sharadar: idx,
		}

		p := &proposedAsset{
			ticker: "FOO",
			rng:    dateRange{Start: d("2010-01-04"), End: d("2018-09-20")},
			isLast: true,
			record: massiveStock{Ticker: "FOO", Name: "Foo", CompositeFIGI: "BBG000000001", Type: "CS"},
		}

		asset := b.finalize(context.Background(), p, nil)
		Expect(asset).NotTo(BeNil())
		Expect(asset.ListingDate).To(Equal("2010-01-04"))
	})
})

var _ = Describe("AssetBuilder.finalize with Sharadar enrichment", func() {
	It("fills CUSIP and Sector from a date-overlapping Sharadar record", func() {
		idx := buildSharadarIndex([]sharadarRecord{
			{
				Ticker:         "FOO",
				BaseTicker:     "FOO",
				Suffix:         0,
				CUSIPs:         []string{"123456789"},
				Sector:         "Technology",
				Industry:       "Software",
				FirstPriceDate: d("2010-01-01"),
				LastPriceDate:  d("2018-12-31"),
			},
		})

		b := &AssetBuilder{
			api:      &massiveAssetFetcher{},
			archive:  newEmptyArchive(),
			tracked:  map[string]struct{}{"CS": {}, "ADRC": {}, "ETF": {}, "CEF": {}, "ETN": {}, "UNK": {}},
			todayIDs: nil,
			sharadar: idx,
		}

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
		Expect(asset.CUSIP).To(Equal([]string{"123456789"}))
		Expect(asset.Sector).To(Equal("Technology"))
		Expect(asset.Industry).To(Equal("Software"))
	})

	It("does nothing when the Sharadar index has no overlapping record", func() {
		idx := buildSharadarIndex([]sharadarRecord{
			{
				Ticker:         "FOO1",
				BaseTicker:     "FOO",
				Suffix:         1,
				CUSIPs:         []string{"123456789"},
				FirstPriceDate: d("1990-01-01"),
				LastPriceDate:  d("1995-12-31"),
			},
		})

		b := &AssetBuilder{
			api:      &massiveAssetFetcher{},
			archive:  newEmptyArchive(),
			tracked:  map[string]struct{}{"CS": {}, "ADRC": {}, "ETF": {}, "CEF": {}, "ETN": {}, "UNK": {}},
			todayIDs: nil,
			sharadar: idx,
		}

		p := &proposedAsset{
			ticker: "FOO",
			rng:    dateRange{Start: d("2015-04-10"), End: d("2018-09-20")},
			isLast: true,
			record: massiveStock{Ticker: "FOO", Name: "Foo Industries", CompositeFIGI: "BBG000000001", Type: "CS"},
		}

		asset := b.finalize(context.Background(), p, nil)
		Expect(asset).NotTo(BeNil())
		Expect(asset.CUSIP).To(BeEmpty())
		Expect(asset.Sector).To(Equal(""))
	})
})
