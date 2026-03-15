package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var migrateLegacyCmd = &cobra.Command{
	Use:   "migrate-legacy",
	Short: "Migrate data from a legacy penny-vault database",
	Long: `Copies and transforms data from a legacy penny-vault database into the
current pv-data library. Requires two separate databases: the legacy source
and the pv-data destination (already initialized with 'pvdata init').

Tables migrated: eod, assets, market_holidays, zacks_financials
The legacy database is not modified.`,
	RunE: runMigrateLegacy,
}

func init() {
	rootCmd.AddCommand(migrateLegacyCmd)
	migrateLegacyCmd.Flags().String("source", "", "Connection URL for the legacy database (required)")
	migrateLegacyCmd.Flags().Bool("dry-run", false, "Print what would be done without making changes")
	migrateLegacyCmd.Flags().Bool("force", false, "Clean up failed prior run before migrating")

	if err := migrateLegacyCmd.MarkFlagRequired("source"); err != nil {
		log.Fatal().Err(err).Msg("could not mark source flag as required")
	}
}

// requiredLegacyTables are tables that must exist in the source for migration to proceed.
var requiredLegacyTables = []string{"eod", "assets"}

func runMigrateLegacy(cmd *cobra.Command, args []string) error {
	ctx := context.Background()
	dryRun, _ := cmd.Flags().GetBool("dry-run")
	force, _ := cmd.Flags().GetBool("force")
	sourceURL, _ := cmd.Flags().GetString("source")

	// Connect to destination (pv-data library)
	destURL := viper.GetString("db.url")
	if destURL == "" {
		return fmt.Errorf("db.url is not configured. Run 'pvdata init' first")
	}

	destPool, err := pgxpool.New(ctx, destURL)
	if err != nil {
		return fmt.Errorf("connect to destination database: %w", err)
	}
	defer destPool.Close()

	// Connect to source (legacy database)
	sourcePool, err := pgxpool.New(ctx, sourceURL)
	if err != nil {
		return fmt.Errorf("connect to source database: %w", err)
	}
	defer sourcePool.Close()

	myLibrary, err := library.NewFromDB(ctx, destURL)
	if err != nil {
		return fmt.Errorf("load library (has 'pvdata init' been run?): %w", err)
	}
	defer myLibrary.Close()

	// Force cleanup if requested
	if force {
		if err := cleanupLegacyMigration(ctx, destPool, myLibrary); err != nil {
			return fmt.Errorf("force cleanup: %w", err)
		}
	}

	// Preflight checks
	if err := preflightChecks(ctx, sourcePool, destPool); err != nil {
		return err
	}

	if dryRun {
		log.Info().Msg("[dry-run] Preflight checks passed. Would proceed with migration.")
		return nil
	}

	// Redirect zerolog to a file while bubbletea is running to avoid corrupting the TUI.
	logFile, err := os.CreateTemp("", "pvdata-migrate-*.log")
	if err != nil {
		return fmt.Errorf("create log file: %w", err)
	}
	defer logFile.Close()

	prevLogger := log.Logger
	log.Logger = zerolog.New(logFile).With().Timestamp().Logger()

	log.Info().Str("LogFile", logFile.Name()).Msg("migration logs redirected to file")

	progressCh := make(chan progressMsg, 100)

	var migrationErr error

	go func() {
		defer close(progressCh)

		migrationErr = executeMigration(ctx, sourcePool, destPool, myLibrary, progressCh)
	}()

	model := newMigrationModel(progressCh)
	p := tea.NewProgram(model)

	if _, err := p.Run(); err != nil {
		log.Logger = prevLogger

		log.Info().Str("LogFile", logFile.Name()).Msg("migration log file")

		return fmt.Errorf("TUI error: %w", err)
	}

	log.Logger = prevLogger

	log.Info().Str("LogFile", logFile.Name()).Msg("migration log file")

	return migrationErr
}

