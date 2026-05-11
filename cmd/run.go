/*
Copyright 2024
*/
package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/penny-vault/pvdata/tui"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// runCmd represents the run command
var runCmd = &cobra.Command{
	Use:   "run [subscription-id...]",
	Short: "Run data import subscriptions",
	Long: `The run sub-command executes subscriptions and saves the data they generate. If no
arguments are provided then run will present a subscription picker. If subscription IDs are
provided then each subscription will execute sequentially.`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		// --lookback and --start-date both scope a run by setting the
		// same provider lookback context value, so they're mutually
		// exclusive. --start-date is converted to the equivalent
		// lookback (now - parsed date), which after the providers'
		// per-day truncation yields the same start boundary.
		lookbackStr := viper.GetString("lookback")
		startDateStr := viper.GetString("start-date")

		switch {
		case lookbackStr != "" && startDateStr != "":
			fmt.Fprintf(os.Stderr, "Error: --lookback and --start-date are mutually exclusive\n")
			os.Exit(1)
		case lookbackStr != "":
			lookback, err := parseLookback(lookbackStr)
			if err != nil {
				log.Fatal().Err(err).Str("lookback", lookbackStr).Msg("invalid lookback value")
			}

			ctx = context.WithValue(ctx, provider.LookbackKey, lookback)
		case startDateStr != "":
			lookback, err := lookbackFromStartDate(startDateStr, time.Now().UTC())
			if err != nil {
				log.Fatal().Err(err).Str("start-date", startDateStr).Msg("invalid start-date value")
			}

			ctx = context.WithValue(ctx, provider.LookbackKey, lookback)
		}

		// Validate and inject ticker/FIGI filter
		tickerFilter := strings.ToUpper(strings.TrimSpace(viper.GetString("ticker")))
		figiFilter := strings.TrimSpace(viper.GetString("figi"))

		if tickerFilter != "" && figiFilter != "" {
			fmt.Fprintf(os.Stderr, "Error: --ticker and --figi are mutually exclusive\n")
			os.Exit(1)
		}

		if tickerFilter != "" {
			ctx = context.WithValue(ctx, provider.TickerFilterKey, tickerFilter)
			log.Info().Str("ticker", tickerFilter).Msg("filtering run to single security")
		}

		if figiFilter != "" {
			ctx = context.WithValue(ctx, provider.FigiFilterKey, figiFilter)
			log.Info().Str("figi", figiFilter).Msg("filtering run to single security")
		}

		if zipPath := viper.GetString("companyfacts-zip"); zipPath != "" {
			ctx = context.WithValue(ctx, provider.CompanyFactsZipKey, zipPath)
			log.Info().Str("path", zipPath).Msg("using local companyfacts.zip")
		}

		if cutoffStr := viper.GetString("filing-cutoff"); cutoffStr != "" {
			cutoff, err := time.Parse("2006-01-02", cutoffStr)
			if err != nil {
				log.Fatal().Err(err).Str("filing-cutoff", cutoffStr).Msg("invalid filing-cutoff date; use YYYY-MM-DD format")
			}

			ctx = context.WithValue(ctx, provider.FilingCutoffKey, cutoff)
			log.Info().Time("cutoff", cutoff).Msg("filtering filings to those filed on or before cutoff date")
		}

		// load the library
		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not connect to library")
		}

		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal().Err(err).Msg("could not determine home directory")
		}

		logFile := filepath.Join(home, ".pvdata.log")

		logWriter, err := tui.NewDualWriter(logFile)
		if err != nil {
			log.Fatal().Err(err).Str("LogFile", logFile).Msg("could not create log writer")
		}
		defer logWriter.Close()

		log.Logger = zerolog.New(logWriter).With().Timestamp().Logger()

		result, err := tui.RunPreflight(ctx, myLibrary, args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}

		runManager := tui.NewRunManager(myLibrary, result.Subscriptions)

		// The TUI is opt-in: the default mode is headless (logs streamed
		// to stderr) so `pvdata run` plays well with cron, scripts, and
		// CI. Pass --tui to get the interactive run dashboard.
		if !viper.GetBool("tui") {
			consoleWriter := zerolog.ConsoleWriter{Out: os.Stderr}
			log.Logger = zerolog.New(consoleWriter).With().Timestamp().Logger()

			runManager.RunAll(ctx)
		} else {
			if !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd()) {
				log.Fatal().Msg("--tui requires a terminal; rerun without the flag for headless mode")
			}

			if err := tui.Run(ctx, myLibrary, runManager, logWriter); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	},
}

