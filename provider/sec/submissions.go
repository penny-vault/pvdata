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
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/goccy/go-json"
	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"golang.org/x/sync/singleflight"
	"golang.org/x/time/rate"
)

// SubmissionsResponse mirrors the subset of the
// data.sec.gov/submissions/CIK*.json payload we use for asset
// enrichment. Filings.recent and overflow file lists are intentionally
// omitted; companyfacts.go already handles the filings list when it
// needs them.
type SubmissionsResponse struct {
	CIK                             string             `json:"cik"`
	EntityType                      string             `json:"entityType"`
	SIC                             string             `json:"sic"`
	SICDescription                  string             `json:"sicDescription"`
	Name                            string             `json:"name"`
	Tickers                         []string           `json:"tickers"`
	Exchanges                       []string           `json:"exchanges"`
	LEI                             string             `json:"lei"`
	Description                     string             `json:"description"`
	Website                         string             `json:"website"`
	InvestorWebsite                 string             `json:"investorWebsite"`
	Category                        string             `json:"category"`
	FiscalYearEnd                   string             `json:"fiscalYearEnd"`
	StateOfIncorporation            string             `json:"stateOfIncorporation"`
	StateOfIncorporationDescription string             `json:"stateOfIncorporationDescription"`
	Phone                           string             `json:"phone"`
	Addresses                       map[string]Address `json:"addresses"`
	FormerNames                     []FormerName       `json:"formerNames"`
	Filings                         FilingsBlock       `json:"filings"`
}

// FilingsBlock is the top of the filings tree in the SEC submissions
// payload. We use its `files[].filingFrom` overflow metadata to find
// the earliest filing date without having to fetch every overflow
// file — a single overflow entry's filingFrom IS that file's earliest
// filing, so the global earliest is the min across all overflow
// `filingFrom` values combined with the min of `recent.filingDate`.
type FilingsBlock struct {
	Recent FilingsRecent `json:"recent"`
	Files  []FilingsFile `json:"files"`
}

// FilingsRecent holds the most recent batch of filings (typically up
// to ~1000). Arrays are parallel and ordered newest-first, so the
// last element of FilingDate is the earliest filing in this batch.
type FilingsRecent struct {
	FilingDate []string `json:"filingDate"`
	Form       []string `json:"form"`
}

// FilingsFile is a pointer to an overflow file holding older filings
// for entities with a long filing history. filingFrom / filingTo
// bracket the date range of filings inside the file so we don't have
// to read its contents just to know the earliest date.
type FilingsFile struct {
	Name        string `json:"name"`
	FilingFrom  string `json:"filingFrom"`
	FilingTo    string `json:"filingTo"`
	FilingCount int    `json:"filingCount"`
}

// EarliestFilingDate returns the earliest filing date the SEC has for
// this entity, formatted as YYYY-MM-DD. Returns "" when no filings
// are available. Walks both the `recent` batch and the overflow
// `files` metadata so the result is correct for entities with many
// thousands of filings (Brown-Forman, GE, etc.).
func (s *SubmissionsResponse) EarliestFilingDate() string {
	if s == nil {
		return ""
	}

	earliest := ""

	if len(s.Filings.Recent.FilingDate) > 0 {
		// Array is newest-first; last element is the earliest in batch.
		earliest = s.Filings.Recent.FilingDate[len(s.Filings.Recent.FilingDate)-1]
	}

	for _, f := range s.Filings.Files {
		if f.FilingFrom == "" {
			continue
		}

		if earliest == "" || f.FilingFrom < earliest {
			earliest = f.FilingFrom
		}
	}

	return earliest
}

