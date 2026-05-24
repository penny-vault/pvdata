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
package provider

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

// EodRow holds the fields needed to compute adjusted close prices.
type EodRow struct {
	Close       float64
	Dividend    float64
	SplitFactor float64
	AdjClose    float64
}

// ComputeAdjustedClose implements CRSP dividend/split adjustment.
// Rows must be in reverse chronological order (newest first).
// See: http://crsp.org/products/documentation/crsp-calculations
func ComputeAdjustedClose(rows []EodRow) []EodRow {
	adjustFactor := 1.0
	for i := range rows {
		rows[i].AdjClose = rows[i].Close / adjustFactor
		if rows[i].Close > 0 {
			adjustFactor *= (1 + (rows[i].Dividend / rows[i].Close)) * rows[i].SplitFactor
		} else {
			adjustFactor = 1.0
		}
	}

	return rows
}

// PurgeExpiredData is a PostFetch hook that deletes rows older than the
// dataset's TTL from all of the subscription's data tables. The TTL is
// retrieved from the dataset configuration via the subscription's provider.
func PurgeExpiredData(ctx context.Context, subscription *library.Subscription) error {
	subProvider, ok := Map[subscription.Provider]
	if !ok {
		return nil
	}

	subDataset, ok := subProvider.Datasets()[subscription.Dataset]
	if !ok || subDataset.TTL == 0 {
		return nil
	}

	cutoff := time.Now().Add(-subDataset.TTL)

	conn, err := subscription.Library.AcquireWithTimeout(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for purge: %w", err)
	}
	defer conn.Release()

	for _, tableName := range subscription.DataTables {
		tag, err := conn.Exec(ctx,
			fmt.Sprintf("DELETE FROM %s WHERE event_date < $1", tableName),
			cutoff,
		)
		if err != nil {
			log.Error().Err(err).Str("Table", tableName).Msg("purge expired data failed")
			continue
		}

		if tag.RowsAffected() > 0 {
			log.Info().Str("Table", tableName).Int64("Deleted", tag.RowsAffected()).Time("Cutoff", cutoff).Msg("purged expired data")
		}
	}

	return nil
}

// adjustEodWorkerCount returns the parallelism used by AdjustEodPrices.
// Override via db.adjust_workers. Default 8 — comfortably under the
// 25-conn pool default so other queries are not starved.
func adjustEodWorkerCount() int {
	n := viper.GetInt("db.adjust_workers")
	if n <= 0 {
		return 8
	}

	return n
}

// adjustEodFigi runs the CRSP adjustment for a single figi: SELECT every
// row in reverse chronological order, compute, and write the adj_close
// values back via a single UNNEST update. Each call acquires its own
// pool connection so callers may invoke it concurrently.
func adjustEodFigi(ctx context.Context, lib *library.Library, tableName, figi string) error {
	conn, err := lib.AcquireWithTimeout(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for %s: %w", figi, err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx,
		fmt.Sprintf("SELECT event_date, close, dividend, split_factor FROM %s WHERE composite_figi = $1 ORDER BY event_date DESC", tableName), figi)
	if err != nil {
		return fmt.Errorf("query eod for %s: %w", figi, err)
	}

	type eodWithDate struct {
		EventDate time.Time
		EodRow
	}

	var records []eodWithDate

	for rows.Next() {
		var r eodWithDate
		if err := rows.Scan(&r.EventDate, &r.Close, &r.Dividend, &r.SplitFactor); err != nil {
			rows.Close()
			return fmt.Errorf("scan eod row for %s: %w", figi, err)
		}

		records = append(records, r)
	}

	rows.Close()

	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate eod rows for %s: %w", figi, err)
	}

	if len(records) == 0 {
		return nil
	}

	eodRows := make([]EodRow, len(records))
	for i, r := range records {
		eodRows[i] = r.EodRow
	}

	ComputeAdjustedClose(eodRows)

	dates := make([]time.Time, len(records))
	adjs := make([]float64, len(records))

	for i, r := range records {
		dates[i] = r.EventDate
		adjs[i] = eodRows[i].AdjClose
	}

	if _, err := conn.Exec(ctx,
		fmt.Sprintf(`UPDATE %s SET adj_close = u.adj
		FROM unnest($1::date[], $2::float8[]) AS u(d, adj)
		WHERE composite_figi = $3 AND event_date = u.d`, tableName),
		dates, adjs, figi); err != nil {
		return fmt.Errorf("batch update adj_close for %s: %w", figi, err)
	}

	return nil
}

