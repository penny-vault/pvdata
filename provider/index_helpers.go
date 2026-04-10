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
	"strings"
	"time"

	"github.com/adrg/strutil"
	"github.com/adrg/strutil/metrics"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// IndexMember represents a constituent of an index with its FIGI and weight.
type IndexMember struct {
	CompositeFigi string
	Weight        float64
}

const WeightChangeThreshold = 0.01

// ShouldTakeSnapshot returns true if a new snapshot should be taken based on the
// configured frequency, the date of the last snapshot, and the current processing date.
func ShouldTakeSnapshot(lastSnapshotDate, currentDate time.Time, frequency string) bool {
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

// DiffSnapshots compares current holdings against previous holdings
// and returns maps of added, removed, and weight-changed tickers.
// A weight change is significant when the absolute difference exceeds WeightChangeThreshold.
func DiffSnapshots(current, previous map[string]IndexMember) (added, removed, weightChanged map[string]IndexMember) {
	added = make(map[string]IndexMember)
	removed = make(map[string]IndexMember)
	weightChanged = make(map[string]IndexMember)

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

		if delta >= WeightChangeThreshold-1e-9 {
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

// DiffOptions configures DiffSnapshotsWithThreshold weight-change detection.
// A weight is considered changed when |delta| >= max(AbsoluteThreshold, prev.Weight * RelativeThreshold).
// If RelativeThreshold is 0, only the absolute threshold applies. If both thresholds are
// zero (the struct zero value), every non-zero weight delta is reported as a change —
// callers should set at least one threshold explicitly.
type DiffOptions struct {
	AbsoluteThreshold float64 // absolute weight delta required (e.g., 0.01)
	RelativeThreshold float64 // fraction of previous weight (e.g., 0.25 = 25%)
}

// DiffSnapshotsWithThreshold compares current holdings against previous holdings using
// configurable thresholds for weight-change detection. Adds and removes are reported
// regardless of threshold settings.
func DiffSnapshotsWithThreshold(current, previous map[string]IndexMember, opts DiffOptions) (added, removed, weightChanged map[string]IndexMember) {
	added = make(map[string]IndexMember)
	removed = make(map[string]IndexMember)
	weightChanged = make(map[string]IndexMember)

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

		threshold := opts.AbsoluteThreshold

		relThreshold := prev.Weight * opts.RelativeThreshold
		if relThreshold > threshold {
			threshold = relThreshold
		}

		// Skip unchanged weights; the 1e-9 epsilon tolerates floating-point
		// representation noise at the threshold boundary, matching DiffSnapshots.
		if delta > 0 && delta >= threshold-1e-9 {
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

// LastSnapshotDate queries the database for the most recent snapshot date for the given index.
func LastSnapshotDate(ctx context.Context, pool *pgxpool.Pool, table, indexTicker string) time.Time {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire db connection for lastSnapshotDate")
		return time.Time{}
	}
	defer conn.Release()

	var snapshotDate time.Time

	sql := fmt.Sprintf(`SELECT COALESCE(MAX(snapshot_date), '0001-01-01') FROM %s WHERE index_ticker = $1`, table)

	err = conn.QueryRow(ctx, sql, indexTicker).Scan(&snapshotDate)
	if err != nil {
		log.Error().Err(err).Msg("could not query last snapshot date")
		return time.Time{}
	}

	return snapshotDate
}

// PreviousSnapshotTickers queries the database for all tickers in the most recent
// snapshot for the given index name, returning a map of ticker->compositeFigi.
func PreviousSnapshotTickers(ctx context.Context, pool *pgxpool.Pool, table, indexTicker string) map[string]string {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire db connection for previousSnapshotTickers")
		return map[string]string{}
	}
	defer conn.Release()

	sql := fmt.Sprintf(`SELECT ticker, composite_figi FROM %s
		WHERE index_ticker = $1 AND snapshot_date = (
			SELECT MAX(snapshot_date) FROM %s WHERE index_ticker = $1
		)`, table, table)

	rows, err := conn.Query(ctx, sql, indexTicker)
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

// CurrentIndexMembers reconstructs the true index membership as of a given date
// by taking the most recent snapshot on or before asOfDate and applying all
// changelog entries (adds, removes, weight-changes) between that snapshot and asOfDate.
func CurrentIndexMembers(ctx context.Context, pool *pgxpool.Pool, snapshotTable, changelogTable, indexTicker string, asOfDate time.Time) map[string]IndexMember {
	if pool == nil {
		return map[string]IndexMember{}
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire db connection for currentIndexMembers")
		return map[string]IndexMember{}
	}
	defer conn.Release()

	// Get the most recent snapshot on or before asOfDate
	var snapshotDate time.Time

	err = conn.QueryRow(ctx,
		fmt.Sprintf(`SELECT COALESCE(MAX(snapshot_date), '0001-01-01') FROM %s WHERE index_ticker = $1 AND snapshot_date <= $2`, snapshotTable),
		indexTicker, asOfDate,
	).Scan(&snapshotDate)
	if err != nil {
		log.Error().Err(err).Msg("could not query snapshot date for currentIndexMembers")
		return map[string]IndexMember{}
	}

	result := make(map[string]IndexMember)

	if !snapshotDate.IsZero() {
		// Load the snapshot (single row with JSONB constituents)
		var constituents []data.IndexConstituent

		err = conn.QueryRow(ctx,
			fmt.Sprintf(`SELECT constituents FROM %s WHERE index_ticker = $1 AND snapshot_date = $2`, snapshotTable),
			indexTicker, snapshotDate,
		).Scan(&constituents)
		if err != nil {
			log.Error().Err(err).Msg("could not query snapshot for currentIndexMembers")
			return map[string]IndexMember{}
		}

		for _, c := range constituents {
			result[c.Ticker] = IndexMember{CompositeFigi: c.CompositeFigi, Weight: c.Weight}
		}
	}

	// Apply changelog entries after the snapshot date up to asOfDate
	changeRows, err := conn.Query(ctx,
		fmt.Sprintf(`SELECT ticker, composite_figi, action, weight FROM %s WHERE index_ticker = $1 AND event_date > $2 AND event_date <= $3 ORDER BY event_date`, changelogTable),
		indexTicker, snapshotDate, asOfDate,
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
			result[ticker] = IndexMember{CompositeFigi: figi, Weight: weight}
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

// EmitWeightChanges emits IndexChange observations with "weight-change" action.
func EmitWeightChanges(changes map[string]IndexMember, indexTicker string, eventDate time.Time, subscription *data.Observation, out chan<- *data.Observation) {
	for ticker, member := range changes {
		out <- &data.Observation{
			IndexChange: &data.IndexChange{
				Ticker:        ticker,
				CompositeFigi: member.CompositeFigi,
				IndexTicker:   indexTicker,
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

// TradingDays returns NYSE trading days between start and end (inclusive)
// by calling the database's trading_days(DATE, DATE) function.
func TradingDays(ctx context.Context, pool *pgxpool.Pool, start, end time.Time) ([]time.Time, error) {
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

// EmitChangelog emits IndexChange observations for adds and removes.
func EmitChangelog(adds, removes map[string]IndexMember, indexTicker string, eventDate time.Time, subscription *data.Observation, out chan<- *data.Observation) {
	for ticker, member := range adds {
		out <- &data.Observation{
			IndexChange: &data.IndexChange{
				Ticker:        ticker,
				CompositeFigi: member.CompositeFigi,
				IndexTicker:   indexTicker,
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
				IndexTicker:   indexTicker,
				EventDate:     eventDate,
				Action:        "remove",
			},
			ObservationDate:  subscription.ObservationDate,
			SubscriptionID:   subscription.SubscriptionID,
			SubscriptionName: subscription.SubscriptionName,
		}
	}
}

const JaroWinklerThreshold = 0.85

// ResolveShareClass checks if a ticker ending in A or B corresponds to an
// internal asset with a "/" separator (e.g. BRKB -> BRK/B). The match is
// verified by comparing the holding name against the internal asset
// name using Jaro-Winkler similarity. On success it populates figiMap for
// the ticker and returns true.
func ResolveShareClass(ticker, holdingName string, figiMap map[string]string, assetNameMap map[string]string, logger *zerolog.Logger) bool {
	if len(ticker) < 2 {
		return false
	}

	lastChar := ticker[len(ticker)-1]
	if lastChar != 'A' && lastChar != 'B' {
		return false
	}

	candidate := ticker[:len(ticker)-1] + "/" + string(lastChar)

	f := figiMap[candidate]
	if f == "" {
		return false
	}

	candidateName := assetNameMap[candidate]
	if candidateName == "" || holdingName == "" {
		return false
	}

	similarity := strutil.Similarity(
		strings.ToLower(holdingName),
		strings.ToLower(candidateName),
		metrics.NewJaroWinkler(),
	)

	if similarity < JaroWinklerThreshold {
		// Fallback: if the first two words match after normalization, accept
		// the match. Handles cases like "U-Haul Holding Company" vs
		// "U HAUL NON VOTING SERIES N" where suffixes diverge but the core
		// company name is the same.
		if !FirstWordsMatch(holdingName, candidateName, 2) {
			logger.Debug().
				Str("ISharesTicker", ticker).
				Str("CandidateTicker", candidate).
				Str("HoldingName", holdingName).
				Str("AssetName", candidateName).
				Float64("Similarity", similarity).
				Msg("share class candidate rejected -- name similarity too low")

			return false
		}
	}

	logger.Info().
		Str("ISharesTicker", ticker).
		Str("ResolvedTicker", candidate).
		Float64("Similarity", similarity).
		Msg("resolved share class ticker via name match")

	figiMap[ticker] = f

	return true
}

// FirstWordsMatch normalizes two names (lowercase, remove hyphens) and checks
// whether their first n words are identical.
func FirstWordsMatch(a, b string, n int) bool {
	normalize := func(s string) []string {
		s = strings.ToLower(s)
		s = strings.ReplaceAll(s, "-", " ")

		return strings.Fields(s)
	}

	wa := normalize(a)
	wb := normalize(b)

	if len(wa) < n || len(wb) < n {
		return false
	}

	for i := range n {
		if wa[i] != wb[i] {
			return false
		}
	}

	return true
}
