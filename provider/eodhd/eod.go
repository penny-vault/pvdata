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
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
)

// -- URL templates --

const (
	bulkLastDayURLTemplate        = "https://eodhd.com/api/eod-bulk-last-day/%s"
	perTickerEODURLTemplate       = "https://eodhd.com/api/eod/%s.%s"
	perTickerSplitsURLTemplate    = "https://eodhd.com/api/splits/%s.%s"
	perTickerDividendsURLTemplate = "https://eodhd.com/api/div/%s.%s"
)

// -- Mode selection --

type eodFetchMode int

const (
	modeBulk eodFetchMode = iota
	modePerTicker
)

func eodMode(tickerFilter, figiFilter string, lookback time.Duration) eodFetchMode {
	if tickerFilter != "" || figiFilter != "" || lookback > 30*24*time.Hour {
		return modePerTicker
	}

	return modeBulk
}

// -- Response types and parsers --

// splitKey is the (ticker, date) tuple used to merge bulk-fetched
// splits and dividends back into the EOD rows for a single date.
type splitKey struct {
	Ticker string
	Date   string
}

type bulkEODRow struct {
	Code   string  `json:"code"`
	Date   string  `json:"date"`
	Open   float64 `json:"open"`
	High   float64 `json:"high"`
	Low    float64 `json:"low"`
	Close  float64 `json:"close"`
	Volume float64 `json:"volume"`
}

// bulkEODRecord is a parsed bulk-EOD row with the ticker normalized
// to pv-data convention.
type bulkEODRecord struct {
	Ticker string
	Date   string
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume float64
}

func parseBulkEOD(body []byte) ([]bulkEODRecord, error) {
	var rows []bulkEODRow

	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("could not unmarshal bulk-EOD response: %w", err)
	}

	out := make([]bulkEODRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, bulkEODRecord{
			Ticker: normalizeTicker(r.Code),
			Date:   r.Date,
			Open:   r.Open,
			High:   r.High,
			Low:    r.Low,
			Close:  r.Close,
			Volume: r.Volume,
		})
	}

	return out, nil
}

type bulkSplitRow struct {
	Code  string `json:"code"`
	Date  string `json:"date"`
	Split string `json:"split"`
}

func parseBulkSplits(body []byte) (map[splitKey]float64, error) {
	var rows []bulkSplitRow

	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("could not unmarshal bulk-splits response: %w", err)
	}

	out := make(map[splitKey]float64, len(rows))
	for _, r := range rows {
		factor := parseSplitFactor(r.Split)
		if factor == 1.0 {
			continue
		}

		out[splitKey{Ticker: normalizeTicker(r.Code), Date: r.Date}] = factor
	}

	return out, nil
}

type bulkDividendRow struct {
	Code  string  `json:"code"`
	Date  string  `json:"date"`
	Value float64 `json:"value"`
}

func parseBulkDividends(body []byte) (map[splitKey]float64, error) {
	var rows []bulkDividendRow

	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("could not unmarshal bulk-dividends response: %w", err)
	}

	out := make(map[splitKey]float64, len(rows))
	for _, r := range rows {
		if r.Value == 0 {
			continue
		}

		out[splitKey{Ticker: normalizeTicker(r.Code), Date: r.Date}] = r.Value
	}

	return out, nil
}

type perTickerEODRow struct {
	Date          string  `json:"date"`
	Open          float64 `json:"open"`
	High          float64 `json:"high"`
	Low           float64 `json:"low"`
	Close         float64 `json:"close"`
	AdjustedClose float64 `json:"adjusted_close"`
	Volume        float64 `json:"volume"`
}

func parsePerTickerEOD(body []byte) ([]perTickerEODRow, error) {
	var rows []perTickerEODRow

	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("could not unmarshal per-ticker EOD response: %w", err)
	}

	return rows, nil
}

type perTickerSplitRow struct {
	Date  string `json:"date"`
	Split string `json:"split"`
}

