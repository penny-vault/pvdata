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

	"github.com/go-resty/resty/v2"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
	"github.com/tidwall/gjson"
)

// CIKEntry holds the SEC-published ticker and entity name for a CIK.
type CIKEntry struct {
	Ticker string
	Name   string
}

// AssetInfo holds the resolved ticker and FIGI for a CIK.
type AssetInfo struct {
	Ticker        string
	CompositeFigi string
	CIK           int

	// SiblingFigi is another share class's composite FIGI under the same CIK
	// (e.g. BRK.A for BRK.B). Used by the market-ratio share_factor formula.
	SiblingFigi string
}

// ParseCompanyTickers parses the SEC company_tickers.json format into a CIK->entry map.
func ParseCompanyTickers(jsonData []byte) (map[int]CIKEntry, error) {
	result := make(map[int]CIKEntry)
	root := gjson.ParseBytes(jsonData)

	root.ForEach(func(_, entry gjson.Result) bool {
		cik := int(entry.Get("cik_str").Int())
		if cik == 0 {
			return true
		}

		result[cik] = CIKEntry{
			Ticker: entry.Get("ticker").String(),
			Name:   entry.Get("title").String(),
		}

		return true
	})

	return result, nil
}

// FormatCIK formats a CIK integer as the zero-padded string used in SEC URLs.
func FormatCIK(cik int) string {
	return fmt.Sprintf("CIK%010d", cik)
}

// FetchCompanyTickers fetches and parses SEC's company_tickers.json. SEC
// updates the file daily, so callers should not cache the response.
func FetchCompanyTickers(ctx context.Context, client *resty.Client) (map[int]CIKEntry, error) {
	resp, err := client.R().SetContext(ctx).Get(companyTickersURL)
	if err != nil {
		return nil, fmt.Errorf("fetching SEC company_tickers.json: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("SEC returned status %d for company_tickers.json", resp.StatusCode())
	}

	entries, err := ParseCompanyTickers(resp.Body())
	if err != nil {
		return nil, fmt.Errorf("parsing SEC company_tickers.json: %w", err)
	}

	log.Info().Int("entries", len(entries)).Msg("fetched SEC company_tickers.json")

	return entries, nil
}

// LoadCIKMapFromDB returns three maps from the active assets table: by CIK,
// by ticker, and a sibling FIGI map keyed by FIGI for dual-class filers
// (BRK.A/BRK.B, GOOG/GOOGL). Single-class CIKs with multiple ticker aliases
// share one FIGI and contribute nothing to the sibling map.
func LoadCIKMapFromDB(ctx context.Context, pool *pgxpool.Pool) (map[int]AssetInfo, map[string]AssetInfo, map[string]string, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Release()

	// Only active assets: inactive rows sharing a CIK with a live listing
	// (e.g. MCD's inactive BBG000C9R4J8 alongside active BBG000BNSZP1) can
	// otherwise win the CIK slot via map iteration order and cause SEC
	// observations to be written under a FIGI Sharadar doesn't track.
	rows, err := conn.Query(ctx,
		`SELECT ticker, composite_figi, cik FROM assets WHERE cik IS NOT NULL AND cik != '' AND active = TRUE`)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("querying assets: %w", err)
	}
	defer rows.Close()

	byCIK := make(map[int]AssetInfo)
	byTicker := make(map[string]AssetInfo)
	figiByCIK := make(map[int]map[string]struct{})

	for rows.Next() {
		var ticker, figi, cikStr string
		if err := rows.Scan(&ticker, &figi, &cikStr); err != nil {
			log.Warn().Err(err).Msg("error scanning asset row for CIK map")
			continue
		}

		var cik int
		if _, err := fmt.Sscanf(cikStr, "%d", &cik); err != nil || cik == 0 {
			continue
		}

		info := AssetInfo{
			Ticker:        ticker,
			CompositeFigi: figi,
			CIK:           cik,
		}

		byCIK[cik] = info
		byTicker[ticker] = info

		if figi != "" {
			if figiByCIK[cik] == nil {
				figiByCIK[cik] = make(map[string]struct{})
			}

			figiByCIK[cik][figi] = struct{}{}
		}
	}

	// Derive sibling FIGIs: for each CIK whose assets span more than one
	// distinct FIGI, map each FIGI to the set of other FIGIs under the same
	// CIK. Dual-class filers (BRK.A/BRK.B, GOOG/GOOGL, etc.) produce a 1:1
	// pairing; single-class filers with multiple ticker aliases (JPM/AMJ/...
	// on one FIGI) contribute nothing.
	siblingFigi := make(map[string]string)

	for _, figis := range figiByCIK {
		if len(figis) < 2 {
			continue
		}

		list := make([]string, 0, len(figis))
		for f := range figis {
			list = append(list, f)
		}

		for _, f := range list {
			for _, other := range list {
				if other == f {
					continue
				}

				siblingFigi[f] = other

				break
			}
		}
	}

	return byCIK, byTicker, siblingFigi, rows.Err()
}