func preflightChecks(ctx context.Context, sourcePool, destPool *pgxpool.Pool) error {
	// Check source has required legacy tables
	sourceConn, err := sourcePool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire source connection: %w", err)
	}
	defer sourceConn.Release()

	for _, tbl := range requiredLegacyTables {
		var exists bool

		err = sourceConn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
			tbl).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check source table %s: %w", tbl, err)
		}

		if !exists {
			return fmt.Errorf("required table %q not found in source database", tbl)
		}
	}

	// Check destination has pv-data tables
	destConn, err := destPool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire destination connection: %w", err)
	}
	defer destConn.Release()

	for _, tbl := range []string{"subscriptions", "published_views"} {
		var exists bool

		err = destConn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
			tbl).Scan(&exists)
		if err != nil {
			return fmt.Errorf("check destination table %s: %w", tbl, err)
		}

		if !exists {
			return fmt.Errorf("pv-data table %q not found in destination. Run 'pvdata init' first", tbl)
		}
	}

	// Check no legacy subscriptions already exist in destination
	var legacySubCount int

	err = destConn.QueryRow(ctx,
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

func cleanupLegacyMigration(ctx context.Context, destPool *pgxpool.Pool, myLibrary *library.Library) error {
	log.Info().Msg("cleaning up previous legacy migration...")

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
					if err := library.DeletePublishedView(ctx, destPool, dt.ViewName); err != nil {
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

	log.Info().Msg("cleanup complete")

	return nil
}

func executeMigration(ctx context.Context, sourcePool, destPool *pgxpool.Pool, myLibrary *library.Library, progressCh chan<- progressMsg) error {
	// All writes go to destination in a transaction
	destConn, err := destPool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire destination connection: %w", err)
	}
	defer destConn.Release()

	tx, err := destConn.Begin(ctx)
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

	progressCh <- progressMsg{step: "Creating subscriptions"}

	// Validate composite_figi lengths in source
	if err := validateCompositeFigi(ctx, sourcePool); err != nil {
		return fmt.Errorf("validate composite_figi: %w", err)
	}

	// Create subscriptions in destination
	subs, err := createLegacySubscriptions(ctx, tx, myLibrary)
	if err != nil {
		return fmt.Errorf("create subscriptions: %w", err)
	}

	progressCh <- progressMsg{step: "Created subscriptions", done: true}

	// Copy and transform data from source to destination
	if err := copyData(ctx, sourcePool, tx, subs, progressCh); err != nil {
		return fmt.Errorf("copy data: %w", err)
	}

	// Update subscription metadata
	if err := updateSubscriptionMetadata(ctx, tx, subs); err != nil {
		return fmt.Errorf("update metadata: %w", err)
	}

	progressCh <- progressMsg{step: "Updated metadata", done: true}

	// Commit
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit migration: %w", err)
	}

	log.Info().Msg("legacy migration completed successfully")

	return nil
}

