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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
)

const (
	intradayURLTemplate = "https://eodhd.com/api/intraday/%s.%s"

	// intradayMaxRange is the largest from-to window EODHD accepts in a
	// single 1-minute intraday request.
	intradayMaxRange = 120 * 24 * time.Hour

	// intradayBackfillThreshold is the lookback at which we switch from
	// "currently active" universe to "active anywhere in the window."
	// Matches the EOD loader's mode threshold.
	intradayBackfillThreshold = 30 * 24 * time.Hour
)

// -- Response parsing --

type intradayRow struct {
	Timestamp int64   `json:"timestamp"`
	Open      float64 `json:"open"`
	High      float64 `json:"high"`
	Low       float64 `json:"low"`
	Close     float64 `json:"close"`
	Volume    float64 `json:"volume"`
}

func parseIntradayResponse(body []byte, ticker, compositeFigi string) ([]*data.IntradayBar, error) {
	var rows []intradayRow

	if err := json.Unmarshal(body, &rows); err != nil {
		return nil, fmt.Errorf("could not unmarshal intraday response: %w", err)
	}

	out := make([]*data.IntradayBar, 0, len(rows))

	for _, r := range rows {
		if r.Timestamp <= 0 {
			continue
		}

		out = append(out, &data.IntradayBar{
			Date:          time.Unix(r.Timestamp, 0).UTC(),
			Ticker:        ticker,
			CompositeFigi: compositeFigi,
			Open:          r.Open,
			High:          r.High,
			Low:           r.Low,
			Close:         r.Close,
			Volume:        r.Volume,
		})
	}

	return out, nil
}

// -- Range chunking --

type intradayChunk struct {
	From time.Time
	To   time.Time
}

func chunkRange(from, to time.Time, maxWindow time.Duration) []intradayChunk {
	if !from.Before(to) {
		return nil
	}

	var chunks []intradayChunk

	for cursor := from; cursor.Before(to); {
		end := cursor.Add(maxWindow)
		if end.After(to) {
			end = to
		}

		chunks = append(chunks, intradayChunk{From: cursor, To: end})

		cursor = end
	}

	return chunks
}

// -- Universe selection --

// assetsActiveInWindow returns the subset of assets that were active
// at some point on or after windowStart. Currently-active assets are
// always included; assets marked inactive are included only when
// their DelistingDate parses successfully and lands on or after
// windowStart.
func assetsActiveInWindow(assets []*data.Asset, windowStart time.Time) []*data.Asset {
	out := make([]*data.Asset, 0, len(assets))

	for _, a := range assets {
		if a.Active {
			out = append(out, a)
			continue
		}

		if a.DelistingDate == "" {
			continue
		}

		delisted, err := time.Parse(time.RFC3339, a.DelistingDate)
		if err != nil {
			continue
		}

		if !delisted.Before(windowStart) {
			out = append(out, a)
		}
	}

	return out
}

// -- Loader entrypoint --

func downloadEodhdIntraday(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
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

	exchanges := parseExchanges(subscription.Config["exchanges"])
	defaultExchange := exchanges[0]

	lookback := provider.LookbackFromContext(ctx, 5*24*time.Hour)
	to := time.Now().UTC()
	from := to.Add(-lookback)

	tickerFilter, figiFilter := provider.SecurityFilterFromContext(ctx)
	assetTypeFilter := parseAssetTypeFilter(subscription.Config["assetTypes"])

	rateLimit := readRateLimit(subscription.Config["rateLimit"])
	limiter := rate.NewLimiter(rate.Limit(float64(rateLimit)/61.0), 1)
	client := newClient(subscription.Config["apiKey"])

	conn, err := subscription.Library.AcquireWithTimeout(ctx)
	if err != nil {
		log.Panic().Msg("could not acquire database connection")
	}
	defer conn.Release()

	assets, err := loadIntradayUniverse(ctx, conn, subscription.DataTablesMap[data.AssetKey], from, lookback)
	if err != nil {
		logger.Error().Err(err).Msg("could not load asset universe")

		runSummary.Status = data.RunFailed

		return
	}

	jobs := buildIntradayJobs(assets, defaultExchange, assetTypeFilter, tickerFilter, figiFilter)

	if len(jobs) == 0 {
		logger.Warn().Msg("no assets in scope for intraday fetch")
		return
	}

	workerCount := defaultEODWorkers
	if w, err := strconv.Atoi(subscription.Config["workers"]); err == nil && w > 0 {
		workerCount = w
	}

	if workerCount > len(jobs) {
		workerCount = len(jobs)
	}

	logger.Info().
		Int("workers", workerCount).
		Int("tickers", len(jobs)).
		Dur("lookback", lookback).
		Bool("backfill", lookback > intradayBackfillThreshold).
		Msg("intraday worker pool starting")

	chunks := chunkRange(from, to, intradayMaxRange)

	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()

	jobCh := make(chan intradayJob)

	var (
		wg             sync.WaitGroup
		numObsAtomic   atomic.Int64
		dailyLimitFlag atomic.Bool
	)

	for range workerCount {
		wg.Add(1)

		go func() {
			defer wg.Done()

			for j := range jobCh {
				bars, err := fetchIntradayChunks(workerCtx, client, limiter, j, chunks)
				if errors.Is(err, errDailyRateLimit) {
					dailyLimitFlag.Store(true)
					cancelWorkers()

					return
				}

				if err != nil {
					if errors.Is(err, context.Canceled) {
						return
					}

					logger.Warn().Err(err).Str("Ticker", j.Ticker).Msg("intraday fetch failed, skipping")

					continue
				}

				for _, bar := range bars {
					select {
					case <-workerCtx.Done():
						return
					case out <- &data.Observation{
						IntradayBar:      bar,
						ObservationDate:  time.Now(),
						SubscriptionID:   subscription.ID,
						SubscriptionName: subscription.Name,
					}:
						numObsAtomic.Add(1)
					}
				}
			}
		}()
	}

feed:
	for _, j := range jobs {
		select {
		case <-workerCtx.Done():
			break feed
		case jobCh <- j:
		}
	}

	close(jobCh)
	wg.Wait()

	numObs = int(numObsAtomic.Load())

	if dailyLimitFlag.Load() {
		runSummary.Status = data.RunFailed
	}
}

