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
	"strings"
	"sync"

	"github.com/goccy/go-json"
	"github.com/rs/zerolog"
	"golang.org/x/sync/singleflight"
)

// nameSearchURL is the SEC EDGAR full-text-search index endpoint.
const nameSearchURL = "https://efts.sec.gov/LATEST/search-index"

// nameSearchResp mirrors the subset of the EDGAR search response we consume.
type nameSearchResp struct {
	Hits struct {
		Hits []struct {
			Source struct {
				CIKs         []string `json:"ciks"`
				DisplayNames []string `json:"display_names"`
				Form         string   `json:"form"`
				FileDate     string   `json:"file_date"`
			} `json:"_source"`
		} `json:"hits"`
	} `json:"hits"`
}

// Process-wide cache keyed by (normalized name, year).
var (
	nameSearchCacheMu sync.RWMutex
	nameSearchCache   = map[string]string{}

	// nameSearchSingleflight dedupes concurrent calls for the same bucket.
	nameSearchSingleflight singleflight.Group
)

// nameSearchCacheKey returns the cache key for a (name, year) pair.
// Year-bucketing lets reused names across different listing eras stay distinct.
func nameSearchCacheKey(normalized string, year int) string {
	return fmt.Sprintf("%s|%d", normalized, year)
}

// normalizeSearchName lowercases the name, replaces punctuation with spaces,
// and strips trailing corporate-suffix words (Corp/Inc/Co/Ltd/SA/LLC/...).
func normalizeSearchName(name string) string {
	// Replace punctuation with spaces so dotted suffixes ("Inc.", "L.L.C.")
	// reach the trailing-word check.
	s := strings.ToLower(strings.TrimSpace(name))
	if s == "" {
		return ""
	}

	s = strings.Map(func(r rune) rune {
		switch r {
		case ',', '.', '&', '/', '\\':
			return ' '
		}

		return r
	}, s)

	// Recombine recognized corporate-suffix forms that fragmented when we
	// stripped dots ("l l c" -> "llc").
	for _, pair := range [][2]string{
		{" l l c", " llc"}, {" l l p", " llp"},
		{" n v", " nv"}, {" s a", " sa"}, {" a g", " ag"},
	} {
		s = strings.ReplaceAll(s, pair[0], pair[1])
	}

	// Strip trailing corporate-suffix words by word equality (not suffix
	// matching) so stacked suffixes like "Holdings Group Inc" collapse fully.
	suffixWords := map[string]struct{}{
		"corporation": {}, "corp": {},
		"incorporated": {}, "inc": {},
		"company": {}, "co": {},
		"limited": {}, "ltd": {},
		"llc": {}, "llp": {},
		"plc": {},
		"sa":  {}, "ag": {}, "nv": {},
		"trust": {}, "holdings": {}, "group": {},
	}

	words := strings.Fields(s)
	for len(words) > 0 {
		last := words[len(words)-1]
		if _, ok := suffixWords[last]; !ok {
			break
		}

		words = words[:len(words)-1]
	}

	return strings.Join(words, " ")
}

// FindCIKByName searches EDGAR for filings whose issuer name matches name in
// a window around year and returns the most-frequent CIK. Requires at least
// two hits to claim a match; returns ("", false) on no match or when SEC's
// submissions client is not configured. Honours submissionsLimiter so the
// combined SEC call rate stays under the 10 req/s fair-access limit.
func FindCIKByName(ctx context.Context, name string, year int) (string, bool) {
	normalized := normalizeSearchName(name)
	if normalized == "" {
		return "", false
	}

	key := nameSearchCacheKey(normalized, year)

	nameSearchCacheMu.RLock()

	cached, ok := nameSearchCache[key]

	nameSearchCacheMu.RUnlock()

	if ok {
		if cached == "" {
			return "", false
		}

		return cached, true
	}

	res, _, _ := nameSearchSingleflight.Do(key, func() (any, error) {
		nameSearchCacheMu.RLock()

		cached, ok := nameSearchCache[key]

		nameSearchCacheMu.RUnlock()

		if ok {
			return cached, nil
		}

		cik, _ := fetchAndCacheCIKByName(ctx, name, normalized, year, key)

		return cik, nil
	})

	cik, _ := res.(string)
	if cik == "" {
		return "", false
	}

	return cik, true
}

