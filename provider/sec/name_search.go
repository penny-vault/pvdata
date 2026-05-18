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

// nameSearchURL is the SEC EDGAR full-text-search index endpoint. It
// accepts a query string and optional form/date filters and returns a
// JSON document of filing hits, each tagged with the issuing CIK and
// display name. We use it to recover the correct historical CIK for a
// ticker whose Massive-provided CIK turns out to be a different
// entity's (typically a same-ticker successor's).
const nameSearchURL = "https://efts.sec.gov/LATEST/search-index"

// nameSearchResp mirrors the subset of the EDGAR search response we
// consume. `hits.hits[]._source.ciks` is a string array of zero-padded
// CIKs; `display_names` is parallel and useful for diagnostics.
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

// nameSearchCache is a process-wide cache keyed by the normalized
// (name, year) tuple the search was run with. Same name across many
// records resolves to one network call per (name, year) bucket.
var (
	nameSearchCacheMu sync.RWMutex
	nameSearchCache   = map[string]string{}

	// nameSearchSingleflight dedupes concurrent FindCIKByName calls for
	// the same (name, year) bucket so a thundering herd of walk workers
	// generates one EDGAR search call, not N.
	nameSearchSingleflight singleflight.Group
)

// nameSearchCacheKey returns the cache key for a (name, year) pair.
// Year-bucketing lets two records with the same name but very different
// listing eras (rare, but happens with reused names) keep distinct
// results.
func nameSearchCacheKey(normalized string, year int) string {
	return fmt.Sprintf("%s|%d", normalized, year)
}

// normalizeSearchName lowercases and trims the name and strips common
// corporate suffixes so a Massive name ("AMERICREDIT CORP") and an SEC
// display name ("AMERICREDIT CORP  (CIK 0000804269)") collapse to the
// same key. Suffix stripping is applied conservatively — we drop only
// the trailing tokens that don't disambiguate (Corp/Inc/Co/Ltd/SA/LLC).
func normalizeSearchName(name string) string {
	// Lowercase + replace punctuation with spaces so suffixes like
	// "Inc." or "L.L.C." that carry an internal dot are still
	// reachable by the trailing-word check below.
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

	// Recombine single-letter dotted abbreviations that fragmented when
	// we stripped dots above ("l l c" -> "llc", "n v" -> "nv"). Only
	// the recognized corporate-suffix forms; we don't want to merge
	// arbitrary single letters in the name itself.
	for _, pair := range [][2]string{
		{" l l c", " llc"}, {" l l p", " llp"},
		{" n v", " nv"}, {" s a", " sa"}, {" a g", " ag"},
	} {
		s = strings.ReplaceAll(s, pair[0], pair[1])
	}

	// Strip trailing corporate-suffix words one at a time using word
	// equality (not suffix matching). This handles "Corp" alone (which
	// collapses to "") and stacked suffixes like "Holdings Group Inc"
	// without leaving fragments behind. Suffixes are a closed set; any
	// remaining word is treated as part of the entity's identity.
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

// FindCIKByName searches SEC EDGAR for 10-K-style filings whose issuer
// display name matches the supplied name in a window around the
// supplied year. Returns the most-frequently-occurring CIK in the
// result set ("most filings under this name in this era"), which is
// the strongest signal for "this is the right historical entity."
//
// Returns ("", false) when:
//   - name is empty
//   - SEC's submissions client is not configured (rate limiter / UA
//     would not be set up otherwise)
//   - the search returns no matches
//   - the top-frequency CIK appears fewer than 2 times (single hit could
//     easily be a coincidence; demand at least two filings under this
//     name in the window)
//
// Cached in-memory by (normalizedName, year) for the lifetime of the
// process so repeat searches across many records of the same issuer
// don't re-hit EDGAR. Honours the same submissionsLimiter as the
// per-CIK submissions endpoint so the combined SEC call rate stays
// under the 10 req/s fair-access limit.
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

	// singleflight collapses parallel cache-misses for the same
	// (normalizedName, year) into one EDGAR search request.
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

// fetchAndCacheCIKByName performs the network calls and populates
// the cache. Split from FindCIKByName for unit-testability of the
// caching/normalization layer independent of the network.
//
// Two attempts are made, in order:
//
//  1. Exact-phrase search using the original name. Most precise; for
//     entities whose SEC display name matches the asset's name
//     verbatim, this returns the right CIK directly.
//  2. Exact-phrase search using the suffix-stripped normalized form,
//     fired only when the first attempt produced no candidate and
//     the stripped form differs from the original. This catches
//     entities whose SEC display name uses a different corporate
//     suffix than the asset's name (e.g., Massive says "ZALE
//     CORPORATION" while Zale's own SEC filings use "ZALE CORP").
//
// Both attempts pipe their hits through pickBestCIK, which requires
// the candidate's display name to normalize to the same key as the
// asset's name and to appear in at least two filings. The relaxed
// second query widens the search net but does not loosen that gate,
// so unrelated filings that merely mention the asset's name in body
// text never end up returned as the candidate CIK.
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

// searchForCIK issues a single exact-phrase EDGAR full-text search
// for query within a ±3 year window around year and returns whatever
// CIK pickBestCIK selects from the hits using normalized as the
// display-name gate. The function performs no caching; the caller is
// responsible for storing the final result.
//
// The query intentionally does not pass a `forms=` filter. EDGAR's
// full-text-search backend does not parse the comma-separated list
// as multiple values; passing `forms=10-K,10-K/A,...` is treated as
// a single literal string that never matches a real form code, and
// the result set is filtered down to almost nothing — even the
// asset's own 10-K filings disappear. The display-name normalization
// gate inside pickBestCIK is what provides precision; restricting to
// 10-K family forms only mattered as an optimization, and removing
// it actually improves the match rate without weakening any
// correctness check.
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

// pickBestCIK chooses the CIK that appears most often in hits whose
// display name normalizes to the same key as the supplied normalized
// name. The most-frequent CIK is the strongest signal that we found
// the right entity, and the display-name re-check protects against
// SEC's tokenizer occasionally returning matches whose display name
// doesn't actually contain the search phrase.
//
// Requires at least two hits with the winning CIK to claim a match;
// a single hit could be coincidence.
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

// normalizeDisplayName strips the "  (CIK ##########)" trailing
// annotation EDGAR appends to display_names and then runs the same
// normalization as normalizeSearchName.
func normalizeDisplayName(displayName string) string {
	s := displayName

	if idx := strings.LastIndex(s, "(CIK"); idx != -1 {
		s = s[:idx]
	}

	return normalizeSearchName(s)
}
