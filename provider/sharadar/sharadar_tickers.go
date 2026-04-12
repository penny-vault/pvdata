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
package sharadar

import (
	"context"
	"regexp"
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
	"github.com/tidwall/gjson"
	"golang.org/x/time/rate"
)

type sharadarTicker struct {
	PermaTicker    int64
	Ticker         string
	Name           string
	Exchange       string
	IsDelisted     string
	Category       string
	CUSIPs         string // space separated cusips
	SICCode        int64
	SICSector      string
	SICIndustry    string
	FAMASector     string
	FAMAIndustry   string
	Sector         string
	Industry       string
	ScaleMarketcap string
	ScaleRevenue   string
	RelatedTickers string // space separated related tickers
	Currency       string
	Location       string
	LastUpdated    time.Time
	FirstAdded     time.Time
	FirstPriceDate time.Time
	LastPriceDate  time.Time
	FirstQuarter   string
	LastQuarter    string
	SECFilings     string
	CompanySite    string
}

func downloadAllSharadarTickers(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
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
		log.Info().Str("provider", "sharadar").Str("dataset", "Stock Tickers").Msg("ticker/FIGI filtering not applicable to asset catalog downloads, skipping")
		return
	}

	rateLimit, err := strconv.Atoi(subscription.Config["rateLimit"])
	if err != nil {
		logger.Error().Err(err).Str("configRateLimit", subscription.Config["rateLimit"]).Msg("could not convert rateLimit configuration parameter to an integer")

		rateLimit = 5000
	}

	if rateLimit <= 0 {
		rateLimit = 5000
	}

	limiter := rate.NewLimiter(rate.Limit(float64(rateLimit)/float64(61)), 1)

	client := resty.New().
		SetQueryParam("api_key", subscription.Config["apiKey"]).
		SetRetryCount(3).
		SetRetryWaitTime(5 * time.Second).
		SetRetryMaxWaitTime(30 * time.Second).
		AddRetryCondition(func(r *resty.Response, err error) bool {
			return err != nil || r.StatusCode() == 429 || r.StatusCode() >= 500
		}).
		SetTimeout(60 * time.Second)

	cursor := ""
	for {
		log.Info().Str("cursor", cursor).Msg("Fetching next page sharadar tickers")

		var n int

		cursor, n = downloadSharadarTickers(ctx, subscription, client, limiter, cursor, out)
		numObs += n

		if cursor == "" {
			break
		}
	}
}

