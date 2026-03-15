# Data Validation Design

## Problem

pv-data ingests financial data from multiple providers (Tiingo, Sharadar, Zacks, FRED, etc.) with minimal validation beyond database constraints. There is no mechanism to catch structurally broken data (failed accounting identities), statistical anomalies (1000x revenue jumps from data errors), or disagreements between providers reporting the same data. Bad data enters the main tables silently.

## Solution

A validation engine that runs on data types (not subscriptions), enforces rules both inline during ingestion and in batch mode, and routes flagged observations to either a quarantine table (pending human review) or a zerolog warning.

## Core Abstractions

### ObservationKey Interface

Each data type sub-struct (`Fundamental`, `Eod`, `Metric`, `EconomicIndicator`, etc.) implements `ObservationKey` to express its own identity, since primary keys vary by type (composite_figi + event_date for EOD, series + event_date for economic indicators, etc.):

```go
type ObservationKey interface {
    DataType() data.DataType
    PrimaryKey() map[string]any   // actual DB PK column values
    DisplayLabel() string         // human-readable for CLI output
}
```

The `Observation` container delegates to whichever inner type is populated. Rules receive `*data.Observation` and extract the sub-type to call `ObservationKey` methods.

### Rule Interface

```go
type Rule interface {
    ID() string                       // stable code identifier, e.g. "identity.gross_profit"
    Name() string                     // human-readable name
    DataTypes() []data.DataType       // which data types this rule applies to
    Validate(ctx context.Context, vctx *ValidationContext, obs *data.Observation) ([]ValidationResult, error)
}

// ValidationContext provides rules with access to historical data, universe stats,
// cross-source data, and DB queries. Populated by the engine before invoking rules.
type ValidationContext struct {
    DB             *pgxpool.Pool
    UniverseStats  map[string]UniverseStats   // keyed by "datatype.field"
}

// TickerHistory returns historical values for a given identifier, data type, and field.
// Used by temporal anomaly rules to assess ticker-relative change.
func (vc *ValidationContext) TickerHistory(key ObservationKey, field string) ([]HistoricalValue, error)

// CrossSourceObservations returns matching observations from all other subscriptions
// that provide the same data type. Used by cross-source validation rules.
func (vc *ValidationContext) CrossSourceObservations(key ObservationKey) ([]*data.Observation, error)
```

### ValidationResult

```go
type ValidationResult struct {
    RuleID      string
    Severity    Severity              // Warning or Quarantine
    DataType    data.DataType
    Message     string
    Details     map[string]any        // rule-specific evidence (expected vs actual, delta, etc.)
    Observation *data.Observation     // the full observation, used for quarantine storage
}
```

Severity is a Go type with two values: `Warning` and `Quarantine`.

- **Warning**: logged via zerolog with rule ID, message, details, and observation context. Data is saved normally.
- **Quarantine**: observation is serialized to the quarantine table. Data is NOT saved to the main table.

### Anomaly Detection Strategy Interface

Pluggable strategies for temporal anomaly detection. Start with z-score/IQR, designed so more sophisticated approaches (timeseries GPT, etc.) can be swapped in later.

```go
type AnomalyStrategy interface {
    Name() string
    Evaluate(ctx context.Context, input AnomalyInput) (AnomalyOutput, error)
}

type AnomalyInput struct {
    Key            ObservationKey    // identifies the observation (composite_figi for securities, series for economic indicators, etc.)
    Field          string
    CurrentValue   float64
    TickerHistory  []HistoricalValue
    UniverseStats  UniverseStats
}

type HistoricalValue struct {
    EventDate time.Time
    Value     float64
}

type UniverseStats struct {
    Min         float64
    Max         float64
    Mean        float64
    Median      float64
    StdDev      float64
    Percentiles map[int]float64   // e.g., {5: ..., 25: ..., 75: ..., 95: ...}
}

type AnomalyOutput struct {
    Score       float64           // 0 = normal, 1 = extreme
    Severity    Severity          // derived from score vs configured thresholds
    Explanation string
}
```

Severity for temporal checks is derived from the anomaly score. The scale of the anomaly dictates whether it is a warning or quarantine. Two dimensions are evaluated:

1. **Ticker-relative**: how unusual is this value compared to the ticker's own history?
2. **Universe-relative plausibility**: is the absolute value within the range of what real companies report?

A 100x revenue jump that still lands within the observed universe range (e.g., a fast-growing company like NVIDIA) may produce a warning. A 100x jump to a value no company has ever reported is a quarantine. This prevents false alarms on legitimately fast-growing companies while still catching data errors.

Thresholds are configurable per rule via the `params` column: `{"strategy": "zscore", "quarantine_threshold": 0.85, "warning_threshold": 0.6}`.

## Rule Categories

### Category 1: Accounting Identity Checks

