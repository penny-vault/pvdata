package tui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/charmbracelet/huh"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pelletier/go-toml/v2"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
)

// PreflightResult holds the results of pre-flight validation.
type PreflightResult struct {
	Subscriptions []*library.Subscription
}

// RunPreflight validates configuration and prompts for missing values.
func RunPreflight(ctx context.Context, myLibrary *library.Library, subscriptionIDs []string) (*PreflightResult, error) {
	// Step 1: Validate database connection
	conn, err := myLibrary.Pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not connect to database: %w", err)
	}
	conn.Release()

	// Step 2: Validate default.asset_table
	if err := validateAssetTable(ctx, myLibrary.Pool); err != nil {
		return nil, err
	}

	// Step 3: Resolve subscriptions
	var subscriptions []*library.Subscription
	if len(subscriptionIDs) > 0 {
		for _, id := range subscriptionIDs {
			sub, err := myLibrary.SubscriptionFromID(ctx, id)
			if err != nil {
				return nil, fmt.Errorf("could not load subscription %s: %w", id, err)
			}
			subscriptions = append(subscriptions, sub)
		}
	} else {
		subscriptions, err = selectSubscriptions(ctx, myLibrary)
		if err != nil {
			return nil, err
		}
	}

	if len(subscriptions) == 0 {
		return nil, fmt.Errorf("no subscriptions selected")
	}

	return &PreflightResult{
		Subscriptions: subscriptions,
	}, nil
}

func validateAssetTable(ctx context.Context, pool *pgxpool.Pool) error {
	assetTable := viper.GetString("default.asset_table")
	if assetTable != "" {
		return nil
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return fmt.Errorf("could not acquire connection: %w", err)
	}
	defer conn.Release()

	rows, err := conn.Query(ctx,
		`SELECT DISTINCT unnest(data_tables)
		 FROM subscriptions
		 WHERE data_types @> ARRAY['asset-description']::datatype[]`)
	if err != nil {
		return fmt.Errorf("could not query asset tables: %w", err)
	}
	defer rows.Close()

	var tables []string
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			return fmt.Errorf("could not scan asset table: %w", err)
		}
		tables = append(tables, table)
	}

	if len(tables) == 0 {
		return fmt.Errorf("no asset tables found; create an asset subscription first")
	}

	if len(tables) == 1 {
		assetTable = tables[0]
		log.Info().Str("AssetTable", assetTable).Msg("auto-selected asset table")
	} else {
		options := make([]huh.Option[string], len(tables))
		for i, t := range tables {
			options[i] = huh.NewOption(t, t)
		}

		form := huh.NewForm(
			huh.NewGroup(
				huh.NewSelect[string]().
					Title("Select the default asset table:").
					Description("This table is used to look up active assets for data providers.").
					Options(options...).
					Value(&assetTable),
			),
		)

		if err := form.Run(); err != nil {
			return fmt.Errorf("asset table selection cancelled: %w", err)
		}
	}

	viper.Set("default.asset_table", assetTable)

	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("could not determine home directory: %w", err)
	}

	configFN := filepath.Join(home, ".pvdata.toml")

	existingData, err := os.ReadFile(configFN)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("could not read config file: %w", err)
	}

	configMap := make(map[string]any)
	if len(existingData) > 0 {
		if err := toml.Unmarshal(existingData, &configMap); err != nil {
			return fmt.Errorf("could not parse config file: %w", err)
		}
	}

	defaultSection, ok := configMap["default"].(map[string]any)
	if !ok {
		defaultSection = make(map[string]any)
	}
	defaultSection["asset_table"] = assetTable
	configMap["default"] = defaultSection

	newData, err := toml.Marshal(configMap)
	if err != nil {
		return fmt.Errorf("could not marshal config: %w", err)
	}

	if err := os.WriteFile(configFN, newData, 0644); err != nil {
		return fmt.Errorf("could not write config file: %w", err)
	}

	log.Info().Str("AssetTable", assetTable).Str("ConfigFile", configFN).Msg("saved default asset table to config")

	return nil
}

func selectSubscriptions(ctx context.Context, myLibrary *library.Library) ([]*library.Subscription, error) {
	allSubs, err := myLibrary.Subscriptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not load subscriptions: %w", err)
	}

	if len(allSubs) == 0 {
		return nil, fmt.Errorf("no subscriptions found; use 'pvdata subscribe' to create one")
	}

	options := make([]huh.Option[string], len(allSubs))
	for i, sub := range allSubs {
		label := fmt.Sprintf("%s (%s/%s)", sub.Name, sub.Provider, sub.Dataset)
		options[i] = huh.NewOption(label, sub.ID.String())
	}

	var selectedIDs []string
	form := huh.NewForm(
		huh.NewGroup(
			huh.NewMultiSelect[string]().
				Title("Select subscriptions to run:").
				Options(options...).
				Value(&selectedIDs),
		),
	)

	if err := form.Run(); err != nil {
		return nil, fmt.Errorf("subscription selection cancelled: %w", err)
	}

	idMap := make(map[string]*library.Subscription, len(allSubs))
	for _, sub := range allSubs {
		idMap[sub.ID.String()] = sub
	}

	selected := make([]*library.Subscription, 0, len(selectedIDs))
	for _, id := range selectedIDs {
		if sub, ok := idMap[id]; ok {
			selected = append(selected, sub)
		}
	}

	return selected, nil
}
