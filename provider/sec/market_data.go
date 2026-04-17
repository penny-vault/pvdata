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

// EnrichOption tunes EnrichMarketData behavior. Use WithMultiClass to enable
// the market-ratio share_factor formula for dual-class filers.
type EnrichOption func(*enrichConfig)

type enrichConfig struct {
	cf          *CompanyFacts
	siblingFigi string
}

// WithMultiClass supplies the per-class cover-page share facts (from
// cf.ClassShares) and the sibling share class's composite FIGI for the
// market-ratio share_factor formula:
//
//	share_factor = (A*A_price + B*B_price) / ((A+B) * our_price)
//
// A_shares and B_shares come from cf.ClassShares at the most recent filing on
// or before f.EventDate. A_price / B_price come from lookupPrice at that same
// filing date. When siblingFigi is empty or no class pair is found, the
// formula falls back to the legacy WAS_d / SharesBasic ratio.
func WithMultiClass(cf *CompanyFacts, siblingFigi string) EnrichOption {
	return func(c *enrichConfig) {
		c.cf = cf
		c.siblingFigi = siblingFigi
	}
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
func EnrichMarketData(fundamentals []*data.Fundamental, lookupPrice PriceLookupFn, opts ...EnrichOption) {
	var cfg enrichConfig
	for _, opt := range opts {
		opt(&cfg)
	}
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

	// shareFactorRawByFundamental holds the unrounded share_factor for each
	// fundamental. The stored f.ShareFactor is rounded to 3 decimals (to
	// match Sharadar's displayed column); Phase 2 ratio fields need the
	// unrounded value to avoid compounding rounding error.
	shareFactorRawByFundamental := make(map[*data.Fundamental]float64)

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

			// Compute share_factor for multi-class share companies.
			// When weighted-average diluted shares significantly exceed
			// shares outstanding, the company has multiple share classes
			// where the traded instrument represents a fraction of the
			// total economic ownership (e.g. BRK/B: 1 Class A = 1500
			// Class B). The share_factor bridges SharesBasic to the
			// total equivalent shares for market cap calculation.
			//
			// Preferred formula (when WithMultiClass is configured and both
			// class share counts + sibling price are available):
			//
			//	share_factor = (A*A_price + B*B_price) / ((A+B) * our_price)
			//
			// Sharadar uses this market-ratio formulation because A and B
			// shares do NOT trade at exactly the nominal conversion ratio
			// (e.g. 1500 for BRK); the actual A/B price ratio fluctuates a
			// fraction of a percent and share_factor reflects that.
			//
			// Fallback formula (single-class or when multi-class data is
			// unavailable): WAS_diluted / SharesBasic. This approximates
			// the market-ratio answer by assuming A_price / B_price equals
			// the legal conversion ratio.
			shareFactor := 1.0
			shareFactorRaw := 1.0

			if f.SharesBasic > 0 && f.WeightedAverageSharesDiluted > 0 {
				ratio := float64(f.WeightedAverageSharesDiluted) / float64(f.SharesBasic)
				if ratio > 1.1 {
					shareFactorRaw = ratio
					shareFactor = math.Round(ratio*1000) / 1000

					if mrRaw, mrSF, ok := computeMarketRatioShareFactor(cfg, f, price, lookupPrice); ok {
						shareFactorRaw = mrRaw
						shareFactor = mrSF
					}
				}
			}

			mktCap := int64(price * float64(f.SharesBasic) * shareFactorRaw)

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
			f.ShareFactor = shareFactor
			f.FxUSD = 1.0
			f.MarketCapitalization = mktCap
			f.EnterpriseValue = ev

			shareFactorRawByFundamental[f] = shareFactorRaw

			// For multi-class companies, WeightedAverageShares from the XBRL
			// tag represents the combined total across all share classes
			// (e.g. BRK/B: 2.16B B-equivalent including Class A). Sharadar
			// instead uses the per-class shares (same as SharesBasic) for
			// WeightedAverageShares, with share_factor bridging to the total.
			// This gives per-share metrics denominated in the traded class.
			if shareFactor > 1.1 && f.SharesBasic > 0 {
				f.WeightedAverageShares = int64(f.SharesBasic)
			}

			// For dual-class filers, per-share metrics derived off of
			// WeightedAverageShares need to divide by the B-equivalent share
			// count (WAS × share_factor) rather than the raw per-class count.
			// The initial MappingDerived computation ran before share_factor
			// was known, so rescale now with the unrounded share_factor.
			if shareFactorRaw > 1.1 && f.WeightedAverageShares > 0 {
				was := float64(f.WeightedAverageShares)

				if f.Revenues != 0 {
					f.SalesPerShare = math.Round(float64(f.Revenues)/(was*shareFactorRaw)*1000) / 1000
				}

				if f.Equity != 0 {
					f.BookValuePerShare = math.Round(float64(f.Equity)/(was*shareFactorRaw)*1000) / 1000
				}

				if f.FreeCashFlow != 0 {
					f.FreeCashFlowPerShare = math.Round(float64(f.FreeCashFlow)/(was*shareFactorRaw)*1000) / 1000
				}

				if f.TangibleAssetValue != 0 {
					f.TangibleAssetsBookValuePerShare = math.Round(float64(f.TangibleAssetValue)/(was*shareFactorRaw)*1000) / 1000
				}
			}

			if f.Equity != 0 {
				f.PB = math.Round(float64(mktCap)/float64(f.Equity)*1000) / 1000
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
				f.PE = math.Round(float64(mktCap)/float64(f.NetIncomeCommonStock)*1000) / 1000
			}

			if f.Revenues != 0 {
				f.PS = math.Round(float64(mktCap)/float64(f.Revenues)*1000) / 1000
			}

			if f.EPS != 0 {
				f.PE1 = math.Round(f.Price/f.EPS*1000) / 1000
			}

			// PS1 = Price / SalesPerShare. Compute from the unrounded ratio
			// (Price × WAS / Revenues) rather than dividing by the stored
			// SalesPerShare — which is already rounded to 3 decimals and
			// would compound rounding error. For dual-class filers (BRK/B),
			// WeightedAverageShares was overridden to SharesBasic (the raw
			// per-class count); scale by the unrounded share_factor so the
			// denominator is the B-equivalent share count Sharadar uses.
			if f.Revenues != 0 && f.WeightedAverageShares != 0 {
				scaledWAS := float64(f.WeightedAverageShares)
				if sfRaw, ok := shareFactorRawByFundamental[f]; ok && sfRaw > 1.1 {
					scaledWAS *= sfRaw
				}

				f.PS1 = math.Round(f.Price*scaledWAS/float64(f.Revenues)*1000) / 1000
			} else if f.SalesPerShare != 0 {
				f.PS1 = math.Round(f.Price/f.SalesPerShare*1000) / 1000
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

// computeMarketRatioShareFactor implements Sharadar's share_factor formula
// for dual-class filers:
//
//	share_factor = (A*A_price + B*B_price) / ((A+B) * our_price)
//
// where A/B are the Class A and Class B raw cover-page share counts from the
// most recent 10-K/10-Q on or before f.EventDate, A_price/B_price are the
// corresponding closing prices on that filing date, and our_price is the
// price of the class currently being processed. Returns (rawSF, roundedSF,
// true) when all four inputs are available and positive; (0, 0, false)
// otherwise so the caller can fall back to the legacy WAS_d / SharesBasic
// ratio. The raw (unrounded) share_factor is what Sharadar uses internally
// for market_capitalization; the rounded value matches the stored display
// column (3 decimals).
func computeMarketRatioShareFactor(cfg enrichConfig, f *data.Fundamental, ourPrice float64, lookupPrice PriceLookupFn) (float64, float64, bool) {
	if cfg.cf == nil || cfg.siblingFigi == "" {
		return 0, 0, false
	}

	filed, classA, classB, ok := resolveClassSharesAsOf(cfg.cf, f.EventDate)
	if !ok {
		return 0, 0, false
	}

	siblingPrice := lookupPrice(cfg.siblingFigi, filed)
	if siblingPrice <= 0 {
		return 0, 0, false
	}

	ourPriceAtFiling := lookupPrice(f.CompositeFigi, filed)
	if ourPriceAtFiling <= 0 {
		ourPriceAtFiling = ourPrice
	}

	// Map raw class counts to "our" and "sibling" by price magnitude: the
	// senior class trades higher (e.g. BRK.A ≈ 1500 × BRK.B), so whichever
	// of our price vs sibling price is higher identifies the senior/junior
	// roles independent of ticker-suffix conventions.
	var ourShares, siblingShares float64

	if ourPriceAtFiling < siblingPrice {
		ourShares = classB
		siblingShares = classA
	} else {
		ourShares = classA
		siblingShares = classB
	}

	if ourShares <= 0 || siblingShares <= 0 {
		return 0, 0, false
	}

	totalShares := ourShares + siblingShares
	totalMarketCap := siblingShares*siblingPrice + ourShares*ourPriceAtFiling

	sf := totalMarketCap / (totalShares * ourPriceAtFiling)

	return sf, math.Round(sf*1000) / 1000, true
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
