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
	"errors"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/goccy/go-json"
	"github.com/gosimple/slug"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/penny-vault/pvdata/provider/sec"
)

const defaultRateLimit = 600

var (
	ErrInvalidStatusCode = errors.New("invalid status code received")
	massiveExchangeMap   = map[string]data.Exchange{
		"XNAS": data.NasdaqExchange,
		"BATS": data.BATSExchange,
		"XASE": data.NYSEMktExchange,
		"XNYS": data.NYSEExchange,
	}
)

// massiveAssetFetcher carries the per-run REST client, rate limiter,
// and publish channel shared by the asset builder's goroutines. The
// catalog provider holds its own copy of this type (independent of
// provider/massive) so the two providers do not share state.
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
// the cooldown on its next Wait and sleeps until it expires.
type globalBackoff struct {
	mu    sync.Mutex
	until time.Time
}

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

// aggRow is the per-day aggregate parquet row format that Massive's
// EOD subscription writes. The EODArchive loader reads these rows; the
// catalog provider never writes them. Tags must match the writer's
// schema verbatim.
type aggRow struct {
	Ticker       string  `parquet:"ticker"`
	Volume       float64 `parquet:"volume"`
	Open         float64 `parquet:"open"`
	Close        float64 `parquet:"close"`
	High         float64 `parquet:"high"`
	Low          float64 `parquet:"low"`
	WindowStart  int64   `parquet:"window_start"`
	Transactions int64   `parquet:"transactions"`
}

// newMassiveRESTClient builds a resty client configured for the
// long-running paginated workloads we run against api.massive.com.
// HTTP/2 GOAWAY frames - sent by the server after extended idle or
// when shedding load - surface as transport errors that the default
// resty client treats as fatal, aborting long backfills.
// Exponential-backoff retry on transport errors and 5xx responses
// recovers transparently in those cases.
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

