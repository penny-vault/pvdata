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

	// Entry thresholds: a stock must clear these to enter the universe.
	liquidityTurnoverEntry = 0.0005 // median DV / market cap
	priceFloorEntry        = 2.0    // prior-day close
	sizePercentileEntry    = 0.25   // bottom quartile excluded

	// Removal thresholds: a stock already in the universe is only removed
	// when it falls below these more lenient levels. This hysteresis prevents
	// churn from stocks oscillating near filter boundaries.
	liquidityTurnoverRemoval = 0.0003
	priceFloorRemoval        = 1.50
	sizePercentileRemoval    = 0.15

	// earlyEntryPercentile is the top quintile threshold (80th percentile).
	earlyEntryPercentile = 0.80
	// bufferDays is the number of consecutive trading days a stock must
	// consistently qualify (or disqualify) before it is added to (or removed
	// from) the universe. This prevents churn from stocks sitting right at a
	// filter boundary.
	bufferDays = 21
	// hardDisqualifyPrice is the price threshold below which a stock is
	// immediately removed from the universe, bypassing the buffer period.
	hardDisqualifyPrice = 1.0

	// snapshotMonth and snapshotDay define the annual snapshot anchor date
	// (winter solstice, December 21).
	snapshotMonth = time.December
	snapshotDay   = 21

	// minBroadMcaps is the minimum number of distinct CS market-cap rows the
	// metrics table must have for a trading day before pvindex will compute
	// the universe for that day. The active US CS universe on whitelisted
	// exchanges is roughly 4000-7000 names; if we see fewer than this floor
	// the metrics importer hasn't run (or only partially ran), in which case
	// the universe computation would emit spurious add/remove changelog
	// events driven by data-pipeline state rather than real index movement.
	// Skipping cleanly is preferable to writing dirt.
	minBroadMcaps = 500
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

// stockMetrics holds the per-stock filter values computed by the filter chain.
// The caller applies entry or removal thresholds against these values.
type stockMetrics struct {
	Member     provider.IndexMember
	Turnover   float64
	PriorClose float64
	MarketCap  int64
	EarlyEntry bool
}

// universeResult holds the output of computeUniverseForDate.
type universeResult struct {
	// Candidates contains every stock that passes data availability, contiguity,
	// and share-class dedup -- with filter metrics attached. No size, liquidity,
	// or price filter has been applied.
	Candidates map[string]stockMetrics
}

