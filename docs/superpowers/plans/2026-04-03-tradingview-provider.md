# TradingView Index Constituents Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fetch index constituent membership from TradingView's screener API and track membership changes over time.

**Architecture:** New provider in `provider/tradingview/` sub-package that hits an unauthenticated JSON API to get full constituent lists, diffs against DB state to emit IndexChange events, and takes yearly snapshots. Uses exported shared functions from `provider/` package (`DiffSnapshots`, `EmitChangelog`, etc.).

**Tech Stack:** Go, resty (HTTP), zerolog (logging), Ginkgo/Gomega (testing)

**Prerequisite:** The provider sub-package refactoring plan must be completed first (all providers in sub-packages, shared functions exported).

---

### Task 1: Create tradingview_indexes.json

**Files:**
- Create: `provider/tradingview/tradingview_indexes.json`

- [ ] **Step 1: Create the directory and JSON file**

```bash
mkdir -p provider/tradingview
```

Create `provider/tradingview/tradingview_indexes.json`:

```json
[
  {
    "symbol": "SPX",
    "indexName": "S&P 500",
    "symbolID": "SYML:SP;SPX"
  },
  {
    "symbol": "MID",
    "indexName": "S&P MidCap 400",
    "symbolID": "SYML:SP;MID"
  },
  {
    "symbol": "OEX",
    "indexName": "S&P 100",
    "symbolID": "SYML:SP;OEX"
  },
  {
    "symbol": "IXIC",
    "indexName": "Nasdaq Composite",
    "symbolID": "SYML:NASDAQ;IXIC"
  },
  {
    "symbol": "NDX",
    "indexName": "Nasdaq 100",
    "symbolID": "SYML:NASDAQ;NDX"
  },
  {
    "symbol": "RUI",
    "indexName": "Russell 1000",
    "symbolID": "SYML:TVC;RUI"
  },
  {
    "symbol": "RUT",
    "indexName": "Russell 2000",
    "symbolID": "SYML:TVC;RUT"
  }
]
```

- [ ] **Step 2: Commit**

```bash
git add provider/tradingview/tradingview_indexes.json
git commit -m "feat: add TradingView index catalog"
```

---

### Task 2: Write tests for response parsing and ticker normalization

**Files:**
- Create: `provider/tradingview/tradingview_suite_test.go`
- Create: `provider/tradingview/tradingview_test.go`

- [ ] **Step 1: Create test suite file**

Create `provider/tradingview/tradingview_suite_test.go`:

```go
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
package tradingview

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestTradingView(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "TradingView Suite")
}
```

- [ ] **Step 2: Create test file**

Create `provider/tradingview/tradingview_test.go`:

