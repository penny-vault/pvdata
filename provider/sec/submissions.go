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

// SubmissionsResponse mirrors the subset of data.sec.gov/submissions/CIK*.json
// used for asset enrichment.
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

// FilingsBlock is the top of the filings tree in the submissions payload.
// `files[].filingFrom` lets us find the earliest filing date without
// fetching each overflow file.
type FilingsBlock struct {
	Recent FilingsRecent `json:"recent"`
	Files  []FilingsFile `json:"files"`
}

// FilingsRecent holds the most recent batch of filings (typically up to
// ~1000). Arrays are parallel and ordered newest-first.
type FilingsRecent struct {
	FilingDate []string `json:"filingDate"`
	Form       []string `json:"form"`
}

// FilingsFile is an overflow file pointer holding older filings. filingFrom
// / filingTo bracket the date range inside the file.
type FilingsFile struct {
	Name        string `json:"name"`
	FilingFrom  string `json:"filingFrom"`
	FilingTo    string `json:"filingTo"`
	FilingCount int    `json:"filingCount"`
}

// EarliestFilingDate returns the earliest filing date (YYYY-MM-DD) across
// both the `recent` batch and the overflow `files` metadata, or "".
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

// EarliestFilingDateForForm returns the earliest filing date in the recent
// batch whose form starts with prefix (case-insensitive), or "". Used to
// recover fund launch dates (N-1A for ETF/MF, N-2 for CEF) so multi-fund
// sponsors don't get assigned the parent issuer's earliest filing. Only the
// `recent` batch is scanned because overflow files don't carry per-filing
// form metadata.
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
// submissions payload. `isForeignLocation` is omitted because SEC returns
// it as an integer or null, not a bool.
type Address struct {
	Street1        string `json:"street1"`
	Street2        string `json:"street2"`
	City           string `json:"city"`
	StateOrCountry string `json:"stateOrCountry"`
	ZIPCode        string `json:"zipCode"`
}

// FormerName captures one entry from the historical name list. Dates are
// ISO timestamps when SEC returned them.
type FormerName struct {
	Name string `json:"name"`
	From string `json:"from"`
	To   string `json:"to"`
}

// submissionsRateLimit stays one under SEC's published 10 req/s policy for
// headroom across concurrent callers.
const submissionsRateLimit = 9

var (
	submissionsClientOnce sync.Once
	submissionsClient     *resty.Client
	submissionsLimiter    = rate.NewLimiter(rate.Limit(submissionsRateLimit), 1)

	submissionsCacheMu sync.RWMutex
	submissionsCache   = map[int]*SubmissionsResponse{}

	// submissionsSingleflight dedupes concurrent FetchSubmissions calls.
	submissionsSingleflight singleflight.Group
)

// defaultSECUserAgent is sent when `sec.userAgent` is unset. SEC's fair-access
// policy requires a valid User-Agent.
const defaultSECUserAgent = "pvdata/1.0 sec@pennyvault.com"

// secUserAgent returns the configured User-Agent or defaultSECUserAgent.
func secUserAgent() string {
	if ua := viper.GetString("sec.userAgent"); ua != "" {
		return ua
	}

	return defaultSECUserAgent
}

// submissionsHTTPClient returns the lazily-initialized resty client used for
// /submissions requests.
func submissionsHTTPClient() *resty.Client {
	submissionsClientOnce.Do(func() {
		submissionsClient = NewSECClient(secUserAgent(), submissionsLimiter)
	})

	return submissionsClient
}

// FetchSubmissions returns the parsed /submissions/CIK*.json payload, cached
// process-wide. Returns (nil, nil) when the CIK is unknown (404).
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

	key := strconv.Itoa(cik)

	res, err, _ := submissionsSingleflight.Do(key, func() (any, error) {
		// Re-check cache inside the singleflight in case another caller
		// landed the result while we were queueing.
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

// EnrichSubmissions fills empty descriptive fields (Name, SIC, Description,
// CorporateUrl, HeadquartersLocation, OtherIdentifiers["lei"]) on each asset
// from the SEC submissions endpoint. Only operates on assets with a CIK and
// only fills previously-empty fields. formerNames are not persisted yet but
// are logged at debug.
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
		// successor's CIK (ACF -> CIK 1405287 "Stream Global Services" when
		// the historical AmeriCredit had CIK 804269). Signal: SEC's earliest
		// filing for the supplied CIK postdates the asset's ListingDate by a
		// meaningful margin.
		if corrected, correctedSub := correctMisattributedCIK(ctx, asset, sub, logger); correctedSub != nil {
			asset.CIK = corrected
			sub = correctedSub
		}

		fillFromSubmissions(asset, sub, logger)
	}
}

// misattributionGraceDays caps the plausible lag between IPO and first SEC
// filing; beyond one year we treat the CIK as belonging to a different entity.
const misattributionGraceDays = 365

// correctMisattributedCIK detects when sub's CIK appears to belong to a
// different entity than the historical asset and, if so, searches SEC by
// asset.Name for the correct CIK. Returns ("", nil) when no swap is needed.
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

// historicalNameAt returns the entity name the issuer used during the supplied
// window, preferring a formerName whose validity range overlaps the window
// over the current sub.Name. SEC keeps one CIK across renames so the
// current-state name would be wrong for a delisted historical ticker.
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

		// Open-ended from/to are treated as ±infinity.
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

// dateOnly returns the YYYY-MM-DD prefix of an ISO timestamp.
func dateOnly(ts string) string {
	s := strings.TrimSpace(ts)
	if len(s) < 10 {
		return s
	}

	return s[:10]
}

// fillFromSubmissions applies sub to asset, only filling empty fields.
func fillFromSubmissions(asset *data.Asset, sub *SubmissionsResponse, logger *zerolog.Logger) {
	// Prefer a formerName covering the asset's life window over sub.Name:
	// SEC keeps one CIK across renames, so a delisted historical ticker
	// would otherwise inherit the entity's post-rename name.
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
