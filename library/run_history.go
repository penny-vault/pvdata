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
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog/log"
)

// StatusToString converts a StatusType to its string representation.
func StatusToString(s data.StatusType) string {
	switch s {
	case data.RunSuccess:
		return "success"
	case data.RunInProgress:
		return "running"
	default:
		return "failed"
	}
}

// RunHistoryEntry represents a row from the run_history table.
type RunHistoryEntry struct {
	ID              string    `json:"id"`
	SubscriptionID  string    `json:"subscription_id"`
	StartTime       time.Time `json:"start_time"`
	EndTime         time.Time `json:"end_time"`
	NumObservations int       `json:"num_observations"`
	Status          string    `json:"status"`
	CreatedOn       time.Time `json:"created_on"`
}

// SparklineData holds a single day's aggregated observation count.
type SparklineData struct {
	Date            string `json:"date"`
	NumObservations int    `json:"num_observations"`
}

// SaveRunHistory persists a RunSummary to the run_history table and updates
// the subscription's stats (last_run, num_records_last_import, total_records,
// first_obs_date, last_obs_date). Returns the inserted run_history id.
func (myLibrary *Library) SaveRunHistory(ctx context.Context, summary data.RunSummary) error {
	_, err := myLibrary.InsertRunHistory(ctx, summary)

	return err
}

// InsertRunHistory inserts a RunSummary and returns the new run_history id.
// Use UpdateRunLog to attach captured log output once it is fully drained.
func (myLibrary *Library) InsertRunHistory(ctx context.Context, summary data.RunSummary) (string, error) {
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Release()

	var runID string

	err = conn.QueryRow(ctx,
		`INSERT INTO run_history (subscription_id, start_time, end_time, num_observations, status)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING id::text`,
		summary.SubscriptionID, summary.StartTime, summary.EndTime, summary.NumObservations,
		StatusToString(summary.Status),
	).Scan(&runID)
	if err != nil {
		return "", err
	}

	log.Info().
		Str("SubscriptionID", summary.SubscriptionID.String()).
		Str("SubscriptionName", summary.SubscriptionName).
		Int("NumObservations", summary.NumObservations).
		Str("Status", StatusToString(summary.Status)).
		Msg("saved run history")

	if summary.Status == data.RunSuccess {
		if err := myLibrary.updateSubscriptionStats(ctx, conn, summary); err != nil {
			log.Error().Err(err).Str("SubscriptionID", summary.SubscriptionID.String()).Msg("failed to update subscription stats")
		}
	}

	return runID, nil
}

// UpdateRunLog attaches captured log output to a run_history row that has
// already been inserted. Use this after draining the LogCapture buffer at
// the end of a run so the persisted log includes post-fetch hooks,
// healthcheck pings, and the final completion line.
func (myLibrary *Library) UpdateRunLog(ctx context.Context, runID, runLog string) error {
	if runID == "" || runLog == "" {
		return nil
	}

	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	_, err = conn.Exec(ctx,
		`UPDATE run_history SET log = $1 WHERE id = $2`,
		runLog, runID,
	)

	return err
}

// BeginRun inserts a run_history row with status='running' and
// num_observations=0 and returns its id. end_time is initialised
// to start_time as a placeholder; FinalizeRun overwrites it on
// completion.
func (myLibrary *Library) BeginRun(ctx context.Context, summary data.RunSummary) (string, error) {
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Release()

	var runID string

	err = conn.QueryRow(ctx,
		`INSERT INTO run_history (subscription_id, start_time, end_time, num_observations, status)
		VALUES ($1, $2, $2, 0, 'running')
		RETURNING id::text`,
		summary.SubscriptionID, summary.StartTime,
	).Scan(&runID)
	if err != nil {
		return "", err
	}

	log.Info().
		Str("SubscriptionID", summary.SubscriptionID.String()).
		Str("SubscriptionName", summary.SubscriptionName).
		Str("RunID", runID).
		Msg("started run")

	return runID, nil
}

// UpdateRunProgress overwrites num_observations on a running row.
// No-op when runID is empty so callers don't have to branch when
// BeginRun failed. The UPDATE is filtered by status='running', so
// late ticks that arrive after FinalizeRun has flipped the status
// silently affect zero rows and return nil — by design, callers
// don't need to coordinate the throttle's last flush against the
// finaliser.
func (myLibrary *Library) UpdateRunProgress(ctx context.Context, runID string, numObservations int) error {
	if runID == "" {
		return nil
	}

	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	_, err = conn.Exec(ctx,
		`UPDATE run_history SET num_observations = $1
		 WHERE id = $2 AND status = 'running'`,
		numObservations, runID,
	)

	return err
}

