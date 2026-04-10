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
)

// minMetricsCoverage is the minimum number of distinct FIGIs with market cap
// data on a single date before we consider the metrics data sufficient to
// start computing the index. This avoids starting during the ramp-up period
// when only a handful of stocks have coverage.
const minMetricsCoverage = 1000

// computeDateRange returns the [start, end] date range for the universe computation:
// start = 200 trading days after the first date where metrics has >= minMetricsCoverage stocks
// end = LEAST(MAX(eod.event_date), MAX(metrics.event_date))
func computeDateRange(ctx context.Context, pool *pgxpool.Pool) (time.Time, time.Time, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("acquire conn for computeDateRange: %w", err)
	}
	defer conn.Release()

	// Find the earliest date where metrics has meaningful coverage.
	var metricsReady time.Time
	if err := conn.QueryRow(ctx,
		`SELECT MIN(event_date) FROM (
			SELECT event_date, COUNT(DISTINCT composite_figi) AS cnt
			FROM metrics
			WHERE market_cap > 0
			GROUP BY event_date
			HAVING COUNT(DISTINCT composite_figi) >= $1
		) t`, minMetricsCoverage,
	).Scan(&metricsReady); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("query metrics ready date: %w", err)
	}

	var maxMetric, maxEod time.Time

	if err := conn.QueryRow(ctx, `SELECT MAX(event_date) FROM metrics`).Scan(&maxMetric); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("query metric max date: %w", err)
	}

	if err := conn.QueryRow(ctx, `SELECT MAX(event_date) FROM eod`).Scan(&maxEod); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("query eod max date: %w", err)
	}

	// Find the 200th trading day after metricsReady.
	var startDate time.Time
	if err := conn.QueryRow(ctx,
		`SELECT dt FROM trading_days($1::date, ($1::date + INTERVAL '400 days')::date) AS t(dt)
		 ORDER BY dt LIMIT 1 OFFSET 199`,
		metricsReady,
	).Scan(&startDate); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("compute start date: %w", err)
	}

	endDate := maxMetric
	if maxEod.Before(endDate) {
		endDate = maxEod
	}

	return startDate, endDate, nil
}

// chunkTradingDays loads trading days in [start, end] from the database and splits them
// into fixed-size chunks. Each chunk is a slice of trading days.
func chunkTradingDays(ctx context.Context, pool *pgxpool.Pool, start, end time.Time, chunkSize int) ([][]time.Time, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn for chunkTradingDays: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx, `SELECT dt FROM trading_days($1::date, $2::date) AS t(dt) ORDER BY dt`, start, end)
	if err != nil {
		return nil, fmt.Errorf("query trading days: %w", err)
	}
	defer rows.Close()

	var allDays []time.Time

	for rows.Next() {
		var dt time.Time
		if err := rows.Scan(&dt); err != nil {
			return nil, fmt.Errorf("scan trading day: %w", err)
		}

		allDays = append(allDays, dt)
	}

	if len(allDays) == 0 {
		return nil, nil
	}

	chunks := make([][]time.Time, 0, (len(allDays)/chunkSize)+1)
	for i := 0; i < len(allDays); i += chunkSize {
		end := i + chunkSize
		if end > len(allDays) {
			end = len(allDays)
		}

		chunks = append(chunks, allDays[i:end])
	}

	return chunks, nil
}
