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
package cmd

import (
	"bytes"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"charm.land/glamour/v2"
	"github.com/charmbracelet/x/term"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/mattn/go-isatty"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type fieldKind int

const (
	kindInt fieldKind = iota
	kindFloat
)

type fundamentalField struct {
	column string
	kind   fieldKind
}

// fundamentalFields enumerates every numeric column of the fundamentals table
// in the same order as data/datatype.go FundamentalsKey schema.
var fundamentalFields = []fundamentalField{
	{"accumulated_other_comprehensive_income", kindInt},
	{"total_assets", kindInt},
	{"average_assets", kindInt},
	{"current_assets", kindInt},
	{"assets_non_current", kindInt},
	{"asset_turnover", kindFloat},
	{"book_value_per_share", kindFloat},
	{"capital_expenditure", kindInt},
	{"cash_and_equivalents", kindInt},
	{"cost_of_revenue", kindInt},
	{"consolidated_income", kindInt},
	{"current_ratio", kindFloat},
	{"debt_to_equity_ratio", kindFloat},
	{"total_debt", kindInt},
	{"debt_current", kindInt},
	{"debt_non_current", kindInt},
	{"deferred_revenue", kindInt},
	{"depreciation_amortization_and_accretion", kindInt},
	{"deposits", kindInt},
	{"dividend_yield", kindFloat},
	{"dividends_per_basic_common_share", kindFloat},
	{"ebit", kindInt},
	{"ebitda", kindInt},
	{"ebitda_margin", kindFloat},
	{"ebt", kindInt},
	{"eps", kindFloat},
	{"eps_diluted", kindFloat},
	{"equity", kindInt},
	{"equity_avg", kindInt},
	{"enterprise_value", kindInt},
	{"ev_to_ebit", kindInt},
	{"ev_to_ebitda", kindFloat},
	{"free_cash_flow", kindInt},
	{"free_cash_flow_per_share", kindFloat},
	{"fx_usd", kindFloat},
	{"gross_profit", kindInt},
	{"gross_margin", kindFloat},
	{"intangibles", kindInt},
	{"interest_expense", kindInt},
	{"invested_capital", kindInt},
	{"invested_capital_average", kindInt},
	{"inventory", kindInt},
	{"investments", kindInt},
	{"investments_current", kindInt},
	{"investments_non_current", kindInt},
	{"total_liabilities", kindInt},
	{"current_liabilities", kindInt},
	{"liabilities_non_current", kindInt},
	{"market_capitalization", kindInt},
	{"net_cash_flow", kindInt},
	{"net_cash_flow_business", kindInt},
	{"net_cash_flow_common", kindInt},
	{"net_cash_flow_debt", kindInt},
	{"net_cash_flow_dividend", kindInt},
	{"net_cash_flow_from_financing", kindInt},
	{"net_cash_flow_from_investing", kindInt},
	{"net_cash_flow_invest", kindInt},
	{"net_cash_flow_from_operations", kindInt},
	{"net_cash_flow_fx", kindInt},
	{"net_income", kindInt},
	{"net_income_common_stock", kindInt},
	{"net_loss_income_discontinued_operations", kindInt},
	{"net_income_to_non_controlling_interests", kindInt},
	{"profit_margin", kindFloat},
	{"operating_expenses", kindInt},
	{"operating_income", kindInt},
	{"payables", kindInt},
	{"payout_ratio", kindFloat},
	{"pb", kindFloat},
	{"pe", kindFloat},
	{"pe1", kindFloat},
	{"property_plant_and_equipment_net", kindInt},
	{"preferred_dividends_income_statement_impact", kindInt},
	{"price", kindFloat},
	{"ps", kindFloat},
	{"ps1", kindFloat},
	{"receivables", kindInt},
	{"accumulated_retained_earnings_deficit", kindInt},
	{"revenues", kindInt},
	{"r_and_d_expenses", kindInt},
	{"roa", kindFloat},
	{"roe", kindFloat},
	{"roic", kindFloat},
	{"return_on_sales", kindFloat},
	{"share_based_compensation", kindInt},
	{"selling_general_and_administrative_expense", kindInt},
	{"share_factor", kindFloat},
	{"shares_basic", kindInt},
	{"weighted_average_shares", kindInt},
	{"weighted_average_shares_diluted", kindInt},
	{"sales_per_share", kindFloat},
	{"tangible_asset_value", kindInt},
	{"tax_assets", kindInt},
	{"income_tax_expense", kindInt},
	{"tax_liabilities", kindInt},
	{"tangible_assets_book_value_per_share", kindFloat},
	{"working_capital", kindInt},
}

func fieldByName(name string) (fundamentalField, bool) {
	for _, f := range fundamentalFields {
		if f.column == name {
			return f, true
		}
	}

	return fundamentalField{}, false
}

// valuesDiffer returns true when a and b differ by more than the configured
// tolerance. A nil pointer represents SQL NULL. Two nulls are equal; a null
// compared to a non-null value always differs. Non-null values satisfy
// "not different" when either |a-b| <= absTol OR |a-b| / max(|a|,|b|) <= relTol.
func valuesDiffer(a, b *float64, relTol, absTol float64) bool {
	if a == nil && b == nil {
		return false
	}

	if a == nil || b == nil {
		return true
	}

	diff := math.Abs(*a - *b)
	if diff == 0 {
		return false
	}

	if diff <= absTol {
		return false
	}

	denom := math.Max(math.Abs(*a), math.Abs(*b))
	if denom == 0 {
		return true
	}

	return diff/denom > relTol
}

// discoverFundamentalsSubscriptions scans the supplied subscriptions and
// returns the table names for the single active sec and sharadar subscriptions
// that publish the fundamentals data type. Errors out if either is missing,
// inactive-only, or present more than once.
func discoverFundamentalsSubscriptions(subs []*library.Subscription) (secTable, sharadarTable string, err error) {
	var (
		secMatches      []*library.Subscription
		sharadarMatches []*library.Subscription
	)

	for _, sub := range subs {
		if !sub.Active {
			continue
		}

		tbl, ok := sub.DataTablesMap[data.FundamentalsKey]
		if !ok || tbl == "" {
			continue
		}

		switch sub.Provider {
		case "sec":
			secMatches = append(secMatches, sub)
		case "sharadar":
			sharadarMatches = append(sharadarMatches, sub)
		}
	}

	if len(secMatches) == 0 {
		return "", "", fmt.Errorf("no active sec subscription with data type %q found", data.FundamentalsKey)
	}

	if len(secMatches) > 1 {
		names := make([]string, 0, len(secMatches))
		for _, s := range secMatches {
			names = append(names, s.Name)
		}

		return "", "", fmt.Errorf("multiple active sec subscriptions with data type %q: %s", data.FundamentalsKey, strings.Join(names, ", "))
	}

	if len(sharadarMatches) == 0 {
		return "", "", fmt.Errorf("no active sharadar subscription with data type %q found", data.FundamentalsKey)
	}

	if len(sharadarMatches) > 1 {
		names := make([]string, 0, len(sharadarMatches))
		for _, s := range sharadarMatches {
			names = append(names, s.Name)
		}

		return "", "", fmt.Errorf("multiple active sharadar subscriptions with data type %q: %s", data.FundamentalsKey, strings.Join(names, ", "))
	}

	return secMatches[0].DataTablesMap[data.FundamentalsKey], sharadarMatches[0].DataTablesMap[data.FundamentalsKey], nil
}

type rawCompareFlags struct {
	tickers        []string
	since          string
	until          string
	dimensions     []string
	fields         []string
	relTol         float64
	absTol         float64
	format         string
	output         string
	excludeForeign bool
	groupBy        string
}

type compareOptions struct {
	tickers    []string
	since      time.Time
	until      time.Time
	dimensions []string
	fields     []fundamentalField
	relTol     float64
	absTol     float64
	format     string
	output     string
	groupBy    string
}

func resolveCompareOptions(raw rawCompareFlags) (compareOptions, error) {
	groupBy := raw.groupBy
	if groupBy == "" {
		groupBy = "ticker"
	}

	opts := compareOptions{
		tickers: raw.tickers,
		relTol:  raw.relTol,
		absTol:  raw.absTol,
		format:  raw.format,
		output:  raw.output,
		groupBy: groupBy,
	}

	if raw.format != "text" && raw.format != "csv" {
		return compareOptions{}, fmt.Errorf("invalid --format %q: must be text or csv", raw.format)
	}

	if groupBy != "ticker" && groupBy != "field" {
		return compareOptions{}, fmt.Errorf("invalid --group-by %q: must be ticker or field", raw.groupBy)
	}

	if raw.since != "" {
		t, err := time.Parse("2006-01-02", raw.since)
		if err != nil {
			return compareOptions{}, fmt.Errorf("invalid --since %q: %w", raw.since, err)
		}

		opts.since = t
	}

	if raw.until != "" {
		t, err := time.Parse("2006-01-02", raw.until)
		if err != nil {
			return compareOptions{}, fmt.Errorf("invalid --until %q: %w", raw.until, err)
		}

		opts.until = t
	}

	for _, d := range raw.dimensions {
		opts.dimensions = append(opts.dimensions, strings.ToUpper(strings.TrimSpace(d)))
	}

	if len(raw.fields) == 0 {
		opts.fields = append(opts.fields, fundamentalFields...)
	} else {
		for _, name := range raw.fields {
			name = strings.TrimSpace(name)

			f, ok := fieldByName(name)
			if !ok {
				return compareOptions{}, fmt.Errorf("unknown fundamental field %q", name)
			}

			opts.fields = append(opts.fields, f)
		}
	}

	return opts, nil
}

// expandTickerAliases takes a list of user-supplied tickers and returns the
// union of those tickers plus every other ticker in either fundamentals table
// that shares a composite_figi with one of them. This bridges the SEC/Sharadar
// ticker-format difference for dual-class securities (SEC "BRK.B" vs Sharadar
// "BRK/B") so the downstream ticker filter returns rows on both sides.
func expandTickerAliases(ctx context.Context, lib *library.Library, secTable, sharadarTable string, tickers []string) ([]string, error) {
	if len(tickers) == 0 {
		return tickers, nil
	}

	sql := fmt.Sprintf(
		`WITH allrows AS (
	SELECT ticker, composite_figi FROM %s WHERE composite_figi != ''
	UNION
	SELECT ticker, composite_figi FROM %s WHERE composite_figi != ''
)
SELECT DISTINCT ticker FROM allrows
WHERE composite_figi IN (SELECT DISTINCT composite_figi FROM allrows WHERE ticker = ANY($1))
   OR ticker = ANY($1)`,
		secTable, sharadarTable,
	)

	rows, err := lib.Pool.Query(ctx, sql, tickers)
	if err != nil {
		return nil, fmt.Errorf("query ticker aliases: %w", err)
	}
	defer rows.Close()

	seen := make(map[string]struct{}, len(tickers))
	for _, t := range tickers {
		seen[t] = struct{}{}
	}

	out := append([]string(nil), tickers...)

	for rows.Next() {
		var t string
		if scanErr := rows.Scan(&t); scanErr != nil {
			return nil, fmt.Errorf("scan ticker alias: %w", scanErr)
		}

		if _, ok := seen[t]; ok {
			continue
		}

		seen[t] = struct{}{}

		out = append(out, t)
	}

	return out, rows.Err()
}

// buildDateKeyQuery returns a SQL statement (and args) that yields the union
// of distinct date_key values across the sec and sharadar tables, filtered by
// tickers/dimensions/date range if configured.
func buildDateKeyQuery(secTable, sharadarTable string, opts compareOptions) (string, []interface{}) {
	var args []interface{}

	placeholder := func(v interface{}) string {
		args = append(args, v)

		return fmt.Sprintf("$%d", len(args))
	}

	where := func() string {
		var parts []string

		if len(opts.tickers) > 0 {
			parts = append(parts, fmt.Sprintf("ticker = ANY(%s)", placeholder(opts.tickers)))
		}

		if len(opts.dimensions) > 0 {
			parts = append(parts, fmt.Sprintf("dimension = ANY(%s)", placeholder(opts.dimensions)))
		}

		if !opts.since.IsZero() {
			parts = append(parts, fmt.Sprintf("date_key >= %s", placeholder(opts.since)))
		}

		if !opts.until.IsZero() {
			parts = append(parts, fmt.Sprintf("date_key <= %s", placeholder(opts.until)))
		}

		if len(parts) == 0 {
			return ""
		}

		return " WHERE " + strings.Join(parts, " AND ")
	}

	secWhere := where()
	sharadarWhere := where()

	sql := fmt.Sprintf(
		`SELECT date_key FROM (
	SELECT DISTINCT date_key FROM %s%s
	UNION
	SELECT DISTINCT date_key FROM %s%s
) dk ORDER BY date_key`,
		secTable, secWhere,
		sharadarTable, sharadarWhere,
	)

	return sql, args
}

// fundamentalRow holds the identifying columns plus one *float64 per numeric
// field (nil = SQL NULL). The values slice is indexed the same as opts.fields.
type fundamentalRow struct {
	ticker        string
	compositeFigi string
	dimension     string
	dateKey       time.Time
	values        []*float64
}

// rowKey identifies a row within a single date_key.
type rowKey struct {
	compositeFigi string
	dimension     string
}

func (r *fundamentalRow) key() rowKey {
	return rowKey{compositeFigi: r.compositeFigi, dimension: r.dimension}
}

// buildRowQuery produces the SELECT that fetches all matching rows for a
// single date_key. Positional args: [filter args..., dateKey].
func buildRowQuery(table string, opts compareOptions) string {
	cols := []string{"ticker", "composite_figi", "dimension", "date_key"}
	for _, f := range opts.fields {
		cols = append(cols, f.column)
	}

	var wherePieces []string

	paramIdx := 0
	nextParam := func() string {
		paramIdx++

		return fmt.Sprintf("$%d", paramIdx)
	}

	if len(opts.tickers) > 0 {
		wherePieces = append(wherePieces, "ticker = ANY("+nextParam()+")")
	}

	if len(opts.dimensions) > 0 {
		wherePieces = append(wherePieces, "dimension = ANY("+nextParam()+")")
	}

	wherePieces = append(wherePieces, "date_key = "+nextParam())

	return fmt.Sprintf(
		"SELECT %s FROM %s WHERE %s",
		strings.Join(cols, ", "), table, strings.Join(wherePieces, " AND "),
	)
}

// buildRowQueryArgs produces the positional argument slice matching
// buildRowQuery for a specific date_key.
func buildRowQueryArgs(opts compareOptions, dateKey time.Time) []any {
	var args []any

	if len(opts.tickers) > 0 {
		args = append(args, opts.tickers)
	}

	if len(opts.dimensions) > 0 {
		args = append(args, opts.dimensions)
	}

	args = append(args, dateKey)

	return args
}

// scanRows reads all rows from `rows` into fundamentalRow values. The caller is
// responsible for closing `rows`.
func scanRows(rows pgx.Rows, fields []fundamentalField) ([]*fundamentalRow, error) {
	var out []*fundamentalRow

	for rows.Next() {
		var (
			ticker   string
			figi     string
			dim      string
			dk       time.Time
			ints     = make([]*int64, 0, len(fields))
			numerics = make([]*pgtype.Numeric, 0, len(fields))
		)

		dests := []any{&ticker, &figi, &dim, &dk}

		for _, f := range fields {
			switch f.kind {
			case kindInt:
				var p *int64

				ints = append(ints, p)
				dests = append(dests, &ints[len(ints)-1])
			case kindFloat:
				n := &pgtype.Numeric{}
				numerics = append(numerics, n)
				dests = append(dests, n)
			}
		}

		if err := rows.Scan(dests...); err != nil {
			return nil, err
		}

		row := &fundamentalRow{
			ticker:        ticker,
			compositeFigi: figi,
			dimension:     dim,
			dateKey:       dk,
			values:        make([]*float64, len(fields)),
		}

		intIdx, numIdx := 0, 0

		for i, f := range fields {
			switch f.kind {
			case kindInt:
				if ints[intIdx] != nil {
					v := float64(*ints[intIdx])
					row.values[i] = &v
				}

				intIdx++
			case kindFloat:
				n := numerics[numIdx]
				if n.Valid {
					f64, err := n.Float64Value()
					if err != nil {
						return nil, fmt.Errorf("numeric conversion for %s: %w", f.column, err)
					}

					if f64.Valid {
						v := f64.Float64
						row.values[i] = &v
					}
				}

				numIdx++
			}
		}

		out = append(out, row)
	}

	return out, rows.Err()
}

// diffKind distinguishes the three diff record flavors.
type diffKind int

const (
	diffField       diffKind = iota // matching row with a field mismatch
	diffMissingSec                  // row present in sharadar but not sec
	diffMissingShar                 // row present in sec but not sharadar
)

type diffRecord struct {
	kind          diffKind
	ticker        string
	compositeFigi string
	dimension     string
	dateKey       time.Time
	field         string   // for diffField
	secValue      *float64 // for diffField
	sharadarValue *float64 // for diffField
}

// diffRowSet diffs the sec and sharadar rows for a single date_key. It returns
// one diffRecord for every differing field, plus missing-row records for keys
// present in only one side.
func diffRowSet(secRows, sharadarRows []*fundamentalRow, fields []fundamentalField, relTol, absTol float64) []diffRecord {
	secByKey := make(map[rowKey]*fundamentalRow, len(secRows))
	for _, r := range secRows {
		secByKey[r.key()] = r
	}

	sharadarByKey := make(map[rowKey]*fundamentalRow, len(sharadarRows))
	for _, r := range sharadarRows {
		sharadarByKey[r.key()] = r
	}

	var out []diffRecord

	// Rows present in both: per-field comparison.
	for key, secRow := range secByKey {
		shRow, ok := sharadarByKey[key]
		if !ok {
			out = append(out, diffRecord{
				kind:          diffMissingShar,
				ticker:        secRow.ticker,
				compositeFigi: secRow.compositeFigi,
				dimension:     secRow.dimension,
				dateKey:       secRow.dateKey,
			})

			continue
		}

		for i, f := range fields {
			if valuesDiffer(secRow.values[i], shRow.values[i], relTol, absTol) {
				out = append(out, diffRecord{
					kind:          diffField,
					ticker:        secRow.ticker,
					compositeFigi: secRow.compositeFigi,
					dimension:     secRow.dimension,
					dateKey:       secRow.dateKey,
					field:         f.column,
					secValue:      secRow.values[i],
					sharadarValue: shRow.values[i],
				})
			}
		}
	}

	// Rows present only in sharadar.
	for key, shRow := range sharadarByKey {
		if _, ok := secByKey[key]; ok {
			continue
		}

		out = append(out, diffRecord{
			kind:          diffMissingSec,
			ticker:        shRow.ticker,
			compositeFigi: shRow.compositeFigi,
			dimension:     shRow.dimension,
			dateKey:       shRow.dateKey,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].compositeFigi != out[j].compositeFigi {
			return out[i].compositeFigi < out[j].compositeFigi
		}

		if !out[i].dateKey.Equal(out[j].dateKey) {
			return out[i].dateKey.Before(out[j].dateKey)
		}

		if out[i].field != out[j].field {
			return out[i].field < out[j].field
		}

		return out[i].dimension < out[j].dimension
	})

	return out
}

type diffWriter interface {
	Write(rec diffRecord) error
	Close() error
}

// mdDiffWriter buffers all diff records and writes them as markdown tables
// grouped by ticker / composite FIGI (or by field if groupBy == "field") on
// Close.
type mdDiffWriter struct {
	w       io.Writer
	recs    []diffRecord
	groupBy string
}

func newMDDiffWriter(w io.Writer, groupBy string) *mdDiffWriter {
	return &mdDiffWriter{w: w, groupBy: groupBy}
}

func (m *mdDiffWriter) Write(rec diffRecord) error {
	m.recs = append(m.recs, rec)
	return nil
}

func (m *mdDiffWriter) Close() error {
	var err error
	if m.groupBy == "field" {
		err = m.writeGroupedByField()
	} else {
		err = m.writeGroupedByTicker()
	}

	if err != nil {
		return err
	}

	fmt.Fprintf(m.w, "\n**Total diffs: %d**\n", len(m.recs))

	return nil
}

func (m *mdDiffWriter) writeGroupedByTicker() error {
	// Group records by (ticker, compositeFigi).
	type groupKey struct{ ticker, figi string }

	groups := make(map[groupKey][]diffRecord)

	var order []groupKey

	for _, r := range m.recs {
		k := groupKey{r.ticker, r.compositeFigi}
		if _, ok := groups[k]; !ok {
			order = append(order, k)
		}

		groups[k] = append(groups[k], r)
	}

	for i, k := range order {
		if i > 0 {
			fmt.Fprintln(m.w)
		}

		fmt.Fprintf(m.w, "## %s (%s)\n\n", k.ticker, k.figi)
		fmt.Fprintln(m.w, "| Date Key | Field | Dimension | SEC | Sharadar | Diff % |")
		fmt.Fprintln(m.w, "|----------|-------|-----------|----:|--------:|-------:|")

		recs := groups[k]
		prevDate := ""
		prevField := ""

		for j, r := range recs {
			curDate := r.dateKey.Format("2006-01-02")

			// Insert a separator row between date groups.
			if j > 0 && curDate != prevDate {
				fmt.Fprintln(m.w, "| | | | | | |")
			}

			// Suppress repeated date and field values.
			displayDate := curDate
			if curDate == prevDate {
				displayDate = ""
			}

			switch r.kind {
			case diffField:
				displayField := r.field
				if curDate == prevDate && r.field == prevField {
					displayField = ""
				}

				_, relDiff := diffStats(r.secValue, r.sharadarValue)
				diffPct := formatDiffPercent(relDiff)
				fmt.Fprintf(m.w, "| %s | %s | %s | %s | %s | %s |\n",
					displayDate, displayField, r.dimension,
					formatValueCompact(r.secValue), formatValueCompact(r.sharadarValue),
					diffPct)

				prevField = r.field
			case diffMissingSec:
				fmt.Fprintf(m.w, "| %s | *(missing in sec)* | %s | | | |\n",
					displayDate, r.dimension)

				prevField = ""
			case diffMissingShar:
				fmt.Fprintf(m.w, "| %s | *(missing in sharadar)* | %s | | | |\n",
					displayDate, r.dimension)

				prevField = ""
			}

			prevDate = curDate
		}
	}

	return nil
}

func (m *mdDiffWriter) writeGroupedByField() error {
	// Missing-row records have no field; emit them under a synthetic group.
	const missingGroup = ""

	groups := make(map[string][]diffRecord)

	var order []string

	for _, r := range m.recs {
		field := r.field
		if r.kind != diffField {
			field = missingGroup
		}

		if _, ok := groups[field]; !ok {
			order = append(order, field)
		}

		groups[field] = append(groups[field], r)
	}

	sort.Slice(order, func(i, j int) bool {
		// Put missing-row group last so it doesn't crowd the real fields.
		if order[i] == missingGroup {
			return false
		}

		if order[j] == missingGroup {
			return true
		}

		return order[i] < order[j]
	})

	for i, field := range order {
		if i > 0 {
			fmt.Fprintln(m.w)
		}

		recs := groups[field]

		sort.SliceStable(recs, func(i, j int) bool {
			if recs[i].ticker != recs[j].ticker {
				return recs[i].ticker < recs[j].ticker
			}

			if !recs[i].dateKey.Equal(recs[j].dateKey) {
				return recs[i].dateKey.Before(recs[j].dateKey)
			}

			return recs[i].dimension < recs[j].dimension
		})

		if field == missingGroup {
			fmt.Fprintf(m.w, "## *(missing rows)*\n\n")
		} else {
			fmt.Fprintf(m.w, "## %s\n\n", field)
		}

		fmt.Fprintln(m.w, "| Ticker | Date Key | Dimension | SEC | Sharadar | Diff % |")
		fmt.Fprintln(m.w, "|--------|----------|-----------|----:|--------:|-------:|")

		prevTicker := ""

		for j, r := range recs {
			// Insert a separator row between ticker groups.
			if j > 0 && r.ticker != prevTicker {
				fmt.Fprintln(m.w, "| | | | | | |")
			}

			displayTicker := r.ticker
			if r.ticker == prevTicker {
				displayTicker = ""
			}

			switch r.kind {
			case diffField:
				_, relDiff := diffStats(r.secValue, r.sharadarValue)
				diffPct := formatDiffPercent(relDiff)
				fmt.Fprintf(m.w, "| %s | %s | %s | %s | %s | %s |\n",
					displayTicker, r.dateKey.Format("2006-01-02"), r.dimension,
					formatValueCompact(r.secValue), formatValueCompact(r.sharadarValue),
					diffPct)
			case diffMissingSec:
				fmt.Fprintf(m.w, "| %s | %s | %s | *(missing in sec)* | | |\n",
					displayTicker, r.dateKey.Format("2006-01-02"), r.dimension)
			case diffMissingShar:
				fmt.Fprintf(m.w, "| %s | %s | %s | | *(missing in sharadar)* | |\n",
					displayTicker, r.dateKey.Format("2006-01-02"), r.dimension)
			}

			prevTicker = r.ticker
		}
	}

	return nil
}

type csvDiffWriter struct {
	w    *csv.Writer
	init bool
}

func newCSVDiffWriter(w io.Writer) *csvDiffWriter {
	return &csvDiffWriter{w: csv.NewWriter(w)}
}

func (c *csvDiffWriter) writeHeader() error {
	if c.init {
		return nil
	}

	c.init = true

	return c.w.Write([]string{"ticker", "composite_figi", "dimension", "date_key", "kind", "field", "sec_value", "sharadar_value", "abs_diff", "rel_diff"})
}

func (c *csvDiffWriter) Write(rec diffRecord) error {
	if err := c.writeHeader(); err != nil {
		return err
	}

	kind := ""
	field := ""
	secVal := ""
	shVal := ""
	absStr := ""
	relStr := ""

	switch rec.kind {
	case diffField:
		kind = "diff"
		field = rec.field
		secVal = formatValue(rec.secValue)
		shVal = formatValue(rec.sharadarValue)
		ad, rd := diffStats(rec.secValue, rec.sharadarValue)
		absStr = formatValue(&ad)
		relStr = fmt.Sprintf("%.6f", rd)
	case diffMissingSec:
		kind = "missing_in_sec"
	case diffMissingShar:
		kind = "missing_in_sharadar"
	}

	return c.w.Write([]string{
		rec.ticker, rec.compositeFigi, rec.dimension, rec.dateKey.Format("2006-01-02"),
		kind, field, secVal, shVal, absStr, relStr,
	})
}

func (c *csvDiffWriter) Close() error {
	if err := c.writeHeader(); err != nil {
		return err
	}

	c.w.Flush()

	return c.w.Error()
}

// formatValue renders a *float64 as either an empty string (for NULL), a
// decimal integer (for whole numbers), or a fixed-precision float.
func formatValue(v *float64) string {
	if v == nil {
		return ""
	}

	if *v == math.Trunc(*v) && math.Abs(*v) < 1e18 {
		return fmt.Sprintf("%d", int64(*v))
	}

	return fmt.Sprintf("%g", *v)
}

// formatValueCompact renders a *float64 with limited precision for
// human-readable text output (6 significant digits). Whole numbers are
// formatted with comma separators (e.g. 348,020,000,000).
func formatValueCompact(v *float64) string {
	if v == nil {
		return ""
	}

	if *v == math.Trunc(*v) && math.Abs(*v) < 1e18 {
		return formatIntWithCommas(int64(*v))
	}

	return fmt.Sprintf("%.6g", *v)
}

// formatIntWithCommas renders an int64 with thousands separators.
func formatIntWithCommas(n int64) string {
	s := fmt.Sprintf("%d", n)

	negative := false
	if s[0] == '-' {
		negative = true
		s = s[1:]
	}

	// Insert commas from the right every 3 digits.
	var b strings.Builder

	for i, ch := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}

		b.WriteRune(ch)
	}

	if negative {
		return "-" + b.String()
	}

	return b.String()
}

