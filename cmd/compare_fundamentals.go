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
	"fmt"
	"math"
	"strings"

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
