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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/go-resty/resty/v2"
	"github.com/goccy/go-json"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/penny-vault/pvdata/provider/sec"
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

	// eodArchive indexes the per-day aggregate parquet files under
	// parquet_backup_dir/<subscription_slug>. Loaded lazily on first
	// call to eodArchiveForRun and shared read-only across publish
	// goroutines. nil when no parquet_backup_dir is configured.
	eodArchive     *EODArchive
	eodArchiveOnce sync.Once
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

// defaultTrackedAssetTypes is the Massive `type` allowlist applied
// after the bulk /v3/reference/tickers response is fetched. The walk
// no longer sends a server-side `&type=` filter — Massive's reference
// data omits the type tag for most pre-2010 tickers, so a server-side
// type filter silently drops thousands of common-stock issuers each
// snapshot. Instead the walk fetches without a type filter and this
// allowlist runs locally; records with an empty type fall through to
// sec.ResolveAssetType for classification.
var defaultTrackedAssetTypes = []string{"CS", "ADRC", "ETF", "CEF", "ETN", "UNK"}

// trackedTypeSet returns the per-run allowlist as a lookup map. Reads
// the context's AssetTypeFilter so a --asset-type override on the
// command line narrows the set without changing the default.
func trackedTypeSet(ctx context.Context) map[string]struct{} {
	types := provider.AssetTypeFilterFromContext(ctx, defaultTrackedAssetTypes)
	out := make(map[string]struct{}, len(types))

	for _, t := range types {
		out[t] = struct{}{}
	}

	return out
}