// formatDiffPercent renders a relative diff as a percentage string with
// markdown emphasis for larger values: plain below 0.5%, **bold** from 0.5-1%,
// and ***bold italic*** above 1%.
func formatDiffPercent(relDiff float64) string {
	pct := relDiff * 100

	s := fmt.Sprintf("%.2f%%", pct)

	switch {
	case pct >= 1.0:
		return "***" + s + "***"
	case pct >= 0.5:
		return "**" + s + "**"
	default:
		return s
	}
}

// diffStats returns (|a-b|, |a-b|/max(|a|,|b|)). Caller must ensure a and b
// are both non-nil.
func diffStats(a, b *float64) (absDiff, relDiff float64) {
	absDiff = math.Abs(*a - *b)

	denom := math.Max(math.Abs(*a), math.Abs(*b))
	if denom > 0 {
		relDiff = absDiff / denom
	}

	return
}

// cfProgressMsg reports progress from the comparison goroutine.
type cfProgressMsg struct {
	currentDateKey time.Time
	processed      int
	total          int
	diffCount      int
}

// drainProgress reads cfProgressMsg values from progressCh until the channel
// is closed, printing an inline progress line to stderr on each update. When
// the comparison goroutine finishes it reads the final error from doneCh.
func drainProgress(progressCh <-chan cfProgressMsg, doneCh <-chan error, isTTY bool) error {
	start := time.Now()

	for msg := range progressCh {
		if !isTTY {
			continue
		}

		elapsed := time.Since(start).Truncate(time.Second)

		fmt.Fprintf(os.Stderr, "\r\033[K  %d / %d date_keys  diffs: %d  current: %s  (%s)",
			msg.processed, msg.total, msg.diffCount,
			msg.currentDateKey.Format("2006-01-02"), elapsed)
	}

	if isTTY {
		fmt.Fprint(os.Stderr, "\r\033[K") // clear the progress line
	}

	return <-doneCh
}

