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
package tradingview

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"maps"
	"math/rand/v2"
	"net/url"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/figi"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type TradingView struct{}

type tvIndex struct {
	Symbol   string `json:"symbol"`
	Name     string `json:"indexName"`
	SymbolID string `json:"symbolID"`
}

type tvHolding struct {
	Ticker string
	Name   string
}

type tvParseResult struct {
	TotalCount int
	Holdings   []tvHolding
}

//go:embed tradingview_indexes.json
var tradingViewIndexData []byte

var indexMap map[string]tvIndex

func init() {
	provider.Register("tradingview", &TradingView{})

	var entries []tvIndex

	if err := json.Unmarshal(tradingViewIndexData, &entries); err != nil {
		panic("failed to parse embedded tradingview_indexes.json: " + err.Error())
	}

	indexMap = make(map[string]tvIndex, len(entries))
	for _, e := range entries {
		indexMap[e.Symbol] = e
	}
}

func (tv *TradingView) Name() string {
	return "TradingView"
}

func (tv *TradingView) ConfigDescription() map[string]string {
	return map[string]string{
		"indexes": "Comma-separated index symbols to track (e.g. SPX,NDX). Defaults to all supported indexes if left empty.",
	}
}

func (tv *TradingView) Description() string {
	return "Track index constituents and membership changes by querying TradingView's screener API."
}

func (tv *TradingView) Datasets() map[string]provider.Dataset {
	return map[string]provider.Dataset{
		"Index Constituents": {
			Name:        "Index Constituents",
			Description: "Download index constituent membership and track changes over time.",
			DataTypes: []*data.DataType{
				data.DataTypes[data.IndexSnapshotKey],
				data.DataTypes[data.IndexChangelogKey],
			},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: downloadTradingViewConstituents,
		},
	}
}

const screenerURL = "https://screener-facade.tradingview.com/screener-facade/api/v1/screener-table/scan"

const requestBody = `{"lang":"en","range":[0,5000],"sort":{"sortBy":{"id":"MarketCap","params":{}},"sortOrder":"desc","nullsFirst":false},"scanner_product_label":"symbols-components"}`

// normalizeTVTicker strips the "EXCHANGE:" prefix from a TradingView symbol
// and converts dots to slashes for share class tickers (e.g. MOG.A -> MOG/A).
func normalizeTVTicker(symbol string) string {
	if idx := strings.Index(symbol, ":"); idx >= 0 {
		symbol = symbol[idx+1:]
	}

	symbol = strings.ReplaceAll(symbol, ".", "/")

	return symbol
}

