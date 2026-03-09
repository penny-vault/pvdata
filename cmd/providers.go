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
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

// providersCmd represents the providers command
var providersCmd = &cobra.Command{
	Use:   "providers <name>",
	Short: "List all providers available or get details about a specific provider",
	Run: func(cmd *cobra.Command, args []string) {
		r, _ := glamour.NewTermRenderer(
			// detect background color and pick either the default dark or light theme
			glamour.WithAutoStyle(),
			// wrap output at specific width (default is 80)
			glamour.WithWordWrap(80),
		)

		builder := strings.Builder{}

		if len(args) > 0 {
			if provider, ok := provider.Map[args[0]]; ok {
				fmt.Fprintf(&builder, "# %s\n", provider.Name())
				builder.WriteString(provider.Description())
				builder.WriteString("\n\n## Datasets\n")

				for _, dataset := range provider.Datasets() {
					start, end := dataset.DateRange()
					fmt.Fprintf(&builder, "- %s (%s to %s): %s\n", dataset.Name, start.Format("2006-01-02"), end.Format("2006-01-02"), dataset.Description)
				}
			}
		} else {
			builder.WriteString("# Available Providers\n")

			for _, provider := range provider.Map {
				fmt.Fprintf(&builder, "\n## %s\n", provider.Name())
				builder.WriteString(provider.Description())
			}
		}

		out, err := r.Render(builder.String())
		if err != nil {
			log.Fatal().Err(err).Msg("could not render provider document")
		}

		fmt.Print(out)
	},
}

func init() {
	rootCmd.AddCommand(providersCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// providersCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// providersCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
