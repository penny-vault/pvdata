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
	"strings"

	"github.com/golang-migrate/migrate/v4"
	"github.com/penny-vault/pvdata/db"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var migrateCmd = &cobra.Command{
	Use:   "migrate",
	Short: "Run pending database migrations",
	Run: func(cmd *cobra.Command, args []string) {
		dbURL := viper.GetString("db.url")
		if dbURL == "" {
			log.Fatal().Msg("db.url not configured")
		}

		migrateURL := strings.ReplaceAll(dbURL, "postgres://", "pgx5://")

		current, err := db.CurrentVersion(migrateURL)
		if err != nil {
			log.Fatal().Err(err).Msg("could not read current migration version")
		}

		latest, err := db.LatestVersion()
		if err != nil {
			log.Fatal().Err(err).Msg("could not determine latest migration version")
		}

		if current < latest {
			log.Info().Uint("from", current).Uint("to", latest).Msg("applying migrations")
		}

		version, err := db.Migrate(migrateURL)
		if err != nil {
			if errors.Is(err, migrate.ErrNoChange) {
				log.Info().Uint("version", version).Msg("database is already up to date")
			} else {
				log.Fatal().Err(err).Msg("migration failed")
			}
		} else {
			log.Info().Uint("version", version).Msg("migrations applied successfully")
		}

		ctx := context.Background()

		myLibrary, err := library.NewFromDB(ctx, dbURL)
		if err != nil {
			log.Fatal().Err(err).Msg("could not connect to library")
		}
		defer myLibrary.Close()

		subs, err := myLibrary.Subscriptions(ctx)
		if err != nil {
			log.Fatal().Err(err).Msg("could not load subscriptions")
		}

		var migrated int

		for _, sub := range subs {
			before := sub.SchemaVersion

			if err := sub.RunMigrations(ctx); err != nil {
				log.Error().Err(err).Str("subscription", sub.Name).Msg("subscription migration failed")
				continue
			}

			if sub.SchemaVersion != before {
				log.Info().Str("subscription", sub.Name).Int("from", before).Int("to", sub.SchemaVersion).Msg("migrated subscription tables")

				migrated++
			}
		}

		log.Info().Int("migrated", migrated).Int("checked", len(subs)).Msg("subscription migrations complete")
	},
}

func init() {
	rootCmd.AddCommand(migrateCmd)
}