// filterToTrackedTypes keeps assets whose Massive `type` is in tracked,
// promotes untyped-but-CIK-bearing records via sec.ResolveAssetType,
// and drops everything else (preferreds, warrants, structured products,
// untyped no-CIK foreign listings, etc.). The returned slice reuses
// the input's backing array; callers must not retain the original.
//
// Per-record outcomes:
//   - typed and in tracked  -> kept verbatim
//   - typed and not tracked -> dropped (PFD, SP, WARRANT, OTHER, ...)
//   - untyped, has CIK      -> SEC ResolveAssetType; if it returns a
//     tracked type the asset is promoted to that type and kept,
//     otherwise dropped
//   - untyped, no CIK       -> dropped with a single debug log line
func filterToTrackedTypes(ctx context.Context, assets []*data.Asset, tracked map[string]struct{}) []*data.Asset {
	logger := zerolog.Ctx(ctx)
	kept := assets[:0]

	for _, asset := range assets {
		if asset == nil {
			continue
		}

		traced := asset != nil && traceAsset(ctx, asset.Ticker)
		t := string(asset.AssetType)

		validForStr := ""
		if !asset.ValidFor.IsZero() {
			validForStr = asset.ValidFor.Format("2006-01-02")
		}

		// Ticker-shape and name-based drops run before the type
		// allowlist because Massive serves units, warrants, rights,
		// and its own test symbols typed as CS, so the typed-keep
		// branch would otherwise let them through.
		if dropReason, drop := marketIneligibleReason(asset); drop {
			if traced {
				logger.Info().
					Str("Stage", "filter-market-ineligible").
					Str("Ticker", asset.Ticker).
					Str("AssetType", t).
					Str("Name", asset.Name).
					Str("Reason", dropReason).
					Str("ValidFor", validForStr).
					Msg("trace: dropping market-ineligible record")
			} else {
				logger.Debug().
					Str("Stage", "filter-market-ineligible").
					Str("Ticker", asset.Ticker).
					Str("AssetType", t).
					Str("Name", asset.Name).
					Str("Reason", dropReason).
					Str("ValidFor", validForStr).
					Msg("dropping market-ineligible record")
			}

			continue
		}

		if t != "" {
			if _, ok := tracked[t]; ok {
				if traced {
					logger.Info().
						Str("Stage", "filter-typed-keep").
						Str("Ticker", asset.Ticker).
						Str("AssetType", t).
						Str("CIK", asset.CIK).
						Str("ValidFor", validForStr).
						Msg("trace: keeping typed asset")
				}

				kept = append(kept, asset)
			} else if traced {
				logger.Info().
					Str("Stage", "filter-typed-drop").
					Str("Ticker", asset.Ticker).
					Str("AssetType", t).
					Str("ValidFor", validForStr).
					Msg("trace: dropping typed asset (type not in tracked allowlist)")
			}

			continue
		}

		if asset.CIK == "" {
			if traced {
				logger.Info().
					Str("Stage", "filter-untyped-no-cik").
					Str("Ticker", asset.Ticker).
					Str("CompositeFigi", asset.CompositeFigi).
					Str("ValidFor", validForStr).
					Msg("untyped record without CIK; not emitting")
			} else {
				logger.Debug().
					Str("Stage", "filter-untyped-no-cik").
					Str("Ticker", asset.Ticker).
					Str("CompositeFigi", asset.CompositeFigi).
					Str("ValidFor", validForStr).
					Msg("untyped record without CIK; not emitting")
			}

			continue
		}

		if traced {
			logger.Info().
				Str("Stage", "filter-untyped-resolve").
				Str("Ticker", asset.Ticker).
				Str("CIK", asset.CIK).
				Str("ValidFor", validForStr).
				Msg("trace: calling sec.ResolveAssetType")
		}

		year := 0
		if !asset.ValidFor.IsZero() {
			year = asset.ValidFor.Year()
		}

		resolved, resolvedCIK, ok := sec.ResolveAssetTypeWithCIKCorrection(ctx, asset.CIK, asset.Ticker, asset.Name, year)
		if ok && resolvedCIK != asset.CIK {
			logger.Info().
				Str("Stage", "filter-cik-corrected").
				Str("Ticker", asset.Ticker).
				Str("OldCIK", asset.CIK).
				Str("NewCIK", resolvedCIK).
				Str("ResolvedType", string(resolved)).
				Str("Name", asset.Name).
				Str("ValidFor", validForStr).
				Msg("corrected misattributed CIK via SEC name search at walk-time filter")

			asset.CIK = resolvedCIK
		}

		if !ok {
			if traced {
				logger.Info().
					Str("Stage", "filter-sec-unresolved").
					Str("Ticker", asset.Ticker).
					Str("CIK", asset.CIK).
					Str("ValidFor", validForStr).
					Msg("SEC could not resolve type for untyped record; dropping")
			} else {
				logger.Debug().
					Str("Stage", "filter-sec-unresolved").
					Str("Ticker", asset.Ticker).
					Str("CIK", asset.CIK).
					Str("ValidFor", validForStr).
					Msg("SEC could not resolve type for untyped record; dropping")
			}

			continue
		}

		if _, want := tracked[string(resolved)]; !want {
			if traced {
				logger.Info().
					Str("Stage", "filter-sec-untracked").
					Str("Ticker", asset.Ticker).
					Str("CIK", asset.CIK).
					Str("ResolvedType", string(resolved)).
					Str("ValidFor", validForStr).
					Msg("SEC-resolved type not in tracked allowlist; dropping")
			} else {
				logger.Debug().
					Str("Stage", "filter-sec-untracked").
					Str("Ticker", asset.Ticker).
					Str("CIK", asset.CIK).
					Str("ResolvedType", string(resolved)).
					Str("ValidFor", validForStr).
					Msg("SEC-resolved type not in tracked allowlist; dropping")
			}

			continue
		}

		if traced {
			logger.Info().
				Str("Stage", "filter-sec-promote").
				Str("Ticker", asset.Ticker).
				Str("CIK", asset.CIK).
				Str("ResolvedType", string(resolved)).
				Msg("trace: promoting via SEC-resolved type")
		}

		asset.AssetType = resolved
		kept = append(kept, asset)
	}

	return kept
}

