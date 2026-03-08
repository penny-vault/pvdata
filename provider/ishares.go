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
	ProductID string
	Slug      string
	IndexName string
}

var iSharesETFMap = map[string]iSharesETF{
	"IVV":  {ProductID: "239726", Slug: "ishares-core-s-p-500-etf", IndexName: "sp500"},
	"IWB":  {ProductID: "239707", Slug: "ishares-russell-1000-etf", IndexName: "russell-1000"},
	"IWD":  {ProductID: "239708", Slug: "ishares-russell-1000-value-etf", IndexName: "russell-1000-value"},
	"IWF":  {ProductID: "239706", Slug: "ishares-russell-1000-growth-etf", IndexName: "russell-1000-growth"},
	"IWM":  {ProductID: "239710", Slug: "ishares-russell-2000-etf", IndexName: "russell-2000"},
	"IJH":  {ProductID: "239763", Slug: "ishares-core-s-p-mid-cap-etf", IndexName: "sp-mid-cap-400"},
	"IJR":  {ProductID: "239774", Slug: "ishares-core-s-p-small-cap-etf", IndexName: "sp-small-cap-600"},
	"IXUS": {ProductID: "244048", Slug: "ishares-core-msci-total-international-stock-etf", IndexName: "msci-total-intl"},
	"IEFA": {ProductID: "244049", Slug: "ishares-core-msci-eafe-etf", IndexName: "msci-eafe"},
	"IEMG": {ProductID: "244050", Slug: "ishares-core-msci-emerging-markets-etf", IndexName: "msci-emerging"},
	"IVW":  {ProductID: "239725", Slug: "ishares-s-p-500-growth-etf", IndexName: "sp500-growth"},
	"IVE":  {ProductID: "239728", Slug: "ishares-s-p-500-value-etf", IndexName: "sp500-value"},
	"ITOT": {ProductID: "239724", Slug: "ishares-core-s-p-total-u-s-stock-market-etf", IndexName: "sp-total-us"},
	"IWV":  {ProductID: "239714", Slug: "ishares-russell-3000-etf", IndexName: "russell-3000"},
	"IWR":  {ProductID: "239718", Slug: "ishares-russell-mid-cap-etf", IndexName: "russell-mid-cap"},
	"IWS":  {ProductID: "239719", Slug: "ishares-russell-mid-cap-value-etf", IndexName: "russell-mid-cap-value"},
	"IWP":  {ProductID: "239717", Slug: "ishares-russell-mid-cap-growth-etf", IndexName: "russell-mid-cap-growth"},
	"IWO":  {ProductID: "239709", Slug: "ishares-russell-2000-growth-etf", IndexName: "russell-2000-growth"},
	"IWN":  {ProductID: "239711", Slug: "ishares-russell-2000-value-etf", IndexName: "russell-2000-value"},
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
