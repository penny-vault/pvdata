package cmd

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/penny-vault/pvdata/checks"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/penny-vault/pvdata/provider/sharadar"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var importCmd = &cobra.Command{
	Use:   "import [flags] <file1> [file2...]",
	Short: "Import data from local files into a subscription",
	Long: `Import data from local parquet or CSV files into an existing subscription.
The subscription must already exist (create with 'pvdata subscribe').

Supported file formats: .parquet, .csv, .csv.zst, .csv.zip

Examples:
  pvdata import --subscription my-fundamentals sharadar_sf1_20231226.parquet
  pvdata import --subscription abc123 data/*.parquet`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		consoleWriter := zerolog.ConsoleWriter{Out: os.Stderr}
		log.Logger = zerolog.New(consoleWriter).With().Timestamp().Logger()

		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not connect to library")
		}

		subFlag := viper.GetString("import.subscription")
		if subFlag == "" {
			log.Fatal().Msg("--subscription is required")
		}

		sub, err := resolveSubscription(ctx, myLibrary, subFlag)
		if err != nil {
			log.Fatal().Err(err).Str("subscription", subFlag).Msg("could not find subscription")
		}

		log.Info().Str("subscription", sub.Name).Str("provider", sub.Provider).Str("dataset", sub.Dataset).Msg("using subscription")

		prov, ok := provider.Map[sub.Provider]
		if !ok {
			log.Fatal().Str("provider", sub.Provider).Msg("provider not found")
		}

		fi, ok := prov.(provider.FileImporter)
		if !ok {
			log.Fatal().Str("provider", sub.Provider).Msg("provider does not support file import")
		}

		// Validate files exist and match subscription dataset
		files := args
		for _, f := range files {
			if _, err := os.Stat(f); os.IsNotExist(err) {
				log.Fatal().Str("file", f).Msg("file does not exist")
			}

			detected, detectErr := sharadar.DetectSharadarDataset(f)
			if detectErr != nil {
				log.Warn().Err(detectErr).Str("file", f).Msg("could not detect dataset from file headers, proceeding anyway")
			} else if detected != sub.Dataset {
				log.Fatal().
					Str("file", f).
					Str("detected", detected).
					Str("subscription_dataset", sub.Dataset).
					Msgf("file %s looks like %s data but subscription %q is for dataset %s", f, detected, sub.Name, sub.Dataset)
			}
		}

		if err := sub.ManagePartitions(ctx); err != nil {
			log.Error().Err(err).Msg("ManagePartitions failed")
		}

		if err := sub.RunMigrations(ctx); err != nil {
			log.Error().Err(err).Msg("RunMigrations failed")
		}

		outChan := make(chan *data.Observation, 1000)
		exitChan := make(chan data.RunSummary, 1)

		var wg sync.WaitGroup
		wg.Add(1)

		go myLibrary.SaveObservations(outChan, &wg, checks.NewInlineValidator(checks.InlineChecks()))

		fetchLogger := log.With().Str("SubscriptionID", sub.ID.String()).Logger()
		fetchCtx := fetchLogger.WithContext(ctx)

		fi.ImportFiles(fetchCtx, sub, files, outChan, exitChan)

		summary := <-exitChan

		close(outChan)
		wg.Wait()

		// Persist run history
		if err := myLibrary.SaveRunHistory(ctx, summary); err != nil {
			log.Error().Err(err).Msg("failed to save run history")
		}

		if summary.Status == data.RunSuccess {
			subDataset, dsOk := prov.Datasets()[sub.Dataset]
			if dsOk && len(subDataset.PostFetch) > 0 {
				for _, hook := range subDataset.PostFetch {
					if err := hook(ctx, sub); err != nil {
						log.Error().Err(err).Msg("post-fetch hook failed")
						break
					}
				}
			}
		}

		if summary.Status == data.RunFailed {
			log.Error().Int("observations", summary.NumObservations).Msg("import failed")
			os.Exit(1)
		} else {
			log.Info().Int("observations", summary.NumObservations).Msg("import completed successfully")
		}
	},
}

func resolveSubscription(ctx context.Context, lib *library.Library, nameOrID string) (*library.Subscription, error) {
	sub, err := lib.SubscriptionFromID(ctx, nameOrID)
	if err == nil {
		return sub, nil
	}

	allSubs, err := lib.Subscriptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not load subscriptions: %w", err)
	}

	var matches []*library.Subscription

	for _, s := range allSubs {
		if s.Name == nameOrID {
			matches = append(matches, s)
		}
	}

	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no subscription found matching %q", nameOrID)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("ambiguous subscription name %q matches %d subscriptions; use the subscription ID instead", nameOrID, len(matches))
	}
}

func init() {
	importCmd.Flags().StringP("subscription", "s", "", "Subscription name or ID (required)")

	if err := importCmd.MarkFlagRequired("subscription"); err != nil {
		log.Fatal().Err(err).Msg("could not mark subscription flag as required")
	}

	if err := viper.BindPFlag("import.subscription", importCmd.Flags().Lookup("subscription")); err != nil {
		log.Fatal().Err(err).Msg("could not bind subscription flag")
	}

	rootCmd.AddCommand(importCmd)
}
