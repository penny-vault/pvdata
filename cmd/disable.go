/*
Copyright 2024
*/
package cmd

import (
	"context"

	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// disableCmd represents the disable command
var disableCmd = &cobra.Command{
	Use:   "disable <subscription-id>",
	Short: "Disable active subscriptions",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not load library info")
		}

		for _, id := range args {
			sub, err := myLibrary.SubscriptionFromID(ctx, id)
			if err != nil {
				log.Fatal().Err(err).Str("ID", id).Msg("could not get subscription for ID")
			}

			if err := sub.Deactivate(ctx); err != nil {
				log.Fatal().Err(err).Msg("could not deactivate subscription")
			}

			log.Info().Str("ID", id).Msg("subscription disabled")
		}
	},
}

func init() {
	rootCmd.AddCommand(disableCmd)
}
