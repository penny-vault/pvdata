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
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/tidwall/gjson"
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

type sharadarSP500 struct {
	Ticker string
	Date   string
	Action string
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
		runSummary.Status = data.RunSuccess
		exitNotification <- runSummary
	}()

	// Get a list of active assets
	conn, err := subscription.Library.Pool.Acquire(ctx)
	if err != nil {
		log.Panic().Msg("could not acquire database connection")
	}

	defer conn.Release()

	assets := data.ActiveAssets(ctx, conn)
	figiMap := make(map[string]string, len(assets))
	for _, asset := range assets {
		figiMap[asset.Ticker] = asset.CompositeFigi
	}

	// get a map of sp500 constituents
	client := resty.New().SetQueryParam("api_key", subscription.Config["apiKey"])
	sp500Url := "https://data.nasdaq.com/api/v3/datatables/SHARADAR/SP500"
	resp, err := client.R().SetQueryParam("action", "current").Get(sp500Url)
	if err != nil {
		logger.Error().Err(err).Msg("failed to download tickers")
	}

	if resp.StatusCode() >= 400 {
		logger.Error().Int("StatusCode", resp.StatusCode()).Str("Url", sp500Url).Bytes("Body", resp.Body()).Msg("error when requesting url")
		return
	}

	sp500Map := make(map[string]bool, 500)
	currDate := ""

	responseBody := string(resp.Body())
	result := gjson.Get(responseBody, "datatable.data")
	for _, val := range result.Array() {
		ticker := &sharadarSP500{
			Date:   val.Get("0").String(),
			Action: val.Get("1").String(),
			Ticker: strings.ReplaceAll(val.Get("2").String(), ".", "/"),
		}

		sp500Map[ticker.Ticker] = true
		currDate = ticker.Date
	}

	cursor := ""
	for {
		log.Info().Str("cursor", cursor).Msg("Fetching next page sharadar tickers")
		cursor = downloadSharadarMetrics(ctx, subscription, cursor, out, currDate, sp500Map, figiMap)
		if cursor == "" {
			break
		}
	}
}

func downloadSharadarMetrics(ctx context.Context, subscription *library.Subscription, cursor string, out chan<- *data.Observation, forDate string, sp500Map map[string]bool, figiMap map[string]string) string {
	logger := zerolog.Ctx(ctx)

	// get nyc timezone
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		logger.Panic().Err(err).Msg("could not load timezone")
		return ""
	}

	client := resty.New().SetQueryParam("api_key", subscription.Config["apiKey"])

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
	}

	if resp.StatusCode() >= 400 {
		logger.Error().Int("StatusCode", resp.StatusCode()).Str("Url", dailyUrl).Bytes("Body", resp.Body()).Msg("error when requesting url")
		return ""
	}

	responseBody := string(resp.Body())
	result := gjson.Get(responseBody, "datatable.data")
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
		pvMetric := metric.PvMetric(sp500Map, figiMap, nyc)

		out <- &data.Observation{
			Metric:           pvMetric,
			ObservationDate:  time.Now(),
			SubscriptionID:   subscription.ID,
			SubscriptionName: subscription.Name,
		}
	}

	return gjson.Get(responseBody, "meta.next_cursor_id").String()
}

func (metric *sharadarMetric) PvMetric(sp500Map map[string]bool, figiMap map[string]string, loc *time.Location) *data.Metric {
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

	if _, ok := sp500Map[pvMetric.Ticker]; ok {
		pvMetric.SP500 = true
	}

	return pvMetric
}