func parsePerTickerSplits(body []byte) (map[string]float64, error) {
	var rows []perTickerSplitRow

	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("could not unmarshal per-ticker splits response: %w", err)
	}

	out := make(map[string]float64, len(rows))
	for _, r := range rows {
		factor := parseSplitFactor(r.Split)
		if factor == 1.0 {
			continue
		}

		out[r.Date] = factor
	}

	return out, nil
}

type perTickerDividendRow struct {
	Date  string  `json:"date"`
	Value float64 `json:"value"`
}

func parsePerTickerDividends(body []byte) (map[string]float64, error) {
	var rows []perTickerDividendRow

	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("could not unmarshal per-ticker dividends response: %w", err)
	}

	out := make(map[string]float64, len(rows))
	for _, r := range rows {
		if r.Value == 0 {
			continue
		}

		out[r.Date] = r.Value
	}

	return out, nil
}

// parseSplitFactor parses an EODHD split string of the form
// "numerator/denominator" (e.g. "2/1", "3/2", "1/4") into the
// equivalent multiplicative factor. An empty or unparseable input
// returns 1.0 so callers can treat "no split" uniformly.
func parseSplitFactor(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 1.0
	}

	parts := strings.SplitN(s, "/", 2)
	if len(parts) != 2 {
		return 1.0
	}

	num, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 1.0
	}

	den, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil || den == 0 {
		return 1.0
	}

	return num / den
}

// buildEodEvent converts a YYYY-MM-DD date string into a NYSE-close
// timestamp (16:00 America/New_York), matching the pv-data convention
// used by the tiingo provider.
func buildEodEvent(dateStr string) (time.Time, error) {
	d, err := time.Parse("2006-01-02", dateStr)
	if err != nil {
		return time.Time{}, fmt.Errorf("could not parse date %q: %w", dateStr, err)
	}

	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.Time{}, fmt.Errorf("could not load NYC timezone: %w", err)
	}

	return time.Date(d.Year(), d.Month(), d.Day(), 16, 0, 0, 0, nyc), nil
}

// buildEodRecord assembles a *data.Eod from a parsed per-ticker row,
// optional dividend cash, and optional split factor. A split factor of
// 0 is treated as "no split" and rendered as 1.0.
func buildEodRecord(ticker, compositeFigi string, row perTickerEODRow, dividend, split float64) (*data.Eod, error) {
	ts, err := buildEodEvent(row.Date)
	if err != nil {
		return nil, err
	}

	if split == 0 {
		split = 1.0
	}

	return &data.Eod{
		Date:          ts,
		Ticker:        ticker,
		CompositeFigi: compositeFigi,
		Open:          row.Open,
		High:          row.High,
		Low:           row.Low,
		Close:         row.Close,
		Volume:        row.Volume,
		Dividend:      dividend,
		Split:         split,
	}, nil
}

// -- Loader entrypoint --

func downloadEodhdEOD(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
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
	lookback := provider.LookbackFromContext(ctx, 7*24*time.Hour)

	exchanges := parseExchanges(subscription.Config["exchanges"])
	assetTypeFilter := parseAssetTypeFilter(subscription.Config["assetTypes"])
	rateLimit := readRateLimit(subscription.Config["rateLimit"])
	limiter := rate.NewLimiter(rate.Limit(float64(rateLimit)/61.0), 1)
	client := newClient(subscription.Config["apiKey"])

	conn, err := subscription.Library.AcquireWithTimeout(ctx)
	if err != nil {
		log.Panic().Msg("could not acquire database connection")
	}
	defer conn.Release()

	dbAssets, err := data.ActiveAssets(ctx, conn, subscription.DataTablesMap[data.AssetKey])
	if err != nil {
		logger.Error().Err(err).Msg("could not load active assets")

		runSummary.Status = data.RunFailed

		return
	}

	tickerToFigi := make(map[string]string, len(dbAssets))
	for _, a := range dbAssets {
		if len(assetTypeFilter) > 0 {
			if _, ok := assetTypeFilter[a.AssetType]; !ok {
				continue
			}
		}

		tickerToFigi[a.Ticker] = a.CompositeFigi
	}

	mode := eodMode(tickerFilter, figiFilter, lookback)

	logger.Info().
		Str("mode", eodModeName(mode)).
		Int("scope_assets", len(tickerToFigi)).
		Strs("exchanges", exchanges).
		Dur("lookback", lookback).
		Msg("eodhd EOD loader starting")

	switch mode {
	case modeBulk:
		n, err := runBulkEOD(ctx, client, limiter, exchanges, tickerToFigi, lookback, subscription, out)
		if errors.Is(err, errDailyRateLimit) {
			runSummary.Status = data.RunFailed
		}

		numObs += n
	case modePerTicker:
		n, err := runPerTickerEOD(ctx, client, limiter, dbAssets, exchanges, assetTypeFilter, tickerFilter, figiFilter, lookback, subscription, out)
		if errors.Is(err, errDailyRateLimit) {
			runSummary.Status = data.RunFailed
		}

		numObs += n
	}
}

