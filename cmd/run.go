/*
Copyright 2024
*/
package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/penny-vault/pvdata/data"
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

		// load the library
		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not connect to library")
		}

		daemon := viper.GetBool("daemon")

		if daemon {
			// Daemon mode: schedule subscriptions on their cron schedules, no TUI
			consoleWriter := zerolog.ConsoleWriter{Out: os.Stderr}
			log.Logger = zerolog.New(consoleWriter).With().Timestamp().Logger()

			runDaemon(ctx, myLibrary, args)
		} else {
			// TUI mode
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

			consoleWriter := zerolog.ConsoleWriter{Out: logWriter}
			log.Logger = zerolog.New(consoleWriter).With().Timestamp().Logger()

			result, err := tui.RunPreflight(ctx, myLibrary, args)
			if err != nil {
				log.Fatal().Err(err).Msg("pre-flight validation failed")
			}

			runManager := tui.NewRunManager(myLibrary, result.Subscriptions)

			if err := tui.Run(ctx, myLibrary, runManager, logWriter); err != nil {
				fmt.Fprintf(os.Stderr, "Error: %v\n", err)
				os.Exit(1)
			}
		}
	},
}

func init() {
	runCmd.Flags().StringP("lookback", "l", "", "Override data lookback period (e.g. 14d, 4w, 6m, 1y)")
	runCmd.Flags().BoolP("daemon", "d", false, "Run without TUI, logging to stderr")
	viper.BindPFlag("lookback", runCmd.Flags().Lookup("lookback"))
	viper.BindPFlag("daemon", runCmd.Flags().Lookup("daemon"))
	rootCmd.AddCommand(runCmd)
}

func runDaemon(ctx context.Context, myLibrary *library.Library, filterIDs []string) {
	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		log.Fatal().Err(err).Msg("could not load timezone")
	}

	scheduler, err := gocron.NewScheduler(gocron.WithLocation(nyc))
	if err != nil {
		log.Fatal().Err(err).Msg("could not create scheduler")
	}

	allSubs, err := myLibrary.Subscriptions(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("could not load subscriptions")
	}

	// Filter to requested subscription IDs, or use all active ones
	filterSet := make(map[string]bool, len(filterIDs))
	for _, id := range filterIDs {
		filterSet[id] = true
	}

	var subscriptions []*library.Subscription
	for _, sub := range allSubs {
		if !sub.Active {
			continue
		}
		if len(filterSet) > 0 && !filterSet[sub.ID.String()] {
			continue
		}
		subscriptions = append(subscriptions, sub)
	}

	if len(subscriptions) == 0 {
		log.Fatal().Msg("no active subscriptions to schedule")
	}

	// Schedule each subscription on its cron schedule
	for _, sub := range subscriptions {
		sub := sub // capture loop variable
		_, err := scheduler.NewJob(
			gocron.CronJob(sub.Schedule, false),
			gocron.NewTask(func() {
				runSubscription(ctx, myLibrary, sub)
			}),
		)
		if err != nil {
			log.Fatal().Err(err).Str("subscription", sub.Name).Str("schedule", sub.Schedule).Msg("could not schedule subscription")
		}
		log.Info().Str("subscription", sub.Name).Str("schedule", sub.Schedule).Msg("scheduled subscription")
	}

	scheduler.Start()
	log.Info().Int("count", len(subscriptions)).Msg("daemon started, waiting for scheduled runs")

	// Block until signal
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh

	log.Info().Msg("shutting down daemon")
	scheduler.Shutdown()
}

func runSubscription(ctx context.Context, myLibrary *library.Library, subscription *library.Subscription) {
	logger := log.With().Str("subscription", subscription.Name).Logger()
	logger.Info().Msg("starting subscription run")

	// Manage partitions and migrations
	if err := subscription.ManagePartitions(ctx); err != nil {
		logger.Error().Err(err).Msg("ManagePartitions failed")
	}
	if err := subscription.RunMigrations(ctx); err != nil {
		logger.Error().Err(err).Msg("RunMigrations failed")
	}

	subProvider, ok := provider.Map[subscription.Provider]
	if !ok {
		logger.Error().Str("provider", subscription.Provider).Msg("provider not found")
		return
	}

	subDataset, ok := subProvider.Datasets()[subscription.Dataset]
	if !ok {
		logger.Error().Str("dataset", subscription.Dataset).Msg("dataset not found")
		return
	}

	outChan := make(chan *data.Observation, 1000)
	exitChan := make(chan data.RunSummary, 1)

	var wg sync.WaitGroup
	wg.Add(1)
	go myLibrary.SaveObservations(outChan, &wg)

	fetchLogger := logger.With().Str("SubscriptionID", subscription.ID.String()).Logger()
	fetchCtx := fetchLogger.WithContext(ctx)

	subDataset.Fetch(fetchCtx, subscription, outChan, exitChan)

	summary := <-exitChan
	close(outChan)
	wg.Wait()

	// Run post-fetch hooks
	if summary.Status == data.RunSuccess && len(subDataset.PostFetch) > 0 {
		for _, hook := range subDataset.PostFetch {
			if err := hook(ctx, subscription); err != nil {
				logger.Error().Err(err).Msg("post-fetch hook failed")
				break
			}
		}
	}

	if summary.Status == data.RunFailed {
		logger.Error().Int("observations", summary.NumObservations).Msg("subscription run failed")
	} else {
		logger.Info().Int("observations", summary.NumObservations).Msg("subscription run completed")
	}
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