// fetchAndCacheCIKByName runs the EDGAR query and caches the result. Two
// attempts: exact-phrase on the original name, then on the suffix-stripped
// form if the first returned nothing. Both pipe through pickBestCIK, which
// re-checks the display-name normalization gate.
func fetchAndCacheCIKByName(ctx context.Context, originalName, normalized string, year int, key string) (string, error) {
	cik, err := searchForCIK(ctx, originalName, normalized, year)
	if err != nil {
		return "", err
	}

	if cik == "" {
		lowered := strings.ToLower(strings.TrimSpace(originalName))
		if normalized != "" && normalized != lowered {
			retryCIK, retryErr := searchForCIK(ctx, normalized, normalized, year)
			if retryErr == nil && retryCIK != "" {
				cik = retryCIK
			}
		}
	}

	nameSearchCacheMu.Lock()
	nameSearchCache[key] = cik
	nameSearchCacheMu.Unlock()

	return cik, nil
}

// searchForCIK issues one exact-phrase EDGAR full-text search within a
// ±3 year window. The query intentionally omits `forms=`: EDGAR's full-text
// backend treats a comma-separated list as a single literal string and
// filters the result set to almost nothing. Precision comes from the
// display-name gate in pickBestCIK.
func searchForCIK(ctx context.Context, query, normalized string, year int) (string, error) {
	client := submissionsHTTPClient()
	if client == nil {
		return "", nil
	}

	startYear := year - 3
	endYear := year + 3

	startdt := fmt.Sprintf("%04d-01-01", startYear)
	enddt := fmt.Sprintf("%04d-12-31", endYear)

	quoted := fmt.Sprintf(`"%s"`, strings.TrimSpace(query))

	req := client.R().
		SetContext(ctx).
		SetQueryParam("q", quoted).
		SetQueryParam("dateRange", "custom").
		SetQueryParam("startdt", startdt).
		SetQueryParam("enddt", enddt)

	logger := zerolog.Ctx(ctx)
	logger.Debug().
		Str("Query", query).
		Str("Normalized", normalized).
		Str("From", startdt).
		Str("To", enddt).
		Msg("sec: GET /search-index (name search)")

	resp, err := req.Get(nameSearchURL)
	if err != nil {
		return "", fmt.Errorf("sec name search %s: %w", query, err)
	}

	if resp.StatusCode() != http.StatusOK {
		return "", fmt.Errorf("sec name search %s: status %d", query, resp.StatusCode())
	}

	var parsed nameSearchResp
	if err := json.Unmarshal(resp.Body(), &parsed); err != nil {
		return "", fmt.Errorf("sec name search decode %s: %w", query, err)
	}

	return pickBestCIK(parsed.Hits.Hits, normalized), nil
}

// pickBestCIK returns the most-frequent CIK among hits whose display name
// normalizes to the same key as normalized. Requires at least two hits with
// the winning CIK; a single hit could be coincidence.
func pickBestCIK(hits []struct {
	Source struct {
		CIKs         []string `json:"ciks"`
		DisplayNames []string `json:"display_names"`
		Form         string   `json:"form"`
		FileDate     string   `json:"file_date"`
	} `json:"_source"`
}, normalized string) string {
	counts := map[string]int{}

	for _, h := range hits {
		for i, c := range h.Source.CIKs {
			if c == "" {
				continue
			}

			// Verify the display name normalizes to the same key.
			displayName := ""
			if i < len(h.Source.DisplayNames) {
				displayName = h.Source.DisplayNames[i]
			}

			if normalizeDisplayName(displayName) != normalized {
				continue
			}

			counts[c]++
		}
	}

	if len(counts) == 0 {
		return ""
	}

	var (
		bestCIK   string
		bestCount int
	)

	for c, n := range counts {
		if n > bestCount {
			bestCIK = c
			bestCount = n
		}
	}

	if bestCount < 2 {
		return ""
	}

	return bestCIK
}

// normalizeDisplayName strips the "(CIK ##########)" trailing annotation
// EDGAR appends to display_names and then runs normalizeSearchName.
func normalizeDisplayName(displayName string) string {
	s := displayName

	if idx := strings.LastIndex(s, "(CIK"); idx != -1 {
		s = s[:idx]
	}

	return normalizeSearchName(s)
}
