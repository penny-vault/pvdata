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
	"math/rand"
	"os"
	"strings"
	"unicode"

	"charm.land/huh/v2"
	"charm.land/lipgloss/v2"
	"github.com/gosimple/slug"
	"github.com/penny-vault/pvdata/healthcheck"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// subscribeCmd represents the subscribe command
var subscribeCmd = &cobra.Command{
	Use:   "subscribe <data provider>",
	Short: "Create a new subscription",
	Long: `Subscriptions are the primary mechanism pv-data uses to import
data. To create a new subscription select the data provider desired and
the wizard will walk you through the rest of the process to setup a new
subscription.

When creating a subscription a couple of things happen:

    1. Configuration, like authentication details, are saved in the library
    2. Database tables are initialized
    3. A regular import schedule is defined

Also see: subscriptions, unsubscribe`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		var (
			dataProvider provider.Provider
			ok           bool
			confirmed    bool
			monitored    bool

			subName     string
			subDataset  string
			subSchedule string
		)

		ctx := context.Background()

		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not connect to library")
		}

		// check if data provider exists
		providerName := args[0]
		if dataProvider, ok = provider.Map[providerName]; !ok {
			fmt.Printf("Data Provider '%s' doesn't exist.\n", providerName)
			fmt.Printf("Run `pvdata providers` for a complete list of available providers")
			os.Exit(1)
		}

		r := []rune(providerName)
		subName = string(append([]rune{unicode.ToUpper(r[0])}, r[1:]...))

		minuteChoice := rand.Intn(12) * 5
		hourChoice := rand.Intn(9)
		subSchedule = fmt.Sprintf("%d %d * * 1-5", minuteChoice, hourChoice)

		// build a dataset selection field
		datasetOptions := make([]huh.Option[string], 0, len(dataProvider.Datasets()))
		for k, v := range dataProvider.Datasets() {
			datasetOptions = append(datasetOptions, huh.NewOption(v.Name, k))
		}

		// create a new field group for configuring the provider
		configFields := make([]huh.Field, 0, len(dataProvider.ConfigDescription()))

		config := make(map[string]*string, len(dataProvider.ConfigDescription()))
		for k, v := range dataProvider.ConfigDescription() {
			val := ""
			config[k] = &val
			configFields = append(configFields, huh.NewInput().Title(v).Value(config[k]))
		}

		// walk user through settings required for subscription
		var form *huh.Form

		if len(configFields) > 0 {
			form = huh.NewForm(
				huh.NewGroup(
					huh.NewInput().
						Title("What should the subscription be named?").
						Value(&subName),
					huh.NewSelect[string]().
						Title("Which dataset do you want to subscribe to?").
						Options(datasetOptions...).
						Value(&subDataset),
					huh.NewInput().
						Title("What schedule should the subscription run on?").
						Value(&subSchedule),
					huh.NewConfirm().
						Title("Should a healthcheck.io monitor be created for the subscription?").
						Value(&monitored),
				),
				huh.NewGroup(configFields...),
			)
		} else {
			form = huh.NewForm(
				huh.NewGroup(
					huh.NewInput().
						Title("What should the subscription be named?").
						Value(&subName),
					huh.NewSelect[string]().
						Title("Which dataset do you want to subscribe to?").
						Options(datasetOptions...).
						Value(&subDataset),
					huh.NewInput().
						Title("What schedule should the subscription run on?").
						Value(&subSchedule),
					huh.NewConfirm().
						Title("Should a healthcheck.io monitor be created for the subscription?").
						Value(&monitored),
				),
			)
		}

		err = form.Run()
		if err != nil {
			log.Fatal().Err(err).Msg("failed to create wizard")
		}

		datasetObj := dataProvider.Datasets()[subDataset]

		selectedDataTypes := make([]string, len(datasetObj.DataTypes))
		for i, dt := range datasetObj.DataTypes {
			selectedDataTypes[i] = dt.Name
		}

		// build configuration map
		subConfig := make(map[string]string, len(config))
		for k, v := range config {
			subConfig[k] = *v
		}

		// create a new subscription
		subscription, err := provider.NewSubscription(providerName, subDataset, subConfig, selectedDataTypes, myLibrary)
		if err != nil {
			log.Fatal().Err(err).Msg("unexpected error occurred when creating subscription")
		}

		subscription.Name = subName
		subscription.Schedule = subSchedule
		subscription.Dataset = subDataset

		// Print subscription summary
		{
			var sb strings.Builder

			keyword := func(s string) string {
				return lipgloss.NewStyle().Foreground(lipgloss.Color("212")).Render(s)
			}

			isMonitored := "no"
			if monitored {
				isMonitored = "yes"
			}

			fmt.Fprintf(&sb,
				"%s\n\nID: %s\nName: %s\nProvider: %s\nDataset: %s\nSchedule: %s\nMonitored: %s\n\n",
				lipgloss.NewStyle().Bold(true).Render("NEW SUBSCRIPTION"),
				keyword(subscription.ID.String()),
				keyword(subscription.Name),
				keyword(subscription.Provider),
				keyword(subscription.Dataset),
				keyword(subscription.Schedule),
				keyword(isMonitored),
			)

			fmt.Fprintln(&sb, lipgloss.NewStyle().Bold(true).Render("Provider Configuration"))

			for k, v := range subscription.Config {
				fmt.Fprintf(&sb, "\n%s: %s", k, keyword(v))
			}

			fmt.Println(
				lipgloss.NewStyle().
					Width(60).
					BorderStyle(lipgloss.RoundedBorder()).
					BorderForeground(lipgloss.Color("63")).
					Padding(1, 2).
					Render(sb.String()),
			)
		}

		confirmForm := huh.NewForm(
			huh.NewGroup(
				huh.NewConfirm().
					Title("Create subscription?").
					Value(&confirmed),
			),
		)

		err = confirmForm.Run()
		if err != nil {
			log.Fatal().Err(err).Msg("failed to create wizard")
		}

		if confirmed {
			if monitored {
				checkSlug := slug.Make(fmt.Sprintf("%s %s %s %s", subscription.Name, subscription.Provider, subscription.Dataset, subscription.ID.String()[:5]))

				expected := provider.DefaultExpectedDuration

				if p, ok := provider.Map[subscription.Provider]; ok {
					if ds, ok := p.Datasets()[subscription.Dataset]; ok && ds.ExpectedDuration > 0 {
						expected = ds.ExpectedDuration
					}
				}

				checkID, err := healthcheck.Create(
					fmt.Sprintf("%s %s (%s)", subscription.Name, subscription.Dataset, subscription.ID.String()[:5]),
					checkSlug,
					subscription.DataTypes,
					subscription.Schedule,
					expected,
				)
				if err != nil {
					log.Fatal().Err(err).Msg("creating healthcheck failed")
				}

				subscription.HealthCheckID = checkID
			}

			if err := subscription.Save(ctx); err != nil {
				log.Fatal().Err(err).Msg("failed saving subscription")
			}

			log.Info().Msg("subscription created")
		} else {
			log.Info().Msg("Not saving subscription")
		}
	},
}

func init() {
	rootCmd.AddCommand(subscribeCmd)

	// Here you will define your flags and configuration settings.

	// Cobra supports Persistent Flags which will work for this command
	// and all subcommands, e.g.:
	// subscribeCmd.PersistentFlags().String("foo", "", "A help for foo")

	// Cobra supports local flags which will only run when this command
	// is called directly, e.g.:
	// subscribeCmd.Flags().BoolP("toggle", "t", false, "Help message for toggle")
}
