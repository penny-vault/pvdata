# Zacks Provider Feature Parity Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Bring the Zacks provider to feature parity with the legacy importer by adding new data types (estimate, consensus, index), modifying the metric schema, and mapping all useful CSV fields into the appropriate data types.

**Architecture:** Three new data types are added to the core data model. The metric type is widened (add pe_forward/peg/price_to_cash_flow/beta, drop sp500). The Zacks dataset declaration is expanded to emit multiple observation types from a single CSV download. The existing `SaveObservations` routing in `library/database.go` is extended to handle new types.

**Tech Stack:** Go, PostgreSQL, pgx, gocsv, Playwright (existing), parquet-go (existing)

**Design doc:** `docs/plans/2026-03-07-zacks-parity-design.md`

---

### Task 1: Add `estimate` data type

**Files:**
- Create: `data/estimate.go`
- Modify: `data/datatype.go:65-75` (add EstimateKey constant and DataTypes entry)
- Modify: `data/datatype.go:41-54` (add Estimate field to Observation struct)
- Modify: `library/database.go:175-242` (add Estimate routing in SaveObservations)

**Step 1: Create `data/estimate.go`**

```go
package data

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

type Estimate struct {
	Ticker        string
	CompositeFigi string
	EventDate     time.Time
	Series        string
	Value         float64
	NumAnalysts   int
	StdDev        float64
}

func (estimate *Estimate) SaveDB(ctx context.Context, tbl string, dbConn *pgxpool.Conn) error {
	if estimate.CompositeFigi == "" {
		return nil
	}

	tx, err := dbConn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Commit(ctx); err != nil {
			log.Error().Err(err).Msg("error committing estimate transaction to database")
		}
	}()

	sql := fmt.Sprintf(`INSERT INTO %[1]s (
		"ticker",
		"composite_figi",
		"event_date",
		"series",
		"value",
		"num_analysts",
		"std_dev"
	) VALUES (
		$1, $2, $3, $4, $5, $6, $7
	) ON CONFLICT ON CONSTRAINT %[1]s_pkey DO UPDATE SET
		ticker = EXCLUDED.ticker,
		value = EXCLUDED.value,
		num_analysts = EXCLUDED.num_analysts,
		std_dev = EXCLUDED.std_dev`, tbl)

	_, err = tx.Exec(ctx, sql,
		estimate.Ticker,
		estimate.CompositeFigi,
		estimate.EventDate,
		estimate.Series,
		estimate.Value,
		estimate.NumAnalysts,
		estimate.StdDev,
	)

	if err != nil {
		log.Error().Err(err).Str("SQL", sql).Msg("save estimate to DB failed")
		if err2 := tx.Rollback(ctx); err2 != nil {
			log.Error().Err(err).Msg("error rollingback tx")
		}
	}

	return err
}
```

**Step 2: Add EstimateKey constant and DataTypes entry in `data/datatype.go`**

Add to the const block after `EODKey`:
```go
EstimateKey = "estimate"
```

Add to `DataTypes` map:
```go
EstimateKey: {
    Name:     EstimateKey,
    ViewName: "estimates",
    Schema: `CREATE TABLE %[1]s (
    ticker         CHARACTER VARYING(10) NOT NULL,
    composite_figi CHARACTER(12)         NOT NULL,
    event_date     DATE                  NOT NULL,
    series         TEXT                  NOT NULL,
    value          REAL                  NOT NULL,
    num_analysts   INT,
    std_dev        REAL,
    PRIMARY KEY (composite_figi, series, event_date)
);

CREATE INDEX %[1]s_ticker_idx ON %[1]s(ticker, series);
CREATE INDEX %[1]s_event_date_idx ON %[1]s(event_date, series);`,
    Migrations:    []string{},
    Version:       0,
    IsPartitioned: false,
},
```

**Step 3: Add Estimate field to Observation struct in `data/datatype.go`**

Add `Estimate *Estimate` to the Observation struct, after the `EodQuote` field.

