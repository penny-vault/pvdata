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
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/playwright_helpers"
	"github.com/playwright-community/playwright-go"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
)

type IShares struct{}

type iSharesETF struct {
	ProductID string `json:"productId"`
	Slug      string `json:"slug"`
	IndexName string `json:"indexName"`
}

//go:embed ishares_etfs.json
var iSharesETFData []byte

var iSharesETFMap map[string]iSharesETF

func init() {
	var entries []struct {
		Ticker string `json:"ticker"`
		iSharesETF
	}

	if err := json.Unmarshal(iSharesETFData, &entries); err != nil {
		panic("failed to parse embedded ishares_etfs.json: " + err.Error())
	}

	iSharesETFMap = make(map[string]iSharesETF, len(entries))
	for _, e := range entries {
		iSharesETFMap[e.Ticker] = e.iSharesETF
	}
}

func (ishares *IShares) Name() string {
	return "iShares"
}

func (ishares *IShares) ConfigDescription() map[string]string {
	return map[string]string{
		"tickers":           "Comma-separated list of iShares ETF tickers to track (e.g. IVV,IWM,IJH)",
		"snapshotFrequency": "How often to take snapshots: daily, weekly, monthly, quarterly (default: weekly)",
	}
}

func (ishares *IShares) Description() string {
	return "iShares by BlackRock provides ETF holdings data. This provider scrapes index constituent holdings with weights from the iShares website."
}

func (ishares *IShares) Datasets() map[string]Dataset {
	return map[string]Dataset{
		"iShares Holdings": {
			Name:        "iShares Holdings",
			Description: "Download ETF holdings and track index membership changes.",
			DataTypes:   []*data.DataType{data.DataTypes[data.IndexKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: downloadISharesHoldings,
		},
	}
}

func downloadISharesHoldings(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
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

	// Parse tickers from config
	tickerStr := subscription.Config["tickers"]
	if tickerStr == "" {
		logger.Error().Msg("no tickers configured for iShares provider")

		runSummary.Status = data.RunFailed

		return
	}

	tickers := strings.Split(tickerStr, ",")
	for i := range tickers {
		tickers[i] = strings.TrimSpace(tickers[i])
	}

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

	// Process each ticker
	for _, ticker := range tickers {
		etf, ok := iSharesETFMap[ticker]
		if !ok {
			logger.Warn().Str("Ticker", ticker).Msg("unknown iShares ETF ticker, skipping")
			continue
		}

		n, err := downloadSingleISharesETF(ctx, page, etf, figiMap, snapshotFrequency, subscription, out)
		if err != nil {
			logger.Error().Err(err).Str("Ticker", ticker).Msg("failed to download iShares ETF holdings")
			continue
		}

		numObs += n
	}

	runSummary.Status = data.RunSuccess
}

func downloadSingleISharesETF(
	ctx context.Context,
	page playwright.Page,
	etf iSharesETF,
	figiMap map[string]string,
	snapshotFrequency string,
	subscription *library.Subscription,
	out chan<- *data.Observation,
) (int, error) {
	logger := zerolog.Ctx(ctx)
	numObs := 0

	// Navigate to the product page
	productURL := fmt.Sprintf("https://www.ishares.com/us/products/%s/%s", etf.ProductID, etf.Slug)
	logger.Info().Str("URL", productURL).Str("IndexName", etf.IndexName).Msg("navigating to iShares product page")

	if _, err := page.Goto(productURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(60000),
	}); err != nil {
		return 0, fmt.Errorf("could not navigate to %s: %w", productURL, err)
	}

	// Download the XLS holdings file
	download, err := page.ExpectDownload(func() error {
		return page.Locator("a[href*='.ajax'][href*='fileType=xls']").Click()
	})
	if err != nil {
		return 0, fmt.Errorf("download failed for %s: %w", etf.IndexName, err)
	}

	path, err := download.Path()
	if err != nil {
		return 0, fmt.Errorf("could not get download path for %s: %w", etf.IndexName, err)
	}

	fileData, err := os.ReadFile(path)
	if err != nil {
		return 0, fmt.Errorf("could not read downloaded file for %s: %w", etf.IndexName, err)
	}

	logger.Info().Int("Bytes", len(fileData)).Str("IndexName", etf.IndexName).Msg("downloaded iShares holdings file")

	// Parse the XML/XLS data
	parseResult, err := parseISharesXML(fileData)
	if err != nil {
		return 0, fmt.Errorf("could not parse iShares XML for %s: %w", etf.IndexName, err)
	}

	if len(parseResult.Holdings) == 0 {
		logger.Warn().Str("IndexName", etf.IndexName).Msg("no holdings found in downloaded file")
		return 0, nil
	}

	logger.Info().
		Int("NumHoldings", len(parseResult.Holdings)).
		Time("SnapshotDate", parseResult.SnapshotDate).
		Str("IndexName", etf.IndexName).
		Msg("parsed iShares holdings")

	// Build current holdings map (ticker -> figi)
	currentHoldings := make(map[string]string, len(parseResult.Holdings))
	for _, holding := range parseResult.Holdings {
		if figi, ok := figiMap[holding.Ticker]; ok {
			currentHoldings[holding.Ticker] = figi
		}
	}

	// Get previous snapshot and emit changelog
	table := subscription.DataTablesMap[data.IndexKey]
	previous := previousSnapshotTickers(ctx, subscription.Library.Pool, table, etf.IndexName)
	added, removed := diffSnapshots(currentHoldings, previous)

	eventDate := parseResult.SnapshotDate
	if eventDate.IsZero() {
		eventDate = time.Now().UTC().Truncate(24 * time.Hour)
	}

	emitChangelog(added, removed, etf.IndexName, eventDate, &data.Observation{
		ObservationDate:  time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}, out)
	numObs += len(added) + len(removed)

	// Check if a snapshot should be taken
	lastDate := lastSnapshotDate(ctx, subscription.Library.Pool, table, etf.IndexName)
	if shouldTakeSnapshot(lastDate, snapshotFrequency) {
		// Build a weight map for quick lookup
		weightMap := make(map[string]float64, len(parseResult.Holdings))
		for _, holding := range parseResult.Holdings {
			weightMap[holding.Ticker] = holding.Weight
		}

		snapshotDate := parseResult.SnapshotDate
		if snapshotDate.IsZero() {
			snapshotDate = time.Now().UTC().Truncate(24 * time.Hour)
		}

		for ticker, figi := range currentHoldings {
			out <- &data.Observation{
				IndexSnapshot: &data.IndexSnapshot{
					Ticker:        ticker,
					CompositeFigi: figi,
					IndexName:     etf.IndexName,
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
			Str("IndexName", etf.IndexName).
			Time("SnapshotDate", snapshotDate).
			Msg("emitted index snapshots")
	}

	return numObs, nil
}