var compareFundamentalsCmd = &cobra.Command{
	Use:   "compare-fundamentals",
	Short: "Compare fundamentals rows between the SEC and Sharadar providers",
	Long: `compare-fundamentals auto-discovers the active subscriptions with
provider "sec" and "sharadar" that manage the fundamentals data type and prints
the differences between matching rows. Rows are matched on
(composite_figi, dimension, date_key). Fields are compared with a relative
tolerance plus an absolute tolerance floor.`,
	Run: runCompareFundamentals,
}

func runCompareFundamentals(cmd *cobra.Command, args []string) {
	if viper.GetBool("compare-fundamentals.list-fields") {
		for _, f := range fundamentalFields {
			fmt.Println(f.column)
		}

		return
	}

	ctx := context.Background()

	raw := rawCompareFlags{
		tickers:        viper.GetStringSlice("compare-fundamentals.ticker"),
		since:          viper.GetString("compare-fundamentals.since"),
		until:          viper.GetString("compare-fundamentals.until"),
		dimensions:     viper.GetStringSlice("compare-fundamentals.dimension"),
		fields:         viper.GetStringSlice("compare-fundamentals.fields"),
		relTol:         viper.GetFloat64("compare-fundamentals.rel-tol"),
		absTol:         viper.GetFloat64("compare-fundamentals.abs-tol"),
		format:         viper.GetString("compare-fundamentals.format"),
		output:         viper.GetString("compare-fundamentals.output"),
		excludeForeign: viper.GetBool("compare-fundamentals.exclude-foreign"),
		groupBy:        viper.GetString("compare-fundamentals.group-by"),
	}

	opts, err := resolveCompareOptions(raw)
	if err != nil {
		log.Fatal().Err(err).Msg("invalid compare-fundamentals options")
	}

	myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
	if err != nil {
		log.Fatal().Err(err).Msg("could not connect to library")
	}

	defer myLibrary.Close()

	subs, err := myLibrary.Subscriptions(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("could not load subscriptions")
	}

	secTable, sharadarTable, err := discoverFundamentalsSubscriptions(subs)
	if err != nil {
		log.Fatal().Err(err).Msg("could not discover fundamentals subscriptions")
	}

	if raw.excludeForeign {
		conn, connErr := myLibrary.Pool.Acquire(ctx)
		if connErr != nil {
			log.Fatal().Err(connErr).Msg("could not acquire db connection for domestic ticker query")
		}

		domesticSQL := fmt.Sprintf("SELECT DISTINCT ticker FROM %s WHERE dimension = 'ARQ'", secTable)

		arqRows, arqErr := conn.Query(ctx, domesticSQL)
		if arqErr != nil {
			conn.Release()
			log.Fatal().Err(arqErr).Msg("could not query domestic tickers from sec table")
		}

		var domesticTickers []string

		for arqRows.Next() {
			var t string
			if scanErr := arqRows.Scan(&t); scanErr != nil {
				arqRows.Close()
				conn.Release()
				log.Fatal().Err(scanErr).Msg("could not scan domestic ticker")
			}

			domesticTickers = append(domesticTickers, t)
		}

		arqRows.Close()

		if arqErr = arqRows.Err(); arqErr != nil {
			conn.Release()
			log.Fatal().Err(arqErr).Msg("error reading domestic ticker rows")
		}

		conn.Release()

		log.Info().Int("count", len(domesticTickers)).Msg("domestic tickers found (have ARQ data in sec table)")

		if len(opts.tickers) > 0 {
			// Intersect user-provided filter with domestic set.
			domesticSet := make(map[string]struct{}, len(domesticTickers))
			for _, t := range domesticTickers {
				domesticSet[t] = struct{}{}
			}

			filtered := opts.tickers[:0]
			for _, t := range opts.tickers {
				if _, ok := domesticSet[t]; ok {
					filtered = append(filtered, t)
				}
			}

			opts.tickers = filtered
		} else {
			opts.tickers = domesticTickers
		}
	}

	// Expand requested tickers to include cross-provider aliases that share
	// a composite_figi. SEC normalizes tickers with a dot (e.g. BRK.B) while
	// Sharadar uses a slash (BRK/B); without this step, filtering on one
	// form hides every row on the other side.
	if len(opts.tickers) > 0 {
		expanded, expErr := expandTickerAliases(ctx, myLibrary, secTable, sharadarTable, opts.tickers)
		if expErr != nil {
			log.Fatal().Err(expErr).Msg("could not expand ticker aliases")
		}

		opts.tickers = expanded
	}

	isTTY := isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())

	// All formats write into a buffer first so progress output to stderr
	// doesn't interleave with results.
	var buf bytes.Buffer

	var writer diffWriter

	switch opts.format {
	case "csv":
		writer = newCSVDiffWriter(&buf)
	default:
		writer = newMDDiffWriter(&buf, opts.groupBy)
	}

	progressCh := make(chan cfProgressMsg, 8)
	doneCh := make(chan error, 1)

	var totalDiffs int

	go func() {
		defer close(progressCh)

		err := runComparison(ctx, myLibrary, secTable, sharadarTable, opts, writer, progressCh, &totalDiffs)
		doneCh <- err
	}()

	if compErr := drainProgress(progressCh, doneCh, isTTY); compErr != nil {
		log.Fatal().Err(compErr).Msg("comparison failed")
	}

	if cerr := writer.Close(); cerr != nil {
		log.Error().Err(cerr).Msg("closing diff writer")
	}

	// Render markdown through glamour when writing to a TTY.
	output := buf.Bytes()

	if isTTY && opts.format != "csv" {
		width := 120
		if w, _, err := term.GetSize(os.Stdout.Fd()); err == nil && w > 0 {
			width = w
		}

		tr, trErr := glamour.NewTermRenderer(
			glamour.WithStandardStyle("dark"),
			glamour.WithWordWrap(width),
			glamour.WithTableWrap(false),
		)
		if trErr == nil {
			if rendered, gerr := tr.RenderBytes(output); gerr == nil {
				output = rendered
			}
		}
	}

	// Write to file or stdout.
	if opts.output != "" {
		if ferr := os.WriteFile(opts.output, buf.Bytes(), 0o644); ferr != nil {
			log.Fatal().Err(ferr).Str("path", opts.output).Msg("could not write output file")
		}
	} else {
		if _, werr := os.Stdout.Write(output); werr != nil {
			log.Error().Err(werr).Msg("writing output")
		}
	}
}