func init() {
	runCmd.Flags().StringP("lookback", "l", "", "Override data lookback period (e.g. 14d, 4w, 6m, 1y)")
	runCmd.Flags().String("start-date", "", "Start data fetch from this date (YYYY-MM-DD); mutually exclusive with --lookback")
	runCmd.Flags().String("ticker", "", "Filter run to a single security by ticker (e.g. AAPL)")
	runCmd.Flags().String("figi", "", "Filter run to a single security by composite FIGI (e.g. BBG000B9XRY4)")
	runCmd.Flags().String("companyfacts-zip", "", "Use a local companyfacts.zip instead of downloading from SEC")
	runCmd.Flags().String("filing-cutoff", "", "Exclude SEC filings filed after this date (YYYY-MM-DD format)")
	runCmd.Flags().Int("asset-workers", 0, "Worker count for the Massive asset discovery + details fan-out (0 = use default of 32)")
	runCmd.Flags().Bool("tui", false, "Show the interactive run dashboard (default: headless logging to stderr)")

	if err := viper.BindPFlag("tui", runCmd.Flags().Lookup("tui")); err != nil {
		log.Fatal().Err(err).Msg("could not bind tui flag")
	}

	if err := viper.BindPFlag("lookback", runCmd.Flags().Lookup("lookback")); err != nil {
		log.Fatal().Err(err).Msg("could not bind lookback flag")
	}

	if err := viper.BindPFlag("start-date", runCmd.Flags().Lookup("start-date")); err != nil {
		log.Fatal().Err(err).Msg("could not bind start-date flag")
	}

	if err := viper.BindPFlag("ticker", runCmd.Flags().Lookup("ticker")); err != nil {
		log.Fatal().Err(err).Msg("could not bind ticker flag")
	}

	if err := viper.BindPFlag("figi", runCmd.Flags().Lookup("figi")); err != nil {
		log.Fatal().Err(err).Msg("could not bind figi flag")
	}

	if err := viper.BindPFlag("companyfacts-zip", runCmd.Flags().Lookup("companyfacts-zip")); err != nil {
		log.Fatal().Err(err).Msg("could not bind companyfacts-zip flag")
	}

	if err := viper.BindPFlag("filing-cutoff", runCmd.Flags().Lookup("filing-cutoff")); err != nil {
		log.Fatal().Err(err).Msg("could not bind filing-cutoff flag")
	}

	if err := viper.BindPFlag("massive.asset_walk_workers", runCmd.Flags().Lookup("asset-workers")); err != nil {
		log.Fatal().Err(err).Msg("could not bind asset-workers flag")
	}

	rootCmd.AddCommand(runCmd)
}

// parseLookback parses a human-friendly duration string with suffixes:
// d (days), w (weeks), m (months), y (years). A bare number is treated as days.
func parseLookback(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty lookback value")
	}

	// Separate the numeric part from the suffix
	suffix := s[len(s)-1:]
	numStr := s[:len(s)-1]

	// If the last character is a digit, treat the whole string as days
	if suffix[0] >= '0' && suffix[0] <= '9' {
		numStr = s
		suffix = "d"
	}

	n, err := strconv.Atoi(numStr)
	if err != nil {
		return 0, fmt.Errorf("invalid number in lookback %q: %w", s, err)
	}

	if n <= 0 {
		return 0, fmt.Errorf("lookback must be positive, got %d", n)
	}

	switch suffix {
	case "d":
		return time.Duration(n) * 24 * time.Hour, nil
	case "w":
		return time.Duration(n) * 7 * 24 * time.Hour, nil
	case "m":
		return time.Duration(n) * 30 * 24 * time.Hour, nil
	case "y":
		return time.Duration(n) * 365 * 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("unknown lookback suffix %q; use d (days), w (weeks), m (months), or y (years)", suffix)
	}
}

// lookbackFromStartDate converts an absolute YYYY-MM-DD into the
// equivalent provider lookback (now - parsed). Providers truncate the
// derived start to a day boundary so sub-day drift between this
// computation and each provider's time.Now() does not affect the
// resulting fetch window.
func lookbackFromStartDate(s string, now time.Time) (time.Duration, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty start-date value")
	}

	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return 0, fmt.Errorf("invalid start-date %q: use YYYY-MM-DD format", s)
	}

	if !t.Before(now) {
		return 0, fmt.Errorf("start-date %s is not before now (%s)", s, now.Format("2006-01-02"))
	}

	return now.Sub(t), nil
}
