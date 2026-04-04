# Provider Sub-Package Refactoring Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move every provider into its own sub-package under `provider/` so providers are isolated and the core package contains only shared infrastructure.

**Architecture:** The `provider/` package retains shared types (`Provider`, `Dataset`, `IndexMember`, helpers). Each provider moves to `provider/<name>/` with its own package. `discover.go` imports all sub-packages and builds the registry. All currently unexported shared functions (`diffSnapshots`, `emitChangelog`, etc.) are exported so sub-packages can use them.

**Tech Stack:** Go (package restructuring only, no new dependencies)

---

## File Structure After Refactoring

**Core (`provider/`):**
- `provider.go` -- Provider interface, Dataset struct, LookbackFromContext
- `discover.go` -- imports all sub-packages, builds Map
- `subscription.go` -- NewSubscription
- `hooks.go` -- PostFetch hooks (AdjustEodPrices, PurgeExpiredData, ComputeAdjustedClose)
- `index_helpers.go` -- shared index types and functions (exported)
- `provider_suite_test.go` -- test suite
- `hooks_test.go` -- hook tests
- `index_helpers_test.go` -- helper tests

**Sub-packages:**
- `provider/fred/` -- fred.go
- `provider/ishares/` -- ishares.go, ishares_parser.go, ishares_etfs.json, ishares_parser_test.go, ishares_embed_test.go
- `provider/legacy/` -- legacy.go, legacy_test.go
- `provider/massive/` -- massive.go (registered as both "massive" and "polygon")
- `provider/nasdaq/` -- nasdaq.go
- `provider/sharadar/` -- sharadar.go, sharadar_fundamentals.go, sharadar_import.go, sharadar_metrics.go, sharadar_tickers.go, sharadar_import_test.go
- `provider/tiingo/` -- tiingo.go
- `provider/yfinance/` -- yfinance_streamer.go (not registered in Map)
- `provider/zacks/` -- zacks.go

---

### Task 1: Export shared functions in index_helpers.go

The index helper functions are currently unexported. Sub-packages need access to them. Export the types and functions.

**Files:**
- Modify: `provider/index_helpers.go`
- Modify: `provider/index_helpers_test.go`
- Modify: `provider/ishares.go` (update references to renamed types)
- Modify: `provider/nasdaq.go` (update references to renamed types)

- [ ] **Step 1: Export types and functions in index_helpers.go**

Rename all unexported identifiers to exported:

| Before | After |
|--------|-------|
| `indexMember` | `IndexMember` |
| `weightChangeThreshold` | `WeightChangeThreshold` |
| `shouldTakeSnapshot` | `ShouldTakeSnapshot` |
| `diffSnapshots` | `DiffSnapshots` |
| `lastSnapshotDate` | `LastSnapshotDate` |
| `previousSnapshotTickers` | `PreviousSnapshotTickers` |
| `currentIndexMembers` | `CurrentIndexMembers` |
| `emitWeightChanges` | `EmitWeightChanges` |
| `tradingDays` | `TradingDays` |
| `emitChangelog` | `EmitChangelog` |
| `resolveShareClass` | `ResolveShareClass` |
| `firstWordsMatch` | `FirstWordsMatch` |
| `jaroWinklerThreshold` | `JaroWinklerThreshold` |

Also export struct fields are already exported (`CompositeFigi`, `Weight`), so `IndexMember` is the only struct rename needed.

- [ ] **Step 2: Update index_helpers_test.go**

Update test references to use the exported names (e.g. `shouldTakeSnapshot` -> `ShouldTakeSnapshot`, `diffSnapshots` -> `DiffSnapshots`, `indexMember` -> `IndexMember`).

- [ ] **Step 3: Update ishares.go references**

Update all references in `provider/ishares.go`:
- `indexMember` -> `IndexMember`
- `diffSnapshots` -> `DiffSnapshots`
- `emitChangelog` -> `EmitChangelog`
- `emitWeightChanges` -> `EmitWeightChanges`
- `currentIndexMembers` -> `CurrentIndexMembers`
- `lastSnapshotDate` -> `LastSnapshotDate`
- `shouldTakeSnapshot` -> `ShouldTakeSnapshot`
- `tradingDays` -> `TradingDays`
- `resolveShareClass` -> `ResolveShareClass`

- [ ] **Step 4: Update nasdaq.go references**

Update all references in `provider/nasdaq.go`:
- `previousSnapshotTickers` -> `PreviousSnapshotTickers`
- `diffSnapshots` -> `DiffSnapshots`
- `emitChangelog` -> `EmitChangelog`
- `lastSnapshotDate` -> `LastSnapshotDate`
- `shouldTakeSnapshot` -> `ShouldTakeSnapshot`
- `indexMember` -> `IndexMember`

- [ ] **Step 5: Update ishares_parser_test.go references**

