/*
Copyright © 2024 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"context"
	"os"
	"sync"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/penny-vault/pvdata/web"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// runCmd represents the run command
var runCmd = &cobra.Command{
	Use:   "run [subscription-id...]",
	Short: "Run data import subscriptions",
	Long: `The run sub-command executes subscriptions and saves the data they generate. If no
arguments are provided then run will execute as a daemon and execute each subscription at the
scheduled times. If subscription IDs are provided then each subscription will execute
sequentially (ignoring any set schedule).`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		// load the library
		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not connect to library")
		}

		outChan := make(chan *data.Observation, 1000)
		exitChan := make(chan data.RunSummary, 5)

		var wg sync.WaitGroup
		wg.Add(1)
		go myLibrary.SaveObservations(outChan, &wg)

		// check if we are running in daemon mode
		if len(args) == 0 {
			// no args provided -- run as a daemon

			// start the web server
			app := web.CreateFiberApp()

			port := viper.GetString("web.port")
			if port == "" {
				port = "3000"
			}

			app.Listen(":" + port)

			os.Exit(0)
		}

		// not daemon mode, execute each subscription individually
		for _, subscriptionID := range args {
			subscription, err := myLibrary.SubscriptionFromID(ctx, subscriptionID)
			if err != nil {
				log.Fatal().Err(err).Str("SubscriptionID", subscriptionID).Msg("could not load subscription")
			}

			var (
				subProvider provider.Provider
				subDataset  provider.Dataset
				ok          bool
			)

			// create any needed partitions
			err = subscription.ManagePartitions(ctx)
			if err != nil {
				log.Error().Err(err).Msg("ManagePartitions returned an error")
			}

			if subProvider, ok = provider.Map[subscription.Provider]; !ok {
				log.Fatal().Str("ProviderKey", subscription.Provider).Msg("subscription is mis-configured, provider not found")
			}

			if subDataset, ok = subProvider.Datasets()[subscription.Dataset]; !ok {
				log.Fatal().Str("ProviderKey", subscription.Provider).Str("DatasetKey", subscription.Dataset).
					Msg("subscription is mis-configured, dataset not found")
			}

			fetchLogger := log.With().Str("SubscriptionID", subscriptionID).Logger()
			ctx = fetchLogger.WithContext(ctx)

			subDataset.Fetch(ctx, subscription, outChan, exitChan)

			// read the exit message from exitChan
			summaryMsg := <-exitChan
			fetchLogger.Info().Time("StartTime", summaryMsg.StartTime).Time("EndTime", summaryMsg.EndTime).Str("RunTime", summaryMsg.EndTime.Sub(summaryMsg.StartTime).String()).Msg("finished running subscription")
		}

		// close the output channel
		close(outChan)

		// wait for library SaveObservations to finish
		wg.Wait()
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
}
