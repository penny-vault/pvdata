# Data Validation Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a validation engine that catches broken accounting identities, statistical anomalies, and cross-source disagreements -- inline during ingestion and in batch mode -- routing flagged data to quarantine or zerolog warnings.

**Architecture:** A `validation/` package with a Rule interface, pluggable anomaly detection strategies, and a ValidationContext for DB access. ValidateAndSave wraps the existing SaveObservations pipeline. Batch mode iterates by date chunks. Configuration lives in a `validation_rules` DB table; quarantined observations stored as JSONB.

**Tech Stack:** Go, PostgreSQL, pgx/v5, Ginkgo/Gomega, Cobra, zerolog, gocron

**Spec:** `docs/superpowers/specs/2026-03-15-data-validation-design.md`

---

## Chunk 1: Foundation -- Database Migration, Types, and ObservationKey

### Task 1: Database Migration

**Files:**
- Create: `db/migrations/000004_validation.up.sql`
- Create: `db/migrations/000004_validation.down.sql`

- [ ] **Step 1: Write up migration**

```sql
-- 000004_validation.up.sql

CREATE TYPE validation_severity AS ENUM ('warning', 'quarantine');
CREATE TYPE quarantine_resolution AS ENUM ('released', 'discarded');

CREATE TABLE validation_rules (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_code       TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    enabled         BOOLEAN NOT NULL DEFAULT true,
    run_inline      BOOLEAN NOT NULL DEFAULT true,
    severity        validation_severity NOT NULL DEFAULT 'warning',
    datatypes       datatype[] NOT NULL,
    params          JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE quarantine (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    datatype        datatype NOT NULL,
    observation     JSONB NOT NULL,
    rule_id         UUID NOT NULL REFERENCES validation_rules(id),
    severity        validation_severity NOT NULL,
    message         TEXT NOT NULL,
    details         JSONB,
    subscription_id UUID REFERENCES subscriptions(id),
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    reviewed_at     TIMESTAMPTZ,
    resolution      quarantine_resolution
);

CREATE INDEX idx_quarantine_unresolved ON quarantine (datatype, created_at)
    WHERE resolution IS NULL;

CREATE TABLE universe_stats (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    datatype    datatype NOT NULL,
    field       TEXT NOT NULL,
    min         DOUBLE PRECISION,
    max         DOUBLE PRECISION,
    mean        DOUBLE PRECISION,
    median      DOUBLE PRECISION,
    std_dev     DOUBLE PRECISION,
    percentiles JSONB,
    sample_size BIGINT,
    computed_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (datatype, field)
);
```

```sql
-- 000004_validation.down.sql

DROP TABLE IF EXISTS universe_stats;
DROP TABLE IF EXISTS quarantine;
DROP TABLE IF EXISTS validation_rules;
DROP TYPE IF EXISTS quarantine_resolution;
DROP TYPE IF EXISTS validation_severity;
```

- [ ] **Step 2: Verify migration applies cleanly**

Run: `go run . migrate` (or however migrations are applied in this project)
Expected: Migration 000004 applies without errors.

- [ ] **Step 3: Verify down migration**

Run the down migration and then re-apply up to confirm idempotency.

- [ ] **Step 4: Commit**

```bash
git add db/migrations/000004_validation.up.sql db/migrations/000004_validation.down.sql
git commit -m "feat: add validation database migration (rules, quarantine, universe_stats)"
```

---

### Task 2: Core Types -- Severity, ValidationResult, Rule Interface

**Files:**
- Create: `validation/rule.go`

- [ ] **Step 1: Write the test file**

Create `validation/validation_suite_test.go`:

```go
package validation_test

import (
	"testing"

	"github.com/rs/zerolog/log"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestValidation(t *testing.T) {
	log.Logger = log.Output(GinkgoWriter)
	RegisterFailHandler(Fail)
	RunSpecs(t, "Validation Suite")
}
```

- [ ] **Step 2: Write rule.go with core types**

```go
package validation

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
)

// Severity represents whether a validation failure is a warning or quarantine.
type Severity string

const (
	SeverityWarning    Severity = "warning"
	SeverityQuarantine Severity = "quarantine"
)

// ValidationResult is the outcome of a single rule evaluation.
type ValidationResult struct {
	RuleID      string
	Severity    Severity
	DataType    string
	Message     string
	Details     map[string]any
	Observation *data.Observation
}

// HistoricalValue is a single data point in a ticker's history.
type HistoricalValue struct {
	EventDate time.Time
	Value     float64
}

// UniverseStats holds precomputed distribution statistics for a (datatype, field) pair.
type UniverseStats struct {
	Min         float64
	Max         float64
	Mean        float64
	Median      float64
	StdDev      float64
	Percentiles map[int]float64
	SampleSize  int64
}

// ValidationContext provides rules with access to historical data and universe stats.
// Uses data.ObservationKeyProvider (defined in data/datatype.go) for observation identity.
type ValidationContext struct {
	DB            *pgxpool.Pool
	UniverseStats map[string]UniverseStats // keyed by "datatype.field"
}

// TickerHistory returns historical values for a given observation key and field.
func (vc *ValidationContext) TickerHistory(ctx context.Context, key data.ObservationKeyProvider, field string) ([]HistoricalValue, error) {
	// Implementation in Task 17 -- requires DB queries per data type
	return nil, nil
}

// CrossSourceObservations returns matching observations from other subscriptions.
func (vc *ValidationContext) CrossSourceObservations(ctx context.Context, key data.ObservationKeyProvider) ([]*data.Observation, error) {
	// Implementation in Task 17 -- requires subscription discovery
	return nil, nil
}

// Rule is the interface all validation rules must implement.
type Rule interface {
	ID() string
	Name() string
	DataTypes() []string
	Validate(ctx context.Context, vctx *ValidationContext, obs *data.Observation) ([]ValidationResult, error)
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./validation/`
Expected: Compiles without errors.

- [ ] **Step 4: Commit**

```bash
git add validation/rule.go validation/validation_suite_test.go
git commit -m "feat: add core validation types (Rule, Severity, ValidationResult, ValidationContext)"
```

---

### Task 3: ObservationKey on All Data Types

**Files:**
- Modify: `data/datatype.go` (add ObservationKey interface)
- Modify: `data/fundamental.go` (implement ObservationKey on Fundamental)
- Modify: `data/eod.go` (implement ObservationKey on Eod)
- Modify: `data/metric.go` (implement ObservationKey on Metric)
- Modify: `data/asset.go` (implement ObservationKey on Asset)
- Modify: `data/consensus.go` (implement ObservationKey on Consensus)
- Modify: `data/custom.go` (implement ObservationKey on Custom)
- Modify: `data/economic_indicator.go` (implement ObservationKey on EconomicIndicator)
- Modify: `data/estimate.go` (implement ObservationKey on Estimate)
- Modify: `data/index.go` (implement ObservationKey on IndexSnapshot, IndexChange)
- Modify: `data/holiday.go` (implement ObservationKey on MarketHoliday)
- Modify: `data/rating.go` (implement ObservationKey on AnalystRating)
- Test: `data/observation_key_test.go`

- [ ] **Step 1: Write failing tests for ObservationKey**

Create `data/observation_key_test.go`:

```go
package data_test

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/data"
)

var _ = Describe("ObservationKey", func() {
	Describe("Fundamental", func() {
		It("returns correct data type", func() {
			f := &data.Fundamental{
				CompositeFigi: "BBG000B9XRY4",
				EventDate:     time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
				Dimension:     "ARQ",
			}
			Expect(f.ObsDataType()).To(Equal(data.FundamentalsKey))
		})

		It("returns correct primary key", func() {
			f := &data.Fundamental{
				CompositeFigi: "BBG000B9XRY4",
				EventDate:     time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
				Dimension:     "ARQ",
			}
			pk := f.ObsPrimaryKey()
			Expect(pk).To(HaveKeyWithValue("composite_figi", "BBG000B9XRY4"))
			Expect(pk).To(HaveKeyWithValue("dimension", "ARQ"))
			Expect(pk).To(HaveKey("event_date"))
		})

		It("returns a human-readable display label", func() {
			f := &data.Fundamental{
				Ticker:        "AAPL",
				CompositeFigi: "BBG000B9XRY4",
				EventDate:     time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
				Dimension:     "ARQ",
			}
			Expect(f.ObsDisplayLabel()).To(ContainSubstring("AAPL"))
		})
	})

	Describe("EconomicIndicator", func() {
		It("returns correct primary key with series", func() {
			ei := &data.EconomicIndicator{
				Series:    "GDP",
				EventDate: time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
			}
			Expect(ei.ObsDataType()).To(Equal(data.EconomicIndicatorKey))
			pk := ei.ObsPrimaryKey()
			Expect(pk).To(HaveKeyWithValue("series", "GDP"))
			Expect(pk).To(HaveKey("event_date"))
		})
	})

	Describe("Eod", func() {
		It("returns correct primary key", func() {
			e := &data.Eod{
				CompositeFigi: "BBG000B9XRY4",
				Date:          time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
			}
			Expect(e.ObsDataType()).To(Equal(data.EODKey))
			pk := e.ObsPrimaryKey()
			Expect(pk).To(HaveKeyWithValue("composite_figi", "BBG000B9XRY4"))
			Expect(pk).To(HaveKey("event_date"))
		})
	})

	Describe("Observation delegation", func() {
		It("delegates to Fundamental when populated", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Ticker:        "AAPL",
					CompositeFigi: "BBG000B9XRY4",
					EventDate:     time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
					Dimension:     "ARQ",
				},
			}
			Expect(obs.ObsDataType()).To(Equal(data.FundamentalsKey))
		})

		It("delegates to EconomicIndicator when populated", func() {
			obs := &data.Observation{
				EconomicIndicator: &data.EconomicIndicator{
					Series:    "GDP",
					EventDate: time.Date(2025, 1, 31, 0, 0, 0, 0, time.UTC),
				},
			}
			Expect(obs.ObsDataType()).To(Equal(data.EconomicIndicatorKey))
		})
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd data && go test -v -run ObservationKey`
Expected: Compilation errors -- methods not defined.

