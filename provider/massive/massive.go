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
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/go-resty/resty/v2"
	"github.com/goccy/go-json"
	"github.com/jackc/pgx/v5"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/figi"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
)

func init() {
	provider.Register("massive", &Massive{})
}

var (
	ErrInvalidStatusCode = errors.New("invalid status code received")
	massiveExchangeMap   = map[string]data.Exchange{
		"XNAS": data.NasdaqExchange,
		"BATS": data.BATSExchange,
		"XASE": data.NYSEMktExchange,
		"XNYS": data.NYSEExchange,
	}
)

type Massive struct {
}

func (massive *Massive) Name() string {
	return "massive"
}

func (massive *Massive) ConfigDescription() map[string]string {
	return map[string]string{
		"apiKey":    "Enter your Massive API key:",
		"rateLimit": "What is the maximum number of requests per minute?",
		"filer":     "Where should logos and icons be saved? (e.g. file:///path/)",
	}
}

func (massive *Massive) Description() string {
	return `The Massive Stocks API provides REST endpoints that let you query the latest market data from all US stock exchanges. You can also find data on company financials, stock market holidays, corporate actions, and more.`
}

func (massive *Massive) Datasets() map[string]provider.Dataset {
	return map[string]provider.Dataset{
		"Market Holidays": {
			Name:        "Market Holidays",
			Description: "Get upcoming market holidays and their open/close times.",
			DataTypes:   []*data.DataType{data.DataTypes[data.MarketHolidaysKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: downloadMassiveMarketHolidays,
		},

		"Stock Tickers": {
			Name:        "Stock Tickers",
			Description: "Details about tradeable stocks and ETFs.",
			DataTypes:   []*data.DataType{data.DataTypes[data.AssetKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(1949, 4, 19, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: downloadMassiveAssets,
		},
	}
}

// Private interfaces

type massiveHoliday struct {
	Date     string `json:"date"`
	Exchange string `json:"exchange"`
	Name     string `json:"name"`
	Open     string `json:"open"`
	Close    string `json:"close"`
	Status   string `json:"status"`
}

type massiveAssetFetcher struct {
	subscription *library.Subscription
	client       *resty.Client
	limiter      *rate.Limiter
	publishChan  chan<- *data.Observation
	numPublished int
	branding     *brandingBudget
}

type massiveResponse struct {
	Results   *json.RawMessage `json:"results"`
	Status    string           `json:"status"`
	RequestID string           `json:"request_id"`
	Count     int              `json:"count"`
	Next      string           `json:"next_url"`
}

type massiveAddress struct {
	Address1   string `json:"address1"`
	City       string `json:"city"`
	State      string `json:"state"`
	PostalCode string `json:"postal_code"`
}

type massiveBranding struct {
	LogoURL string `json:"logo_url"`
	IconURL string `json:"icon_url"`
}

type massiveStock struct {
	Ticker          string          `json:"ticker"`
	Name            string          `json:"name"`
	Description     string          `json:"description"`
	CompositeFIGI   string          `json:"composite_figi"`
	ShareClassFIGI  string          `json:"share_class_figi"`
	PrimaryExchange string          `json:"primary_exchange"`
	Type            string          `json:"type"`
	Active          bool            `json:"active"`
	CIK             string          `json:"cik"`
	SIC             string          `json:"sic_code"`
	CorporateURL    string          `json:"homepage_url"`
	ListDate        string          `json:"list_date"`
	DelistDate      string          `json:"delisted_utc"`
	Branding        massiveBranding `json:"branding"`
	Address         massiveAddress  `json:"address"`
	LastUpdated     string          `json:"last_updated_utc"`
}

func downloadMassiveAssets(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
	logger := zerolog.Ctx(ctx)

	runSummary := data.RunSummary{
		StartTime:        time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}

	// get a list of all active assets
	assets := make([]*data.Asset, 0, 6000)

	api := &massiveAssetFetcher{
		subscription: subscription,
		publishChan:  out,
		branding:     NewBrandingBudget(maxIconLogoFetchesPerRun),
	}

	defer func() {
		runSummary.EndTime = time.Now()

		runSummary.NumObservations = api.numPublished
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

	api.client = resty.New().SetQueryParam("apiKey", subscription.Config["apiKey"])
	api.limiter = rate.NewLimiter(rate.Limit(float64(rateLimit)/float64(61)), 1)

	for _, assetType := range []string{"CS", "ADRC", "ETF"} {
		if tmpAssets, err := api.assets(ctx, assetType); err != nil {
			logger.Error().Err(err).Str("AssetType", assetType).Msg("error getting ticker information")

			runSummary.Status = data.RunFailed

			return
		} else {
			assets = append(assets, tmpAssets...)
		}
	}

	logger.Info().Int("Count", len(assets)).Msg("got assets from massive")

	// Apply ticker/FIGI filter if set
	tickerFilter, figiFilter := provider.SecurityFilterFromContext(ctx)
	if tickerFilter != "" || figiFilter != "" {
		var filtered []*data.Asset

		for _, asset := range assets {
			if tickerFilter != "" && strings.EqualFold(asset.Ticker, tickerFilter) {
				filtered = append(filtered, asset)
			} else if figiFilter != "" && asset.CompositeFigi == figiFilter {
				filtered = append(filtered, asset)
			}
		}

		if len(filtered) == 0 {
			candidates := make([]string, 0, len(assets))
			for _, asset := range assets {
				if tickerFilter != "" {
					candidates = append(candidates, asset.Ticker)
				} else {
					candidates = append(candidates, asset.CompositeFigi)
				}
			}

			input := tickerFilter
			if input == "" {
				input = figiFilter
			}

			suggestions := provider.SuggestMatch(input, candidates)
			if len(suggestions) > 0 {
				log.Error().Str("input", input).Strs("suggestions", suggestions).Msg("security not found in Massive universe; did you mean one of these?")
			} else {
				log.Error().Str("input", input).Msg("security not found in Massive universe")
			}

			runSummary.Status = data.RunFailed

			return
		}

		assets = filtered

		log.Info().Int("filtered_assets", len(filtered)).Msg("applied security filter")
	}

	// remove any assets that haven't been updated since our last
	// look
	assetDetail, err := api.filterAssetsByLastUpdated(ctx, assets)
	if err != nil {
		// logged by caller
		runSummary.Status = data.RunFailed

		return
	}

	// fetch asset details
	logger.Info().Int("NumToQueryDetailsFor", len(assetDetail)).Msg("querying massive for asset details")

	api.assetDetails(ctx, assetDetail)

	// get delisting date for inactive assets
	err = api.delistedAssets(ctx, assets)
	if err != nil {
		// logged by caller
		runSummary.Status = data.RunFailed

		return
	}
}

func downloadMassiveMarketHolidays(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
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
		log.Info().Str("provider", "massive").Str("dataset", "Market Holidays").Msg("ticker/FIGI filtering not applicable to this dataset, skipping")
		return
	}

	rateLimit, err := strconv.Atoi(subscription.Config["rateLimit"])
	if err != nil {
		logger.Error().Err(err).Str("configRateLimit", subscription.Config["rateLimit"]).Msg("could not convert rateLimit configuration parameter to an integer")

		runSummary.Status = data.RunFailed

		return
	}

	if rateLimit <= 0 {
		rateLimit = 5000
	}

	client := resty.New().SetQueryParam("apiKey", subscription.Config["apiKey"])
	limiter := rate.NewLimiter(rate.Limit(float64(rateLimit)/float64(61)), 1)

	// get nyc timezone
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		logger.Panic().Err(err).Msg("could not load timezone")
		return
	}

	// fetch upcoming market holidays
	if err := limiter.Wait(ctx); err != nil {
		log.Panic().Err(err).Msg("rate limit wait failed")
	}

	respContent := make([]*massiveHoliday, 0)

	resp, err := client.R().
		SetResult(&respContent).
		Get("https://api.massive.com/v1/marketstatus/upcoming")
	if err != nil {
		logger.Error().Err(err).Msg("resty returned an error when querying reference/tickers")

		runSummary.Status = data.RunFailed

		return
	}

	if resp.StatusCode() >= 300 {
		logger.Error().Int("StatusCode", resp.StatusCode()).Msg("massive returned an invalid HTTP response")

		runSummary.Status = data.RunFailed

		return
	}

	for _, holiday := range respContent {
		massiveDate, err := time.Parse("2006-01-02", holiday.Date)
		if err != nil {
			logger.Error().Err(err).Str("massiveDate", holiday.Date).Msg("could not parse date from massive object")
			continue
		}

		eventDate := time.Date(massiveDate.Year(), massiveDate.Month(), massiveDate.Day(), 9, 30, 0, 0, nyc)

		closeTime := time.Date(massiveDate.Year(), massiveDate.Month(), massiveDate.Day(), 16, 0, 0, 0, nyc)
		if holiday.Close != "" {
			closeTime, err = time.Parse(time.RFC3339Nano, holiday.Close)
			if err != nil {
				logger.Error().Err(err).Str("massiveClose", holiday.Close).Msg("could not parse close date from massive object")

				runSummary.Status = data.RunFailed

				return
			}

			closeTime = closeTime.In(nyc)
		}

		marketHoliday := &data.MarketHoliday{
			Name:       holiday.Name,
			EventDate:  eventDate,
			Market:     holiday.Exchange,
			EarlyClose: holiday.Status == "early-close",
			CloseTime:  closeTime,
		}

		out <- &data.Observation{
			MarketHoliday:    marketHoliday,
			ObservationDate:  time.Now(),
			SubscriptionID:   subscription.ID,
			SubscriptionName: subscription.Name,
		}

		numObs++
	}
}

func (api *massiveAssetFetcher) publish(asset *data.Asset) {
	if asset.CompositeFigi == "" {
		figi.Enrich(asset)
	}

	if asset.CompositeFigi == "" {
		return
	}

	// make a copy of the asset and fix ticker to match pv-data standard
	// e.g. BRK.A -> BRK/A
	asset2 := *asset
	asset2.Ticker = massiveTicker2PvTicker(asset2.Ticker)

	api.publishChan <- &data.Observation{
		AssetObject:      &asset2,
		ObservationDate:  time.Now(),
		SubscriptionID:   api.subscription.ID,
		SubscriptionName: api.subscription.Name,
	}

	api.numPublished++
}

func (api *massiveAssetFetcher) assets(ctx context.Context, assetType string) ([]*data.Asset, error) {
	logger := zerolog.Ctx(ctx)

	var respContent massiveResponse

	assets := make([]*data.Asset, 0, 6000)

	// first we query the reference endpoint which is faster than the details endpoint
	// this gives us a list of all assets we should query details for
	// NOTE: results are limited to stocks

	// maxQueries is a protective measure to make sure we don't get into
	// an infinite loop
	maxQueries := 1000

	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		logger.Panic().Err(err).Msg("could not load timezone")
		return []*data.Asset{}, err
	}

	if err := api.limiter.Wait(ctx); err != nil {
		log.Panic().Err(err).Msg("rate limit wait failed")
	}

	resp, err := api.client.R().
		SetQueryParam("market", "stocks").
		SetQueryParam("active", "true").
		SetQueryParam("type", assetType).
		SetQueryParam("limit", "1000").
		SetResult(&respContent).
		Get("https://api.massive.com/v3/reference/tickers")
	if err != nil {
		logger.Error().Err(err).Msg("resty returned an error when querying reference/tickers")
		return assets, err
	}

	for ii := 0; ii < maxQueries; ii++ {
		if resp.StatusCode() >= 300 {
			logger.Error().Int("StatusCode", resp.StatusCode()).Str("ResponseBody", string(resp.Body())).
				Str("URL", "https://api.massive.com/v3/reference/tickers").
				Msg("received an invalid status code when querying massive reference/tickers endpoint")

			return assets, fmt.Errorf("%w (%d): %s", ErrInvalidStatusCode, resp.StatusCode(), string(resp.Body()))
		}

		// de-serealize stock content
		massiveTickers := make([]*massiveStock, 0, 1000)
		if err := json.Unmarshal(*respContent.Results, &massiveTickers); err != nil {
			log.Error().Err(err).Msg("could not unmarshal response of massive tickers")
			return nil, err
		}

		logger.Debug().Int("ReceivedNAssets", len(massiveTickers)).Str("AssetType", assetType).Msg("got tickers")

		for _, ticker := range massiveTickers {
			lastUpdated, err := time.Parse(time.RFC3339, ticker.LastUpdated)
			if err != nil {
				logger.Error().Err(err).Str("Ticker", ticker.Ticker).Msg("could not parse last updated string for tickers")
				continue
			}

			lastUpdated = lastUpdated.In(nyc)

			massiveAsset := &data.Asset{
				Ticker:          ticker.Ticker,
				Name:            ticker.Name,
				CompositeFigi:   ticker.CompositeFIGI,
				ShareClassFigi:  ticker.ShareClassFIGI,
				PrimaryExchange: massiveExchangeMap[ticker.PrimaryExchange],
				AssetType:       data.AssetType(ticker.Type),
				LastUpdated:     lastUpdated,
				CIK:             ticker.CIK,
			}

			assets = append(assets, massiveAsset)
		}

		// check if all results have been returned
		if respContent.Next == "" {
			break
		}

		// get next result
		next := respContent.Next
		respContent.Next = ""

		logger.Debug().Str("Next", next).Str("AssetType", assetType).Int("ii", ii).Msg("making next query")

		if err := api.limiter.Wait(ctx); err != nil {
			log.Panic().Err(err).Msg("rate limit wait failed")
		}

		resp, err = api.client.R().
			SetResult(&respContent).
			Get(next)
		if err != nil {
			logger.Error().Err(err).Msg("resty returned an error when querying reference/tickers")
			return assets, err
		}
	}

	return assets, nil
}

func (api *massiveAssetFetcher) filterAssetsByLastUpdated(ctx context.Context, assets []*data.Asset) ([]*data.Asset, error) {
	logger := zerolog.Ctx(ctx)

	assetDetail := make([]*data.Asset, 0, len(assets))
	assetUpdate := make([]*data.Asset, 0, len(assets))

	// Use the pool directly so each query auto-acquires and releases
	// instead of pinning one connection across thousands of per-asset
	// SELECTs. The previous version held a conn for the whole loop and
	// helped saturate the pgxpool.
	pool := api.subscription.Library.Pool

	// Enrich any assets that have no figi
	toEnrich := make([]*data.Asset, 0, len(assets)/2)
	for _, asset := range assets {
		if asset.CompositeFigi == "" {
			toEnrich = append(toEnrich, asset)
		}
	}

	log.Debug().Int("NumAssetsToEnrich", len(toEnrich)).Msg("Enriching assets with FIGI")
	figi.Enrich(toEnrich...)

	// for each asset determine if details need to be queried
	for _, asset := range assets {
		var lastUpdated time.Time

		if asset.CompositeFigi == "" {
			log.Warn().Str("Ticker", asset.Ticker).Str("Name", asset.Name).Msg("skipping ticker due to unknown figi")
			continue
		}

		sql := fmt.Sprintf("SELECT COALESCE(last_updated, '0001-01-01'::timestamp) as last_updated FROM %s WHERE composite_figi=$1 AND ticker=$2 LIMIT 1", api.subscription.DataTablesMap[data.AssetKey])

		err := pool.QueryRow(
			ctx,
			sql,
			asset.CompositeFigi,
			massiveTicker2PvTicker(asset.Ticker),
		).Scan(&lastUpdated)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				assetDetail = append(assetDetail, asset)
				continue
			}

			logger.Error().Err(err).Str("SQL", sql).Str("CompositeFIGI", asset.CompositeFigi).Str("Ticker", asset.Ticker).Msg("error when querying database for asset")

			return nil, err
		}

		if lastUpdated.Before(asset.LastUpdated) {
			assetUpdate = append(assetUpdate, asset)
		}
	}

	// sort assetUpdate by lastupdated
	slices.SortFunc(assetUpdate, func(a, b *data.Asset) int {
		switch {
		case a.LastUpdated.Before(b.LastUpdated):
			return -1
		case a.LastUpdated.Equal(b.LastUpdated):
			return 0
		default:
			return 1
		}
	})

	// limit updates to a max of 100 assets
	assetUpdateLen := len(assetUpdate)
	numAssetsToUpdate := int(math.Min(float64(assetUpdateLen), 100))

	if numAssetsToUpdate > 0 {
		assetDetail = append(assetDetail, assetUpdate[:numAssetsToUpdate]...)
	}

	// Missing-branding lane: surface DB assets whose icon_url /
	// logo_url is NULL — i.e., we never managed to upload binaries
	// for them. Limit per run so first runs don't drown the API.
	apiAssetMap := make(map[string]struct{}, len(assets))
	for _, a := range assets {
		apiAssetMap[fmt.Sprintf("%s:%s", massiveTicker2PvTicker(a.Ticker), a.CompositeFigi)] = struct{}{}
	}

	missingSQL := fmt.Sprintf(`SELECT
			ticker, composite_figi, share_class_figi, primary_exchange,
			asset_type, active, name, description, corporate_url, sector,
			industry, sic_code, cik, cusips, isins, other_identifiers,
			similar_tickers, tags,
			coalesce(to_char(listed, 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '') as listed,
			coalesce(to_char(delisted, 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '') as delisted,
			last_updated
		FROM %s
		WHERE active = true
		  AND (icon_url IS NULL OR logo_url IS NULL)
		LIMIT %d`, api.subscription.DataTablesMap[data.AssetKey], maxMissingBrandingPerRun*2)

	missingRows, missingErr := pool.Query(ctx, missingSQL)
	if missingErr != nil {
		logger.Warn().Err(missingErr).Msg("could not query missing-branding assets; skipping lane")
	} else {
		var dbMissing []*data.Asset
		if scanErr := pgxscan.ScanAll(&dbMissing, missingRows); scanErr != nil {
			logger.Warn().Err(scanErr).Msg("could not scan missing-branding assets; skipping lane")
		} else {
			added := 0

			for _, m := range dbMissing {
				if added >= maxMissingBrandingPerRun {
					break
				}

				key := fmt.Sprintf("%s:%s", m.Ticker, m.CompositeFigi)
				if _, present := apiAssetMap[key]; !present {
					continue // delisted or no longer in Massive
				}

				assetDetail = append(assetDetail, m)
				added++
			}

			if added > 0 {
				logger.Info().Int("AddedMissingBranding", added).Msg("queued missing-branding assets for refresh")
			}
		}
	}

	return assetDetail, nil
}

func (api *massiveAssetFetcher) delistedAssets(ctx context.Context, assets []*data.Asset) error {
	logger := zerolog.Ctx(ctx)

	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		logger.Error().Err(err).Msg("could not load timezone")
		return err
	}

	// Single-shot query against the pool — no need to hold a conn
	// across the post-processing logic below.
	pool := api.subscription.Library.Pool

	assetMap := make(map[string]*data.Asset, len(assets))
	for _, asset := range assets {
		assetMap[asset.ID()] = asset
	}

	// get a list of assets that are currently active in the database
	inactive := make([]*data.Asset, 0, 50)

	rows, err := pool.Query(ctx, fmt.Sprintf(`SELECT
		ticker,
		composite_figi,
		share_class_figi,
		primary_exchange,
		asset_type,
		active,
		name,
		description,
		corporate_url,
		sector,
		industry,
		sic_code,
		cik,
		cusips,
		isins,
		other_identifiers,
		similar_tickers,
		tags,
		coalesce(to_char(listed, 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '') as listed,
		coalesce(to_char(delisted, 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '') as delisted,
		last_updated
	FROM %s WHERE active=true`, api.subscription.DataTablesMap[data.AssetKey]))
	if err != nil {
		logger.Error().Err(err).Msg("error when querying database for active tickers")
		return err
	}

	var dbActiveAssets []*data.Asset

	err = pgxscan.ScanAll(&dbActiveAssets, rows)
	if err != nil {
		logger.Error().Err(err).Msg("error when scanning values into dbActiveAssets")
	}

	// for all active database assets that are not in the response
	// of active assets in massive, add to the potentially inactive list
	for _, asset := range dbActiveAssets {
		if _, ok := assetMap[asset.ID()]; !ok {
			inactive = append(inactive, asset)
		}
	}

	if len(inactive) == 0 {
		// no inactive assets to consider
		return nil
	}

	// build a lookup map of potential inactive assets
	inactiveMap := make(map[string]*data.Asset, len(inactive))
	for _, asset := range inactive {
		log.Info().Str("InactivePossible", asset.ID()).Send()
		inactiveMap[asset.ID()] = asset
	}

	deactivated := make(map[string]*data.Asset, len(inactiveMap))

	for _, assetType := range []string{"CS", "ADRC", "ETF"} {
		// query massive for inactive assets
		var respContent massiveResponse

		if err := api.limiter.Wait(ctx); err != nil {
			log.Panic().Err(err).Msg("rate limit failed")
		}

		resp, err := api.client.R().
			SetQueryParam("active", "false").
			SetQueryParam("sort", "last_updated_utc").
			SetQueryParam("order", "desc").
			SetQueryParam("limit", "1000").
			SetQueryParam("type", assetType).
			SetResult(&respContent).
			Get("https://api.massive.com/v3/reference/tickers")
		if err != nil {
			logger.Error().Err(err).Msg("error when retrieving inactive assets")
		}

		// limit the number of queries as a safety precaution to ensure
		// that we are not in an infinite loop
		maxQueries := 300
		updatedCount := 0

		for ii := 0; ii < maxQueries; ii++ {
			if resp.StatusCode() >= 300 {
				logger.Error().Int("StatusCode", resp.StatusCode()).Str("ResponseBody", string(resp.Body())).
					Str("URL", "https://api.massive.com/v3/reference/tickers").
					Msg("received an invalid status code when querying massive reference/tickers endpoint")

				return fmt.Errorf("%w (%d): %s", ErrInvalidStatusCode, resp.StatusCode(), string(resp.Body()))
			}

			// de-serealize stock content
			massiveAssets := make([]*massiveStock, 0, 1000)
			if err := json.Unmarshal(*respContent.Results, &massiveAssets); err != nil {
				log.Error().Err(err).Msg("json unmarshal of massive assets failed")
				return err
			}

			logger.Debug().Int("ReceivedNAssets", len(massiveAssets)).Msg("got inactive tickers")

			for _, massiveAsset := range massiveAssets {
				lastUpdated, err := time.Parse(time.RFC3339, massiveAsset.LastUpdated)
				if err != nil {
					logger.Error().Err(err).Str("Ticker", massiveAsset.Ticker).Msg("could not parse last updated string for tickers")
				}

				lastUpdated = lastUpdated.In(nyc)

				asset := data.Asset{
					Ticker:        massiveAsset.Ticker,
					CompositeFigi: massiveAsset.CompositeFIGI,
				}

				// lookup the completely filled out asset and update its values
				// publish the updated asset
				if inactiveAsset, ok := inactiveMap[asset.ID()]; ok {
					inactiveAsset.DelistingDate = strings.Split(massiveAsset.DelistDate, "T")[0]
					inactiveAsset.LastUpdated = lastUpdated
					inactiveAsset.Active = false
					deactivated[asset.ID()] = inactiveAsset
					api.publish(inactiveAsset)

					updatedCount++
				}
			}

			// check if all results have been returned
			if respContent.Next == "" || updatedCount >= len(inactive) {
				break
			}

			// get next result
			next := respContent.Next
			respContent.Next = ""

			logger.Debug().Str("Next", next).Int("ii", ii).Msg("making next query")

			if err := api.limiter.Wait(ctx); err != nil {
				log.Panic().Err(err).Msg("rate limit failed")
			}

			resp, err = api.client.R().
				SetResult(&respContent).
				Get(next)
			if err != nil {
				logger.Error().Err(err).Msg("resty returned an error when querying reference/tickers")
				return err
			}
		}
	}

	// find the disjoint set of Assets that are possibly inactive and those
	// that were deactivated. Assets can only appear in the aforementioned set
	// for a limited period of time before we mark them as inactive
	for _, possibleInactiveAsset := range inactiveMap {
		if _, ok := deactivated[possibleInactiveAsset.ID()]; !ok {
			// asset was not de-activated ... check to see how old it is
			timeSinceLastUpdate := time.Since(possibleInactiveAsset.LastUpdated)
			if timeSinceLastUpdate > 14*24*time.Hour {
				// if asset hasn't been updated in the last 14 days mark as
				// inactive
				possibleInactiveAsset.LastUpdated = time.Now().In(nyc)
				possibleInactiveAsset.Active = false
				api.publish(possibleInactiveAsset)
			}
		}
	}

	return nil
}

