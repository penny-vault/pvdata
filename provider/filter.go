// Copyright 2024
// SPDX-License-Identifier: Apache-2.0

package provider

import "context"

const TickerFilterKey contextKey = "ticker_filter"
const FigiFilterKey contextKey = "figi_filter"

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
