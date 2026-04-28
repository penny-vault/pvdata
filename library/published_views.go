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
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog/log"
)

// Querier is an interface satisfied by both *pgxpool.Pool and pgx.Tx,
// allowing functions to work with either a pool connection or an existing transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

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
// published view. This returns a single CREATE OR REPLACE VIEW statement, or a
// DROP VIEW IF EXISTS statement when there are zero sources.
func (pv *PublishedView) GenerateViewSQL() []string {
	var gen data.ViewGenerator
	if dt, ok := data.DataTypes[pv.DataTypeKey]; ok && dt != nil {
		gen = dt.ViewGenerator
	}

	return []string{generateUnionSQL(pv.ViewName, pv.Sources, gen, dateColumnForType(pv.DataTypeKey))}
}

// generateUnionSQL builds a CREATE OR REPLACE VIEW or DROP VIEW statement.
// When gen is non-nil, the SELECT/FROM portion of each leg is delegated to it
// instead of using the default "SELECT * FROM <table>". dateColumn names the
// column used for FromDate/UntilDate WHERE bounds; "" disables the bounds.
func generateUnionSQL(viewName string, sources []ViewSource, gen data.ViewGenerator, dateColumn string) string {
	if len(sources) == 0 {
		return fmt.Sprintf("DROP VIEW IF EXISTS %s", viewName)
	}

	legs := make([]string, len(sources))
	for i, s := range sources {
		legs[i] = buildLeg(s, gen, dateColumn)
	}

	return fmt.Sprintf("CREATE OR REPLACE VIEW %s AS %s", viewName, strings.Join(legs, " UNION ALL "))
}

// buildLeg renders one leg of a published view: the SELECT/FROM clause
// (default or generator-supplied) followed by an optional WHERE filter
// derived from the source's date bounds.
func buildLeg(s ViewSource, gen data.ViewGenerator, dateColumn string) string {
	var sel string
	if gen != nil {
		sel = gen.SelectFrom(s.TableName)
	} else {
		sel = fmt.Sprintf("SELECT * FROM %s", s.TableName)
	}

	where := buildWhereClause(s, dateColumn)
	if where == "" {
		return sel
	}

	return sel + " WHERE " + where
}

// buildWhereClause produces the WHERE conditions for a single source
// based on its date bounds, using dateColumn as the column name. When
// dateColumn is empty (e.g., asset descriptions have no date axis),
// FromDate/UntilDate are silently ignored to avoid generating SQL
// referencing a non-existent column.
func buildWhereClause(s ViewSource, dateColumn string) string {
	if dateColumn == "" {
		return ""
	}

	var parts []string
	if s.FromDate != nil {
		parts = append(parts, fmt.Sprintf("%s >= '%s'", dateColumn, s.FromDate.Format("2006-01-02")))
	}

	if s.UntilDate != nil {
		parts = append(parts, fmt.Sprintf("%s < '%s'", dateColumn, s.UntilDate.Format("2006-01-02")))
	}

	return strings.Join(parts, " AND ")
}

// dateColumnForType returns the column used for date-bounded WHERE
// clauses on a published view of the given data type. Returns ""
// for data types with no date axis (asset descriptions); returns
// "snapshot_date" for index snapshots; "event_date" for everything
// else, matching the column name used by those tables' schemas.
func dateColumnForType(dataTypeKey string) string {
	switch dataTypeKey {
	case data.AssetKey:
		return ""
	case data.IndexSnapshotKey:
		return "snapshot_date"
	default:
		return "event_date"
	}
}

// ValidateSources checks that the date ranges of sources do not overlap.
// It delegates to CheckOverlaps and returns an error with the first overlap
// message if any overlaps are found.
func (pv *PublishedView) ValidateSources() error {
	overlaps := pv.CheckOverlaps()
	if len(overlaps) > 0 {
		return fmt.Errorf("%s", overlaps[0])
	}

	return nil
}

// CheckOverlaps returns human-readable descriptions of any overlapping date
// ranges between sources. Returns an empty slice when there are no overlaps.
func (pv *PublishedView) CheckOverlaps() []string {
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

	var overlaps []string

	for i := 1; i < len(items); i++ {
		if items[i].from.Before(items[i-1].until) {
			overlaps = append(overlaps, fmt.Sprintf(
				"overlapping date ranges: %s [until %s] and %s [from %s]",
				items[i-1].name, items[i-1].until.Format("2006-01-02"),
				items[i].name, items[i].from.Format("2006-01-02"),
			))
		}
	}

	return overlaps
}

