# iShares CSV API + Rate Limiting Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Switch the iShares provider from Playwright XLS download to direct HTTP CSV download with randomized rate limiting.

**Architecture:** Replace Playwright browser automation with resty HTTP client calling the iShares CSV endpoint. Replace XML parser with CSV parser. Add 5-45 second randomized delay between requests.

**Tech Stack:** `github.com/go-resty/resty/v2` (existing dependency), `encoding/csv` (stdlib), `math/rand/v2` (stdlib)

**Spec:** `docs/superpowers/specs/2026-03-29-ishares-catalog-expansion-design.md` (Phase 2 section)

---

### Task 1: Replace XML parser with CSV parser

**Files:**
- Modify: `provider/ishares_parser.go` (full rewrite)
- Modify: `provider/ishares_parser_test.go` (update sample data)

- [ ] **Step 1: Write the CSV parser test with CSV sample data**

Replace the contents of `provider/ishares_parser_test.go` with:

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
package provider

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("parseISharesCSV", func() {
	sampleCSV := []byte("\xef\xbb\xbf" + `iShares Russell 1000 Value ETF
Fund Holdings as of,"Mar 05, 2026"
Inception Date,"May 22, 2000"
Shares Outstanding,"188,400,000.00"
Stock,"-"
Bond,"-"
Cash,"-"
Other,"-"

Ticker,Name,Sector,Asset Class,Market Value,Weight (%),Notional Value,Quantity,Price,Location,Exchange,Currency,FX Rate,Market Currency,Accrual Date
"AAPL","APPLE INC","Information Technology","Equity","500,000,000.00","5.25","500,000,000.00","2,000,000.00","250.00","United States","NASDAQ","USD","1.00","USD","-"
"CASH","CASH COLLATERAL","-","Cash and/or Derivatives","100,000.00","0.01","100,000.00","100,000.00","1.00","United States","-","USD","1.00","USD","-"
"MSFT","MICROSOFT CORP","Information Technology","Equity","400,000,000.00","4.20","400,000,000.00","1,000,000.00","400.00","United States","NASDAQ","USD","1.00","USD","-"
`)

	It("parses holdings from CSV", func() {
		result, err := parseISharesCSV(sampleCSV)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Holdings).To(HaveLen(2))
	})

	It("extracts the snapshot date", func() {
		result, err := parseISharesCSV(sampleCSV)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.SnapshotDate.Year()).To(Equal(2026))
		Expect(result.SnapshotDate.Month()).To(Equal(time.March))
		Expect(result.SnapshotDate.Day()).To(Equal(5))
	})

	It("extracts ticker and weight", func() {
		result, err := parseISharesCSV(sampleCSV)
		Expect(err).ToNot(HaveOccurred())
		var aapl *iSharesHolding
		for _, h := range result.Holdings {
			if h.Ticker == "AAPL" {
				aapl = &h
				break
			}
		}
		Expect(aapl).ToNot(BeNil())
		Expect(aapl.Weight).To(BeNumerically("~", 0.0525, 0.0001))
	})

	It("filters out non-equity holdings", func() {
		result, err := parseISharesCSV(sampleCSV)
		Expect(err).ToNot(HaveOccurred())
		for _, h := range result.Holdings {
			Expect(h.Ticker).ToNot(Equal("CASH"))
		}
	})
})
```

- [ ] **Step 2: Run the test to confirm it fails**

```bash
ginkgo run -race ./provider/ --focus "parseISharesCSV"
```

Expected: FAIL -- `parseISharesCSV` does not exist yet.

- [ ] **Step 3: Implement the CSV parser**

Replace the contents of `provider/ishares_parser.go` with:

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
	Ticker string
	Weight float64
}

type iSharesParseResult struct {
	SnapshotDate time.Time
	Holdings     []iSharesHolding
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
	assetClassCol := colIdx["Asset Class"]
	weightCol := colIdx["Weight (%)"]

	// Parse data rows
	for _, record := range records[headerIdx+1:] {
		if len(record) <= weightCol {
			continue
		}

		assetClass := strings.TrimSpace(record[assetClassCol])
		if assetClass != "Equity" {
			continue
		}

		ticker := strings.TrimSpace(record[tickerCol])
		if ticker == "" {
			continue
		}

		weightStr := strings.ReplaceAll(record[weightCol], ",", "")
		weightPct, err := strconv.ParseFloat(strings.TrimSpace(weightStr), 64)
		if err != nil {
			continue
		}

		result.Holdings = append(result.Holdings, iSharesHolding{
			Ticker: ticker,
			Weight: weightPct / 100.0,
		})
	}

	return result, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
ginkgo run -race ./provider/ --focus "parseISharesCSV"
```