func downloadSharadarTickers(ctx context.Context, subscription *library.Subscription, client *resty.Client, limiter *rate.Limiter, cursor string, out chan<- *data.Observation) (string, int) {
	logger := zerolog.Ctx(ctx)

	if err := limiter.Wait(ctx); err != nil {
		logger.Error().Err(err).Msg("rate limit wait failed")
		return "", 0
	}

	enrichAssets := make([]*data.Asset, 0, 10000)
	allAssets := make([]*data.Asset, 0, 10000)

	// get nyc timezone
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		logger.Panic().Err(err).Msg("could not load timezone")
		return "", 0
	}

	tickerUrl := "https://data.nasdaq.com/api/v3/datatables/SHARADAR/TICKERS"

	req := client.R()
	if cursor != "" {
		req.SetQueryParam("qopts.cursor_id", cursor)
	}

	resp, err := req.SetQueryParam("table", "SF1").Get(tickerUrl)
	if err != nil {
		logger.Error().Err(err).Msg("failed to download tickers")
		return "", 0
	}

	if resp.StatusCode() >= 400 {
		logger.Error().Int("StatusCode", resp.StatusCode()).Str("Url", tickerUrl).Bytes("Body", resp.Body()).Msg("error when requesting url")
		return "", 0
	}

	responseBody := string(resp.Body())

	result := gjson.Get(responseBody, "datatable.data")
	for _, val := range result.Array() {
		ticker := &sharadarTicker{
			PermaTicker:    val.Get("1").Int(),
			Ticker:         val.Get("2").String(),
			Name:           val.Get("3").String(),
			Exchange:       val.Get("4").String(),
			IsDelisted:     val.Get("5").String(),
			Category:       val.Get("6").String(),
			CUSIPs:         val.Get("7").String(),
			SICCode:        val.Get("8").Int(),
			SICSector:      val.Get("9").String(),
			SICIndustry:    val.Get("10").String(),
			FAMASector:     val.Get("11").String(),
			FAMAIndustry:   val.Get("12").String(),
			Sector:         val.Get("13").String(),
			Industry:       val.Get("14").String(),
			ScaleMarketcap: val.Get("15").String(),
			ScaleRevenue:   val.Get("16").String(),
			RelatedTickers: val.Get("17").String(),
			Currency:       val.Get("18").String(),
			Location:       val.Get("19").String(),
			FirstQuarter:   val.Get("24").String(),
			LastQuarter:    val.Get("25").String(),
			SECFilings:     val.Get("26").String(),
			CompanySite:    val.Get("27").String(),
		}

		lastUpdatedStr := val.Get("20").String()
		if lastUpdatedStr != "" {
			ticker.LastUpdated, err = time.Parse("2006-01-02", lastUpdatedStr)
			if err != nil {
				log.Error().Err(err).Str("InputStr", lastUpdatedStr).Msg("could not parse last updated date")

				ticker.LastUpdated = time.Now().In(nyc)
			}
		}

		firstAddedStr := val.Get("21").String()
		if firstAddedStr != "" {
			ticker.FirstAdded, err = time.Parse("2006-01-02", firstAddedStr)
			if err != nil {
				log.Error().Err(err).Str("InputStr", firstAddedStr).Msg("could not parse first added date")

				ticker.FirstAdded = time.Time{}
			}
		}

		firstPriceStr := val.Get("22").String()
		if firstPriceStr != "" {
			ticker.FirstPriceDate, err = time.Parse("2006-01-02", firstPriceStr)
			if err != nil {
				log.Error().Err(err).Str("InputStr", firstPriceStr).Msg("could not parse first price date")

				ticker.FirstPriceDate = time.Time{}
			}
		}

		lastPriceStr := val.Get("22").String()
		if lastPriceStr != "" {
			ticker.LastPriceDate, err = time.Parse("2006-01-02", lastPriceStr)
			if err != nil {
				log.Error().Err(err).Str("InputStr", lastPriceStr).Msg("could not parse last price date")

				ticker.LastPriceDate = time.Time{}
			}
		}

		if ticker.Exchange == "" {
			continue
		}

		// convert to pv asset type
		pvAsset := ticker.ToAsset()

		// ignore unknown assets or exchanges
		if pvAsset.PrimaryExchange == data.OTCExchange ||
			pvAsset.PrimaryExchange == data.IndexExchange ||
			pvAsset.PrimaryExchange == data.UnknownExchange ||
			pvAsset.AssetType == data.INDEX ||
			pvAsset.AssetType == data.UnknownAsset {
			continue
		}

		if pvAsset.Active {
			enrichAssets = append(enrichAssets, pvAsset)
		}

		allAssets = append(allAssets, pvAsset)
	}

	// enrich assets
	figi.Enrich(enrichAssets...)

	count := 0

	for _, asset := range allAssets {
		out <- &data.Observation{
			AssetObject:      asset,
			ObservationDate:  time.Now(),
			SubscriptionID:   subscription.ID,
			SubscriptionName: subscription.Name,
		}

		count++
	}

	return gjson.Get(responseBody, "meta.next_cursor_id").String(), count
}

func (ticker *sharadarTicker) ToAsset() *data.Asset {
	asset := &data.Asset{
		Ticker:               ticker.Ticker,
		Name:                 ticker.Name,
		PrimaryExchange:      ticker.NormalizedExchange(),
		AssetType:            ticker.NormalizedCategory(),
		Active:               ticker.IsDelisted == "N",
		CorporateUrl:         ticker.CompanySite,
		SIC:                  func() *int { v := int(ticker.SICCode); return &v }(),
		HeadquartersLocation: ticker.Location,
		Industry:             ticker.Industry,
		Sector:               ticker.Sector,
		ListingDate:          ticker.FirstAdded.Format(time.RFC3339),
		OtherIdentifiers: map[string]string{
			"sharadar": strconv.Itoa(int(ticker.PermaTicker)),
		},
		LastUpdated: ticker.LastUpdated,
	}

	// fix ticker
	asset.Ticker = strings.ReplaceAll(asset.Ticker, ".", "/")

	// cusips
	ticker.CUSIPs = strings.TrimSpace(ticker.CUSIPs)
	if ticker.CUSIPs != "" {
		asset.CUSIP = strings.Split(ticker.CUSIPs, " ")
	}

	// similar tickers
	ticker.RelatedTickers = strings.TrimSpace(ticker.RelatedTickers)
	if ticker.RelatedTickers != "" {
		asset.SimilarTickers = strings.Split(ticker.RelatedTickers, " ")
		for idx, val := range asset.SimilarTickers {
			asset.SimilarTickers[idx] = strings.ReplaceAll(val, ".", "/")
		}
	}

	// try and parse CIK from SC filings URL
	regex := regexp.MustCompile(`CIK=(\d+)`)
	ciks := regex.FindStringSubmatch(ticker.SECFilings)

	if len(ciks) == 2 {
		asset.CIK = ciks[1]
	}

	// if the asset is not active set the delisting date
	if !asset.Active {
		asset.DelistingDate = ticker.LastPriceDate.Format(time.RFC3339)
	}

	return asset
}