// EarliestFilingDateForForm returns the earliest filing date in the
// recent-filings batch whose form starts with the supplied prefix
// (case-insensitive). Returns "" when no matching form is found.
//
// Used to recover fund launch dates: an ETF/MF's first N-1A is filed
// when the fund registers, and a CEF's first N-2 plays the analogous
// role. These dates are a better approximation of the fund's actual
// trading start than the issuer's earliest filing of any kind, which
// for a multi-fund sponsor (Invesco, BlackRock, ProShares) can predate
// any individual fund's launch by decades.
//
// Only the `recent` batch is scanned. SEC's overflow files don't carry
// per-filing form information without fetching each overflow JSON, and
// the recent batch covers the entire filing history of every fund we
// observed in practice (an active fund accumulates fewer than 1000
// filings over its life). A fund whose registration filings have
// rolled off `recent` is rare enough that the cost of paging is not
// justified for the data-quality gain.
func (s *SubmissionsResponse) EarliestFilingDateForForm(prefix string) string {
	if s == nil || prefix == "" {
		return ""
	}

	upper := strings.ToUpper(strings.TrimSpace(prefix))
	if upper == "" {
		return ""
	}

	forms := s.Filings.Recent.Form
	dates := s.Filings.Recent.FilingDate
	earliest := ""

	for i, form := range forms {
		if i >= len(dates) {
			break
		}

		if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(form)), upper) {
			continue
		}

		date := strings.TrimSpace(dates[i])
		if date == "" {
			continue
		}

		if earliest == "" || date < earliest {
			earliest = date
		}
	}

	return earliest
}

// Address is one of the mailing/business addresses returned in the
// submissions payload. We surface city + stateOrCountry on the asset's
// HeadquartersLocation when available. The `isForeignLocation` field
// in SEC's response is an integer (0/1) or null, not a bool — we just
// skip it since the city/stateOrCountry pair is enough for our use.
type Address struct {
	Street1        string `json:"street1"`
	Street2        string `json:"street2"`
	City           string `json:"city"`
	StateOrCountry string `json:"stateOrCountry"`
	ZIPCode        string `json:"zipCode"`
}

// FormerName captures one entry from the historical name list. Dates
// are ISO timestamps when SEC returned them.
type FormerName struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// submissionsRateLimit is the cap on SEC EDGAR requests per second.
// SEC's published policy is 10 req/s with a valid User-Agent header;
// we stay one under that for headroom across concurrent callers.
const submissionsRateLimit = 9

var (
	submissionsClientOnce sync.Once
	submissionsClient     *resty.Client
	submissionsLimiter    = rate.NewLimiter(rate.Limit(submissionsRateLimit), 1)

	submissionsCacheMu sync.RWMutex
	submissionsCache   = map[int]*SubmissionsResponse{}

	// submissionsSingleflight deduplicates concurrent FetchSubmissions
	// calls for the same CIK so a thundering herd of workers that all
	// miss the cache only generates one network request. Without this
	// the cache is correct but useless under high concurrency: N workers
	// that all hit FetchSubmissions(CIK=X) while the cache is empty all
	// independently fetch X, then race to write the same result.
	submissionsSingleflight singleflight.Group
)

// defaultSECUserAgent is the User-Agent header sent to data.sec.gov
// when no `sec.userAgent` is configured. SEC requires a valid header
// per their fair-access policy; the default identifies the project and
// provides a contact address.
const defaultSECUserAgent = "pvdata/1.0 sec@pennyvault.com"

// secUserAgent returns the configured User-Agent string, falling back
// to defaultSECUserAgent when unset. Reads `sec.userAgent` from viper
// which also honours the `PV_SEC_USERAGENT` environment variable.
func secUserAgent() string {
	if ua := viper.GetString("sec.userAgent"); ua != "" {
		return ua
	}

	return defaultSECUserAgent
}

// submissionsHTTPClient returns the lazily-initialized resty client
// used for /submissions requests. Always returns a usable client
// because secUserAgent() falls back to defaultSECUserAgent.
func submissionsHTTPClient() *resty.Client {
	submissionsClientOnce.Do(func() {
		submissionsClient = NewSECClient(secUserAgent(), submissionsLimiter)
	})

	return submissionsClient
}