```go
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
package tradingview

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("TradingView", func() {
	Describe("parseTradingViewResponse", func() {
		It("parses symbols and descriptions from JSON response", func() {
			jsonData := []byte(`{
				"totalCount": 3,
				"symbols": ["NYSE:AAPL", "NASDAQ:MSFT", "NYSE:MOG.A"],
				"data": [{
					"id": "TickerUniversal",
					"rawValues": [
						{"name": "AAPL", "description": "Apple Inc.", "exchange": "NYSE"},
						{"name": "MSFT", "description": "Microsoft Corp", "exchange": "NASDAQ"},
						{"name": "MOG.A", "description": "Moog Inc.", "exchange": "NYSE"}
					]
				}]
			}`)

			result, err := parseTradingViewResponse(jsonData)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.TotalCount).To(Equal(3))
			Expect(result.Holdings).To(HaveLen(3))
		})

		It("normalizes share class tickers from dots to slashes", func() {
			jsonData := []byte(`{
				"totalCount": 2,
				"symbols": ["NYSE:MOG.A", "NYSE:BRK.B"],
				"data": [{
					"id": "TickerUniversal",
					"rawValues": [
						{"name": "MOG.A", "description": "Moog Inc.", "exchange": "NYSE"},
						{"name": "BRK.B", "description": "Berkshire Hathaway Inc.", "exchange": "NYSE"}
					]
				}]
			}`)

			result, err := parseTradingViewResponse(jsonData)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Holdings[0].Ticker).To(Equal("MOG/A"))
			Expect(result.Holdings[1].Ticker).To(Equal("BRK/B"))
		})

		It("extracts description as Name", func() {
			jsonData := []byte(`{
				"totalCount": 1,
				"symbols": ["NYSE:AAPL"],
				"data": [{
					"id": "TickerUniversal",
					"rawValues": [
						{"name": "AAPL", "description": "Apple Inc.", "exchange": "NYSE"}
					]
				}]
			}`)

			result, err := parseTradingViewResponse(jsonData)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Holdings[0].Name).To(Equal("Apple Inc."))
		})

		It("returns error on invalid JSON", func() {
			_, err := parseTradingViewResponse([]byte(`not json`))
			Expect(err).To(HaveOccurred())
		})

		It("returns empty holdings when response has no symbols", func() {
			jsonData := []byte(`{"totalCount": 0, "symbols": [], "data": []}`)
			result, err := parseTradingViewResponse(jsonData)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Holdings).To(BeEmpty())
		})

		It("handles response with no TickerUniversal data block", func() {
			jsonData := []byte(`{
				"totalCount": 2,
				"symbols": ["NYSE:AAPL", "NASDAQ:MSFT"],
				"data": [{"id": "MarketCap", "rawValues": [100, 200]}]
			}`)

			result, err := parseTradingViewResponse(jsonData)
			Expect(err).ToNot(HaveOccurred())
			Expect(result.Holdings).To(HaveLen(2))
			Expect(result.Holdings[0].Ticker).To(Equal("AAPL"))
			Expect(result.Holdings[0].Name).To(Equal(""))
		})
	})

	Describe("normalizeTVTicker", func() {
		It("strips exchange prefix", func() {
			Expect(normalizeTVTicker("NYSE:AAPL")).To(Equal("AAPL"))
		})

		It("converts dots to slashes for share classes", func() {
			Expect(normalizeTVTicker("NYSE:MOG.A")).To(Equal("MOG/A"))
			Expect(normalizeTVTicker("NYSE:BRK.B")).To(Equal("BRK/B"))
		})

		It("handles ticker with no exchange prefix", func() {
			Expect(normalizeTVTicker("AAPL")).To(Equal("AAPL"))
		})
	})

	Describe("indexMap", func() {
		It("contains all expected indices", func() {
			Expect(indexMap).To(HaveKey("SPX"))
			Expect(indexMap).To(HaveKey("MID"))
			Expect(indexMap).To(HaveKey("OEX"))
			Expect(indexMap).To(HaveKey("IXIC"))
			Expect(indexMap).To(HaveKey("NDX"))
			Expect(indexMap).To(HaveKey("RUI"))
			Expect(indexMap).To(HaveKey("RUT"))
		})

		It("has correct index name for SPX", func() {
			Expect(indexMap["SPX"].IndexName).To(Equal("S&P 500"))
		})
	})
})
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `ginkgo run -race ./provider/tradingview/`
Expected: FAIL -- `parseTradingViewResponse`, `normalizeTVTicker`, `indexMap` undefined.

- [ ] **Step 4: Commit**

```bash
git add provider/tradingview/
git commit -m "test: add TradingView provider tests (red)"
```

---

### Task 3: Implement TradingView provider

**Files:**
- Create: `provider/tradingview/tradingview.go`

- [ ] **Step 1: Write the provider implementation**

Create `provider/tradingview/tradingview.go`:

```go
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
package tradingview

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/figi"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
)

// TradingView implements the provider.Provider interface.
type TradingView struct{}

type tvIndex struct {
	Symbol    string `json:"symbol"`
	IndexName string `json:"indexName"`
	SymbolID  string `json:"symbolID"`
}

//go:embed tradingview_indexes.json
var tvIndexData []byte

var indexMap map[string]tvIndex

func init() {
	var entries []tvIndex

	if err := json.Unmarshal(tvIndexData, &entries); err != nil {
		panic("failed to parse embedded tradingview_indexes.json: " + err.Error())
	}

	indexMap = make(map[string]tvIndex, len(entries))
	for _, e := range entries {
		indexMap[e.Symbol] = e
	}
}

func (tv *TradingView) Name() string {
	return "TradingView"
}

