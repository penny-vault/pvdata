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
	"fmt"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog"
)

// DefaultBackfillLimit caps the number of assets BackfillEmpty
// attempts to resolve in one invocation. The Refinitiv free tier caps
// at 5,000 requests/day; 2,000 backfill assets at up to 2 API calls
// each = ~2,000-4,000 requests per invocation (the lower bound when
// CIK alone resolves both org and instrument PermIDs), paired with up
// to 2,000 inline Enrich requests during the rest of the run. That
// leaves room for roughly one full run/day before Refinitiv 429s;
// when 429 does land Enrich short-circuits the remainder of the run
// via ErrRateLimited.
const DefaultBackfillLimit = 2000

// BackfillEmpty scans the subscription's own asset_description table for
// rows missing OrganizationPermID or InstrumentPermID, resolves up to
// `limit` of them via Enrich, and writes the resolved values back to that
// table. Asset providers call this at the end of their Fetch so PermID
// coverage is maintained by the provider that owns the table, drawing on
// the same per-run API budget as the inline Enrich calls. Pass limit =
// DefaultBackfillLimit for the standard cadence; pass 0 to no-op. Skips
// silently when no permid.apikey is configured or the table is absent.
func BackfillEmpty(ctx context.Context, sub *library.Subscription, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}

	logger := zerolog.Ctx(ctx)

	if APIKey() == "" {
		logger.Debug().Msg("permid: no API key configured; skipping backfill")
		return 0, nil
	}

	// Honour the per-run API budget set by WithAPIBudget. --no-permid
	// sets the budget to 0 specifically to avoid the rate-limited
	// Refinitiv path; without this check, BackfillEmpty would still
	// grind through up to DefaultBackfillLimit assets at PermID's 5
	// req/s cap, which was exactly the silent stall the operator hit
	// before this guard was added.
	if remaining := apiBudgetFromContext(ctx); remaining.Load() <= 0 {
		logger.Info().Msg("permid: API budget is 0; skipping backfill")
		return 0, nil
	}

	tbl := sub.DataTablesMap[data.AssetKey]
	if tbl == "" {
		return 0, nil
	}

	logger.Info().Int("Limit", limit).Str("Table", tbl).Msg("permid: starting backfill of empty PermIDs")

	conn, err := sub.Library.AcquireWithTimeout(ctx)
	if err != nil {
		return 0, fmt.Errorf("permid backfill: acquire connection: %w", err)
	}
	defer conn.Release()

	// Order common-stock candidates first so the limited daily API
	// budget gets spent on the asset class we most care about for
	// PermID coverage. ADRC (depositary receipts) is the natural
	// runner-up. Everything else (ETF / MF / CEF / ETN / unknown)
	// trails. Secondary order on ticker keeps the pick deterministic
	// across runs so we don't churn the same residual subset.
	sql := fmt.Sprintf(`SELECT
		ticker,
		composite_figi,
		coalesce(share_class_figi, '') as share_class_figi,
		coalesce(cik, '') as cik,
		coalesce(organization_permid, '') as organization_permid,
		coalesce(instrument_permid, '') as instrument_permid,
		coalesce(name, '') as name,
		coalesce(active, false) as active
	FROM %s
	WHERE composite_figi <> ''
	  AND (organization_permid IS NULL OR organization_permid = ''
	       OR instrument_permid IS NULL OR instrument_permid = '')
	ORDER BY
	  CASE asset_type
	    WHEN 'CS'   THEN 0
	    WHEN 'ADRC' THEN 1
	    ELSE             2
	  END,
	  ticker
	LIMIT %d`, tbl, limit)

	rows, queryErr := conn.Query(ctx, sql)
	if queryErr != nil {
		return 0, fmt.Errorf("permid backfill: query %s: %w", tbl, queryErr)
	}

	var candidates []*data.Asset
	if scanErr := pgxscan.ScanAll(&candidates, rows); scanErr != nil {
		return 0, fmt.Errorf("permid backfill: scan %s: %w", tbl, scanErr)
	}

	if len(candidates) == 0 {
		logger.Debug().Msg("permid backfill: no candidates")

		return 0, nil
	}

	logger.Info().
		Int("Candidates", len(candidates)).
		Int("Budget", limit).
		Msg("permid backfill: resolving missing PermIDs")

	// Reuse Enrich's full pipeline: it consults the asset index
	// first (free DB-level lookup), then spends API budget for
	// what is left. WithAPIBudget caps that spend to the same
	// limit we passed for candidate collection.
	enrichCtx := WithAPIBudget(ctx, limit)
	Enrich(enrichCtx, candidates...)

	resolved := 0

	for _, a := range candidates {
		if a.OrganizationPermID == "" && a.InstrumentPermID == "" {
			continue
		}

		// COALESCE-via-NULLIF on the new values preserves any
		// pre-existing PermID when Enrich only resolved one of
		// the two; a partial resolution must not overwrite a
		// good value with an empty string.
		updateSQL := fmt.Sprintf(`UPDATE %s SET
			organization_permid = COALESCE(NULLIF($1, ''), organization_permid),
			instrument_permid = COALESCE(NULLIF($2, ''), instrument_permid)
		WHERE ticker = $3 AND composite_figi = $4`, tbl)

		if _, err := conn.Exec(ctx, updateSQL,
			a.OrganizationPermID,
			a.InstrumentPermID,
			a.Ticker,
			a.CompositeFigi,
		); err != nil {
			logger.Warn().
				Err(err).
				Str("Table", tbl).
				Str("Ticker", a.Ticker).
				Msg("permid backfill: update failed")

			continue
		}

		resolved++
	}

	logger.Info().
		Int("Resolved", resolved).
		Int("Candidates", len(candidates)).
		Msg("permid backfill: done")

	return resolved, nil
}
