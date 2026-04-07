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

// LoadCIKMapFromDB loads a CIK -> AssetInfo map from the assets in the database.
// This provides the primary lookup path for resolving CIKs to tickers and FIGIs.
// The pool parameter is *pgxpool.Pool (Library.Pool is a public field, not a method).
func LoadCIKMapFromDB(ctx context.Context, pool *pgxpool.Pool) (map[int]AssetInfo, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquiring connection: %w", err)
	}
	defer conn.Release()

	// Note: The actual table and column names must match your schema.
	// Check the assets table DDL in data/datatype.go for exact column names.
	rows, err := conn.Query(ctx,
		`SELECT ticker, composite_figi, cik FROM assets WHERE cik IS NOT NULL AND cik != ''`)
	if err != nil {
		return nil, fmt.Errorf("querying assets: %w", err)
	}
	defer rows.Close()

	result := make(map[int]AssetInfo)

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

		result[cik] = AssetInfo{
			Ticker:        ticker,
			CompositeFigi: figi,
			CIK:           cik,
		}
	}

	return result, rows.Err()
}
