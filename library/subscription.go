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
	"errors"
	"fmt"
	"os/user"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/gosimple/slug"
	"github.com/jackc/pgx/v5"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/healthcheck"
	"github.com/rs/zerolog/log"
)

type Subscription struct {
	ID   uuid.UUID `json:"id"`
	Name string    `json:"name"`

	Provider string            `json:"provider"`
	Dataset  string            `json:"dataset"`
	Config   map[string]string `json:"config"`

	DataTables    []string          `json:"data_tables"`
	DataTypes     []string          `json:"data_types"`
	DataTablesMap map[string]string `json:"data_tables_map"`
	IsPartitioned bool              `json:"is_partitioned"`

	TotalRecords         int64 `json:"total_records"`
	NumRecordsLastImport int64 `json:"num_records_last_import"`

	TotalSecurities         int64 `json:"total_securities"`
	NumSecuritiesLastImport int64 `json:"num_securities_last_import"`

	FirstObsDate time.Time `json:"first_obs_date"`
	LastObsDate  time.Time `json:"last_obs_date"`

	Schedule      string    `json:"schedule"`
	HealthCheckID string    `json:"health_check_id"`
	NextRun       time.Time `json:"next_run"`
	NextRunHuman  string    `json:"next_run_human"`
	LastRun       time.Time `json:"last_run"`
	LastRunStatus string    `json:"last_run_status" db:"last_run_status"`
	Active        bool      `json:"active"`
	SchemaVersion int       `json:"schema_version"`

	CreatedOn time.Time `json:"created_on"`
	CreatedBy string    `json:"created_by"`

	Library *Library `json:"-"`
}

type dateRange struct {
	Start int
	End   int
}

// Delete the subscription from database along with all associated tables
func (subscription *Subscription) Delete(ctx context.Context) error {
	// Before dropping tables, check published views for references. Only
	// Postgres-backed tables can be referenced by published views, so the
	// check is scoped to those.
	for idx, tblName := range subscription.DataTables {
		if subscription.dataTypeAt(idx) != nil && subscription.dataTypeAt(idx).Backend != data.BackendPostgres {
			continue
		}

		referenced, err := PublishedViewReferencesTable(ctx, subscription.Library.Pool, tblName)
		if err != nil {
			return fmt.Errorf("could not check published views: %w", err)
		}

		if referenced {
			return fmt.Errorf("cannot delete subscription: table %s is used by a published view. Run 'pvdata publish' to remove it first", tblName)
		}
	}

	conn, err := subscription.Library.AcquireWithTimeout(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Rollback(ctx); err != nil {
			if !errors.Is(err, pgx.ErrTxClosed) {
				log.Error().Err(err).Msg("error rollingback tx")
			}
		}
	}()

	// DROP TABLE on a partitioned parent automatically drops its child
	// partitions, so iterating the parent tables is sufficient for both
	// partitioned and non-partitioned data types. ClickHouse-backed tables
	// are dropped after the Postgres transaction commits so a CH outage
	// cannot strand a half-deleted subscription row.
	for idx, tblName := range subscription.DataTables {
		dt := subscription.dataTypeAt(idx)
		if dt != nil && dt.Backend != data.BackendPostgres {
			continue
		}

		log.Info().Str("TableName", tblName).Msg("delete table")

		_, err := tx.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s;", tblName))
		if err != nil {
			return err
		}
	}

	// delete subscription entry
	if _, err := tx.Exec(ctx, "DELETE FROM subscriptions WHERE id=$1", subscription.ID); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	if err := subscription.dropClickHouseTables(ctx); err != nil {
		return err
	}

	// now that all database related modification has succeeded delete any corresponding health check
	if subscription.HealthCheckID != "" {
		if err := healthcheck.Delete(subscription.HealthCheckID); err != nil {
			return err
		}
	}

	return nil
}

