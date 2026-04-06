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

// RevenueChange detects ARQ records where revenue changed more than 10x quarter-over-quarter.
type RevenueChange struct{}

func (c *RevenueChange) Name() string        { return "revenue_change" }
func (c *RevenueChange) Description() string { return "Revenue changed > 10x quarter-over-quarter" }
func (c *RevenueChange) Phase() CheckPhase   { return PhaseAudit }
func (c *RevenueChange) Severity() CheckSeverity {
	return SeverityWarning
}
func (c *RevenueChange) DataTypes() []string { return []string{"fundamental"} }

func (c *RevenueChange) Audit(ctx context.Context, pool *pgxpool.Pool, table string, lastChecked *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer conn.Release()

	baseQuery := fmt.Sprintf(`
		SELECT ticker, composite_figi, dimension, event_date, revenues, prev_revenues
		FROM (
			SELECT
				ticker,
				composite_figi,
				dimension,
				event_date,
				revenues,
				LAG(revenues) OVER (PARTITION BY composite_figi, dimension ORDER BY event_date) AS prev_revenues
			FROM %s
			WHERE dimension = 'ARQ'
		) sub
		WHERE prev_revenues IS NOT NULL
		  AND prev_revenues != 0
		  AND revenues != 0
		  AND ABS(revenues::float / prev_revenues::float) > 10`, table)

	var args []interface{}

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
			dimension     string
			eventDate     time.Time
			revenues      int64
			prevRevenues  int64
		)

		if err := rows.Scan(&ticker, &compositeFigi, &dimension, &eventDate, &revenues, &prevRevenues); err != nil {
			return nil, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      SeverityWarning,
			Ticker:        ticker,
			CompositeFigi: compositeFigi,
			Dimension:     dimension,
			EventDate:     eventDate,
			Field:         "revenues",
			Message:       "revenue changed > 10x quarter-over-quarter",
			Expected:      fmt.Sprintf("within 10x of %d", prevRevenues),
			Actual:        fmt.Sprintf("%d", revenues),
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
