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

	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/web"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var serveCmd = &cobra.Command{
	Use:   "serve",
	Short: "Start the web UI server",
	Long:  `Start the Fiber web server that serves the pvdata web UI and API.`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not connect to library")
		}
		defer myLibrary.Close()

		port := viper.GetString("web.port")
		if port == "" {
			port = "3000"
		}

		app := web.CreateFiberApp(myLibrary)

		log.Info().Str("port", port).Msg("starting web server")

		if err := app.Listen(":" + port); err != nil {
			log.Fatal().Err(err).Msg("web server failed")
		}
	},
}

func init() {
	rootCmd.AddCommand(serveCmd)
}
