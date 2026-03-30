# iShares ETF Catalog Expansion Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the hardcoded 19-entry iShares ETF map with an embedded JSON catalog of ~287 US equity ETFs.

**Architecture:** The hardcoded `iSharesETFMap` Go map literal in `provider/ishares.go` is replaced by a `//go:embed` directive that loads `provider/ishares_etfs.json` at init time. All downstream code (parser, helpers, fetch logic) stays unchanged.

**Tech Stack:** Go `encoding/json`, `embed` stdlib packages. No new dependencies.

**Spec:** `docs/superpowers/specs/2026-03-29-ishares-catalog-expansion-design.md`

---

### Task 1: Create the JSON data file

**Files:**
- Create: `provider/ishares_etfs.json`

The data file has been pre-generated from scraping the iShares website and is available at `ishares_equity_etfs_final.json` in the project root.

- [ ] **Step 1: Copy the pre-generated JSON file into place**

```bash
cp ishares_equity_etfs_final.json provider/ishares_etfs.json
```

- [ ] **Step 2: Verify the file is valid JSON with expected structure**

```bash
python3 -c "
import json
with open('provider/ishares_etfs.json') as f:
    etfs = json.load(f)
print(f'Entries: {len(etfs)}')
assert len(etfs) > 280
for e in etfs:
    assert 'ticker' in e and 'productId' in e and 'slug' in e and 'indexName' in e
    assert len(e['ticker']) > 0
    assert len(e['productId']) > 0
print('All entries valid')
# Check known ETF
ivv = [e for e in etfs if e['ticker'] == 'IVV'][0]
assert ivv['productId'] == '239726'
assert ivv['indexName'] == 'sp500'
print('IVV check passed')
"
```

Expected: `Entries: 287`, `All entries valid`, `IVV check passed`

- [ ] **Step 3: Commit**

```bash
git add provider/ishares_etfs.json
git commit -m "data: add comprehensive iShares equity ETF catalog (287 ETFs)"
```

---

### Task 2: Write the failing test for embed loading

**Files:**
- Create: `provider/ishares_embed_test.go`

- [ ] **Step 1: Write test that verifies the ETF map is populated from embedded JSON**

Create `provider/ishares_embed_test.go`:

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
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestISharesEmbed(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "iShares Embed Suite")
}

var _ = Describe("iSharesETFMap", func() {
	It("is populated with entries from the embedded JSON", func() {
		Expect(len(iSharesETFMap)).To(BeNumerically(">", 280))
	})

	It("contains IVV with correct metadata", func() {
		etf, ok := iSharesETFMap["IVV"]
		Expect(ok).To(BeTrue())
		Expect(etf.ProductID).To(Equal("239726"))
		Expect(etf.IndexName).To(Equal("sp500"))
		Expect(etf.Slug).To(ContainSubstring("sp-500"))
	})

	It("contains IWM with correct metadata", func() {
		etf, ok := iSharesETFMap["IWM"]
		Expect(ok).To(BeTrue())
		Expect(etf.ProductID).To(Equal("239710"))
		Expect(etf.IndexName).To(Equal("russell-2000"))
	})

	It("preserves all original 19 ETFs", func() {
		originalTickers := []string{
			"IVV", "IWB", "IWD", "IWF", "IWM", "IJH", "IJR",
			"IXUS", "IEFA", "IEMG", "IVW", "IVE", "ITOT",
			"IWV", "IWR", "IWS", "IWP", "IWO", "IWN",
		}
		for _, ticker := range originalTickers {
			_, ok := iSharesETFMap[ticker]
			Expect(ok).To(BeTrue(), "expected %s to be in iSharesETFMap", ticker)
		}
	})

	It("has unique index names", func() {
		seen := make(map[string]string)
		for ticker, etf := range iSharesETFMap {
			if prev, ok := seen[etf.IndexName]; ok {
				Fail("duplicate indexName '" + etf.IndexName + "' for tickers " + prev + " and " + ticker)
			}
			seen[etf.IndexName] = ticker
		}
	})
})
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
ginkgo run -race ./provider/ --focus "iSharesETFMap"
```

Expected: Tests fail because `iSharesETFMap` only has 19 entries (the hardcoded ones), so the `BeNumerically(">", 280)` check fails.

- [ ] **Step 3: Commit the test**

```bash
git add provider/ishares_embed_test.go
git commit -m "test: add tests for embedded iShares ETF catalog loading"
```

---

### Task 3: Replace hardcoded map with embedded JSON loading

**Files:**
- Modify: `provider/ishares.go:17-60` (imports and map declaration)

- [ ] **Step 1: Modify ishares.go to use go:embed**

Replace the imports and hardcoded map in `provider/ishares.go`. The new code should:

1. Add `"embed"` and `"encoding/json"` to the imports
2. Remove the hardcoded map literal
3. Add the embed directive and init function

Replace the import block (lines 17-30):

```go
import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/playwright_helpers"
	"github.com/playwright-community/playwright-go"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
)
```

Replace the `iSharesETF` struct, the hardcoded map, and add the embed + init (lines 32-60):

```go
type IShares struct{}

type iSharesETF struct {
	ProductID string `json:"productId"`
	Slug      string `json:"slug"`
	IndexName string `json:"indexName"`
}

//go:embed ishares_etfs.json
var iSharesETFData []byte

var iSharesETFMap map[string]iSharesETF

func init() {
	var entries []struct {
		Ticker string `json:"ticker"`
		iSharesETF
	}

	if err := json.Unmarshal(iSharesETFData, &entries); err != nil {
		panic("failed to parse embedded ishares_etfs.json: " + err.Error())
	}

	iSharesETFMap = make(map[string]iSharesETF, len(entries))
	for _, e := range entries {
		iSharesETFMap[e.Ticker] = e.iSharesETF
	}
}
```

Note: JSON tags are added to the `iSharesETF` struct fields so the embedded struct unmarshaling works correctly.

- [ ] **Step 2: Run the tests to verify they pass**

```bash
ginkgo run -race ./provider/ --focus "iSharesETFMap"
```

Expected: All 5 tests pass.

- [ ] **Step 3: Run the full provider test suite**

```bash
ginkgo run -race ./provider/
```

Expected: All existing tests still pass.

- [ ] **Step 4: Run the linter**

```bash
make lint
```

Expected: No lint errors.

- [ ] **Step 5: Commit**

```bash
git add provider/ishares.go
git commit -m "refactor: load iShares ETF catalog from embedded JSON

Replace the hardcoded 19-entry iSharesETFMap with go:embed loading
from ishares_etfs.json, expanding coverage to 287 US equity ETFs."
```

---

### Task 4: Clean up temporary files

**Files:**
- Delete: `cmd/ishares_scrape/main.go`, `ishares_page.html`, `ishares_products.json`, `ishares_all_etfs.json`, `ishares_equity_etfs_final.json`, `ishares_screener_response.json`

- [ ] **Step 1: Remove temporary scraping files**

```bash
rm -rf cmd/ishares_scrape/
rm -f ishares_page.html ishares_products.json ishares_all_etfs.json ishares_equity_etfs_final.json ishares_screener_response.json ishares_sitemap.xml
```

- [ ] **Step 2: Verify build still works**

```bash
make build
```

Expected: Build succeeds.

- [ ] **Step 3: Commit cleanup**

```bash
git add -A
git commit -m "chore: remove temporary iShares scraping utilities"
```