// FetchSubmissions returns the parsed /submissions/CIK*.json payload
// for the given CIK. Responses are cached process-wide; the same CIK
// queried twice in a run hits the network once. Returns
// ErrSECNotConfigured when no User-Agent is set; nil + nil error when
// the CIK is unknown to SEC (404).
func FetchSubmissions(ctx context.Context, cik int) (*SubmissionsResponse, error) {
	if cik <= 0 {
		return nil, nil
	}

	submissionsCacheMu.RLock()

	cached, ok := submissionsCache[cik]

	submissionsCacheMu.RUnlock()

	if ok {
		return cached, nil
	}

	// singleflight key dedupes by CIK so only one goroutine performs
	// the network fetch even when 128 walk workers all miss the cache
	// for the same CIK on the same tick.
	key := strconv.Itoa(cik)

	res, err, _ := submissionsSingleflight.Do(key, func() (any, error) {
		// Re-check the cache inside the singleflight closure in case
		// another caller landed the result while we were queueing.
		submissionsCacheMu.RLock()

		cached, ok := submissionsCache[cik]

		submissionsCacheMu.RUnlock()

		if ok {
			return cached, nil
		}

		client := submissionsHTTPClient()
		url := submissionsURL + FormatCIK(cik) + ".json"

		zerolog.Ctx(ctx).Debug().Int("CIK", cik).Str("URL", url).Msg("sec: GET /submissions")

		resp, err := client.R().SetContext(ctx).Get(url)
		if err != nil {
			return nil, fmt.Errorf("sec submissions GET %s: %w", url, err)
		}

		if resp.StatusCode() == http.StatusNotFound {
			submissionsCacheMu.Lock()
			submissionsCache[cik] = nil
			submissionsCacheMu.Unlock()

			return (*SubmissionsResponse)(nil), nil
		}

		if resp.StatusCode() != http.StatusOK {
			return nil, fmt.Errorf("sec submissions GET %s: status %d body %s", url, resp.StatusCode(), string(resp.Body()))
		}

		var out SubmissionsResponse

		if err := json.Unmarshal(resp.Body(), &out); err != nil {
			return nil, fmt.Errorf("sec submissions decode CIK %d: %w", cik, err)
		}

		submissionsCacheMu.Lock()
		submissionsCache[cik] = &out
		submissionsCacheMu.Unlock()

		return &out, nil
	})
	if err != nil {
		return nil, err
	}

	if res == nil {
		return nil, nil
	}

	sub, _ := res.(*SubmissionsResponse)

	return sub, nil
}

// EnrichSubmissions fills empty descriptive fields on each asset from
// the SEC submissions endpoint. Only operates on assets with a
// non-empty CIK. Uses the configured `sec.userAgent` (or the project
// default when unset) for the SEC fair-access User-Agent header.
//
// Fields populated when previously empty:
//   - asset.Name                  <- SubmissionsResponse.Name
//   - asset.SIC                   <- parsed SubmissionsResponse.SIC
//   - asset.Description           <- SubmissionsResponse.Description
//   - asset.CorporateUrl          <- SubmissionsResponse.Website
//   - asset.HeadquartersLocation  <- formatted from Addresses["business"]
//
// asset.ListingDate is a backstop fill (only when Massive supplied
// no value) using an asset-type-aware SEC signal: N-1A for ETF/MF,
// N-2 for CEF, EarliestFilingDate for single-ticker non-fund CIKs.
// Shared-CIK non-funds and ETN get no SEC fill. See
// applyListingDateFromSEC for details.
//
// formerNames are not persisted yet (no column / sidecar table); they
// are logged so an operator can confirm coverage. A follow-up patch
// can route them into predecessor-detection or a new schema column.
func EnrichSubmissions(ctx context.Context, assets ...*data.Asset) {
	logger := zerolog.Ctx(ctx)

	for _, asset := range assets {
		if asset.CIK == "" {
			continue
		}

		cik, err := strconv.Atoi(asset.CIK)
		if err != nil {
			continue
		}

		sub, err := FetchSubmissions(ctx, cik)
		if err != nil {
			logger.Warn().
				Err(err).
				Str("Ticker", asset.Ticker).
				Str("CIK", asset.CIK).
				Msg("sec: submissions fetch failed; skipping enrichment for this asset")

			continue
		}

		if sub == nil {
			continue
		}

		// Detect misattribution: Massive sometimes returns a same-ticker
		// successor's CIK (e.g. ACF -> CIK 1405287 "Stream Global
		// Services", but the historical AmeriCredit Corp had CIK 804269).
		// Signal: SEC's earliest filing for the supplied CIK postdates
		// the asset's ListingDate (the walk firstSeen) by a meaningful
		// margin. When that happens, search SEC by the asset's Name and
		// swap to the historical CIK if a confident match comes back.
		if corrected, correctedSub := correctMisattributedCIK(ctx, asset, sub, logger); correctedSub != nil {
			asset.CIK = corrected
			sub = correctedSub
		}

		fillFromSubmissions(asset, sub, logger)
	}
}