**Step 4: Add Estimate routing in `library/database.go` SaveObservations**

Add after the Rating block:
```go
if elem.Estimate != nil {
    if err := elem.Estimate.SaveDB(ctx, subscription.DataTablesMap[data.EstimateKey], conn); err != nil {
        log.Error().Err(err).Msg("cannot save estimate to database")
    }
}
```

**Step 5: Build and verify**

Run: `go build ./...`
Expected: Clean build

**Step 6: Commit**

```
git add data/estimate.go data/datatype.go library/database.go
git commit -m "feat: add estimate data type for forward-looking consensus data"
```

---

### Task 2: Add `consensus` data type

**Files:**
- Create: `data/consensus.go`
- Modify: `data/datatype.go` (add ConsensusKey constant and DataTypes entry)
- Modify: `data/datatype.go` (add Consensus field to Observation struct)
- Modify: `library/database.go` (add Consensus routing in SaveObservations)

**Step 1: Create `data/consensus.go`**

```go
package data

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

type Consensus struct {
	Ticker               string
	CompositeFigi        string
	EventDate            time.Time
	AvgRecommendation    float64
	NumAnalysts          int
	NumStrongBuyOrBuy    int
	NumHold              int
	NumSellOrStrongSell  int
	NumUpgrades          int
	NumDowngrades        int
	AvgTargetPrice       float64
}

func (consensus *Consensus) SaveDB(ctx context.Context, tbl string, dbConn *pgxpool.Conn) error {
	if consensus.CompositeFigi == "" {
		return nil
	}

	tx, err := dbConn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Commit(ctx); err != nil {
			log.Error().Err(err).Msg("error committing consensus transaction to database")
		}
	}()

	sql := fmt.Sprintf(`INSERT INTO %[1]s (
		"ticker",
		"composite_figi",
		"event_date",
		"avg_recommendation",
		"num_analysts",
		"num_strong_buy_or_buy",
		"num_hold",
		"num_sell_or_strong_sell",
		"num_upgrades",
		"num_downgrades",
		"avg_target_price"
	) VALUES (
		$1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11
	) ON CONFLICT ON CONSTRAINT %[1]s_pkey DO UPDATE SET
		ticker = EXCLUDED.ticker,
		avg_recommendation = EXCLUDED.avg_recommendation,
		num_analysts = EXCLUDED.num_analysts,
		num_strong_buy_or_buy = EXCLUDED.num_strong_buy_or_buy,
		num_hold = EXCLUDED.num_hold,
		num_sell_or_strong_sell = EXCLUDED.num_sell_or_strong_sell,
		num_upgrades = EXCLUDED.num_upgrades,
		num_downgrades = EXCLUDED.num_downgrades,
		avg_target_price = EXCLUDED.avg_target_price`, tbl)

	_, err = tx.Exec(ctx, sql,
		consensus.Ticker,
		consensus.CompositeFigi,
		consensus.EventDate,
		consensus.AvgRecommendation,
		consensus.NumAnalysts,
		consensus.NumStrongBuyOrBuy,
		consensus.NumHold,
		consensus.NumSellOrStrongSell,
		consensus.NumUpgrades,
		consensus.NumDowngrades,
		consensus.AvgTargetPrice,
	)

	if err != nil {
		log.Error().Err(err).Str("SQL", sql).Msg("save consensus to DB failed")
		if err2 := tx.Rollback(ctx); err2 != nil {
			log.Error().Err(err).Msg("error rollingback tx")
		}
	}

	return err
}
```

**Step 2: Add ConsensusKey constant and DataTypes entry in `data/datatype.go`**

Add to the const block:
```go
ConsensusKey = "consensus"
```

Add to `DataTypes` map:
```go
ConsensusKey: {
    Name:     ConsensusKey,
    ViewName: "consensus",
    Schema: `CREATE TABLE %[1]s (
    ticker                   CHARACTER VARYING(10) NOT NULL,
    composite_figi           CHARACTER(12)         NOT NULL,
    event_date               DATE                  NOT NULL,
    avg_recommendation       REAL,
    num_analysts             INT,
    num_strong_buy_or_buy    INT,
    num_hold                 INT,
    num_sell_or_strong_sell  INT,
    num_upgrades             INT,
    num_downgrades           INT,
    avg_target_price         REAL,
    PRIMARY KEY (composite_figi, event_date)
);

