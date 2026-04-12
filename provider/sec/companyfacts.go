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
	"archive/zip"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/rs/zerolog/log"
	"github.com/tidwall/gjson"
	"golang.org/x/time/rate"
)

const (
	companyFactsURL    = "https://data.sec.gov/api/xbrl/companyfacts/"
	companyFactsZipURL = "https://www.sec.gov/Archives/edgar/daily-index/xbrl/companyfacts.zip"
	companyTickersURL  = "https://www.sec.gov/files/company_tickers.json"
	// edgarFeedPageSize is the number of entries returned per EDGAR feed page.
	edgarFeedPageSize = 100
	// edgarFeedURLFormat is a format string for the EDGAR ATOM feed; the %d
	// placeholder is the start offset used for pagination.
	edgarFeedURLFormat = "https://www.sec.gov/cgi-bin/browse-edgar?action=getcurrent&type=10-K%%2C10-Q&dateb=&owner=include&count=100&search_text=&start=%d&output=atom"
)

// Fact represents a single XBRL fact value from an SEC filing.
type Fact struct {
	End   time.Time // Period end date (always present)
	Start time.Time // Period start date (present for duration concepts; zero for instant concepts)
	Filed time.Time // Date the filing was submitted to SEC
	Val   float64   // The reported value
	// Accn is the SEC accession number identifying the filing that reported
	// this fact. It is currently parsed for completeness but not used for
	// dedup or correlation. A future enhancement could key on Accn to detect
	// "this exact filing was already processed" at the fact level, or to
	// group facts reported together in the same filing for provenance
	// tracking.
	Accn  string // SEC accession number
	Form  string // Filing form type (10-K, 10-Q)
	FP    string // Fiscal period (FY, Q1, Q2, Q3, Q4)
	Frame string // XBRL frame identifier (e.g. CY2023Q3I)
	FY    int    // Fiscal year
}

// CompanyFacts holds parsed SEC EDGAR companyfacts data for a single entity.
type CompanyFacts struct {
	CIK        int               // Central Index Key
	EntityName string            // Company name
	Facts      map[string][]Fact // Map of concept name to facts (e.g. "Assets" -> []Fact)
}

// unitPreference defines the priority order for selecting unit types.
// Lower index = higher preference.
var unitPreference = []string{"USD", "USD/shares", "shares", "pure"}

const dateFormat = "2006-01-02"

// xbrlNamespaces lists the XBRL taxonomy namespaces parsed from SEC EDGAR
// companyfacts JSON. "us-gaap" contains financial statement data (balance
// sheet, income statement, cash flow); "dei" (Document and Entity Information)
// contains filing metadata including EntityCommonStockSharesOutstanding -- the
// cover-page share count that Sharadar uses for sharesbas.
var xbrlNamespaces = []string{"us-gaap", "dei"}

// ParseCompanyFacts parses SEC EDGAR companyfacts JSON into a CompanyFacts struct.
// It only includes facts from 10-K and 10-Q filings and selects the preferred
// unit type when multiple are available for a concept.
func ParseCompanyFacts(jsonData []byte) (*CompanyFacts, error) {
	if !gjson.ValidBytes(jsonData) {
		return nil, fmt.Errorf("invalid JSON data")
	}

	root := gjson.ParseBytes(jsonData)

	cf := &CompanyFacts{
		CIK:        int(root.Get("cik").Int()),
		EntityName: root.Get("entityName").String(),
		Facts:      make(map[string][]Fact),
	}

	for _, ns := range xbrlNamespaces {
		nsData := root.Get("facts." + ns)
		if !nsData.Exists() {
			continue
		}

		parseNamespaceFacts(nsData, cf)
	}

	return cf, nil
}

// parseNamespaceFacts extracts XBRL facts from a single taxonomy namespace
// (e.g. us-gaap or dei) and merges them into cf.Facts.
func parseNamespaceFacts(nsData gjson.Result, cf *CompanyFacts) {
	nsData.ForEach(func(conceptName, conceptData gjson.Result) bool {
		units := conceptData.Get("units")
		if !units.Exists() {
			return true
		}

		// Select the preferred unit type
		var selectedUnit gjson.Result

		for _, unitName := range unitPreference {
			candidate := units.Get(unitName)
			if candidate.Exists() {
				selectedUnit = candidate

				break
			}
		}

		// If no preferred unit found, try the first available unit
		if !selectedUnit.Exists() {
			units.ForEach(func(_, unitData gjson.Result) bool {
				selectedUnit = unitData
				return false // stop after first
			})
		}

		if !selectedUnit.Exists() {
			return true
		}

		var facts []Fact

		selectedUnit.ForEach(func(_, entry gjson.Result) bool {
			form := entry.Get("form").String()

			// Only include 10-K and 10-Q filings
			if form != "10-K" && form != "10-Q" {
				return true
			}

			f := Fact{
				Val:   entry.Get("val").Float(),
				Accn:  entry.Get("accn").String(),
				Form:  form,
				FP:    entry.Get("fp").String(),
				Frame: entry.Get("frame").String(),
				FY:    int(entry.Get("fy").Int()),
			}

			// Parse end date
			if endStr := entry.Get("end").String(); endStr != "" {
				if t, err := time.Parse(dateFormat, endStr); err == nil {
					f.End = t
				}
			}

			// Parse start date (only present for duration concepts)
			if startStr := entry.Get("start").String(); startStr != "" {
				if t, err := time.Parse(dateFormat, startStr); err == nil {
					f.Start = t
				}
			}

			// Parse filed date
			if filedStr := entry.Get("filed").String(); filedStr != "" {
				if t, err := time.Parse(dateFormat, filedStr); err == nil {
					f.Filed = t
				}
			}

			facts = append(facts, f)

			return true
		})

		if len(facts) > 0 {
			// Sort facts by Filed ascending so ResolveFieldsForFiling can use
			// binary search to slice the prefix of facts available at any
			// given filing date. This is a one-time cost per concept that
			// avoids O(N) scans on every (period, filing-date) lookup.
			//
			// Use a stable sort so facts filed on the same day preserve their
			// JSON parse order; this keeps downstream resolution deterministic
			// when multiple facts share a filing date (e.g. comparative
			// balance-sheet entries reported in the same 10-K).
			sort.SliceStable(facts, func(i, j int) bool {
				return facts[i].Filed.Before(facts[j].Filed)
			})

			cf.Facts[conceptName.String()] = facts
		}

		return true
	})
}

