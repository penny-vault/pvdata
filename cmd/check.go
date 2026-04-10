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
	"sort"
	"strings"

	"github.com/go-resty/resty/v2"
	"github.com/google/uuid"
	"github.com/penny-vault/pvdata/checks"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Run data quality checks against the database",
	Long: `The check sub-command runs audit checks against the database to detect
data quality issues. By default it runs incrementally, only checking data
newer than the last audit. Use --lookback or --full to override.`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		// Parse flags
		lookbackStr := viper.GetString("check.lookback")
		full := viper.GetBool("check.full")
		dataTypes := viper.GetStringSlice("check.data-type")
		checkNames := viper.GetStringSlice("check.check")

		opts := checks.AuditOptions{
			Full:      full,
			DataTypes: dataTypes,
			Checks:    checkNames,
		}

		if lookbackStr != "" {
			lb, err := parseLookback(lookbackStr)
			if err != nil {
				log.Fatal().Err(err).Str("lookback", lookbackStr).Msg("invalid lookback value")
			}

			opts.Lookback = &lb
		}

		// Connect to library
		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not connect to library")
		}

		defer myLibrary.Close()

		// Load subscriptions
		subscriptions, err := myLibrary.Subscriptions(ctx)
		if err != nil {
			log.Fatal().Err(err).Msg("could not load subscriptions")
		}

		runner := checks.NewAuditRunner(checks.AuditChecks(), myLibrary.Pool)

		runID := uuid.New()

		var allResults []checks.CheckResult

		for _, sub := range subscriptions {
			if !sub.Active {
				continue
			}

			for idx, dt := range sub.DataTypes {
				if len(dataTypes) > 0 && !stringSliceContains(dataTypes, dt) {
					continue
				}

				table := sub.DataTables[idx]

				log.Info().
					Str("subscription", sub.Name).
					Str("dataType", dt).
					Str("table", table).
					Msg("running audit checks")

				tableOpts := opts
				tableOpts.DataTypes = []string{dt}

				results, err := runner.Run(ctx, tableOpts, table)
				if err != nil {
					log.Error().Err(err).
						Str("subscription", sub.Name).
						Str("table", table).
						Msg("audit check failed")

					continue
				}

				// Tag results with data type
				for i := range results {
					if results[i].DataType == "" {
						results[i].DataType = dt
					}
				}

				if len(results) > 0 {
					if saveErr := checks.SaveResults(ctx, myLibrary.Pool, results, sub.ID, runID); saveErr != nil {
						log.Error().Err(saveErr).Msg("failed to save check results")
					}
				}

				allResults = append(allResults, results...)
			}
		}

		printCheckSummary(allResults)

		// Ping healthcheck if configured
		if hcID := viper.GetString("healthchecks.data_quality_id"); hcID != "" {
			pingURL := fmt.Sprintf("https://hc-ping.com/%s", hcID)
			client := resty.New()

			if _, pingErr := client.R().Get(pingURL); pingErr != nil {
				log.Warn().Err(pingErr).Str("check_id", hcID).Msg("healthcheck ping failed")
			}
		}

		// Exit non-zero if critical or error severity found
		for _, r := range allResults {
			if r.Severity == checks.SeverityCritical || r.Severity == checks.SeverityError {
				os.Exit(1)
			}
		}
	},
}

func init() {
	checkCmd.Flags().StringP("lookback", "l", "", "Override data lookback period (e.g. 14d, 4w, 6m, 1y)")
	checkCmd.Flags().Bool("full", false, "Run full audit ignoring checkpoints")
	checkCmd.Flags().StringSlice("data-type", nil, "Limit checks to specific data types (e.g. fundamental,eod)")
	checkCmd.Flags().StringSlice("check", nil, "Limit to specific check names")

	if err := viper.BindPFlag("check.lookback", checkCmd.Flags().Lookup("lookback")); err != nil {
		log.Fatal().Err(err).Msg("could not bind check.lookback flag")
	}

	if err := viper.BindPFlag("check.full", checkCmd.Flags().Lookup("full")); err != nil {
		log.Fatal().Err(err).Msg("could not bind check.full flag")
	}

	if err := viper.BindPFlag("check.data-type", checkCmd.Flags().Lookup("data-type")); err != nil {
		log.Fatal().Err(err).Msg("could not bind check.data-type flag")
	}

	if err := viper.BindPFlag("check.check", checkCmd.Flags().Lookup("check")); err != nil {
		log.Fatal().Err(err).Msg("could not bind check.check flag")
	}

	rootCmd.AddCommand(checkCmd)
}

// printCheckSummary prints grouped results by DataType then CheckName and a totals line.
func printCheckSummary(results []checks.CheckResult) {
	if len(results) == 0 {
		fmt.Println("No data quality issues found.")
		return
	}

	titler := cases.Title(language.English)

	// Group: dataType -> checkName -> []CheckResult
	type group struct {
		severity checks.CheckSeverity
		tickers  []string
		count    int
	}

	grouped := make(map[string]map[string]*group)

	for _, r := range results {
		dt := r.DataType
		if dt == "" {
			dt = "unknown"
		}

		if grouped[dt] == nil {
			grouped[dt] = make(map[string]*group)
		}

		g := grouped[dt][r.CheckName]
		if g == nil {
			g = &group{severity: r.Severity}
			grouped[dt][r.CheckName] = g
		}

		g.count++

		if r.Ticker != "" && len(g.tickers) < 5 {
			g.tickers = append(g.tickers, r.Ticker)
		}
	}

	// Sort data types for deterministic output
	dts := make([]string, 0, len(grouped))
	for dt := range grouped {
		dts = append(dts, dt)
	}

	sort.Strings(dts)

	for _, dt := range dts {
		fmt.Printf("\n%s:\n", titler.String(dt))

		checkNames := make([]string, 0, len(grouped[dt]))
		for name := range grouped[dt] {
			checkNames = append(checkNames, name)
		}

		sort.Strings(checkNames)

		for _, name := range checkNames {
			g := grouped[dt][name]
			tickerStr := ""

			if len(g.tickers) > 0 {
				tickerStr = " (" + strings.Join(g.tickers, ", ") + ")"
			}

			fmt.Printf("  [%s] %s: %d issue(s)%s\n", g.severity, name, g.count, tickerStr)
		}
	}

	// Tally totals
	var nCritical, nError, nWarning, nInfo int

	for _, r := range results {
		switch r.Severity {
		case checks.SeverityCritical:
			nCritical++
		case checks.SeverityError:
			nError++
		case checks.SeverityWarning:
			nWarning++
		case checks.SeverityInfo:
			nInfo++
		}
	}

	fmt.Printf("\nTotal: %d critical, %d errors, %d warnings, %d info\n",
		nCritical, nError, nWarning, nInfo)
}

func stringSliceContains(slice []string, s string) bool {
	for _, v := range slice {
		if v == s {
			return true
		}
	}

	return false
}