CREATE INDEX %[1]s_ticker_idx ON %[1]s(ticker);
CREATE INDEX %[1]s_event_date_idx ON %[1]s(event_date);`,
    Migrations:    []string{},
    Version:       0,
    IsPartitioned: false,
},
```

**Step 3: Add Consensus field to Observation struct**

Add `Consensus *Consensus` to the Observation struct.

**Step 4: Add Consensus routing in `library/database.go`**

```go
if elem.Consensus != nil {
    if err := elem.Consensus.SaveDB(ctx, subscription.DataTablesMap[data.ConsensusKey], conn); err != nil {
        log.Error().Err(err).Msg("cannot save consensus to database")
    }
}
```

**Step 5: Build and verify**

Run: `go build ./...`

**Step 6: Commit**

```
git add data/consensus.go data/datatype.go library/database.go
git commit -m "feat: add consensus data type for broker recommendation aggregation"
```

---

### Task 3: Add `index` data type

The index type uses two tables (snapshot + changelog) created from a single schema string. This is a new pattern -- the schema creates both tables using the `%[1]s` placeholder.

**Files:**
- Create: `data/index.go`
- Modify: `data/datatype.go` (add IndexKey constant and DataTypes entry)
- Modify: `data/datatype.go` (add IndexSnapshot and IndexChange fields to Observation struct)
- Modify: `library/database.go` (add Index routing in SaveObservations)

**Step 1: Create `data/index.go`**

```go
package data

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

type IndexSnapshot struct {
	Ticker        string
	CompositeFigi string
	IndexName     string
	SnapshotDate  time.Time
}

func (idx *IndexSnapshot) SaveDB(ctx context.Context, tbl string, dbConn *pgxpool.Conn) error {
	if idx.CompositeFigi == "" {
		return nil
	}

	tx, err := dbConn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Commit(ctx); err != nil {
			log.Error().Err(err).Msg("error committing index snapshot transaction to database")
		}
	}()

	sql := fmt.Sprintf(`INSERT INTO %[1]s_snapshot (
		"composite_figi",
		"ticker",
		"index_name",
		"snapshot_date"
	) VALUES (
		$1, $2, $3, $4
	) ON CONFLICT ON CONSTRAINT %[1]s_snapshot_pkey DO NOTHING`, tbl)

	_, err = tx.Exec(ctx, sql,
		idx.CompositeFigi,
		idx.Ticker,
		idx.IndexName,
		idx.SnapshotDate,
	)

	if err != nil {
		log.Error().Err(err).Str("SQL", sql).Msg("save index snapshot to DB failed")
		if err2 := tx.Rollback(ctx); err2 != nil {
			log.Error().Err(err).Msg("error rollingback tx")
		}
	}

	return err
}

type IndexChange struct {
	Ticker        string
	CompositeFigi string
	IndexName     string
	EventDate     time.Time
	Action        string // "add" or "remove"
}

func (idx *IndexChange) SaveDB(ctx context.Context, tbl string, dbConn *pgxpool.Conn) error {
	if idx.CompositeFigi == "" {
		return nil
	}

	tx, err := dbConn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Commit(ctx); err != nil {
			log.Error().Err(err).Msg("error committing index change transaction to database")
		}
	}()

	sql := fmt.Sprintf(`INSERT INTO %[1]s_changelog (
		"composite_figi",
		"ticker",
		"index_name",
		"event_date",
		"action"
	) VALUES (
		$1, $2, $3, $4, $5
	) ON CONFLICT ON CONSTRAINT %[1]s_changelog_pkey DO UPDATE SET
		action = EXCLUDED.action`, tbl)

	_, err = tx.Exec(ctx, sql,
		idx.CompositeFigi,
		idx.Ticker,
		idx.IndexName,
		idx.EventDate,
		idx.Action,
	)

	if err != nil {
		log.Error().Err(err).Str("SQL", sql).Msg("save index change to DB failed")
		if err2 := tx.Rollback(ctx); err2 != nil {
			log.Error().Err(err).Msg("error rollingback tx")
		}
	}

	return err
}
```

