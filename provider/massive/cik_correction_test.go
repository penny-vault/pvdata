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

var _ = Describe("earliestTradingEvidence", func() {
	var api *massiveAssetFetcher

	BeforeEach(func() {
		api = newFetcherWithArchive(buildBBIArchive())
	})

	It("returns the walk firstSeen for the ticker-CIK pair when only that is populated", func() {
		api.walkWindowsByCIK["BBI:0001085734"] = walkWindow{
			firstSeen: time.Date(2003, 9, 10, 0, 0, 0, 0, time.UTC),
			lastSeen:  time.Date(2010, 7, 6, 0, 0, 0, 0, time.UTC),
		}

		asset := &data.Asset{
			Ticker:   "BBI",
			CIK:      "0001085734",
			ValidFor: time.Date(2004, 7, 9, 0, 0, 0, 0, time.UTC),
		}

		// Empty the archive so only the walk evidence is in play.
		api.eodArchive = nil

		Expect(api.earliestTradingEvidence(asset)).To(Equal(time.Date(2003, 9, 10, 0, 0, 0, 0, time.UTC)))
	})

	It("returns the EOD lifecycle's start date when only that is populated", func() {
		asset := &data.Asset{
			Ticker:   "BBI",
			ValidFor: time.Date(2004, 7, 9, 0, 0, 0, 0, time.UTC),
		}

		Expect(api.earliestTradingEvidence(asset)).To(Equal(time.Date(2003, 9, 10, 0, 0, 0, 0, time.UTC)))
	})

	It("returns the earliest of walk and EOD evidence when both are populated", func() {
		// EOD says the lifecycle started 2019-09-03. Walk somehow saw the
		// (ticker, CIK) pair earlier on 2018-07-01 — earliest wins.
		api.walkWindowsByCIK["BBI:0000819050"] = walkWindow{
			firstSeen: time.Date(2018, 7, 1, 0, 0, 0, 0, time.UTC),
			lastSeen:  time.Date(2022, 9, 7, 0, 0, 0, 0, time.UTC),
		}

		asset := &data.Asset{
			Ticker:   "BBI",
			CIK:      "0000819050",
			ValidFor: time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC),
		}

		Expect(api.earliestTradingEvidence(asset)).To(Equal(time.Date(2018, 7, 1, 0, 0, 0, 0, time.UTC)))
	})

	It("looks up walk by composite_figi when CIK is empty", func() {
		api.walkWindowsByFigi["BBI:BBG000BGN354"] = walkWindow{
			firstSeen: time.Date(2019, 9, 3, 0, 0, 0, 0, time.UTC),
			lastSeen:  time.Date(2022, 9, 7, 0, 0, 0, 0, time.UTC),
		}

		api.eodArchive = nil

		asset := &data.Asset{
			Ticker:        "BBI",
			CompositeFigi: "BBG000BGN354",
			ValidFor:      time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC),
		}

		Expect(api.earliestTradingEvidence(asset)).To(Equal(time.Date(2019, 9, 3, 0, 0, 0, 0, time.UTC)))
	})

	It("returns the zero time when no walk window matches and the archive has no ranges for the ticker", func() {
		asset := &data.Asset{
			Ticker:   "ZZZZZ",
			ValidFor: time.Date(2020, 6, 1, 0, 0, 0, 0, time.UTC),
		}

		Expect(api.earliestTradingEvidence(asset).IsZero()).To(BeTrue())
	})

	It("snaps a ValidFor past every archive range to the most recent lifecycle", func() {
		// ValidFor 2026-12-31 sits past the most recent range's end
		// (2022-09-07). Should snap to that lifecycle's start.
		asset := &data.Asset{
			Ticker:   "BBI",
			ValidFor: time.Date(2026, 12, 31, 0, 0, 0, 0, time.UTC),
		}

		Expect(api.earliestTradingEvidence(asset)).To(Equal(time.Date(2019, 9, 3, 0, 0, 0, 0, time.UTC)))
	})
})

var _ = Describe("candidateClaimsTicker", func() {
	It("returns true when the candidate's tickers list contains the asset's ticker", func() {
		Expect(candidateClaimsTicker([]string{"AAPL", "MSFT"}, "AAPL")).To(BeTrue())
	})

	It("returns false when the candidate's tickers list does not contain the asset's ticker", func() {
		Expect(candidateClaimsTicker([]string{"AAPL", "MSFT"}, "GOOG")).To(BeFalse())
	})

	It("compares case-insensitively and trims whitespace on both sides", func() {
		Expect(candidateClaimsTicker([]string{" aapl "}, "AAPL")).To(BeTrue())
		Expect(candidateClaimsTicker([]string{"AAPL"}, " aapl ")).To(BeTrue())
	})

	It("returns false for an empty target so the caller does not see a phantom match", func() {
		Expect(candidateClaimsTicker([]string{"AAPL"}, "")).To(BeFalse())
		Expect(candidateClaimsTicker([]string{}, "")).To(BeFalse())
	})
})
