# SEC Market-Data Field Enrichment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Compute 14 market-data fields (price, market_cap, PE, PB, PS, etc.) on SEC fundamentals by looking up EOD close prices from the published `eod` view.

**Architecture:** A new `enrichMarketData` function accepts a slice of `*data.Fundamental` for one company and a price-lookup function. It groups fundamentals by DateKey, looks up the EOD close for each EventDate, computes market-data fields on all dimensions, and copies ratio fields from trailing to quarterly dimensions. The price-lookup function is injectable for testability. In `emitFundamentals`, observations are buffered instead of sent immediately, enriched after all are built, then sent.

**Tech Stack:** Go 1.25, pgx/v5 (EOD queries), Ginkgo v2 + Gomega (tests)

---

### Task 1: Pure market-data computation logic

**Files:**
- Create: `provider/sec/market_data.go`
- Create: `provider/sec/market_data_test.go`

This task creates the core computation function that populates the 14 market-data fields on `*data.Fundamental` structs, given a price lookup function. No database access.

- [ ] **Step 1: Write failing tests**

In `provider/sec/market_data_test.go`:

```go
// Copyright 2024
// SPDX-License-Identifier: Apache-2.0

package sec

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pvdata/data"
)

var _ = Describe("EnrichMarketData", func() {
	// stubPriceFn returns a fixed price for any lookup.
	stubPriceFn := func(price float64) PriceLookupFn {
		return func(compositeFigi string, eventDate time.Time) float64 {
			return price
		}
	}

	// noPriceFn simulates missing EOD data.
	noPriceFn := stubPriceFn(0)

	Describe("trailing/annual dimension fields", func() {
		It("computes all 14 fields on an ART record", func() {
			art := &data.Fundamental{
				CompositeFigi:                "BBG000TEST01",
				Dimension:                    "ART",
				EventDate:                    time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
				DateKey:                      time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC),
				SharesBasic:                  15000000000,
				TotalDebt:                    100000000000,
				CashAndEquivalents:           30000000000,
				Equity:                       70000000000,
				NetIncomeCommonStock:         100000000000,
				Revenues:                     400000000000,
				EPS:                          6.5,
				EPSDiluted:                   6.3,
				SalesPerShare:                26.0,
				EBIT:                         130000000000,
				EBITDA:                       140000000000,
				DividendsPerBasicCommonShare: 1.0,
			}

			fundamentals := []*data.Fundamental{art}
			EnrichMarketData(fundamentals, stubPriceFn(236.0))

			Expect(art.Price).To(BeNumerically("~", 236.0, 0.001))
			Expect(art.MarketCapitalization).To(Equal(int64(236.0 * 15000000000)))
			Expect(art.EnterpriseValue).To(Equal(art.MarketCapitalization + 100000000000 - 30000000000))
			Expect(art.ShareFactor).To(BeNumerically("~", 1.0, 0.001))
			Expect(art.FxUSD).To(BeNumerically("~", 1.0, 0.001))

			// Ratios
			expectedMktCap := float64(art.MarketCapitalization)
			Expect(art.PE).To(BeNumerically("~", expectedMktCap/100000000000, 0.001))
			Expect(art.PB).To(BeNumerically("~", expectedMktCap/70000000000, 0.001))
			Expect(art.PS).To(BeNumerically("~", expectedMktCap/400000000000, 0.001))
			Expect(art.PE1).To(BeNumerically("~", 236.0/6.5, 0.001))
			Expect(art.PS1).To(BeNumerically("~", 236.0/26.0, 0.001))
			Expect(art.EVtoEBIT).To(Equal(int64(float64(art.EnterpriseValue) / 130000000000)))
			Expect(art.EVtoEBITDA).To(BeNumerically("~", float64(art.EnterpriseValue)/140000000000, 0.001))
			Expect(art.DividendYield).To(BeNumerically("~", 1.0/236.0, 0.00001))
			Expect(art.PayoutRatio).To(BeNumerically("~", 1.0/6.3, 0.001))
		})
	})

	Describe("quarterly dimension copies from trailing", func() {
		It("copies ratio fields from ART to ARQ at the same DateKey", func() {
			dateKey := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

			art := &data.Fundamental{
				CompositeFigi: "BBG000TEST01", Dimension: "ART",
				EventDate: time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
				DateKey:   dateKey,
				SharesBasic: 15000000000, TotalDebt: 100000000000,
				CashAndEquivalents: 30000000000, Equity: 70000000000,
				NetIncomeCommonStock: 100000000000, Revenues: 400000000000,
				EPS: 6.5, EPSDiluted: 6.3, SalesPerShare: 26.0,
				EBIT: 130000000000, EBITDA: 140000000000,
				DividendsPerBasicCommonShare: 1.0,
			}

			arq := &data.Fundamental{
				CompositeFigi: "BBG000TEST01", Dimension: "ARQ",
				EventDate: time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
				DateKey:   dateKey,
				SharesBasic: 15000000000, TotalDebt: 100000000000,
				CashAndEquivalents: 30000000000, Equity: 70000000000,
				NetIncomeCommonStock: 36000000000, Revenues: 124000000000,
			}

			EnrichMarketData([]*data.Fundamental{art, arq}, stubPriceFn(236.0))

			// ARQ gets its own price, market_cap, EV, PB
			Expect(arq.Price).To(BeNumerically("~", 236.0, 0.001))
			Expect(arq.MarketCapitalization).To(Equal(art.MarketCapitalization))
			Expect(arq.PB).To(BeNumerically("~", float64(arq.MarketCapitalization)/70000000000, 0.001))

			// ARQ copies ratio fields from ART
			Expect(arq.PE).To(Equal(art.PE))
			Expect(arq.PS).To(Equal(art.PS))
			Expect(arq.PE1).To(Equal(art.PE1))
			Expect(arq.PS1).To(Equal(art.PS1))
			Expect(arq.EVtoEBIT).To(Equal(art.EVtoEBIT))
			Expect(arq.EVtoEBITDA).To(Equal(art.EVtoEBITDA))
			Expect(arq.DividendYield).To(Equal(art.DividendYield))
			Expect(arq.PayoutRatio).To(Equal(art.PayoutRatio))
		})

		It("copies from MRT to MRQ", func() {
			dateKey := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

			mrt := &data.Fundamental{
				CompositeFigi: "BBG000TEST01", Dimension: "MRT",
				EventDate: time.Date(2024, 12, 28, 0, 0, 0, 0, time.UTC),
				DateKey: dateKey,
				SharesBasic: 15000000000, TotalDebt: 100000000000,
				CashAndEquivalents: 30000000000, Equity: 70000000000,
				NetIncomeCommonStock: 100000000000, Revenues: 400000000000,
				EPS: 6.5, EPSDiluted: 6.3, SalesPerShare: 26.0,
				EBIT: 130000000000, EBITDA: 140000000000,
				DividendsPerBasicCommonShare: 1.0,
			}

			mrq := &data.Fundamental{
				CompositeFigi: "BBG000TEST01", Dimension: "MRQ",
				EventDate: time.Date(2024, 12, 28, 0, 0, 0, 0, time.UTC),
				DateKey: dateKey,
				SharesBasic: 15000000000, Equity: 70000000000,
			}

			EnrichMarketData([]*data.Fundamental{mrt, mrq}, stubPriceFn(255.0))

			Expect(mrq.PE).To(Equal(mrt.PE))
			Expect(mrq.PS).To(Equal(mrt.PS))
		})
	})

	Describe("division by zero", func() {
		It("leaves ratio at zero when denominator is zero", func() {
			art := &data.Fundamental{
				CompositeFigi: "BBG000TEST01", Dimension: "ART",
				EventDate: time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
				DateKey:   time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC),
				SharesBasic: 15000000000,
				NetIncomeCommonStock: 0, // zero denominator
				Revenues: 0,
				Equity:   0,
				EPS:      0,
				EBIT:     0,
				EBITDA:   0,
			}

			EnrichMarketData([]*data.Fundamental{art}, stubPriceFn(236.0))

			Expect(art.Price).To(BeNumerically("~", 236.0, 0.001))
			Expect(art.MarketCapitalization).To(BeNumerically(">", 0))
			Expect(art.PE).To(BeNumerically("==", 0))
			Expect(art.PB).To(BeNumerically("==", 0))
			Expect(art.PS).To(BeNumerically("==", 0))
			Expect(art.PE1).To(BeNumerically("==", 0))
			Expect(art.EVtoEBIT).To(Equal(int64(0)))
		})
	})

	Describe("missing EOD price", func() {
		It("leaves all market-data fields at zero when price is zero", func() {
			art := &data.Fundamental{
				CompositeFigi: "BBG000TEST01", Dimension: "ART",
				EventDate: time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
				DateKey:   time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC),
				SharesBasic: 15000000000,
				NetIncomeCommonStock: 100000000000,
			}

			EnrichMarketData([]*data.Fundamental{art}, noPriceFn)

			Expect(art.Price).To(BeNumerically("==", 0))
			Expect(art.MarketCapitalization).To(Equal(int64(0)))
			Expect(art.PE).To(BeNumerically("==", 0))
		})
	})

	Describe("pe1 uses basic EPS", func() {
		It("computes pe1 from EPS not EPSDiluted", func() {
			art := &data.Fundamental{
				CompositeFigi: "BBG000TEST01", Dimension: "ART",
				EventDate: time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
				DateKey:   time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC),
				SharesBasic: 15000000000,
				EPS:        6.5,
				EPSDiluted: 6.3,
			}

			EnrichMarketData([]*data.Fundamental{art}, stubPriceFn(236.0))

			// pe1 = price / eps (basic)
			Expect(art.PE1).To(BeNumerically("~", 236.0/6.5, 0.001))
			// NOT price / eps_diluted (which would be 236/6.3 = 37.46)
			Expect(art.PE1).NotTo(BeNumerically("~", 236.0/6.3, 0.001))
		})
	})

	Describe("PB computed independently for quarterly", func() {
		It("uses the quarterly dimension's own equity for PB", func() {
			dateKey := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)

			art := &data.Fundamental{
				CompositeFigi: "BBG000TEST01", Dimension: "ART",
				EventDate: time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
				DateKey: dateKey,
				SharesBasic: 15000000000, Equity: 70000000000,
				NetIncomeCommonStock: 100000000000, Revenues: 400000000000,
				EPS: 6.5, EPSDiluted: 6.3, SalesPerShare: 26.0,
				EBIT: 130000000000, EBITDA: 140000000000,
				TotalDebt: 100000000000, CashAndEquivalents: 30000000000,
				DividendsPerBasicCommonShare: 1.0,
			}

			arq := &data.Fundamental{
				CompositeFigi: "BBG000TEST01", Dimension: "ARQ",
				EventDate: time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
				DateKey: dateKey,
				SharesBasic: 15000000000,
				Equity: 65000000000, // different equity than ART
				TotalDebt: 100000000000, CashAndEquivalents: 30000000000,
			}

			EnrichMarketData([]*data.Fundamental{art, arq}, stubPriceFn(236.0))

			// ARQ PB uses its own equity (65B), not ART's (70B)
			expectedPB := float64(arq.MarketCapitalization) / 65000000000
			Expect(arq.PB).To(BeNumerically("~", expectedPB, 0.001))
			Expect(arq.PB).NotTo(Equal(art.PB))
		})
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race --focus "EnrichMarketData" ./provider/sec/`
Expected: FAIL -- `EnrichMarketData` and `PriceLookupFn` not defined.

