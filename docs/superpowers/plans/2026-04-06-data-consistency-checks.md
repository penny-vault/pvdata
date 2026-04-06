# Data Consistency Checks Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a layered data validation system that catches quality problems during imports (inline) and on-demand audits, persists findings to a database table, and surfaces them through the web UI and CLI.

**Architecture:** A `checks/` package defines check interfaces and a registry. Inline checks run in `SaveObservations` before each `SaveDB` call; audit checks run via `pvdata check`. All findings go to an append-only `data_quality_issues` table. The web UI gets a Data Quality page with summary and filterable issues table.

**Tech Stack:** Go (pgx/v5, zerolog, Cobra), PostgreSQL, Vue 3 + PrimeVue, Fiber v2, Ginkgo v2 + Gomega

---

### Task 1: Database Migration

**Files:**
- Create: `db/migrations/000007_data_quality.up.sql`
- Create: `db/migrations/000007_data_quality.down.sql`
- Modify: `db/migrate.go:30` (bump RequiredVersion)

- [ ] **Step 1: Write migration up file**

```sql
-- db/migrations/000007_data_quality.up.sql
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

- [ ] **Step 2: Write migration down file**

```sql
-- db/migrations/000007_data_quality.down.sql
DROP TABLE IF EXISTS audit_checkpoints;
DROP TABLE IF EXISTS data_quality_issues;
```

- [ ] **Step 3: Bump RequiredVersion**

In `db/migrate.go`, change:
```go
const RequiredVersion uint = 7
```

- [ ] **Step 4: Verify migration compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./db/...`
Expected: no errors

- [ ] **Step 5: Commit**

```bash
git add db/migrations/000007_data_quality.up.sql db/migrations/000007_data_quality.down.sql db/migrate.go
git commit -m "feat: add data_quality_issues and audit_checkpoints migration"
```

---

### Task 2: Check Types and Registry

**Files:**
- Create: `checks/check.go`
- Create: `checks/registry.go`
- Create: `checks/checks_suite_test.go`
- Create: `checks/registry_test.go`

- [ ] **Step 1: Write failing test for registry**

```go
// checks/checks_suite_test.go
package checks_test

import (
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestChecks(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Checks Suite")
}
```

```go
// checks/registry_test.go
package checks_test

import (
	"context"

	"github.com/penny-vault/pvdata/checks"
	"github.com/penny-vault/pvdata/data"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// stubInlineCheck implements InlineCheck for testing
type stubInlineCheck struct {
	name     string
	severity checks.CheckSeverity
}

func (s *stubInlineCheck) Name() string                  { return s.name }
func (s *stubInlineCheck) Description() string           { return "stub check" }
func (s *stubInlineCheck) Phase() checks.CheckPhase      { return checks.PhaseInline }
func (s *stubInlineCheck) Severity() checks.CheckSeverity { return s.severity }
func (s *stubInlineCheck) DataTypes() []string            { return []string{"fundamental"} }
func (s *stubInlineCheck) Validate(_ context.Context, _ *data.Observation) ([]checks.CheckResult, bool) {
	return nil, false
}

var _ = Describe("Registry", func() {
	BeforeEach(func() {
		checks.ClearRegistry()
	})

	It("registers and retrieves inline checks", func() {
		stub := &stubInlineCheck{name: "test-check", severity: checks.SeverityWarning}
		checks.RegisterInline(stub)
		Expect(checks.InlineChecks()).To(HaveLen(1))
		Expect(checks.InlineChecks()[0].Name()).To(Equal("test-check"))
	})

	It("returns empty slices when no checks registered", func() {
		Expect(checks.InlineChecks()).To(BeEmpty())
		Expect(checks.AuditChecks()).To(BeEmpty())
	})
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race ./checks/`
Expected: FAIL (package does not exist yet)

- [ ] **Step 3: Write check types and registry**

```go
// checks/check.go
package checks

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
)

type CheckSeverity int

const (
	SeverityInfo CheckSeverity = iota
	SeverityWarning
	SeverityError
	SeverityCritical
)

func (s CheckSeverity) String() string {
	switch s {
	case SeverityInfo:
		return "info"
	case SeverityWarning:
		return "warning"
	case SeverityError:
		return "error"
	case SeverityCritical:
		return "critical"
	default:
		return "unknown"
	}
}

type CheckPhase int

const (
	PhaseInline CheckPhase = iota
	PhaseAudit
	PhaseBoth
)

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
	Validate(ctx context.Context, obs *data.Observation) ([]CheckResult, bool)
}

type AuditCheck interface {
	Check
	Audit(ctx context.Context, pool *pgxpool.Pool, table string, lastChecked *time.Time, lookback *time.Duration) ([]CheckResult, error)
}
```

```go
// checks/registry.go
package checks

var (
	inlineChecks []InlineCheck
	auditChecks  []AuditCheck
)

func RegisterInline(c InlineCheck) {
	inlineChecks = append(inlineChecks, c)
}

func RegisterAudit(c AuditCheck) {
	auditChecks = append(auditChecks, c)
}

func InlineChecks() []InlineCheck {
	return inlineChecks
}

func AuditChecks() []AuditCheck {
	return auditChecks
}

func ClearRegistry() {
	inlineChecks = nil
	auditChecks = nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race ./checks/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add checks/check.go checks/registry.go checks/checks_suite_test.go checks/registry_test.go
git commit -m "feat: add check types, interfaces, and registry"
```

---

### Task 3: Inline Validator

**Files:**
- Create: `checks/validator.go`
- Create: `checks/validator_test.go`

- [ ] **Step 1: Write failing test for validator**

```go
// checks/validator_test.go
package checks_test

import (
	"context"
	"time"

	"github.com/penny-vault/pvdata/checks"
	"github.com/penny-vault/pvdata/data"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type passingCheck struct{}

func (p *passingCheck) Name() string                  { return "passing" }
func (p *passingCheck) Description() string           { return "always passes" }
func (p *passingCheck) Phase() checks.CheckPhase      { return checks.PhaseInline }
func (p *passingCheck) Severity() checks.CheckSeverity { return checks.SeverityWarning }
func (p *passingCheck) DataTypes() []string            { return []string{"fundamental"} }
func (p *passingCheck) Validate(_ context.Context, _ *data.Observation) ([]checks.CheckResult, bool) {
	return nil, false
}

type failingCheck struct {
	block bool
}

func (f *failingCheck) Name() string                  { return "failing" }
func (f *failingCheck) Description() string           { return "always fails" }
func (f *failingCheck) Phase() checks.CheckPhase      { return checks.PhaseInline }
func (f *failingCheck) Severity() checks.CheckSeverity { return checks.SeverityCritical }
func (f *failingCheck) DataTypes() []string            { return []string{"fundamental"} }
func (f *failingCheck) Validate(_ context.Context, obs *data.Observation) ([]checks.CheckResult, bool) {
	return []checks.CheckResult{
		{
			CheckName: "failing",
			Severity:  checks.SeverityCritical,
			Ticker:    obs.Fundamental.Ticker,
			DataType:  "fundamental",
			Message:   "test failure",
		},
	}, f.block
}

var _ = Describe("InlineValidator", func() {
	It("returns no results when all checks pass", func() {
		v := checks.NewInlineValidator([]checks.InlineCheck{&passingCheck{}})
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:        "AAPL",
				CompositeFigi: "BBG000B9XRY4",
				EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := v.Validate(context.Background(), obs)
		Expect(results).To(BeEmpty())
		Expect(block).To(BeFalse())
	})

	It("returns results and block=true when a blocking check fails", func() {
		v := checks.NewInlineValidator([]checks.InlineCheck{&failingCheck{block: true}})
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:        "AAPL",
				CompositeFigi: "BBG000B9XRY4",
				EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := v.Validate(context.Background(), obs)
		Expect(results).To(HaveLen(1))
		Expect(results[0].CheckName).To(Equal("failing"))
		Expect(block).To(BeTrue())
	})

	It("returns results but block=false when a non-blocking check fails", func() {
		v := checks.NewInlineValidator([]checks.InlineCheck{&failingCheck{block: false}})
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:        "AAPL",
				CompositeFigi: "BBG000B9XRY4",
				EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := v.Validate(context.Background(), obs)
		Expect(results).To(HaveLen(1))
		Expect(block).To(BeFalse())
	})

	It("aggregates results from multiple checks", func() {
		v := checks.NewInlineValidator([]checks.InlineCheck{
			&failingCheck{block: false},
			&failingCheck{block: true},
		})
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:        "AAPL",
				CompositeFigi: "BBG000B9XRY4",
				EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := v.Validate(context.Background(), obs)
		Expect(results).To(HaveLen(2))
		Expect(block).To(BeTrue())
	})
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race ./checks/`
Expected: FAIL with "NewInlineValidator" undefined

- [ ] **Step 3: Write inline validator**

```go
// checks/validator.go
package checks

import (
	"context"

	"github.com/penny-vault/pvdata/data"
)

type InlineValidator struct {
	checks []InlineCheck
}

func NewInlineValidator(checks []InlineCheck) *InlineValidator {
	return &InlineValidator{checks: checks}
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

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race ./checks/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add checks/validator.go checks/validator_test.go
git commit -m "feat: add inline validator for observation checks"
```

---

### Task 4: Issue Persistence

**Files:**
- Create: `checks/persistence.go`
- Create: `checks/persistence_test.go`

- [ ] **Step 1: Write failing test for SaveResults**

This test requires a real database. Use build tag `integration`.

```go
// checks/persistence_test.go
//go:build integration

package checks_test

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/checks"
	"github.com/spf13/viper"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SaveResults", func() {
	var pool *pgxpool.Pool

	BeforeEach(func() {
		var err error
		pool, err = pgxpool.New(context.Background(), viper.GetString("db.url"))
		Expect(err).ToNot(HaveOccurred())
	})

	AfterEach(func() {
		// clean up test data
		pool.Exec(context.Background(), "DELETE FROM data_quality_issues WHERE check_name LIKE 'test-%'")
		pool.Close()
	})

	It("saves check results to data_quality_issues", func() {
		results := []checks.CheckResult{
			{
				CheckName:     "test-positive-assets",
				Severity:      checks.SeverityCritical,
				Ticker:        "AAPL",
				CompositeFigi: "BBG000B9XRY4",
				Dimension:     "ARQ",
				EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
				Field:         "total_assets",
				Message:       "Total assets must be > 0",
				Expected:      "> 0",
				Actual:        "-1000",
				DataType:      "fundamental",
			},
		}

		subID := uuid.New()
		runID := uuid.New()

		err := checks.SaveResults(context.Background(), pool, results, subID, runID)
		Expect(err).ToNot(HaveOccurred())

		var count int
		err = pool.QueryRow(context.Background(),
			"SELECT count(*) FROM data_quality_issues WHERE check_name = 'test-positive-assets'").Scan(&count)
		Expect(err).ToNot(HaveOccurred())
		Expect(count).To(Equal(1))
	})

	It("handles empty results without error", func() {
		err := checks.SaveResults(context.Background(), pool, nil, uuid.New(), uuid.New())
		Expect(err).ToNot(HaveOccurred())
	})
})
```

- [ ] **Step 2: Write SaveResults implementation**

