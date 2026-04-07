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
	"strconv"
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

	skippedMissingFIGI := 0

	isBackfill := sub.LastObsDate.IsZero()
	if isBackfill {
		if err := runBackfill(ctx, client, cikMap, sub, out, &numObservations, &skippedMissingFIGI); err != nil {
			runSummary.Status = data.RunFailed
			return
		}
	} else {
		if err := runIncremental(ctx, client, cikMap, sub, sub.LastObsDate, out, &numObservations, &skippedMissingFIGI); err != nil {
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

func runBackfill(ctx context.Context, client *resty.Client, cikMap map[int]AssetInfo, sub *library.Subscription, out chan<- *data.Observation, numObservations, skippedMissingFIGI *int) error {
	processed := 0

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
			return err
		}

		emitFundamentals(cf, asset, sub, time.Time{}, out, numObservations)

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

	log.Info().Int("total_processed", processed).Msg("backfill complete")

	return nil
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

// emitFundamentals processes a CompanyFacts into data.Fundamental observations
// for all 6 dimensions and sends them to the output channel.
//
// The since parameter limits emission to periods filed on or after that time.
// Pass time.Time{} (zero value) to emit every period without filtering.
// MRFiledDate is used for the comparison so that restatements filed after
// since are re-emitted even if the period itself is older.
func emitFundamentals(cf *CompanyFacts, asset AssetInfo, sub *library.Subscription, since time.Time, out chan<- *data.Observation, numObservations *int) {
	periods := IdentifyPeriods(cf)

	// Collect quarterly periods for TTM computation, keyed by normalized event date.
	// All quarters are kept (even those filtered out below) so TTM windows can
	// still find their constituent quarters; the since check is reapplied per
	// window when emitting TTM observations.
	type quarterData struct {
		period   Period
		arFields map[string]float64
		mrFields map[string]float64
	}

	var quarters []quarterData

	for _, p := range periods {
		eventDate := NormalizeEventDate(p.PeriodEnd, p.FormType)

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

		// Skip emitting per-period observations for periods filed before since.
		// MRFiledDate is used so that restatements filed after since are re-emitted.
		if !since.IsZero() && p.MRFiledDate.Before(since) {
			continue
		}

		if p.FormType == "10-Q" {
			// ARQ
			fundamental := BuildFundamental(arFields, asset.Ticker, asset.CompositeFigi, "ARQ",
				eventDate, p.ARFiledDate, p.PeriodEnd, p.ARFiledDate)
			out <- &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  eventDate,
				SubscriptionID:   sub.ID,
				SubscriptionName: sub.Name,
			}

			*numObservations++

			// MRQ
			fundamental = BuildFundamental(mrFields, asset.Ticker, asset.CompositeFigi, "MRQ",
				eventDate, p.PeriodEnd, p.PeriodEnd, p.MRFiledDate)
			out <- &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  eventDate,
				SubscriptionID:   sub.ID,
				SubscriptionName: sub.Name,
			}

			*numObservations++
		}

		if p.FormType == "10-K" {
			// ARY
			fundamental := BuildFundamental(arFields, asset.Ticker, asset.CompositeFigi, "ARY",
				eventDate, p.ARFiledDate, p.PeriodEnd, p.ARFiledDate)
			out <- &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  eventDate,
				SubscriptionID:   sub.ID,
				SubscriptionName: sub.Name,
			}

			*numObservations++

			// MRY
			fundamental = BuildFundamental(mrFields, asset.Ticker, asset.CompositeFigi, "MRY",
				eventDate, p.PeriodEnd, p.PeriodEnd, p.MRFiledDate)
			out <- &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  eventDate,
				SubscriptionID:   sub.ID,
				SubscriptionName: sub.Name,
			}

			*numObservations++
		}
	}

	// Compute TTM for each quarter that has 4 preceding quarters
	for i := 3; i < len(quarters); i++ {
		q := quarters[i]
		eventDate := NormalizeEventDate(q.period.PeriodEnd, q.period.FormType)

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

		// ART
		arQSlice := make([]map[string]float64, 4)
		for j := 0; j < 4; j++ {
			arQSlice[j] = quarters[i-3+j].arFields
		}

		if ttm := ComputeTTM(arQSlice); ttm != nil {
			fundamental := BuildFundamental(ttm, asset.Ticker, asset.CompositeFigi, "ART",
				eventDate, q.period.ARFiledDate, q.period.PeriodEnd, latestARFiled)
			out <- &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  eventDate,
				SubscriptionID:   sub.ID,
				SubscriptionName: sub.Name,
			}

			*numObservations++
		}

		// MRT
		mrQSlice := make([]map[string]float64, 4)
		for j := 0; j < 4; j++ {
			mrQSlice[j] = quarters[i-3+j].mrFields
		}

		if ttm := ComputeTTM(mrQSlice); ttm != nil {
			fundamental := BuildFundamental(ttm, asset.Ticker, asset.CompositeFigi, "MRT",
				eventDate, q.period.PeriodEnd, q.period.PeriodEnd, latestMRFiled)
			out <- &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  eventDate,
				SubscriptionID:   sub.ID,
				SubscriptionName: sub.Name,
			}

			*numObservations++
		}
	}
}
