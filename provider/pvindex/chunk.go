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
package pvindex

import (
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/provider"
)

const (
	// minDayCountStandard is the standard contiguity threshold (200 trading days).
	minDayCountStandard = 200
	// minDayCountEarlyEntry is the minimum data required for the IPO early-entry path.
	minDayCountEarlyEntry = 30
	// liquidityThresholdUSD is the minimum 200-day median dollar volume.
	liquidityThresholdUSD = 2_500_000.0
	// priceFloorUSD is the minimum prior-day close.
	priceFloorUSD = 2.0
	// sizePercentile is the 25th percentile cutoff for market cap.
	sizePercentile = 0.25
	// earlyEntryPercentile is the top quintile threshold (80th percentile).
	earlyEntryPercentile = 0.80
)

// perDayInput is the data needed to compute the universe for a single date.
// Loaded once per chunk and reused across days.
type perDayInput struct {
	Date            time.Time
	WindowStart     time.Time // inclusive lower bound for the 200-day rolling window
	WindowEnd       time.Time // inclusive upper bound (= D - 1 trading day)
	TradingDayCount int       // expected count of trading days in [WindowStart, WindowEnd]
	Assets          []*data.Asset
	EodByFigi       map[string][]eodRow
	MarketCapByFigi map[string]int64
	BroadMarketCaps []int64 // all market caps in the broad CS pool on D, used for percentile thresholds
}

// computeUniverseForDate runs the full filter chain for a single trading day and
// returns the resulting universe as map[ticker]IndexMember with cap-weights assigned.
func computeUniverseForDate(in perDayInput) map[string]provider.IndexMember {
	// Step 1 (asset master) is assumed to have already been applied to in.Assets.

	// Step 3: rolling stats per FIGI.
	statsByFigi := make(map[string]stats, len(in.Assets))

	for _, a := range in.Assets {
		rows := in.EodByFigi[a.CompositeFigi]
		statsByFigi[a.CompositeFigi] = rollingStats(rows, in.WindowStart, in.WindowEnd)
	}

	// Step 4: percentile baseline (broad CS pool).
	sizeCutoff := percentileInt64(in.BroadMarketCaps, sizePercentile)
	earlyEntryCutoff := percentileInt64(in.BroadMarketCaps, earlyEntryPercentile)

	// Step 5: data availability.
	type candidate struct {
		asset      *data.Asset
		priorClose float64
		marketCap  int64
		medianDV   float64
	}

	candidates := make([]candidate, 0, len(in.Assets))

	for _, a := range in.Assets {
		st := statsByFigi[a.CompositeFigi]

		mcap, hasMcap := in.MarketCapByFigi[a.CompositeFigi]
		if !hasMcap || mcap <= 0 {
			continue
		}

		standardEligible := st.dayCount >= minDayCountStandard
		earlyEligible := st.dayCount >= minDayCountEarlyEntry && st.dayCount < minDayCountStandard && mcap >= earlyEntryCutoff

		if !standardEligible && !earlyEligible {
			continue
		}

		// Contiguity check for standard path: dayCount must equal expected trading day count.
		// For early-entry, contiguity is implicit (all available rows count toward dayCount).
		if standardEligible && st.dayCount != in.TradingDayCount {
			continue
		}

		// Prior close = most recent in-window EOD close.
		priorClose := mostRecentClose(in.EodByFigi[a.CompositeFigi], in.WindowEnd)

		candidates = append(candidates, candidate{
			asset:      a,
			priorClose: priorClose,
			marketCap:  mcap,
			medianDV:   st.medianDV,
		})
	}

	// Step 6: share-class deduplication.
	dvByFigi := make(map[string]float64, len(candidates))
	assetsForDedup := make([]*data.Asset, 0, len(candidates))
	candByFigi := make(map[string]candidate, len(candidates))

	for _, c := range candidates {
		dvByFigi[c.asset.CompositeFigi] = c.medianDV
		assetsForDedup = append(assetsForDedup, c.asset)
		candByFigi[c.asset.CompositeFigi] = c
	}

	deduped := dedupShareClasses(assetsForDedup, dvByFigi)

	// Step 7: market cap percentile filter.
	survivors := make([]candidate, 0, len(deduped))

	for _, a := range deduped {
		c := candByFigi[a.CompositeFigi]
		if c.marketCap < sizeCutoff {
			continue
		}

		survivors = append(survivors, c)
	}

	// Step 8: liquidity filter.
	liquidSurvivors := make([]candidate, 0, len(survivors))

	for _, c := range survivors {
		if c.medianDV < liquidityThresholdUSD {
			continue
		}

		liquidSurvivors = append(liquidSurvivors, c)
	}

	// Step 9: price guard rail.
	priceSurvivors := make([]candidate, 0, len(liquidSurvivors))

	for _, c := range liquidSurvivors {
		if c.priorClose < priceFloorUSD {
			continue
		}

		priceSurvivors = append(priceSurvivors, c)
	}

	// Step 10: cap weights.
	caps := make(map[string]int64, len(priceSurvivors))
	for _, c := range priceSurvivors {
		caps[c.asset.CompositeFigi] = c.marketCap
	}

	weights := assignCapWeights(caps)

	universe := make(map[string]provider.IndexMember, len(priceSurvivors))
	for _, c := range priceSurvivors {
		universe[c.asset.Ticker] = provider.IndexMember{
			CompositeFigi: c.asset.CompositeFigi,
			Weight:        weights[c.asset.CompositeFigi],
		}
	}

	return universe
}

// mostRecentClose returns the close price of the most recent EOD row at or before the
// given date, or 0 if no such row exists.
func mostRecentClose(rows []eodRow, asOf time.Time) float64 {
	var (
		bestDate  time.Time
		bestClose float64
	)

	for _, r := range rows {
		if r.Date.After(asOf) {
			continue
		}

		if r.Date.After(bestDate) {
			bestDate = r.Date
			bestClose = r.Close
		}
	}

	return bestClose
}