```go
// checks/persistence.go
package checks

import (
	"context"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

func SaveResults(ctx context.Context, pool *pgxpool.Pool, results []CheckResult, subscriptionID uuid.UUID, runID uuid.UUID) error {
	if len(results) == 0 {
		return nil
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		_ = tx.Rollback(ctx)
	}()

	for _, r := range results {
		_, err := tx.Exec(ctx,
			`INSERT INTO data_quality_issues
			(check_name, severity, data_type, ticker, composite_figi, dimension,
			 event_date, field, message, expected, actual, subscription_id, run_id)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13)`,
			r.CheckName, r.Severity.String(), r.DataType, r.Ticker, r.CompositeFigi,
			r.Dimension, r.EventDate, r.Field, r.Message, r.Expected, r.Actual,
			subscriptionID, runID,
		)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}
```

- [ ] **Step 3: Run unit tests to verify compilation**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./checks/...`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add checks/persistence.go checks/persistence_test.go
git commit -m "feat: add check result persistence to data_quality_issues"
```

---

### Task 5: Basic Sanity Checks (Layer 1)

**Files:**
- Create: `checks/positive_assets.go`
- Create: `checks/positive_revenue.go`
- Create: `checks/positive_shares.go`
- Create: `checks/valid_dates.go`
- Create: `checks/required_fields.go`
- Create: `checks/sanity_test.go`

- [ ] **Step 1: Write failing tests for sanity checks**

```go
// checks/sanity_test.go
package checks_test

import (
	"context"
	"time"

	"github.com/penny-vault/pvdata/checks"
	"github.com/penny-vault/pvdata/data"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Sanity Checks", func() {
	Describe("PositiveAssets", func() {
		var check checks.InlineCheck

		BeforeEach(func() {
			check = &checks.PositiveAssets{}
		})

		It("passes when total assets > 0", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Ticker:        "AAPL",
					CompositeFigi: "BBG000B9XRY4",
					Dimension:     "ARQ",
					EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
					TotalAssets:   352583000000,
				},
			}
			results, block := check.Validate(context.Background(), obs)
			Expect(results).To(BeEmpty())
			Expect(block).To(BeFalse())
		})

		It("fails with block when total assets <= 0", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Ticker:        "AAPL",
					CompositeFigi: "BBG000B9XRY4",
					Dimension:     "ARQ",
					EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
					TotalAssets:   -1000,
				},
			}
			results, block := check.Validate(context.Background(), obs)
			Expect(results).To(HaveLen(1))
			Expect(results[0].Severity).To(Equal(checks.SeverityCritical))
			Expect(results[0].Field).To(Equal("total_assets"))
			Expect(block).To(BeTrue())
		})

		It("skips non-fundamental observations", func() {
			obs := &data.Observation{
				EodQuote: &data.Eod{Ticker: "AAPL"},
			}
			results, block := check.Validate(context.Background(), obs)
			Expect(results).To(BeEmpty())
			Expect(block).To(BeFalse())
		})

		It("skips when total assets is zero (missing data)", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Ticker:        "AAPL",
					CompositeFigi: "BBG000B9XRY4",
					Dimension:     "ARQ",
					EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
					TotalAssets:   0,
				},
			}
			results, block := check.Validate(context.Background(), obs)
			Expect(results).To(HaveLen(1))
			Expect(block).To(BeTrue())
		})
	})

	Describe("PositiveShares", func() {
		var check checks.InlineCheck

		BeforeEach(func() {
			check = &checks.PositiveShares{}
		})

		It("passes when shares > 0", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Ticker:                       "AAPL",
					CompositeFigi:                "BBG000B9XRY4",
					Dimension:                    "ARQ",
					EventDate:                    time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
					WeightedAverageSharesDiluted: 15500000000,
				},
			}
			results, block := check.Validate(context.Background(), obs)
			Expect(results).To(BeEmpty())
			Expect(block).To(BeFalse())
		})

		It("fails when shares <= 0", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Ticker:                       "AAPL",
					CompositeFigi:                "BBG000B9XRY4",
					Dimension:                    "ARQ",
					EventDate:                    time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
					WeightedAverageSharesDiluted: -100,
				},
			}
			results, block := check.Validate(context.Background(), obs)
			Expect(results).To(HaveLen(1))
			Expect(results[0].Severity).To(Equal(checks.SeverityCritical))
			Expect(block).To(BeTrue())
		})
	})

	Describe("ValidDates", func() {
		var check checks.InlineCheck

		BeforeEach(func() {
			check = &checks.ValidDates{}
		})

		It("passes when dates are in the past", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Ticker:        "AAPL",
					CompositeFigi: "BBG000B9XRY4",
					Dimension:     "ARQ",
					EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
					ReportPeriod:  time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
					DateKey:       time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC),
				},
			}
			results, block := check.Validate(context.Background(), obs)
			Expect(results).To(BeEmpty())
			Expect(block).To(BeFalse())
		})

		It("fails when event date is in the future", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Ticker:        "AAPL",
					CompositeFigi: "BBG000B9XRY4",
					Dimension:     "ARQ",
					EventDate:     time.Date(2099, 1, 1, 0, 0, 0, 0, time.UTC),
					ReportPeriod:  time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
					DateKey:       time.Date(2025, 5, 1, 0, 0, 0, 0, time.UTC),
				},
			}
			results, block := check.Validate(context.Background(), obs)
			Expect(results).To(HaveLen(1))
			Expect(results[0].Field).To(Equal("event_date"))
			Expect(block).To(BeTrue())
		})
	})

	Describe("PositiveRevenue", func() {
		var check checks.InlineCheck

		BeforeEach(func() {
			check = &checks.PositiveRevenue{}
		})

		It("passes when revenue >= 0", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Ticker:        "AAPL",
					CompositeFigi: "BBG000B9XRY4",
					Dimension:     "ARQ",
					EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
					Revenues:      94836000000,
				},
			}
			results, block := check.Validate(context.Background(), obs)
			Expect(results).To(BeEmpty())
			Expect(block).To(BeFalse())
		})

		It("fails when revenue < 0", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Ticker:        "AAPL",
					CompositeFigi: "BBG000B9XRY4",
					Dimension:     "ARQ",
					EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
					Revenues:      -500000,
				},
			}
			results, block := check.Validate(context.Background(), obs)
			Expect(results).To(HaveLen(1))
			Expect(results[0].Severity).To(Equal(checks.SeverityError))
			Expect(block).To(BeFalse())
		})
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race ./checks/`
Expected: FAIL (types not defined)

- [ ] **Step 3: Implement sanity checks**

```go
// checks/positive_assets.go
package checks

import (
	"context"
	"fmt"

	"github.com/penny-vault/pvdata/data"
)

type PositiveAssets struct{}

func (c *PositiveAssets) Name() string             { return "positive-assets" }
func (c *PositiveAssets) Description() string      { return "Total assets must be > 0" }
func (c *PositiveAssets) Phase() CheckPhase        { return PhaseBoth }
func (c *PositiveAssets) Severity() CheckSeverity  { return SeverityCritical }
func (c *PositiveAssets) DataTypes() []string       { return []string{"fundamental"} }

func (c *PositiveAssets) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental
	if f.TotalAssets <= 0 {
		return []CheckResult{
			{
				CheckName:     c.Name(),
				Severity:      c.Severity(),
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         "total_assets",
				Message:       "Total assets must be > 0",
				Expected:      "> 0",
				Actual:        fmt.Sprintf("%d", f.TotalAssets),
				DataType:      "fundamental",
			},
		}, true
	}

	return nil, false
}
```

```go
// checks/positive_revenue.go
package checks

import (
	"context"
	"fmt"

	"github.com/penny-vault/pvdata/data"
)

type PositiveRevenue struct{}

func (c *PositiveRevenue) Name() string             { return "positive-revenue" }
func (c *PositiveRevenue) Description() string      { return "Revenue must be >= 0" }
func (c *PositiveRevenue) Phase() CheckPhase        { return PhaseBoth }
func (c *PositiveRevenue) Severity() CheckSeverity  { return SeverityError }
func (c *PositiveRevenue) DataTypes() []string       { return []string{"fundamental"} }

func (c *PositiveRevenue) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental
	if f.Revenues < 0 {
		return []CheckResult{
			{
				CheckName:     c.Name(),
				Severity:      c.Severity(),
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         "revenues",
				Message:       "Revenue must be >= 0",
				Expected:      ">= 0",
				Actual:        fmt.Sprintf("%d", f.Revenues),
				DataType:      "fundamental",
			},
		}, false
	}

	return nil, false
}
```

```go
// checks/positive_shares.go
package checks

import (
	"context"
	"fmt"

	"github.com/penny-vault/pvdata/data"
)

type PositiveShares struct{}

func (c *PositiveShares) Name() string             { return "positive-shares" }
func (c *PositiveShares) Description() string      { return "Shares outstanding must be > 0" }
func (c *PositiveShares) Phase() CheckPhase        { return PhaseBoth }
func (c *PositiveShares) Severity() CheckSeverity  { return SeverityCritical }
func (c *PositiveShares) DataTypes() []string       { return []string{"fundamental"} }

func (c *PositiveShares) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental
	if f.WeightedAverageSharesDiluted <= 0 {
		return []CheckResult{
			{
				CheckName:     c.Name(),
				Severity:      c.Severity(),
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         "weighted_average_shares_diluted",
				Message:       "Shares outstanding must be > 0",
				Expected:      "> 0",
				Actual:        fmt.Sprintf("%d", f.WeightedAverageSharesDiluted),
				DataType:      "fundamental",
			},
		}, true
	}

	return nil, false
}
```

```go
// checks/valid_dates.go
package checks

import (
	"context"
	"time"

	"github.com/penny-vault/pvdata/data"
)

type ValidDates struct{}

func (c *ValidDates) Name() string             { return "valid-dates" }
func (c *ValidDates) Description() string      { return "Dates must not be in the future" }
func (c *ValidDates) Phase() CheckPhase        { return PhaseBoth }
func (c *ValidDates) Severity() CheckSeverity  { return SeverityCritical }
func (c *ValidDates) DataTypes() []string       { return []string{"fundamental"} }

func (c *ValidDates) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental
	now := time.Now()
	var results []CheckResult

	type dateField struct {
		name  string
		value time.Time
	}

	for _, df := range []dateField{
		{"event_date", f.EventDate},
		{"report_period", f.ReportPeriod},
		{"date_key", f.DateKey},
	} {
		if !df.value.IsZero() && df.value.After(now) {
			results = append(results, CheckResult{
				CheckName:     c.Name(),
				Severity:      c.Severity(),
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         df.name,
				Message:       df.name + " is in the future",
				Expected:      "<= " + now.Format("2006-01-02"),
				Actual:        df.value.Format("2006-01-02"),
				DataType:      "fundamental",
			})
		}
	}

	return results, len(results) > 0
}
```