Deterministic checks on `fundamental` data type. A failed identity means the data is structurally wrong.

| Rule Code | Formula (using actual DB/Go field names) |
|---|---|
| `identity.gross_profit` | revenues - cost_of_revenue = gross_profit |
| `identity.operating_income` | gross_profit - operating_expenses = operating_income |
| `identity.working_capital` | current_assets - current_liabilities = working_capital |
| `identity.balance_sheet` | total_assets = total_liabilities + equity |
| `identity.free_cash_flow` | net_cash_flow_from_operations - capital_expenditure = free_cash_flow |
| `identity.net_income` | ebt - income_tax_expense = net_income |

Note: The `identity.net_income` check may not hold exactly when discontinued operations or non-controlling interests are present. The tolerance parameter should absorb these differences. If finer-grained accounting is needed, additional identity rules can be added later.

Tolerance is configurable via `params` (e.g., `{"tolerance_pct": 0.01}`) to allow rounding differences. Default severity: `quarantine`.

### Category 2: Temporal Anomaly Detection

Applied to any numeric field on any data type. Uses the pluggable `AnomalyStrategy` interface with two-dimensional scoring (ticker-relative + universe-relative).

The initial built-in strategy is z-score/IQR:
- Compute ticker-relative z-score (how far from its own history)
- Check universe-relative plausibility (is the absolute value within observed range)
- Combine both signals into a single anomaly score using `max(ticker_score, universe_score)` -- the observation is as anomalous as its worst dimension. This means a value that is extreme on either axis triggers detection, while a value that is normal on both passes. The combination method is internal to the strategy implementation and other strategies may combine differently.
- Minimum history threshold: if a ticker has fewer than 4 historical observations, the ticker-relative dimension is skipped and only universe-relative plausibility is evaluated. Configurable via `params`: `{"min_history": 4}`.

### Category 3: Cross-Source Validation

Compares observations across subscriptions sharing the same data type.

**Discovery:** The engine queries the `subscriptions` table to find all subscriptions that provide the same data type. For example, if both a Sharadar and a Tiingo subscription provide `fundamental` data, they are cross-source candidates.

**Matching:** For each data type, observations are matched by their `ObservationKey` (e.g., same composite_figi + event_date + dimension for fundamentals). The `ValidationContext.CrossSourceObservations()` method loads matching observations from all other subscriptions.

**Comparison:** All numeric fields on the matched observations are compared. Tolerance is configurable per rule via `params`:
- `{"tolerance_pct": 0.02}` -- flag if values differ by more than 2% (relative)
- `{"tolerance_abs": 1000}` -- flag if values differ by more than 1000 (absolute)
- Both can be specified; the observation passes if it satisfies either tolerance.

**Severity:** Disagreements default to `warning` severity. The assumption is that one source may be more timely or use slightly different accounting treatments. Large disagreements (configurable via `quarantine_pct` threshold in params) escalate to `quarantine`.

**Non-security types:** For data types without composite_figi (e.g., economic indicators), matching uses the type's own `ObservationKey` (e.g., series + event_date).

## Database Schema

### validation_rules

Configuration table for rules. Rules are implemented in Go code but expose tunable knobs here.

```sql
CREATE TYPE validation_severity AS ENUM ('warning', 'quarantine');

CREATE TABLE validation_rules (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    rule_code       TEXT NOT NULL UNIQUE,
    name            TEXT NOT NULL,
    enabled         BOOLEAN NOT NULL DEFAULT true,
    run_inline      BOOLEAN NOT NULL DEFAULT true,
    severity        validation_severity NOT NULL DEFAULT 'warning',
    datatypes      datatype[] NOT NULL,
    params          JSONB NOT NULL DEFAULT '{}',
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Go `Rule` implementations register by `rule_code`. The system looks up or auto-creates the DB row on startup.

The `severity` column in `validation_rules` serves as a **default** for the rule. Rules that compute severity dynamically (e.g., temporal anomaly rules deriving severity from anomaly score) use the DB severity only when no score-based determination is available. Identity and cross-source rules use the DB severity directly. In all cases, the DB value can be overridden by the rule's code logic -- the DB provides the baseline, the code can escalate or de-escalate based on the specific observation.

### quarantine

```sql
CREATE TYPE quarantine_resolution AS ENUM ('released', 'discarded');

