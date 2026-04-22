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

package sec

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
)

// edgarFeedMaxPages caps the number of EDGAR feed pages fetched in a single
// incremental run. At edgarFeedPageSize=100 this allows up to 5,000 filings.
const edgarFeedMaxPages = 50

func init() {
	provider.Register("sec", &SEC{})
}

type SEC struct{}

func (s *SEC) Name() string {
	return "SEC"
}

func (s *SEC) Description() string {
	return "SEC EDGAR fundamentals extracted from 10-K and 10-Q XBRL filings via the companyfacts API"
}

func (s *SEC) ConfigDescription() map[string]string {
	return map[string]string{
		"userAgent": "Email address for SEC User-Agent header (e.g. pvdata/1.0 user@email.com):",
		"rateLimit": "Maximum requests per second to SEC EDGAR (default 10):",
	}
}

func (s *SEC) Datasets() map[string]provider.Dataset {
	return map[string]provider.Dataset{
		"Fundamentals": {
			Name:        "Fundamentals",
			Description: "Financial statement fundamentals from SEC EDGAR XBRL filings (10-K and 10-Q).",
			DataTypes:   []*data.DataType{data.DataTypes[data.FundamentalsKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2009, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: fetchFundamentals,
		},
	}
}

func fetchFundamentals(ctx context.Context, sub *library.Subscription, out chan<- *data.Observation, exit chan<- data.RunSummary) {
	runSummary := data.RunSummary{
		StartTime:        time.Now(),
		SubscriptionID:   sub.ID,
		SubscriptionName: sub.Name,
	}

	numObservations := 0

	defer func() {
		runSummary.EndTime = time.Now().UTC()
		runSummary.NumObservations = numObservations

		if runSummary.Status != data.RunFailed {
			runSummary.Status = data.RunSuccess
		}

		exit <- runSummary
	}()

	userAgent := sub.Config["userAgent"]
	if userAgent == "" {
		log.Error().Msg("SEC provider requires userAgent config")

		runSummary.Status = data.RunFailed

		return
	}

	reqPerSec := 10

	if rateStr, ok := sub.Config["rateLimit"]; ok {
		if r, err := strconv.Atoi(rateStr); err == nil && r > 0 {
			reqPerSec = r
		}
	}

	limiter := rate.NewLimiter(rate.Limit(reqPerSec), 1)
	client := NewSECClient(userAgent, limiter)

	// Load CIK -> asset map from database (primary lookup path).
	// dbTickerMap indexes every asset by ticker so we can resolve tickers
	// that share a CIK (e.g. JPM, AMJ, AMJB, VYLD all map to CIK 19617).
	// siblingFigiMap maps each FIGI of a dual-class filer (e.g. BRK.A/BRK.B)
	// to the other class's FIGI; consumed by EnrichMarketData to fetch the
	// sibling-class price for the market-ratio share_factor formula.
	dbCIKMap, dbTickerMap, siblingFigiMap, err := LoadCIKMapFromDB(ctx, sub.Library.Pool)
	if err != nil {
		log.Error().Err(err).Msg("error loading CIK map from database")

		runSummary.Status = data.RunFailed

		return
	}

	// Fetch SEC's company_tickers.json as a fallback for CIKs not in the
	// database. This file is fetched fresh on each run so we always pick up
	// SEC's latest mappings (the file changes daily).
	secTickers, err := FetchCompanyTickers(ctx, client)
	if err != nil {
		// Don't fail the whole run if the fallback fetch fails -- the DB map
		// may still cover the universe we care about. Log and proceed.
		log.Warn().Err(err).Msg("error fetching SEC company_tickers.json; proceeding with DB CIK map only")

		secTickers = nil
	}

	cikMap := make(map[int]AssetInfo, len(dbCIKMap)+len(secTickers))
	for cik, info := range dbCIKMap {
		if sibling, ok := siblingFigiMap[info.CompositeFigi]; ok {
			info.SiblingFigi = sibling
		}

		cikMap[cik] = info
	}

	fromSEC := 0

	for cik, entry := range secTickers {
		if _, ok := cikMap[cik]; ok {
			continue
		}

		// CIK not in DB: add an entry with the SEC ticker but no FIGI.
		// SaveDB drops fundamentals with empty CompositeFigi, so these
		// observations will not be persisted -- emitFundamentals tracks
		// the count and reports it in the run summary so users know
		// there's a coverage gap.
		cikMap[cik] = AssetInfo{
			Ticker:        entry.Ticker,
			CompositeFigi: "",
			CIK:           cik,
		}

		fromSEC++
	}

	log.Info().
		Int("total_ciks", len(cikMap)).
		Int("from_db", len(dbCIKMap)).
		Int("from_sec", fromSEC).
		Msg("built combined CIK map")

	// Apply ticker/FIGI filter if set. Multiple tickers can share a single
	// CIK, so the CIK map may store a different ticker than the one
	// requested. We fall back to dbTickerMap which indexes every DB asset
	// by ticker, ensuring e.g. --ticker JPM resolves even when the CIK map
	// entry for CIK 19617 holds VYLD.
	tickerFilter, figiFilter := provider.SecurityFilterFromContext(ctx)
	if tickerFilter != "" || figiFilter != "" {
		filtered := make(map[int]AssetInfo)

		for cik, info := range cikMap {
			if tickerFilter != "" && strings.EqualFold(info.Ticker, tickerFilter) {
				filtered[cik] = info
			} else if figiFilter != "" && info.CompositeFigi == figiFilter {
				filtered[cik] = info
			}
		}

		// Ticker not found via CIK map scan — try the ticker index which
		// covers all DB assets regardless of CIK collisions.
		if len(filtered) == 0 && tickerFilter != "" {
			if asset, ok := dbTickerMap[strings.ToUpper(tickerFilter)]; ok {
				if sibling, ok := siblingFigiMap[asset.CompositeFigi]; ok {
					asset.SiblingFigi = sibling
				}

				filtered[asset.CIK] = asset
			}
		}

		if len(filtered) == 0 {
			candidates := make([]string, 0, len(cikMap))
			for _, info := range cikMap {
				if tickerFilter != "" {
					candidates = append(candidates, info.Ticker)
				} else {
					candidates = append(candidates, info.CompositeFigi)
				}
			}

			// Also include tickers from the full DB index so the fuzzy
			// suggestions cover all known tickers, not just CIK-map
			// survivors.
			if tickerFilter != "" {
				for t := range dbTickerMap {
					candidates = append(candidates, t)
				}
			}

			input := tickerFilter
			if input == "" {
				input = figiFilter
			}

			suggestions := provider.SuggestMatch(input, candidates)
			if len(suggestions) > 0 {
				log.Error().Str("input", input).Strs("suggestions", suggestions).Msg("security not found in SEC universe; did you mean one of these?")
			} else {
				log.Error().Str("input", input).Msg("security not found in SEC universe")
			}

			runSummary.Status = data.RunFailed

			return
		}

		cikMap = filtered

		log.Info().Int("filtered_ciks", len(filtered)).Msg("applied security filter to CIK map")
	}

	// Set up EOD price lookup for market-data enrichment.
	eodView := FindEODViewName(ctx, sub.Library.Pool)
	if eodView != "" {
		SetPriceLookupFn(NewEODPriceLookup(ctx, sub.Library.Pool, eodView))
		log.Info().Str("eod_view", eodView).Msg("market-data enrichment enabled")
	} else {
		SetPriceLookupFn(nil)
		log.Warn().Msg("no published EOD view found; market-data fields will be zero")
	}

	skippedMissingFIGI := 0

	// Determine the cutoff date for which periods to emit. If --lookback is
	// set it takes precedence; otherwise fall back to the subscription's
	// last observation date (zero means full backfill).
	since := sub.LastObsDate
	if lookback := provider.LookbackFromContext(ctx, 0); lookback > 0 {
		since = time.Now().Add(-lookback)
	}

	isBackfill := sub.LastObsDate.IsZero()
	if isBackfill {
		if err := runBackfill(ctx, client, cikMap, sub, since, out, &numObservations, &skippedMissingFIGI); err != nil {
			runSummary.Status = data.RunFailed
			return
		}
	} else {
		if err := runIncremental(ctx, client, cikMap, sub, since, out, &numObservations, &skippedMissingFIGI); err != nil {
			runSummary.Status = data.RunFailed
			return
		}
	}

	if skippedMissingFIGI > 0 {
		log.Warn().
			Int("skipped_ciks_missing_figi", skippedMissingFIGI).
			Msg("CIKs were resolved via SEC company_tickers.json but skipped because no composite FIGI is known; run OpenFIGI lookup to close coverage gap")
	}
}

// individualFetchThreshold is the maximum number of CIKs for which we fetch
// individual companyfacts JSON files instead of downloading the full ~2GB
// companyfacts.zip. Individual fetches are much faster for small runs (e.g.
// single-ticker development) but don't scale for full backfills.
const individualFetchThreshold = 20

func runBackfill(ctx context.Context, client *resty.Client, cikMap map[int]AssetInfo, sub *library.Subscription, since time.Time, out chan<- *data.Observation, numObservations, skippedMissingFIGI *int) error {
	processed := 0
	processErrors := 0

	// Track the latest-period coverage (number of resolved fields) for each
	// company so we can report a coverage distribution in the run summary.
	// Companies with no periods emitted are not included.
	coverageSamples := make([]int, 0, len(cikMap))

	processCompany := func(cik int, cf *CompanyFacts) {
		asset, ok := cikMap[cik]
		if !ok {
			return
		}

		if asset.CompositeFigi == "" {
			*skippedMissingFIGI++
			return
		}

		latestCoverage, periodsEmitted := emitFundamentals(cf, asset, sub, since, out, numObservations)
		if periodsEmitted > 0 {
			coverageSamples = append(coverageSamples, latestCoverage)
		}

		processed++
		if processed%1000 == 0 {
			log.Info().Int("processed", processed).Msg("backfill progress")
		}
	}

	localZip := provider.CompanyFactsZipFromContext(ctx)

	// When the CIK set is small and no local zip is specified, fetch
	// individual companyfacts JSONs from the SEC API. This avoids
	// downloading the full ~2GB companyfacts.zip for single-ticker or
	// small-batch runs.
	if localZip == "" && len(cikMap) <= individualFetchThreshold {
		log.Info().Int("ciks", len(cikMap)).Msg("fetching individual companyfacts from SEC API")

		for cik := range cikMap {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			cf, err := FetchCompanyFacts(ctx, client, cik)
			if err != nil {
				processErrors++

				log.Warn().Err(err).Int("cik", cik).Msg("error fetching companyfacts")

				continue
			}

			// Enrich with extension facts from the actual filing documents.
			// The companyfacts API only includes us-gaap and dei namespaces;
			// company extension tags (e.g. msft:DepreciationAmortizationAndOther)
			// are only available in the inline XBRL filings.
			EnrichWithExtensionFacts(ctx, client, cik, cf)

			// Apply filing cutoff if set — remove facts filed after the cutoff date.
			// This must happen after enrichment so extension facts are also filtered.
			if cutoff, ok := provider.FilingCutoffFromContext(ctx); ok {
				cf.FilterByFilingDate(cutoff)
			}

			processCompany(cik, cf)
		}
	} else {
		err := DownloadCompanyFactsZip(ctx, client, localZip, func(cik int, jsonData []byte) error {
			cf, err := ParseCompanyFacts(jsonData)
			if err != nil {
				processErrors++

				log.Warn().Err(err).Int("cik", cik).Msg("error parsing companyfacts in backfill")

				return nil
			}

			// Apply filing cutoff if set.
			if cutoff, ok := provider.FilingCutoffFromContext(ctx); ok {
				cf.FilterByFilingDate(cutoff)
			}

			processCompany(cik, cf)

			return nil
		})
		if err != nil {
			log.Error().Err(err).Msg("error during backfill")
			return err
		}
	}

	if processErrors > 0 {
		log.Warn().
			Int("process_errors", processErrors).
			Int("total_processed", processed).
			Msg("backfill completed with processing errors")
	}

	logCoverageDistribution(coverageSamples)

	log.Info().
		Int("total_processed", processed).
		Int("process_errors", processErrors).
		Msg("backfill complete")

	return nil
}

// logCoverageDistribution logs percentile statistics of per-company
// latest-period coverage over the full backfill. This helps surface XBRL
// mapping gaps: a low p50 signals a systemic mapping problem, while a large
// gap between p50 and p99 typically reflects companies reporting only a
// subset of the expected tags (e.g. funds or shell companies).
func logCoverageDistribution(samples []int) {
	if len(samples) == 0 {
		return
	}

	sort.Ints(samples)

	sum := 0
	for _, v := range samples {
		sum += v
	}

	percentile := func(p float64) int {
		if len(samples) == 0 {
			return 0
		}

		idx := int(float64(len(samples)-1) * p)

		return samples[idx]
	}

	log.Info().
		Int("companies", len(samples)).
		Int("total_mappings", len(FieldMappings)).
		Int("min", samples[0]).
		Int("p50", percentile(0.50)).
		Int("p90", percentile(0.90)).
		Int("p99", percentile(0.99)).
		Int("max", samples[len(samples)-1]).
		Int("avg", sum/len(samples)).
		Msg("sec backfill field coverage distribution")
}

// fetchEDGARFeed fetches a single page of the EDGAR ATOM filing feed starting
// at the given offset. It returns the parsed filing entries for that page.
func fetchEDGARFeed(ctx context.Context, client *resty.Client, start int) ([]FilingEntry, error) {
	url := fmt.Sprintf(edgarFeedURLFormat, start)

	resp, err := client.R().SetContext(ctx).Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetching EDGAR feed page (start=%d): %w", start, err)
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("SEC returned status %d for EDGAR feed page (start=%d)", resp.StatusCode(), start)
	}

	filings, err := ParseFilingFeed(resp.Body())
	if err != nil {
		return nil, fmt.Errorf("parsing EDGAR feed page (start=%d): %w", start, err)
	}

	return filings, nil
}

func runIncremental(ctx context.Context, client *resty.Client, cikMap map[int]AssetInfo, sub *library.Subscription, since time.Time, out chan<- *data.Observation, numObservations, skippedMissingFIGI *int) error {
	// Paginate through the EDGAR feed, collecting unique CIKs filed on/after
	// since. Stop when we hit a page containing entries older than since, or
	// when we exhaust the hard page cap.
	seen := make(map[int]bool)
	hitLimit := true

	var ciksToFetch []int

pageLoop:
	for page := 0; page < edgarFeedMaxPages; page++ {
		start := page * edgarFeedPageSize

		filings, err := fetchEDGARFeed(ctx, client, start)
		if err != nil {
			log.Error().Err(err).Int("start", start).Msg("error fetching EDGAR filing feed page")

			return err
		}

		if len(filings) == 0 {
			hitLimit = false

			break
		}

		reachedCutoff := false

		for _, filing := range filings {
			if filing.Filed.Before(since) {
				// Once we see a filing older than since we've gone past the
				// cutoff. Finish processing this page (in case ordering isn't
				// strict) and stop paginating.
				reachedCutoff = true
				continue
			}

			if seen[filing.CIK] {
				continue
			}

			seen[filing.CIK] = true

			if _, ok := cikMap[filing.CIK]; !ok {
				continue
			}

			ciksToFetch = append(ciksToFetch, filing.CIK)
		}

		if reachedCutoff {
			hitLimit = false

			break pageLoop
		}
	}

	if hitLimit {
		log.Warn().
			Int("max_pages", edgarFeedMaxPages).
			Int("page_size", edgarFeedPageSize).
			Time("since", since).
			Msg("EDGAR feed pagination hit hard page limit; some filings may have been missed")
	}

	for _, cik := range ciksToFetch {
		asset := cikMap[cik]

		// Skip CIKs without a composite FIGI: SaveDB drops these records,
		// so fetching companyfacts would only waste an SEC API call.
		if asset.CompositeFigi == "" {
			*skippedMissingFIGI++

			continue
		}

		cf, err := FetchCompanyFacts(ctx, client, cik)
		if err != nil {
			log.Warn().Err(err).Int("cik", cik).Msg("error fetching companyfacts")
			continue
		}

		emitFundamentals(cf, asset, sub, since, out, numObservations)
	}

	return nil
}

// coverageWarnThresholdPct is the minimum percentage of FieldMappings that
// should resolve for a company's most recent period before emitFundamentals
// logs a warning. Most healthy filers resolve well above this line; dropping
// below it usually indicates a company with non-standard XBRL tags that are
// missing from our fallback list.
const coverageWarnThresholdPct = 50

// emitFundamentals processes a CompanyFacts into data.Fundamental observations
// for all 6 dimensions and sends them to the output channel.
//
// The since parameter limits emission to periods filed on or after that time.
// Pass time.Time{} (zero value) to emit every period without filtering.
// MRFiledDate is used for the comparison so that restatements filed after
// since are re-emitted even if the period itself is older.
//
// copyFieldMap returns a shallow copy of a resolved field map.
func copyFieldMap(m map[string]float64) map[string]float64 {
	cp := make(map[string]float64, len(m))
	for k, v := range m {
		cp[k] = v
	}

	return cp
}

// Returns the number of fields resolved for the company's most recent period
// and the count of non-TTM periods emitted (ARQ + ARY). The caller uses these
// to track coverage statistics across the run. latestPeriodCoverage is zero if
// no periods were identified.
func emitFundamentals(cf *CompanyFacts, asset AssetInfo, sub *library.Subscription, since time.Time, out chan<- *data.Observation, numObservations *int) (latestPeriodCoverage, periodsEmitted int) {
	periods := IdentifyPeriods(cf)

	// Track coverage for the chronologically most recent period. We record
	// coverage regardless of the since filter so incremental runs still see
	// the true latest-period coverage for the company.
	var latestPeriodEnd time.Time

	// Collect quarterly periods for de-cumulation and TTM computation. All
	// quarters are kept (even those filtered by since) so TTM windows and
	// de-cumulation chains remain complete; the since check is reapplied when
	// emitting.
	type quarterData struct {
		period        Period
		arFields      map[string]float64 // original resolved values (may be YTD for cash flow)
		mrFields      map[string]float64 // original resolved values (may be YTD for cash flow)
		arEmit        map[string]float64 // de-cumulated values for emission and TTM
		mrEmit        map[string]float64 // de-cumulated values for emission and TTM
		arCumPerShare map[string]float64 // YTD cumulative per-share flow values (AR view)
		mrCumPerShare map[string]float64 // YTD cumulative per-share flow values (MR view)
	}

	var quarters []quarterData

	var annuals []quarterData

	var buffered []*data.Observation

	// Identify fields whose underlying XBRL concepts the company has stopped
	// reporting. A concept is stale if it appeared in older filings but not in
	// the latest 10-K. Stale fields are stripped from MR field maps so the MR
	// view reflects the company's current reporting practice.
	staleMRFields := identifyStaleMRFields(cf)

	for _, p := range periods {
		// AR: resolve using only facts available at the earliest filing date
		arFields := ResolveFieldsForFiling(cf, p.PeriodEnd, p.FormType, p.ARFiledDate)

		// MR: resolve using all facts including restatements. When AR and MR
		// filing dates are the same (no restatements -- the common case), the
		// resolved field maps are identical, so reuse the AR map. The maps are
		// treated as read-only after this point so sharing the reference is
		// safe.
		var mrFields map[string]float64
		if p.ARFiledDate.Equal(p.MRFiledDate) {
			mrFields = arFields
		} else {
			mrFields = ResolveFieldsForFiling(cf, p.PeriodEnd, p.FormType, p.MRFiledDate)

			// Fill gaps in AR data from MR. Later filings sometimes add
			// comparative values for XBRL concepts that were not present in
			// the original filing (e.g. MSFT started reporting short-term
			// debt proceeds separately in FY2025 and included FY2024
			// comparatives). Sharadar's AR dimension includes such
			// back-filled values, so we merge them to match.
			filled := false

			for k, v := range mrFields {
				if _, ok := arFields[k]; !ok {
					arFields[k] = v
					filled = true
				}
			}

			// Recompute derived fields if any gaps were filled. Only
			// recompute fields that don't have FallbackTags, since those
			// with FallbackTags may have been resolved from a tag that is
			// more accurate than the formula (e.g. D&A resolved from a
			// combined tag rather than individual components).
			if filled {
				for _, m := range FieldMappings {
					if m.Type != MappingDerived || len(m.FallbackTags) > 0 {
						continue
					}

					if val, ok := computeDerived(m, arFields); ok {
						arFields[m.FieldName] = val
					}
				}
			}

		}

		// Track all quarters regardless of since so TTM windows are complete.
		if p.FormType == "10-Q" {
			arCumPS := ResolveCumulativePerShareForFiling(cf, p.PeriodEnd, p.FormType, p.ARFiledDate)

			var mrCumPS map[string]float64
			if p.ARFiledDate.Equal(p.MRFiledDate) {
				mrCumPS = arCumPS
			} else {
				mrCumPS = ResolveCumulativePerShareForFiling(cf, p.PeriodEnd, p.FormType, p.MRFiledDate)
			}

			quarters = append(quarters, quarterData{
				period: p, arFields: arFields, mrFields: mrFields,
				arCumPerShare: arCumPS, mrCumPerShare: mrCumPS,
			})
		}

		// Track the chronologically latest period's coverage. Done before the
		// since filter so the reported coverage reflects the true most recent
		// period, not just the newest period emitted this run.
		if p.PeriodEnd.After(latestPeriodEnd) {
			latestPeriodEnd = p.PeriodEnd
			latestPeriodCoverage = len(arFields)
		}

		if p.FormType == "10-K" {
			// Pull StmtPointInTime values that only appear on later 10-Q
			// comparatives (LLY tags TaxesPayableCurrent at end=2024-12-31
			// only on Q1/Q2/Q3 2025 10-Q comparative balance sheets, never
			// on the original 10-K). Merge into mrFields so the synthesized
			// Q4 quarter's MR view picks up the comparative balance. Filter
			// to the latest filing date across ALL form types so that 10-Q
			// comparatives filed after the 10-K are visible (the period's
			// MRFiledDate is restricted to 10-K filings).
			latestFiledForPeriod := p.MRFiledDate

			for _, facts := range cf.Facts {
				for i := range facts {
					if facts[i].End.Equal(p.PeriodEnd) && facts[i].Filed.After(latestFiledForPeriod) {
						latestFiledForPeriod = facts[i].Filed
					}
				}
			}

			mrFromQuarterly := ResolveFieldsForFiling(cf, p.PeriodEnd, "10-Q", latestFiledForPeriod)
			derivedRecompute := false

			for _, m := range FieldMappings {
				if m.StatementType != StmtPointInTime {
					continue
				}

				if _, has := mrFields[m.FieldName]; has {
					continue
				}

				v, ok := mrFromQuarterly[m.FieldName]
				if !ok {
					continue
				}

				// Detach from arFields before mutating (mrFields may share the
				// arFields map when no restatements were observed in the 10-K).
				if !derivedRecompute {
					mrFieldsCopy := copyFieldMap(mrFields)
					mrFields = mrFieldsCopy
				}

				mrFields[m.FieldName] = v
				derivedRecompute = true
			}

			if derivedRecompute {
				// Only recompute StmtPointInTime Derived fields — the only
				// kind whose operands can change from the StmtPointInTime
				// values just merged in. Recomputing StmtFlow Derived (e.g.
				// D&A) here would overwrite values resolved via FallbackTag
				// in the original 10-K resolution with operand-sum values.
				for _, m := range FieldMappings {
					if m.Type != MappingDerived || m.StatementType != StmtPointInTime {
						continue
					}

					// For fields with FallbackTags, only recompute when the
					// FallbackTag chain did not actually resolve a value
					// (i.e., the existing value came from the formula too).
					if len(m.FallbackTags) > 0 {
						if _, hasInQuarterly := mrFromQuarterly[m.FieldName]; hasInQuarterly {
							qFormula, qOk := computeDerived(m, mrFromQuarterly)
							if !qOk || mrFromQuarterly[m.FieldName] != qFormula {
								continue
							}
						}
					}

					if val, ok := computeDerived(m, mrFields); ok {
						mrFields[m.FieldName] = val
					}
				}
			}

			annuals = append(annuals, quarterData{period: p, arFields: arFields, mrFields: mrFields})
		}
	}

	// De-cumulate YTD cash flow values for quarterly periods. SEC 10-Q filings
	// report cash flow items as year-to-date cumulative values only. For Q1 the
	// YTD equals the quarterly value; for Q2/Q3 we subtract the prior quarter's
	// YTD to isolate the single-quarter amount.
	//
	// Consecutive quarters within the same fiscal year are identified by a gap
	// of <= 120 days between period ends. A larger gap (e.g. Q3 -> Q1 across a
	// 10-K boundary) means the current quarter is a new fiscal year's Q1 and
	// its YTD value is already the quarterly value.
	const maxQuarterGapDays = 120

	for i := range quarters {
		q := &quarters[i]

		if i > 0 {
			prev := &quarters[i-1]
			gapDays := q.period.PeriodEnd.Sub(prev.period.PeriodEnd).Hours() / 24

			if gapDays <= maxQuarterGapDays {
				// Consecutive quarter in same fiscal year: de-cumulate using
				// the prior quarter's ORIGINAL (pre-de-cumulation) YTD values.
				q.arEmit = DecumulateYTD(cf, q.arFields, prev.arFields, prev.period.PeriodEnd, q.period.PeriodEnd, q.period.FormType, q.period.ARFiledDate)
				q.mrEmit = DecumulateYTD(cf, q.mrFields, prev.mrFields, prev.period.PeriodEnd, q.period.PeriodEnd, q.period.FormType, q.period.MRFiledDate)

				applyMRComparativeFilter(cf, q.mrEmit, q.period.PeriodEnd)

				continue
			}
		}

		// Q1 or no prior quarter: no de-cumulation needed. Copy the maps
		// so that later MR-only overrides (e.g. SharesBasic) don't leak
		// into the AR emit maps when AR and MR share the same reference.
		q.arEmit = copyFieldMap(q.arFields)
		q.mrEmit = copyFieldMap(q.mrFields)

		applyMRComparativeFilter(cf, q.mrEmit, q.period.PeriodEnd)
	}

	// Synthesize Q4 entries from 10-K annual data and preceding quarters.
	// Each synthesized Q4 is inserted into the sorted quarters slice so it
	// participates in TTM computation and is emitted as ARQ/MRQ.
	for _, a := range annuals {
		// Rebuild inputs each iteration so previously synthesized Q4s are
		// available as preceding quarters for later annuals.
		inputs := make([]synthesizeInput, len(quarters))
		for i, q := range quarters {
			inputs[i] = synthesizeInput{
				periodEnd:     q.period.PeriodEnd,
				arEmit:        q.arEmit,
				mrEmit:        q.mrEmit,
				arCumPerShare: q.arCumPerShare,
				mrCumPerShare: q.mrCumPerShare,
			}
		}

		arQ4, mrQ4 := SynthesizeQ4(a.arFields, a.mrFields, a.period, inputs)
		if arQ4 == nil {
			continue
		}

		// Check if a quarter already exists at this period end. If it does
		// but lacks revenue data (e.g. a phantom quarter from comparative
		// balance sheet data in a subsequent 10-Q), replace it with the
		// synthesized Q4 which has complete flow data.
		existingIdx := -1

		for idx, q := range quarters {
			if q.period.PeriodEnd.Equal(a.period.PeriodEnd) {
				existingIdx = idx

				break
			}
		}

		if existingIdx >= 0 {
			existingQ := &quarters[existingIdx]

			_, hasRevAR := existingQ.arEmit["Revenues"]
			_, hasRevMR := existingQ.mrEmit["Revenues"]

			if hasRevAR || hasRevMR {
				// Existing quarter has revenue data — keep it.
				continue
			}

			// Replace the phantom quarter with the synthesized Q4.
			quarters[existingIdx] = quarterData{
				period: Period{
					PeriodEnd:   a.period.PeriodEnd,
					FormType:    "10-Q",
					ARFiledDate: a.period.ARFiledDate,
					MRFiledDate: a.period.MRFiledDate,
				},
				arFields: arQ4,
				mrFields: mrQ4,
				arEmit:   arQ4,
				mrEmit:   mrQ4,
			}

			continue
		}

		q4 := quarterData{
			period: Period{
				PeriodEnd:   a.period.PeriodEnd,
				FormType:    "10-Q",
				ARFiledDate: a.period.ARFiledDate,
				MRFiledDate: a.period.MRFiledDate,
			},
			arFields: arQ4,
			mrFields: mrQ4,
			arEmit:   arQ4,
			mrEmit:   mrQ4,
		}

		// Insert into sorted position by PeriodEnd.
		idx := sort.Search(len(quarters), func(i int) bool {
			return quarters[i].period.PeriodEnd.After(q4.period.PeriodEnd)
		})

		quarters = append(quarters, quarterData{})
		copy(quarters[idx+1:], quarters[idx:])
		quarters[idx] = q4
	}

	// Override DPS with cash-paid methodology for all quarters (including
	// synthesized Q4). This must happen after de-cumulation and Q4 synthesis
	// so _absDividendsPaid is the correct single-quarter cash amount.
	for i := range quarters {
		OverrideDPSFromCash(cf, quarters[i].arEmit, false, quarters[i].period.PeriodEnd, false)
		OverrideDPSFromCash(cf, quarters[i].mrEmit, true, quarters[i].period.PeriodEnd, false)
	}

	// Override SharesBasic in mrEmit for MR dimensions. Sharadar's MR
	// semantics use the EntityCommonStockSharesOutstanding from the most
	// recently filed 10-K/10-Q as of the period end date (the latest known
	// cover-page shares), NOT from the filing that reports the period's own
	// data. Apply the override before annual averaging and emission so MRQ,
	// MRT, and MRY all see the corrected value.
	for i := range quarters {
		if val, ok := resolveSharesBasicAsOf(cf, quarters[i].period.PeriodEnd); ok {
			quarters[i].mrEmit["SharesBasic"] = val
		}
	}

	// Override SharesBasic in arEmit for AR dimensions. The standard
	// ResolveDirect matches EntityCommonStockSharesOutstanding by normalized
	// date, which fails for companies whose DEI end dates are in a different
	// quarter than the fiscal period end (e.g., NVDA's January FY has DEI
	// dates in February that normalize to the next quarter). Use the same
	// filing-date resolution with the AR filing date to pick the DEI fact
	// from the actual filing.
	//
	// For synthesized Q4 quarters (whose period end matches an annual), use
	// the annual-AR resolver so CAT-style filers (10-K cover page discloses
	// only the fiscal-Q2 shares) get the mid-year value.
	annualPeriodEnds := make(map[time.Time]bool, len(annuals))
	for _, a := range annuals {
		annualPeriodEnds[a.period.PeriodEnd] = true
	}

	for i := range quarters {
		q := &quarters[i]

		var (
			val float64
			ok  bool
		)

		if annualPeriodEnds[q.period.PeriodEnd] {
			val, ok = resolveSharesBasicForAnnualAR(cf, q.period.ARFiledDate, q.period.PeriodEnd)
		} else {
			val, ok = resolveSharesBasicAsOf(cf, q.period.ARFiledDate)
		}

		if ok {
			q.arEmit["SharesBasic"] = val
		}
	}

	// Strip stale fields from MR quarterly emit maps and recompute derived
	// fields. This must happen before TTM computation so that trailing sums
	// use the corrected MR values.
	for i := range quarters {
		stripStaleAndRecompute(quarters[i].mrEmit, staleMRFields, cf)
	}

	// Apply bank-specific post-processing (TotalDebt/InvestedCapital/NCFDebt
	// overrides, SGA recompute, etc.) to quarter emit maps before annual
	// balance-sheet averages are computed. Without this, the 4-quarter
	// averages in ComputeMultiQAverages see the mapping-level values that
	// precede the bank overrides, yielding a stale InvestedCapitalAverage
	// for ARY/MRY on bank filers.
	for i := range quarters {
		overrideNCFDebtResidual(cf, quarters[i].arEmit, quarters[i].period.PeriodEnd, quarters[i].period.FormType)
		overrideNCFDebtResidual(cf, quarters[i].mrEmit, quarters[i].period.PeriodEnd, quarters[i].period.FormType)
	}

	// For banks, override MR DPS with prior quarter's declared rate (= cash-
	// paid). Must happen before annual emission so MRY can sum the corrected
	// cash-paid quarterly values. Skip when the filer reports
	// PreferredStockDividendsIncomeStatementImpact: in that case the preferred
	// deduction is already represented on the income statement, and Sharadar
	// uses the declared per-share value directly (GS pattern) rather than the
	// cash-paid prior-quarter shift (JPM pattern). This mirrors the NetIncome
	// tag selection: the same marker distinguishes the two styles.
	hasPreferredISImpact := conceptFiledQuarterly(cf, []string{
		"PreferredStockDividendsIncomeStatementImpact",
		"PreferredStockDividendsAndOtherAdjustments",
	})
	if !conceptFiledQuarterly(cf, []string{"AssetsCurrent"}) && !hasPreferredISImpact {
		for i := 1; i < len(quarters); i++ {
			prev := &quarters[i-1]
			prevDPS, found := prev.arFields["DividendsPerBasicCommonShare"]

			if !found {
				prevDPS, found = prev.arEmit["DividendsPerBasicCommonShare"]
			}

			if !found {
				prevDPS, found = prev.mrEmit["DividendsPerBasicCommonShare"]
			}

			if !found && i > 1 {
				grandPrev := &quarters[i-2]

				prevDPS, found = grandPrev.arFields["DividendsPerBasicCommonShare"]

				if !found {
					prevDPS, found = grandPrev.arEmit["DividendsPerBasicCommonShare"]
				}
			}

			if found {
				quarters[i].mrEmit["DividendsPerBasicCommonShare"] = prevDPS
			}
		}
	}

	// Period-average fields (AverageAssets, EquityAvg, InvestedCapitalAverage)
	// and derived ratios (ROA, ROE, ROIC) are intentionally NOT computed for
	// quarterly dimensions (ARQ/MRQ). See #56 for rationale.

	// findConstituentQuarters returns the quarterly entries whose PeriodEnd
	// falls within ~365 days before annualEnd (inclusive). This gives us the
	// 4 fiscal-year quarters for 4-quarter balance sheet averaging.
	//
	// The 370-day window can pick up the prior year's synthesized Q4 (which
	// shares the same period end as the prior annual). If that happens the
	// slice is capped to the 4 most recent quarters so the average matches
	// Sharadar's 4-point methodology (current FY quarters only, excluding
	// the prior year-end snapshot).
	findConstituentQuarters := func(qs []quarterData, annualEnd time.Time) []quarterData {
		const maxDays = 370

		var result []quarterData

		for idx := range qs {
			days := annualEnd.Sub(qs[idx].period.PeriodEnd).Hours() / 24
			if days >= 0 && days <= maxDays {
				result = append(result, qs[idx])
			}
		}

		// Cap to the 4 most recent quarters (qs is sorted by PeriodEnd
		// ascending, so take the tail).
		if len(result) > 4 {
			result = result[len(result)-4:]
		}

		return result
	}

	// Compute period averages and emit annual observations.
	const maxAnnualGapDays = 425 // ~14 months, handles fiscal year shifts

	for i := range annuals {
		a := &annuals[i]

		// Annual data is full-year; no de-cumulation needed. Copy into
		// arEmit/mrEmit so that merging period averages does not mutate
		// the original arFields (which may be read as prev in the next
		// iteration).
		a.arEmit = copyFieldMap(a.arFields)
		a.mrEmit = copyFieldMap(a.mrFields)

		// Override DPS with cash-paid methodology for annual data. The annual
		// period ends on the fiscal year-end date (Q4); LLY-style filers
		// disclose the FY declared total only on the 10-K (no 10-Q fact at
		// that date), so AR keeps the declared value while MR uses cash-paid.
		OverrideDPSFromCash(cf, a.arEmit, false, a.period.PeriodEnd, true)
		OverrideDPSFromCash(cf, a.mrEmit, true, a.period.PeriodEnd, true)

		// Strip stale fields from MR annual and recompute derived fields.
		// This matches Sharadar's MR semantics: if a company stops reporting
		// a concept (e.g. InterestExpense), derived fields like EBIT should
		// be recomputed without it.
		stripStaleAndRecompute(a.mrEmit, staleMRFields, cf)

		// Find the 4 constituent quarters for this fiscal year to compute
		// 4-quarter balance sheet averages (matching Sharadar methodology).
		// Fall back to 2-point (prior annual + current annual) averaging when
		// quarterly data is not available (e.g. companies with only 10-K filings).
		annualQs := findConstituentQuarters(quarters, a.period.PeriodEnd)
		if len(annualQs) > 0 {
			arMaps := make([]map[string]float64, len(annualQs))
			mrMaps := make([]map[string]float64, len(annualQs))

			for j, qd := range annualQs {
				arMaps[j] = qd.arEmit
				mrMaps[j] = qd.mrEmit
			}

			for k, v := range ComputeMultiQAverages(a.arEmit, arMaps) {
				a.arEmit[k] = v
			}

			for k, v := range ComputeMultiQAverages(a.mrEmit, mrMaps) {
				a.mrEmit[k] = v
			}
		} else if i > 0 {
			prev := &annuals[i-1]
			gapDays := a.period.PeriodEnd.Sub(prev.period.PeriodEnd).Hours() / 24

			if gapDays <= maxAnnualGapDays {
				arFallback := []map[string]float64{prev.arEmit, a.arEmit}
				for k, v := range ComputeMultiQAverages(a.arEmit, arFallback) {
					a.arEmit[k] = v
				}

				mrFallback := []map[string]float64{prev.mrEmit, a.mrEmit}
				for k, v := range ComputeMultiQAverages(a.mrEmit, mrFallback) {
					a.mrEmit[k] = v
				}
			}
		}

		calendarDate := NormalizeEventDate(a.period.PeriodEnd, a.period.FormType)

		if !since.IsZero() && a.period.MRFiledDate.Before(since) {
			continue
		}

		// ARY — override SharesBasic for AR semantics (see quarterly override above).
		// Use the annual-specific resolver: Sharadar's ARQ shares_basic for 10-K
		// periods matches the cover-page disclosure, which for some filers (CAT)
		// only appears as mid-fiscal-year (Q2) prose.
		if val, ok := resolveSharesBasicForAnnualAR(cf, a.period.ARFiledDate, a.period.PeriodEnd); ok {
			a.arEmit["SharesBasic"] = val
		}

		// Strip tax withholding from annual emit maps: Sharadar only
		// includes it in quarterly NCFCOMMON for bundled-presentation
		// filers like NVDA, not in the annual aggregation. Restaurant
		// filers (TXRH) report tax withholding as a distinct cash-flow
		// line item, and Sharadar's annual NCFCOMMON equals the sum of
		// quarterly stubs including that line — gated via PreOpeningCosts
		// to preserve the strip for NVDA-style filers.
		if !conceptFiledQuarterly(cf, []string{"PreOpeningCosts"}) {
			annualTWHStale := map[string]bool{"_taxWithholdingShareComp": true}
			stripStaleAndRecompute(a.arEmit, annualTWHStale, cf)
			stripStaleAndRecompute(a.mrEmit, annualTWHStale, cf)
		}

		// Apply bank overrides to annual emit maps. Only for banks —
		// the bundlesFinancing (NVDA) path modifies NCFCOMMON via tax
		// withholding strip, making the annual residual incorrect.
		if !conceptFiledQuarterly(cf, []string{"AssetsCurrent"}) {
			overrideNCFDebtResidual(cf, a.arEmit, a.period.PeriodEnd, a.period.FormType)
			overrideNCFDebtResidual(cf, a.mrEmit, a.period.PeriodEnd, a.period.FormType)
		}

		if !conceptFiledQuarterly(cf, []string{"Deposits", "DepositsDomestic", "DepositsTotal"}) {
			deriveCostOfRevenueBottomUp(a.arEmit)
			deriveCostOfRevenueBottomUp(a.mrEmit)
			deriveCostOfRevenueForSegmentFiler(cf, a.arEmit)
			deriveCostOfRevenueForSegmentFiler(cf, a.mrEmit)
			deriveCostOfRevenueForRestaurantFiler(cf, a.arEmit)
			deriveCostOfRevenueForRestaurantFiler(cf, a.mrEmit)
			deriveCostOfRevenueForEnergyFiler(cf, a.arEmit)
			deriveCostOfRevenueForEnergyFiler(cf, a.mrEmit)
			deriveCostOfRevenueForFullCostEnergyFiler(cf, a.arEmit)
			deriveCostOfRevenueForFullCostEnergyFiler(cf, a.mrEmit)
			deriveCostOfRevenueForIndustrialFinancialFiler(cf, a.arEmit)
			deriveCostOfRevenueForIndustrialFinancialFiler(cf, a.mrEmit)
		}

		overrideLiabilitiesForFullCostEnergyFiler(cf, a.arEmit)
		overrideLiabilitiesForFullCostEnergyFiler(cf, a.mrEmit)
		overrideInterestExpenseForFullCostEnergyFiler(cf, a.arEmit)
		overrideInterestExpenseForFullCostEnergyFiler(cf, a.mrEmit)
		overrideNCIForFullCostEnergyFiler(cf, a.arEmit)
		overrideNCIForFullCostEnergyFiler(cf, a.mrEmit)
		overrideInvestingClassificationForFullCostEnergyFiler(cf, a.arEmit)
		overrideInvestingClassificationForFullCostEnergyFiler(cf, a.mrEmit)

		overrideNCFBusinessAsResidualForReceivablesFiler(cf, a.arEmit)
		overrideNCFBusinessAsResidualForReceivablesFiler(cf, a.mrEmit)

		fundamental := BuildFundamental(a.arEmit, asset.Ticker, asset.CompositeFigi, "ARY",
			a.period.ARFiledDate, calendarDate, a.period.PeriodEnd, a.period.ARFiledDate)
		buffered = append(buffered, &data.Observation{
			Fundamental:      fundamental,
			ObservationDate:  calendarDate,
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		})

		*numObservations++

		// MRY — override SharesBasic for MR semantics (see quarterly override above).
		if val, ok := resolveSharesBasicAsOf(cf, a.period.PeriodEnd); ok {
			a.mrEmit["SharesBasic"] = val
		}

		// For banks, replace MRY DPS with the sum of cash-paid quarterly DPS.
		// The annual mrEmit has the declared DPS from the 10-K; MRY should
		// match the MRT trailing sum of cash-paid (prior quarter declared) values.
		if !conceptFiledQuarterly(cf, []string{"AssetsCurrent"}) && conceptFiledQuarterly(cf, []string{"Deposits"}) {
			cashDPSSum := 0.0
			cashDPSCount := 0

			for j := range quarters {
				if NormalizeEventDate(quarters[j].period.PeriodEnd, "10-K").Equal(NormalizeEventDate(a.period.PeriodEnd, "10-K")) {
					// This quarter belongs to this fiscal year
					if v, ok := quarters[j].mrEmit["DividendsPerBasicCommonShare"]; ok {
						cashDPSSum += v
						cashDPSCount++
					}
				}
			}

			if cashDPSCount == 4 {
				a.mrEmit["DividendsPerBasicCommonShare"] = cashDPSSum
			}
		}

		fundamental = BuildFundamental(a.mrEmit, asset.Ticker, asset.CompositeFigi, "MRY",
			a.period.PeriodEnd, calendarDate, a.period.PeriodEnd, a.period.MRFiledDate)
		buffered = append(buffered, &data.Observation{
			Fundamental:      fundamental,
			ObservationDate:  calendarDate,
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		})

		*numObservations++

		periodsEmitted++
	}

	// Emit quarterly observations using de-cumulated values.
	// Strip fields that are only meaningful for annual/trailing dimensions (#56).
	quarterOnlyExclude := []string{
		"AverageAssets", "EquityAvg", "InvestedCapitalAverage",
		"ROA", "ROE", "ROIC", "AssetTurnover", "ReturnOnSales",
	}

	for i := range quarters {
		q := &quarters[i]

		for _, key := range quarterOnlyExclude {
			delete(q.arEmit, key)
			delete(q.mrEmit, key)
		}

		calendarDate := NormalizeEventDate(q.period.PeriodEnd, q.period.FormType)

		// For banks, use the prior quarter's declared DPS as the cash-paid
		// DPS for MR dimensions only. Try arFields, then arEmit for
		// synthesized Q4 quarters. Only affects MR dimensions — AR keeps
		// the declared value. Skipped when the filer reports
		// PreferredStockDividendsIncomeStatementImpact (GS pattern): those
		// filers have Sharadar using the declared per-share value directly
		// rather than the cash-paid prior-quarter shift.
		if i > 0 && !conceptFiledQuarterly(cf, []string{"AssetsCurrent"}) && !hasPreferredISImpact {
			prev := &quarters[i-1]
			prevDPS, found := prev.arFields["DividendsPerBasicCommonShare"]

			if !found {
				prevDPS, found = prev.arEmit["DividendsPerBasicCommonShare"]
			}

			if !found {
				prevDPS, found = prev.mrEmit["DividendsPerBasicCommonShare"]
			}

			// For Q1 of each year: the prior Q4 is synthesized and may not
			// have DPS in any map. Fall back to Q3 (i-2) which is a regular
			// 10-Q quarter with DPS available.
			if !found && i > 1 {
				grandPrev := &quarters[i-2]

				prevDPS, found = grandPrev.arFields["DividendsPerBasicCommonShare"]

				if !found {
					prevDPS, found = grandPrev.arEmit["DividendsPerBasicCommonShare"]
				}
			}

			if found {
				q.mrEmit["DividendsPerBasicCommonShare"] = prevDPS
			}
		}

		if !since.IsZero() && q.period.MRFiledDate.Before(since) {
			continue
		}

		// Override NetCashFlowDebt for quarterly dimensions as the residual
		// of financing cash flow: debt = financing - common - dividend.
		// Sharadar's quarterly NCFDEBT captures small items (e.g. finance
		// lease payments) that aren't separately tagged in XBRL. The
		// residual naturally picks them up when the other components match.
		overrideNCFDebtResidual(cf, q.arEmit, q.period.PeriodEnd, q.period.FormType)
		overrideNCFDebtResidual(cf, q.mrEmit, q.period.PeriodEnd, q.period.FormType)

		// Derive CostOfRevenue bottom-up for insurance/conglomerates without
		// standard COGS tags. Banks (which report Deposits) have Revenue =
		// GrossProfit by definition (no COGS), so this derivation is skipped.
		if !conceptFiledQuarterly(cf, []string{"Deposits", "DepositsDomestic", "DepositsTotal"}) {
			deriveCostOfRevenueBottomUp(q.arEmit)
			deriveCostOfRevenueBottomUp(q.mrEmit)
			deriveCostOfRevenueForSegmentFiler(cf, q.arEmit)
			deriveCostOfRevenueForSegmentFiler(cf, q.mrEmit)
			deriveCostOfRevenueForRestaurantFiler(cf, q.arEmit)
			deriveCostOfRevenueForRestaurantFiler(cf, q.mrEmit)
			deriveCostOfRevenueForEnergyFiler(cf, q.arEmit)
			deriveCostOfRevenueForEnergyFiler(cf, q.mrEmit)
			deriveCostOfRevenueForFullCostEnergyFiler(cf, q.arEmit)
			deriveCostOfRevenueForFullCostEnergyFiler(cf, q.mrEmit)
			deriveCostOfRevenueForIndustrialFinancialFiler(cf, q.arEmit)
			deriveCostOfRevenueForIndustrialFinancialFiler(cf, q.mrEmit)
		}

		overrideLiabilitiesForFullCostEnergyFiler(cf, q.arEmit)
		overrideLiabilitiesForFullCostEnergyFiler(cf, q.mrEmit)
		overrideInterestExpenseForFullCostEnergyFiler(cf, q.arEmit)
		overrideInterestExpenseForFullCostEnergyFiler(cf, q.mrEmit)
		overrideNCIForFullCostEnergyFiler(cf, q.arEmit)
		overrideNCIForFullCostEnergyFiler(cf, q.mrEmit)
		overrideInvestingClassificationForFullCostEnergyFiler(cf, q.arEmit)
		overrideInvestingClassificationForFullCostEnergyFiler(cf, q.mrEmit)

		overrideNCFBusinessAsResidualForReceivablesFiler(cf, q.arEmit)
		overrideNCFBusinessAsResidualForReceivablesFiler(cf, q.mrEmit)

		// ARQ
		fundamental := BuildFundamental(q.arEmit, asset.Ticker, asset.CompositeFigi, "ARQ",
			q.period.ARFiledDate, calendarDate, q.period.PeriodEnd, q.period.ARFiledDate)
		buffered = append(buffered, &data.Observation{
			Fundamental:      fundamental,
			ObservationDate:  calendarDate,
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		})

		*numObservations++

		// MRQ
		fundamental = BuildFundamental(q.mrEmit, asset.Ticker, asset.CompositeFigi, "MRQ",
			q.period.PeriodEnd, calendarDate, q.period.PeriodEnd, q.period.MRFiledDate)
		buffered = append(buffered, &data.Observation{
			Fundamental:      fundamental,
			ObservationDate:  calendarDate,
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		})

		*numObservations++

		periodsEmitted++
	}

	// Compute TTM for each quarter that has 4 preceding quarters.
	// Uses de-cumulated (single-quarter) values so the TTM sum is correct.
	for i := 3; i < len(quarters); i++ {
		q := quarters[i]
		calendarDate := NormalizeEventDate(q.period.PeriodEnd, q.period.FormType)

		// Verify the 4 quarters span roughly 12 months. If a 10-Q is missing
		// from the sequence (or fiscal-year boundaries shifted), the span will
		// be too short or too long and the resulting TTM would be misleading.
		spanStart := quarters[i-3].period.PeriodEnd
		spanEnd := q.period.PeriodEnd
		spanDays := int(spanEnd.Sub(spanStart).Hours() / 24)

		if spanDays < ttmMinSpanDays || spanDays > ttmMaxSpanDays {
			log.Warn().
				Str("ticker", asset.Ticker).
				Time("span_start", spanStart).
				Time("span_end", spanEnd).
				Int("span_days", spanDays).
				Msg("skipping TTM computation: 4-quarter span outside expected range")

			continue
		}

		// Skip TTM windows where none of the 4 constituent quarters were
		// filed/restated on or after since. If any constituent quarter was
		// touched after since the TTM might have changed, so we re-emit.
		if !since.IsZero() {
			anyRecent := false

			for j := 0; j < 4; j++ {
				if !quarters[i-3+j].period.MRFiledDate.Before(since) {
					anyRecent = true
					break
				}
			}

			if !anyRecent {
				continue
			}
		}

		// The TTM "lastUpdated" is the most recent filing date among the
		// constituent quarters: any restatement to any quarter in the
		// window invalidates the prior TTM, so the latest filing date is
		// the freshness marker the data quality checks should compare
		// against.
		latestARFiled := quarters[i-3].period.ARFiledDate
		latestMRFiled := quarters[i-3].period.MRFiledDate

		for j := 1; j < 4; j++ {
			if quarters[i-3+j].period.ARFiledDate.After(latestARFiled) {
				latestARFiled = quarters[i-3+j].period.ARFiledDate
			}

			if quarters[i-3+j].period.MRFiledDate.After(latestMRFiled) {
				latestMRFiled = quarters[i-3+j].period.MRFiledDate
			}
		}

		// When the TTM window coincides with a fiscal year (the latest
		// quarter is a synthesized Q4 at the 10-K period end), period-average
		// fields like WeightedAverageShares should use the annual filing's
		// value rather than the Q4-specific synthesis. The annual value is the
		// true full-year weighted average, matching Sharadar's trailing output.
		var matchingAR, matchingMR map[string]float64

		for idx := range annuals {
			if annuals[idx].period.PeriodEnd.Equal(q.period.PeriodEnd) {
				matchingAR = annuals[idx].arEmit
				matchingMR = annuals[idx].mrEmit

				break
			}
		}

		// For banks, the annual MR DPS should use cash-paid (sum of
		// prior-quarter declared rates), not the declared total from
		// the 10-K. Without this, overridePeriodAvg would replace the
		// correctly-summed TTM cash-paid DPS with the declared annual.
		// Use Deposits as the bank sentinel (more specific than absence of
		// AssetsCurrent, which test fixtures may lack).
		if matchingMR != nil && !conceptFiledQuarterly(cf, []string{"AssetsCurrent"}) && conceptFiledQuarterly(cf, []string{"Deposits"}) {
			delete(matchingMR, "DividendsPerBasicCommonShare")
		}

		overridePeriodAvg := func(ttm, annualFields map[string]float64) {
			if annualFields == nil {
				return
			}

			for _, m := range FieldMappings {
				switch {
				case m.StatementType == StmtPeriodAverage:
					// Use the annual filing's true full-year weighted average.
				case m.StatementType == StmtFlow && m.ValueType == "float64":
					// Per-share flow fields (EPS, EPSDiluted, DividendsPerBasicCommonShare):
					// use the annual filing's value directly. Summing 4 individually
					// rounded quarterly per-share values may differ from the company-
					// reported annual value by up to $0.01.
				default:
					continue
				}

				if v, ok := annualFields[m.FieldName]; ok {
					ttm[m.FieldName] = v
				}
			}

			// Recompute derived metric fields since the period-average
			// fields they depend on (e.g. WeightedAverageShares used by
			// SalesPerShare, BookValuePerShare, etc.) have changed.
			for _, m := range FieldMappings {
				if m.Type != MappingDerived || m.StatementType != StmtMetric {
					continue
				}

				if val, ok := computeDerived(m, ttm); ok {
					ttm[m.FieldName] = val
				}
			}
		}

		// ART — uses de-cumulated quarterly values
		arQSlice := make([]map[string]float64, 4)
		for j := 0; j < 4; j++ {
			arQSlice[j] = quarters[i-3+j].arEmit
		}

		// bankFixTTMNCI replaces the TTM NCI sum with the annual value when
		// the TTM coincides with a fiscal year. Quarterly NCI values may
		// not sum to the annual due to the cumulative vs single-quarter
		// discrepancy in NetIncome (2M for JPM).
		bankFixTTMNCI := func(ttm, annualFields map[string]float64) {
			if annualFields == nil || conceptFiledQuarterly(cf, []string{"AssetsCurrent"}) {
				return
			}

			if v, ok := annualFields["NetIncomeToNonControllingInterests"]; ok {
				ttm["NetIncomeToNonControllingInterests"] = v
			}
		}

		// overrideFlowsAtFiscalYearEnd replaces TTM flow fields with the
		// annual filing's value when the trailing 12-month window exactly
		// coincides with the fiscal year. For MRT this matters whenever a
		// later filing restated comparative quarterlies (e.g. BRK's Q1 2025
		// 10-Q restated Q1 2024 SGA by +768M): the sum of restated MRQ
		// values no longer matches the annual total from the 10-K, but
		// Sharadar's MRT at the annual date_key equals MRY. Using the
		// annual value directly avoids the mismatch. For ART this is
		// algebraically a no-op (sum of original quarters equals annual)
		// and guards against cumulative rounding drift.
		//
		// Derived flow fields (D&A, GrossProfit, OperatingExpenses, EBITDA,
		// etc.) are copied from annualFields directly — we do NOT recompute
		// them via their formulas because some formulas depend on helper
		// operands (_depreciation, _amortizationOfIntangibles, ...) that
		// may not be populated in annualFields for filers that only report
		// a single aggregate D&A concept. Ratio fields (gross_margin,
		// ebitda_margin, etc.) ARE recomputed since they depend on the
		// overridden flow totals.
		overrideFlowsAtFiscalYearEnd := func(ttm, annualFields map[string]float64) {
			if annualFields == nil {
				return
			}

			for _, m := range FieldMappings {
				if m.StatementType != StmtFlow || m.ValueType != "int64" {
					continue
				}

				if v, ok := annualFields[m.FieldName]; ok {
					ttm[m.FieldName] = v
				}
			}

			// Recompute StmtMetric derived fields (ratios like gross_margin,
			// ebitda_margin) from the overridden flow totals.
			for _, m := range FieldMappings {
				if m.Type != MappingDerived || m.StatementType != StmtMetric {
					continue
				}

				if val, ok := computeDerived(m, ttm); ok {
					ttm[m.FieldName] = val
				}
			}
		}

		if ttm := ComputeTTM(arQSlice, false); ttm != nil {
			overridePeriodAvg(ttm, matchingAR)
			bankFixTTMNCI(ttm, matchingAR)
			overrideFlowsAtFiscalYearEnd(ttm, matchingAR)

			for k, v := range ComputeMultiQAverages(ttm, arQSlice) {
				ttm[k] = v
			}

			fundamental := BuildFundamental(ttm, asset.Ticker, asset.CompositeFigi, "ART",
				q.period.ARFiledDate, calendarDate, q.period.PeriodEnd, latestARFiled)
			buffered = append(buffered, &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  calendarDate,
				SubscriptionID:   sub.ID,
				SubscriptionName: sub.Name,
			})

			*numObservations++
		}

		// MRT — uses de-cumulated quarterly values
		mrQSlice := make([]map[string]float64, 4)
		for j := 0; j < 4; j++ {
			mrQSlice[j] = quarters[i-3+j].mrEmit
		}

		if ttm := ComputeTTM(mrQSlice, false); ttm != nil {
			overridePeriodAvg(ttm, matchingMR)
			bankFixTTMNCI(ttm, matchingMR)
			overrideFlowsAtFiscalYearEnd(ttm, matchingMR)

			for k, v := range ComputeMultiQAverages(ttm, mrQSlice) {
				ttm[k] = v
			}

			fundamental := BuildFundamental(ttm, asset.Ticker, asset.CompositeFigi, "MRT",
				q.period.PeriodEnd, calendarDate, q.period.PeriodEnd, latestMRFiled)
			buffered = append(buffered, &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  calendarDate,
				SubscriptionID:   sub.ID,
				SubscriptionName: sub.Name,
			})

			*numObservations++
		}
	}

	// Enrich buffered observations with market-data fields if a price
	// lookup function is available.
	if priceLookupFn != nil {
		var fundamentals []*data.Fundamental

		for _, obs := range buffered {
			if obs.Fundamental != nil {
				fundamentals = append(fundamentals, obs.Fundamental)
			}
		}

		var opts []EnrichOption
		if asset.SiblingFigi != "" {
			opts = append(opts, WithMultiClass(cf, asset.SiblingFigi))
		}

		EnrichMarketData(fundamentals, priceLookupFn, opts...)
	}

	// Clean up stale records for this ticker before sending new observations.
	// Prior runs may have used different dateKey conventions (e.g., filing
	// date vs period end), leaving orphan rows that create spurious diffs.
	// Collect the set of valid (dimension, event_date) pairs from the current
	// run and delete any existing records not in this set.
	if asset.CompositeFigi != "" && len(buffered) > 0 {
		cleanupCtx := context.Background()

		tbl := sub.DataTablesMap[data.FundamentalsKey]
		if tbl != "" {
			type obsKey struct {
				dim       string
				eventDate time.Time
			}

			validKeys := make(map[obsKey]bool)

			for _, obs := range buffered {
				if obs.Fundamental != nil {
					validKeys[obsKey{obs.Fundamental.Dimension, obs.Fundamental.EventDate}] = true
				}
			}

			conn, err := sub.Library.Pool.Acquire(cleanupCtx)
			if err == nil {
				rows, err := conn.Query(cleanupCtx,
					fmt.Sprintf("SELECT dimension, event_date FROM %s WHERE composite_figi = $1", tbl),
					asset.CompositeFigi)
				if err == nil {
					var toDelete []obsKey

					for rows.Next() {
						var (
							dim string
							ed  time.Time
						)
						if err := rows.Scan(&dim, &ed); err == nil {
							if !validKeys[obsKey{dim, ed}] {
								toDelete = append(toDelete, obsKey{dim, ed})
							}
						}
					}

					rows.Close()

					for _, k := range toDelete {
						_, _ = conn.Exec(cleanupCtx,
							fmt.Sprintf("DELETE FROM %s WHERE composite_figi = $1 AND dimension = $2 AND event_date = $3", tbl),
							asset.CompositeFigi, k.dim, k.eventDate)
					}

					if len(toDelete) > 0 {
						log.Debug().Str("ticker", asset.Ticker).Int("deleted", len(toDelete)).Msg("cleaned up stale observations")
					}
				}

				conn.Release()
			}
		}
	}

	// Send all buffered observations to the output channel.
	for _, obs := range buffered {
		out <- obs
	}

	// Log per-company coverage. We use Debug for healthy companies (most of
	// the universe) to avoid flooding logs during a 10,000+ company backfill,
	// and escalate to Warn when coverage is concerning so mapping gaps are
	// surfaced. Companies with no periods identified are skipped: there's
	// nothing to compare against and the upstream parser already logs them.
	if len(periods) > 0 {
		totalMappings := len(FieldMappings)
		coveragePct := 0

		if totalMappings > 0 {
			coveragePct = 100 * latestPeriodCoverage / totalMappings
		}

		if coveragePct < coverageWarnThresholdPct {
			log.Warn().
				Str("ticker", asset.Ticker).
				Int("cik", asset.CIK).
				Int("periods_emitted", periodsEmitted).
				Int("resolved", latestPeriodCoverage).
				Int("total", totalMappings).
				Int("pct", coveragePct).
				Msg("sec coverage for company")
		} else {
			log.Debug().
				Str("ticker", asset.Ticker).
				Int("cik", asset.CIK).
				Int("periods_emitted", periodsEmitted).
				Int("resolved", latestPeriodCoverage).
				Int("total", totalMappings).
				Int("pct", coveragePct).
				Msg("sec coverage for company")
		}
	}

	return latestPeriodCoverage, periodsEmitted
}
