# Migrate Legacy Database Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `pvdata migrate-legacy` CLI command that converts old penny-vault database tables into the new pv-data subscription system.

**Architecture:** Move old tables to a `legacy` schema, create subscriptions with properly-structured tables, and copy/transform data -- all within a single database transaction. Requires refactoring `Save()`, `SavePublishedView()`, and `ManagePartitions()` to accept an optional caller-provided transaction.

**Tech Stack:** Go, pgx/v5, Cobra CLI, Ginkgo/Gomega tests, golang-migrate

**Spec:** `docs/superpowers/specs/2026-03-14-migrate-legacy-design.md`

---

## File Structure

| File | Responsibility |
|---|---|
| `db/migrations/000003_add_datatype_enum_values.up.sql` | Add missing enum values to `datatype` type |
| `db/migrations/000003_add_datatype_enum_values.down.sql` | Remove added enum values |
| `library/subscription.go` | Refactor `Save()` and `ManagePartitions()` to accept optional `pgx.Tx` |
| `library/published_views.go` | Refactor `SavePublishedView()`, `ApplyPublishedView()`, `ValidateSourceTables()` to accept optional `pgx.Tx` |
| `library/subscription_test.go` | Tests for refactored Save() behavior (new file or append) |
| `provider/legacy.go` | Legacy provider with no-op Fetch |
| `provider/discover.go` | Register legacy provider |
| `provider/legacy_test.go` | Tests for legacy provider metadata |
| `cmd/migrate_legacy.go` | Cobra command for `pvdata migrate-legacy` |
| `cmd/migrate_legacy_test.go` | Tests for migration SQL generation and logic |

---

## Chunk 1: Database Migration and Transaction Refactor

### Task 1: Add missing datatype enum values

**Files:**
- Create: `db/migrations/000003_add_datatype_enum_values.up.sql`
- Create: `db/migrations/000003_add_datatype_enum_values.down.sql`

- [ ] **Step 1: Create up migration**

```sql
ALTER TYPE datatype ADD VALUE IF NOT EXISTS 'consensus';
ALTER TYPE datatype ADD VALUE IF NOT EXISTS 'estimate';
ALTER TYPE datatype ADD VALUE IF NOT EXISTS 'index';
ALTER TYPE datatype ADD VALUE IF NOT EXISTS 'quote';
```

Note: `ALTER TYPE ... ADD VALUE` cannot run inside a transaction block in PostgreSQL, so this migration file must NOT be wrapped in `BEGIN`/`COMMIT`. The `golang-migrate` tool runs each file as a separate statement by default.

- [ ] **Step 2: Create down migration**

```sql
-- PostgreSQL does not support removing enum values.
-- To reverse, you would need to recreate the type.
-- This is intentionally left as a no-op since these values should have existed from the start.
```

- [ ] **Step 3: Commit**

```bash
git add db/migrations/000003_add_datatype_enum_values.up.sql db/migrations/000003_add_datatype_enum_values.down.sql
git commit -m "feat: add missing datatype enum values (consensus, estimate, index, quote)"
```

---

### Task 2: Introduce `Querier` interface for optional transaction support

The functions `SavePublishedView()`, `ApplyPublishedView()`, and `ValidateSourceTables()` currently take `*pgxpool.Pool`. `Save()` and `ManagePartitions()` acquire their own connections and create their own transactions. We need all of these to optionally use a caller-provided transaction.

pgx's `pgx.Tx` and `*pgxpool.Pool` both satisfy an `Exec`/`Query`/`QueryRow` interface. We define a `Querier` interface so functions can accept either.

**Files:**
- Modify: `library/published_views.go`
- Modify: `library/subscription.go`

- [ ] **Step 1: Write the failing test for SavePublishedView with a Querier**

Create or append to `library/published_views_test.go`. Since `SavePublishedView` hits the DB we cannot unit test the transaction behavior directly without a DB. Instead, verify the refactored function signatures compile and the existing non-DB tests still pass.