Update `resolveShareClass` -> `ResolveShareClass` in test calls.

- [ ] **Step 6: Build and test**

Run: `go build ./... && ginkgo run -race ./provider/`
Expected: All tests pass.

- [ ] **Step 7: Lint**

Run: `golangci-lint run --fix ./provider/...`

- [ ] **Step 8: Commit**

```bash
git add provider/
git commit -m "refactor: export shared index helper functions for sub-package access"
```

---

### Task 2: Move Fred to provider/fred/

**Files:**
- Create: `provider/fred/fred.go` (move from `provider/fred.go`)
- Modify: `provider/discover.go`
- Delete: `provider/fred.go`

- [ ] **Step 1: Create provider/fred/ directory and move the file**

```bash
mkdir -p provider/fred
mv provider/fred.go provider/fred/fred.go
```

- [ ] **Step 2: Update package declaration**

In `provider/fred/fred.go`, change:
```go
package provider
```
to:
```go
package fred
```

- [ ] **Step 3: Update type references**

In `provider/fred/fred.go`:
- Add import: `"github.com/penny-vault/pvdata/provider"`
- Change `Dataset` -> `provider.Dataset`
- Change `Provider` interface references if any
- Change any `data.DataType` references (these should already use the `data` package directly)

Check the imports -- `fred.go` likely uses `data`, `library`, `zerolog`, and `resty`. The only provider-package type it needs is `Dataset`.

- [ ] **Step 4: Export the type**

`Fred` struct is already exported. Verify `Name()`, `ConfigDescription()`, `Description()`, `Datasets()` are all exported (they should be since they implement the interface).

- [ ] **Step 5: Update discover.go**

Add import and update Map:
```go
import (
	"github.com/penny-vault/pvdata/provider/fred"
)

// In Map:
"fred": &fred.Fred{},
```

- [ ] **Step 6: Build and test**

Run: `go build ./... && ginkgo run -race ./provider/... ./provider/fred/...`
Expected: All tests pass.

- [ ] **Step 7: Commit**

```bash
git add provider/
git commit -m "refactor: move Fred provider to provider/fred sub-package"
```

---

### Task 3: Move Legacy to provider/legacy/

**Files:**
- Create: `provider/legacy/legacy.go` (move from `provider/legacy.go`)
- Create: `provider/legacy/legacy_test.go` (move from `provider/legacy_test.go`)
- Modify: `provider/discover.go`

- [ ] **Step 1: Create directory and move files**

```bash
mkdir -p provider/legacy
mv provider/legacy.go provider/legacy/legacy.go
mv provider/legacy_test.go provider/legacy/legacy_test.go
```

- [ ] **Step 2: Update package declarations**

Change `package provider` to `package legacy` in both files.

- [ ] **Step 3: Update type references in legacy.go**

Add import `"github.com/penny-vault/pvdata/provider"` and qualify `Dataset` as `provider.Dataset`.

- [ ] **Step 4: Update test file**

In `legacy_test.go`, the test suite setup references may need updating. If it has its own `TestLegacy` function, add a suite file or adjust. If it uses the parent suite, create `provider/legacy/legacy_suite_test.go`:

```go
package legacy

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestLegacy(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Legacy Suite")
}
```

- [ ] **Step 5: Update discover.go**

Add import and update Map entry.

- [ ] **Step 6: Build and test**

Run: `go build ./... && ginkgo run -race ./provider/... ./provider/legacy/...`

- [ ] **Step 7: Commit**

```bash
git add provider/
git commit -m "refactor: move Legacy provider to provider/legacy sub-package"
```

---

### Task 4: Move Massive to provider/massive/

**Files:**
- Create: `provider/massive/massive.go` (move from `provider/massive.go`)
- Modify: `provider/discover.go`

- [ ] **Step 1: Create directory and move file**

```bash
mkdir -p provider/massive
mv provider/massive.go provider/massive/massive.go
```

- [ ] **Step 2: Update package declaration and imports**

Change `package provider` to `package massive`. Add `"github.com/penny-vault/pvdata/provider"` import and qualify `Dataset` references.

- [ ] **Step 3: Update discover.go**

Both "massive" and "polygon" entries point to `&Massive{}`. Update:

```go
import (
	"github.com/penny-vault/pvdata/provider/massive"
)

// In Map:
"massive": &massive.Massive{},
"polygon": &massive.Massive{},
```

- [ ] **Step 4: Build and test**

Run: `go build ./... && ginkgo run -race ./provider/...`

- [ ] **Step 5: Commit**

```bash
git add provider/
git commit -m "refactor: move Massive provider to provider/massive sub-package"
```

---

### Task 5: Move Tiingo to provider/tiingo/

**Files:**
- Create: `provider/tiingo/tiingo.go` (move from `provider/tiingo.go`)
- Modify: `provider/discover.go`

