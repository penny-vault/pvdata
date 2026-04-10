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
	"github.com/rs/zerolog/log"
)

// loadCandidateAssets reads all rows from the `assets` view and returns those passing
// the structural filter (active, CS, exchange whitelist, not LP).
func loadCandidateAssets(ctx context.Context, pool *pgxpool.Pool) ([]*data.Asset, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn for loadCandidateAssets: %w", err)
	}
	defer conn.Release()

	all, err := data.ActiveAssets(ctx, conn, "assets")
	if err != nil {
		return nil, fmt.Errorf("load active assets: %w", err)
	}

	filtered := filterAssetMaster(all)

	log.Debug().
		Int("total_active", len(all)).
		Int("after_structural_filter", len(filtered)).
		Msg("loaded candidate assets")

	return filtered, nil
}

// loadEodChunk reads EOD rows for the given FIGIs over [start, end] inclusive from
// the `eod` published view. Returns a map keyed by composite_figi.
func loadEodChunk(ctx context.Context, pool *pgxpool.Pool, figis []string, start, end time.Time) (map[string][]eodRow, error) {
	if len(figis) == 0 {
		return map[string][]eodRow{}, nil
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn for loadEodChunk: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx,
		`SELECT composite_figi, event_date, close, volume
		 FROM eod
		 WHERE event_date BETWEEN $1 AND $2
		   AND composite_figi = ANY($3)
		 ORDER BY composite_figi, event_date`,
		start, end, figis,
	)
	if err != nil {
		return nil, fmt.Errorf("query eod chunk: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]eodRow)

	for rows.Next() {
		var (
			figi   string
			date   time.Time
			closeP float64
			volume float64
		)

		if err := rows.Scan(&figi, &date, &closeP, &volume); err != nil {
			return nil, fmt.Errorf("scan eod row: %w", err)
		}

		out[figi] = append(out[figi], eodRow{Date: date, Close: closeP, Volume: volume})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate eod rows: %w", err)
	}

	return out, nil
}

// loadMarketCapAsOf reads the most recent market_cap on or before `asOf` for each FIGI
// in the input. Used to populate per-day market cap lookups within a chunk.
func loadMarketCapAsOf(ctx context.Context, pool *pgxpool.Pool, figis []string, asOf time.Time) (map[string]int64, error) {
	if len(figis) == 0 {
		return map[string]int64{}, nil
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn for loadMarketCapAsOf: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx,
		`SELECT DISTINCT ON (composite_figi) composite_figi, market_cap
		 FROM metrics
		 WHERE composite_figi = ANY($1)
		   AND event_date <= $2
		   AND market_cap > 0
		 ORDER BY composite_figi, event_date DESC`,
		figis, asOf,
	)
	if err != nil {
		return nil, fmt.Errorf("query market_cap: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int64)

	for rows.Next() {
		var (
			figi string
			mcap int64
		)

		if err := rows.Scan(&figi, &mcap); err != nil {
			return nil, fmt.Errorf("scan metric row: %w", err)
		}

		out[figi] = mcap
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate metric rows: %w", err)
	}

	return out, nil
}

// loadBroadMarketCaps returns the market caps of all CS rows on the broad pool baseline:
// active CS on whitelisted exchanges with a metric row on or before `asOf`. Used as the
// percentile baseline for the size and early-entry filters.
func loadBroadMarketCaps(ctx context.Context, pool *pgxpool.Pool, asOf time.Time) ([]int64, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn for loadBroadMarketCaps: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx,
		`WITH latest AS (
		   SELECT DISTINCT ON (m.composite_figi) m.composite_figi, m.market_cap
		   FROM metrics m
		   JOIN assets a USING (composite_figi)
		   WHERE a.active = true
		     AND a.asset_type = 'CS'
		     AND a.primary_exchange IN ('NASDAQ','NYSE','NYSE MKT','NYSE ARCA','BATS','AMEX','XNAS','XNYS','XASE','ARCX')
		     AND m.event_date <= $1
		     AND m.market_cap > 0
		   ORDER BY m.composite_figi, m.event_date DESC
		 )
		 SELECT market_cap FROM latest`,
		asOf,
	)
	if err != nil {
		return nil, fmt.Errorf("query broad market caps: %w", err)
	}
	defer rows.Close()

	out := make([]int64, 0, 5000)

	for rows.Next() {
		var mcap int64
		if err := rows.Scan(&mcap); err != nil {
			return nil, fmt.Errorf("scan broad mcap row: %w", err)
		}

		out = append(out, mcap)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate broad mcap rows: %w", err)
	}

	return out, nil
}
