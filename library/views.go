package library

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog/log"
)

// allViewNames returns the set of canonical view names from DataTypes.
func allViewNames() []string {
	seen := make(map[string]bool)
	var names []string
	for _, dt := range data.DataTypes {
		if dt.ViewName != "" && !seen[dt.ViewName] {
			seen[dt.ViewName] = true
			names = append(names, dt.ViewName)
		}
	}
	return names
}

// SetPreferredView creates or replaces a view for the given data type key
// that points to the specified subscription table.
func SetPreferredView(ctx context.Context, pool *pgxpool.Pool, dataTypeKey string, tableName string) error {
	dt := data.DataTypes[dataTypeKey]
	if dt == nil {
		return fmt.Errorf("unknown data type: %s", dataTypeKey)
	}
	if dt.ViewName == "" {
		return fmt.Errorf("data type %s has no view name configured", dataTypeKey)
	}

	sql := fmt.Sprintf("CREATE OR REPLACE VIEW %s AS SELECT * FROM %s", dt.ViewName, tableName)

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	_, err = conn.Exec(ctx, sql)
	if err != nil {
		return fmt.Errorf("create view %s -> %s: %w", dt.ViewName, tableName, err)
	}

	log.Info().Str("View", dt.ViewName).Str("Table", tableName).Msg("set preferred view")
	return nil
}

// DropPreferredView drops the view for the given data type key if it exists.
func DropPreferredView(ctx context.Context, pool *pgxpool.Pool, dataTypeKey string) error {
	dt := data.DataTypes[dataTypeKey]
	if dt == nil {
		return fmt.Errorf("unknown data type: %s", dataTypeKey)
	}
	if dt.ViewName == "" {
		return nil
	}

	sql := fmt.Sprintf("DROP VIEW IF EXISTS %s", dt.ViewName)

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	_, err = conn.Exec(ctx, sql)
	if err != nil {
		return fmt.Errorf("drop view %s: %w", dt.ViewName, err)
	}

	log.Info().Str("View", dt.ViewName).Msg("dropped preferred view")
	return nil
}

// PreferredViews queries pg_views and returns a map of view name -> underlying table name
// for all canonical preferred views that currently exist.
func PreferredViews(ctx context.Context, pool *pgxpool.Pool) (map[string]string, error) {
	names := allViewNames()
	if len(names) == 0 {
		return nil, nil
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	// Build a quoted list for the IN clause
	quoted := make([]string, len(names))
	for i, n := range names {
		quoted[i] = fmt.Sprintf("'%s'", n)
	}

	sql := fmt.Sprintf(
		"SELECT viewname, definition FROM pg_views WHERE schemaname = 'public' AND viewname IN (%s)",
		strings.Join(quoted, ", "),
	)

	rows, err := conn.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("query preferred views: %w", err)
	}
	defer rows.Close()

	// Pattern to extract table name from view definition like " SELECT col FROM tablename;"
	fromRe := regexp.MustCompile(`(?i)\bFROM\s+(\S+?)[\s;]*$`)

	result := make(map[string]string)
	for rows.Next() {
		var viewName, definition string
		if err := rows.Scan(&viewName, &definition); err != nil {
			return nil, err
		}

		definition = strings.TrimSpace(definition)
		if matches := fromRe.FindStringSubmatch(definition); len(matches) >= 2 {
			result[viewName] = matches[1]
		}
	}

	return result, nil
}
