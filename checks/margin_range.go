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

// MarginRange finds records where gross_margin, ebitda_margin, or profit_margin is outside [-1, 1]
// (and non-zero).
type MarginRange struct{}

func (c *MarginRange) Name() string { return "margin_range" }
func (c *MarginRange) Description() string {
	return "Margin fields must be within -1 to 1 range (when non-zero)"
}
func (c *MarginRange) Phase() CheckPhase { return PhaseAudit }
func (c *MarginRange) Severity() CheckSeverity {
	return SeverityWarning
}
func (c *MarginRange) DataTypes() []string { return []string{"fundamental"} }

type marginRow struct {
	ticker        string
	compositeFigi string
	dimension     string
	eventDate     time.Time
	grossMargin   float64
	ebitdaMargin  float64
	profitMargin  float64
}

func (c *MarginRange) Audit(ctx context.Context, pool *pgxpool.Pool, table string, lastChecked *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer conn.Release()

	baseQuery := fmt.Sprintf(`
		SELECT ticker, composite_figi, dimension, event_date, gross_margin, ebitda_margin, profit_margin
		FROM %s
		WHERE (gross_margin != 0 AND (gross_margin < -1 OR gross_margin > 1))
		   OR (ebitda_margin != 0 AND (ebitda_margin < -1 OR ebitda_margin > 1))
		   OR (profit_margin != 0 AND (profit_margin < -1 OR profit_margin > 1))`, table)

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
		var row marginRow

		if err := rows.Scan(
			&row.ticker,
			&row.compositeFigi,
			&row.dimension,
			&row.eventDate,
			&row.grossMargin,
			&row.ebitdaMargin,
			&row.profitMargin,
		); err != nil {
			return nil, err
		}

		marginFields := []struct {
			name  string
			value float64
		}{
			{"gross_margin", row.grossMargin},
			{"ebitda_margin", row.ebitdaMargin},
			{"profit_margin", row.profitMargin},
		}

		for _, mf := range marginFields {
			if mf.value != 0 && (mf.value < -1 || mf.value > 1) {
				results = append(results, CheckResult{
					CheckName:     c.Name(),
					Severity:      SeverityWarning,
					Ticker:        row.ticker,
					CompositeFigi: row.compositeFigi,
					Dimension:     row.dimension,
					EventDate:     row.eventDate,
					Field:         mf.name,
					Message:       fmt.Sprintf("%s is outside expected range [-1, 1]", mf.name),
					Expected:      "-1 <= value <= 1",
					Actual:        fmt.Sprintf("%g", mf.value),
					DataType:      "fundamental",
				})
			}
		}
	}

	return results, rows.Err()
}
