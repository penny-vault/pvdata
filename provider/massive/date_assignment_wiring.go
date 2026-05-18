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
package massive

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider/sec"
)

// secRegistrationFormPrefix returns the SEC form prefix that signals
// the registration / launch filing for a given asset type:
//
//   - ETF, MutualFund -> "N-1A"
//   - CEF             -> "N-2"
//   - everything else -> "" (caller should consult the CIK's earliest
//     filing of any kind, but only when the CIK belongs to a single
//     ticker — multi-ticker CIKs are sponsor-level and meaningless as
//     a per-asset listing signal)
func secRegistrationFormPrefix(t data.AssetType) string {
	switch t {
	case data.ETF, data.MutualFund:
		return "N-1A"
	case data.CEF:
		return "N-2"
	}

	return ""
}

// buildDateCandidates assembles a DateCandidates struct for a single
// asset by reading the per-ticker Massive reference values already on
// the asset, the walk-window indexes on api, the per-lifecycle EOD
// archive range that contains the asset's observation date, and the
// SEC submissions cache (via sec.FetchSubmissions, which is cache-
// backed so a repeat call costs nothing). The result is the
// single-asset input to AssignDatesForTicker.
//
// MassiveEODArchiveFirstBar and MassiveEODArchiveLastBar come from the
// archive range that contains asset.ValidFor. When two assets share a
// ticker, each gets the first and last bar of its own lifecycle, not
// the ticker-wide span. The CoverageStart and CoverageEnd fields
// remain archive-wide so the edge-buffer check in eodFirstBarUsableAsValue
// still rejects the lifecycle of an asset whose first bar sits at the
// start of our coverage.
func (api *massiveAssetFetcher) buildDateCandidates(ctx context.Context, asset *data.Asset) DateCandidates {
	c := DateCandidates{
		AssetType: asset.AssetType,
		Active:    asset.Active,
	}

	if t := parseISODate(asset.ListingDate); !t.IsZero() {
		c.MassiveReferenceListingDate = t
	}

	if t := parseISODate(asset.DelistingDate); !t.IsZero() {
		c.MassiveReferenceDelistingDate = t
	}

	if win, ok := api.lookupWalkWindow(asset); ok {
		c.MassiveReferenceWalkFirstSeen = win.firstSeen
		c.MassiveReferenceWalkLastSeen = win.lastSeen
		c.MassiveReferenceWalkStart = api.walkStart
		c.MassiveReferenceWalkEnd = api.walkEnd
	}

	if archive := api.eodArchiveForRun(); archive != nil {
		coverageStart, coverageEnd := archive.Coverage()
		c.MassiveEODArchiveCoverageStart = coverageStart
		c.MassiveEODArchiveCoverageEnd = coverageEnd

		ranges := archive.Ranges(asset.Ticker)
		if len(ranges) > 0 {
			assetDate := asset.ValidFor
			if assetDate.IsZero() {
				assetDate = time.Now()
			}

			lifecycle, found := lifecycleContaining(ranges, assetDate)
			if !found && assetDate.After(ranges[len(ranges)-1].End) {
				// Active assets typically arrive with a ValidFor set
				// to today (or zero, which we default to now above).
				// The archive's latest bar usually lags by a few days,
				// so today's snapshot will sit past the latest range's
				// End. Treat anything past every known range as the
				// most recent lifecycle, since the asset is by
				// definition still part of that lifecycle.
				lifecycle = ranges[len(ranges)-1]
				found = true
			}

			if found {
				c.MassiveEODArchiveFirstBar = lifecycle.Start
				c.MassiveEODArchiveLastBar = lifecycle.End
			} else {
				zerolog.Ctx(ctx).Warn().
					Str("Ticker", asset.Ticker).
					Time("ValidFor", asset.ValidFor).
					Int("ArchiveRangeCount", len(ranges)).
					Msg("massive: asset date does not fall inside any EOD archive lifecycle range; skipping per-lifecycle EOD candidates")
			}
		}
	}

	if asset.CIK != "" {
		if cikInt, err := strconv.Atoi(asset.CIK); err == nil && cikInt > 0 {
			if sub, err := sec.FetchSubmissions(ctx, cikInt); err == nil && sub != nil {
				prefix := secRegistrationFormPrefix(asset.AssetType)
				c.SECFormPrefix = prefix

				switch {
				case prefix != "":
					// Fund types: the CIK is the sponsor, so the
					// per-fund registration form (N-1A for ETF/MF,
					// N-2 for CEF) is the right SEC signal. The
					// sponsor's earliest filing of any kind is wrong
					// for a fund and is not used here.
					if d := sub.EarliestFilingDateForForm(prefix); d != "" {
						c.SECEarliestFilingMatchingForm = parseISODate(d)
					}
				case asset.AssetType != data.ETN:
					// Non-fund, non-ETN: the CIK is the issuer (often
					// shared across class shares like BF/A and BF/B,
					// or BRK/A and BRK/B). Use the CIK's earliest
					// filing of any kind as a last-resort listing
					// lower bound. ETN is excluded because a debt
					// instrument's prospectus filing is far enough
					// from the listing that the date is misleading.
					if d := sub.EarliestFilingDate(); d != "" {
						c.SECEarliestFilingMatchingForm = parseISODate(d)
					}
				}
			}
		}
	}

	return c
}