// misattributionGraceDays is how many days SEC's earliest filing date
// can postdate the asset's ListingDate before we flag the CIK as
// misattributed. A modest tolerance avoids false positives for issuers
// whose first 10-K landed a few quarters after the IPO. One full year
// is comfortably outside any plausible filing lag.
const misattributionGraceDays = 365

// correctMisattributedCIK detects when sub's CIK appears to belong to a
// different entity than the historical asset and, if so, searches SEC
// by asset.Name for the correct CIK. Returns ("", nil) when the
// existing CIK looks correct; otherwise returns the corrected CIK and
// its freshly-fetched SubmissionsResponse so the caller can swap both
// atomically. Logs at Info on swap so the operator sees CIK changes.
func correctMisattributedCIK(ctx context.Context, asset *data.Asset, sub *SubmissionsResponse, logger *zerolog.Logger) (string, *SubmissionsResponse) {
	if asset.ListingDate == "" || sub == nil {
		return "", nil
	}

	earliest := sub.EarliestFilingDate()
	if earliest == "" {
		return "", nil
	}

	listed, err := time.Parse("2006-01-02", asset.ListingDate)
	if err != nil {
		return "", nil
	}

	earliestT, err := time.Parse("2006-01-02", earliest)
	if err != nil {
		return "", nil
	}

	gap := earliestT.Sub(listed)
	if gap < time.Duration(misattributionGraceDays)*24*time.Hour {
		return "", nil
	}

	if strings.TrimSpace(asset.Name) == "" {
		return "", nil
	}

	correctedCIK, ok := FindCIKByName(ctx, asset.Name, listed.Year())
	if !ok {
		return "", nil
	}

	if strings.TrimLeft(correctedCIK, "0") == strings.TrimLeft(asset.CIK, "0") {
		return "", nil
	}

	n, err := strconv.Atoi(correctedCIK)
	if err != nil {
		return "", nil
	}

	correctedSub, err := FetchSubmissions(ctx, n)
	if err != nil || correctedSub == nil {
		return "", nil
	}

	logger.Info().
		Str("Ticker", asset.Ticker).
		Str("OldCIK", asset.CIK).
		Str("NewCIK", correctedCIK).
		Str("OldCIKName", sub.Name).
		Str("NewCIKName", correctedSub.Name).
		Str("ListingDate", asset.ListingDate).
		Str("OldCIKEarliestFiling", earliest).
		Str("NewCIKEarliestFiling", correctedSub.EarliestFilingDate()).
		Msg("sec: corrected misattributed CIK via name search")

	return correctedCIK, correctedSub
}