Expected: All 4 tests pass.

- [ ] **Step 5: Run full provider test suite**

```bash
ginkgo run -race ./provider/
```

Expected: All tests pass. Note: the `parseISharesXML` function no longer exists, but nothing else calls it -- only `downloadSingleISharesETF` does, and that will be updated in Task 2.

If the build fails because `ishares.go` still references `parseISharesXML`, that's expected and will be fixed in Task 2. In that case, temporarily verify just the parser tests pass.

- [ ] **Step 6: Commit**

```bash
git add provider/ishares_parser.go provider/ishares_parser_test.go
git commit -m "$(cat <<'EOF'
refactor: replace iShares XML parser with CSV parser

Switch from parsing SpreadsheetML XML to parsing the CSV format
returned by the iShares holdings API endpoint.
EOF
)"
```

---

### Task 2: Switch download logic from Playwright to resty with rate limiting

**Files:**
- Modify: `provider/ishares.go` (download functions + imports)

- [ ] **Step 1: Replace the download logic in ishares.go**

The imports need to change -- remove Playwright, add resty and math/rand:

```go
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
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog"
)
```

Note: `"os"`, `"github.com/penny-vault/pvdata/playwright_helpers"`, `"github.com/playwright-community/playwright-go"`, and `"github.com/spf13/viper"` are removed.

Replace `downloadISharesHoldings` (lines 92-177). The key changes:
- Remove Playwright startup/teardown
- Create a resty client with retry config
- Add randomized delay (5-45s) between requests in the ticker loop

```go
const iSharesHoldingsURLTemplate = "https://www.ishares.com/us/products/%s/%s/1467271812596.ajax?fileType=csv&fileName=%s_holdings&dataType=fund"

func downloadISharesHoldings(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
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

	// Parse tickers from config
	tickerStr := subscription.Config["tickers"]
	if tickerStr == "" {
		logger.Error().Msg("no tickers configured for iShares provider")

		runSummary.Status = data.RunFailed

		return
	}

	tickers := strings.Split(tickerStr, ",")
	for i := range tickers {
		tickers[i] = strings.TrimSpace(tickers[i])
	}

	snapshotFrequency := subscription.Config["snapshotFrequency"]
	if snapshotFrequency == "" {
		snapshotFrequency = "weekly"
	}

	// Acquire DB connection and build figi map
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
	for _, asset := range assets {
		figiMap[asset.Ticker] = asset.CompositeFigi
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
		SetHeader("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36").
		SetHeader("Accept", "text/csv,text/plain,*/*")

	// Process each ticker
	for i, ticker := range tickers {
		etf, ok := iSharesETFMap[ticker]
		if !ok {
			logger.Warn().Str("Ticker", ticker).Msg("unknown iShares ETF ticker, skipping")
			continue
		}

		n, err := downloadSingleISharesETF(ctx, client, ticker, etf, figiMap, snapshotFrequency, subscription, out)
		if err != nil {
			logger.Error().Err(err).Str("Ticker", ticker).Msg("failed to download iShares ETF holdings")
			continue
		}

		numObs += n

		// Randomized delay between requests (5-45 seconds), skip after last ticker
		if i < len(tickers)-1 {
			delay := 5*time.Second + time.Duration(rand.IntN(40))*time.Second
			logger.Info().Dur("Delay", delay).Msg("waiting between iShares requests")
			time.Sleep(delay)
		}
	}

	runSummary.Status = data.RunSuccess
}
```

