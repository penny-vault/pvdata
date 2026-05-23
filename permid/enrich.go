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
	"strings"
	"sync/atomic"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog"
)

// DefaultEnrichAPIBudget is the maximum number of PermID API lookups
// Enrich will perform per run when a shared budget is attached via
// WithAPIBudget (cmd/run and web/run do this at run start). The
// Refinitiv free tier caps at 5,000 requests/day; 2,000 inline lookups
// per run plus a sibling 2,000 in BackfillEmpty means a full run uses
// up to ~4,000 quota, leaving room for one full run/day before
// Refinitiv 429s. When 429 does land, permid.Enrich short-circuits the
// remainder of the run via ErrRateLimited and defers to tomorrow's
// quota.
const DefaultEnrichAPIBudget = 2000

type enrichBudgetCtxKey struct{}

// WithAPIBudget attaches a shared API-call budget to ctx. The budget
// is a pool: every permid.Enrich call that derives from the same ctx
// decrements the same atomic counter, so the cap applies across every
// publish() call within a run instead of resetting per-call. Pass 0 or
// a negative value to disable Enrich's API calls entirely (DB-index
// fill still runs).
func WithAPIBudget(ctx context.Context, budget int) context.Context {
	remaining := &atomic.Int64{}
	remaining.Store(int64(budget))

	return context.WithValue(ctx, enrichBudgetCtxKey{}, remaining)
}

// apiBudgetFromContext returns the shared budget counter for the
// current run. When no budget is attached to ctx, returns a fresh
// pointer pre-loaded with DefaultEnrichAPIBudget — this is the
// stand-alone-call default (e.g. unit tests). Callers entering a run
// should attach a budget via WithAPIBudget once at the top of the run
// so every Enrich call shares it.
func apiBudgetFromContext(ctx context.Context) *atomic.Int64 {
	if v := ctx.Value(enrichBudgetCtxKey{}); v != nil {
		if ptr, ok := v.(*atomic.Int64); ok {
			return ptr
		}
	}

	remaining := &atomic.Int64{}
	remaining.Store(int64(DefaultEnrichAPIBudget))

	return remaining
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
		logger.Debug().Int("Assets", len(assets)).Msg("permid: no API key configured; skipping PermID API resolution")
		return
	}

	remaining := apiBudgetFromContext(ctx)
	if remaining.Load() <= 0 {
		// Budget==0 is the --no-permid path (or end-of-quota), and is
		// expected; log it at Info once per Enrich call so an operator
		// running with --no-permid sees the path is being short-circuited
		// rather than wondering why descriptive fields stay empty.
		logger.Info().
			Int("Assets", len(assets)).
			Msg("permid: API budget exhausted (likely --no-permid); skipping PermID API resolution")

		return
	}

	startBudget := remaining.Load()
	startTime := time.Now()

	logger.Info().
		Int("Assets", len(assets)).
		Int64("RemainingBudget", startBudget).
		Msg("permid: starting PermID API resolution")

	limiter := RateLimit()

	var (
		processed int
		resolved  int
	)

	defer func() {
		logger.Info().
			Int("Assets", len(assets)).
			Int("Processed", processed).
			Int("Resolved", resolved).
			Int64("BudgetConsumed", startBudget-remaining.Load()).
			Int64("RemainingBudget", remaining.Load()).
			Str("Elapsed", time.Since(startTime).Round(time.Second).String()).
			Msg("permid: finished PermID API resolution")
	}()

	progressTick := time.Now()

	for _, asset := range assets {
		processed++

		// Heartbeat every 30s so a big batch (e.g. BackfillEmpty's 250
		// candidates) is never silent for longer than that while the 5
		// req/s rate limiter slowly drains the queue.
		if time.Since(progressTick) > 30*time.Second {
			progressTick = time.Now()

			logger.Info().
				Int("Processed", processed).
				Int("Total", len(assets)).
				Int("ResolvedSoFar", resolved).
				Int64("RemainingBudget", remaining.Load()).
				Str("Elapsed", time.Since(startTime).Round(time.Second).String()).
				Msg("permid: heartbeat")
		}

		startOrg := asset.OrganizationPermID
		startInstrument := asset.InstrumentPermID

		if asset.OrganizationPermID != "" && asset.InstrumentPermID != "" {
			continue
		}

		orgPermID := asset.OrganizationPermID

		// Step 2: CIK -> Organization PermID.
		if orgPermID == "" && asset.CIK != "" {
			if remaining.Load() <= 0 {
				logger.Info().
					Msg("permid: API budget exhausted; remaining assets deferred to next run")

				break
			}

			remaining.Add(-1)

			id, err := LookupByCIK(ctx, asset.CIK, limiter)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}

				if errors.Is(err, ErrRateLimited) {
					logger.Warn().
						Err(err).
						Msg("permid: refinitiv daily quota exhausted (429); halting PermID resolution for this run")

					remaining.Store(0)

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
					Msg("permid: API budget exhausted; remaining assets deferred to next run")

				break
			}

			remaining.Add(-1)

			result, err := LookupByTicker(ctx, asset.Ticker, asset.Name, orgPermID, limiter)
			if err != nil {
				if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
					return
				}

				if errors.Is(err, ErrRateLimited) {
					logger.Warn().
						Err(err).
						Msg("permid: refinitiv daily quota exhausted (429); halting PermID resolution for this run")

					remaining.Store(0)

					return
				}

				logger.Warn().
					Err(err).
					Str("Ticker", asset.Ticker).
					Msg("permid: ticker lookup failed")
			} else {
				if orgPermID == "" && result.OrgPermID != "" {
					orgPermID = result.OrgPermID

					logger.Debug().
						Str("Ticker", asset.Ticker).
						Str("OrganizationPermID", orgPermID).
						Msg("permid: resolved Organization PermID from ticker (name-gated)")
				}

				if result.InstrumentPermID != "" {
					asset.InstrumentPermID = result.InstrumentPermID

					logger.Debug().
						Str("Ticker", asset.Ticker).
						Str("InstrumentPermID", result.InstrumentPermID).
						Msg("permid: resolved Instrument PermID from ticker")
				}

				fillFromLookup(asset, result, logger)
			}
		}

		if asset.OrganizationPermID == "" && orgPermID != "" {
			asset.OrganizationPermID = orgPermID
		}

		if asset.OrganizationPermID != startOrg || asset.InstrumentPermID != startInstrument {
			resolved++
		}
	}
}