// marketIneligibleSuffixes is the set of ticker suffixes (after the
// slash separator pvdata uses for class-share / lifecycle decorations)
// that mark a record as a non-tradable instrument. Tickers ending in
// these are units (SPAC share + warrant bundles), warrants, rights,
// or when-distributed placeholders — none of which pvdata tracks.
var marketIneligibleSuffixes = []string{"/U", "/UN", "/W", "/WS", "/R", "/RT", "/WD"}

// marketIneligibleNamePatterns is the set of case-insensitive name
// substrings that mark a record as a placeholder / non-tradable
// security regardless of its asset_type. Patterns with leading or
// trailing whitespace are word-boundary-aware to avoid matching
// substrings of legitimate company names (e.g. "preferred" inside
// "Preferred Apartment Communities" — rare, but the leading space on
// " pfd" is safer than a bare token).
var marketIneligibleNamePatterns = []string{
	"when issued",
	"when-issued",
	"w.i.",
	"ex-distribution",
	"ex distribution",
	"ex-dist",
	"test symbol",
	" pfd",
	"pfd ",
	"preferred",
	" warrant",
	"subscription rt",
	" w.d.",
}

// hasLowercaseLetter reports whether s contains at least one ASCII
// lowercase letter. Used to detect Massive's placeholder-ticker
// convention (lowercase markers w=when-issued, p=preferred, r=rights,
// rw=rights-when-issued) which is not valid for real US equities.
func hasLowercaseLetter(s string) bool {
	for _, r := range s {
		if r >= 'a' && r <= 'z' {
			return true
		}
	}

	return false
}

// nameSaysNonTradable looks at the company name and returns true when
// it contains a substring that marks the record as a non-tradable
// instrument (warrant, preferred, when-issued, ex-distribution, test
// symbol, etc.). This check always fires regardless of the provider's
// classification: Massive types its own test symbols (the canonical
// example is IPOO with name "IPOO Test Symbol") and rebadged
// preferred and warrant placeholders as CS, so the company name is
// the only signal that catches them.
func nameSaysNonTradable(asset *data.Asset) (string, bool) {
	if asset == nil {
		return "", false
	}

	lowerName := strings.ToLower(asset.Name)
	for _, pat := range marketIneligibleNamePatterns {
		if strings.Contains(lowerName, pat) {
			return "name_contains=" + strings.TrimSpace(pat), true
		}
	}

	return "", false
}

// csRebadgeNamePatterns are the marketIneligibleNamePatterns entries
// that only catch CS-rebadged placeholders; they false-positive on
// fund/ADR names that describe holdings or the underlying security.
var csRebadgeNamePatterns = map[string]struct{}{
	"preferred": {},
	" pfd":      {},
	"pfd ":      {},
}

var fundOrADRTypes = map[string]struct{}{
	"FUND":                  {},
	string(data.ETF):        {},
	string(data.CEF):        {},
	string(data.ADRC):       {},
	string(data.MutualFund): {},
	string(data.ETN):        {},
}

// nameSaysNonTradableForType skips csRebadgeNamePatterns when the
// Massive type is a fund or ADR class.
func nameSaysNonTradableForType(name, assetType string) (string, bool) {
	lowerName := strings.ToLower(name)
	_, typeExempt := fundOrADRTypes[strings.TrimSpace(assetType)]

	for _, pat := range marketIneligibleNamePatterns {
		if typeExempt {
			if _, skip := csRebadgeNamePatterns[pat]; skip {
				continue
			}
		}

		if strings.Contains(lowerName, pat) {
			return "name_contains=" + strings.TrimSpace(pat), true
		}
	}

	return "", false
}