- [ ] **Step 3: Implement EnrichMarketData**

In `provider/sec/market_data.go`:

```go
// Copyright 2024
// SPDX-License-Identifier: Apache-2.0

package sec

import (
	"math"
	"time"

	"github.com/penny-vault/pvdata/data"
)

// PriceLookupFn returns the unadjusted close price for a given composite FIGI
// and event date. Returns 0 if no price is available.
type PriceLookupFn func(compositeFigi string, eventDate time.Time) float64

// EnrichMarketData populates the 14 market-data fields on a slice of
// Fundamental records for a single company. It groups records by DateKey,
// looks up EOD close prices, computes fields on trailing/annual dimensions,
// and copies ratio fields from trailing to quarterly dimensions.
func EnrichMarketData(fundamentals []*data.Fundamental, lookupPrice PriceLookupFn) {
	// Group by DateKey so we can find trailing/quarterly pairs.
	type dateKeyGroup struct {
		fundamentals []*data.Fundamental
	}

	groups := make(map[time.Time]*dateKeyGroup)

	for _, f := range fundamentals {
		g, ok := groups[f.DateKey]
		if !ok {
			g = &dateKeyGroup{}
			groups[f.DateKey] = g
		}

		g.fundamentals = append(g.fundamentals, f)
	}

	for _, g := range groups {
		enrichGroup(g.fundamentals, lookupPrice)
	}
}

// enrichGroup enriches all fundamentals sharing the same DateKey.
func enrichGroup(fundamentals []*data.Fundamental, lookupPrice PriceLookupFn) {
	// Phase 1: compute price, market_cap, EV, PB, share_factor, fx_usd for all dimensions.
	for _, f := range fundamentals {
		price := lookupPrice(f.CompositeFigi, f.EventDate)
		if price == 0 {
			continue
		}

		f.Price = price
		f.ShareFactor = 1.0
		f.FxUSD = 1.0
		f.MarketCapitalization = int64(price * float64(f.SharesBasic))
		f.EnterpriseValue = f.MarketCapitalization + f.TotalDebt - f.CashAndEquivalents

		if f.Equity != 0 {
			f.PB = float64(f.MarketCapitalization) / float64(f.Equity)
		}
	}

	// Phase 2: compute ratio fields on trailing/annual dimensions.
	for _, f := range fundamentals {
		if f.Price == 0 {
			continue
		}

		dim := f.Dimension
		if dim != "ART" && dim != "MRT" && dim != "ARY" && dim != "MRY" {
			continue
		}

		mktCap := float64(f.MarketCapitalization)

		if f.NetIncomeCommonStock != 0 {
			f.PE = mktCap / float64(f.NetIncomeCommonStock)
		}

		if f.Revenues != 0 {
			f.PS = mktCap / float64(f.Revenues)
		}

		if f.EPS != 0 {
			f.PE1 = f.Price / f.EPS
		}

		if f.SalesPerShare != 0 {
			f.PS1 = f.Price / f.SalesPerShare
		}

		if f.EBIT != 0 {
			f.EVtoEBIT = int64(math.Round(float64(f.EnterpriseValue) / float64(f.EBIT)))
		}

		if f.EBITDA != 0 {
			f.EVtoEBITDA = float64(f.EnterpriseValue) / float64(f.EBITDA)
		}

		if f.Price != 0 {
			f.DividendYield = f.DividendsPerBasicCommonShare / f.Price
		}

		if f.EPSDiluted != 0 {
			f.PayoutRatio = f.DividendsPerBasicCommonShare / f.EPSDiluted
		}
	}

	// Phase 3: copy ratio fields from trailing to quarterly dimensions.
	// ART -> ARQ, MRT -> MRQ. Matched by DateKey (already grouped).
	trailingByPrefix := make(map[string]*data.Fundamental) // "AR" or "MR" -> trailing fundamental

	for _, f := range fundamentals {
		switch f.Dimension {
		case "ART":
			trailingByPrefix["AR"] = f
		case "MRT":
			trailingByPrefix["MR"] = f
		}
	}

	for _, f := range fundamentals {
		var prefix string

		switch f.Dimension {
		case "ARQ":
			prefix = "AR"
		case "MRQ":
			prefix = "MR"
		default:
			continue
		}

		trailing, ok := trailingByPrefix[prefix]
		if !ok {
			continue
		}

		f.PE = trailing.PE
		f.PS = trailing.PS
		f.PE1 = trailing.PE1
		f.PS1 = trailing.PS1
		f.EVtoEBIT = trailing.EVtoEBIT
		f.EVtoEBITDA = trailing.EVtoEBITDA
		f.DividendYield = trailing.DividendYield
		f.PayoutRatio = trailing.PayoutRatio
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `ginkgo run -race --focus "EnrichMarketData" ./provider/sec/`
Expected: PASS

- [ ] **Step 5: Run all SEC tests for regressions**

Run: `ginkgo run -race ./provider/sec/`
Expected: All tests pass.

- [ ] **Step 6: Commit**

```bash
git add provider/sec/market_data.go provider/sec/market_data_test.go
git commit -m "feat(sec): add EnrichMarketData to compute 14 market-data fields (#38)"
```

---

### Task 2: EOD price lookup function

**Files:**
- Modify: `provider/sec/market_data.go` (add `NewEODPriceLookup`)

This creates a `PriceLookupFn` that queries the published `eod` view for the most recent close on or before a given date.

- [ ] **Step 1: Implement the EOD price lookup factory**

Add to the bottom of `provider/sec/market_data.go`:

```go
// NewEODPriceLookup returns a PriceLookupFn that queries the given EOD view
// for the unadjusted close price on or before the event date. It looks back
// up to 7 calendar days to handle weekends and holidays. Returns 0 if no
// price is found.
//
// The returned function acquires and releases a connection per call. For bulk
// enrichment this is acceptable because the number of distinct event dates
// per company is small (typically < 100).
func NewEODPriceLookup(ctx context.Context, pool *pgxpool.Pool, eodViewName string) PriceLookupFn {
	return func(compositeFigi string, eventDate time.Time) float64 {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			log.Error().Err(err).Msg("acquire connection for EOD price lookup")
			return 0
		}
		defer conn.Release()

		var price float64

		err = conn.QueryRow(ctx,
			fmt.Sprintf(`SELECT close FROM %s
				WHERE composite_figi = $1 AND event_date <= $2 AND event_date >= $3
				ORDER BY event_date DESC LIMIT 1`, eodViewName),
			compositeFigi, eventDate, eventDate.AddDate(0, 0, -7),
		).Scan(&price)

		if err != nil {
			// No price found is normal for historical data without EOD coverage.
			return 0
		}

		return price
	}
}

