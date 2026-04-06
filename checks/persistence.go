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

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func SaveResults(ctx context.Context, pool *pgxpool.Pool, results []CheckResult, subscriptionID uuid.UUID, runID uuid.UUID) error {
	if len(results) == 0 {
		return nil
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}

	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		_ = tx.Rollback(ctx)
	}()

	for _, r := range results {
		_, err := tx.Exec(ctx,
			`INSERT INTO data_quality_issues
			(check_name, severity, data_type, ticker, composite_figi, dimension,
			 event_date, field, message, expected, actual, subscription_id, run_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			r.CheckName, r.Severity.String(), r.DataType, r.Ticker, r.CompositeFigi,
			r.Dimension, r.EventDate, r.Field, r.Message, r.Expected, r.Actual,
			subscriptionID, runID,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}
