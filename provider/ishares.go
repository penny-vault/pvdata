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
	"math/rand/v2"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog"
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

const iSharesHoldingsURLTemplate = "https://www.ishares.com/us/products/%s/%s/1467271812596.ajax?fileType=csv&fileName=%s_holdings&dataType=fund"

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

	// Create HTTP client
	client := resty.New().
		SetRetryCount(3).
		SetRetryWaitTime(10*time.Second).
		SetRetryMaxWaitTime(60*time.Second).
		AddRetryCondition(func(r *resty.Response, err error) bool {
			return err != nil || r.StatusCode() == 429 || r.StatusCode() >= 500
		}).
		SetTimeout(60*time.Second).
		SetHeader("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36").
		SetHeader("Accept", "text/csv,text/plain,*/*")

	// Process each ticker
	for i, ticker := range tickers {
		etf, ok := iSharesETFMap[ticker]
		if !ok {
			logger.Warn().Str("Ticker", ticker).Msg("unknown iShares ETF ticker, skipping")
			continue
		}

		n, err := downloadSingleISharesETF(ctx, client, ticker, etf, figiMap, snapshotFrequency, subscription, out)
		if err != nil {
			logger.Error().Err(err).Str("Ticker", ticker).Msg("failed to download iShares ETF holdings")
			continue
		}

		numObs += n

		// Randomized delay between requests (5-45 seconds), skip after last ticker
		if i < len(tickers)-1 {
			delay := 5*time.Second + time.Duration(rand.IntN(41))*time.Second
			logger.Info().Dur("Delay", delay).Msg("waiting between iShares requests")
			time.Sleep(delay)
		}
	}

	runSummary.Status = data.RunSuccess
}

func downloadSingleISharesETF(
	ctx context.Context,
	client *resty.Client,
	ticker string,
	etf iSharesETF,
	figiMap map[string]string,
	snapshotFrequency string,
	subscription *library.Subscription,
	out chan<- *data.Observation,
) (int, error) {
	logger := zerolog.Ctx(ctx)
	numObs := 0

	// Download CSV holdings file
	csvURL := fmt.Sprintf(iSharesHoldingsURLTemplate, etf.ProductID, etf.Slug, ticker)
	logger.Info().Str("URL", csvURL).Str("IndexName", etf.IndexName).Msg("downloading iShares holdings CSV")

	resp, err := client.R().SetContext(ctx).Get(csvURL)
	if err != nil {
		return 0, fmt.Errorf("HTTP request failed for %s: %w", etf.IndexName, err)
	}

	if resp.StatusCode() != 200 {
		return 0, fmt.Errorf("HTTP %d for %s", resp.StatusCode(), etf.IndexName)
	}

	csvData := resp.Body()
	logger.Info().Int("Bytes", len(csvData)).Str("IndexName", etf.IndexName).Msg("downloaded iShares holdings CSV")

	// Parse the CSV data
	parseResult, err := parseISharesCSV(csvData)
	if err != nil {
		return 0, fmt.Errorf("could not parse iShares CSV for %s: %w", etf.IndexName, err)
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

		for t, figi := range currentHoldings {
			out <- &data.Observation{
				IndexSnapshot: &data.IndexSnapshot{
					Ticker:        t,
					CompositeFigi: figi,
					IndexName:     etf.IndexName,
					SnapshotDate:  snapshotDate,
					Weight:        weightMap[t],
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
