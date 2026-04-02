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

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog/log"
)

// indexMember represents a constituent of an index with its FIGI and weight.
type indexMember struct {
	CompositeFigi string
	Weight        float64
}

const weightChangeThreshold = 0.01

// shouldTakeSnapshot returns true if a new snapshot should be taken based on the
// configured frequency, the date of the last snapshot, and the current processing date.
func shouldTakeSnapshot(lastSnapshotDate, currentDate time.Time, frequency string) bool {
	if lastSnapshotDate.IsZero() {
		return true
	}

	var interval time.Duration

	switch frequency {
	case "daily":
		interval = 24 * time.Hour
	case "weekly":
		interval = 7 * 24 * time.Hour
	case "monthly":
		interval = 30 * 24 * time.Hour
	case "quarterly":
		interval = 90 * 24 * time.Hour
	case "yearly":
		interval = 365 * 24 * time.Hour
	default:
		interval = 7 * 24 * time.Hour
	}

	return currentDate.Sub(lastSnapshotDate) >= interval
}

// diffSnapshots compares current holdings against previous holdings
// and returns maps of added, removed, and weight-changed tickers.
// A weight change is significant when the absolute difference exceeds weightChangeThreshold.
func diffSnapshots(current, previous map[string]indexMember) (added, removed, weightChanged map[string]indexMember) {
	added = make(map[string]indexMember)
	removed = make(map[string]indexMember)
	weightChanged = make(map[string]indexMember)

	for ticker, member := range current {
		prev, ok := previous[ticker]
		if !ok {
			added[ticker] = member
			continue
		}

		delta := member.Weight - prev.Weight
		if delta < 0 {
			delta = -delta
		}

		if delta >= weightChangeThreshold-1e-9 {
			weightChanged[ticker] = member
		}
	}

	for ticker, member := range previous {
		if _, ok := current[ticker]; !ok {
			removed[ticker] = member
		}
	}

	return
}

// lastSnapshotDate queries the database for the most recent snapshot date for the given index.
func lastSnapshotDate(ctx context.Context, pool *pgxpool.Pool, table, indexName string) time.Time {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire db connection for lastSnapshotDate")
		return time.Time{}
	}
	defer conn.Release()

	var snapshotDate time.Time

	sql := fmt.Sprintf(`SELECT COALESCE(MAX(snapshot_date), '0001-01-01') FROM %s_snapshot WHERE index_name = $1`, table)

	err = conn.QueryRow(ctx, sql, indexName).Scan(&snapshotDate)
	if err != nil {
		log.Error().Err(err).Msg("could not query last snapshot date")
		return time.Time{}
	}

	return snapshotDate
}

// previousSnapshotTickers queries the database for all tickers in the most recent
// snapshot for the given index name, returning a map of ticker->compositeFigi.
func previousSnapshotTickers(ctx context.Context, pool *pgxpool.Pool, table, indexName string) map[string]string {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire db connection for previousSnapshotTickers")
		return map[string]string{}
	}
	defer conn.Release()

	sql := fmt.Sprintf(`SELECT ticker, composite_figi FROM %s_snapshot
		WHERE index_name = $1 AND snapshot_date = (
			SELECT MAX(snapshot_date) FROM %s_snapshot WHERE index_name = $1
		)`, table, table)

	rows, err := conn.Query(ctx, sql, indexName)
	if err != nil {
		log.Error().Err(err).Msg("could not query previous snapshot tickers")
		return map[string]string{}
	}
	defer rows.Close()

	result := make(map[string]string)

	for rows.Next() {
		var ticker, figi string
		if err := rows.Scan(&ticker, &figi); err != nil {
			log.Error().Err(err).Msg("error scanning previous snapshot row")
			continue
		}

		result[ticker] = figi
	}

	return result
}

