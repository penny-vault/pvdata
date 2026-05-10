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
	"github.com/penny-vault/pvdata/data"
)

var _ = Describe("historicalAssetUniverse", func() {
	d := func(year, month, day int) time.Time {
		return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	}

	Describe("figiAt", func() {
		It("returns false for an unknown ticker", func() {
			u := newHistoricalAssetUniverse()
			_, ok := u.figiAt("ZZZZ", d(2024, 1, 1))
			Expect(ok).To(BeFalse())
		})

		It("returns the figi for a still-active ticker (delisted=zero)", func() {
			u := newHistoricalAssetUniverse()
			u.add("AAPL", "BBG000B9XRY4", d(1980, 12, 12), time.Time{})

			figi, ok := u.figiAt("AAPL", d(2024, 6, 1))
			Expect(ok).To(BeTrue())
			Expect(figi).To(Equal("BBG000B9XRY4"))
		})

		It("returns the figi for a delisted ticker if the date falls in its window", func() {
			u := newHistoricalAssetUniverse()
			u.add("XYZ", "BBG000XYZXYZ", d(2003, 1, 1), d(2010, 6, 30))

			figi, ok := u.figiAt("XYZ", d(2008, 5, 15))
			Expect(ok).To(BeTrue())
			Expect(figi).To(Equal("BBG000XYZXYZ"))
		})

		It("returns false when the date is before the listed date", func() {
			u := newHistoricalAssetUniverse()
			u.add("AAPL", "BBG000B9XRY4", d(1980, 12, 12), time.Time{})

			_, ok := u.figiAt("AAPL", d(1979, 1, 1))
			Expect(ok).To(BeFalse())
		})

		It("returns false when the date is after the delisted date", func() {
			u := newHistoricalAssetUniverse()
			u.add("XYZ", "BBG000XYZXYZ", d(2003, 1, 1), d(2010, 6, 30))

			_, ok := u.figiAt("XYZ", d(2011, 1, 1))
			Expect(ok).To(BeFalse())
		})

		It("treats listed=zero as far past (always listed)", func() {
			u := newHistoricalAssetUniverse()
			u.add("UNKLST", "BBG000UNKNOWN", time.Time{}, d(2010, 6, 30))

			figi, ok := u.figiAt("UNKLST", d(1900, 1, 1))
			Expect(ok).To(BeTrue())
			Expect(figi).To(Equal("BBG000UNKNOWN"))
		})

		It("returns the right figi when a ticker is reused across non-overlapping windows", func() {
			u := newHistoricalAssetUniverse()
			u.add("ABC", "BBG000ABC1OLD", d(1995, 1, 1), d(2003, 12, 31)) // delisted in 2003
			u.add("ABC", "BBG000ABC2NEW", d(2015, 6, 1), time.Time{})     // reissued in 2015

			// 2000: matches the old window
			figi, ok := u.figiAt("ABC", d(2000, 6, 1))
			Expect(ok).To(BeTrue())
			Expect(figi).To(Equal("BBG000ABC1OLD"))

			// 2020: matches the new window
			figi, ok = u.figiAt("ABC", d(2020, 6, 1))
			Expect(ok).To(BeTrue())
			Expect(figi).To(Equal("BBG000ABC2NEW"))

			// 2010: in the gap → no match
			_, ok = u.figiAt("ABC", d(2010, 1, 1))
			Expect(ok).To(BeFalse())
		})

		It("includes the delisted day itself (boundary inclusive on the upper end)", func() {
			u := newHistoricalAssetUniverse()
			u.add("XYZ", "BBG000XYZXYZ", d(2003, 1, 1), d(2010, 6, 30))

			_, ok := u.figiAt("XYZ", d(2010, 6, 30))
			Expect(ok).To(BeTrue())
		})

		It("includes the listed day itself (boundary inclusive on the lower end)", func() {
			u := newHistoricalAssetUniverse()
			u.add("XYZ", "BBG000XYZXYZ", d(2003, 1, 1), d(2010, 6, 30))

			_, ok := u.figiAt("XYZ", d(2003, 1, 1))
			Expect(ok).To(BeTrue())
		})
	})

	Describe("buildHistoricalUniverse", func() {
		It("drops assets with empty composite_figi", func() {
			assets := []*data.Asset{
				{Ticker: "GOOD", CompositeFigi: "BBG000GOOD0000"},
				{Ticker: "BAD", CompositeFigi: ""},
			}
			u := buildHistoricalUniverse(assets, "", "")
			Expect(u.tickerCount()).To(Equal(1))

			_, ok := u.figiAt("GOOD", d(2024, 6, 1))
			Expect(ok).To(BeTrue())

			_, ok = u.figiAt("BAD", d(2024, 6, 1))
			Expect(ok).To(BeFalse())
		})

		It("applies the ticker filter case-insensitively", func() {
			assets := []*data.Asset{
				{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4"},
				{Ticker: "MSFT", CompositeFigi: "BBG000BPH459"},
			}
			u := buildHistoricalUniverse(assets, "aapl", "")
			Expect(u.tickerCount()).To(Equal(1))

			_, ok := u.figiAt("AAPL", d(2024, 6, 1))
			Expect(ok).To(BeTrue())

			_, ok = u.figiAt("MSFT", d(2024, 6, 1))
			Expect(ok).To(BeFalse())
		})

		It("applies the figi filter exactly", func() {
			assets := []*data.Asset{
				{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4"},
				{Ticker: "MSFT", CompositeFigi: "BBG000BPH459"},
			}
			u := buildHistoricalUniverse(assets, "", "BBG000BPH459")
			Expect(u.tickerCount()).To(Equal(1))

			_, ok := u.figiAt("MSFT", d(2024, 6, 1))
			Expect(ok).To(BeTrue())
		})

		It("parses ISO-formatted listing/delisting dates", func() {
			assets := []*data.Asset{
				{
					Ticker:        "XYZ",
					CompositeFigi: "BBG000XYZXYZ",
					ListingDate:   "2003-01-01T00:00:00.000000Z",
					DelistingDate: "2010-06-30T00:00:00.000000Z",
				},
			}
			u := buildHistoricalUniverse(assets, "", "")

			_, ok := u.figiAt("XYZ", d(2008, 5, 15))
			Expect(ok).To(BeTrue())

			_, ok = u.figiAt("XYZ", d(2011, 1, 1))
			Expect(ok).To(BeFalse())
		})

		It("treats unparseable dates as zero (open-ended)", func() {
			assets := []*data.Asset{
				{
					Ticker:        "XYZ",
					CompositeFigi: "BBG000XYZXYZ",
					ListingDate:   "garbage",
					DelistingDate: "",
				},
			}
			u := buildHistoricalUniverse(assets, "", "")

			// Unparseable listing falls back to zero → "always listed"
			_, ok := u.figiAt("XYZ", d(1900, 1, 1))
			Expect(ok).To(BeTrue())
		})
	})

	Describe("parseAssetDate", func() {
		It("returns zero for empty input", func() {
			Expect(parseAssetDate("")).To(Equal(time.Time{}))
		})

		It("parses the database to_char output (microsecond precision)", func() {
			got := parseAssetDate("2003-09-10T00:00:00.000000Z")
			Expect(got).To(Equal(time.Date(2003, 9, 10, 0, 0, 0, 0, time.UTC)))
		})

		It("parses an ISO 8601 timestamp without microseconds", func() {
			got := parseAssetDate("2003-09-10T00:00:00Z")
			Expect(got).To(Equal(time.Date(2003, 9, 10, 0, 0, 0, 0, time.UTC)))
		})

		It("parses a bare date", func() {
			got := parseAssetDate("2003-09-10")
			Expect(got).To(Equal(time.Date(2003, 9, 10, 0, 0, 0, 0, time.UTC)))
		})

		It("returns zero for unparseable input", func() {
			Expect(parseAssetDate("not-a-date")).To(Equal(time.Time{}))
		})
	})
})
