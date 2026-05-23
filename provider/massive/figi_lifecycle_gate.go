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
	"time"

	"github.com/rs/zerolog"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/figi"
)

// enrichFigi wraps figi.Enrich with a Massive-specific lifecycle gate.
// The gate refuses an enrichment-derived FIGI when Massive has already
// assigned that same FIGI to a different lifecycle of the same ticker,
// using the EOD archive's gap-split per-ticker ranges to define what
// counts as a lifecycle. A rejected FIGI is replaced with a synthetic
// one keyed to the asset's CIK (when available) or ticker+name. The
// gate only fires on assets whose CompositeFigi was empty before
// figi.Enrich ran and was filled by figi.Enrich's OpenFIGI step;
// pre-existing and synthetic FIGIs pass through unchanged.
func (api *massiveAssetFetcher) enrichFigi(ctx context.Context, assets ...*data.Asset) {
	before := make(map[*data.Asset]string, len(assets))
	for _, a := range assets {
		before[a] = a.CompositeFigi
	}

	figi.Enrich(ctx, assets...)

	logger := zerolog.Ctx(ctx)

	for _, a := range assets {
		if applyFigiOverride(logger, a) {
			continue
		}

		if before[a] != "" {
			continue
		}

		if a.CompositeFigi == "" {
			continue
		}

		if figi.IsSyntheticFIGI(a.CompositeFigi) {
			continue
		}

		if !api.figiBelongsToDifferentLifecycle(ctx, a, a.CompositeFigi) {
			continue
		}

		rejected := a.CompositeFigi
		a.CompositeFigi = ""
		a.ShareClassFigi = ""

		switch {
		case a.CIK != "":
			a.CompositeFigi = figi.GenerateSyntheticFIGIFromCIK(a.CIK, a.Ticker)
		case a.Name != "":
			a.CompositeFigi = figi.GenerateSyntheticFIGI(a.Ticker, a.Name)
		}

		logger.Info().
			Str("Ticker", a.Ticker).
			Str("RejectedFigi", rejected).
			Str("ReplacementFigi", a.CompositeFigi).
			Time("ValidFor", a.ValidFor).
			Msg("massive: rejected enrichment-derived FIGI because it belongs to a different lifecycle of the ticker; replaced with synthetic")
	}
}

// knownTwoAssetTicker declares a ticker that has been held by two
// distinct entities across a transition the EOD archive cannot
// detect — typically a same-day spin-off whose price discontinuity
// is the only signal that the pre- and post-transition bars belong
// to different legal entities. Listed explicitly here, with both
// entities and the transition date baked in, so the pipeline
// records them as two assets with non-overlapping windows instead
// of collapsing them into one.
type knownTwoAssetTicker struct {
	Ticker       string
	Predecessor  knownAsset
	Successor    knownAsset
	TransitionAt string // YYYY-MM-DD: predecessor's last trading day
}

// knownAsset describes one of the two assets declared by a
// knownTwoAssetTicker. CompositeFigi may be "" to mean "mint a
// synthetic FIGI from CIK" — used for entities that never had a
// real FIGI of their own.
type knownAsset struct {
	CIK           string
	CompositeFigi string
}

// knownTwoAssetTickers is the explicit list of tickers that need
// the override. Each entry says: "this ticker has been held by two
// distinct entities; here is each entity's FIGI and the date one
// took over from the other."
var knownTwoAssetTickers = []knownTwoAssetTicker{
	{
		// Alcoa Inc was the AA ticker holder from 1888 (well before
		// our EOD coverage starts in 2003) through the 2016 Alcoa
		// Corp spin-off. Alcoa Corporation took the AA ticker on
		// 2016-11-01, the first regular trading day after the formal
		// split. The transition is invisible to the EOD archive's
		// 20-day gap rule because it is a single trading day, and
		// Massive labels every AA bar with Alcoa Corp's composite
		// FIGI regardless of date. The only signal of two entities
		// is the ~22% overnight price drop between 2016-10-31 close
		// ($28.72) and 2016-11-01 open ($22.10).
		Ticker:       "AA",
		Predecessor:  knownAsset{CIK: "0000004281", CompositeFigi: ""},
		Successor:    knownAsset{CIK: "0001675149", CompositeFigi: "BBG00B3T3HD3"},
		TransitionAt: "2016-10-31",
	},
}

// applyFigiOverride rewrites the asset's composite FIGI, listing,
// and delisting fields when the asset matches one of the entities
// declared in knownTwoAssetTickers. Returns true when an override
// applied so the caller can skip the rest of the per-asset
// enrichment loop.
func applyFigiOverride(logger *zerolog.Logger, asset *data.Asset) bool {
	if asset.CIK == "" {
		return false
	}

	for _, entry := range knownTwoAssetTickers {
		if entry.Ticker != asset.Ticker {
			continue
		}

		switch asset.CIK {
		case entry.Predecessor.CIK:
			rewriteOverride(logger, asset, entry.Predecessor.CompositeFigi, "", entry.TransitionAt, entry.Ticker)

			return true
		case entry.Successor.CIK:
			rewriteOverride(logger, asset, entry.Successor.CompositeFigi, nextCalendarDay(entry.TransitionAt), "", entry.Ticker)

			return true
		}
	}

	return false
}

