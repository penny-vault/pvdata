package tui

import (
	"context"
	"fmt"

	"charm.land/huh/v2"
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
	conn, err := myLibrary.AcquireWithTimeout(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not connect to database: %w", err)
	}

	conn.Release()

	// Step 2: Validate published views
	if err := validatePublishedViews(ctx, myLibrary); err != nil {
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

// validatePublishedViews refreshes the SQL of every persisted
// published view so that code-level changes to the view-generator
// (e.g., new dedup rules or column additions) take effect on the next
// run. It deliberately does NOT create or prompt for new views: the
// published-view layer is operator-managed via `pvdata publish` and
// surprising mutations during a data-fetch run are out of scope.
func validatePublishedViews(ctx context.Context, myLibrary *library.Library) error {
	allViews, err := library.LoadPublishedViews(ctx, myLibrary.Pool)
	if err != nil {
		return fmt.Errorf("could not load published views: %w", err)
	}

	for _, pv := range allViews {
		if err := library.ApplyPublishedView(ctx, myLibrary.Pool, pv); err != nil {
			log.Warn().Err(err).Str("View", pv.ViewName).Msg("could not re-apply published view")
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