func (ticker *sharadarTicker) NormalizedExchange() data.Exchange {
	switch ticker.Exchange {
	case "BATS":
		return data.BATSExchange
	case "NASDAQ":
		return data.NasdaqExchange
	case "NMFQS":
		return data.NMFQSExchange
	case "NYSE":
		return data.NYSEExchange
	case "NYSEARCA":
		return data.ARCAExchange
	case "NYSEMKT":
		return data.NYSEMktExchange
	case "INDEX":
		return data.IndexExchange
	case "OTC":
		return data.OTCExchange
	case "AMEX":
		return data.NYSEMktExchange
	default:
		log.Panic().Str("Exchange", ticker.Exchange).Msg("Sharadar exchange is unknown")
		return data.UnknownExchange
	}
}

func (ticker *sharadarTicker) NormalizedCategory() data.AssetType {
	switch ticker.Category {
	case "ETF":
		return data.ETF
	case "CEF":
		return data.CEF
	case "ETD":
		return data.UnknownAsset
	case "CEF Warrant":
		return data.UnknownAsset
	case "CEF Preferred":
		return data.UnknownAsset
	case "ETN":
		return data.ETN
	case "UNIT":
		return data.UnknownAsset
	case "ETMF":
		return data.UnknownAsset
	case "IDX":
		return data.INDEX
	case "ADR Common Stock Primary Class":
		return data.ADRC
	case "ADR Common Stock":
		return data.ADRC
	case "ADR Preferred Stock":
		return data.UnknownAsset
	case "ADR Common Stock Secondary Class":
		return data.ADRC
	case "Canadian Common Stock":
		return data.UnknownAsset
	case "Canadian Common Stock Primary Class":
		return data.UnknownAsset
	case "Canadian Common Stock Warrant":
		return data.UnknownAsset
	case "Canadian Preferred Stock":
		return data.UnknownAsset
	case "Canadian Common Stock Secondary Class":
		return data.UnknownAsset
	case "Domestic Common Stock":
		return data.CommonStock
	case "Domestic Common Stock Secondary Class":
		return data.CommonStock
	case "Domestic Common Stock Primary Class":
		return data.CommonStock
	case "Domestic Common Stock Warrant":
		return data.UnknownAsset
	case "ADR Common Stock Warrant":
		return data.UnknownAsset
	case "Institutional Investor":
		return data.UnknownAsset
	case "Domestic Preferred Stock":
		return data.UnknownAsset
	default:
		log.Panic().Object("Sharadar", ticker).Str("Category", ticker.Category).Msg("unknown Sharadar category")
		return data.UnknownAsset
	}
}

func (ticker *sharadarTicker) MarshalZerologObject(e *zerolog.Event) {
	e.Str("Ticker", ticker.Ticker)
	e.Int64("PermaTicker", ticker.PermaTicker)
	e.Str("Name", ticker.Name)
	e.Str("Exchange", ticker.Exchange)
	e.Str("IsDelisted", ticker.IsDelisted)
	e.Str("Category", ticker.Category)
	e.Str("CUSIPs", ticker.CUSIPs) // space separated cusips
	e.Int64("SICCode", ticker.SICCode)
	e.Str("SICSector", ticker.SICSector)
	e.Str("SICIndustry", ticker.SICIndustry)
	e.Str("FAMASector", ticker.FAMASector)
	e.Str("FAMAIndustry", ticker.FAMAIndustry)
	e.Str("Sector", ticker.Sector)
	e.Str("Industry", ticker.Industry)
	e.Str("ScaleMarketcap", ticker.ScaleMarketcap)
	e.Str("ScaleRevenue", ticker.ScaleRevenue)
	e.Str("RelatedTickers", ticker.RelatedTickers) // space separated related tickers
	e.Str("Currency", ticker.Currency)
	e.Str("Location", ticker.Location)
	e.Time("LastUpdated", ticker.LastUpdated)
	e.Time("FirstAdded", ticker.FirstAdded)
	e.Time("FirstPriceDate", ticker.FirstPriceDate)
	e.Time("LastPriceDate", ticker.LastPriceDate)
	e.Str("FirstQuarter", ticker.FirstQuarter)
	e.Str("LastQuarter", ticker.LastQuarter)
	e.Str("SECFilings", ticker.SECFilings)
	e.Str("CompanySite", ticker.CompanySite)
}