// Activate the subscription
func (subscription *Subscription) Activate(ctx context.Context) error {
	conn, err := subscription.Library.AcquireWithTimeout(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Rollback(ctx); err != nil {
			if !errors.Is(err, pgx.ErrTxClosed) {
				log.Error().Err(err).Msg("error rollingback tx")
			}
		}
	}()

	// activate subscription entry
	if _, err := tx.Exec(ctx, "UPDATE subscriptions SET active='t' WHERE id=$1", subscription.ID); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// now that all database related modification has succeeded resume any corresponding health check
	if subscription.HealthCheckID != "" {
		if err := healthcheck.Resume(subscription.HealthCheckID); err != nil {
			return err
		}
	}

	return nil
}

// Deactivate the subscription; all data is still saved in the database but the subscription
// is marked as inactive and it won't show up in reports
func (subscription *Subscription) Deactivate(ctx context.Context) error {
	conn, err := subscription.Library.AcquireWithTimeout(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Rollback(ctx); err != nil {
			if !errors.Is(err, pgx.ErrTxClosed) {
				log.Error().Err(err).Msg("error rollingback tx")
			}
		}
	}()

	// de-activate subscription entry
	if _, err := tx.Exec(ctx, "UPDATE subscriptions SET active='f' WHERE id=$1", subscription.ID); err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return err
	}

	// now that all database related modification has succeeded pause any corresponding health check
	if subscription.HealthCheckID != "" {
		if err := healthcheck.Pause(subscription.HealthCheckID); err != nil {
			return err
		}
	}

	return nil
}

// SaveWithTx saves the subscription using the provided transaction.
// The caller owns the transaction lifecycle (commit/rollback).
func (subscription *Subscription) SaveWithTx(ctx context.Context, tx pgx.Tx) error {
	// create table structure for each data type this dataset produces
	if err := subscription.createTables(ctx, tx); err != nil {
		return err
	}

	// make sure current user is set on subscription
	if user, err := user.Current(); err != nil {
		return err
	} else {
		subscription.CreatedBy = user.Username
	}

	// create an entry in the subscription table
	if _, err := tx.Exec(ctx, `INSERT INTO subscriptions
("id", "name", "provider", "dataset", "config", "data_tables", "data_types",
 "schedule", "health_check_id", "schema_version", "created_by")
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);`, subscription.ID.String(),
		subscription.Name, subscription.Provider, subscription.Dataset, subscription.Config,
		subscription.DataTables, subscription.DataTypes, subscription.Schedule,
		subscription.HealthCheckID, subscription.SchemaVersion, subscription.CreatedBy); err != nil {
		return err
	}

	// manage partitions
	if err := subscription.managePartitionsWithTransaction(ctx, tx); err != nil {
		return err
	}

	return nil
}

