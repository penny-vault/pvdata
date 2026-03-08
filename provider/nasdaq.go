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
package provider

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/playwright_helpers"
	"github.com/playwright-community/playwright-go"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
)

const NASDAQ_NDX_URL = "https://www.nasdaq.com/market-activity/quotes/nasdaq-ndx-index"

type Nasdaq struct{}

type nasdaqHolding struct {
	Ticker string
	Weight float64
}

func (n *Nasdaq) Name() string {
	return "Nasdaq"
}

func (n *Nasdaq) ConfigDescription() map[string]string {
	return map[string]string{
		"snapshotFrequency": "How often to take snapshots: daily, weekly, monthly, quarterly (default: weekly)",
	}
}

func (n *Nasdaq) Description() string {
	return "Nasdaq provides NDX-100 index constituent data. This provider scrapes index constituent holdings with weights from the Nasdaq website."
}

func (n *Nasdaq) Datasets() map[string]Dataset {
	return map[string]Dataset{
		"NDX-100 Holdings": {
			Name:        "NDX-100 Holdings",
			Description: "Download NDX-100 index holdings and track index membership changes.",
			DataTypes:   []*data.DataType{data.DataTypes[data.IndexKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: downloadNasdaqHoldings,
		},
	}
}

func downloadNasdaqHoldings(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
	logger := zerolog.Ctx(ctx)

	runSummary := data.RunSummary{
		StartTime:        time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}

	numObs := 0

	defer func() {
		runSummary.EndTime = time.Now()
		runSummary.NumObservations = numObs
		exitNotification <- runSummary
	}()

	snapshotFrequency := subscription.Config["snapshotFrequency"]
	if snapshotFrequency == "" {
		snapshotFrequency = "weekly"
	}

	// Acquire DB connection and build figi map
	conn, err := subscription.Library.Pool.Acquire(ctx)
	if err != nil {
		logger.Error().Err(err).Msg("could not acquire database connection")
		runSummary.Status = data.RunFailed
		return
	}
	defer conn.Release()

	assets, err := data.ActiveAssets(ctx, conn)
	if err != nil {
		logger.Error().Err(err).Msg("could not load active assets")
		runSummary.Status = data.RunFailed
		return
	}

	figiMap := make(map[string]string, len(assets))
	for _, asset := range assets {
		figiMap[asset.Ticker] = asset.CompositeFigi
	}

	// Start Playwright
	page, browserContext, browser, pw := playwright_helpers.StartPlaywright(viper.GetBool("playwright.headless"))
	defer playwright_helpers.StopPlaywright(page, browserContext, browser, pw)

	// Navigate to the Nasdaq NDX-100 page
	logger.Info().Str("URL", NASDAQ_NDX_URL).Msg("navigating to Nasdaq NDX-100 page")

	if _, err := page.Goto(NASDAQ_NDX_URL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(60000),
	}); err != nil {
		logger.Error().Err(err).Msg("could not navigate to Nasdaq NDX-100 page")
		runSummary.Status = data.RunFailed
		return
	}

	// Wait for the ARIA role-based table to load
	tableSelector := "[role='table']"
	if err := page.Locator(tableSelector).WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(30000),
	}); err != nil {
		logger.Error().Err(err).Msg("timed out waiting for NDX-100 constituents table")
		runSummary.Status = data.RunFailed
		return
	}

	// Extract data rows only (they have data-row-index attribute, unlike the header row)
	rows, err := page.Locator(fmt.Sprintf("%s [data-row-index]", tableSelector)).All()
	if err != nil {
		logger.Error().Err(err).Msg("could not locate table rows")
		runSummary.Status = data.RunFailed
		return
	}

	logger.Info().Int("NumRows", len(rows)).Msg("found NDX-100 constituent rows")

	holdings := make([]nasdaqHolding, 0, len(rows))
	for _, row := range rows {
		// Ticker is in the first cell, wrapped in an <a> tag
		tickerLink := row.Locator("a[href*='/market-activity/stocks/']").First()
		tickerText, err := tickerLink.InnerText()
		if err != nil {
			continue
		}
		ticker := strings.TrimSpace(tickerText)
		if ticker == "" {
			continue
		}

		// Nasdaq does not provide weight data; weight will be 0
		holdings = append(holdings, nasdaqHolding{
			Ticker: ticker,
		})
	}

	if len(holdings) == 0 {
		logger.Warn().Msg("no holdings found in NDX-100 constituents table")
		runSummary.Status = data.RunFailed
		return
	}

	logger.Info().Int("NumHoldings", len(holdings)).Msg("parsed NDX-100 holdings")

	// Build current holdings map (ticker -> figi)
	currentHoldings := make(map[string]string, len(holdings))
	for _, holding := range holdings {
		if figi, ok := figiMap[holding.Ticker]; ok {
			currentHoldings[holding.Ticker] = figi
		}
	}

	// Get previous snapshot and emit changelog
	table := subscription.DataTablesMap[data.IndexKey]
	previous := previousSnapshotTickers(ctx, subscription.Library.Pool, table, "ndx100")
	added, removed := diffSnapshots(currentHoldings, previous)

	eventDate := time.Now().UTC().Truncate(24 * time.Hour)

	emitChangelog(added, removed, "ndx100", eventDate, &data.Observation{
		ObservationDate:  time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}, out)
	numObs += len(added) + len(removed)

	// Check if a snapshot should be taken
	lastDate := lastSnapshotDate(ctx, subscription.Library.Pool, table, "ndx100")
	if shouldTakeSnapshot(lastDate, snapshotFrequency) {
		// Build a weight map for quick lookup
		weightMap := make(map[string]float64, len(holdings))
		for _, holding := range holdings {
			weightMap[holding.Ticker] = holding.Weight
		}

		snapshotDate := time.Now().UTC().Truncate(24 * time.Hour)

		for ticker, figi := range currentHoldings {
			out <- &data.Observation{
				IndexSnapshot: &data.IndexSnapshot{
					Ticker:        ticker,
					CompositeFigi: figi,
					IndexName:     "ndx100",
					SnapshotDate:  snapshotDate,
					Weight:        weightMap[ticker],
				},
				ObservationDate:  time.Now(),
				SubscriptionID:   subscription.ID,
				SubscriptionName: subscription.Name,
			}
			numObs++
		}

		logger.Info().
			Int("NumSnapshots", len(currentHoldings)).
			Time("SnapshotDate", snapshotDate).
			Msg("emitted NDX-100 index snapshots")
	}

	runSummary.Status = data.RunSuccess
}