// FinalizeRun updates a running run_history row to its terminal
// state. On success it also refreshes subscription stats. When
// runID is empty it falls back to InsertRunHistory so callers
// that didn't use BeginRun still record a row.
func (myLibrary *Library) FinalizeRun(ctx context.Context, runID string, summary data.RunSummary) error {
	if runID == "" {
		_, err := myLibrary.InsertRunHistory(ctx, summary)

		return err
	}

	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	_, err = conn.Exec(ctx,
		`UPDATE run_history
		 SET end_time = $1, num_observations = $2, status = $3
		 WHERE id = $4`,
		summary.EndTime, summary.NumObservations, StatusToString(summary.Status), runID,
	)
	if err != nil {
		return err
	}

	log.Info().
		Str("SubscriptionID", summary.SubscriptionID.String()).
		Str("SubscriptionName", summary.SubscriptionName).
		Int("NumObservations", summary.NumObservations).
		Str("Status", StatusToString(summary.Status)).
		Str("RunID", runID).
		Msg("finalised run")

	if summary.Status == data.RunSuccess {
		if err := myLibrary.updateSubscriptionStats(ctx, conn, summary); err != nil {
			log.Error().Err(err).Str("SubscriptionID", summary.SubscriptionID.String()).Msg("failed to update subscription stats")
		}
	}

	return nil
}

// MarkAbandonedRunsFailed transitions every run_history row still
// in status='running' to 'failed'. Any in-flight goroutines were
// lost with the previous process so these rows are permanently
// abandoned. end_time is set to now() — i.e., server restart time
// — which is a conservative upper bound on the actual run
// duration; the goroutine could have died any time between
// start_time and now(), and we don't have a more accurate
// timestamp to record. Returns the number of rows updated.
func (myLibrary *Library) MarkAbandonedRunsFailed(ctx context.Context) (int64, error) {
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()

	tag, err := conn.Exec(ctx,
		`UPDATE run_history
		 SET status = 'failed', end_time = now()
		 WHERE status = 'running'`,
	)
	if err != nil {
		return 0, err
	}

	return tag.RowsAffected(), nil
}

// updateSubscriptionStats updates a subscription's stats after a successful run.
func (myLibrary *Library) updateSubscriptionStats(ctx context.Context, conn *pgxpool.Conn, summary data.RunSummary) error {
	sub, err := myLibrary.SubscriptionFromID(ctx, summary.SubscriptionID.String())
	if err != nil {
		return fmt.Errorf("load subscription for stats update: %w", err)
	}

	// Count total records and date range across all data tables.
	var totalRecords int64

	for _, tableName := range sub.DataTables {
		var count int64

		err := conn.QueryRow(ctx, fmt.Sprintf("SELECT COUNT(*) FROM %s", tableName)).Scan(&count)
		if err != nil {
			log.Warn().Err(err).Str("table", tableName).Msg("could not count records in table")
			continue
		}

		totalRecords += count
	}

	_, err = conn.Exec(ctx,
		`UPDATE subscriptions
		 SET last_run = $1, num_records_last_import = $2, total_records = $3
		 WHERE id = $4`,
		summary.EndTime, summary.NumObservations, totalRecords, summary.SubscriptionID,
	)
	if err != nil {
		return fmt.Errorf("update subscription stats: %w", err)
	}

	log.Info().
		Str("SubscriptionID", summary.SubscriptionID.String()).
		Int64("TotalRecords", totalRecords).
		Int("NumRecordsLastImport", summary.NumObservations).
		Msg("updated subscription stats")

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
		`SELECT id::text, subscription_id::text, start_time, end_time,
		num_observations, status, created_on
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

// RunHistoryLog returns the captured log text for a single run_history row,
// addressed by its UUID. An empty string is returned when no log is stored
// (either the run pre-dates the log column or the 30-day retention has
// already cleared it).
func (myLibrary *Library) RunHistoryLog(ctx context.Context, runID string) (string, error) {
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return "", err
	}
	defer conn.Release()

	var runLog *string

	err = conn.QueryRow(ctx,
		`SELECT log FROM run_history WHERE id = $1`,
		runID,
	).Scan(&runLog)
	if err != nil {
		return "", err
	}

	if runLog == nil {
		return "", nil
	}

	return *runLog, nil
}

// SweepRunLogs nulls out captured log text on run_history rows older than
// retention. Returns the number of rows whose log was cleared.
func (myLibrary *Library) SweepRunLogs(ctx context.Context, retention time.Duration) (int64, error) {
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return 0, err
	}
	defer conn.Release()

	cutoff := time.Now().Add(-retention)

	tag, err := conn.Exec(ctx,
		`UPDATE run_history SET log = NULL
		 WHERE log IS NOT NULL AND created_on < $1`,
		cutoff,
	)
	if err != nil {
		return 0, err
	}

	return tag.RowsAffected(), nil
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