// FetchCompanyFacts downloads the companyfacts JSON for a single CIK from SEC EDGAR.
func FetchCompanyFacts(ctx context.Context, client *resty.Client, cik int) (*CompanyFacts, error) {
	url := companyFactsURL + FormatCIK(cik) + ".json"

	resp, err := client.R().
		SetContext(ctx).
		Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetching companyfacts for CIK %d: %w", cik, err)
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("SEC returned status %d for CIK %d", resp.StatusCode(), cik)
	}

	return ParseCompanyFacts(resp.Body())
}

// DownloadCompanyFactsZip downloads and extracts the bulk companyfacts.zip file,
// calling processFn for each individual CIK JSON file. The download is streamed
// to a temp file on disk to avoid loading the entire ~1GB archive into memory;
// the zip itself requires random access (its central directory is at the end of
// the file), so it is then opened from the temp file using zip.OpenReader.
func DownloadCompanyFactsZip(ctx context.Context, client *resty.Client, processFn func(cik int, jsonData []byte) error) error {
	log.Info().Msg("downloading companyfacts.zip from SEC (this may take several minutes)")

	tmpFile, err := os.CreateTemp("", "sec-companyfacts-*.zip")
	if err != nil {
		return fmt.Errorf("creating temp file for companyfacts.zip: %w", err)
	}

	tmpName := tmpFile.Name()

	defer func() {
		if removeErr := os.Remove(tmpName); removeErr != nil && !os.IsNotExist(removeErr) {
			log.Warn().Err(removeErr).Str("file", tmpName).Msg("error removing temp file")
		}
	}()

	resp, err := client.R().
		SetContext(ctx).
		SetDoNotParseResponse(true).
		Get(companyFactsZipURL)
	if err != nil {
		if closeErr := tmpFile.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Str("file", tmpName).Msg("error closing temp file")
		}

		return fmt.Errorf("downloading companyfacts.zip: %w", err)
	}

	rawBody := resp.RawBody()

	defer func() {
		if closeErr := rawBody.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Msg("error closing companyfacts.zip response body")
		}
	}()

	if resp.StatusCode() != http.StatusOK {
		if closeErr := tmpFile.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Str("file", tmpName).Msg("error closing temp file")
		}

		return fmt.Errorf("SEC returned status %d for companyfacts.zip", resp.StatusCode())
	}

	if _, err := io.Copy(tmpFile, rawBody); err != nil {
		if closeErr := tmpFile.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Str("file", tmpName).Msg("error closing temp file")
		}

		return fmt.Errorf("streaming companyfacts.zip to temp file: %w", err)
	}

	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("closing temp file for companyfacts.zip: %w", err)
	}

	reader, err := zip.OpenReader(tmpName)
	if err != nil {
		return fmt.Errorf("opening companyfacts.zip: %w", err)
	}

	defer func() {
		if closeErr := reader.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Msg("error closing companyfacts.zip reader")
		}
	}()

	for _, f := range reader.File {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if filepath.Ext(f.Name) != ".json" {
			continue
		}

		// Extract CIK from filename (e.g., "CIK0000320193.json")
		base := strings.TrimSuffix(filepath.Base(f.Name), ".json")
		base = strings.TrimPrefix(base, "CIK")

		cik, err := strconv.Atoi(base)
		if err != nil {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			log.Warn().Err(err).Str("file", f.Name).Msg("error opening file in zip")
			continue
		}

		jsonData, err := io.ReadAll(rc)

		if closeErr := rc.Close(); closeErr != nil {
			log.Warn().Err(closeErr).Str("file", f.Name).Msg("error closing file in zip")
		}

		if err != nil {
			log.Warn().Err(err).Str("file", f.Name).Msg("error reading file in zip")
			continue
		}

		if err := processFn(cik, jsonData); err != nil {
			log.Warn().Err(err).Int("cik", cik).Msg("error processing companyfacts")
		}
	}

	return nil
}

// NewSECClient creates a resty HTTP client configured for SEC EDGAR API access.
func NewSECClient(userAgent string, limiter *rate.Limiter) *resty.Client {
	client := resty.New().
		SetHeader("User-Agent", userAgent).
		SetHeader("Accept", "application/json").
		SetTimeout(60 * time.Second).
		SetRetryCount(3).
		SetRetryWaitTime(5 * time.Second).
		AddRetryCondition(func(r *resty.Response, err error) bool {
			return r != nil && (r.StatusCode() == http.StatusTooManyRequests || r.StatusCode() >= 500)
		}).
		OnBeforeRequest(func(_ *resty.Client, r *resty.Request) error {
			return limiter.Wait(r.Context())
		})

	return client
}