Replace `downloadSingleISharesETF` (lines 179-303):

```go
func downloadSingleISharesETF(
	ctx context.Context,
	client *resty.Client,
	ticker string,
	etf iSharesETF,
	figiMap map[string]string,
	snapshotFrequency string,
	subscription *library.Subscription,
	out chan<- *data.Observation,
) (int, error) {
	logger := zerolog.Ctx(ctx)
	numObs := 0

	// Download CSV holdings file
	csvURL := fmt.Sprintf(iSharesHoldingsURLTemplate, etf.ProductID, etf.Slug, ticker)
	logger.Info().Str("URL", csvURL).Str("IndexName", etf.IndexName).Msg("downloading iShares holdings CSV")

	resp, err := client.R().SetContext(ctx).Get(csvURL)
	if err != nil {
		return 0, fmt.Errorf("HTTP request failed for %s: %w", etf.IndexName, err)
	}

	if resp.StatusCode() != 200 {
		return 0, fmt.Errorf("HTTP %d for %s", resp.StatusCode(), etf.IndexName)
	}

	csvData := resp.Body()
	logger.Info().Int("Bytes", len(csvData)).Str("IndexName", etf.IndexName).Msg("downloaded iShares holdings CSV")

	// Parse the CSV data
	parseResult, err := parseISharesCSV(csvData)
	if err != nil {
		return 0, fmt.Errorf("could not parse iShares CSV for %s: %w", etf.IndexName, err)
	}

	if len(parseResult.Holdings) == 0 {
		logger.Warn().Str("IndexName", etf.IndexName).Msg("no holdings found in downloaded file")
		return 0, nil
	}

	logger.Info().
		Int("NumHoldings", len(parseResult.Holdings)).
		Time("SnapshotDate", parseResult.SnapshotDate).
		Str("IndexName", etf.IndexName).
		Msg("parsed iShares holdings")

	// Build current holdings map (ticker -> figi)
	currentHoldings := make(map[string]string, len(parseResult.Holdings))
	for _, holding := range parseResult.Holdings {
		if figi, ok := figiMap[holding.Ticker]; ok {
			currentHoldings[holding.Ticker] = figi
		}
	}

	// Get previous snapshot and emit changelog
	table := subscription.DataTablesMap[data.IndexKey]
	previous := previousSnapshotTickers(ctx, subscription.Library.Pool, table, etf.IndexName)
	added, removed := diffSnapshots(currentHoldings, previous)

	eventDate := parseResult.SnapshotDate
	if eventDate.IsZero() {
		eventDate = time.Now().UTC().Truncate(24 * time.Hour)
	}

	emitChangelog(added, removed, etf.IndexName, eventDate, &data.Observation{
		ObservationDate:  time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}, out)
	numObs += len(added) + len(removed)

	// Check if a snapshot should be taken
	lastDate := lastSnapshotDate(ctx, subscription.Library.Pool, table, etf.IndexName)
	if shouldTakeSnapshot(lastDate, snapshotFrequency) {
		// Build a weight map for quick lookup
		weightMap := make(map[string]float64, len(parseResult.Holdings))
		for _, holding := range parseResult.Holdings {
			weightMap[holding.Ticker] = holding.Weight
		}

		snapshotDate := parseResult.SnapshotDate
		if snapshotDate.IsZero() {
			snapshotDate = time.Now().UTC().Truncate(24 * time.Hour)
		}

		for t, figi := range currentHoldings {
			out <- &data.Observation{
				IndexSnapshot: &data.IndexSnapshot{
					Ticker:        t,
					CompositeFigi: figi,
					IndexName:     etf.IndexName,
					SnapshotDate:  snapshotDate,
					Weight:        weightMap[t],
				},
				ObservationDate:  time.Now(),
				SubscriptionID:   subscription.ID,
				SubscriptionName: subscription.Name,
			}

			numObs++
		}

		logger.Info().
			Int("NumSnapshots", len(currentHoldings)).
			Str("IndexName", etf.IndexName).
			Time("SnapshotDate", snapshotDate).
			Msg("emitted index snapshots")
	}

	return numObs, nil
}
```

