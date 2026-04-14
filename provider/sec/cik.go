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
}

// ParseCompanyTickers parses the SEC company_tickers.json format into a CIK->entry map.
// The JSON is an object with numeric string keys and objects like:
// {"cik_str": 320193, "ticker": "AAPL", "title": "Apple Inc."}
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

// FetchCompanyTickers fetches the SEC's company_tickers.json file and parses
// it into a CIK -> CIKEntry map. The file is ~10MB and is published by SEC at
// companyTickersURL; SEC updates it daily so callers should not cache the
// response between runs.
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

// LoadCIKMapFromDB loads asset information from the database, returning two
// maps: a CIK-keyed map (one representative entry per CIK) and a ticker-keyed
// map (every asset with a CIK). Multiple tickers can share a single CIK (e.g.
// JPM, AMJ, AMJB, VYLD all belong to CIK 19617). The CIK map picks one entry
// arbitrarily; callers that need to look up a specific ticker should consult
// the ticker map.
func LoadCIKMapFromDB(ctx context.Context, pool *pgxpool.Pool) (map[int]AssetInfo, map[string]AssetInfo, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx,
		`SELECT ticker, composite_figi, cik FROM assets WHERE cik IS NOT NULL AND cik != ''`)
	if err != nil {
		return nil, nil, fmt.Errorf("querying assets: %w", err)
	}
	defer rows.Close()

	byCIK := make(map[int]AssetInfo)
	byTicker := make(map[string]AssetInfo)

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
	}

	return byCIK, byTicker, rows.Err()
}
