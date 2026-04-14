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

package sec

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
)

// PriceLookupFn returns the EOD close price for a given asset and date.
// Returns 0 if no price is available.
type PriceLookupFn func(compositeFigi string, eventDate time.Time) float64

// priceLookupFn is the active price lookup function. Set by fetchFundamentals
// when the published EOD view is available.
var priceLookupFn PriceLookupFn

// SetPriceLookupFn sets the package-level price lookup function. Passing nil
// disables market-data enrichment.
func SetPriceLookupFn(fn PriceLookupFn) {
	priceLookupFn = fn
}

// EnrichMarketData populates price-derived market-data fields on each
// Fundamental record. It groups records by DateKey and processes them in
// three phases:
//
//  1. Phase 1 (all dimensions): set Price, ShareFactor, FxUSD,
//     MarketCapitalization, EnterpriseValue, and PB from a price lookup.
//     Records with a zero price are skipped entirely.
//
//  2. Phase 2 (trailing/annual: ART, MRT, ARY, MRY): compute ratio fields
//     PE, PS, PE1, PS1, EVtoEBIT, EVtoEBITDA, DividendYield.
//
//     2b. PayoutRatio (all dimensions): DPS / EPS (basic) is computed
//     independently for every dimension (quarterly values differ from
//     trailing values).
//
//  3. Phase 3 (quarterly: ARQ, MRQ): copy ratio fields (but NOT PB or
//     PayoutRatio) from the corresponding trailing dimension within the
//     same DateKey group.
func EnrichMarketData(fundamentals []*data.Fundamental, lookupPrice PriceLookupFn) {
	// Group fundamentals by DateKey.
	type dateKey = time.Time

	groups := make(map[dateKey][]*data.Fundamental)
	for _, f := range fundamentals {
		groups[f.DateKey] = append(groups[f.DateKey], f)
	}

	// Sort date keys chronologically so MR dimensions can reference the
	// previous quarter's balance sheet for enterprise value.
	dateKeys := make([]time.Time, 0, len(groups))
	for dk := range groups {
		dateKeys = append(dateKeys, dk)
	}

	slices.SortFunc(dateKeys, func(a, b time.Time) int { return a.Compare(b) })

	// Track previous quarter's debt/cash for MR EV calculation.
	var (
		prevDebt, prevCash int64
		havePrev           bool
	)

	// Map of calendar-quarter date key → MRQ enterprise value. MRY copies
	// its EV from the MRQ at the fiscal year-end, not the latest MRQ before
	// the MRY date key. The fiscal year-end's calendar-quarter date is
	// NormalizeEventDate(ReportPeriod, "10-Q").
	mrqEVByDateKey := make(map[time.Time]int64)

	for _, dk := range dateKeys {
		group := groups[dk]

		// Build an index by Dimension for quick lookup within the group.
		byDim := make(map[string]*data.Fundamental, len(group))
		for _, f := range group {
			byDim[f.Dimension] = f
		}

		// Phase 1: price-based fields for all dimensions.
		for _, f := range group {
			price := lookupPrice(f.CompositeFigi, f.EventDate)
			if price == 0 {
				continue
			}

			mktCap := int64(price * float64(f.SharesBasic))

			// MR dimensions use the previous quarter's debt and cash for
			// enterprise value. Sharadar's MR price/shares reflect the most
			// recent filing, but the balance sheet data available at that
			// point is from the prior quarter's filing.
			debt := f.TotalDebt
			cash := f.CashAndEquivalents

			if havePrev && strings.HasPrefix(f.Dimension, "MR") {
				debt = prevDebt
				cash = prevCash
			}

			ev := mktCap + debt - cash

			// MRY copies its EV from the fiscal year-end MRQ. The fiscal
			// year-end's calendar-quarter key is the normalized report period.
			if f.Dimension == "MRY" && !f.ReportPeriod.IsZero() {
				fyEndQKey := NormalizeEventDate(f.ReportPeriod, "10-Q")
				if fyEV, ok := mrqEVByDateKey[fyEndQKey]; ok {
					ev = fyEV
				}
			}

			f.Price = price
			f.ShareFactor = 1.0
			f.FxUSD = 1.0
			f.MarketCapitalization = mktCap
			f.EnterpriseValue = ev

			if f.Equity != 0 {
				f.PB = float64(mktCap) / float64(f.Equity)
			}
		}

		// Save the MRQ EV keyed by its date key so MRY can look up the
		// fiscal year-end MRQ regardless of intervening quarters.
		if mrq, ok := byDim["MRQ"]; ok && mrq.EnterpriseValue != 0 {
			mrqEVByDateKey[dk] = mrq.EnterpriseValue
		}

		// Update previous quarter's debt/cash for the next iteration.
		// Use a quarterly dimension (ARQ preferred) because annual
		// dimensions (ARY/MRY) carry the fiscal year-end balance sheet
		// which may differ from the calendar quarter's balance sheet.
		for _, dim := range []string{"ARQ", "MRQ", "ART", "MRT"} {
			if f, ok := byDim[dim]; ok {
				prevDebt = f.TotalDebt
				prevCash = f.CashAndEquivalents
				havePrev = true

				break
			}
		}

		// Phase 2: ratio fields for trailing/annual dimensions.
		trailingDims := []string{"ART", "MRT", "ARY", "MRY"}
		for _, dim := range trailingDims {
			f, ok := byDim[dim]
			if !ok || f.Price == 0 {
				continue
			}

			mktCap := f.MarketCapitalization
			ev := f.EnterpriseValue

			if f.NetIncomeCommonStock != 0 {
				f.PE = float64(mktCap) / float64(f.NetIncomeCommonStock)
			}

			if f.Revenues != 0 {
				f.PS = float64(mktCap) / float64(f.Revenues)
			}

			if f.EPS != 0 {
				f.PE1 = f.Price / f.EPS
			}

			if f.Revenues != 0 && f.WeightedAverageShares != 0 {
				f.PS1 = f.Price / (float64(f.Revenues) / float64(f.WeightedAverageShares))
			} else if f.SalesPerShare != 0 {
				f.PS1 = f.Price / f.SalesPerShare
			}

			if f.EBIT != 0 {
				f.EVtoEBIT = int64(math.Round(float64(ev) / float64(f.EBIT)))
			}

			if f.EBITDA != 0 {
				f.EVtoEBITDA = math.Round(float64(ev)/float64(f.EBITDA)*1000) / 1000
			}

			if f.Price != 0 {
				f.DividendYield = math.Round(f.DividendsPerBasicCommonShare/f.Price*1000) / 1000
			}
		}

		// PayoutRatio = DPS / EPS (basic) -- does not depend on price or
		// market-data, so compute it for every dimension independently.
		for _, f := range group {
			if f.EPS != 0 {
				f.PayoutRatio = math.Round(f.DividendsPerBasicCommonShare/f.EPS*1000) / 1000
			}
		}

		// Phase 3: copy ratio fields from trailing to quarterly dimensions.
		// ART -> ARQ, MRT -> MRQ.
		quarterlyPairs := [][2]string{
			{"ART", "ARQ"},
			{"MRT", "MRQ"},
		}

		for _, pair := range quarterlyPairs {
			trailing, quarterly := pair[0], pair[1]

			src, srcOK := byDim[trailing]
			dst, dstOK := byDim[quarterly]

			if !srcOK || !dstOK {
				continue
			}

			// Only copy if the quarterly record has a valid price.
			if dst.Price == 0 {
				continue
			}

			dst.PE = src.PE
			dst.PS = src.PS
			dst.PE1 = src.PE1
			dst.PS1 = src.PS1
			dst.EVtoEBIT = src.EVtoEBIT
			dst.EVtoEBITDA = src.EVtoEBITDA
			dst.DividendYield = src.DividendYield
		}
	}
}