// runComparison fetches date_keys, loads rows per date_key from both tables,
// diffs them, and emits diffs to `writer`. It sends progress updates on
// progressCh but does not close it (the caller closes it when this returns).
func runComparison(
	ctx context.Context,
	myLibrary *library.Library,
	secTable, sharadarTable string,
	opts compareOptions,
	writer diffWriter,
	progressCh chan<- cfProgressMsg,
	totalDiffs *int,
) error {
	// Phase 1 — date_keys.
	dkSQL, dkArgs := buildDateKeyQuery(secTable, sharadarTable, opts)

	dkRows, err := myLibrary.Pool.Query(ctx, dkSQL, dkArgs...)
	if err != nil {
		return fmt.Errorf("query date_keys: %w", err)
	}

	var dateKeys []time.Time

	for dkRows.Next() {
		var dk time.Time
		if err := dkRows.Scan(&dk); err != nil {
			dkRows.Close()

			return fmt.Errorf("scan date_key: %w", err)
		}

		dateKeys = append(dateKeys, dk)
	}

	dkRows.Close()

	if err := dkRows.Err(); err != nil {
		return fmt.Errorf("date_key rows: %w", err)
	}

	total := len(dateKeys)

	progressCh <- cfProgressMsg{total: total}

	// Phase 2 — per date_key.
	secSQL := buildRowQuery(secTable, opts)
	sharadarSQL := buildRowQuery(sharadarTable, opts)

	for i, dk := range dateKeys {
		args := buildRowQueryArgs(opts, dk)

		secRowsPgx, err := myLibrary.Pool.Query(ctx, secSQL, args...)
		if err != nil {
			return fmt.Errorf("query sec rows: %w", err)
		}

		secRows, err := scanRows(secRowsPgx, opts.fields)
		secRowsPgx.Close()

		if err != nil {
			return fmt.Errorf("scan sec rows: %w", err)
		}

		shRowsPgx, err := myLibrary.Pool.Query(ctx, sharadarSQL, args...)
		if err != nil {
			return fmt.Errorf("query sharadar rows: %w", err)
		}

		sharadarRows, err := scanRows(shRowsPgx, opts.fields)
		shRowsPgx.Close()

		if err != nil {
			return fmt.Errorf("scan sharadar rows: %w", err)
		}

		diffs := diffRowSet(secRows, sharadarRows, opts.fields, opts.relTol, opts.absTol)
		for _, rec := range diffs {
			if err := writer.Write(rec); err != nil {
				return fmt.Errorf("write diff: %w", err)
			}
		}

		*totalDiffs += len(diffs)

		progressCh <- cfProgressMsg{
			currentDateKey: dk,
			processed:      i + 1,
			total:          total,
			diffCount:      *totalDiffs,
		}
	}

	return nil
}

