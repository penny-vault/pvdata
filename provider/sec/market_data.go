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

// WithMultiClass supplies per-class cover-page shares (cf.ClassShares) and the
// sibling share class's composite FIGI for the market-ratio share_factor
// formula. When siblingFigi is empty or no class pair is found, the formula
// falls back to the legacy WAS_d / SharesBasic ratio.
func WithMultiClass(cf *CompanyFacts, siblingFigi string) EnrichOption {
	return func(c *enrichConfig) {
		c.cf = cf
		c.siblingFigi = siblingFigi
	}
}

// EnrichMarketData populates price-derived fields on each Fundamental record.
// Groups records by DateKey and processes three phases: (1) price-derived
// fields for all dimensions, (2) ratio fields for trailing/annual dimensions
// plus PayoutRatio for all dimensions, (3) copy ratios from trailing to
// quarterly dimensions within the same DateKey group.
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

	// MRY copies its EV from the MRQ at the fiscal year-end (not the latest
	// MRQ before the MRY date key), so we key MRQ EVs by their date.
	mrqEVByDateKey := make(map[time.Time]int64)

	// f.ShareFactor is rounded to 3 decimals to match Sharadar's displayed
	// column; Phase 2 ratios need the unrounded value to avoid compounding
	// rounding error.
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

			// Multi-class share_factor bridges SharesBasic to the total
			// economic share count. Preferred: market-ratio formula via
			// WithMultiClass. Fallback: WAS_diluted / SharesBasic, which
			// assumes A/B trade at the legal conversion ratio (e.g. 1500
			// for BRK), though they actually deviate by a fraction of a
			// percent.
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

			// MR dimensions use the previous quarter's debt/cash for EV:
			// MR price/shares reflect the most recent filing, but the
			// balance sheet at that point is from the prior quarter.
			debt := f.TotalDebt
			cash := f.CashAndEquivalents

			if havePrev && strings.HasPrefix(f.Dimension, "MR") {
				debt = prevDebt
				cash = prevCash
			}

			ev := mktCap + debt - cash

			// MRY copies its EV from the fiscal year-end MRQ.
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

			// Sharadar denominates per-share metrics in the traded class:
			// WeightedAverageShares = SharesBasic, with share_factor bridging
			// to the multi-class total.
			if shareFactor > 1.1 && f.SharesBasic > 0 {
				f.WeightedAverageShares = int64(f.SharesBasic)
			}

			// Per-share metrics need to divide by the B-equivalent share count
			// (WAS × share_factor). MappingDerived ran before share_factor was
			// known, so rescale with the unrounded value.
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

		if mrq, ok := byDim["MRQ"]; ok && mrq.EnterpriseValue != 0 {
			mrqEVByDateKey[dk] = mrq.EnterpriseValue
		}

		// Update previous quarter's debt/cash for the next iteration. Use a
		// quarterly dimension; annual dimensions carry the fiscal year-end
		// balance sheet, which may differ from the calendar quarter's.
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
			// rather than the stored SalesPerShare (already rounded), and
			// scale WAS by the unrounded share_factor for dual-class filers.
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

		// PayoutRatio = DPS / EPS (basic) -- independent of market data,
		// computed for every dimension.
		for _, f := range group {
			if f.EPS != 0 {
				f.PayoutRatio = math.Round(f.DividendsPerBasicCommonShare/f.EPS*1000) / 1000
			}
		}

		// Phase 3: copy ratios from trailing to quarterly (ART->ARQ, MRT->MRQ).
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
// view for the unadjusted close on or before the given date (within 7 days
// back to handle weekends and holidays). Returns 0 when no price is found.
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

// computeMarketRatioShareFactor implements Sharadar's dual-class share_factor:
// share_factor = (A*A_price + B*B_price) / ((A+B) * our_price). Returns
// (rawSF, roundedSF, true) when all inputs are available.
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

	// Map class counts to "our" and "sibling" by price magnitude: the senior
	// class trades higher (e.g. BRK.A ~ 1500 x BRK.B), independent of any
	// ticker-suffix convention.
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

// FindEODViewName returns the ViewName of the published "eod" view, or "".
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