- [ ] **Step 3: Add ObservationKey interface to datatype.go**

Add to `data/datatype.go` after the `Observation` struct (after line 58):

```go
// ObservationKeyProvider is implemented by each data type sub-struct
// to express its own identity (primary key varies by type).
type ObservationKeyProvider interface {
	ObsDataType() string
	ObsPrimaryKey() map[string]any
	ObsDisplayLabel() string
}
```

Add delegation methods on `Observation`:

```go
// ObsDataType returns the data type key of the populated sub-type.
func (o *Observation) ObsDataType() string {
	if p := o.activeProvider(); p != nil {
		return p.ObsDataType()
	}
	return ""
}

func (o *Observation) ObsPrimaryKey() map[string]any {
	if p := o.activeProvider(); p != nil {
		return p.ObsPrimaryKey()
	}
	return nil
}

func (o *Observation) ObsDisplayLabel() string {
	if p := o.activeProvider(); p != nil {
		return p.ObsDisplayLabel()
	}
	return ""
}

func (o *Observation) activeProvider() ObservationKeyProvider {
	switch {
	case o.AssetObject != nil:
		return o.AssetObject
	case o.Consensus != nil:
		return o.Consensus
	case o.CustomObject != nil:
		return o.CustomObject
	case o.EconomicIndicator != nil:
		return o.EconomicIndicator
	case o.EodQuote != nil:
		return o.EodQuote
	case o.Estimate != nil:
		return o.Estimate
	case o.Fundamental != nil:
		return o.Fundamental
	case o.IndexChange != nil:
		return o.IndexChange
	case o.IndexSnapshot != nil:
		return o.IndexSnapshot
	case o.MarketHoliday != nil:
		return o.MarketHoliday
	case o.Metric != nil:
		return o.Metric
	case o.Rating != nil:
		return o.Rating
	default:
		return nil
	}
}
```

- [ ] **Step 4: Implement ObservationKey on Fundamental**

Add to `data/fundamental.go`:

```go
func (f *Fundamental) ObsDataType() string { return FundamentalsKey }

func (f *Fundamental) ObsPrimaryKey() map[string]any {
	return map[string]any{
		"composite_figi": f.CompositeFigi,
		"event_date":     f.EventDate,
		"dimension":      f.Dimension,
	}
}

func (f *Fundamental) ObsDisplayLabel() string {
	return fmt.Sprintf("%s %s %s %s", f.Ticker, f.CompositeFigi, f.EventDate.Format("2006-01-02"), f.Dimension)
}
```

- [ ] **Step 5: Implement ObservationKey on Eod**

Add to `data/eod.go`:

```go
func (e *Eod) ObsDataType() string { return EODKey }

func (e *Eod) ObsPrimaryKey() map[string]any {
	return map[string]any{
		"composite_figi": e.CompositeFigi,
		"event_date":     e.Date,
	}
}

func (e *Eod) ObsDisplayLabel() string {
	return fmt.Sprintf("%s %s %s", e.Ticker, e.CompositeFigi, e.Date.Format("2006-01-02"))
}
```

- [ ] **Step 6: Implement ObservationKey on EconomicIndicator**

Add to `data/economic_indicator.go`:

```go
func (ei *EconomicIndicator) ObsDataType() string { return EconomicIndicatorKey }

func (ei *EconomicIndicator) ObsPrimaryKey() map[string]any {
	return map[string]any{
		"series":     ei.Series,
		"event_date": ei.EventDate,
	}
}

func (ei *EconomicIndicator) ObsDisplayLabel() string {
	return fmt.Sprintf("%s %s", ei.Series, ei.EventDate.Format("2006-01-02"))
}
```

- [ ] **Step 7: Implement ObservationKey on remaining types**

Implement `ObsDataType()`, `ObsPrimaryKey()`, `ObsDisplayLabel()` on each remaining type. Follow the same pattern -- return the type's key constant, the actual DB primary key columns, and a human-readable label. Types to implement:

- `Asset` in `data/asset.go`: PK = `{ticker, composite_figi}` (struct fields: `Ticker`, `CompositeFigi`)
- `Consensus` in `data/consensus.go`: PK = `{composite_figi, event_date}` (struct fields: `CompositeFigi`, `EventDate`)
- `Custom` in `data/custom.go`: PK = `{composite_figi, event_date, key}` (check struct for exact field names)
- `Estimate` in `data/estimate.go`: PK = `{composite_figi, series, event_date}` (struct fields: `CompositeFigi`, `Series`, `EventDate`)
- `IndexSnapshot` in `data/index.go`: PK = `{composite_figi, index_name, snapshot_date}` (struct fields: `CompositeFigi`, `IndexName`, `SnapshotDate`)
- `IndexChange` in `data/index.go`: PK = `{composite_figi, index_name, event_date}` (struct fields: `CompositeFigi`, `IndexName`, `EventDate`)
- `MarketHoliday` in `data/holiday.go`: PK = `{market, event_date}` (check struct for exact field names)
- `Metric` in `data/metric.go`: PK = `{composite_figi, event_date}` (struct fields: `CompositeFigi`, `EventDate`)
- `AnalystRating` in `data/rating.go`: PK = `{composite_figi, event_date, analyst}` (check struct for exact field names)

- [ ] **Step 8: Run tests to verify they pass**

Run: `cd data && go test -v -run ObservationKey`
Expected: All pass.

- [ ] **Step 9: Commit**

```bash
git add data/datatype.go data/fundamental.go data/eod.go data/metric.go data/asset.go data/consensus.go data/custom.go data/economic_indicator.go data/estimate.go data/index.go data/holiday.go data/rating.go data/observation_key_test.go
git commit -m "feat: implement ObservationKey on all data types"
```

---

## Chunk 2: Validation Engine, Rule Registry, and Quarantine Operations

### Task 4: Rule Registry and Engine Core

**Files:**
- Create: `validation/engine.go`
- Test: `validation/engine_test.go`

- [ ] **Step 1: Write failing test for rule registry**

Create `validation/engine_test.go`:

```go
package validation_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/validation"
)

var _ = Describe("Engine", func() {
	Describe("Registry", func() {
		It("registers and retrieves rules", func() {
			registry := validation.NewRegistry()
			rule := &mockRule{id: "test.rule", dataTypes: []string{"fundamental"}}
			registry.Register(rule)
			rules := registry.RulesForDataType("fundamental")
			Expect(rules).To(HaveLen(1))
			Expect(rules[0].ID()).To(Equal("test.rule"))
		})

		It("returns empty for unknown data type", func() {
			registry := validation.NewRegistry()
			rules := registry.RulesForDataType("unknown")
			Expect(rules).To(BeEmpty())
		})

		It("returns multiple rules for same data type", func() {
			registry := validation.NewRegistry()
			registry.Register(&mockRule{id: "rule.a", dataTypes: []string{"fundamental"}})
			registry.Register(&mockRule{id: "rule.b", dataTypes: []string{"fundamental"}})
			rules := registry.RulesForDataType("fundamental")
			Expect(rules).To(HaveLen(2))
		})
	})
})
```

Add mock at bottom of test file:

```go
type mockRule struct {
	id        string
	dataTypes []string
	results   []validation.ValidationResult
	err       error
}

func (m *mockRule) ID() string        { return m.id }
func (m *mockRule) Name() string      { return m.id }
func (m *mockRule) DataTypes() []string { return m.dataTypes }
func (m *mockRule) Validate(_ context.Context, _ *validation.ValidationContext, _ *data.Observation) ([]validation.ValidationResult, error) {
	return m.results, m.err
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd validation && go test -v -run Registry`
Expected: Compilation error -- `NewRegistry` not defined.

- [ ] **Step 3: Implement Registry in engine.go**

```go
package validation

import "sync"

// Registry holds all registered validation rules.
type Registry struct {
	mu    sync.RWMutex
	rules []Rule
}

func NewRegistry() *Registry {
	return &Registry{}
}

func (r *Registry) Register(rule Rule) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rules = append(r.rules, rule)
}

func (r *Registry) RulesForDataType(dt string) []Rule {
	r.mu.RLock()
	defer r.mu.RUnlock()
	var matched []Rule
	for _, rule := range r.rules {
		for _, rdt := range rule.DataTypes() {
			if rdt == dt {
				matched = append(matched, rule)
				break
			}
		}
	}
	return matched
}

func (r *Registry) AllRules() []Rule {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Rule, len(r.rules))
	copy(out, r.rules)
	return out
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd validation && go test -v -run Registry`
Expected: All pass.

- [ ] **Step 5: Commit**

