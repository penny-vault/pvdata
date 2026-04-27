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
	"fmt"
	"os"

	"github.com/google/uuid"
	"github.com/pelletier/go-toml/v2"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// exportedSubscription is the on-disk representation of a subscription's
// configuration. Per-subscription data tables are NOT exported — they are
// recreated from the dataType.Schema on import, and the actual observations
// will be re-fetched on the subscription's next scheduled run.
type exportedSubscription struct {
	ID            string            `toml:"id"`
	Name          string            `toml:"name"`
	Provider      string            `toml:"provider"`
	Dataset       string            `toml:"dataset"`
	Schedule      string            `toml:"schedule"`
	HealthCheckID string            `toml:"health_check_id,omitempty"`
	Active        bool              `toml:"active"`
	SchemaVersion int               `toml:"schema_version"`
	DataTypes     []string          `toml:"data_types"`
	Config        map[string]string `toml:"config"`
}

type subscriptionExportFile struct {
	Subscriptions []exportedSubscription `toml:"subscription"`
}

var subscriptionsCmd = &cobra.Command{
	Use:   "subscriptions",
	Short: "Manage subscriptions in bulk",
	Long: `Bulk operations on subscription configuration: export to a TOML
file and import from a TOML file. The data tables that subscriptions
write into are not exported — only the configuration rows.

Restore flow:

    pvdata subscriptions export subs.toml   # on old library
    pvdata init                             # on new library
    pvdata subscriptions import subs.toml   # on new library

After importing into a fresh library, no historical data is present —
each subscription's next scheduled run (or an explicit "pvdata run")
re-fetches from its provider.

The export file contains provider API keys and other secrets stored in
each subscription's config. Treat the file as sensitive.`,
}

var subscriptionsExportCmd = &cobra.Command{
	Use:   "export <file>",
	Short: "Export all subscriptions to a TOML file",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not connect to library")
		}

		subs, err := myLibrary.Subscriptions(ctx)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to load subscriptions")
		}

		out := subscriptionExportFile{
			Subscriptions: make([]exportedSubscription, 0, len(subs)),
		}

		for _, s := range subs {
			out.Subscriptions = append(out.Subscriptions, exportedSubscription{
				ID:            s.ID.String(),
				Name:          s.Name,
				Provider:      s.Provider,
				Dataset:       s.Dataset,
				Schedule:      s.Schedule,
				HealthCheckID: s.HealthCheckID,
				Active:        s.Active,
				SchemaVersion: s.SchemaVersion,
				DataTypes:     s.DataTypes,
				Config:        s.Config,
			})
		}

		f, err := os.Create(args[0])
		if err != nil {
			log.Fatal().Err(err).Str("file", args[0]).Msg("could not create output file")
		}
		defer f.Close()

		if err := toml.NewEncoder(f).Encode(out); err != nil {
			log.Fatal().Err(err).Msg("failed to encode subscriptions to TOML")
		}

		log.Info().Int("count", len(out.Subscriptions)).Str("file", args[0]).Msg("exported subscriptions")
	},
}

var subscriptionsImportCmd = &cobra.Command{
	Use:   "import <file>",
	Short: "Import subscriptions from a TOML file (creates rows and per-subscription tables)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not connect to library")
		}

		raw, err := os.ReadFile(args[0])
		if err != nil {
			log.Fatal().Err(err).Str("file", args[0]).Msg("could not read input file")
		}

		var imp subscriptionExportFile
		if err := toml.Unmarshal(raw, &imp); err != nil {
			log.Fatal().Err(err).Msg("could not parse subscription file")
		}

		var imported, failed int

		for _, e := range imp.Subscriptions {
			id, err := uuid.Parse(e.ID)
			if err != nil {
				log.Error().Err(err).Str("id", e.ID).Str("name", e.Name).Msg("invalid subscription id, skipping")

				failed++

				continue
			}

			sub := &library.Subscription{
				ID:            id,
				Name:          e.Name,
				Provider:      e.Provider,
				Dataset:       e.Dataset,
				Schedule:      e.Schedule,
				HealthCheckID: e.HealthCheckID,
				Active:        e.Active,
				SchemaVersion: e.SchemaVersion,
				DataTypes:     e.DataTypes,
				Config:        e.Config,
				Library:       myLibrary,
			}

			sub.ComputeTableNames()

			if err := sub.Save(ctx); err != nil {
				log.Error().Err(err).Str("name", sub.Name).Str("id", sub.ID.String()).Msg("failed to import subscription")

				failed++

				continue
			}

			imported++

			log.Info().Str("name", sub.Name).Str("id", sub.ID.String()).Msg("imported subscription")
		}

		log.Info().Int("imported", imported).Int("failed", failed).Int("total", len(imp.Subscriptions)).Msg("import complete")

		if failed > 0 {
			fmt.Fprintf(os.Stderr, "%d subscriptions failed to import; see log for details\n", failed)
			os.Exit(1)
		}
	},
}

func init() {
	subscriptionsCmd.AddCommand(subscriptionsExportCmd)
	subscriptionsCmd.AddCommand(subscriptionsImportCmd)
	rootCmd.AddCommand(subscriptionsCmd)
}
