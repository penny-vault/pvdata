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

// AssetsChange detects ARQ records where total_assets changed more than 5x quarter-over-quarter.
type AssetsChange struct{}

func (c *AssetsChange) Name() string        { return "assets_change" }
func (c *AssetsChange) Description() string { return "TotalAssets changed > 5x quarter-over-quarter" }
func (c *AssetsChange) Phase() CheckPhase   { return PhaseAudit }
func (c *AssetsChange) Severity() CheckSeverity {
	return SeverityWarning
}
func (c *AssetsChange) DataTypes() []string { return []string{"fundamental"} }

func (c *AssetsChange) Audit(ctx context.Context, pool *pgxpool.Pool, table string, lastChecked *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer conn.Release()

	baseQuery := fmt.Sprintf(`
		SELECT ticker, composite_figi, dimension, event_date, total_assets, prev_assets
		FROM (
			SELECT
				ticker,
				composite_figi,
				dimension,
				event_date,
				total_assets,
				LAG(total_assets) OVER (PARTITION BY composite_figi, dimension ORDER BY event_date) AS prev_assets
			FROM %s
			WHERE dimension = 'ARQ'
		) sub
		WHERE prev_assets IS NOT NULL
		  AND prev_assets != 0
		  AND total_assets != 0
		  AND ABS(total_assets::float / prev_assets::float) > 5`, table)

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
			totalAssets   int64
			prevAssets    int64
		)

		if err := rows.Scan(&ticker, &compositeFigi, &dimension, &eventDate, &totalAssets, &prevAssets); err != nil {
			return nil, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      SeverityWarning,
			Ticker:        ticker,
			CompositeFigi: compositeFigi,
			Dimension:     dimension,
			EventDate:     eventDate,
			Field:         "total_assets",
			Message:       "total_assets changed > 5x quarter-over-quarter",
			Expected:      fmt.Sprintf("within 5x of %d", prevAssets),
			Actual:        fmt.Sprintf("%d", totalAssets),
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
