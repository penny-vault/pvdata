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
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
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
	tickers    []string
	since      string
	until      string
	dimensions []string
	fields     []string
	relTol     float64
	absTol     float64
	format     string
	output     string
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
}

func resolveCompareOptions(raw rawCompareFlags) (compareOptions, error) {
	opts := compareOptions{
		tickers: raw.tickers,
		relTol:  raw.relTol,
		absTol:  raw.absTol,
		format:  raw.format,
		output:  raw.output,
	}

	if raw.format != "text" && raw.format != "csv" {
		return compareOptions{}, fmt.Errorf("invalid --format %q: must be text or csv", raw.format)
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

		if out[i].dimension != out[j].dimension {
			return out[i].dimension < out[j].dimension
		}

		return out[i].field < out[j].field
	})

	return out
}

type diffWriter interface {
	Write(rec diffRecord) error
	Close() error
}

type textDiffWriter struct {
	w       io.Writer
	lastKey string
}

func newTextDiffWriter(w io.Writer) *textDiffWriter {
	return &textDiffWriter{w: w}
}

func (t *textDiffWriter) Write(rec diffRecord) error {
	header := fmt.Sprintf("%s  %s  %s  %s",
		rec.ticker, rec.compositeFigi, rec.dateKey.Format("2006-01-02"), rec.dimension)

	if header != t.lastKey {
		if t.lastKey != "" {
			if _, err := fmt.Fprintln(t.w); err != nil {
				return err
			}
		}

		if _, err := fmt.Fprintln(t.w, header); err != nil {
			return err
		}

		t.lastKey = header
	}

	switch rec.kind {
	case diffField:
		absDiff, relDiff := diffStats(rec.secValue, rec.sharadarValue)
		_, err := fmt.Fprintf(t.w, "  %-50s sec=%s  sharadar=%s  abs=%s  rel=%.6f\n",
			rec.field, formatValue(rec.secValue), formatValue(rec.sharadarValue), formatValue(&absDiff), relDiff)

		return err
	case diffMissingSec:
		_, err := fmt.Fprintln(t.w, "  (missing in sec)")

		return err
	case diffMissingShar:
		_, err := fmt.Fprintln(t.w, "  (missing in sharadar)")

		return err
	}

	return nil
}

func (t *textDiffWriter) Close() error { return nil }

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
	ctx := context.Background()

	myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
	if err != nil {
		log.Fatal().Err(err).Msg("could not connect to library")
	}

	defer myLibrary.Close()

	_ = ctx
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

	for _, name := range []string{"ticker", "since", "until", "dimension", "fields", "rel-tol", "abs-tol", "format", "output"} {
		if err := viper.BindPFlag("compare-fundamentals."+name, compareFundamentalsCmd.Flags().Lookup(name)); err != nil {
			log.Panic().Err(err).Str("flag", name).Msg("BindPFlag failed for compare-fundamentals")
		}
	}

	rootCmd.AddCommand(compareFundamentalsCmd)
}
