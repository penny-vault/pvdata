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
	"strconv"
	"strings"
	"time"

	"github.com/rs/zerolog"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/provider/sec"
)

// cikMisattributionGraceDays is how far Massive's supplied CIK's
// earliest SEC filing may postdate the asset's earliest known trading
// evidence before we flag the CIK as misattributed. One year is well
// outside any plausible filing lag (companies file their first 10-K
// within months of going public, not years).
const cikMisattributionGraceDays = 365

// correctMisattributedCIK detects and corrects cases where Massive
// has tagged a ticker with a CIK that belongs to a different entity.
// The signal is a time gap: the asset has evidence of trading (walk
// firstSeen for ticker+CIK, or EOD archive first bar of the lifecycle
// containing asset.ValidFor) more than a year before SEC's earliest
// filing under the supplied CIK. When the gap is wide enough, the
// function searches SEC by the asset's name for a candidate CIK and
// swaps if the candidate passes the same gates the walk-time
// ResolveAssetTypeWithCIKCorrection wrapper uses (different CIK,
// no ticker conflict against the candidate's SEC tickers list).
func (api *massiveAssetFetcher) correctMisattributedCIK(ctx context.Context, asset *data.Asset) {
	if strings.TrimSpace(asset.CIK) == "" || strings.TrimSpace(asset.Name) == "" {
		return
	}

	cikN, err := strconv.Atoi(asset.CIK)
	if err != nil || cikN <= 0 {
		return
	}

	earliestEvidence := api.earliestTradingEvidence(asset)
	if earliestEvidence.IsZero() {
		return
	}

	sub, err := sec.FetchSubmissions(ctx, cikN)
	if err != nil || sub == nil {
		return
	}

	earliest := sub.EarliestFilingDate()
	if earliest == "" {
		return
	}

	earliestT, err := time.Parse("2006-01-02", earliest)
	if err != nil {
		return
	}

	if earliestT.Sub(earliestEvidence) < time.Duration(cikMisattributionGraceDays)*24*time.Hour {
		return
	}

	correctedCIK, ok := sec.FindCIKByName(ctx, asset.Name, earliestEvidence.Year())
	if !ok {
		return
	}

	if strings.TrimLeft(correctedCIK, "0") == strings.TrimLeft(asset.CIK, "0") {
		return
	}

	candidateN, err := strconv.Atoi(correctedCIK)
	if err != nil || candidateN <= 0 {
		return
	}

	candidateSub, err := sec.FetchSubmissions(ctx, candidateN)
	if err != nil || candidateSub == nil {
		return
	}

	if len(candidateSub.Tickers) > 0 && !candidateClaimsTicker(candidateSub.Tickers, asset.Ticker) {
		return
	}

	logger := zerolog.Ctx(ctx)
	logger.Info().
		Str("Ticker", asset.Ticker).
		Str("OldCIK", asset.CIK).
		Str("NewCIK", correctedCIK).
		Str("OldCIKName", sub.Name).
		Str("NewCIKName", candidateSub.Name).
		Time("EarliestEvidence", earliestEvidence).
		Str("OldCIKEarliestFiling", earliest).
		Msg("massive: corrected misattributed CIK via SEC name search at enrich time")

	asset.CIK = correctedCIK
}

// earliestTradingEvidence returns the earliest non-zero date for which
// we have direct evidence the asset was trading: the walk's first-
// seen date for the (ticker, CIK) or (ticker, FIGI) pair, or the EOD
// archive's first bar of the lifecycle containing asset.ValidFor.
// Returns the zero time when no evidence is available.
func (api *massiveAssetFetcher) earliestTradingEvidence(asset *data.Asset) time.Time {
	var earliest time.Time

	if asset.CIK != "" && api.walkWindowsByCIK != nil {
		if win, ok := api.walkWindowsByCIK[asset.Ticker+":"+asset.CIK]; ok && !win.firstSeen.IsZero() {
			if earliest.IsZero() || win.firstSeen.Before(earliest) {
				earliest = win.firstSeen
			}
		}
	}

	if asset.CompositeFigi != "" && api.walkWindowsByFigi != nil {
		if win, ok := api.walkWindowsByFigi[asset.Ticker+":"+asset.CompositeFigi]; ok && !win.firstSeen.IsZero() {
			if earliest.IsZero() || win.firstSeen.Before(earliest) {
				earliest = win.firstSeen
			}
		}
	}

	if asset.Name != "" && api.walkWindowsByName != nil {
		if win, ok := api.walkWindowsByName[asset.Ticker+":name:"+asset.Name]; ok && !win.firstSeen.IsZero() {
			if earliest.IsZero() || win.firstSeen.Before(earliest) {
				earliest = win.firstSeen
			}
		}
	}

	if archive := api.eodArchiveForRun(); archive != nil {
		ranges := archive.Ranges(asset.Ticker)
		if len(ranges) > 0 {
			assetDate := asset.ValidFor
			if assetDate.IsZero() {
				assetDate = time.Now()
			}

			lifecycle, found := lifecycleContaining(ranges, assetDate)
			if !found && assetDate.After(ranges[len(ranges)-1].End) {
				lifecycle = ranges[len(ranges)-1]
				found = true
			}

			if found && !lifecycle.Start.IsZero() {
				if earliest.IsZero() || lifecycle.Start.Before(earliest) {
					earliest = lifecycle.Start
				}
			}
		}
	}

	return earliest
}

// candidateClaimsTicker reports whether the candidate CIK's SEC
// tickers list contains the asset's ticker (case- and whitespace-
// insensitive). An empty tickers list returns false; the caller is
// expected to treat empty as silent rather than as a positive
// conflict signal.
func candidateClaimsTicker(list []string, ticker string) bool {
	target := strings.ToUpper(strings.TrimSpace(ticker))
	if target == "" {
		return false
	}

	for _, t := range list {
		if strings.ToUpper(strings.TrimSpace(t)) == target {
			return true
		}
	}

	return false
}