// rewriteOverride applies a per-asset override and logs the diff.
// An empty figi means "mint a synthetic FIGI from CIK"; an empty
// listing or delisting means "leave the field as the algorithm
// chose."
func rewriteOverride(logger *zerolog.Logger, asset *data.Asset, newFigi, newListing, newDelisting, ticker string) {
	if newFigi == "" {
		newFigi = figi.GenerateSyntheticFIGIFromCIK(asset.CIK, ticker)
	}

	event := logger.Info().
		Str("Ticker", asset.Ticker).
		Str("CIK", asset.CIK)

	if asset.CompositeFigi != newFigi {
		event = event.Str("OldFigi", asset.CompositeFigi).Str("NewFigi", newFigi)

		asset.CompositeFigi = newFigi
		asset.ShareClassFigi = ""
	}

	if newListing != "" && asset.ListingDate != newListing {
		event = event.Str("OldListingDate", asset.ListingDate).Str("NewListingDate", newListing)

		asset.ListingDate = newListing
	}

	if newDelisting != "" && asset.DelistingDate != newDelisting {
		event = event.Str("OldDelistingDate", asset.DelistingDate).Str("NewDelistingDate", newDelisting)

		asset.DelistingDate = newDelisting
	}

	event.Msg("massive: applied known-two-asset-ticker override")
}

// nextCalendarDay returns iso+1 as a YYYY-MM-DD string, or "" if
// iso does not parse. Used to derive the successor's listing date
// from the predecessor's transition (last trading) day.
func nextCalendarDay(iso string) string {
	t, err := time.Parse("2006-01-02", iso)
	if err != nil {
		return ""
	}

	return t.AddDate(0, 0, 1).Format("2006-01-02")
}

// figiBelongsToDifferentLifecycle returns true when Massive has assigned
// the proposed FIGI to a different lifecycle of asset.Ticker than the
// one asset itself belongs to. The lifecycle of an observation is the
// EOD archive range (gap-split) that contains its date. Returns false
// (the enrichment is allowed to stand) in any situation where the
// archive cannot tell us the lifecycle: no archive loaded, no
// per-ticker ranges, or no walk-side or today-side Massive
// assignment of the FIGI to compare against.
func (api *massiveAssetFetcher) figiBelongsToDifferentLifecycle(ctx context.Context, asset *data.Asset, candidate string) bool {
	if candidate == "" {
		return false
	}

	archive := api.eodArchiveForRun()
	if archive == nil {
		return false
	}

	ranges := archive.Ranges(asset.Ticker)
	if len(ranges) == 0 {
		return false
	}

	assetDate := asset.ValidFor
	if assetDate.IsZero() {
		assetDate = time.Now()
	}

	assetRange, found := lifecycleContaining(ranges, assetDate)
	if !found {
		zerolog.Ctx(ctx).Warn().
			Str("Ticker", asset.Ticker).
			Time("ValidFor", asset.ValidFor).
			Int("ArchiveRangeCount", len(ranges)).
			Msg("massive: asset date does not fall inside any EOD archive lifecycle range for ticker; skipping figi lifecycle gate")

		return false
	}

	walkKey := asset.Ticker + ":" + candidate
	if win, ok := api.walkWindowsByFigi[walkKey]; ok {
		massiveDate := win.firstSeen
		if massiveDate.IsZero() {
			massiveDate = win.lastSeen
		}

		if !massiveDate.IsZero() {
			if massiveRange, ok := lifecycleContaining(ranges, massiveDate); ok {
				if !sameRange(assetRange, massiveRange) {
					return true
				}
			}
		}
	}

	return false
}

// lifecycleContaining returns the first range in ranges whose
// [Start, End] inclusive window contains t. Returns (zero, false) when
// t lies outside every range.
//
// Both sides of the comparison are normalized to calendar-day
// precision before testing. Ranges come from the EOD archive index
// at YYYY-MM-DD granularity (UTC midnight), but t can be a full
// timestamp in any timezone (walk LastSeen, asset ValidFor in EST,
// etc.). Raw timestamp comparison would reject otherwise-matching
// observations whose time-of-day pushes them past the range end in
// UTC — e.g., 2019-12-06T21:27:06-05:00 is past 2019-12-06T00:00:00Z
// as a timestamp but is the same calendar day as the range's End
// 2019-12-06. Date-only comparison preserves the intent that the
// range is a YYYY-MM-DD window.
func lifecycleContaining(ranges []dateRange, t time.Time) (dateRange, bool) {
	tDay := dateOnly(t)

	for _, r := range ranges {
		rStart := dateOnly(r.Start)
		rEnd := dateOnly(r.End)

		if tDay.Before(rStart) || tDay.After(rEnd) {
			continue
		}

		return r, true
	}

	return dateRange{}, false
}

// dateOnly returns UTC midnight of t's local calendar day (using
// t.Date() so the year/month/day are extracted in t's own timezone).
// Used to compare timestamps against day-precision range bounds
// without timezone-induced day rollover.
func dateOnly(t time.Time) time.Time {
	if t.IsZero() {
		return t
	}

	y, m, d := t.Date()

	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

// sameRange reports whether two ranges have identical Start and End.
// Used to compare two lifecycle assignments by value rather than by
// slice index, so the comparison is robust to range slices that may
// be reordered or copied between calls.
func sameRange(a, b dateRange) bool {
	return a.Start.Equal(b.Start) && a.End.Equal(b.End)
}

// previousLifecycleEnd returns the End of the range immediately
// preceding lifecycle in ranges, or zero when lifecycle is the
// earliest range (or is absent from the slice). Used to bound a
// per-lifecycle asset's MassiveReferenceListingDate: a Massive
// list_date older than the previous lifecycle's end belongs to a
// different (earlier) entity on the same ticker and must not be
// accepted as this asset's listing.
func previousLifecycleEnd(ranges []dateRange, lifecycle dateRange) time.Time {
	var prevEnd time.Time

	for _, r := range ranges {
		if !r.End.Before(lifecycle.Start) {
			continue
		}

		if prevEnd.IsZero() || r.End.After(prevEnd) {
			prevEnd = r.End
		}
	}

	return prevEnd
}