// FindEODViewName looks up the published view name for the "eod" data type.
// Returns empty string if no published EOD view exists.
func FindEODViewName(ctx context.Context, pool *pgxpool.Pool) string {
	views, err := library.LoadPublishedViews(ctx, pool)
	if err != nil {
		log.Error().Err(err).Msg("load published views for EOD lookup")
		return ""
	}

	for _, v := range views {
		if v.DataTypeKey == "eod" {
			return v.ViewName
		}
	}

	return ""
}
```

Add these imports to the file's import block:

```go
"context"
"fmt"

"github.com/jackc/pgx/v5/pgxpool"
"github.com/penny-vault/pvdata/library"
"github.com/rs/zerolog/log"
```

- [ ] **Step 2: Run lint**

Run: `golangci-lint run --fix ./provider/sec/`
Expected: 0 issues.

- [ ] **Step 3: Run all SEC tests for regressions**

Run: `ginkgo run -race ./provider/sec/`
Expected: All tests pass.

- [ ] **Step 4: Commit**

```bash
git add provider/sec/market_data.go
git commit -m "feat(sec): add EOD price lookup and view discovery for market-data enrichment (#38)"
```

---

### Task 3: Wire enrichment into emitFundamentals

**Files:**
- Modify: `provider/sec/sec.go` (`emitFundamentals` function)

Change `emitFundamentals` to buffer observations, enrich them, then send to the output channel.

- [ ] **Step 1: Add a test verifying enrichment populates price on emitted observations**

In `provider/sec/market_data_test.go`, add a new `Describe` block:

```go
var _ = Describe("emitFundamentals market-data enrichment", func() {
	It("populates price on emitted fundamentals when priceLookupFn is set", func() {
		cf := &CompanyFacts{
			CIK:        999,
			EntityName: "TestCo",
			Facts: map[string][]Fact{
				"Revenues": {
					{Val: 100000, Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC), Form: "10-Q", Filed: time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)},
				},
			},
		}

		asset := AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST01", CIK: 999}
		sub := &library.Subscription{Name: "test"}
		out := make(chan *data.Observation, 100)
		numObs := 0

		// Set the package-level price lookup function for testing.
		SetPriceLookupFn(func(compositeFigi string, eventDate time.Time) float64 {
			return 150.0
		})
		defer SetPriceLookupFn(nil)

		emitFundamentals(cf, asset, sub, time.Time{}, out, &numObs)
		close(out)

		for obs := range out {
			if obs.Fundamental != nil {
				Expect(obs.Fundamental.Price).To(BeNumerically("~", 150.0, 0.001))
			}
		}
	})

	It("emits fundamentals with zero price when no lookup function is set", func() {
		cf := &CompanyFacts{
			CIK:        999,
			EntityName: "TestCo",
			Facts: map[string][]Fact{
				"Revenues": {
					{Val: 100000, Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC), Form: "10-Q", Filed: time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)},
				},
			},
		}

		asset := AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST01", CIK: 999}
		sub := &library.Subscription{Name: "test"}
		out := make(chan *data.Observation, 100)
		numObs := 0

		SetPriceLookupFn(nil)

		emitFundamentals(cf, asset, sub, time.Time{}, out, &numObs)
		close(out)

		for obs := range out {
			if obs.Fundamental != nil {
				Expect(obs.Fundamental.Price).To(BeNumerically("==", 0))
			}
		}
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `ginkgo run -race --focus "emitFundamentals market-data" ./provider/sec/`
Expected: FAIL -- `SetPriceLookupFn` not defined.