```go
// checks/required_fields.go
package checks

import (
	"context"

	"github.com/penny-vault/pvdata/data"
)

type RequiredFields struct{}

func (c *RequiredFields) Name() string             { return "required-fields" }
func (c *RequiredFields) Description() string      { return "Key fields must be non-zero" }
func (c *RequiredFields) Phase() CheckPhase        { return PhaseBoth }
func (c *RequiredFields) Severity() CheckSeverity  { return SeverityError }
func (c *RequiredFields) DataTypes() []string       { return []string{"fundamental"} }

func (c *RequiredFields) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental
	var results []CheckResult

	type field struct {
		name  string
		value int64
	}

	for _, fld := range []field{
		{"revenues", f.Revenues},
		{"total_assets", f.TotalAssets},
		{"equity", f.Equity},
	} {
		if fld.value == 0 {
			results = append(results, CheckResult{
				CheckName:     c.Name(),
				Severity:      c.Severity(),
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         fld.name,
				Message:       fld.name + " is zero (missing or invalid)",
				Expected:      "!= 0",
				Actual:        "0",
				DataType:      "fundamental",
			})
		}
	}

	return results, false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race ./checks/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add checks/positive_assets.go checks/positive_revenue.go checks/positive_shares.go checks/valid_dates.go checks/required_fields.go checks/sanity_test.go
git commit -m "feat: add layer 1 basic sanity checks"
```

---

### Task 6: Cross-Field Consistency Checks (Layer 2)

**Files:**
- Create: `checks/balance_sheet_identity.go`
- Create: `checks/gross_profit_calc.go`
- Create: `checks/operating_income_calc.go`
- Create: `checks/net_income_eps.go`
- Create: `checks/cash_flow_sum.go`
- Create: `checks/current_ratio_calc.go`
- Create: `checks/consistency_test.go`

- [ ] **Step 1: Write failing tests for consistency checks**

```go
// checks/consistency_test.go
package checks_test

import (
	"context"
	"math"
	"time"

	"github.com/penny-vault/pvdata/checks"
	"github.com/penny-vault/pvdata/data"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Consistency Checks", func() {
	Describe("BalanceSheetIdentity", func() {
		var check checks.InlineCheck

		BeforeEach(func() {
			check = &checks.BalanceSheetIdentity{}
		})

		It("passes when assets = liabilities + equity", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Ticker:           "AAPL",
					CompositeFigi:    "BBG000B9XRY4",
					Dimension:        "ARQ",
					EventDate:        time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
					TotalAssets:      352583000000,
					TotalLiabilities: 277797000000,
					Equity:           74786000000,
				},
			}
			results, block := check.Validate(context.Background(), obs)
			Expect(results).To(BeEmpty())
			Expect(block).To(BeFalse())
		})

		It("fails when assets != liabilities + equity", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Ticker:           "AAPL",
					CompositeFigi:    "BBG000B9XRY4",
					Dimension:        "ARQ",
					EventDate:        time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
					TotalAssets:      352583000000,
					TotalLiabilities: 277797000000,
					Equity:           50000000000,
				},
			}
			results, block := check.Validate(context.Background(), obs)
			Expect(results).To(HaveLen(1))
			Expect(results[0].Severity).To(Equal(checks.SeverityError))
			Expect(block).To(BeFalse())
		})

		It("skips when all values are zero", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Ticker:        "AAPL",
					CompositeFigi: "BBG000B9XRY4",
					Dimension:     "ARQ",
					EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
				},
			}
			results, _ := check.Validate(context.Background(), obs)
			Expect(results).To(BeEmpty())
		})
	})

	Describe("GrossProfitCalc", func() {
		var check checks.InlineCheck

		BeforeEach(func() {
			check = &checks.GrossProfitCalc{}
		})

		It("passes when gross profit = revenue - cost of revenue", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Ticker:        "AAPL",
					CompositeFigi: "BBG000B9XRY4",
					Dimension:     "ARQ",
					EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
					Revenues:      94836000000,
					CostOfRevenue: 42025000000,
					GrossProfit:   52811000000,
				},
			}
			results, _ := check.Validate(context.Background(), obs)
			Expect(results).To(BeEmpty())
		})

		It("fails when gross profit does not match", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Ticker:        "AAPL",
					CompositeFigi: "BBG000B9XRY4",
					Dimension:     "ARQ",
					EventDate:     time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
					Revenues:      94836000000,
					CostOfRevenue: 42025000000,
					GrossProfit:   99999000000,
				},
			}
			results, _ := check.Validate(context.Background(), obs)
			Expect(results).To(HaveLen(1))
			Expect(results[0].Severity).To(Equal(checks.SeverityWarning))
		})
	})

	Describe("NetIncomeEPS", func() {
		var check checks.InlineCheck

		BeforeEach(func() {
			check = &checks.NetIncomeEPS{}
		})

		It("passes when EPS * shares ~= net income", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Ticker:                       "AAPL",
					CompositeFigi:                "BBG000B9XRY4",
					Dimension:                    "ARQ",
					EventDate:                    time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
					EPSDiluted:                   1.53,
					WeightedAverageSharesDiluted: 15500000000,
					NetIncome:                    23715000000,
				},
			}
			results, _ := check.Validate(context.Background(), obs)
			Expect(results).To(BeEmpty())
		})

		It("fails when EPS * shares diverges from net income", func() {
			obs := &data.Observation{
				Fundamental: &data.Fundamental{
					Ticker:                       "AAPL",
					CompositeFigi:                "BBG000B9XRY4",
					Dimension:                    "ARQ",
					EventDate:                    time.Date(2025, 3, 31, 0, 0, 0, 0, time.UTC),
					EPSDiluted:                   100.0,
					WeightedAverageSharesDiluted: 15500000000,
					NetIncome:                    23715000000,
				},
			}
			results, _ := check.Validate(context.Background(), obs)
			Expect(results).To(HaveLen(1))
		})
	})

	// Suppress unused import warning
	var _ = math.Abs
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race ./checks/`
Expected: FAIL (types not defined)

- [ ] **Step 3: Implement consistency checks**

```go
// checks/balance_sheet_identity.go
package checks

import (
	"context"
	"fmt"
	"math"

	"github.com/penny-vault/pvdata/data"
)

type BalanceSheetIdentity struct{}

func (c *BalanceSheetIdentity) Name() string             { return "balance-sheet-identity" }
func (c *BalanceSheetIdentity) Description() string      { return "Assets = Liabilities + Equity" }
func (c *BalanceSheetIdentity) Phase() CheckPhase        { return PhaseBoth }
func (c *BalanceSheetIdentity) Severity() CheckSeverity  { return SeverityError }
func (c *BalanceSheetIdentity) DataTypes() []string       { return []string{"fundamental"} }

func (c *BalanceSheetIdentity) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental

	if f.TotalAssets == 0 && f.TotalLiabilities == 0 && f.Equity == 0 {
		return nil, false
	}

	expected := f.TotalLiabilities + f.Equity
	diff := math.Abs(float64(f.TotalAssets - expected))
	tolerance := math.Max(float64(f.TotalAssets)*0.001, 1000)

	if diff > tolerance {
		return []CheckResult{
			{
				CheckName:     c.Name(),
				Severity:      c.Severity(),
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         "total_assets,total_liabilities,equity",
				Message:       "Assets != Liabilities + Equity",
				Expected:      fmt.Sprintf("%d", expected),
				Actual:        fmt.Sprintf("%d", f.TotalAssets),
				DataType:      "fundamental",
			},
		}, false
	}

	return nil, false
}
```

```go
// checks/gross_profit_calc.go
package checks

import (
	"context"
	"fmt"
	"math"

	"github.com/penny-vault/pvdata/data"
)

type GrossProfitCalc struct{}

func (c *GrossProfitCalc) Name() string             { return "gross-profit-calc" }
func (c *GrossProfitCalc) Description() string      { return "GrossProfit = Revenue - CostOfRevenue" }
func (c *GrossProfitCalc) Phase() CheckPhase        { return PhaseBoth }
func (c *GrossProfitCalc) Severity() CheckSeverity  { return SeverityWarning }
func (c *GrossProfitCalc) DataTypes() []string       { return []string{"fundamental"} }

func (c *GrossProfitCalc) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental

	if f.Revenues == 0 && f.CostOfRevenue == 0 && f.GrossProfit == 0 {
		return nil, false
	}

	expected := f.Revenues - f.CostOfRevenue
	diff := math.Abs(float64(f.GrossProfit - expected))
	tolerance := math.Max(math.Abs(float64(f.Revenues))*0.001, 1000)

	if diff > tolerance {
		return []CheckResult{
			{
				CheckName:     c.Name(),
				Severity:      c.Severity(),
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         "gross_profit",
				Message:       "GrossProfit != Revenue - CostOfRevenue",
				Expected:      fmt.Sprintf("%d", expected),
				Actual:        fmt.Sprintf("%d", f.GrossProfit),
				DataType:      "fundamental",
			},
		}, false
	}

	return nil, false
}
```

```go
// checks/operating_income_calc.go
package checks

import (
	"context"
	"fmt"
	"math"

	"github.com/penny-vault/pvdata/data"
)

type OperatingIncomeCalc struct{}

func (c *OperatingIncomeCalc) Name() string             { return "operating-income-calc" }
func (c *OperatingIncomeCalc) Description() string      { return "OperatingIncome = GrossProfit - OpEx" }
func (c *OperatingIncomeCalc) Phase() CheckPhase        { return PhaseBoth }
func (c *OperatingIncomeCalc) Severity() CheckSeverity  { return SeverityWarning }
func (c *OperatingIncomeCalc) DataTypes() []string       { return []string{"fundamental"} }

func (c *OperatingIncomeCalc) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental

	if f.GrossProfit == 0 && f.OperatingExpenses == 0 && f.OperatingIncome == 0 {
		return nil, false
	}

	expected := f.GrossProfit - f.OperatingExpenses
	diff := math.Abs(float64(f.OperatingIncome - expected))
	tolerance := math.Max(math.Abs(float64(f.GrossProfit))*0.001, 1000)

	if diff > tolerance {
		return []CheckResult{
			{
				CheckName:     c.Name(),
				Severity:      c.Severity(),
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         "operating_income",
				Message:       "OperatingIncome != GrossProfit - OperatingExpenses",
				Expected:      fmt.Sprintf("%d", expected),
				Actual:        fmt.Sprintf("%d", f.OperatingIncome),
				DataType:      "fundamental",
			},
		}, false
	}

	return nil, false
}
```

```go
// checks/net_income_eps.go
package checks

import (
	"context"
	"fmt"
	"math"

	"github.com/penny-vault/pvdata/data"
)

type NetIncomeEPS struct{}

func (c *NetIncomeEPS) Name() string             { return "net-income-eps" }
func (c *NetIncomeEPS) Description() string      { return "EPS * Shares ~= NetIncome" }
func (c *NetIncomeEPS) Phase() CheckPhase        { return PhaseBoth }
func (c *NetIncomeEPS) Severity() CheckSeverity  { return SeverityWarning }
func (c *NetIncomeEPS) DataTypes() []string       { return []string{"fundamental"} }

func (c *NetIncomeEPS) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental

	if f.EPSDiluted == 0 || f.WeightedAverageSharesDiluted == 0 || f.NetIncome == 0 {
		return nil, false
	}

	implied := f.EPSDiluted * float64(f.WeightedAverageSharesDiluted)
	diff := math.Abs(implied - float64(f.NetIncome))
	tolerance := math.Max(math.Abs(float64(f.NetIncome))*0.05, 1000000)

	if diff > tolerance {
		return []CheckResult{
			{
				CheckName:     c.Name(),
				Severity:      c.Severity(),
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         "eps_diluted,weighted_average_shares_diluted,net_income",
				Message:       "EPS * SharesDiluted diverges from NetIncome",
				Expected:      fmt.Sprintf("%.0f", implied),
				Actual:        fmt.Sprintf("%d", f.NetIncome),
				DataType:      "fundamental",
			},
		}, false
	}

	return nil, false
}
```