// ValidateSourceTables checks that all source tables referenced by the published
// view actually exist in the database.
func ValidateSourceTables(ctx context.Context, q Querier, pv *PublishedView) error {
	if len(pv.Sources) == 0 {
		return nil
	}

	for _, src := range pv.Sources {
		var exists bool

		err := q.QueryRow(ctx,
			`SELECT EXISTS (
			   SELECT 1 FROM information_schema.tables
			   WHERE table_name = $1 AND table_schema = 'public'
			 )`, src.TableName).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check table existence for %s: %w", src.TableName, err)
		}

		if !exists {
			return fmt.Errorf("source table %s does not exist", src.TableName)
		}
	}

	return nil
}

// ApplyPublishedView executes the generated SQL statements for the given
// published view against the database.
func ApplyPublishedView(ctx context.Context, q Querier, pv *PublishedView) error {
	sqls := pv.GenerateViewSQL()
	for _, sql := range sqls {
		log.Info().Str("sql", sql).Msg("applying published view SQL")

		if _, err := q.Exec(ctx, sql); err != nil {
			return fmt.Errorf("exec published view SQL: %w", err)
		}
	}

	return nil
}

// SavePublishedView upserts the published view to the database and applies the view.
// Overlapping date ranges are allowed; use CheckOverlaps to get warnings.
func SavePublishedView(ctx context.Context, q Querier, pv *PublishedView) error {
	if err := ValidateSourceTables(ctx, q, pv); err != nil {
		return fmt.Errorf("validate source tables: %w", err)
	}

	if pv.ID == uuid.Nil {
		pv.ID = uuid.New()
	}

	sourcesJSON, err := json.Marshal(pv.Sources)
	if err != nil {
		return fmt.Errorf("marshal sources: %w", err)
	}

	_, err = q.Exec(ctx,
		`INSERT INTO published_views (id, view_name, data_type_key, sources)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (view_name)
		 DO UPDATE SET data_type_key = EXCLUDED.data_type_key, sources = EXCLUDED.sources`,
		pv.ID, pv.ViewName, pv.DataTypeKey, sourcesJSON,
	)
	if err != nil {
		return fmt.Errorf("upsert published view: %w", err)
	}

	return ApplyPublishedView(ctx, q, pv)
}

// LoadPublishedViews loads all published views from the database.
func LoadPublishedViews(ctx context.Context, q Querier) ([]*PublishedView, error) {
	rows, err := q.Query(ctx,
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
func LoadPublishedView(ctx context.Context, q Querier, viewName string) (*PublishedView, error) {
	pv := &PublishedView{}

	var sourcesJSON []byte

	err := q.QueryRow(ctx,
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

// LoadPublishedViewByID loads a single published view by its UUID.
func LoadPublishedViewByID(ctx context.Context, q Querier, id uuid.UUID) (*PublishedView, error) {
	pv := &PublishedView{}

	var sourcesJSON []byte

	err := q.QueryRow(ctx,
		`SELECT id, view_name, data_type_key, sources FROM published_views WHERE id = $1`,
		id,
	).Scan(&pv.ID, &pv.ViewName, &pv.DataTypeKey, &sourcesJSON)
	if err != nil {
		return nil, fmt.Errorf("load published view %s: %w", id, err)
	}

	if err := json.Unmarshal(sourcesJSON, &pv.Sources); err != nil {
		return nil, fmt.Errorf("unmarshal sources for %s: %w", id, err)
	}

	return pv, nil
}

// DeletePublishedView drops the database view(s) and deletes the row from
// the published_views table.
func DeletePublishedView(ctx context.Context, q Querier, viewName string) error {
	// Load the view first so we can generate the correct DROP statements.
	pv, err := LoadPublishedView(ctx, q, viewName)
	if err != nil {
		return fmt.Errorf("load for delete: %w", err)
	}

	// Generate DROP by setting sources to empty.
	dropPV := &PublishedView{
		ViewName:    pv.ViewName,
		DataTypeKey: pv.DataTypeKey,
		Sources:     []ViewSource{},
	}
	if err := ApplyPublishedView(ctx, q, dropPV); err != nil {
		return fmt.Errorf("drop views: %w", err)
	}

	_, err = q.Exec(ctx, `DELETE FROM published_views WHERE view_name = $1`, viewName)
	if err != nil {
		return fmt.Errorf("delete published view row: %w", err)
	}

	log.Info().Str("ViewName", viewName).Msg("deleted published view")

	return nil
}

// PublishedViewReferencesTable checks whether any published view references the
// given table name in its sources. It uses a proper JSONB query to match exact
// table names rather than substring matching.
func PublishedViewReferencesTable(ctx context.Context, q Querier, tableName string) (bool, error) {
	var count int

	err := q.QueryRow(ctx,
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