The real test here is that the code compiles with the new signatures. Run:

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...
```

Expected: build failure (signatures not yet changed)

Actually, since we haven't changed signatures yet, the build will succeed. The test is that after changing signatures, existing callers still compile. We will do this as a verify step after the change.

- [ ] **Step 2: Define the Querier interface in `library/published_views.go`**

Add near the top of the file, after the imports:

```go
// Querier is an interface satisfied by both *pgxpool.Pool and pgx.Tx,
// allowing functions to work with either a pool connection or an existing transaction.
type Querier interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}
```

Add `"github.com/jackc/pgx/v5/pgconn"` to the imports if not already present.

- [ ] **Step 3: Refactor `ValidateSourceTables` to use `Querier`**

In `library/published_views.go`, change the signature from:

```go
func ValidateSourceTables(ctx context.Context, pool *pgxpool.Pool, pv *PublishedView) error {
```

to:

```go
func ValidateSourceTables(ctx context.Context, q Querier, pv *PublishedView) error {
```

Replace the body: remove the `pool.Acquire(ctx)` / `defer conn.Release()` block. Replace `conn.QueryRow(...)` with `q.QueryRow(...)`.

The full updated function body:

```go
func ValidateSourceTables(ctx context.Context, q Querier, pv *PublishedView) error {
	for _, src := range pv.Sources {
		tables := []string{src.TableName}
		if pv.DataTypeKey == data.IndexKey {
			tables = []string{src.TableName + "_snapshot", src.TableName + "_changelog"}
		}

		for _, tbl := range tables {
			var exists bool

			err := q.QueryRow(ctx,
				`SELECT EXISTS (
				   SELECT 1 FROM information_schema.tables
				   WHERE table_name = $1 AND table_schema = 'public'
				 )`, tbl).Scan(&exists)
			if err != nil {
				return fmt.Errorf("check table existence for %s: %w", tbl, err)
			}

			if !exists {
				return fmt.Errorf("source table %s does not exist", tbl)
			}
		}
	}

	return nil
}
```

- [ ] **Step 4: Refactor `ApplyPublishedView` to use `Querier`**

Change signature from:

```go
func ApplyPublishedView(ctx context.Context, pool *pgxpool.Pool, pv *PublishedView) error {
```

to:

```go
func ApplyPublishedView(ctx context.Context, q Querier, pv *PublishedView) error {
```

Replace body: remove `pool.Acquire(ctx)` / `defer conn.Release()`, use `q.Exec(...)` directly:

```go
func ApplyPublishedView(ctx context.Context, q Querier, pv *PublishedView) error {
	sqls := pv.GenerateViewSQL()
	for _, sql := range sqls {
		log.Info().Str("sql", sql).Msg("applying published view SQL")

		if _, err := q.Exec(ctx, sql); err != nil {
			return fmt.Errorf("exec published view SQL: %w", err)
		}
	}

	return nil
}
```

- [ ] **Step 5: Refactor `SavePublishedView` to use `Querier`**

Change signature from:

```go
func SavePublishedView(ctx context.Context, pool *pgxpool.Pool, pv *PublishedView) error {
```

to:

```go
func SavePublishedView(ctx context.Context, q Querier, pv *PublishedView) error {
```

Replace body: remove `pool.Acquire(ctx)` / `defer conn.Release()`, use `q` directly:

```go
func SavePublishedView(ctx context.Context, q Querier, pv *PublishedView) error {
	if err := pv.ValidateSources(); err != nil {
		return fmt.Errorf("validate sources: %w", err)
	}

	if err := ValidateSourceTables(ctx, q, pv); err != nil {
		return fmt.Errorf("validate source tables: %w", err)
	}

	if pv.ID == uuid.Nil {
		pv.ID = uuid.New()
	}

	sourcesJSON, err := json.Marshal(pv.Sources)
	if err != nil {
		return fmt.Errorf("marshal sources: %w", err)
	}

	_, err = q.Exec(ctx,
		`INSERT INTO published_views (id, view_name, data_type_key, sources)
		 VALUES ($1, $2, $3, $4)
		 ON CONFLICT (view_name)
		 DO UPDATE SET data_type_key = EXCLUDED.data_type_key, sources = EXCLUDED.sources`,
		pv.ID, pv.ViewName, pv.DataTypeKey, sourcesJSON,
	)
	if err != nil {
		return fmt.Errorf("upsert published view: %w", err)
	}

	return ApplyPublishedView(ctx, q, pv)
}
```

- [ ] **Step 6: Refactor `LoadPublishedViews` to use `Querier`**

Change signature from:

```go
func LoadPublishedViews(ctx context.Context, pool *pgxpool.Pool) ([]*PublishedView, error) {
```

to:

```go
func LoadPublishedViews(ctx context.Context, q Querier) ([]*PublishedView, error) {
```

Remove `pool.Acquire(ctx)` / `defer conn.Release()`, use `q.Query(...)` directly.

- [ ] **Step 7: Update `DeletePublishedView` and `PublishedViewReferencesTable` to use `Querier`**

Same pattern: change `pool *pgxpool.Pool` to `q Querier`, remove acquire/release, use `q` directly. Update `LoadPublishedView` (singular) too if it exists.

- [ ] **Step 8: Update all callers of the refactored functions**

Search for all call sites. Since `*pgxpool.Pool` satisfies the `Querier` interface, existing callers that pass `pool` will continue to work without changes. However, verify by checking:

- `library/subscription.go` line 275: `LoadPublishedViews(ctx, subscription.Library.Pool)` -- still works, Pool satisfies Querier
- `library/subscription.go` line 306: `SavePublishedView(ctx, subscription.Library.Pool, pv)` -- still works
- `library/subscription.go` line 78: `PublishedViewReferencesTable(ctx, subscription.Library.Pool, tblName)` -- still works
- `cmd/publish.go` and `tui/publish.go`: find and verify all call sites pass Pool

- [ ] **Step 9: Verify build and existing tests pass**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...
cd /Users/jdf/Developer/penny-vault/pv-data && go test ./library/... -v
```

Expected: all pass with no changes to callers (Pool satisfies Querier)

- [ ] **Step 10: Commit**

```bash
git add library/published_views.go library/subscription.go
git commit -m "refactor: introduce Querier interface for optional transaction support

Replace *pgxpool.Pool parameters with Querier interface in
SavePublishedView, ApplyPublishedView, ValidateSourceTables,
LoadPublishedViews, DeletePublishedView, and PublishedViewReferencesTable.
Both *pgxpool.Pool and pgx.Tx satisfy Querier, so existing callers
are unaffected."
```

---

### Task 3: Refactor `Subscription.Save()` to accept optional transaction

**Files:**
- Modify: `library/subscription.go`

- [ ] **Step 1: Add `SaveWithTx` method**

Rather than adding variadic options to `Save()`, add a new `SaveWithTx` method that accepts a `pgx.Tx`. Refactor `Save()` to create its own transaction and delegate to `SaveWithTx`. This avoids changing `Save()`'s signature.

```go
// SaveWithTx saves the subscription using the provided transaction.
// The caller owns the transaction lifecycle (commit/rollback).
func (subscription *Subscription) SaveWithTx(ctx context.Context, tx pgx.Tx) error {
	// create table structure for each data type this dataset produces
	if err := subscription.createTables(ctx, tx); err != nil {
		return err
	}

	// make sure current user is set on subscription
	if user, err := user.Current(); err != nil {
		return err
	} else {
		subscription.CreatedBy = user.Username
	}

	// create an entry in the subscription table
	if _, err := tx.Exec(ctx, `INSERT INTO subscriptions
("id", "name", "provider", "dataset", "config", "data_tables", "data_types",
 "schedule", "health_check_id", "schema_version", "created_by")
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11);`, subscription.ID.String(),
		subscription.Name, subscription.Provider, subscription.Dataset, subscription.Config,
		subscription.DataTables, subscription.DataTypes, subscription.Schedule,
		subscription.HealthCheckID, subscription.SchemaVersion, subscription.CreatedBy); err != nil {
		return err
	}

	// manage partitions
	if err := subscription.managePartitionsWithTransaction(ctx, tx); err != nil {
		return err
	}

	// auto-create published views for data types that don't have one yet
	existingViews, err := LoadPublishedViews(ctx, tx)
	if err != nil {
		log.Warn().Err(err).Msg("could not query existing published views")
	} else {
		existingSet := make(map[string]bool)
		for _, pv := range existingViews {
			existingSet[pv.ViewName] = true
		}

		for _, dataTypeKey := range subscription.DataTypes {
			dt := data.DataTypes[dataTypeKey]
			if dt == nil || dt.ViewName == "" {
				continue
			}

			if existingSet[dt.ViewName] {
				continue
			}

			tableName := subscription.DataTablesMap[dataTypeKey]
			if tableName == "" {
				continue
			}

			pv := &PublishedView{
				ViewName:    dt.ViewName,
				DataTypeKey: dataTypeKey,
				Sources: []ViewSource{
					{TableName: tableName, SubscriptionID: subscription.ID.String()},
				},
			}
			if err := SavePublishedView(ctx, tx, pv); err != nil {
				log.Warn().Err(err).Str("DataType", dataTypeKey).Msg("could not auto-create published view")
			}
		}
	}

	return nil
}
```

- [ ] **Step 2: Refactor `Save()` to delegate to `SaveWithTx()`**

Replace the body of `Save()`:

```go
func (subscription *Subscription) Save(ctx context.Context) error {
	conn, err := subscription.Library.Pool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Rollback(ctx); err != nil {
			if !errors.Is(err, pgx.ErrTxClosed) {
				log.Error().Err(err).Msg("error rollingback tx")
			}
		}
	}()

	if err := subscription.SaveWithTx(ctx, tx); err != nil {
		return err
	}

	return tx.Commit(ctx)
}
```

Note: This moves published view auto-creation inside the transaction -- a behavior improvement even outside the migration use case.

- [ ] **Step 3: Verify build and existing tests pass**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...
cd /Users/jdf/Developer/penny-vault/pv-data && go test ./library/... -v
```

- [ ] **Step 4: Commit**

```bash
git add library/subscription.go
git commit -m "refactor: add SaveWithTx for caller-managed transactions

Extract Save() logic into SaveWithTx(tx) so callers can participate
in a larger transaction. Save() now delegates to SaveWithTx with its
own transaction. Published view auto-creation now runs inside the
transaction."
```

---

## Chunk 2: Legacy Provider

### Task 4: Create legacy provider

**Files:**
- Create: `provider/legacy.go`
- Modify: `provider/discover.go`
- Create: `provider/legacy_test.go`

- [ ] **Step 1: Write the failing test**

Create `provider/legacy_test.go`:

```go
package provider_test

import (
	"testing"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/provider"
)

func TestLegacyProvider(t *testing.T) {
	p, ok := provider.Map["legacy"]
	if !ok {
		t.Fatal("legacy provider not registered in Map")
	}

	if p.Name() != "Legacy" {
		t.Errorf("expected name 'Legacy', got %q", p.Name())
	}

	datasets := p.Datasets()

	// Check EOD dataset
	eodDS, ok := datasets["eod"]
	if !ok {
		t.Fatal("missing 'eod' dataset")
	}
	if len(eodDS.DataTypes) != 1 || eodDS.DataTypes[0].Name != data.EODKey {
		t.Errorf("eod dataset should have exactly one data type: eod")
	}
	if eodDS.Fetch != nil {
		t.Error("eod dataset Fetch should be nil")
	}

	// Check assets dataset
	assetsDS, ok := datasets["assets"]
	if !ok {
		t.Fatal("missing 'assets' dataset")
	}
	if len(assetsDS.DataTypes) != 1 || assetsDS.DataTypes[0].Name != data.AssetKey {
		t.Errorf("assets dataset should have exactly one data type: asset-description")
	}

	// Check market-holidays dataset
	mhDS, ok := datasets["market-holidays"]
	if !ok {
		t.Fatal("missing 'market-holidays' dataset")
	}
	if len(mhDS.DataTypes) != 1 || mhDS.DataTypes[0].Name != data.MarketHolidaysKey {
		t.Errorf("market-holidays dataset should have exactly one data type: market-holidays")
	}

	// Check Zacks Screener Data dataset
	zacksDS, ok := datasets["Zacks Screener Data"]
	if !ok {
		t.Fatal("missing 'Zacks Screener Data' dataset")
	}
	expectedTypes := map[string]bool{
		data.RatingKey:    true,
		data.MetricKey:    true,
		data.EstimateKey:  true,
		data.ConsensusKey: true,
	}
	if len(zacksDS.DataTypes) != 4 {
		t.Errorf("expected 4 data types for zacks, got %d", len(zacksDS.DataTypes))
	}
	for _, dt := range zacksDS.DataTypes {
		if !expectedTypes[dt.Name] {
			t.Errorf("unexpected data type %q in zacks dataset", dt.Name)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run TestLegacyProvider -v
```

Expected: FAIL -- `legacy provider not registered in Map`

- [ ] **Step 3: Create `provider/legacy.go`**

```go
package provider

import (
	"github.com/penny-vault/pvdata/data"
)

// Legacy is a provider for legacy database data that has been migrated.
// It has no Fetch function -- data is populated by the migrate-legacy command.
type Legacy struct{}

func (l *Legacy) Name() string {
	return "Legacy"
}

func (l *Legacy) ConfigDescription() map[string]string {
	return map[string]string{}
}

func (l *Legacy) Description() string {
	return "Migrated data from a legacy penny-vault database. Not fetchable."
}

func (l *Legacy) Datasets() map[string]Dataset {
	return map[string]Dataset{
		"eod": {
			Name:        "eod",
			Description: "Legacy EOD price data",
			DataTypes:   []*data.DataType{data.DataTypes[data.EODKey]},
		},
		"assets": {
			Name:        "assets",
			Description: "Legacy asset descriptions",
			DataTypes:   []*data.DataType{data.DataTypes[data.AssetKey]},
		},
		"market-holidays": {
			Name:        "market-holidays",
			Description: "Legacy market holidays",
			DataTypes:   []*data.DataType{data.DataTypes[data.MarketHolidaysKey]},
		},
		"Zacks Screener Data": {
			Name:        "Zacks Screener Data",
			Description: "Legacy Zacks screener data",
			DataTypes: []*data.DataType{
				data.DataTypes[data.RatingKey],
				data.DataTypes[data.MetricKey],
				data.DataTypes[data.EstimateKey],
				data.DataTypes[data.ConsensusKey],
			},
		},
	}
}
```

- [ ] **Step 4: Register in `provider/discover.go`**

Add to the `Map`:

```go
"legacy": &Legacy{},
```

- [ ] **Step 5: Run test to verify it passes**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run TestLegacyProvider -v
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add provider/legacy.go provider/legacy_test.go provider/discover.go
git commit -m "feat: add legacy provider for migrated database data"
```

---

## Chunk 3: Migrate-Legacy Command

### Task 5: Create the migrate-legacy command with preflight checks

**Files:**
- Create: `cmd/migrate_legacy.go`

- [ ] **Step 1: Create the cobra command skeleton with preflight checks**

```go
package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var migrateLegacyCmd = &cobra.Command{
	Use:   "migrate-legacy",
	Short: "Migrate a legacy penny-vault database to the pv-data subscription system",
	Long: `Moves legacy tables to a 'legacy' schema, creates subscriptions with
properly-structured tables, and copies/transforms data. The entire operation
runs in a single transaction.

Tables converted: eod, assets, market_holidays, zacks_financials
Tables moved only: reported_financials, seeking_alpha, zacks_number_1, trading_days,
  schema_migrations, activity, announcements, portfolios, portfolio_transactions,
  portfolio_measurements, profile`,
	RunE: runMigrateLegacy,
}

func init() {
	rootCmd.AddCommand(migrateLegacyCmd)
	migrateLegacyCmd.Flags().Bool("dry-run", false, "Print what would be done without making changes")
	migrateLegacyCmd.Flags().Bool("force", false, "Clean up failed prior run before migrating")
}

// legacyTables lists all tables expected in the old penny-vault database.
var legacyTables = []string{
	"activity", "announcements", "assets", "eod", "market_holidays",
	"portfolio_measurements", "portfolio_transactions", "portfolios",
	"profile", "reported_financials", "schema_migrations", "seeking_alpha",
	"trading_days", "zacks_financials", "zacks_number_1",
}

// requiredLegacyTables are tables that must exist for migration to proceed.
var requiredLegacyTables = []string{"eod", "assets"}

func runMigrateLegacy(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("force")

	dbURL := viper.GetString("db.url")
	if dbURL == "" {
		return fmt.Errorf("db.url is not configured")
	}

	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	defer pool.Close()

	myLibrary, err := library.NewFromDB(ctx, dbURL)
	if err != nil {
		return fmt.Errorf("create library: %w", err)
	}
	defer myLibrary.Close()

	// Force cleanup if requested
	if force {
		if err := cleanupLegacyMigration(ctx, pool, myLibrary); err != nil {
			return fmt.Errorf("force cleanup: %w", err)
		}
	}

	// Preflight checks
	if err := preflightChecks(ctx, pool); err != nil {
		return err
	}

	if dryRun {
		log.Info().Msg("[dry-run] Preflight checks passed. Would proceed with migration.")
		return nil
	}

	// Run the migration
	return executeMigration(ctx, pool, myLibrary)
}

func preflightChecks(ctx context.Context, pool *pgxpool.Pool) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	// Check that legacy schema does not already exist
	var schemaExists bool
	err = conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = 'legacy')`).Scan(&schemaExists)
	if err != nil {
		return fmt.Errorf("check legacy schema: %w", err)
	}
	if schemaExists {
		return fmt.Errorf("legacy schema already exists. Use --force to clean up a failed prior run")
	}

	// Check that required legacy tables exist in public
	for _, tbl := range requiredLegacyTables {
		var exists bool
		err = conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
			tbl).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check table %s: %w", tbl, err)
		}
		if !exists {
			return fmt.Errorf("required legacy table %q not found in public schema", tbl)
		}
	}

	// Check that pv-data library tables exist
	for _, tbl := range []string{"subscriptions", "published_views"} {
		var exists bool
		err = conn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
			tbl).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check table %s: %w", tbl, err)
		}
		if !exists {
			return fmt.Errorf("pv-data table %q not found. Run database migrations first", tbl)
		}
	}

	// Check no legacy subscriptions exist
	var legacySubCount int
	err = conn.QueryRow(ctx,
		`SELECT count(*) FROM subscriptions WHERE provider = 'legacy'`).Scan(&legacySubCount)
	if err != nil {
		return fmt.Errorf("check legacy subscriptions: %w", err)
	}
	if legacySubCount > 0 {
		return fmt.Errorf("subscriptions with provider 'legacy' already exist. Use --force to clean up")
	}

	log.Info().Msg("preflight checks passed")
	return nil
}

func cleanupLegacyMigration(ctx context.Context, pool *pgxpool.Pool, myLibrary *library.Library) error {
	log.Info().Msg("cleaning up previous legacy migration...")

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	// Delete legacy subscriptions and their tables
	subs, err := myLibrary.Subscriptions(ctx)
	if err != nil {
		return fmt.Errorf("list subscriptions: %w", err)
	}

	for _, sub := range subs {
		if sub.Provider == "legacy" {
			// Remove published view references first
			for _, dataTypeKey := range sub.DataTypes {
				dt := data.DataTypes[dataTypeKey]
				if dt != nil && dt.ViewName != "" {
					if err := library.DeletePublishedView(ctx, pool, dt.ViewName); err != nil {
						log.Warn().Err(err).Str("ViewName", dt.ViewName).Msg("could not delete published view")
					}
				}
			}

			if err := sub.Delete(ctx); err != nil {
				return fmt.Errorf("delete legacy subscription %s: %w", sub.Name, err)
			}
			log.Info().Str("Name", sub.Name).Msg("deleted legacy subscription")
		}
	}

	// Move tables back from legacy schema to public if legacy schema exists
	var schemaExists bool
	err = conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.schemata WHERE schema_name = 'legacy')`).Scan(&schemaExists)
	if err != nil {
		return err
	}

	if schemaExists {
		// Get all tables in legacy schema
		rows, err := conn.Query(ctx,
			`SELECT table_name FROM information_schema.tables WHERE table_schema = 'legacy'`)
		if err != nil {
			return err
		}
		defer rows.Close()

		var tables []string
		for rows.Next() {
			var tbl string
			if err := rows.Scan(&tbl); err != nil {
				return err
			}
			tables = append(tables, tbl)
		}

		for _, tbl := range tables {
			_, err := conn.Exec(ctx, fmt.Sprintf("ALTER TABLE legacy.%s SET SCHEMA public", tbl))
			if err != nil {
				log.Warn().Err(err).Str("Table", tbl).Msg("could not move table back to public")
			}
		}

		_, err = conn.Exec(ctx, "DROP SCHEMA IF EXISTS legacy")
		if err != nil {
			return fmt.Errorf("drop legacy schema: %w", err)
		}
	}

	log.Info().Msg("cleanup complete")
	return nil
}

func executeMigration(ctx context.Context, pool *pgxpool.Pool, myLibrary *library.Library) error {
	// Implementation in next task
	return fmt.Errorf("not yet implemented")
}
```

Note: add `"github.com/penny-vault/pvdata/data"` to the imports for the `cleanupLegacyMigration` function.

- [ ] **Step 2: Verify build**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add cmd/migrate_legacy.go
git commit -m "feat: add migrate-legacy command skeleton with preflight checks"
```

---

### Task 6: Implement the migration execution

**Files:**
- Modify: `cmd/migrate_legacy.go`

- [ ] **Step 1: Implement `executeMigration`**

Replace the stub `executeMigration` function with the full implementation:

```go
func executeMigration(ctx context.Context, pool *pgxpool.Pool, myLibrary *library.Library) error {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection: %w", err)
	}
	defer conn.Release()

	tx, err := conn.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	defer func() {
		if err := tx.Rollback(ctx); err != nil {
			if !errors.Is(err, pgx.ErrTxClosed) {
				log.Error().Err(err).Msg("error rolling back migration transaction")
			}
		}
	}()

	// Step 1: Create legacy schema and move tables
	if err := moveToLegacySchema(ctx, tx); err != nil {
		return fmt.Errorf("move tables to legacy schema: %w", err)
	}

	// Step 2: Validate composite_figi lengths
	if err := validateCompositeFigi(ctx, tx); err != nil {
		return fmt.Errorf("validate composite_figi: %w", err)
	}

	// Step 3: Create subscriptions
	subs, err := createLegacySubscriptions(ctx, tx, myLibrary)
	if err != nil {
		return fmt.Errorf("create subscriptions: %w", err)
	}

	// Step 4: Copy and transform data
	if err := copyData(ctx, tx, subs); err != nil {
		return fmt.Errorf("copy data: %w", err)
	}

	// Step 5: Update subscription metadata
	if err := updateSubscriptionMetadata(ctx, tx, subs); err != nil {
		return fmt.Errorf("update metadata: %w", err)
	}

	// Commit
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}

	log.Info().Msg("legacy migration completed successfully")
	return nil
}
```

Add these imports to the file: `"errors"`, `"github.com/jackc/pgx/v5"`.

- [ ] **Step 2: Implement `moveToLegacySchema`**

```go
func moveToLegacySchema(ctx context.Context, tx pgx.Tx) error {
	log.Info().Msg("creating legacy schema and moving tables...")

	if _, err := tx.Exec(ctx, "CREATE SCHEMA legacy"); err != nil {
		return fmt.Errorf("create schema: %w", err)
	}

	for _, tbl := range legacyTables {
		// Check if table exists before trying to move it
		var exists bool
		err := tx.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
			tbl).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check table %s: %w", tbl, err)
		}

		if exists {
			if _, err := tx.Exec(ctx, fmt.Sprintf("ALTER TABLE public.%s SET SCHEMA legacy", tbl)); err != nil {
				return fmt.Errorf("move table %s: %w", tbl, err)
			}
			log.Info().Str("Table", tbl).Msg("moved to legacy schema")
		} else {
			log.Warn().Str("Table", tbl).Msg("table not found, skipping")
		}
	}

	return nil
}
```

- [ ] **Step 3: Implement `validateCompositeFigi`**

```go
func validateCompositeFigi(ctx context.Context, tx pgx.Tx) error {
	tables := []string{"legacy.eod", "legacy.zacks_financials"}

	for _, tbl := range tables {
		var exists bool
		err := tx.QueryRow(ctx,
			fmt.Sprintf(`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'legacy' AND table_name = '%s')`,
				tbl[len("legacy."):])).Scan(&exists)
		if err != nil {
			return err
		}
		if !exists {
			continue
		}

		var badCount int
		err = tx.QueryRow(ctx,
			fmt.Sprintf(`SELECT COUNT(*) FROM %s WHERE LENGTH(TRIM(composite_figi)) != 12`, tbl)).Scan(&badCount)
		if err != nil {
			return fmt.Errorf("validate composite_figi in %s: %w", tbl, err)
		}

		if badCount > 0 {
			log.Warn().Int("Count", badCount).Str("Table", tbl).
				Msg("rows with invalid composite_figi length will be excluded from migration")
		}
	}

	return nil
}
```

- [ ] **Step 4: Implement `createLegacySubscriptions`**

```go
type legacySubscriptions struct {
	eod            *library.Subscription
	assets         *library.Subscription
	marketHolidays *library.Subscription
	zacks          *library.Subscription
}

