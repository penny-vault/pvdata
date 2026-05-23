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
package massive

import (
	"context"
	"fmt"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog"
	"golang.org/x/time/rate"
)

// flatFilesPublishHourEST is the hour (in America/New_York) at which
// the prior trading day's flat file becomes available in the
// flat-files bucket. Before this cutoff we have to fall back to the
// Daily Market Summary REST endpoint to get the most recent bar.
const flatFilesPublishHourEST = 11

// dailyMarketSummaryURL is the base URL for the Daily Market Summary
// REST endpoint (GET /v2/aggs/grouped/locale/us/market/stocks/{date}).
// It is a var (not const) so httptest-backed tests can swap it to a
// local server.
var dailyMarketSummaryURL = "https://api.massive.com/v2/aggs/grouped/locale/us/market/stocks"

// flatFileAvailableForDate reports whether the flat file covering
// trading date d is expected to be published by `now`. Massive's
// flat-files bucket gets the previous trading day's file at roughly
// 11:00 America/New_York; until that cutoff we have to use the
// Daily Market Summary REST endpoint to pick up that day's data.
func flatFileAvailableForDate(d, now time.Time) bool {
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return true
	}

	publishAt := time.Date(d.Year(), d.Month(), d.Day()+1, flatFilesPublishHourEST, 0, 0, 0, nyc)

	return !now.In(nyc).Before(publishAt)
}

// dailyMarketSummaryRow mirrors one element of the `results` array
// returned by the Daily Market Summary endpoint.
type dailyMarketSummaryRow struct {
	Ticker       string  `json:"T"`
	Open         float64 `json:"o"`
	High         float64 `json:"h"`
	Low          float64 `json:"l"`
	Close        float64 `json:"c"`
	Volume       float64 `json:"v"`
	VWAP         float64 `json:"vw"`
	Transactions int64   `json:"n"`
	Timestamp    int64   `json:"t"`
	OTC          bool    `json:"otc"`
}

// dailyMarketSummaryResponse is the envelope for the Daily Market
// Summary endpoint. It is a separate struct from massiveResponse
// because the endpoint returns `results` as a typed array rather than
// a generic blob, and uses camelCase top-level keys (`resultsCount`)
// instead of the v3 reference endpoints' snake_case.
type dailyMarketSummaryResponse struct {
	Adjusted     bool                    `json:"adjusted"`
	QueryCount   int                     `json:"queryCount"`
	RequestID    string                  `json:"request_id"`
	ResultsCount int                     `json:"resultsCount"`
	Status       string                  `json:"status"`
	Results      []dailyMarketSummaryRow `json:"results"`
}

// fetchDailyMarketSummary calls the Daily Market Summary REST endpoint
// for date d and converts the response into aggRow records compatible
// with the flat-file processing path. `adjusted=false` is passed so
// split factors are applied downstream from the splits map, matching
// the flat-file convention (raw OHLC + separate split factor).
func fetchDailyMarketSummary(ctx context.Context, client *resty.Client, limiter *rate.Limiter, d time.Time) ([]aggRow, error) {
	if err := limiter.Wait(ctx); err != nil {
		return nil, err
	}

	dateStr := d.Format("2006-01-02")
	url := fmt.Sprintf("%s/%s", dailyMarketSummaryURL, dateStr)

	var resp dailyMarketSummaryResponse

	httpResp, err := client.R().
		SetContext(ctx).
		SetQueryParam("adjusted", "false").
		SetQueryParam("include_otc", "false").
		SetResult(&resp).
		Get(url)
	if err != nil {
		return nil, err
	}

	if httpResp.StatusCode() >= 300 {
		return nil, fmt.Errorf("%w (%d): %s", ErrInvalidStatusCode, httpResp.StatusCode(), string(httpResp.Body()))
	}

	rows := make([]aggRow, 0, len(resp.Results))
	for _, r := range resp.Results {
		rows = append(rows, aggRow{
			Ticker:       r.Ticker,
			Volume:       r.Volume,
			Open:         r.Open,
			Close:        r.Close,
			High:         r.High,
			Low:          r.Low,
			WindowStart:  r.Timestamp,
			Transactions: r.Transactions,
		})
	}

	return rows, nil
}

// streamDailyMarketSummaryForDate is the REST counterpart to
// streamDayAggsForDate. It pulls the day's bars from the Daily Market
// Summary endpoint instead of S3 and emits them through the same Eod
// observation path. It deliberately does NOT write a parquet backup:
// the next run after flatFilesPublishHourEST will refetch the same
// date from the authoritative flat file and backup that copy instead.
func streamDailyMarketSummaryForDate(ctx context.Context, client *resty.Client, limiter *rate.Limiter, sub *library.Subscription, universe *data.AssetHistory, splits, divs corporateActions, d time.Time, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)

	rows, err := fetchDailyMarketSummary(ctx, client, limiter, d)
	if err != nil {
		return 0, err
	}

	logger.Info().
		Time("date", d).
		Int("rows", len(rows)).
		Msg("fetched daily market summary via REST")

	return emitDayAggRowsAsEod(ctx, sub, universe, splits, divs, rows, d, "daily_market_summary", out)
}