**Step 2: Add IndexKey constant and DataTypes entry in `data/datatype.go`**

Add to the const block:
```go
IndexKey = "index"
```

Add to `DataTypes` map:
```go
IndexKey: {
    Name:     IndexKey,
    ViewName: "indices",
    Schema: `CREATE TABLE %[1]s_snapshot (
    composite_figi CHARACTER(12)         NOT NULL,
    ticker         CHARACTER VARYING(10) NOT NULL,
    index_name     TEXT                  NOT NULL,
    snapshot_date  DATE                  NOT NULL,
    PRIMARY KEY (composite_figi, index_name, snapshot_date)
);

CREATE TABLE %[1]s_changelog (
    composite_figi CHARACTER(12)         NOT NULL,
    ticker         CHARACTER VARYING(10) NOT NULL,
    index_name     TEXT                  NOT NULL,
    event_date     DATE                  NOT NULL,
    action         TEXT                  NOT NULL,
    PRIMARY KEY (composite_figi, index_name, event_date)
);

CREATE INDEX %[1]s_snapshot_index_name_idx ON %[1]s_snapshot(index_name, snapshot_date);
CREATE INDEX %[1]s_changelog_index_name_idx ON %[1]s_changelog(index_name, event_date);`,
    Migrations:    []string{},
    Version:       0,
    IsPartitioned: false,
},
```

**Step 3: Add IndexSnapshot and IndexChange fields to Observation struct**

Add both to the Observation struct:
```go
IndexSnapshot *IndexSnapshot
IndexChange   *IndexChange
```

**Step 4: Add Index routing in `library/database.go`**

```go
if elem.IndexSnapshot != nil {
    if err := elem.IndexSnapshot.SaveDB(ctx, subscription.DataTablesMap[data.IndexKey], conn); err != nil {
        log.Error().Err(err).Msg("cannot save index snapshot to database")
    }
}

if elem.IndexChange != nil {
    if err := elem.IndexChange.SaveDB(ctx, subscription.DataTablesMap[data.IndexKey], conn); err != nil {
        log.Error().Err(err).Msg("cannot save index change to database")
    }
}
```

**Step 5: Build and verify**

Run: `go build ./...`

**Step 6: Commit**

```
git add data/index.go data/datatype.go library/database.go
git commit -m "feat: add index data type with snapshot and changelog tables"
```

---

### Task 4: Modify `metric` data type -- add columns, drop sp500

This is a schema change. Existing metric subscriptions (Sharadar) already have tables with the old schema. The `Migrations` field on DataType exists for this purpose but is currently empty. We add a migration and bump the version.

**Files:**
- Modify: `data/datatype.go` (update MetricKey schema, add migration, bump version)
- Modify: `data/metric.go` (update Metric struct and SaveDB)
- Modify: `provider/sharadar_metrics.go` (stop writing sp500 field)

**Step 1: Update MetricKey schema in `data/datatype.go`**

