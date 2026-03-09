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
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog/log"
)

// ViewSource represents a single table contributing to a published view,
// optionally bounded by a date range. FromDate is inclusive, UntilDate is exclusive.
type ViewSource struct {
	TableName      string     `json:"table_name"`
	SubscriptionID string     `json:"subscription_id"`
	FromDate       *time.Time `json:"from_date,omitempty"`
	UntilDate      *time.Time `json:"until_date,omitempty"`
}

// PublishedView represents a database view composed of one or more source tables.
type PublishedView struct {
	ID          uuid.UUID    `json:"id"`
	ViewName    string       `json:"view_name"`
	DataTypeKey string       `json:"data_type_key"`
	Sources     []ViewSource `json:"sources"`
}

// GenerateViewSQL produces the SQL statements needed to create (or drop) the
// published view. For most data types this returns a single CREATE OR REPLACE VIEW
// statement. For the "index" data type it returns two statements: one for the
// _snapshot view and one for the _changelog view. When there are zero sources
// it returns DROP VIEW IF EXISTS statements.
func (pv *PublishedView) GenerateViewSQL() []string {
	if pv.DataTypeKey == data.IndexKey {
		return []string{
			generateUnionSQL(pv.ViewName+"_snapshot", "_snapshot", pv.Sources),
			generateUnionSQL(pv.ViewName+"_changelog", "_changelog", pv.Sources),
		}
	}

	return []string{generateUnionSQL(pv.ViewName, "", pv.Sources)}
}

// generateUnionSQL builds a CREATE OR REPLACE VIEW or DROP VIEW statement.
// tableSuffix is appended to each source table name (e.g. "_snapshot" for index types).
func generateUnionSQL(viewName, tableSuffix string, sources []ViewSource) string {
	if len(sources) == 0 {
		return fmt.Sprintf("DROP VIEW IF EXISTS %s", viewName)
	}

	if len(sources) == 1 {
		s := sources[0]
		tbl := s.TableName + tableSuffix
		where := buildWhereClause(s)
		if where == "" {
			return fmt.Sprintf("CREATE OR REPLACE VIEW %s AS SELECT * FROM %s", viewName, tbl)
		}
		return fmt.Sprintf("CREATE OR REPLACE VIEW %s AS SELECT * FROM %s WHERE %s", viewName, tbl, where)
	}

	var legs []string
	for _, s := range sources {
		tbl := s.TableName + tableSuffix
		where := buildWhereClause(s)
		if where == "" {
			legs = append(legs, fmt.Sprintf("SELECT * FROM %s", tbl))
		} else {
			legs = append(legs, fmt.Sprintf("SELECT * FROM %s WHERE %s", tbl, where))
		}
	}

	return fmt.Sprintf("CREATE OR REPLACE VIEW %s AS %s", viewName, strings.Join(legs, " UNION ALL "))
}

// buildWhereClause produces the WHERE conditions for a single source based on
// its date bounds. Uses event_date as the column name.
func buildWhereClause(s ViewSource) string {
	var parts []string
	if s.FromDate != nil {
		parts = append(parts, fmt.Sprintf("event_date >= '%s'", s.FromDate.Format("2006-01-02")))
	}
	if s.UntilDate != nil {
		parts = append(parts, fmt.Sprintf("event_date < '%s'", s.UntilDate.Format("2006-01-02")))
	}
	return strings.Join(parts, " AND ")
}

// ValidateSources checks that the date ranges of sources do not overlap.
// It uses sentinel dates for nil bounds and verifies that when sorted by
// from date, each source's from >= the previous source's until.
func (pv *PublishedView) ValidateSources() error {
	if len(pv.Sources) <= 1 {
		return nil
	}

	type bounded struct {
		from  time.Time
		until time.Time
		name  string
	}

	sentinelMin := time.Date(1800, 1, 1, 0, 0, 0, 0, time.UTC)
	sentinelMax := time.Date(2200, 1, 1, 0, 0, 0, 0, time.UTC)

	items := make([]bounded, len(pv.Sources))
	for i, s := range pv.Sources {
		b := bounded{name: s.TableName, from: sentinelMin, until: sentinelMax}
		if s.FromDate != nil {
			b.from = *s.FromDate
		}
		if s.UntilDate != nil {
			b.until = *s.UntilDate
		}
		items[i] = b
	}

	sort.Slice(items, func(i, j int) bool {
		return items[i].from.Before(items[j].from)
	})

	for i := 1; i < len(items); i++ {
		if items[i].from.Before(items[i-1].until) {
			return fmt.Errorf(
				"overlapping date ranges: %s [until %s] and %s [from %s]",
				items[i-1].name, items[i-1].until.Format("2006-01-02"),
				items[i].name, items[i].from.Format("2006-01-02"),
			)
		}
	}

	return nil
}

