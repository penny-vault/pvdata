package cmd

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
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
			Library:   myLibrary,
			// active defaults to true in DB; updateSubscriptionMetadata sets it to false
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

	// Market Holidays (optional -- table may not exist in legacy DB)
	if exists, _ := legacyTableExists(ctx, tx, "market_holidays"); exists {
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
	} else {
		log.Warn().Msg("legacy.market_holidays not found, skipping")
	}

	// Zacks (optional -- table may not exist in legacy DB)
	if exists, _ := legacyTableExists(ctx, tx, "zacks_financials"); exists {
		if err := copyZacksRatings(ctx, tx, subs.zacks); err != nil {
			return err
		}

		if err := copyZacksMetrics(ctx, tx, subs.zacks); err != nil {
			return err
		}

		if err := copyZacksEstimates(ctx, tx, subs.zacks); err != nil {
			return err
		}

		if err := copyZacksConsensus(ctx, tx, subs.zacks); err != nil {
			return err
		}
	} else {
		log.Warn().Msg("legacy.zacks_financials not found, skipping")
	}

	return nil
}

func legacyTableExists(ctx context.Context, tx pgx.Tx, tableName string) (bool, error) {
	var exists bool

	err := tx.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'legacy' AND table_name = $1)`,
		tableName).Scan(&exists)

	return exists, err
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
		// eps-f0: number_of_analysts_in_f0_consensus is REAL in legacy, cast to int; std_dev defaults to 0
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
