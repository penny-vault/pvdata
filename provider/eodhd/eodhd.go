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
package eodhd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/figi"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
)

func init() {
	provider.Register("eodhd", &EODHD{})
}

type EODHD struct{}

func (e *EODHD) Name() string {
	return "eodhd"
}

func (e *EODHD) ConfigDescription() map[string]string {
	return map[string]string{
		"apiKey":          "Enter your EODHD API token:",
		"rateLimit":       "Maximum requests per minute (default 1000).",
		"exchanges":       "Comma-separated EODHD exchange codes for asset/EOD scope (default 'US').",
		"assetTypes":      "Comma-separated pv-data asset types to include (CS, ETF, MF, ADRC, ...). Leave blank for all.",
		"includeDelisted": "Set to 'true' to also fetch delisted tickers.",
		"workers":         "Per-ticker concurrency for EOD/intraday workers (default 10).",
	}
}

func (e *EODHD) Description() string {
	return `EODHD provides asset descriptions, end-of-day OHLCV, and 1-minute intraday bars from eodhd.com. Subscribe to "Stock Tickers", "EOD", or "Intraday 1m".`
}

func (e *EODHD) Datasets() map[string]provider.Dataset {
	return map[string]provider.Dataset{
		"Stock Tickers": {
			Name:        "Stock Tickers",
			Description: "Asset catalog (ticker, name, exchange, type, ISIN) with FIGI enrichment.",
			DataTypes:   []*data.DataType{data.DataTypes[data.AssetKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: downloadEodhdAssets,
		},
		"EOD": {
			Name:        "EOD",
			Description: "End-of-day OHLCV with splits and dividends.",
			DataTypes:   []*data.DataType{data.DataTypes[data.EODKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(1972, 6, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			PostFetch: []provider.PostFetchHook{provider.AdjustEodPrices},
			Fetch:     downloadEodhdEOD,
		},
		"Intraday 1m": {
			Name:        "Intraday 1m",
			Description: "1-minute OHLCV bars for the active asset universe (excluding mutual funds).",
			DataTypes:   []*data.DataType{data.DataTypes[data.IntradayKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: downloadEodhdIntraday,
		},
	}
}

// -- Symbol normalization --

// normalizeTicker converts an EODHD ticker code to the pv-data convention.
// EODHD uses dashes for share classes (BRK-A); pv-data uses slashes (BRK/A).
func normalizeTicker(code string) string {
	return strings.ReplaceAll(code, "-", "/")
}

// -- Type and exchange mapping --

// mapAssetType translates an EODHD `Type` field to pv-data's AssetType. EODHD
// reports Preferred Stock as a distinct type; we fold those into CommonStock
// because pv-data does not have a separate preferred-share type and the
// downstream pricing/fundamentals semantics are close enough for now.
func mapAssetType(eodhdType string) data.AssetType {
	switch eodhdType {
	case "Common Stock", "Preferred Stock":
		return data.CommonStock
	case "ETF":
		return data.ETF
	case "Fund", "Mutual Fund":
		return data.MutualFund
	default:
		return data.UnknownAsset
	}
}

var exchangeMap = map[string]data.Exchange{
	"NASDAQ":    data.NasdaqExchange,
	"NYSE":      data.NYSEExchange,
	"BATS":      data.BATSExchange,
	"NYSE MKT":  data.NYSEMktExchange,
	"NYSE ARCA": data.ARCAExchange,
	"NMFQS":     data.NMFQSExchange,
	"OTC":       data.OTCExchange,
}

func mapExchange(eodhdExchange string) data.Exchange {
	if v, ok := exchangeMap[eodhdExchange]; ok {
		return v
	}

	return data.UnknownExchange
}

// -- Symbol-list response parsing --

type eodhdSymbol struct {
	Code     string `json:"Code"`
	Name     string `json:"Name"`
	Country  string `json:"Country"`
	Exchange string `json:"Exchange"`
	Currency string `json:"Currency"`
	Type     string `json:"Type"`
	Isin     string `json:"Isin"`
}

// parseSymbolList decodes the EODHD exchange-symbol-list payload into a
// slice of pv-data assets. Rows whose Type does not map to a known
// pv-data AssetType are dropped (they include warrants, rights, units,
// etc. which the rest of the pipeline can't price). When delisted is
// true the rows are marked Active=false; the DelistingDate is left
// empty because EODHD does not return a delisting date in this
// endpoint and a synthetic "now" timestamp would be misleading.
func parseSymbolList(body []byte, delisted bool) ([]*data.Asset, error) {
	var rows []eodhdSymbol

	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("could not unmarshal exchange-symbol-list: %w", err)
	}

	now := time.Now()

	out := make([]*data.Asset, 0, len(rows))

	for _, row := range rows {
		assetType := mapAssetType(row.Type)
		if assetType == data.UnknownAsset {
			continue
		}

		asset := &data.Asset{
			Ticker:          normalizeTicker(row.Code),
			Name:            row.Name,
			AssetType:       assetType,
			PrimaryExchange: mapExchange(row.Exchange),
			LastUpdated:     now,
			Active:          !delisted,
		}

		if row.Isin != "" {
			asset.ISIN = []string{row.Isin}
		}

		out = append(out, asset)
	}

	return out, nil
}

// -- Asset loader --

const (
	exchangeSymbolListURLTemplate = "https://eodhd.com/api/exchange-symbol-list/%s"
)

func downloadEodhdAssets(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
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

		if runSummary.Status != data.RunFailed {
			runSummary.Status = data.RunSuccess
		}

		exitNotification <- runSummary
	}()

	tickerFilter, figiFilter := provider.SecurityFilterFromContext(ctx)
	if tickerFilter != "" || figiFilter != "" {
		logger.Info().Str("provider", "eodhd").Str("dataset", "Stock Tickers").Msg("ticker/FIGI filtering not applicable to asset catalog downloads, skipping")
		return
	}

	exchanges := parseExchanges(subscription.Config["exchanges"])
	includeDelisted := strings.EqualFold(subscription.Config["includeDelisted"], "true")
	assetTypeFilter := parseAssetTypeFilter(subscription.Config["assetTypes"])

	if len(assetTypeFilter) > 0 {
		logger.Info().Strs("asset_types", assetTypeFilterKeys(assetTypeFilter)).Msg("applying asset type filter")
	}

	rateLimit := readRateLimit(subscription.Config["rateLimit"])
	limiter := rate.NewLimiter(rate.Limit(float64(rateLimit)/61.0), 1)
	client := newClient(subscription.Config["apiKey"])

	allAssets := make([]*data.Asset, 0, 64000)

	for _, exchange := range exchanges {
		assets, err := fetchSymbolList(ctx, client, limiter, exchange, false)
		if err != nil {
			if errors.Is(err, errDailyRateLimit) {
				runSummary.Status = data.RunFailed
				return
			}

			logger.Error().Err(err).Str("Exchange", exchange).Msg("could not fetch active symbols")

			continue
		}

		allAssets = append(allAssets, assets...)

		if includeDelisted {
			delistedAssets, err := fetchSymbolList(ctx, client, limiter, exchange, true)
			if err != nil {
				if errors.Is(err, errDailyRateLimit) {
					runSummary.Status = data.RunFailed
					return
				}

				logger.Error().Err(err).Str("Exchange", exchange).Msg("could not fetch delisted symbols")

				continue
			}

			allAssets = append(allAssets, delistedAssets...)
		}
	}

	if len(assetTypeFilter) > 0 {
		filtered := make([]*data.Asset, 0, len(allAssets))

		for _, a := range allAssets {
			if _, ok := assetTypeFilter[a.AssetType]; ok {
				filtered = append(filtered, a)
			}
		}

		logger.Info().Int("before", len(allAssets)).Int("after", len(filtered)).Msg("applied asset type filter")

		allAssets = filtered
	}

	logger.Info().Int("count", len(allAssets)).Msg("resolving FIGIs via OpenFIGI")
	figi.Enrich(allAssets...)

	for _, a := range allAssets {
		if a.CompositeFigi == "" {
			a.CompositeFigi = figi.GenerateSyntheticFIGI(a.Ticker, a.Name)
			logger.Info().Str("ticker", a.Ticker).Str("name", a.Name).Str("figi", a.CompositeFigi).Msg("generated synthetic FIGI")
		}
	}

	// Reconcile: anything in DB that no longer appears in the EODHD
	// universe (and matches the asset-type filter) gets marked delisted.
	conn, err := subscription.Library.AcquireWithTimeout(ctx)
	if err != nil {
		log.Panic().Msg("could not acquire database connection")
	}
	defer conn.Release()

	dbAssets, err := data.ActiveAssets(ctx, conn, subscription.DataTablesMap[data.AssetKey])
	if err != nil {
		logger.Error().Err(err).Msg("could not load active assets from database")

		runSummary.Status = data.RunFailed

		return
	}

	figiSeen := make(map[string]struct{}, len(allAssets))
	for _, a := range allAssets {
		figiSeen[a.CompositeFigi] = struct{}{}
	}

	for _, dbAsset := range dbAssets {
		if len(assetTypeFilter) > 0 {
			if _, ok := assetTypeFilter[dbAsset.AssetType]; !ok {
				continue
			}
		}

		if _, ok := figiSeen[dbAsset.CompositeFigi]; ok {
			continue
		}

		// We don't know when the asset actually delisted; EODHD's
		// symbol list does not carry that field. Leaving DelistingDate
		// empty is honest. Active=false + last_updated together signal
		// "noticed missing on this run".
		dbAsset.Active = false
		dbAsset.DelistingDate = ""
		allAssets = append(allAssets, dbAsset)
	}

	for _, a := range allAssets {
		if a.CompositeFigi == "" {
			continue
		}

		out <- &data.Observation{
			AssetObject:      a,
			ObservationDate:  time.Now(),
			SubscriptionID:   subscription.ID,
			SubscriptionName: subscription.Name,
		}

		numObs++
	}
}

// -- HTTP helpers --

func newClient(apiKey string) *resty.Client {
	return resty.New().
		SetQueryParam("api_token", apiKey).
		SetQueryParam("fmt", "json").
		SetRetryCount(3).
		SetRetryWaitTime(5 * time.Second).
		SetRetryMaxWaitTime(30 * time.Second).
		AddRetryCondition(func(r *resty.Response, err error) bool {
			return err != nil || r.StatusCode() >= 500
		}).
		SetTimeout(60 * time.Second)
}

func fetchSymbolList(ctx context.Context, client *resty.Client, limiter *rate.Limiter, exchange string, delisted bool) ([]*data.Asset, error) {
	if err := limiter.Wait(ctx); err != nil {
		return nil, err
	}

	url := fmt.Sprintf(exchangeSymbolListURLTemplate, exchange)

	req := client.R().SetContext(ctx)
	if delisted {
		req = req.SetQueryParam("delisted", "1")
	}

	resp, err := doWithRateLimit(ctx, func() (*resty.Response, error) {
		return req.Get(url)
	})
	if err != nil {
		return nil, err
	}

	if resp.StatusCode() >= 300 {
		return nil, fmt.Errorf("unexpected HTTP status %d for %s", resp.StatusCode(), url)
	}

	return parseSymbolList(resp.Body(), delisted)
}

// -- Config parsing --

func parseExchanges(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return []string{"US"}
	}

	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))

	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		out = append(out, p)
	}

	if len(out) == 0 {
		return []string{"US"}
	}

	return out
}

func parseAssetTypeFilter(raw string) map[data.AssetType]struct{} {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	out := make(map[data.AssetType]struct{})

	for part := range strings.SplitSeq(raw, ",") {
		code := strings.ToUpper(strings.TrimSpace(part))
		if code == "" {
			continue
		}

		out[data.AssetType(code)] = struct{}{}
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

func assetTypeFilterKeys(m map[data.AssetType]struct{}) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, string(k))
	}

	return keys
}

func readRateLimit(raw string) int {
	if v, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && v > 0 {
		return v
	}

	return 1000
}