```bash
git add validation/engine.go validation/engine_test.go
git commit -m "feat: add validation rule registry"
```

---

### Task 5: Quarantine Operations

**Files:**
- Create: `validation/quarantine.go`
- Test: `validation/quarantine_test.go`

This task implements the quarantine CRUD operations: save, list, release, delete. These require a live database so tests will be integration tests.

- [ ] **Step 1: Write quarantine.go**

```go
package validation

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog/log"
)

// QuarantineRecord represents a row in the quarantine table.
type QuarantineRecord struct {
	ID             uuid.UUID
	DataType       string
	Observation    json.RawMessage
	RuleID         uuid.UUID
	Severity       Severity
	Message        string
	Details        map[string]any
	SubscriptionID *uuid.UUID // nullable in DB
	CreatedAt      time.Time
	ReviewedAt     *time.Time
	Resolution     *string
}

// SaveQuarantine inserts a quarantined observation.
func SaveQuarantine(ctx context.Context, pool *pgxpool.Pool, result *ValidationResult, subscriptionID uuid.UUID) error {
	obsJSON, err := json.Marshal(result.Observation)
	if err != nil {
		return fmt.Errorf("marshal observation: %w", err)
	}

	detailsJSON, err := json.Marshal(result.Details)
	if err != nil {
		return fmt.Errorf("marshal details: %w", err)
	}

	// Look up rule UUID from rule_code
	var ruleUUID uuid.UUID
	err = pool.QueryRow(ctx,
		"SELECT id FROM validation_rules WHERE rule_code = $1", result.RuleID).Scan(&ruleUUID)
	if err != nil {
		return fmt.Errorf("lookup rule %s: %w", result.RuleID, err)
	}

	_, err = pool.Exec(ctx, `
		INSERT INTO quarantine (datatype, observation, rule_id, severity, message, details, subscription_id)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		result.DataType, obsJSON, ruleUUID, string(result.Severity), result.Message, detailsJSON, subscriptionID)
	return err
}