- [ ] **Step 3: Modify emitFundamentals to buffer and enrich**

In `provider/sec/market_data.go`, add a package-level variable and setter:

```go
// priceLookupFn is the active price lookup function. Set by fetchFundamentals
// when the published EOD view is available.
var priceLookupFn PriceLookupFn

// SetPriceLookupFn sets the package-level price lookup function. Passing nil
// disables market-data enrichment.
func SetPriceLookupFn(fn PriceLookupFn) {
	priceLookupFn = fn
}
```

In `provider/sec/sec.go`, make two changes to `emitFundamentals`:

**Change 1:** Instead of sending observations directly to `out`, buffer them. Replace every `out <- &data.Observation{...}` with appending to a local `var buffered []*data.Observation` slice. There are 6 places where observations are sent:
- ARY (line ~557)
- MRY (line ~567)
- ARQ (line ~684)
- MRQ (line ~696)
- ART (line ~777)
- MRT (line ~794)

For each, change from:

```go
out <- &data.Observation{
    Fundamental:      fundamental,
    ObservationDate:  calendarDate,
    SubscriptionID:   sub.ID,
    SubscriptionName: sub.Name,
}
```

To:

```go
buffered = append(buffered, &data.Observation{
    Fundamental:      fundamental,
    ObservationDate:  calendarDate,
    SubscriptionID:   sub.ID,
    SubscriptionName: sub.Name,
})
```