- [ ] **Step 1: Create directory and move file**

```bash
mkdir -p provider/tiingo
mv provider/tiingo.go provider/tiingo/tiingo.go
```

- [ ] **Step 2: Update package declaration and imports**

Change `package provider` to `package tiingo`. Add `"github.com/penny-vault/pvdata/provider"` import. Qualify:
- `Dataset` -> `provider.Dataset`
- `LookbackFromContext` -> `provider.LookbackFromContext`
- `AdjustEodPrices` -> `provider.AdjustEodPrices`

- [ ] **Step 3: Update discover.go**

Add import and update Map entry.

- [ ] **Step 4: Build and test**

Run: `go build ./... && ginkgo run -race ./provider/... ./provider/tiingo/...`

- [ ] **Step 5: Commit**

```bash
git add provider/
git commit -m "refactor: move Tiingo provider to provider/tiingo sub-package"
```

---

### Task 6: Move Nasdaq to provider/nasdaq/

**Files:**
- Create: `provider/nasdaq/nasdaq.go` (move from `provider/nasdaq.go`)
- Modify: `provider/discover.go`

- [ ] **Step 1: Create directory and move file**

```bash
mkdir -p provider/nasdaq
mv provider/nasdaq.go provider/nasdaq/nasdaq.go
```

- [ ] **Step 2: Update package declaration and imports**

Change `package provider` to `package nasdaq`. Add `"github.com/penny-vault/pvdata/provider"` import. Qualify:
- `Dataset` -> `provider.Dataset`
- `IndexMember` -> `provider.IndexMember`
- `PreviousSnapshotTickers` -> `provider.PreviousSnapshotTickers`
- `DiffSnapshots` -> `provider.DiffSnapshots`
- `EmitChangelog` -> `provider.EmitChangelog`
- `LastSnapshotDate` -> `provider.LastSnapshotDate`
- `ShouldTakeSnapshot` -> `provider.ShouldTakeSnapshot`

- [ ] **Step 3: Update discover.go**

Add import and update Map entry.

- [ ] **Step 4: Build and test**

Run: `go build ./... && ginkgo run -race ./provider/... ./provider/nasdaq/...`

- [ ] **Step 5: Commit**

```bash
git add provider/
git commit -m "refactor: move Nasdaq provider to provider/nasdaq sub-package"
```

---

### Task 7: Move Zacks to provider/zacks/

**Files:**
- Create: `provider/zacks/zacks.go` (move from `provider/zacks.go`)
- Modify: `provider/discover.go`

- [ ] **Step 1: Create directory and move file**

```bash
mkdir -p provider/zacks
mv provider/zacks.go provider/zacks/zacks.go
```

- [ ] **Step 2: Update package declaration and imports**

Change `package provider` to `package zacks`. Add `"github.com/penny-vault/pvdata/provider"` import and qualify `Dataset` references.

- [ ] **Step 3: Update discover.go**

Add import and update Map entry.

- [ ] **Step 4: Build and test**

Run: `go build ./... && ginkgo run -race ./provider/... ./provider/zacks/...`

- [ ] **Step 5: Commit**

```bash
git add provider/
git commit -m "refactor: move Zacks provider to provider/zacks sub-package"
```

---

### Task 8: Move Sharadar to provider/sharadar/

**Files:**
- Create: `provider/sharadar/` directory with all sharadar files
- Modify: `provider/discover.go`

- [ ] **Step 1: Create directory and move files**

```bash
mkdir -p provider/sharadar
mv provider/sharadar.go provider/sharadar/sharadar.go
mv provider/sharadar_fundamentals.go provider/sharadar/sharadar_fundamentals.go
mv provider/sharadar_import.go provider/sharadar/sharadar_import.go
mv provider/sharadar_metrics.go provider/sharadar/sharadar_metrics.go
mv provider/sharadar_tickers.go provider/sharadar/sharadar_tickers.go
mv provider/sharadar_import_test.go provider/sharadar/sharadar_import_test.go
```

- [ ] **Step 2: Update package declarations**

Change `package provider` to `package sharadar` in all 6 files.

- [ ] **Step 3: Update type references**

Add `"github.com/penny-vault/pvdata/provider"` import to files that reference `Dataset` or `FileImporter`. Qualify:
- `Dataset` -> `provider.Dataset`
- `FileImporter` interface if referenced

- [ ] **Step 4: Create test suite file**

Create `provider/sharadar/sharadar_suite_test.go`:

```go
package sharadar

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestSharadar(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Sharadar Suite")
}
```

- [ ] **Step 5: Update discover.go**

Add import and update Map entry.

- [ ] **Step 6: Build and test**

Run: `go build ./... && ginkgo run -race ./provider/... ./provider/sharadar/...`

- [ ] **Step 7: Commit**