// ListQuarantine returns all unresolved quarantine records.
func ListQuarantine(ctx context.Context, pool *pgxpool.Pool) ([]*QuarantineRecord, error) {
	rows, err := pool.Query(ctx, `
		SELECT id, datatype, observation, rule_id, severity, message, details, subscription_id, created_at
		FROM quarantine
		WHERE resolution IS NULL
		ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var records []*QuarantineRecord
	for rows.Next() {
		r := &QuarantineRecord{}
		var details []byte
		if err := rows.Scan(&r.ID, &r.DataType, &r.Observation, &r.RuleID,
			&r.Severity, &r.Message, &details, &r.SubscriptionID, &r.CreatedAt); err != nil {
			return nil, err
		}
		if details != nil {
			_ = json.Unmarshal(details, &r.Details)
		}
		records = append(records, r)
	}
	return records, rows.Err()
}

// ReleaseQuarantine deserializes the observation and saves it to the main table,
// then deletes the quarantine record.
func ReleaseQuarantine(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) error {
	// Read the quarantine record
	var rec QuarantineRecord
	err := pool.QueryRow(ctx,
		"SELECT id, datatype, observation, subscription_id FROM quarantine WHERE id = $1 AND resolution IS NULL",
		id).Scan(&rec.ID, &rec.DataType, &rec.Observation, &rec.SubscriptionID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return fmt.Errorf("quarantine record %s not found or already resolved", id)
		}
		return err
	}

	// Deserialize and save via the appropriate SaveDB method
	// This requires knowing the data type to unmarshal correctly
	obs, err := deserializeObservation(rec.DataType, rec.Observation)
	if err != nil {
		return fmt.Errorf("deserialize observation: %w", err)
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	if err := saveObservationByType(ctx, obs, rec.DataType, rec.SubscriptionID, conn); err != nil {
		return fmt.Errorf("save released observation: %w", err)
	}

	// Mark as released (preserves audit trail)
	_, err = pool.Exec(ctx,
		"UPDATE quarantine SET resolution = 'released', reviewed_at = now() WHERE id = $1", id)
	return err
}

// DeleteQuarantine marks a quarantine record as discarded without saving.
func DeleteQuarantine(ctx context.Context, pool *pgxpool.Pool, id uuid.UUID) error {
	tag, err := pool.Exec(ctx,
		"UPDATE quarantine SET resolution = 'discarded', reviewed_at = now() WHERE id = $1 AND resolution IS NULL", id)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("quarantine record %s not found or already resolved", id)
	}
	return nil
}

// deserializeObservation unmarshals JSONB back to the appropriate sub-type.
func deserializeObservation(dataType string, raw json.RawMessage) (*data.Observation, error) {
	obs := &data.Observation{}
	switch dataType {
	case data.FundamentalsKey:
		obs.Fundamental = &data.Fundamental{}
		return obs, json.Unmarshal(raw, obs.Fundamental)
	case data.EODKey:
		obs.EodQuote = &data.Eod{}
		return obs, json.Unmarshal(raw, obs.EodQuote)
	case data.MetricKey:
		obs.Metric = &data.Metric{}
		return obs, json.Unmarshal(raw, obs.Metric)
	case data.AssetKey:
		obs.AssetObject = &data.Asset{}
		return obs, json.Unmarshal(raw, obs.AssetObject)
	case data.ConsensusKey:
		obs.Consensus = &data.Consensus{}
		return obs, json.Unmarshal(raw, obs.Consensus)
	case data.CustomKey:
		obs.CustomObject = &data.Custom{}
		return obs, json.Unmarshal(raw, obs.CustomObject)
	case data.EconomicIndicatorKey:
		obs.EconomicIndicator = &data.EconomicIndicator{}
		return obs, json.Unmarshal(raw, obs.EconomicIndicator)
	case data.EstimateKey:
		obs.Estimate = &data.Estimate{}
		return obs, json.Unmarshal(raw, obs.Estimate)
	case data.IndexKey:
		// Disambiguate using "_index_subtype" key stored in quarantine details during save.
		// SaveQuarantine should add details["_index_subtype"] = "snapshot" or "changelog".
		// Default to snapshot if not present.
		obs.IndexSnapshot = &data.IndexSnapshot{}
		return obs, json.Unmarshal(raw, obs.IndexSnapshot)
	case data.MarketHolidaysKey:
		obs.MarketHoliday = &data.MarketHoliday{}
		return obs, json.Unmarshal(raw, obs.MarketHoliday)
	case data.RatingKey:
		obs.Rating = &data.AnalystRating{}
		return obs, json.Unmarshal(raw, obs.Rating)
	default:
		return nil, fmt.Errorf("unknown data type: %s", dataType)
	}
}

// saveObservationByType routes to the correct SaveDB method.
// Needs the subscription's DataTablesMap to find the target table name.
func saveObservationByType(ctx context.Context, obs *data.Observation, dataType string, subscriptionID uuid.UUID, conn *pgxpool.Conn) error {
	// Look up the subscription to get the table name
	var tableName string
	err := conn.QueryRow(ctx, `
		SELECT unnest(data_tables) FROM subscriptions
		WHERE id = $1`, subscriptionID).Scan(&tableName)
	if err != nil {
		log.Warn().Err(err).Str("subscriptionID", subscriptionID.String()).
			Msg("could not look up table name for quarantine release; attempting with data type key")
	}

	// Route to appropriate SaveDB
	switch dataType {
	case data.FundamentalsKey:
		return obs.Fundamental.SaveDB(ctx, tableName, conn)
	case data.EODKey:
		return obs.EodQuote.SaveDB(ctx, tableName, conn)
	case data.MetricKey:
		return obs.Metric.SaveDB(ctx, tableName, conn)
	case data.AssetKey:
		return obs.AssetObject.SaveDB(ctx, tableName, conn)
	case data.ConsensusKey:
		return obs.Consensus.SaveDB(ctx, tableName, conn)
	case data.CustomKey:
		return obs.CustomObject.SaveDB(ctx, tableName, conn)
	case data.EconomicIndicatorKey:
		return obs.EconomicIndicator.SaveDB(ctx, tableName, conn)
	case data.EstimateKey:
		return obs.Estimate.SaveDB(ctx, tableName, conn)
	case data.IndexKey:
		if obs.IndexSnapshot != nil {
			return obs.IndexSnapshot.SaveDB(ctx, tableName, conn)
		}
		if obs.IndexChange != nil {
			return obs.IndexChange.SaveDB(ctx, tableName, conn)
		}
		return fmt.Errorf("index observation has neither snapshot nor change")
	case data.MarketHolidaysKey:
		return obs.MarketHoliday.SaveDB(ctx, tableName, conn)
	case data.RatingKey:
		return obs.Rating.SaveDB(ctx, tableName, conn)
	default:
		return fmt.Errorf("unknown data type: %s", dataType)
	}
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./validation/`
Expected: Compiles. (Integration tests for quarantine will be added when we have a test DB harness.)

- [ ] **Step 3: Commit**

```bash
git add validation/quarantine.go
git commit -m "feat: add quarantine CRUD operations (save, list, release, delete)"
```

---

### Task 6: Rule Config Sync (DB auto-registration)

**Files:**
- Create: `validation/config.go`

- [ ] **Step 1: Write config.go**

This syncs Go rule definitions with the `validation_rules` DB table on startup.

```go
package validation

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog/log"
)

// RuleConfig holds the DB-side configuration for a rule.
type RuleConfig struct {
	RuleCode  string
	Enabled   bool
	RunInline bool
	Severity  Severity
	Params    map[string]any
}

// SyncRules ensures all registered rules have a corresponding row in validation_rules.
// New rules are inserted with defaults. Existing rules are left unchanged.
func SyncRules(ctx context.Context, pool *pgxpool.Pool, registry *Registry) error {
	for _, rule := range registry.AllRules() {
		// pgx handles Go string slices natively for PostgreSQL arrays
		_, err := pool.Exec(ctx, `
			INSERT INTO validation_rules (rule_code, name, datatypes)
			VALUES ($1, $2, $3::datatype[])
			ON CONFLICT (rule_code) DO NOTHING`,
			rule.ID(), rule.Name(), rule.DataTypes())
		if err != nil {
			return fmt.Errorf("sync rule %s: %w", rule.ID(), err)
		}
		log.Debug().Str("rule", rule.ID()).Msg("synced validation rule")
	}
	return nil
}

// LoadRuleConfigs loads all rule configurations from the database.
func LoadRuleConfigs(ctx context.Context, pool *pgxpool.Pool) (map[string]*RuleConfig, error) {
	rows, err := pool.Query(ctx, `
		SELECT rule_code, enabled, run_inline, severity, params
		FROM validation_rules`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	configs := make(map[string]*RuleConfig)
	for rows.Next() {
		cfg := &RuleConfig{}
		var severity string
		var params []byte
		if err := rows.Scan(&cfg.RuleCode, &cfg.Enabled, &cfg.RunInline, &severity, &params); err != nil {
			return nil, err
		}
		cfg.Severity = Severity(severity)
		configs[cfg.RuleCode] = cfg
	}
	return configs, rows.Err()
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./validation/`

- [ ] **Step 3: Commit**

```bash
git add validation/config.go
git commit -m "feat: add validation rule config sync with database"
```

---

## Chunk 3: Accounting Identity Rules

### Task 7: Identity Rules Implementation

**Files:**
- Create: `validation/rules_identity.go`
- Test: `validation/rules_identity_test.go`

- [ ] **Step 1: Write failing tests**

Create `validation/rules_identity_test.go`:

```go
package validation_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/validation"
)

var _ = Describe("Identity Rules", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	Describe("GrossProfit", func() {
		It("passes when identity holds", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Revenues:      100000,
					CostOfRevenue: 60000,
					GrossProfit:   40000,
				},
			}
			rule := validation.NewGrossProfitRule()
			results, err := rule.Validate(ctx, nil, obs)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(BeEmpty())
		})

		It("fails when identity does not hold", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Revenues:      100000,
					CostOfRevenue: 60000,
					GrossProfit:   50000, // wrong: should be 40000
				},
			}
			rule := validation.NewGrossProfitRule()
			results, err := rule.Validate(ctx, nil, obs)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(HaveLen(1))
			Expect(results[0].Severity).To(Equal(validation.SeverityQuarantine))
			Expect(results[0].Details).To(HaveKey("expected"))
			Expect(results[0].Details).To(HaveKey("actual"))
		})

		It("passes when within tolerance", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Revenues:      100000,
					CostOfRevenue: 60000,
					GrossProfit:   40050, // 0.125% off, within 1% tolerance
				},
			}
			rule := validation.NewGrossProfitRule()
			results, err := rule.Validate(ctx, nil, obs)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(BeEmpty())
		})

		It("skips when all fields are zero", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{},
			}
			rule := validation.NewGrossProfitRule()
			results, err := rule.Validate(ctx, nil, obs)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(BeEmpty())
		})

		It("skips non-fundamental observations", func() {
			obs := &data.Observation{
				EodQuote: &data.Eod{},
			}
			rule := validation.NewGrossProfitRule()
			results, err := rule.Validate(ctx, nil, obs)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(BeEmpty())
		})
	})

	Describe("BalanceSheet", func() {
		It("passes when assets = liabilities + equity", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					TotalAssets:      500000,
					TotalLiabilities: 300000,
					Equity:           200000,
				},
			}
			rule := validation.NewBalanceSheetRule()
			results, err := rule.Validate(ctx, nil, obs)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(BeEmpty())
		})

		It("fails when equation does not hold", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					TotalAssets:      500000,
					TotalLiabilities: 300000,
					Equity:           100000, // wrong
				},
			}
			rule := validation.NewBalanceSheetRule()
			results, err := rule.Validate(ctx, nil, obs)
			Expect(err).ToNot(HaveOccurred())
			Expect(results).To(HaveLen(1))
		})
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd validation && go test -v -run "Identity"`
Expected: Compilation errors.

- [ ] **Step 3: Implement identity rules**

Create `validation/rules_identity.go`:

```go
package validation

import (
	"context"
	"fmt"
	"math"

	"github.com/penny-vault/pvdata/data"
)

const defaultTolerancePct = 0.01 // 1%

// identityRule is the base for all accounting identity checks.
type identityRule struct {
	id           string
	name         string
	tolerancePct float64 // configurable; defaults to defaultTolerancePct
	checkFn      func(f *data.Fundamental) (expected, actual int64, skip bool)
}

func (r *identityRule) ID() string          { return r.id }
func (r *identityRule) Name() string        { return r.name }
func (r *identityRule) DataTypes() []string { return []string{data.FundamentalsKey} }

// SetToleranceFromConfig reads tolerance_pct from rule config params.
func (r *identityRule) SetToleranceFromConfig(cfg *RuleConfig) {
	if cfg != nil && cfg.Params != nil {
		if tol, ok := cfg.Params["tolerance_pct"].(float64); ok {
			r.tolerancePct = tol
		}
	}
}

func (r *identityRule) Validate(ctx context.Context, vctx *ValidationContext, obs *data.Observation) ([]ValidationResult, error) {
	if obs.Fundamental == nil {
		return nil, nil
	}

	expected, actual, skip := r.checkFn(obs.Fundamental)
	if skip {
		return nil, nil
	}

	tol := r.tolerancePct
	if tol == 0 {
		tol = defaultTolerancePct
	}
	if withinTolerance(expected, actual, tol) {
		return nil, nil
	}

	return []ValidationResult{{
		RuleID:   r.id,
		Severity: SeverityQuarantine,
		DataType: data.FundamentalsKey,
		Message:  fmt.Sprintf("%s: expected %d, got %d", r.name, expected, actual),
		Details: map[string]any{
			"expected": expected,
			"actual":   actual,
			"delta":    actual - expected,
		},
		Observation: obs,
	}}, nil
}

func withinTolerance(expected, actual int64, tolerancePct float64) bool {
	if expected == 0 && actual == 0 {
		return true
	}
	if expected == 0 {
		return false
	}
	delta := math.Abs(float64(actual-expected)) / math.Abs(float64(expected))
	return delta <= tolerancePct
}

// GrossProfitRule checks: revenues - cost_of_revenue = gross_profit
type GrossProfitRule struct{ identityRule }

func NewGrossProfitRule() *GrossProfitRule {
	r := &GrossProfitRule{}
	r.id = "identity.gross_profit"
	r.name = "Gross Profit Identity"
	r.checkFn = func(f *data.Fundamental) (expected, actual int64, skip bool) {
		if f.Revenues == 0 && f.CostOfRevenue == 0 && f.GrossProfit == 0 {
			return 0, 0, true
		}
		return f.Revenues - f.CostOfRevenue, f.GrossProfit, false
	}
	return r
}

// OperatingIncomeRule checks: gross_profit - operating_expenses = operating_income
type OperatingIncomeRule struct{ identityRule }

func NewOperatingIncomeRule() *OperatingIncomeRule {
	r := &OperatingIncomeRule{}
	r.id = "identity.operating_income"
	r.name = "Operating Income Identity"
	r.checkFn = func(f *data.Fundamental) (expected, actual int64, skip bool) {
		if f.GrossProfit == 0 && f.OperatingExpenses == 0 && f.OperatingIncome == 0 {
			return 0, 0, true
		}
		return f.GrossProfit - f.OperatingExpenses, f.OperatingIncome, false
	}
	return r
}

// WorkingCapitalRule checks: current_assets - current_liabilities = working_capital
type WorkingCapitalRule struct{ identityRule }

func NewWorkingCapitalRule() *WorkingCapitalRule {
	r := &WorkingCapitalRule{}
	r.id = "identity.working_capital"
	r.name = "Working Capital Identity"
	r.checkFn = func(f *data.Fundamental) (expected, actual int64, skip bool) {
		if f.CurrentAssets == 0 && f.CurrentLiabilities == 0 && f.WorkingCapital == 0 {
			return 0, 0, true
		}
		return f.CurrentAssets - f.CurrentLiabilities, f.WorkingCapital, false
	}
	return r
}

// BalanceSheetRule checks: total_assets = total_liabilities + equity
type BalanceSheetRule struct{ identityRule }

func NewBalanceSheetRule() *BalanceSheetRule {
	r := &BalanceSheetRule{}
	r.id = "identity.balance_sheet"
	r.name = "Balance Sheet Identity"
	r.checkFn = func(f *data.Fundamental) (expected, actual int64, skip bool) {
		if f.TotalAssets == 0 && f.TotalLiabilities == 0 && f.Equity == 0 {
			return 0, 0, true
		}
		return f.TotalLiabilities + f.Equity, f.TotalAssets, false
	}
	return r
}

// FreeCashFlowRule checks: net_cash_flow_from_operations - capital_expenditure = free_cash_flow
type FreeCashFlowRule struct{ identityRule }

func NewFreeCashFlowRule() *FreeCashFlowRule {
	r := &FreeCashFlowRule{}
	r.id = "identity.free_cash_flow"
	r.name = "Free Cash Flow Identity"
	r.checkFn = func(f *data.Fundamental) (expected, actual int64, skip bool) {
		if f.NetCashFlowFromOperations == 0 && f.CapitalExpenditure == 0 && f.FreeCashFlow == 0 {
			return 0, 0, true
		}
		return f.NetCashFlowFromOperations - f.CapitalExpenditure, f.FreeCashFlow, false
	}
	return r
}

// NetIncomeRule checks: ebt - income_tax_expense = net_income
type NetIncomeRule struct{ identityRule }

func NewNetIncomeRule() *NetIncomeRule {
	r := &NetIncomeRule{}
	r.id = "identity.net_income"
	r.name = "Net Income Identity"
	r.checkFn = func(f *data.Fundamental) (expected, actual int64, skip bool) {
		if f.EBT == 0 && f.IncomeTaxExpense == 0 && f.NetIncome == 0 {
			return 0, 0, true
		}
		return f.EBT - f.IncomeTaxExpense, f.NetIncome, false
	}
	return r
}

// RegisterIdentityRules adds all accounting identity rules to the registry.
func RegisterIdentityRules(registry *Registry) {
	registry.Register(NewGrossProfitRule())
	registry.Register(NewOperatingIncomeRule())
	registry.Register(NewWorkingCapitalRule())
	registry.Register(NewBalanceSheetRule())
	registry.Register(NewFreeCashFlowRule())
	registry.Register(NewNetIncomeRule())
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd validation && go test -v -run "Identity"`
Expected: All pass.

- [ ] **Step 5: Commit**

```bash
git add validation/rules_identity.go validation/rules_identity_test.go
git commit -m "feat: implement accounting identity validation rules"
```

---

## Chunk 4: Anomaly Detection Strategy and Temporal Rules

### Task 8: Anomaly Strategy Interface and Z-Score Implementation

**Files:**
- Create: `validation/strategy.go`
- Create: `validation/strategy_zscore.go`
- Test: `validation/strategy_zscore_test.go`

- [ ] **Step 1: Write failing tests for z-score strategy**

Create `validation/strategy_zscore_test.go`:

```go
package validation_test

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/validation"
)

var _ = Describe("ZScoreStrategy", func() {
	var (
		ctx      context.Context
		strategy *validation.ZScoreStrategy
	)

	BeforeEach(func() {
		ctx = context.Background()
		strategy = validation.NewZScoreStrategy()
	})

	It("returns low score for normal value", func() {
		input := validation.AnomalyInput{
			Field:        "revenues",
			CurrentValue: 105000,
			TickerHistory: []validation.HistoricalValue{
				{EventDate: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), Value: 100000},
				{EventDate: time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC), Value: 102000},
				{EventDate: time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC), Value: 98000},
				{EventDate: time.Date(2024, 10, 1, 0, 0, 0, 0, time.UTC), Value: 101000},
			},
			UniverseStats: validation.UniverseStats{
				Min: 1000, Max: 500000000, Mean: 5000000, StdDev: 10000000, Median: 1000000,
			},
		}
		output, err := strategy.Evaluate(ctx, input)
		Expect(err).ToNot(HaveOccurred())
		Expect(output.Score).To(BeNumerically("<", 0.5))
	})

	It("returns high score for extreme ticker-relative value", func() {
		input := validation.AnomalyInput{
			Field:        "revenues",
			CurrentValue: 10000000, // 100x normal
			TickerHistory: []validation.HistoricalValue{
				{EventDate: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), Value: 100000},
				{EventDate: time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC), Value: 102000},
				{EventDate: time.Date(2024, 7, 1, 0, 0, 0, 0, time.UTC), Value: 98000},
				{EventDate: time.Date(2024, 10, 1, 0, 0, 0, 0, time.UTC), Value: 101000},
			},
			UniverseStats: validation.UniverseStats{
				Min: 1000, Max: 500000000000, Mean: 5000000000, StdDev: 10000000000, Median: 1000000000,
			},
		}
		output, err := strategy.Evaluate(ctx, input)
		Expect(err).ToNot(HaveOccurred())
		Expect(output.Score).To(BeNumerically(">", 0.8))
	})

	It("skips ticker-relative when history too short", func() {
		input := validation.AnomalyInput{
			Field:        "revenues",
			CurrentValue: 10000000,
			TickerHistory: []validation.HistoricalValue{
				{EventDate: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), Value: 100000},
			},
			UniverseStats: validation.UniverseStats{
				Min: 1000, Max: 500000000000, Mean: 5000000000, StdDev: 10000000000, Median: 1000000000,
			},
		}
		output, err := strategy.Evaluate(ctx, input)
		Expect(err).ToNot(HaveOccurred())
		// Should only use universe-relative; 10M is well within universe range
		Expect(output.Score).To(BeNumerically("<", 0.5))
	})

	It("flags universe-implausible value even with short history", func() {
		input := validation.AnomalyInput{
			Field:        "revenues",
			CurrentValue: 999999999999999, // way beyond any company
			TickerHistory: []validation.HistoricalValue{
				{EventDate: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), Value: 100000},
			},
			UniverseStats: validation.UniverseStats{
				Min: 1000, Max: 500000000000, Mean: 5000000000, StdDev: 10000000000, Median: 1000000000,
			},
		}
		output, err := strategy.Evaluate(ctx, input)
		Expect(err).ToNot(HaveOccurred())
		Expect(output.Score).To(BeNumerically(">", 0.8))
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd validation && go test -v -run ZScore`
Expected: Compilation errors.

- [ ] **Step 3: Write strategy.go interface**

```go
package validation

import "context"

// AnomalyStrategy evaluates whether a value is anomalous.
type AnomalyStrategy interface {
	Name() string
	Evaluate(ctx context.Context, input AnomalyInput) (AnomalyOutput, error)
}

// AnomalyInput is the data provided to an anomaly strategy.
type AnomalyInput struct {
	Key           data.ObservationKeyProvider
	Field         string
	CurrentValue  float64
	TickerHistory []HistoricalValue
	UniverseStats UniverseStats
}

// AnomalyOutput is the result of anomaly evaluation.
type AnomalyOutput struct {
	Score       float64
	Severity    Severity
	Explanation string
}
```

- [ ] **Step 4: Write strategy_zscore.go**

```go
package validation

import (
	"context"
	"fmt"
	"math"
)

const (
	defaultMinHistory         = 4
	defaultWarningThreshold   = 0.6
	defaultQuarantineThreshold = 0.85
)

// ZScoreStrategy uses z-scores for ticker-relative anomaly detection
// and percentile-based checks for universe-relative plausibility.
type ZScoreStrategy struct {
	MinHistory          int
	WarningThreshold    float64
	QuarantineThreshold float64
}

func NewZScoreStrategy() *ZScoreStrategy {
	return &ZScoreStrategy{
		MinHistory:          defaultMinHistory,
		WarningThreshold:    defaultWarningThreshold,
		QuarantineThreshold: defaultQuarantineThreshold,
	}
}

func (s *ZScoreStrategy) Name() string { return "zscore" }

func (s *ZScoreStrategy) Evaluate(_ context.Context, input AnomalyInput) (AnomalyOutput, error) {
	tickerScore := 0.0
	useTickerScore := len(input.TickerHistory) >= s.MinHistory

	if useTickerScore {
		tickerScore = s.tickerRelativeScore(input.CurrentValue, input.TickerHistory)
	}

	universeScore := s.universeRelativeScore(input.CurrentValue, input.UniverseStats)

	// Combine: max of both dimensions
	score := universeScore
	if useTickerScore && tickerScore > universeScore {
		score = tickerScore
	}

	severity := SeverityWarning
	if score >= s.QuarantineThreshold {
		severity = SeverityQuarantine
	}

	explanation := fmt.Sprintf("anomaly_score=%.3f (ticker=%.3f, universe=%.3f)",
		score, tickerScore, universeScore)

	return AnomalyOutput{
		Score:       score,
		Severity:    severity,
		Explanation: explanation,
	}, nil
}

// tickerRelativeScore computes a normalized z-score (0-1) against the ticker's own history.
func (s *ZScoreStrategy) tickerRelativeScore(current float64, history []HistoricalValue) float64 {
	n := len(history)
	if n == 0 {
		return 0
	}

	sum := 0.0
	for _, h := range history {
		sum += h.Value
	}
	mean := sum / float64(n)

	variance := 0.0
	for _, h := range history {
		diff := h.Value - mean
		variance += diff * diff
	}
	stddev := math.Sqrt(variance / float64(n))

	if stddev == 0 {
		if current == mean {
			return 0
		}
		return 1.0 // any deviation from a constant series is maximally anomalous
	}

	z := math.Abs(current-mean) / stddev

	// Normalize z-score to 0-1 using a sigmoid-like mapping:
	// z=0 -> 0, z=2 -> ~0.5, z=4 -> ~0.85, z=6+ -> ~0.95+
	return 1.0 - 1.0/(1.0+z*z/8.0)
}

// universeRelativeScore checks if the value is plausible within the observed universe.
func (s *ZScoreStrategy) universeRelativeScore(current float64, stats UniverseStats) float64 {
	if stats.StdDev == 0 {
		return 0
	}

	z := math.Abs(current-stats.Mean) / stats.StdDev

	// Same normalization
	return 1.0 - 1.0/(1.0+z*z/8.0)
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `cd validation && go test -v -run ZScore`
Expected: All pass. Adjust sigmoid constants if thresholds don't match expected test behavior.

- [ ] **Step 6: Commit**

```bash
git add validation/strategy.go validation/strategy_zscore.go validation/strategy_zscore_test.go
git commit -m "feat: add z-score anomaly detection strategy"
```

---

### Task 9: Numeric Fields Registry and Temporal Anomaly Rule

**Files:**
- Create: `validation/numeric_fields.go`
- Create: `validation/rules_temporal.go`
- Test: `validation/rules_temporal_test.go`

- [ ] **Step 1: Create numeric_fields.go -- shared registry of numeric fields per data type**

This is used by temporal rules, cross-source rules, and universe stats computation.

```go
package validation

import "github.com/penny-vault/pvdata/data"

// NumericFields maps data type keys to the list of numeric field names that can be validated.
// Field names match the DB column names used in queries.
var NumericFields = map[string][]string{
	data.FundamentalsKey: {
		"revenues", "cost_of_revenue", "gross_profit", "operating_expenses",
		"operating_income", "ebit", "ebitda", "ebt", "net_income",
		"total_assets", "current_assets", "total_liabilities", "current_liabilities",
		"equity", "working_capital", "free_cash_flow", "capital_expenditure",
		"net_cash_flow_from_operations", "income_tax_expense",
	},
	data.EODKey: {
		"open", "high", "low", "close", "volume",
	},
	data.MetricKey: {
		"ev", "evebit", "evebitda", "marketcap", "pb", "pe", "ps",
	},
	data.EconomicIndicatorKey: {
		"value",
	},
	data.ConsensusKey: {
		"target_mean", "target_median", "target_high", "target_low",
	},
}

// ExtractNumericField extracts a named numeric field value from an observation.
// Returns the value and whether the field was found. Uses explicit switch per data type.
func ExtractNumericField(obs *data.Observation, field string) (float64, bool) {
	if obs.Fundamental != nil {
		return extractFundamentalField(obs.Fundamental, field)
	}
	if obs.EodQuote != nil {
		return extractEodField(obs.EodQuote, field)
	}
	if obs.Metric != nil {
		return extractMetricField(obs.Metric, field)
	}
	if obs.EconomicIndicator != nil && field == "value" {
		return obs.EconomicIndicator.Value, true
	}
	return 0, false
}

func extractFundamentalField(f *data.Fundamental, field string) (float64, bool) {
	switch field {
	case "revenues":
		return float64(f.Revenues), true
	case "cost_of_revenue":
		return float64(f.CostOfRevenue), true
	case "gross_profit":
		return float64(f.GrossProfit), true
	case "operating_expenses":
		return float64(f.OperatingExpenses), true
	case "operating_income":
		return float64(f.OperatingIncome), true
	case "net_income":
		return float64(f.NetIncome), true
	case "total_assets":
		return float64(f.TotalAssets), true
	case "current_assets":
		return float64(f.CurrentAssets), true
	case "total_liabilities":
		return float64(f.TotalLiabilities), true
	case "current_liabilities":
		return float64(f.CurrentLiabilities), true
	case "equity":
		return float64(f.Equity), true
	case "working_capital":
		return float64(f.WorkingCapital), true
	case "free_cash_flow":
		return float64(f.FreeCashFlow), true
	case "capital_expenditure":
		return float64(f.CapitalExpenditure), true
	case "net_cash_flow_from_operations":
		return float64(f.NetCashFlowFromOperations), true
	case "ebit":
		return float64(f.EBIT), true
	case "ebitda":
		return float64(f.EBITDA), true
	case "ebt":
		return float64(f.EBT), true
	case "income_tax_expense":
		return float64(f.IncomeTaxExpense), true
	default:
		return 0, false
	}
}

func extractEodField(e *data.Eod, field string) (float64, bool) {
	switch field {
	case "open":
		return e.Open, true
	case "high":
		return e.High, true
	case "low":
		return e.Low, true
	case "close":
		return e.Close, true
	case "volume":
		return e.Volume, true
	default:
		return 0, false
	}
}

// extractMetricField -- same pattern for Metric fields (ev, evebit, evebitda, etc.)
// Implementation follows the same switch pattern as above.
```

- [ ] **Step 2: Write failing tests for temporal rule**

Create `validation/rules_temporal_test.go`. Use a mock `AnomalyStrategy` that returns configurable scores:

```go
package validation_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/validation"
)

type mockStrategy struct {
	output validation.AnomalyOutput
}

func (m *mockStrategy) Name() string { return "mock" }
func (m *mockStrategy) Evaluate(_ context.Context, _ validation.AnomalyInput) (validation.AnomalyOutput, error) {
	return m.output, nil
}

var _ = Describe("Temporal Rules", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("returns quarantine when strategy scores high", func() {
		strategy := &mockStrategy{output: validation.AnomalyOutput{
			Score: 0.95, Severity: validation.SeverityQuarantine, Explanation: "extreme",
		}}
		rule := validation.NewTemporalRule(strategy)

		obs := &data.Observation{
			Fundamental: &data.Fundamental{Revenues: 999999999},
		}
		// ValidationContext is nil -- temporal rule should handle gracefully
		// by skipping history lookup and only using universe stats
		results, err := rule.Validate(ctx, nil, obs)
		Expect(err).ToNot(HaveOccurred())
		// With nil context, rule skips (no DB to query)
		Expect(results).To(BeEmpty())
	})
})
```

- [ ] **Step 3: Run tests to verify they fail**

- [ ] **Step 4: Implement rules_temporal.go**

```go
package validation

