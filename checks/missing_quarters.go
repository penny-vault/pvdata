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

// MissingQuarters uses a LEAD window function to find gaps > 120 days between consecutive
// ARQ event_dates for the same composite_figi.
type MissingQuarters struct{}

func (c *MissingQuarters) Name() string { return "missing_quarters" }
func (c *MissingQuarters) Description() string {
	return "Detects gaps > 120 days between consecutive ARQ event_dates"
}
func (c *MissingQuarters) Phase() CheckPhase { return PhaseAudit }
func (c *MissingQuarters) Severity() CheckSeverity {
	return SeverityError
}
func (c *MissingQuarters) DataTypes() []string { return []string{"fundamental"} }

func (c *MissingQuarters) Audit(ctx context.Context, pool *pgxpool.Pool, table string, lastChecked *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer conn.Release()

	baseQuery := fmt.Sprintf(`
		SELECT ticker, composite_figi, event_date, next_event_date, gap_days
		FROM (
			SELECT
				ticker,
				composite_figi,
				event_date,
				LEAD(event_date) OVER (PARTITION BY composite_figi ORDER BY event_date) AS next_event_date,
				LEAD(event_date) OVER (PARTITION BY composite_figi ORDER BY event_date) - event_date AS gap_days
			FROM %s
			WHERE dimension = 'ARQ'
		) sub
		WHERE next_event_date IS NOT NULL
		  AND gap_days > 120`, table)

	var args []any

	if lastChecked != nil {
		baseQuery += " AND event_date > $1"

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
			eventDate     time.Time
			nextEventDate time.Time
			gapDays       int
		)

		if err := rows.Scan(&ticker, &compositeFigi, &eventDate, &nextEventDate, &gapDays); err != nil {
			return nil, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      SeverityError,
			Ticker:        ticker,
			CompositeFigi: compositeFigi,
			Dimension:     "ARQ",
			EventDate:     eventDate,
			Field:         "event_date",
			Message:       fmt.Sprintf("gap of %d days between consecutive ARQ quarters", gapDays),
			Expected:      "<= 120 days gap",
			Actual:        fmt.Sprintf("%d days until %s", gapDays, nextEventDate.Format("2006-01-02")),
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