// loadIntradayUniverse returns the asset universe for an intraday run.
// For short lookbacks it returns the currently-active set; for
// backfills (longer than intradayBackfillThreshold) it includes any
// asset whose DelistingDate is on or after the start of the window so
// historical bars for since-delisted tickers are still pulled.
func loadIntradayUniverse(ctx context.Context, conn *pgxpool.Conn, assetTable string, from time.Time, lookback time.Duration) ([]*data.Asset, error) {
	if lookback > intradayBackfillThreshold {
		all, err := data.AllAssets(ctx, conn, assetTable)
		if err != nil {
			return nil, err
		}

		return assetsActiveInWindow(all, from), nil
	}

	return data.ActiveAssets(ctx, conn, assetTable)
}

// intradayJob is a single ticker's fetch envelope.
type intradayJob struct {
	Ticker        string
	CompositeFigi string
	Exchange      string
}

func buildIntradayJobs(assets []*data.Asset, defaultExchange string, assetTypeFilter map[data.AssetType]struct{}, tickerFilter, figiFilter string) []intradayJob {
	jobs := make([]intradayJob, 0, len(assets))

	for _, a := range assets {
		if a.CompositeFigi == "" {
			continue
		}

		// Mutual funds price once per day at NAV; they have no
		// intraday data on EODHD and would just burn quota.
		if a.AssetType == data.MutualFund {
			continue
		}

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

		jobs = append(jobs, intradayJob{
			Ticker:        a.Ticker,
			CompositeFigi: a.CompositeFigi,
			Exchange:      defaultExchange,
		})
	}

	return jobs
}

func fetchIntradayChunks(ctx context.Context, client *resty.Client, limiter *rate.Limiter, job intradayJob, chunks []intradayChunk) ([]*data.IntradayBar, error) {
	logger := zerolog.Ctx(ctx)
	eodhdTicker := denormalizeTicker(job.Ticker)
	url := fmt.Sprintf(intradayURLTemplate, eodhdTicker, job.Exchange)

	var bars []*data.IntradayBar

	for _, chunk := range chunks {
		if err := limiter.Wait(ctx); err != nil {
			return bars, err
		}

		from := chunk.From.Unix()
		to := chunk.To.Unix()

		resp, err := doWithRateLimit(ctx, func() (*resty.Response, error) {
			return client.R().SetContext(ctx).
				SetQueryParam("interval", "1m").
				SetQueryParam("from", strconv.FormatInt(from, 10)).
				SetQueryParam("to", strconv.FormatInt(to, 10)).
				Get(url)
		})
		if err != nil {
			return bars, err
		}

		if resp.StatusCode() >= 300 {
			logger.Warn().Int("StatusCode", resp.StatusCode()).Str("Ticker", job.Ticker).Time("From", chunk.From).Time("To", chunk.To).Msg("intraday HTTP error, skipping chunk")
			continue
		}

		chunkBars, err := parseIntradayResponse(resp.Body(), job.Ticker, job.CompositeFigi)
		if err != nil {
			return bars, err
		}

		bars = append(bars, chunkBars...)
	}

	return bars, nil
}