func createLegacySubscriptions(ctx context.Context, tx pgx.Tx, myLibrary *library.Library) (*legacySubscriptions, error) {
	log.Info().Msg("creating legacy subscriptions...")

	subs := &legacySubscriptions{}

	type subDef struct {
		name      string
		dataset   string
		dataTypes []string
		target    **library.Subscription
	}

	defs := []subDef{
		{"Legacy EOD", "eod", []string{data.EODKey}, &subs.eod},
		{"Legacy Assets", "assets", []string{data.AssetKey}, &subs.assets},
		{"Legacy Market Holidays", "market-holidays", []string{data.MarketHolidaysKey}, &subs.marketHolidays},
		{"Legacy Zacks", "Zacks Screener Data", []string{data.RatingKey, data.MetricKey, data.EstimateKey, data.ConsensusKey}, &subs.zacks},
	}

	for _, d := range defs {
		sub := &library.Subscription{
			ID:        uuid.New(),
			Name:      d.name,
			Provider:  "legacy",
			Dataset:   d.dataset,
			Config:    map[string]string{},
			DataTypes: d.dataTypes,
			Active:    false,
			Library:   myLibrary,
		}
		sub.ComputeTableNames()

		if err := sub.SaveWithTx(ctx, tx); err != nil {
			return nil, fmt.Errorf("save subscription %s: %w", d.name, err)
		}

		*d.target = sub
		log.Info().Str("Name", d.name).Strs("Tables", sub.DataTables).Msg("created subscription")
	}

	return subs, nil
}
```

Add `"github.com/google/uuid"` and `"github.com/penny-vault/pvdata/data"` to imports.

- [ ] **Step 5: Implement `copyData`**

```go
func copyData(ctx context.Context, tx pgx.Tx, subs *legacySubscriptions) error {
	log.Info().Msg("copying and transforming data...")

	// EOD
	eodTable := subs.eod.DataTablesMap[data.EODKey]
	if eodTable != "" {
		result, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, open, high, low, close, adj_close, volume, dividend, split_factor)
SELECT ticker, TRIM(composite_figi), event_date, open, high, low, close, COALESCE(adj_close, close), volume, dividend, split_factor
FROM legacy.eod
WHERE LENGTH(TRIM(composite_figi)) = 12`, eodTable))
		if err != nil {
			return fmt.Errorf("copy eod: %w", err)
		}
		log.Info().Int64("Rows", result.RowsAffected()).Msg("copied EOD data")
	}

	// Assets
	assetsTable := subs.assets.DataTablesMap[data.AssetKey]
	if assetsTable != "" {
		result, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, share_class_figi, primary_exchange, asset_type, active, name, description, corporate_url, sector, industry, cik, cusips, isins, other_identifiers, similar_tickers, tags, listed, delisted, last_updated)
SELECT ticker, composite_figi, share_class_figi, primary_exchange,
  CASE asset_type
    WHEN 'Common Stock' THEN 'CS'
    WHEN 'Preferred Stock' THEN 'PS'
    WHEN 'Exchange Traded Fund' THEN 'ETF'
    WHEN 'Exchange Traded Note' THEN 'ETN'
    WHEN 'Mutual Fund' THEN 'MF'
    WHEN 'Closed-End Fund' THEN 'CEF'
    WHEN 'American Depository Receipt Common' THEN 'ADRC'
    WHEN 'FRED' THEN 'FRED'
    WHEN 'Synthetic History' THEN 'SYNTH'
  END::assettype,
  active, name, description, corporate_url, sector, industry, cik,
  CASE WHEN cusip IS NOT NULL THEN ARRAY[TRIM(cusip)] ELSE '{}' END,
  CASE WHEN isin IS NOT NULL THEN ARRAY[TRIM(isin)] ELSE '{}' END,
  '{}'::jsonb,
  similar_tickers, tags, listed_utc, delisted_utc, last_updated_utc
FROM legacy.assets`, assetsTable))
		if err != nil {
			return fmt.Errorf("copy assets: %w", err)
		}
		log.Info().Int64("Rows", result.RowsAffected()).Msg("copied Assets data")
	}

	// Market Holidays
	mhTable := subs.marketHolidays.DataTablesMap[data.MarketHolidaysKey]
	if mhTable != "" {
		result, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (holiday, event_date, market, early_close, close_time)
SELECT holiday, event_date, market, early_close, close_time
FROM legacy.market_holidays`, mhTable))
		if err != nil {
			return fmt.Errorf("copy market_holidays: %w", err)
		}
		log.Info().Int64("Rows", result.RowsAffected()).Msg("copied Market Holidays data")
	}

	// Zacks -> Rating
	if err := copyZacksRatings(ctx, tx, subs.zacks); err != nil {
		return err
	}

	// Zacks -> Metric
	if err := copyZacksMetrics(ctx, tx, subs.zacks); err != nil {
		return err
	}

	// Zacks -> Estimate
	if err := copyZacksEstimates(ctx, tx, subs.zacks); err != nil {
		return err
	}

	// Zacks -> Consensus
	if err := copyZacksConsensus(ctx, tx, subs.zacks); err != nil {
		return err
	}

	return nil
}

func copyZacksRatings(ctx context.Context, tx pgx.Tx, sub *library.Subscription) error {
	tbl := sub.DataTablesMap[data.RatingKey]
	if tbl == "" {
		return nil
	}

	ratingQueries := []struct {
		analyst string
		sql     string
	}{
		{"zacks-rank", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, analyst, rating)
SELECT ticker, TRIM(composite_figi), event_date, 'zacks-rank', zacks_rank
FROM legacy.zacks_financials
WHERE zacks_rank IS NOT NULL AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
		{"zacks-value", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, analyst, rating)
SELECT ticker, TRIM(composite_figi), event_date, 'zacks-value',
  CASE TRIM(value_score) WHEN 'A' THEN 1 WHEN 'B' THEN 2 WHEN 'C' THEN 3 WHEN 'D' THEN 4 WHEN 'F' THEN 5 END
FROM legacy.zacks_financials
WHERE TRIM(value_score) IN ('A','B','C','D','F') AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
		{"zacks-growth", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, analyst, rating)
SELECT ticker, TRIM(composite_figi), event_date, 'zacks-growth',
  CASE TRIM(growth_score) WHEN 'A' THEN 1 WHEN 'B' THEN 2 WHEN 'C' THEN 3 WHEN 'D' THEN 4 WHEN 'F' THEN 5 END
FROM legacy.zacks_financials
WHERE TRIM(growth_score) IN ('A','B','C','D','F') AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
		{"zacks-momentum", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, analyst, rating)
SELECT ticker, TRIM(composite_figi), event_date, 'zacks-momentum',
  CASE TRIM(momentum_score) WHEN 'A' THEN 1 WHEN 'B' THEN 2 WHEN 'C' THEN 3 WHEN 'D' THEN 4 WHEN 'F' THEN 5 END
FROM legacy.zacks_financials
WHERE TRIM(momentum_score) IN ('A','B','C','D','F') AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
		{"zacks-vgm", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, analyst, rating)
SELECT ticker, TRIM(composite_figi), event_date, 'zacks-vgm',
  CASE TRIM(vgm_score) WHEN 'A' THEN 1 WHEN 'B' THEN 2 WHEN 'C' THEN 3 WHEN 'D' THEN 4 WHEN 'F' THEN 5 END
FROM legacy.zacks_financials
WHERE TRIM(vgm_score) IN ('A','B','C','D','F') AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
	}

	var totalRows int64
	for _, q := range ratingQueries {
		result, err := tx.Exec(ctx, q.sql)
		if err != nil {
			return fmt.Errorf("copy zacks rating %s: %w", q.analyst, err)
		}
		totalRows += result.RowsAffected()
	}

	log.Info().Int64("Rows", totalRows).Msg("copied Zacks Rating data")
	return nil
}

func copyZacksMetrics(ctx context.Context, tx pgx.Tx, sub *library.Subscription) error {
	tbl := sub.DataTablesMap[data.MetricKey]
	if tbl == "" {
		return nil
	}

	result, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, market_cap, ev, pe, pb, ps, ev_ebit, ev_ebitda, pe_forward, peg, price_to_cash_flow, beta)
SELECT ticker, TRIM(composite_figi), event_date,
  COALESCE(market_cap_mil, 0) * 1000000,
  0,
  COALESCE(pe_trailing_12_months, 0),
  COALESCE(price_to_book, 0),
  COALESCE(price_to_sales, 0),
  0,
  0,
  COALESCE(pe_f1, 0),
  COALESCE(peg_ratio, 0),
  COALESCE(price_to_cash_flow, 0),
  COALESCE(beta, 0)
FROM legacy.zacks_financials
WHERE LENGTH(TRIM(composite_figi)) = 12`, tbl))
	if err != nil {
		return fmt.Errorf("copy zacks metrics: %w", err)
	}

	log.Info().Int64("Rows", result.RowsAffected()).Msg("copied Zacks Metric data")
	return nil
}