// NewEODPriceLookup returns a PriceLookupFn that queries the published EOD
// view for the unadjusted close price nearest to and on or before the given
// date (up to 7 days back to handle weekends and holidays). Returns 0 if no
// price is found; this is normal for historical gaps and is not treated as an
// error.
func NewEODPriceLookup(ctx context.Context, pool *pgxpool.Pool, eodViewName string) PriceLookupFn {
	query := fmt.Sprintf(
		`SELECT close FROM %s WHERE composite_figi = $1 AND event_date <= $2 AND event_date >= $3 ORDER BY event_date DESC LIMIT 1`,
		eodViewName,
	)

	return func(compositeFigi string, eventDate time.Time) float64 {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			log.Error().Err(err).Msg("failed to acquire connection for EOD price lookup")

			return 0
		}

		defer conn.Release()

		var close float64

		err = conn.QueryRow(ctx, query, compositeFigi, eventDate, eventDate.AddDate(0, 0, -7)).Scan(&close)
		if err != nil {
			return 0
		}

		return close
	}
}

// FindEODViewName loads all published views and returns the ViewName of the
// one whose DataTypeKey is "eod". Returns an empty string if no such view
// exists.
func FindEODViewName(ctx context.Context, pool *pgxpool.Pool) string {
	views, err := library.LoadPublishedViews(ctx, pool)
	if err != nil {
		log.Error().Err(err).Msg("failed to load published views")

		return ""
	}

	for _, v := range views {
		if v.DataTypeKey == "eod" {
			return v.ViewName
		}
	}

	return ""
}
