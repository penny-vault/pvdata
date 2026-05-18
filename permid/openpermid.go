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
package permid

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/adrg/strutil"
	"github.com/adrg/strutil/metrics"
	"github.com/go-resty/resty/v2"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"golang.org/x/time/rate"
)

// ErrRateLimited indicates Refinitiv returned HTTP 429 — the daily
// 5,000-request ceiling for the Open Data public tier has been hit.
// Callers should stop issuing further requests for the day; retrying
// will not succeed until the quota resets. Enrich zeros its shared
// budget when it sees this error so concurrent Enrich invocations
// short-circuit on their next budget check.
var ErrRateLimited = errors.New("permid: refinitiv rate-limited (429)")

// PermIDSearchURL is the Refinitiv PermID Open Data search endpoint.
// The free tier returns organizations / instruments / quotes for the
// supplied `q=` query string. Declared as a var (not a const) so
// internal tests can redirect it to an httptest server stub.
var PermIDSearchURL = "https://api-eit.refinitiv.com/permid/search"

// permidURLPrefix is stripped from `@id` values to yield the raw
// canonical identifier (e.g. "1-4295905573").
const permidURLPrefix = "https://permid.org/"

// SearchResponse mirrors the Refinitiv PermID search API JSON shape.
type SearchResponse struct {
	Result struct {
		Organizations EntityBucket `json:"organizations"`
		Instruments   EntityBucket `json:"instruments"`
		Quotes        EntityBucket `json:"quotes"`
	} `json:"result"`
}

// EntityBucket is one of the per-entity-type sections of the response.
type EntityBucket struct {
	EntityType string   `json:"entityType"`
	Total      int      `json:"total"`
	Start      int      `json:"start"`
	Num        int      `json:"num"`
	Entities   []Entity `json:"entities"`
}

// Entity is the union of the fields exposed across organization,
// instrument and quote search results. Fields that do not apply to a
// given entity type are simply left empty.
type Entity struct {
	ID               string `json:"@id"`
	OrganizationName string `json:"organizationName"`
	OrgSubtype       string `json:"orgSubtype"`
	PrimaryTicker    string `json:"primaryTicker"`
	HasName          string `json:"hasName"`
	AssetClass       string `json:"assetClass"`
	IsIssuedBy       string `json:"isIssuedBy"`
	IsIssuedByName   string `json:"isIssuedByName"`

	// Organization-only enrichment fields.
	HasHoldingClassification string `json:"hasHoldingClassification"`
	HasURL                   string `json:"hasURL"`

	// Quote-only enrichment fields.
	HasMic            string `json:"hasMic"`
	HasRIC            string `json:"hasRIC"`
	HasExchangeTicker string `json:"hasExchangeTicker"`
	IsQuoteOf         string `json:"isQuoteOf"`
	HasPrimaryQuote   string `json:"hasPrimaryQuote"`
}

// LookupResult is the rich return type from LookupByTicker — it holds
// the resolved PermIDs alongside enrichment fields (canonical name,
// corporate URL, asset class, MIC) that callers can use to fill empty
// fields on the requesting asset. Empty strings mean "no value
// available"; callers should not overwrite asset fields that already
// have a value.
type LookupResult struct {
	OrgPermID        string
	InstrumentPermID string

	OrgName       string // from organizations[].organizationName
	OrgURL        string // from organizations[].hasURL
	OrgClass      string // from organizations[].hasHoldingClassification
	InstrumentMIC string // from quotes[].hasMic for the instrument's primary quote
	AssetClass    string // from instruments[].assetClass
}