func copyZacksEstimates(ctx context.Context, tx pgx.Tx, sub *library.Subscription) error {
	tbl := sub.DataTablesMap[data.EstimateKey]
	if tbl == "" {
		return nil
	}

	estimateQueries := []struct {
		series string
		sql    string
	}{
		{"eps-q0", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-q0',
  COALESCE(q0_consensus_est_last_completed_fiscal_qtr, 0), COALESCE(number_of_analysts_in_q0_consensus, 0), 0
FROM legacy.zacks_financials
WHERE (q0_consensus_est_last_completed_fiscal_qtr IS NOT NULL OR number_of_analysts_in_q0_consensus IS NOT NULL) AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
		{"eps-q1", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-q1',
  COALESCE(q1_consensus_est, 0), COALESCE(number_of_analysts_in_q1_consensus, 0), COALESCE(stdev_q1_q1_consensus_ratio, 0)
FROM legacy.zacks_financials
WHERE (q1_consensus_est IS NOT NULL OR number_of_analysts_in_q1_consensus IS NOT NULL) AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
		{"eps-q2", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-q2',
  COALESCE(q2_consensus_est_next_fiscal_qtr, 0), COALESCE(number_of_analysts_in_q2_consensus, 0), COALESCE(stdev_q2_q2_consensus_ratio, 0)
FROM legacy.zacks_financials
WHERE (q2_consensus_est_next_fiscal_qtr IS NOT NULL OR number_of_analysts_in_q2_consensus IS NOT NULL) AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
		{"eps-f0", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-f0',
  COALESCE(f0_consensus_est, 0), COALESCE(number_of_analysts_in_f0_consensus, 0)::int, 0
FROM legacy.zacks_financials
WHERE (f0_consensus_est IS NOT NULL OR number_of_analysts_in_f0_consensus IS NOT NULL) AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
		{"eps-f1", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-f1',
  COALESCE(f1_consensus_est, 0), COALESCE(number_of_analysts_in_f1_consensus, 0), COALESCE(stdev_f1_f1_consensus_ratio, 0)
FROM legacy.zacks_financials
WHERE (f1_consensus_est IS NOT NULL OR number_of_analysts_in_f1_consensus IS NOT NULL) AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
		{"eps-f2", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-f2',
  COALESCE(f2_consensus_est, 0), COALESCE(number_of_analysts_in_f2_consensus, 0), 0
FROM legacy.zacks_financials
WHERE (f2_consensus_est IS NOT NULL OR number_of_analysts_in_f2_consensus IS NOT NULL) AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
		{"sales-q1", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'sales-q1',
  COALESCE(q1_consensus_sales_est_mil, 0), 0, 0
FROM legacy.zacks_financials
WHERE q1_consensus_sales_est_mil IS NOT NULL AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
		{"sales-f1", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'sales-f1',
  COALESCE(f1_consensus_sales_est_mil, 0), 0, 0
FROM legacy.zacks_financials
WHERE f1_consensus_sales_est_mil IS NOT NULL AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
		{"lt-growth", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'lt-growth',
  COALESCE(long_term_growth_consensus_est, 0), 0, 0
FROM legacy.zacks_financials
WHERE long_term_growth_consensus_est IS NOT NULL AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
		{"earnings-esp", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'earnings-esp',
  COALESCE(earnings_esp, 0), 0, 0
FROM legacy.zacks_financials
WHERE earnings_esp IS NOT NULL AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
		{"eps-surprise-last", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-surprise-last',
  COALESCE(last_eps_surprise_percent, 0), 0, 0
FROM legacy.zacks_financials
WHERE last_eps_surprise_percent IS NOT NULL AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
		{"eps-surprise-prev", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-surprise-prev',
  COALESCE(previous_eps_surprise_percent, 0), 0, 0
FROM legacy.zacks_financials
WHERE previous_eps_surprise_percent IS NOT NULL AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
		{"eps-surprise-avg-4q", fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, series, value, num_analysts, std_dev)
SELECT ticker, TRIM(composite_figi), event_date, 'eps-surprise-avg-4q',
  COALESCE(avg_eps_surprise_last_4_qtrs, 0), 0, 0
FROM legacy.zacks_financials
WHERE avg_eps_surprise_last_4_qtrs IS NOT NULL AND LENGTH(TRIM(composite_figi)) = 12`, tbl)},
	}

	var totalRows int64
	for _, q := range estimateQueries {
		result, err := tx.Exec(ctx, q.sql)
		if err != nil {
			return fmt.Errorf("copy zacks estimate %s: %w", q.series, err)
		}
		totalRows += result.RowsAffected()
	}

	log.Info().Int64("Rows", totalRows).Msg("copied Zacks Estimate data")
	return nil
}

func copyZacksConsensus(ctx context.Context, tx pgx.Tx, sub *library.Subscription) error {
	tbl := sub.DataTablesMap[data.ConsensusKey]
	if tbl == "" {
		return nil
	}

	result, err := tx.Exec(ctx, fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, avg_recommendation, num_analysts, num_strong_buy_or_buy, num_hold, num_sell_or_strong_sell, num_upgrades, num_downgrades, avg_target_price)
SELECT ticker, TRIM(composite_figi), event_date,
  current_avg_broker_rec, num_brokers_in_rating,
  num_rating_strong_buy_or_buy, num_rating_hold, num_rating_strong_sell_or_sell,
  number_rating_upgrades, number_rating_downgrades, average_target_price
FROM legacy.zacks_financials
WHERE (current_avg_broker_rec IS NOT NULL OR num_brokers_in_rating IS NOT NULL) AND LENGTH(TRIM(composite_figi)) = 12`, tbl))
	if err != nil {
		return fmt.Errorf("copy zacks consensus: %w", err)
	}

	log.Info().Int64("Rows", result.RowsAffected()).Msg("copied Zacks Consensus data")
	return nil
}
```

- [ ] **Step 2: Implement `updateSubscriptionMetadata`**

```go
func updateSubscriptionMetadata(ctx context.Context, tx pgx.Tx, subs *legacySubscriptions) error {
	log.Info().Msg("updating subscription metadata...")

	allSubs := []*library.Subscription{subs.eod, subs.assets, subs.marketHolidays, subs.zacks}

	for _, sub := range allSubs {
		for _, tbl := range sub.DataTables {
			// Get record count and date range
			var count int64
			err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, tbl)).Scan(&count)
			if err != nil {
				log.Warn().Err(err).Str("Table", tbl).Msg("could not count records")
				continue
			}
			sub.TotalRecords += count
		}

		// Get date range from the first table that has event_date
		for _, tbl := range sub.DataTables {
			var hasEventDate bool
			err := tx.QueryRow(ctx,
				`SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name = $1 AND column_name = 'event_date')`,
				tbl).Scan(&hasEventDate)
			if err != nil || !hasEventDate {
				continue
			}

			var minDate, maxDate time.Time
			err = tx.QueryRow(ctx,
				fmt.Sprintf(`SELECT COALESCE(MIN(event_date), '0001-01-01'), COALESCE(MAX(event_date), '0001-01-01') FROM %s`, tbl)).
				Scan(&minDate, &maxDate)
			if err != nil {
				continue
			}

			if sub.FirstObsDate.IsZero() || minDate.Before(sub.FirstObsDate) {
				sub.FirstObsDate = minDate
			}
			if maxDate.After(sub.LastObsDate) {
				sub.LastObsDate = maxDate
			}
		}

		_, err := tx.Exec(ctx,
			`UPDATE subscriptions SET total_records = $1, first_obs_date = $2, last_obs_date = $3, active = false, schedule = '' WHERE id = $4`,
			sub.TotalRecords, sub.FirstObsDate, sub.LastObsDate, sub.ID)
		if err != nil {
			return fmt.Errorf("update metadata for %s: %w", sub.Name, err)
		}

		log.Info().Str("Name", sub.Name).Int64("Records", sub.TotalRecords).Msg("updated subscription metadata")
	}

	return nil
}
```

- [ ] **Step 3: Verify build**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...
```

Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add cmd/migrate_legacy.go
git commit -m "feat: implement migrate-legacy data copy and transformation

Moves legacy tables to legacy schema, creates subscriptions via
SaveWithTx, copies EOD/assets/market_holidays/zacks data with
appropriate transforms, and updates subscription metadata.
All within a single transaction."
```

---

## Chunk 4: Testing

### Task 7: Add tests for the migration command

**Files:**
- Create: `cmd/migrate_legacy_test.go`

Since the migration is heavily database-dependent, the most valuable tests verify the SQL generation logic and the provider wiring. Full integration testing requires a real PostgreSQL database with legacy schema, which is best done manually against the actual legacy database.

- [ ] **Step 1: Write tests**

```go
package cmd

import (
	"testing"

	"github.com/penny-vault/pvdata/data"
)

func TestLegacyTablesListIsComplete(t *testing.T) {
	expected := map[string]bool{
		"activity": true, "announcements": true, "assets": true,
		"eod": true, "market_holidays": true, "portfolio_measurements": true,
		"portfolio_transactions": true, "portfolios": true, "profile": true,
		"reported_financials": true, "schema_migrations": true,
		"seeking_alpha": true, "trading_days": true,
		"zacks_financials": true, "zacks_number_1": true,
	}

	if len(legacyTables) != len(expected) {
		t.Errorf("expected %d legacy tables, got %d", len(expected), len(legacyTables))
	}

	for _, tbl := range legacyTables {
		if !expected[tbl] {
			t.Errorf("unexpected table in legacyTables: %s", tbl)
		}
	}
}

func TestRequiredLegacyTablesAreSubsetOfLegacyTables(t *testing.T) {
	legacySet := make(map[string]bool)
	for _, tbl := range legacyTables {
		legacySet[tbl] = true
	}

	for _, tbl := range requiredLegacyTables {
		if !legacySet[tbl] {
			t.Errorf("required table %q is not in legacyTables", tbl)
		}
	}
}

func TestZacksDataTypesMatchProvider(t *testing.T) {
	// Verify the data types we use in createLegacySubscriptions match
	// what exists in the DataTypes registry
	zacksTypes := []string{data.RatingKey, data.MetricKey, data.EstimateKey, data.ConsensusKey}
	for _, dt := range zacksTypes {
		if data.DataTypes[dt] == nil {
			t.Errorf("data type %q not found in DataTypes registry", dt)
		}
	}
}
```

- [ ] **Step 2: Run tests**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && go test ./cmd/ -run TestLegacy -v
```

Expected: PASS

- [ ] **Step 3: Commit**

```bash
git add cmd/migrate_legacy_test.go
git commit -m "test: add unit tests for migrate-legacy command"
```

---

### Task 8: Final build and lint check

- [ ] **Step 1: Run full build**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...
```

- [ ] **Step 2: Run all tests**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && go test ./...
```

- [ ] **Step 3: Run linter**

```bash
cd /Users/jdf/Developer/penny-vault/pv-data && golangci-lint run
```

Fix any issues that arise.

- [ ] **Step 4: Final commit if any fixes were needed**

```bash
git add -A
git commit -m "fix: address lint and build issues"
```
