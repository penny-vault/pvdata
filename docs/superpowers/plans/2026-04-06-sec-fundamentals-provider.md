# SEC Fundamentals Provider Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a new `provider/sec/` package that extracts fundamental financial data from SEC EDGAR companyfacts API, producing `data.Fundamental` observations matching Sharadar's schema.

**Architecture:** The provider fetches pre-parsed XBRL data from SEC's companyfacts API (bulk zip for backfill, per-company JSON for incremental updates via RSS feed discovery). A data-driven mapping table translates ~2,770 US-GAAP XBRL tags to ~80 Sharadar fundamental fields. Restatement tracking produces 6 dimensions (ARQ/MRQ/ARY/MRY/ART/MRT).

**Tech Stack:** Go, go-resty/resty (HTTP), tidwall/gjson (JSON), golang.org/x/time/rate (rate limiting), Ginkgo v2 + Gomega (testing)

**Spec:** `docs/superpowers/specs/2026-04-06-sec-fundamentals-provider-design.md`

---

## File Structure

```
provider/sec/
  sec.go                  # Provider struct, registration, datasets, config
  companyfacts.go         # CompanyFacts types, JSON parsing, HTTP client, bulk zip
  mapping.go              # Mapping engine: resolve direct + derived fields
  mapping_config.go       # Data-driven mapping table (111 Sharadar fields -> XBRL tags)
  dimensions.go           # Period identification, AR/MR restatement, TTM computation
  cik.go                  # CIK <-> ticker/FIGI resolution
  rss.go                  # XBRL RSS feed polling for filing discovery
  sec_suite_test.go       # Ginkgo test suite
  sec_test.go             # Provider registration tests
  companyfacts_test.go    # companyfacts parser tests
  mapping_test.go         # Mapping engine tests
  dimensions_test.go      # Dimension/TTM tests
  cik_test.go             # CIK resolution tests
  rss_test.go             # RSS feed tests
  testdata/               # Fixture files
    CIK0000320193.json    # Apple companyfacts sample (trimmed)
    rss_sample.xml        # XBRL RSS feed sample
```

---

### Task 1: Provider Scaffold

Register the SEC provider with stub datasets.

**Files:**
- Create: `provider/sec/sec.go`
- Create: `provider/sec/sec_suite_test.go`
- Create: `provider/sec/sec_test.go`

- [ ] **Step 1: Write the failing test**

Create `provider/sec/sec_suite_test.go`:

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

package sec

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSEC(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "SEC Suite")
}
```

Create `provider/sec/sec_test.go`:

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

package sec

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pvdata/provider"
)

var _ = Describe("SEC Provider", func() {
	It("registers itself in the provider map", func() {
		p, ok := provider.Map["sec"]
		Expect(ok).To(BeTrue())
		Expect(p.Name()).To(Equal("SEC"))
	})

	It("returns the correct description", func() {
		p := provider.Map["sec"]
		Expect(p.Description()).To(ContainSubstring("SEC EDGAR"))
	})

	It("requires userAgent config", func() {
		p := provider.Map["sec"]
		cfg := p.ConfigDescription()
		Expect(cfg).To(HaveKey("userAgent"))
	})

	It("defines a Fundamentals dataset", func() {
		p := provider.Map["sec"]
		ds := p.Datasets()
		Expect(ds).To(HaveKey("Fundamentals"))
		Expect(ds["Fundamentals"].Name).To(Equal("Fundamentals"))
	})
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `ginkgo run -race ./provider/sec/`

Expected: Compilation failure -- package `sec` does not exist.

- [ ] **Step 3: Write the implementation**

Create `provider/sec/sec.go`:

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

package sec

import (
	"context"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
)

func init() {
	provider.Register("sec", &SEC{})
}

type SEC struct{}

func (s *SEC) Name() string {
	return "SEC"
}

func (s *SEC) Description() string {
	return "SEC EDGAR fundamentals extracted from 10-K and 10-Q XBRL filings via the companyfacts API"
}

func (s *SEC) ConfigDescription() map[string]string {
	return map[string]string{
		"userAgent": "Email address for SEC User-Agent header (e.g. pvdata/1.0 user@email.com):",
		"rateLimit": "Maximum requests per second to SEC EDGAR (default 10):",
	}
}

func (s *SEC) Datasets() map[string]provider.Dataset {
	return map[string]provider.Dataset{
		"Fundamentals": {
			Name:        "Fundamentals",
			Description: "Financial statement fundamentals from SEC EDGAR XBRL filings (10-K and 10-Q).",
			DataTypes:   []*data.DataType{data.DataTypes[data.FundamentalsKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2009, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: fetchFundamentals,
		},
	}
}

func fetchFundamentals(ctx context.Context, sub *library.Subscription, out chan<- *data.Observation, exit chan<- data.RunSummary) {
	// TODO: implement in later tasks
	exit <- data.RunSummary{}
}
```

- [ ] **Step 4: Add blank import to ensure registration**

Check how Sharadar's blank import works. There should be a file that imports all providers. Find it and add `_ "github.com/penny-vault/pvdata/provider/sec"` alongside the existing sharadar import.

- [ ] **Step 5: Run test to verify it passes**

Run: `ginkgo run -race ./provider/sec/`

Expected: 4 specs passed, 0 failures.

- [ ] **Step 6: Run linter**

Run: `golangci-lint run --fix ./provider/sec/...`

Expected: No errors.

- [ ] **Step 7: Commit**

```bash
git add provider/sec/
git commit -m "feat(sec): scaffold SEC provider with registration and stub fetch"
```

---

### Task 2: CompanyFacts Types and Parser

Parse SEC companyfacts JSON into Go structs.

**Files:**
- Create: `provider/sec/companyfacts.go`
- Create: `provider/sec/companyfacts_test.go`
- Create: `provider/sec/testdata/CIK0000320193.json`

**Context:** The companyfacts JSON structure is:
```json
{
  "cik": 320193,
  "entityName": "Apple Inc.",
  "facts": {
    "us-gaap": {
      "Revenues": {
        "label": "Revenues",
        "description": "...",
        "units": {
          "USD": [
            {"end": "2016-09-24", "val": 215639000000, "accn": "...", "fy": 2018, "fp": "FY", "form": "10-K", "filed": "2018-11-05", "start": "2015-09-27", "frame": "CY2016"}
          ]
        }
      }
    }
  }
}
```

Duration concepts (income statement, cash flow) have `start` + `end`. Instant concepts (balance sheet) have only `end`.

- [ ] **Step 1: Create a trimmed test fixture**

Download Apple's companyfacts JSON and trim it to 5-10 concepts covering different types (duration/USD, instant/USD, USD/shares). Save to `provider/sec/testdata/CIK0000320193.json`.

Fetch via:
```bash
curl -s -H "User-Agent: pvdata/1.0 test@example.com" \
  "https://data.sec.gov/api/xbrl/companyfacts/CIK0000320193.json" \
  | python3 -c "
import json, sys
d = json.load(sys.stdin)
keep = ['Revenues','Assets','CashAndCashEquivalentsAtCarryingValue',
        'NetIncomeLoss','EarningsPerShareBasic','EarningsPerShareDiluted',
        'CommonStockSharesOutstanding','StockholdersEquity',
        'OperatingIncomeLoss','CostOfGoodsAndServicesSold']
filtered = {k: v for k, v in d['facts'].get('us-gaap', {}).items() if k in keep}
d['facts']['us-gaap'] = filtered
d['facts'].pop('dei', None)
json.dump(d, sys.stdout, indent=2)
" > provider/sec/testdata/CIK0000320193.json
```

- [ ] **Step 2: Write the failing test**

Create `provider/sec/companyfacts_test.go`:

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

