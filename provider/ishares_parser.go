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
package provider

import (
	"bytes"
	"encoding/csv"
	"io"
	"strconv"
	"strings"
	"time"
)

type iSharesHolding struct {
	Ticker   string
	Name     string
	Exchange string
	Weight   float64
}

type iSharesParseResult struct {
	SnapshotDate time.Time
	Holdings     []iSharesHolding
}

// validTicker returns true if the ticker looks like a real equity ticker.
// US equity tickers (NASDAQ/NYSE) are letters only. iShares CSVs sometimes
// contain placeholders ("-"), internal IDs ("1697089D", "P5N994"), or
// rights/warrants with spaces ("MPTI RT") that should be skipped.
func validTicker(ticker string) bool {
	if ticker == "" || ticker == "-" {
		return false
	}

	for _, c := range ticker {
		if (c < 'A' || c > 'Z') && (c < 'a' || c > 'z') {
			return false
		}
	}

	return true
}

func parseISharesCSV(csvData []byte) (*iSharesParseResult, error) {
	// Strip BOM
	csvData = bytes.TrimPrefix(csvData, []byte("\xef\xbb\xbf"))

	result := &iSharesParseResult{}
	reader := csv.NewReader(bytes.NewReader(csvData))
	reader.LazyQuotes = true
	reader.FieldsPerRecord = -1 // variable field count in metadata rows

	// Read all records
	var records [][]string

	for {
		record, err := reader.Read()
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, err
		}

		records = append(records, record)
	}

	// Extract snapshot date from "Fund Holdings as of" row
	for _, record := range records {
		if len(record) >= 2 && strings.TrimSpace(record[0]) == "Fund Holdings as of" {
			dateStr := strings.TrimSpace(record[1])
			if t, err := time.Parse("Jan 02, 2006", dateStr); err == nil {
				result.SnapshotDate = t
			}

			break
		}
	}

	// Find the header row (starts with "Ticker")
	headerIdx := -1

	for i, record := range records {
		if len(record) > 0 && strings.TrimSpace(record[0]) == "Ticker" {
			headerIdx = i
			break
		}
	}

	if headerIdx < 0 {
		return result, nil
	}

	// Build column index map
	colIdx := make(map[string]int)
	for i, col := range records[headerIdx] {
		colIdx[strings.TrimSpace(col)] = i
	}

	tickerCol := colIdx["Ticker"]
	nameCol := colIdx["Name"]
	assetClassCol := colIdx["Asset Class"]
	exchangeCol := colIdx["Exchange"]

	// Some iShares CSVs use "Weight (%)" while others use "Market Weight"
	weightCol := -1

	for _, candidate := range []string{"Weight (%)", "Market Weight"} {
		if idx, ok := colIdx[candidate]; ok {
			weightCol = idx
			break
		}
	}

	if weightCol < 0 {
		return result, nil
	}

	minCols := max(tickerCol, nameCol, assetClassCol, weightCol, exchangeCol) + 1

	// Parse data rows
	for _, record := range records[headerIdx+1:] {
		if len(record) < minCols {
			continue
		}

		assetClass := strings.TrimSpace(record[assetClassCol])
		if assetClass != "Equity" {
			continue
		}

		ticker := strings.TrimSpace(record[tickerCol])
		if !validTicker(ticker) {
			continue
		}

		name := strings.TrimSpace(record[nameCol])
		exchange := strings.TrimSpace(record[exchangeCol])

		// Skip non-constituent holdings: contingent value rights and unlisted
		// securities are ETF portfolio artifacts, not index members.
		if strings.HasSuffix(strings.ToUpper(name), " CVR") || strings.HasSuffix(strings.ToUpper(name), "- CVR") {
			continue
		}

		if strings.Contains(strings.ToUpper(exchange), "NO MARKET") || strings.Contains(strings.ToUpper(exchange), "UNLISTED") {
			continue
		}

		weightStr := strings.ReplaceAll(record[weightCol], ",", "")

		weightPct, err := strconv.ParseFloat(strings.TrimSpace(weightStr), 64)
		if err != nil {
			continue
		}

		result.Holdings = append(result.Holdings, iSharesHolding{
			Ticker:   ticker,
			Name:     name,
			Exchange: exchange,
			Weight:   weightPct / 100.0,
		})
	}

	return result, nil
}
