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
package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// MissingEOD flags trading days within a ticker's expected coverage
// window where no EOD row exists. The expected window is the
// intersection of:
//
//  1. The ticker's tradable range from the assets view
//     (assets.listed → COALESCE(assets.delisted, today)).
//  2. The subscription's actual data range
//     (MIN(event_date) → MAX(event_date) in the table).
//
// Trading days are derived from the market_holidays table for the
// "NYSE" market; non-trading days (weekends + recognised market
// closures) are excluded automatically.
type MissingEOD struct{}

func (c *MissingEOD) Name() string { return "missing_eod" }
func (c *MissingEOD) Description() string {
	return "Flags trading days inside a ticker's expected coverage window with no EOD row"
}
func (c *MissingEOD) Phase() CheckPhase       { return PhaseAudit }
func (c *MissingEOD) Severity() CheckSeverity { return SeverityWarning }
func (c *MissingEOD) DataTypes() []string     { return []string{"eod"} }

// missingEODMaxFindings caps how many missing dates we surface per
// audit run. A subscription with a years-long gap could otherwise
// produce hundreds of thousands of findings; the cap forces the
// operator to investigate the root cause via a direct query.
const missingEODMaxFindings = 1000

// holidayMarket is the canonical market label for US-equity calendar
// lookups in market_holidays. Subscriptions that ingest holidays use
// this label.
const holidayMarket = "NYSE"

func (c *MissingEOD) Audit(ctx context.Context, pool *pgxpool.Pool, table string, _ *time.Time, lookback *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer conn.Release()

	// Resolve the assets view name from the registry-managed lookup
	// so this check works regardless of how publish.go has named it.
	assetsView, err := assetsViewName(ctx, conn)
	if err != nil {
		return nil, err
	}

	// Derive the subscription's coverage range from the table itself.
	// MIN/MAX(event_date) is the canonical answer to "what does this
	// dataset actually have"; first_obs_date/last_obs_date columns
	// are not reliably populated by the regular run flow.
	var subFirst, subLast time.Time

	subCovQuery := fmt.Sprintf("SELECT MIN(event_date), MAX(event_date) FROM %s", table)

	if err := conn.QueryRow(ctx, subCovQuery).Scan(&subFirst, &subLast); err != nil {
		return nil, fmt.Errorf("derive subscription coverage range: %w", err)
	}

	if subFirst.IsZero() || subLast.IsZero() {
		// Empty table; nothing to check.
		return nil, nil
	}

	// Optional lookback narrows the scan to recent dates only. This
	// keeps incremental audits cheap; pass --full to scan the full
	// range.
	scanFrom := subFirst

	if lookback != nil {
		cutoff := time.Now().UTC().Add(-*lookback)
		if cutoff.After(scanFrom) {
			scanFrom = cutoff
		}
	}

	query := fmt.Sprintf(`
		WITH ticker_ranges AS (
		  SELECT
		    a.ticker,
		    a.composite_figi,
		    GREATEST($1::date, a.listed::date)                          AS expected_start,
		    LEAST($2::date, COALESCE(a.delisted::date, $2::date))       AS expected_end
		  FROM %s a
		  WHERE a.composite_figi IS NOT NULL
		    AND a.composite_figi <> ''
		    AND a.listed IS NOT NULL
		    AND GREATEST($1::date, a.listed::date)
		      <= LEAST($2::date, COALESCE(a.delisted::date, $2::date))
		),
		trading_days AS (
		  SELECT day::date AS event_date
		  FROM generate_series($1::date, $2::date, '1 day') AS day
		  WHERE EXTRACT(DOW FROM day) NOT IN (0, 6)
		    AND NOT EXISTS (
		      SELECT 1
		      FROM market_holidays mh
		      WHERE mh.event_date::date = day::date
		        AND mh.market = $3
		        AND mh.early_close = false
		    )
		),
		expected AS (
		  SELECT t.ticker, t.composite_figi, td.event_date
		  FROM ticker_ranges t
		  JOIN trading_days td
		    ON td.event_date BETWEEN t.expected_start AND t.expected_end
		)
		SELECT e.ticker, e.composite_figi, e.event_date
		FROM expected e
		LEFT JOIN %s r
		  ON r.composite_figi = e.composite_figi
		 AND r.event_date     = e.event_date
		WHERE r.event_date IS NULL
		ORDER BY e.event_date, e.ticker
		LIMIT %d`, assetsView, table, missingEODMaxFindings)

	rows, err := conn.Query(ctx, query, scanFrom, subLast, holidayMarket)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var results []CheckResult

	for rows.Next() {
		var (
			ticker    string
			figi      string
			eventDate time.Time
		)

		if err := rows.Scan(&ticker, &figi, &eventDate); err != nil {
			return nil, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      SeverityWarning,
			Ticker:        ticker,
			CompositeFigi: figi,
			EventDate:     eventDate,
			Field:         "event_date",
			Message:       fmt.Sprintf("no EOD row in %s for trading day %s", table, eventDate.Format("2006-01-02")),
			Expected:      "1 row",
			Actual:        "0 rows",
			DataType:      "eod",
		})
	}

	return results, rows.Err()
}

// assetsViewName looks up the published view name for the asset
// description data type. Falls back to the conventional "assets" name
// if no published view is registered (which matches the default
// `pvdata publish`-built view name).
func assetsViewName(ctx context.Context, conn *pgxpool.Conn) (string, error) {
	var name string

	err := conn.QueryRow(ctx,
		`SELECT view_name FROM published_views WHERE data_type_key = 'asset-description' LIMIT 1`,
	).Scan(&name)
	if err != nil {
		// Default convention from data/datatype.go (AssetKey ViewName).
		return "assets", nil //nolint:nilerr // fallback is intentional
	}

	return name, nil
}
