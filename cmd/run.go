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

		// If --lookback is set, parse and inject it into the context for providers to use
		if lookbackStr := viper.GetString("lookback"); lookbackStr != "" {
			lookback, err := parseLookback(lookbackStr)
			if err != nil {
				log.Fatal().Err(err).Str("lookback", lookbackStr).Msg("invalid lookback value")
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

		if !isatty.IsTerminal(os.Stdout.Fd()) && !isatty.IsCygwinTerminal(os.Stdout.Fd()) {
			// No TTY available -- run without TUI, log to stderr
			consoleWriter := zerolog.ConsoleWriter{Out: os.Stderr}
			log.Logger = zerolog.New(consoleWriter).With().Timestamp().Logger()

			runManager.RunAll(ctx)
		} else {
			if err := tui.Run(ctx, myLibrary, runManager, logWriter); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	},
}

func init() {
	runCmd.Flags().StringP("lookback", "l", "", "Override data lookback period (e.g. 14d, 4w, 6m, 1y)")
	runCmd.Flags().String("ticker", "", "Filter run to a single security by ticker (e.g. AAPL)")
	runCmd.Flags().String("figi", "", "Filter run to a single security by composite FIGI (e.g. BBG000B9XRY4)")
	runCmd.Flags().String("companyfacts-zip", "", "Use a local companyfacts.zip instead of downloading from SEC")
	runCmd.Flags().String("filing-cutoff", "", "Exclude SEC filings filed after this date (YYYY-MM-DD format)")

	if err := viper.BindPFlag("lookback", runCmd.Flags().Lookup("lookback")); err != nil {
		log.Fatal().Err(err).Msg("could not bind lookback flag")
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
