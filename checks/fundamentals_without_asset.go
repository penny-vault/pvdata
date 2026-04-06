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

// FundamentalsWithoutAsset finds fundamentals records whose composite_figi doesn't exist in
// the assets view.
type FundamentalsWithoutAsset struct{}

func (c *FundamentalsWithoutAsset) Name() string { return "fundamentals_without_asset" }
func (c *FundamentalsWithoutAsset) Description() string {
	return "Fundamentals records with no corresponding asset record"
}
func (c *FundamentalsWithoutAsset) Phase() CheckPhase { return PhaseAudit }
func (c *FundamentalsWithoutAsset) Severity() CheckSeverity {
	return SeverityError
}
func (c *FundamentalsWithoutAsset) DataTypes() []string { return []string{"fundamental"} }

func (c *FundamentalsWithoutAsset) Audit(ctx context.Context, pool *pgxpool.Pool, table string, lastChecked *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}

	defer conn.Release()

	// Check that assets view exists before querying.
	var assetsExists bool

	err = conn.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM information_schema.views
			WHERE table_name = 'assets'
		)`).Scan(&assetsExists)
	if err != nil {
		return nil, err
	}

	if !assetsExists {
		return nil, nil
	}

	baseQuery := fmt.Sprintf(`
		SELECT DISTINCT f.ticker, f.composite_figi
		FROM %s f
		LEFT JOIN assets a ON f.composite_figi = a.composite_figi
		WHERE a.composite_figi IS NULL`, table)

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
		)

		if err := rows.Scan(&ticker, &compositeFigi); err != nil {
			return nil, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      SeverityError,
			Ticker:        ticker,
			CompositeFigi: compositeFigi,
			Field:         "composite_figi",
			Message:       "composite_figi not found in assets",
			Expected:      "composite_figi present in assets view",
			Actual:        compositeFigi,
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