// eodArchiveForRun returns the per-fetcher EOD archive index, loaded
// lazily on first call. The archive lives under
// parquet_backup_dir/<eod_subscription_slug>, where eod_subscription
// is the Massive subscription whose data_types include "eod" — not
// the current fetcher's asset-description subscription. Returns nil
// when parquet_backup_dir is unset, the EOD subscription cannot be
// located, or the archive directory does not exist.
func (api *massiveAssetFetcher) eodArchiveForRun() *EODArchive {
	api.eodArchiveOnce.Do(func() {
		baseDir := strings.TrimSpace(viper.GetString("parquet_backup_dir"))
		if baseDir == "" {
			return
		}

		eodSub, err := api.findEODSubscription()
		if err != nil {
			log.Warn().Err(err).Msg("massive: could not locate EOD subscription; date assignment will run without EOD candidates")

			return
		}

		if eodSub == nil {
			return
		}

		rootDir := filepath.Join(baseDir, subscriptionBackupSlug(eodSub))

		archive, err := loadOrReuseEODArchive(rootDir)
		if err != nil {
			log.Warn().
				Err(err).
				Str("Root", rootDir).
				Msg("massive: eod archive load failed; date assignment will run without EOD candidates")

			return
		}

		coverageStart, coverageEnd := archive.Coverage()
		log.Info().
			Str("Root", rootDir).
			Int("Tickers", archive.TickerCount()).
			Time("CoverageStart", coverageStart).
			Time("CoverageEnd", coverageEnd).
			Msg("massive: eod archive loaded for date assignment")

		api.eodArchive = archive
	})

	return api.eodArchive
}

// findEODSubscription returns the Massive subscription whose
// data_types include "eod". Returns (nil, nil) when no such
// subscription exists; a non-nil error surfaces a DB lookup failure
// so the caller can log it.
func (api *massiveAssetFetcher) findEODSubscription() (*library.Subscription, error) {
	if api.subscription == nil || api.subscription.Library == nil {
		return nil, nil
	}

	subs, err := api.subscription.Library.Subscriptions(context.Background())
	if err != nil {
		return nil, err
	}

	for _, sub := range subs {
		if sub == nil || sub.Provider != "massive" {
			continue
		}

		for _, dt := range sub.DataTypes {
			if dt == "eod" {
				return sub, nil
			}
		}
	}

	return nil, nil
}

// lookupWalkWindow returns the walk window for asset, trying the FIGI
// index first and falling back to the CIK index.
func (api *massiveAssetFetcher) lookupWalkWindow(asset *data.Asset) (walkWindow, bool) {
	if asset.CompositeFigi != "" && api.walkWindowsByFigi != nil {
		if win, ok := api.walkWindowsByFigi[asset.Ticker+":"+asset.CompositeFigi]; ok {
			return win, true
		}
	}

	if asset.CIK != "" && api.walkWindowsByCIK != nil {
		if win, ok := api.walkWindowsByCIK[asset.Ticker+":"+asset.CIK]; ok {
			return win, true
		}
	}

	return walkWindow{}, false
}