```go
// checks/cash_flow_sum.go
package checks

import (
	"context"
	"fmt"
	"math"

	"github.com/penny-vault/pvdata/data"
)

type CashFlowSum struct{}

func (c *CashFlowSum) Name() string             { return "cash-flow-sum" }
func (c *CashFlowSum) Description() string      { return "OpCF + InvCF + FinCF ~= NetCashFlow" }
func (c *CashFlowSum) Phase() CheckPhase        { return PhaseBoth }
func (c *CashFlowSum) Severity() CheckSeverity  { return SeverityWarning }
func (c *CashFlowSum) DataTypes() []string       { return []string{"fundamental"} }

func (c *CashFlowSum) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental

	if f.NetCashFlowFromOperations == 0 && f.NetCashFlowFromInvesting == 0 && f.NetCashFlowFromFinancing == 0 {
		return nil, false
	}

	expected := f.NetCashFlowFromOperations + f.NetCashFlowFromInvesting + f.NetCashFlowFromFinancing
	diff := math.Abs(float64(f.NetCashFlow - expected))
	tolerance := math.Max(math.Abs(float64(expected))*0.01, 1000000)

	if diff > tolerance {
		return []CheckResult{
			{
				CheckName:     c.Name(),
				Severity:      c.Severity(),
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         "net_cash_flow",
				Message:       "OperatingCF + InvestingCF + FinancingCF != NetCashFlow",
				Expected:      fmt.Sprintf("%d", expected),
				Actual:        fmt.Sprintf("%d", f.NetCashFlow),
				DataType:      "fundamental",
			},
		}, false
	}

	return nil, false
}
```

```go
// checks/current_ratio_calc.go
package checks

import (
	"context"
	"fmt"
	"math"

	"github.com/penny-vault/pvdata/data"
)

type CurrentRatioCalc struct{}

func (c *CurrentRatioCalc) Name() string             { return "current-ratio-calc" }
func (c *CurrentRatioCalc) Description() string      { return "CurrentRatio ~= CurrentAssets / CurrentLiabilities" }
func (c *CurrentRatioCalc) Phase() CheckPhase        { return PhaseBoth }
func (c *CurrentRatioCalc) Severity() CheckSeverity  { return SeverityWarning }
func (c *CurrentRatioCalc) DataTypes() []string       { return []string{"fundamental"} }

func (c *CurrentRatioCalc) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental

	if f.CurrentAssets == 0 || f.CurrentLiabilities == 0 || f.CurrentRatio == 0 {
		return nil, false
	}

	expected := float64(f.CurrentAssets) / float64(f.CurrentLiabilities)
	diff := math.Abs(f.CurrentRatio - expected)

	if diff > 0.05 {
		return []CheckResult{
			{
				CheckName:     c.Name(),
				Severity:      c.Severity(),
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         "current_ratio",
				Message:       "CurrentRatio != CurrentAssets / CurrentLiabilities",
				Expected:      fmt.Sprintf("%.4f", expected),
				Actual:        fmt.Sprintf("%.4f", f.CurrentRatio),
				DataType:      "fundamental",
			},
		}, false
	}

	return nil, false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race ./checks/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add checks/balance_sheet_identity.go checks/gross_profit_calc.go checks/operating_income_calc.go checks/net_income_eps.go checks/cash_flow_sum.go checks/current_ratio_calc.go checks/consistency_test.go
git commit -m "feat: add layer 2 cross-field consistency checks"
```

---

### Task 7: Check Registration (init)

**Files:**
- Create: `checks/register.go`

- [ ] **Step 1: Create registration file**

```go
// checks/register.go
package checks

func init() {
	// Layer 1: Basic Sanity
	RegisterInline(&PositiveAssets{})
	RegisterInline(&PositiveRevenue{})
	RegisterInline(&PositiveShares{})
	RegisterInline(&ValidDates{})
	RegisterInline(&RequiredFields{})

	// Layer 2: Cross-Field Consistency
	RegisterInline(&BalanceSheetIdentity{})
	RegisterInline(&GrossProfitCalc{})
	RegisterInline(&OperatingIncomeCalc{})
	RegisterInline(&NetIncomeEPS{})
	RegisterInline(&CashFlowSum{})
	RegisterInline(&CurrentRatioCalc{})
}
```