Replace the MetricKey Schema with:
```sql
CREATE TABLE %[1]s (
ticker         CHARACTER VARYING(10) NOT NULL,
composite_figi CHARACTER(12)         NOT NULL,
event_date     DATE                  NOT NULL,
market_cap     BIGINT                NOT NULL DEFAULT 0,
ev             BIGINT                NOT NULL DEFAULT 0,
pe             REAL                  NOT NULL DEFAULT 0.0,
pb             REAL                  NOT NULL DEFAULT 0.0,
ps             REAL                  NOT NULL DEFAULT 0.0,
ev_ebit        REAL                  NOT NULL DEFAULT 0.0,
ev_ebitda      REAL                  NOT NULL DEFAULT 0.0,
pe_forward     REAL                  NOT NULL DEFAULT 0.0,
peg            REAL                  NOT NULL DEFAULT 0.0,
price_to_cash_flow REAL              NOT NULL DEFAULT 0.0,
beta           REAL                  NOT NULL DEFAULT 0.0,
CHECK (LENGTH(TRIM(BOTH composite_figi)) = 12),
PRIMARY KEY (composite_figi, event_date)
) PARTITION BY RANGE (event_date);

CREATE INDEX %[1]s_event_date_idx ON %[1]s(event_date);
CREATE INDEX %[1]s_ticker_idx ON %[1]s(ticker);
```

Add migration:
```go
Migrations: []string{
    `ALTER TABLE %[1]s ADD COLUMN IF NOT EXISTS pe_forward REAL NOT NULL DEFAULT 0.0;
     ALTER TABLE %[1]s ADD COLUMN IF NOT EXISTS peg REAL NOT NULL DEFAULT 0.0;
     ALTER TABLE %[1]s ADD COLUMN IF NOT EXISTS price_to_cash_flow REAL NOT NULL DEFAULT 0.0;
     ALTER TABLE %[1]s ADD COLUMN IF NOT EXISTS beta REAL NOT NULL DEFAULT 0.0;
     ALTER TABLE %[1]s DROP COLUMN IF EXISTS sp500;`,
},
Version: 1,
```

**Step 2: Update Metric struct in `data/metric.go`**

Replace the struct with:
```go
type Metric struct {
	Ticker           string
	CompositeFigi    string
	EventDate        time.Time
	MarketCap        int64
	EV               int64
	PE               float64
	PB               float64
	PS               float64
	EVtoEBIT         float64
	EVtoEBITDA       float64
	PEForward        float64
	PEG              float64
	PriceToCashFlow  float64
	Beta             float64
}
```

Update `SaveDB` to insert/upsert the new columns and remove `sp500`:
```go
sql := fmt.Sprintf(`INSERT INTO %[1]s (
    "ticker",
    "composite_figi",
    "event_date",
    "market_cap",
    "ev",
    "pe",
    "pb",
    "ps",
    "ev_ebit",
    "ev_ebitda",
    "pe_forward",
    "peg",
    "price_to_cash_flow",
    "beta"
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14
) ON CONFLICT ON CONSTRAINT %[1]s_pkey DO UPDATE SET
    ticker = EXCLUDED.ticker,
    market_cap = EXCLUDED.market_cap,
    ev = EXCLUDED.ev,
    pe = EXCLUDED.pe,
    pb = EXCLUDED.pb,
    ps = EXCLUDED.ps,
    ev_ebit = EXCLUDED.ev_ebit,
    ev_ebitda = EXCLUDED.ev_ebitda,
    pe_forward = EXCLUDED.pe_forward,
    peg = EXCLUDED.peg,
    price_to_cash_flow = EXCLUDED.price_to_cash_flow,
    beta = EXCLUDED.beta`, tbl)

_, err = tx.Exec(ctx, sql,
    metric.Ticker,
    metric.CompositeFigi,
    metric.EventDate,
    metric.MarketCap,
    metric.EV,
    metric.PE,
    metric.PB,
    metric.PS,
    metric.EVtoEBIT,
    metric.EVtoEBITDA,
    metric.PEForward,
    metric.PEG,
    metric.PriceToCashFlow,
    metric.Beta,
)
```

**Step 3: Update `provider/sharadar_metrics.go`**

In the `PvMetric` method, remove the SP500 assignment:
```go
// Remove these lines:
// if _, ok := sp500Map[pvMetric.Ticker]; ok {
//     pvMetric.SP500 = true
// }
```

Also remove the sp500Map parameter from `PvMetric` and `downloadSharadarMetrics` signatures, and the SP500 fetch logic from `downloadAllSharadarMetrics`. The S&P 500 membership data should eventually come from an index-type subscription, but removing it now is fine -- it was a boolean crammed into the wrong table.

