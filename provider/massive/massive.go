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
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/go-resty/resty/v2"
	"github.com/goccy/go-json"
	"github.com/jackc/pgx/v5"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/figi"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/permid"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
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

// ConfigDescription returns provider-level prompts shared by every
// Massive dataset. There are none today: the REST credentials apply
// only to REST-using datasets, the S3 keys apply only to flat-file
// datasets, and asset-binary settings apply only to Stock Tickers.
// Each dataset declares the keys it actually needs in its Dataset
// definition below.
func (massive *Massive) ConfigDescription() map[string]string {
	return map[string]string{}
}

func (massive *Massive) Description() string {
	return `The Massive Stocks API provides REST endpoints that let you query the latest market data from all US stock exchanges. You can also find data on company financials, stock market holidays, corporate actions, and more.`
}

func (massive *Massive) Datasets() map[string]provider.Dataset {
	restAPIPrompts := map[string]string{
		"apiKey":    "Enter your Massive API key:",
		"rateLimit": "What is the maximum number of requests per minute?",
	}

	flatFilesPrompts := map[string]string{
		"flatFilesAccessKey": "S3 access key id for files.massive.com:",
		"flatFilesSecretKey": "S3 secret access key for files.massive.com:",
	}

	return map[string]provider.Dataset{
		"Market Holidays": {
			Name:        "Market Holidays",
			Description: "Get upcoming market holidays and their open/close times.",
			DataTypes:   []*data.DataType{data.DataTypes[data.MarketHolidaysKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			ConfigDescription: restAPIPrompts,
			Fetch:             downloadMassiveMarketHolidays,
		},

		"Stock Tickers": {
			Name:        "Stock Tickers",
			Description: "Details about tradeable stocks and ETFs.",
			DataTypes:   []*data.DataType{data.DataTypes[data.AssetKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(1949, 4, 19, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			ConfigDescription: mergeConfigDescriptions(restAPIPrompts, map[string]string{
				"filer":         "Where should logos and icons be saved? (e.g. file:///path/)",
				"iconLogoLimit": "Max icon/logo fetches per run (blank = 100, 0 = unlimited):",
			}),
			Fetch: downloadMassiveAssets,
		},

		"EOD": {
			Name:        "EOD",
			Description: "End-of-day OHLCV pulled from the S3 flat-files endpoint, enriched with splits and dividends from the REST API.",
			DataTypes:   []*data.DataType{data.DataTypes[data.EODKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2003, 9, 10, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			ConfigDescription: mergeConfigDescriptions(restAPIPrompts, flatFilesPrompts),
			PostFetch:         []provider.PostFetchHook{provider.AdjustEodPrices},
			Fetch:             downloadMassiveEOD,
		},

		"1-Minute Bars": {
			Name:        "1-Minute Bars",
			Description: "1-minute OHLCV bars pulled from the S3 flat-files endpoint. Includes pre-market and after-hours rows. Stored in ClickHouse via the IntradayKey backend.",
			DataTypes:   []*data.DataType{data.DataTypes[data.IntradayKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2003, 9, 10, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			ConfigDescription: flatFilesPrompts,
			Fetch:             downloadMassiveMinute,
		},

		"1-Minute Bars (Live)": {
			Name:        "1-Minute Bars (Live)",
			Description: "Live 1-minute OHLCV bars streamed from the Massive websocket. One session per weekday: connects in the early morning and disconnects at 20:35 America/New_York. Stored in ClickHouse via the IntradayKey backend; the daily flat-files job is the source of truth and dedupes via ReplacingMergeTree.",
			DataTypes:   []*data.DataType{data.DataTypes[data.IntradayKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2003, 9, 10, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			ConfigDescription: map[string]string{
				"apiKey": "Enter your Massive API key:",
				"feed":   "Which websocket feed? (real-time or delayed)",
			},
			Fetch: downloadMassiveMinuteLive,
		},
	}
}

// newMassiveRESTClient builds a resty client configured for the
// long-running paginated workloads we run against api.massive.com.
// HTTP/2 GOAWAY frames - sent by the server after extended idle or
// when shedding load - surface as transport errors that the default
// resty client treats as fatal, aborting long backfills. Exponential-
// backoff retry on transport errors and 5xx responses recovers
// transparently in those cases.
func newMassiveRESTClient(apiKey string) *resty.Client {
	return resty.New().
		SetQueryParam("apiKey", apiKey).
		SetRetryCount(5).
		SetRetryWaitTime(2 * time.Second).
		SetRetryMaxWaitTime(30 * time.Second).
		AddRetryCondition(func(resp *resty.Response, err error) bool {
			if err != nil {
				return true
			}

			return resp.StatusCode() >= 500
		})
}

// mergeConfigDescriptions returns a new map containing every key/value
// from the given maps. Later maps win on key collisions. Used to compose
// dataset ConfigDescriptions out of small reusable groups (REST creds,
// flat-files creds, etc.) without sharing mutable state.
func mergeConfigDescriptions(parts ...map[string]string) map[string]string {
	out := make(map[string]string)

	for _, m := range parts {
		maps.Copy(out, m)
	}

	return out
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
	subscription       *library.Subscription
	client             *resty.Client
	limiter            *rate.Limiter
	publishChan        chan<- *data.Observation
	numPublished       atomic.Int64
	branding           *brandingBudget
	missingBrandingCap int
	cooldown           globalBackoff

	// walkWindowsByFigi and walkWindowsByCIK record the (firstSeen,
	// lastSeen) pair for every asset observed during the historical
	// walk. Two indexes because Massive's list response sometimes
	// omits composite_figi while filling it in on the per-ticker
	// details endpoint — keying only on ticker:figi would miss that
	// case. The CIK index handles it: assets without a list-time
	// FIGI but with a CIK still get a window we can find later
	// using the FIGI assetDetail eventually returned alongside the
	// same CIK.
	//
	// Both maps are populated once by the walk under a local mutex,
	// then published here after g.Wait(); reads from assetDetail
	// post-processing are spawned after the publication so no
	// further synchronization is needed. nil before / outside a
	// historical walk.
	//
	// walkStart/walkEnd record the [start, end) window so the
	// derived-date cutoff math is available alongside the per-asset
	// windows.
	walkWindowsByFigi map[string]walkWindow
	walkWindowsByCIK  map[string]walkWindow
	walkStart         time.Time
	walkEnd           time.Time
}

// walkWindow holds the first and last business day on which an asset
// appeared in the historical-walk universe. Drives walk-derived
// listing/delisting date fallback in assetDetail.
type walkWindow struct {
	firstSeen time.Time
	lastSeen  time.Time
}

// globalBackoff coordinates a fleet-wide pause when the upstream server
// starts shedding (HTTP/2 GOAWAY, connection reset by peer, etc.). The
// failing worker calls Trip with a duration; every other worker sees
// the cooldown on its next Wait and sleeps until it expires. The first
// trip wins — subsequent trips do not extend a cooldown already in
// flight — so a single storm produces a single recovery window, not a
// growing one.
type globalBackoff struct {
	mu    sync.Mutex
	until time.Time
}

// Wait blocks until the cooldown clears, or returns ctx's error if
// the context is cancelled first. Returns immediately when no
// cooldown is set or the cooldown has already expired.
func (b *globalBackoff) Wait(ctx context.Context) error {
	b.mu.Lock()
	until := b.until
	b.mu.Unlock()

	if until.IsZero() {
		return nil
	}

	d := time.Until(until)
	if d <= 0 {
		return nil
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Trip installs a fleet-wide cooldown of d (no-op when a longer
// cooldown is already in flight). Workers blocked in Wait will resume
// once the cooldown elapses.
func (b *globalBackoff) Trip(d time.Duration) {
	b.mu.Lock()
	defer b.mu.Unlock()

	target := time.Now().Add(d)
	if target.After(b.until) {
		b.until = target
	}
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

	iconLogoLimit, missingBrandingCap, err := parseIconLogoLimit(subscription.Config["iconLogoLimit"])
	if err != nil {
		logger.Error().Err(err).Str("iconLogoLimit", subscription.Config["iconLogoLimit"]).Msg("could not convert iconLogoLimit configuration parameter to an integer")

		runSummary.Status = data.RunFailed

		return
	}

	api := &massiveAssetFetcher{
		subscription:       subscription,
		publishChan:        out,
		branding:           NewBrandingBudget(iconLogoLimit),
		missingBrandingCap: missingBrandingCap,
	}

	defer func() {
		runSummary.EndTime = time.Now()

		runSummary.NumObservations = int(api.numPublished.Load())
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
		rateLimit = defaultRateLimit
	}

	api.client = newMassiveRESTClient(subscription.Config["apiKey"])
	api.limiter = rate.NewLimiter(rate.Limit(float64(rateLimit)/float64(61)), 1)

	// Today's snapshot drives delisted-asset detection. It must reflect
	// the API's current active=true universe, not a unioned historical
	// view, otherwise tickers delisted during the lookback window would
	// appear "still active" to delistedAssets() and never get marked
	// inactive.
	var todayUniverse []*data.Asset

	for _, assetType := range []string{"CS", "ADRC", "ETF"} {
		tmpAssets, err := api.assets(ctx, assetType, time.Time{})
		if err != nil {
			logger.Error().Err(err).Str("AssetType", assetType).Msg("error getting ticker information")

			runSummary.Status = data.RunFailed

			return
		}

		todayUniverse = append(todayUniverse, tmpAssets...)
	}

	// Walk business days in the lookback window using the API's as-of
	// date parameter to discover tickers that were active during the
	// window but are not in today's snapshot (e.g., listed-then-delisted
	// within the window). Dedupe by Asset.ID(), keeping the most-recent
	// as-of-date payload per ticker. ValidFor is set so SaveDB's guard
	// can preserve the correct active/delisted state if a stale
	// observation lands after a fresher one.
	lookback := provider.LookbackFromContext(ctx, defaultAssetLookback)

	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		logger.Error().Err(err).Msg("could not load timezone")

		runSummary.Status = data.RunFailed

		return
	}

	// walkEndAnchor defaults to today's NYC midnight; --end-date
	// overrides for targeted historical backfills (e.g. walk only
	// Blockbuster's BBI window 2009-01-01 → 2010-12-31 without
	// re-walking everything between then and today).
	now := time.Now().In(nyc)
	walkEndAnchor := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, nyc)

	if endDate, ok := provider.EndDateFromContext(ctx); ok {
		walkEndAnchor = time.Date(endDate.Year(), endDate.Month(), endDate.Day(), 0, 0, 0, 0, nyc)
	}

	todayMidnight := walkEndAnchor
	startMidnight := walkEndAnchor.Add(-lookback)

	historicalMap := map[string]*data.Asset{}

	if err := api.walkHistoricalAssets(ctx, startMidnight, todayMidnight, []string{"CS", "ADRC", "ETF"}, historicalMap); err != nil {
		logger.Error().Err(err).Msg("error getting historical ticker information")

		runSummary.Status = data.RunFailed

		return
	}

	// Cross-check walk-discovered composites with OpenFIGI and collapse
	// (ticker, share_class) groups that the walk left with multiple
	// composites. Removes Massive's foreign-exchange substitutions that
	// would otherwise persist as duplicate XNAS rows.
	api.sanitizeWalkComposites(ctx, historicalMap)

	for id, asset := range historicalMap {
		if !traceAsset(ctx, asset.Ticker) {
			continue
		}

		logger.Info().
			Str("Stage", "post-sanitize-historicalMap").
			Str("ID", id).
			Str("Ticker", asset.Ticker).
			Str("CompositeFigi", asset.CompositeFigi).
			Str("ShareClassFigi", asset.ShareClassFigi).
			Str("CIK", asset.CIK).
			Str("Name", asset.Name).
			Time("ValidFor", asset.ValidFor).
			Msg("trace: historicalMap entry survived sanitize")
	}

	// Combined universe: today's snapshot plus lookback-only discoveries.
	todaySet := make(map[string]struct{}, len(todayUniverse))
	for _, a := range todayUniverse {
		todaySet[a.ID()] = struct{}{}
	}

	assets = append(assets, todayUniverse...)

	for id, a := range historicalMap {
		if _, present := todaySet[id]; !present {
			assets = append(assets, a)
		}
	}

	logger.Info().
		Int("TodayCount", len(todayUniverse)).
		Int("LookbackOnly", len(assets)-len(todayUniverse)).
		Dur("Lookback", lookback).
		Msg("got assets from massive")

	// Apply ticker/FIGI filter if set
	tickerFilter, figiFilter := provider.SecurityFilterFromContext(ctx)
	if tickerFilter != "" || figiFilter != "" {
		var (
			filtered      []*data.Asset
			filteredToday []*data.Asset
		)

		for _, asset := range assets {
			if tickerFilter != "" && strings.EqualFold(asset.Ticker, tickerFilter) {
				filtered = append(filtered, asset)
			} else if figiFilter != "" && asset.CompositeFigi == figiFilter {
				filtered = append(filtered, asset)
			}
		}

		for _, asset := range todayUniverse {
			if tickerFilter != "" && strings.EqualFold(asset.Ticker, tickerFilter) {
				filteredToday = append(filteredToday, asset)
			} else if figiFilter != "" && asset.CompositeFigi == figiFilter {
				filteredToday = append(filteredToday, asset)
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
		todayUniverse = filteredToday

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

	// get delisting date for inactive assets — operates on today's
	// snapshot only so the disjoint-set logic against active DB rows
	// is correct.
	err = api.delistedAssets(ctx, todayUniverse)
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
		rateLimit = defaultRateLimit
	}

	client := newMassiveRESTClient(subscription.Config["apiKey"])
	limiter := rate.NewLimiter(rate.Limit(float64(rateLimit)/float64(61)), 1)

	// get nyc timezone
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		logger.Panic().Err(err).Msg("could not load timezone")
		return
	}

	// fetch upcoming market holidays
	if err := limiter.Wait(ctx); err != nil {
		logger.Error().Err(err).Msg("rate limit wait failed (context likely cancelled); aborting")

		runSummary.Status = data.RunFailed

		return
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

func (api *massiveAssetFetcher) publish(ctx context.Context, asset *data.Asset) {
	logger := zerolog.Ctx(ctx)

	if traceAsset(ctx, asset.Ticker) {
		logger.Info().
			Str("Stage", "publish-entry").
			Str("Ticker", asset.Ticker).
			Str("CompositeFigi", asset.CompositeFigi).
			Str("CIK", asset.CIK).
			Str("DelistingDate", asset.DelistingDate).
			Msg("trace: publish entry")
	}

	if asset.CompositeFigi == "" {
		figi.Enrich(ctx, asset)

		if traceAsset(ctx, asset.Ticker) {
			logger.Info().
				Str("Stage", "publish-post-enrich").
				Str("Ticker", asset.Ticker).
				Str("CompositeFigi", asset.CompositeFigi).
				Str("CIK", asset.CIK).
				Str("DelistingDate", asset.DelistingDate).
				Msg("trace: publish figi.Enrich result")
		}
	}

	if asset.OrganizationPermID == "" || asset.InstrumentPermID == "" {
		permid.Enrich(ctx, asset)
	}

	if asset.CompositeFigi == "" {
		if traceAsset(ctx, asset.Ticker) {
			logger.Warn().
				Str("Stage", "publish-drop").
				Str("Ticker", asset.Ticker).
				Str("CIK", asset.CIK).
				Str("Name", asset.Name).
				Msg("trace: publish dropping asset (composite_figi still empty after enrich)")
		}

		return
	}

	api.publishChan <- &data.Observation{
		AssetObject:      asset,
		ObservationDate:  time.Now(),
		SubscriptionID:   api.subscription.ID,
		SubscriptionName: api.subscription.Name,
	}

	api.numPublished.Add(1)

	if traceAsset(ctx, asset.Ticker) {
		logger.Info().
			Str("Stage", "publish-sent").
			Str("Ticker", asset.Ticker).
			Str("CompositeFigi", asset.CompositeFigi).
			Msg("trace: observation sent to SaveObservations channel")
	}
}

// defaultAssetWalkConcurrency is the default worker count for the
// parallel asset-walk + details fan-out. Workers share api.limiter,
// so the effective throughput is still capped by rateLimit; setting
// this above what the rate limiter releases per second buys no
// additional speedup.
const defaultAssetWalkConcurrency = 32

// assetWalkConcurrency reads the worker count from viper at run
// time so it can be tuned without recompiling. The --asset-workers
// flag on `pvdata run` binds to the `massive.asset_walk_workers`
// key. Falls back to defaultAssetWalkConcurrency when unset or
// non-positive.
func assetWalkConcurrency() int {
	n := viper.GetInt("massive.asset_walk_workers")
	if n <= 0 {
		return defaultAssetWalkConcurrency
	}

	return n
}

// historicalAssetJob is one (date, assetType) unit of work for the
// parallel historical asset walk.
type historicalAssetJob struct {
	date      time.Time
	assetType string
}

// walkHistoricalAssets fans out api.assets() calls across workers
// for every (business day, asset type) pair in [start, end).
// historicalMap is populated under a mutex so concurrent workers do
// not race on map writes; the keep-newest-by-ValidFor invariant is
// preserved from the previous serial loop. Workers all queue through
// api.limiter so the API rate cap is still honoured.
func (api *massiveAssetFetcher) walkHistoricalAssets(ctx context.Context, start, end time.Time, assetTypes []string, historicalMap map[string]*data.Asset) error {
	logger := zerolog.Ctx(ctx)
	workers := assetWalkConcurrency()

	logger.Info().
		Int("Workers", workers).
		Time("Start", start).
		Time("End", end).
		Strs("AssetTypes", assetTypes).
		Msg("starting parallel historical asset walk")

	jobCh := make(chan historicalAssetJob, workers*4)

	var mu sync.Mutex

	walkWindowsByFigi := make(map[string]walkWindow, 16384)
	walkWindowsByCIK := make(map[string]walkWindow, 16384)

	g, gctx := errgroup.WithContext(ctx)

	for range workers {
		g.Go(func() error {
			for job := range jobCh {
				tmpAssets, err := fetchHistoricalAssetPage(gctx, api, job, logger)
				if err != nil {
					// Context errors are propagated so an outer cancel
					// (Ctrl-C, parent run failure) still tears the walk
					// down. Anything else means we have already retried
					// the page and exhausted attempts; log and skip so
					// a long backfill across thousands of (date, type)
					// pairs is not lost to one page.
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						return err
					}

					logger.Error().
						Err(err).
						Str("AssetType", job.assetType).
						Time("Date", job.date).
						Msg("historical asset page failed after retries; continuing walk")

					continue
				}

				mu.Lock()
				for _, a := range tmpAssets {
					a.ValidFor = job.date

					id := walkHistoricalKey(a)

					existing, ok := historicalMap[id]
					if !ok || existing.ValidFor.Before(job.date) {
						historicalMap[id] = a
					}

					if a.CompositeFigi != "" {
						updateWalkWindow(walkWindowsByFigi, a.Ticker+":"+a.CompositeFigi, job.date)
					}

					if a.CIK != "" {
						updateWalkWindow(walkWindowsByCIK, a.Ticker+":"+a.CIK, job.date)
					}

					if traceAsset(ctx, a.Ticker) {
						logger.Info().
							Str("Stage", "walk-observation").
							Str("Ticker", a.Ticker).
							Str("Date", job.date.Format("2006-01-02")).
							Str("CompositeFigi", a.CompositeFigi).
							Str("ShareClassFigi", a.ShareClassFigi).
							Str("CIK", a.CIK).
							Str("Name", a.Name).
							Str("AssetType", string(a.AssetType)).
							Str("WalkKey", id).
							Msg("trace: walk surfaced asset")
					}
				}
				mu.Unlock()
			}

			return nil
		})
	}

	g.Go(func() error {
		defer close(jobCh)

		for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
			if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
				continue
			}

			for _, assetType := range assetTypes {
				select {
				case jobCh <- historicalAssetJob{date: d, assetType: assetType}:
				case <-gctx.Done():
					return gctx.Err()
				}
			}
		}

		return nil
	})

	if err := g.Wait(); err != nil {
		return err
	}

	// Publish the walk-derived windows for assetDetail post-processing.
	// All worker writes happen-before this assignment via g.Wait();
	// subsequent reads from assetDetail goroutines are spawned after
	// this returns, so no further synchronization is needed.
	api.walkWindowsByFigi = walkWindowsByFigi
	api.walkWindowsByCIK = walkWindowsByCIK
	api.walkStart = start
	api.walkEnd = end

	return nil
}

// traceAsset returns true when the asset should produce a per-ticker
// trace log. Gated by the --ticker filter on ctx so a normal full run
// stays quiet; only the operator-targeted ticker emits the trace
// stream. Used at every gate where an asset can be dropped or
// transformed so a missing-output investigation (e.g. "why didn't
// Blockbuster get a synthetic FIGI?") can be answered from one run.
func traceAsset(ctx context.Context, ticker string) bool {
	if ticker == "" {
		return false
	}

	filter, _ := provider.SecurityFilterFromContext(ctx)
	if filter == "" {
		return false
	}

	return strings.EqualFold(ticker, filter)
}

// walkHistoricalKey returns the historicalMap key for a walk-observed
// asset. Asset.ID() is "ticker:composite_figi" which collides on
// "ticker:" whenever composite is empty — and Massive does serve
// empty-composite rows occasionally, sometimes for distinct entities
// reusing the same ticker (e.g. 2009-06-15 BBI=Blockbuster CIK
// 0001085734, vs 2019-09-24 BBI=Brickell Biotech with both
// composite_figi and cik null). Without disambiguation the
// keep-newest-by-ValidFor invariant lets the later anomalous row
// overwrite the earlier predecessor, and the predecessor never reaches
// figi.Enrich to be minted as a synthetic.
//
// Disambiguation precedence: composite > CIK > name. The CIK fallback
// distinguishes different entities under the same ticker; the name
// fallback catches the truly identifier-less Massive anomalies so they
// at least don't collide with each other, but also makes them visible
// so they can be triaged later if they ever turn out to be real.
func walkHistoricalKey(a *data.Asset) string {
	if a.CompositeFigi != "" {
		return a.Ticker + ":" + a.CompositeFigi
	}

	if a.CIK != "" {
		return a.Ticker + ":cik:" + a.CIK
	}

	return a.Ticker + ":name:" + a.Name
}

// updateWalkWindow extends the (firstSeen, lastSeen) bounds for key
// in idx by date. Called once per (asset, day) observation under the
// walk's local mutex.
func updateWalkWindow(idx map[string]walkWindow, key string, date time.Time) {
	win := idx[key]
	if win.firstSeen.IsZero() || date.Before(win.firstSeen) {
		win.firstSeen = date
	}

	if win.lastSeen.IsZero() || date.After(win.lastSeen) {
		win.lastSeen = date
	}

	idx[key] = win
}

// compositeConfirmer is the signature of figi.LookupCompositesByFIGI,
// extracted to a named type so tests can inject a stub without
// touching the production OpenFIGI client.
type compositeConfirmer func(ctx context.Context, figis []string) map[string]*figi.OpenFigiAsset

// sanitizeWalkComposites is the api-bound entry point that wires the
// production OpenFIGI confirmer into the pure-logic sanitizer below.
func (api *massiveAssetFetcher) sanitizeWalkComposites(ctx context.Context, historicalMap map[string]*data.Asset) {
	sanitizeWalkComposites(ctx, historicalMap, api.walkWindowsByFigi, figi.LookupCompositesByFIGI)
}

// sanitizeWalkComposites scrubs dirty composite_figis the historical
// walk picked up so they do not get persisted as separate asset rows.
// Massive's reference dataset occasionally substitutes a foreign-
// exchange composite for a US ticker on isolated dates and still
// labels primary_exchange as XNAS; without this step every such day
// becomes a duplicate row under the same ticker.
//
// Two passes:
//
//  1. OpenFIGI cross-check. Every distinct composite observed in the
//     walk is resolved against OpenFIGI by ID_BB_GLOBAL. Any composite
//     OpenFIGI confirms as non-US (ExchangeCode != "US") is dropped.
//     Composites OpenFIGI does not know about (delisted / evicted) are
//     kept — the walk vote is the best signal available.
//
//  2. (ticker, share_class_figi) dedup. Among rows that survive step 1,
//     any group that still has multiple composites collapses to the
//     one with the longest walk-window span; non-empty CIK and
//     lexicographic order are deterministic tiebreakers.
//
// Both passes also remove the dropped IDs from walkWindowsByFigi so
// downstream applyWalkDerivedDates does not key into ghost rows.
func sanitizeWalkComposites(
	ctx context.Context,
	historicalMap map[string]*data.Asset,
	walkWindowsByFigi map[string]walkWindow,
	confirm compositeConfirmer,
) {
	logger := zerolog.Ctx(ctx)

	if len(historicalMap) == 0 {
		return
	}

	// Step 1: gather distinct composites and ask OpenFIGI to confirm
	// each. Batched at the active OpenFIGI batch size inside the
	// confirmer.
	figisToConfirm := make([]string, 0, len(historicalMap))
	seen := make(map[string]struct{}, len(historicalMap))

	for _, asset := range historicalMap {
		if asset.CompositeFigi == "" {
			continue
		}

		if _, ok := seen[asset.CompositeFigi]; ok {
			continue
		}

		seen[asset.CompositeFigi] = struct{}{}

		figisToConfirm = append(figisToConfirm, asset.CompositeFigi)
	}

	confirmed := confirm(ctx, figisToConfirm)

	droppedNonUS := 0

	for id, asset := range historicalMap {
		openFigi, ok := confirmed[asset.CompositeFigi]
		if !ok {
			continue
		}

		if openFigi.ExchangeCode == "US" {
			continue
		}

		logger.Info().
			Str("Ticker", asset.Ticker).
			Str("CompositeFigi", asset.CompositeFigi).
			Str("ShareClassFigi", asset.ShareClassFigi).
			Str("OpenFigiExchCode", openFigi.ExchangeCode).
			Str("OpenFigiTicker", openFigi.Ticker).
			Msg("dropping walk-observed composite confirmed as non-US by OpenFIGI")

		delete(historicalMap, id)
		delete(walkWindowsByFigi, id)

		droppedNonUS++
	}

	// Step 2: dedup (ticker, share_class_figi) groups remaining.
	groups := make(map[string][]string)

	for id, asset := range historicalMap {
		if asset.ShareClassFigi == "" || asset.CompositeFigi == "" {
			continue
		}

		key := asset.Ticker + ":" + asset.ShareClassFigi
		groups[key] = append(groups[key], id)
	}

	droppedDup := 0

	for _, ids := range groups {
		if len(ids) < 2 {
			continue
		}

		winner := ids[0]
		for _, id := range ids[1:] {
			if compositeBeats(historicalMap[id], walkWindowsByFigi[id], historicalMap[winner], walkWindowsByFigi[winner]) {
				winner = id
			}
		}

		for _, id := range ids {
			if id == winner {
				continue
			}

			logger.Info().
				Str("Ticker", historicalMap[id].Ticker).
				Str("CompositeFigi", historicalMap[id].CompositeFigi).
				Str("ShareClassFigi", historicalMap[id].ShareClassFigi).
				Str("CanonicalCompositeFigi", historicalMap[winner].CompositeFigi).
				Msg("dropping duplicate composite for same (ticker, share_class_figi)")

			delete(historicalMap, id)
			delete(walkWindowsByFigi, id)

			droppedDup++
		}
	}

	if droppedNonUS > 0 || droppedDup > 0 {
		logger.Info().
			Int("DroppedNonUS", droppedNonUS).
			Int("DroppedDuplicates", droppedDup).
			Int("Remaining", len(historicalMap)).
			Msg("sanitized walk composites")
	}
}

// compositeBeats returns true when candidate should replace current
// as canonical composite for a (ticker, share_class_figi) group.
// Vote: longest walk-window span wins (the legitimate composite is
// observed across the security's full active window, while a dirty
// foreign-exchange substitution tends to appear on one or two days).
// Tiebreakers: non-empty CIK (dirty rows often have empty CIK), then
// lexicographically smaller composite for determinism.
func compositeBeats(candidate *data.Asset, candidateWindow walkWindow, current *data.Asset, currentWindow walkWindow) bool {
	candidateSpan := candidateWindow.lastSeen.Sub(candidateWindow.firstSeen)
	currentSpan := currentWindow.lastSeen.Sub(currentWindow.firstSeen)

	if candidateSpan != currentSpan {
		return candidateSpan > currentSpan
	}

	candidateHasCIK := candidate.CIK != ""
	currentHasCIK := current.CIK != ""

	if candidateHasCIK != currentHasCIK {
		return candidateHasCIK
	}

	return candidate.CompositeFigi < current.CompositeFigi
}

// historicalAssetPageMaxAttempts is the per-page retry ceiling for
// walkHistoricalAssets. Counts the initial try plus retries.
const historicalAssetPageMaxAttempts = 4

// historicalAssetPageBaseBackoff is the first cooldown duration when a
// page fails. Doubled on each subsequent retry, so 15s → 30s → 60s
// → 120s in the worst case. Sized so the upstream server has real
// time to recover from a connection-shedding event (HTTP/2 GOAWAY,
// connection reset) before all workers slam back in.
const historicalAssetPageBaseBackoff = 15 * time.Second

// fetchHistoricalAssetPage calls api.assets for one (date, type) job,
// retrying transport-error failures with an exponentially growing
// fleet-wide cooldown so the server has time to recover from
// connection-reset / HTTP/2 GOAWAY storms. Returns context errors
// immediately so an outer cancel still tears the walk down. Returns
// the last non-context error after attempts are exhausted.
func fetchHistoricalAssetPage(ctx context.Context, api *massiveAssetFetcher, job historicalAssetJob, logger *zerolog.Logger) ([]*data.Asset, error) {
	var lastErr error

	for attempt := 1; attempt <= historicalAssetPageMaxAttempts; attempt++ {
		assets, err := api.assets(ctx, job.assetType, job.date)
		if err == nil {
			return assets, nil
		}

		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err
		}

		lastErr = err

		if attempt == historicalAssetPageMaxAttempts {
			break
		}

		backoff := historicalAssetPageBaseBackoff * time.Duration(1<<(attempt-1))
		api.cooldown.Trip(backoff)

		logger.Warn().
			Err(err).
			Str("AssetType", job.assetType).
			Time("Date", job.date).
			Int("Attempt", attempt).
			Dur("FleetCooldown", backoff).
			Msg("historical asset page failed; tripping fleet cooldown and retrying")

		if waitErr := api.cooldown.Wait(ctx); waitErr != nil {
			return nil, waitErr
		}
	}

	return nil, fmt.Errorf("historical assets %s %s after %d attempts: %w",
		job.assetType, job.date.Format("2006-01-02"), historicalAssetPageMaxAttempts, lastErr)
}

func (api *massiveAssetFetcher) assets(ctx context.Context, assetType string, asOfDate time.Time) ([]*data.Asset, error) {
	logger := zerolog.Ctx(ctx)

	if err := api.cooldown.Wait(ctx); err != nil {
		return nil, err
	}

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
		return []*data.Asset{}, err
	}

	const tickersURL = "https://api.massive.com/v3/reference/tickers"

	req := api.client.R().
		SetQueryParam("market", "stocks").
		SetQueryParam("active", "true").
		SetQueryParam("type", assetType).
		SetQueryParam("limit", "1000").
		SetResult(&respContent)

	asOfStr := ""
	if !asOfDate.IsZero() {
		asOfStr = asOfDate.Format("2006-01-02")
		req = req.SetQueryParam("date", asOfStr)
	}

	logger.Info().
		Str("URL", tickersURL).
		Str("Market", "stocks").
		Str("Active", "true").
		Str("AssetType", assetType).
		Str("Limit", "1000").
		Str("AsOfDate", asOfStr).
		Msg("massive reference/tickers initial request")

	resp, err := req.Get(tickersURL)
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
				Ticker:          massiveTicker2PvTicker(ticker.Ticker),
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
			return assets, err
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

	// Apply walk-derived dates first so figi.Enrich sees the correct
	// DelistingDate on walk-discovered predecessors like Blockbuster
	// (BBI 1999-2010). Without this, predecessor rows still carry an
	// empty DelistingDate at this point, figi.Enrich's OpenFIGI step
	// runs and finds nothing (the predecessor's composite is long
	// gone from OpenFIGI), the synthetic-from-CIK step is skipped
	// because it gates on DelistingDate, and the asset is dropped a
	// few lines below with "skipping ticker due to unknown figi".
	for _, asset := range toEnrich {
		api.applyWalkDerivedDates(asset)

		if traceAsset(ctx, asset.Ticker) {
			logger.Info().
				Str("Stage", "post-applyWalkDerivedDates").
				Str("Ticker", asset.Ticker).
				Str("CompositeFigi", asset.CompositeFigi).
				Str("CIK", asset.CIK).
				Str("ListingDate", asset.ListingDate).
				Str("DelistingDate", asset.DelistingDate).
				Msg("trace: applyWalkDerivedDates result")
		}
	}

	log.Debug().Int("NumAssetsToEnrich", len(toEnrich)).Msg("Enriching assets with FIGI")
	figi.Enrich(ctx, toEnrich...)

	for _, asset := range toEnrich {
		if !traceAsset(ctx, asset.Ticker) {
			continue
		}

		logger.Info().
			Str("Stage", "post-figi.Enrich").
			Str("Ticker", asset.Ticker).
			Str("CompositeFigi", asset.CompositeFigi).
			Str("ShareClassFigi", asset.ShareClassFigi).
			Str("CIK", asset.CIK).
			Str("DelistingDate", asset.DelistingDate).
			Msg("trace: figi.Enrich result")
	}

	permid.Enrich(ctx, assets...)

	// for each asset determine if details need to be queried
	for _, asset := range assets {
		var lastUpdated time.Time

		if asset.CompositeFigi == "" {
			log.Warn().Str("Ticker", asset.Ticker).Str("Name", asset.Name).Msg("skipping ticker due to unknown figi")

			if traceAsset(ctx, asset.Ticker) {
				logger.Warn().
					Str("Stage", "filterAssetsByLastUpdated-drop").
					Str("Ticker", asset.Ticker).
					Str("Name", asset.Name).
					Str("CIK", asset.CIK).
					Str("DelistingDate", asset.DelistingDate).
					Msg("trace: asset dropped because composite_figi is empty after enrich")
			}

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

				if traceAsset(ctx, asset.Ticker) {
					logger.Info().
						Str("Stage", "filterAssetsByLastUpdated-queue-detail").
						Str("Ticker", asset.Ticker).
						Str("CompositeFigi", asset.CompositeFigi).
						Msg("trace: no DB row, queued for assetDetail")
				}

				continue
			}

			logger.Error().Err(err).Str("SQL", sql).Str("CompositeFIGI", asset.CompositeFigi).Str("Ticker", asset.Ticker).Msg("error when querying database for asset")

			return nil, err
		}

		if lastUpdated.Before(asset.LastUpdated) {
			assetUpdate = append(assetUpdate, asset)

			if traceAsset(ctx, asset.Ticker) {
				logger.Info().
					Str("Stage", "filterAssetsByLastUpdated-queue-update").
					Str("Ticker", asset.Ticker).
					Str("CompositeFigi", asset.CompositeFigi).
					Time("DBLastUpdated", lastUpdated).
					Time("APILastUpdated", asset.LastUpdated).
					Msg("trace: DB row exists but API is newer; queued in assetUpdate (subject to 100-cap)")
			}
		} else if traceAsset(ctx, asset.Ticker) {
			logger.Info().
				Str("Stage", "filterAssetsByLastUpdated-skip-fresh").
				Str("Ticker", asset.Ticker).
				Str("CompositeFigi", asset.CompositeFigi).
				Time("DBLastUpdated", lastUpdated).
				Time("APILastUpdated", asset.LastUpdated).
				Msg("trace: DB row is current; asset not refreshed this run")
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

	// Oversample the SQL LIMIT so that delisted/no-longer-in-API rows
	// don't starve the queue of refreshable candidates. Guard against
	// overflow when the cap is effectively unlimited.
	sqlLimit := api.missingBrandingCap
	if api.missingBrandingCap < math.MaxInt32/2 {
		sqlLimit = api.missingBrandingCap * 2
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
		LIMIT %d`, api.subscription.DataTablesMap[data.AssetKey], sqlLimit)

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
				if added >= api.missingBrandingCap {
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
			return err
		}

		const tickersURL = "https://api.massive.com/v3/reference/tickers"

		logger.Info().
			Str("URL", tickersURL).
			Str("Active", "false").
			Str("Sort", "last_updated_utc").
			Str("Order", "desc").
			Str("Limit", "1000").
			Str("AssetType", assetType).
			Msg("massive reference/tickers initial request (inactive lookup)")

		resp, err := api.client.R().
			SetQueryParam("active", "false").
			SetQueryParam("sort", "last_updated_utc").
			SetQueryParam("order", "desc").
			SetQueryParam("limit", "1000").
			SetQueryParam("type", assetType).
			SetResult(&respContent).
			Get(tickersURL)
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
					Ticker:        massiveTicker2PvTicker(massiveAsset.Ticker),
					CompositeFigi: massiveAsset.CompositeFIGI,
				}

				// lookup the completely filled out asset and update its values
				// publish the updated asset
				if inactiveAsset, ok := inactiveMap[asset.ID()]; ok {
					inactiveAsset.DelistingDate = strings.Split(massiveAsset.DelistDate, "T")[0]
					inactiveAsset.LastUpdated = lastUpdated
					inactiveAsset.Active = false
					deactivated[asset.ID()] = inactiveAsset
					api.publish(ctx, inactiveAsset)

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
				return err
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
				api.publish(ctx, possibleInactiveAsset)
			}
		}
	}

	return nil
}

func (api *massiveAssetFetcher) assetDetails(ctx context.Context, assets []*data.Asset) {
	logger := zerolog.Ctx(ctx)

	if len(assets) == 0 {
		return
	}

	workers := assetWalkConcurrency()
	total := len(assets)

	logger.Info().
		Int("Workers", workers).
		Int("Total", total).
		Msg("starting parallel asset-details fan-out")

	jobCh := make(chan *data.Asset, workers*4)
	progressLogger := newDetailsProgressLogger(logger, total)

	g, gctx := errgroup.WithContext(ctx)

	for range workers {
		g.Go(func() error {
			for asset := range jobCh {
				fullAsset, err := api.assetDetail(gctx, asset)
				if err != nil {
					// Match the serial behaviour: log and skip a single
					// failed detail so one bad ticker cannot abort the
					// whole batch. Context errors still propagate so an
					// outer cancel can unwind the workers.
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						return err
					}

					logger.Error().Err(err).Msg("received an error when querying massive details")
					progressLogger.tick()

					continue
				}

				api.publish(gctx, fullAsset)
				progressLogger.tick()
			}

			return nil
		})
	}

	g.Go(func() error {
		defer close(jobCh)

		for _, asset := range assets {
			select {
			case jobCh <- asset:
			case <-gctx.Done():
				return gctx.Err()
			}
		}

		return nil
	})

	if err := g.Wait(); err != nil {
		logger.Error().Err(err).Msg("asset-details fan-out aborted")
	}
}

// detailsProgressLogger emits an ETA log roughly every 60 seconds
// from the parallel assetDetails workers. tick() is safe for
// concurrent use; completed is an atomic counter so the rate.Sometimes
// gate cannot race with worker increments.
type detailsProgressLogger struct {
	logger    *zerolog.Logger
	total     int
	started   time.Time
	completed atomic.Int64
	sometimes rate.Sometimes
	mu        sync.Mutex
}

func newDetailsProgressLogger(logger *zerolog.Logger, total int) *detailsProgressLogger {
	return &detailsProgressLogger{
		logger:    logger,
		total:     total,
		started:   time.Now(),
		sometimes: rate.Sometimes{Interval: 60 * time.Second},
	}
}

func (p *detailsProgressLogger) tick() {
	done := int(p.completed.Add(1))

	p.mu.Lock()
	defer p.mu.Unlock()

	p.sometimes.Do(func() {
		secondsPerItem := time.Since(p.started) / time.Duration(done)
		left := p.total - done
		timeLeft := secondsPerItem * time.Duration(left)
		p.logger.Info().
			Int("Completed", done).
			Str("SinceStarted", time.Since(p.started).Round(time.Second).String()).
			Int("NumAssetsLeft", left).
			Str("secondsPerItem", secondsPerItem.Round(time.Second).String()).
			Str("ETA", timeLeft.Round(time.Second).String()).
			Msg("asset detail progress")
	})
}

func (api *massiveAssetFetcher) assetDetail(ctx context.Context, asset *data.Asset) (*data.Asset, error) {
	var respContent massiveResponse

	logger := zerolog.Ctx(ctx)
	detailsURL := fmt.Sprintf("https://api.massive.com/v3/reference/tickers/%s", asset.Ticker)

	if traceAsset(ctx, asset.Ticker) {
		logger.Info().
			Str("Stage", "assetDetail-entry").
			Str("Ticker", asset.Ticker).
			Str("CompositeFigi", asset.CompositeFigi).
			Time("ValidFor", asset.ValidFor).
			Msg("trace: calling /v3/reference/tickers details endpoint")
	}

	if err := api.limiter.Wait(ctx); err != nil {
		return nil, err
	}

	// Currently-delisted tickers only resolve at this endpoint when
	// date= matches a day they were active; without it the API returns
	// NOT_FOUND. Historical-walk assets carry ValidFor (set in
	// walkHistoricalAssets); today's-snapshot assets have zero ValidFor
	// and still resolve without a date param.
	req := api.client.R().SetResult(&respContent)
	if !asset.ValidFor.IsZero() {
		req = req.SetQueryParam("date", asset.ValidFor.Format("2006-01-02"))
	}

	resp, err := req.Get(detailsURL)
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
			return nil, err
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
			return nil, err
		}

		resp, err := api.client.R().Get(massiveAsset.Branding.LogoURL)
		if err != nil {
			logger.Error().Err(err).Msg("error when fetching asset logo")
			return nil, err
		}

		logo = resp.Body()
		logoMimeType = resp.Header().Get("Content-Type")
	}

	// Build the fullAsset that downstream publish() / SaveDB will see.
	// Most fields come from the per-ticker details endpoint, but a few
	// must carry over from the input asset:
	//
	//   - OrganizationPermID / InstrumentPermID: Massive's response has
	//     no PermID field. filterAssetsByLastUpdated already enriched
	//     the input asset via permid.Enrich; without preserving those
	//     values here, the freshly-built fullAsset would be empty and
	//     publish() would either re-resolve (wasting another Refinitiv
	//     call against the daily quota) or — once the per-run budget
	//     is exhausted — silently land in the DB with an empty PermID,
	//     throwing away the upstream resolution that was already paid
	//     for.
	//
	//   - CIK: usually present in the details response, but for some
	//     long-delisted tickers the response omits it (the trace shows
	//     this for the 2019-09-24 BBI anomaly). Fall back to the
	//     walk-observed CIK on the input asset so applyWalkDerivedDates'
	//     CIK index can still resolve a window.
	cik := massiveAsset.CIK
	if cik == "" {
		cik = asset.CIK
	}

	assetDetail := &data.Asset{
		Ticker:               massiveTicker2PvTicker(massiveAsset.Ticker),
		CompositeFigi:        massiveAsset.CompositeFIGI,
		ShareClassFigi:       massiveAsset.ShareClassFIGI,
		Name:                 massiveAsset.Name,
		Description:          massiveAsset.Description,
		Active:               massiveAsset.Active,
		PrimaryExchange:      massiveExchangeMap[massiveAsset.PrimaryExchange],
		AssetType:            data.AssetType(massiveAsset.Type),
		HeadquartersLocation: location,
		CIK:                  cik,
		OrganizationPermID:   asset.OrganizationPermID,
		InstrumentPermID:     asset.InstrumentPermID,
		SIC:                  &sicCode,
		CorporateUrl:         massiveAsset.CorporateURL,
		Icon:                 icon,
		IconMimeType:         iconMimeType,
		Logo:                 logo,
		LogoMimeType:         logoMimeType,
		ListingDate:          massiveAsset.ListDate,
		LastUpdated:          asset.LastUpdated,
		ValidFor:             asset.ValidFor,
	}

	api.applyWalkDerivedDates(assetDetail)

	if traceAsset(ctx, assetDetail.Ticker) {
		logger.Info().
			Str("Stage", "assetDetail-exit").
			Str("Ticker", assetDetail.Ticker).
			Str("CompositeFigi", assetDetail.CompositeFigi).
			Str("ShareClassFigi", assetDetail.ShareClassFigi).
			Str("CIK", assetDetail.CIK).
			Str("Name", assetDetail.Name).
			Str("ListingDate", assetDetail.ListingDate).
			Str("DelistingDate", assetDetail.DelistingDate).
			Bool("Active", assetDetail.Active).
			Msg("trace: assetDetail constructed fullAsset")
	}

	return assetDetail, nil
}

// walkDerivedBufferDays is how many calendar days the walk-derived
// cutoff backs off from the walk boundaries before declaring an
// asset listed/delisted. Sized to absorb the longest market closures
// we expect to see in a continuous walk: regular long weekends
// (Thanksgiving Thu–Sun is 4 calendar days), Christmas/NYE clusters
// when the holiday falls on a Friday, and unscheduled closures like
// Hurricane Sandy in October 2012 (markets closed 5 days). 14 days
// gives comfortable margin around all of these so an actively-traded
// asset is never falsely flagged as delisted just because the walk
// happened to land right after a long closure.
const walkDerivedBufferDays = 14

// applyWalkDerivedDates fills ListingDate / DelistingDate on asset
// from the historical-walk windows when assetDetail did not provide
// them. The per-ticker details endpoint is authoritative when it
// returns data; this fallback covers entities whose details endpoint
// omits a listing or delisting timestamp (typical for long-delisted
// tickers where the per-ticker endpoint returns NOT_FOUND or sparse
// fields).
//
// Lookup is tried first by ticker:composite_figi and then by
// ticker:cik. The CIK fallback covers the case where the walk
// observed the asset with an empty list-response FIGI (so the
// FIGI-keyed index missed it) but assetDetail has since filled in
// the FIGI — CIK is stable across the two responses.
func (api *massiveAssetFetcher) applyWalkDerivedDates(asset *data.Asset) {
	if api.walkWindowsByFigi == nil && api.walkWindowsByCIK == nil {
		return
	}

	var (
		win walkWindow
		ok  bool
	)

	if asset.CompositeFigi != "" && api.walkWindowsByFigi != nil {
		win, ok = api.walkWindowsByFigi[asset.Ticker+":"+asset.CompositeFigi]
	}

	if !ok && asset.CIK != "" && api.walkWindowsByCIK != nil {
		win, ok = api.walkWindowsByCIK[asset.Ticker+":"+asset.CIK]
	}

	if !ok {
		return
	}

	buffer := time.Duration(walkDerivedBufferDays) * 24 * time.Hour

	if asset.DelistingDate == "" {
		if win.lastSeen.Before(api.walkEnd.Add(-buffer)) {
			asset.DelistingDate = win.lastSeen.AddDate(0, 0, 1).Format("2006-01-02")
		}
	}

	if asset.ListingDate == "" {
		if win.firstSeen.After(api.walkStart.Add(buffer)) {
			asset.ListingDate = win.firstSeen.Format("2006-01-02")
		}
	}
}

func massiveTicker2PvTicker(ticker string) string {
	return strings.ReplaceAll(ticker, ".", "/")
}

const maxIconLogoFetchesPerRun = 100
const maxMissingBrandingPerRun = 100
const defaultAssetLookback = 14 * 24 * time.Hour

// parseIconLogoLimit reads the iconLogoLimit subscription config and
// returns (budget, missingBrandingCap):
//   - blank/unset: defaults to 100 / 100 (current behavior).
//   - "0" or any non-positive: treated as unlimited; budget is 0
//     (brandingBudget interprets <=0 as no cap) and missingBrandingCap
//     is math.MaxInt32 so the missing-branding lane refreshes every
//     candidate.
//   - positive N: both lanes are capped at N.
func parseIconLogoLimit(raw string) (int, int, error) {
	if raw == "" {
		return maxIconLogoFetchesPerRun, maxMissingBrandingPerRun, nil
	}

	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, 0, err
	}

	if n <= 0 {
		return 0, math.MaxInt32, nil
	}

	return n, n, nil
}

// brandingBudget caps how many icon/logo HTTP fetches a single
// run performs. A non-positive cap means unlimited. Allow() is
// safe for concurrent use so the parallel assetDetails workers
// can share one budget instance.
type brandingBudget struct {
	mu        sync.Mutex
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

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.remaining <= 0 {
		return false
	}

	b.remaining--

	return true
}
