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

// resolveAssetTypeCache memoizes the form-vote result keyed by CIK so
// the same CIK queried from many pages of a walk pays the form-vote
// cost (and the FetchSubmissions cache lookup) exactly once. Without
// this, a full-universe walk re-parses the same ~250-row form list
// per-record per-day for every untyped ticker — the same cost the
// submissionsCache eliminates for the network layer, repeated for
// CPU.
type resolveResult struct {
	assetType data.AssetType
	ok        bool
}

var (
	resolveAssetTypeCacheMu sync.RWMutex
	resolveAssetTypeCache   = map[int]resolveResult{}

	// resolveAssetTypeSingleflight collapses concurrent ResolveAssetType
	// calls for the same CIK; without it, 128 walk workers all missing
	// the cache simultaneously would each spawn FetchSubmissions
	// goroutines and re-do the form-vote parse.
	resolveAssetTypeSingleflight singleflight.Group
)

// ResolveAssetType inspects SEC submissions for the given CIK and
// returns the asset type the entity primarily files as. Used by the
// Massive walk to classify records that Massive returns without a
// type tag — common for delisted pre-2010 tickers where Massive's
// reference data has the entity but no `type` field.
//
// Form-to-type mapping (case-insensitive, prefix-based to catch the
// many SEC form variants — 10-K, 10-K/A, 10-KSB, 10KSB40, 10-K405,
// 10-KT, etc. all signal common-stock operating-issuer status):
//
//   - 10-K* / 10K*    -> data.CommonStock (incl. small business 10-KSB)
//   - 10-Q* / 10Q*    -> data.CommonStock (incl. small business 10-QSB)
//   - 20-F* / 40-F*   -> data.ADRC        (foreign private issuer)
//   - N-CSR* / N-2*   -> data.CEF
//   - N-1A*           -> data.MutualFund
//
// Form votes are counted across the `recent` filings batch only. The
// overflow `files[]` would require fetching each old-filings JSON to
// see its forms — recent filings carry the same type signal for any
// entity that filed within the last ~1000 filings, which covers every
// active and recently-delisted issuer (e.g., small banks that delisted
// in the early 2000s still appear in `recent` because their pre-delist
// filings total well under 1000).
//
// CommonStock wins ties: BDCs and equity REITs file both 10-K and N-2,
// and they trade as common stock. ADRC wins ties with CommonStock when
// a foreign issuer files both 20-F (foreign annual) and an occasional
// 10-K, which is rare in practice.
//
// Returns ("", false) when:
//   - CIK is empty/invalid
//   - SEC has no record for the CIK (404)
//   - No filings match a known type-signaling form
//
// Uses the same process-wide submissionsCache as FetchSubmissions, so a
// CIK queried for descriptive enrichment and again for type resolution
// hits the network only once.
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

// ResolveAssetTypeWithCIKCorrection runs ResolveAssetType against the
// supplied CIK and, when no tracked type comes back, tries a single
// remediation: search SEC by the asset's name for a different CIK
// whose filings do resolve to a tracked type. The function returns
// the resolved type, the CIK that produced it (the supplied CIK on a
// direct hit; the corrected CIK on a successful remediation), and a
// boolean indicating whether any tracked type was resolved at all.
//
// The remediation is intentionally narrow. It only fires when the
// supplied CIK has no operating-company-shaped filings (the same
// signal ResolveAssetType uses, just inverted), and a candidate is
// accepted only when all of the following hold:
//
//   - The candidate CIK is different from the supplied CIK.
//   - FindCIKByName accepts the match by its existing similarity gate.
//   - The candidate's SEC `tickers` array is empty or contains the
//     asset's ticker. A non-empty `tickers` array that does not
//     include the asset's ticker is a positive conflict signal and
//     causes the remediation to bail.
//   - ResolveAssetType against the candidate returns a tracked type.
//
// year is the year the SEC name search should center its ±3-year
// window on. Pass the asset's ValidFor year (walk-surfaced) or the
// year of the listing era when known.
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

// containsTicker reports whether list contains ticker under case- and
// whitespace-insensitive comparison. An empty ticker argument returns
// false, which keeps the caller from inferring a match when the
// asset itself has no ticker to test against.
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
// ResolveAssetType. Split out from the network-dependent caller so
// the form-matching rules are unit-testable without standing up a
// stubbed SEC client. See ResolveAssetType for the form-to-type
// mapping and tie-break rules.
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