func init() {
	compareFundamentalsCmd.Flags().StringSlice("ticker", nil, "Limit comparison to these tickers (comma-separated)")
	compareFundamentalsCmd.Flags().String("since", "", "Only compare rows with date_key >= this date (YYYY-MM-DD)")
	compareFundamentalsCmd.Flags().String("until", "", "Only compare rows with date_key <= this date (YYYY-MM-DD)")
	compareFundamentalsCmd.Flags().StringSlice("dimension", nil, "Limit comparison to these dimensions (ARQ,MRQ,ARY,MRY,ART,MRT) — default all")
	compareFundamentalsCmd.Flags().StringSlice("fields", nil, "Limit comparison to these fundamental field names — default all")
	compareFundamentalsCmd.Flags().Float64("rel-tol", 0.0001, "Relative tolerance: values differ when |a-b| / max(|a|,|b|) > rel-tol")
	compareFundamentalsCmd.Flags().Float64("abs-tol", 0, "Absolute tolerance floor: values differ only when |a-b| > abs-tol")
	compareFundamentalsCmd.Flags().String("format", "text", "Output format: text or csv")
	compareFundamentalsCmd.Flags().String("output", "", "Write output to this file instead of stdout")
	compareFundamentalsCmd.Flags().Bool("exclude-foreign", true, "Exclude tickers with no ARQ data in the SEC table (foreign filers)")
	compareFundamentalsCmd.Flags().Bool("list-fields", false, "Print the available fundamental field names and exit")
	compareFundamentalsCmd.Flags().String("group-by", "ticker", "Group text output by: ticker or field")

	for _, name := range []string{"ticker", "since", "until", "dimension", "fields", "rel-tol", "abs-tol", "format", "output", "exclude-foreign", "list-fields", "group-by"} {
		if err := viper.BindPFlag("compare-fundamentals."+name, compareFundamentalsCmd.Flags().Lookup(name)); err != nil {
			log.Panic().Err(err).Str("flag", name).Msg("BindPFlag failed for compare-fundamentals")
		}
	}

	rootCmd.AddCommand(compareFundamentalsCmd)
}
