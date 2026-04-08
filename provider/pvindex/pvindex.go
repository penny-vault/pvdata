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
	"strconv"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
)

const (
	defaultIndexTicker = "us-tradable"
	defaultChunkSize   = 63
)

// Pvindex is a derived provider that computes investable universes by reading from
// the canonical published views (eod, metrics, assets) rather than fetching from an
// external source.
type Pvindex struct{}

func init() {
	provider.Register("pvindex", &Pvindex{})
}

func (p *Pvindex) Name() string {
	return "pvindex"
}

func (p *Pvindex) Description() string {
	return "Derived index provider that computes investable universes from canonical EOD, metric, and asset views."
}

func (p *Pvindex) ConfigDescription() map[string]string {
	return map[string]string{
		"index_ticker":        "Optional. Override the index ticker (default: us-tradable).",
		"start_date_override": "Optional. Force a later start date in YYYY-MM-DD format. Used for testing or selective backfill.",
		"chunk_size_days":     "Optional. Number of trading days per processing chunk (default: 63).",
	}
}

func (p *Pvindex) Datasets() map[string]provider.Dataset {
	return map[string]provider.Dataset{
		"US Tradable Universe": {
			Name:        "US Tradable Universe",
			Description: "Daily-recomputed investable universe of US common stocks: structural + liquidity + size + price filters with annual cap-weighted snapshots.",
			DataTypes: []*data.DataType{
				data.DataTypes[data.IndexSnapshotKey],
				data.DataTypes[data.IndexChangelogKey],
			},
			DateRange: func() (time.Time, time.Time) {
				// Stub: real implementation lands in Task 11.
				return time.Date(1999, 4, 30, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			TTL:   0,
			Fetch: fetchTradableUniverse,
		},
	}
}

func fetchTradableUniverse(ctx context.Context, sub *library.Subscription, out chan<- *data.Observation, exit chan<- data.RunSummary) {
	logger := zerolog.Ctx(ctx)
	pool := sub.Library.Pool

	runSummary := data.RunSummary{
		StartTime:        time.Now(),
		SubscriptionID:   sub.ID,
		SubscriptionName: sub.Name,
		Status:           data.RunSuccess,
	}

	defer func() {
		runSummary.EndTime = time.Now()
		exit <- runSummary
	}()

	indexTicker := defaultIndexTicker
	if v := sub.Config["index_ticker"]; v != "" {
		indexTicker = v
	}

	chunkSize := defaultChunkSize

	if v := sub.Config["chunk_size_days"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			chunkSize = n
		}
	}

	startDate, endDate, err := computeDateRange(ctx, pool)
	if err != nil {
		logger.Error().Err(err).Msg("compute date range failed")

		runSummary.Status = data.RunFailed

		return
	}

	// Honor start_date_override.
	if v := sub.Config["start_date_override"]; v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil && t.After(startDate) {
			startDate = t
		}
	}

	// Incremental: skip dates already covered.
	highWater := provider.LastSnapshotDate(ctx, pool, sub.DataTablesMap[data.IndexSnapshotKey], indexTicker)
	if !highWater.IsZero() && highWater.After(startDate) {
		startDate = highWater.AddDate(0, 0, 1)
	}

	if startDate.After(endDate) {
		logger.Info().Msg("no new dates to process")
		return
	}

	candidates, err := loadCandidateAssets(ctx, pool)
	if err != nil {
		logger.Error().Err(err).Msg("load candidate assets failed")

		runSummary.Status = data.RunFailed

		return
	}

	chunks, err := chunkTradingDays(ctx, pool, startDate, endDate, chunkSize)
	if err != nil {
		logger.Error().Err(err).Msg("chunk trading days failed")

		runSummary.Status = data.RunFailed

		return
	}

	totalObs := 0

	for _, chunk := range chunks {
		if err := processChunk(ctx, pool, sub, indexTicker, chunk, candidates, out); err != nil {
			logger.Error().Err(err).Time("chunk_start", chunk[0]).Msg("process chunk failed")

			runSummary.Status = data.RunFailed

			return
		}

		totalObs += len(chunk)
	}

	runSummary.NumObservations = totalObs
}
