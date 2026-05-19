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
)

// buildBBIArchive constructs an EOD archive that mirrors the real-
// world BBI shape: an early Blockbuster-style lifecycle that sits at
// the start of our coverage (2003-09-10 to 2010-07-06), and a later
// Brickell-style lifecycle that sits fully inside coverage
// (2019-09-03 to 2022-09-07). The archive is built directly rather
// than via per-day files so the test does not have to write the
// thousands of daily parquet files that would otherwise be needed to
// keep the long lifecycles from being split by the 20-day gap rule.
func buildBBIArchive() *EODArchive {
	return &EODArchive{
		tickers: map[string][]dateRange{
			"BBI": {
				{
					Start: time.Date(2003, 9, 10, 0, 0, 0, 0, time.UTC),
					End:   time.Date(2010, 7, 6, 0, 0, 0, 0, time.UTC),
				},
				{
					Start: time.Date(2019, 9, 3, 0, 0, 0, 0, time.UTC),
					End:   time.Date(2022, 9, 7, 0, 0, 0, 0, time.UTC),
				},
			},
		},
		coverageStart: time.Date(2003, 9, 10, 0, 0, 0, 0, time.UTC),
		coverageEnd:   time.Date(2022, 9, 7, 0, 0, 0, 0, time.UTC),
	}
}

var _ = Describe("buildDateCandidates EOD lifecycle selection", func() {
	var (
		ctx context.Context
		api *massiveAssetFetcher
	)

	BeforeEach(func() {
		ctx = context.Background()
		api = newFetcherWithArchive(buildBBIArchive())
	})

	It("returns the early lifecycle's first and last bars when ValidFor falls in the early lifecycle", func() {
		asset := &data.Asset{
			Ticker:   "BBI",
			ValidFor: time.Date(2004, 7, 9, 0, 0, 0, 0, time.UTC),
		}

		c := api.buildDateCandidates(ctx, asset)

		Expect(c.MassiveEODArchiveFirstBar).To(Equal(time.Date(2003, 9, 10, 0, 0, 0, 0, time.UTC)))
		Expect(c.MassiveEODArchiveLastBar).To(Equal(time.Date(2010, 7, 6, 0, 0, 0, 0, time.UTC)))
	})

	It("returns the late lifecycle's first and last bars when ValidFor falls in the late lifecycle", func() {
		asset := &data.Asset{
			Ticker:   "BBI",
			ValidFor: time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC),
		}

		c := api.buildDateCandidates(ctx, asset)

		Expect(c.MassiveEODArchiveFirstBar).To(Equal(time.Date(2019, 9, 3, 0, 0, 0, 0, time.UTC)))
		Expect(c.MassiveEODArchiveLastBar).To(Equal(time.Date(2022, 9, 7, 0, 0, 0, 0, time.UTC)))
	})

	It("keeps coverage start and end as the archive-wide span so the edge-buffer check still rejects edge-touching lifecycles", func() {
		asset := &data.Asset{
			Ticker:   "BBI",
			ValidFor: time.Date(2004, 7, 9, 0, 0, 0, 0, time.UTC),
		}

		c := api.buildDateCandidates(ctx, asset)

		Expect(c.MassiveEODArchiveCoverageStart).To(Equal(time.Date(2003, 9, 10, 0, 0, 0, 0, time.UTC)))
		Expect(c.MassiveEODArchiveCoverageEnd).To(Equal(time.Date(2022, 9, 7, 0, 0, 0, 0, time.UTC)))
	})

	It("leaves the EOD bar candidates zero when ValidFor falls in a gap between lifecycles", func() {
		asset := &data.Asset{
			Ticker:   "BBI",
			ValidFor: time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC),
		}

		c := api.buildDateCandidates(ctx, asset)

		Expect(c.MassiveEODArchiveFirstBar.IsZero()).To(BeTrue())
		Expect(c.MassiveEODArchiveLastBar.IsZero()).To(BeTrue())
	})

	It("leaves the EOD bar candidates zero when the ticker has no ranges in the archive", func() {
		asset := &data.Asset{
			Ticker:   "ZZZZ",
			ValidFor: time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC),
		}

		c := api.buildDateCandidates(ctx, asset)

		Expect(c.MassiveEODArchiveFirstBar.IsZero()).To(BeTrue())
		Expect(c.MassiveEODArchiveLastBar.IsZero()).To(BeTrue())
	})

	It("treats today's-snapshot assets (ValidFor zero) as belonging to the most recent lifecycle", func() {
		asset := &data.Asset{
			Ticker: "BBI",
		}

		c := api.buildDateCandidates(ctx, asset)

		Expect(c.MassiveEODArchiveFirstBar).To(Equal(time.Date(2019, 9, 3, 0, 0, 0, 0, time.UTC)))
		Expect(c.MassiveEODArchiveLastBar).To(Equal(time.Date(2022, 9, 7, 0, 0, 0, 0, time.UTC)))
	})
})

var _ = Describe("lookupWalkWindowWithKey name-fallback", func() {
	var api *massiveAssetFetcher

	BeforeEach(func() {
		api = newFetcherWithArchive(buildBBIArchive())
	})

	It("returns the window keyed by ticker:name when both FIGI and CIK are absent", func() {
		api.walkWindowsByName["MCC:name:MESTEK INC"] = walkWindow{
			firstSeen: time.Date(2003, 9, 10, 0, 0, 0, 0, time.UTC),
			lastSeen:  time.Date(2006, 8, 30, 0, 0, 0, 0, time.UTC),
		}

		asset := &data.Asset{Ticker: "MCC", Name: "MESTEK INC"}

		key, win, ok := api.lookupWalkWindowWithKey(asset)
		Expect(ok).To(BeTrue())
		Expect(key).To(Equal("name=MCC:name:MESTEK INC"))
		Expect(win.firstSeen).To(Equal(time.Date(2003, 9, 10, 0, 0, 0, 0, time.UTC)))
		Expect(win.lastSeen).To(Equal(time.Date(2006, 8, 30, 0, 0, 0, 0, time.UTC)))
	})

	It("prefers a FIGI hit over a name hit when both are present", func() {
		api.walkWindowsByFigi["AAPL:BBG000B9XRY4"] = walkWindow{
			firstSeen: time.Date(2010, 1, 4, 0, 0, 0, 0, time.UTC),
			lastSeen:  time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC),
		}
		api.walkWindowsByName["AAPL:name:Apple Inc."] = walkWindow{
			firstSeen: time.Date(2005, 6, 1, 0, 0, 0, 0, time.UTC),
			lastSeen:  time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC),
		}

		asset := &data.Asset{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", Name: "Apple Inc."}

		key, win, ok := api.lookupWalkWindowWithKey(asset)
		Expect(ok).To(BeTrue())
		Expect(key).To(Equal("figi=AAPL:BBG000B9XRY4"))
		Expect(win.firstSeen).To(Equal(time.Date(2010, 1, 4, 0, 0, 0, 0, time.UTC)))
	})

	It("returns a diagnostic miss key when no index has the asset", func() {
		asset := &data.Asset{Ticker: "ZZZZZ", Name: "Nowhere Co"}

		key, _, ok := api.lookupWalkWindowWithKey(asset)
		Expect(ok).To(BeFalse())
		Expect(key).To(Equal("tried=ZZZZZ:name:Nowhere Co"))
	})
})