// sharedRateLimiter is the process-wide rate limiter for every
// outbound Refinitiv PermID request. The Refinitiv public-tier cap is
// 4 req/s (observed via the x-ratelimit-limit-second response header).
// Allocating a fresh limiter per Enrich call would defeat the cap: a
// run with the massive provider's 32-worker publish() fan-out would
// otherwise have 32 independent 4-req/s limiters and burst to ~128
// req/s aggregate. One package-level limiter coordinates every caller.
//
// The tier also enforces a 5,000-request DAILY ceiling
// (x-ratelimit-limit-day). This limiter does not cap on the daily
// budget; permid.Enrich.WithAPIBudget handles that side. Once the
// daily quota is exhausted Refinitiv returns 429 and Enrich logs a
// warning per asset.
var sharedRateLimiter = rate.NewLimiter(rate.Every(time.Second/4), 4)

// RateLimit returns the shared Refinitiv PermID rate limiter. All
// callers within a process share one limiter so the aggregate request
// rate never exceeds the upstream 4 req/s cap, regardless of how many
// concurrent Enrich / Lookup invocations are in flight.
func RateLimit() *rate.Limiter {
	return sharedRateLimiter
}

// APIKey returns the PermID API key from viper (configured at
// permid.apikey in .pvdata.toml or the equivalent env var).
func APIKey() string {
	return viper.GetString("permid.apikey")
}

// permidRequestTimeout bounds individual Refinitiv /permid calls.
// Without this, a stalled connection at Refinitiv's edge can hang the
// publish pipeline indefinitely (no log lines appear because resty
// blocks inside Get until the underlying TCP read returns).
const permidRequestTimeout = 30 * time.Second

// Search calls the PermID search endpoint with the given query string
// (e.g. "ticker:AAPL" or "cik:0000320193") and returns the parsed
// response. The caller is responsible for waiting on the rate limiter
// before invoking Search; Search itself does not enforce the cap.
func Search(ctx context.Context, query string) (*SearchResponse, error) {
	apiKey := APIKey()
	if apiKey == "" {
		return nil, fmt.Errorf("permid: no API key configured; set permid.apikey in your config")
	}

	var resp SearchResponse

	client := resty.New().SetTimeout(permidRequestTimeout)

	zerolog.Ctx(ctx).Debug().Str("Query", query).Str("URL", PermIDSearchURL).Msg("permid: GET /search")

	r, err := client.R().
		SetContext(ctx).
		SetHeader("X-AG-Access-Token", apiKey).
		SetQueryParam("q", query).
		SetResult(&resp).
		Get(PermIDSearchURL)
	if err != nil {
		return nil, fmt.Errorf("permid search %q: %w", query, err)
	}

	if r.StatusCode() == http.StatusTooManyRequests {
		return nil, fmt.Errorf("permid search %q: %w (body: %s)", query, ErrRateLimited, string(r.Body()))
	}

	if r.StatusCode() >= 400 {
		return nil, fmt.Errorf("permid search %q returned %d: %s", query, r.StatusCode(), string(r.Body()))
	}

	return &resp, nil
}

// LookupByCIK returns the Organization PermID for a SEC CIK, or "" when
// the PermID Open Data set has no record. CIK is zero-padded to 10
// digits before the query (Refinitiv only accepts the padded form).
//
// CIK is a deterministic key — Refinitiv assigns one Organization
// PermID per SEC entity — so no name-similarity gate is needed on the
// result.
func LookupByCIK(ctx context.Context, cik string, limiter *rate.Limiter) (string, error) {
	cik = NormalizeCIK(cik)
	if cik == "" {
		return "", nil
	}

	if err := limiter.Wait(ctx); err != nil {
		return "", err
	}

	resp, err := Search(ctx, "cik:"+cik)
	if err != nil {
		return "", err
	}

	if len(resp.Result.Organizations.Entities) == 0 {
		return "", nil
	}

	return rawID(resp.Result.Organizations.Entities[0].ID), nil
}

