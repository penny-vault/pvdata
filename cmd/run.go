/*
Copyright 2024
*/
package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/tui"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// runCmd represents the run command
var runCmd = &cobra.Command{
	Use:   "run [subscription-id...]",
	Short: "Run data import subscriptions",
	Long: `The run sub-command executes subscriptions and saves the data they generate. If no
arguments are provided then run will present a subscription picker. If subscription IDs are
provided then each subscription will execute sequentially.`,
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		// load the library
		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not connect to library")
		}

		// Set up dual logging (file + TUI channel)
		home, err := os.UserHomeDir()
		if err != nil {
			log.Fatal().Err(err).Msg("could not determine home directory")
		}
		logFile := filepath.Join(home, ".pvdata.log")
		logWriter, err := tui.NewDualWriter(logFile)
		if err != nil {
			log.Fatal().Err(err).Str("LogFile", logFile).Msg("could not create log writer")
		}
		defer logWriter.Close()

		// Configure zerolog to use the dual writer with console formatting
		consoleWriter := zerolog.ConsoleWriter{Out: logWriter}
		log.Logger = zerolog.New(consoleWriter).With().Timestamp().Logger()

		// Run pre-flight validation
		result, err := tui.RunPreflight(ctx, myLibrary, args)
		if err != nil {
			log.Fatal().Err(err).Msg("pre-flight validation failed")
		}

		// Create RunManager
		runManager := tui.NewRunManager(myLibrary, result.Subscriptions)

		// Launch TUI
		if err := tui.Run(ctx, myLibrary, runManager, logWriter); err != nil {
			fmt.Fprintf(os.Stderr, "Error: %v\n", err)
			os.Exit(1)
		}
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
}