// computeUniverseForDate runs the data-availability and dedup stages of the
// filter chain and returns per-stock metrics. Size, liquidity, and price
// thresholds are NOT applied here -- the caller applies entry or removal
// thresholds against the returned metrics.
func computeUniverseForDate(in perDayInput) universeResult {
	// Step 1 (asset master) is assumed to have already been applied to in.Assets.

	// Step 3: rolling stats per FIGI.
	statsByFigi := make(map[string]stats, len(in.Assets))

	for _, a := range in.Assets {
		rows := in.EodByFigi[a.CompositeFigi]
		statsByFigi[a.CompositeFigi] = rollingStats(rows, in.WindowStart, in.WindowEnd)
	}

	// Step 4: percentile baseline (broad CS pool).
	earlyEntryCutoff := percentileInt64(in.BroadMarketCaps, earlyEntryPercentile)

	// Step 5: data availability.
	type candidate struct {
		asset      *data.Asset
		priorClose float64
		marketCap  int64
		medianDV   float64
		earlyEntry bool
	}

	candidates := make([]candidate, 0, len(in.Assets))

	for _, a := range in.Assets {
		// Respect the asset's listing/delisting dates: a name cannot be a
		// candidate before it was listed or on/after it delisted. This is the
		// same alive-on-D check used during hard removal, applied here so the
		// entry path doesn't rely solely on data availability.
		if !assetAliveOn(a, in.Date) {
			continue
		}

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
			earlyEntry: earlyEligible,
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

	// Build per-stock metrics for the caller to threshold.
	result := make(map[string]stockMetrics, len(deduped))

	for _, a := range deduped {
		c := candByFigi[a.CompositeFigi]
		turnover := c.medianDV / float64(c.marketCap)

		result[c.asset.Ticker] = stockMetrics{
			Member: provider.IndexMember{
				CompositeFigi: c.asset.CompositeFigi,
			},
			Turnover:   turnover,
			PriorClose: c.priorClose,
			MarketCap:  c.marketCap,
			EarlyEntry: c.earlyEntry,
		}
	}

	return universeResult{Candidates: result}
}

// applyThresholds filters candidates by the given size, liquidity, and price
// thresholds and assigns cap-weights. Returns the resulting universe.
func applyThresholds(candidates map[string]stockMetrics, sizePercCutoff int64, liquidityMin, priceMin float64) (map[string]provider.IndexMember, map[string]bool) {
	caps := make(map[string]int64)
	survivors := make(map[string]stockMetrics)

	for ticker, sm := range candidates {
		if sm.MarketCap < sizePercCutoff {
			continue
		}

		if sm.Turnover < liquidityMin {
			continue
		}

		if sm.PriorClose < priceMin {
			continue
		}

		survivors[ticker] = sm
		caps[sm.Member.CompositeFigi] = sm.MarketCap
	}

	weights := assignCapWeights(caps)
	universe := make(map[string]provider.IndexMember, len(survivors))
	earlyEntry := make(map[string]bool)

	for ticker, sm := range survivors {
		universe[ticker] = provider.IndexMember{
			CompositeFigi: sm.Member.CompositeFigi,
			Weight:        weights[sm.Member.CompositeFigi],
		}

		if sm.EarlyEntry {
			earlyEntry[ticker] = true
		}
	}

	return universe, earlyEntry
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

// graceExpiryDate returns the date 200 trading days after the given date.
func graceExpiryDate(ctx context.Context, pool *pgxpool.Pool, from time.Time) time.Time {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return from.AddDate(0, 0, 280) // fallback
	}
	defer conn.Release()

	var expiry time.Time

	err = conn.QueryRow(ctx,
		`SELECT dt FROM trading_days($1::date, ($1::date + INTERVAL '400 days')::date) AS t(dt)
		 ORDER BY dt LIMIT 1 OFFSET $2`,
		from, minDayCountStandard,
	).Scan(&expiry)
	if err != nil {
		return from.AddDate(0, 0, 280) // fallback
	}

	return expiry
}

// chunkState carries state that must persist across chunk boundaries.
type chunkState struct {
	PendingAdd      map[string]int
	PendingRemove   map[string]int
	EarlyEntryGrace map[string]time.Time
	// LastSnapshot is the most recent snapshot date that has either been
	// observed in the database (seeded once before processing begins, scoped
	// to dates strictly before the run's start) or emitted during this run.
	// It must be tracked in memory rather than re-queried per day because a
	// global MAX(snapshot_date) lookup can return a date in the future of the
	// day being processed, which would suppress every historical annual
	// snapshot the run is meant to write.
	LastSnapshot time.Time
}

func newChunkState() *chunkState {
	return &chunkState{
		PendingAdd:      make(map[string]int),
		PendingRemove:   make(map[string]int),
		EarlyEntryGrace: make(map[string]time.Time),
	}
}

// processChunk executes the universe computation for one chunk of trading days.
// It loads source data once, iterates days, computes the universe, diffs against the
// prior state, and emits observations to `out`. The prior state is loaded from the DB
// at chunk start and maintained in memory across the chunk's days. The chunkState
// carries pending add/remove counters and grace periods across chunk boundaries.
func processChunk(
	ctx context.Context,
	pool *pgxpool.Pool,
	sub *library.Subscription,
	indexTicker string,
	tradingDays []time.Time,
	candidates []*data.Asset,
	state *chunkState,
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
	figiByTicker := make(map[string]string, len(candidates))
	assetByFigi := make(map[string]*data.Asset, len(candidates))

	for i, a := range candidates {
		figis[i] = a.CompositeFigi
		figiByTicker[a.Ticker] = a.CompositeFigi
		assetByFigi[a.CompositeFigi] = a
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

	pendingAdd := state.PendingAdd
	pendingRemove := state.PendingRemove
	earlyEntryGrace := state.EarlyEntryGrace

	// prevMcap carries forward market caps across days so that FIGIs with
	// gaps in metrics data retain their last known value.
	prevMcap := make(map[string]int64)

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

		todayMcap, err := loadMarketCapAsOf(ctx, pool, figis, d)
		if err != nil {
			return fmt.Errorf("load market cap for %s: %w", d.Format("2006-01-02"), err)
		}

		// Merge today's values into the carry-forward map.
		for figi, mc := range todayMcap {
			prevMcap[figi] = mc
		}

		mcapByFigi := prevMcap

		broadMcaps, err := loadBroadMarketCaps(ctx, pool, d)
		if err != nil {
			return fmt.Errorf("load broad mcaps for %s: %w", d.Format("2006-01-02"), err)
		}

		if len(broadMcaps) < minBroadMcaps {
			logger.Warn().
				Time("date", d).
				Int("count", len(broadMcaps)).
				Int("minRequired", minBroadMcaps).
				Msg("metrics data incomplete for date; skipping pvindex day")

			continue
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

		// Single filter chain call -- returns per-stock metrics without
		// applying size/liquidity/price thresholds.
		result := computeUniverseForDate(input)

		// The size cutoff is computed from the broad market; all candidates
		// share the same value. Use entry percentile for the entry cutoff.
		sizeCutoffEntry := percentileInt64(broadMcaps, sizePercentileEntry)
		sizeCutoffRemoval := percentileInt64(broadMcaps, sizePercentileRemoval)

		// Apply entry thresholds (strict) to determine new candidates.
		entryState, earlyEntryFlags := applyThresholds(
			result.Candidates,
			sizeCutoffEntry,
			liquidityTurnoverEntry,
			priceFloorEntry,
		)

		// Apply removal thresholds (lenient) to determine retention.
		retainState, _ := applyThresholds(
			result.Candidates,
			sizeCutoffRemoval,
			liquidityTurnoverRemoval,
			priceFloorRemoval,
		)

		// Update pending counters.
		for ticker := range entryState {
			if _, inPrev := prevState[ticker]; !inPrev {
				pendingAdd[ticker]++
				delete(pendingRemove, ticker)
			} else {
				delete(pendingRemove, ticker)
				delete(pendingAdd, ticker)
			}
		}

		for ticker := range prevState {
			if _, inEntry := entryState[ticker]; inEntry {
				continue
			}

			if _, inRetain := retainState[ticker]; inRetain {
				delete(pendingRemove, ticker)
				continue
			}

			if graceExpiry, ok := earlyEntryGrace[ticker]; ok && d.Before(graceExpiry) {
				continue
			}

			pendingRemove[ticker]++
			delete(pendingAdd, ticker)
		}

		// Promote pending adds that have reached the buffer threshold.
		confirmedAdds := make(map[string]provider.IndexMember)

		for ticker, count := range pendingAdd {
			if count >= bufferDays {
				confirmedAdds[ticker] = entryState[ticker]
				prevState[ticker] = entryState[ticker]
				delete(pendingAdd, ticker)

				if earlyEntryFlags[ticker] {
					earlyEntryGrace[ticker] = graceExpiryDate(ctx, pool, d)
				}
			}
		}

		// Promote pending removes that have reached the buffer threshold.
		confirmedRemoves := make(map[string]provider.IndexMember)

		for ticker, count := range pendingRemove {
			if count >= bufferDays {
				confirmedRemoves[ticker] = prevState[ticker]
				delete(prevState, ticker)
				delete(pendingRemove, ticker)
			}
		}

		// Hard disqualification: bypass the buffer for stocks that are
		// clearly no longer tradable (delisted on or before d, or price
		// collapse below $1).
		for ticker := range pendingRemove {
			figi := figiByTicker[ticker]

			hardRemove := false
			if !assetAliveOn(assetByFigi[figi], d) {
				hardRemove = true
			} else {
				price := mostRecentClose(eodByFigi[figi], d)
				if price > 0 && price < hardDisqualifyPrice {
					hardRemove = true
				}
			}

			if hardRemove {
				confirmedRemoves[ticker] = prevState[ticker]
				delete(prevState, ticker)
				delete(pendingRemove, ticker)
			}
		}

		// On the very first day, prevState is empty so seed it from entryState.
		if len(prevState) == 0 && len(entryState) > 0 {
			for ticker, m := range entryState {
				prevState[ticker] = m

				if earlyEntryFlags[ticker] {
					earlyEntryGrace[ticker] = graceExpiryDate(ctx, pool, d)
				}
			}

			pendingAdd = make(map[string]int)
		}

		// Recompute current-day cap weights for add entries.
		// prevState already reflects the post-change universe (adds in, removes out).
		if len(confirmedAdds) > 0 {
			addCaps := make(map[string]int64, len(prevState))
			for _, m := range prevState {
				if mc, ok := mcapByFigi[m.CompositeFigi]; ok && mc > 0 {
					addCaps[m.CompositeFigi] = mc
				}
			}

			addWeights := assignCapWeights(addCaps)

			for ticker, m := range confirmedAdds {
				m.Weight = addWeights[m.CompositeFigi]
				confirmedAdds[ticker] = m
			}
		}

		obsTemplate := &data.Observation{
			ObservationDate:  d,
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}

		// Window-replace: clear any prior changelog/snapshot rows for d before
		// emitting fresh ones, so re-runs converge even when the new run emits
		// a different set of events than a previous run did (e.g. previously
		// missing metrics now loaded). Without this, upsert can reconcile
		// same-key value drift but cannot remove orphan rows the new run no
		// longer emits.
		if err := provider.DeleteIndexRange(ctx, pool, sub.DataTablesMap[data.IndexSnapshotKey], sub.DataTablesMap[data.IndexChangelogKey], indexTicker, d, d); err != nil {
			return fmt.Errorf("clear prior index rows for %s on %s: %w", indexTicker, d.Format("2006-01-02"), err)
		}

		provider.EmitChangelog(confirmedAdds, confirmedRemoves, indexTicker, d, obsTemplate, out)

		// Emit annual snapshot on the first trading day on or after December 21
		// (winter solstice), or on cold start. state.LastSnapshot is tracked
		// in memory and updated below when a snapshot is emitted; querying the
		// DB here would return a global MAX that may be in the future of d
		// (e.g. when re-running history with a recent snapshot already in the
		// table) and would suppress every historical snapshot.
		if provider.ShouldTakeAnnualSnapshot(state.LastSnapshot, d, snapshotMonth, snapshotDay) {
			// Recompute cap weights for all current members using today's market caps.
			snapshotCaps := make(map[string]int64, len(prevState))
			for _, m := range prevState {
				if mc, ok := mcapByFigi[m.CompositeFigi]; ok && mc > 0 {
					snapshotCaps[m.CompositeFigi] = mc
				}
			}

			snapshotWeights := assignCapWeights(snapshotCaps)

			constituents := make([]data.IndexConstituent, 0, len(prevState))
			for tk, m := range prevState {
				constituents = append(constituents, data.IndexConstituent{
					Ticker:        tk,
					CompositeFigi: m.CompositeFigi,
					Weight:        snapshotWeights[m.CompositeFigi],
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

			state.LastSnapshot = d
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
		   SELECT dt FROM trading_days(($1::date - INTERVAL '400 days')::date, ($1::date - INTERVAL '1 day')::date) AS t(dt)
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
