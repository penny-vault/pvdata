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

// buildTwoLifecycleArchive creates an EOD archive for ticker ABI with
// two lifecycle ranges: an early one in 2003 and a late one in 2025.
// The dates inside each lifecycle are 10 days apart, so the
// 20-day-gap split keeps each lifecycle as a single range, while the
// years-long gap between them produces two distinct ranges.
func buildTwoLifecycleArchive() *EODArchive {
	root := GinkgoT().TempDir()

	writeArchiveDayFile(root, time.Date(2003, 9, 10, 0, 0, 0, 0, time.UTC), []string{"ABI"})
	writeArchiveDayFile(root, time.Date(2003, 9, 20, 0, 0, 0, 0, time.UTC), []string{"ABI"})
	writeArchiveDayFile(root, time.Date(2025, 6, 26, 0, 0, 0, 0, time.UTC), []string{"ABI"})
	writeArchiveDayFile(root, time.Date(2025, 7, 6, 0, 0, 0, 0, time.UTC), []string{"ABI"})

	archive, err := LoadEODArchive(root)
	Expect(err).NotTo(HaveOccurred())

	return archive
}

// newFetcherWithArchive constructs a massiveAssetFetcher whose
// eodArchive is preloaded with the supplied archive and whose
// eodArchiveOnce is already consumed, so eodArchiveForRun() returns
// the test archive without running its viper-dependent production
// loader.
func newFetcherWithArchive(archive *EODArchive) *massiveAssetFetcher {
	api := &massiveAssetFetcher{
		walkWindowsByFigi: make(map[string]walkWindow),
		walkWindowsByCIK:  make(map[string]walkWindow),
		walkWindowsByName: make(map[string]walkWindow),
	}
	api.eodArchiveOnce.Do(func() {})
	api.eodArchive = archive

	return api
}

var _ = Describe("figiBelongsToDifferentLifecycle", func() {
	var (
		ctx     context.Context
		api     *massiveAssetFetcher
		archive *EODArchive
	)

	BeforeEach(func() {
		ctx = context.Background()
		archive = buildTwoLifecycleArchive()
		api = newFetcherWithArchive(archive)
	})

	It("rejects a FIGI that Massive assigned to a different lifecycle of the ticker", func() {
		api.walkWindowsByFigi["ABI:BBG01VCKWDB6"] = walkWindow{
			firstSeen: time.Date(2025, 6, 26, 0, 0, 0, 0, time.UTC),
			lastSeen:  time.Date(2025, 7, 6, 0, 0, 0, 0, time.UTC),
		}

		asset := &data.Asset{
			Ticker:   "ABI",
			ValidFor: time.Date(2003, 9, 15, 0, 0, 0, 0, time.UTC),
		}

		Expect(api.figiBelongsToDifferentLifecycle(ctx, asset, "BBG01VCKWDB6")).To(BeTrue())
	})

	It("accepts a FIGI that Massive assigned to the same lifecycle of the ticker", func() {
		api.walkWindowsByFigi["ABI:BBG01VCKWDB6"] = walkWindow{
			firstSeen: time.Date(2025, 6, 26, 0, 0, 0, 0, time.UTC),
			lastSeen:  time.Date(2025, 7, 6, 0, 0, 0, 0, time.UTC),
		}

		asset := &data.Asset{
			Ticker:   "ABI",
			ValidFor: time.Date(2025, 7, 1, 0, 0, 0, 0, time.UTC),
		}

		Expect(api.figiBelongsToDifferentLifecycle(ctx, asset, "BBG01VCKWDB6")).To(BeFalse())
	})

	It("accepts when Massive has never assigned the candidate FIGI to the ticker", func() {
		asset := &data.Asset{
			Ticker:   "ABI",
			ValidFor: time.Date(2003, 9, 15, 0, 0, 0, 0, time.UTC),
		}

		Expect(api.figiBelongsToDifferentLifecycle(ctx, asset, "BBG000UNKNOWN")).To(BeFalse())
	})

	It("accepts when no EOD archive is loaded", func() {
		api.eodArchive = nil

		api.walkWindowsByFigi["ABI:BBG01VCKWDB6"] = walkWindow{
			firstSeen: time.Date(2025, 6, 26, 0, 0, 0, 0, time.UTC),
			lastSeen:  time.Date(2025, 7, 6, 0, 0, 0, 0, time.UTC),
		}

		asset := &data.Asset{
			Ticker:   "ABI",
			ValidFor: time.Date(2003, 9, 15, 0, 0, 0, 0, time.UTC),
		}

		Expect(api.figiBelongsToDifferentLifecycle(ctx, asset, "BBG01VCKWDB6")).To(BeFalse())
	})

	It("accepts when the archive has no ranges for the ticker", func() {
		asset := &data.Asset{
			Ticker:   "NOTHERE",
			ValidFor: time.Date(2003, 9, 15, 0, 0, 0, 0, time.UTC),
		}

		Expect(api.figiBelongsToDifferentLifecycle(ctx, asset, "BBG000XYZ")).To(BeFalse())
	})

	It("accepts when the asset's ValidFor falls in a gap between two lifecycles", func() {
		api.walkWindowsByFigi["ABI:BBG01VCKWDB6"] = walkWindow{
			firstSeen: time.Date(2025, 6, 26, 0, 0, 0, 0, time.UTC),
			lastSeen:  time.Date(2025, 7, 6, 0, 0, 0, 0, time.UTC),
		}

		asset := &data.Asset{
			Ticker:   "ABI",
			ValidFor: time.Date(2015, 6, 1, 0, 0, 0, 0, time.UTC),
		}

		Expect(api.figiBelongsToDifferentLifecycle(ctx, asset, "BBG01VCKWDB6")).To(BeFalse())
	})

	It("accepts an empty candidate FIGI", func() {
		asset := &data.Asset{
			Ticker:   "ABI",
			ValidFor: time.Date(2003, 9, 15, 0, 0, 0, 0, time.UTC),
		}

		Expect(api.figiBelongsToDifferentLifecycle(ctx, asset, "")).To(BeFalse())
	})
})

var _ = Describe("lifecycleContaining", func() {
	ranges := []dateRange{
		{Start: time.Date(2003, 9, 10, 0, 0, 0, 0, time.UTC), End: time.Date(2003, 9, 20, 0, 0, 0, 0, time.UTC)},
		{Start: time.Date(2025, 6, 26, 0, 0, 0, 0, time.UTC), End: time.Date(2025, 7, 6, 0, 0, 0, 0, time.UTC)},
	}

	It("returns the range whose inclusive [start, end] window contains the date", func() {
		r, ok := lifecycleContaining(ranges, time.Date(2003, 9, 15, 0, 0, 0, 0, time.UTC))
		Expect(ok).To(BeTrue())
		Expect(r.Start).To(Equal(ranges[0].Start))
	})

	It("includes the range's start date in the window", func() {
		r, ok := lifecycleContaining(ranges, ranges[0].Start)
		Expect(ok).To(BeTrue())
		Expect(r.Start).To(Equal(ranges[0].Start))
	})

	It("includes the range's end date in the window", func() {
		r, ok := lifecycleContaining(ranges, ranges[1].End)
		Expect(ok).To(BeTrue())
		Expect(r.Start).To(Equal(ranges[1].Start))
	})

	It("returns ok=false when the date falls before every range", func() {
		_, ok := lifecycleContaining(ranges, time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC))
		Expect(ok).To(BeFalse())
	})

	It("returns ok=false when the date falls in a gap between two ranges", func() {
		_, ok := lifecycleContaining(ranges, time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC))
		Expect(ok).To(BeFalse())
	})
})