// currentIndexMembers reconstructs the true index membership as of a given date
// by taking the most recent snapshot on or before asOfDate and applying all
// changelog entries (adds, removes, weight-changes) between that snapshot and asOfDate.
func currentIndexMembers(ctx context.Context, pool *pgxpool.Pool, table, indexName string, asOfDate time.Time) map[string]indexMember {
	if pool == nil {
		return map[string]indexMember{}
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire db connection for currentIndexMembers")
		return map[string]indexMember{}
	}
	defer conn.Release()

	// Get the most recent snapshot on or before asOfDate
	var snapshotDate time.Time

	err = conn.QueryRow(ctx,
		fmt.Sprintf(`SELECT COALESCE(MAX(snapshot_date), '0001-01-01') FROM %s_snapshot WHERE index_name = $1 AND snapshot_date <= $2`, table),
		indexName, asOfDate,
	).Scan(&snapshotDate)
	if err != nil {
		log.Error().Err(err).Msg("could not query snapshot date for currentIndexMembers")
		return map[string]indexMember{}
	}

	result := make(map[string]indexMember)

	if !snapshotDate.IsZero() {
		// Load the snapshot (single row with JSONB constituents)
		var constituents []data.IndexConstituent

		err = conn.QueryRow(ctx,
			fmt.Sprintf(`SELECT constituents FROM %s_snapshot WHERE index_name = $1 AND snapshot_date = $2`, table),
			indexName, snapshotDate,
		).Scan(&constituents)
		if err != nil {
			log.Error().Err(err).Msg("could not query snapshot for currentIndexMembers")
			return map[string]indexMember{}
		}

		for _, c := range constituents {
			result[c.Ticker] = indexMember{CompositeFigi: c.CompositeFigi, Weight: c.Weight}
		}
	}

	// Apply changelog entries after the snapshot date up to asOfDate
	changeRows, err := conn.Query(ctx,
		fmt.Sprintf(`SELECT ticker, composite_figi, action, weight FROM %s_changelog WHERE index_name = $1 AND event_date > $2 AND event_date <= $3 ORDER BY event_date`, table),
		indexName, snapshotDate, asOfDate,
	)
	if err != nil {
		log.Error().Err(err).Msg("could not query changelog for currentIndexMembers")
		return result
	}
	defer changeRows.Close()

	for changeRows.Next() {
		var ticker, figi, action string

		var weight float64

		if err := changeRows.Scan(&ticker, &figi, &action, &weight); err != nil {
			log.Error().Err(err).Msg("error scanning changelog row in currentIndexMembers")
			continue
		}

		switch action {
		case "add":
			result[ticker] = indexMember{CompositeFigi: figi, Weight: weight}
		case "remove":
			delete(result, ticker)
		case "weight-change":
			if member, ok := result[ticker]; ok {
				member.Weight = weight
				result[ticker] = member
			}
		}
	}

	return result
}

// emitWeightChanges emits IndexChange observations with "weight-change" action.
func emitWeightChanges(changes map[string]indexMember, indexName string, eventDate time.Time, subscription *data.Observation, out chan<- *data.Observation) {
	for ticker, member := range changes {
		out <- &data.Observation{
			IndexChange: &data.IndexChange{
				Ticker:        ticker,
				CompositeFigi: member.CompositeFigi,
				IndexName:     indexName,
				EventDate:     eventDate,
				Action:        "weight-change",
				Weight:        member.Weight,
			},
			ObservationDate:  subscription.ObservationDate,
			SubscriptionID:   subscription.SubscriptionID,
			SubscriptionName: subscription.SubscriptionName,
		}
	}
}

// tradingDays returns NYSE trading days between start and end (inclusive)
// by calling the database's trading_days(DATE, DATE) function.
func tradingDays(ctx context.Context, pool *pgxpool.Pool, start, end time.Time) ([]time.Time, error) {
	if pool == nil {
		return nil, fmt.Errorf("database pool is nil")
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not acquire db connection for tradingDays: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx, `SELECT dt FROM trading_days($1::date, $2::date) AS dt ORDER BY dt`, start, end)
	if err != nil {
		return nil, fmt.Errorf("could not query trading days: %w", err)
	}
	defer rows.Close()

	var days []time.Time

	for rows.Next() {
		var dt time.Time
		if err := rows.Scan(&dt); err != nil {
			return nil, fmt.Errorf("error scanning trading day: %w", err)
		}

		days = append(days, dt)
	}

	return days, nil
}

// emitChangelog emits IndexChange observations for adds and removes.
func emitChangelog(adds, removes map[string]indexMember, indexName string, eventDate time.Time, subscription *data.Observation, out chan<- *data.Observation) {
	for ticker, member := range adds {
		out <- &data.Observation{
			IndexChange: &data.IndexChange{
				Ticker:        ticker,
				CompositeFigi: member.CompositeFigi,
				IndexName:     indexName,
				EventDate:     eventDate,
				Action:        "add",
				Weight:        member.Weight,
			},
			ObservationDate:  subscription.ObservationDate,
			SubscriptionID:   subscription.SubscriptionID,
			SubscriptionName: subscription.SubscriptionName,
		}
	}

	for ticker, member := range removes {
		out <- &data.Observation{
			IndexChange: &data.IndexChange{
				Ticker:        ticker,
				CompositeFigi: member.CompositeFigi,
				IndexName:     indexName,
				EventDate:     eventDate,
				Action:        "remove",
			},
			ObservationDate:  subscription.ObservationDate,
			SubscriptionID:   subscription.SubscriptionID,
			SubscriptionName: subscription.SubscriptionName,
		}
	}
}
