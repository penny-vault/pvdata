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

// MetricFundamentalAgree compares PE between fundamentals and metrics views. Flags records where
// both are non-zero and differ by > 10%. Requires both views to exist.
type MetricFundamentalAgree struct{}

func (c *MetricFundamentalAgree) Name() string { return "metric_fundamental_agree" }
func (c *MetricFundamentalAgree) Description() string {
	return "PE in fundamentals and metrics views must agree within 10%"
}
func (c *MetricFundamentalAgree) Phase() CheckPhase { return PhaseAudit }
func (c *MetricFundamentalAgree) Severity() CheckSeverity {
	return SeverityWarning
}
func (c *MetricFundamentalAgree) DataTypes() []string { return []string{"fundamental", "metric"} }

func (c *MetricFundamentalAgree) Audit(ctx context.Context, pool *pgxpool.Pool, _ string, lastChecked *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer conn.Release()

	// Check that both views exist.
	var fundamentalsExists, metricsExists bool

	err = conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.views
			WHERE table_name = 'fundamentals'
		)`).Scan(&fundamentalsExists)
	if err != nil {
		return nil, err
	}

	err = conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.views
			WHERE table_name = 'metrics'
		)`).Scan(&metricsExists)
	if err != nil {
		return nil, err
	}

	if !fundamentalsExists || !metricsExists {
		return nil, nil
	}

	baseQuery := `
		SELECT f.ticker, f.composite_figi, f.dimension, f.event_date, f.pe AS fund_pe, m.pe AS metric_pe
		FROM fundamentals f
		JOIN metrics m ON f.composite_figi = m.composite_figi AND f.event_date = m.event_date
		WHERE f.pe != 0 AND m.pe != 0
		  AND ABS(f.pe - m.pe) / ABS(f.pe) > 0.10`

	var args []interface{}

	if lastChecked != nil {
		baseQuery += " AND f.event_date > $1"

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
			fundPE        float64
			metricPE      float64
		)

		if err := rows.Scan(&ticker, &compositeFigi, &dimension, &eventDate, &fundPE, &metricPE); err != nil {
			return nil, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      SeverityWarning,
			Ticker:        ticker,
			CompositeFigi: compositeFigi,
			Dimension:     dimension,
			EventDate:     eventDate,
			Field:         "pe",
			Message:       "PE in fundamentals and metrics differ by > 10%",
			Expected:      fmt.Sprintf("within 10%% of fundamentals PE %g", fundPE),
			Actual:        fmt.Sprintf("%g", metricPE),
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