func eodModeName(m eodFetchMode) string {
	if m == modeBulk {
		return "bulk"
	}

	return "per-ticker"
}

// -- Bulk fetch --

func runBulkEOD(ctx context.Context, client *resty.Client, limiter *rate.Limiter, exchanges []string, tickerToFigi map[string]string, lookback time.Duration, subscription *library.Subscription, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)
	end := time.Now().UTC().Truncate(24 * time.Hour)
	start := end.Add(-lookback).Truncate(24 * time.Hour)

	numObs := 0

	for _, exchange := range exchanges {
		for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
			dateStr := d.Format("2006-01-02")

			records, splits, divs, err := fetchBulkAllForDate(ctx, client, limiter, exchange, dateStr)
			if err != nil {
				if errors.Is(err, errDailyRateLimit) {
					return numObs, err
				}

				logger.Warn().Err(err).Str("Exchange", exchange).Str("Date", dateStr).Msg("bulk EOD fetch failed for date, skipping")

				continue
			}

			for _, r := range records {
				figiVal, ok := tickerToFigi[r.Ticker]
				if !ok {
					continue
				}

				dividend := divs[splitKey{Ticker: r.Ticker, Date: r.Date}]
				split := splits[splitKey{Ticker: r.Ticker, Date: r.Date}]

				eod, err := buildEodRecord(r.Ticker, figiVal, perTickerEODRow{
					Date: r.Date, Open: r.Open, High: r.High, Low: r.Low, Close: r.Close, Volume: r.Volume,
				}, dividend, split)
				if err != nil {
					logger.Warn().Err(err).Str("Ticker", r.Ticker).Str("Date", r.Date).Msg("could not build EOD record")
					continue
				}

				select {
				case <-ctx.Done():
					return numObs, ctx.Err()
				case out <- &data.Observation{
					EodQuote:         eod,
					ObservationDate:  time.Now(),
					SubscriptionID:   subscription.ID,
					SubscriptionName: subscription.Name,
				}:
					numObs++
				}
			}
		}
	}

	return numObs, nil
}