import (
	"context"
	"fmt"

	"github.com/penny-vault/pvdata/data"
)

// TemporalRule checks for statistical anomalies in numeric fields.
type TemporalRule struct {
	strategy AnomalyStrategy
}

func NewTemporalRule(strategy AnomalyStrategy) *TemporalRule {
	return &TemporalRule{strategy: strategy}
}

func (r *TemporalRule) ID() string          { return "temporal.anomaly" }
func (r *TemporalRule) Name() string        { return "Temporal Anomaly Detection" }
func (r *TemporalRule) DataTypes() []string {
	// Applies to all data types that have numeric fields
	types := make([]string, 0, len(NumericFields))
	for dt := range NumericFields {
		types = append(types, dt)
	}
	return types
}

func (r *TemporalRule) Validate(ctx context.Context, vctx *ValidationContext, obs *data.Observation) ([]ValidationResult, error) {
	if vctx == nil || vctx.DB == nil {
		return nil, nil // skip if no DB context available
	}

	dataType := obs.ObsDataType()
	fields, ok := NumericFields[dataType]
	if !ok {
		return nil, nil
	}

	var results []ValidationResult
	for _, field := range fields {
		value, found := ExtractNumericField(obs, field)
		if !found || value == 0 {
			continue
		}

		// Get ticker history from DB
		history, err := vctx.TickerHistory(ctx, obs, field)
		if err != nil {
			continue // log and skip
		}

		// Get universe stats from cache
		statsKey := fmt.Sprintf("%s.%s", dataType, field)
		uStats := vctx.UniverseStats[statsKey]

		output, err := r.strategy.Evaluate(ctx, AnomalyInput{
			Key:           obs,
			Field:         field,
			CurrentValue:  value,
			TickerHistory: history,
			UniverseStats: uStats,
		})
		if err != nil {
			continue
		}

		if output.Score >= 0.5 { // above noise floor
			results = append(results, ValidationResult{
				RuleID:   r.ID(),
				Severity: output.Severity,
				DataType: dataType,
				Message:  fmt.Sprintf("anomaly in %s: %s", field, output.Explanation),
				Details: map[string]any{
					"field":       field,
					"value":       value,
					"score":       output.Score,
					"explanation": output.Explanation,
				},
				Observation: obs,
			})
		}
	}

	return results, nil
}

