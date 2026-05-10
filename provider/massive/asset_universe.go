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
	"strings"
	"time"

	"github.com/penny-vault/pvdata/data"
)

// historicalAssetUniverse resolves "what was the composite_figi for
// this ticker on this date?" — including for tickers that have been
// delisted since. The flat-file fetch needs the as-of-date semantics
// because a ticker active on 2003-10-08 might be inactive today, but
// its 2003 bars are still valid data.
//
// Same ticker can appear under multiple FIGIs across time (ticker
// reuse after a long delisting); each ticker's slice holds every
// known window and figiAt picks the one whose [listed, delisted]
// range covers the requested date.
type historicalAssetUniverse struct {
	byTicker map[string][]assetWindow
}

type assetWindow struct {
	figi     string
	listed   time.Time // zero = listed date unknown; treated as far past
	delisted time.Time // zero = still active; treated as far future
}

func newHistoricalAssetUniverse() *historicalAssetUniverse {
	return &historicalAssetUniverse{byTicker: map[string][]assetWindow{}}
}

func (u *historicalAssetUniverse) add(ticker, figi string, listed, delisted time.Time) {
	u.byTicker[ticker] = append(u.byTicker[ticker], assetWindow{
		figi:     figi,
		listed:   listed,
		delisted: delisted,
	})
}

// figiAt returns the composite_figi for ticker that was active on the
// given date, plus a boolean indicating whether any window matched. A
// zero listed time is treated as "always listed before"; a zero
// delisted time is treated as "still active".
func (u *historicalAssetUniverse) figiAt(ticker string, date time.Time) (string, bool) {
	for _, w := range u.byTicker[ticker] {
		if !w.listed.IsZero() && date.Before(w.listed) {
			continue
		}

		if !w.delisted.IsZero() && date.After(w.delisted) {
			continue
		}

		return w.figi, true
	}

	return "", false
}

// tickerCount returns the number of distinct tickers in the universe.
// Used for diagnostic logging at run start.
func (u *historicalAssetUniverse) tickerCount() int {
	return len(u.byTicker)
}

// buildHistoricalUniverse constructs the universe from a slice of
// assets, applying ticker/figi filters and dropping rows without a
// composite_figi (which can never be matched to flat-file rows).
func buildHistoricalUniverse(assets []*data.Asset, tickerFilter, figiFilter string) *historicalAssetUniverse {
	u := newHistoricalAssetUniverse()

	for _, a := range assets {
		if a.CompositeFigi == "" {
			continue
		}

		if tickerFilter != "" && !strings.EqualFold(a.Ticker, tickerFilter) {
			continue
		}

		if figiFilter != "" && a.CompositeFigi != figiFilter {
			continue
		}

		u.add(a.Ticker, a.CompositeFigi, parseAssetDate(a.ListingDate), parseAssetDate(a.DelistingDate))
	}

	return u
}

// parseAssetDate parses a listed/delisted timestamp string from the
// assets table. The Asset struct stores these as strings formatted via
// to_char(). Returns the zero time on empty / unparseable input - the
// figiAt logic treats zero as an open-ended bound on that side.
func parseAssetDate(s string) time.Time {
	if s == "" {
		return time.Time{}
	}

	formats := []string{
		"2006-01-02T15:04:05.000000Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02",
	}

	for _, layout := range formats {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}

	return time.Time{}
}