func (tv *TradingView) ConfigDescription() map[string]string {
	return map[string]string{
		"indexes": "Comma-separated index symbols (e.g. SPX,MID,RUT). Defaults to all supported indices if left empty.",
	}
}

func (tv *TradingView) Description() string {
	return "Track index constituents and membership changes using TradingView index component data."
}

func (tv *TradingView) Datasets() map[string]provider.Dataset {
	return map[string]provider.Dataset{
		"Index Constituents": {
			Name:        "Index Constituents",
			Description: "Download index constituent membership and track changes over time.",
			DataTypes:   []*data.DataType{data.DataTypes[data.IndexKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: downloadConstituents,
		},
	}
}

// -- Response parsing --

type tvHolding struct {
	Ticker string
	Name   string
}

type tvParseResult struct {
	TotalCount int
	Holdings   []tvHolding
}

type tvRawResponse struct {
	TotalCount int           `json:"totalCount"`
	Symbols    []string      `json:"symbols"`
	Data       []tvDataBlock `json:"data"`
}

type tvDataBlock struct {
	ID        string          `json:"id"`
	RawValues json.RawMessage `json:"rawValues"`
}

type tvTickerInfo struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Exchange    string `json:"exchange"`
}

func normalizeTVTicker(symbol string) string {
	if idx := strings.Index(symbol, ":"); idx >= 0 {
		symbol = symbol[idx+1:]
	}

	symbol = strings.ReplaceAll(symbol, ".", "/")

	return symbol
}

func parseTradingViewResponse(jsonData []byte) (*tvParseResult, error) {
	var raw tvRawResponse
	if err := json.Unmarshal(jsonData, &raw); err != nil {
		return nil, fmt.Errorf("failed to parse TradingView response: %w", err)
	}

	nameMap := make(map[string]string)

	for _, block := range raw.Data {
		if block.ID != "TickerUniversal" {
			continue
		}

		var infos []tvTickerInfo
		if err := json.Unmarshal(block.RawValues, &infos); err != nil {
			break
		}

		for _, info := range infos {
			nameMap[info.Name] = info.Description
		}
	}

	result := &tvParseResult{
		TotalCount: raw.TotalCount,
		Holdings:   make([]tvHolding, 0, len(raw.Symbols)),
	}

	for _, sym := range raw.Symbols {
		rawTicker := sym
		if idx := strings.Index(rawTicker, ":"); idx >= 0 {
			rawTicker = rawTicker[idx+1:]
		}

		result.Holdings = append(result.Holdings, tvHolding{
			Ticker: normalizeTVTicker(sym),
			Name:   nameMap[rawTicker],
		})
	}

	return result, nil
}

// -- Fetch logic --

const baseURL = "https://screener-facade.tradingview.com/screener-facade/api/v1/screener-table/scan"

func downloadConstituents(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
	logger := zerolog.Ctx(ctx)

	runSummary := data.RunSummary{
		StartTime:        time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}

	numObs := 0

	defer func() {
		runSummary.EndTime = time.Now()
		runSummary.NumObservations = numObs
		exitNotification <- runSummary
	}()

	// Parse index symbols from config; default to all
	indexStr := subscription.Config["indexes"]

	var symbols []string

	if indexStr == "" {
		for s := range indexMap {
			symbols = append(symbols, s)
		}
	} else {
		symbols = strings.Split(indexStr, ",")
		for i := range symbols {
			symbols[i] = strings.ToUpper(strings.TrimSpace(symbols[i]))
		}
	}

	// Acquire DB connection and build figi + name maps
	conn, err := subscription.Library.Pool.Acquire(ctx)
	if err != nil {
		logger.Error().Err(err).Msg("could not acquire database connection")
		runSummary.Status = data.RunFailed

		return
	}
	defer conn.Release()

	assets, err := data.ActiveAssets(ctx, conn)
	if err != nil {
		logger.Error().Err(err).Msg("could not load active assets")
		runSummary.Status = data.RunFailed

		return
	}

	figiMap := make(map[string]string, len(assets))
	assetNameMap := make(map[string]string, len(assets))

	for _, asset := range assets {
		figiMap[asset.Ticker] = asset.CompositeFigi
		assetNameMap[asset.Ticker] = asset.Name
	}

	// Create HTTP client
	client := resty.New().
		SetRetryCount(3).
		SetRetryWaitTime(10 * time.Second).
		SetRetryMaxWaitTime(60 * time.Second).
		AddRetryCondition(func(r *resty.Response, err error) bool {
			return err != nil || r.StatusCode() == 429 || r.StatusCode() >= 500
		}).
		SetTimeout(60 * time.Second).
		SetHeader("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36").
		SetHeader("Accept", "application/json").
		SetHeader("Content-Type", "text/plain;charset=UTF-8").
		SetHeader("Origin", "https://www.tradingview.com").
		SetHeader("Referer", "https://www.tradingview.com/")

	for i, symbol := range symbols {
		idx, ok := indexMap[symbol]
		if !ok {
			logger.Warn().Str("Symbol", symbol).Msg("unknown TradingView index symbol, skipping")
			continue
		}

		n, err := fetchIndex(ctx, client, idx, figiMap, assetNameMap, subscription, out)
		if err != nil {
			logger.Error().Err(err).Str("Symbol", symbol).Str("IndexName", idx.IndexName).Msg("failed to fetch TradingView index")
			continue
		}

		numObs += n

		// Randomized delay between 30s and 2min, skip after last
		if i < len(symbols)-1 {
			delay := 30*time.Second + time.Duration(rand.IntN(91))*time.Second
			logger.Info().Dur("Delay", delay).Msg("waiting between TradingView requests")

			select {
			case <-time.After(delay):
			case <-ctx.Done():
				runSummary.Status = data.RunFailed
				return
			}
		}
	}

	runSummary.Status = data.RunSuccess
}

func fetchIndex(
	ctx context.Context,
	client *resty.Client,
	idx tvIndex,
	figiMap map[string]string,
	assetNameMap map[string]string,
	subscription *library.Subscription,
	out chan<- *data.Observation,
) (int, error) {
	logger := zerolog.Ctx(ctx)
	numObs := 0
	table := subscription.DataTablesMap[data.IndexKey]

	requestURL := baseURL + "?table_id=symbols.components&version=54&columnset_id=overview&symbol_constituents_id=" + idx.SymbolID
	requestBody := `{"lang":"en","range":[0,5000],"sort":{"sortBy":{"id":"MarketCap","params":{}},"sortOrder":"desc","nullsFirst":false},"scanner_product_label":"symbols-components"}`

	logger.Info().
		Str("IndexName", idx.IndexName).
		Str("Symbol", idx.Symbol).
		Msg("fetching TradingView index components")

	resp, err := client.R().SetContext(ctx).SetBody(requestBody).Post(requestURL)
	if err != nil {
		return 0, fmt.Errorf("HTTP request failed for %s: %w", idx.IndexName, err)
	}

	if resp.StatusCode() != 200 {
		return 0, fmt.Errorf("HTTP %d for %s", resp.StatusCode(), idx.IndexName)
	}

	parseResult, err := parseTradingViewResponse(resp.Body())
	if err != nil {
		return 0, fmt.Errorf("could not parse response for %s: %w", idx.IndexName, err)
	}

	if len(parseResult.Holdings) == 0 {
		logger.Warn().Str("IndexName", idx.IndexName).Msg("no constituents found")
		return 0, nil
	}

	logger.Info().
		Int("NumConstituents", len(parseResult.Holdings)).
		Int("TotalCount", parseResult.TotalCount).
		Str("IndexName", idx.IndexName).
		Msg("parsed TradingView index components")

	// Resolve tickers against internal assets
	var unknownAssets []*data.Asset

	for _, holding := range parseResult.Holdings {
		if figiMap[holding.Ticker] != "" {
			continue
		}

		if provider.ResolveShareClass(holding.Ticker, holding.Name, figiMap, assetNameMap, logger) {
			continue
		}

		unknownAssets = append(unknownAssets, &data.Asset{Ticker: holding.Ticker, Name: holding.Name})
	}

	obsTemplate := &data.Observation{
		ObservationDate:  time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}

	if len(unknownAssets) > 0 {
		unknownTickers := make([]string, len(unknownAssets))
		for j, a := range unknownAssets {
			unknownTickers[j] = a.Ticker
		}

		logger.Info().
			Int("Count", len(unknownAssets)).
			Strs("Tickers", unknownTickers).
			Str("IndexName", idx.IndexName).
			Msg("resolving unknown tickers via OpenFIGI")

		figi.Enrich(unknownAssets...)

		for _, asset := range unknownAssets {
			if asset.CompositeFigi != "" {
				figiMap[asset.Ticker] = asset.CompositeFigi

				out <- &data.Observation{
					AssetObject:      asset,
					ObservationDate:  obsTemplate.ObservationDate,
					SubscriptionID:   obsTemplate.SubscriptionID,
					SubscriptionName: obsTemplate.SubscriptionName,
				}

				numObs++
			}
		}
	}

	// Build current holdings map -- abort on any unresolved ticker
	currentHoldings := make(map[string]provider.IndexMember, len(parseResult.Holdings))

	var unresolved []string

	for _, holding := range parseResult.Holdings {
		f := figiMap[holding.Ticker]
		if f != "" {
			currentHoldings[holding.Ticker] = provider.IndexMember{
				CompositeFigi: f,
				Weight:        0,
			}
		} else {
			unresolved = append(unresolved, holding.Ticker+" ("+holding.Name+")")
		}
	}

	if len(unresolved) > 0 {
		logger.Error().
			Int("Unresolved", len(unresolved)).
			Int("TotalConstituents", len(parseResult.Holdings)).
			Strs("Holdings", unresolved).
			Str("IndexName", idx.IndexName).
			Msg("aborting index update -- constituents have unresolved FIGIs")

		return numObs, fmt.Errorf("%d constituents for %s have no FIGI", len(unresolved), idx.IndexName)
	}

	// Load DB state and diff
	today := time.Now().UTC().Truncate(24 * time.Hour)
	state := provider.CurrentIndexMembers(ctx, subscription.Library.Pool, table, idx.IndexName, today)
	memLastSnapshotDate := provider.LastSnapshotDate(ctx, subscription.Library.Pool, table, idx.IndexName)

	added, removed, _ := provider.DiffSnapshots(currentHoldings, state)

	// Emit changelog
	provider.EmitChangelog(added, removed, idx.IndexName, today, obsTemplate, out)
	numObs += len(added) + len(removed)

	// Emit snapshot if due
	if provider.ShouldTakeSnapshot(memLastSnapshotDate, today, "yearly") {
		constituents := make([]data.IndexConstituent, 0, len(currentHoldings))
		for t, member := range currentHoldings {
			constituents = append(constituents, data.IndexConstituent{
				Ticker:        t,
				CompositeFigi: member.CompositeFigi,
				Weight:        0,
			})
		}

		out <- &data.Observation{
			IndexSnapshot: &data.IndexSnapshot{
				IndexName:    idx.IndexName,
				SnapshotDate: today,
				Constituents: constituents,
			},
			ObservationDate:  obsTemplate.ObservationDate,
			SubscriptionID:   obsTemplate.SubscriptionID,
			SubscriptionName: obsTemplate.SubscriptionName,
		}

		numObs++

		logger.Info().
			Int("NumConstituents", len(constituents)).
			Str("IndexName", idx.IndexName).
			Time("SnapshotDate", today).
			Msg("emitted index snapshot")
	}

	return numObs, nil
}
```

- [ ] **Step 2: Build and test**

Run: `go build ./... && ginkgo run -race ./provider/tradingview/`
Expected: All tests pass.

- [ ] **Step 3: Lint**

Run: `golangci-lint run --fix ./provider/tradingview/...`
Expected: 0 issues.

- [ ] **Step 4: Commit**

```bash
git add provider/tradingview/
git commit -m "feat: add TradingView index constituents provider"
```

---

### Task 4: Register the provider

**Files:**
- Modify: `provider/discover.go`

- [ ] **Step 1: Add TradingView to the provider map**

Add import and Map entry in `provider/discover.go`:

```go
import (
	"github.com/penny-vault/pvdata/provider/tradingview"
)

// In Map:
"tradingview": &tradingview.TradingView{},
```

- [ ] **Step 2: Build and test**

Run: `go build ./... && ginkgo run -race ./provider/...`
Expected: All tests pass.

- [ ] **Step 3: Commit**

```bash
git add provider/discover.go
git commit -m "feat: register TradingView provider"
```