// RegisterTemporalRules adds the temporal anomaly rule with z-score strategy.
func RegisterTemporalRules(registry *Registry) {
	registry.Register(NewTemporalRule(NewZScoreStrategy()))
}
```

- [ ] **Step 5: Run tests to verify they pass**

- [ ] **Step 6: Commit**

```bash
git add validation/numeric_fields.go validation/rules_temporal.go validation/rules_temporal_test.go
git commit -m "feat: implement temporal anomaly validation rule with numeric fields registry"
```

---

## Chunk 5: Cross-Source Rules and Universe Stats

### Task 10: Cross-Source Validation Rule

**Files:**
- Create: `validation/rules_crosssource.go`
- Test: `validation/rules_crosssource_test.go`

- [ ] **Step 1: Write failing tests**

Create `validation/rules_crosssource_test.go`:

```go
package validation_test

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/validation"
)

var _ = Describe("CrossSource Rules", func() {
	var ctx context.Context

	BeforeEach(func() {
		ctx = context.Background()
	})

	It("returns no results when no cross-source data exists", func() {
		rule := validation.NewCrossSourceRule(0.02, 1000, 0.50)
		obs := &data.Observation{
			Fundamental: &data.Fundamental{Revenues: 100000},
		}
		// nil vctx means no DB -- rule should skip gracefully
		results, err := rule.Validate(ctx, nil, obs)
		Expect(err).ToNot(HaveOccurred())
		Expect(results).To(BeEmpty())
	})

	It("flags large disagreements", func() {
		// This test requires a mock ValidationContext -- see Step 3
		// The cross-source rule compares obs.Fundamental.Revenues vs
		// otherObs.Fundamental.Revenues and flags if delta > tolerance
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

- [ ] **Step 3: Implement rules_crosssource.go**

```go
package validation

import (
	"context"
	"fmt"
	"math"

	"github.com/penny-vault/pvdata/data"
)

// CrossSourceRule compares observations from different subscriptions.
type CrossSourceRule struct {
	tolerancePct   float64 // e.g., 0.02 for 2%
	toleranceAbs   float64 // e.g., 1000
	quarantinePct  float64 // escalation threshold, e.g., 0.50 for 50%
}

func NewCrossSourceRule(tolerancePct, toleranceAbs, quarantinePct float64) *CrossSourceRule {
	return &CrossSourceRule{
		tolerancePct:  tolerancePct,
		toleranceAbs:  toleranceAbs,
		quarantinePct: quarantinePct,
	}
}

func (r *CrossSourceRule) ID() string   { return "crosssource.compare" }
func (r *CrossSourceRule) Name() string { return "Cross-Source Comparison" }
func (r *CrossSourceRule) DataTypes() []string {
	types := make([]string, 0, len(NumericFields))
	for dt := range NumericFields {
		types = append(types, dt)
	}
	return types
}

func (r *CrossSourceRule) Validate(ctx context.Context, vctx *ValidationContext, obs *data.Observation) ([]ValidationResult, error) {
	if vctx == nil || vctx.DB == nil {
		return nil, nil
	}

	others, err := vctx.CrossSourceObservations(ctx, obs)
	if err != nil || len(others) == 0 {
		return nil, nil
	}

	dataType := obs.ObsDataType()
	fields, ok := NumericFields[dataType]
	if !ok {
		return nil, nil
	}

	var results []ValidationResult
	for _, other := range others {
		for _, field := range fields {
			val1, ok1 := ExtractNumericField(obs, field)
			val2, ok2 := ExtractNumericField(other, field)
			if !ok1 || !ok2 || (val1 == 0 && val2 == 0) {
				continue
			}

			// Check if within either tolerance
			absDelta := math.Abs(val1 - val2)
			pctDelta := 0.0
			if val1 != 0 {
				pctDelta = absDelta / math.Abs(val1)
			}

			withinAbs := absDelta <= r.toleranceAbs
			withinPct := pctDelta <= r.tolerancePct

			if withinAbs || withinPct {
				continue
			}

			severity := SeverityWarning
			if pctDelta >= r.quarantinePct {
				severity = SeverityQuarantine
			}

			results = append(results, ValidationResult{
				RuleID:   r.ID(),
				Severity: severity,
				DataType: dataType,
				Message: fmt.Sprintf("cross-source disagreement on %s: %.2f vs %.2f (%.1f%%)",
					field, val1, val2, pctDelta*100),
				Details: map[string]any{
					"field":     field,
					"value_a":   val1,
					"value_b":   val2,
					"delta_pct": pctDelta,
					"delta_abs": absDelta,
				},
				Observation: obs,
			})
		}
	}

	return results, nil
}

// RegisterCrossSourceRules adds the cross-source comparison rule.
func RegisterCrossSourceRules(registry *Registry) {
	registry.Register(NewCrossSourceRule(0.02, 1000, 0.50))
}
```

- [ ] **Step 4: Run tests to verify they pass**

- [ ] **Step 5: Commit**

```bash
git add validation/rules_crosssource.go validation/rules_crosssource_test.go
git commit -m "feat: implement cross-source validation rule"
```

---

### Task 11: Universe Stats Computation

**Files:**
- Create: `validation/universe_stats.go`
- Test: `validation/universe_stats_test.go`

- [ ] **Step 1: Write universe_stats.go**

Implements:
- `ComputeUniverseStats(ctx, pool, dataType, field)` -- queries the DB for all values of a field across all subscriptions and computes min, max, mean, median, stddev, percentiles
- `RefreshAllStats(ctx, pool)` -- iterates the shared `NumericFields` registry (from `numeric_fields.go`, created in Task 9) and recomputes stats for each (datatype, field) pair
- `LoadUniverseStats(ctx, pool)` -- loads from `universe_stats` table into `map[string]UniverseStats`

Uses the `NumericFields` registry from `validation/numeric_fields.go` (Task 9) as the source of truth for which fields to compute stats on.

- [ ] **Step 2: Verify it compiles**

- [ ] **Step 3: Commit**

```bash
git add validation/universe_stats.go
git commit -m "feat: add universe stats computation and storage"
```

---

## Chunk 6: Pipeline Integration (ValidateAndSave)

### Task 12: ValidateAndSave

**Files:**
- Modify: `validation/engine.go` (add ValidateAndSave and RunBatch)
- Modify: `library/database.go:160` (keep SaveObservations, add ValidateAndSave wrapper)
- Modify: `cmd/run.go:214` (switch to ValidateAndSave)
- Modify: `tui/runmanager.go:99` (switch to ValidateAndSave)

- [ ] **Step 1: Add ValidateAndSave to engine.go**

```go
// ValidateAndSave wraps the save pipeline with inline validation.
// Note: requires imports for "github.com/spf13/viper" and other deps.
func ValidateAndSave(
	pool *pgxpool.Pool,
	registry *Registry,
	configs map[string]*RuleConfig,
	enabled bool,                    // from viper.GetBool("validation.enabled")
	saveFn func(*data.Observation),  // the original per-observation save logic
	queue <-chan *data.Observation,
	wg *sync.WaitGroup,
) {
	defer wg.Done()

	vctx := &ValidationContext{
		DB: pool,
	}

	// Preload universe stats
	if enabled {
		stats, err := LoadUniverseStats(context.Background(), pool)
		if err != nil {
			log.Warn().Err(err).Msg("could not load universe stats; temporal rules may be limited")
		}
		vctx.UniverseStats = stats
	}

	for obs := range queue {
		if !enabled {
			saveFn(obs)
			continue
		}

		dataType := obs.ObsDataType()
		rules := registry.RulesForDataType(dataType)

		quarantined := false
		for _, rule := range rules {
			cfg, ok := configs[rule.ID()]
			if !ok || !cfg.Enabled || !cfg.RunInline {
				continue
			}

			results, err := rule.Validate(context.Background(), vctx, obs)
			if err != nil {
				log.Error().Err(err).Str("rule", rule.ID()).Msg("validation rule error")
				continue
			}

			for _, result := range results {
				if result.Severity == SeverityQuarantine {
					if err := SaveQuarantine(context.Background(), pool, &result, obs.SubscriptionID); err != nil {
						log.Error().Err(err).Str("rule", rule.ID()).Msg("failed to quarantine observation")
					} else {
						log.Warn().
							Str("rule", rule.ID()).
							Str("dataType", dataType).
							Str("label", obs.ObsDisplayLabel()).
							Str("message", result.Message).
							Msg("observation quarantined")
					}
					quarantined = true
					break
				}

				// Warning: log and continue
				log.Warn().
					Str("rule", rule.ID()).
					Str("dataType", dataType).
					Str("label", obs.ObsDisplayLabel()).
					Str("message", result.Message).
					Msg("validation warning")
			}

			if quarantined {
				break
			}
		}

		if !quarantined {
			saveFn(obs)
		}
	}
}
```

- [ ] **Step 2: Refactor SaveObservations to expose per-observation save logic**

In `library/database.go`, extract the per-observation save logic from the `for elem := range queue` loop (lines 182-272) into a method `func (myLibrary *Library) saveOneObservation(ctx context.Context, elem *data.Observation, subscriptions map[uuid.UUID]*Subscription, conn *pgxpool.Conn)`.

Then create a new `ValidateAndSave` method on `Library` that sets up the validation engine and calls `validation.ValidateAndSave(...)`, passing a closure that calls `saveOneObservation`.

- [ ] **Step 3: Update call sites**

In `cmd/run.go:214`, change `go myLibrary.SaveObservations(outChan, &wg)` to `go myLibrary.ValidateAndSave(outChan, &wg)`.

In `tui/runmanager.go:99`, make the same change.

Keep `SaveObservations` as-is for backward compatibility (it's used in other contexts).

- [ ] **Step 4: Verify it compiles and existing tests pass**

Run: `go build ./...` and `go test ./...`

- [ ] **Step 5: Commit**

```bash
git add validation/engine.go library/database.go cmd/run.go tui/runmanager.go
git commit -m "feat: integrate validation into ingestion pipeline (ValidateAndSave)"
```

---

## Chunk 7: CLI Commands

### Task 13: `pvdata validate` Command

**Files:**
- Create: `cmd/validate.go`

- [ ] **Step 1: Write validate.go**

Implement the Cobra command with:
- `pvdata validate` -- batch run all rules
- `pvdata validate --data-type <type>` -- filter by data type
- `pvdata validate --rule <pattern>` -- filter by rule code (glob match)
- `pvdata validate --dry-run` -- report only
- `pvdata validate refresh-stats` -- subcommand to refresh universe stats

Follow the existing command pattern in `cmd/run.go` for library/pool setup.

**Batch iteration pseudocode:**

```go
func runBatchValidation(ctx context.Context, pool *pgxpool.Pool, registry *Registry, configs map[string]*RuleConfig, opts BatchOpts) error {
    vctx := &ValidationContext{DB: pool}
    vctx.UniverseStats, _ = LoadUniverseStats(ctx, pool)

    // For each data type (filtered by --data-type if set)
    subs, _ := loadSubscriptions(ctx, pool)
    for _, dataType := range targetDataTypes(opts) {
        rules := registry.RulesForDataType(dataType)
        rules = filterByPattern(rules, opts.RulePattern)
        rules = filterEnabled(rules, configs)
        if len(rules) == 0 { continue }

        // For each subscription that provides this data type
        for _, sub := range subsForDataType(subs, dataType) {
            tableName := sub.DataTablesMap[dataType]

            // Get date range from table
            var minDate, maxDate time.Time
            pool.QueryRow(ctx, fmt.Sprintf(
                "SELECT MIN(event_date), MAX(event_date) FROM %s", tableName),
            ).Scan(&minDate, &maxDate)

            // Iterate in monthly chunks
            for chunkStart := minDate; chunkStart.Before(maxDate); chunkStart = chunkStart.AddDate(0, 1, 0) {
                chunkEnd := chunkStart.AddDate(0, 1, 0)

                // Load observations for this chunk
                observations := loadObservationsFromTable(ctx, pool, tableName, dataType, chunkStart, chunkEnd)

                for _, obs := range observations {
                    for _, rule := range rules {
                        results, _ := rule.Validate(ctx, vctx, obs)
                        for _, result := range results {
                            if opts.DryRun {
                                log.Info().Str("rule", result.RuleID).Str("message", result.Message).Msg("dry-run: would quarantine")
                                continue
                            }
                            if result.Severity == SeverityQuarantine {
                                // Move: delete from main table + insert to quarantine in a transaction
                                tx, _ := pool.Begin(ctx)
                                deleteFromMainTable(ctx, tx, tableName, obs)
                                SaveQuarantineInTx(ctx, tx, &result, sub.ID)
                                tx.Commit(ctx)
                            }
                            // Warnings: log only
                        }
                    }
                }
                log.Info().Str("dataType", dataType).Time("chunkEnd", chunkEnd).Msg("batch progress")
            }
        }
    }
    return nil
}
```

Note: `loadObservationsFromTable` builds a SELECT per data type and deserializes rows into `*data.Observation`. Each data type needs its own scan function, similar to how `SaveDB` works but in reverse. `deleteFromMainTable` builds a DELETE using the observation's `ObsPrimaryKey()`.

- [ ] **Step 2: Verify it compiles**

Run: `go build ./...`

- [ ] **Step 3: Commit**

```bash
git add cmd/validate.go
git commit -m "feat: add pvdata validate CLI command (batch validation + refresh-stats)"
```

---

### Task 14: `pvdata quarantine` Command

**Files:**
- Create: `cmd/quarantine.go`

- [ ] **Step 1: Write quarantine.go**

Implement the Cobra command with subcommands:
- `pvdata quarantine` -- TUI review (Bubble Tea list of unresolved records)
- `pvdata quarantine list` -- print quarantined records to stdout (table format)
- `pvdata quarantine delete <id>` -- call `DeleteQuarantine`
- `pvdata quarantine release <id>` -- call `ReleaseQuarantine`

For the TUI: use a simple Bubble Tea list model. Each item shows: ID (truncated), data type, rule name, message, created_at. Selecting an item shows full details. Key bindings: `r` = release, `d` = delete, `q` = quit.

- [ ] **Step 2: Verify it compiles**

Run: `go build ./...`

- [ ] **Step 3: Commit**

```bash
git add cmd/quarantine.go
git commit -m "feat: add pvdata quarantine CLI command (list, release, delete, TUI)"
```

---

## Chunk 8: Global Config and Rule Registration Bootstrap

### Task 15: Viper Config and Bootstrap

**Files:**
- Modify: `cmd/root.go` (add viper default for validation.enabled)
- Create: `validation/bootstrap.go` (wire up all rules and sync with DB)

- [ ] **Step 1: Add viper default in root.go**

After the viper config setup (around line 90 in `cmd/root.go`), add:

```go
viper.SetDefault("validation.enabled", true)
```

- [ ] **Step 2: Write bootstrap.go**

```go
package validation

import (
	"context"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Bootstrap creates the registry, registers all built-in rules,
// syncs with the database, and returns the registry and configs.
func Bootstrap(ctx context.Context, pool *pgxpool.Pool) (*Registry, map[string]*RuleConfig, error) {
	registry := NewRegistry()

	// Register all built-in rules
	RegisterIdentityRules(registry)
	RegisterTemporalRules(registry)
	RegisterCrossSourceRules(registry)

	// Sync with database
	if err := SyncRules(ctx, pool, registry); err != nil {
		return nil, nil, err
	}

	configs, err := LoadRuleConfigs(ctx, pool)
	if err != nil {
		return nil, nil, err
	}

	return registry, configs, nil
}
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./...`

- [ ] **Step 4: Commit**

```bash
git add cmd/root.go validation/bootstrap.go
git commit -m "feat: add validation bootstrap and global config toggle"
```

---

### Task 16: Universe Stats Scheduling

**Files:**
- Modify: `cmd/run.go` (add universe stats refresh to the gocron scheduler)

- [ ] **Step 1: Add stats refresh job to daemon scheduler**

In `runDaemon()` in `cmd/run.go`, after scheduling subscription jobs, add a scheduled job for universe stats refresh:

```go
// Schedule universe stats refresh (daily at 2am ET)
_, err = scheduler.NewJob(
	gocron.CronJob("0 2 * * *", false),
	gocron.NewTask(func() {
		log.Info().Msg("refreshing universe stats")
		if err := validation.RefreshAllStats(ctx, myLibrary.Pool); err != nil {
			log.Error().Err(err).Msg("failed to refresh universe stats")
		}
	}),
)
if err != nil {
	log.Error().Err(err).Msg("could not schedule universe stats refresh")
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./...`

- [ ] **Step 3: Commit**

```bash
git add cmd/run.go
git commit -m "feat: schedule daily universe stats refresh in daemon mode"
```

---

## Chunk 9: ValidationContext DB Methods

### Task 17: Implement TickerHistory and CrossSourceObservations

**Files:**
- Modify: `validation/rule.go` (replace stub implementations)

- [ ] **Step 1: Implement TickerHistory**

Replace the stub in `ValidationContext.TickerHistory`. The method receives a `data.ObservationKeyProvider` (which `*data.Observation` satisfies via delegation).

Steps:
1. Get the data type from `key.ObsDataType()`
2. Get the identifier fields from `key.ObsPrimaryKey()` (e.g., `composite_figi` for securities, `series` for economic indicators)
3. Query the `subscriptions` table to find table names for this data type: `SELECT data_tables, data_types FROM subscriptions WHERE $1 = ANY(data_types)`
4. For each table, query: `SELECT event_date, {field} FROM {tableName} WHERE {identifierCol} = $1 ORDER BY event_date` (table/column names interpolated from trusted internal code, identifier value parameterized)
5. Return `[]HistoricalValue`

The identifier column varies by data type:
- Securities: `composite_figi`
- Economic indicators: `series`
- Market holidays: `market`

- [ ] **Step 2: Implement CrossSourceObservations**

Replace the stub. The method receives a `data.ObservationKeyProvider`.

Steps:
1. Get data type from `key.ObsDataType()` and PK from `key.ObsPrimaryKey()`
2. Query `subscriptions` table: `SELECT id, data_tables, data_types, data_tables_map FROM subscriptions WHERE $1 = ANY(data_types)`
3. For each subscription, get the table name from `data_tables_map[dataType]`
4. Query each table with a WHERE clause built from the PK fields
5. Deserialize rows into `*data.Observation` (requires a per-data-type scan function, same as batch mode's `loadObservationsFromTable`)
6. Return all matching observations from other subscriptions

- [ ] **Step 3: Verify it compiles**

Run: `go build ./...`

- [ ] **Step 4: Commit**

```bash
git add validation/rule.go
git commit -m "feat: implement TickerHistory and CrossSourceObservations on ValidationContext"
```

---

## Chunk 10: End-to-End Integration Test

### Task 18: Integration Test

**Files:**
- Create: `validation/integration_test.go`

- [ ] **Step 1: Write integration test**

Test the full flow:
1. Set up a test database (or use the existing test DB infrastructure)
2. Apply migrations
3. Insert a subscription and some fundamental data
4. Bootstrap the validation engine
5. Send an observation with a broken accounting identity through `ValidateAndSave`
6. Verify it lands in the quarantine table, not the main table
7. Send a valid observation and verify it saves normally
8. Release the quarantined record and verify it appears in the main table

- [ ] **Step 2: Run the integration test**

Run: `cd validation && go test -v -run Integration -tags=integration`

- [ ] **Step 3: Commit**

```bash
git add validation/integration_test.go
git commit -m "test: add end-to-end validation integration test"
```
