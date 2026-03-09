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
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/gocarina/gocsv"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/figi"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
)

type Tiingo struct {
}

var tiingoExchangeMap = map[string]data.Exchange{
	"BATS":      data.BATSExchange,
	"NASDAQ":    data.NasdaqExchange,
	"NMFQS":     data.NMFQSExchange,
	"NYSE":      data.NYSEExchange,
	"NYSE ARCA": data.ARCAExchange,
	"NYSE MKT":  data.NYSEMktExchange,
}

func (tiingo *Tiingo) Name() string {
	return "tiingo"
}

func (tiingo *Tiingo) ConfigDescription() map[string]string {
	return map[string]string{
		"apiKey":    "Enter your tiingo API key:",
		"rateLimit": "What is the maximum number of requests per minute?",
	}
}

func (tiingo *Tiingo) Description() string {
	return `Tiingo provides EOD, Realtime, News and Fundamental data for stocks. Tiingo built a custom data processing engine that prioritizes performance, cleanliness, and completeness.`
}

func (tiingo *Tiingo) Datasets() map[string]Dataset {
	return map[string]Dataset{
		"EOD": {
			Name:        "EOD",
			Description: "Get end-of-day stock prices for active assets.",
			DataTypes:   []*data.DataType{data.DataTypes[data.EODKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(1960, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: downloadTiingoEODQuotes,
		},

		"Stock Tickers": {
			Name:        "Stock Tickers",
			Description: "Details about tradeable stocks, ADRs, Mutual Funds and ETFs.",
			DataTypes:   []*data.DataType{data.DataTypes[data.AssetKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2014, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: downloadTiingoAssets,
		},
	}
}

// Private interface

type tiingoEod struct {
	Date          string  `json:"date"`
	Ticker        string  `json:"ticker"`
	CompositeFigi string  `json:"compositeFigi"`
	Open          float64 `json:"open"`
	High          float64 `json:"high"`
	Low           float64 `json:"low"`
	Close         float64 `json:"close"`
	Volume        float64 `json:"volume"`
	Dividend      float64 `json:"divCash"`
	Split         float64 `json:"splitFactor"`
}

type tiingoAsset struct {
	Ticker        string `json:"ticker" csv:"ticker"`
	Exchange      string `json:"exchange" csv:"exchange"`
	AssetType     string `json:"assetType" csv:"assetType"`
	PriceCurrency string `json:"priceCurrency" csv:"priceCurrency"`
	StartDate     string `json:"startDate" csv:"startDate"`
	EndDate       string `json:"endDate" csv:"endDate"`
}

func downloadTiingoEODQuotes(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
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
		runSummary.Status = data.RunFailed
		return
	}

	if rateLimit <= 0 {
		rateLimit = 5000
	}

	client := resty.New().
		SetQueryParam("token", subscription.Config["apiKey"]).
		SetRetryCount(3).
		SetRetryWaitTime(5 * time.Second).
		SetRetryMaxWaitTime(30 * time.Second).
		AddRetryCondition(func(r *resty.Response, err error) bool {
			return err != nil || r.StatusCode() == 429 || r.StatusCode() >= 500
		}).
		SetTimeout(60 * time.Second)
	limiter := rate.NewLimiter(rate.Limit(float64(rateLimit)/float64(61)), 1)

	// get nyc timezone
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		logger.Panic().Err(err).Msg("could not load timezone")
		return
	}

	// fetch ticker EOD prices
	if err := limiter.Wait(ctx); err != nil {
		log.Panic().Err(err).Msg("rate limit wait failed")
	}

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

	log.Debug().Int("NumAssets", len(assets)).Msg("downloading EOD quotes from Tiingo")

	lookback := LookbackFromContext(ctx, 14*24*time.Hour)
	startDate := time.Now().Add(-lookback)
	startDateStr := startDate.Format("2006-01-02")

	for _, asset := range assets {
		// reformat ticker for tiingo
		ticker := strings.ReplaceAll(asset.Ticker, "/", "-")
		url := fmt.Sprintf("https://api.tiingo.com/tiingo/daily/%s/prices", ticker)

		respContent := make([]*tiingoEod, 0)
		resp, err := client.R().
			SetQueryParam("startDate", startDateStr).
			SetResult(&respContent).
			Get(url)
		if err != nil {
			logger.Error().Err(err).Msg("resty returned an error when querying eod prices")
			return
		}

		if resp.StatusCode() >= 300 {
			logger.Error().Int("StatusCode", resp.StatusCode()).Str("Ticker", ticker).Str("URL", resp.Request.URL).Msg("tiigno returned an invalid HTTP response")
			continue
		}

		for _, quote := range respContent {
			quoteDate, err := time.Parse(time.RFC3339Nano, quote.Date)
			if err != nil {
				logger.Error().Err(err).Str("tiingoDate", quote.Date).Msg("could not parse date from tiingo eod object")
				continue
			}

			// set tiingo date to correct time zone and market close
			quoteDate = time.Date(quoteDate.Year(), quoteDate.Month(), quoteDate.Day(), 16, 0, 0, 0, nyc)

			eodQuote := &data.Eod{
				Date:          quoteDate,
				Ticker:        asset.Ticker,
				CompositeFigi: asset.CompositeFigi,
				Open:          quote.Open,
				High:          quote.High,
				Low:           quote.Low,
				Close:         quote.Close,
				Volume:        quote.Volume,
				Dividend:      quote.Dividend,
				Split:         quote.Split,
			}

			out <- &data.Observation{
				EodQuote:         eodQuote,
				ObservationDate:  time.Now(),
				SubscriptionID:   subscription.ID,
				SubscriptionName: subscription.Name,
			}
			numObs++
		}
	}
}

func downloadTiingoAssets(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
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

	// get nyc timezone
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		logger.Panic().Err(err).Msg("could not load timezone")
		return
	}

	tickerUrl := "https://apimedia.tiingo.com/docs/tiingo/daily/supported_tickers.zip"
	client := resty.New().
		SetRetryCount(3).
		SetRetryWaitTime(5 * time.Second).
		SetRetryMaxWaitTime(30 * time.Second).
		AddRetryCondition(func(r *resty.Response, err error) bool {
			return err != nil || r.StatusCode() == 429 || r.StatusCode() >= 500
		}).
		SetTimeout(60 * time.Second)
	assets := []*tiingoAsset{}

	resp, err := client.R().Get(tickerUrl)
	if err != nil {
		logger.Error().Err(err).Msg("failed to download tickers")
	}

	if resp.StatusCode() >= 400 {
		logger.Error().Int("StatusCode", resp.StatusCode()).Str("Url", tickerUrl).Bytes("Body", resp.Body()).Msg("error when requesting tiingo supported_tickers.zip")
		return
	}

	// unzip downloaded data
	body := resp.Body()
	if err != nil {
		logger.Error().Err(err).Msg("could not read response body when downloading supported tickers from tiingo")
		return
	}

	zipReader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		logger.Error().Err(err).Msg("failed to read tiingo supported tickers zip file")
		return
	}

	// Read all the files from zip archive
	var tickerCsvBytes []byte
	if len(zipReader.File) == 0 {
		logger.Error().Msg("no files contained in tiingo supported tickers zip file")
		return
	}

	zipFile := zipReader.File[0]
	tickerCsvBytes, err = readZipFile(zipFile)
	if err != nil {
		logger.Error().Err(err).Msg("failed to read ticker csv from tiingo supported tickers zip file")
		return
	}

	if err := gocsv.UnmarshalBytes(tickerCsvBytes, &assets); err != nil {
		logger.Error().Err(err).Msg("failed to unmarshal tiingo supported tickers csv")
		return
	}

	validExchanges := []string{"BATS", "NASDAQ", "NMFQS", "NYSE", "NYSE ARCA", "NYSE MKT"}
	commonAssets := make([]*data.Asset, 0, 25000)
	for _, tiingoAsset := range assets {
		// remove assets on invalid exchanges
		keep := false
		for _, exchange := range validExchanges {
			if tiingoAsset.Exchange == exchange {
				keep = true
			}
		}
		if !keep {
			continue
		}

		// If both the start date and end date are not set skip it
		if tiingoAsset.StartDate == "" && tiingoAsset.EndDate == "" {
			continue
		}

		// filter out tickers we should ignore
		if tiingoIgnoreTicker(tiingoAsset.Ticker) {
			continue
		}

		tiingoAsset.Ticker = strings.ReplaceAll(tiingoAsset.Ticker, "-", "/")
		pvAsset := &data.Asset{
			Ticker:          tiingoAsset.Ticker,
			ListingDate:     tiingoAsset.StartDate,
			DelistingDate:   tiingoAsset.EndDate,
			PrimaryExchange: tiingoExchangeMap[tiingoAsset.Exchange],
			LastUpdated:     time.Now(),
		}

		switch tiingoAsset.AssetType {
		case "Stock":
			pvAsset.AssetType = data.CommonStock
		case "ETF":
			pvAsset.AssetType = data.ETF
		case "Mutual Fund":
			pvAsset.AssetType = data.MutualFund
		}

		if tiingoAsset.EndDate != "" {
			endDate, err := time.Parse("2006-01-02", tiingoAsset.EndDate)
			if err != nil {
				log.Warn().Str("EndDate", tiingoAsset.EndDate).Err(err).Msg("could not parse end date")
			}

			endDate = endDate.In(nyc)

			now := time.Now().In(nyc)
			age := now.Sub(endDate)
			if age < (time.Hour * 24 * 7) {
				pvAsset.DelistingDate = ""
			} else {
				pvAsset.DelistingDate = endDate.Format(time.RFC3339)
			}
		}

		if pvAsset.DelistingDate == "" {
			pvAsset.Active = true
			commonAssets = append(commonAssets, pvAsset)
		}
	}

	log.Debug().Int("NumAssetsToEnrich", len(commonAssets)).Msg("number of assets to enrich with Composite FIGI")
	figi.Enrich(commonAssets...)

	pvAssetMap := make(map[string]*data.Asset, len(commonAssets))
	for _, asset := range commonAssets {
		if asset.CompositeFigi != "" {
			pvAssetMap[asset.CompositeFigi] = asset
		}
	}

	// get a list of assets already in the database
	conn, err := subscription.Library.Pool.Acquire(ctx)
	if err != nil {
		log.Panic().Msg("could not acquire database connection")
	}

	defer conn.Release()

	activeDBAssets, err := data.ActiveAssets(ctx, conn, subscription.DataTablesMap[data.AssetKey])
	if err != nil {
		logger.Error().Err(err).Msg("could not load active assets from database")
		return
	}

	// determine which assets are no longer active
	for _, dbAsset := range activeDBAssets {
		_, ok := pvAssetMap[dbAsset.CompositeFigi]
		if !ok {
			dbAsset.Active = false
			dbAsset.DelistingDate = time.Now().In(nyc).Format(time.RFC3339)
			commonAssets = append(commonAssets, dbAsset)
		}
	}

	for _, asset := range commonAssets {
		if asset.CompositeFigi == "" {
			continue
		}

		// make a copy of the asset and fix ticker to match pv-data standard
		// e.g. BRK.A -> BRK/A
		asset2 := *asset
		asset2.Ticker = strings.ReplaceAll(asset2.Ticker, "-", "/")

		out <- &data.Observation{
			AssetObject:      &asset2,
			ObservationDate:  time.Now(),
			SubscriptionID:   subscription.ID,
			SubscriptionName: subscription.Name,
		}
		numObs++
	}
}

// tiingoIgnoreTicker interprets the structure of the ticker to identify
// the share type (Warrant, Unit, Preferred Share, etc.) and filters
// out unsupported stock types
func tiingoIgnoreTicker(ticker string) bool {
	ignore := strings.HasPrefix(ticker, "ATEST")
	ignore = ignore || strings.HasPrefix(ticker, "NTEST")
	ignore = ignore || strings.HasPrefix(ticker, "PTEST")
	ignore = ignore || strings.Contains(ticker, " ")
	matcher := regexp.MustCompile(`^[A-Za-z0-9]+-[WPU]{1}.*$`)
	ignore = ignore || matcher.Match([]byte(ticker))
	matcher = regexp.MustCompile(`^[A-Za-z0-9]{4}[WPU]{1}.*$`)
	ignore = ignore || matcher.Match([]byte(ticker))

	return ignore
}

func readZipFile(zf *zip.File) ([]byte, error) {
	f, err := zf.Open()
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
