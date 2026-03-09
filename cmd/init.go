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
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/huh"
	"github.com/jackc/pgx/v5"
	"github.com/pelletier/go-toml/v2"
	"github.com/penny-vault/pvdata/db"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// initCmd represents the init command
var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Gather database configuration and setup schema",
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		myLibrary := &library.Library{}

		var openFigiAPIKey string

		form := huh.NewForm(
			// Gather details about the library and who owns it
			huh.NewGroup(
				huh.NewInput().
					Title("Give the library a name:").
					Value(&myLibrary.Name),

				huh.NewInput().
					Title("Who owns the library?").
					Value(&myLibrary.Owner),
			),

			// Get details about the database
			huh.NewGroup(
				huh.NewInput().
					Title("Provide the DSN for connecting to your PostgreSQL database (postgres://[user[:password]@][netloc][:port][/dbname][?param1=value1&...])").
					Value(&myLibrary.DBUrl).
					Validate(func(dsn string) error {
						_, err := pgx.ParseConfig(dsn)
						return err
					}),
			),

			// API keys for enrichment services
			huh.NewGroup(
				huh.NewInput().
					Title("OpenFIGI API key (optional, leave blank to skip):").
					Description("Used to enrich assets with FIGI identifiers. Get one at https://www.openfigi.com/api").
					Value(&openFigiAPIKey),
			),
		)

		err := form.Run()
		if err != nil {
			log.Fatal().Err(err).Msg("error gathering database settings")
		}

		log.Info().Msg("creating database tables")

		// run migration
		dbURL := strings.ReplaceAll(myLibrary.DBUrl, "postgres://", "pgx5://")

		err = db.Migrate(dbURL)
		if err != nil {
			log.Fatal().Err(err).Msg("error running database migration")
		}

		log.Info().Msg("database tables created")
		log.Info().Msg("Saving library name and owner to database")

		// save library name and owner to database
		if err := myLibrary.Connect(ctx); err != nil {
			log.Fatal().Err(err).Msg("could not connect to database")
		}
		defer myLibrary.Close()

		err = myLibrary.SaveDB(ctx)
		if err != nil {
			log.Fatal().Err(err).Msg("error saving library settings to database")
		}

		// save database settings to config file
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal().Err(err).Msg("could not determine user home directory")
		}

		configFN := filepath.Join(home, ".pvdata.toml")
		log.Info().Str("ConfigFile", configFN).Msg("Saving database connection info to config file")

		configMap := map[string]any{
			"name":  myLibrary.Name,
			"owner": myLibrary.Owner,
			"db": map[string]any{
				"url": myLibrary.DBUrl,
			},
		}

		if openFigiAPIKey != "" {
			configMap["openfigi"] = map[string]any{
				"apikey": openFigiAPIKey,
			}
		}

		configData, err := toml.Marshal(configMap)
		if err != nil {
			log.Fatal().Err(err).Msg("could not marshal configuration data")
		}

		err = os.WriteFile(configFN, configData, 0644)
		if err != nil {
			log.Fatal().Err(err).Str("FileName", configFN).Msg("could not save configuration to file")
		}

		log.Info().Msg("Your data library has been initialized")
	},
}

func init() {
	rootCmd.AddCommand(initCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// initCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// initCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
