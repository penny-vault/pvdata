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

// StaleData groups ARQ records by ticker/composite_figi and flags those whose MAX(event_date)
// is older than 6 months. Always performs a full scan regardless of lastChecked.
type StaleData struct{}

func (c *StaleData) Name() string        { return "stale_data" }
func (c *StaleData) Description() string { return "Fundamentals not updated in > 6 months" }
func (c *StaleData) Phase() CheckPhase   { return PhaseAudit }
func (c *StaleData) Severity() CheckSeverity {
	return SeverityWarning
}
func (c *StaleData) DataTypes() []string { return []string{"fundamental"} }

func (c *StaleData) Audit(ctx context.Context, pool *pgxpool.Pool, table string, _ *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer conn.Release()

	query := fmt.Sprintf(`
		SELECT ticker, composite_figi, MAX(event_date) AS latest_date
		FROM %s
		WHERE dimension = 'ARQ'
		GROUP BY ticker, composite_figi
		HAVING MAX(event_date) < now() - INTERVAL '6 months'`, table)

	rows, err := conn.Query(ctx, query)
	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var results []CheckResult

	for rows.Next() {
		var (
			ticker        string
			compositeFigi string
			latestDate    time.Time
		)

		if err := rows.Scan(&ticker, &compositeFigi, &latestDate); err != nil {
			return nil, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      SeverityWarning,
			Ticker:        ticker,
			CompositeFigi: compositeFigi,
			Dimension:     "ARQ",
			EventDate:     latestDate,
			Field:         "event_date",
			Message:       "no ARQ data in > 6 months",
			Expected:      "event_date within last 6 months",
			Actual:        latestDate.Format("2006-01-02"),
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
