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
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
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

// processChunk executes the universe computation for one chunk of trading days.
// It loads source data once, iterates days, computes the universe, diffs against the
// prior state, and emits observations to `out`. The prior state is loaded from the DB
// at chunk start and maintained in memory across the chunk's days.
func processChunk(
	ctx context.Context,
	pool *pgxpool.Pool,
	sub *library.Subscription,
	indexTicker string,
	tradingDays []time.Time,
	candidates []*data.Asset,
	out chan<- *data.Observation,
) error {
	if len(tradingDays) == 0 {
		return nil
	}

	logger := zerolog.Ctx(ctx)

	chunkStart := tradingDays[0]
	chunkEnd := tradingDays[len(tradingDays)-1]

	// 200 trading days of EOD prefix before the chunk start (~400 calendar days
	// covers 200 trading days comfortably).
	loadStart := chunkStart.AddDate(0, 0, -400)

	figis := make([]string, len(candidates))
	for i, a := range candidates {
		figis[i] = a.CompositeFigi
	}

	logger.Info().
		Time("chunk_start", chunkStart).
		Time("chunk_end", chunkEnd).
		Int("trading_days", len(tradingDays)).
		Int("candidate_assets", len(candidates)).
		Msg("loading chunk data")

	eodByFigi, err := loadEodChunk(ctx, pool, figis, loadStart, chunkEnd)
	if err != nil {
		return fmt.Errorf("load eod chunk: %w", err)
	}

	// Reconstruct prior state from DB at chunk start.
	prevState := provider.CurrentIndexMembers(
		ctx,
		pool,
		sub.DataTablesMap[data.IndexSnapshotKey],
		sub.DataTablesMap[data.IndexChangelogKey],
		indexTicker,
		chunkStart.AddDate(0, 0, -1),
	)

	for _, d := range tradingDays {
		// Determine the trailing 200-trading-day window ending d-1.
		windowDays, err := loadTrailingWindow(ctx, pool, d, minDayCountStandard)
		if err != nil {
			return fmt.Errorf("load trailing window for %s: %w", d.Format("2006-01-02"), err)
		}

		if len(windowDays) < minDayCountEarlyEntry {
			logger.Warn().Time("date", d).Msg("insufficient trading days for window; skipping")
			continue
		}

		windowStart := windowDays[0]
		windowEnd := windowDays[len(windowDays)-1]

		mcapByFigi, err := loadMarketCapAsOf(ctx, pool, figis, d)
		if err != nil {
			return fmt.Errorf("load market cap for %s: %w", d.Format("2006-01-02"), err)
		}

		broadMcaps, err := loadBroadMarketCaps(ctx, pool, d)
		if err != nil {
			return fmt.Errorf("load broad mcaps for %s: %w", d.Format("2006-01-02"), err)
		}

		input := perDayInput{
			Date:            d,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: len(windowDays),
			Assets:          candidates,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		newState := computeUniverseForDate(input)

		adds, removes, weightChanges := provider.DiffSnapshotsWithThreshold(
			newState,
			prevState,
			provider.DiffOptions{RelativeThreshold: 0.25},
		)

		obsTemplate := &data.Observation{
			ObservationDate:  d,
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}

		provider.EmitChangelog(adds, removes, indexTicker, d, obsTemplate, out)
		provider.EmitWeightChanges(weightChanges, indexTicker, d, obsTemplate, out)

		// Emit annual snapshot if a year has elapsed since the last in-DB snapshot,
		// or if no snapshot has ever been taken (cold start).
		lastSnapshot := provider.LastSnapshotDate(ctx, pool, sub.DataTablesMap[data.IndexSnapshotKey], indexTicker)
		if provider.ShouldTakeSnapshot(lastSnapshot, d, "yearly") {
			constituents := make([]data.IndexConstituent, 0, len(newState))
			for tk, m := range newState {
				constituents = append(constituents, data.IndexConstituent{
					Ticker:        tk,
					CompositeFigi: m.CompositeFigi,
					Weight:        m.Weight,
				})
			}

			out <- &data.Observation{
				IndexSnapshot: &data.IndexSnapshot{
					IndexTicker:  indexTicker,
					SnapshotDate: d,
					Constituents: constituents,
				},
				ObservationDate:  d,
				SubscriptionID:   sub.ID,
				SubscriptionName: sub.Name,
			}
		}

		// Apply emitted changes to prevState in memory for the next day's diff.
		for tk := range removes {
			delete(prevState, tk)
		}

		for tk, m := range adds {
			prevState[tk] = m
		}

		for tk, m := range weightChanges {
			prevState[tk] = m
		}
	}

	return nil
}

// loadTrailingWindow returns the trailing N trading days ending at (asOf - 1 trading day).
func loadTrailingWindow(ctx context.Context, pool *pgxpool.Pool, asOf time.Time, n int) ([]time.Time, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn for loadTrailingWindow: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx,
		`SELECT dt FROM (
		   SELECT dt FROM trading_days($1::date - INTERVAL '400 days', $1::date - INTERVAL '1 day') AS t(dt)
		   ORDER BY dt DESC
		   LIMIT $2
		 ) sub
		 ORDER BY dt`,
		asOf, n,
	)
	if err != nil {
		return nil, fmt.Errorf("query trailing window: %w", err)
	}
	defer rows.Close()

	var out []time.Time

	for rows.Next() {
		var dt time.Time
		if err := rows.Scan(&dt); err != nil {
			return nil, fmt.Errorf("scan trailing window day: %w", err)
		}

		out = append(out, dt)
	}

	return out, nil
}
