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
package ishares

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
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
)

type IShares struct{}

type iSharesETF struct {
	ProductID       string    `json:"productId"`
	Slug            string    `json:"slug"`
	IndexName       string    `json:"indexName"`
	IndexTicker     string    `json:"indexTicker"`
	BloombergTicker string    `json:"bloombergTicker"`
	InceptionDate   time.Time `json:"-"`
	InceptionStr    string    `json:"inceptionDate"`
}

//go:embed ishares_etfs.json
var iSharesETFData []byte

var iSharesETFMap map[string]iSharesETF

func init() {
	provider.Register("ishares", &IShares{})

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

func (ishares *IShares) Datasets() map[string]provider.Dataset {
	return map[string]provider.Dataset{
		"Index Constituents": {
			Name:        "Index Constituents",
			Description: "Download index constituent holdings with weights and track membership changes over time.",
			DataTypes: []*data.DataType{
				data.DataTypes[data.IndexSnapshotKey],
				data.DataTypes[data.IndexChangelogKey],
			},
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
			tickers[i] = strings.ToUpper(strings.TrimSpace(tickers[i]))
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
	assetNameMap := make(map[string]string, len(assets))

	for _, asset := range assets {
		figiMap[asset.Ticker] = asset.CompositeFigi
		assetNameMap[asset.Ticker] = asset.Name
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

		n, err := downloadSingleISharesETF(ctx, client, ticker, etf, figiMap, assetNameMap, subscription, out)
		if err != nil {
			logger.Error().Err(err).Str("Ticker", ticker).Msg("failed to download iShares ETF holdings")
			continue
		}

		numObs += n

		// Randomized delay between requests (2-10 seconds), skip after last ticker
		if i < len(tickers)-1 {
			delay := 2*time.Second + time.Duration(rand.IntN(9))*time.Second
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
	assetNameMap map[string]string,
	subscription *library.Subscription,
	out chan<- *data.Observation,
) (int, error) {
	logger := zerolog.Ctx(ctx)
	numObs := 0
	snapshotTable := subscription.DataTablesMap[data.IndexSnapshotKey]
	changelogTable := subscription.DataTablesMap[data.IndexChangelogKey]

	lookback := provider.LookbackFromContext(ctx, 14*24*time.Hour)

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
		days, err := provider.TradingDays(ctx, subscription.Library.Pool, startDate, yesterday)
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
	state := provider.CurrentIndexMembers(ctx, subscription.Library.Pool, snapshotTable, changelogTable, etf.IndexTicker, startDate)
	memLastSnapshotDate := provider.LastSnapshotDate(ctx, subscription.Library.Pool, snapshotTable, etf.IndexTicker)

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

		logger.Info().Str("URL", csvURL).Str("IndexTicker", etf.IndexTicker).Time("Date", fd.date).Msg("downloading iShares holdings CSV")

		resp, err := client.R().SetContext(ctx).Get(csvURL)
		if err != nil {
			logger.Error().Err(err).Str("IndexTicker", etf.IndexTicker).Time("Date", fd.date).Msg("HTTP request failed")
			continue
		}

		if resp.StatusCode() != 200 {
			logger.Error().Int("StatusCode", resp.StatusCode()).Str("IndexTicker", etf.IndexTicker).Time("Date", fd.date).Msg("HTTP error")
			continue
		}

		csvData := resp.Body()
		logger.Info().Int("Bytes", len(csvData)).Str("IndexTicker", etf.IndexTicker).Msg("downloaded iShares holdings CSV")

		parseResult, err := parseISharesCSV(csvData)
		if err != nil {
			logger.Error().Err(err).Str("IndexTicker", etf.IndexTicker).Time("Date", fd.date).Msg("could not parse CSV")
			continue
		}

		if len(parseResult.Holdings) == 0 {
			logger.Warn().Str("IndexTicker", etf.IndexTicker).Time("Date", fd.date).Msg("no holdings found")
			continue
		}

		logger.Info().
			Int("NumHoldings", len(parseResult.Holdings)).
			Time("SnapshotDate", parseResult.SnapshotDate).
			Str("IndexTicker", etf.IndexTicker).
			Msg("parsed iShares holdings")

		// Resolve tickers missing from figiMap:
		// 1. Try share class match (e.g. BRKB -> BRK/B) verified by name similarity
		// 2. Fall back to OpenFIGI for truly unknown tickers
		var unknownAssets []*data.Asset

		for _, holding := range parseResult.Holdings {
			if figiMap[holding.Ticker] != "" {
				continue
			}

			if provider.ResolveShareClass(holding.Ticker, holding.Name, figiMap, assetNameMap, logger) {
				continue
			}

			unknownAssets = append(unknownAssets, &data.Asset{Ticker: holding.Ticker, Name: holding.Name})
		}

		if len(unknownAssets) > 0 {
			unknownTickers := make([]string, len(unknownAssets))
			for j, a := range unknownAssets {
				unknownTickers[j] = a.Ticker
			}

			logger.Info().
				Int("Count", len(unknownAssets)).
				Strs("Tickers", unknownTickers).
				Str("IndexTicker", etf.IndexTicker).
				Msg("resolving unknown tickers via OpenFIGI")

			figi.Enrich(unknownAssets...)

			for _, asset := range unknownAssets {
				if asset.CompositeFigi != "" {
					figiMap[asset.Ticker] = asset.CompositeFigi

					// Emit asset so it gets saved to the database
					out <- &data.Observation{
						AssetObject:      asset,
						ObservationDate:  obsTemplate.ObservationDate,
						SubscriptionID:   obsTemplate.SubscriptionID,
						SubscriptionName: obsTemplate.SubscriptionName,
					}

					numObs++
				}
			}
		}

		// Build current holdings map -- fail this index if any ticker is unresolved
		currentHoldings := make(map[string]provider.IndexMember, len(parseResult.Holdings))

		var unresolvedHoldings []iSharesHolding

		for _, holding := range parseResult.Holdings {
			f := figiMap[holding.Ticker]
			if f != "" {
				currentHoldings[holding.Ticker] = provider.IndexMember{
					CompositeFigi: f,
					Weight:        holding.Weight,
				}
			} else {
				unresolvedHoldings = append(unresolvedHoldings, holding)
			}
		}

		if len(unresolvedHoldings) > 0 {
			// Separate near-zero weight holdings (safe to omit) from
			// significant holdings (must abort).
			var significant, negligible []string

			for _, h := range unresolvedHoldings {
				detail := h.Ticker + " (" + h.Name + " / " + h.Exchange + ")"

				if h.Weight < 0.0001 { // < 0.01%
					negligible = append(negligible, detail)
				} else {
					significant = append(significant, detail)
				}
			}

			if len(negligible) > 0 {
				logger.Warn().
					Int("Count", len(negligible)).
					Strs("Holdings", negligible).
					Str("IndexTicker", etf.IndexTicker).
					Msg("omitting near-zero weight holdings with unresolved FIGIs")
			}

			if len(significant) > 0 {
				logger.Error().
					Int("Unresolved", len(significant)).
					Int("TotalHoldings", len(parseResult.Holdings)).
					Strs("Holdings", significant).
					Str("IndexTicker", etf.IndexTicker).
					Msg("aborting index update -- holdings have unresolved FIGIs even after OpenFIGI lookup")

				return numObs, fmt.Errorf("%d holdings for %s have no FIGI", len(significant), etf.IndexTicker)
			}
		}

		// Diff against in-memory state
		added, removed, weightChanged := provider.DiffSnapshots(currentHoldings, state)

		eventDate := parseResult.SnapshotDate
		if eventDate.IsZero() {
			eventDate = fd.date
		}

		// Emit changelog: adds and removes
		provider.EmitChangelog(added, removed, etf.IndexTicker, eventDate, obsTemplate, out)
		numObs += len(added) + len(removed)

		// Emit weight changes
		provider.EmitWeightChanges(weightChanged, etf.IndexTicker, eventDate, obsTemplate, out)
		numObs += len(weightChanged)

		// Check if snapshot is due
		if provider.ShouldTakeSnapshot(memLastSnapshotDate, eventDate, "yearly") {
			constituents := make([]data.IndexConstituent, 0, len(currentHoldings))
			for t, member := range currentHoldings {
				constituents = append(constituents, data.IndexConstituent{
					Ticker:        t,
					CompositeFigi: member.CompositeFigi,
					Weight:        member.Weight,
				})
			}

			out <- &data.Observation{
				IndexSnapshot: &data.IndexSnapshot{
					IndexTicker:  etf.IndexTicker,
					SnapshotDate: eventDate,
					Constituents: constituents,
				},
				ObservationDate:  obsTemplate.ObservationDate,
				SubscriptionID:   obsTemplate.SubscriptionID,
				SubscriptionName: obsTemplate.SubscriptionName,
			}

			numObs++
			memLastSnapshotDate = eventDate

			logger.Info().
				Int("NumConstituents", len(constituents)).
				Str("IndexTicker", etf.IndexTicker).
				Time("SnapshotDate", eventDate).
				Msg("emitted index snapshot")
		}

		// Update in-memory state
		maps.Copy(state, added)

		for t := range removed {
			delete(state, t)
		}

		maps.Copy(state, weightChanged)

		// Rate-limit delay between requests (skip after last)
		if i < len(dates)-1 {
			delay := 2*time.Second + time.Duration(rand.IntN(9))*time.Second
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
