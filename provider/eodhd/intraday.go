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
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
)

const (
	intradayURLTemplate = "https://eodhd.com/api/intraday/%s.%s"

	// intradayMaxRange is the largest from-to window EODHD accepts in a
	// single 1-minute intraday request.
	intradayMaxRange = 120 * 24 * time.Hour
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

// -- Config parsing --

// intradayEntry pairs a configured ticker with the EODHD exchange code
// to query against.
type intradayEntry struct {
	Ticker   string
	Exchange string
}

// parseIntradayTickers parses the comma-separated intradayTickers
// config value. Each entry may be a plain ticker (which uses the
// provided defaultExchange) or "TICKER.EXCHANGE" for an explicit
// override. Tickers are stored in pv-data form (slashes), but slashes
// would not survive a config string anyway; share-class entries
// should be written as "BRK-A" in the config and are translated in
// parseIntradayTickers itself.
func parseIntradayTickers(raw, defaultExchange string) []intradayEntry {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}

	var out []intradayEntry

	for part := range strings.SplitSeq(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}

		ticker := part
		exchange := defaultExchange

		if dot := strings.Index(part, "."); dot >= 0 {
			ticker = strings.TrimSpace(part[:dot])
			exchange = strings.TrimSpace(part[dot+1:])
		}

		if ticker == "" {
			continue
		}

		out = append(out, intradayEntry{
			Ticker:   normalizeTicker(ticker),
			Exchange: exchange,
		})
	}

	return out
}

func readIntradayLookback(raw string) int {
	if v, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && v > 0 {
		return v
	}

	return 5
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

	entries := parseIntradayTickers(subscription.Config["intradayTickers"], defaultExchange)
	if len(entries) == 0 {
		logger.Warn().Msg("intradayTickers is empty; intraday loader has no work to do")
		return
	}

	lookbackDays := readIntradayLookback(subscription.Config["intradayLookbackDays"])

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
		tickerToFigi[a.Ticker] = a.CompositeFigi
	}

	type job struct {
		entry intradayEntry
		figi  string
	}

	jobs := make([]job, 0, len(entries))

	for _, e := range entries {
		f, ok := tickerToFigi[e.Ticker]
		if !ok || f == "" {
			logger.Warn().Str("Ticker", e.Ticker).Msg("intraday ticker has no FIGI in DB, skipping")
			continue
		}

		jobs = append(jobs, job{entry: e, figi: f})
	}

	if len(jobs) == 0 {
		logger.Warn().Msg("no intraday jobs after FIGI resolution")
		return
	}

	workerCount := defaultEODWorkers
	if w, err := strconv.Atoi(subscription.Config["workers"]); err == nil && w > 0 {
		workerCount = w
	}

	if workerCount > len(jobs) {
		workerCount = len(jobs)
	}

	logger.Info().Int("workers", workerCount).Int("tickers", len(jobs)).Int("lookbackDays", lookbackDays).Msg("intraday worker pool starting")

	to := time.Now().UTC()
	from := to.AddDate(0, 0, -lookbackDays)

	chunks := chunkRange(from, to, intradayMaxRange)

	workerCtx, cancelWorkers := context.WithCancel(ctx)
	defer cancelWorkers()

	jobCh := make(chan job)

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
				bars, err := fetchIntradayChunks(workerCtx, client, limiter, j.entry, j.figi, chunks)
				if errors.Is(err, errDailyRateLimit) {
					dailyLimitFlag.Store(true)
					cancelWorkers()

					return
				}

				if err != nil {
					if errors.Is(err, context.Canceled) {
						return
					}

					logger.Warn().Err(err).Str("Ticker", j.entry.Ticker).Msg("intraday fetch failed, skipping")

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

func fetchIntradayChunks(ctx context.Context, client *resty.Client, limiter *rate.Limiter, entry intradayEntry, compositeFigi string, chunks []intradayChunk) ([]*data.IntradayBar, error) {
	logger := zerolog.Ctx(ctx)
	eodhdTicker := denormalizeTicker(entry.Ticker)
	url := fmt.Sprintf(intradayURLTemplate, eodhdTicker, entry.Exchange)

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
			logger.Warn().Int("StatusCode", resp.StatusCode()).Str("Ticker", entry.Ticker).Time("From", chunk.From).Time("To", chunk.To).Msg("intraday HTTP error, skipping chunk")
			continue
		}

		chunkBars, err := parseIntradayResponse(resp.Body(), entry.Ticker, compositeFigi)
		if err != nil {
			return bars, err
		}

		bars = append(bars, chunkBars...)
	}

	return bars, nil
}