// fillFromLookup populates empty descriptive fields on asset from a
// PermID search result. Only empty fields are filled; nothing already
// resolved by a higher-priority source (Massive details, OpenFIGI,
// asset index) is overwritten.
//
// Mapping:
//   - asset.Name              <- result.OrgName
//   - asset.CorporateUrl      <- result.OrgURL
//   - asset.AssetType         <- assetClassToAssetType(result.AssetClass)
//   - asset.PrimaryExchange   <- micToExchange(result.InstrumentMIC)
func fillFromLookup(asset *data.Asset, result *LookupResult, logger *zerolog.Logger) {
	if result == nil {
		return
	}

	if asset.Name == "" && result.OrgName != "" {
		asset.Name = result.OrgName
	}

	if asset.CorporateUrl == "" && result.OrgURL != "" {
		asset.CorporateUrl = result.OrgURL
	}

	if asset.AssetType == "" || asset.AssetType == data.UnknownAsset {
		if mapped := assetClassToAssetType(result.AssetClass); mapped != "" {
			asset.AssetType = mapped
		}
	}

	if asset.PrimaryExchange == "" || asset.PrimaryExchange == data.UnknownExchange {
		if mapped := micToExchange(result.InstrumentMIC); mapped != "" {
			asset.PrimaryExchange = mapped
		}
	}

	if logger != nil && result.OrgName != "" {
		logger.Debug().
			Str("Ticker", asset.Ticker).
			Str("PermIDOrgName", result.OrgName).
			Str("PermIDAssetClass", result.AssetClass).
			Str("PermIDMic", result.InstrumentMIC).
			Msg("permid: filled descriptive fields from lookup result")
	}
}

// assetClassToAssetType maps Refinitiv's assetClass string onto our
// data.AssetType enum. Unknown values return "" so the caller leaves
// the asset's AssetType untouched.
func assetClassToAssetType(assetClass string) data.AssetType {
	switch strings.ToLower(strings.TrimSpace(assetClass)) {
	case "ordinary shares", "common stock":
		return data.CommonStock
	case "depository receipts", "american depository receipts":
		return data.ADRC
	case "exchange traded fund", "etf":
		return data.ETF
	case "exchange traded note", "etn":
		return data.ETN
	case "closed end fund", "closed-end fund":
		return data.CEF
	case "mutual fund":
		return data.MutualFund
	default:
		return ""
	}
}

// micToExchange maps a MIC code onto our data.Exchange enum. Returns ""
// for MICs we don't know about so the caller leaves the field alone
// rather than landing UNK in the DB.
func micToExchange(mic string) data.Exchange {
	switch strings.ToUpper(strings.TrimSpace(mic)) {
	case "XNYS":
		return data.NYSEExchange
	case "XNAS", "XNGS", "XNCM", "XNMS":
		return data.NasdaqExchange
	case "XASE":
		return data.NYSEMktExchange
	case "BATS":
		return data.BATSExchange
	case "ARCX":
		return data.ARCAExchange
	case "OTC":
		return data.OTCExchange
	default:
		return ""
	}
}

// fillFromAssetIndex copies OrganizationPermID and InstrumentPermID
// onto each asset from a matching entry in the existing-assets index
// (data.AssetIndexFromContext). Pure local lookup, no API calls; does
// not overwrite values already on the asset. Lookup keys on
// ticker:composite_figi or ticker:cik — ticker-alone matches are
// refused because reused tickers would risk copying the wrong entity.
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
