# Data Consistency Checks

## Overview

A layered validation system for detecting data quality problems in pv-data, with emphasis on fundamentals. Checks run both inline (during imports) and as standalone audits, with findings persisted to a database table and surfaced through the web UI and CLI.

## Architecture

### Package: `checks/`

New top-level package, parallel to `data/`, `library/`, `provider/`. Depends on `data` (for types) and `pgxpool` (for audit queries). The `library` package imports `checks` and wires it into the pipeline -- same one-way dependency as `library` importing `data`.

### Check Interface and Registry

```go
type CheckSeverity int // Info, Warning, Error, Critical

type CheckPhase int    // Inline, Audit, Both

type CheckResult struct {
    CheckName     string
    Severity      CheckSeverity
    Ticker        string
    CompositeFigi string
    Dimension     string
    EventDate     time.Time
    Field         string
    Message       string
    Expected      string
    Actual        string
    DataType      string
}

type Check interface {
    Name() string
    Description() string
    Phase() CheckPhase
    Severity() CheckSeverity
    DataTypes() []string
}

type InlineCheck interface {
    Check
    // Validates a single observation before save.
    // Returns results and whether to block the write.
    Validate(ctx context.Context, obs *data.Observation) ([]CheckResult, bool)
}

type AuditCheck interface {
    Check
    // Runs against the database.
    Audit(ctx context.Context, pool *pgxpool.Pool, table string, lastChecked *time.Time, lookback *time.Duration) ([]CheckResult, error)
}
```

Checks self-register into a global registry (same pattern providers use). Each check lives in its own file under `checks/`.

The `bool` return on `InlineCheck.Validate` controls whether the observation is blocked from being written. Only `Critical` severity checks return `true`.

## Database Schema

Migration `000007_data_quality_issues.up.sql`:

```sql
CREATE TABLE data_quality_issues (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    check_name      TEXT NOT NULL,
    severity        TEXT NOT NULL,
    data_type       TEXT NOT NULL,
    ticker          TEXT,
    composite_figi  TEXT,
    dimension       TEXT,
    event_date      DATE,
    field           TEXT,
    message         TEXT NOT NULL,
    expected        TEXT,
    actual          TEXT,
    subscription_id UUID,
    run_id          UUID,
    detected_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX dqi_ticker_idx ON data_quality_issues (composite_figi, event_date);
CREATE INDEX dqi_check_idx ON data_quality_issues (check_name, detected_at);

CREATE TABLE audit_checkpoints (
    check_name      TEXT PRIMARY KEY,
    last_run        TIMESTAMPTZ NOT NULL,
    last_event_date DATE
);
```

The issues table is append-only. No resolution tracking, no deduplication. Checks find problems, write rows.

## Inline Validation Pipeline

Today:

```
Provider -> channel -> SaveObservations() -> SaveDB()
```

Becomes:

```
Provider -> channel -> SaveObservations() -> InlineValidator -> SaveDB()
```

The `InlineValidator` holds registered inline checks:

```go
type InlineValidator struct {
    checks []InlineCheck
}

func (v *InlineValidator) Validate(ctx context.Context, obs *data.Observation) ([]CheckResult, bool) {
    var results []CheckResult
    block := false
    for _, c := range v.checks {
        r, b := c.Validate(ctx, obs)
        results = append(results, r...)
        if b {
            block = true
        }
    }
    return results, block
}
```

Integration into `SaveObservations`:

- Before each `SaveDB()` call, run the validator
- Persist any `CheckResult` entries to `data_quality_issues`
- If `block == true`, skip `SaveDB()` and log at error level
- If `block == false`, save normally

Blocked observations and `SaveDB` failures are tracked as counters on the run. A run with any blocks or failures is marked as failed in `SaveRunHistory`, which triggers the healthcheck ping failure for that subscription.

## Audit Runner and `pvdata check` Command

### Audit Runner

```go
type AuditRunner struct {
    checks []AuditCheck
    pool   *pgxpool.Pool
}

type AuditOptions struct {
    Lookback  *time.Duration
    Full      bool
    DataTypes []string
    Checks    []string
}

func (r *AuditRunner) Run(ctx context.Context, opts AuditOptions) ([]CheckResult, error)
```

Default behavior (no flags): reads `audit_checkpoints` for each check, only audits data newer than the checkpoint. Updates checkpoint after each check completes.

### Command

