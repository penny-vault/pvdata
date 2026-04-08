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
package pvindex

import (
	"math"
	"sort"
	"strings"

	"github.com/penny-vault/pvdata/data"
)

// lpSuffixes is the list of legal-form suffixes that identify a limited partnership.
// Matched as case-insensitive whole-word suffixes against the trimmed asset name.
var lpSuffixes = []string{
	" LP",
	" L.P.",
	" L P",
	" LLP",
	" LIMITED PARTNERSHIP",
}

// isLPName returns true if the asset name ends in a recognized LP suffix.
// Suffix matching is case-insensitive and whitespace-tolerant.
func isLPName(name string) bool {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return false
	}

	upper := strings.ToUpper(trimmed)
	for _, suffix := range lpSuffixes {
		if strings.HasSuffix(upper, suffix) {
			return true
		}
	}

	return false
}

// allowedExchanges is the whitelist of US-listed common stock exchanges.
// Both display-name and MIC code formats are accepted, because the assets table
// currently contains a mix of both. See "Known limitation: exchange field inconsistency"
// in the design spec.
var allowedExchanges = map[data.Exchange]struct{}{
	"NASDAQ":    {},
	"XNAS":      {},
	"NYSE":      {},
	"XNYS":      {},
	"NYSE MKT":  {},
	"XASE":      {},
	"AMEX":      {},
	"NYSE ARCA": {},
	"ARCX":      {},
	"BATS":      {},
}

// filterAssetMaster returns the subset of assets passing the structural filter:
// active = true, asset_type = CS, primary_exchange in whitelist, name does not match
// an LP suffix.
func filterAssetMaster(assets []*data.Asset) []*data.Asset {
	out := make([]*data.Asset, 0, len(assets))

	for _, a := range assets {
		if !a.Active {
			continue
		}

		if a.AssetType != data.CommonStock {
			continue
		}

		if _, ok := allowedExchanges[a.PrimaryExchange]; !ok {
			continue
		}

		if isLPName(a.Name) {
			continue
		}

		out = append(out, a)
	}

	return out
}

// dedupShareClasses groups assets by CIK (or composite_figi as fallback for null CIK)
// and keeps the row with the highest median dollar volume per group. Ties are broken
// alphabetically by ticker for deterministic output.
func dedupShareClasses(assets []*data.Asset, dvByFigi map[string]float64) []*data.Asset {
	type bestRow struct {
		asset *data.Asset
		dv    float64
	}

	groups := make(map[string]bestRow, len(assets))

	for _, a := range assets {
		key := a.CIK
		if key == "" {
			key = a.CompositeFigi
		}

		dv := dvByFigi[a.CompositeFigi]

		current, exists := groups[key]
		if !exists {
			groups[key] = bestRow{asset: a, dv: dv}
			continue
		}

		if dv > current.dv || (dv == current.dv && a.Ticker < current.asset.Ticker) {
			groups[key] = bestRow{asset: a, dv: dv}
		}
	}

	out := make([]*data.Asset, 0, len(groups))
	for _, b := range groups {
		out = append(out, b.asset)
	}

	return out
}

// assignCapWeights computes market-cap-weighted weights from a map of composite_figi
// to market_cap. Weights are normalized to sum to 1.0. Returns an empty map if the
// input is empty or the total market cap is zero.
func assignCapWeights(caps map[string]int64) map[string]float64 {
	if len(caps) == 0 {
		return map[string]float64{}
	}

	var total int64
	for _, c := range caps {
		total += c
	}

	if total <= 0 {
		return map[string]float64{}
	}

	weights := make(map[string]float64, len(caps))

	totalF := float64(total)
	for figi, c := range caps {
		weights[figi] = float64(c) / totalF
	}

	return weights
}

// percentileInt64 returns the value at the given percentile (0..1) of the input slice.
// Uses the "nearest rank" method: for n values, the rank index = ceil(n*p), clamped
// to [1, n]. The returned value is sorted[rank-1]. Does not modify the input slice.
// Returns 0 for empty input.
func percentileInt64(values []int64, p float64) int64 {
	if len(values) == 0 {
		return 0
	}

	if len(values) == 1 {
		return values[0]
	}

	sorted := make([]int64, len(values))
	copy(sorted, values)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })

	rank := int(math.Ceil(float64(len(sorted)) * p))
	if rank < 1 {
		rank = 1
	}

	if rank > len(sorted) {
		rank = len(sorted)
	}

	return sorted[rank-1]
}
