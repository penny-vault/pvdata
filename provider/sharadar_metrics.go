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
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/tidwall/gjson"
	"golang.org/x/time/rate"
)

type sharadarMetric struct {
	Ticker      string
	Date        string // YYYY-MM-DD
	LastUpdated string // YYYY-MM-DD
	EV          float64
	EVtoEBIT    float64
	EVtoEBITDA  float64
	MarketCap   float64
	PB          float64
	PE          float64
	PS          float64
}

func downloadAllSharadarMetrics(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
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

	rateLimit, err := strconv.Atoi(subscription.Config["rateLimit"])
	if err != nil {
		logger.Error().Err(err).Str("configRateLimit", subscription.Config["rateLimit"]).Msg("could not convert rateLimit configuration parameter to an integer")

		rateLimit = 5000
	}

	if rateLimit <= 0 {
		rateLimit = 5000
	}

	limiter := rate.NewLimiter(rate.Limit(float64(rateLimit)/float64(61)), 1)

	// Get a list of active assets
	conn, err := subscription.Library.Pool.Acquire(ctx)
	if err != nil {
		log.Panic().Msg("could not acquire database connection")
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

	// fetch current SP500 constituents and emit index snapshots
	client := resty.New().
		SetQueryParam("api_key", subscription.Config["apiKey"]).
		SetRetryCount(3).
		SetRetryWaitTime(5 * time.Second).
		SetRetryMaxWaitTime(30 * time.Second).
		AddRetryCondition(func(r *resty.Response, err error) bool {
			return err != nil || r.StatusCode() == 429 || r.StatusCode() >= 500
		}).
		SetTimeout(60 * time.Second)

	if err := limiter.Wait(ctx); err != nil {
		logger.Error().Err(err).Msg("rate limit wait failed")

		runSummary.Status = data.RunFailed

		return
	}

	sp500Url := "https://data.nasdaq.com/api/v3/datatables/SHARADAR/SP500"

	resp, err := client.R().SetQueryParam("action", "current").Get(sp500Url)
	if err != nil {
		logger.Error().Err(err).Msg("failed to download SP500 constituents")
	}

	forDate := time.Now().Format("2006-01-02")

	if resp != nil && resp.StatusCode() >= 400 {
		logger.Error().Int("StatusCode", resp.StatusCode()).Str("Url", sp500Url).Bytes("Body", resp.Body()).Msg("error when requesting SP500 url")
	}

	cursor := ""
	for {
		log.Info().Str("cursor", cursor).Msg("Fetching next page sharadar metrics")

		var n int

		cursor, n = downloadSharadarMetrics(ctx, subscription, client, limiter, cursor, out, forDate, figiMap)
		numObs += n

		if cursor == "" {
			break
		}
	}
}

func downloadSharadarMetrics(ctx context.Context, subscription *library.Subscription, client *resty.Client, limiter *rate.Limiter, cursor string, out chan<- *data.Observation, forDate string, figiMap map[string]string) (string, int) {
	logger := zerolog.Ctx(ctx)

	if err := limiter.Wait(ctx); err != nil {
		logger.Error().Err(err).Msg("rate limit wait failed")
		return "", 0
	}

	// get nyc timezone
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		logger.Panic().Err(err).Msg("could not load timezone")
		return "", 0
	}

	// download daily metrics
	dailyUrl := "https://data.nasdaq.com/api/v3/datatables/SHARADAR/DAILY"
	req := client.R()

	if cursor != "" {
		req.SetQueryParam("qopts.cursor_id", cursor)
	} else {
		req.SetQueryParam("date", forDate)
	}

	resp, err := req.Get(dailyUrl)
	if err != nil {
		logger.Error().Err(err).Msg("failed to download daily metrics from sharadar")
		return "", 0
	}

	if resp.StatusCode() >= 400 {
		logger.Error().Int("StatusCode", resp.StatusCode()).Str("Url", dailyUrl).Bytes("Body", resp.Body()).Msg("error when requesting url")
		return "", 0
	}

	responseBody := string(resp.Body())
	result := gjson.Get(responseBody, "datatable.data")
	count := 0

	for _, val := range result.Array() {
		metric := &sharadarMetric{
			Ticker:      val.Get("0").String(),
			Date:        val.Get("1").String(), // YYYY-MM-DD
			LastUpdated: val.Get("2").String(), // YYYY-MM-DD
			EV:          val.Get("3").Float(),
			EVtoEBIT:    val.Get("4").Float(),
			EVtoEBITDA:  val.Get("5").Float(),
			MarketCap:   val.Get("6").Float(),
			PB:          val.Get("7").Float(),
			PE:          val.Get("8").Float(),
			PS:          val.Get("9").Float(),
		}

		// convert to pv metric type
		pvMetric := metric.PvMetric(figiMap, nyc)

		out <- &data.Observation{
			Metric:           pvMetric,
			ObservationDate:  time.Now(),
			SubscriptionID:   subscription.ID,
			SubscriptionName: subscription.Name,
		}

		count++
	}

	return gjson.Get(responseBody, "meta.next_cursor_id").String(), count
}

func (metric *sharadarMetric) PvMetric(figiMap map[string]string, loc *time.Location) *data.Metric {
	pvMetric := &data.Metric{
		Ticker:     strings.ReplaceAll(metric.Ticker, ".", "/"),
		MarketCap:  int64(metric.MarketCap * 1e6),
		EV:         int64(metric.EV * 1e6),
		PE:         metric.PE,
		PB:         metric.PB,
		PS:         metric.PS,
		EVtoEBIT:   metric.EVtoEBIT,
		EVtoEBITDA: metric.EVtoEBITDA,
	}

	if figi, ok := figiMap[pvMetric.Ticker]; ok {
		pvMetric.CompositeFigi = figi
	}

	if date, err := time.Parse("2006-01-02", metric.Date); err == nil {
		pvMetric.EventDate = time.Date(date.Year(), date.Month(), date.Day(), 0, 0, 0, 0, loc)
	} else {
		log.Error().Err(err).Msg("error parsing metric date")
	}

	return pvMetric
}