// tvResponse mirrors the JSON structure returned by the TradingView screener API.
type tvResponse struct {
	TotalCount int      `json:"totalCount"`
	Symbols    []string `json:"symbols"`
	Data       []struct {
		ID        string `json:"id"`
		RawValues []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"rawValues"`
	} `json:"data"`
}

// parseTradingViewResponse parses the JSON response from TradingView's screener API.
func parseTradingViewResponse(jsonData []byte) (*tvParseResult, error) {
	var resp tvResponse

	if err := json.Unmarshal(jsonData, &resp); err != nil {
		return nil, fmt.Errorf("could not parse TradingView response: %w", err)
	}

	// Build a map of ticker -> description from the TickerUniversal data block.
	nameMap := make(map[string]string)

	for _, block := range resp.Data {
		if block.ID == "TickerUniversal" {
			for _, rv := range block.RawValues {
				nameMap[rv.Name] = rv.Description
			}

			break
		}
	}

	holdings := make([]tvHolding, 0, len(resp.Symbols))

	for _, sym := range resp.Symbols {
		ticker := normalizeTVTicker(sym)
		holdings = append(holdings, tvHolding{
			Ticker: ticker,
			Name:   nameMap[ticker],
		})
	}

	return &tvParseResult{
		TotalCount: resp.TotalCount,
		Holdings:   holdings,
	}, nil
}

func downloadTradingViewConstituents(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
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

	tickerFilter, figiFilter := provider.SecurityFilterFromContext(ctx)
	if tickerFilter != "" || figiFilter != "" {
		log.Info().Str("provider", "tradingview").Msg("ticker/FIGI filtering not applicable to this provider, skipping")
		return
	}

	// Parse index symbols from config; default to all supported indexes.
	indexStr := subscription.Config["indexes"]

	var symbols []string

	if indexStr == "" {
		for s := range indexMap {
			symbols = append(symbols, s)
		}
	} else {
		symbols = strings.Split(indexStr, ",")
		for i := range symbols {
			symbols[i] = strings.ToUpper(strings.TrimSpace(symbols[i]))
		}
	}

	// Acquire DB connection and build FIGI map.
	conn, err := subscription.Library.AcquireWithTimeout(ctx)
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

	// Create HTTP client.
	client := resty.New().
		SetRetryCount(3).
		SetRetryWaitTime(10*time.Second).
		SetRetryMaxWaitTime(60*time.Second).
		AddRetryCondition(func(r *resty.Response, err error) bool {
			return err != nil || r.StatusCode() == 429 || r.StatusCode() >= 500
		}).
		SetTimeout(60*time.Second).
		SetHeader("Accept", "application/json").
		SetHeader("Content-Type", "text/plain;charset=UTF-8").
		SetHeader("Origin", "https://www.tradingview.com").
		SetHeader("Referer", "https://www.tradingview.com/").
		SetHeader("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	// Process each index.
	for i, symbol := range symbols {
		idx, ok := indexMap[symbol]
		if !ok {
			logger.Warn().Str("Symbol", symbol).Msg("unknown TradingView index symbol, skipping")
			continue
		}

		n, err := downloadSingleIndex(ctx, client, idx, figiMap, assetNameMap, subscription, out)
		if err != nil {
			logger.Error().Err(err).Str("Symbol", symbol).Msg("failed to download TradingView index constituents")
			continue
		}

		numObs += n

		// Randomized delay between requests (30s to 2m), skip after last.
		if i < len(symbols)-1 {
			delay := 30*time.Second + time.Duration(rand.IntN(91))*time.Second
			logger.Info().Dur("Delay", delay).Msg("waiting between TradingView index requests")

			select {
			case <-time.After(delay):
			case <-ctx.Done():
				runSummary.Status = data.RunFailed

				return
			}
		}
	}

	runSummary.Status = data.RunSuccess
}

func downloadSingleIndex(
	ctx context.Context,
	client *resty.Client,
	idx tvIndex,
	figiMap map[string]string,
	assetNameMap map[string]string,
	subscription *library.Subscription,
	out chan<- *data.Observation,
) (int, error) {
	logger := zerolog.Ctx(ctx)
	numObs := 0
	snapshotTable := subscription.DataTablesMap[data.IndexSnapshotKey]
	changelogTable := subscription.DataTablesMap[data.IndexChangelogKey]

	// Fetch constituents from TradingView.
	encodedSymbolID := url.QueryEscape(idx.SymbolID)

	fullURL := fmt.Sprintf("%s?table_id=symbols.components&version=54&columnset_id=overview&symbol_constituents_id=%s",
		screenerURL, encodedSymbolID)

	logger.Info().Str("URL", fullURL).Str("IndexTicker", idx.Symbol).Msg("downloading TradingView index constituents")

	resp, err := client.R().SetContext(ctx).SetBody(requestBody).Post(fullURL)
	if err != nil {
		return 0, fmt.Errorf("HTTP request failed for %s: %w", idx.Symbol, err)
	}

	if resp.StatusCode() != 200 {
		return 0, fmt.Errorf("HTTP error %d for %s", resp.StatusCode(), idx.Symbol)
	}

	parseResult, err := parseTradingViewResponse(resp.Body())
	if err != nil {
		return 0, fmt.Errorf("could not parse response for %s: %w", idx.Symbol, err)
	}

	if len(parseResult.Holdings) == 0 {
		logger.Warn().Str("IndexTicker", idx.Symbol).Msg("no holdings found")
		return 0, nil
	}

	logger.Info().
		Int("NumHoldings", len(parseResult.Holdings)).
		Int("TotalCount", parseResult.TotalCount).
		Str("IndexTicker", idx.Symbol).
		Msg("parsed TradingView index constituents")

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

	obsTemplate := &data.Observation{
		ObservationDate:  time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}

	if len(unknownAssets) > 0 {
		unknownTickers := make([]string, len(unknownAssets))
		for j, a := range unknownAssets {
			unknownTickers[j] = a.Ticker
		}

		logger.Info().
			Int("Count", len(unknownAssets)).
			Strs("Tickers", unknownTickers).
			Str("IndexTicker", idx.Symbol).
			Msg("resolving unknown tickers via OpenFIGI")

		figi.Enrich(unknownAssets...)

		for _, asset := range unknownAssets {
			if asset.CompositeFigi != "" {
				figiMap[asset.Ticker] = asset.CompositeFigi

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

	// Build current holdings map -- abort if any ticker is unresolved.
	currentHoldings := make(map[string]provider.IndexMember, len(parseResult.Holdings))

	var unresolvedTickers []string

	for _, holding := range parseResult.Holdings {
		f := figiMap[holding.Ticker]
		if f != "" {
			currentHoldings[holding.Ticker] = provider.IndexMember{
				CompositeFigi: f,
				Weight:        0,
			}
		} else {
			unresolvedTickers = append(unresolvedTickers, holding.Ticker+" ("+holding.Name+")")
		}
	}

	if len(unresolvedTickers) > 0 {
		logger.Error().
			Int("Unresolved", len(unresolvedTickers)).
			Int("TotalHoldings", len(parseResult.Holdings)).
			Strs("Holdings", unresolvedTickers).
			Str("IndexTicker", idx.Symbol).
			Msg("aborting index update -- holdings have unresolved FIGIs even after OpenFIGI lookup")

		return numObs, fmt.Errorf("%d holdings for %s have no FIGI", len(unresolvedTickers), idx.Symbol)
	}

	// Compute today's date as event date.
	eventDate := time.Now().UTC().Truncate(24 * time.Hour)

	// Load current state from DB and diff.
	state := provider.CurrentIndexMembers(ctx, subscription.Library.Pool, snapshotTable, changelogTable, idx.Symbol, eventDate)
	memLastSnapshotDate := provider.LastSnapshotDate(ctx, subscription.Library.Pool, snapshotTable, idx.Symbol)

	added, removed, _ := provider.DiffSnapshots(currentHoldings, state)

	// Emit changelog: adds and removes.
	provider.EmitChangelog(added, removed, idx.Symbol, eventDate, obsTemplate, out)
	numObs += len(added) + len(removed)

	// Check if snapshot is due.
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
				IndexTicker:  idx.Symbol,
				SnapshotDate: eventDate,
				Constituents: constituents,
			},
			ObservationDate:  obsTemplate.ObservationDate,
			SubscriptionID:   obsTemplate.SubscriptionID,
			SubscriptionName: obsTemplate.SubscriptionName,
		}

		numObs++

		logger.Info().
			Int("NumConstituents", len(constituents)).
			Str("IndexTicker", idx.Symbol).
			Time("SnapshotDate", eventDate).
			Msg("emitted index snapshot")
	}

	// Update in-memory state (for potential future use within the same run).
	maps.Copy(state, added)

	for t := range removed {
		delete(state, t)
	}

	return numObs, nil
}
