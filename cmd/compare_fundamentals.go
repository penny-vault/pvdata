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

	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

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
	rootCmd.AddCommand(compareFundamentalsCmd)

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
}