// tickerSaysNonTradable looks at the ticker symbol and returns true
// when its shape marks the record as non-tradable: a lowercase
// placeholder marker (Massive uses lowercase letters as when-issued /
// preferred / rights flags inside otherwise-uppercase tickers) or a
// slash-decorated suffix (/U, /UN, /W, /WS, /R, /RT, /WD). The
// builder only runs this check when Massive's type field is empty;
// when type is set, a typed CS record with an unusual ticker shape
// should be trusted.
func tickerSaysNonTradable(asset *data.Asset) (string, bool) {
	if asset == nil {
		return "", false
	}

	if hasLowercaseLetter(asset.Ticker) {
		return "ticker_has_lowercase_marker", true
	}

	for _, suffix := range marketIneligibleSuffixes {
		if strings.HasSuffix(asset.Ticker, suffix) {
			return "ticker_suffix=" + suffix, true
		}
	}

	return "", false
}

// marketIneligibleReason combines the name and ticker checks. The
// daily-side path (filterToTrackedTypes inside delistedAssets) wants
// the legacy "drop on any signal" behavior; the builder splits the
// checks instead so the ticker branch only fires for untyped records.
func marketIneligibleReason(asset *data.Asset) (string, bool) {
	if reason, drop := tickerSaysNonTradable(asset); drop {
		return reason, true
	}

	return nameSaysNonTradable(asset)
}

// shortDelistedDurationDays is the minimum allowed duration for a
// delisted security whose listed-to-delisted window has no
// corresponding EOD archive coverage. Records shorter than this with
// no EOD evidence are almost certainly Massive-catalog placeholders
// rather than real listings — Massive frequently surfaces a ticker on
// a single date with a name like "Avadim Health, Inc. Common Stock"
// for entities that never actually traded. The threshold is generous
// (about 6 months) because the EOD-coverage gate carries most of the
// safety: any real security that traded for the window has bars in
// the archive and is exempt regardless of how short the window is.
const shortDelistedDurationDays = 180

// shortDelistedNoCoverageReason reports whether a delisted asset
// should be dropped at publish time, and returns ("", false) when
// the asset should publish. The check applies only to already-delisted
// records (Active=false with DelistingDate set); currently-trading
// assets are never dropped by this rule. Two gates must both fire
// for a drop:
//
//  1. The (DelistingDate - ListingDate) duration is shorter than
//     shortDelistedDurationDays. An empty ListingDate is treated as
//     zero duration — a Massive-emitted stub with a delisting boundary
//     but no listing evidence is the strongest phantom signal we have.
//  2. The EOD archive has no range overlapping the asset's
//     [ListingDate, DelistingDate] window — we have no trade-data
//     evidence the security actually existed for that window. When
//     ListingDate is empty the overlap check uses the delisting date
//     as the upper edge of the window; any archive range that starts
//     on or before delisting counts as evidence.
func (api *massiveAssetFetcher) shortDelistedNoCoverageReason(asset *data.Asset) (string, bool) {
	if asset == nil || asset.Active {
		return "", false
	}

	listed := parseISODate(asset.ListingDate)
	delisted := parseISODate(asset.DelistingDate)

	if delisted.IsZero() {
		return "", false
	}

	durationDays := 0
	if !listed.IsZero() {
		durationDays = int(delisted.Sub(listed) / (24 * time.Hour))
		if durationDays >= shortDelistedDurationDays {
			return "", false
		}
	}

	archive := api.eodArchiveForRun()
	if archive == nil {
		return "", false
	}

	ranges := archive.Ranges(asset.Ticker)
	for _, r := range ranges {
		if !listed.IsZero() && r.End.Before(listed) {
			continue
		}

		if r.Start.After(delisted) {
			continue
		}

		return "", false
	}

	return fmt.Sprintf("duration=%dd_no_eod_coverage", durationDays), true
}

