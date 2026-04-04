# FIGI Resolution Fix Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix silent data loss in index imports by resolving FIGIs for all tickers (including delisted), generating synthetic FIGIs for unresolvable tickers, and erroring instead of silently dropping changelog entries.

**Architecture:** Improve the existing `data` and `figi` packages with `AllAssets`, unlisted equity lookups, and synthetic FIGI generation. Update the SP500 import to use the improved pipeline. Make `IndexChange.SaveDB` error on empty FIGI.

**Tech Stack:** Go, OpenFIGI API, Ginkgo/Gomega testing

---

### Task 1: Add AllAssets function

**Files:**
- Modify: `data/asset.go`

- [ ] **Step 1: Add AllAssets function**

Add this function directly below the existing `ActiveAssets` function (around line 133):

```go
// AllAssets returns all assets (active and delisted) from the specified table.
func AllAssets(ctx context.Context, dbConn *pgxpool.Conn, tables ...string) ([]*Asset, error) {
	var assetTable string
	if len(tables) == 0 {
		assetTable = "assets"
	} else {
		assetTable = tables[0]
	}

	sql := fmt.Sprintf(`SELECT
		ticker,
		composite_figi,
		share_class_figi,
		primary_exchange,
		asset_type,
		active,
		name,
		description,
		corporate_url,
		sector,
		industry,
		sic_code,
		cik,
		cusips,
		isins,
		other_identifiers,
		similar_tickers,
		tags,
		coalesce(to_char(listed, 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '') as listed,
		coalesce(to_char(delisted, 'YYYY-MM-DD"T"HH24:MI:SS.US"Z"'), '') as delisted,
		last_updated
	FROM %s`, assetTable)

	rows, err := dbConn.Query(ctx, sql)
	if err != nil {
		return nil, fmt.Errorf("query all assets from %s: %w", assetTable, err)
	}

	var assets []*Asset

	err = pgxscan.ScanAll(&assets, rows)
	if err != nil {
		return nil, fmt.Errorf("scan all assets: %w", err)
	}

	return assets, nil
}
```

- [ ] **Step 2: Verify build**

Run: `go build ./data/...`
Expected: Clean build.

- [ ] **Step 3: Commit**

```bash
git add data/asset.go
git commit -m "feat: add AllAssets function to query active and delisted assets"
```

---

### Task 2: Add includeUnlistedEquities support to OpenFIGI

**Files:**
- Modify: `figi/openfigi.go`

- [ ] **Step 1: Add field to OpenFigiQuery**

Add `IncludeUnlistedEquities` to the `OpenFigiQuery` struct:

```go
type OpenFigiQuery struct {
	IdType                  string `json:"idType"`
	IdValue                 string `json:"idValue"`
	ExchangeCode            string `json:"exchCode"`
	MarketSectorDescription string `json:"marketSecDes"`
	IncludeUnlistedEquities *bool  `json:"includeUnlistedEquities,omitempty"`
}
```

Use `*bool` with `omitempty` so it's omitted from the JSON when not set (preserving current behavior for existing callers).

- [ ] **Step 2: Add LookupFigiUnlisted function**

Add below the existing `LookupFigi` function:

```go
// LookupFigiUnlisted works like LookupFigi but includes delisted/unlisted equities in the search.
func LookupFigiUnlisted(assets []*data.Asset, rateLimiter *rate.Limiter) map[string]*OpenFigiAsset {
	maxBatch := batchSize()

	if viper.GetString("openfigi.apikey") == "" {
		log.Warn().Msg("no OpenFIGI API key configured -- using reduced batch size (10 per request); set openfigi.apikey in your config or re-run `pvdata init`")
	}

	includeUnlisted := true

	query := make([]*OpenFigiQuery, 0, maxBatch)
	result := make(map[string]*OpenFigiAsset)

	for _, asset := range assets {
		query = append(query, &OpenFigiQuery{
			IdType:                  "TICKER",
			IdValue:                 asset.Ticker,
			ExchangeCode:            "US",
			MarketSectorDescription: "Equity",
			IncludeUnlistedEquities: &includeUnlisted,
		})

		if len(query) == maxBatch {
			if err := rateLimiter.Wait(context.Background()); err != nil {
				log.Panic().Err(err).Msg("rate limiter failed")
			}

			mappingResponse, _ := mapFigis(query)
			for _, resp := range mappingResponse {
				for _, figiAsset := range resp.Data {
					result[figiAsset.Ticker] = figiAsset
				}
			}

			query = make([]*OpenFigiQuery, 0, maxBatch)
		}
	}

	if len(query) > 0 {
		if err := rateLimiter.Wait(context.Background()); err != nil {
			log.Panic().Err(err).Msg("rate limiter failed")
		}

		mappingResponse, _ := mapFigis(query)
		for _, resp := range mappingResponse {
			for _, figiAsset := range resp.Data {
				result[figiAsset.Ticker] = figiAsset
			}
		}
	}

	return result
}
```

- [ ] **Step 3: Verify build**

Run: `go build ./figi/...`
Expected: Clean build.

- [ ] **Step 4: Commit**

```bash
git add figi/openfigi.go
git commit -m "feat: add LookupFigiUnlisted for delisted equity FIGI resolution"
```

---

### Task 3: Synthetic FIGI generation

**Files:**
- Create: `figi/synthetic.go`
- Create: `figi/synthetic_test.go`
- Create: `figi/figi_suite_test.go`

- [ ] **Step 1: Create the test suite file**

Create `figi/figi_suite_test.go`:

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
package figi

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestFigi(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Figi Suite")
}
```

- [ ] **Step 2: Write the failing tests**

Create `figi/synthetic_test.go`:

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
package figi

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("GenerateSyntheticFIGI", func() {
	It("returns a 12-character string", func() {
		result := GenerateSyntheticFIGI("AD1", "AMSTED INDUSTRIES INC")
		Expect(result).To(HaveLen(12))
	})

	It("starts with PVG prefix", func() {
		result := GenerateSyntheticFIGI("AD1", "AMSTED INDUSTRIES INC")
		Expect(result[:3]).To(Equal("PVG"))
	})

	It("uses only valid FIGI characters in positions 4-11", func() {
		result := GenerateSyntheticFIGI("AD1", "AMSTED INDUSTRIES INC")
		validChars := "0123456789BCDFGHJKLMNPQRSTVWXYZ"
		for i := 3; i < 11; i++ {
			Expect(validChars).To(ContainSubstring(string(result[i])),
				"character at position %d is '%c' which is not a valid FIGI character", i+1, result[i])
		}
	})

	It("has a numeric check digit at position 12", func() {
		result := GenerateSyntheticFIGI("AD1", "AMSTED INDUSTRIES INC")
		lastChar := result[11]
		Expect(lastChar >= '0' && lastChar <= '9').To(BeTrue(),
			"last character '%c' is not a digit", lastChar)
	})

	It("has a valid check digit per the modified Luhn algorithm", func() {
		result := GenerateSyntheticFIGI("AD1", "AMSTED INDUSTRIES INC")
		Expect(ValidateFIGICheckDigit(result)).To(BeTrue())
	})

	It("is deterministic -- same inputs produce same output", func() {
		a := GenerateSyntheticFIGI("CMB", "CHASE MANHATTAN CORP")
		b := GenerateSyntheticFIGI("CMB", "CHASE MANHATTAN CORP")
		Expect(a).To(Equal(b))
	})

	It("produces different FIGIs for different inputs", func() {
		a := GenerateSyntheticFIGI("AD1", "AMSTED INDUSTRIES INC")
		b := GenerateSyntheticFIGI("CMB", "CHASE MANHATTAN CORP")
		Expect(a).NotTo(Equal(b))
	})
})
```

- [ ] **Step 3: Run tests to verify they fail**

Run: `ginkgo run -race ./figi/`
Expected: FAIL -- `GenerateSyntheticFIGI` and `ValidateFIGICheckDigit` undefined.

- [ ] **Step 4: Implement synthetic FIGI generation**

Create `figi/synthetic.go`:

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
package figi

import (
	"crypto/sha256"
	"encoding/binary"
)

// figiAlphabet contains the valid characters for FIGI positions 4-11:
// digits 0-9 plus consonants (no vowels A, E, I, O, U).
const figiAlphabet = "0123456789BCDFGHJKLMNPQRSTVWXYZ"

// luhnAlphabet maps characters to their numeric values for the check digit calculation.
// Uses the full A-Z alphabet (0-9 = 0-9, A-Z = 10-35).
const luhnAlphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"

// GenerateSyntheticFIGI generates a deterministic FIGI-format identifier
// for a ticker that cannot be resolved via OpenFIGI.
//
// Format: PVG + 8 hash-derived characters + check digit = 12 characters.
// PV is the issuer prefix, G is required by the FIGI standard at position 3.
func GenerateSyntheticFIGI(ticker, name string) string {
	hash := sha256.Sum256([]byte(ticker + "|" + name))
	hashInt := binary.BigEndian.Uint64(hash[:8])

	body := make([]byte, 8)
	for i := range 8 {
		body[i] = figiAlphabet[hashInt%uint64(len(figiAlphabet))]
		hashInt /= uint64(len(figiAlphabet))
	}

	first11 := "PVG" + string(body)
	checkDigit := computeFIGICheckDigit(first11)

	return first11 + string('0'+checkDigit)
}

// computeFIGICheckDigit computes the FIGI check digit using the modified Luhn algorithm.
// Input is the first 11 characters of the FIGI.
func computeFIGICheckDigit(first11 string) byte {
	sum := 0

	for i, ch := range first11 {
		val := -1
		for j, c := range luhnAlphabet {
			if c == ch {
				val = j
				break
			}
		}

		if val < 0 {
			continue
		}

		if i%2 == 1 {
			val *= 2
		}

		// Sum individual digits of the (possibly doubled) value
		digits := digitString(val)
		for _, d := range digits {
			sum += int(d - '0')
		}
	}

	return byte((10 - (sum % 10)) % 10)
}

// digitString converts an integer to its decimal digit string.
func digitString(n int) string {
	if n < 10 {
		return string(rune('0' + n))
	}

	result := ""
	for n > 0 {
		result = string(rune('0'+n%10)) + result
		n /= 10
	}

	return result
}

// ValidateFIGICheckDigit validates the check digit of a 12-character FIGI.
func ValidateFIGICheckDigit(figi string) bool {
	if len(figi) != 12 {
		return false
	}

	expected := computeFIGICheckDigit(figi[:11])

	return figi[11] == '0'+expected
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `ginkgo run -race ./figi/`
Expected: All 7 tests pass.

- [ ] **Step 6: Commit**

```bash
git add figi/synthetic.go figi/synthetic_test.go figi/figi_suite_test.go
git commit -m "feat: add synthetic FIGI generation with PV issuer prefix"
```

---

### Task 4: Error on empty FIGI in IndexChange.SaveDB

**Files:**
- Modify: `data/index.go:90-93`

- [ ] **Step 1: Replace the silent return with an error**

In `data/index.go`, replace:

```go
func (idx *IndexChange) SaveDB(ctx context.Context, tbl string, dbConn *pgxpool.Conn) error {
	if idx.CompositeFigi == "" {
		return nil
	}
```

with:

```go
func (idx *IndexChange) SaveDB(ctx context.Context, tbl string, dbConn *pgxpool.Conn) error {
	if idx.CompositeFigi == "" {
		return fmt.Errorf("index change for ticker %s has empty composite FIGI", idx.Ticker)
	}
```

- [ ] **Step 2: Verify build**

Run: `go build ./data/...`
Expected: Clean build.

- [ ] **Step 3: Commit**

```bash
git add data/index.go
git commit -m "fix: error instead of silently dropping index changes with empty FIGI"
```

---

### Task 5: Update importSP500Rows FIGI resolution pipeline

**Files:**
- Modify: `provider/sharadar/sharadar_import.go`

- [ ] **Step 1: Update the import to add figi package**

Add `"github.com/penny-vault/pvdata/figi"` to the imports if not already present.

- [ ] **Step 2: Replace the FIGI resolution block**

Replace the current asset loading and figiMap construction (lines 717-731):

```go
	conn, err := sub.Library.Pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("could not acquire database connection: %w", err)
	}
	defer conn.Release()

	assets, err := data.ActiveAssets(ctx, conn)
	if err != nil {
		return 0, fmt.Errorf("could not load active assets: %w", err)
	}

	figiMap := make(map[string]string, len(assets))
	for _, asset := range assets {
		figiMap[asset.Ticker] = asset.CompositeFigi
	}
```

with:

```go
	conn, err := sub.Library.Pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("could not acquire database connection: %w", err)
	}
	defer conn.Release()

	// Step 1: Load all assets (active + delisted) for FIGI map
	assets, err := data.AllAssets(ctx, conn)
	if err != nil {
		return 0, fmt.Errorf("could not load assets: %w", err)
	}

	figiMap := make(map[string]string, len(assets))
	for _, asset := range assets {
		if asset.CompositeFigi != "" {
			figiMap[asset.Ticker] = asset.CompositeFigi
		}
	}

	// Step 2: Collect all unique tickers from the data
	uniqueTickers := make(map[string]string) // ticker -> name
	for _, row := range rows {
		ticker := strings.TrimSpace(row["ticker"])
		name := strings.TrimSpace(row["name"])
		if ticker != "" {
			uniqueTickers[ticker] = name
		}
	}

	// Step 3: Find tickers missing from FIGI map and resolve via OpenFIGI (including delisted)
	var unresolvedAssets []*data.Asset
	for ticker := range uniqueTickers {
		if figiMap[ticker] == "" {
			unresolvedAssets = append(unresolvedAssets, &data.Asset{Ticker: ticker})
		}
	}

	if len(unresolvedAssets) > 0 {
		logger.Info().Int("count", len(unresolvedAssets)).Msg("resolving unmatched tickers via OpenFIGI (including delisted)")

		rateLimiter := figi.RateLimit()
		figiResults := figi.LookupFigiUnlisted(unresolvedAssets, rateLimiter)

		for _, asset := range unresolvedAssets {
			if result, ok := figiResults[asset.Ticker]; ok && result.CompositeFIGI != "" {
				figiMap[asset.Ticker] = result.CompositeFIGI
			}
		}
	}

	// Step 4: Generate synthetic FIGIs for anything still unresolved, emit as assets
	for ticker, name := range uniqueTickers {
		if figiMap[ticker] == "" {
			syntheticFigi := figi.GenerateSyntheticFIGI(ticker, name)
			figiMap[ticker] = syntheticFigi

			logger.Info().Str("ticker", ticker).Str("name", name).Str("figi", syntheticFigi).Msg("generated synthetic FIGI")

			out <- &data.Observation{
				AssetObject: &data.Asset{
					Ticker:        ticker,
					Name:          name,
					CompositeFigi: syntheticFigi,
					Active:        false,
				},
				ObservationDate:  time.Now(),
				SubscriptionID:   sub.ID,
				SubscriptionName: sub.Name,
			}

			count++
		}
	}

	logger.Info().
		Int("total_tickers", len(uniqueTickers)).
		Int("resolved_from_db", len(uniqueTickers)-len(unresolvedAssets)).
		Int("resolved_from_openfigi", len(unresolvedAssets)-countMissing(figiMap, uniqueTickers)).
		Int("synthetic", countSynthetic(figiMap)).
		Msg("FIGI resolution complete")
```

- [ ] **Step 3: Add helper functions at the bottom of the file**

These are only needed for the log message above. Add them at the end of `sharadar_import.go`:

```go
func countMissing(figiMap map[string]string, tickers map[string]string) int {
	missing := 0
	for ticker := range tickers {
		if figiMap[ticker] == "" {
			missing++
		}
	}

	return missing
}

func countSynthetic(figiMap map[string]string) int {
	count := 0
	for _, f := range figiMap {
		if len(f) >= 3 && f[:3] == "PVG" {
			count++
		}
	}

	return count
}
```

- [ ] **Step 4: Export the rate limiter constructor**

In `figi/openfigi.go`, rename the unexported `rateLimit()` function to `RateLimit()` so it can be called from the import code:

```go
func RateLimit() *rate.Limiter {
	dur := (time.Second * 6) / 25
	openFigiRate := rate.Every(dur)

	return rate.NewLimiter(openFigiRate, 10)
}
```

Also update the one caller inside `figi/openfigi.go` -- the `Enrich` function:

```go
func Enrich(assets ...*data.Asset) {
	rateLimiter := RateLimit()
```

- [ ] **Step 5: Verify build**

Run: `go build ./...`
Expected: Clean build.

- [ ] **Step 6: Lint**

Run: `golangci-lint run --fix ./...`
Expected: 0 issues.

- [ ] **Step 7: Commit**

```bash
git add provider/sharadar/sharadar_import.go figi/openfigi.go
git commit -m "feat: resolve FIGIs for all tickers in SP500 import including delisted and synthetic"
```

---

### Task 6: Run full test suite

- [ ] **Step 1: Run all tests**

Run: `ginkgo run -race ./...`
Expected: All tests pass.

- [ ] **Step 2: Fix any failures and commit**

If tests fail, fix and commit.