**Step 4: Implement migration runner**

Check if a migration runner already exists. If not, add a method to `library/subscription.go` or `library/database.go` that:
1. On subscription load, compares `subscription.SchemaVersion` to `DataType.Version`
2. If behind, runs each migration in sequence (using `fmt.Sprintf(migration, tableName)`)
3. Updates `schema_version` in the subscriptions table

Look at how `SchemaVersion` is used in `library/subscription.go` -- it's stored but never checked. Add this to `Subscription.ManagePartitions()` or create a new `Subscription.RunMigrations()` method called from the run command.

**Step 5: Build and verify**

Run: `go build ./...`

**Step 6: Commit**

```
git add data/datatype.go data/metric.go provider/sharadar_metrics.go library/subscription.go
git commit -m "feat: widen metric schema with pe_forward/peg/price_to_cash_flow/beta, drop sp500"
```

---

### Task 5: Expand Zacks dataset to declare multiple data types

**Files:**
- Modify: `provider/zacks.go:56-68` (update Datasets() DataTypes list)

**Step 1: Update the DataTypes slice**

Change the Datasets() method:
```go
func (zacks *Zacks) Datasets() map[string]Dataset {
    return map[string]Dataset{
        "Zacks Screener Data": {
            Name:        "Zacks Screener Data",
            Description: "Download data using Zacks stock screener tool.",
            DataTypes: []*data.DataType{
                data.DataTypes[data.RatingKey],
                data.DataTypes[data.MetricKey],
                data.DataTypes[data.EstimateKey],
                data.DataTypes[data.ConsensusKey],
            },
            DateRange: func() (time.Time, time.Time) {
                return time.Date(1990, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
            },
            Fetch: downloadZacksData,
        },
    }
}
```

Note: `FundamentalsKey` is intentionally omitted for now. The Zacks CSV provides some fundamental-like fields, but the `Fundamental` struct has 90+ fields with a specific Sharadar-centric schema (dimension, date_key, report_period, etc.) that doesn't map well to the Zacks CSV's flat snapshot data. The balance sheet fields from the CSV (CurrentAssetsMil, etc.) are better served by parquet archival. This can be revisited later if needed.

**Step 2: Build and verify**

Run: `go build ./...`

**Step 3: Commit**

```
git add provider/zacks.go
git commit -m "feat: expand Zacks dataset to declare metric, estimate, and consensus types"
```

---

### Task 6: Add metric observations to Zacks Fetch

**Files:**
- Modify: `provider/zacks.go` (add metric emission in downloadZacksData, after rating emission)

**Step 1: Add metric observation emission**

After the existing rating emission loop in `downloadZacksData`, add metric emission for each enriched record:

```go
// Metrics
out <- &data.Observation{
    Metric: &data.Metric{
        Ticker:          record.Ticker,
        CompositeFigi:   record.CompositeFigi,
        EventDate:       record.EventDate,
        MarketCap:       int64(record.MarketCapMil * 1e6),
        PE:              float64(record.PeTrailing12Months),
        PB:              float64(record.PriceToBook),
        PS:              float64(record.PriceToSales),
        PEForward:       float64(record.PeF1),
        PEG:             float64(record.PegRatio),
        PriceToCashFlow: float64(record.PriceToCashFlow),
        Beta:            float64(record.Beta),
    },
    ObservationDate:  time.Now(),
    SubscriptionID:   subscription.ID,
    SubscriptionName: subscription.Name,
}
numObs++
```

**Step 2: Build and verify**

Run: `go build ./...`

**Step 3: Commit**

```
git add provider/zacks.go
git commit -m "feat: emit metric observations from Zacks CSV data"
```

---

### Task 7: Add consensus observations to Zacks Fetch

**Files:**
- Modify: `provider/zacks.go` (add consensus emission in downloadZacksData)

**Step 1: Add consensus observation emission**