// LookupByTicker searches by ticker and returns:
//   - orgPermID: the Organization PermID for the issuer, picked by
//     either matching knownOrgPermID (when caller already resolved one
//     via CIK) or by passing the name-similarity gate against `name`.
//   - instrumentPermID: the Instrument PermID for the security whose
//     primaryTicker exactly matches `ticker` AND whose isIssuedBy
//     resolves to the chosen org.
//
// Either return value can be empty when no candidate passes the
// safety gate. A non-nil error indicates an API failure; an empty
// result with nil error means "no match, but the API call succeeded".
func LookupByTicker(ctx context.Context, ticker, name, knownOrgPermID string, limiter *rate.Limiter) (*LookupResult, error) {
	ticker = strings.TrimSpace(ticker)
	if ticker == "" {
		return &LookupResult{}, nil
	}

	if err := limiter.Wait(ctx); err != nil {
		return nil, err
	}

	resp, err := Search(ctx, "ticker:"+ticker)
	if err != nil {
		return nil, err
	}

	out := &LookupResult{}

	orgPermID := knownOrgPermID
	if orgPermID == "" {
		for _, e := range resp.Result.Organizations.Entities {
			if NameMatches(name, e.OrganizationName) {
				orgPermID = rawID(e.ID)
				out.OrgName = e.OrganizationName
				out.OrgURL = strings.TrimSpace(e.HasURL)
				out.OrgClass = strings.TrimSpace(e.HasHoldingClassification)

				break
			}
		}
	} else {
		// Already know the org; still capture its descriptive fields
		// when we can find a matching organizations entry.
		for _, e := range resp.Result.Organizations.Entities {
			if rawID(e.ID) != orgPermID {
				continue
			}

			out.OrgName = e.OrganizationName
			out.OrgURL = strings.TrimSpace(e.HasURL)
			out.OrgClass = strings.TrimSpace(e.HasHoldingClassification)

			break
		}
	}

	var primaryQuoteID string

	for _, e := range resp.Result.Instruments.Entities {
		if !strings.EqualFold(e.PrimaryTicker, ticker) {
			continue
		}

		if orgPermID != "" {
			if rawID(e.IsIssuedBy) != orgPermID {
				continue
			}
		} else if !NameMatches(name, e.IsIssuedByName) {
			continue
		}

		out.InstrumentPermID = rawID(e.ID)
		out.AssetClass = strings.TrimSpace(e.AssetClass)
		primaryQuoteID = rawID(e.HasPrimaryQuote)

		break
	}

	// Resolve the primary quote's MIC when present so callers can fill
	// PrimaryExchange on assets where Massive's snapshot omits it.
	if primaryQuoteID != "" {
		for _, e := range resp.Result.Quotes.Entities {
			if rawID(e.ID) != primaryQuoteID {
				continue
			}

			out.InstrumentMIC = strings.TrimSpace(e.HasMic)

			break
		}
	}

	out.OrgPermID = orgPermID

	return out, nil
}

// NameMatches applies the same Jaro-Winkler ≥ 0.85 gate used by the
// iShares share-class resolver, with the FirstWordsMatch fallback for
// rename quirks. Empty inputs always return false.
func NameMatches(a, b string) bool {
	if a == "" || b == "" {
		return false
	}

	similarity := strutil.Similarity(
		strings.ToLower(a),
		strings.ToLower(b),
		metrics.NewJaroWinkler(),
	)

	if similarity >= provider.JaroWinklerThreshold {
		return true
	}

	return provider.FirstWordsMatch(a, b, 2)
}

// NormalizeCIK trims whitespace and zero-pads to 10 digits. Returns
// "" for strings that are empty or longer than 10 digits.
func NormalizeCIK(cik string) string {
	cik = strings.TrimSpace(cik)
	if cik == "" {
		return ""
	}

	trimmed := strings.TrimLeft(cik, "0")
	if trimmed == "" {
		return ""
	}

	if len(trimmed) > 10 {
		return ""
	}

	return strings.Repeat("0", 10-len(trimmed)) + trimmed
}

// rawID strips the "https://permid.org/" prefix so the stored
// identifier is the canonical "1-NNNNNNNNNN" form Refinitiv uses
// elsewhere.
func rawID(url string) string {
	return strings.TrimPrefix(url, permidURLPrefix)
}
