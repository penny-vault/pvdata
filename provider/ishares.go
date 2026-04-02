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
	"maps"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/figi"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog"
)

type IShares struct{}

type iSharesETF struct {
	ProductID       string    `json:"productId"`
	Slug            string    `json:"slug"`
	IndexName       string    `json:"indexName"`
	BloombergTicker string    `json:"bloombergTicker"`
	InceptionDate   time.Time `json:"-"`
	InceptionStr    string    `json:"inceptionDate"`
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
		if e.InceptionStr != "" {
			t, err := time.Parse("2006-01-02", e.InceptionStr)
			if err != nil {
				panic("failed to parse inceptionDate for " + e.Ticker + ": " + err.Error())
			}

			e.InceptionDate = t
		}

		iSharesETFMap[e.Ticker] = e.iSharesETF
	}
}

func (ishares *IShares) Name() string {
	return "iShares"
}

func (ishares *IShares) ConfigDescription() map[string]string {
	return map[string]string{
		"etfs": "Comma-separated iShares ETF tickers whose holdings define the index. Defaults to all supported ETFs if left empty.",
	}
}

func (ishares *IShares) Description() string {
	return "Track index constituents and membership changes by scraping iShares ETF holdings from BlackRock."
}

func (ishares *IShares) Datasets() map[string]Dataset {
	return map[string]Dataset{
		"Index Constituents": {
			Name:        "Index Constituents",
			Description: "Download index constituent holdings with weights and track membership changes over time.",
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

	// Parse ETF tickers from config; default to all supported ETFs
	etfStr := subscription.Config["etfs"]

	var tickers []string

	if etfStr == "" {
		for t := range iSharesETFMap {
			tickers = append(tickers, t)
		}
	} else {
		tickers = strings.Split(etfStr, ",")
		for i := range tickers {
			tickers[i] = strings.TrimSpace(tickers[i])
		}
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

		n, err := downloadSingleISharesETF(ctx, client, ticker, etf, figiMap, subscription, out)
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
	subscription *library.Subscription,
	out chan<- *data.Observation,
) (int, error) {
	logger := zerolog.Ctx(ctx)
	numObs := 0
	table := subscription.DataTablesMap[data.IndexKey]

	lookback := LookbackFromContext(ctx, 14*24*time.Hour)

	// Build the list of dates to fetch.
	// Always includes today (empty asOfDate = current data).
	type fetchDate struct {
		date     time.Time
		asOfDate string // empty means "today / no asOfDate param"
	}

	var dates []fetchDate

	startDate := time.Now().Add(-lookback)

	if !etf.InceptionDate.IsZero() && startDate.Before(etf.InceptionDate) {
		logger.Info().
			Time("OriginalStart", startDate).
			Time("InceptionDate", etf.InceptionDate).
			Str("Ticker", ticker).
			Msg("clamping lookback start to fund inception date")

		startDate = etf.InceptionDate
	}

	yesterday := time.Now().AddDate(0, 0, -1).Truncate(24 * time.Hour)

	if startDate.Before(yesterday) {
		days, err := tradingDays(ctx, subscription.Library.Pool, startDate, yesterday)
		if err != nil {
			logger.Warn().Err(err).Msg("could not query trading days, falling back to today only")
		} else {
			for _, d := range days {
				dates = append(dates, fetchDate{
					date:     d,
					asOfDate: d.Format("20060102"),
				})
			}
		}
	}

	// Always fetch today's data last (no asOfDate)
	dates = append(dates, fetchDate{
		date: time.Now().UTC().Truncate(24 * time.Hour),
	})

	// Load initial state from DB: last snapshot + changelog entries up to start
	state := currentIndexMembers(ctx, subscription.Library.Pool, table, etf.IndexName, startDate)
	memLastSnapshotDate := lastSnapshotDate(ctx, subscription.Library.Pool, table, etf.IndexName)

	obsTemplate := &data.Observation{
		ObservationDate:  time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}

	for i, fd := range dates {
		// Build URL
		csvURL := fmt.Sprintf(iSharesHoldingsURLTemplate, etf.ProductID, etf.Slug, ticker)
		if fd.asOfDate != "" {
			csvURL += "&asOfDate=" + fd.asOfDate
		}

		logger.Info().Str("URL", csvURL).Str("IndexName", etf.IndexName).Time("Date", fd.date).Msg("downloading iShares holdings CSV")

		resp, err := client.R().SetContext(ctx).Get(csvURL)
		if err != nil {
			logger.Error().Err(err).Str("IndexName", etf.IndexName).Time("Date", fd.date).Msg("HTTP request failed")
			continue
		}

		if resp.StatusCode() != 200 {
			logger.Error().Int("StatusCode", resp.StatusCode()).Str("IndexName", etf.IndexName).Time("Date", fd.date).Msg("HTTP error")
			continue
		}

		csvData := resp.Body()
		logger.Info().Int("Bytes", len(csvData)).Str("IndexName", etf.IndexName).Msg("downloaded iShares holdings CSV")

		parseResult, err := parseISharesCSV(csvData)
		if err != nil {
			logger.Error().Err(err).Str("IndexName", etf.IndexName).Time("Date", fd.date).Msg("could not parse CSV")
			continue
		}

		if len(parseResult.Holdings) == 0 {
			logger.Warn().Str("IndexName", etf.IndexName).Time("Date", fd.date).Msg("no holdings found")
			continue
		}

		logger.Info().
			Int("NumHoldings", len(parseResult.Holdings)).
			Time("SnapshotDate", parseResult.SnapshotDate).
			Str("IndexName", etf.IndexName).
			Msg("parsed iShares holdings")

		// Collect tickers missing from figiMap and resolve via OpenFIGI
		var unknownAssets []*data.Asset

		for _, holding := range parseResult.Holdings {
			if figiMap[holding.Ticker] == "" {
				unknownAssets = append(unknownAssets, &data.Asset{Ticker: holding.Ticker})
			}
		}

		if len(unknownAssets) > 0 {
			logger.Info().
				Int("Count", len(unknownAssets)).
				Str("IndexName", etf.IndexName).
				Msg("resolving unknown tickers via OpenFIGI")

			figi.Enrich(unknownAssets...)

			for _, asset := range unknownAssets {
				if asset.CompositeFigi != "" {
					figiMap[asset.Ticker] = asset.CompositeFigi
				}
			}
		}

		// Build current holdings map -- fail this index if any ticker is unresolved
		currentHoldings := make(map[string]indexMember, len(parseResult.Holdings))
		var unresolved []string

		for _, holding := range parseResult.Holdings {
			f := figiMap[holding.Ticker]
			if f != "" {
				currentHoldings[holding.Ticker] = indexMember{
					CompositeFigi: f,
					Weight:        holding.Weight,
				}
			} else {
				unresolved = append(unresolved, holding.Ticker)
			}
		}

		if len(unresolved) > 0 {
			logger.Error().
				Int("Unresolved", len(unresolved)).
				Int("TotalHoldings", len(parseResult.Holdings)).
				Strs("Tickers", unresolved).
				Str("IndexName", etf.IndexName).
				Msg("aborting index update -- holdings have unresolved FIGIs even after OpenFIGI lookup")

			return numObs, fmt.Errorf("%d holdings for %s have no FIGI", len(unresolved), etf.IndexName)
		}

		// Diff against in-memory state
		added, removed, weightChanged := diffSnapshots(currentHoldings, state)

		eventDate := parseResult.SnapshotDate
		if eventDate.IsZero() {
			eventDate = fd.date
		}

		// Emit changelog: adds and removes
		emitChangelog(added, removed, etf.IndexName, eventDate, obsTemplate, out)
		numObs += len(added) + len(removed)

		// Emit weight changes
		emitWeightChanges(weightChanged, etf.IndexName, eventDate, obsTemplate, out)
		numObs += len(weightChanged)

		// Check if snapshot is due
		if shouldTakeSnapshot(memLastSnapshotDate, eventDate, "yearly") {
			for t, member := range currentHoldings {
				out <- &data.Observation{
					IndexSnapshot: &data.IndexSnapshot{
						Ticker:        t,
						CompositeFigi: member.CompositeFigi,
						IndexName:     etf.IndexName,
						SnapshotDate:  eventDate,
						Weight:        member.Weight,
					},
					ObservationDate:  obsTemplate.ObservationDate,
					SubscriptionID:   obsTemplate.SubscriptionID,
					SubscriptionName: obsTemplate.SubscriptionName,
				}

				numObs++
			}

			memLastSnapshotDate = eventDate

			logger.Info().
				Int("NumConstituents", len(currentHoldings)).
				Str("IndexName", etf.IndexName).
				Time("SnapshotDate", eventDate).
				Msg("emitted index snapshots")
		}

		// Update in-memory state
		maps.Copy(state, added)

		for t := range removed {
			delete(state, t)
		}

		maps.Copy(state, weightChanged)

		// Rate-limit delay between requests (skip after last)
		if i < len(dates)-1 {
			delay := 5*time.Second + time.Duration(rand.IntN(41))*time.Second
			logger.Info().Dur("Delay", delay).Msg("waiting between iShares historical requests")

			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return numObs, ctx.Err()
			}
		}
	}

	return numObs, nil
}
