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
package data_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/data"
)

func mustParse(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}

	return t
}

type recordingFiler struct {
	urls map[string]string
}

func (r *recordingFiler) CreateFile(name string, _ []byte) (string, error) {
	url := "https://example.test/" + name
	if r.urls == nil {
		r.urls = map[string]string{}
	}
	r.urls[name] = url

	return url, nil
}

var _ = Describe("Asset.SaveFiles", func() {
	It("populates IconUrl with the returned URL", func() {
		asset := &data.Asset{
			CompositeFigi: "BBG000FOO",
			Icon:          []byte{1, 2, 3},
			IconMimeType:  "image/png",
		}

		filer := &recordingFiler{}
		Expect(asset.SaveFiles(context.Background(), filer)).To(Succeed())
		Expect(asset.IconUrl).To(Equal("https://example.test/BBG000FOO-icon.png"))
	})

	It("populates LogoUrl with the returned URL", func() {
		asset := &data.Asset{
			CompositeFigi: "BBG000FOO",
			Logo:          []byte{4, 5, 6},
			LogoMimeType:  "image/jpeg",
		}

		filer := &recordingFiler{}
		Expect(asset.SaveFiles(context.Background(), filer)).To(Succeed())
		Expect(asset.LogoUrl).To(Equal("https://example.test/BBG000FOO-logo.jpg"))
	})

	It("leaves URL unchanged when there is no payload", func() {
		asset := &data.Asset{
			CompositeFigi: "BBG000FOO",
			IconUrl:       "previous-url",
		}

		filer := &recordingFiler{}
		Expect(asset.SaveFiles(context.Background(), filer)).To(Succeed())
		Expect(asset.IconUrl).To(Equal("previous-url"))
	})
})

