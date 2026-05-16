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
	"os"
	"strings"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var figiCmd = &cobra.Command{
	Use:   "figi <ticker> <date>",
	Short: "Resolve the composite FIGI for a ticker on a given date",
	Long: `Resolve the composite FIGI that was active for <ticker> on <date>,
using the shared data.AssetHistory index that providers consult when
assigning FIGIs to imported observations.

<date> is parsed as YYYY-MM-DD. Prints the matching FIGI to stdout
(pipe-friendly) and the matching asset name and listing window to
stderr. Exits 1 with the known windows for the ticker if no window
covers the supplied date.

Example:
  pvdata figi BBI 2010-06-01
  pvdata figi BBI 2020-06-01`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		ticker := strings.ToUpper(strings.TrimSpace(args[0]))

		date, err := time.Parse("2006-01-02", strings.TrimSpace(args[1]))
		if err != nil {
			log.Fatal().Err(err).Str("date", args[1]).Msg("invalid date; use YYYY-MM-DD")
		}

		ctx := context.Background()

		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not connect to library")
		}

		conn, err := myLibrary.AcquireWithTimeout(ctx)
		if err != nil {
			log.Fatal().Err(err).Msg("could not acquire database connection")
		}

		assets, err := data.AllAssets(ctx, conn)
		conn.Release()

		if err != nil {
			log.Fatal().Err(err).Msg("could not load assets")
		}

		history := data.NewAssetHistory(assets)

		match, ok := history.AssetAt(ticker, date)
		if !ok {
			windows := history.WindowsFor(ticker)
			if len(windows) == 0 {
				fmt.Fprintf(os.Stderr, "no asset rows found for ticker %s\n", ticker)
			} else {
				fmt.Fprintf(os.Stderr, "no FIGI window covers %s on %s; %d known window(s):\n", ticker, date.Format("2006-01-02"), len(windows))

				for _, a := range windows {
					fmt.Fprintf(os.Stderr, "  %s  listed=%s  delisted=%s  active=%t  %s\n",
						a.CompositeFigi,
						formatAssetDate(a.ListingDate),
						formatAssetDate(a.DelistingDate),
						a.Active,
						a.Name,
					)
				}
			}

			os.Exit(1)
		}

		fmt.Fprintf(os.Stderr, "%s on %s -> %s (%s); listed=%s delisted=%s active=%t\n",
			ticker,
			date.Format("2006-01-02"),
			match.CompositeFigi,
			match.Name,
			formatAssetDate(match.ListingDate),
			formatAssetDate(match.DelistingDate),
			match.Active,
		)
		fmt.Println(match.CompositeFigi)
	},
}

func formatAssetDate(s string) string {
	if s == "" {
		return "-"
	}

	for _, layout := range []string{
		"2006-01-02T15:04:05.000000Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Format("2006-01-02")
		}
	}

	return s
}

func init() {
	rootCmd.AddCommand(figiCmd)
}
