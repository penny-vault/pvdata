package tui

import (
	"context"
	"fmt"

	"github.com/charmbracelet/huh"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
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

	// Step 2: Validate preferred views
	if err := validatePreferredViews(ctx, myLibrary); err != nil {
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

// validatePreferredViews checks that preferred views exist for all data types
// that have subscriptions. If only one subscription provides a data type, the
// view is auto-created. If multiple subscriptions provide it, the user is
// prompted to choose.
func validatePreferredViews(ctx context.Context, myLibrary *library.Library) error {
	existingViews, err := library.PreferredViews(ctx, myLibrary.Pool)
	if err != nil {
		return fmt.Errorf("could not query preferred views: %w", err)
	}

	allSubs, err := myLibrary.Subscriptions(ctx)
	if err != nil {
		return fmt.Errorf("could not load subscriptions: %w", err)
	}

	// Build a map: data type key -> list of (subscription, table name)
	type subTable struct {
		sub       *library.Subscription
		tableName string
	}
	dataTypeProviders := make(map[string][]subTable)
	for _, sub := range allSubs {
		if !sub.Active {
			continue
		}
		for _, dtKey := range sub.DataTypes {
			tableName := sub.DataTablesMap[dtKey]
			if tableName != "" {
				dataTypeProviders[dtKey] = append(dataTypeProviders[dtKey], subTable{sub, tableName})
			}
		}
	}

	// For each data type that has providers but no view, create one
	for dtKey, providers := range dataTypeProviders {
		dt := data.DataTypes[dtKey]
		if dt == nil || dt.ViewName == "" {
			continue
		}

		if _, exists := existingViews[dt.ViewName]; exists {
			continue
		}

		if len(providers) == 1 {
			// Auto-create
			if err := library.SetPreferredView(ctx, myLibrary.Pool, dtKey, providers[0].tableName); err != nil {
				return fmt.Errorf("could not auto-create view %s: %w", dt.ViewName, err)
			}
			log.Info().Str("View", dt.ViewName).Str("Table", providers[0].tableName).Msg("auto-created preferred view")
		} else {
			// Prompt user to choose
			options := make([]huh.Option[string], len(providers))
			for i, p := range providers {
				label := fmt.Sprintf("%s (%s/%s) -> %s", p.sub.Name, p.sub.Provider, p.sub.Dataset, p.tableName)
				options[i] = huh.NewOption(label, p.tableName)
			}

			var selected string
			form := huh.NewForm(
				huh.NewGroup(
					huh.NewSelect[string]().
						Title(fmt.Sprintf("Select the preferred table for '%s' view:", dt.ViewName)).
						Description("Multiple subscriptions provide this data type. Choose which one the view should point to.").
						Options(options...).
						Value(&selected),
				),
			)

			if err := form.Run(); err != nil {
				return fmt.Errorf("view selection for %s cancelled: %w", dt.ViewName, err)
			}

			if err := library.SetPreferredView(ctx, myLibrary.Pool, dtKey, selected); err != nil {
				return fmt.Errorf("could not create view %s: %w", dt.ViewName, err)
			}
		}
	}

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