```go
// Consensus
out <- &data.Observation{
    Consensus: &data.Consensus{
        Ticker:              record.Ticker,
        CompositeFigi:       record.CompositeFigi,
        EventDate:           record.EventDate,
        AvgRecommendation:   float64(record.CurrentAvgBrokerRec),
        NumAnalysts:         record.NumBrokersInRating,
        NumStrongBuyOrBuy:   record.NumRatingStrongBuyOrBuy,
        NumHold:             record.NumRatingHold,
        NumSellOrStrongSell: record.NumRatingStrongSellOrSell,
        NumUpgrades:         record.NumberRatingUpgrades,
        NumDowngrades:       record.NumberRatingDowngrades,
        AvgTargetPrice:      record.AverageTargetPrice,
    },
    ObservationDate:  time.Now(),
    SubscriptionID:   subscription.ID,
    SubscriptionName: subscription.Name,
}
numObs++
```

**Step 2: Build and verify**

Run: `go build ./...`

**Step 3: Commit**

```
git add provider/zacks.go
git commit -m "feat: emit consensus observations from Zacks CSV data"
```

---

### Task 8: Add estimate observations to Zacks Fetch

**Files:**
- Modify: `provider/zacks.go` (add estimate emission in downloadZacksData)

**Step 1: Add estimate observation emission**

Create a helper to emit estimate observations to avoid repetition. Add this function:

```go
func emitEstimate(record *ZacksRecord, series string, value float64, numAnalysts int, stdDev float64, subscription *library.Subscription, out chan<- *data.Observation) {
	if value == 0 && numAnalysts == 0 {
		return
	}
	out <- &data.Observation{
		Estimate: &data.Estimate{
			Ticker:        record.Ticker,
			CompositeFigi: record.CompositeFigi,
			EventDate:     record.EventDate,
			Series:        series,
			Value:         value,
			NumAnalysts:   numAnalysts,
			StdDev:        stdDev,
		},
		ObservationDate:  time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}
}
```

Then in the enriched record loop:

```go
// Estimates
emitEstimate(record, "eps-q0", float64(record.Q0ConsensusEstLastCompletedFiscalQtr), record.NumberOfAnalystsInQ0Consensus, 0, subscription, out)
emitEstimate(record, "eps-q1", float64(record.Q1ConsensusEst), record.NumberOfAnalystsInQ1Consensus, float64(record.StdevQ1Q1ConsensusRatio), subscription, out)
emitEstimate(record, "eps-q2", float64(record.Q2ConsensusEstNextFiscalQtr), record.NumberOfAnalystsInQ2Consensus, float64(record.StdevQ2Q2ConsensusRatio), subscription, out)
emitEstimate(record, "eps-f0", float64(record.F0ConsensusEst), int(record.NumberOfAnalystsInF0Consensus), 0, subscription, out)
emitEstimate(record, "eps-f1", float64(record.F1ConsensusEst), record.NumberOfAnalystsInF1Consensus, float64(record.StdevF1F1ConsensusRatio), subscription, out)
emitEstimate(record, "eps-f2", float64(record.F2ConsensusEst), record.NumberOfAnalystsInF2Consensus, 0, subscription, out)
emitEstimate(record, "sales-q1", float64(record.Q1ConsensusSalesEstMil), 0, 0, subscription, out)
emitEstimate(record, "sales-f1", float64(record.F1ConsensusSalesEstMil), 0, 0, subscription, out)
emitEstimate(record, "lt-growth", float64(record.LongTermGrowthConsensusEst), 0, 0, subscription, out)
emitEstimate(record, "earnings-esp", float64(record.EarningsEsp), 0, 0, subscription, out)
emitEstimate(record, "eps-surprise-last", float64(record.LastEpsSurprisePercent), 0, 0, subscription, out)
emitEstimate(record, "eps-surprise-prev", float64(record.PreviousEpsSurprisePercent), 0, 0, subscription, out)
emitEstimate(record, "eps-surprise-avg-4q", float64(record.AvgEpsSurpriseLast4Qtrs), 0, 0, subscription, out)
```