// downloadMassiveAssets is the Stock Tickers subscription Fetch. It
// produces one observation per (ticker, EOD lifecycle range) by
// running the EOD-archive-driven AssetBuilder and then runs the
// daily-side delistedAssets handler to catch tickers that fell out of
// today's active snapshot. See docs/asset_builder_design.md for the
// builder's design and docs/asset_builder_migration.md for the list
// of historical-pipeline handlers this replaces.
func downloadMassiveAssets(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
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

	// Today's snapshot drives two things: the builder's active=true
	// gate on the most-recent EOD lifecycle, and delistedAssets'
	// disjoint-set comparison against DB-active rows. It must reflect
	// the API's current active=true universe, not a unioned historical
	// view, so tickers delisted between this run and the last would
	// otherwise appear "still active" and never get marked inactive.
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

	// Apply --ticker / --figi filters to today's snapshot. The builder
	// honors --ticker via the ctx-derived security filter inside
	// AssetBuilder.discoverProposals; here we narrow todayUniverse to
	// match so the active-set gate and delistedAssets see the same
	// scoped universe.
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

	builder := NewAssetBuilder(api, tracked, todayActive)

	if err := builder.BuildAll(ctx); err != nil {
		logger.Error().Err(err).Msg("massive: asset builder aborted")

		runSummary.Status = data.RunFailed

		return
	}

	// Daily-side handler: mark DB-active rows that are no longer in
	// today's active snapshot as delisted. Operates on the filtered
	// today's snapshot so the disjoint-set logic against active DB
	// rows is correct.
	logger.Info().Int("TodaySnapshot", len(todayUniverse)).Msg("stage: checking for newly-delisted assets")

	delistStart := time.Now()

	if err := api.delistedAssets(ctx, todayUniverse); err != nil {
		runSummary.Status = data.RunFailed

		return
	}

	logger.Info().
		Dur("Elapsed", time.Since(delistStart).Round(time.Second)).
		Int64("NumPublished", api.numPublished.Load()).
		Msg("stage: Stock Tickers run complete")
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

// publish sends a fully-enriched and date-assigned asset on the
// publishChan for the SaveObservations consumer to persist. Assets
// still missing a composite_figi at this stage are dropped because
// the asset_description table's primary key requires both ticker and
// composite_figi. Enrichment (figi/permid/sec/zacks) and date
// assignment must have run before this function is called.
func (api *massiveAssetFetcher) publish(ctx context.Context, asset *data.Asset) {
	logger := zerolog.Ctx(ctx)

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

	if reason, drop := api.shortDelistedNoCoverageReason(asset); drop {
		logger.Info().
			Str("Stage", "publish-drop-short-delisted-no-coverage").
			Str("Ticker", asset.Ticker).
			Str("CompositeFigi", asset.CompositeFigi).
			Str("Name", asset.Name).
			Str("ListingDate", asset.ListingDate).
			Str("DelistingDate", asset.DelistingDate).
			Str("Reason", reason).
			Msg("publish: dropping delisted asset whose window has no EOD coverage and is shorter than the minimum")

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
		SetQueryParam("limit", "1000").
		SetResult(&respContent)

	if assetType != "" {
		req = req.SetQueryParam("type", assetType)
	}

	asOfStr := ""
	if !asOfDate.IsZero() {
		asOfStr = asOfDate.Format("2006-01-02")
		req = req.SetQueryParam("date", asOfStr)
	}

	// Scope the walk to a single ticker when --ticker is set so a
	// targeted backfill doesn't fetch thousands of irrelevant rows
	// per snapshot date. The post-walk security filter still runs as
	// a defence-in-depth check; this is purely a server-side prune.
	// Massive uses `.` notation for class-share tickers (BF.A) while
	// the rest of pvdata uses `/` (BF/A), so convert back before the
	// API call.
	tickerFilter, _ := provider.SecurityFilterFromContext(ctx)
	if tickerFilter != "" {
		req = req.SetQueryParam("ticker", pvTicker2MassiveTicker(tickerFilter))
	}

	logger.Info().
		Str("URL", tickersURL).
		Str("Market", "stocks").
		Str("Active", "true").
		Str("AssetType", assetType).
		Str("Limit", "1000").
		Str("AsOfDate", asOfStr).
		Str("TickerFilter", tickerFilter).
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

// lastEODBarDelisting returns a delisting date string ("YYYY-MM-DD")
// derived from the latest EOD bar for ticker in the run's EOD archive,
// plus one calendar day. The convention matches chooseDelistingDate's
// massive_eod_archive_last_bar candidate: the asset's last observed
// trading day is the day before its delisting. Returns ("", false)
// when the archive is unavailable or has no bars for ticker. Used by
// delistedAssets to fill DelistingDate from a trading-evidence signal
// instead of a metadata timestamp.
func lastEODBarDelisting(api *massiveAssetFetcher, ticker string) (string, bool) {
	archive := api.eodArchiveForRun()
	if archive == nil {
		return "", false
	}

	ranges := archive.Ranges(ticker)
	if len(ranges) == 0 {
		return "", false
	}

	last := ranges[len(ranges)-1].End
	if last.IsZero() {
		return "", false
	}

	return last.AddDate(0, 0, 1).Format("2006-01-02"), true
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

	// Query Massive for inactive assets without an API-side type
	// filter; the post-fetch trackedTypeSet allowlist mirrors the live
	// walk so the two passes agree on which records to consider.
	tracked := trackedTypeSet(ctx)

	var respContent massiveResponse

	if err := api.limiter.Wait(ctx); err != nil {
		return err
	}

	const tickersURL = "https://api.massive.com/v3/reference/tickers"

	tickerFilter, _ := provider.SecurityFilterFromContext(ctx)

	logger.Info().
		Str("URL", tickersURL).
		Str("Active", "false").
		Str("Sort", "last_updated_utc").
		Str("Order", "desc").
		Str("Limit", "1000").
		Str("TickerFilter", tickerFilter).
		Msg("massive reference/tickers initial request (inactive lookup, no type filter)")

	req := api.client.R().
		SetQueryParam("active", "false").
		SetQueryParam("sort", "last_updated_utc").
		SetQueryParam("order", "desc").
		SetQueryParam("limit", "1000").
		SetResult(&respContent)

	if tickerFilter != "" {
		req = req.SetQueryParam("ticker", pvTicker2MassiveTicker(tickerFilter))
	}

	resp, err := req.Get(tickersURL)
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

		// Apply the post-fetch tracked-type allowlist. Build skinny
		// data.Asset records carrying only the fields the filter needs
		// (Ticker, CompositeFigi, AssetType, CIK), then pair each kept
		// record back to its raw massiveStock so we can read the
		// DelistDate / LastUpdated fields the filter doesn't touch.
		pageAssets := make([]*data.Asset, 0, len(massiveAssets))
		rawByKey := make(map[string]*massiveStock, len(massiveAssets))

		for _, ms := range massiveAssets {
			pvTicker := massiveTicker2PvTicker(ms.Ticker)
			pageAssets = append(pageAssets, &data.Asset{
				Ticker:        pvTicker,
				CompositeFigi: ms.CompositeFIGI,
				AssetType:     data.AssetType(ms.Type),
				CIK:           ms.CIK,
			})
			rawByKey[pvTicker+":"+ms.CompositeFIGI] = ms
		}

		pageAssets = filterToTrackedTypes(ctx, pageAssets, tracked)

		for _, asset := range pageAssets {
			inactiveAsset, ok := inactiveMap[asset.ID()]
			if !ok {
				continue
			}

			raw, ok := rawByKey[asset.Ticker+":"+asset.CompositeFigi]
			if !ok {
				continue
			}

			lastUpdated, err := time.Parse(time.RFC3339, raw.LastUpdated)
			if err != nil {
				logger.Error().Err(err).Str("Ticker", raw.Ticker).Msg("could not parse last updated string for tickers")
			}

			lastUpdated = lastUpdated.In(nyc)

			inactiveAsset.DelistingDate = strings.Split(raw.DelistDate, "T")[0]

			// active=false implies delisted must be set. Massive
			// returned this asset on the inactive endpoint but
			// occasionally without a delisted_utc; fall back to the
			// last EOD bar we have for the ticker (+1 calendar day).
			// last_updated_utc is metadata mtime — when Massive
			// touched the record, not when trading stopped — so it
			// is not a valid delisting signal.
			if inactiveAsset.DelistingDate == "" {
				if d, ok := lastEODBarDelisting(api, inactiveAsset.Ticker); ok {
					inactiveAsset.DelistingDate = d
				} else {
					// No trading evidence for a delisting date. The
					// active=false-implies-delisted-set invariant is
					// strict: we cannot flip Active without a date,
					// so we leave the existing DB row untouched and
					// surface the conflict for operator review. A
					// subsequent run after EOD coverage catches up
					// (or after Massive populates delisted_utc) will
					// resolve it.
					logger.Error().
						Str("Ticker", inactiveAsset.Ticker).
						Str("CompositeFigi", inactiveAsset.CompositeFigi).
						Msg("massive: Massive reports inactive but provided no delisted_utc and EOD archive has no bars; leaving DB row untouched to preserve the active=false-implies-delisted invariant")

					continue
				}
			}

			inactiveAsset.LastUpdated = lastUpdated
			inactiveAsset.Active = false
			deactivated[asset.ID()] = inactiveAsset
			api.publish(ctx, inactiveAsset)

			updatedCount++
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

	// find the disjoint set of Assets that are possibly inactive and
	// those that were deactivated. Assets can only appear in the
	// aforementioned set for a limited period of time before we mark
	// them as inactive. The stale-14-days path runs in both daily and
	// lookback contexts, so we set DelistingDate here from the EOD
	// archive's last bar for the ticker rather than relying on the
	// date-assignment chain (which only runs during lookbacks). The
	// EOD last bar is the right trading-evidence signal — it is when
	// the asset actually stopped trading, not a metadata mtime. The
	// active=false-implies-delisted-set invariant is strict, so when
	// the EOD archive has no bars for the ticker we do NOT flip
	// Active; we leave the row untouched and log an error.
	now := time.Now().In(nyc)

	for _, possibleInactiveAsset := range inactiveMap {
		if _, ok := deactivated[possibleInactiveAsset.ID()]; !ok {
			// asset was not de-activated ... check to see how old it is
			timeSinceLastUpdate := time.Since(possibleInactiveAsset.LastUpdated)
			if timeSinceLastUpdate > 14*24*time.Hour {
				// if asset hasn't been updated in the last 14 days mark as
				// inactive
				if possibleInactiveAsset.DelistingDate == "" {
					d, ok := lastEODBarDelisting(api, possibleInactiveAsset.Ticker)
					if !ok {
						logger.Error().
							Str("Ticker", possibleInactiveAsset.Ticker).
							Str("CompositeFigi", possibleInactiveAsset.CompositeFigi).
							Time("LastUpdated", possibleInactiveAsset.LastUpdated).
							Msg("massive: stale-14-days asset has no EOD archive bars; leaving DB row active to preserve the active=false-implies-delisted invariant")

						continue
					}

					possibleInactiveAsset.DelistingDate = d
				}

				possibleInactiveAsset.LastUpdated = now
				possibleInactiveAsset.Active = false

				api.publish(ctx, possibleInactiveAsset)
			}
		}
	}

	return nil
}

func massiveTicker2PvTicker(ticker string) string {
	return strings.ReplaceAll(ticker, ".", "/")
}

// pvTicker2MassiveTicker reverses massiveTicker2PvTicker for outbound
// API calls. Massive's `/v3/reference/tickers` endpoint uses `.` for
// class-share separators (BF.A) while the rest of pvdata uses `/`
// (BF/A). Sending `BF/A` returns empty results.
func pvTicker2MassiveTicker(ticker string) string {
	return strings.ReplaceAll(ticker, "/", ".")
}

const maxIconLogoFetchesPerRun = 100
const maxMissingBrandingPerRun = 100

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