// ValidateSourceTables checks that all source tables referenced by the published
// view actually exist in the database.
func ValidateSourceTables(ctx context.Context, pool *pgxpool.Pool, pv *PublishedView) error {
	if len(pv.Sources) == 0 {
		return nil
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	for _, src := range pv.Sources {
		tables := []string{src.TableName}
		if pv.DataTypeKey == data.IndexKey {
			tables = []string{src.TableName + "_snapshot", src.TableName + "_changelog"}
		}

		for _, tbl := range tables {
			var exists bool
			err := conn.QueryRow(ctx,
				`SELECT EXISTS (
				   SELECT 1 FROM information_schema.tables
				   WHERE table_name = $1 AND table_schema = 'public'
				 )`, tbl).Scan(&exists)
			if err != nil {
				return fmt.Errorf("check table existence for %s: %w", tbl, err)
			}
			if !exists {
				return fmt.Errorf("source table %s does not exist", tbl)
			}
		}
	}

	return nil
}

// ApplyPublishedView executes the generated SQL statements for the given
// published view against the database.
func ApplyPublishedView(ctx context.Context, pool *pgxpool.Pool, pv *PublishedView) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	sqls := pv.GenerateViewSQL()
	for _, sql := range sqls {
		log.Info().Str("sql", sql).Msg("applying published view SQL")
		if _, err := conn.Exec(ctx, sql); err != nil {
			return fmt.Errorf("exec published view SQL: %w", err)
		}
	}

	return nil
}

// SavePublishedView validates the sources, upserts the published view to the
// database, and applies the view.
func SavePublishedView(ctx context.Context, pool *pgxpool.Pool, pv *PublishedView) error {
	if err := pv.ValidateSources(); err != nil {
		return fmt.Errorf("validate sources: %w", err)
	}

	if err := ValidateSourceTables(ctx, pool, pv); err != nil {
		return fmt.Errorf("validate source tables: %w", err)
	}

	if pv.ID == uuid.Nil {
		pv.ID = uuid.New()
	}

	sourcesJSON, err := json.Marshal(pv.Sources)
	if err != nil {
		return fmt.Errorf("marshal sources: %w", err)
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	_, err = conn.Exec(ctx,
		`INSERT INTO published_views (id, view_name, data_type_key, sources)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (view_name)
		 DO UPDATE SET data_type_key = EXCLUDED.data_type_key, sources = EXCLUDED.sources`,
		pv.ID, pv.ViewName, pv.DataTypeKey, sourcesJSON,
	)
	if err != nil {
		return fmt.Errorf("upsert published view: %w", err)
	}

	return ApplyPublishedView(ctx, pool, pv)
}

// LoadPublishedViews loads all published views from the database.
func LoadPublishedViews(ctx context.Context, pool *pgxpool.Pool) ([]*PublishedView, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx,
		`SELECT id, view_name, data_type_key, sources FROM published_views ORDER BY view_name`,
	)
	if err != nil {
		return nil, fmt.Errorf("query published views: %w", err)
	}
	defer rows.Close()

	var views []*PublishedView
	for rows.Next() {
		pv := &PublishedView{}
		var sourcesJSON []byte
		if err := rows.Scan(&pv.ID, &pv.ViewName, &pv.DataTypeKey, &sourcesJSON); err != nil {
			return nil, fmt.Errorf("scan published view: %w", err)
		}
		if err := json.Unmarshal(sourcesJSON, &pv.Sources); err != nil {
			return nil, fmt.Errorf("unmarshal sources for %s: %w", pv.ViewName, err)
		}
		views = append(views, pv)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate published views: %w", err)
	}

	return views, nil
}

// LoadPublishedView loads a single published view by view name.
func LoadPublishedView(ctx context.Context, pool *pgxpool.Pool, viewName string) (*PublishedView, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	pv := &PublishedView{}
	var sourcesJSON []byte
	err = conn.QueryRow(ctx,
		`SELECT id, view_name, data_type_key, sources FROM published_views WHERE view_name = $1`,
		viewName,
	).Scan(&pv.ID, &pv.ViewName, &pv.DataTypeKey, &sourcesJSON)
	if err != nil {
		return nil, fmt.Errorf("load published view %s: %w", viewName, err)
	}

	if err := json.Unmarshal(sourcesJSON, &pv.Sources); err != nil {
		return nil, fmt.Errorf("unmarshal sources for %s: %w", viewName, err)
	}

	return pv, nil
}

// DeletePublishedView drops the database view(s) and deletes the row from
// the published_views table.
func DeletePublishedView(ctx context.Context, pool *pgxpool.Pool, viewName string) error {
	// Load the view first so we can generate the correct DROP statements.
	pv, err := LoadPublishedView(ctx, pool, viewName)
	if err != nil {
		return fmt.Errorf("load for delete: %w", err)
	}

	// Generate DROP by setting sources to empty.
	dropPV := &PublishedView{
		ViewName:    pv.ViewName,
		DataTypeKey: pv.DataTypeKey,
		Sources:     []ViewSource{},
	}
	if err := ApplyPublishedView(ctx, pool, dropPV); err != nil {
		return fmt.Errorf("drop views: %w", err)
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	_, err = conn.Exec(ctx, `DELETE FROM published_views WHERE view_name = $1`, viewName)
	if err != nil {
		return fmt.Errorf("delete published view row: %w", err)
	}

	log.Info().Str("ViewName", viewName).Msg("deleted published view")
	return nil
}

// PublishedViewReferencesTable checks whether any published view references the
// given table name in its sources. It uses a proper JSONB query to match exact
// table names rather than substring matching.
func PublishedViewReferencesTable(ctx context.Context, pool *pgxpool.Pool, tableName string) (bool, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return false, fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	var count int
	err = conn.QueryRow(ctx,
		`SELECT COUNT(*) FROM published_views
		 WHERE EXISTS (
		   SELECT 1 FROM jsonb_array_elements(sources) s
		   WHERE s->>'table_name' = $1
		 )`,
		tableName,
	).Scan(&count)
	if err != nil {
		return false, fmt.Errorf("check table references: %w", err)
	}

	return count > 0, nil
}