**Change 2:** After the TTM loop (after the coverage logging at the end of the function), add enrichment and emission:

```go
	// Enrich buffered observations with market-data fields if a price
	// lookup function is available.
	if priceLookupFn != nil {
		var fundamentals []*data.Fundamental

		for _, obs := range buffered {
			if obs.Fundamental != nil {
				fundamentals = append(fundamentals, obs.Fundamental)
			}
		}

		EnrichMarketData(fundamentals, priceLookupFn)
	}

	// Send all buffered observations to the output channel.
	for _, obs := range buffered {
		out <- obs
	}
```

**Change 3:** Declare the buffer at the top of the function (after `var quarters []quarterData`):

```go
	var buffered []*data.Observation
```

- [ ] **Step 4: Set up the price lookup in fetchFundamentals**

In `provider/sec/sec.go`, in `fetchFundamentals` (around line 75), after the CIK map is built and before `isBackfill` (around line 224), add:

```go
	// Set up EOD price lookup for market-data enrichment.
	eodView := FindEODViewName(ctx, sub.Library.Pool)
	if eodView != "" {
		SetPriceLookupFn(NewEODPriceLookup(ctx, sub.Library.Pool, eodView))
		log.Info().Str("eod_view", eodView).Msg("market-data enrichment enabled")
	} else {
		SetPriceLookupFn(nil)
		log.Warn().Msg("no published EOD view found; market-data fields will be zero")
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `ginkgo run -race --focus "emitFundamentals market-data" ./provider/sec/`
Expected: PASS

- [ ] **Step 6: Run all SEC tests for regressions**

Run: `ginkgo run -race ./provider/sec/`
Expected: All tests pass.

- [ ] **Step 7: Run lint**

Run: `golangci-lint run --fix ./provider/sec/`
Expected: 0 issues.

- [ ] **Step 8: Commit**

```bash
git add provider/sec/sec.go provider/sec/market_data.go provider/sec/market_data_test.go
git commit -m "feat(sec): wire market-data enrichment into emitFundamentals pipeline (#38)"
```
