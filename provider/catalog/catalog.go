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
package catalog

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"golang.org/x/time/rate"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
)

func init() {
	provider.Register("catalog", &Catalog{})
}

// Catalog is the Historical Asset Catalog provider. It reconstructs
// per-ticker asset lifecycles from the EOD parquet archive and fills
// each lifecycle with metadata gathered from the Massive reference
// endpoint, SEC filings, and OpenFIGI. The catalog provider is
// independent of provider/massive; it carries its own REST client,
// archive reader, and reconciliation logic.
type Catalog struct{}

func (c *Catalog) Name() string {
	return "catalog"
}

func (c *Catalog) Description() string {
	return `The Historical Asset Catalog reconstructs per-ticker asset lifecycles from the EOD parquet archive and enriches each lifecycle with reference metadata from the Massive REST API, SEC filings, and OpenFIGI. Use this provider to build a definitive cross-source asset catalog separate from the live Massive Stock Tickers feed.`
}

func (c *Catalog) ConfigDescription() map[string]string {
	return map[string]string{}
}

func (c *Catalog) Datasets() map[string]provider.Dataset {
	return map[string]provider.Dataset{
		"Historical Asset Catalog": {
			Name:        "Historical Asset Catalog",
			Description: "Per-ticker asset lifecycles reconstructed from the EOD parquet archive and enriched from Massive, SEC, and OpenFIGI.",
			DataTypes:   []*data.DataType{data.DataTypes[data.AssetKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(1949, 4, 19, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			ConfigDescription: map[string]string{
				"apiKey":             "Enter your Massive API key:",
				"rateLimit":          "What is the maximum number of requests per minute?",
				"sharadarTickersDir": "Directory containing Sharadar TICKERS *.csv.zst files (leave blank to disable Sharadar enrichment):",
			},
			Fetch: downloadHistoricalAssetCatalog,
		},
	}
}

// downloadHistoricalAssetCatalog fetches today's Massive active=true
// snapshot, then walks the EOD parquet archive to build one asset row
// per (ticker, lifecycle) span. Lifecycle metadata is filled from
// Massive's per-ticker reference endpoint with SEC and OpenFIGI used
// for CIK correction and FIGI validation. Unlike the live Massive
// Stock Tickers fetch, this provider does not run a delisted-detection
// pass — that remains a live-data concern owned by provider/massive.
func downloadHistoricalAssetCatalog(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
	logger := zerolog.Ctx(ctx)

	runSummary := data.RunSummary{
		StartTime:        time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}

	iconLogoLimit, missingBrandingCap, err := parseIconLogoLimit(subscription.Config["iconLogoLimit"])
	if err != nil {
		logger.Error().Err(err).Str("iconLogoLimit", subscription.Config["iconLogoLimit"]).Msg("could not convert iconLogoLimit configuration parameter to an integer")

		runSummary.Status = data.RunFailed

		runSummary.EndTime = time.Now()
		exitNotification <- runSummary

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

	tracked := trackedTypeSet(ctx)

	logger.Info().Msg("stage: fetching today's snapshot (active=true, no type filter)")

	snapStart := time.Now()

	todayRaw, err := api.assets(ctx, "", time.Time{})
	if err != nil {
		logger.Error().Err(err).Msg("error getting ticker information")

		runSummary.Status = data.RunFailed

		return
	}

	logger.Info().
		Int("Raw", len(todayRaw)).
		Dur("Elapsed", time.Since(snapStart).Round(time.Millisecond)).
		Msg("stage: today's snapshot fetched; running tracked-type filter (SEC for untyped)")

	filterStart := time.Now()
	todayUniverse := filterToTrackedTypes(ctx, todayRaw, tracked)

	// Apply --ticker / --figi filters to today's snapshot so the
	// active-set gate matches the scoped universe the builder honors
	// via the ctx-derived security filter.
	tickerFilter, figiFilter := provider.SecurityFilterFromContext(ctx)
	if tickerFilter != "" || figiFilter != "" {
		filtered := todayUniverse[:0]

		for _, a := range todayUniverse {
			switch {
			case tickerFilter != "" && strings.EqualFold(a.Ticker, tickerFilter):
				filtered = append(filtered, a)
			case figiFilter != "" && a.CompositeFigi == figiFilter:
				filtered = append(filtered, a)
			}
		}

		todayUniverse = filtered
	}

	todayActive := make(map[string]struct{}, len(todayUniverse))
	for _, a := range todayUniverse {
		todayActive[a.Ticker] = struct{}{}
	}

	logger.Info().
		Int("Raw", len(todayRaw)).
		Int("Kept", len(todayUniverse)).
		Int("ActiveTickers", len(todayActive)).
		Dur("Elapsed", time.Since(filterStart).Round(time.Millisecond)).
		Msg("stage: today's snapshot filtered; starting EOD-driven asset builder")

	sharadarIdx, err := loadSharadarTickerIndex(ctx, subscription.Config["sharadarTickersDir"])
	if err != nil {
		logger.Error().Err(err).Msg("catalog: sharadar ticker index load failed")

		runSummary.Status = data.RunFailed

		return
	}

	builder := NewAssetBuilder(api, tracked, todayActive, sharadarIdx)

	if err := builder.BuildAll(ctx); err != nil {
		logger.Error().Err(err).Msg("catalog: asset builder aborted")

		runSummary.Status = data.RunFailed

		return
	}

	logger.Info().
		Int64("NumPublished", api.numPublished.Load()).
		Msg("stage: Historical Asset Catalog run complete")
}
