/*
Copyright © 2024 NAME HERE <EMAIL ADDRESS>
*/
package cmd

import (
	"context"
	"fmt"

	"charm.land/huh/v2"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var deleteSubscription bool

// unsubscribeCmd represents the unsubscribe command
var unsubscribeCmd = &cobra.Command{
	Use:   "unsubscribe <subscription-id>",
	Short: "Unsubscribe from the specified dataset",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not load library info")
		}

		action := "de-activate"
		if deleteSubscription {
			action = "delete"
		}

		for _, id := range args {
			sub, err := myLibrary.SubscriptionFromID(ctx, id)
			if err != nil {
				log.Fatal().Err(err).Str("ID", id).Msg("could not get subscription for ID")
			}

			confirmed := false
			confirmForm := huh.NewForm(
				huh.NewGroup(
					huh.NewConfirm().
						Title(fmt.Sprintf("Are you sure you want to %s '%s'?", action, sub.Name)).
						Value(&confirmed),
				),
			)

			err = confirmForm.Run()
			if err != nil {
				log.Fatal().Err(err).Msg("failed to create wizard")
			}

			if confirmed {
				fmt.Printf("%s '%s'...\n", action, sub.Name)

				if deleteSubscription {
					if err := sub.Delete(ctx); err != nil {
						log.Fatal().Err(err).Msg("could not delete subscription")
					}
				} else {
					if err := sub.Deactivate(ctx); err != nil {
						log.Fatal().Err(err).Msg("could not de-activate subscription")
					}
				}
			} else {
				fmt.Printf("Ok, we won't %s '%s'\n", action, sub.Name)
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(unsubscribeCmd)
	unsubscribeCmd.Flags().BoolVarP(&deleteSubscription, "delete", "d", false, "delete subscription; warning this will delete the subscription and tables associated with said description")
}