// AdjustEodPrices is a PostFetch hook that computes adjusted close prices
// for all assets in the subscription's EOD table. Figis are processed
// concurrently by a worker pool; each worker takes its own pool
// connection. Worker count is configurable via db.adjust_workers.
func AdjustEodPrices(ctx context.Context, subscription *library.Subscription) error {
	tableName := subscription.DataTablesMap[data.EODKey]
	if tableName == "" {
		return nil
	}

	log.Info().Str("Table", tableName).Msg("adjusting EOD prices")

	figis, err := loadDistinctFigis(ctx, subscription.Library, tableName)
	if err != nil {
		return err
	}

	if len(figis) == 0 {
		log.Info().Str("Table", tableName).Msg("no figis to adjust")
		return nil
	}

	workers := min(adjustEodWorkerCount(), len(figis))

	log.Info().
		Str("Table", tableName).
		Int("Assets", len(figis)).
		Int("Workers", workers).
		Msg("starting EOD price adjustment")

	started := time.Now()
	heartbeat := rate.Sometimes{Interval: 15 * time.Second}

	var processed atomic.Int64

	jobs := make(chan string)
	g, gctx := errgroup.WithContext(ctx)

	for range workers {
		g.Go(func() error {
			for figi := range jobs {
				if err := adjustEodFigi(gctx, subscription.Library, tableName, figi); err != nil {
					return err
				}

				done := processed.Add(1)

				heartbeat.Do(func() {
					elapsed := time.Since(started)
					pct := float64(done) / float64(len(figis)) * 100

					var eta time.Duration
					if done > 0 {
						eta = time.Duration(float64(elapsed) * float64(int64(len(figis))-done) / float64(done))
					}

					log.Info().
						Str("Table", tableName).
						Int64("processed", done).
						Int("total", len(figis)).
						Str("progress", fmt.Sprintf("%.1f%%", pct)).
						Str("elapsed", elapsed.Round(time.Second).String()).
						Str("eta", eta.Round(time.Second).String()).
						Msg("EOD price adjustment in progress")
				})
			}

			return nil
		})
	}

	g.Go(func() error {
		defer close(jobs)

		for _, figi := range figis {
			select {
			case <-gctx.Done():
				return gctx.Err()
			case jobs <- figi:
			}
		}

		return nil
	})

	if err := g.Wait(); err != nil {
		return fmt.Errorf("eod price adjustment: %w", err)
	}

	log.Info().
		Str("Table", tableName).
		Int("Assets", len(figis)).
		Str("elapsed", time.Since(started).Round(time.Second).String()).
		Msg("EOD price adjustment complete")

	return nil
}

// loadDistinctFigis returns the set of composite_figis present in
// tableName, used to drive the per-figi adjustment loop.
func loadDistinctFigis(ctx context.Context, lib *library.Library, tableName string) ([]string, error) {
	conn, err := lib.AcquireWithTimeout(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection for figi list: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx, fmt.Sprintf("SELECT DISTINCT composite_figi FROM %s", tableName))
	if err != nil {
		return nil, fmt.Errorf("query distinct figis: %w", err)
	}
	defer rows.Close()

	var figis []string

	for rows.Next() {
		var figi string
		if err := rows.Scan(&figi); err != nil {
			return nil, fmt.Errorf("scan figi: %w", err)
		}

		figis = append(figis, figi)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate figis: %w", err)
	}

	return figis, nil
}
