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
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-co-op/gocron/v2"
	"github.com/golang-migrate/migrate/v4"
	"github.com/google/uuid"
	"github.com/penny-vault/pvdata/db"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/web"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

const runLogRetention = 30 * 24 * time.Hour

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

		if cleared, err := myLibrary.MarkAbandonedRunsFailed(ctx); err != nil {
			log.Warn().Err(err).Msg("could not clean up abandoned runs")
		} else if cleared > 0 {
			log.Info().Int64("count", cleared).Msg("marked abandoned runs as failed")
		}

		// Run registry shared between scheduled runs and the web SSE handlers
		// so the UI can attach to scheduled runs in flight.
		registry := web.NewRunRegistry()

		// Tee zerolog output through LogCapture so per-run logs are buffered
		// for SSE streaming and DB persistence in addition to the console.
		logCapture := web.NewLogCapture(registry, 0)
		log.Logger = log.Output(io.MultiWriter(zerolog.ConsoleWriter{Out: os.Stderr}, logCapture))

		// Start the scheduler
		nyc, err := time.LoadLocation("America/New_York")
		if err != nil {
			log.Fatal().Err(err).Msg("could not load timezone")
		}

		gocronScheduler, err := gocron.NewScheduler(gocron.WithLocation(nyc))
		if err != nil {
			log.Fatal().Err(err).Msg("could not create scheduler")
		}

		// The scheduler runner re-loads each subscription from the library at
		// fire time so cron edits, config changes, and active flips made via
		// the web UI are picked up without restarting the server.
		subScheduler := web.NewScheduler(gocronScheduler, func(subID uuid.UUID) {
			sub, err := myLibrary.SubscriptionFromID(ctx, subID.String())
			if err != nil {
				log.Error().Err(err).Str("subscription_id", subID.String()).Msg("scheduled run: could not load subscription")
				return
			}

			if !sub.Active {
				log.Info().Str("subscription", sub.Name).Msg("scheduled run skipped: subscription is inactive")
				return
			}

			runScheduled(ctx, myLibrary, registry, logCapture, sub)
		})

		allSubs, err := myLibrary.Subscriptions(ctx)
		if err != nil {
			log.Fatal().Err(err).Msg("could not load subscriptions")
		}

		var scheduled int

		for _, sub := range allSubs {
			if !sub.Active {
				continue
			}

			if err := subScheduler.Schedule(sub); err != nil {
				log.Fatal().Err(err).Str("subscription", sub.Name).Str("schedule", sub.Schedule).Msg("could not schedule subscription")
			}

			log.Info().Str("subscription", sub.Name).Str("schedule", sub.Schedule).Msg("scheduled subscription")

			scheduled++
		}

		// Daily sweep: clear log text on run_history rows older than 30 days.
		if _, err := gocronScheduler.NewJob(
			gocron.DailyJob(1, gocron.NewAtTimes(gocron.NewAtTime(3, 0, 0))),
			gocron.NewTask(func() {
				cleared, err := myLibrary.SweepRunLogs(ctx, runLogRetention)
				if err != nil {
					log.Warn().Err(err).Msg("run log sweep failed")
					return
				}

				if cleared > 0 {
					log.Info().Int64("cleared", cleared).Msg("swept expired run logs")
				}
			}),
		); err != nil {
			log.Warn().Err(err).Msg("could not schedule run-log sweep")
		}

		gocronScheduler.Start()
		log.Info().Int("count", scheduled).Msg("scheduler started")

		// Start the web server
		port := viper.GetString("web.port")
		if port == "" {
			port = "3000"
		}

		app := web.CreateFiberApp(myLibrary, registry, logCapture, subScheduler)

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
			schedulerDone <- gocronScheduler.Shutdown()
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

func runScheduled(ctx context.Context, myLibrary *library.Library, registry *web.RunRegistry, logCapture *web.LogCapture, subscription *library.Subscription) {
	subID := subscription.ID.String()

	run, ok := registry.TryReserve(subID)
	if !ok {
		log.Warn().Str("subscription", subscription.Name).Msg("scheduled run skipped: another run is already in progress")

		return
	}

	web.RunSubscription(ctx, myLibrary, subscription, web.RunOptions{
		Run:        run,
		Source:     web.RunSourceScheduled,
		LogCapture: logCapture,
	})
}
