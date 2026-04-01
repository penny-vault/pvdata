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
	"strings"

	"github.com/penny-vault/pvdata/db"
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

		if err := db.Migrate(migrateURL); err != nil {
			if err.Error() == "no change" {
				log.Info().Uint("version", db.RequiredVersion).Msg("database is already up to date")
				return
			}

			log.Fatal().Err(err).Msg("migration failed")
		}

		log.Info().Uint("version", db.RequiredVersion).Msg("migrations applied successfully")
	},
}

func init() {
	rootCmd.AddCommand(migrateCmd)
}