- [ ] **Step 2: Run the full provider tests**

```bash
ginkgo run -race ./provider/
```

Expected: All tests pass. The embed test, parser test, and helper tests should all work.

- [ ] **Step 3: Run the linter**

```bash
make lint
```

Expected: No new lint errors.

- [ ] **Step 4: Build the full binary**

```bash
make build
```

Expected: Build succeeds.

- [ ] **Step 5: Commit**

```bash
git add provider/ishares.go
git commit -m "$(cat <<'EOF'
refactor: switch iShares provider from Playwright to CSV API

Replace Playwright browser automation with direct HTTP requests to the
iShares CSV holdings endpoint using resty. Add randomized 5-45 second
delay between requests to avoid hammering the server.
EOF
)"
```

---

### Task 3: Update integration test

**Files:**
- Modify: `provider/integration_test.go`

- [ ] **Step 1: Replace the Playwright-based integration test with a resty-based one**

Replace the `TestISharesDownloadAndParse` function in `provider/integration_test.go` with:

```go
func TestISharesDownloadAndParse(t *testing.T) {
	etf := iSharesETFMap["IWD"] // Russell 1000 Value -- a known ETF
	ticker := "IWD"

	client := resty.New().
		SetTimeout(60 * time.Second).
		SetHeader("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36").
		SetHeader("Accept", "text/csv,text/plain,*/*")

	csvURL := fmt.Sprintf(iSharesHoldingsURLTemplate, etf.ProductID, etf.Slug, ticker)
	t.Logf("Fetching %s", csvURL)

	resp, err := client.R().Get(csvURL)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}

	if resp.StatusCode() != 200 {
		t.Fatalf("HTTP %d: %s", resp.StatusCode(), string(resp.Body()[:min(500, len(resp.Body()))]))
	}

	t.Logf("Downloaded %d bytes", len(resp.Body()))

	// Parse the CSV
	result, err := parseISharesCSV(resp.Body())
	if err != nil {
		t.Fatalf("parsing failed: %v", err)
	}

	t.Logf("Snapshot date: %s", result.SnapshotDate.Format("2006-01-02"))
	t.Logf("Number of holdings: %d", len(result.Holdings))

	if len(result.Holdings) < 100 {
		t.Errorf("Expected at least 100 holdings for Russell 1000 Value, got %d", len(result.Holdings))
	}

	// Print first 5 holdings
	for i, h := range result.Holdings {
		if i >= 5 {
			break
		}
		t.Logf("  %s: weight=%.4f (%.2f%%)", h.Ticker, h.Weight, h.Weight*100)
	}
}
```

The imports for this file need to change too. Replace the import block:

```go
import (
	"fmt"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"
)
```

The `TestNasdaqScrape` function stays unchanged (it still uses Playwright). The `os` and `strings` imports are only needed if `TestNasdaqScrape` uses them -- check and adjust accordingly.

Actually, looking at the existing code, `TestNasdaqScrape` uses `strings` and imports from `playwright_helpers` and `playwright-go`. So the imports need to include everything for both test functions:

```go
import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/penny-vault/pvdata/playwright_helpers"
	"github.com/playwright-community/playwright-go"
)
```

Note: `os` is no longer needed (was only used for the XLS file read).

- [ ] **Step 2: Build to verify compilation**

```bash
go build ./provider/
```

Expected: Compiles successfully.

- [ ] **Step 3: Commit**

```bash
git add provider/integration_test.go
git commit -m "test: update iShares integration test to use CSV API"
```