// historicalNameAt returns the entity name the issuer was using during
// the supplied window, preferring a formerName whose validity range
// covers the window over the current sub.Name. Empty window is treated
// as "active now", returning sub.Name. Used by fillFromSubmissions to
// avoid overwriting a historical asset's name with the entity's
// post-rename name (e.g. ACF was "AmeriCredit Corp" 1994-2010 even
// though CIK 0000804269 is now "General Motors Financial Company").
func historicalNameAt(sub *SubmissionsResponse, listingDate, delistingDate string) string {
	if sub == nil {
		return ""
	}

	windowStart := strings.TrimSpace(listingDate)
	windowEnd := strings.TrimSpace(delistingDate)

	if windowStart == "" && windowEnd == "" {
		return sub.Name
	}

	for _, fn := range sub.FormerNames {
		from := dateOnly(fn.From)
		to := dateOnly(fn.To)

		// A formerName overlaps the asset's life when its [from, to]
		// window intersects the asset's [listing, delisting] window.
		// Open-ended fields (empty from/to) are treated as ±infinity.
		if windowEnd != "" && from != "" && from > windowEnd {
			continue
		}

		if windowStart != "" && to != "" && to < windowStart {
			continue
		}

		if strings.TrimSpace(fn.Name) == "" {
			continue
		}

		return fn.Name
	}

	return sub.Name
}

// dateOnly strips the time portion of an ISO timestamp ("2010-10-06T00:00:00.000Z" -> "2010-10-06").
// Empty input returns empty.
func dateOnly(ts string) string {
	s := strings.TrimSpace(ts)
	if len(s) < 10 {
		return s
	}

	return s[:10]
}

// fillFromSubmissions applies the response to the asset. Only empty
// fields are filled; previously-resolved data is not overwritten. SIC
// parsing skips silently on a malformed string.
func fillFromSubmissions(asset *data.Asset, sub *SubmissionsResponse, logger *zerolog.Logger) {
	// Prefer a formerName whose validity range overlaps the asset's
	// life window over the current sub.Name. SEC keeps one CIK across
	// renames (e.g. CIK 0000804269 was "AMERICREDIT CORP" 1994-2010,
	// then renamed "General Motors Financial Company"), so the
	// current-state name would be wrong for a historical ticker like
	// ACF that delisted before the rename.
	if asset.Name == "" {
		if name := historicalNameAt(sub, asset.ListingDate, asset.DelistingDate); name != "" {
			asset.Name = name
		}
	}

	if (asset.SIC == nil || *asset.SIC == 0) && sub.SIC != "" {
		if n, err := strconv.Atoi(sub.SIC); err == nil {
			asset.SIC = &n
		}
	}

	if asset.Description == "" && sub.Description != "" {
		asset.Description = sub.Description
	}

	if asset.CorporateUrl == "" && sub.Website != "" {
		asset.CorporateUrl = sub.Website
	}

	if asset.HeadquartersLocation == "" {
		if biz, ok := sub.Addresses["business"]; ok && biz.City != "" {
			if biz.StateOrCountry != "" {
				asset.HeadquartersLocation = fmt.Sprintf("%s, %s", biz.City, biz.StateOrCountry)
			} else {
				asset.HeadquartersLocation = biz.City
			}
		}
	}

	if sub.LEI != "" {
		if asset.OtherIdentifiers == nil {
			asset.OtherIdentifiers = map[string]string{}
		}

		if _, exists := asset.OtherIdentifiers["lei"]; !exists {
			asset.OtherIdentifiers["lei"] = sub.LEI
		}
	}

	if len(sub.FormerNames) > 0 && logger != nil {
		names := make([]string, 0, len(sub.FormerNames))
		for _, fn := range sub.FormerNames {
			names = append(names, fn.Name)
		}

		logger.Debug().
			Str("Ticker", asset.Ticker).
			Str("CIK", asset.CIK).
			Strs("FormerNames", names).
			Msg("sec: submissions returned former-name history (not yet persisted)")
	}
}
