// Copyright 2024
// SPDX-License-Identifier: Apache-2.0
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
// http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.
package permid

import (
	"context"
	"errors"
	"sync/atomic"

	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog"
)

// DefaultEnrichAPIBudget is the maximum number of PermID API lookups
// Enrich will perform per Enrich() call when no budget is supplied via
// context. The Refinitiv free tier caps at 5,000 requests/day; 250
// inline lookups per run keeps a comfortable margin while still
// catching the common new-asset case. Long backfills should rely on
// BackfillEmpty for additional capacity rather than raising this.
const DefaultEnrichAPIBudget = 250

type enrichBudgetCtxKey struct{}

// WithAPIBudget attaches a per-Enrich API-call budget to ctx. The
// budget caps the total number of PermID Lookup* calls Enrich will
// issue before falling back to leaving assets unresolved. Pass 0 or a
// negative value to disable Enrich's API calls entirely (DB index
// fill still runs).
func WithAPIBudget(ctx context.Context, budget int) context.Context {
	return context.WithValue(ctx, enrichBudgetCtxKey{}, budget)
}

// apiBudgetFromContext returns the configured API budget, falling
// back to DefaultEnrichAPIBudget when none is set.
func apiBudgetFromContext(ctx context.Context) int {
	if v := ctx.Value(enrichBudgetCtxKey{}); v != nil {
		if n, ok := v.(int); ok {
			return n
		}
	}

	return DefaultEnrichAPIBudget
}

// Enrich fills OrganizationPermID and InstrumentPermID on each asset
// using a three-step escalation. Each step only operates on assets
// still missing a PermID after the previous step:
//
//  1. Existing-assets index (loaded once per run via
//     data.WithAssetIndex in the context; nil when absent, in which
//     case step 1 is skipped). Reuses PermIDs we have previously
//     persisted so we do not re-spend the Refinitiv daily quota on
//     entities the DB already knows.
//
//  2. Lookup by CIK — deterministic Organization PermID resolution
//     with no name-similarity gate needed. CIK keys directly map to
//     a single org row on Refinitiv's side.
//
//  3. Lookup by ticker — yields the Instrument PermID and an
//     Organization PermID when step 2 did not produce one. Results
//     are validated either by isIssuedBy matching the CIK-resolved
//     org or by the name-similarity gate against the asset's name.
//
// Steps 2 and 3 share an API-call budget (default
// DefaultEnrichAPIBudget; override via WithAPIBudget). Once the
// budget is exhausted, remaining assets are left for a later run
// rather than dropping into 429 / hard-fail territory against the
// 5,000/day daily ceiling.
//
// Enrich is best-effort: API errors on one asset are logged but do
// not abort the batch. Skips silently when no permid.apikey is
// configured.
func Enrich(ctx context.Context, assets ...*data.Asset) {
	logger := zerolog.Ctx(ctx)

	// Step 1 runs regardless of whether the API is reachable — it
	// touches no network and costs no quota.
	fillFromAssetIndex(ctx, assets)

	if APIKey() == "" {
		logger.Debug().Msg("permid: no API key configured; skipping PermID API resolution")
		return
	}

	budget := int64(apiBudgetFromContext(ctx))
	if budget <= 0 {
		logger.Debug().Msg("permid: API budget is 0 for this run; skipping PermID API resolution")
		return
	}

	var remaining atomic.Int64
	remaining.Store(budget)

	limiter := RateLimit()

	for _, asset := range assets {
		if asset.OrganizationPermID != "" && asset.InstrumentPermID != "" {
			continue
		}

		orgPermID := asset.OrganizationPermID

		// Step 2: CIK -> Organization PermID.
		if orgPermID == "" && asset.CIK != "" {
			if remaining.Load() <= 0 {
				logger.Info().
					Int64("Budget", budget).
					Msg("permid: API budget exhausted; remaining assets deferred to next run")

				break
			}

			remaining.Add(-1)

			id, err := LookupByCIK(ctx, asset.CIK, limiter)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}

				logger.Warn().
					Err(err).
					Str("Ticker", asset.Ticker).
					Str("CIK", asset.CIK).
					Msg("permid: CIK lookup failed; will try ticker fallback")
			} else if id != "" {
				orgPermID = id

				logger.Debug().
					Str("Ticker", asset.Ticker).
					Str("CIK", asset.CIK).
					Str("OrganizationPermID", orgPermID).
					Msg("permid: resolved Organization PermID from CIK")
			}
		}

		// Step 3: ticker -> instrument (and org if still unresolved).
		if asset.Ticker != "" && (orgPermID == "" || asset.InstrumentPermID == "") {
			if remaining.Load() <= 0 {
				logger.Info().
					Int64("Budget", budget).
					Msg("permid: API budget exhausted; remaining assets deferred to next run")

				break
			}

			remaining.Add(-1)

			gotOrg, gotInstr, err := LookupByTicker(ctx, asset.Ticker, asset.Name, orgPermID, limiter)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}

				logger.Warn().
					Err(err).
					Str("Ticker", asset.Ticker).
					Msg("permid: ticker lookup failed")
			} else {
				if orgPermID == "" && gotOrg != "" {
					orgPermID = gotOrg

					logger.Debug().
						Str("Ticker", asset.Ticker).
						Str("OrganizationPermID", orgPermID).
						Msg("permid: resolved Organization PermID from ticker (name-gated)")
				}

				if gotInstr != "" {
					asset.InstrumentPermID = gotInstr

					logger.Debug().
						Str("Ticker", asset.Ticker).
						Str("InstrumentPermID", gotInstr).
						Msg("permid: resolved Instrument PermID from ticker")
				}
			}
		}

		if asset.OrganizationPermID == "" && orgPermID != "" {
			asset.OrganizationPermID = orgPermID
		}
	}
}

// fillFromAssetIndex copies OrganizationPermID and InstrumentPermID
// onto each asset from a matching entry in the existing-assets index
// (data.AssetIndexFromContext). Pure local lookup, no API calls.
// Does not overwrite a value already on the asset.
//
// Lookup goes through AssetIndex.Lookup, which keys on
// ticker:composite_figi or ticker:cik. Ticker-alone matches are
// refused: a reused ticker like BBI (Blockbuster 2010, Brickell
// Biotech 2022) would otherwise risk copying the wrong entity's
// PermIDs.
func fillFromAssetIndex(ctx context.Context, assets []*data.Asset) {
	idx := data.AssetIndexFromContext(ctx)
	if idx.IsZero() {
		return
	}

	for _, asset := range assets {
		if asset.OrganizationPermID != "" && asset.InstrumentPermID != "" {
			continue
		}

		match, ok := idx.Lookup(asset)
		if !ok {
			continue
		}

		if asset.OrganizationPermID == "" && match.OrganizationPermID != "" {
			asset.OrganizationPermID = match.OrganizationPermID
		}

		if asset.InstrumentPermID == "" && match.InstrumentPermID != "" {
			asset.InstrumentPermID = match.InstrumentPermID
		}
	}
}
