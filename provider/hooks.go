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
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
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

	conn, err := subscription.Library.Pool.Acquire(ctx)
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

// AdjustEodPrices is a PostFetch hook that computes adjusted close prices
// for all assets in the subscription's EOD table.
func AdjustEodPrices(ctx context.Context, subscription *library.Subscription) error {
	tableName := subscription.DataTablesMap[data.EODKey]
	if tableName == "" {
		return nil
	}

	log.Info().Str("Table", tableName).Msg("adjusting EOD prices")

	conn, err := subscription.Library.Pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for eod adjust: %w", err)
	}
	defer conn.Release()

	// Get distinct composite_figis
	figiRows, err := conn.Query(ctx, fmt.Sprintf("SELECT DISTINCT composite_figi FROM %s", tableName))
	if err != nil {
		return fmt.Errorf("query distinct figis: %w", err)
	}

	var figis []string

	for figiRows.Next() {
		var figi string
		if err := figiRows.Scan(&figi); err != nil {
			figiRows.Close()
			return err
		}

		figis = append(figis, figi)
	}

	figiRows.Close()

	if err := figiRows.Err(); err != nil {
		return fmt.Errorf("iterate figis: %w", err)
	}

	for _, figi := range figis {
		rows, err := conn.Query(ctx,
			fmt.Sprintf("SELECT event_date, close, dividend, split_factor FROM %s WHERE composite_figi = $1 ORDER BY event_date DESC", tableName), figi)
		if err != nil {
			log.Error().Err(err).Str("FIGI", figi).Msg("query eod for adjustment failed")
			continue
		}

		type eodWithDate struct {
			EventDate any
			EodRow
		}

		var records []eodWithDate

		for rows.Next() {
			var r eodWithDate
			if err := rows.Scan(&r.EventDate, &r.Close, &r.Dividend, &r.SplitFactor); err != nil {
				rows.Close()
				return err
			}

			records = append(records, r)
		}

		rows.Close()

		if err := rows.Err(); err != nil {
			return fmt.Errorf("iterate eod rows for %s: %w", figi, err)
		}

		if len(records) == 0 {
			continue
		}

		// Extract EodRows for computation
		eodRows := make([]EodRow, len(records))
		for i, r := range records {
			eodRows[i] = r.EodRow
		}

		ComputeAdjustedClose(eodRows)

		// Batch update
		tx, err := conn.Begin(ctx)
		if err != nil {
			return err
		}

		for i, r := range records {
			if _, err := tx.Exec(ctx,
				fmt.Sprintf("UPDATE %s SET adj_close = $1 WHERE composite_figi = $2 AND event_date = $3", tableName),
				eodRows[i].AdjClose, figi, r.EventDate); err != nil {
				if rbErr := tx.Rollback(ctx); rbErr != nil {
					log.Error().Err(rbErr).Msg("failed to rollback transaction")
				}

				return err
			}
		}

		if err := tx.Commit(ctx); err != nil {
			return fmt.Errorf("commit adj_close update for %s: %w", figi, err)
		}
	}

	log.Info().Str("Table", tableName).Int("Assets", len(figis)).Msg("EOD price adjustment complete")

	return nil
}
