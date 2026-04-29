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

	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog/log"
)

// MigrateAllSubscriptions applies pending DataType migrations across
// every subscription in a single coordinated transaction. Published
// views that span multiple subscription tables are dropped once at
// the start, all per-table migrations run, then views are rebuilt
// once at the end.
//
// This avoids the "each UNION query must have the same number of
// columns" failure that happens when subscriptions are migrated one
// at a time: a per-sub TX brings its own table to schema version N
// and tries to rebuild the shared view, but peer tables in other
// subs are still at version N-1, so the rebuilt UNION has mismatched
// column lists.
//
// Returns the number of subscriptions whose schema_version actually
// advanced and the total number of subscriptions checked. The
// transaction is atomic: if any migration fails, the whole batch
// rolls back and no schema_version is bumped.
func (myLibrary *Library) MigrateAllSubscriptions(ctx context.Context) (migrated, total int, err error) {
	subs, err := myLibrary.Subscriptions(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("load subscriptions: %w", err)
	}

	type pendingSub struct {
		sub        *Subscription
		fromVer    int
		targetVer  int
		dataTables []string
	}

	pending := make([]pendingSub, 0, len(subs))
	tableSet := make(map[string]struct{})

	for _, sub := range subs {
		target := 0
		hasPending := false

		var tables []string

		for idx, dataTypeName := range sub.DataTypes {
			dataType := data.DataTypes[dataTypeName]
			if dataType == nil {
				continue
			}

			if dataType.Version > target {
				target = dataType.Version
			}

			if sub.SchemaVersion < dataType.Version {
				hasPending = true

				tables = append(tables, sub.DataTables[idx])
			}
		}

		if hasPending {
			pending = append(pending, pendingSub{
				sub:        sub,
				fromVer:    sub.SchemaVersion,
				targetVer:  target,
				dataTables: tables,
			})

			for _, t := range tables {
				tableSet[t] = struct{}{}
			}
		}
	}

	if len(pending) == 0 {
		return 0, len(subs), nil
	}

	conn, err := myLibrary.AcquireWithTimeout(ctx)
	if err != nil {
		return 0, 0, err
	}
	defer conn.Release()

	allViews, err := LoadPublishedViews(ctx, conn)
	if err != nil {
		return 0, 0, fmt.Errorf("load published views: %w", err)
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
		return 0, 0, fmt.Errorf("begin migration tx: %w", err)
	}

	defer func() { _ = tx.Rollback(ctx) }()

	// Drop every affected view once. Views remain dropped until the
	// rebuild step at the end of this TX; if anything fails before
	// that, the rollback restores them.
	for _, pv := range affectedViews {
		if _, err := tx.Exec(ctx, fmt.Sprintf("DROP VIEW IF EXISTS %s", pv.ViewName)); err != nil {
			return 0, 0, fmt.Errorf("drop published view %s: %w", pv.ViewName, err)
		}
	}

	// Apply every pending sub's migrations within the same TX so all
	// peer tables reach the same schema version before any rebuild.
	for _, p := range pending {
		for idx, dataTypeName := range p.sub.DataTypes {
			dataType := data.DataTypes[dataTypeName]
			if dataType == nil {
				continue
			}

			if p.sub.SchemaVersion >= dataType.Version {
				continue
			}

			dataTable := p.sub.DataTables[idx]

			for i := p.sub.SchemaVersion; i < dataType.Version; i++ {
				if i >= len(dataType.Migrations) {
					continue
				}

				migrationSQL := fmt.Sprintf(dataType.Migrations[i], dataTable)
				log.Info().Str("Subscription", p.sub.Name).Str("Table", dataTable).Int("Migration", i).Msg("running migration")

				if _, err := tx.Exec(ctx, migrationSQL); err != nil {
					return 0, 0, fmt.Errorf("migration %d for %s (subscription %s): %w", i, dataTable, p.sub.Name, err)
				}
			}
		}
	}

	// Rebuild every affected view exactly once, after every peer
	// table is at its target schema version.
	for _, pv := range affectedViews {
		for _, sql := range pv.GenerateViewSQL() {
			if _, err := tx.Exec(ctx, sql); err != nil {
				return 0, 0, fmt.Errorf("rebuild published view %s: %w", pv.ViewName, err)
			}
		}
	}

	// Bump each pending sub's schema_version. Done after the rebuild
	// so a rebuild failure rolls back the version bumps too.
	for _, p := range pending {
		if p.targetVer > p.sub.SchemaVersion {
			if _, err := tx.Exec(ctx,
				"UPDATE subscriptions SET schema_version=$1 WHERE id=$2",
				p.targetVer, p.sub.ID,
			); err != nil {
				return 0, 0, fmt.Errorf("update schema_version for %s: %w", p.sub.Name, err)
			}
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("commit migration tx: %w", err)
	}

	for i := range pending {
		if pending[i].targetVer > pending[i].sub.SchemaVersion {
			pending[i].sub.SchemaVersion = pending[i].targetVer
			migrated++
		}
	}

	return migrated, len(subs), nil
}