var _ = Describe("BuildAssetIndex / Lookup", func() {
	It("skips rows with empty ticker or empty composite_figi", func() {
		idx := data.BuildAssetIndex([]*data.Asset{
			{Ticker: "", CompositeFigi: "BBG000A1"},
			{Ticker: "AAPL", CompositeFigi: ""},
			{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4"},
		})
		Expect(idx.Len()).To(Equal(1))
	})

	It("looks up by CompositeFigi as the most-specific identifier", func() {
		idx := data.BuildAssetIndex([]*data.Asset{
			{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", CIK: "0000320193"},
		})
		match, ok := idx.Lookup(&data.Asset{CompositeFigi: "BBG000B9XRY4"})
		Expect(ok).To(BeTrue())
		Expect(match.Ticker).To(Equal("AAPL"))
	})

	It("looks up by ShareClassFigi when only that identifier is supplied", func() {
		idx := data.BuildAssetIndex([]*data.Asset{
			{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", ShareClassFigi: "BBG001S5N8V8"},
		})
		match, ok := idx.Lookup(&data.Asset{ShareClassFigi: "BBG001S5N8V8"})
		Expect(ok).To(BeTrue())
		Expect(match.Ticker).To(Equal("AAPL"))
	})

	It("looks up by InstrumentPermID", func() {
		idx := data.BuildAssetIndex([]*data.Asset{
			{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", InstrumentPermID: "1-8590932301"},
		})
		match, ok := idx.Lookup(&data.Asset{InstrumentPermID: "1-8590932301"})
		Expect(ok).To(BeTrue())
		Expect(match.CompositeFigi).To(Equal("BBG000B9XRY4"))
	})

	It("looks up by any matching CUSIP or ISIN on the incoming asset", func() {
		idx := data.BuildAssetIndex([]*data.Asset{
			{
				Ticker:        "POAGX",
				CompositeFigi: "BBG000QPWDZ3",
				CUSIP:         []string{"74160Q202"},
				ISIN:          []string{"US74160Q2021"},
			},
		})

		byCUSIP, ok := idx.Lookup(&data.Asset{CUSIP: []string{"74160Q202"}})
		Expect(ok).To(BeTrue())
		Expect(byCUSIP.Ticker).To(Equal("POAGX"))

		byISIN, ok := idx.Lookup(&data.Asset{ISIN: []string{"US74160Q2021"}})
		Expect(ok).To(BeTrue())
		Expect(byISIN.Ticker).To(Equal("POAGX"))
	})

	It("looks up by Ticker:CIK for the share-class disambiguation case", func() {
		idx := data.BuildAssetIndex([]*data.Asset{
			{Ticker: "BBI", CompositeFigi: "PVG_BLOCKBUSTER", CIK: "0001085734"},
			{Ticker: "BBI", CompositeFigi: "BBG_BRICKELL", CIK: "0000819050"},
		})

		blockbuster, ok := idx.Lookup(&data.Asset{Ticker: "BBI", CIK: "0001085734"})
		Expect(ok).To(BeTrue())
		Expect(blockbuster.CompositeFigi).To(Equal("PVG_BLOCKBUSTER"))

		brickell, ok := idx.Lookup(&data.Asset{Ticker: "BBI", CIK: "0000819050"})
		Expect(ok).To(BeTrue())
		Expect(brickell.CompositeFigi).To(Equal("BBG_BRICKELL"))
	})

	It("looks up by Ticker:OrganizationPermID", func() {
		idx := data.BuildAssetIndex([]*data.Asset{
			{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", OrganizationPermID: "1-4295905573"},
		})
		match, ok := idx.Lookup(&data.Asset{Ticker: "AAPL", OrganizationPermID: "1-4295905573"})
		Expect(ok).To(BeTrue())
		Expect(match.CompositeFigi).To(Equal("BBG000B9XRY4"))
	})

	It("looks up by ticker alone when both sides are active and the index has exactly one active row", func() {
		// Tiingo emits POAGX with only ticker + name + Active=true.
		// The DB has POAGX as a single active row. A ticker can be
		// assigned to at most one active entity at a time, so ticker
		// alone is unambiguous when both sides agree the asset is
		// currently active.
		idx := data.BuildAssetIndex([]*data.Asset{
			{Ticker: "POAGX", CompositeFigi: "BBG000QPWDZ3", Active: true},
		})
		match, ok := idx.Lookup(&data.Asset{Ticker: "POAGX", Active: true})
		Expect(ok).To(BeTrue())
		Expect(match.CompositeFigi).To(Equal("BBG000QPWDZ3"))
	})

	It("does not fire the ticker-alone path when the incoming observation is old (backfill case)", func() {
		// 2006-01-01 walk encounters a ticker that the index has as
		// currently active. The historical observation cannot prove
		// the entity is still that one today — the ticker may have
		// been reassigned. The ValidFor gate refuses the shortcut.
		idx := data.BuildAssetIndex([]*data.Asset{
			{Ticker: "BBI", CompositeFigi: "BBG_BRICKELL", Active: true, Name: "Brickell Biotech Inc"},
		})
		stale := &data.Asset{
			Ticker:   "BBI",
			Active:   true,
			Name:     "Blockbuster Inc",
			ValidFor: mustParse("2006-01-01T00:00:00Z"),
		}
		_, ok := idx.Lookup(stale)
		Expect(ok).To(BeFalse())
	})

	It("does not fire the ticker-alone path when the incoming asset is not currently active", func() {
		// Backfill scenario: incoming row represents an historical
		// observation of a since-delisted entity. Even if the DB
		// has a single active row for the same ticker (a different
		// entity that took it over after delisting), we must not
		// match — ticker-alone is safe only when both sides agree
		// "currently active".
		idx := data.BuildAssetIndex([]*data.Asset{
			{Ticker: "BBI", CompositeFigi: "BBG_BRICKELL", Active: true, Name: "Brickell Biotech Inc"},
		})
		_, ok := idx.Lookup(&data.Asset{
			Ticker: "BBI",
			Active: false, // historical observation of Blockbuster
			Name:   "Blockbuster Inc",
		})
		Expect(ok).To(BeFalse())
	})

	It("falls through ticker-alone lookup when no row for that ticker is active", func() {
		idx := data.BuildAssetIndex([]*data.Asset{
			{Ticker: "BBI", CompositeFigi: "PVG_BLOCKBUSTER", Active: false, Name: "Blockbuster Inc"},
			{Ticker: "BBI", CompositeFigi: "BBG_BRICKELL", Active: false, Name: "Brickell Biotech Inc"},
		})
		_, ok := idx.Lookup(&data.Asset{Ticker: "BBI", Active: true})
		Expect(ok).To(BeFalse())
	})

	It("falls through ticker-alone when multiple active rows share a ticker", func() {
		idx := data.BuildAssetIndex([]*data.Asset{
			{Ticker: "X", CompositeFigi: "BBG_A", Active: true},
			{Ticker: "X", CompositeFigi: "BBG_B", Active: true},
		})
		_, ok := idx.Lookup(&data.Asset{Ticker: "X", Active: true})
		Expect(ok).To(BeFalse())
	})

	It("looks up by Ticker+Name when only those are supplied (Tiingo mutual fund case)", func() {
		// Simulates Tiingo providing only ticker + name; DB has the
		// richer row (POAGX with CompositeFigi and CUSIP). The
		// fallback finds the DB row via name similarity.
		idx := data.BuildAssetIndex([]*data.Asset{
			{
				Ticker:        "POAGX",
				Name:          "PRIMECAP Odyssey Funds - PRIMECAP Odyssey Aggressive Growth Fund",
				CompositeFigi: "BBG000QPWDZ3",
				CUSIP:         []string{"74160Q202"},
			},
		})
		match, ok := idx.Lookup(&data.Asset{
			Ticker: "POAGX",
			Name:   "PRIMECAP Odyssey Aggressive Growth Fund",
		})
		Expect(ok).To(BeTrue())
		Expect(match.CompositeFigi).To(Equal("BBG000QPWDZ3"))
	})

	It("rejects Ticker+Name when the names are dissimilar (reuse guard)", func() {
		idx := data.BuildAssetIndex([]*data.Asset{
			{Ticker: "BBI", CompositeFigi: "PVG_BLOCKBUSTER", Name: "Blockbuster Inc"},
		})
		// An incoming Brickell Biotech with the same ticker but a
		// totally different name must NOT match the Blockbuster row.
		_, ok := idx.Lookup(&data.Asset{Ticker: "BBI", Name: "Brickell Biotech Inc"})
		Expect(ok).To(BeFalse())
	})

	It("refuses bare-ticker lookups (no name, no other identifiers)", func() {
		idx := data.BuildAssetIndex([]*data.Asset{
			{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", CIK: "0000320193"},
		})
		_, ok := idx.Lookup(&data.Asset{Ticker: "AAPL"})
		Expect(ok).To(BeFalse())
	})

	It("prefers active over inactive when the same key collides", func() {
		idx := data.BuildAssetIndex([]*data.Asset{
			{Ticker: "ACME", CompositeFigi: "BBG_OLD", CIK: "0001000001", Active: false},
			{Ticker: "ACME", CompositeFigi: "BBG_NEW", CIK: "0001000001", Active: true},
		})
		match, ok := idx.Lookup(&data.Asset{Ticker: "ACME", CIK: "0001000001"})
		Expect(ok).To(BeTrue())
		Expect(match.CompositeFigi).To(Equal("BBG_NEW"))
	})

	It("among equally-active rows, keeps the most recently updated", func() {
		older := &data.Asset{Ticker: "ACME", CompositeFigi: "BBG_OLD", CIK: "0001000001", Active: true}
		older.LastUpdated = mustParse("2024-01-01T00:00:00Z")
		newer := &data.Asset{Ticker: "ACME", CompositeFigi: "BBG_NEW", CIK: "0001000001", Active: true}
		newer.LastUpdated = mustParse("2025-06-15T00:00:00Z")
		idx := data.BuildAssetIndex([]*data.Asset{older, newer})
		match, ok := idx.Lookup(&data.Asset{Ticker: "ACME", CIK: "0001000001"})
		Expect(ok).To(BeTrue())
		Expect(match.CompositeFigi).To(Equal("BBG_NEW"))
	})
})

var _ = Describe("AssetIndex context helpers", func() {
	It("round-trips the index through WithAssetIndex / AssetIndexFromContext", func() {
		idx := data.BuildAssetIndex([]*data.Asset{
			{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", CIK: "0000320193"},
		})
		ctx := data.WithAssetIndex(context.Background(), idx)
		got := data.AssetIndexFromContext(ctx)

		match, ok := got.Lookup(&data.Asset{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4"})
		Expect(ok).To(BeTrue())
		Expect(match.CIK).To(Equal("0000320193"))
	})

	It("returns a zero-value index when no index is attached", func() {
		Expect(data.AssetIndexFromContext(context.Background()).IsZero()).To(BeTrue())
	})

	It("leaves the context unchanged when given a zero-value index", func() {
		orig := context.Background()
		ctx := data.WithAssetIndex(orig, data.AssetIndex{})
		Expect(ctx).To(Equal(orig))
		Expect(data.AssetIndexFromContext(ctx).IsZero()).To(BeTrue())
	})
})
