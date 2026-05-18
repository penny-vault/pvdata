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
// one keyed to the asset's CIK (when available) or ticker+name.
//
// The gate only fires on assets whose CompositeFigi was empty before
// figi.Enrich ran and was filled by figi.Enrich's OpenFIGI step. FIGIs
// that already existed on the asset, FIGIs that figi.Enrich left empty,
// and synthetic FIGIs (PVG prefix) all pass through unchanged.
func (api *massiveAssetFetcher) enrichFigi(ctx context.Context, assets ...*data.Asset) {
	before := make(map[*data.Asset]string, len(assets))
	for _, a := range assets {
		before[a] = a.CompositeFigi
	}

	figi.Enrich(ctx, assets...)

	logger := zerolog.Ctx(ctx)

	for _, a := range assets {
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

// figiBelongsToDifferentLifecycle returns true when Massive has assigned
// the proposed FIGI to a different lifecycle of asset.Ticker than the
// one asset itself belongs to. The lifecycle of an observation is the
// EOD archive range (gap-split) that contains its date.
//
// Returns false (the enrichment is allowed to stand) in any situation
// where the archive cannot tell us the lifecycle: no archive loaded,
// no per-ticker ranges, no walk-side or today-side Massive assignment
// of the FIGI to compare against. The one anomaly case — an asset
// whose date falls outside every known range for its ticker — is
// logged at warn level before returning false, because Massive
// should not return assets for dates outside its trading-close
// coverage window.
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
func lifecycleContaining(ranges []dateRange, t time.Time) (dateRange, bool) {
	for _, r := range ranges {
		if t.Before(r.Start) || t.After(r.End) {
			continue
		}

		return r, true
	}

	return dateRange{}, false
}

// sameRange reports whether two ranges have identical Start and End.
// Used to compare two lifecycle assignments by value rather than by
// slice index, so the comparison is robust to range slices that may
// be reordered or copied between calls.
func sameRange(a, b dateRange) bool {
	return a.Start.Equal(b.Start) && a.End.Equal(b.End)
}