CREATE TABLE quarantine (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    datatype       datatype NOT NULL,
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
```

To release: deserialize the JSONB back into the appropriate Go struct and run it through the normal `SaveDB` path, then remove from quarantine. To discard: remove from quarantine.

Recommended index for efficient listing of unresolved entries:

```sql
CREATE INDEX idx_quarantine_unresolved ON quarantine (datatype, created_at)
    WHERE resolution IS NULL;
```

### universe_stats

Precomputed distribution statistics for anomaly detection, refreshed on a schedule (like other subscriptions).

```sql
CREATE TABLE universe_stats (
    id          UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    datatype   datatype NOT NULL,
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

Stats refresh runs on a configurable schedule using the existing gocron infrastructure, managed like other scheduled jobs.

## Global Configuration

Validation can be disabled entirely via `.pvdata.toml` (read by viper):

```toml
[validation]
enabled = true   # set to false to disable all validation (inline and batch)
```

When `validation.enabled = false`:
- Inline validation is skipped; `ValidateAndSave` behaves identically to `SaveObservations`
- `pvdata validate` exits immediately with a message that validation is disabled
- `pvdata quarantine` commands still work (reviewing/releasing existing quarantined data is always available)

Defaults to `true` if not specified.

## Pipeline Integration

### Inline (during ingestion)

`ValidateAndSave` replaces `SaveObservations` in the ingestion pipeline. It has the same signature: `func (lib *Library) ValidateAndSave(queue <-chan *data.Observation, wg *sync.WaitGroup)`. It consumes from the channel sequentially (same as the current `SaveObservations`), running validation rules on each observation before saving. Sequential processing is necessary because rules may perform DB lookups (ticker history, cross-source observations) and parallel execution would complicate transaction ordering and quarantine decisions.

```
Provider.Fetch() -> outChan -> ValidateAndSave()
```

For each observation:

1. Load enabled rules (where `run_inline = true`) for this observation's data type
2. Run each rule, collect `[]ValidationResult`
3. If any result has `quarantine` severity: serialize observation to the quarantine table, skip normal save
4. If results are warnings only: save normally, log warnings via zerolog
5. If no results: save normally

### Batch (`pvdata validate`)

Reads existing data from main tables, runs all enabled rules:

- Warnings are logged via zerolog
- Quarantined records are **moved** from the main table to the quarantine table (delete + insert within a transaction)
- Supports `--dry-run` flag: report what would happen without making changes

**Batch iteration strategy:** Data is processed in chunks by event_date range to avoid loading entire tables into memory. The default chunk size is one month. For partitioned tables (EOD, metrics), this aligns with the existing partition boundaries. The `--data-type` and `--rule` flags limit which tables and rules are evaluated. Progress is logged via zerolog (record count, current date range, quarantine/warning counts).

## CLI Commands

### pvdata validate

```
pvdata validate                          # run all enabled rules against all data
pvdata validate --data-type fundamental  # filter by data type
pvdata validate --rule identity.*        # filter by rule pattern
pvdata validate --dry-run                # report only, no changes
pvdata validate refresh-stats            # manually trigger universe stats recomputation
```

### pvdata quarantine

```
pvdata quarantine                        # TUI review interface (Bubble Tea)
pvdata quarantine list                   # print quarantined records to stdout
pvdata quarantine delete <id>            # remove from quarantine table, do not save
pvdata quarantine release <id>           # save to main table, remove from quarantine
```

The `quarantine` TUI presents a list of quarantined records with rule message, details, and a preview of the observation. User navigates and releases or deletes each one.

## File Structure

```
/validation/
    engine.go              # ValidateAndSave, batch runner, rule registry
    rule.go                # Rule interface, ValidationResult, Severity types
    rules_identity.go      # accounting identity check implementations
    rules_temporal.go      # temporal anomaly rule implementations
    rules_crosssource.go   # cross-source comparison implementations
    strategy.go            # AnomalyStrategy interface
    strategy_zscore.go     # initial z-score/IQR implementation
    universe_stats.go      # stats computation, scheduling, and storage
    quarantine.go          # quarantine table operations (save, release, delete)

/cmd/
    validate.go            # pvdata validate command and subcommands
    quarantine.go          # pvdata quarantine command and subcommands

/db/migrations/
    NNNNNN_validation.up.sql    # validation_severity, quarantine_resolution enums;
                                # validation_rules, quarantine, universe_stats tables
    NNNNNN_validation.down.sql
```

### Modified Files

- **`library/database.go`** -- replace `SaveObservations` call with `ValidateAndSave`
- **`data/datatype.go`** -- add `ObservationKey` interface; each data type struct implements it
- **All data type files** -- implement `ObservationKey` on every sub-type: `data/fundamental.go`, `data/eod.go`, `data/metric.go`, `data/asset.go`, `data/consensus.go`, `data/custom.go`, `data/economic_indicator.go`, `data/estimate.go`, `data/index.go` (both IndexChange and IndexSnapshot), `data/market_holiday.go`, `data/rating.go`

### Unchanged (Reused)

- Provider implementations (Tiingo, Sharadar, etc.) -- no changes needed
- Existing `SaveDB` methods on each data type -- quarantine release reuses these
- TUI infrastructure (Bubble Tea) -- quarantine TUI builds on existing patterns
- gocron scheduling -- universe stats refresh uses existing scheduler
