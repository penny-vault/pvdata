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
package library

import (
	"context"

	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog/log"
)

// StatusToString converts a StatusType to its string representation.
func StatusToString(s data.StatusType) string {
	if s == data.RunSuccess {
		return "success"
	}

	return "failed"
}

// RunHistoryEntry represents a row from the run_history table.
type RunHistoryEntry struct {
	ID              string `json:"id"`
	SubscriptionID  string `json:"subscription_id"`
	StartTime       string `json:"start_time"`
	EndTime         string `json:"end_time"`
	NumObservations int    `json:"num_observations"`
	Status          string `json:"status"`
	CreatedOn       string `json:"created_on"`
}

// SparklineData holds a single day's aggregated observation count.
type SparklineData struct {
	Date            string `json:"date"`
	NumObservations int    `json:"num_observations"`
}

// SaveRunHistory persists a RunSummary to the run_history table.
func (myLibrary *Library) SaveRunHistory(ctx context.Context, summary data.RunSummary) error {
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	_, err = conn.Exec(ctx,
		`INSERT INTO run_history (subscription_id, start_time, end_time, num_observations, status)
		VALUES ($1, $2, $3, $4, $5)`,
		summary.SubscriptionID, summary.StartTime, summary.EndTime, summary.NumObservations, StatusToString(summary.Status),
	)
	if err != nil {
		return err
	}

	log.Info().
		Str("SubscriptionID", summary.SubscriptionID.String()).
		Str("SubscriptionName", summary.SubscriptionName).
		Int("NumObservations", summary.NumObservations).
		Str("Status", StatusToString(summary.Status)).
		Msg("saved run history")

	return nil
}

// RunHistory returns paginated run history entries for a subscription.
// It returns the entries, the total count of matching rows, and any error.
func (myLibrary *Library) RunHistory(ctx context.Context, subscriptionID string, limit, offset int) ([]RunHistoryEntry, int, error) {
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return nil, 0, err
	}
	defer conn.Release()

	pattern := subscriptionID + "%"

	var total int

	err = conn.QueryRow(ctx,
		`SELECT count(*) FROM run_history WHERE subscription_id::text LIKE $1`,
		pattern,
	).Scan(&total)
	if err != nil {
		return nil, 0, err
	}

	rows, err := conn.Query(ctx,
		`SELECT id::text, subscription_id::text, start_time::text, end_time::text,
		num_observations, status, created_on::text
		FROM run_history
		WHERE subscription_id::text LIKE $1
		ORDER BY start_time DESC
		LIMIT $2 OFFSET $3`,
		pattern, limit, offset,
	)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var entries []RunHistoryEntry

	for rows.Next() {
		var entry RunHistoryEntry

		err = rows.Scan(&entry.ID, &entry.SubscriptionID, &entry.StartTime, &entry.EndTime,
			&entry.NumObservations, &entry.Status, &entry.CreatedOn)
		if err != nil {
			return nil, 0, err
		}

		entries = append(entries, entry)
	}

	if err = rows.Err(); err != nil {
		return nil, 0, err
	}

	return entries, total, nil
}

// RunHistorySparkline returns daily aggregated observation counts for the last 30 days.
func (myLibrary *Library) RunHistorySparkline(ctx context.Context, subscriptionID string) ([]SparklineData, error) {
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	pattern := subscriptionID + "%"

	rows, err := conn.Query(ctx,
		`SELECT start_time::date::text AS date, sum(num_observations)::int AS num_observations
		FROM run_history
		WHERE subscription_id::text LIKE $1
		AND start_time >= now() - interval '30 days'
		GROUP BY start_time::date
		ORDER BY start_time::date`,
		pattern,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var sparkline []SparklineData

	for rows.Next() {
		var entry SparklineData

		err = rows.Scan(&entry.Date, &entry.NumObservations)
		if err != nil {
			return nil, err
		}

		sparkline = append(sparkline, entry)
	}

	if err = rows.Err(); err != nil {
		return nil, err
	}

	return sparkline, nil
}