- [ ] **Step 2: Verify compilation**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./checks/...`
Expected: no errors

- [ ] **Step 3: Commit**

```bash
git add checks/register.go
git commit -m "feat: auto-register all inline checks via init()"
```

---

### Task 8: Wire Inline Validator into SaveObservations

**Files:**
- Modify: `library/database.go:160-274`

- [ ] **Step 1: Modify SaveObservations to accept and use InlineValidator**

In `library/database.go`, change the `SaveObservations` signature and add validation logic. The key changes:

1. Add `checks` import
2. Accept `*checks.InlineValidator` parameter
3. Before each `SaveDB` call, run the validator
4. If blocked, skip SaveDB and increment a blocked counter
5. Save check results to the issues table
6. Return a summary of blocked/failed counts

Change the function signature from:

```go
func (myLibrary *Library) SaveObservations(queue <-chan *data.Observation, wg *sync.WaitGroup) {
```

To:

```go
func (myLibrary *Library) SaveObservations(queue <-chan *data.Observation, wg *sync.WaitGroup, validator *checks.InlineValidator) {
```

Add imports for `"github.com/penny-vault/pvdata/checks"` and `"github.com/google/uuid"`.

Inside the `for elem := range queue` loop, before the existing SaveDB dispatch block, add:

```go
		if validator != nil {
			results, block := validator.Validate(ctx, elem)
			if len(results) > 0 {
				if err := checks.SaveResults(ctx, myLibrary.Pool, results, elem.SubscriptionID, uuid.Nil); err != nil {
					log.Error().Err(err).Msg("failed to save check results")
				}

				for _, r := range results {
					log.Warn().
						Str("check", r.CheckName).
						Str("severity", r.Severity.String()).
						Str("ticker", r.Ticker).
						Str("field", r.Field).
						Msg(r.Message)
				}
			}

			if block {
				log.Error().
					Str("ticker", elem.SubscriptionName).
					Msg("observation blocked by inline check")
				continue
			}
		}
```

- [ ] **Step 2: Update all callers of SaveObservations**

Search for all callers: they pass `nil` for the validator until we wire up the full integration. Callers are in:
- `tui/run_manager.go` or wherever the TUI calls SaveObservations
- `web/handlers_run_now.go`
- `cmd/serve.go`
- `cmd/import.go`

For each caller, update the call from:
```go
myLibrary.SaveObservations(outChan, &wg)
```
To:
```go
myLibrary.SaveObservations(outChan, &wg, checks.NewInlineValidator(checks.InlineChecks()))
```

Add import `"github.com/penny-vault/pvdata/checks"` to each file.

- [ ] **Step 3: Run lint**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && golangci-lint run --fix ./...`
Expected: no errors (or auto-fixed)

- [ ] **Step 4: Run tests**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race ./...`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add library/database.go tui/ web/ cmd/ checks/
git commit -m "feat: wire inline validator into SaveObservations pipeline"
```

---

### Task 9: Audit Runner

**Files:**
- Create: `checks/audit_runner.go`
- Create: `checks/audit_runner_test.go`

- [ ] **Step 1: Write failing test for audit runner**

```go
// checks/audit_runner_test.go
package checks_test

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/checks"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

type stubAuditCheck struct {
	results []checks.CheckResult
}

func (s *stubAuditCheck) Name() string             { return "stub-audit" }
func (s *stubAuditCheck) Description() string      { return "stub" }
func (s *stubAuditCheck) Phase() checks.CheckPhase { return checks.PhaseAudit }
func (s *stubAuditCheck) Severity() checks.CheckSeverity { return checks.SeverityWarning }
func (s *stubAuditCheck) DataTypes() []string       { return []string{"fundamental"} }
func (s *stubAuditCheck) Audit(_ context.Context, _ *pgxpool.Pool, _ string, _ *time.Time, _ *time.Duration) ([]checks.CheckResult, error) {
	return s.results, nil
}

var _ = Describe("AuditRunner", func() {
	It("collects results from all audit checks", func() {
		stubResults := []checks.CheckResult{
			{CheckName: "stub-audit", Message: "test issue"},
		}
		runner := checks.NewAuditRunner([]checks.AuditCheck{
			&stubAuditCheck{results: stubResults},
		}, nil)
		opts := checks.AuditOptions{}
		results, err := runner.Run(context.Background(), opts, "test_table")
		Expect(err).ToNot(HaveOccurred())
		Expect(results).To(HaveLen(1))
		Expect(results[0].CheckName).To(Equal("stub-audit"))
	})

	It("returns empty results when no checks registered", func() {
		runner := checks.NewAuditRunner(nil, nil)
		results, err := runner.Run(context.Background(), checks.AuditOptions{}, "test_table")
		Expect(err).ToNot(HaveOccurred())
		Expect(results).To(BeEmpty())
	})
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race ./checks/`
Expected: FAIL (NewAuditRunner undefined)

- [ ] **Step 3: Implement audit runner**

```go
// checks/audit_runner.go
package checks

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type AuditOptions struct {
	Lookback  *time.Duration
	Full      bool
	DataTypes []string
	Checks    []string
}

type AuditRunner struct {
	checks []AuditCheck
	pool   *pgxpool.Pool
}

func NewAuditRunner(checks []AuditCheck, pool *pgxpool.Pool) *AuditRunner {
	return &AuditRunner{checks: checks, pool: pool}
}

func (r *AuditRunner) Run(ctx context.Context, opts AuditOptions, table string) ([]CheckResult, error) {
	var allResults []CheckResult

	for _, c := range r.checks {
		if len(opts.DataTypes) > 0 && !matchesDataType(c, opts.DataTypes) {
			continue
		}

		if len(opts.Checks) > 0 && !matchesCheckName(c, opts.Checks) {
			continue
		}

		var lastChecked *time.Time
		if !opts.Full {
			lastChecked = r.loadCheckpoint(ctx, c.Name())
		}

		results, err := c.Audit(ctx, r.pool, table, lastChecked, opts.Lookback)
		if err != nil {
			return allResults, err
		}

		allResults = append(allResults, results...)

		if r.pool != nil {
			r.saveCheckpoint(ctx, c.Name())
		}
	}

	return allResults, nil
}

func (r *AuditRunner) loadCheckpoint(ctx context.Context, checkName string) *time.Time {
	if r.pool == nil {
		return nil
	}

	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return nil
	}
	defer conn.Release()

	var lastRun time.Time

	err = conn.QueryRow(ctx,
		"SELECT last_run FROM audit_checkpoints WHERE check_name = $1", checkName).Scan(&lastRun)
	if err != nil {
		return nil
	}

	return &lastRun
}

func (r *AuditRunner) saveCheckpoint(ctx context.Context, checkName string) {
	conn, err := r.pool.Acquire(ctx)
	if err != nil {
		return
	}
	defer conn.Release()

	_, _ = conn.Exec(ctx,
		`INSERT INTO audit_checkpoints (check_name, last_run)
		 VALUES ($1, now())
		 ON CONFLICT (check_name) DO UPDATE SET last_run = now()`, checkName)
}

func matchesDataType(c Check, dataTypes []string) bool {
	for _, dt := range c.DataTypes() {
		for _, want := range dataTypes {
			if dt == want {
				return true
			}
		}
	}

	return false
}

func matchesCheckName(c Check, names []string) bool {
	for _, name := range names {
		if c.Name() == name {
			return true
		}
	}

	return false
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race ./checks/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add checks/audit_runner.go checks/audit_runner_test.go
git commit -m "feat: add audit runner with checkpoint support"
```

---

### Task 10: `pvdata check` Command

**Files:**
- Create: `cmd/check.go`

- [ ] **Step 1: Create the check command**

```go
// cmd/check.go
package cmd

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/penny-vault/pvdata/checks"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "Run data quality checks against the database",
	Long: `The check sub-command runs audit checks against the database to detect
data quality issues. By default it runs incrementally, only checking data
newer than the last audit. Use --lookback or --full to override.`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not connect to library")
		}
		defer myLibrary.Close()

		opts := checks.AuditOptions{}

		if lookbackStr := viper.GetString("check.lookback"); lookbackStr != "" {
			lookback, err := parseLookback(lookbackStr)
			if err != nil {
				log.Fatal().Err(err).Str("lookback", lookbackStr).Msg("invalid lookback value")
			}

			opts.Lookback = &lookback
		}

		if full, _ := cmd.Flags().GetBool("full"); full {
			opts.Full = true
		}

		if dt := viper.GetString("check.data-type"); dt != "" {
			opts.DataTypes = []string{dt}
		}

		if cn := viper.GetString("check.check"); cn != "" {
			opts.Checks = []string{cn}
		}

		// Get subscription tables for fundamentals
		subs, err := myLibrary.Subscriptions(ctx)
		if err != nil {
			log.Fatal().Err(err).Msg("could not load subscriptions")
		}

		runner := checks.NewAuditRunner(checks.AuditChecks(), myLibrary.Pool)

		var allResults []checks.CheckResult

		for _, sub := range subs {
			for _, dt := range sub.DataTypes {
				tableName := sub.DataTablesMap[dt]
				if tableName == "" {
					continue
				}

				results, err := runner.Run(ctx, opts, tableName)
				if err != nil {
					log.Error().Err(err).Str("table", tableName).Msg("audit check failed")
					continue
				}

				if len(results) > 0 {
					if err := checks.SaveResults(ctx, myLibrary.Pool, results, sub.ID, uuid.Nil); err != nil {
						log.Error().Err(err).Msg("failed to save audit results")
					}

					allResults = append(allResults, results...)
				}
			}
		}

		printCheckSummary(allResults)

		// ping healthcheck if configured
		if hcID := viper.GetString("healthchecks.data_quality_id"); hcID != "" {
			hasCriticalOrError := false

			for _, r := range allResults {
				if r.Severity == checks.SeverityCritical || r.Severity == checks.SeverityError {
					hasCriticalOrError = true
					break
				}
			}

			if hasCriticalOrError {
				log.Warn().Msg("data quality check found critical/error issues; healthcheck ping failed")
			}
		}

		// exit non-zero if critical or error issues found
		for _, r := range allResults {
			if r.Severity == checks.SeverityCritical || r.Severity == checks.SeverityError {
				os.Exit(1)
			}
		}
	},
}

func printCheckSummary(results []checks.CheckResult) {
	if len(results) == 0 {
		fmt.Println("No data quality issues found.")
		return
	}

	// group by data type, then by check name
	type checkSummary struct {
		count    int
		severity checks.CheckSeverity
		tickers  map[string]bool
	}

	byType := make(map[string]map[string]*checkSummary)

	for _, r := range results {
		if byType[r.DataType] == nil {
			byType[r.DataType] = make(map[string]*checkSummary)
		}

		if byType[r.DataType][r.CheckName] == nil {
			byType[r.DataType][r.CheckName] = &checkSummary{tickers: make(map[string]bool)}
		}

		s := byType[r.DataType][r.CheckName]
		s.count++
		s.severity = r.Severity

		if r.Ticker != "" {
			s.tickers[r.Ticker] = true
		}
	}

	// sort data types
	dataTypes := make([]string, 0, len(byType))
	for dt := range byType {
		dataTypes = append(dataTypes, dt)
	}

	sort.Strings(dataTypes)

	for _, dt := range dataTypes {
		fmt.Printf("%s:\n", strings.Title(dt))

		checkNames := make([]string, 0, len(byType[dt]))
		for cn := range byType[dt] {
			checkNames = append(checkNames, cn)
		}

		sort.Strings(checkNames)

		for _, cn := range checkNames {
			s := byType[dt][cn]
			tickerList := ""

			if len(s.tickers) > 0 && len(s.tickers) <= 5 {
				tickers := make([]string, 0, len(s.tickers))
				for t := range s.tickers {
					tickers = append(tickers, t)
				}

				sort.Strings(tickers)

				tickerList = fmt.Sprintf("  (%s)", strings.Join(tickers, ", "))
			}

			label := s.severity.String() + "s"
			if s.count == 1 {
				label = s.severity.String()
			}

			fmt.Printf("  %-30s %d %s%s\n", cn, s.count, label, tickerList)
		}
	}

	// totals
	counts := map[checks.CheckSeverity]int{}
	for _, r := range results {
		counts[r.Severity]++
	}

	fmt.Printf("\nTotal: %d critical, %d errors, %d warnings, %d info\n",
		counts[checks.SeverityCritical],
		counts[checks.SeverityError],
		counts[checks.SeverityWarning],
		counts[checks.SeverityInfo],
	)
}

func init() {
	checkCmd.Flags().StringP("lookback", "l", "", "Override lookback period (e.g. 6m, 2y)")
	checkCmd.Flags().Bool("full", false, "Run full audit ignoring checkpoints")
	checkCmd.Flags().String("data-type", "", "Filter to specific data type (e.g. fundamental)")
	checkCmd.Flags().String("check", "", "Run a specific check by name")

	_ = viper.BindPFlag("check.lookback", checkCmd.Flags().Lookup("lookback"))
	_ = viper.BindPFlag("check.data-type", checkCmd.Flags().Lookup("data-type"))
	_ = viper.BindPFlag("check.check", checkCmd.Flags().Lookup("check"))

	rootCmd.AddCommand(checkCmd)
}
```

- [ ] **Step 2: Verify compilation**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`
Expected: no errors

- [ ] **Step 3: Run lint**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && golangci-lint run --fix ./...`
Expected: no errors (or auto-fixed). Note: `strings.Title` is deprecated -- replace with `cases.Title(language.English).String(dt)` from `golang.org/x/text/cases` and `golang.org/x/text/language` if the linter flags it.

- [ ] **Step 4: Commit**

```bash
git add cmd/check.go
git commit -m "feat: add pvdata check command for data quality audits"
```

---

### Task 11: Statistical Outlier Detection Checks (Layer 3 - Audit Only)

**Files:**
- Create: `checks/revenue_change.go`
- Create: `checks/assets_change.go`
- Create: `checks/pe_range.go`
- Create: `checks/margin_range.go`
- Create: `checks/outlier_test.go`

These are audit-only checks that query the database for historical comparisons.

- [ ] **Step 1: Write revenue_change audit check**

```go
// checks/revenue_change.go
package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type RevenueChange struct{}

func (c *RevenueChange) Name() string             { return "revenue-change" }
func (c *RevenueChange) Description() string      { return "Revenue changed > 10x quarter-over-quarter" }
func (c *RevenueChange) Phase() CheckPhase        { return PhaseAudit }
func (c *RevenueChange) Severity() CheckSeverity  { return SeverityWarning }
func (c *RevenueChange) DataTypes() []string       { return []string{"fundamental"} }

func (c *RevenueChange) Audit(ctx context.Context, pool *pgxpool.Pool, table string, lastChecked *time.Time, lookback *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	query := fmt.Sprintf(`
		WITH ranked AS (
			SELECT ticker, composite_figi, dimension, event_date, revenues,
				LAG(revenues) OVER (PARTITION BY composite_figi, dimension ORDER BY event_date) AS prev_revenues
			FROM %s
			WHERE dimension = 'ARQ' AND revenues != 0
		)
		SELECT ticker, composite_figi, dimension, event_date, revenues, prev_revenues
		FROM ranked
		WHERE prev_revenues != 0
		  AND ABS(revenues::float / prev_revenues::float) > 10`, table)

	args := []interface{}{}
	if lastChecked != nil {
		query = fmt.Sprintf(`
			WITH ranked AS (
				SELECT ticker, composite_figi, dimension, event_date, revenues,
					LAG(revenues) OVER (PARTITION BY composite_figi, dimension ORDER BY event_date) AS prev_revenues
				FROM %s
				WHERE dimension = 'ARQ' AND revenues != 0
			)
			SELECT ticker, composite_figi, dimension, event_date, revenues, prev_revenues
			FROM ranked
			WHERE prev_revenues != 0
			  AND ABS(revenues::float / prev_revenues::float) > 10
			  AND event_date > $1`, table)
		args = append(args, *lastChecked)
	}

	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []CheckResult

	for rows.Next() {
		var ticker, figi, dimension string
		var eventDate time.Time
		var revenues, prevRevenues int64

		if err := rows.Scan(&ticker, &figi, &dimension, &eventDate, &revenues, &prevRevenues); err != nil {
			return results, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      c.Severity(),
			Ticker:        ticker,
			CompositeFigi: figi,
			Dimension:     dimension,
			EventDate:     eventDate,
			Field:         "revenues",
			Message:       "Revenue changed > 10x quarter-over-quarter",
			Expected:      fmt.Sprintf("~%d", prevRevenues),
			Actual:        fmt.Sprintf("%d", revenues),
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
```

- [ ] **Step 2: Write assets_change audit check**

```go
// checks/assets_change.go
package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type AssetsChange struct{}

func (c *AssetsChange) Name() string             { return "assets-change" }
func (c *AssetsChange) Description() string      { return "Total assets changed > 5x quarter-over-quarter" }
func (c *AssetsChange) Phase() CheckPhase        { return PhaseAudit }
func (c *AssetsChange) Severity() CheckSeverity  { return SeverityWarning }
func (c *AssetsChange) DataTypes() []string       { return []string{"fundamental"} }

func (c *AssetsChange) Audit(ctx context.Context, pool *pgxpool.Pool, table string, lastChecked *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	dateFilter := ""
	args := []interface{}{}

	if lastChecked != nil {
		dateFilter = "AND event_date > $1"
		args = append(args, *lastChecked)
	}

	query := fmt.Sprintf(`
		WITH ranked AS (
			SELECT ticker, composite_figi, dimension, event_date, total_assets,
				LAG(total_assets) OVER (PARTITION BY composite_figi, dimension ORDER BY event_date) AS prev_assets
			FROM %s
			WHERE dimension = 'ARQ' AND total_assets != 0
		)
		SELECT ticker, composite_figi, dimension, event_date, total_assets, prev_assets
		FROM ranked
		WHERE prev_assets != 0
		  AND ABS(total_assets::float / prev_assets::float) > 5
		  %s`, table, dateFilter)

	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []CheckResult

	for rows.Next() {
		var ticker, figi, dimension string
		var eventDate time.Time
		var assets, prevAssets int64

		if err := rows.Scan(&ticker, &figi, &dimension, &eventDate, &assets, &prevAssets); err != nil {
			return results, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      c.Severity(),
			Ticker:        ticker,
			CompositeFigi: figi,
			Dimension:     dimension,
			EventDate:     eventDate,
			Field:         "total_assets",
			Message:       "Total assets changed > 5x quarter-over-quarter",
			Expected:      fmt.Sprintf("~%d", prevAssets),
			Actual:        fmt.Sprintf("%d", assets),
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
```

- [ ] **Step 3: Write pe_range and margin_range checks**

```go
// checks/pe_range.go
package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type PERange struct{}

func (c *PERange) Name() string             { return "pe-range" }
func (c *PERange) Description() string      { return "PE ratio outside 0-1000 range" }
func (c *PERange) Phase() CheckPhase        { return PhaseAudit }
func (c *PERange) Severity() CheckSeverity  { return SeverityInfo }
func (c *PERange) DataTypes() []string       { return []string{"fundamental"} }

func (c *PERange) Audit(ctx context.Context, pool *pgxpool.Pool, table string, lastChecked *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	dateFilter := ""
	args := []interface{}{}

	if lastChecked != nil {
		dateFilter = "AND event_date > $1"
		args = append(args, *lastChecked)
	}

	query := fmt.Sprintf(`
		SELECT ticker, composite_figi, dimension, event_date, pe
		FROM %s
		WHERE pe != 0 AND (pe < 0 OR pe > 1000)
		%s`, table, dateFilter)

	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []CheckResult

	for rows.Next() {
		var ticker, figi, dimension string
		var eventDate time.Time
		var pe float64

		if err := rows.Scan(&ticker, &figi, &dimension, &eventDate, &pe); err != nil {
			return results, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      c.Severity(),
			Ticker:        ticker,
			CompositeFigi: figi,
			Dimension:     dimension,
			EventDate:     eventDate,
			Field:         "pe",
			Message:       "PE ratio outside 0-1000 range",
			Expected:      "0-1000",
			Actual:        fmt.Sprintf("%.2f", pe),
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
```

```go
// checks/margin_range.go
package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type MarginRange struct{}

func (c *MarginRange) Name() string             { return "margin-range" }
func (c *MarginRange) Description() string      { return "Margins outside -100% to +100%" }
func (c *MarginRange) Phase() CheckPhase        { return PhaseAudit }
func (c *MarginRange) Severity() CheckSeverity  { return SeverityWarning }
func (c *MarginRange) DataTypes() []string       { return []string{"fundamental"} }

func (c *MarginRange) Audit(ctx context.Context, pool *pgxpool.Pool, table string, lastChecked *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	dateFilter := ""
	args := []interface{}{}

	if lastChecked != nil {
		dateFilter = "AND event_date > $1"
		args = append(args, *lastChecked)
	}

	query := fmt.Sprintf(`
		SELECT ticker, composite_figi, dimension, event_date,
			gross_margin, ebitda_margin, profit_margin
		FROM %s
		WHERE (gross_margin < -1 OR gross_margin > 1
			OR ebitda_margin < -1 OR ebitda_margin > 1
			OR profit_margin < -1 OR profit_margin > 1)
		  AND (gross_margin != 0 OR ebitda_margin != 0 OR profit_margin != 0)
		%s`, table, dateFilter)

	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []CheckResult

	for rows.Next() {
		var ticker, figi, dimension string
		var eventDate time.Time
		var grossMargin, ebitdaMargin, profitMargin float64

		if err := rows.Scan(&ticker, &figi, &dimension, &eventDate, &grossMargin, &ebitdaMargin, &profitMargin); err != nil {
			return results, err
		}

		for _, mf := range []struct {
			name  string
			value float64
		}{
			{"gross_margin", grossMargin},
			{"ebitda_margin", ebitdaMargin},
			{"profit_margin", profitMargin},
		} {
			if mf.value != 0 && (mf.value < -1 || mf.value > 1) {
				results = append(results, CheckResult{
					CheckName:     c.Name(),
					Severity:      c.Severity(),
					Ticker:        ticker,
					CompositeFigi: figi,
					Dimension:     dimension,
					EventDate:     eventDate,
					Field:         mf.name,
					Message:       fmt.Sprintf("%s outside -100%% to +100%%", mf.name),
					Expected:      "-1.0 to 1.0",
					Actual:        fmt.Sprintf("%.4f", mf.value),
					DataType:      "fundamental",
				})
			}
		}
	}

	return results, rows.Err()
}
```

- [ ] **Step 4: Register audit checks in register.go**

Add to `checks/register.go` init():

```go
	// Layer 3: Statistical Outlier Detection (Audit only)
	RegisterAudit(&RevenueChange{})
	RegisterAudit(&AssetsChange{})
	RegisterAudit(&PERange{})
	RegisterAudit(&MarginRange{})
```

- [ ] **Step 5: Verify compilation and run lint**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./... && golangci-lint run --fix ./...`
Expected: no errors

- [ ] **Step 6: Commit**

```bash
git add checks/revenue_change.go checks/assets_change.go checks/pe_range.go checks/margin_range.go checks/register.go
git commit -m "feat: add layer 3 statistical outlier audit checks"
```

---

### Task 12: Coverage and Staleness Checks (Layer 4 - Audit Only)

**Files:**
- Create: `checks/missing_quarters.go`
- Create: `checks/stale_data.go`
- Create: `checks/eod_without_fundamentals.go`
- Create: `checks/fundamentals_without_asset.go`

- [ ] **Step 1: Write missing_quarters check**

```go
// checks/missing_quarters.go
package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type MissingQuarters struct{}

func (c *MissingQuarters) Name() string             { return "missing-quarters" }
func (c *MissingQuarters) Description() string      { return "Active ticker has gap in quarterly fundamentals" }
func (c *MissingQuarters) Phase() CheckPhase        { return PhaseAudit }
func (c *MissingQuarters) Severity() CheckSeverity  { return SeverityError }
func (c *MissingQuarters) DataTypes() []string       { return []string{"fundamental"} }

func (c *MissingQuarters) Audit(ctx context.Context, pool *pgxpool.Pool, table string, lastChecked *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	dateFilter := ""
	args := []interface{}{}

	if lastChecked != nil {
		dateFilter = "AND curr.event_date > $1"
		args = append(args, *lastChecked)
	}

	// find gaps > 120 days between consecutive ARQ records for same ticker
	query := fmt.Sprintf(`
		WITH ordered AS (
			SELECT ticker, composite_figi, dimension, event_date,
				LEAD(event_date) OVER (PARTITION BY composite_figi ORDER BY event_date) AS next_date
			FROM %s
			WHERE dimension = 'ARQ'
		)
		SELECT ticker, composite_figi, 'ARQ' as dimension, event_date, next_date
		FROM ordered curr
		WHERE next_date IS NOT NULL
		  AND next_date - event_date > 120
		  %s`, table, dateFilter)

	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []CheckResult

	for rows.Next() {
		var ticker, figi, dimension string
		var eventDate, nextDate time.Time

		if err := rows.Scan(&ticker, &figi, &dimension, &eventDate, &nextDate); err != nil {
			return results, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      c.Severity(),
			Ticker:        ticker,
			CompositeFigi: figi,
			Dimension:     dimension,
			EventDate:     eventDate,
			Field:         "event_date",
			Message:       fmt.Sprintf("Gap of %d days between quarterly filings", int(nextDate.Sub(eventDate).Hours()/24)),
			Expected:      "~90 days",
			Actual:        fmt.Sprintf("%d days", int(nextDate.Sub(eventDate).Hours()/24)),
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
```

- [ ] **Step 2: Write stale_data check**

```go
// checks/stale_data.go
package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type StaleData struct{}

func (c *StaleData) Name() string             { return "stale-data" }
func (c *StaleData) Description() string      { return "Most recent fundamental older than expected" }
func (c *StaleData) Phase() CheckPhase        { return PhaseAudit }
func (c *StaleData) Severity() CheckSeverity  { return SeverityWarning }
func (c *StaleData) DataTypes() []string       { return []string{"fundamental"} }

func (c *StaleData) Audit(ctx context.Context, pool *pgxpool.Pool, table string, _ *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	// find tickers whose most recent ARQ is more than 6 months old
	sixMonthsAgo := time.Now().AddDate(0, -6, 0)

	query := fmt.Sprintf(`
		SELECT ticker, composite_figi, MAX(event_date) as latest
		FROM %s
		WHERE dimension = 'ARQ'
		GROUP BY ticker, composite_figi
		HAVING MAX(event_date) < $1`, table)

	rows, err := conn.Query(ctx, query, sixMonthsAgo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []CheckResult

	for rows.Next() {
		var ticker, figi string
		var latest time.Time

		if err := rows.Scan(&ticker, &figi, &latest); err != nil {
			return results, err
		}

		daysSince := int(time.Since(latest).Hours() / 24)

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      c.Severity(),
			Ticker:        ticker,
			CompositeFigi: figi,
			Dimension:     "ARQ",
			EventDate:     latest,
			Field:         "event_date",
			Message:       fmt.Sprintf("Most recent quarterly filing is %d days old", daysSince),
			Expected:      "< 180 days",
			Actual:        fmt.Sprintf("%d days", daysSince),
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
```

- [ ] **Step 3: Write eod_without_fundamentals and fundamentals_without_asset checks**

These checks need to query across published views (cross-table). They receive the table name but need access to the view names.

```go
// checks/eod_without_fundamentals.go
package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type EodWithoutFundamentals struct{}

func (c *EodWithoutFundamentals) Name() string             { return "eod-without-fundamentals" }
func (c *EodWithoutFundamentals) Description() string      { return "Ticker has EOD data but no recent fundamentals" }
func (c *EodWithoutFundamentals) Phase() CheckPhase        { return PhaseAudit }
func (c *EodWithoutFundamentals) Severity() CheckSeverity  { return SeverityWarning }
func (c *EodWithoutFundamentals) DataTypes() []string       { return []string{"fundamental"} }

func (c *EodWithoutFundamentals) Audit(ctx context.Context, pool *pgxpool.Pool, table string, _ *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	sixMonthsAgo := time.Now().AddDate(0, -6, 0)

	// Check if the eod view exists; skip if not
	var eodExists bool

	err = conn.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM information_schema.views WHERE table_name = 'eod')").Scan(&eodExists)
	if err != nil || !eodExists {
		return nil, err
	}

	var fundExists bool

	err = conn.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM information_schema.views WHERE table_name = 'fundamentals')").Scan(&fundExists)
	if err != nil || !fundExists {
		return nil, err
	}

	query := `
		SELECT DISTINCT e.ticker, e.composite_figi
		FROM eod e
		LEFT JOIN fundamentals f ON e.composite_figi = f.composite_figi AND f.dimension = 'ARQ'
			AND f.event_date > $1
		WHERE e.event_date > $1
		  AND f.composite_figi IS NULL
		  AND e.composite_figi != ''`

	rows, err := conn.Query(ctx, query, sixMonthsAgo)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []CheckResult

	for rows.Next() {
		var ticker, figi string

		if err := rows.Scan(&ticker, &figi); err != nil {
			return results, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      c.Severity(),
			Ticker:        ticker,
			CompositeFigi: figi,
			Field:         "composite_figi",
			Message:       "Has recent EOD data but no fundamentals in last 6 months",
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
```

```go
// checks/fundamentals_without_asset.go
package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type FundamentalsWithoutAsset struct{}

func (c *FundamentalsWithoutAsset) Name() string             { return "fundamentals-without-asset" }
func (c *FundamentalsWithoutAsset) Description() string      { return "Fundamental for unknown CompositeFigi" }
func (c *FundamentalsWithoutAsset) Phase() CheckPhase        { return PhaseAudit }
func (c *FundamentalsWithoutAsset) Severity() CheckSeverity  { return SeverityError }
func (c *FundamentalsWithoutAsset) DataTypes() []string       { return []string{"fundamental"} }

func (c *FundamentalsWithoutAsset) Audit(ctx context.Context, pool *pgxpool.Pool, table string, lastChecked *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	// Check if assets view exists
	var assetsExists bool

	err = conn.QueryRow(ctx,
		"SELECT EXISTS(SELECT 1 FROM information_schema.views WHERE table_name = 'assets')").Scan(&assetsExists)
	if err != nil || !assetsExists {
		return nil, err
	}

	dateFilter := ""
	args := []interface{}{}

	if lastChecked != nil {
		dateFilter = "AND f.event_date > $1"
		args = append(args, *lastChecked)
	}

	query := fmt.Sprintf(`
		SELECT DISTINCT f.ticker, f.composite_figi
		FROM %s f
		LEFT JOIN assets a ON f.composite_figi = a.composite_figi
		WHERE a.composite_figi IS NULL
		  AND f.composite_figi != ''
		  %s`, table, dateFilter)

	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []CheckResult

	for rows.Next() {
		var ticker, figi string

		if err := rows.Scan(&ticker, &figi); err != nil {
			return results, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      c.Severity(),
			Ticker:        ticker,
			CompositeFigi: figi,
			Field:         "composite_figi",
			Message:       "Fundamental record exists for CompositeFigi not in assets",
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
```

- [ ] **Step 4: Register in register.go**

Add to `checks/register.go` init():

```go
	// Layer 4: Coverage and Staleness (Audit only)
	RegisterAudit(&MissingQuarters{})
	RegisterAudit(&StaleData{})
	RegisterAudit(&EodWithoutFundamentals{})
	RegisterAudit(&FundamentalsWithoutAsset{})
```

- [ ] **Step 5: Verify compilation and lint**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./... && golangci-lint run --fix ./...`
Expected: no errors

- [ ] **Step 6: Commit**

```bash
git add checks/missing_quarters.go checks/stale_data.go checks/eod_without_fundamentals.go checks/fundamentals_without_asset.go checks/register.go
git commit -m "feat: add layer 4 coverage and staleness audit checks"
```

---

### Task 13: Cross-Type Consistency Checks (Layer 5 - Audit Only)

**Files:**
- Create: `checks/metric_fundamental_agree.go`
- Create: `checks/duplicate_observations.go`

- [ ] **Step 1: Write metric_fundamental_agree check**

```go
// checks/metric_fundamental_agree.go
package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type MetricFundamentalAgree struct{}

func (c *MetricFundamentalAgree) Name() string             { return "metric-fundamental-agree" }
func (c *MetricFundamentalAgree) Description() string      { return "Metrics and fundamentals agree on overlapping fields" }
func (c *MetricFundamentalAgree) Phase() CheckPhase        { return PhaseAudit }
func (c *MetricFundamentalAgree) Severity() CheckSeverity  { return SeverityWarning }
func (c *MetricFundamentalAgree) DataTypes() []string       { return []string{"fundamental", "metric"} }

func (c *MetricFundamentalAgree) Audit(ctx context.Context, pool *pgxpool.Pool, _ string, lastChecked *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	// Check if both views exist
	for _, view := range []string{"fundamentals", "metrics"} {
		var exists bool

		err = conn.QueryRow(ctx,
			"SELECT EXISTS(SELECT 1 FROM information_schema.views WHERE table_name = $1)", view).Scan(&exists)
		if err != nil || !exists {
			return nil, err
		}
	}

	dateFilter := ""
	args := []interface{}{}

	if lastChecked != nil {
		dateFilter = "AND f.event_date > $1"
		args = append(args, *lastChecked)
	}

	// Compare PE between fundamentals and metrics
	query := fmt.Sprintf(`
		SELECT f.ticker, f.composite_figi, f.dimension, f.event_date,
			f.pe as fund_pe, m.pe as metric_pe
		FROM fundamentals f
		JOIN metrics m ON f.composite_figi = m.composite_figi AND f.event_date = m.event_date
		WHERE f.pe != 0 AND m.pe != 0
		  AND ABS(f.pe - m.pe) / GREATEST(ABS(f.pe), 0.01) > 0.1
		  %s`, dateFilter)

	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []CheckResult

	for rows.Next() {
		var ticker, figi, dimension string
		var eventDate time.Time
		var fundPE, metricPE float64

		if err := rows.Scan(&ticker, &figi, &dimension, &eventDate, &fundPE, &metricPE); err != nil {
			return results, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      c.Severity(),
			Ticker:        ticker,
			CompositeFigi: figi,
			Dimension:     dimension,
			EventDate:     eventDate,
			Field:         "pe",
			Message:       "PE ratio disagrees between fundamentals and metrics",
			Expected:      fmt.Sprintf("%.2f (fundamentals)", fundPE),
			Actual:        fmt.Sprintf("%.2f (metrics)", metricPE),
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
```

- [ ] **Step 2: Write duplicate_observations check**

```go
// checks/duplicate_observations.go
package checks

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type DuplicateObservations struct{}

func (c *DuplicateObservations) Name() string             { return "duplicate-observations" }
func (c *DuplicateObservations) Description() string      { return "Same key appears with different values across tables" }
func (c *DuplicateObservations) Phase() CheckPhase        { return PhaseAudit }
func (c *DuplicateObservations) Severity() CheckSeverity  { return SeverityError }
func (c *DuplicateObservations) DataTypes() []string       { return []string{"fundamental"} }

func (c *DuplicateObservations) Audit(ctx context.Context, pool *pgxpool.Pool, table string, lastChecked *time.Time, _ *time.Duration) ([]CheckResult, error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	defer conn.Release()

	// Check for duplicate primary keys within the table itself
	// (should not happen due to PK constraint, but check for same key with different revenues)
	dateFilter := ""
	args := []interface{}{}

	if lastChecked != nil {
		dateFilter = "WHERE event_date > $1"
		args = append(args, *lastChecked)
	}

	query := fmt.Sprintf(`
		SELECT composite_figi, dimension, event_date, COUNT(*) as cnt
		FROM %s
		%s
		GROUP BY composite_figi, dimension, event_date
		HAVING COUNT(*) > 1`, table, dateFilter)

	rows, err := conn.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var results []CheckResult

	for rows.Next() {
		var figi, dimension string
		var eventDate time.Time
		var cnt int

		if err := rows.Scan(&figi, &dimension, &eventDate, &cnt); err != nil {
			return results, err
		}

		results = append(results, CheckResult{
			CheckName:     c.Name(),
			Severity:      c.Severity(),
			CompositeFigi: figi,
			Dimension:     dimension,
			EventDate:     eventDate,
			Field:         "composite_figi,dimension,event_date",
			Message:       fmt.Sprintf("Duplicate primary key found (%d occurrences)", cnt),
			DataType:      "fundamental",
		})
	}

	return results, rows.Err()
}
```

- [ ] **Step 3: Register in register.go**

Add to `checks/register.go` init():

```go
	// Layer 5: Cross-Type Consistency (Audit only)
	RegisterAudit(&MetricFundamentalAgree{})
	RegisterAudit(&DuplicateObservations{})
```

- [ ] **Step 4: Verify compilation and lint**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./... && golangci-lint run --fix ./...`
Expected: no errors

- [ ] **Step 5: Commit**

```bash
git add checks/metric_fundamental_agree.go checks/duplicate_observations.go checks/register.go
git commit -m "feat: add layer 5 cross-type consistency audit checks"
```

---

### Task 14: Web API Endpoints

**Files:**
- Create: `web/handlers_quality.go`
- Modify: `web/route.go:19-42`

- [ ] **Step 1: Write quality handlers**

```go
// web/handlers_quality.go
package web

import (
	"strconv"

	"github.com/gofiber/fiber/v2"
	"github.com/rs/zerolog/log"
)

// GetQualityIssues returns paginated, filterable data quality issues.
func GetQualityIssues(c *fiber.Ctx) error {
	myLibrary := getLibrary(c)

	limit, _ := strconv.Atoi(c.Query("limit", "50"))
	offset, _ := strconv.Atoi(c.Query("offset", "0"))
	severity := c.Query("severity")
	dataType := c.Query("data_type")
	checkName := c.Query("check_name")
	ticker := c.Query("ticker")

	query := `SELECT id, check_name, severity, data_type, ticker, composite_figi,
		dimension, event_date, field, message, expected, actual,
		subscription_id, run_id, detected_at
		FROM data_quality_issues WHERE 1=1`
	countQuery := `SELECT count(*) FROM data_quality_issues WHERE 1=1`

	args := []interface{}{}
	paramIdx := 1

	if severity != "" {
		query += " AND severity = $" + strconv.Itoa(paramIdx)
		countQuery += " AND severity = $" + strconv.Itoa(paramIdx)
		args = append(args, severity)
		paramIdx++
	}

	if dataType != "" {
		query += " AND data_type = $" + strconv.Itoa(paramIdx)
		countQuery += " AND data_type = $" + strconv.Itoa(paramIdx)
		args = append(args, dataType)
		paramIdx++
	}

	if checkName != "" {
		query += " AND check_name = $" + strconv.Itoa(paramIdx)
		countQuery += " AND check_name = $" + strconv.Itoa(paramIdx)
		args = append(args, checkName)
		paramIdx++
	}

	if ticker != "" {
		query += " AND ticker = $" + strconv.Itoa(paramIdx)
		countQuery += " AND ticker = $" + strconv.Itoa(paramIdx)
		args = append(args, ticker)
		paramIdx++
	}

	query += " ORDER BY detected_at DESC"
	query += " LIMIT $" + strconv.Itoa(paramIdx) + " OFFSET $" + strconv.Itoa(paramIdx+1)

	conn, err := myLibrary.Pool.Acquire(c.UserContext())
	if err != nil {
		log.Error().Err(err).Msg("could not acquire connection")
		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code: "500", Message: "database connection error",
		})
	}
	defer conn.Release()

	// get total count
	var total int

	countArgs := make([]interface{}, len(args))
	copy(countArgs, args)

	err = conn.QueryRow(c.UserContext(), countQuery, countArgs...).Scan(&total)
	if err != nil {
		log.Error().Err(err).Msg("could not count issues")
		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code: "500", Message: "could not count issues",
		})
	}

	queryArgs := append(args, limit, offset)

	rows, err := conn.Query(c.UserContext(), query, queryArgs...)
	if err != nil {
		log.Error().Err(err).Msg("could not query issues")
		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code: "500", Message: "could not load issues",
		})
	}
	defer rows.Close()

	type Issue struct {
		ID             string  `json:"id"`
		CheckName      string  `json:"check_name"`
		Severity       string  `json:"severity"`
		DataType       string  `json:"data_type"`
		Ticker         *string `json:"ticker"`
		CompositeFigi  *string `json:"composite_figi"`
		Dimension      *string `json:"dimension"`
		EventDate      *string `json:"event_date"`
		Field          *string `json:"field"`
		Message        string  `json:"message"`
		Expected       *string `json:"expected"`
		Actual         *string `json:"actual"`
		SubscriptionID *string `json:"subscription_id"`
		RunID          *string `json:"run_id"`
		DetectedAt     string  `json:"detected_at"`
	}

	issues := []Issue{}

	for rows.Next() {
		var issue Issue

		err := rows.Scan(&issue.ID, &issue.CheckName, &issue.Severity, &issue.DataType,
			&issue.Ticker, &issue.CompositeFigi, &issue.Dimension, &issue.EventDate,
			&issue.Field, &issue.Message, &issue.Expected, &issue.Actual,
			&issue.SubscriptionID, &issue.RunID, &issue.DetectedAt)
		if err != nil {
			log.Error().Err(err).Msg("could not scan issue row")
			continue
		}

		issues = append(issues, issue)
	}

	return c.JSON(fiber.Map{
		"issues": issues,
		"total":  total,
		"limit":  limit,
		"offset": offset,
	})
}

// GetQualitySummary returns aggregate counts by severity and check name.
func GetQualitySummary(c *fiber.Ctx) error {
	myLibrary := getLibrary(c)

	conn, err := myLibrary.Pool.Acquire(c.UserContext())
	if err != nil {
		log.Error().Err(err).Msg("could not acquire connection")
		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code: "500", Message: "database connection error",
		})
	}
	defer conn.Release()

	rows, err := conn.Query(c.UserContext(), `
		SELECT check_name, severity, data_type, count(*) as cnt
		FROM data_quality_issues
		GROUP BY check_name, severity, data_type
		ORDER BY severity, check_name`)
	if err != nil {
		log.Error().Err(err).Msg("could not query summary")
		return c.Status(fiber.StatusInternalServerError).JSON(HttpError{
			Code: "500", Message: "could not load summary",
		})
	}
	defer rows.Close()

	type SummaryRow struct {
		CheckName string `json:"check_name"`
		Severity  string `json:"severity"`
		DataType  string `json:"data_type"`
		Count     int    `json:"count"`
	}

	var summary []SummaryRow

	for rows.Next() {
		var row SummaryRow

		if err := rows.Scan(&row.CheckName, &row.Severity, &row.DataType, &row.Count); err != nil {
			continue
		}

		summary = append(summary, row)
	}

	return c.JSON(summary)
}
```

- [ ] **Step 2: Register routes in route.go**

Add to `web/route.go` in `SetupRoutes`, after the `api.Post("/sql/export", ExportSQL)` line:

```go
	api.Get("/quality/issues", GetQualityIssues)
	api.Get("/quality/summary", GetQualitySummary)
```

- [ ] **Step 3: Verify compilation and lint**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./... && golangci-lint run --fix ./...`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add web/handlers_quality.go web/route.go
git commit -m "feat: add data quality API endpoints"
```

---

### Task 15: Web UI - Data Quality Page

**Files:**
- Create: `web/ui/src/pages/DataQualityPage.vue`
- Modify: `web/ui/src/router/index.ts`
- Modify: `web/ui/src/App.vue`
- Modify: `web/ui/src/lib/api.ts`

- [ ] **Step 1: Add API client functions**

Append to `web/ui/src/lib/api.ts`:

```typescript
// ---------- Data Quality ----------

export async function getQualityIssues(params: Record<string, string> = {}) {
  const qs = new URLSearchParams(params).toString()
  const res = await authFetch(`/quality/issues${qs ? '?' + qs : ''}`)
  return handleResponse<{ issues: any[]; total: number; limit: number; offset: number }>(res)
}

export async function getQualitySummary() {
  const res = await authFetch('/quality/summary')
  return handleResponse<any[]>(res)
}
```

- [ ] **Step 2: Create Data Quality page**

```vue
<!-- web/ui/src/pages/DataQualityPage.vue -->
<script setup lang="ts">
import { ref, onMounted, computed, watch } from 'vue'
import DataTable from 'primevue/datatable'
import Column from 'primevue/column'
import Select from 'primevue/select'
import InputText from 'primevue/inputtext'
import Tag from 'primevue/tag'
import { getQualityIssues, getQualitySummary } from '@/lib/api'

const issues = ref<any[]>([])
const summary = ref<any[]>([])
const total = ref(0)
const loading = ref(false)
const page = ref(0)
const rows = ref(50)

const severityFilter = ref('')
const dataTypeFilter = ref('')
const tickerFilter = ref('')

const severityOptions = [
  { label: 'All', value: '' },
  { label: 'Critical', value: 'critical' },
  { label: 'Error', value: 'error' },
  { label: 'Warning', value: 'warning' },
  { label: 'Info', value: 'info' },
]

const severityCounts = computed(() => {
  const counts: Record<string, number> = { critical: 0, error: 0, warning: 0, info: 0 }
  for (const row of summary.value) {
    counts[row.severity] = (counts[row.severity] || 0) + row.count
  }
  return counts
})

function severityColor(severity: string): 'danger' | 'warn' | 'info' | 'secondary' {
  switch (severity) {
    case 'critical': return 'danger'
    case 'error': return 'danger'
    case 'warning': return 'warn'
    default: return 'info'
  }
}

async function loadIssues() {
  loading.value = true
  try {
    const params: Record<string, string> = {
      limit: rows.value.toString(),
      offset: (page.value * rows.value).toString(),
    }
    if (severityFilter.value) params.severity = severityFilter.value
    if (dataTypeFilter.value) params.data_type = dataTypeFilter.value
    if (tickerFilter.value) params.ticker = tickerFilter.value

    const data = await getQualityIssues(params)
    issues.value = data.issues
    total.value = data.total
  } finally {
    loading.value = false
  }
}

async function loadSummary() {
  summary.value = await getQualitySummary()
}

function onPage(event: any) {
  page.value = event.page
  rows.value = event.rows
  loadIssues()
}

watch([severityFilter, dataTypeFilter, tickerFilter], () => {
  page.value = 0
  loadIssues()
})

onMounted(() => {
  loadSummary()
  loadIssues()
})
</script>

<template>
  <div>
    <h1>Data Quality</h1>

    <div style="display: flex; gap: 1rem; margin-bottom: 1.5rem">
      <div v-for="(count, sev) in severityCounts" :key="sev" class="summary-card">
        <Tag :severity="severityColor(sev as string)" :value="sev" />
        <span class="summary-count">{{ count }}</span>
      </div>
    </div>

    <div style="display: flex; gap: 1rem; margin-bottom: 1rem">
      <Select v-model="severityFilter" :options="severityOptions" optionLabel="label" optionValue="value" placeholder="Severity" />
      <InputText v-model="dataTypeFilter" placeholder="Data type" />
      <InputText v-model="tickerFilter" placeholder="Ticker" />
    </div>

    <DataTable
      :value="issues"
      :loading="loading"
      :lazy="true"
      :paginator="true"
      :rows="rows"
      :totalRecords="total"
      :first="page * rows"
      @page="onPage"
      stripedRows
      size="small"
    >
      <Column field="detected_at" header="Detected" sortable style="width: 140px" />
      <Column field="severity" header="Severity" style="width: 100px">
        <template #body="{ data }">
          <Tag :severity="severityColor(data.severity)" :value="data.severity" />
        </template>
      </Column>
      <Column field="check_name" header="Check" sortable />
      <Column field="ticker" header="Ticker" sortable style="width: 100px" />
      <Column field="data_type" header="Type" style="width: 120px" />
      <Column field="message" header="Message" />
      <Column field="event_date" header="Event Date" style="width: 120px" />
      <Column field="field" header="Field" style="width: 160px" />
    </DataTable>
  </div>
</template>

<style scoped>
.summary-card {
  display: flex;
  align-items: center;
  gap: 0.5rem;
  padding: 0.75rem 1rem;
  border: 1px solid rgba(255, 255, 255, 0.08);
  border-radius: 8px;
}

.summary-count {
  font-size: 1.5rem;
  font-weight: 600;
}
</style>
```

- [ ] **Step 3: Add route**

In `web/ui/src/router/index.ts`, add before the auth-callback route:

```typescript
  {
    path: '/data-quality',
    name: 'data-quality',
    component: () => import('@/pages/DataQualityPage.vue'),
  },
```

- [ ] **Step 4: Add menu item**

In `web/ui/src/App.vue`, add to the `menuItems` array:

```typescript
  { label: 'Data Quality', icon: 'pi pi-check-circle', command: () => router.push('/data-quality') },
```

- [ ] **Step 5: Build frontend**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && make build-ui`
Expected: no errors

- [ ] **Step 6: Commit**

```bash
git add web/ui/src/pages/DataQualityPage.vue web/ui/src/router/index.ts web/ui/src/App.vue web/ui/src/lib/api.ts
git commit -m "feat: add Data Quality page to web UI"
```

---

### Task 16: Post-Run CLI Summary

**Files:**
- Create: `checks/summary.go`
- Modify: `cmd/serve.go` (where subscription run completes)
- Modify: `web/handlers_run_now.go` (where web-triggered run completes)

- [ ] **Step 1: Create summary helper**

```go
// checks/summary.go
package checks

import "fmt"

// FormatSummaryLine returns a one-line summary like:
// "Data quality: 2 critical, 5 errors, 12 warnings"
// Returns empty string if no results.
func FormatSummaryLine(results []CheckResult) string {
	if len(results) == 0 {
		return ""
	}

	counts := map[CheckSeverity]int{}
	for _, r := range results {
		counts[r.Severity]++
	}

	return fmt.Sprintf("Data quality: %d critical, %d errors, %d warnings (run `pvdata check` for details)",
		counts[SeverityCritical],
		counts[SeverityError],
		counts[SeverityWarning],
	)
}
```

- [ ] **Step 2: Wire into serve.go run completion**

In `cmd/serve.go`, after the `wg.Wait()` call (around line 177), query recent issues and log the summary:

```go
	// After wg.Wait() -- query issues from this run
	var critCount, errCount, warnCount int
	_ = conn.QueryRow(ctx,
		`SELECT
			coalesce(sum(case when severity='critical' then 1 else 0 end), 0),
			coalesce(sum(case when severity='error' then 1 else 0 end), 0),
			coalesce(sum(case when severity='warning' then 1 else 0 end), 0)
		FROM data_quality_issues
		WHERE subscription_id = $1 AND detected_at > $2`,
		subscription.ID, summary.StartTime).Scan(&critCount, &errCount, &warnCount)

	if critCount+errCount+warnCount > 0 {
		logger.Warn().
			Int("critical", critCount).
			Int("errors", errCount).
			Int("warnings", warnCount).
			Msg("data quality issues detected (run `pvdata check` for details)")
	}
```

This requires acquiring a connection from the pool for the query. Use `myLibrary.Pool.Acquire(ctx)`.

- [ ] **Step 3: Verify compilation**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`
Expected: no errors

- [ ] **Step 4: Commit**

```bash
git add checks/summary.go cmd/serve.go
git commit -m "feat: add post-run data quality summary line"
```

---

### Task 17: Final Integration Test and Lint

**Files:** (no new files)

- [ ] **Step 1: Run full lint**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && golangci-lint run --fix ./...`
Expected: no errors

- [ ] **Step 2: Run all unit tests**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race ./...`
Expected: PASS

- [ ] **Step 3: Verify build**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && make build`
Expected: no errors

- [ ] **Step 4: Commit any lint fixes**

```bash
git add -A
git commit -m "chore: lint fixes for data consistency checks"
```