// fetchBulkAllForDate fetches eod, splits, and dividends for one
// (exchange, date) pair as three sequential calls. Splits/dividends
// failures are tolerated (returned as empty maps) since they are
// frequently empty for any given date.
func fetchBulkAllForDate(ctx context.Context, client *resty.Client, limiter *rate.Limiter, exchange, dateStr string) ([]bulkEODRecord, map[splitKey]float64, map[splitKey]float64, error) {
	logger := zerolog.Ctx(ctx)

	if err := limiter.Wait(ctx); err != nil {
		return nil, nil, nil, err
	}

	url := fmt.Sprintf(bulkLastDayURLTemplate, exchange)

	resp, err := doWithRateLimit(ctx, func() (*resty.Response, error) {
		return client.R().SetContext(ctx).SetQueryParam("date", dateStr).Get(url)
	})
	if err != nil {
		return nil, nil, nil, err
	}

	if resp.StatusCode() >= 300 {
		return nil, nil, nil, fmt.Errorf("bulk EOD HTTP %d for %s/%s", resp.StatusCode(), exchange, dateStr)
	}

	records, err := parseBulkEOD(resp.Body())
	if err != nil {
		return nil, nil, nil, err
	}

	splits, err := fetchBulkExtra(ctx, client, limiter, exchange, dateStr, "splits", parseBulkSplits)
	if err != nil {
		if errors.Is(err, errDailyRateLimit) {
			return records, nil, nil, err
		}

		logger.Warn().Err(err).Str("Exchange", exchange).Str("Date", dateStr).Msg("could not fetch bulk splits, continuing without")

		splits = map[splitKey]float64{}
	}

	divs, err := fetchBulkExtra(ctx, client, limiter, exchange, dateStr, "dividends", parseBulkDividends)
	if err != nil {
		if errors.Is(err, errDailyRateLimit) {
			return records, splits, nil, err
		}

		logger.Warn().Err(err).Str("Exchange", exchange).Str("Date", dateStr).Msg("could not fetch bulk dividends, continuing without")

		divs = map[splitKey]float64{}
	}

	return records, splits, divs, nil
}

func fetchBulkExtra(ctx context.Context, client *resty.Client, limiter *rate.Limiter, exchange, dateStr, kind string, parse func([]byte) (map[splitKey]float64, error)) (map[splitKey]float64, error) {
	if err := limiter.Wait(ctx); err != nil {
		return nil, err
	}

	url := fmt.Sprintf(bulkLastDayURLTemplate, exchange)

	resp, err := doWithRateLimit(ctx, func() (*resty.Response, error) {
		return client.R().SetContext(ctx).
			SetQueryParam("date", dateStr).
			SetQueryParam("type", kind).
			Get(url)
	})
	if err != nil {
		return nil, err
	}

	if resp.StatusCode() >= 300 {
		return nil, fmt.Errorf("bulk %s HTTP %d", kind, resp.StatusCode())
	}

	return parse(resp.Body())
}

// -- Per-ticker fetch --

const defaultEODWorkers = 10

func runPerTickerEOD(ctx context.Context, client *resty.Client, limiter *rate.Limiter, dbAssets []*data.Asset, exchanges []string, assetTypeFilter map[data.AssetType]struct{}, tickerFilter, figiFilter string, lookback time.Duration, subscription *library.Subscription, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)

	primaryExchange := exchanges[0]

	assets := make([]*data.Asset, 0, len(dbAssets))
	for _, a := range dbAssets {
		if len(assetTypeFilter) > 0 {
			if _, ok := assetTypeFilter[a.AssetType]; !ok {
				continue
			}
		}

		if tickerFilter != "" && !strings.EqualFold(a.Ticker, tickerFilter) {
			continue
		}

		if figiFilter != "" && a.CompositeFigi != figiFilter {
			continue
		}

		assets = append(assets, a)
	}

	if len(assets) == 0 {
		logger.Warn().Msg("no assets in scope for per-ticker EOD fetch")
		return 0, nil
	}

	workerCount := defaultEODWorkers
	if w, err := strconv.Atoi(subscription.Config["workers"]); err == nil && w > 0 {
		workerCount = w
	}

	if workerCount > len(assets) {
		workerCount = len(assets)
	}

	logger.Info().Int("workers", workerCount).Int("assets", len(assets)).Msg("per-ticker EOD worker pool starting")

	startStr := time.Now().Add(-lookback).Format("2006-01-02")

	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()

	jobCh := make(chan *data.Asset)

	var (
		wg             sync.WaitGroup
		numObs         atomic.Int64
		dailyLimitFlag atomic.Bool
	)

	for range workerCount {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for asset := range jobCh {
				if err := limiter.Wait(workerCtx); err != nil {
					return
				}

				eodRows, splits, divs, err := fetchPerTickerAll(workerCtx, client, limiter, asset.Ticker, primaryExchange, startStr)
				if errors.Is(err, errDailyRateLimit) {
					dailyLimitFlag.Store(true)
					cancelWorkers()

					return
				}

				if err != nil {
					if errors.Is(err, context.Canceled) {
						return
					}

					logger.Warn().Err(err).Str("Ticker", asset.Ticker).Msg("per-ticker EOD fetch failed, skipping")

					continue
				}

				for _, row := range eodRows {
					eod, err := buildEodRecord(asset.Ticker, asset.CompositeFigi, row, divs[row.Date], splits[row.Date])
					if err != nil {
						logger.Warn().Err(err).Str("Ticker", asset.Ticker).Str("Date", row.Date).Msg("could not build EOD record")
						continue
					}

					select {
					case <-workerCtx.Done():
						return
					case out <- &data.Observation{
						EodQuote:         eod,
						ObservationDate:  time.Now(),
						SubscriptionID:   subscription.ID,
						SubscriptionName: subscription.Name,
					}:
						numObs.Add(1)
					}
				}
			}
		}()
	}