func validateCompositeFigi(ctx context.Context, sourcePool *pgxpool.Pool) error {
	sourceConn, err := sourcePool.Acquire(ctx)
	if err != nil {
		return err
	}
	defer sourceConn.Release()

	for _, tbl := range []string{"eod", "zacks_financials"} {
		var exists bool

		err := sourceConn.QueryRow(ctx,
			`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
			tbl).Scan(&exists)
		if err != nil {
			return err
		}

		if !exists {
			continue
		}

		var badCount int

		err = sourceConn.QueryRow(ctx,
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

// copyData reads from sourcePool and writes to tx (destination).
// For tables with event_date, it batches by year for progress reporting.
func copyData(ctx context.Context, sourcePool *pgxpool.Pool, tx pgx.Tx, subs *legacySubscriptions, progressCh chan<- progressMsg) error {
	log.Info().Msg("copying and transforming data...")

	sourceConn, err := sourcePool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("acquire source connection: %w", err)
	}
	defer sourceConn.Release()

	// EOD -- batch by year
	eodTable := subs.eod.DataTablesMap[data.EODKey]
	if eodTable != "" {
		if err := copyEodByYear(ctx, sourceConn, tx, eodTable, progressCh); err != nil {
			return err
		}
	}

	// Assets -- single copy
	assetsTable := subs.assets.DataTablesMap[data.AssetKey]
	if assetsTable != "" {
		if err := copyAssets(ctx, sourceConn, tx, assetsTable, progressCh); err != nil {
			return err
		}
	}

	// Market Holidays
	if exists, _ := sourceTableExists(ctx, sourceConn, "market_holidays"); exists {
		mhTable := subs.marketHolidays.DataTablesMap[data.MarketHolidaysKey]
		if mhTable != "" {
			if err := copyMarketHolidays(ctx, sourceConn, tx, mhTable, progressCh); err != nil {
				return err
			}
		}
	} else {
		log.Warn().Msg("market_holidays not found in source, skipping")
	}

	// Zacks
	if exists, _ := sourceTableExists(ctx, sourceConn, "zacks_financials"); exists {
		if err := copyZacksRatings(ctx, sourceConn, tx, subs.zacks, progressCh); err != nil {
			return err
		}

		if err := copyZacksMetrics(ctx, sourceConn, tx, subs.zacks, progressCh); err != nil {
			return err
		}

		if err := copyZacksEstimates(ctx, sourceConn, tx, subs.zacks, progressCh); err != nil {
			return err
		}

		if err := copyZacksConsensus(ctx, sourceConn, tx, subs.zacks, progressCh); err != nil {
			return err
		}
	} else {
		log.Warn().Msg("zacks_financials not found in source, skipping")
	}

	return nil
}

func sourceTableExists(ctx context.Context, conn *pgxpool.Conn, tableName string) (bool, error) {
	var exists bool

	err := conn.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM information_schema.tables WHERE table_schema = 'public' AND table_name = $1)`,
		tableName).Scan(&exists)

	return exists, err
}

// copyEodByYear reads EOD data from source year-by-year and inserts into destination.
func copyEodByYear(ctx context.Context, sourceConn *pgxpool.Conn, tx pgx.Tx, destTable string, progressCh chan<- progressMsg) error {
	var totalCount int64

	if err := sourceConn.QueryRow(ctx,
		`SELECT COUNT(*) FROM eod WHERE LENGTH(TRIM(composite_figi)) = 12`).Scan(&totalCount); err != nil {
		return fmt.Errorf("count eod: %w", err)
	}

	var minYear, maxYear int

	if err := sourceConn.QueryRow(ctx,
		`SELECT COALESCE(EXTRACT(YEAR FROM MIN(event_date))::int, 0), COALESCE(EXTRACT(YEAR FROM MAX(event_date))::int, 0) FROM eod`).Scan(&minYear, &maxYear); err != nil {
		return fmt.Errorf("eod date range: %w", err)
	}

	if totalCount == 0 {
		progressCh <- progressMsg{step: "EOD data", current: 0, done: true}

		return nil
	}

	var copiedRows int64

	for year := minYear; year <= maxYear; year++ {
		rows, err := sourceConn.Query(ctx,
			fmt.Sprintf(`SELECT ticker, TRIM(composite_figi), event_date, open, high, low, close, COALESCE(adj_close, close), volume, dividend, split_factor
FROM eod WHERE LENGTH(TRIM(composite_figi)) = 12 AND event_date >= '%d-01-01' AND event_date < '%d-01-01'`, year, year+1))
		if err != nil {
			return fmt.Errorf("read eod year %d: %w", year, err)
		}

		for rows.Next() {
			var (
				ticker, figi                                            string
				eventDate                                               time.Time
				open, high, low, close, adjClose, dividend, splitFactor float64
				volume                                                  int64
			)

			if err := rows.Scan(&ticker, &figi, &eventDate, &open, &high, &low, &close, &adjClose, &volume, &dividend, &splitFactor); err != nil {
				rows.Close()

				return fmt.Errorf("scan eod row: %w", err)
			}

			_, err := tx.Exec(ctx,
				fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, open, high, low, close, adj_close, volume, dividend, split_factor)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, destTable),
				ticker, figi, eventDate, open, high, low, close, adjClose, volume, dividend, splitFactor)
			if err != nil {
				rows.Close()

				return fmt.Errorf("insert eod row: %w", err)
			}

			copiedRows++
		}

		rows.Close()

		progressCh <- progressMsg{step: "EOD data", current: copiedRows, total: totalCount}
	}

	progressCh <- progressMsg{step: "EOD data", current: copiedRows, done: true}

	return nil
}

