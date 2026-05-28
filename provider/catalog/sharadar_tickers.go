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
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/rs/zerolog"

	"github.com/penny-vault/pvdata/data"
)

// sharadarRecord captures one row from the Sharadar TICKERS table in
// the subset of fields catalog uses for backstop enrichment. One
// (ticker, holding-entity) pair per record; reused tickers produce
// multiple records.
type sharadarRecord struct {
	Ticker         string
	BaseTicker     string
	Suffix         int
	PermaTicker    string
	Name           string
	CUSIPs         []string
	CIK            string
	SICCode        int
	Sector         string
	Industry       string
	SimilarTickers []string
	Location       string
	CorporateURL   string
	FirstPriceDate time.Time
	LastPriceDate  time.Time
	IsDelisted     bool
	LastUpdated    time.Time
}

// sharadarTickerIndex groups sharadarRecord values by their suffix-
// stripped base ticker. Sharadar's TICKERS file disambiguates reused
// tickers by appending a numeric suffix, with the bare ticker
// reserved for the most recent holder — verified empirically:
//
//	AAC      ARES ACQUISITION CORP            2021 → 2023   (bare = most recent)
//	AAC1     ARCADIA FINANCIAL LTD            1992 → 2000   (older holder)
//	ABI1     APPLIED BIOSYSTEMS INC           1986 → 2008   (no bare; 1 = most recent)
//	ABI2     AMERICAN BANKERS INSURANCE GROUP 1986 → 1999   (older than ABI1)
//
// Each pvdata lifecycle is matched to the Sharadar record whose
// LastPriceDate is closest to the lifecycle's End — that is the
// entity-transition date, the only signal that survives Sharadar's
// habit of stamping pre-modern records with FirstPriceDate=1986-01-01.
type sharadarTickerIndex struct {
	byBase map[string][]sharadarRecord
}

// sharadarDataStartSentinel is the 1986-01-01 stamp Sharadar applies
// to any pre-1986 listing — its data window begins there.
var sharadarDataStartSentinel = time.Date(1986, 1, 1, 0, 0, 0, 0, time.UTC)

// sharadarListDateUsable reports whether sharadar's FirstPriceDate is
// safe to use as a listing date for a lifecycle starting at rngStart.
func sharadarListDateUsable(firstPriceDate, rngStart time.Time) bool {
	if firstPriceDate.IsZero() {
		return false
	}

	if !firstPriceDate.Before(rngStart) {
		return false
	}

	if firstPriceDate.Equal(sharadarDataStartSentinel) {
		return false
	}

	return true
}

var sharadarSuffixPattern = regexp.MustCompile(`^(.*?)(\d+)$`)

var sharadarCIKPattern = regexp.MustCompile(`[?&]CIK=0*(\d+)`)

// loadSharadarTickerIndex reads the most-recent Sharadar TICKERS file
// in dir and returns a per-base-ticker index. dir == "" disables
// Sharadar enrichment and returns (nil, nil).
func loadSharadarTickerIndex(ctx context.Context, dir string) (*sharadarTickerIndex, error) {
	logger := zerolog.Ctx(ctx)

	dir = strings.TrimSpace(dir)
	if dir == "" {
		return nil, nil
	}

	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			logger.Warn().Str("Dir", dir).Msg("catalog sharadar: directory does not exist; skipping Sharadar enrichment")
			return nil, nil
		}

		return nil, fmt.Errorf("stat sharadar dir %s: %w", dir, err)
	}

	if !info.IsDir() {
		return nil, fmt.Errorf("catalog sharadar: %s is not a directory", dir)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "*TICKERS*.zst"))
	if err != nil {
		return nil, fmt.Errorf("glob sharadar tickers in %s: %w", dir, err)
	}

	if len(matches) == 0 {
		logger.Warn().Str("Dir", dir).Msg("catalog sharadar: no *TICKERS*.zst files found; skipping Sharadar enrichment")
		return nil, nil
	}

	sort.Sort(sort.Reverse(sort.StringSlice(matches)))
	chosen := matches[0]

	logger.Info().Str("File", chosen).Int("Candidates", len(matches)).Msg("catalog sharadar: loading tickers")

	loadStart := time.Now()

	records, err := parseSharadarTickersFile(chosen)
	if err != nil {
		return nil, fmt.Errorf("parse sharadar tickers %s: %w", chosen, err)
	}

	idx := buildSharadarIndex(records)

	logger.Info().
		Int("Records", len(records)).
		Int("BaseTickers", len(idx.byBase)).
		Dur("Elapsed", time.Since(loadStart).Round(time.Millisecond)).
		Msg("catalog sharadar: tickers loaded")

	return idx, nil
}

