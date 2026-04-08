# pvindex Tradable Universe Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a new `provider/pvindex/` package that computes a daily-recomputed investable universe of US-listed common stocks (`us-tradable`) by reading from canonical published views (`eod`, `metrics`, `assets`) and emitting `IndexSnapshot` and `IndexChange` observations using the existing index data types.

**Architecture:** Derived provider — reads from the canonical published views rather than fetching externally. Filter chain (asset master → rolling stats → percentile baseline → data availability → share-class dedup → percentile filter → liquidity → price → cap-weight). Backfill runs in 63-trading-day chunks with one bulk EOD/metric query per chunk and an in-memory rolling-window median per FIGI. Annual snapshots + daily changelog with relative weight-change threshold.

**Tech Stack:** Go, jackc/pgx/v5 (Postgres), Ginkgo v2 + Gomega (testing), zerolog (logging). Reuses existing `provider/index_helpers.go` (with one new helper variant).

**Spec:** `docs/superpowers/specs/2026-04-07-pvindex-tradable-universe-design.md`

---

## File Structure

```
provider/pvindex/
  pvindex.go              # Provider struct, init(), Name/Description/ConfigDescription, Datasets, Fetch entry point
  date_range.go           # computeDateRange (queries eod and metrics views)
  loader.go               # Database loaders for assets, eod, metrics chunks
  filter.go               # Pure filter functions (LP suffix, asset master, contiguity, share-class dedup, percentile, liquidity, price, cap-weight)
  rolling.go              # Rolling-window median dollar volume computation
  chunk.go                # Per-chunk processor: orchestrates day-by-day universe computation and observation emission
  pvindex_suite_test.go   # Ginkgo suite registration
  pvindex_test.go         # Provider interface tests
  filter_test.go          # Filter function unit tests
  rolling_test.go         # Rolling median unit tests
  chunk_test.go           # Chunk processor unit tests (with synthetic in-memory data)
  integration_test.go     # Build tag `integration` — DB-backed end-to-end test
  testdata/
    universe_2023_jan.golden.json  # Expected universe constituents for golden integration test
```

Modified files:
- `provider/index_helpers.go` — add `DiffSnapshotsWithThreshold` and `DiffOptions` type
- `provider/index_helpers_test.go` — add tests for the new helper
- `cmd/providers_register.go` — add blank import for pvindex

---

### Task 1: Helper — DiffSnapshotsWithThreshold

Add a relative-threshold variant of `DiffSnapshots` to `provider/index_helpers.go`. Existing `DiffSnapshots` is unchanged. The new function takes a `DiffOptions` struct with absolute and relative thresholds; a weight is considered changed when `|delta| >= max(absoluteThreshold, prev.Weight * relativeThreshold)`.

**Files:**
- Modify: `provider/index_helpers.go` (after line 98, the existing `DiffSnapshots` function)
- Modify: `provider/index_helpers_test.go` (after the existing `diffSnapshots` Describe block)

- [ ] **Step 1: Write the failing tests**

Append to `provider/index_helpers_test.go`:

```go
var _ = Describe("diffSnapshotsWithThreshold", func() {
	It("matches DiffSnapshots when only AbsoluteThreshold is set", func() {
		current := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
			"MSFT": {CompositeFigi: "BBG000BPH459", Weight: 0.06},
		}
		previous := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
			"MSFT": {CompositeFigi: "BBG000BPH459", Weight: 0.04},
		}
		adds, removes, weightChanges := DiffSnapshotsWithThreshold(current, previous, DiffOptions{AbsoluteThreshold: 0.01})
		Expect(adds).To(BeEmpty())
		Expect(removes).To(BeEmpty())
		Expect(weightChanges).To(HaveLen(1))
		Expect(weightChanges).To(HaveKey("MSFT"))
	})

	It("uses relative threshold when set", func() {
		// AAPL prev=0.0003, current=0.00040. delta=0.0001, prev*0.25 = 0.000075
		// 0.0001 >= 0.000075 -> CHANGED
		// MSFT prev=0.0003, current=0.00031. delta=0.00001, prev*0.25 = 0.000075
		// 0.00001 < 0.000075 -> NOT changed
		current := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.00040},
			"MSFT": {CompositeFigi: "BBG000BPH459", Weight: 0.00031},
		}
		previous := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.00030},
			"MSFT": {CompositeFigi: "BBG000BPH459", Weight: 0.00030},
		}
		adds, removes, weightChanges := DiffSnapshotsWithThreshold(current, previous, DiffOptions{RelativeThreshold: 0.25})
		Expect(adds).To(BeEmpty())
		Expect(removes).To(BeEmpty())
		Expect(weightChanges).To(HaveLen(1))
		Expect(weightChanges).To(HaveKey("AAPL"))
		Expect(weightChanges).NotTo(HaveKey("MSFT"))
	})

	It("uses max(absolute, prev*relative) when both are set", func() {
		// prev=0.10, current=0.108. delta=0.008.
		// abs=0.01, prev*rel=0.10*0.25=0.025. max=0.025. 0.008 < 0.025 -> NOT changed.
		current := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.108},
		}
		previous := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.10},
		}
		_, _, weightChanges := DiffSnapshotsWithThreshold(current, previous, DiffOptions{AbsoluteThreshold: 0.01, RelativeThreshold: 0.25})
		Expect(weightChanges).To(BeEmpty())
	})

	It("detects adds and removes regardless of threshold mode", func() {
		current := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
			"NEW1": {CompositeFigi: "BBG000NEW001", Weight: 0.01},
		}
		previous := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
			"OLD1": {CompositeFigi: "BBG000OLD001", Weight: 0.02},
		}
		adds, removes, _ := DiffSnapshotsWithThreshold(current, previous, DiffOptions{RelativeThreshold: 0.25})
		Expect(adds).To(HaveKey("NEW1"))
		Expect(removes).To(HaveKey("OLD1"))
	})

	It("treats prev.Weight=0 as falling back to absolute threshold", func() {
		// prev weight is 0, so prev*rel = 0. max(abs, 0) = abs. delta must clear absolute.
		current := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.005},
		}
		previous := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.0},
		}
		_, _, weightChanges := DiffSnapshotsWithThreshold(current, previous, DiffOptions{AbsoluteThreshold: 0.01, RelativeThreshold: 0.25})
		Expect(weightChanges).To(BeEmpty())
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race --focus "diffSnapshotsWithThreshold" ./provider/`
Expected: FAIL with "undefined: DiffSnapshotsWithThreshold" / "undefined: DiffOptions"

- [ ] **Step 3: Add the implementation**

In `provider/index_helpers.go`, immediately after the existing `DiffSnapshots` function (right before `// LastSnapshotDate` on line ~100), add:

```go
// DiffOptions configures DiffSnapshotsWithThreshold weight-change detection.
// A weight is considered changed when |delta| >= max(AbsoluteThreshold, prev.Weight * RelativeThreshold).
// If RelativeThreshold is 0, only the absolute threshold applies.
type DiffOptions struct {
	AbsoluteThreshold float64 // absolute weight delta required (e.g., 0.01)
	RelativeThreshold float64 // fraction of previous weight (e.g., 0.25 = 25%)
}

// DiffSnapshotsWithThreshold compares current holdings against previous holdings using
// configurable thresholds for weight-change detection. Adds and removes are reported
// regardless of threshold settings.
func DiffSnapshotsWithThreshold(current, previous map[string]IndexMember, opts DiffOptions) (added, removed, weightChanged map[string]IndexMember) {
	added = make(map[string]IndexMember)
	removed = make(map[string]IndexMember)
	weightChanged = make(map[string]IndexMember)

	for ticker, member := range current {
		prev, ok := previous[ticker]
		if !ok {
			added[ticker] = member
			continue
		}

		delta := member.Weight - prev.Weight
		if delta < 0 {
			delta = -delta
		}

		threshold := opts.AbsoluteThreshold

		relTh := prev.Weight * opts.RelativeThreshold
		if relTh > threshold {
			threshold = relTh
		}

		if delta >= threshold-1e-12 && delta > 0 {
			// Note: we keep the legacy >= behavior of DiffSnapshots; the -1e-12 avoids
			// strict floating-point edge cases when threshold == 0 + delta == 0.
			weightChanged[ticker] = member
		}
	}

	for ticker, member := range previous {
		if _, ok := current[ticker]; !ok {
			removed[ticker] = member
		}
	}

	return
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `ginkgo run -race --focus "diffSnapshotsWithThreshold" ./provider/`
Expected: PASS for all 5 specs.

Also rerun the existing diffSnapshots tests to confirm no regression:
Run: `ginkgo run -race --focus "diffSnapshots" ./provider/`
Expected: PASS, no failures.

- [ ] **Step 5: Commit**

```bash
git add provider/index_helpers.go provider/index_helpers_test.go
git commit -m "feat(provider): add DiffSnapshotsWithThreshold for relative weight thresholds"
```

---

### Task 2: Package Skeleton + Suite + Provider Registration

Create the empty package, Ginkgo suite, and a stub Provider implementation that registers itself. This task does not yet implement Fetch — it returns an empty dataset map for now so the provider can compile and register.

**Files:**
- Create: `provider/pvindex/pvindex.go`
- Create: `provider/pvindex/pvindex_suite_test.go`
- Create: `provider/pvindex/pvindex_test.go`
- Modify: `cmd/providers_register.go`

- [ ] **Step 1: Write the failing tests**

Create `provider/pvindex/pvindex_suite_test.go`:

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
package pvindex

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestPvindex(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "pvindex Suite")
}
```

Create `provider/pvindex/pvindex_test.go`:

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
package pvindex

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pvdata/provider"
)