**Step 2: Build and verify**

Run: `go build ./...`

**Step 3: Commit**

```
git add provider/zacks.go
git commit -m "feat: emit estimate observations from Zacks CSV data"
```

---

### Task 9: Add migration runner

The metric schema change (Task 4) added a migration. We need a mechanism to apply it to existing subscriptions.

**Files:**
- Modify: `library/subscription.go` (add RunMigrations method)
- Modify: `library/database.go` (call RunMigrations during subscription load or before fetch)

**Step 1: Add RunMigrations method to `library/subscription.go`**

```go
// RunMigrations applies any pending schema migrations for the subscription's data types
func (subscription *Subscription) RunMigrations(ctx context.Context) error {
	conn, err := subscription.Library.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	for idx, dataTypeName := range subscription.DataTypes {
		dataType := data.DataTypes[dataTypeName]
		if dataType == nil {
			continue
		}

		if subscription.SchemaVersion >= dataType.Version {
			continue
		}

		dataTable := subscription.DataTables[idx]

		for i := subscription.SchemaVersion; i < dataType.Version; i++ {
			if i < len(dataType.Migrations) {
				migrationSQL := fmt.Sprintf(dataType.Migrations[i], dataTable)
				log.Info().Str("Table", dataTable).Int("Migration", i).Msg("running migration")
				if _, err := conn.Exec(ctx, migrationSQL); err != nil {
					return fmt.Errorf("migration %d for %s failed: %w", i, dataTable, err)
				}
			}
		}
	}

	// update schema version to latest
	maxVersion := 0
	for _, dataTypeName := range subscription.DataTypes {
		dataType := data.DataTypes[dataTypeName]
		if dataType != nil && dataType.Version > maxVersion {
			maxVersion = dataType.Version
		}
	}

	if maxVersion > subscription.SchemaVersion {
		if _, err := conn.Exec(ctx, "UPDATE subscriptions SET schema_version=$1 WHERE id=$2", maxVersion, subscription.ID); err != nil {
			return fmt.Errorf("failed to update schema version: %w", err)
		}
		subscription.SchemaVersion = maxVersion
	}

	return nil
}
```

**Step 2: Call RunMigrations before fetch**

In the run command flow (wherever subscriptions are loaded before fetching), call `subscription.RunMigrations(ctx)`. Look at how `ManagePartitions` is called and add `RunMigrations` alongside it. Check `cmd/run.go` or the TUI run flow for the right place.

**Step 3: Build and verify**

Run: `go build ./...`

**Step 4: Commit**

```
git add library/subscription.go cmd/run.go
git commit -m "feat: add schema migration runner for data type version upgrades"
```

---

### Task 10: Run full test suite and verify

**Step 1: Run tests**

Run: `go test ./...`
Expected: All tests pass

**Step 2: Build binary**

Run: `go build -o pvdata .`
Expected: Clean build

**Step 3: Verify the prefer command still works**

Run: `./pvdata prefer`
Expected: Lists current preferred views (may be empty if no subscriptions exist)

**Step 4: Commit any fixes**

If any fixes were needed, commit them.

---

## Summary of all files changed

| File | Action |
|---|---|
| `data/estimate.go` | Create -- Estimate struct and SaveDB |
| `data/consensus.go` | Create -- Consensus struct and SaveDB |
| `data/index.go` | Create -- IndexSnapshot, IndexChange structs and SaveDB |
| `data/datatype.go` | Modify -- add 3 new keys/types, add Observation fields, update metric schema |
| `data/metric.go` | Modify -- add PEForward/PEG/PriceToCashFlow/Beta fields, drop SP500 |
| `library/database.go` | Modify -- add routing for Estimate, Consensus, IndexSnapshot, IndexChange |
| `library/subscription.go` | Modify -- add RunMigrations method |
| `provider/zacks.go` | Modify -- expand DataTypes, emit metric/consensus/estimate observations |
| `provider/sharadar_metrics.go` | Modify -- remove SP500 field usage |