// parseSharadarTickersFile opens path, decompresses the zstd stream,
// and parses every CSV row into a sharadarRecord.
func parseSharadarTickersFile(path string) ([]sharadarRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	dec, err := zstd.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("zstd reader for %s: %w", path, err)
	}
	defer dec.Close()

	return parseSharadarTickersCSV(dec)
}

// parseSharadarTickersCSV consumes a CSV stream from r and returns
// one sharadarRecord per data row. Header is read to map column
// names → indices, so the parser is resilient to column reorderings.
func parseSharadarTickersCSV(r io.Reader) ([]sharadarRecord, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1

	header, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read header: %w", err)
	}

	colIdx := make(map[string]int, len(header))
	for i, h := range header {
		colIdx[strings.ToLower(strings.TrimSpace(h))] = i
	}

	get := func(row []string, name string) string {
		i, ok := colIdx[name]
		if !ok || i >= len(row) {
			return ""
		}

		return row[i]
	}

	records := make([]sharadarRecord, 0, 32000)

	for {
		row, err := cr.Read()
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("read row %d: %w", len(records)+1, err)
		}

		ticker := strings.TrimSpace(get(row, "ticker"))
		if ticker == "" {
			continue
		}

		base, suffix := splitSharadarSuffix(ticker)

		rec := sharadarRecord{
			Ticker:       sharadarNormalizeTicker(ticker),
			BaseTicker:   sharadarNormalizeTicker(base),
			Suffix:       suffix,
			PermaTicker:  strings.TrimSpace(get(row, "permaticker")),
			Name:         strings.TrimSpace(get(row, "name")),
			CUSIPs:       splitSpaceSeparated(get(row, "cusips")),
			CIK:          extractSharadarCIK(get(row, "secfilings")),
			SICCode:      atoiOrZero(strings.TrimSpace(get(row, "siccode"))),
			Sector:       strings.TrimSpace(get(row, "sector")),
			Industry:     strings.TrimSpace(get(row, "industry")),
			Location:     strings.TrimSpace(get(row, "location")),
			CorporateURL: strings.TrimSpace(get(row, "companysite")),
			IsDelisted:   strings.EqualFold(strings.TrimSpace(get(row, "isdelisted")), "Y"),
		}

		for _, t := range splitSpaceSeparated(get(row, "relatedtickers")) {
			rec.SimilarTickers = append(rec.SimilarTickers, sharadarNormalizeTicker(t))
		}

		rec.FirstPriceDate = parseSharadarDate(get(row, "firstpricedate"))
		rec.LastPriceDate = parseSharadarDate(get(row, "lastpricedate"))
		rec.LastUpdated = parseSharadarDate(get(row, "lastupdated"))

		records = append(records, rec)
	}

	return records, nil
}

// buildSharadarIndex groups records by BaseTicker. When two rows
// share both BaseTicker and Suffix, the row with the more recent
// LastUpdated wins.
func buildSharadarIndex(records []sharadarRecord) *sharadarTickerIndex {
	byKey := make(map[string]sharadarRecord, len(records))

	for _, r := range records {
		key := r.BaseTicker + "|" + strconv.Itoa(r.Suffix)
		if existing, ok := byKey[key]; ok && !r.LastUpdated.After(existing.LastUpdated) {
			continue
		}

		byKey[key] = r
	}

	byBase := make(map[string][]sharadarRecord, len(byKey))
	for _, r := range byKey {
		byBase[r.BaseTicker] = append(byBase[r.BaseTicker], r)
	}

	for base, list := range byBase {
		sort.Slice(list, func(i, j int) bool {
			return list[i].Suffix < list[j].Suffix
		})
		byBase[base] = list
	}

	return &sharadarTickerIndex{byBase: byBase}
}

