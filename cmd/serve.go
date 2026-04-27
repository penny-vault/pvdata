// Copyright 2024
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package cmd

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/golang-migrate/migrate/v4"
	"github.com/penny-vault/pvdata/checks"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/db"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/penny-vault/pvdata/web"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the web server and subscription scheduler",
	Long: `Start the web server and schedule active subscriptions on their cron schedules.
The server runs until interrupted with Ctrl+C.`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		dbURL := viper.GetString("db.url")

		// Run any pending database migrations
		migrateURL := strings.ReplaceAll(dbURL, "postgres://", "pgx5://")
		if _, err := db.Migrate(migrateURL); err != nil && !errors.Is(err, migrate.ErrNoChange) {
			log.Fatal().Err(err).Msg("database migration failed")
		}

		myLibrary, err := library.NewFromDB(ctx, dbURL)
		if err != nil {
			log.Fatal().Err(err).Msg("could not connect to library")
		}
		defer myLibrary.Close()

		// Start the scheduler
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

		var scheduled int

		for _, sub := range allSubs {
			if !sub.Active {
				continue
			}

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

			scheduled++
		}

		scheduler.Start()
		log.Info().Int("count", scheduled).Msg("scheduler started")

		// Start the web server
		port := viper.GetString("web.port")
		if port == "" {
			port = "3000"
		}

		app := web.CreateFiberApp(myLibrary)

		go func() {
			log.Info().Str("port", port).Msg("starting web server")

			if err := app.Listen(":" + port); err != nil {
				log.Error().Err(err).Msg("web server failed")
			}
		}()

		// Block until signal
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh

		log.Info().Msg("shutting down")

		// Bounded web-server shutdown: if SSE clients refuse to close
		// within the timeout, abandon the wait so the process can exit.
		const webShutdownTimeout = 15 * time.Second
		if err := app.ShutdownWithTimeout(webShutdownTimeout); err != nil {
			log.Error().Err(err).Msg("error shutting down web server")
		}

		// Bounded scheduler shutdown: scheduler.Shutdown waits for any
		// in-flight subscription run, which can take tens of minutes.
		// Give it a short window, then move on.
		const schedulerShutdownTimeout = 30 * time.Second

		schedulerDone := make(chan error, 1)

		go func() {
			schedulerDone <- scheduler.Shutdown()
		}()

		select {
		case err := <-schedulerDone:
			if err != nil {
				log.Error().Err(err).Msg("error shutting down scheduler")
			}
		case <-time.After(schedulerShutdownTimeout):
			log.Warn().Dur("timeout", schedulerShutdownTimeout).Msg("scheduler did not shut down in time; abandoning in-flight runs")
		}
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}

func runSubscription(ctx context.Context, myLibrary *library.Library, subscription *library.Subscription) {
	logger := log.With().Str("subscription", subscription.Name).Logger()
	logger.Info().Msg("starting subscription run")

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

	go myLibrary.SaveObservations(outChan, &wg, checks.NewInlineValidator(checks.InlineChecks()))

	fetchLogger := logger.With().Str("SubscriptionID", subscription.ID.String()).Logger()
	fetchCtx := fetchLogger.WithContext(ctx)

	subDataset.Fetch(fetchCtx, subscription, outChan, exitChan)

	summary := <-exitChan

	close(outChan)
	wg.Wait()

	if err := myLibrary.SaveRunHistory(ctx, summary); err != nil {
		logger.Error().Err(err).Msg("failed to save run history")
	}

	// Log data quality summary for this run
	qualityConn, qErr := myLibrary.Pool.Acquire(ctx)
	if qErr == nil {
		var critCount, errCount, warnCount int

		_ = qualityConn.QueryRow(ctx,
			`SELECT
				coalesce(sum(case when severity='critical' then 1 else 0 end), 0),
				coalesce(sum(case when severity='error' then 1 else 0 end), 0),
				coalesce(sum(case when severity='warning' then 1 else 0 end), 0)
			FROM data_quality_issues
			WHERE subscription_id = $1 AND detected_at > $2`,
			subscription.ID, summary.StartTime).Scan(&critCount, &errCount, &warnCount)
		qualityConn.Release()

		if critCount+errCount+warnCount > 0 {
			logger.Warn().
				Int("critical", critCount).
				Int("errors", errCount).
				Int("warnings", warnCount).
				Msg("data quality issues detected (run `pvdata check` for details)")
		}
	}

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