func (api *massiveAssetFetcher) assetDetails(ctx context.Context, assets []*data.Asset) {
	logger := zerolog.Ctx(ctx)

	sometimes := rate.Sometimes{Interval: 60 * time.Second}
	started := time.Now()

	for idx, asset := range assets {
		fullAsset, err := api.assetDetail(ctx, asset)
		if err != nil {
			logger.Error().Err(err).Msg("received an error when querying massive details")
			continue
		}

		api.publish(fullAsset)

		sometimes.Do(func() {
			secondsPerItem := time.Since(started) / time.Duration(idx+1)
			timeLeft := secondsPerItem * time.Duration(len(assets)-idx)
			logger.Info().Int("Completed", idx+1).Str("SinceStarted", time.Since(started).Round(time.Second).String()).Int("NumAssetsLeft", len(assets)-idx).Str("secondsPerItem", secondsPerItem.Round(time.Second).String()).Str("ETA", timeLeft.Round(time.Second).String()).Msg("asset detail progress")
		})
	}
}

func (api *massiveAssetFetcher) assetDetail(ctx context.Context, asset *data.Asset) (*data.Asset, error) {
	var respContent massiveResponse

	logger := zerolog.Ctx(ctx)
	detailsURL := fmt.Sprintf("https://api.massive.com/v3/reference/tickers/%s", asset.Ticker)

	if err := api.limiter.Wait(ctx); err != nil {
		log.Panic().Err(err).Msg("rate limit failed")
	}

	resp, err := api.client.R().
		SetResult(&respContent).
		Get(detailsURL)
	if err != nil {
		logger.Error().Err(err).Msg("resty returned an error when querying v3/reference/tickers details")
		return nil, err
	}

	if resp.StatusCode() >= 300 {
		logger.Error().Int("StatusCode", resp.StatusCode()).Str("ResponseBody", string(resp.Body())).
			Str("URL", detailsURL).
			Msg("received an invalid status code when querying massive reference/tickers details endpoint")

		return nil, fmt.Errorf("%w (%d): %s", ErrInvalidStatusCode, resp.StatusCode(), string(resp.Body()))
	}

	// de-serealize stock content
	var massiveAsset massiveStock

	err = json.Unmarshal(*respContent.Results, &massiveAsset)
	if err != nil {
		logger.Error().Err(err).Msg("error when unmarshalling json from details response")
		return nil, err
	}

	location := ""
	if massiveAsset.Address.City != "" {
		location = fmt.Sprintf("%s, %s", massiveAsset.Address.City, massiveAsset.Address.State)
	}

	sicCode, err := strconv.Atoi(massiveAsset.SIC)
	if err != nil {
		sicCode = 0
	}

	// fetch icon and logo
	var (
		icon         []byte
		iconMimeType string
	)

	if massiveAsset.Branding.IconURL != "" && api.branding.Allow() {
		if err := api.limiter.Wait(ctx); err != nil {
			log.Panic().Err(err).Msg("rate limit failed")
		}

		resp, err := api.client.R().Get(massiveAsset.Branding.IconURL)
		if err != nil {
			logger.Error().Err(err).Msg("error when fetching asset icon")
			return nil, err
		}

		icon = resp.Body()
		iconMimeType = resp.Header().Get("Content-Type")
	}

	var (
		logo         []byte
		logoMimeType string
	)

	if massiveAsset.Branding.LogoURL != "" && api.branding.Allow() {
		if err := api.limiter.Wait(ctx); err != nil {
			log.Panic().Err(err).Msg("rate limit failed")
		}

		resp, err := api.client.R().Get(massiveAsset.Branding.LogoURL)
		if err != nil {
			logger.Error().Err(err).Msg("error when fetching asset logo")
			return nil, err
		}

		logo = resp.Body()
		logoMimeType = resp.Header().Get("Content-Type")
	}

	// build Asset object
	assetDetail := &data.Asset{
		Ticker:               massiveAsset.Ticker,
		CompositeFigi:        massiveAsset.CompositeFIGI,
		ShareClassFigi:       massiveAsset.ShareClassFIGI,
		Name:                 massiveAsset.Name,
		Description:          massiveAsset.Description,
		Active:               massiveAsset.Active,
		PrimaryExchange:      massiveExchangeMap[massiveAsset.PrimaryExchange],
		AssetType:            data.AssetType(massiveAsset.Type),
		HeadquartersLocation: location,
		CIK:                  massiveAsset.CIK,
		SIC:                  &sicCode,
		CorporateUrl:         massiveAsset.CorporateURL,
		Icon:                 icon,
		IconMimeType:         iconMimeType,
		Logo:                 logo,
		LogoMimeType:         logoMimeType,
		ListingDate:          massiveAsset.ListDate,
		LastUpdated:          asset.LastUpdated,
	}

	return assetDetail, nil
}

func massiveTicker2PvTicker(ticker string) string {
	return strings.ReplaceAll(ticker, ".", "/")
}

const maxIconLogoFetchesPerRun = 100
const maxMissingBrandingPerRun = 100

// brandingBudget caps how many icon/logo HTTP fetches a single
// run performs. A non-positive cap means unlimited. NOT
// thread-safe: a Massive run is sequential by construction
// (downloadMassiveAssets -> assetDetails iterates one goroutine);
// any reuse across goroutines must add external synchronisation.
type brandingBudget struct {
	limit     int
	remaining int
}

// NewBrandingBudget returns a budget allowing up to limit Allow()
// calls. limit <= 0 disables the cap.
func NewBrandingBudget(limit int) *brandingBudget {
	return &brandingBudget{limit: limit, remaining: limit}
}

// Allow consumes one slot and returns true if the request is allowed
// under the cap.
func (b *brandingBudget) Allow() bool {
	if b == nil || b.limit <= 0 {
		return true
	}

	if b.remaining <= 0 {
		return false
	}

	b.remaining--

	return true
}
