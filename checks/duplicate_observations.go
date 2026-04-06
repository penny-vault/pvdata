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

// DuplicateObservations checks for duplicate primary keys (composite_figi, dimension, event_date)
// within the table having COUNT > 1.
type DuplicateObservations struct{}

func (c *DuplicateObservations) Name() string { return "duplicate_observations" }
func (c *DuplicateObservations) Description() string {
	return "Detects duplicate primary keys (composite_figi, dimension, event_date)"
}
func (c *DuplicateObservations) Phase() CheckPhase { return PhaseAudit }
func (c *DuplicateObservations) Severity() CheckSeverity {
	return SeverityError
}
func (c *DuplicateObservations) DataTypes() []string { return []string{"fundamental"} }

func (c *DuplicateObservations) Audit(ctx context.Context, pool *pgxpool.Pool, table string, lastChecked *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer conn.Release()

	baseQuery := fmt.Sprintf(`
		SELECT ticker, composite_figi, dimension, event_date, COUNT(*) AS cnt
		FROM %s`, table)

	var args []interface{}

	if lastChecked != nil {
		baseQuery += " WHERE event_date > $1"

		args = append(args, *lastChecked)
	}

	baseQuery += `
		GROUP BY ticker, composite_figi, dimension, event_date
		HAVING COUNT(*) > 1`

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
			cnt           int
		)

		if err := rows.Scan(&ticker, &compositeFigi, &dimension, &eventDate, &cnt); err != nil {
			return nil, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      SeverityError,
			Ticker:        ticker,
			CompositeFigi: compositeFigi,
			Dimension:     dimension,
			EventDate:     eventDate,
			Field:         "composite_figi,dimension,event_date",
			Message:       fmt.Sprintf("duplicate primary key with %d rows", cnt),
			Expected:      "1 row per composite_figi/dimension/event_date",
			Actual:        fmt.Sprintf("%d rows", cnt),
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
