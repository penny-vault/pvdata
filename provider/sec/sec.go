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

	// Load CIK -> asset map from database (primary lookup path)
	dbCIKMap, err := LoadCIKMapFromDB(ctx, sub.Library.Pool)
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

	// Apply ticker/FIGI filter if set
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

		if len(filtered) == 0 {
			candidates := make([]string, 0, len(cikMap))
			for _, info := range cikMap {
				if tickerFilter != "" {
					candidates = append(candidates, info.Ticker)
				} else {
					candidates = append(candidates, info.CompositeFigi)
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

func runBackfill(ctx context.Context, client *resty.Client, cikMap map[int]AssetInfo, sub *library.Subscription, since time.Time, out chan<- *data.Observation, numObservations, skippedMissingFIGI *int) error {
	processed := 0
	processErrors := 0

	// Track the latest-period coverage (number of resolved fields) for each
	// company so we can report a coverage distribution in the run summary.
	// Companies with no periods emitted are not included.
	coverageSamples := make([]int, 0, len(cikMap))

	err := DownloadCompanyFactsZip(ctx, client, func(cik int, jsonData []byte) error {
		asset, ok := cikMap[cik]
		if !ok {
			return nil // Unknown company, skip
		}

		// Skip CIKs without a composite FIGI: SaveDB drops these records,
		// so processing them would only waste work. Track the count so
		// users see how big the coverage gap is.
		if asset.CompositeFigi == "" {
			*skippedMissingFIGI++

			return nil
		}

		cf, err := ParseCompanyFacts(jsonData)
		if err != nil {
			// Count parse errors but return nil so the loop continues
			// processing the rest of the archive. The framework's
			// idempotent upserts mean partial success is acceptable.
			processErrors++

			log.Warn().Err(err).Int("cik", cik).Msg("error parsing companyfacts in backfill")

			return nil
		}

		latestCoverage, periodsEmitted := emitFundamentals(cf, asset, sub, since, out, numObservations)
		if periodsEmitted > 0 {
			coverageSamples = append(coverageSamples, latestCoverage)
		}

		processed++
		if processed%1000 == 0 {
			log.Info().Int("processed", processed).Msg("backfill progress")
		}

		return nil
	})
	if err != nil {
		log.Error().Err(err).Msg("error during backfill")

		return err
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
		period   Period
		arFields map[string]float64 // original resolved values (may be YTD for cash flow)
		mrFields map[string]float64 // original resolved values (may be YTD for cash flow)
		arEmit   map[string]float64 // de-cumulated values for emission and TTM
		mrEmit   map[string]float64 // de-cumulated values for emission and TTM
	}

	var quarters []quarterData

	var annuals []quarterData

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
		}

		// Track all quarters regardless of since so TTM windows are complete.
		if p.FormType == "10-Q" {
			quarters = append(quarters, quarterData{period: p, arFields: arFields, mrFields: mrFields})
		}

		// Track the chronologically latest period's coverage. Done before the
		// since filter so the reported coverage reflects the true most recent
		// period, not just the newest period emitted this run.
		if p.PeriodEnd.After(latestPeriodEnd) {
			latestPeriodEnd = p.PeriodEnd
			latestPeriodCoverage = len(arFields)
		}

		if p.FormType == "10-K" {
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
				q.arEmit = DecumulateYTD(cf, q.arFields, prev.arFields, q.period.PeriodEnd, q.period.FormType)
				q.mrEmit = DecumulateYTD(cf, q.mrFields, prev.mrFields, q.period.PeriodEnd, q.period.FormType)

				continue
			}
		}

		// Q1 or no prior quarter: no de-cumulation needed.
		q.arEmit = q.arFields
		q.mrEmit = q.mrFields
	}

	// Compute period-average fields (AverageAssets, EquityAvg,
	// InvestedCapitalAverage) and derived ratios (ROA, ROE, ROIC) from
	// consecutive quarterly balance sheet values.
	for i := range quarters {
		q := &quarters[i]

		if i == 0 {
			continue
		}

		prev := &quarters[i-1]
		gapDays := q.period.PeriodEnd.Sub(prev.period.PeriodEnd).Hours() / 24

		if gapDays > maxQuarterGapDays {
			continue
		}

		for k, v := range ComputePeriodAverages(q.arEmit, prev.arEmit) {
			q.arEmit[k] = v
		}

		for k, v := range ComputePeriodAverages(q.mrEmit, prev.mrEmit) {
			q.mrEmit[k] = v
		}
	}

	// Compute period averages and emit annual observations.
	const maxAnnualGapDays = 425 // ~14 months, handles fiscal year shifts

	for i := range annuals {
		a := &annuals[i]

		// Annual data is full-year; no de-cumulation needed. Store
		// directly in arEmit/mrEmit.
		a.arEmit = a.arFields
		a.mrEmit = a.mrFields

		if i > 0 {
			prev := &annuals[i-1]
			gapDays := a.period.PeriodEnd.Sub(prev.period.PeriodEnd).Hours() / 24

			if gapDays <= maxAnnualGapDays {
				for k, v := range ComputePeriodAverages(a.arEmit, prev.arEmit) {
					a.arEmit[k] = v
				}

				for k, v := range ComputePeriodAverages(a.mrEmit, prev.mrEmit) {
					a.mrEmit[k] = v
				}
			}
		}

		calendarDate := NormalizeEventDate(a.period.PeriodEnd, a.period.FormType)

		if !since.IsZero() && a.period.MRFiledDate.Before(since) {
			continue
		}

		// ARY
		fundamental := BuildFundamental(a.arEmit, asset.Ticker, asset.CompositeFigi, "ARY",
			a.period.ARFiledDate, calendarDate, a.period.PeriodEnd, a.period.ARFiledDate)
		out <- &data.Observation{
			Fundamental:      fundamental,
			ObservationDate:  calendarDate,
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}

		*numObservations++

		// MRY
		fundamental = BuildFundamental(a.mrEmit, asset.Ticker, asset.CompositeFigi, "MRY",
			a.period.PeriodEnd, calendarDate, a.period.PeriodEnd, a.period.MRFiledDate)
		out <- &data.Observation{
			Fundamental:      fundamental,
			ObservationDate:  calendarDate,
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}

		*numObservations++

		periodsEmitted++
	}

	// Emit quarterly observations using de-cumulated values.
	for i := range quarters {
		q := &quarters[i]
		calendarDate := NormalizeEventDate(q.period.PeriodEnd, q.period.FormType)

		if !since.IsZero() && q.period.MRFiledDate.Before(since) {
			continue
		}

		// ARQ
		fundamental := BuildFundamental(q.arEmit, asset.Ticker, asset.CompositeFigi, "ARQ",
			q.period.ARFiledDate, calendarDate, q.period.PeriodEnd, q.period.ARFiledDate)
		out <- &data.Observation{
			Fundamental:      fundamental,
			ObservationDate:  calendarDate,
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}

		*numObservations++

		// MRQ
		fundamental = BuildFundamental(q.mrEmit, asset.Ticker, asset.CompositeFigi, "MRQ",
			q.period.PeriodEnd, calendarDate, q.period.PeriodEnd, q.period.MRFiledDate)
		out <- &data.Observation{
			Fundamental:      fundamental,
			ObservationDate:  calendarDate,
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}

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

		// ART — uses de-cumulated quarterly values
		arQSlice := make([]map[string]float64, 4)
		for j := 0; j < 4; j++ {
			arQSlice[j] = quarters[i-3+j].arEmit
		}

		if ttm := ComputeTTM(arQSlice); ttm != nil {
			fundamental := BuildFundamental(ttm, asset.Ticker, asset.CompositeFigi, "ART",
				q.period.ARFiledDate, calendarDate, q.period.PeriodEnd, latestARFiled)
			out <- &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  calendarDate,
				SubscriptionID:   sub.ID,
				SubscriptionName: sub.Name,
			}

			*numObservations++
		}

		// MRT — uses de-cumulated quarterly values
		mrQSlice := make([]map[string]float64, 4)
		for j := 0; j < 4; j++ {
			mrQSlice[j] = quarters[i-3+j].mrEmit
		}

		if ttm := ComputeTTM(mrQSlice); ttm != nil {
			fundamental := BuildFundamental(ttm, asset.Ticker, asset.CompositeFigi, "MRT",
				q.period.PeriodEnd, calendarDate, q.period.PeriodEnd, latestMRFiled)
			out <- &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  calendarDate,
				SubscriptionID:   sub.ID,
				SubscriptionName: sub.Name,
			}

			*numObservations++
		}
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