```bash
git add provider/
git commit -m "refactor: move Sharadar provider to provider/sharadar sub-package"
```

---

### Task 9: Move iShares to provider/ishares/

**Files:**
- Create: `provider/ishares/` directory with all ishares files
- Modify: `provider/discover.go`

- [ ] **Step 1: Create directory and move files**

```bash
mkdir -p provider/ishares
mv provider/ishares.go provider/ishares/ishares.go
mv provider/ishares_parser.go provider/ishares/ishares_parser.go
mv provider/ishares_etfs.json provider/ishares/ishares_etfs.json
mv provider/ishares_parser_test.go provider/ishares/ishares_parser_test.go
mv provider/ishares_embed_test.go provider/ishares/ishares_embed_test.go
```

- [ ] **Step 2: Update package declarations**

Change `package provider` to `package ishares` in all `.go` files.

- [ ] **Step 3: Update type references in ishares.go**

Add `"github.com/penny-vault/pvdata/provider"` import. Qualify:
- `Dataset` -> `provider.Dataset`
- `LookbackFromContext` -> `provider.LookbackFromContext`
- `IndexMember` -> `provider.IndexMember`
- `DiffSnapshots` -> `provider.DiffSnapshots`
- `EmitChangelog` -> `provider.EmitChangelog`
- `EmitWeightChanges` -> `provider.EmitWeightChanges`
- `CurrentIndexMembers` -> `provider.CurrentIndexMembers`
- `LastSnapshotDate` -> `provider.LastSnapshotDate`
- `ShouldTakeSnapshot` -> `provider.ShouldTakeSnapshot`
- `TradingDays` -> `provider.TradingDays`
- `ResolveShareClass` -> `provider.ResolveShareClass`

- [ ] **Step 4: Create test suite file**

Create `provider/ishares/ishares_suite_test.go`:

```go
package ishares

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestIShares(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "IShares Suite")
}
```

- [ ] **Step 5: Update discover.go**

Add import and update Map entry.

- [ ] **Step 6: Build and test**

Run: `go build ./... && ginkgo run -race ./provider/... ./provider/ishares/...`

- [ ] **Step 7: Commit**

```bash
git add provider/
git commit -m "refactor: move iShares provider to provider/ishares sub-package"
```

---

### Task 10: Move YFinance to provider/yfinance/

**Files:**
- Create: `provider/yfinance/yfinance_streamer.go` (move from `provider/yfinance_streamer.go`)
- Also move: `provider/yfinance.pb.go` -> `provider/yfinance/yfinance.pb.go`

- [ ] **Step 1: Create directory and move files**

```bash
mkdir -p provider/yfinance
mv provider/yfinance_streamer.go provider/yfinance/yfinance_streamer.go
mv provider/yfinance.pb.go provider/yfinance/yfinance.pb.go
```

- [ ] **Step 2: Update package declarations**

Change `package provider` to `package yfinance` in both files.

- [ ] **Step 3: Update type references**

Add `"github.com/penny-vault/pvdata/provider"` import and qualify `Dataset` references.

Note: YFinance is NOT registered in the Map -- do not add it to discover.go.

- [ ] **Step 4: Build and test**

Run: `go build ./...`

- [ ] **Step 5: Commit**

```bash
git add provider/
git commit -m "refactor: move YFinance provider to provider/yfinance sub-package"
```

---

### Task 11: Move integration tests

**Files:**
- Modify: `provider/integration_test.go`

- [ ] **Step 1: Update integration test imports**

The integration test file references `parseISharesCSV` which will now be in the `ishares` sub-package. Since this is an integration test with build tag `integration`, it should either:
- Move to `provider/ishares/` if it only tests iShares
- Stay in `provider/` but import the sub-packages

Check the contents and decide. If it tests multiple providers, keep it in `provider/` and update imports. If it's iShares-only, move it.

- [ ] **Step 2: Build and test**

Run: `go build ./...`

- [ ] **Step 3: Commit**

```bash
git add provider/
git commit -m "refactor: update integration tests for sub-package structure"
```

---

### Task 12: Clean up and verify

- [ ] **Step 1: Verify no provider files remain in provider/ root**

```bash
ls provider/*.go
```

Expected remaining files:
- `provider.go`
- `discover.go`
- `subscription.go`
- `hooks.go`
- `index_helpers.go`
- `provider_suite_test.go`
- `hooks_test.go`
- `index_helpers_test.go`
- `integration_test.go` (if kept at root)

- [ ] **Step 2: Full build and test**

```bash
go build ./...
ginkgo run -race ./provider/...
```

Expected: All tests pass across all sub-packages.

- [ ] **Step 3: Lint**

```bash
golangci-lint run --fix ./provider/...
```

- [ ] **Step 4: Commit any cleanup**

```bash
git add -A
git commit -m "refactor: finalize provider sub-package restructuring"
```
