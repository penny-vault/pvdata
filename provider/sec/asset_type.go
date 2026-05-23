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
	"strconv"
	"strings"
	"sync"

	"github.com/penny-vault/pvdata/data"
	"golang.org/x/sync/singleflight"
)

// resolveAssetTypeCache memoizes the form-vote result keyed by CIK.
type resolveResult struct {
	assetType data.AssetType
	ok        bool
}

var (
	resolveAssetTypeCacheMu sync.RWMutex
	resolveAssetTypeCache   = map[int]resolveResult{}

	// resolveAssetTypeSingleflight collapses concurrent calls for the same CIK.
	resolveAssetTypeSingleflight singleflight.Group
)

// ResolveAssetType inspects SEC submissions for the given CIK and returns the
// asset type the entity primarily files as. Used by the Massive walk to
// classify records that Massive returns without a type tag — common for
// delisted pre-2010 tickers where Massive's reference data has the entity but
// no `type` field.
func ResolveAssetType(ctx context.Context, cik string) (data.AssetType, bool) {
	n, err := strconv.Atoi(strings.TrimSpace(cik))
	if err != nil || n <= 0 {
		return "", false
	}

	resolveAssetTypeCacheMu.RLock()

	cached, hit := resolveAssetTypeCache[n]

	resolveAssetTypeCacheMu.RUnlock()

	if hit {
		return cached.assetType, cached.ok
	}

	key := strconv.Itoa(n)

	res, _, _ := resolveAssetTypeSingleflight.Do(key, func() (any, error) {
		resolveAssetTypeCacheMu.RLock()

		cached, hit := resolveAssetTypeCache[n]

		resolveAssetTypeCacheMu.RUnlock()

		if hit {
			return cached, nil
		}

		sub, err := FetchSubmissions(ctx, n)
		if err != nil || sub == nil {
			r := resolveResult{}

			resolveAssetTypeCacheMu.Lock()
			resolveAssetTypeCache[n] = r
			resolveAssetTypeCacheMu.Unlock()

			return r, nil
		}

		at, ok := resolveTypeFromForms(sub.Filings.Recent.Form)
		r := resolveResult{assetType: at, ok: ok}

		resolveAssetTypeCacheMu.Lock()
		resolveAssetTypeCache[n] = r
		resolveAssetTypeCacheMu.Unlock()

		return r, nil
	})

	r, _ := res.(resolveResult)

	return r.assetType, r.ok
}

// ResolveAssetTypeWithCIKCorrection runs ResolveAssetType against cik and, on
// miss, tries a single remediation: search SEC by name for a different CIK
// whose filings resolve. A candidate is rejected when its SEC `tickers` array
// is non-empty and does not include ticker. year centers the SEC name
// search's ±3-year window.
func ResolveAssetTypeWithCIKCorrection(ctx context.Context, cik, ticker, name string, year int) (data.AssetType, string, bool) {
	if at, ok := ResolveAssetType(ctx, cik); ok {
		return at, cik, true
	}

	trimmedName := strings.TrimSpace(name)
	if trimmedName == "" || year == 0 {
		return "", cik, false
	}

	candidateCIK, found := FindCIKByName(ctx, trimmedName, year)
	if !found {
		return "", cik, false
	}

	if strings.TrimLeft(candidateCIK, "0") == strings.TrimLeft(strings.TrimSpace(cik), "0") {
		return "", cik, false
	}

	candidateN, err := strconv.Atoi(candidateCIK)
	if err != nil || candidateN <= 0 {
		return "", cik, false
	}

	candidateSub, err := FetchSubmissions(ctx, candidateN)
	if err != nil || candidateSub == nil {
		return "", cik, false
	}

	if len(candidateSub.Tickers) > 0 && !containsTicker(candidateSub.Tickers, ticker) {
		return "", cik, false
	}

	candidateType, ok := ResolveAssetType(ctx, candidateCIK)
	if !ok {
		return "", cik, false
	}

	return candidateType, candidateCIK, true
}

// containsTicker reports whether list contains ticker (case- and
// whitespace-insensitive). An empty ticker returns false so a missing ticker
// is not treated as a match.
func containsTicker(list []string, ticker string) bool {
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

// resolveTypeFromForms is the pure form-vote tabulation used by
// ResolveAssetType. See ResolveAssetType for the tie-break rules.
func resolveTypeFromForms(forms []string) (data.AssetType, bool) {
	var csVotes, adrcVotes, cefVotes, mfVotes int

	for _, form := range forms {
		upper := strings.ToUpper(strings.TrimSpace(form))

		switch {
		case strings.HasPrefix(upper, "10-K"), strings.HasPrefix(upper, "10K"):
			csVotes++
		case strings.HasPrefix(upper, "10-Q"), strings.HasPrefix(upper, "10Q"):
			csVotes++
		case strings.HasPrefix(upper, "20-F"), strings.HasPrefix(upper, "20F"),
			strings.HasPrefix(upper, "40-F"), strings.HasPrefix(upper, "40F"):
			adrcVotes++
		case strings.HasPrefix(upper, "N-CSR"), strings.HasPrefix(upper, "N-2"):
			cefVotes++
		case strings.HasPrefix(upper, "N-1A"):
			mfVotes++
		}
	}

	if csVotes == 0 && adrcVotes == 0 && cefVotes == 0 && mfVotes == 0 {
		return "", false
	}

	if csVotes >= adrcVotes && csVotes >= cefVotes && csVotes >= mfVotes {
		return data.CommonStock, true
	}

	if adrcVotes >= cefVotes && adrcVotes >= mfVotes {
		return data.ADRC, true
	}

	if cefVotes >= mfVotes {
		return data.CEF, true
	}

	return data.MutualFund, true
}