// subscriptionBackupSlug returns a stable per-subscription directory
// segment used to namespace flat-file backups so multiple subscriptions
// pointing at the same parquet_backup_dir do not overwrite one another.
// The format mirrors ComputeTableNames (slug + 5-char id suffix) so a
// backup directory is easy to correlate with its subscription row.
func subscriptionBackupSlug(sub *library.Subscription) string {
	s := slug.Make(fmt.Sprintf("%s %s", sub.Name, sub.ID.String()[:5]))
	return strings.ReplaceAll(s, "-", "_")
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

// marketIneligibleSuffixes is the set of ticker suffixes (after the
// slash separator pvdata uses for class-share / lifecycle decorations)
// that mark a record as a non-tradable instrument. Tickers ending in
// these are units (SPAC share + warrant bundles), warrants, rights,
// or when-distributed placeholders — none of which pvdata tracks.
var marketIneligibleSuffixes = []string{"/U", "/UN", "/W", "/WS", "/R", "/RT", "/WD"}

// marketIneligibleNamePatterns is the set of case-insensitive name
// substrings that mark a record as a placeholder / non-tradable
// security regardless of its asset_type.
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

// bondCouponMaturityPattern matches the coupon-and-maturity stamp
// that compact bond names carry — a percent rate immediately followed
// by a 2- or 4-digit maturity year (e.g. "7.15%38", "5%26",
// "10.25%2029"). Real equities and funds essentially never contain
// this shape.
//
// Caught examples from observed Massive reference rows:
//
//	VIRGINIA ELEC&PWR 98 A 7.15%38
//	GTE FLORIDA INC 1ST MTG 5.70%23
//	PUB SVC ELEC&GAS 1ST&REF MTG SR YY 7%24
var bondCouponMaturityPattern = regexp.MustCompile(`\b\d+(\.\d+)?%\d{2,4}\b`)

// bondNotesDuePattern matches the verbose "<coupon>% [Senior |Sub |...]
// Notes [D]ue <YYYY>" form Massive uses for exchange-traded debt
// instruments. The required "due YYYY" anchor keeps the regex from
// false-positiving on bond ETFs whose names contain "Bond" or "Notes"
// as descriptive tokens (e.g. "iShares Aggregate Bond Fund").
//
// Caught examples from observed catalog rows:
//
//	Abacus Global Management, Inc. 9.875% Fixed Rate Senior Notes due 2028
//	Adamas Trust, Inc. 9.125% Senior Notes Due 2030
//	Atlas Financial Holdings, Inc. 6.625% Senior Unsecured Notes Due 2022
//	Argo Group International Holdings, Ltd. 6.5% Senior Notes Due 2042
//	Atlas Corp. 7.125% Notes due 2027
//	eBay Inc. 6.0% Notes Due 2056
var bondNotesDuePattern = regexp.MustCompile(`(?i)\bnotes?\s+due\s+\d{4}\b`)

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

func nameSaysNonTradable(asset *data.Asset) (string, bool) {
	if asset == nil {
		return "", false
	}

	if bondCouponMaturityPattern.MatchString(asset.Name) {
		return "name_matches_bond_coupon_maturity", true
	}

	if bondNotesDuePattern.MatchString(asset.Name) {
		return "name_matches_bond_notes_due", true
	}

	lowerName := strings.ToLower(asset.Name)
	for _, pat := range marketIneligibleNamePatterns {
		if strings.Contains(lowerName, pat) {
			return "name_contains=" + strings.TrimSpace(pat), true
		}
	}

	return "", false
}

// nameSaysNonTradableForType skips csRebadgeNamePatterns when the
// Massive type is a fund or ADR class. The bond coupon-maturity
// regex fires unconditionally — funds and ADRs never carry that
// shape in their own name, so it is safe to keep even on the
// fund/ADR exempt path.
func nameSaysNonTradableForType(name, assetType string) (string, bool) {
	if bondCouponMaturityPattern.MatchString(name) {
		return "name_matches_bond_coupon_maturity", true
	}

	if bondNotesDuePattern.MatchString(name) {
		return "name_matches_bond_notes_due", true
	}

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

func marketIneligibleReason(asset *data.Asset) (string, bool) {
	if reason, drop := tickerSaysNonTradable(asset); drop {
		return reason, true
	}

	return nameSaysNonTradable(asset)
}

// defaultTrackedAssetTypes is the Massive `type` allowlist applied
// after the bulk /v3/reference/tickers response is fetched.
var defaultTrackedAssetTypes = []string{"CS", "ADRC", "ETF", "CEF", "ETN", "UNK"}

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
// and drops everything else.
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

// shortDelistedDurationDays is the minimum allowed duration for a
// delisted security whose listed-to-delisted window has no
// corresponding EOD archive coverage.
const shortDelistedDurationDays = 180

// shortDelistedNoCoverageReason reports whether a delisted asset
// should be dropped at publish time, and returns ("", false) when the
// asset should publish.
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

// publish sends the asset observation downstream after applying the
// publish-time guard rules. Assets still missing a composite_figi at
// this stage are dropped because the assets table primary key requires
// both ticker and composite_figi.
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
// stream.
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

// assets fetches every active=true record from Massive's reference
// endpoint and returns them as data.Asset rows. Used for the catalog
// Fetch's today's-snapshot step.
func (api *massiveAssetFetcher) assets(ctx context.Context, assetType string, asOfDate time.Time) ([]*data.Asset, error) {
	logger := zerolog.Ctx(ctx)

	if err := api.cooldown.Wait(ctx); err != nil {
		return nil, err
	}

	var respContent massiveResponse

	assets := make([]*data.Asset, 0, 6000)

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
		Msg("catalog reference/tickers initial request")

	resp, err := req.Get(tickersURL)
	if err != nil {
		logger.Error().Err(err).Msg("resty returned an error when querying reference/tickers")
		return assets, err
	}

	for ii := 0; ii < maxQueries; ii++ {
		if resp.StatusCode() >= 300 {
			logger.Error().Int("StatusCode", resp.StatusCode()).Str("ResponseBody", string(resp.Body())).
				Str("URL", tickersURL).
				Msg("received an invalid status code when querying massive reference/tickers endpoint")

			return assets, fmt.Errorf("%w (%d): %s", ErrInvalidStatusCode, resp.StatusCode(), string(resp.Body()))
		}

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

		if respContent.Next == "" {
			break
		}

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

const maxIconLogoFetchesPerRun = 100
const maxMissingBrandingPerRun = 100

// parseIconLogoLimit reads the iconLogoLimit subscription config and
// returns (budget, missingBrandingCap).
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

// brandingBudget caps how many icon/logo HTTP fetches a single run
// performs. A non-positive cap means unlimited. Allow() is safe for
// concurrent use so parallel assetDetails workers can share one budget.
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
