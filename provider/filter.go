// Copyright 2024
// SPDX-License-Identifier: Apache-2.0

package provider

import "context"

const TickerFilterKey contextKey = "ticker_filter"
const FigiFilterKey contextKey = "figi_filter"
const AssetTypeFilterKey contextKey = "asset_type_filter"

// SecurityFilterFromContext returns the ticker and/or FIGI filter values
// from the context. Returns empty strings if no filter is set.
func SecurityFilterFromContext(ctx context.Context) (ticker, figi string) {
	if v := ctx.Value(TickerFilterKey); v != nil {
		ticker, _ = v.(string)
	}

	if v := ctx.Value(FigiFilterKey); v != nil {
		figi, _ = v.(string)
	}

	return
}

// AssetTypeFilterFromContext returns the asset-type filter list (e.g.
// ["CS"], ["CS","PFD"]) set via --asset-type, falling back to the
// provided default when no filter is in the context.
func AssetTypeFilterFromContext(ctx context.Context, defaults []string) []string {
	if v := ctx.Value(AssetTypeFilterKey); v != nil {
		if types, ok := v.([]string); ok && len(types) > 0 {
			return types
		}
	}

	return defaults
}
