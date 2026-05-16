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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/data"
)

var _ = Describe("AssetHistory", func() {
	d := func(year, month, day int) time.Time {
		return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	}

	Describe("FIGIAt", func() {
		It("returns false for an unknown ticker", func() {
			h := data.NewAssetHistory(nil)
			_, ok := h.FIGIAt("ZZZZ", d(2024, 1, 1))
			Expect(ok).To(BeFalse())
		})

		It("returns the figi for a still-active ticker (delisted=zero)", func() {
			assets := []*data.Asset{{
				Ticker:        "AAPL",
				CompositeFigi: "BBG000B9XRY4",
				ListingDate:   "1980-12-12",
			}}
			h := data.NewAssetHistory(assets)

			figi, ok := h.FIGIAt("AAPL", d(2024, 6, 1))
			Expect(ok).To(BeTrue())
			Expect(figi).To(Equal("BBG000B9XRY4"))
		})

		It("returns the figi for a delisted ticker if the date falls in its window", func() {
			assets := []*data.Asset{{
				Ticker:        "XYZ",
				CompositeFigi: "BBG000XYZXYZ",
				ListingDate:   "2003-01-01",
				DelistingDate: "2010-06-30",
			}}
			h := data.NewAssetHistory(assets)

			figi, ok := h.FIGIAt("XYZ", d(2008, 5, 15))
			Expect(ok).To(BeTrue())
			Expect(figi).To(Equal("BBG000XYZXYZ"))
		})

		It("returns false when the date is before the listed date", func() {
			assets := []*data.Asset{{
				Ticker:        "AAPL",
				CompositeFigi: "BBG000B9XRY4",
				ListingDate:   "1980-12-12",
			}}
			h := data.NewAssetHistory(assets)

			_, ok := h.FIGIAt("AAPL", d(1979, 1, 1))
			Expect(ok).To(BeFalse())
		})

		It("returns false when the date is after the delisted date", func() {
			assets := []*data.Asset{{
				Ticker:        "XYZ",
				CompositeFigi: "BBG000XYZXYZ",
				ListingDate:   "2003-01-01",
				DelistingDate: "2010-06-30",
			}}
			h := data.NewAssetHistory(assets)

			_, ok := h.FIGIAt("XYZ", d(2011, 1, 1))
			Expect(ok).To(BeFalse())
		})

		It("treats unparseable listing date as far past (always listed)", func() {
			assets := []*data.Asset{{
				Ticker:        "UNKLST",
				CompositeFigi: "BBG000UNKNOWN",
				ListingDate:   "garbage",
				DelistingDate: "2010-06-30",
			}}
			h := data.NewAssetHistory(assets)

			figi, ok := h.FIGIAt("UNKLST", d(1900, 1, 1))
			Expect(ok).To(BeTrue())
			Expect(figi).To(Equal("BBG000UNKNOWN"))
		})

		It("returns the right figi when a ticker is reused across non-overlapping windows", func() {
			assets := []*data.Asset{
				{Ticker: "ABC", CompositeFigi: "BBG000ABC1OLD", ListingDate: "1995-01-01", DelistingDate: "2003-12-31"},
				{Ticker: "ABC", CompositeFigi: "BBG000ABC2NEW", ListingDate: "2015-06-01"},
			}
			h := data.NewAssetHistory(assets)

			figi, ok := h.FIGIAt("ABC", d(2000, 6, 1))
			Expect(ok).To(BeTrue())
			Expect(figi).To(Equal("BBG000ABC1OLD"))

			figi, ok = h.FIGIAt("ABC", d(2020, 6, 1))
			Expect(ok).To(BeTrue())
			Expect(figi).To(Equal("BBG000ABC2NEW"))

			_, ok = h.FIGIAt("ABC", d(2010, 1, 1))
			Expect(ok).To(BeFalse())
		})

		It("includes the delisted day itself (boundary inclusive on the upper end)", func() {
			assets := []*data.Asset{{
				Ticker:        "XYZ",
				CompositeFigi: "BBG000XYZXYZ",
				ListingDate:   "2003-01-01",
				DelistingDate: "2010-06-30",
			}}
			h := data.NewAssetHistory(assets)

			_, ok := h.FIGIAt("XYZ", d(2010, 6, 30))
			Expect(ok).To(BeTrue())
		})

		It("includes the listed day itself (boundary inclusive on the lower end)", func() {
			assets := []*data.Asset{{
				Ticker:        "XYZ",
				CompositeFigi: "BBG000XYZXYZ",
				ListingDate:   "2003-01-01",
				DelistingDate: "2010-06-30",
			}}
			h := data.NewAssetHistory(assets)

			_, ok := h.FIGIAt("XYZ", d(2003, 1, 1))
			Expect(ok).To(BeTrue())
		})

		It("normalizes the lookup ticker to uppercase", func() {
			assets := []*data.Asset{{Ticker: "aapl", CompositeFigi: "BBG000B9XRY4"}}
			h := data.NewAssetHistory(assets)

			_, ok := h.FIGIAt("AAPL", d(2024, 6, 1))
			Expect(ok).To(BeTrue())

			_, ok = h.FIGIAt("aapl", d(2024, 6, 1))
			Expect(ok).To(BeTrue())
		})
	})

	Describe("AssetAt", func() {
		It("returns the matching Asset record, not just the FIGI", func() {
			a := &data.Asset{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", Name: "Apple Inc."}
			h := data.NewAssetHistory([]*data.Asset{a})

			got, ok := h.AssetAt("AAPL", d(2024, 6, 1))
			Expect(ok).To(BeTrue())
			Expect(got).To(BeIdenticalTo(a))
		})

		It("prefers the earliest-delisted window when two windows cover the same date", func() {
			// Real-world case: Blockbuster's BBI tenancy ended 2010-07-07.
			// A later entity (Brickell Biotech) has an erroneously-broad
			// listing date that overlaps Blockbuster's window. The
			// resolver must pick Blockbuster for any date covered by
			// both regardless of insertion order.
			older := &data.Asset{Ticker: "BBI", CompositeFigi: "PV-OLD", ListingDate: "1993-03-16", DelistingDate: "2010-07-07"}
			newer := &data.Asset{Ticker: "BBI", CompositeFigi: "PV-NEW", ListingDate: "1993-03-16", DelistingDate: "2022-09-08"}

			// Order A: older first
			hA := data.NewAssetHistory([]*data.Asset{older, newer})
			a, ok := hA.AssetAt("BBI", d(2010, 6, 1))
			Expect(ok).To(BeTrue())
			Expect(a).To(BeIdenticalTo(older))

			// Order B: newer first — same result, proving order independence
			hB := data.NewAssetHistory([]*data.Asset{newer, older})
			a, ok = hB.AssetAt("BBI", d(2010, 6, 1))
			Expect(ok).To(BeTrue())
			Expect(a).To(BeIdenticalTo(older))
		})

		It("falls through to the still-active window only when no delisted window matches", func() {
			// Two records, both list at 2000-01-01. One delisted in
			// 2020, the other still active. For a 2015 date both match;
			// the finite-end (delisted) record wins. For 2024 only the
			// active record matches.
			active := &data.Asset{Ticker: "ABC", CompositeFigi: "PV-ACTIVE", ListingDate: "2000-01-01"}
			delisted := &data.Asset{Ticker: "ABC", CompositeFigi: "PV-OLD", ListingDate: "2000-01-01", DelistingDate: "2020-12-31"}
			h := data.NewAssetHistory([]*data.Asset{active, delisted})

			got, ok := h.AssetAt("ABC", d(2015, 6, 1))
			Expect(ok).To(BeTrue())
			Expect(got).To(BeIdenticalTo(delisted))

			got, ok = h.AssetAt("ABC", d(2024, 6, 1))
			Expect(ok).To(BeTrue())
			Expect(got).To(BeIdenticalTo(active))
		})
	})

	Describe("NewAssetHistory", func() {
		It("drops assets with empty composite_figi", func() {
			assets := []*data.Asset{
				{Ticker: "GOOD", CompositeFigi: "BBG000GOOD0000"},
				{Ticker: "BAD", CompositeFigi: ""},
			}
			h := data.NewAssetHistory(assets)
			Expect(h.TickerCount()).To(Equal(1))

			_, ok := h.FIGIAt("GOOD", d(2024, 6, 1))
			Expect(ok).To(BeTrue())

			_, ok = h.FIGIAt("BAD", d(2024, 6, 1))
			Expect(ok).To(BeFalse())
		})

		It("parses ISO-formatted listing/delisting dates", func() {
			assets := []*data.Asset{{
				Ticker:        "XYZ",
				CompositeFigi: "BBG000XYZXYZ",
				ListingDate:   "2003-01-01T00:00:00.000000Z",
				DelistingDate: "2010-06-30T00:00:00.000000Z",
			}}
			h := data.NewAssetHistory(assets)

			_, ok := h.FIGIAt("XYZ", d(2008, 5, 15))
			Expect(ok).To(BeTrue())

			_, ok = h.FIGIAt("XYZ", d(2011, 1, 1))
			Expect(ok).To(BeFalse())
		})

		It("treats unparseable dates as zero (open-ended)", func() {
			assets := []*data.Asset{{
				Ticker:        "XYZ",
				CompositeFigi: "BBG000XYZXYZ",
				ListingDate:   "garbage",
			}}
			h := data.NewAssetHistory(assets)

			_, ok := h.FIGIAt("XYZ", d(1900, 1, 1))
			Expect(ok).To(BeTrue())
		})
	})

	Describe("WindowsFor", func() {
		It("returns every known window for a ticker in insertion order", func() {
			a1 := &data.Asset{Ticker: "ABC", CompositeFigi: "BBG1", ListingDate: "1995-01-01", DelistingDate: "2003-12-31"}
			a2 := &data.Asset{Ticker: "ABC", CompositeFigi: "BBG2", ListingDate: "2015-06-01"}
			h := data.NewAssetHistory([]*data.Asset{a1, a2})

			Expect(h.WindowsFor("ABC")).To(Equal([]*data.Asset{a1, a2}))
		})

		It("returns nil for an unknown ticker", func() {
			h := data.NewAssetHistory(nil)
			Expect(h.WindowsFor("ZZZ")).To(BeEmpty())
		})
	})
})