package sec

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CompanyFacts", func() {
	Describe("ParseCompanyFacts", func() {
		It("parses CIK and entity name", func() {
			jsonData, err := os.ReadFile("testdata/CIK0000320193.json")
			Expect(err).NotTo(HaveOccurred())

			cf, err := ParseCompanyFacts(jsonData)
			Expect(err).NotTo(HaveOccurred())
			Expect(cf.CIK).To(Equal(320193))
			Expect(cf.EntityName).To(Equal("Apple Inc."))
		})

		It("parses US-GAAP concepts", func() {
			jsonData, err := os.ReadFile("testdata/CIK0000320193.json")
			Expect(err).NotTo(HaveOccurred())

			cf, err := ParseCompanyFacts(jsonData)
			Expect(err).NotTo(HaveOccurred())
			Expect(cf.Facts).To(HaveKey("Revenues"))
			Expect(cf.Facts).To(HaveKey("Assets"))
		})

		It("parses duration facts with start and end dates", func() {
			jsonData, err := os.ReadFile("testdata/CIK0000320193.json")
			Expect(err).NotTo(HaveOccurred())

			cf, err := ParseCompanyFacts(jsonData)
			Expect(err).NotTo(HaveOccurred())

			revFacts := cf.Facts["Revenues"]
			Expect(len(revFacts)).To(BeNumerically(">", 0))
			// Duration concepts should have Start set
			found := false
			for _, f := range revFacts {
				if !f.Start.IsZero() {
					found = true
					break
				}
			}
			Expect(found).To(BeTrue(), "expected at least one Revenues fact with a start date")
		})

		It("parses instant facts without start dates", func() {
			jsonData, err := os.ReadFile("testdata/CIK0000320193.json")
			Expect(err).NotTo(HaveOccurred())

			cf, err := ParseCompanyFacts(jsonData)
			Expect(err).NotTo(HaveOccurred())

			assetFacts := cf.Facts["Assets"]
			Expect(len(assetFacts)).To(BeNumerically(">", 0))
			// Instant concepts should have zero Start
			for _, f := range assetFacts {
				Expect(f.Start.IsZero()).To(BeTrue(), "Assets is an instant concept, should not have start date")
			}
		})

		It("filters to only 10-K and 10-Q forms", func() {
			jsonData, err := os.ReadFile("testdata/CIK0000320193.json")
			Expect(err).NotTo(HaveOccurred())

			cf, err := ParseCompanyFacts(jsonData)
			Expect(err).NotTo(HaveOccurred())

			for _, facts := range cf.Facts {
				for _, f := range facts {
					Expect(f.Form).To(BeElementOf("10-K", "10-Q"),
						"only 10-K and 10-Q facts should be included")
				}
			}
		})
	})
})
```

- [ ] **Step 3: Run test to verify it fails**

Run: `ginkgo run -race ./provider/sec/`

Expected: Compilation failure -- `ParseCompanyFacts` is not defined.

- [ ] **Step 4: Write the implementation**

Create `provider/sec/companyfacts.go`:

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

package sec

import (
	"fmt"
	"time"

	"github.com/tidwall/gjson"
)

// Fact represents a single XBRL fact from the SEC companyfacts API.
type Fact struct {
	End   time.Time // Period end date (always present)
	Start time.Time // Period start date (present for duration concepts, zero for instant)
	Filed time.Time // Date the filing was submitted to SEC
	Val   float64   // The reported value
	Accn  string    // SEC accession number
	Form  string    // Filing type: "10-K" or "10-Q"
	FY    int       // Fiscal year
	FP    string    // Fiscal period: "FY", "Q1", "Q2", "Q3"
	Frame string    // Period frame tag, e.g. "CY2018Q2I"
}

// CompanyFacts holds all parsed XBRL facts for a single company.
type CompanyFacts struct {
	CIK        int
	EntityName string
	Facts      map[string][]Fact // keyed by US-GAAP concept name (e.g. "Revenues")
}

// ParseCompanyFacts parses SEC companyfacts JSON into a CompanyFacts struct.
// Only facts from 10-K and 10-Q filings are included. For concepts reported in
// multiple units (e.g. USD and EUR), only USD (or shares, USD/shares, pure) facts
// are included.
func ParseCompanyFacts(jsonData []byte) (*CompanyFacts, error) {
	root := gjson.ParseBytes(jsonData)

	cik := root.Get("cik").Int()
	entityName := root.Get("entityName").String()
	if cik == 0 {
		return nil, fmt.Errorf("missing or zero CIK in companyfacts JSON")
	}

	cf := &CompanyFacts{
		CIK:        int(cik),
		EntityName: entityName,
		Facts:      make(map[string][]Fact),
	}

	usGAAP := root.Get("facts.us-gaap")
	if !usGAAP.Exists() {
		return cf, nil
	}

	usGAAP.ForEach(func(conceptName, conceptData gjson.Result) bool {
		concept := conceptName.String()
		var facts []Fact

		// Try each unit type we care about
		for _, unit := range []string{"USD", "USD/shares", "shares", "pure"} {
			unitPath := fmt.Sprintf("units.%s", unit)
			unitData := conceptData.Get(unitPath)
			if !unitData.Exists() {
				continue
			}

			unitData.ForEach(func(_, entry gjson.Result) bool {
				form := entry.Get("form").String()
				if form != "10-K" && form != "10-Q" {
					return true // skip non 10-K/10-Q filings
				}

				f := Fact{
					Val:   entry.Get("val").Float(),
					Accn:  entry.Get("accn").String(),
					Form:  form,
					FY:    int(entry.Get("fy").Int()),
					FP:    entry.Get("fp").String(),
					Frame: entry.Get("frame").String(),
				}

				if endStr := entry.Get("end").String(); endStr != "" {
					if t, err := time.Parse("2006-01-02", endStr); err == nil {
						f.End = t
					}
				}

				if startStr := entry.Get("start").String(); startStr != "" {
					if t, err := time.Parse("2006-01-02", startStr); err == nil {
						f.Start = t
					}
				}

				if filedStr := entry.Get("filed").String(); filedStr != "" {
					if t, err := time.Parse("2006-01-02", filedStr); err == nil {
						f.Filed = t
					}
				}

				facts = append(facts, f)
				return true
			})

			// For USD concepts, don't also load other unit types
			if len(facts) > 0 {
				break
			}
		}

		if len(facts) > 0 {
			cf.Facts[concept] = facts
		}

		return true
	})

	return cf, nil
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `ginkgo run -race ./provider/sec/`

Expected: All specs pass.

- [ ] **Step 6: Run linter**

Run: `golangci-lint run --fix ./provider/sec/...`

- [ ] **Step 7: Commit**

```bash
git add provider/sec/companyfacts.go provider/sec/companyfacts_test.go provider/sec/testdata/
git commit -m "feat(sec): add companyfacts JSON parser with types"
```

---

### Task 3: Mapping Configuration

Define the data-driven mapping table that translates XBRL tags to `data.Fundamental` fields.

**Files:**
- Create: `provider/sec/mapping_config.go`
- Create: `provider/sec/mapping_test.go` (config validation tests only; engine tests in Task 4)

**Context:** Each `data.Fundamental` field maps to either:
- **Direct**: An ordered list of US-GAAP XBRL tag names to try (first match wins)
- **Derived**: A formula referencing other fields (e.g., EBITDA = EBIT + DepreciationAmortizationAndAccretion)

The `StatementType` controls TTM computation: "flow" items sum 4 quarters, "point_in_time" items use latest quarter.

- [ ] **Step 1: Write the failing test**

Add to `provider/sec/mapping_test.go`:

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

package sec

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Mapping Config", func() {
	It("has no duplicate field names", func() {
		seen := make(map[string]bool)
		for _, m := range FieldMappings {
			Expect(seen[m.FieldName]).To(BeFalse(),
				"duplicate field name: %s", m.FieldName)
			seen[m.FieldName] = true
		}
	})

	It("direct mappings have at least one XBRL tag", func() {
		for _, m := range FieldMappings {
			if m.Type == MappingDirect {
				Expect(len(m.XBRLTags)).To(BeNumerically(">", 0),
					"direct mapping %s has no XBRL tags", m.FieldName)
			}
		}
	})

	It("derived mappings have a formula", func() {
		for _, m := range FieldMappings {
			if m.Type == MappingDerived {
				Expect(len(m.Operands)).To(BeNumerically(">", 0),
					"derived mapping %s has no operands", m.FieldName)
			}
		}
	})

	It("derived formula operands reference existing field names", func() {
		fieldSet := make(map[string]bool)
		for _, m := range FieldMappings {
			fieldSet[m.FieldName] = true
		}

		for _, m := range FieldMappings {
			if m.Type == MappingDerived {
				for _, op := range m.Operands {
					Expect(fieldSet[op]).To(BeTrue(),
						"derived mapping %s references unknown field %s", m.FieldName, op)
				}
			}
		}
	})

	It("all mappings have a valid statement type", func() {
		for _, m := range FieldMappings {
			Expect(m.StatementType).To(BeElementOf(StmtFlow, StmtPointInTime, StmtMetric),
				"mapping %s has invalid statement type: %s", m.FieldName, m.StatementType)
		}
	})
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `ginkgo run -race ./provider/sec/`

Expected: Compilation failure -- types and `FieldMappings` not defined.

- [ ] **Step 3: Write the implementation**

Create `provider/sec/mapping_config.go`. This file is large because it contains the full mapping table. The mapping types and the complete field mapping data are shown below.

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

package sec

// MappingType indicates whether a field is read directly from XBRL or derived.
type MappingType string

const (
	MappingDirect  MappingType = "direct"
	MappingDerived MappingType = "derived"
)

// StatementType controls how TTM is computed for this field.
type StatementType string

const (
	StmtFlow        StatementType = "flow"          // Sum 4 quarters for TTM
	StmtPointInTime StatementType = "point_in_time"  // Use latest quarter for TTM
	StmtMetric      StatementType = "metric"         // Recomputed from other fields, not summed
)

// FormulaOp defines the operation for derived fields.
type FormulaOp string

const (
	OpAdd      FormulaOp = "add"      // A + B + ...
	OpSubtract FormulaOp = "subtract" // A - B
	OpDivide   FormulaOp = "divide"   // A / B
)

// FieldMapping maps a data.Fundamental field to XBRL tag(s) or a formula.
type FieldMapping struct {
	FieldName     string        // data.Fundamental field name (e.g. "Revenues")
	Type          MappingType   // "direct" or "derived"
	StatementType StatementType // Controls TTM computation
	ValueType     string        // "int64" or "float64" -- matches data.Fundamental field type

	// For direct mappings: ordered list of XBRL concept names to try
	XBRLTags []string

	// For derived mappings: formula
	Op       FormulaOp // Operation to apply
	Operands []string  // Field names to use as operands

	// For derived mappings that also have a direct XBRL fallback
	FallbackTags []string
}

// FieldMappings defines the complete mapping from XBRL to data.Fundamental fields.
// Order matters for derived fields -- dependencies must come before dependents.
// The edgartools project (MIT license, github.com/dgunning/edgartools) was used
// as a reference for XBRL tag selection, validated against 32,240 real SEC filings.
var FieldMappings = []FieldMapping{
	// ==================== BALANCE SHEET (point-in-time) ====================

	{
		FieldName: "TotalAssets", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"Assets"},
	},
	{
		FieldName: "CurrentAssets", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"AssetsCurrent"},
	},
	{
		FieldName: "AssetsNonCurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"AssetsNoncurrent"},
		// Fallback: Assets - AssetsCurrent (computed in derived pass if missing)
	},
	{
		FieldName: "CashAndEquivalents", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"CashAndCashEquivalentsAtCarryingValue",
			"CashCashEquivalentsAndShortTermInvestments",
			"Cash",
			"CashEquivalentsAtCarryingValue",
		},
	},
	{
		FieldName: "Inventory", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"InventoryNet", "InventoryFinishedGoodsAndWorkInProcess"},
	},
	{
		FieldName: "Investments", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"Investments",
			"ShortTermInvestments",
			"LongTermInvestments",
			"MarketableSecuritiesCurrent",
		},
	},
	{
		FieldName: "InvestmentsCurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"ShortTermInvestments",
			"MarketableSecuritiesCurrent",
			"AvailableForSaleSecuritiesDebtSecuritiesCurrent",
		},
	},
	{
		FieldName: "InvestmentsNonCurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"LongTermInvestments",
			"MarketableSecuritiesNoncurrent",
			"AvailableForSaleSecuritiesDebtSecuritiesNoncurrent",
		},
	},
	{
		FieldName: "Receivables", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"AccountsReceivableNetCurrent",
			"AccountsReceivableNet",
			"ReceivablesNetCurrent",
		},
	},
	{
		FieldName: "Payables", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"AccountsPayableCurrent",
			"AccountsPayableAndAccruedLiabilitiesCurrent",
		},
	},
	{
		FieldName: "Deposits", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"Deposits", "DepositsDomestic", "DepositsTotal"},
	},
	{
		FieldName: "PropertyPlantAndEquipmentNet", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"PropertyPlantAndEquipmentNet"},
	},
	{
		FieldName: "Intangibles", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"IntangibleAssetsNetIncludingGoodwill",
			"IntangibleAssetsNetExcludingGoodwill",
			"Goodwill",
		},
	},
	{
		FieldName: "TaxAssets", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"DeferredTaxAssetsNet",
			"DeferredTaxAssetsNetCurrent",
			"DeferredIncomeTaxAssetsNet",
		},
	},
	{
		FieldName: "TaxLiabilities", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"DeferredTaxLiabilities",
			"DeferredIncomeTaxLiabilitiesNet",
			"DeferredTaxLiabilitiesNoncurrent",
		},
	},
	{
		FieldName: "TotalDebt", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"DebtCurrent",
			"LongTermDebtAndCapitalLeaseObligations",
			"LongTermDebt",
		},
		// Often needs to be computed as DebtCurrent + LongTermDebt
	},
	{
		FieldName: "DebtCurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"ShortTermBorrowings",
			"LongTermDebtCurrent",
			"DebtCurrent",
			"CommercialPaper",
		},
	},
	{
		FieldName: "DebtNonCurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"LongTermDebtNoncurrent",
			"LongTermDebt",
			"LongTermDebtAndCapitalLeaseObligationsIncludingCurrentMaturities",
		},
	},
	{
		FieldName: "DeferredRevenue", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"DeferredRevenue",
			"ContractWithCustomerLiability",
			"DeferredRevenueCurrent",
		},
	},
	{
		FieldName: "TotalLiabilities", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"Liabilities", "LiabilitiesAndStockholdersEquity"},
	},
	{
		FieldName: "CurrentLiabilities", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"LiabilitiesCurrent"},
	},
	{
		FieldName: "LiabilitiesNonCurrent", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"LiabilitiesNoncurrent"},
	},
	{
		FieldName: "Equity", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"StockholdersEquity",
			"StockholdersEquityIncludingPortionAttributableToNoncontrollingInterest",
		},
	},
	{
		FieldName: "AccumulatedOtherComprehensiveIncome", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"AccumulatedOtherComprehensiveIncomeLossNetOfTax",
		},
	},
	{
		FieldName: "AccumulatedRetainedEarningsDeficit", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{"RetainedEarningsAccumulatedDeficit"},
	},

	// ==================== INCOME STATEMENT (flow) ====================

	{
		FieldName: "Revenues", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"Revenues",
			"RevenueFromContractWithCustomerExcludingAssessedTax",
			"RevenueFromContractWithCustomerIncludingAssessedTax",
			"SalesRevenueNet",
			"SalesRevenueGoodsNet",
			"SalesRevenueServicesNet",
			"InterestAndDividendIncomeOperating",
			"RegulatedAndUnregulatedOperatingRevenue",
			"HealthCareOrganizationRevenue",
			"RevenueMineralSales",
			"OilAndGasRevenue",
			"FinancialServicesRevenue",
			"ElectricUtilityRevenue",
		},
	},
	{
		FieldName: "CostOfRevenue", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"CostOfGoodsAndServicesSold",
			"CostOfGoodsSold",
			"CostOfRevenue",
			"CostOfGoodsAndServiceExcludingDepreciationDepletionAndAmortization",
		},
	},
	{
		FieldName: "GrossProfit", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags:     []string{"GrossProfit"},
		// Fallback: Revenues - CostOfRevenue
	},
	{
		FieldName: "OperatingExpenses", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"OperatingExpenses",
			"CostsAndExpenses",
		},
	},
	{
		FieldName: "SellingGeneralAndAdministrativeExpense", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"SellingGeneralAndAdministrativeExpense",
			"GeneralAndAdministrativeExpense",
		},
	},
	{
		FieldName: "RandDExpenses", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{"ResearchAndDevelopmentExpense"},
	},
	{
		FieldName: "OperatingIncome", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"OperatingIncomeLoss",
		},
	},
	{
		FieldName: "InterestExpense", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"InterestExpense",
			"InterestExpenseDebt",
			"InterestIncomeExpenseNet",
		},
	},
	{
		FieldName: "IncomeTaxExpense", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"IncomeTaxExpenseBenefit",
			"IncomeTaxesPaid",
		},
	},
	{
		FieldName: "NetIncome", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"NetIncomeLoss",
			"ProfitLoss",
		},
	},
	{
		FieldName: "NetIncomeCommonStock", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"NetIncomeLossAvailableToCommonStockholdersBasic",
			"NetIncomeLoss",
		},
	},
	{
		FieldName: "ConsolidatedIncome", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"ProfitLoss",
			"IncomeLossFromContinuingOperationsIncludingPortionAttributableToNoncontrollingInterest",
			"NetIncomeLoss",
		},
	},
	{
		FieldName: "NetLossIncomeDiscontinuedOperations", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"IncomeLossFromDiscontinuedOperationsNetOfTax",
			"DiscontinuedOperationIncomeLossFromDiscontinuedOperationDuringPhaseOutPeriodNetOfTax",
		},
	},
	{
		FieldName: "NetIncomeToNonControllingInterests", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"NetIncomeLossAttributableToNoncontrollingInterest",
		},
	},
	{
		FieldName: "PreferredDividendsIncomeStatementImpact", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"PreferredStockDividendsIncomeStatementImpact",
			"PreferredStockDividendsAndOtherAdjustments",
		},
	},

	// ==================== PER-SHARE DATA (flow) ====================

	{
		FieldName: "EPS", Type: MappingDirect, StatementType: StmtFlow, ValueType: "float64",
		XBRLTags: []string{"EarningsPerShareBasic"},
	},
	{
		FieldName: "EPSDiluted", Type: MappingDirect, StatementType: StmtFlow, ValueType: "float64",
		XBRLTags: []string{"EarningsPerShareDiluted"},
	},
	{
		FieldName: "DividendsPerBasicCommonShare", Type: MappingDirect, StatementType: StmtFlow, ValueType: "float64",
		XBRLTags: []string{
			"CommonStockDividendsPerShareDeclared",
			"CommonStockDividendsPerShareCashPaid",
		},
	},

	// ==================== SHARE COUNTS ====================

	{
		FieldName: "SharesBasic", Type: MappingDirect, StatementType: StmtPointInTime, ValueType: "int64",
		XBRLTags: []string{
			"CommonStockSharesOutstanding",
			"EntityCommonStockSharesOutstanding",
		},
	},
	{
		FieldName: "WeightedAverageShares", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"WeightedAverageNumberOfShareOutstandingBasicAndDiluted",
			"WeightedAverageNumberOfSharesOutstandingBasic",
		},
	},
	{
		FieldName: "WeightedAverageSharesDiluted", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"WeightedAverageNumberOfDilutedSharesOutstanding",
			"WeightedAverageNumberOfShareOutstandingBasicAndDiluted",
		},
	},

	// ==================== CASH FLOW STATEMENT (flow) ====================

	{
		FieldName: "NetCashFlowFromOperations", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"NetCashProvidedByUsedInOperatingActivities",
			"NetCashProvidedByUsedInOperatingActivitiesContinuingOperations",
		},
	},
	{
		FieldName: "NetCashFlowFromInvesting", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"NetCashProvidedByUsedInInvestingActivities",
			"NetCashProvidedByUsedInInvestingActivitiesContinuingOperations",
		},
	},
	{
		FieldName: "NetCashFlowFromFinancing", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"NetCashProvidedByUsedInFinancingActivities",
			"NetCashProvidedByUsedInFinancingActivitiesContinuingOperations",
		},
	},
	{
		FieldName: "DepreciationAmortizationAndAccretion", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"DepreciationDepletionAndAmortization",
			"DepreciationAmortizationAndAccretionNet",
			"DepreciationAndAmortization",
			"Depreciation",
		},
	},
	{
		FieldName: "CapitalExpenditure", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"PaymentsToAcquirePropertyPlantAndEquipment",
			"PaymentsToAcquireProductiveAssets",
			"CapitalExpendituresIncurredButNotYetPaid",
		},
	},
	{
		FieldName: "ShareBasedCompensation", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"ShareBasedCompensation",
			"AllocatedShareBasedCompensationExpense",
		},
	},
	{
		FieldName: "NetCashFlowBusiness", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"PaymentsToAcquireBusinessesNetOfCashAcquired",
			"PaymentsToAcquireBusinessesGross",
		},
	},
	{
		FieldName: "NetCashFlowCommon", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"PaymentsForRepurchaseOfCommonStock",
			"ProceedsFromIssuanceOfCommonStock",
		},
	},
	{
		FieldName: "NetCashFlowDebt", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"ProceedsFromIssuanceOfLongTermDebt",
			"RepaymentsOfLongTermDebt",
		},
	},
	{
		FieldName: "NetCashFlowDividend", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"PaymentsOfDividendsCommonStock",
			"PaymentsOfDividends",
		},
	},
	{
		FieldName: "NetCashFlowInvest", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"PaymentsToAcquireInvestments",
			"ProceedsFromSaleAndMaturityOfMarketableSecurities",
		},
	},
	{
		FieldName: "NetCashFlowFx", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"EffectOfExchangeRateOnCashCashEquivalentsRestrictedCashAndRestrictedCashEquivalents",
			"EffectOfExchangeRateOnCashAndCashEquivalents",
		},
	},
	{
		FieldName: "NetCashFlow", Type: MappingDirect, StatementType: StmtFlow, ValueType: "int64",
		XBRLTags: []string{
			"CashCashEquivalentsRestrictedCashAndRestrictedCashEquivalentsPeriodIncreaseDecreaseIncludingExchangeRateEffect",
			"CashAndCashEquivalentsPeriodIncreaseDecrease",
			"CashPeriodIncreaseDecrease",
		},
	},

	// ==================== DERIVED FIELDS ====================
	// These must come AFTER their dependencies in the list.

	// EBIT = NetIncome + IncomeTaxExpense + InterestExpense
	{
		FieldName: "EBIT", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		FallbackTags: []string{
			"IncomeLossFromContinuingOperationsBeforeIncomeTaxesExtraordinaryItemsNoncontrollingInterest",
		},
		Op:       OpAdd,
		Operands: []string{"NetIncome", "IncomeTaxExpense", "InterestExpense"},
	},
	// EBITDA = EBIT + DepreciationAmortizationAndAccretion
	{
		FieldName: "EBITDA", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op:       OpAdd,
		Operands: []string{"EBIT", "DepreciationAmortizationAndAccretion"},
	},
	// EBT = NetIncome + IncomeTaxExpense
	{
		FieldName: "EBT", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		FallbackTags: []string{
			"IncomeLossFromContinuingOperationsBeforeIncomeTaxesExtraordinaryItemsNoncontrollingInterest",
			"IncomeLossFromContinuingOperationsBeforeIncomeTaxesMinorityInterestAndIncomeLossFromEquityMethodInvestments",
		},
		Op:       OpAdd,
		Operands: []string{"NetIncome", "IncomeTaxExpense"},
	},
	// FreeCashFlow = NetCashFlowFromOperations - CapitalExpenditure
	{
		FieldName: "FreeCashFlow", Type: MappingDerived, StatementType: StmtFlow, ValueType: "int64",
		Op:       OpSubtract,
		Operands: []string{"NetCashFlowFromOperations", "CapitalExpenditure"},
	},
	// WorkingCapital = CurrentAssets - CurrentLiabilities
	{
		FieldName: "WorkingCapital", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		Op:       OpSubtract,
		Operands: []string{"CurrentAssets", "CurrentLiabilities"},
	},
	// TangibleAssetValue = TotalAssets - Intangibles
	{
		FieldName: "TangibleAssetValue", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		Op:       OpSubtract,
		Operands: []string{"TotalAssets", "Intangibles"},
	},
	// InvestedCapital = TotalDebt + TotalAssets - Intangibles - CashAndEquivalents - CurrentLiabilities
	// Simplified: TotalDebt + Equity - CashAndEquivalents (alternative definition)
	// Using Sharadar's definition: Debt + Assets - Intangibles - CashnEq - LiabilitiesC
	// This is complex -- we compute: TotalDebt + TotalAssets - Intangibles - CashAndEquivalents - CurrentLiabilities
	{
		FieldName: "InvestedCapital", Type: MappingDerived, StatementType: StmtPointInTime, ValueType: "int64",
		Op:       OpAdd,
		Operands: []string{"TotalDebt", "TotalAssets"},
		// Note: Full formula needs subtraction of multiple fields. This will need
		// custom handling in the engine (see Task 4). The engine will recognize
		// InvestedCapital as a special case.
	},

	// ==================== RATIO METRICS (derived) ====================

	// GrossMargin = GrossProfit / Revenues
	{
		FieldName: "GrossMargin", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"GrossProfit", "Revenues"},
	},
	// ProfitMargin = NetIncomeCommonStock / Revenues
	{
		FieldName: "ProfitMargin", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"NetIncomeCommonStock", "Revenues"},
	},
	// EBITDAMargin = EBITDA / Revenues
	{
		FieldName: "EBITDAMargin", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"EBITDA", "Revenues"},
	},
	// CurrentRatio = CurrentAssets / CurrentLiabilities
	{
		FieldName: "CurrentRatio", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"CurrentAssets", "CurrentLiabilities"},
	},
	// DebtToEquityRatio = TotalLiabilities / Equity
	{
		FieldName: "DebtToEquityRatio", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"TotalLiabilities", "Equity"},
	},
	// AssetTurnover = Revenues / TotalAssets
	{
		FieldName: "AssetTurnover", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"Revenues", "TotalAssets"},
	},
	// ReturnOnSales = EBIT / Revenues
	{
		FieldName: "ReturnOnSales", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"EBIT", "Revenues"},
	},

	// ==================== PER-SHARE METRICS (derived) ====================

	// FreeCashFlowPerShare = FreeCashFlow / WeightedAverageShares
	{
		FieldName: "FreeCashFlowPerShare", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"FreeCashFlow", "WeightedAverageShares"},
	},
	// BookValuePerShare = Equity / SharesBasic
	{
		FieldName: "BookValuePerShare", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"Equity", "SharesBasic"},
	},
	// SalesPerShare = Revenues / WeightedAverageShares
	{
		FieldName: "SalesPerShare", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"Revenues", "WeightedAverageShares"},
	},
	// TangibleAssetsBookValuePerShare = TangibleAssetValue / SharesBasic
	{
		FieldName: "TangibleAssetsBookValuePerShare", Type: MappingDerived, StatementType: StmtMetric, ValueType: "float64",
		Op:       OpDivide,
		Operands: []string{"TangibleAssetValue", "SharesBasic"},
	},

	// Note: The following Sharadar fields require market price data and are
	// NOT computable from SEC filings alone. They are intentionally omitted:
	// - MarketCapitalization, EnterpriseValue, PE, PB, PS, PE1, PS1
	// - EVtoEBIT, EVtoEBITDA, DividendYield, PayoutRatio, Price
	// - ShareFactor, FxUSD
	// - ROA, ROE, ROIC (these need average values across periods)
	// - AverageAssets, EquityAvg, InvestedCapitalAverage (need prior period)
	//
	// These will be computed in a later pass once we have multi-period data
	// available (see dimensions.go for average computations).
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `ginkgo run -race ./provider/sec/`

Expected: All specs pass.

- [ ] **Step 5: Run linter**

Run: `golangci-lint run --fix ./provider/sec/...`

- [ ] **Step 6: Commit**

```bash
git add provider/sec/mapping_config.go provider/sec/mapping_test.go
git commit -m "feat(sec): add XBRL-to-Fundamental field mapping config"
```

---

### Task 4: Mapping Engine

Resolve direct and derived fields from parsed CompanyFacts for a target period.

**Files:**
- Create: `provider/sec/mapping.go`
- Modify: `provider/sec/mapping_test.go`

**Context:** Given a `CompanyFacts` and a target period end date + form type, resolve all `FieldMapping` entries into a `map[string]float64` of field values. Direct fields do XBRL tag lookup with fallback. Derived fields compute from resolved values.

- [ ] **Step 1: Write the failing test**

Append to `provider/sec/mapping_test.go`:

```go
var _ = Describe("Mapping Engine", func() {
	var cf *CompanyFacts

	BeforeEach(func() {
		jsonData, err := os.ReadFile("testdata/CIK0000320193.json")
		Expect(err).NotTo(HaveOccurred())
		cf, err = ParseCompanyFacts(jsonData)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("ResolveDirect", func() {
		It("resolves a direct field from XBRL facts", func() {
			// Apple's Assets (instant, balance sheet) for a 10-K period
			periodEnd := time.Date(2018, 9, 29, 0, 0, 0, 0, time.UTC)
			val, ok := ResolveDirect(cf, FieldMapping{
				FieldName: "TotalAssets",
				Type:      MappingDirect,
				XBRLTags:  []string{"Assets"},
			}, periodEnd, "10-K")
			Expect(ok).To(BeTrue())
			Expect(val).To(BeNumerically(">", 0))
		})

		It("falls back through tag list when first tag not found", func() {
			periodEnd := time.Date(2018, 9, 29, 0, 0, 0, 0, time.UTC)
			val, ok := ResolveDirect(cf, FieldMapping{
				FieldName: "CashAndEquivalents",
				Type:      MappingDirect,
				XBRLTags:  []string{"NonExistentTag", "CashAndCashEquivalentsAtCarryingValue"},
			}, periodEnd, "10-K")
			Expect(ok).To(BeTrue())
			Expect(val).To(BeNumerically(">", 0))
		})

		It("returns false when no tag matches", func() {
			periodEnd := time.Date(2018, 9, 29, 0, 0, 0, 0, time.UTC)
			_, ok := ResolveDirect(cf, FieldMapping{
				FieldName: "Test",
				Type:      MappingDirect,
				XBRLTags:  []string{"CompletelyFakeTag"},
			}, periodEnd, "10-K")
			Expect(ok).To(BeFalse())
		})
	})

	Describe("ResolveAllFields", func() {
		It("resolves both direct and derived fields", func() {
			periodEnd := time.Date(2018, 9, 29, 0, 0, 0, 0, time.UTC)
			resolved := ResolveAllFields(cf, periodEnd, "10-K")

			// Direct fields
			_, hasRevenues := resolved["Revenues"]
			Expect(hasRevenues).To(BeTrue())

			_, hasAssets := resolved["TotalAssets"]
			Expect(hasAssets).To(BeTrue())
		})
	})
})
```

Add the `os` and `time` imports to the import block in `mapping_test.go`.

- [ ] **Step 2: Run test to verify it fails**

Run: `ginkgo run -race ./provider/sec/`

Expected: Compilation failure -- `ResolveDirect` and `ResolveAllFields` not defined.

- [ ] **Step 3: Write the implementation**

Create `provider/sec/mapping.go`:

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

package sec

import (
	"time"
)

// ResolveDirect attempts to find a value for a direct field mapping by searching
// the CompanyFacts for matching XBRL tags. Tags are tried in order; the first
// match for the given period end date and form type wins.
//
// For instant (balance sheet) concepts, matches facts where End == periodEnd.
// For duration (income/cash flow) concepts, matches facts where End == periodEnd
// and the filing form matches.
func ResolveDirect(cf *CompanyFacts, m FieldMapping, periodEnd time.Time, formType string) (float64, bool) {
	tags := m.XBRLTags
	if m.Type == MappingDerived {
		tags = m.FallbackTags
	}

	for _, tag := range tags {
		facts, ok := cf.Facts[tag]
		if !ok {
			continue
		}

		// Find the best matching fact for this period
		var best *Fact
		for i := range facts {
			f := &facts[i]

			// Must match the period end date
			if !f.End.Equal(periodEnd) {
				continue
			}

			// Must match the form type
			if f.Form != formType {
				continue
			}

			// For duration concepts, verify the period length is reasonable
			// (roughly a quarter for 10-Q, roughly a year for 10-K)
			if !f.Start.IsZero() {
				days := f.End.Sub(f.Start).Hours() / 24
				if formType == "10-K" && days < 300 {
					continue // Skip quarterly data in an annual filing
				}
				if formType == "10-Q" && days > 200 {
					continue // Skip annual data in a quarterly filing
				}
			}

			// Prefer the fact with the latest filing date (most recent data)
			if best == nil || f.Filed.After(best.Filed) {
				best = f
			}
		}

		if best != nil {
			return best.Val, true
		}
	}

	return 0, false
}

// ResolveAllFields resolves all configured field mappings for a given period.
// Direct fields are resolved first, then derived fields are computed from the
// resolved values.
func ResolveAllFields(cf *CompanyFacts, periodEnd time.Time, formType string) map[string]float64 {
	resolved := make(map[string]float64)

	for _, m := range FieldMappings {
		switch m.Type {
		case MappingDirect:
			if val, ok := ResolveDirect(cf, m, periodEnd, formType); ok {
				resolved[m.FieldName] = val
			}

		case MappingDerived:
			// Try direct XBRL fallback tags first
			if len(m.FallbackTags) > 0 {
				if val, ok := ResolveDirect(cf, m, periodEnd, formType); ok {
					resolved[m.FieldName] = val
					continue
				}
			}

			// Compute from formula
			if val, ok := computeDerived(m, resolved); ok {
				resolved[m.FieldName] = val
			}
		}
	}

	return resolved
}

// computeDerived evaluates a derived field's formula using already-resolved values.
func computeDerived(m FieldMapping, resolved map[string]float64) (float64, bool) {
	// All operands must be present
	vals := make([]float64, len(m.Operands))
	for i, op := range m.Operands {
		v, ok := resolved[op]
		if !ok {
			return 0, false
		}
		vals[i] = v
	}

	switch m.Op {
	case OpAdd:
		sum := 0.0
		for _, v := range vals {
			sum += v
		}
		return sum, true

	case OpSubtract:
		if len(vals) < 2 {
			return 0, false
		}
		return vals[0] - vals[1], true

	case OpDivide:
		if len(vals) < 2 || vals[1] == 0 {
			return 0, false
		}
		return vals[0] / vals[1], true
	}

	return 0, false
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `ginkgo run -race ./provider/sec/`

Expected: All specs pass.

- [ ] **Step 5: Run linter**

Run: `golangci-lint run --fix ./provider/sec/...`

- [ ] **Step 6: Commit**

```bash
git add provider/sec/mapping.go provider/sec/mapping_test.go
git commit -m "feat(sec): add mapping engine with direct and derived field resolution"
```

---

### Task 5: Dimension and Period Engine

Identify reporting periods from CompanyFacts, determine AR vs MR, compute TTM, and produce `data.Fundamental` observations.

**Files:**
- Create: `provider/sec/dimensions.go`
- Create: `provider/sec/dimensions_test.go`

**Context:** From a CompanyFacts, we need to:
1. Identify all unique reporting periods (end dates + form types)
2. For each period, determine the AR (earliest filing) and MR (latest filing) versions
3. Compute TTM by summing 4 recent quarters (flow items) or using latest quarter (point-in-time)
4. Convert resolved fields into `data.Fundamental` structs for all 6 dimensions

- [ ] **Step 1: Write the failing test**

Create `provider/sec/dimensions_test.go`:

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

package sec

import (
	"os"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Dimensions", func() {
	var cf *CompanyFacts

	BeforeEach(func() {
		jsonData, err := os.ReadFile("testdata/CIK0000320193.json")
		Expect(err).NotTo(HaveOccurred())
		cf, err = ParseCompanyFacts(jsonData)
		Expect(err).NotTo(HaveOccurred())
	})

	Describe("IdentifyPeriods", func() {
		It("finds annual and quarterly periods", func() {
			periods := IdentifyPeriods(cf)
			Expect(len(periods)).To(BeNumerically(">", 0))

			hasAnnual := false
			hasQuarterly := false
			for _, p := range periods {
				if p.FormType == "10-K" {
					hasAnnual = true
				}
				if p.FormType == "10-Q" {
					hasQuarterly = true
				}
			}
			Expect(hasAnnual).To(BeTrue())
			Expect(hasQuarterly).To(BeTrue())
		})

		It("includes AR and MR filing dates for each period", func() {
			periods := IdentifyPeriods(cf)
			for _, p := range periods {
				Expect(p.ARFiledDate.IsZero()).To(BeFalse(),
					"period %v should have an AR filing date", p.PeriodEnd)
				Expect(p.MRFiledDate.IsZero()).To(BeFalse(),
					"period %v should have an MR filing date", p.PeriodEnd)
				Expect(p.MRFiledDate).To(BeTemporally(">=", p.ARFiledDate),
					"MR filed date should be >= AR filed date")
			}
		})
	})

	Describe("NormalizeEventDate", func() {
		It("normalizes quarterly date to quarter end", func() {
			// Apple's fiscal Q1 ends late Dec
			d := NormalizeEventDate(time.Date(2018, 12, 29, 0, 0, 0, 0, time.UTC), "10-Q")
			Expect(d).To(Equal(time.Date(2018, 12, 31, 0, 0, 0, 0, time.UTC)))
		})

		It("normalizes annual date to calendar year end", func() {
			// Apple's fiscal year ends late Sep
			d := NormalizeEventDate(time.Date(2018, 9, 29, 0, 0, 0, 0, time.UTC), "10-K")
			Expect(d).To(Equal(time.Date(2018, 12, 31, 0, 0, 0, 0, time.UTC)))
		})
	})
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `ginkgo run -race ./provider/sec/`

Expected: Compilation failure.

- [ ] **Step 3: Write the implementation**

Create `provider/sec/dimensions.go`:

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

package sec

import (
	"sort"
	"time"

	"github.com/penny-vault/pvdata/data"
)

// Period represents a unique reporting period identified from CompanyFacts.
type Period struct {
	PeriodEnd   time.Time // End date of the fiscal period
	FormType    string    // "10-K" or "10-Q"
	ARFiledDate time.Time // Earliest filing date for this period (As Reported)
	MRFiledDate time.Time // Latest filing date for this period (Most Recent Reported)
}

// IdentifyPeriods scans all facts in a CompanyFacts to find unique reporting periods
// and their earliest/latest filing dates.
func IdentifyPeriods(cf *CompanyFacts) []Period {
	type periodKey struct {
		end  time.Time
		form string
	}

	periodMap := make(map[periodKey]*Period)

	for _, facts := range cf.Facts {
		for _, f := range facts {
			key := periodKey{end: f.End, form: f.Form}
			p, exists := periodMap[key]
			if !exists {
				p = &Period{
					PeriodEnd:   f.End,
					FormType:    f.Form,
					ARFiledDate: f.Filed,
					MRFiledDate: f.Filed,
				}
				periodMap[key] = p
			} else {
				if f.Filed.Before(p.ARFiledDate) {
					p.ARFiledDate = f.Filed
				}
				if f.Filed.After(p.MRFiledDate) {
					p.MRFiledDate = f.Filed
				}
			}
		}
	}

	periods := make([]Period, 0, len(periodMap))
	for _, p := range periodMap {
		periods = append(periods, *p)
	}

	sort.Slice(periods, func(i, j int) bool {
		return periods[i].PeriodEnd.Before(periods[j].PeriodEnd)
	})

	return periods
}

// NormalizeEventDate converts a raw period end date to a normalized calendar date,
// matching Sharadar's EventDate convention:
// - Quarterly: snaps to the nearest calendar quarter end (3/31, 6/30, 9/30, 12/31)
// - Annual: snaps to the calendar year end (12/31)
func NormalizeEventDate(periodEnd time.Time, formType string) time.Time {
	if formType == "10-K" {
		return time.Date(periodEnd.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
	}

	// Quarterly: snap to nearest quarter end
	month := periodEnd.Month()
	switch {
	case month <= 3:
		return time.Date(periodEnd.Year(), 3, 31, 0, 0, 0, 0, time.UTC)
	case month <= 6:
		return time.Date(periodEnd.Year(), 6, 30, 0, 0, 0, 0, time.UTC)
	case month <= 9:
		return time.Date(periodEnd.Year(), 9, 30, 0, 0, 0, 0, time.UTC)
	default:
		return time.Date(periodEnd.Year(), 12, 31, 0, 0, 0, 0, time.UTC)
	}
}

// ResolveFieldsForFiling resolves all fields using only facts filed on or before
// a specific date. This allows producing AR (earliest filed) vs MR (latest filed)
// views of the same period.
func ResolveFieldsForFiling(cf *CompanyFacts, periodEnd time.Time, formType string, filedDate time.Time) map[string]float64 {
	// Build a filtered CompanyFacts containing only facts filed on or before filedDate
	filtered := &CompanyFacts{
		CIK:        cf.CIK,
		EntityName: cf.EntityName,
		Facts:      make(map[string][]Fact),
	}

	for concept, facts := range cf.Facts {
		var kept []Fact
		for _, f := range facts {
			if !f.Filed.After(filedDate) {
				kept = append(kept, f)
			}
		}
		if len(kept) > 0 {
			filtered.Facts[concept] = kept
		}
	}

	return ResolveAllFields(filtered, periodEnd, formType)
}

// ComputeTTM computes trailing twelve month values from the 4 most recent quarterly
// resolved field sets. Flow items are summed; point-in-time items use the latest value.
func ComputeTTM(quarters []map[string]float64) map[string]float64 {
	if len(quarters) < 4 {
		return nil
	}

	// Use the 4 most recent quarters
	recent := quarters[len(quarters)-4:]
	result := make(map[string]float64)

	for _, m := range FieldMappings {
		switch m.StatementType {
		case StmtFlow:
			// Sum all 4 quarters
			sum := 0.0
			found := 0
			for _, q := range recent {
				if v, ok := q[m.FieldName]; ok {
					sum += v
					found++
				}
			}
			if found == 4 {
				result[m.FieldName] = sum
			}

		case StmtPointInTime:
			// Use the latest quarter's value
			if v, ok := recent[3][m.FieldName]; ok {
				result[m.FieldName] = v
			}

		case StmtMetric:
			// Recompute from TTM values (will be done after flow/point-in-time)
		}
	}

	// Recompute derived metrics from TTM values
	for _, m := range FieldMappings {
		if m.Type == MappingDerived && m.StatementType == StmtMetric {
			if val, ok := computeDerived(m, result); ok {
				result[m.FieldName] = val
			}
		}
	}

	return result
}

// BuildFundamental converts a resolved field map into a data.Fundamental struct.
func BuildFundamental(fields map[string]float64, ticker, compositeFigi, dimension string, eventDate, dateKey, reportPeriod time.Time) *data.Fundamental {
	f := &data.Fundamental{
		EventDate:    eventDate,
		Ticker:       ticker,
		CompositeFigi: compositeFigi,
		Dimension:    dimension,
		DateKey:      dateKey,
		ReportPeriod: reportPeriod,
		LastUpdated:  time.Now().UTC(),
	}

	// Map resolved values to Fundamental fields
	if v, ok := fields["TotalAssets"]; ok {
		f.TotalAssets = int64(v)
	}
	if v, ok := fields["CurrentAssets"]; ok {
		f.CurrentAssets = int64(v)
	}
	if v, ok := fields["AssetsNonCurrent"]; ok {
		f.AssetsNonCurrent = int64(v)
	}
	if v, ok := fields["CashAndEquivalents"]; ok {
		f.CashAndEquivalents = int64(v)
	}
	if v, ok := fields["Inventory"]; ok {
		f.Inventory = int64(v)
	}
	if v, ok := fields["Investments"]; ok {
		f.Investments = int64(v)
	}
	if v, ok := fields["InvestmentsCurrent"]; ok {
		f.InvestmentsCurrent = int64(v)
	}
	if v, ok := fields["InvestmentsNonCurrent"]; ok {
		f.InvestmentsNonCurrent = int64(v)
	}
	if v, ok := fields["Receivables"]; ok {
		f.Receivables = int64(v)
	}
	if v, ok := fields["Payables"]; ok {
		f.Payables = int64(v)
	}
	if v, ok := fields["Deposits"]; ok {
		f.Deposits = int64(v)
	}
	if v, ok := fields["PropertyPlantAndEquipmentNet"]; ok {
		f.PropertyPlantAndEquipmentNet = int64(v)
	}
	if v, ok := fields["Intangibles"]; ok {
		f.Intangibles = int64(v)
	}
	if v, ok := fields["TaxAssets"]; ok {
		f.TaxAssets = int64(v)
	}
	if v, ok := fields["TaxLiabilities"]; ok {
		f.TaxLiabilities = int64(v)
	}
	if v, ok := fields["TotalDebt"]; ok {
		f.TotalDebt = int64(v)
	}
	if v, ok := fields["DebtCurrent"]; ok {
		f.DebtCurrent = int64(v)
	}
	if v, ok := fields["DebtNonCurrent"]; ok {
		f.DebtNonCurrent = int64(v)
	}
	if v, ok := fields["DeferredRevenue"]; ok {
		f.DeferredRevenue = int64(v)
	}
	if v, ok := fields["TotalLiabilities"]; ok {
		f.TotalLiabilities = int64(v)
	}
	if v, ok := fields["CurrentLiabilities"]; ok {
		f.CurrentLiabilities = int64(v)
	}
	if v, ok := fields["LiabilitiesNonCurrent"]; ok {
		f.LiabilitiesNonCurrent = int64(v)
	}
	if v, ok := fields["Equity"]; ok {
		f.Equity = int64(v)
	}
	if v, ok := fields["AccumulatedOtherComprehensiveIncome"]; ok {
		f.AccumulatedOtherComprehensiveIncome = int64(v)
	}
	if v, ok := fields["AccumulatedRetainedEarningsDeficit"]; ok {
		f.AccumulatedRetainedEarningsDeficit = int64(v)
	}

	// Income Statement
	if v, ok := fields["Revenues"]; ok {
		f.Revenues = int64(v)
	}
	if v, ok := fields["CostOfRevenue"]; ok {
		f.CostOfRevenue = int64(v)
	}
	if v, ok := fields["GrossProfit"]; ok {
		f.GrossProfit = int64(v)
	}
	if v, ok := fields["OperatingExpenses"]; ok {
		f.OperatingExpenses = int64(v)
	}
	if v, ok := fields["SellingGeneralAndAdministrativeExpense"]; ok {
		f.SellingGeneralAndAdministrativeExpense = int64(v)
	}
	if v, ok := fields["RandDExpenses"]; ok {
		f.RandDExpenses = int64(v)
	}
	if v, ok := fields["OperatingIncome"]; ok {
		f.OperatingIncome = int64(v)
	}
	if v, ok := fields["InterestExpense"]; ok {
		f.InterestExpense = int64(v)
	}
	if v, ok := fields["IncomeTaxExpense"]; ok {
		f.IncomeTaxExpense = int64(v)
	}
	if v, ok := fields["NetIncome"]; ok {
		f.NetIncome = int64(v)
	}
	if v, ok := fields["NetIncomeCommonStock"]; ok {
		f.NetIncomeCommonStock = int64(v)
	}
	if v, ok := fields["ConsolidatedIncome"]; ok {
		f.ConsolidatedIncome = int64(v)
	}
	if v, ok := fields["NetLossIncomeDiscontinuedOperations"]; ok {
		f.NetLossIncomeDiscontinuedOperations = int64(v)
	}
	if v, ok := fields["NetIncomeToNonControllingInterests"]; ok {
		f.NetIncomeToNonControllingInterests = int64(v)
	}
	if v, ok := fields["PreferredDividendsIncomeStatementImpact"]; ok {
		f.PreferredDividendsIncomeStatementImpact = int64(v)
	}
	if v, ok := fields["EBIT"]; ok {
		f.EBIT = int64(v)
	}
	if v, ok := fields["EBITDA"]; ok {
		f.EBITDA = int64(v)
	}
	if v, ok := fields["EBT"]; ok {
		f.EBT = int64(v)
	}

	// Per-share
	if v, ok := fields["EPS"]; ok {
		f.EPS = v
	}
	if v, ok := fields["EPSDiluted"]; ok {
		f.EPSDiluted = v
	}
	if v, ok := fields["DividendsPerBasicCommonShare"]; ok {
		f.DividendsPerBasicCommonShare = v
	}

	// Share counts
	if v, ok := fields["SharesBasic"]; ok {
		f.SharesBasic = int64(v)
	}
	if v, ok := fields["WeightedAverageShares"]; ok {
		f.WeightedAverageShares = int64(v)
	}
	if v, ok := fields["WeightedAverageSharesDiluted"]; ok {
		f.WeightedAverageSharesDiluted = int64(v)
	}

	// Cash flow
	if v, ok := fields["NetCashFlowFromOperations"]; ok {
		f.NetCashFlowFromOperations = int64(v)
	}
	if v, ok := fields["NetCashFlowFromInvesting"]; ok {
		f.NetCashFlowFromInvesting = int64(v)
	}
	if v, ok := fields["NetCashFlowFromFinancing"]; ok {
		f.NetCashFlowFromFinancing = int64(v)
	}
	if v, ok := fields["DepreciationAmortizationAndAccretion"]; ok {
		f.DepreciationAmortizationAndAccretion = int64(v)
	}
	if v, ok := fields["CapitalExpenditure"]; ok {
		f.CapitalExpenditure = int64(v)
	}
	if v, ok := fields["ShareBasedCompensation"]; ok {
		f.ShareBasedCompensation = int64(v)
	}
	if v, ok := fields["NetCashFlowBusiness"]; ok {
		f.NetCashFlowBusiness = int64(v)
	}
	if v, ok := fields["NetCashFlowCommon"]; ok {
		f.NetCashFlowCommon = int64(v)
	}
	if v, ok := fields["NetCashFlowDebt"]; ok {
		f.NetCashFlowDebt = int64(v)
	}
	if v, ok := fields["NetCashFlowDividend"]; ok {
		f.NetCashFlowDividend = int64(v)
	}
	if v, ok := fields["NetCashFlowInvest"]; ok {
		f.NetCashFlowInvest = int64(v)
	}
	if v, ok := fields["NetCashFlowFx"]; ok {
		f.NetCashFlowFx = int64(v)
	}
	if v, ok := fields["NetCashFlow"]; ok {
		f.NetCashFlow = int64(v)
	}
	if v, ok := fields["FreeCashFlow"]; ok {
		f.FreeCashFlow = int64(v)
	}

	// Derived metrics
	if v, ok := fields["GrossMargin"]; ok {
		f.GrossMargin = v
	}
	if v, ok := fields["ProfitMargin"]; ok {
		f.ProfitMargin = v
	}
	if v, ok := fields["EBITDAMargin"]; ok {
		f.EBITDAMargin = v
	}
	if v, ok := fields["CurrentRatio"]; ok {
		f.CurrentRatio = v
	}
	if v, ok := fields["DebtToEquityRatio"]; ok {
		f.DebtToEquityRatio = v
	}
	if v, ok := fields["AssetTurnover"]; ok {
		f.AssetTurnover = v
	}
	if v, ok := fields["ReturnOnSales"]; ok {
		f.ReturnOnSales = v
	}
	if v, ok := fields["FreeCashFlowPerShare"]; ok {
		f.FreeCashFlowPerShare = v
	}
	if v, ok := fields["BookValuePerShare"]; ok {
		f.BookValuePerShare = v
	}
	if v, ok := fields["SalesPerShare"]; ok {
		f.SalesPerShare = v
	}
	if v, ok := fields["TangibleAssetsBookValuePerShare"]; ok {
		f.TangibleAssetsBookValuePerShare = v
	}
	if v, ok := fields["WorkingCapital"]; ok {
		f.WorkingCapital = int64(v)
	}
	if v, ok := fields["TangibleAssetValue"]; ok {
		f.TangibleAssetValue = int64(v)
	}
	if v, ok := fields["InvestedCapital"]; ok {
		f.InvestedCapital = int64(v)
	}

	return f
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `ginkgo run -race ./provider/sec/`

Expected: All specs pass.

- [ ] **Step 5: Run linter**

Run: `golangci-lint run --fix ./provider/sec/...`

- [ ] **Step 6: Commit**

```bash
git add provider/sec/dimensions.go provider/sec/dimensions_test.go
git commit -m "feat(sec): add period identification, AR/MR dimensions, TTM, and Fundamental builder"
```

---

### Task 6: CIK Resolution

Map SEC CIK identifiers to tickers and CompositeFIGIs.

**Files:**
- Create: `provider/sec/cik.go`
- Create: `provider/sec/cik_test.go`

- [ ] **Step 1: Write the failing test**

Create `provider/sec/cik_test.go`:

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

package sec

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CIK Resolution", func() {
	Describe("ParseCompanyTickers", func() {
		It("parses SEC company_tickers JSON", func() {
			// Sample from https://www.sec.gov/files/company_tickers.json
			jsonData := []byte(`{
				"0": {"cik_str": 320193, "ticker": "AAPL", "title": "Apple Inc."},
				"1": {"cik_str": 789019, "ticker": "MSFT", "title": "MICROSOFT CORP"},
				"2": {"cik_str": 1652044, "ticker": "GOOGL", "title": "Alphabet Inc."}
			}`)

			m, err := ParseCompanyTickers(jsonData)
			Expect(err).NotTo(HaveOccurred())
			Expect(m).To(HaveKey(320193))
			Expect(m[320193].Ticker).To(Equal("AAPL"))
			Expect(m[320193].Name).To(Equal("Apple Inc."))
			Expect(m).To(HaveKey(789019))
			Expect(m[789019].Ticker).To(Equal("MSFT"))
		})
	})

	Describe("FormatCIK", func() {
		It("zero-pads CIK to 10 digits", func() {
			Expect(FormatCIK(320193)).To(Equal("CIK0000320193"))
		})

		It("handles large CIKs", func() {
			Expect(FormatCIK(1652044)).To(Equal("CIK0001652044"))
		})
	})
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `ginkgo run -race ./provider/sec/`

Expected: Compilation failure.

- [ ] **Step 3: Write the implementation**

Create `provider/sec/cik.go`:

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
```

- [ ] **Step 4: Run test to verify it passes**

Run: `ginkgo run -race ./provider/sec/`

Expected: All specs pass.

- [ ] **Step 5: Run linter**

Run: `golangci-lint run --fix ./provider/sec/...`

- [ ] **Step 6: Commit**

```bash
git add provider/sec/cik.go provider/sec/cik_test.go
git commit -m "feat(sec): add CIK resolution (company_tickers parser, DB lookup, formatting)"
```

---

### Task 7: RSS Feed Parser

Parse the SEC XBRL RSS feed to discover new 10-K/10-Q filings for incremental updates.

**Files:**
- Create: `provider/sec/rss.go`
- Create: `provider/sec/rss_test.go`
- Create: `provider/sec/testdata/rss_sample.xml`

- [ ] **Step 1: Create RSS test fixture**

Save a sample of the SEC XBRL RSS feed to `provider/sec/testdata/rss_sample.xml`. Fetch a sample via:
```bash
curl -s -H "User-Agent: pvdata/1.0 test@example.com" \
  "https://www.sec.gov/cgi-bin/browse-edgar?action=getcurrent&type=10-K&dateb=&owner=include&count=5&search_text=&start=0&output=atom" \
  > provider/sec/testdata/rss_sample.xml
```

If the ATOM feed structure isn't suitable, use the EDGAR RSS at `https://www.sec.gov/Archives/edgar/xbrl-rss.xml` instead. Trim to 3-5 entries covering 10-K and 10-Q filings.

- [ ] **Step 2: Write the failing test**

Create `provider/sec/rss_test.go`:

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

package sec

import (
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RSS Feed", func() {
	Describe("ParseFilingFeed", func() {
		It("extracts CIKs and form types from EDGAR feed", func() {
			xmlData, err := os.ReadFile("testdata/rss_sample.xml")
			Expect(err).NotTo(HaveOccurred())

			filings, err := ParseFilingFeed(xmlData)
			Expect(err).NotTo(HaveOccurred())
			Expect(len(filings)).To(BeNumerically(">", 0))

			for _, f := range filings {
				Expect(f.CIK).To(BeNumerically(">", 0))
				Expect(f.FormType).To(BeElementOf("10-K", "10-Q"))
			}
		})
	})
})
```

- [ ] **Step 3: Run test to verify it fails**

Run: `ginkgo run -race ./provider/sec/`

Expected: Compilation failure.

- [ ] **Step 4: Write the implementation**

Create `provider/sec/rss.go`:

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

package sec

import (
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog/log"
)

// FilingEntry represents a single filing discovered from the RSS feed.
type FilingEntry struct {
	CIK       int
	FormType  string
	Filed     time.Time
	AccnNum   string
	CompanyName string
}

// edgarFeed represents the EDGAR ATOM feed structure.
type edgarFeed struct {
	XMLName xml.Name    `xml:"feed"`
	Entries []feedEntry `xml:"entry"`
}

type feedEntry struct {
	Title   string      `xml:"title"`
	Updated string      `xml:"updated"`
	Link    feedLink    `xml:"link"`
	Summary feedSummary `xml:"summary"`
	Category feedCategory `xml:"category"`
}

type feedLink struct {
	Href string `xml:"href,attr"`
}

type feedSummary struct {
	Content string `xml:",chardata"`
}

type feedCategory struct {
	Term  string `xml:"term,attr"`
	Label string `xml:"label,attr"`
}

// ParseFilingFeed parses an EDGAR ATOM feed and returns filing entries filtered
// to only 10-K and 10-Q form types.
func ParseFilingFeed(xmlData []byte) ([]FilingEntry, error) {
	var feed edgarFeed
	if err := xml.Unmarshal(xmlData, &feed); err != nil {
		return nil, fmt.Errorf("parsing EDGAR feed XML: %w", err)
	}

	var filings []FilingEntry
	for _, entry := range feed.Entries {
		formType := entry.Category.Term
		if formType != "10-K" && formType != "10-Q" {
			continue
		}

		cik := extractCIKFromLink(entry.Link.Href)
		if cik == 0 {
			log.Warn().Str("link", entry.Link.Href).Msg("could not extract CIK from feed entry link")
			continue
		}

		filed, _ := time.Parse(time.RFC3339, entry.Updated)

		filings = append(filings, FilingEntry{
			CIK:         cik,
			FormType:    formType,
			Filed:       filed,
			CompanyName: entry.Title,
		})
	}

	return filings, nil
}

// extractCIKFromLink extracts a CIK number from an EDGAR URL path.
// EDGAR URLs contain the CIK as a path component, e.g.:
// https://www.sec.gov/Archives/edgar/data/320193/...
func extractCIKFromLink(href string) int {
	parts := strings.Split(href, "/")
	for i, p := range parts {
		if p == "data" && i+1 < len(parts) {
			cik, err := strconv.Atoi(parts[i+1])
			if err == nil {
				return cik
			}
		}
	}
	return 0
}
```

- [ ] **Step 5: Run test to verify it passes**

Run: `ginkgo run -race ./provider/sec/`

Expected: All specs pass. (Note: The test fixture needs to actually exist and contain valid ATOM XML with category terms "10-K" or "10-Q". If the EDGAR ATOM feed structure differs from what's defined above, adjust the XML structs to match the actual feed format. The test fixture should be validated before running.)

- [ ] **Step 6: Run linter**

Run: `golangci-lint run --fix ./provider/sec/...`

- [ ] **Step 7: Commit**

```bash
git add provider/sec/rss.go provider/sec/rss_test.go provider/sec/testdata/rss_sample.xml
git commit -m "feat(sec): add XBRL RSS feed parser for filing discovery"
```

---

### Task 8: Fetch Function -- Wire Everything Together

Implement the full `fetchFundamentals` function that orchestrates backfill and incremental updates.

**Files:**
- Modify: `provider/sec/sec.go` (replace stub `fetchFundamentals`)
- Modify: `provider/sec/companyfacts.go` (add HTTP fetch + bulk zip download functions)

- [ ] **Step 1: Add HTTP client and bulk download functions to companyfacts.go**

Add to `provider/sec/companyfacts.go`:

```go
import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/go-resty/resty/v2"
	"github.com/rs/zerolog/log"
	"golang.org/x/time/rate"
)

const (
	companyFactsURL    = "https://data.sec.gov/api/xbrl/companyfacts/"
	companyFactsZipURL = "https://www.sec.gov/Archives/edgar/daily-index/xbrl/companyfacts.zip"
	companyTickersURL  = "https://www.sec.gov/files/company_tickers.json"
	edgarFeedURL       = "https://www.sec.gov/cgi-bin/browse-edgar?action=getcurrent&type=10-K%2C10-Q&dateb=&owner=include&count=100&search_text=&start=0&output=atom"
)

// FetchCompanyFacts downloads the companyfacts JSON for a single CIK from SEC EDGAR.
func FetchCompanyFacts(ctx context.Context, client *resty.Client, cik int) (*CompanyFacts, error) {
	url := companyFactsURL + FormatCIK(cik) + ".json"

	resp, err := client.R().
		SetContext(ctx).
		Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetching companyfacts for CIK %d: %w", cik, err)
	}

	if resp.StatusCode() != http.StatusOK {
		return nil, fmt.Errorf("SEC returned status %d for CIK %d", resp.StatusCode(), cik)
	}

	return ParseCompanyFacts(resp.Body())
}

// DownloadCompanyFactsZip downloads and extracts the bulk companyfacts.zip file,
// calling processFn for each individual CIK JSON file. This streams the zip
// rather than writing to disk.
func DownloadCompanyFactsZip(ctx context.Context, client *resty.Client, processFn func(cik int, jsonData []byte) error) error {
	log.Info().Msg("downloading companyfacts.zip from SEC (this may take several minutes)")

	resp, err := client.R().
		SetContext(ctx).
		Get(companyFactsZipURL)
	if err != nil {
		return fmt.Errorf("downloading companyfacts.zip: %w", err)
	}

	if resp.StatusCode() != http.StatusOK {
		return fmt.Errorf("SEC returned status %d for companyfacts.zip", resp.StatusCode())
	}

	body := resp.Body()
	reader, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return fmt.Errorf("opening companyfacts.zip: %w", err)
	}

	for _, f := range reader.File {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if filepath.Ext(f.Name) != ".json" {
			continue
		}

		// Extract CIK from filename (e.g., "CIK0000320193.json")
		base := strings.TrimSuffix(filepath.Base(f.Name), ".json")
		base = strings.TrimPrefix(base, "CIK")
		cik, err := strconv.Atoi(base)
		if err != nil {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			log.Warn().Err(err).Str("file", f.Name).Msg("error opening file in zip")
			continue
		}

		jsonData, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			log.Warn().Err(err).Str("file", f.Name).Msg("error reading file in zip")
			continue
		}

		if err := processFn(cik, jsonData); err != nil {
			log.Warn().Err(err).Int("cik", cik).Msg("error processing companyfacts")
		}
	}

	return nil
}

// NewSECClient creates a resty HTTP client configured for SEC EDGAR API access.
func NewSECClient(userAgent string, limiter *rate.Limiter) *resty.Client {
	client := resty.New().
		SetHeader("User-Agent", userAgent).
		SetHeader("Accept", "application/json").
		SetTimeout(60 * time.Second).
		SetRetryCount(3).
		SetRetryWaitTime(5 * time.Second).
		AddRetryCondition(func(r *resty.Response, err error) bool {
			return r != nil && (r.StatusCode() == http.StatusTooManyRequests || r.StatusCode() >= 500)
		}).
		OnBeforeRequest(func(c *resty.Client, r *resty.Request) error {
			return limiter.Wait(r.Context())
		})

	return client
}
```

- [ ] **Step 2: Implement fetchFundamentals in sec.go**

Replace the stub `fetchFundamentals` in `provider/sec/sec.go`:

```go
func fetchFundamentals(ctx context.Context, sub *library.Subscription, out chan<- *data.Observation, exit chan<- data.RunSummary) {
	defer func() { exit <- data.RunSummary{} }()

	userAgent := sub.Config["userAgent"]
	if userAgent == "" {
		log.Error().Msg("SEC provider requires userAgent config")
		return
	}

	reqPerSec := 10
	if rateStr, ok := sub.Config["rateLimit"]; ok {
		if r, err := strconv.Atoi(rateStr); err == nil && r > 0 {
			reqPerSec = r
		}
	}

	limiter := rate.NewLimiter(rate.Limit(reqPerSec), 1)
	client := NewSECClient(userAgent, limiter)

	// Load CIK -> asset map from database
	cikMap, err := LoadCIKMapFromDB(ctx, sub.Library.Pool)
	if err != nil {
		log.Error().Err(err).Msg("error loading CIK map from database")
		return
	}

	log.Info().Int("known_ciks", len(cikMap)).Msg("loaded CIK map from database")

	isBackfill := sub.LastObsDate.IsZero()
	if isBackfill {
		runBackfill(ctx, client, cikMap, out)
	} else {
		runIncremental(ctx, client, limiter, cikMap, sub.LastObsDate, out)
	}
}

func runBackfill(ctx context.Context, client *resty.Client, cikMap map[int]AssetInfo, out chan<- *data.Observation) {
	processed := 0
	err := DownloadCompanyFactsZip(ctx, client, func(cik int, jsonData []byte) error {
		asset, ok := cikMap[cik]
		if !ok {
			return nil // Unknown company, skip
		}

		cf, err := ParseCompanyFacts(jsonData)
		if err != nil {
			return err
		}

		emitFundamentals(cf, asset, out)

		processed++
		if processed%1000 == 0 {
			log.Info().Int("processed", processed).Msg("backfill progress")
		}

		return nil
	})
	if err != nil {
		log.Error().Err(err).Msg("error during backfill")
	}

	log.Info().Int("total_processed", processed).Msg("backfill complete")
}

func runIncremental(ctx context.Context, client *resty.Client, limiter *rate.Limiter, cikMap map[int]AssetInfo, since time.Time, out chan<- *data.Observation) {
	// Fetch recent filings from EDGAR feed
	resp, err := client.R().SetContext(ctx).Get(edgarFeedURL)
	if err != nil {
		log.Error().Err(err).Msg("error fetching EDGAR filing feed")
		return
	}

	filings, err := ParseFilingFeed(resp.Body())
	if err != nil {
		log.Error().Err(err).Msg("error parsing EDGAR filing feed")
		return
	}

	// Deduplicate CIKs and filter to known assets
	seen := make(map[int]bool)
	for _, filing := range filings {
		if filing.Filed.Before(since) || seen[filing.CIK] {
			continue
		}
		seen[filing.CIK] = true

		asset, ok := cikMap[filing.CIK]
		if !ok {
			continue
		}

		cf, err := FetchCompanyFacts(ctx, client, filing.CIK)
		if err != nil {
			log.Warn().Err(err).Int("cik", filing.CIK).Msg("error fetching companyfacts")
			continue
		}

		emitFundamentals(cf, asset, out)
	}
}

// emitFundamentals processes a CompanyFacts into data.Fundamental observations
// for all 6 dimensions and sends them to the output channel.
func emitFundamentals(cf *CompanyFacts, asset AssetInfo, out chan<- *data.Observation) {
	periods := IdentifyPeriods(cf)

	// Collect quarterly periods for TTM computation, keyed by normalized event date
	type quarterData struct {
		period   Period
		arFields map[string]float64
		mrFields map[string]float64
	}
	var quarters []quarterData

	for _, p := range periods {
		eventDate := NormalizeEventDate(p.PeriodEnd, p.FormType)

		// AR: resolve using only facts available at the earliest filing date
		arFields := ResolveFieldsForFiling(cf, p.PeriodEnd, p.FormType, p.ARFiledDate)
		// MR: resolve using all facts including restatements
		mrFields := ResolveFieldsForFiling(cf, p.PeriodEnd, p.FormType, p.MRFiledDate)

		if p.FormType == "10-Q" {
			quarters = append(quarters, quarterData{period: p, arFields: arFields, mrFields: mrFields})

			// ARQ
			fundamental := BuildFundamental(arFields, asset.Ticker, asset.CompositeFigi, "ARQ",
				eventDate, p.ARFiledDate, p.PeriodEnd)
			out <- &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  eventDate,
				SubscriptionName: "SEC",
			}

			// MRQ
			fundamental = BuildFundamental(mrFields, asset.Ticker, asset.CompositeFigi, "MRQ",
				eventDate, p.PeriodEnd, p.PeriodEnd)
			out <- &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  eventDate,
				SubscriptionName: "SEC",
			}
		}

		if p.FormType == "10-K" {
			// ARY
			fundamental := BuildFundamental(arFields, asset.Ticker, asset.CompositeFigi, "ARY",
				eventDate, p.ARFiledDate, p.PeriodEnd)
			out <- &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  eventDate,
				SubscriptionName: "SEC",
			}

			// MRY
			fundamental = BuildFundamental(mrFields, asset.Ticker, asset.CompositeFigi, "MRY",
				eventDate, p.PeriodEnd, p.PeriodEnd)
			out <- &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  eventDate,
				SubscriptionName: "SEC",
			}
		}
	}

	// Compute TTM for each quarter that has 4 preceding quarters
	for i := 3; i < len(quarters); i++ {
		q := quarters[i]
		eventDate := NormalizeEventDate(q.period.PeriodEnd, q.period.FormType)

		// ART
		arQSlice := make([]map[string]float64, 4)
		for j := 0; j < 4; j++ {
			arQSlice[j] = quarters[i-3+j].arFields
		}
		if ttm := ComputeTTM(arQSlice); ttm != nil {
			fundamental := BuildFundamental(ttm, asset.Ticker, asset.CompositeFigi, "ART",
				eventDate, q.period.ARFiledDate, q.period.PeriodEnd)
			out <- &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  eventDate,
				SubscriptionName: "SEC",
			}
		}

		// MRT
		mrQSlice := make([]map[string]float64, 4)
		for j := 0; j < 4; j++ {
			mrQSlice[j] = quarters[i-3+j].mrFields
		}
		if ttm := ComputeTTM(mrQSlice); ttm != nil {
			fundamental := BuildFundamental(ttm, asset.Ticker, asset.CompositeFigi, "MRT",
				eventDate, q.period.PeriodEnd, q.period.PeriodEnd)
			out <- &data.Observation{
				Fundamental:      fundamental,
				ObservationDate:  eventDate,
				SubscriptionName: "SEC",
			}
		}
	}
}
```

Add necessary imports to `sec.go`: `strconv`, `time`, `rate`, `resty`, `log`.

- [ ] **Step 3: Run linter**

Run: `golangci-lint run --fix ./provider/sec/...`

- [ ] **Step 4: Run all unit tests**

Run: `ginkgo run -race ./provider/sec/`

Expected: All specs pass.

- [ ] **Step 5: Commit**

```bash
git add provider/sec/sec.go provider/sec/companyfacts.go
git commit -m "feat(sec): implement fetchFundamentals with backfill and incremental paths"
```

---

### Task 9: Integration Test with Real Data

Validate the full pipeline against Sharadar data for a known company.

**Files:**
- Create: `provider/sec/integration_test.go`

**Context:** This test fetches real data from SEC EDGAR for Apple and verifies the extracted fundamentals are reasonable. Tagged as integration test.

- [ ] **Step 1: Write the integration test**

Create `provider/sec/integration_test.go`:

```go
//go:build integration

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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/time/rate"
)

var _ = Describe("Integration", func() {
	Describe("Apple fundamentals from SEC EDGAR", func() {
		var cf *CompanyFacts

		BeforeEach(func() {
			limiter := rate.NewLimiter(rate.Limit(10), 1)
			client := NewSECClient("pvdata/1.0 integration-test@example.com", limiter)

			var err error
			cf, err = FetchCompanyFacts(context.Background(), client, 320193) // Apple
			Expect(err).NotTo(HaveOccurred())
			Expect(cf.EntityName).To(Equal("Apple Inc."))
		})

		It("resolves revenue for FY2023 10-K", func() {
			// Apple FY2023 ended Sep 30, 2023
			periodEnd := time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC)
			fields := ResolveAllFields(cf, periodEnd, "10-K")

			rev, ok := fields["Revenues"]
			Expect(ok).To(BeTrue())
			// Apple FY2023 revenue was ~$383B
			Expect(rev).To(BeNumerically("~", 383285000000, 5000000000))
		})

		It("resolves total assets for FY2023 10-K", func() {
			periodEnd := time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC)
			fields := ResolveAllFields(cf, periodEnd, "10-K")

			assets, ok := fields["TotalAssets"]
			Expect(ok).To(BeTrue())
			// Apple FY2023 total assets were ~$352B
			Expect(assets).To(BeNumerically("~", 352583000000, 10000000000))
		})

		It("computes EBITDA for FY2023", func() {
			periodEnd := time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC)
			fields := ResolveAllFields(cf, periodEnd, "10-K")

			ebitda, ok := fields["EBITDA"]
			Expect(ok).To(BeTrue())
			// Apple FY2023 EBITDA was ~$125-130B
			Expect(ebitda).To(BeNumerically(">", 100000000000))
		})

		It("identifies multiple periods", func() {
			periods := IdentifyPeriods(cf)
			Expect(len(periods)).To(BeNumerically(">", 20))
		})

		It("produces fundamentals for all 6 dimensions", func() {
			asset := AssetInfo{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", CIK: 320193}
			observations := make(chan *data.Observation, 1000)

			go func() {
				emitFundamentals(cf, asset, observations)
				close(observations)
			}()

			dimensions := make(map[string]int)
			for obs := range observations {
				dimensions[obs.Fundamental.Dimension]++
			}

			Expect(dimensions).To(HaveKey("ARQ"))
			Expect(dimensions).To(HaveKey("MRQ"))
			Expect(dimensions).To(HaveKey("ARY"))
			Expect(dimensions).To(HaveKey("MRY"))
			Expect(dimensions).To(HaveKey("ART"))
			Expect(dimensions).To(HaveKey("MRT"))
		})
	})
})
```

Add `"github.com/penny-vault/pvdata/data"` to the imports.

- [ ] **Step 2: Run integration test**

Run: `ginkgo run -race --tags=integration ./provider/sec/`

Expected: All integration specs pass.

- [ ] **Step 3: Commit**

```bash
git add provider/sec/integration_test.go
git commit -m "test(sec): add integration test validating Apple fundamentals against known values"
```

---

## Post-Implementation Notes

**Fields intentionally omitted** (require market price data not available from SEC filings):
- MarketCapitalization, EnterpriseValue, PE, PB, PS, PE1, PS1, Price
- EVtoEBIT, EVtoEBITDA, DividendYield, PayoutRatio, ShareFactor, FxUSD
- AverageAssets, EquityAvg, InvestedCapitalAverage (need prior period averaging)
- ROA, ROE, ROIC (need average values)

These can be added in a follow-up by computing them from fundamentals + EOD price data.

**Mapping expansion**: To add Zacks-level fields, add entries to `FieldMappings` in `mapping_config.go` and corresponding fields to `BuildFundamental` in `dimensions.go`. The mapping engine requires no changes.

**DERA consistency checks**: Future work -- download DERA bulk files and compare extracted values against our SEC provider output for automated regression testing.