func copyAssets(ctx context.Context, sourceConn *pgxpool.Conn, tx pgx.Tx, destTable string, progressCh chan<- progressMsg) error {
	rows, err := sourceConn.Query(ctx,
		`SELECT ticker, composite_figi, share_class_figi, primary_exchange,
  asset_type, active, name, description, corporate_url, sector, industry, cik,
  cusip, isin, similar_tickers, tags, listed_utc, delisted_utc, last_updated_utc
FROM assets`)
	if err != nil {
		return fmt.Errorf("read assets: %w", err)
	}
	defer rows.Close()

	assetTypeMap := map[string]string{
		"Common Stock":                       "CS",
		"Preferred Stock":                    "PS",
		"Exchange Traded Fund":               "ETF",
		"Exchange Traded Note":               "ETN",
		"Mutual Fund":                        "MF",
		"Closed-End Fund":                    "CEF",
		"American Depository Receipt Common": "ADRC",
		"FRED":                               "FRED",
		"Synthetic History":                  "SYNTH",
	}

	var count int64

	for rows.Next() {
		var (
			ticker, figi                                                                                                    string
			shareClassFigi, primaryExchange, assetType, name, description, corporateUrl, sector, industry, cik, cusip, isin *string
			active                                                                                                          *bool
			similarTickers, tags                                                                                            []string
			listed, delisted, lastUpdated                                                                                   *time.Time
		)

		if err := rows.Scan(&ticker, &figi, &shareClassFigi, &primaryExchange,
			&assetType, &active, &name, &description, &corporateUrl, &sector, &industry, &cik,
			&cusip, &isin, &similarTickers, &tags, &listed, &delisted, &lastUpdated); err != nil {
			return fmt.Errorf("scan asset row: %w", err)
		}

		// Map asset type
		var mappedType *string

		if assetType != nil {
			if mapped, ok := assetTypeMap[*assetType]; ok {
				mappedType = &mapped
			}
		}

		// Convert cusip/isin to arrays
		var cusips, isins []string
		if cusip != nil {
			cusips = []string{strings.TrimSpace(*cusip)}
		}

		if isin != nil {
			isins = []string{strings.TrimSpace(*isin)}
		}

		_, err := tx.Exec(ctx,
			fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, share_class_figi, primary_exchange, asset_type, active, name, description, corporate_url, sector, industry, cik, cusips, isins, other_identifiers, similar_tickers, tags, listed, delisted, last_updated)
VALUES ($1, $2, $3, $4, $5::assettype, $6, $7, $8, $9, $10, $11, $12, $13, $14, '{}'::jsonb, $15, $16, $17, $18, $19)`, destTable),
			ticker, figi, shareClassFigi, primaryExchange, mappedType, active, name, description, corporateUrl, sector, industry, cik, cusips, isins, similarTickers, tags, listed, delisted, lastUpdated)
		if err != nil {
			return fmt.Errorf("insert asset row: %w", err)
		}

		count++
	}

	progressCh <- progressMsg{step: "Assets", current: count, done: true}

	return nil
}

func copyMarketHolidays(ctx context.Context, sourceConn *pgxpool.Conn, tx pgx.Tx, destTable string, progressCh chan<- progressMsg) error {
	rows, err := sourceConn.Query(ctx, `SELECT holiday, event_date, market, early_close, close_time FROM market_holidays`)
	if err != nil {
		return fmt.Errorf("read market_holidays: %w", err)
	}
	defer rows.Close()

	var count int64

	for rows.Next() {
		var (
			holiday, market string
			eventDate       time.Time
			earlyClose      bool
			closeTime       time.Time
		)

		if err := rows.Scan(&holiday, &eventDate, &market, &earlyClose, &closeTime); err != nil {
			return fmt.Errorf("scan market_holidays row: %w", err)
		}

		_, err := tx.Exec(ctx,
			fmt.Sprintf(`INSERT INTO %s (holiday, event_date, market, early_close, close_time) VALUES ($1, $2, $3, $4, $5)`, destTable),
			holiday, eventDate, market, earlyClose, closeTime)
		if err != nil {
			return fmt.Errorf("insert market_holidays row: %w", err)
		}

		count++
	}

	progressCh <- progressMsg{step: "Market holidays", current: count, done: true}

	return nil
}

func copyZacksRatings(ctx context.Context, sourceConn *pgxpool.Conn, tx pgx.Tx, sub *library.Subscription, progressCh chan<- progressMsg) error {
	tbl := sub.DataTablesMap[data.RatingKey]
	if tbl == "" {
		return nil
	}

	letterGradeToInt := map[string]int{
		"A": 1, "B": 2, "C": 3, "D": 4, "F": 5,
	}

	rows, err := sourceConn.Query(ctx,
		`SELECT ticker, TRIM(composite_figi), event_date, zacks_rank,
  TRIM(value_score), TRIM(growth_score), TRIM(momentum_score), TRIM(vgm_score)
FROM zacks_financials WHERE LENGTH(TRIM(composite_figi)) = 12`)
	if err != nil {
		return fmt.Errorf("read zacks ratings: %w", err)
	}
	defer rows.Close()

	var count int64

	for rows.Next() {
		var (
			ticker, figi                                     string
			eventDate                                        time.Time
			zacksRank                                        *int
			valueScore, growthScore, momentumScore, vgmScore *string
		)

		if err := rows.Scan(&ticker, &figi, &eventDate, &zacksRank,
			&valueScore, &growthScore, &momentumScore, &vgmScore); err != nil {
			return fmt.Errorf("scan zacks rating row: %w", err)
		}

		type ratingEntry struct {
			analyst string
			rating  int
		}

		var entries []ratingEntry

		if zacksRank != nil {
			entries = append(entries, ratingEntry{"zacks-rank", *zacksRank})
		}

		for _, pair := range []struct {
			analyst string
			score   *string
		}{
			{"zacks-value", valueScore},
			{"zacks-growth", growthScore},
			{"zacks-momentum", momentumScore},
			{"zacks-vgm", vgmScore},
		} {
			if pair.score != nil {
				if val, ok := letterGradeToInt[*pair.score]; ok {
					entries = append(entries, ratingEntry{pair.analyst, val})
				}
			}
		}

		for _, e := range entries {
			_, err := tx.Exec(ctx,
				fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, analyst, rating) VALUES ($1, $2, $3, $4, $5)`, tbl),
				ticker, figi, eventDate, e.analyst, e.rating)
			if err != nil {
				return fmt.Errorf("insert zacks rating: %w", err)
			}

			count++
		}
	}

	progressCh <- progressMsg{step: "Zacks ratings", current: count, done: true}

	return nil
}