feed:
	for _, asset := range assets {
		select {
		case <-workerCtx.Done():
			break feed
		case jobCh <- asset:
		}
	}

	close(jobCh)
	wg.Wait()

	if dailyLimitFlag.Load() {
		return int(numObs.Load()), errDailyRateLimit
	}

	return int(numObs.Load()), nil
}

func fetchPerTickerAll(ctx context.Context, client *resty.Client, limiter *rate.Limiter, ticker, exchange, startStr string) ([]perTickerEODRow, map[string]float64, map[string]float64, error) {
	eodTicker := denormalizeTicker(ticker)

	rows, err := fetchPerTickerEOD(ctx, client, eodTicker, exchange, startStr)
	if err != nil {
		return nil, nil, nil, err
	}

	splits, err := fetchPerTickerExtra(ctx, client, limiter, eodTicker, exchange, perTickerSplitsURLTemplate, parsePerTickerSplits)
	if err != nil {
		if errors.Is(err, errDailyRateLimit) {
			return rows, nil, nil, err
		}

		splits = map[string]float64{}
	}

	divs, err := fetchPerTickerExtra(ctx, client, limiter, eodTicker, exchange, perTickerDividendsURLTemplate, parsePerTickerDividends)
	if err != nil {
		if errors.Is(err, errDailyRateLimit) {
			return rows, splits, nil, err
		}

		divs = map[string]float64{}
	}

	return rows, splits, divs, nil
}

func fetchPerTickerEOD(ctx context.Context, client *resty.Client, ticker, exchange, startStr string) ([]perTickerEODRow, error) {
	url := fmt.Sprintf(perTickerEODURLTemplate, ticker, exchange)

	resp, err := doWithRateLimit(ctx, func() (*resty.Response, error) {
		return client.R().SetContext(ctx).SetQueryParam("from", startStr).Get(url)
	})
	if err != nil {
		return nil, err
	}

	if resp.StatusCode() >= 300 {
		return nil, fmt.Errorf("per-ticker EOD HTTP %d for %s.%s", resp.StatusCode(), ticker, exchange)
	}

	return parsePerTickerEOD(resp.Body())
}

func fetchPerTickerExtra(ctx context.Context, client *resty.Client, limiter *rate.Limiter, ticker, exchange, urlTemplate string, parse func([]byte) (map[string]float64, error)) (map[string]float64, error) {
	if err := limiter.Wait(ctx); err != nil {
		return nil, err
	}

	url := fmt.Sprintf(urlTemplate, ticker, exchange)

	resp, err := doWithRateLimit(ctx, func() (*resty.Response, error) {
		return client.R().SetContext(ctx).Get(url)
	})
	if err != nil {
		return nil, err
	}

	if resp.StatusCode() >= 300 {
		return nil, fmt.Errorf("HTTP %d for %s", resp.StatusCode(), url)
	}

	return parse(resp.Body())
}

// denormalizeTicker reverses normalizeTicker for outbound EODHD URLs
// (BRK/A → BRK-A).
func denormalizeTicker(ticker string) string {
	return strings.ReplaceAll(ticker, "/", "-")
}