// isLookbackRun reports whether the walk span is wide enough to count
// as a backfill / lookback (operator passed `--start-date` or a long
// `--lookback`). Reuses the same threshold the rest of the package
// uses to gate other backfill-only behaviors (see filterAssetsByLastUpdated).
// Returns false when the walk window is not set, which is the daily-
// update case where date-assignment cross-checks would only add
// noise — Massive's per-ticker reference is the live truth and there
// is nothing historical to reconcile against.
func (api *massiveAssetFetcher) isLookbackRun() bool {
	if api.walkStart.IsZero() || api.walkEnd.IsZero() {
		return false
	}

	return api.walkEnd.Sub(api.walkStart) > defaultAssetLookback
}

// assignDates runs the date-assignment algorithm for one asset in
// isolation. This is the single-asset entry point used by callers
// that operate on assets one at a time (the pre-figi seeding step in
// filterAssetsByLastUpdated), where rough dates are good enough and
// no other same-ticker asset is in scope.
//
// Use assignDatesForGroup when two or more assets share a ticker and
// the cross-asset reconciliation in AssignDatesForTicker has to see
// them together.
//
// Daily runs short-circuit: when isLookbackRun reports false there is
// nothing historical to reconcile and the per-ticker reference value
// already on the asset is the right answer.
func (api *massiveAssetFetcher) assignDates(ctx context.Context, asset *data.Asset) {
	api.assignDatesForGroup(ctx, []*data.Asset{asset})
}

// assignDatesForGroup runs the date-assignment algorithm across a
// slice of assets that all share the same ticker. Passing every
// same-ticker asset together lets AssignDatesForTicker apply the
// cross-asset reconciliation rules (no overlapping windows, the
// boundary-disagreement check) that a single-asset call cannot see.
// Each asset's chosen listing and delisting dates are written back
// onto the asset in place. Active is flipped to false on any asset
// whose chosen delisting is in the past so the lifecycle flag stays
// consistent with the date.
//
// Daily runs short-circuit: when isLookbackRun reports false there
// is nothing historical to reconcile and the per-ticker reference
// values already on the assets are the right answer.
func (api *massiveAssetFetcher) assignDatesForGroup(ctx context.Context, assets []*data.Asset) {
	if !api.isLookbackRun() || len(assets) == 0 {
		return
	}

	logger := zerolog.Ctx(ctx)

	candidates := make([]DateCandidates, len(assets))
	for i, asset := range assets {
		candidates[i] = api.buildDateCandidates(ctx, asset)
	}

	prev := func(t time.Time) time.Time {
		formatted := api.previousTradingDay(ctx, t)
		if formatted == "" {
			return time.Time{}
		}

		parsed, err := time.Parse("2006-01-02", formatted)
		if err != nil {
			return time.Time{}
		}

		return parsed
	}

	results := AssignDatesForTicker(logger, candidates, prev)

	now := time.Now()

	for i := range assets {
		if i >= len(results) {
			break
		}

		r := results[i]

		assets[i].ListingDate = formatISODate(r.ListingDate)
		assets[i].DelistingDate = formatISODate(r.DelistingDate)

		if !r.DelistingDate.IsZero() && assets[i].Active && r.DelistingDate.Before(now) {
			assets[i].Active = false
		}
	}
}

// parseISODate parses an "YYYY-MM-DD" string into a time.Time. Empty
// or malformed input returns the zero time.Time.
func parseISODate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}

	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return time.Time{}
	}

	return t
}

// formatISODate formats t as "YYYY-MM-DD", or returns "" for the
// zero time.Time.
func formatISODate(t time.Time) string {
	if t.IsZero() {
		return ""
	}

	return t.Format("2006-01-02")
}