```
pvdata check                          # incremental (default)
pvdata check --lookback 2y            # last 2 years
pvdata check --full                   # everything
pvdata check --data-type fundamental  # only fundamentals
pvdata check --check balance-sheet-identity  # specific check
```

Outputs a grouped summary to the terminal and writes all findings to `data_quality_issues`. Exits with non-zero status if critical or error severity issues found.

## Checks

### Layer 1: Basic Sanity (Inline + Audit)

| Check | Severity | Description |
|-------|----------|-------------|
| `positive-assets` | Critical | Total assets must be > 0 |
| `positive-revenue` | Error | Revenue must be >= 0 (except financials) |
| `positive-shares` | Critical | Shares outstanding must be > 0 |
| `valid-dates` | Critical | EventDate, ReportPeriod, DateKey must not be in the future |
| `required-fields` | Error | Key fields non-zero: revenue, total assets, equity, shares outstanding |

### Layer 2: Cross-Field Consistency (Inline + Audit)

| Check | Severity | Description |
|-------|----------|-------------|
| `balance-sheet-identity` | Error | Assets = Liabilities + Equity (within rounding tolerance) |
| `gross-profit-calc` | Warning | GrossProfit = Revenue - CostOfRevenue |
| `operating-income-calc` | Warning | OperatingIncome = GrossProfit - OpEx |
| `net-income-eps` | Warning | EPS * SharesOutstanding ~= NetIncome |
| `cash-flow-sum` | Warning | OperatingCF + InvestingCF + FinancingCF ~= NetCashFlow |
| `current-ratio-calc` | Warning | CurrentRatio ~= CurrentAssets / CurrentLiabilities |

### Layer 3: Statistical Outlier Detection (Audit only)

| Check | Severity | Description |
|-------|----------|-------------|
| `revenue-change` | Warning | Revenue changed > 10x quarter-over-quarter |
| `assets-change` | Warning | Total assets changed > 5x quarter-over-quarter |
| `pe-range` | Info | PE ratio outside 0-1000 range |
| `margin-range` | Warning | Gross/operating/net margin outside -100% to +100% |

### Layer 4: Coverage and Staleness (Audit only)

| Check | Severity | Description |
|-------|----------|-------------|
| `missing-quarters` | Error | Active ticker has gap in quarterly fundamentals (ARQ dimension) |
| `stale-data` | Warning | Ticker's most recent fundamental is older than expected given filing cadence |
| `eod-without-fundamentals` | Warning | Ticker has recent EOD data but no fundamentals in last 6 months |
| `fundamentals-without-asset` | Error | Fundamental record exists for a CompositeFigi not in assets table |

### Layer 5: Cross-Type Consistency (Audit only)

| Check | Severity | Description |
|-------|----------|-------------|
| `metric-fundamental-agree` | Warning | Where metrics and fundamentals overlap (e.g., PE, PB), values should match within tolerance |
| `duplicate-observations` | Error | Same (composite_figi, dimension, event_date) appears with different values across subscription tables |

## Web UI

### API Endpoints

New Fiber route group `/api/v1/quality/`:

- `GET /issues` -- paginated, filterable by severity, data type, check name, ticker, date range. Sortable columns.
- `GET /summary` -- aggregate counts by severity and check name.

### Pages

- **Data Quality dashboard** -- counts by severity, trend over recent runs, issues table with filters
- **Per-ticker view** -- when viewing a specific asset, show its issues inline

## CLI Output

After `pvdata run` completes, one-line summary if issues were found:

```
Data quality: 2 critical, 5 errors, 12 warnings (run `pvdata check` for details)
```

After `pvdata check`, grouped summary:

```
Fundamentals:
  balance-sheet-identity  3 errors   (AAPL, MSFT, GOOG)
  missing-quarters        1 error    (TSLA)
  revenue-change          5 warnings
Metrics:
  metric-fundamental-agree  2 warnings

Total: 0 critical, 4 errors, 7 warnings, 0 info
```

## Healthcheck Integration

**Per-subscription (existing):** If any inline check blocks a write or any `SaveDB` call fails, the subscription's healthcheck pings failure.

**`pvdata check` (new, optional):** Configurable healthcheck ID in `.pvdata.toml` (e.g., `healthchecks.data_quality_apikey`). If configured, pings success/failure based on whether critical/error issues were found. If not configured, skips the ping silently.