// lookupByLifecycle returns the sharadarRecord whose holding window
// best matches the given lifecycle range, or nil when no record
// plausibly held the ticker during the lifecycle.
func (idx *sharadarTickerIndex) lookupByLifecycle(ticker string, rng dateRange) *sharadarRecord {
	if idx == nil {
		return nil
	}

	candidates := idx.byBase[ticker]
	if len(candidates) == 0 {
		return nil
	}

	var best *sharadarRecord

	var bestDistance time.Duration

	for i := range candidates {
		rec := &candidates[i]

		first := rec.FirstPriceDate
		last := rec.LastPriceDate

		if first.IsZero() && last.IsZero() {
			continue
		}

		if !first.IsZero() && first.After(rng.End) {
			continue
		}

		if !last.IsZero() && last.Before(rng.Start) {
			continue
		}

		var distance time.Duration
		if last.IsZero() {
			distance = 0
		} else {
			distance = last.Sub(rng.End)
			if distance < 0 {
				distance = -distance
			}
		}

		if best == nil || distance < bestDistance {
			best = rec
			bestDistance = distance
		}
	}

	return best
}

// applySharadarEnrichment fills missing fields on asset using rec.
// Strict backstop semantics: pvdata's existing value always wins.
func applySharadarEnrichment(ctx context.Context, asset *data.Asset, rec *sharadarRecord) {
	if rec == nil || asset == nil {
		return
	}

	logger := zerolog.Ctx(ctx)

	if len(asset.CUSIP) == 0 && len(rec.CUSIPs) > 0 {
		asset.CUSIP = append([]string(nil), rec.CUSIPs...)
	}

	if asset.CIK == "" {
		asset.CIK = rec.CIK
	} else if rec.CIK != "" && strings.TrimLeft(asset.CIK, "0") != strings.TrimLeft(rec.CIK, "0") {
		logger.Debug().
			Str("Ticker", asset.Ticker).
			Str("CatalogCIK", asset.CIK).
			Str("SharadarCIK", rec.CIK).
			Msg("catalog sharadar: CIK disagreement; keeping pvdata value")
	}

	if asset.Sector == "" {
		asset.Sector = rec.Sector
	}

	if asset.Industry == "" {
		asset.Industry = rec.Industry
	}

	if asset.SIC == nil || *asset.SIC == 0 {
		if rec.SICCode > 0 {
			v := rec.SICCode
			asset.SIC = &v
		}
	}

	if len(asset.SimilarTickers) == 0 && len(rec.SimilarTickers) > 0 {
		asset.SimilarTickers = append([]string(nil), rec.SimilarTickers...)
	}

	if rec.PermaTicker == "" {
		return
	}

	if asset.OtherIdentifiers == nil {
		asset.OtherIdentifiers = map[string]string{}
	}

	if _, set := asset.OtherIdentifiers["sharadar"]; !set {
		asset.OtherIdentifiers["sharadar"] = rec.PermaTicker
	}
}

// splitSharadarSuffix parses a Sharadar ticker into (base, suffix).
func splitSharadarSuffix(ticker string) (string, int) {
	ticker = strings.TrimSpace(ticker)
	if ticker == "" {
		return "", 0
	}

	m := sharadarSuffixPattern.FindStringSubmatch(ticker)
	if m == nil || m[1] == "" {
		return ticker, 0
	}

	n, err := strconv.Atoi(m[2])
	if err != nil {
		return ticker, 0
	}

	return m[1], n
}

// sharadarNormalizeTicker maps Sharadar's class-share separator (".")
// to pvdata's ("/") so a lookup against the catalog's tickers joins
// cleanly.
func sharadarNormalizeTicker(ticker string) string {
	return strings.ReplaceAll(strings.TrimSpace(ticker), ".", "/")
}

// splitSpaceSeparated splits a space-separated list into trimmed,
// non-empty entries.
func splitSpaceSeparated(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}

	parts := strings.Fields(s)

	out := make([]string, 0, len(parts))

	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		out = append(out, p)
	}

	return out
}

// extractSharadarCIK parses the CIK from a Sharadar secfilings URL.
func extractSharadarCIK(url string) string {
	m := sharadarCIKPattern.FindStringSubmatch(url)
	if len(m) != 2 {
		return ""
	}

	return m[1]
}

// parseSharadarDate parses a YYYY-MM-DD date string and returns the
// zero time.Time on any failure mode.
func parseSharadarDate(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}

	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}
	}

	return t
}

// atoiOrZero returns the integer value of s, or 0 when s is empty or
// not a valid integer.
func atoiOrZero(s string) int {
	if s == "" {
		return 0
	}

	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}

	return n
}
