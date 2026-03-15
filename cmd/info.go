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

	"charm.land/glamour/v2"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// infoCmd represents the info command
var infoCmd = &cobra.Command{
	Use:   "info",
	Short: "Display information about the data library",
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not load library info")
		}

		summary, err := myLibrary.Summary(ctx)
		if err != nil {
			log.Fatal().Err(err).Msg("could not create library summary document")
		}

		r, _ := glamour.NewTermRenderer(
			// use GLAMOUR_STYLE env var, defaulting to dark theme
			glamour.WithEnvironmentConfig(),
			// wrap output at specific width (default is 80)
			glamour.WithWordWrap(80),
		)

		out, err := r.Render(summary)
		if err != nil {
			log.Fatal().Err(err).Msg("could not render summary document")
		}

		fmt.Print(out)
	},
}

func init() {
	rootCmd.AddCommand(infoCmd)
}
