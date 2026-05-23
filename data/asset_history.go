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
package data

import (
	"strings"
	"time"
)

// AssetHistory resolves "what asset record was active for this ticker
// on this date?" — including for tickers that have been delisted or
// reused. Importers translating provider rows (ticker + date) into
// FIGI-tagged observations need this as-of-date semantics. Tickers are
// normalized to uppercase at insertion and lookup; callers do not need
// to pre-normalize.
type AssetHistory struct {
	byTicker map[string][]assetHistoryEntry
}

type assetHistoryEntry struct {
	asset    *Asset
	listed   time.Time // zero = listed date unknown; treated as far past
	delisted time.Time // zero = still active; treated as far future
}

// NewAssetHistory builds a history index over the supplied asset
// slice. Assets without a composite_figi are dropped — they could
// never be matched to provider rows anyway. Listing and delisting
// timestamps are parsed with parseAssetDate; unparseable values are
// treated as zero, which AssetAt interprets as an open-ended bound on
// that side.
func NewAssetHistory(assets []*Asset) *AssetHistory {
	h := &AssetHistory{byTicker: map[string][]assetHistoryEntry{}}

	for _, a := range assets {
		if a.CompositeFigi == "" {
			continue
		}

		key := normalizeTicker(a.Ticker)

		h.byTicker[key] = append(h.byTicker[key], assetHistoryEntry{
			asset:    a,
			listed:   parseAssetDate(a.ListingDate),
			delisted: parseAssetDate(a.DelistingDate),
		})
	}

	return h
}

// AssetAt returns the asset record whose [listed, delisted] window
// covers date for ticker. A zero listed time is treated as "always
// listed before"; a zero delisted time is treated as "still active".
// Both endpoints are inclusive. When two or more windows cover the
// same date, the window with the earliest delisted date wins (a
// still-active window only wins when nothing finite matches), making
// resolution insertion-order independent.
func (h *AssetHistory) AssetAt(ticker string, date time.Time) (*Asset, bool) {
	if h == nil {
		return nil, false
	}

	var (
		best    *Asset
		bestEnd time.Time
		found   bool
	)

	for _, e := range h.byTicker[normalizeTicker(ticker)] {
		if !e.listed.IsZero() && date.Before(e.listed) {
			continue
		}

		if !e.delisted.IsZero() && date.After(e.delisted) {
			continue
		}

		if !found || endBefore(e.delisted, bestEnd) {
			best = e.asset
			bestEnd = e.delisted
			found = true
		}
	}

	return best, found
}

// endBefore reports whether window-end `a` is earlier than `b`,
// treating the zero time as "infinitely far in the future" (i.e. the
// window has no delisted date yet). Used as the tiebreaker in AssetAt.
func endBefore(a, b time.Time) bool {
	switch {
	case a.IsZero():
		return false
	case b.IsZero():
		return true
	default:
		return a.Before(b)
	}
}

// FIGIAt is a convenience wrapper that returns just the composite FIGI
// of the asset active on date, or "" / false when no window matches.
func (h *AssetHistory) FIGIAt(ticker string, date time.Time) (string, bool) {
	a, ok := h.AssetAt(ticker, date)
	if !ok {
		return "", false
	}

	return a.CompositeFigi, true
}

// WindowsFor returns every asset record known for ticker, in the
// order they were inserted. Useful for diagnostics when AssetAt /
// FIGIAt fail and the caller wants to show the gap.
func (h *AssetHistory) WindowsFor(ticker string) []*Asset {
	if h == nil {
		return nil
	}

	entries := h.byTicker[normalizeTicker(ticker)]
	out := make([]*Asset, 0, len(entries))

	for _, e := range entries {
		out = append(out, e.asset)
	}

	return out
}

// TickerCount returns the number of distinct tickers in the history.
// Used for diagnostic logging at run start ("zero tickers in scope"
// usually means a filter excluded everything).
func (h *AssetHistory) TickerCount() int {
	if h == nil {
		return 0
	}

	return len(h.byTicker)
}

// Tickers returns the distinct tickers known to the history, in
// unspecified order. Callers that dispatch per-ticker work (one fetch
// per ticker rather than one per asset row) build their job queue
// from this slice so reused tickers fetch once and per-row FIGIAt
// picks the right window per observation date.
func (h *AssetHistory) Tickers() []string {
	if h == nil {
		return nil
	}

	out := make([]string, 0, len(h.byTicker))

	for t := range h.byTicker {
		out = append(out, t)
	}

	return out
}

func normalizeTicker(t string) string {
	return strings.ToUpper(strings.TrimSpace(t))
}

// parseAssetDate parses a listed/delisted timestamp string from the
// assets table. The Asset struct stores these as strings formatted via
// to_char(). Returns the zero time on empty / unparseable input - the
// AssetAt logic treats zero as an open-ended bound on that side.
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
