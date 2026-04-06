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

// PERange finds records where PE is non-zero and outside the range (0, 1000].
type PERange struct{}

func (c *PERange) Name() string        { return "pe_range" }
func (c *PERange) Description() string { return "PE must be between 0 and 1000 (when non-zero)" }
func (c *PERange) Phase() CheckPhase   { return PhaseAudit }
func (c *PERange) Severity() CheckSeverity {
	return SeverityInfo
}
func (c *PERange) DataTypes() []string { return []string{"fundamental"} }

func (c *PERange) Audit(ctx context.Context, pool *pgxpool.Pool, table string, lastChecked *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer conn.Release()

	baseQuery := fmt.Sprintf(`
		SELECT ticker, composite_figi, dimension, event_date, pe
		FROM %s
		WHERE pe != 0 AND (pe < 0 OR pe > 1000)`, table)

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
			pe            float64
		)

		if err := rows.Scan(&ticker, &compositeFigi, &dimension, &eventDate, &pe); err != nil {
			return nil, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      SeverityInfo,
			Ticker:        ticker,
			CompositeFigi: compositeFigi,
			Dimension:     dimension,
			EventDate:     eventDate,
			Field:         "pe",
			Message:       "pe is outside expected range (0, 1000]",
			Expected:      "0 < pe <= 1000",
			Actual:        fmt.Sprintf("%g", pe),
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