// Save the subscription to the database
func (subscription *Subscription) Save(ctx context.Context) error {
	conn, err := subscription.Library.AcquireWithTimeout(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Rollback(ctx); err != nil {
			if !errors.Is(err, pgx.ErrTxClosed) {
				log.Error().Err(err).Msg("error rollingback tx")
			}
		}
	}()

	if err := subscription.SaveWithTx(ctx, tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// Compute table names based on subscription data types
func (subscription *Subscription) ComputeTableNames() {
	ret := make([]string, len(subscription.DataTypes))

	subscription.DataTablesMap = make(map[string]string, len(subscription.DataTypes))
	for idx, dataType := range subscription.DataTypes {
		tbl := slug.Make(fmt.Sprintf("%s %s %s %s", subscription.Provider, subscription.Dataset, dataType, subscription.ID.String()[:5]))
		tbl = strings.ReplaceAll(tbl, "-", "_")
		ret[idx] = tbl

		subscription.DataTablesMap[dataType] = tbl
	}

	subscription.DataTables = ret
}

// ManagePartitions creates any new partitions needed for the subscription
func (subscription *Subscription) ManagePartitions(ctx context.Context) error {
	conn, err := subscription.Library.AcquireWithTimeout(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Rollback(ctx); err != nil {
			if !errors.Is(err, pgx.ErrTxClosed) {
				log.Error().Err(err).Msg("error rollingback tx")
			}
		}
	}()

	// manage partitions
	if err := subscription.managePartitionsWithTransaction(ctx, tx); err != nil {
		log.Error().Err(err).Msg("error encountered when creating partitions")
		return err
	}

	// commit to database
	if err := tx.Commit(ctx); err != nil {
		log.Error().Err(err).Msg("error committing manage partitions transaction")
		return err
	}

	return nil
}

// managePartitionsWithTransaction uses the specified transaction `tx` to create missing partitions
func (subscription *Subscription) managePartitionsWithTransaction(ctx context.Context, tx pgx.Tx) error {
	for idx, dataTypeName := range subscription.DataTypes {
		dataType := data.DataTypes[dataTypeName]
		dataTable := subscription.DataTables[idx]

		if dataType.Backend != data.BackendPostgres {
			continue
		}

		if !dataType.IsPartitioned {
			continue
		}

		switch dataType.PartitionInterval {
		case data.PartitionIntervalMonthly:
			if err := createMonthlyPartitions(ctx, tx, dataTable); err != nil {
				return err
			}
		default:
			if err := create5YearPartitions(ctx, tx, dataTable); err != nil {
				return err
			}
		}
	}

	return nil
}

func create5YearPartitions(ctx context.Context, tx pgx.Tx, dataTable string) error {
	dates := []dateRange{
		{Start: 1900, End: 2000},
		{Start: 2000, End: 2005},
		{Start: 2005, End: 2010},
		{Start: 2010, End: 2015},
	}

	year := time.Now().Year() + 1
	for ii := 2015; ii < year; ii += 5 {
		dates = append(dates, dateRange{Start: ii, End: ii + 5})
	}

	for _, dt := range dates {
		tableName := fmt.Sprintf("%s_%d_%d", dataTable, dt.Start, dt.End)
		sql := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%d-01-01') TO ('%d-01-01');",
			tableName, dataTable, dt.Start, dt.End)
		log.Debug().Str("SQL", sql).Msg("creating partition table")

		if _, err := tx.Exec(ctx, sql); err != nil {
			return err
		}
	}

	return nil
}

// monthlyPartitionStartYear is the earliest year covered by monthly partitions.
// Monthly intervals are reserved for high-cadence data (e.g. intraday quotes),
// so we don't pre-create the long historical tail used by 5-year partitioning.
const monthlyPartitionStartYear = 2020

// monthlyPartitionLookahead is how many months of future partitions to create
// ahead of "today" so writes near month boundaries don't fail.
const monthlyPartitionLookahead = 3

func createMonthlyPartitions(ctx context.Context, tx pgx.Tx, dataTable string) error {
	loc := time.UTC
	start := time.Date(monthlyPartitionStartYear, time.January, 1, 0, 0, 0, 0, loc)
	end := time.Now().In(loc).AddDate(0, monthlyPartitionLookahead, 0)
	endBound := time.Date(end.Year(), end.Month(), 1, 0, 0, 0, 0, loc).AddDate(0, 1, 0)

	for cur := start; cur.Before(endBound); cur = cur.AddDate(0, 1, 0) {
		next := cur.AddDate(0, 1, 0)
		tableName := fmt.Sprintf("%s_%04d_%02d", dataTable, cur.Year(), cur.Month())
		sql := fmt.Sprintf("CREATE TABLE IF NOT EXISTS %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s');",
			tableName, dataTable, cur.Format("2006-01-02"), next.Format("2006-01-02"))
		log.Debug().Str("SQL", sql).Msg("creating monthly partition table")

		if _, err := tx.Exec(ctx, sql); err != nil {
			return err
		}
	}

	return nil
}

// RunMigrations applies any pending schema migrations for the subscription's
// data types. Published views that reference the subscription's tables are
// dropped before migration and re-applied afterwards so column-altering
// migrations can proceed and rebuilt views pick up any updated
// ViewGenerator output. Also self-heals missing ClickHouse-backed tables
// by running CREATE TABLE IF NOT EXISTS on every migration hook, covering
// subscriptions created while CH was unavailable.
func (subscription *Subscription) RunMigrations(ctx context.Context) error {
	if err := subscription.createClickHouseTables(ctx); err != nil {
		return fmt.Errorf("ensure clickhouse tables: %w", err)
	}

	maxVersion := 0
	hasPending := false

	for _, dataTypeName := range subscription.DataTypes {
		dataType := data.DataTypes[dataTypeName]
		if dataType == nil {
			continue
		}

		if dataType.Version > maxVersion {
			maxVersion = dataType.Version
		}

		if subscription.SchemaVersion < dataType.Version {
			hasPending = true
		}
	}

	if !hasPending && maxVersion <= subscription.SchemaVersion {
		return nil
	}

	tableSet := make(map[string]struct{}, len(subscription.DataTables))
	for _, t := range subscription.DataTables {
		tableSet[t] = struct{}{}
	}

	conn, err := subscription.Library.AcquireWithTimeout(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	allViews, err := LoadPublishedViews(ctx, conn)
	if err != nil {
		return fmt.Errorf("load published views: %w", err)
	}

	var affectedViews []*PublishedView

	for _, pv := range allViews {
		for _, src := range pv.Sources {
			if _, ok := tableSet[src.TableName]; ok {
				affectedViews = append(affectedViews, pv)
				break
			}
		}
	}

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin migration tx: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	for _, pv := range affectedViews {
		if _, err := tx.Exec(ctx, fmt.Sprintf("DROP VIEW IF EXISTS %s", pv.ViewName)); err != nil {
			return fmt.Errorf("drop published view %s: %w", pv.ViewName, err)
		}
	}

	for idx, dataTypeName := range subscription.DataTypes {
		dataType := data.DataTypes[dataTypeName]
		if dataType == nil {
			continue
		}

		// Migrations on this path are pgx-driven; ClickHouse-backed types
		// must be migrated through their own (CH-aware) channel.
		if dataType.Backend != data.BackendPostgres {
			continue
		}

		if subscription.SchemaVersion >= dataType.Version {
			continue
		}

		dataTable := subscription.DataTables[idx]

		for i := subscription.SchemaVersion; i < dataType.Version; i++ {
			if i < len(dataType.Migrations) {
				migrationSQL := fmt.Sprintf(dataType.Migrations[i], dataTable)
				log.Info().Str("Table", dataTable).Int("Migration", i).Msg("running migration")

				if _, err := tx.Exec(ctx, migrationSQL); err != nil {
					return fmt.Errorf("migration %d for %s failed: %w", i, dataTable, err)
				}
			}
		}
	}

	for _, pv := range affectedViews {
		for _, sql := range pv.GenerateViewSQL() {
			if _, err := tx.Exec(ctx, sql); err != nil {
				return fmt.Errorf("rebuild published view %s: %w", pv.ViewName, err)
			}
		}
	}

	if maxVersion > subscription.SchemaVersion {
		if _, err := tx.Exec(ctx, "UPDATE subscriptions SET schema_version=$1 WHERE id=$2", maxVersion, subscription.ID); err != nil {
			return fmt.Errorf("failed to update schema version: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration tx: %w", err)
	}

	if maxVersion > subscription.SchemaVersion {
		subscription.SchemaVersion = maxVersion
	}

	return nil
}

func (subscription *Subscription) createTables(ctx context.Context, tx pgx.Tx) error {
	// ClickHouse DDL is applied first, outside the Postgres transaction.
	// CH CREATE TABLE IF NOT EXISTS is idempotent, so leaving an empty CH
	// table behind on a later PG-tx rollback is harmless and self-healing.
	if err := subscription.createClickHouseTables(ctx); err != nil {
		return err
	}

	for idx, dataTypeName := range subscription.DataTypes {
		dataType := data.DataTypes[dataTypeName]
		if dataType == nil || dataType.Backend != data.BackendPostgres {
			continue
		}

		schema := dataType.ExpandedSchema(subscription.DataTables[idx])

		_, err := tx.Exec(ctx, schema)
		if err != nil {
			return err
		}
	}

	return nil
}

// dataTypeAt returns the registered DataType for the given index in the
// subscription's DataTypes slice, or nil if it is not registered.
func (subscription *Subscription) dataTypeAt(idx int) *data.DataType {
	if idx < 0 || idx >= len(subscription.DataTypes) {
		return nil
	}

	return data.DataTypes[subscription.DataTypes[idx]]
}

// HasClickHouseBackedTypes reports whether any DataType on the
// subscription is routed to the ClickHouse backend. Used by the run
// dispatcher to decide whether a missing or disabled ClickHouse
// connection should fail the run before any provider work begins.
func (subscription *Subscription) HasClickHouseBackedTypes() bool {
	for _, name := range subscription.DataTypes {
		if dt, ok := data.DataTypes[name]; ok && dt != nil && dt.Backend == data.BackendClickHouse {
			return true
		}
	}

	return false
}

// createClickHouseTables runs CREATE TABLE IF NOT EXISTS for every
// ClickHouse-backed DataType on the subscription. It is a no-op when no
// CH-backed types are present so non-intraday subscriptions don't even
// open the CH connection. When ClickHouse is disabled, table creation
// is skipped with a warning so the rest of the subscription (and any
// provider-side side effects like the parquet archive) can still
// operate in a parquet-only mode.
func (subscription *Subscription) createClickHouseTables(ctx context.Context) error {
	var pendingTables []string

	var pending []int

	for idx, dataTypeName := range subscription.DataTypes {
		dt := data.DataTypes[dataTypeName]
		if dt == nil || dt.Backend != data.BackendClickHouse {
			continue
		}

		pending = append(pending, idx)
		pendingTables = append(pendingTables, subscription.DataTables[idx])
	}

	if len(pending) == 0 {
		return nil
	}

	if subscription.Library.IsClickHouseDisabled() {
		log.Warn().Strs("tables", pendingTables).Msg("clickhouse is disabled; skipping table creation (data of these types will not be persisted to clickhouse)")
		return nil
	}

	conn, err := subscription.Library.ClickHouse(ctx)
	if err != nil {
		return fmt.Errorf("clickhouse connection: %w", err)
	}

	for _, idx := range pending {
		dt := data.DataTypes[subscription.DataTypes[idx]]
		schema := dt.ExpandedSchema(subscription.DataTables[idx])

		log.Info().Str("TableName", subscription.DataTables[idx]).Msg("creating clickhouse table")

		if err := conn.Exec(ctx, schema); err != nil {
			return fmt.Errorf("create clickhouse table %s: %w", subscription.DataTables[idx], err)
		}
	}

	return nil
}

// dropClickHouseTables drops every ClickHouse-backed table on the
// subscription. Called after the Postgres delete tx commits so a CH
// outage cannot leave the subscription row referencing tables that no
// longer exist. When ClickHouse is disabled, the drop is skipped with
// a warning - the tables cannot have been created in the first place,
// so the subscription row's removal is the only meaningful action.
func (subscription *Subscription) dropClickHouseTables(ctx context.Context) error {
	var pending []string

	for idx, dataTypeName := range subscription.DataTypes {
		dt := data.DataTypes[dataTypeName]
		if dt == nil || dt.Backend != data.BackendClickHouse {
			continue
		}

		pending = append(pending, subscription.DataTables[idx])
	}

	if len(pending) == 0 {
		return nil
	}

	if subscription.Library.IsClickHouseDisabled() {
		log.Warn().Strs("tables", pending).Msg("clickhouse is disabled; skipping table drops (run cleanup manually if tables exist)")
		return nil
	}

	conn, err := subscription.Library.ClickHouse(ctx)
	if err != nil {
		return fmt.Errorf("clickhouse connection: %w", err)
	}

	for _, tbl := range pending {
		log.Info().Str("TableName", tbl).Msg("dropping clickhouse table")

		if err := conn.Exec(ctx, fmt.Sprintf("DROP TABLE IF EXISTS %s", tbl)); err != nil {
			return fmt.Errorf("drop clickhouse table %s: %w", tbl, err)
		}
	}

	return nil
}