var _ = Describe("pvindex Provider", func() {
	It("registers itself in the provider map", func() {
		p, ok := provider.Map["pvindex"]
		Expect(ok).To(BeTrue())
		Expect(p.Name()).To(Equal("pvindex"))
	})

	It("returns a non-empty description", func() {
		p := provider.Map["pvindex"]
		Expect(p.Description()).NotTo(BeEmpty())
	})

	It("declares the US Tradable Universe dataset", func() {
		p := provider.Map["pvindex"]
		datasets := p.Datasets()
		Expect(datasets).To(HaveKey("US Tradable Universe"))
	})

	It("US Tradable Universe dataset emits IndexSnapshot and IndexChangelog data types", func() {
		p := provider.Map["pvindex"]
		ds := p.Datasets()["US Tradable Universe"]
		Expect(ds.DataTypes).To(HaveLen(2))
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race ./provider/pvindex/`
Expected: FAIL — package does not exist / no Go files in directory.

- [ ] **Step 3: Create the package skeleton**

Create `provider/pvindex/pvindex.go`:

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
package pvindex

import (
	"context"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
)

// Pvindex is a derived provider that computes investable universes by reading from
// the canonical published views (eod, metrics, assets) rather than fetching from an
// external source.
type Pvindex struct{}

func init() {
	provider.Register("pvindex", &Pvindex{})
}

func (p *Pvindex) Name() string {
	return "pvindex"
}

func (p *Pvindex) Description() string {
	return "Derived index provider that computes investable universes from canonical EOD, metric, and asset views."
}

func (p *Pvindex) ConfigDescription() map[string]string {
	return map[string]string{
		"index_ticker":        "Optional. Override the index ticker (default: us-tradable).",
		"start_date_override": "Optional. Force a later start date in YYYY-MM-DD format. Used for testing or selective backfill.",
		"chunk_size_days":     "Optional. Number of trading days per processing chunk (default: 63).",
	}
}

func (p *Pvindex) Datasets() map[string]provider.Dataset {
	return map[string]provider.Dataset{
		"US Tradable Universe": {
			Name:        "US Tradable Universe",
			Description: "Daily-recomputed investable universe of US common stocks: structural + liquidity + size + price filters with annual cap-weighted snapshots.",
			DataTypes: []*data.DataType{
				data.DataTypes[data.IndexSnapshotKey],
				data.DataTypes[data.IndexChangelogKey],
			},
			DateRange: func() (time.Time, time.Time) {
				// Stub: real implementation lands in Task 4.
				return time.Date(1999, 4, 30, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			TTL:   0,
			Fetch: fetchTradableUniverse,
		},
	}
}

// fetchTradableUniverse is the dataset Fetch entry point. Real implementation lands in Task 12.
func fetchTradableUniverse(ctx context.Context, sub *library.Subscription, out chan<- *data.Observation, exit chan<- data.RunSummary) {
	exit <- data.RunSummary{
		StartTime:        time.Now(),
		EndTime:          time.Now(),
		SubscriptionID:   sub.ID,
		SubscriptionName: sub.Name,
		Status:           data.RunSuccess,
	}
}
```

- [ ] **Step 4: Register the package via blank import**

Modify `cmd/providers_register.go`. Current contents around line 19:

```go
	_ "github.com/penny-vault/pvdata/provider/fred"
	_ "github.com/penny-vault/pvdata/provider/ishares"
	_ "github.com/penny-vault/pvdata/provider/legacy"
	_ "github.com/penny-vault/pvdata/provider/massive"
	_ "github.com/penny-vault/pvdata/provider/nasdaq"
	_ "github.com/penny-vault/pvdata/provider/sharadar"
	_ "github.com/penny-vault/pvdata/provider/tiingo"
	_ "github.com/penny-vault/pvdata/provider/tradingview"
	_ "github.com/penny-vault/pvdata/provider/zacks"
```

Insert `_ "github.com/penny-vault/pvdata/provider/pvindex"` in alphabetical order (after `nasdaq`, before `sharadar`):

```go
	_ "github.com/penny-vault/pvdata/provider/fred"
	_ "github.com/penny-vault/pvdata/provider/ishares"
	_ "github.com/penny-vault/pvdata/provider/legacy"
	_ "github.com/penny-vault/pvdata/provider/massive"
	_ "github.com/penny-vault/pvdata/provider/nasdaq"
	_ "github.com/penny-vault/pvdata/provider/pvindex"
	_ "github.com/penny-vault/pvdata/provider/sharadar"
	_ "github.com/penny-vault/pvdata/provider/tiingo"
	_ "github.com/penny-vault/pvdata/provider/tradingview"
	_ "github.com/penny-vault/pvdata/provider/zacks"
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `ginkgo run -race ./provider/pvindex/`
Expected: PASS for all 4 specs.

Also build the full binary to ensure registration didn't break anything:
Run: `go build ./...`
Expected: clean build.

- [ ] **Step 6: Commit**

```bash
git add provider/pvindex/pvindex.go provider/pvindex/pvindex_suite_test.go provider/pvindex/pvindex_test.go cmd/providers_register.go
git commit -m "feat(pvindex): add provider skeleton with stub dataset"
```

---

### Task 3: LP Suffix Matcher

Pure function that returns true if a stock name ends in an LP suffix. Used by the asset master filter.

**Files:**
- Create: `provider/pvindex/filter.go`
- Create: `provider/pvindex/filter_test.go`

- [ ] **Step 1: Write the failing tests**

Create `provider/pvindex/filter_test.go`:

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
package pvindex

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("isLPName", func() {
	DescribeTable("LP suffix detection",
		func(name string, expected bool) {
			Expect(isLPName(name)).To(Equal(expected))
		},
		Entry("Enterprise Products LP", "Enterprise Products Partners LP", true),
		Entry("Energy Transfer LP", "Energy Transfer LP", true),
		Entry("MPLX LP", "MPLX LP", true),
		Entry("Brookfield Infrastructure L.P.", "Brookfield Infrastructure L.P.", true),
		Entry("L.P. with trailing whitespace", "Some Partnership L.P.   ", true),
		Entry("LLP suffix", "Big Law LLP", true),
		Entry("Limited Partnership full word", "Foo Limited Partnership", true),
		Entry("L P with space", "ABC Investments L P", true),
		Entry("lower case lp", "enterprise products lp", true),
		Entry("Marsh & McLennan Companies (negative - not LP)", "Marsh & McLennan Companies", false),
		Entry("Apple Inc (negative)", "Apple Inc.", false),
		Entry("MetLife Inc (negative)", "MetLife Inc", false),
		Entry("LP in middle of name (negative)", "LP Holdings Corporation", false),
		Entry("Compass Plc (negative - similar shape)", "Compass Group plc", false),
		Entry("Empty name (negative)", "", false),
	)
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race --focus "isLPName" ./provider/pvindex/`
Expected: FAIL with "undefined: isLPName".

- [ ] **Step 3: Implement isLPName**

Create `provider/pvindex/filter.go`:

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
package pvindex

import (
	"strings"
)

// lpSuffixes is the list of legal-form suffixes that identify a limited partnership.
// Matched as case-insensitive whole-word suffixes against the trimmed asset name.
var lpSuffixes = []string{
	" LP",
	" L.P.",
	" L P",
	" LLP",
	" LIMITED PARTNERSHIP",
}

// isLPName returns true if the asset name ends in a recognized LP suffix.
// Suffix matching is case-insensitive and whitespace-tolerant.
func isLPName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}

	upper := strings.ToUpper(trimmed)
	for _, suffix := range lpSuffixes {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}

	return false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `ginkgo run -race --focus "isLPName" ./provider/pvindex/`
Expected: PASS for all 15 entries.

- [ ] **Step 5: Commit**

```bash
git add provider/pvindex/filter.go provider/pvindex/filter_test.go
git commit -m "feat(pvindex): add LP suffix matcher"
```

---

### Task 4: Asset Master Filter

Pure function that filters a slice of `*data.Asset` to candidates passing the structural filter (active=true, asset_type=CS, exchange whitelist, not LP).

**Files:**
- Modify: `provider/pvindex/filter.go`
- Modify: `provider/pvindex/filter_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `provider/pvindex/filter_test.go`:

```go
import (
	"github.com/penny-vault/pvdata/data"
)

var _ = Describe("filterAssetMaster", func() {
	mkAsset := func(ticker, name, exch string, atype data.AssetType, active bool) *data.Asset {
		return &data.Asset{
			Ticker:          ticker,
			Name:            name,
			PrimaryExchange: data.Exchange(exch),
			AssetType:       atype,
			Active:          active,
		}
	}

	It("keeps active US common stocks on whitelisted exchanges", func() {
		input := []*data.Asset{
			mkAsset("AAPL", "Apple Inc.", "NASDAQ", data.CommonStock, true),
			mkAsset("BAC", "Bank of America Corporation", "NYSE", data.CommonStock, true),
			mkAsset("XOM", "Exxon Mobil Corporation", "XNYS", data.CommonStock, true),
		}
		out := filterAssetMaster(input)
		Expect(out).To(HaveLen(3))
	})

	It("excludes inactive assets", func() {
		input := []*data.Asset{
			mkAsset("DEAD", "Dead Co", "NYSE", data.CommonStock, false),
		}
		out := filterAssetMaster(input)
		Expect(out).To(BeEmpty())
	})

	It("excludes non-CS asset types", func() {
		input := []*data.Asset{
			mkAsset("SPY", "SPDR S&P 500 ETF", "NYSE ARCA", data.ETF, true),
			mkAsset("PCQ", "PIMCO California Muni", "NYSE", data.CEF, true),
			mkAsset("BABA", "Alibaba ADR", "NYSE", data.ADRC, true),
		}
		out := filterAssetMaster(input)
		Expect(out).To(BeEmpty())
	})

	It("excludes OTC and unknown exchanges", func() {
		input := []*data.Asset{
			mkAsset("FOO", "Foo Inc", "OTC", data.CommonStock, true),
			mkAsset("BAR", "Bar Inc", "NMFQS", data.CommonStock, true),
			mkAsset("BAZ", "Baz Inc", "UNK", data.CommonStock, true),
		}
		out := filterAssetMaster(input)
		Expect(out).To(BeEmpty())
	})

	It("accepts both display-name and MIC code formats for whitelisted exchanges", func() {
		input := []*data.Asset{
			mkAsset("A", "Alpha Inc", "NASDAQ", data.CommonStock, true),
			mkAsset("B", "Beta Inc", "XNAS", data.CommonStock, true),
			mkAsset("C", "Gamma Inc", "NYSE", data.CommonStock, true),
			mkAsset("D", "Delta Inc", "XNYS", data.CommonStock, true),
			mkAsset("E", "Epsilon Inc", "NYSE MKT", data.CommonStock, true),
			mkAsset("F", "Zeta Inc", "XASE", data.CommonStock, true),
			mkAsset("G", "Eta Inc", "AMEX", data.CommonStock, true),
			mkAsset("H", "Theta Inc", "NYSE ARCA", data.CommonStock, true),
			mkAsset("I", "Iota Inc", "ARCX", data.CommonStock, true),
			mkAsset("J", "Kappa Inc", "BATS", data.CommonStock, true),
		}
		out := filterAssetMaster(input)
		Expect(out).To(HaveLen(10))
	})

	It("excludes LPs", func() {
		input := []*data.Asset{
			mkAsset("EPD", "Enterprise Products Partners LP", "NYSE", data.CommonStock, true),
			mkAsset("ET", "Energy Transfer LP", "NYSE", data.CommonStock, true),
		}
		out := filterAssetMaster(input)
		Expect(out).To(BeEmpty())
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race --focus "filterAssetMaster" ./provider/pvindex/`
Expected: FAIL with "undefined: filterAssetMaster".

- [ ] **Step 3: Implement filterAssetMaster**

Append to `provider/pvindex/filter.go`:

```go
import (
	"github.com/penny-vault/pvdata/data"
)

// allowedExchanges is the whitelist of US-listed common stock exchanges.
// Both display-name and MIC code formats are accepted, because the assets table
// currently contains a mix of both. See "Known limitation: exchange field inconsistency"
// in the design spec.
var allowedExchanges = map[data.Exchange]struct{}{
	"NASDAQ":    {},
	"XNAS":      {},
	"NYSE":      {},
	"XNYS":      {},
	"NYSE MKT":  {},
	"XASE":      {},
	"AMEX":      {},
	"NYSE ARCA": {},
	"ARCX":      {},
	"BATS":      {},
}

// filterAssetMaster returns the subset of assets passing the structural filter:
// active = true, asset_type = CS, primary_exchange in whitelist, name does not match
// an LP suffix.
func filterAssetMaster(assets []*data.Asset) []*data.Asset {
	out := make([]*data.Asset, 0, len(assets))

	for _, a := range assets {
		if !a.Active {
			continue
		}

		if a.AssetType != data.CommonStock {
			continue
		}

		if _, ok := allowedExchanges[a.PrimaryExchange]; !ok {
			continue
		}

		if isLPName(a.Name) {
			continue
		}

		out = append(out, a)
	}

	return out
}
```

Note: the `import` block at the top of `filter.go` already has `"strings"` from Task 3 — add `"github.com/penny-vault/pvdata/data"` to the same block. Likewise for `filter_test.go`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `ginkgo run -race --focus "filterAssetMaster" ./provider/pvindex/`
Expected: PASS for all 6 specs.

- [ ] **Step 5: Commit**

```bash
git add provider/pvindex/filter.go provider/pvindex/filter_test.go
git commit -m "feat(pvindex): add asset master structural filter"
```

---

### Task 5: Rolling-Window Stats

Compute `(day_count, median_dv)` per FIGI from a slice of EOD rows over a sliding window. Used by every downstream filter.

**Files:**
- Create: `provider/pvindex/rolling.go`
- Create: `provider/pvindex/rolling_test.go`

- [ ] **Step 1: Write the failing tests**

Create `provider/pvindex/rolling_test.go`:

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
package pvindex

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("rollingStats", func() {
	mkRow := func(date string, close, vol float64) eodRow {
		t, _ := time.Parse("2006-01-02", date)
		return eodRow{Date: t, Close: close, Volume: vol}
	}

	It("returns count and median over a window with all rows in range", func() {
		rows := []eodRow{
			mkRow("2024-01-02", 10, 100), // dv=1000
			mkRow("2024-01-03", 20, 100), // dv=2000
			mkRow("2024-01-04", 30, 100), // dv=3000
		}
		windowStart, _ := time.Parse("2006-01-02", "2024-01-01")
		windowEnd, _ := time.Parse("2006-01-02", "2024-01-04")
		stats := rollingStats(rows, windowStart, windowEnd)
		Expect(stats.dayCount).To(Equal(3))
		Expect(stats.medianDV).To(Equal(2000.0))
	})

	It("ignores rows outside the window", func() {
		rows := []eodRow{
			mkRow("2023-12-30", 10, 100),
			mkRow("2024-01-02", 20, 100), // dv=2000
			mkRow("2024-01-03", 30, 100), // dv=3000
			mkRow("2024-01-05", 40, 100),
		}
		windowStart, _ := time.Parse("2006-01-02", "2024-01-02")
		windowEnd, _ := time.Parse("2006-01-02", "2024-01-04")
		stats := rollingStats(rows, windowStart, windowEnd)
		Expect(stats.dayCount).To(Equal(2))
		Expect(stats.medianDV).To(Equal(2500.0))
	})

	It("returns zero stats for empty input", func() {
		windowStart, _ := time.Parse("2006-01-02", "2024-01-01")
		windowEnd, _ := time.Parse("2006-01-02", "2024-01-31")
		stats := rollingStats(nil, windowStart, windowEnd)
		Expect(stats.dayCount).To(Equal(0))
		Expect(stats.medianDV).To(Equal(0.0))
	})

	It("computes median correctly for odd-count window", func() {
		rows := []eodRow{
			mkRow("2024-01-01", 10, 100), // dv=1000
			mkRow("2024-01-02", 20, 100), // dv=2000
			mkRow("2024-01-03", 30, 100), // dv=3000
			mkRow("2024-01-04", 40, 100), // dv=4000
			mkRow("2024-01-05", 50, 100), // dv=5000
		}
		windowStart, _ := time.Parse("2006-01-02", "2024-01-01")
		windowEnd, _ := time.Parse("2006-01-02", "2024-01-05")
		stats := rollingStats(rows, windowStart, windowEnd)
		Expect(stats.dayCount).To(Equal(5))
		Expect(stats.medianDV).To(Equal(3000.0))
	})

	It("computes median correctly for even-count window", func() {
		rows := []eodRow{
			mkRow("2024-01-01", 10, 100), // dv=1000
			mkRow("2024-01-02", 20, 100), // dv=2000
			mkRow("2024-01-03", 30, 100), // dv=3000
			mkRow("2024-01-04", 40, 100), // dv=4000
		}
		windowStart, _ := time.Parse("2006-01-02", "2024-01-01")
		windowEnd, _ := time.Parse("2006-01-02", "2024-01-04")
		stats := rollingStats(rows, windowStart, windowEnd)
		Expect(stats.dayCount).To(Equal(4))
		Expect(stats.medianDV).To(Equal(2500.0))
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race --focus "rollingStats" ./provider/pvindex/`
Expected: FAIL with "undefined: eodRow / undefined: rollingStats".

- [ ] **Step 3: Implement rolling.go**

Create `provider/pvindex/rolling.go`:

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
package pvindex

import (
	"sort"
	"time"
)

// eodRow is the in-memory representation of one EOD row used by the rolling-window
// computation. We do not reuse data.Eod because it carries OHLC fields we don't need
// and we want a tightly-packed struct for the chunk loader's memory budget.
type eodRow struct {
	Date   time.Time
	Close  float64
	Volume float64
}

// stats holds the rolling-window statistics for a single FIGI.
type stats struct {
	dayCount int
	medianDV float64
}

// rollingStats computes (count, median dollar volume) over the rows whose Date
// falls within [windowStart, windowEnd] inclusive. Rows are assumed to be sorted by
// date but the implementation does not require it. Returns zero values if no rows
// fall in the window.
func rollingStats(rows []eodRow, windowStart, windowEnd time.Time) stats {
	if len(rows) == 0 {
		return stats{}
	}

	dvs := make([]float64, 0, len(rows))

	for _, r := range rows {
		if r.Date.Before(windowStart) || r.Date.After(windowEnd) {
			continue
		}

		dvs = append(dvs, r.Close*r.Volume)
	}

	if len(dvs) == 0 {
		return stats{}
	}

	sort.Float64s(dvs)

	var median float64

	mid := len(dvs) / 2
	if len(dvs)%2 == 1 {
		median = dvs[mid]
	} else {
		median = (dvs[mid-1] + dvs[mid]) / 2
	}

	return stats{dayCount: len(dvs), medianDV: median}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `ginkgo run -race --focus "rollingStats" ./provider/pvindex/`
Expected: PASS for all 5 specs.

- [ ] **Step 5: Commit**

```bash
git add provider/pvindex/rolling.go provider/pvindex/rolling_test.go
git commit -m "feat(pvindex): add rolling-window dollar volume stats"
```

---

### Task 6: Share-Class Deduplication

Group candidates by `cik` (or `composite_figi` for null CIK) and keep the row with the highest median dollar volume per group.

**Files:**
- Modify: `provider/pvindex/filter.go`
- Modify: `provider/pvindex/filter_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `provider/pvindex/filter_test.go`:

```go
var _ = Describe("dedupShareClasses", func() {
	mkAssetWithCIK := func(ticker, cik, figi string) *data.Asset {
		return &data.Asset{
			Ticker:        ticker,
			CompositeFigi: figi,
			CIK:           cik,
		}
	}

	It("keeps the highest-DV share class within a CIK group", func() {
		assets := []*data.Asset{
			mkAssetWithCIK("GOOGL", "0001652044", "BBG009S39JX6"),
			mkAssetWithCIK("GOOG", "0001652044", "BBG009S3NB30"),
		}
		dvByFigi := map[string]float64{
			"BBG009S39JX6": 1_500_000_000, // GOOGL
			"BBG009S3NB30": 2_500_000_000, // GOOG (higher)
		}
		out := dedupShareClasses(assets, dvByFigi)
		Expect(out).To(HaveLen(1))
		Expect(out[0].Ticker).To(Equal("GOOG"))
	})

	It("treats null CIK rows as singleton groups", func() {
		assets := []*data.Asset{
			mkAssetWithCIK("XYZ", "", "BBGXYZ00001"),
			mkAssetWithCIK("ABC", "", "BBGABC00001"),
		}
		dvByFigi := map[string]float64{
			"BBGXYZ00001": 100,
			"BBGABC00001": 200,
		}
		out := dedupShareClasses(assets, dvByFigi)
		Expect(out).To(HaveLen(2))
	})

	It("breaks ties by ticker for determinism", func() {
		assets := []*data.Asset{
			mkAssetWithCIK("AAA", "0001234567", "BBGAAA00001"),
			mkAssetWithCIK("BBB", "0001234567", "BBGBBB00001"),
		}
		dvByFigi := map[string]float64{
			"BBGAAA00001": 1000,
			"BBGBBB00001": 1000,
		}
		out := dedupShareClasses(assets, dvByFigi)
		Expect(out).To(HaveLen(1))
		Expect(out[0].Ticker).To(Equal("AAA"))
	})

	It("handles assets with zero dollar volume", func() {
		assets := []*data.Asset{
			mkAssetWithCIK("ZERO", "0001111111", "BBGZERO0001"),
		}
		out := dedupShareClasses(assets, map[string]float64{})
		Expect(out).To(HaveLen(1))
		Expect(out[0].Ticker).To(Equal("ZERO"))
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race --focus "dedupShareClasses" ./provider/pvindex/`
Expected: FAIL with "undefined: dedupShareClasses".

- [ ] **Step 3: Implement dedupShareClasses**

Append to `provider/pvindex/filter.go`:

```go
// dedupShareClasses groups assets by CIK (or composite_figi as fallback for null CIK)
// and keeps the row with the highest median dollar volume per group. Ties are broken
// alphabetically by ticker for deterministic output.
func dedupShareClasses(assets []*data.Asset, dvByFigi map[string]float64) []*data.Asset {
	type bestRow struct {
		asset *data.Asset
		dv    float64
	}

	groups := make(map[string]bestRow, len(assets))

	for _, a := range assets {
		key := a.CIK
		if key == "" {
			key = a.CompositeFigi
		}

		dv := dvByFigi[a.CompositeFigi]

		current, exists := groups[key]
		if !exists {
			groups[key] = bestRow{asset: a, dv: dv}
			continue
		}

		if dv > current.dv || (dv == current.dv && a.Ticker < current.asset.Ticker) {
			groups[key] = bestRow{asset: a, dv: dv}
		}
	}

	out := make([]*data.Asset, 0, len(groups))
	for _, b := range groups {
		out = append(out, b.asset)
	}

	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `ginkgo run -race --focus "dedupShareClasses" ./provider/pvindex/`
Expected: PASS for all 4 specs.

- [ ] **Step 5: Commit**

```bash
git add provider/pvindex/filter.go provider/pvindex/filter_test.go
git commit -m "feat(pvindex): add share-class deduplication"
```

---

### Task 7: Cap-Weight Assignment

Pure function that takes a slice of `(figi, market_cap)` and produces normalized cap weights summing to 1.0.

**Files:**
- Modify: `provider/pvindex/filter.go`
- Modify: `provider/pvindex/filter_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `provider/pvindex/filter_test.go`:

```go
var _ = Describe("assignCapWeights", func() {
	It("normalizes weights to sum to 1.0", func() {
		caps := map[string]int64{
			"BBG000A": 1_000_000_000,
			"BBG000B": 2_000_000_000,
			"BBG000C": 7_000_000_000,
		}
		weights := assignCapWeights(caps)
		Expect(weights).To(HaveLen(3))
		Expect(weights["BBG000A"]).To(BeNumerically("~", 0.10, 1e-9))
		Expect(weights["BBG000B"]).To(BeNumerically("~", 0.20, 1e-9))
		Expect(weights["BBG000C"]).To(BeNumerically("~", 0.70, 1e-9))
	})

	It("returns weights summing to 1.0 within float tolerance", func() {
		caps := map[string]int64{
			"A": 333, "B": 333, "C": 333, "D": 1,
		}
		weights := assignCapWeights(caps)
		var sum float64
		for _, w := range weights {
			sum += w
		}
		Expect(sum).To(BeNumerically("~", 1.0, 1e-9))
	})

	It("returns empty map for empty input", func() {
		Expect(assignCapWeights(nil)).To(BeEmpty())
		Expect(assignCapWeights(map[string]int64{})).To(BeEmpty())
	})

	It("returns empty map when total cap is zero", func() {
		caps := map[string]int64{"A": 0, "B": 0}
		Expect(assignCapWeights(caps)).To(BeEmpty())
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race --focus "assignCapWeights" ./provider/pvindex/`
Expected: FAIL with "undefined: assignCapWeights".

- [ ] **Step 3: Implement assignCapWeights**

Append to `provider/pvindex/filter.go`:

```go
// assignCapWeights computes market-cap-weighted weights from a map of composite_figi
// to market_cap. Weights are normalized to sum to 1.0. Returns an empty map if the
// input is empty or the total market cap is zero.
func assignCapWeights(caps map[string]int64) map[string]float64 {
	if len(caps) == 0 {
		return map[string]float64{}
	}

	var total int64
	for _, c := range caps {
		total += c
	}

	if total <= 0 {
		return map[string]float64{}
	}

	weights := make(map[string]float64, len(caps))

	totalF := float64(total)
	for figi, c := range caps {
		weights[figi] = float64(c) / totalF
	}

	return weights
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `ginkgo run -race --focus "assignCapWeights" ./provider/pvindex/`
Expected: PASS for all 4 specs.

- [ ] **Step 5: Commit**

```bash
git add provider/pvindex/filter.go provider/pvindex/filter_test.go
git commit -m "feat(pvindex): add cap-weight assignment"
```

---

### Task 8: Percentile Computation

Pure function that computes the Nth percentile of an int64 slice, used for both the 25th and 80th percentile thresholds.

**Files:**
- Modify: `provider/pvindex/filter.go`
- Modify: `provider/pvindex/filter_test.go`

- [ ] **Step 1: Write the failing tests**

Append to `provider/pvindex/filter_test.go`:

```go
var _ = Describe("percentileInt64", func() {
	It("computes 25th percentile of a known distribution", func() {
		// 100 values from 1 to 100. 25th percentile = 25 (linear interpolation between 25 and 26 = 25.75 with type-7)
		// Use simple sorting interpretation: percentileInt64 returns the lowest value at or above
		// the cutoff index. For an unambiguous test, use a clearly indexable set.
		vals := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
		// 25th percentile of 10 values: index = (10 * 0.25) = 2.5 -> ceil to 3 -> vals[2] = 30
		Expect(percentileInt64(vals, 0.25)).To(Equal(int64(30)))
	})

	It("computes 80th percentile of a known distribution", func() {
		vals := []int64{10, 20, 30, 40, 50, 60, 70, 80, 90, 100}
		// 80th percentile of 10 values: index = (10 * 0.80) = 8 -> vals[7] = 80 (zero-indexed)
		Expect(percentileInt64(vals, 0.80)).To(Equal(int64(80)))
	})

	It("returns zero for empty input", func() {
		Expect(percentileInt64(nil, 0.5)).To(Equal(int64(0)))
		Expect(percentileInt64([]int64{}, 0.5)).To(Equal(int64(0)))
	})

	It("returns the only element for single-element input", func() {
		Expect(percentileInt64([]int64{42}, 0.25)).To(Equal(int64(42)))
		Expect(percentileInt64([]int64{42}, 0.80)).To(Equal(int64(42)))
	})

	It("does not modify the input slice", func() {
		vals := []int64{50, 10, 30, 20, 40}
		_ = percentileInt64(vals, 0.5)
		Expect(vals).To(Equal([]int64{50, 10, 30, 20, 40}))
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race --focus "percentileInt64" ./provider/pvindex/`
Expected: FAIL with "undefined: percentileInt64".

- [ ] **Step 3: Implement percentileInt64**

Append to `provider/pvindex/filter.go`. Add `"math"` and `"sort"` to the existing import block at the top of `filter.go` (the file already has `"strings"` from Task 3 and `"github.com/penny-vault/pvdata/data"` from Task 4).

```go
// percentileInt64 returns the value at the given percentile (0..1) of the input slice.
// Uses the "nearest rank" method: for n values, the rank index = ceil(n*p), clamped
// to [1, n]. The returned value is sorted[rank-1]. Does not modify the input slice.
// Returns 0 for empty input.
func percentileInt64(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}

	if len(values) == 1 {
		return values[0]
	}

	sorted := make([]int64, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	rank := int(math.Ceil(float64(len(sorted)) * p))
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}

	return sorted[rank-1]
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `ginkgo run -race --focus "percentileInt64" ./provider/pvindex/`
Expected: PASS for all 5 specs.

- [ ] **Step 5: Commit**

```bash
git add provider/pvindex/filter.go provider/pvindex/filter_test.go
git commit -m "feat(pvindex): add nearest-rank percentile helper"
```

---

### Task 9: Per-Day Universe Computation

Orchestrates the filter chain for a single date `D`. Takes pre-loaded asset, EOD, and metric data and produces the universe `map[composite_figi]IndexMember` with cap-weights.

**Files:**
- Create: `provider/pvindex/chunk.go`
- Create: `provider/pvindex/chunk_test.go`

- [ ] **Step 1: Write the failing tests**

Create `provider/pvindex/chunk_test.go`:

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
package pvindex

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pvdata/data"
)

var _ = Describe("computeUniverseForDate", func() {
	// Helper: build a contiguous EOD slice for one FIGI from startDate going N days forward.
	// All trading days are simulated as consecutive calendar days for simplicity in tests.
	mkEodSeries := func(startDate string, n int, close, volume float64) []eodRow {
		t, _ := time.Parse("2006-01-02", startDate)
		out := make([]eodRow, n)
		for i := 0; i < n; i++ {
			out[i] = eodRow{Date: t.AddDate(0, 0, i), Close: close, Volume: volume}
		}
		return out
	}

	mkCSAsset := func(ticker, cik, figi, name string) *data.Asset {
		return &data.Asset{
			Ticker:          ticker,
			Name:            name,
			CompositeFigi:   figi,
			CIK:             cik,
			AssetType:       data.CommonStock,
			PrimaryExchange: "NASDAQ",
			Active:          true,
		}
	}

	It("includes a stock that passes all filters", func() {
		evalDate, _ := time.Parse("2006-01-02", "2024-12-31")
		// 200-day window: 2024-06-15 to 2024-12-30 (using calendar days for simplicity).
		windowStart := evalDate.AddDate(0, 0, -200)
		windowEnd := evalDate.AddDate(0, 0, -1)

		assets := []*data.Asset{mkCSAsset("AAPL", "0000320193", "BBG000B9XRY4", "Apple Inc.")}
		eodByFigi := map[string][]eodRow{
			"BBG000B9XRY4": mkEodSeries("2024-06-14", 200, 100.0, 100_000), // dv = 10M
		}
		mcapByFigi := map[string]int64{
			"BBG000B9XRY4": 3_000_000_000_000, // $3T
		}
		// Build a broad market cap pool that has AAPL and one tiny stock for percentile baseline.
		broadMcaps := []int64{1_000_000, 3_000_000_000_000}
		// Trading days = the 200-day window matches our calendar exactly.
		tradingDayCount := 200

		input := perDayInput{
			Date:            evalDate,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: tradingDayCount,
			Assets:          assets,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		universe := computeUniverseForDate(input)
		Expect(universe).To(HaveKey("AAPL"))
		Expect(universe["AAPL"].Weight).To(BeNumerically("~", 1.0, 1e-9))
	})

	It("excludes a stock with insufficient EOD history (under 30 days)", func() {
		evalDate, _ := time.Parse("2006-01-02", "2024-12-31")
		windowStart := evalDate.AddDate(0, 0, -200)
		windowEnd := evalDate.AddDate(0, 0, -1)

		assets := []*data.Asset{mkCSAsset("NEW", "0001111111", "BBG000NEW001", "New Inc.")}
		eodByFigi := map[string][]eodRow{
			"BBG000NEW001": mkEodSeries(evalDate.AddDate(0, 0, -20).Format("2006-01-02"), 20, 50.0, 100_000),
		}
		mcapByFigi := map[string]int64{"BBG000NEW001": 50_000_000_000}
		broadMcaps := []int64{50_000_000_000, 1_000_000}

		input := perDayInput{
			Date:            evalDate,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: 200,
			Assets:          assets,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		universe := computeUniverseForDate(input)
		Expect(universe).To(BeEmpty())
	})

	It("includes a stock via early-entry path (50 days, top quintile market cap)", func() {
		evalDate, _ := time.Parse("2006-01-02", "2024-12-31")
		windowStart := evalDate.AddDate(0, 0, -200)
		windowEnd := evalDate.AddDate(0, 0, -1)

		assets := []*data.Asset{mkCSAsset("BIGIPO", "0002222222", "BBG000BIGIPO", "BigIPO Inc.")}
		// 50 contiguous days ending day-1
		eodByFigi := map[string][]eodRow{
			"BBG000BIGIPO": mkEodSeries(evalDate.AddDate(0, 0, -50).Format("2006-01-02"), 50, 200.0, 100_000), // dv = 20M
		}
		mcapByFigi := map[string]int64{"BBG000BIGIPO": 80_000_000_000}
		// Broad market cap pool: BIGIPO at $80B is in top 20% of [1B, 5B, 10B, 80B] (80th percentile).
		broadMcaps := []int64{1_000_000_000, 5_000_000_000, 10_000_000_000, 80_000_000_000}

		input := perDayInput{
			Date:            evalDate,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: 200,
			Assets:          assets,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		universe := computeUniverseForDate(input)
		Expect(universe).To(HaveKey("BIGIPO"))
	})

	It("excludes a stock via early-entry path when market cap is below top quintile", func() {
		evalDate, _ := time.Parse("2006-01-02", "2024-12-31")
		windowStart := evalDate.AddDate(0, 0, -200)
		windowEnd := evalDate.AddDate(0, 0, -1)

		assets := []*data.Asset{mkCSAsset("SMALLIPO", "0003333333", "BBG000SMALLIPO", "SmallIPO Inc.")}
		eodByFigi := map[string][]eodRow{
			"BBG000SMALLIPO": mkEodSeries(evalDate.AddDate(0, 0, -50).Format("2006-01-02"), 50, 10.0, 100_000),
		}
		mcapByFigi := map[string]int64{"BBG000SMALLIPO": 500_000_000} // $500M
		// Broad market cap pool: SMALLIPO at $500M is bottom of [500M, 5B, 10B, 80B], not top 20%.
		broadMcaps := []int64{500_000_000, 5_000_000_000, 10_000_000_000, 80_000_000_000}

		input := perDayInput{
			Date:            evalDate,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: 200,
			Assets:          assets,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		universe := computeUniverseForDate(input)
		Expect(universe).To(BeEmpty())
	})

	It("excludes a stock with low ADV", func() {
		evalDate, _ := time.Parse("2006-01-02", "2024-12-31")
		windowStart := evalDate.AddDate(0, 0, -200)
		windowEnd := evalDate.AddDate(0, 0, -1)

		assets := []*data.Asset{mkCSAsset("ILLIQ", "0004444444", "BBG000ILLIQ01", "Illiquid Inc.")}
		eodByFigi := map[string][]eodRow{
			"BBG000ILLIQ01": mkEodSeries("2024-06-14", 200, 10.0, 1_000), // dv = 10K
		}
		mcapByFigi := map[string]int64{"BBG000ILLIQ01": 100_000_000_000}
		broadMcaps := []int64{100_000_000_000}

		input := perDayInput{
			Date:            evalDate,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: 200,
			Assets:          assets,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		universe := computeUniverseForDate(input)
		Expect(universe).To(BeEmpty())
	})

	It("excludes a stock with prior_close below $2", func() {
		evalDate, _ := time.Parse("2006-01-02", "2024-12-31")
		windowStart := evalDate.AddDate(0, 0, -200)
		windowEnd := evalDate.AddDate(0, 0, -1)

		assets := []*data.Asset{mkCSAsset("CHEAP", "0005555555", "BBG000CHEAP01", "Cheap Inc.")}
		eodByFigi := map[string][]eodRow{
			"BBG000CHEAP01": mkEodSeries("2024-06-14", 200, 1.5, 10_000_000), // dv = 15M, but price < $2
		}
		mcapByFigi := map[string]int64{"BBG000CHEAP01": 100_000_000_000}
		broadMcaps := []int64{100_000_000_000}

		input := perDayInput{
			Date:            evalDate,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: 200,
			Assets:          assets,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		universe := computeUniverseForDate(input)
		Expect(universe).To(BeEmpty())
	})

	It("excludes a stock below the 25th percentile market cap cutoff", func() {
		evalDate, _ := time.Parse("2006-01-02", "2024-12-31")
		windowStart := evalDate.AddDate(0, 0, -200)
		windowEnd := evalDate.AddDate(0, 0, -1)

		assets := []*data.Asset{mkCSAsset("TINY", "0006666666", "BBG000TINY001", "Tiny Inc.")}
		eodByFigi := map[string][]eodRow{
			"BBG000TINY001": mkEodSeries("2024-06-14", 200, 10.0, 1_000_000), // dv = 10M, passes ADV
		}
		mcapByFigi := map[string]int64{"BBG000TINY001": 50_000_000} // $50M
		// Broad pool with TINY at the bottom: 25th percentile is well above 50M.
		broadMcaps := []int64{50_000_000, 1_000_000_000, 5_000_000_000, 10_000_000_000, 80_000_000_000}

		input := perDayInput{
			Date:            evalDate,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: 200,
			Assets:          assets,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		universe := computeUniverseForDate(input)
		Expect(universe).To(BeEmpty())
	})

	It("excludes a stock with insufficient EOD rows after a gap", func() {
		evalDate, _ := time.Parse("2006-01-02", "2024-12-31")
		windowStart := evalDate.AddDate(0, 0, -200)
		windowEnd := evalDate.AddDate(0, 0, -1)

		// Build 200 consecutive calendar days starting 2024-06-15, then drop the middle row.
		// After removal, only ~198 rows fall inside the window, so the stock fails the standard-path
		// dayCount threshold (< 200). BroadMarketCaps is set so the stock is also below the
		// 80th-percentile early-entry threshold, so neither path admits it.
		series := mkEodSeries("2024-06-15", 200, 100.0, 100_000)
		series = append(series[:100], series[101:]...)

		assets := []*data.Asset{mkCSAsset("GAPPY", "0007777777", "BBG000GAPPY01", "Gappy Inc.")}
		eodByFigi := map[string][]eodRow{
			"BBG000GAPPY01": series,
		}
		mcapByFigi := map[string]int64{"BBG000GAPPY01": 100_000_000_000} // $100B
		// 80th percentile of [100B, 500B, 1T, 2T, 5T] = ceil(5*0.8)=4 -> sorted[3] = 2T.
		// GAPPY's 100B is well below the 2T early-entry cutoff.
		broadMcaps := []int64{100_000_000_000, 500_000_000_000, 1_000_000_000_000, 2_000_000_000_000, 5_000_000_000_000}

		input := perDayInput{
			Date:            evalDate,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: 200,
			Assets:          assets,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		universe := computeUniverseForDate(input)
		Expect(universe).To(BeEmpty())
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race --focus "computeUniverseForDate" ./provider/pvindex/`
Expected: FAIL with "undefined: perDayInput / undefined: computeUniverseForDate".

- [ ] **Step 3: Implement chunk.go (initial scope: per-day computation only)**

Create `provider/pvindex/chunk.go`:

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
package pvindex

import (
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/provider"
)

const (
	// minDayCountStandard is the standard contiguity threshold (200 trading days).
	minDayCountStandard = 200
	// minDayCountEarlyEntry is the minimum data required for the IPO early-entry path.
	minDayCountEarlyEntry = 30
	// liquidityThresholdUSD is the minimum 200-day median dollar volume.
	liquidityThresholdUSD = 2_500_000.0
	// priceFloorUSD is the minimum prior-day close.
	priceFloorUSD = 2.0
	// sizePercentile is the 25th percentile cutoff for market cap.
	sizePercentile = 0.25
	// earlyEntryPercentile is the top quintile threshold (80th percentile).
	earlyEntryPercentile = 0.80
)

// perDayInput is the data needed to compute the universe for a single date.
// Loaded once per chunk and reused across days.
type perDayInput struct {
	Date            time.Time
	WindowStart     time.Time // inclusive lower bound for the 200-day rolling window
	WindowEnd       time.Time // inclusive upper bound (= D - 1 trading day)
	TradingDayCount int       // expected count of trading days in [WindowStart, WindowEnd]
	Assets          []*data.Asset
	EodByFigi       map[string][]eodRow
	MarketCapByFigi map[string]int64
	BroadMarketCaps []int64 // all market caps in the broad CS pool on D, used for percentile thresholds
}

// computeUniverseForDate runs the full filter chain for a single trading day and
// returns the resulting universe as map[ticker]IndexMember with cap-weights assigned.
func computeUniverseForDate(in perDayInput) map[string]provider.IndexMember {
	// Step 1 (asset master) is assumed to have already been applied to in.Assets.

	// Step 3: rolling stats per FIGI.
	statsByFigi := make(map[string]stats, len(in.Assets))

	for _, a := range in.Assets {
		rows := in.EodByFigi[a.CompositeFigi]
		statsByFigi[a.CompositeFigi] = rollingStats(rows, in.WindowStart, in.WindowEnd)
	}

	// Step 4: percentile baseline for early-entry threshold.
	sizeCutoff := percentileInt64(in.BroadMarketCaps, sizePercentile)
	earlyEntryCutoff := percentileInt64(in.BroadMarketCaps, earlyEntryPercentile)

	// Step 5: data availability.
	type candidate struct {
		asset      *data.Asset
		priorClose float64
		marketCap  int64
		medianDV   float64
	}

	candidates := make([]candidate, 0, len(in.Assets))

	for _, a := range in.Assets {
		st := statsByFigi[a.CompositeFigi]

		mcap, hasMcap := in.MarketCapByFigi[a.CompositeFigi]
		if !hasMcap || mcap <= 0 {
			continue
		}

		standardEligible := st.dayCount >= minDayCountStandard
		earlyEligible := st.dayCount >= minDayCountEarlyEntry && st.dayCount < minDayCountStandard && mcap >= earlyEntryCutoff

		if !standardEligible && !earlyEligible {
			continue
		}

		// Contiguity check for standard path: dayCount must equal expected trading day count.
		// For early-entry, we require dayCount of contiguous days from the listing date,
		// which is implicit (all available rows in the window count toward dayCount).
		if standardEligible && st.dayCount != in.TradingDayCount {
			continue
		}

		// Prior close = most recent in-window EOD close.
		priorClose := mostRecentClose(in.EodByFigi[a.CompositeFigi], in.WindowEnd)

		candidates = append(candidates, candidate{
			asset:      a,
			priorClose: priorClose,
			marketCap:  mcap,
			medianDV:   st.medianDV,
		})
	}

	// Step 6: share-class deduplication.
	dvByFigi := make(map[string]float64, len(candidates))
	assetsForDedup := make([]*data.Asset, 0, len(candidates))
	candByFigi := make(map[string]candidate, len(candidates))

	for _, c := range candidates {
		dvByFigi[c.asset.CompositeFigi] = c.medianDV
		assetsForDedup = append(assetsForDedup, c.asset)
		candByFigi[c.asset.CompositeFigi] = c
	}

	deduped := dedupShareClasses(assetsForDedup, dvByFigi)

	// Step 7: market cap percentile filter.
	survivors := make([]candidate, 0, len(deduped))

	for _, a := range deduped {
		c := candByFigi[a.CompositeFigi]
		if c.marketCap < sizeCutoff {
			continue
		}
		survivors = append(survivors, c)
	}

	// Step 8: liquidity filter.
	liquidSurvivors := make([]candidate, 0, len(survivors))
	for _, c := range survivors {
		if c.medianDV < liquidityThresholdUSD {
			continue
		}
		liquidSurvivors = append(liquidSurvivors, c)
	}

	// Step 9: price guard rail.
	priceSurvivors := make([]candidate, 0, len(liquidSurvivors))
	for _, c := range liquidSurvivors {
		if c.priorClose < priceFloorUSD {
			continue
		}
		priceSurvivors = append(priceSurvivors, c)
	}

	// Step 10: cap weights.
	caps := make(map[string]int64, len(priceSurvivors))
	for _, c := range priceSurvivors {
		caps[c.asset.CompositeFigi] = c.marketCap
	}

	weights := assignCapWeights(caps)

	universe := make(map[string]provider.IndexMember, len(priceSurvivors))
	for _, c := range priceSurvivors {
		universe[c.asset.Ticker] = provider.IndexMember{
			CompositeFigi: c.asset.CompositeFigi,
			Weight:        weights[c.asset.CompositeFigi],
		}
	}

	return universe
}

// mostRecentClose returns the close price of the most recent EOD row at or before the
// given date, or 0 if no such row exists.
func mostRecentClose(rows []eodRow, asOf time.Time) float64 {
	var (
		bestDate  time.Time
		bestClose float64
	)

	for _, r := range rows {
		if r.Date.After(asOf) {
			continue
		}
		if r.Date.After(bestDate) {
			bestDate = r.Date
			bestClose = r.Close
		}
	}

	return bestClose
}

```

- [ ] **Step 4: Run tests to verify they pass**

Run: `ginkgo run -race --focus "computeUniverseForDate" ./provider/pvindex/`
Expected: PASS for all 8 specs.

- [ ] **Step 5: Commit**

```bash
git add provider/pvindex/chunk.go provider/pvindex/chunk_test.go
git commit -m "feat(pvindex): add per-day universe computation"
```

---

### Task 10: DB Loaders

Database functions that read from the canonical published views. These are tested via integration tests against the real DB (no unit tests — the loaders are thin SQL wrappers).

**Files:**
- Create: `provider/pvindex/loader.go`

- [ ] **Step 1: Create loader.go with four loader functions**

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
package pvindex

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog/log"
)

// loadCandidateAssets reads all rows from the `assets` view and returns those passing
// the structural filter (active, CS, exchange whitelist, not LP).
func loadCandidateAssets(ctx context.Context, pool *pgxpool.Pool) ([]*data.Asset, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn for loadCandidateAssets: %w", err)
	}
	defer conn.Release()

	all, err := data.ActiveAssets(ctx, conn, "assets")
	if err != nil {
		return nil, fmt.Errorf("load active assets: %w", err)
	}

	filtered := filterAssetMaster(all)

	log.Debug().
		Int("total_active", len(all)).
		Int("after_structural_filter", len(filtered)).
		Msg("loaded candidate assets")

	return filtered, nil
}

// loadEodChunk reads EOD rows for the given FIGIs over [start, end] inclusive from
// the `eod` published view. Returns a map keyed by composite_figi.
func loadEodChunk(ctx context.Context, pool *pgxpool.Pool, figis []string, start, end time.Time) (map[string][]eodRow, error) {
	if len(figis) == 0 {
		return map[string][]eodRow{}, nil
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn for loadEodChunk: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx,
		`SELECT composite_figi, event_date, close, volume
		 FROM eod
		 WHERE event_date BETWEEN $1 AND $2
		   AND composite_figi = ANY($3)
		 ORDER BY composite_figi, event_date`,
		start, end, figis,
	)
	if err != nil {
		return nil, fmt.Errorf("query eod chunk: %w", err)
	}
	defer rows.Close()

	out := make(map[string][]eodRow)

	for rows.Next() {
		var (
			figi    string
			date    time.Time
			closeP  float64
			volume  float64
		)

		if err := rows.Scan(&figi, &date, &closeP, &volume); err != nil {
			return nil, fmt.Errorf("scan eod row: %w", err)
		}

		out[figi] = append(out[figi], eodRow{Date: date, Close: closeP, Volume: volume})
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate eod rows: %w", err)
	}

	return out, nil
}

// loadMarketCapAsOf reads the most recent market_cap on or before `asOf` for each FIGI
// in the input. Used to populate per-day market cap lookups within a chunk.
func loadMarketCapAsOf(ctx context.Context, pool *pgxpool.Pool, figis []string, asOf time.Time) (map[string]int64, error) {
	if len(figis) == 0 {
		return map[string]int64{}, nil
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn for loadMarketCapAsOf: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx,
		`SELECT DISTINCT ON (composite_figi) composite_figi, market_cap
		 FROM metrics
		 WHERE composite_figi = ANY($1)
		   AND event_date <= $2
		   AND market_cap > 0
		 ORDER BY composite_figi, event_date DESC`,
		figis, asOf,
	)
	if err != nil {
		return nil, fmt.Errorf("query market_cap: %w", err)
	}
	defer rows.Close()

	out := make(map[string]int64)

	for rows.Next() {
		var (
			figi string
			mcap int64
		)

		if err := rows.Scan(&figi, &mcap); err != nil {
			return nil, fmt.Errorf("scan metric row: %w", err)
		}

		out[figi] = mcap
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate metric rows: %w", err)
	}

	return out, nil
}

// loadBroadMarketCaps returns the market caps of all CS rows on the broad pool baseline:
// active CS on whitelisted exchanges with a metric row on or before `asOf`. Used as the
// percentile baseline for the size and early-entry filters.
func loadBroadMarketCaps(ctx context.Context, pool *pgxpool.Pool, asOf time.Time) ([]int64, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn for loadBroadMarketCaps: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx,
		`WITH latest AS (
		   SELECT DISTINCT ON (m.composite_figi) m.composite_figi, m.market_cap
		   FROM metrics m
		   JOIN assets a USING (composite_figi)
		   WHERE a.active = true
		     AND a.asset_type = 'CS'
		     AND a.primary_exchange IN ('NASDAQ','NYSE','NYSE MKT','NYSE ARCA','BATS','AMEX','XNAS','XNYS','XASE','ARCX')
		     AND m.event_date <= $1
		     AND m.market_cap > 0
		   ORDER BY m.composite_figi, m.event_date DESC
		 )
		 SELECT market_cap FROM latest`,
		asOf,
	)
	if err != nil {
		return nil, fmt.Errorf("query broad market caps: %w", err)
	}
	defer rows.Close()

	out := make([]int64, 0, 5000)

	for rows.Next() {
		var mcap int64
		if err := rows.Scan(&mcap); err != nil {
			return nil, fmt.Errorf("scan broad mcap row: %w", err)
		}
		out = append(out, mcap)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate broad mcap rows: %w", err)
	}

	return out, nil
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./provider/pvindex/...`
Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add provider/pvindex/loader.go
git commit -m "feat(pvindex): add canonical-view loaders for assets, eod, metrics"
```

---

### Task 11: Date Range and Chunk Iteration

Compute the dataset date range and iterate trading days in chunks.

**Files:**
- Create: `provider/pvindex/date_range.go`

- [ ] **Step 1: Create date_range.go**

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
package pvindex

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// computeDateRange returns the [start, end] date range for the universe computation:
// start = 200 trading days after MIN(metrics.event_date)
// end = LEAST(MAX(eod.event_date), MAX(metrics.event_date))
func computeDateRange(ctx context.Context, pool *pgxpool.Pool) (time.Time, time.Time, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("acquire conn for computeDateRange: %w", err)
	}
	defer conn.Release()

	var (
		minMetric, maxMetric, maxEod time.Time
	)

	if err := conn.QueryRow(ctx, `SELECT MIN(event_date), MAX(event_date) FROM metrics`).Scan(&minMetric, &maxMetric); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("query metric date range: %w", err)
	}

	if err := conn.QueryRow(ctx, `SELECT MAX(event_date) FROM eod`).Scan(&maxEod); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("query eod max date: %w", err)
	}

	// Find the 200th trading day after minMetric.
	var startDate time.Time
	if err := conn.QueryRow(ctx,
		`SELECT dt FROM trading_days($1::date, $1::date + INTERVAL '400 days') AS t(dt)
		 ORDER BY dt LIMIT 1 OFFSET 199`,
		minMetric,
	).Scan(&startDate); err != nil {
		return time.Time{}, time.Time{}, fmt.Errorf("compute start date: %w", err)
	}

	endDate := maxMetric
	if maxEod.Before(endDate) {
		endDate = maxEod
	}

	return startDate, endDate, nil
}

// chunkTradingDays loads trading days in [start, end] from the database and splits them
// into fixed-size chunks. Each chunk is a slice of trading days.
func chunkTradingDays(ctx context.Context, pool *pgxpool.Pool, start, end time.Time, chunkSize int) ([][]time.Time, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn for chunkTradingDays: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx, `SELECT dt FROM trading_days($1::date, $2::date) AS t(dt) ORDER BY dt`, start, end)
	if err != nil {
		return nil, fmt.Errorf("query trading days: %w", err)
	}
	defer rows.Close()

	var allDays []time.Time

	for rows.Next() {
		var dt time.Time
		if err := rows.Scan(&dt); err != nil {
			return nil, fmt.Errorf("scan trading day: %w", err)
		}
		allDays = append(allDays, dt)
	}

	if len(allDays) == 0 {
		return nil, nil
	}

	chunks := make([][]time.Time, 0, (len(allDays)/chunkSize)+1)
	for i := 0; i < len(allDays); i += chunkSize {
		end := i + chunkSize
		if end > len(allDays) {
			end = len(allDays)
		}
		chunks = append(chunks, allDays[i:end])
	}

	return chunks, nil
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./provider/pvindex/...`
Expected: clean build.

- [ ] **Step 3: Commit**

```bash
git add provider/pvindex/date_range.go
git commit -m "feat(pvindex): add date range and trading-day chunking"
```

---

### Task 12: Chunk Processor and Fetch Wiring

The top-level orchestration. For each chunk: load EOD/metric/asset data once, iterate trading days, compute universe, diff against prior state, emit observations. Replaces the stub `fetchTradableUniverse` from Task 2.

**Files:**
- Modify: `provider/pvindex/chunk.go` (add `processChunk`)
- Modify: `provider/pvindex/pvindex.go` (rewrite `fetchTradableUniverse` to call into chunk processor)

- [ ] **Step 1: Append processChunk to chunk.go**

Add to `provider/pvindex/chunk.go`:

```go
import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog"
)

// processChunk executes the universe computation for one chunk of trading days.
// It loads source data once, iterates days, computes the universe, diffs against the
// prior state, and emits observations to `out`. The prior state is loaded from the DB
// at chunk start and maintained in memory across the chunk's days.
func processChunk(
	ctx context.Context,
	pool *pgxpool.Pool,
	sub *library.Subscription,
	indexTicker string,
	tradingDays []time.Time,
	candidates []*data.Asset,
	out chan<- *data.Observation,
) error {
	if len(tradingDays) == 0 {
		return nil
	}

	logger := zerolog.Ctx(ctx)

	chunkStart := tradingDays[0]
	chunkEnd := tradingDays[len(tradingDays)-1]

	// 200 trading days of EOD prefix before the chunk start.
	loadStart := chunkStart.AddDate(0, 0, -400) // ~400 calendar days covers 200 trading days comfortably

	figis := make([]string, len(candidates))
	for i, a := range candidates {
		figis[i] = a.CompositeFigi
	}

	logger.Info().
		Time("chunk_start", chunkStart).
		Time("chunk_end", chunkEnd).
		Int("trading_days", len(tradingDays)).
		Int("candidate_assets", len(candidates)).
		Msg("loading chunk data")

	eodByFigi, err := loadEodChunk(ctx, pool, figis, loadStart, chunkEnd)
	if err != nil {
		return fmt.Errorf("load eod chunk: %w", err)
	}

	// Reconstruct prior state from DB at chunk start.
	prevState := provider.CurrentIndexMembers(
		ctx,
		pool,
		sub.DataTablesMap[data.IndexSnapshotKey],
		sub.DataTablesMap[data.IndexChangelogKey],
		indexTicker,
		chunkStart.AddDate(0, 0, -1),
	)

	for _, d := range tradingDays {
		// Determine the trailing 200-trading-day window ending d-1.
		windowDays, err := loadTrailingWindow(ctx, pool, d, minDayCountStandard)
		if err != nil {
			return fmt.Errorf("load trailing window for %s: %w", d.Format("2006-01-02"), err)
		}
		if len(windowDays) < minDayCountEarlyEntry {
			logger.Warn().Time("date", d).Msg("insufficient trading days for window; skipping")
			continue
		}

		windowStart := windowDays[0]
		windowEnd := windowDays[len(windowDays)-1]

		mcapByFigi, err := loadMarketCapAsOf(ctx, pool, figis, d)
		if err != nil {
			return fmt.Errorf("load market cap for %s: %w", d.Format("2006-01-02"), err)
		}

		broadMcaps, err := loadBroadMarketCaps(ctx, pool, d)
		if err != nil {
			return fmt.Errorf("load broad mcaps for %s: %w", d.Format("2006-01-02"), err)
		}

		input := perDayInput{
			Date:            d,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: len(windowDays),
			Assets:          candidates,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		newState := computeUniverseForDate(input)

		adds, removes, weightChanges := provider.DiffSnapshotsWithThreshold(
			newState,
			prevState,
			provider.DiffOptions{RelativeThreshold: 0.25},
		)

		ticker := indexTicker
		obsTemplate := &data.Observation{
			ObservationDate:  d,
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}

		provider.EmitChangelog(adds, removes, ticker, d, obsTemplate, out)
		provider.EmitWeightChanges(weightChanges, ticker, d, obsTemplate, out)

		// Emit annual snapshot if a year has elapsed since the last in-DB snapshot,
		// or if no snapshot has ever been taken (cold start).
		lastSnapshot := provider.LastSnapshotDate(ctx, pool, sub.DataTablesMap[data.IndexSnapshotKey], indexTicker)
		if provider.ShouldTakeSnapshot(lastSnapshot, d, "yearly") {
			constituents := make([]data.IndexConstituent, 0, len(newState))
			for tk, m := range newState {
				constituents = append(constituents, data.IndexConstituent{
					Ticker:        tk,
					CompositeFigi: m.CompositeFigi,
					Weight:        m.Weight,
				})
			}

			out <- &data.Observation{
				IndexSnapshot: &data.IndexSnapshot{
					IndexTicker:  indexTicker,
					SnapshotDate: d,
					Constituents: constituents,
				},
				ObservationDate:  d,
				SubscriptionID:   sub.ID,
				SubscriptionName: sub.Name,
			}
		}

		// Apply emitted changes to prevState in memory for the next day's diff.
		for tk := range removes {
			delete(prevState, tk)
		}
		for tk, m := range adds {
			prevState[tk] = m
		}
		for tk, m := range weightChanges {
			prevState[tk] = m
		}
	}

	return nil
}

// loadTrailingWindow returns the trailing 200 trading days ending at (date - 1 trading day).
func loadTrailingWindow(ctx context.Context, pool *pgxpool.Pool, asOf time.Time, n int) ([]time.Time, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire conn for loadTrailingWindow: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx,
		`SELECT dt FROM (
		   SELECT dt FROM trading_days($1::date - INTERVAL '400 days', $1::date - INTERVAL '1 day') AS t(dt)
		   ORDER BY dt DESC
		   LIMIT $2
		 ) sub
		 ORDER BY dt`,
		asOf, n,
	)
	if err != nil {
		return nil, fmt.Errorf("query trailing window: %w", err)
	}
	defer rows.Close()

	var out []time.Time

	for rows.Next() {
		var dt time.Time
		if err := rows.Scan(&dt); err != nil {
			return nil, fmt.Errorf("scan trailing window day: %w", err)
		}
		out = append(out, dt)
	}

	return out, nil
}
```

Note: the `import` block at the top of `chunk.go` from Task 9 has only `time` and `data` and `provider`. After this addition, the imports should also include `context`, `fmt`, `github.com/jackc/pgx/v5/pgxpool`, `github.com/penny-vault/pvdata/library`, and `github.com/rs/zerolog`. Combine into a single import block.

- [ ] **Step 2: Replace fetchTradableUniverse in pvindex.go**

In `provider/pvindex/pvindex.go`, replace the stub `fetchTradableUniverse` function from Task 2 with the real implementation:

```go
import (
	"context"
	"strconv"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
)

const (
	defaultIndexTicker = "us-tradable"
	defaultChunkSize   = 63
)

func fetchTradableUniverse(ctx context.Context, sub *library.Subscription, out chan<- *data.Observation, exit chan<- data.RunSummary) {
	logger := zerolog.Ctx(ctx)
	pool := sub.Library.Pool

	runSummary := data.RunSummary{
		StartTime:        time.Now(),
		SubscriptionID:   sub.ID,
		SubscriptionName: sub.Name,
		Status:           data.RunSuccess,
	}

	defer func() {
		runSummary.EndTime = time.Now()
		exit <- runSummary
	}()

	indexTicker := defaultIndexTicker
	if v := sub.Config["index_ticker"]; v != "" {
		indexTicker = v
	}

	chunkSize := defaultChunkSize
	if v := sub.Config["chunk_size_days"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			chunkSize = n
		}
	}

	startDate, endDate, err := computeDateRange(ctx, pool)
	if err != nil {
		logger.Error().Err(err).Msg("compute date range failed")
		runSummary.Status = data.RunFailed
		return
	}

	// Honor start_date_override.
	if v := sub.Config["start_date_override"]; v != "" {
		if t, err := time.Parse("2006-01-02", v); err == nil && t.After(startDate) {
			startDate = t
		}
	}

	// Incremental: skip dates already covered.
	highWater := provider.LastSnapshotDate(ctx, pool, sub.DataTablesMap[data.IndexSnapshotKey], indexTicker)
	if !highWater.IsZero() && highWater.After(startDate) {
		startDate = highWater.AddDate(0, 0, 1)
	}

	if startDate.After(endDate) {
		logger.Info().Msg("no new dates to process")
		return
	}

	candidates, err := loadCandidateAssets(ctx, pool)
	if err != nil {
		logger.Error().Err(err).Msg("load candidate assets failed")
		runSummary.Status = data.RunFailed
		return
	}

	chunks, err := chunkTradingDays(ctx, pool, startDate, endDate, chunkSize)
	if err != nil {
		logger.Error().Err(err).Msg("chunk trading days failed")
		runSummary.Status = data.RunFailed
		return
	}

	totalObs := 0
	for _, chunk := range chunks {
		if err := processChunk(ctx, pool, sub, indexTicker, chunk, candidates, out); err != nil {
			logger.Error().Err(err).Time("chunk_start", chunk[0]).Msg("process chunk failed")
			runSummary.Status = data.RunFailed
			return
		}
		totalObs += len(chunk)
	}

	runSummary.NumObservations = totalObs
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./...`
Expected: clean build.

- [ ] **Step 4: Run unit tests to ensure nothing regressed**

Run: `ginkgo run -race ./provider/pvindex/`
Expected: PASS (the existing unit tests don't depend on processChunk, so they should still pass).

- [ ] **Step 5: Commit**

```bash
git add provider/pvindex/chunk.go provider/pvindex/pvindex.go
git commit -m "feat(pvindex): add chunk processor and Fetch wiring"
```

---

### Task 13: Integration Test (SCOPED OUT)

**This task was removed during execution.** Per project preference, pv-data does not have build-tag integration tests that depend on a real database connection. The filter-chain logic is covered by the unit tests in Task 9 with synthetic in-memory fixtures.

The original integration test (a build-tag `integration_test.go` that called `pgxpool.New` and ran the loaders against `PV_TEST_DATABASE_URL`) did catch two real production bugs in the DB-side SQL: `trading_days(date, date)` was being called with `date + INTERVAL` arguments that resolve to `timestamp without time zone`, causing `function does not exist` errors at runtime. Both occurrences (in `computeDateRange` and `loadTrailingWindow`) were fixed in commit `a25e617` with explicit `::date` casts. The integration test itself was then removed.

The loader functions (`loadCandidateAssets`, `loadEodChunk`, `loadMarketCapAsOf`, `loadBroadMarketCaps`, `computeDateRange`, `chunkTradingDays`, `loadTrailingWindow`) remain untested by automated tests. They are thin SQL wrappers and should be exercised manually via Task 14 step 5 (smoke test against real DB) before merging.

Original spec preserved below for reference:

**Files:**
- Create: `provider/pvindex/integration_test.go`

- [ ] **Step 1: Create the integration test**

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

//go:build integration

package pvindex

import (
	"context"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("pvindex integration", Ordered, func() {
	var (
		ctx  context.Context
		pool *pgxpool.Pool
	)

	BeforeAll(func() {
		ctx = context.Background()

		dburl := os.Getenv("PV_TEST_DATABASE_URL")
		if dburl == "" {
			Skip("PV_TEST_DATABASE_URL not set; skipping integration test")
		}

		var err error
		pool, err = pgxpool.New(ctx, dburl)
		Expect(err).ToNot(HaveOccurred())
	})

	AfterAll(func() {
		if pool != nil {
			pool.Close()
		}
	})

	It("computes a date range that brackets 1999 to today", func() {
		start, end, err := computeDateRange(ctx, pool)
		Expect(err).ToNot(HaveOccurred())
		Expect(start.Year()).To(BeNumerically(">=", 1999))
		Expect(start.Year()).To(BeNumerically("<=", 2000))
		Expect(end.After(start)).To(BeTrue())
	})

	It("loads candidate assets and finds at least 1000 of them", func() {
		assets, err := loadCandidateAssets(ctx, pool)
		Expect(err).ToNot(HaveOccurred())
		Expect(len(assets)).To(BeNumerically(">=", 1000))
	})

	It("loads broad market caps for a recent date", func() {
		asOf, _ := time.Parse("2006-01-02", "2024-01-08")
		caps, err := loadBroadMarketCaps(ctx, pool, asOf)
		Expect(err).ToNot(HaveOccurred())
		Expect(len(caps)).To(BeNumerically(">=", 1000))
	})

	It("loads EOD chunk for one stock over 30 days", func() {
		start, _ := time.Parse("2006-01-02", "2024-01-01")
		end, _ := time.Parse("2006-01-02", "2024-01-31")
		out, err := loadEodChunk(ctx, pool, []string{"BBG000B9XRY4"}, start, end) // AAPL
		Expect(err).ToNot(HaveOccurred())
		Expect(out).To(HaveKey("BBG000B9XRY4"))
		Expect(len(out["BBG000B9XRY4"])).To(BeNumerically(">=", 15)) // ~20 trading days in Jan
	})

	// End-to-end test: actually compute the universe for one date and verify
	// known stocks are included/excluded.
	It("computes the universe for 2024-12-31 with expected membership", func() {
		evalDate, _ := time.Parse("2006-01-02", "2024-12-31")
		windowStart, _ := time.Parse("2006-01-02", "2024-03-01")
		windowEnd, _ := time.Parse("2006-01-02", "2024-12-30")

		assets, err := loadCandidateAssets(ctx, pool)
		Expect(err).ToNot(HaveOccurred())

		figis := make([]string, len(assets))
		for i, a := range assets {
			figis[i] = a.CompositeFigi
		}

		eodByFigi, err := loadEodChunk(ctx, pool, figis, windowStart, windowEnd)
		Expect(err).ToNot(HaveOccurred())

		mcapByFigi, err := loadMarketCapAsOf(ctx, pool, figis, evalDate)
		Expect(err).ToNot(HaveOccurred())

		broadMcaps, err := loadBroadMarketCaps(ctx, pool, evalDate)
		Expect(err).ToNot(HaveOccurred())

		// Approximate trading day count: count rows in the trading_days function for the window.
		conn, err := pool.Acquire(ctx)
		Expect(err).ToNot(HaveOccurred())
		defer conn.Release()

		var tdCount int
		err = conn.QueryRow(ctx, `SELECT COUNT(*) FROM trading_days($1::date, $2::date)`, windowStart, windowEnd).Scan(&tdCount)
		Expect(err).ToNot(HaveOccurred())

		input := perDayInput{
			Date:            evalDate,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: tdCount,
			Assets:          assets,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		universe := computeUniverseForDate(input)

		// Sanity bounds.
		Expect(len(universe)).To(BeNumerically(">", 500))
		Expect(len(universe)).To(BeNumerically("<", 4000))

		// AAPL must be in the universe.
		Expect(universe).To(HaveKey("AAPL"))
		// Weights sum to 1.0 within float tolerance.
		var sum float64
		for _, m := range universe {
			sum += m.Weight
		}
		Expect(sum).To(BeNumerically("~", 1.0, 1e-6))
	})
})
```

- [ ] **Step 2: Run the integration tests**

Run: `PV_TEST_DATABASE_URL="postgres://pennyvault@localhost:5432/pvdb?sslmode=disable" ginkgo run -race --tags=integration ./provider/pvindex/`
Expected: PASS for all 5 specs. The end-to-end test should report a universe size in the 1500-2500 range with weights summing to 1.0.

If the database URL or credentials differ, adjust the env var. The test skips when `PV_TEST_DATABASE_URL` is unset.

- [ ] **Step 3: Commit**

```bash
git add provider/pvindex/integration_test.go
git commit -m "test(pvindex): add integration tests against canonical views"
```

---

### Task 14: Lint, Final Build, Spec & Plan Commit

Wrap up: run linters, full build, and commit the design spec and plan together with the implementation.

- [ ] **Step 1: Run lint**

Run: `make lint`
Expected: zero errors. Fix any reported issues with `golangci-lint run --fix` and re-run.

- [ ] **Step 2: Full unit test run**

Run: `make test`
Expected: zero failures across the entire repo.

- [ ] **Step 3: Full build**

Run: `make build`
Expected: clean build of the `pvdata` binary.

- [ ] **Step 4: Commit the spec and plan**

The design spec and implementation plan have been on disk since the brainstorming session. Commit them now alongside a final tag commit:

```bash
git add docs/superpowers/specs/2026-04-07-pvindex-tradable-universe-design.md docs/superpowers/plans/2026-04-07-pvindex-tradable-universe.md
git commit -m "docs: add pvindex tradable universe design spec and implementation plan"
```

- [ ] **Step 5: Manual smoke test against real DB (optional, requires DB access)**

If you want to verify the provider runs end-to-end through the normal subscription path (not just unit/integration tests):

1. Create a `pvindex` subscription via the CLI: `pvdata subscription add` and select provider `pvindex`, dataset `US Tradable Universe`, with config `start_date_override = 2024-12-01` to limit the run.
2. Run it on demand: `pvdata run <subscription-id>`.
3. Inspect the resulting tables: `SELECT COUNT(*), MIN(snapshot_date), MAX(snapshot_date) FROM <pvindex_index_snapshot_table>;` and `SELECT COUNT(*) FROM <pvindex_index_changelog_table>;`.

Expected:
- 1 snapshot (the year boundary at 2025-01-01, plus the cold-start day 2024-12-02).
- Several hundred to several thousand changelog rows from the daily computation.
- No errors in `run_history` for the subscription.

This step is optional but recommended for the human reviewer before merging.
