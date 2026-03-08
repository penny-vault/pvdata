package cmd

import (
	"context"
	"fmt"
	"os"

	"github.com/charmbracelet/huh"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var preferCmd = &cobra.Command{
	Use:   "prefer [data-type]",
	Short: "Set or display preferred table views",
	Long: `Manage preferred views that map canonical names (eod, assets,
fundamentals, etc.) to subscription-specific tables. These views allow any
service to query data using simple names like 'SELECT * FROM eod' instead
of subscription-specific table names.

Without arguments, displays all current preferred views. With a data type
argument, prompts to select which subscription table the view should
point to.

Examples:
  pvdata prefer              # show all current views
  pvdata prefer eod          # set preferred table for eod view
  pvdata prefer assets       # set preferred table for assets view`,
	Args: cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not connect to library")
		}
		defer myLibrary.Close()

		if len(args) == 0 {
			showPreferredViews(ctx, myLibrary)
			return
		}

		setPreferredView(ctx, myLibrary, args[0])
	},
}

func showPreferredViews(ctx context.Context, myLibrary *library.Library) {
	views, err := library.PreferredViews(ctx, myLibrary.Pool)
	if err != nil {
		log.Fatal().Err(err).Msg("could not query preferred views")
	}

	if len(views) == 0 {
		fmt.Println("No preferred views configured.")
		fmt.Println("Run 'pvdata prefer <view-name>' to set one, or views are auto-created when subscriptions are saved.")
		return
	}

	fmt.Println("Preferred views:")
	fmt.Println()
	for viewName, tableName := range views {
		fmt.Printf("  %-20s -> %s\n", viewName, tableName)
	}
}

func setPreferredView(ctx context.Context, myLibrary *library.Library, viewNameOrKey string) {
	// Find the data type key for the given view name or key
	var targetKey string
	for key, dt := range data.DataTypes {
		if dt.ViewName == viewNameOrKey || key == viewNameOrKey {
			targetKey = key
			break
		}
	}

	if targetKey == "" {
		fmt.Printf("Unknown data type or view name: %s\n", viewNameOrKey)
		fmt.Println("\nAvailable view names:")
		for key, dt := range data.DataTypes {
			if dt.ViewName != "" {
				fmt.Printf("  %-20s (data type: %s)\n", dt.ViewName, key)
			}
		}
		os.Exit(1)
	}

	dt := data.DataTypes[targetKey]

	// Find all subscriptions that provide this data type
	allSubs, err := myLibrary.Subscriptions(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("could not load subscriptions")
	}

	type candidate struct {
		sub       *library.Subscription
		tableName string
	}

	var candidates []candidate
	for _, sub := range allSubs {
		if tableName, ok := sub.DataTablesMap[targetKey]; ok && tableName != "" {
			candidates = append(candidates, candidate{sub, tableName})
		}
	}

	if len(candidates) == 0 {
		fmt.Printf("No subscriptions provide the '%s' data type.\n", targetKey)
		os.Exit(1)
	}

	// Show current view if it exists
	views, err := library.PreferredViews(ctx, myLibrary.Pool)
	if err != nil {
		log.Fatal().Err(err).Msg("could not query preferred views")
	}
	if current, ok := views[dt.ViewName]; ok {
		fmt.Printf("Current '%s' view points to: %s\n\n", dt.ViewName, current)
	}

	if len(candidates) == 1 {
		tableName := candidates[0].tableName
		if err := library.SetPreferredView(ctx, myLibrary.Pool, targetKey, tableName); err != nil {
			log.Fatal().Err(err).Msg("could not set preferred view")
		}
		fmt.Printf("Set '%s' view -> %s\n", dt.ViewName, tableName)
		return
	}

	// Multiple candidates -- prompt the user
	options := make([]huh.Option[string], len(candidates))
	for i, c := range candidates {
		label := fmt.Sprintf("%s (%s/%s) -> %s", c.sub.Name, c.sub.Provider, c.sub.Dataset, c.tableName)
		options[i] = huh.NewOption(label, c.tableName)
	}

	var selected string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewSelect[string]().
				Title(fmt.Sprintf("Select the preferred table for '%s' view:", dt.ViewName)).
				Options(options...).
				Value(&selected),
		),
	)

	if err := form.Run(); err != nil {
		log.Fatal().Err(err).Msg("selection cancelled")
	}

	if err := library.SetPreferredView(ctx, myLibrary.Pool, targetKey, selected); err != nil {
		log.Fatal().Err(err).Msg("could not set preferred view")
	}

	fmt.Printf("Set '%s' view -> %s\n", dt.ViewName, selected)
}

func init() {
	rootCmd.AddCommand(preferCmd)
}
