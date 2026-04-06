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

// EodWithoutFundamentals finds tickers with recent EOD data but no recent fundamentals (6 months).
// Requires both 'eod' and 'fundamentals' views to exist; returns nil if either is absent.
type EodWithoutFundamentals struct{}

func (c *EodWithoutFundamentals) Name() string { return "eod_without_fundamentals" }
func (c *EodWithoutFundamentals) Description() string {
	return "Tickers with recent EOD data but no recent fundamentals"
}
func (c *EodWithoutFundamentals) Phase() CheckPhase { return PhaseAudit }
func (c *EodWithoutFundamentals) Severity() CheckSeverity {
	return SeverityWarning
}
func (c *EodWithoutFundamentals) DataTypes() []string { return []string{"fundamental", "eod"} }

func (c *EodWithoutFundamentals) Audit(ctx context.Context, pool *pgxpool.Pool, _ string, lastChecked *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer conn.Release()

	// Check that both views exist before querying.
	var eodExists, fundamentalsExists bool

	err = conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.views
			WHERE table_name = 'eod'
		)`).Scan(&eodExists)
	if err != nil {
		return nil, err
	}

	err = conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.views
			WHERE table_name = 'fundamentals'
		)`).Scan(&fundamentalsExists)
	if err != nil {
		return nil, err
	}

	if !eodExists || !fundamentalsExists {
		return nil, nil
	}

	baseQuery := `
		SELECT e.ticker, e.composite_figi
		FROM (
			SELECT ticker, composite_figi, MAX(event_date) AS latest_eod
			FROM eod
			GROUP BY ticker, composite_figi
		) e
		LEFT JOIN (
			SELECT composite_figi, MAX(event_date) AS latest_fundamental
			FROM fundamentals
			WHERE dimension = 'ARQ'
			GROUP BY composite_figi
		) f ON e.composite_figi = f.composite_figi
		WHERE e.latest_eod >= now() - INTERVAL '6 months'
		  AND (f.latest_fundamental IS NULL OR f.latest_fundamental < now() - INTERVAL '6 months')`

	var args []any

	if lastChecked != nil {
		baseQuery += fmt.Sprintf(" AND e.latest_eod > $%d", len(args)+1)
		args = append(args, *lastChecked)
	}

	rows, err := conn.Query(ctx, baseQuery, args...)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var results []CheckResult

	for rows.Next() {
		var (
			ticker        string
			compositeFigi string
		)

		if err := rows.Scan(&ticker, &compositeFigi); err != nil {
			return nil, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      SeverityWarning,
			Ticker:        ticker,
			CompositeFigi: compositeFigi,
			Field:         "event_date",
			Message:       "ticker has recent EOD data but no recent fundamentals",
			Expected:      "fundamentals within last 6 months",
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