func copyZacksMetrics(ctx context.Context, sourceConn *pgxpool.Conn, tx pgx.Tx, sub *library.Subscription, progressCh chan<- progressMsg) error {
	tbl := sub.DataTablesMap[data.MetricKey]
	if tbl == "" {
		return nil
	}

	var totalCount int64

	if err := sourceConn.QueryRow(ctx,
		`SELECT COUNT(*) FROM zacks_financials WHERE LENGTH(TRIM(composite_figi)) = 12`).Scan(&totalCount); err != nil {
		return fmt.Errorf("count zacks metrics: %w", err)
	}

	var minYear, maxYear int

	if err := sourceConn.QueryRow(ctx,
		`SELECT COALESCE(EXTRACT(YEAR FROM MIN(event_date))::int, 0), COALESCE(EXTRACT(YEAR FROM MAX(event_date))::int, 0) FROM zacks_financials`).Scan(&minYear, &maxYear); err != nil {
		return fmt.Errorf("zacks date range: %w", err)
	}

	if totalCount == 0 {
		progressCh <- progressMsg{step: "Zacks metrics", current: 0, done: true}

		return nil
	}

	var copiedRows int64

	for year := minYear; year <= maxYear; year++ {
		rows, err := sourceConn.Query(ctx,
			fmt.Sprintf(`SELECT ticker, TRIM(composite_figi), event_date,
  COALESCE(market_cap_mil, 0), COALESCE(pe_trailing_12_months, 0),
  COALESCE(price_to_book, 0), COALESCE(price_to_sales, 0),
  COALESCE(pe_f1, 0), COALESCE(peg_ratio, 0), COALESCE(price_to_cash_flow, 0), COALESCE(beta, 0)
FROM zacks_financials
WHERE LENGTH(TRIM(composite_figi)) = 12 AND event_date >= '%d-01-01' AND event_date < '%d-01-01'`, year, year+1))
		if err != nil {
			return fmt.Errorf("read zacks metrics year %d: %w", year, err)
		}

		for rows.Next() {
			var (
				ticker, figi                                                    string
				eventDate                                                       time.Time
				marketCapMil, pe, pb, ps, peForward, peg, priceToCashFlow, beta float64
			)

			if err := rows.Scan(&ticker, &figi, &eventDate,
				&marketCapMil, &pe, &pb, &ps, &peForward, &peg, &priceToCashFlow, &beta); err != nil {
				rows.Close()

				return fmt.Errorf("scan zacks metric row: %w", err)
			}

			_, err := tx.Exec(ctx,
				fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, market_cap, ev, pe, pb, ps, ev_ebit, ev_ebitda, pe_forward, peg, price_to_cash_flow, beta)
VALUES ($1, $2, $3, $4, 0, $5, $6, $7, 0, 0, $8, $9, $10, $11)`, tbl),
				ticker, figi, eventDate, int64(marketCapMil*1e6), pe, pb, ps, peForward, peg, priceToCashFlow, beta)
			if err != nil {
				rows.Close()

				return fmt.Errorf("insert zacks metric: %w", err)
			}

			copiedRows++
		}

		rows.Close()

		progressCh <- progressMsg{step: "Zacks metrics", current: copiedRows, total: totalCount}
	}

	progressCh <- progressMsg{step: "Zacks metrics", current: copiedRows, done: true}

	return nil
}

func copyZacksEstimates(ctx context.Context, sourceConn *pgxpool.Conn, tx pgx.Tx, sub *library.Subscription, progressCh chan<- progressMsg) error {
	tbl := sub.DataTablesMap[data.EstimateKey]
	if tbl == "" {
		return nil
	}

	rows, err := sourceConn.Query(ctx,
		`SELECT ticker, TRIM(composite_figi), event_date,
  q0_consensus_est_last_completed_fiscal_qtr, number_of_analysts_in_q0_consensus,
  q1_consensus_est, number_of_analysts_in_q1_consensus, stdev_q1_q1_consensus_ratio,
  q2_consensus_est_next_fiscal_qtr, number_of_analysts_in_q2_consensus, stdev_q2_q2_consensus_ratio,
  f0_consensus_est, number_of_analysts_in_f0_consensus,
  f1_consensus_est, number_of_analysts_in_f1_consensus, stdev_f1_f1_consensus_ratio,
  f2_consensus_est, number_of_analysts_in_f2_consensus,
  q1_consensus_sales_est_mil, f1_consensus_sales_est_mil,
  long_term_growth_consensus_est, earnings_esp,
  last_eps_surprise_percent, previous_eps_surprise_percent, avg_eps_surprise_last_4_qtrs
FROM zacks_financials WHERE LENGTH(TRIM(composite_figi)) = 12`)
	if err != nil {
		return fmt.Errorf("read zacks estimates: %w", err)
	}
	defer rows.Close()

	var count int64

	for rows.Next() {
		var (
			ticker, figi                                               string
			eventDate                                                  time.Time
			q0Est, q1Est, q2Est, f0Est, f1Est, f2Est                   *float32
			q0Analysts, q1Analysts, q2Analysts, f1Analysts, f2Analysts *int
			f0Analysts                                                 *float32
			q1Stdev, q2Stdev, f1Stdev                                  *float32
			salesQ1, salesF1, ltGrowth, earningsEsp                    *float32
			surpriseLast, surprisePrev, surpriseAvg4q                  *float32
		)

		if err := rows.Scan(&ticker, &figi, &eventDate,
			&q0Est, &q0Analysts,
			&q1Est, &q1Analysts, &q1Stdev,
			&q2Est, &q2Analysts, &q2Stdev,
			&f0Est, &f0Analysts,
			&f1Est, &f1Analysts, &f1Stdev,
			&f2Est, &f2Analysts,
			&salesQ1, &salesF1,
			&ltGrowth, &earningsEsp,
			&surpriseLast, &surprisePrev, &surpriseAvg4q); err != nil {
			return fmt.Errorf("scan zacks estimate row: %w", err)
		}

		type estimate struct {
			series      string
			value       float64
			numAnalysts int
			stdDev      float64
		}

		var estimates []estimate

		if q0Est != nil || q0Analysts != nil {
			estimates = append(estimates, estimate{"eps-q0", float64Val(q0Est), intVal(q0Analysts), 0})
		}

		if q1Est != nil || q1Analysts != nil {
			estimates = append(estimates, estimate{"eps-q1", float64Val(q1Est), intVal(q1Analysts), float64Val(q1Stdev)})
		}

		if q2Est != nil || q2Analysts != nil {
			estimates = append(estimates, estimate{"eps-q2", float64Val(q2Est), intVal(q2Analysts), float64Val(q2Stdev)})
		}

		if f0Est != nil || f0Analysts != nil {
			estimates = append(estimates, estimate{"eps-f0", float64Val(f0Est), int(float64Val(f0Analysts)), 0})
		}

		if f1Est != nil || f1Analysts != nil {
			estimates = append(estimates, estimate{"eps-f1", float64Val(f1Est), intVal(f1Analysts), float64Val(f1Stdev)})
		}

		if f2Est != nil || f2Analysts != nil {
			estimates = append(estimates, estimate{"eps-f2", float64Val(f2Est), intVal(f2Analysts), 0})
		}

		if salesQ1 != nil {
			estimates = append(estimates, estimate{"sales-q1", float64Val(salesQ1), 0, 0})
		}

		if salesF1 != nil {
			estimates = append(estimates, estimate{"sales-f1", float64Val(salesF1), 0, 0})
		}

		if ltGrowth != nil {
			estimates = append(estimates, estimate{"lt-growth", float64Val(ltGrowth), 0, 0})
		}

		if earningsEsp != nil {
			estimates = append(estimates, estimate{"earnings-esp", float64Val(earningsEsp), 0, 0})
		}

		if surpriseLast != nil {
			estimates = append(estimates, estimate{"eps-surprise-last", float64Val(surpriseLast), 0, 0})
		}

		if surprisePrev != nil {
			estimates = append(estimates, estimate{"eps-surprise-prev", float64Val(surprisePrev), 0, 0})
		}

		if surpriseAvg4q != nil {
			estimates = append(estimates, estimate{"eps-surprise-avg-4q", float64Val(surpriseAvg4q), 0, 0})
		}

		for _, e := range estimates {
			_, err := tx.Exec(ctx,
				fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, series, value, num_analysts, std_dev) VALUES ($1, $2, $3, $4, $5, $6, $7)`, tbl),
				ticker, figi, eventDate, e.series, e.value, e.numAnalysts, e.stdDev)
			if err != nil {
				return fmt.Errorf("insert estimate %s: %w", e.series, err)
			}

			count++
		}
	}

	progressCh <- progressMsg{step: "Zacks estimates", current: count, done: true}

	return nil
}

func copyZacksConsensus(ctx context.Context, sourceConn *pgxpool.Conn, tx pgx.Tx, sub *library.Subscription, progressCh chan<- progressMsg) error {
	tbl := sub.DataTablesMap[data.ConsensusKey]
	if tbl == "" {
		return nil
	}

	rows, err := sourceConn.Query(ctx,
		`SELECT ticker, TRIM(composite_figi), event_date,
  current_avg_broker_rec, num_brokers_in_rating,
  num_rating_strong_buy_or_buy, num_rating_hold, num_rating_strong_sell_or_sell,
  number_rating_upgrades, number_rating_downgrades, average_target_price
FROM zacks_financials
WHERE (current_avg_broker_rec IS NOT NULL OR num_brokers_in_rating IS NOT NULL) AND LENGTH(TRIM(composite_figi)) = 12`)
	if err != nil {
		return fmt.Errorf("read zacks consensus: %w", err)
	}
	defer rows.Close()

	var count int64

	for rows.Next() {
		var (
			ticker, figi                                                      string
			eventDate                                                         time.Time
			avgRec                                                            *float32
			numAnalysts, numBuy, numHold, numSell, numUpgrades, numDowngrades *int
			avgTargetPrice                                                    *float64
		)

		if err := rows.Scan(&ticker, &figi, &eventDate,
			&avgRec, &numAnalysts, &numBuy, &numHold, &numSell,
			&numUpgrades, &numDowngrades, &avgTargetPrice); err != nil {
			return fmt.Errorf("scan zacks consensus row: %w", err)
		}

		_, err := tx.Exec(ctx,
			fmt.Sprintf(`INSERT INTO %s (ticker, composite_figi, event_date, avg_recommendation, num_analysts, num_strong_buy_or_buy, num_hold, num_sell_or_strong_sell, num_upgrades, num_downgrades, avg_target_price)
VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, tbl),
			ticker, figi, eventDate, avgRec, numAnalysts, numBuy, numHold, numSell, numUpgrades, numDowngrades, avgTargetPrice)
		if err != nil {
			return fmt.Errorf("insert consensus: %w", err)
		}

		count++
	}

	progressCh <- progressMsg{step: "Zacks consensus", current: count, done: true}

	return nil
}

func updateSubscriptionMetadata(ctx context.Context, tx pgx.Tx, subs *legacySubscriptions) error {
	log.Info().Msg("updating subscription metadata...")

	allSubs := []*library.Subscription{subs.eod, subs.assets, subs.marketHolidays, subs.zacks}

	for _, sub := range allSubs {
		for _, tbl := range sub.DataTables {
			var count int64

			err := tx.QueryRow(ctx, fmt.Sprintf(`SELECT COUNT(*) FROM %s`, tbl)).Scan(&count)
			if err != nil {
				log.Warn().Err(err).Str("Table", tbl).Msg("could not count records")
				continue
			}

			sub.TotalRecords += count
		}

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

// Helper functions for nullable value conversion
func float64Val(v *float32) float64 {
	if v == nil {
		return 0
	}

	return float64(*v)
}

func intVal(v *int) int {
	if v == nil {
		return 0
	}

	return *v
}
