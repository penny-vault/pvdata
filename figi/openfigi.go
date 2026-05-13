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
package figi

import (
	"context"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/viper"
	"golang.org/x/time/rate"
)

const (
	OPENFIGI_MAPPING_URL string = "https://api.openfigi.com/v3/mapping"
)

type MappingResponse struct {
	Data []*OpenFigiAsset `json:"data"`
}

type OpenFigiAsset struct {
	Figi                string `json:"figi"`
	SecurityType        string `json:"securityType"`
	MarketSector        string `json:"marketSector"`
	Ticker              string `json:"ticker"`
	Name                string `json:"name"`
	ExchangeCode        string `json:"exchCode"`
	ShareClassFIGI      string `json:"shareClassFIGI"`
	CompositeFIGI       string `json:"compositeFIGI"`
	SecurityType2       string `json:"securityType2"`
	SecurityDescription string `json:"securityDescription"`
}

type OpenFigiQuery struct {
	IdType                  string `json:"idType"`
	IdValue                 string `json:"idValue"`
	ExchangeCode            string `json:"exchCode"`
	MarketSectorDescription string `json:"marketSecDes"`
	IncludeUnlistedEquities *bool  `json:"includeUnlistedEquities,omitempty"`
}

func RateLimit() *rate.Limiter {
	dur := (time.Second * 6) / 25
	openFigiRate := rate.Every(dur)

	return rate.NewLimiter(openFigiRate, 10)
}

func batchSize() int {
	apiKey := viper.GetString("openfigi.apikey")
	if apiKey == "" {
		return 10
	}

	return 100
}

func mapFigis(query []*OpenFigiQuery) ([]*MappingResponse, error) {
	maxBatch := batchSize()
	if len(query) > maxBatch {
		log.Error().Int("BatchSize", len(query)).Int("MaxBatch", maxBatch).Msg("programming error - too many assets in request")
	}

	apiKey := viper.GetString("openfigi.apikey")
	mappingResponse := make([]*MappingResponse, 0)
	client := resty.New()
	resp, err := client.R().
		SetHeader("X-OPENFIGI-APIKEY", apiKey).
		SetBody(query).
		SetResult(&mappingResponse).
		Post(OPENFIGI_MAPPING_URL)

	log.Debug().Str("URL", OPENFIGI_MAPPING_URL).Int("NumTickers", len(query)).Msg("map tickers to FIGIs")

	if err != nil {
		log.Error().Err(err).Msg("OpenFigi api called errored out")
		return []*MappingResponse{}, err
	}

	if resp.StatusCode() >= 400 {
		log.Error().Int("StatusCode", resp.StatusCode()).Str("Body", string(resp.Body())).Msg("openfigi api call returned invalid status code")
		return []*MappingResponse{}, err
	}

	return mappingResponse, nil
}

// Enrich fills CompositeFigi / ShareClassFigi / AssetType on each asset
// using a three-step escalation. Each step only operates on assets still
// missing a CompositeFigi after the previous step:
//
//  1. Existing-assets index (loaded once per run via data.WithAssetIndex
//     in the context; nil when absent, in which case step 1 is skipped).
//     Reuses FIGIs we have previously persisted so that a delisted asset
//     already known to the DB does not get reminted as synthetic.
//
//  2. OpenFIGI mapping API via LookupFigi — active assets only. Delisted
//     assets are intentionally not sent to OpenFIGI: the TICKER lookup
//     does not accept an as-of date and can return the FIGI of a later
//     tenant of the same ticker (e.g. BBI = Blockbuster 2010 then
//     Brickell Biotech 2022).
//
//  3. Synthetic FIGI mint — delisted assets only, when steps 1 and 2 did
//     not produce a FIGI. Prefers GenerateSyntheticFIGIFromCIK(cik,
//     ticker); falls back to GenerateSyntheticFIGI(ticker, name) when
//     CIK is empty.
func Enrich(ctx context.Context, assets ...*data.Asset) {
	logger := zerolog.Ctx(ctx)

	// Step 1: existing-assets index. Lookup is strict and tries
	// every identifier carried on the incoming asset (CompositeFigi,
	// ShareClassFigi, InstrumentPermID, CUSIP, ISIN, Ticker+CIK,
	// Ticker+OrganizationPermID) in order of specificity. A
	// ticker-alone match would risk attributing the wrong entity's
	// FIGI to the incoming asset, so we accept no result when none
	// of the asset's identifiers find a hit.
	if idx := data.AssetIndexFromContext(ctx); !idx.IsZero() {
		for _, asset := range assets {
			if asset.CompositeFigi != "" {
				continue
			}

			match, ok := idx.Lookup(asset)
			if !ok {
				continue
			}

			asset.CompositeFigi = match.CompositeFigi
			if asset.ShareClassFigi == "" {
				asset.ShareClassFigi = match.ShareClassFigi
			}
		}
	}

	// Step 2: OpenFIGI mapping for assets still missing a FIGI and still
	// active.
	rateLimiter := RateLimit()
	emptyFigis := make([]*data.Asset, 0, 100)

	for _, asset := range assets {
		if (asset.CompositeFigi == "" || asset.AssetType == data.UnknownAsset) && asset.DelistingDate == "" {
			emptyFigis = append(emptyFigis, asset)
		}
	}

	figiMap := LookupFigi(emptyFigis, rateLimiter)
	for _, asset := range emptyFigis {
		if assetFigi, ok := figiMap[asset.Ticker]; ok {
			asset.CompositeFigi = assetFigi.CompositeFIGI
			asset.ShareClassFigi = assetFigi.ShareClassFIGI

			if asset.AssetType == data.UnknownAsset {
				switch assetFigi.SecurityType2 {
				case "Partnership Shares":
					asset.AssetType = data.CommonStock
				case "Depositary Receipt":
					asset.AssetType = data.ADRC
				case "Common Stock":
					asset.AssetType = data.CommonStock
				case "Mutual Fund":
					switch assetFigi.SecurityType {
					case "ETP":
						asset.AssetType = data.ETF
					case "Open-End Fund":
						asset.AssetType = data.MutualFund
					case "Closed-End Fund":
						asset.AssetType = data.CEF
					default:
						log.Warn().
							Str("SecurityType", assetFigi.SecurityType).
							Str("SecurityType2", assetFigi.SecurityType2).
							Str("Ticker", asset.Ticker).
							Str("CompositeFigi", assetFigi.CompositeFIGI).
							Msg("asset type is unknown and openfigi security type 2 is unknown")
					}

					asset.AssetType = data.MutualFund
				case "":
				default:
					log.Warn().
						Str("SecurityType", assetFigi.SecurityType).
						Str("SecurityType2", assetFigi.SecurityType2).
						Str("Ticker", asset.Ticker).
						Str("CompositeFigi", assetFigi.CompositeFIGI).
						Msg("asset type is unknown and openfigi security type is unknown")
				}
			}
		}
	}

	// Step 3: synthetic mint for delisted assets still without a FIGI.
	for _, asset := range assets {
		if asset.CompositeFigi != "" {
			continue
		}

		if asset.DelistingDate == "" {
			continue
		}

		switch {
		case asset.CIK != "":
			asset.CompositeFigi = GenerateSyntheticFIGIFromCIK(asset.CIK, asset.Ticker)
			logger.Debug().
				Str("Ticker", asset.Ticker).
				Str("CIK", asset.CIK).
				Str("CompositeFigi", asset.CompositeFigi).
				Str("DelistingDate", asset.DelistingDate).
				Msg("minted synthetic FIGI from CIK+ticker for delisted asset")
		case asset.Ticker != "" && asset.Name != "":
			asset.CompositeFigi = GenerateSyntheticFIGI(asset.Ticker, asset.Name)
			logger.Debug().
				Str("Ticker", asset.Ticker).
				Str("Name", asset.Name).
				Str("CompositeFigi", asset.CompositeFigi).
				Str("DelistingDate", asset.DelistingDate).
				Msg("minted synthetic FIGI from ticker+name for delisted asset (no CIK)")
		default:
			logger.Warn().
				Str("Ticker", asset.Ticker).
				Str("Name", asset.Name).
				Str("DelistingDate", asset.DelistingDate).
				Msg("cannot mint synthetic FIGI for delisted asset: no CIK and no name; asset will be dropped downstream")
		}
	}
}

func LookupFigi(assets []*data.Asset, rateLimiter *rate.Limiter) map[string]*OpenFigiAsset {
	maxBatch := batchSize()

	if viper.GetString("openfigi.apikey") == "" {
		log.Warn().Msg("no OpenFIGI API key configured -- using reduced batch size (10 per request); set openfigi.apikey in your config or re-run `pvdata init`")
	}

	query := make([]*OpenFigiQuery, 0, maxBatch)
	result := make(map[string]*OpenFigiAsset)

	for _, asset := range assets {
		query = append(query, &OpenFigiQuery{
			IdType:                  "TICKER",
			IdValue:                 asset.Ticker,
			ExchangeCode:            "US",
			MarketSectorDescription: "Equity",
		})

		if len(query) == maxBatch {
			if err := rateLimiter.Wait(context.Background()); err != nil {
				log.Panic().Err(err).Msg("rate limiter failed")
			}

			mappingResponse, _ := mapFigis(query)
			for _, resp := range mappingResponse {
				for _, figiAsset := range resp.Data {
					result[figiAsset.Ticker] = figiAsset
				}
			}

			query = make([]*OpenFigiQuery, 0, maxBatch)
		}
	}

	if len(query) > 0 {
		if err := rateLimiter.Wait(context.Background()); err != nil {
			log.Panic().Err(err).Msg("rate limiter failed")
		}

		mappingResponse, _ := mapFigis(query)
		for _, resp := range mappingResponse {
			for _, figiAsset := range resp.Data {
				result[figiAsset.Ticker] = figiAsset
			}
		}
	}

	return result
}

// LookupCompositesByFIGI resolves each composite FIGI through OpenFIGI's
// mapping endpoint (idType=ID_BB_GLOBAL) and returns the result keyed
// by FIGI. Used to verify after the fact that a composite a provider
// gave us actually belongs to a US-listed security — OpenFIGI is
// authoritative for exchCode while provider primary_exchange fields
// are not (Massive in particular sometimes labels foreign-exchange
// composites as XNAS for a US ticker on isolated dates).
//
// Result semantics:
//   - present, ExchangeCode == "US": confirmed US.
//   - present, ExchangeCode != "US": confirmed non-US.
//   - absent: OpenFIGI does not know the FIGI (delisted/evicted).
//     Callers must decide what to do; usually keep the row since the
//     upstream observation is the best signal available.
//
// Batches at the active OpenFIGI batch size (100 with an API key, 10
// without) and respects the standard rate limit.
func LookupCompositesByFIGI(ctx context.Context, figis []string) map[string]*OpenFigiAsset {
	if len(figis) == 0 {
		return nil
	}

	rateLimiter := RateLimit()
	maxBatch := batchSize()
	result := make(map[string]*OpenFigiAsset, len(figis))

	for start := 0; start < len(figis); start += maxBatch {
		end := min(start+maxBatch, len(figis))

		query := make([]*OpenFigiQuery, 0, end-start)
		for _, f := range figis[start:end] {
			query = append(query, &OpenFigiQuery{
				IdType:                  "ID_BB_GLOBAL",
				IdValue:                 f,
				MarketSectorDescription: "Equity",
			})
		}

		if err := rateLimiter.Wait(ctx); err != nil {
			log.Warn().Err(err).Msg("openfigi rate limiter wait failed; aborting composite confirmation")
			return result
		}

		responses, err := mapFigis(query)
		if err != nil {
			log.Warn().Err(err).Int("BatchSize", len(query)).Msg("openfigi composite confirmation batch failed; continuing")
			continue
		}

		for _, r := range responses {
			for _, asset := range r.Data {
				if asset.Figi != "" {
					result[asset.Figi] = asset
				}
			}
		}
	}

	return result
}

func LookupFigiUnlisted(assets []*data.Asset, rateLimiter *rate.Limiter) map[string]*OpenFigiAsset {
	maxBatch := batchSize()
	includeUnlisted := true

	if viper.GetString("openfigi.apikey") == "" {
		log.Warn().Msg("no OpenFIGI API key configured -- using reduced batch size (10 per request); set openfigi.apikey in your config or re-run `pvdata init`")
	}

	query := make([]*OpenFigiQuery, 0, maxBatch)
	result := make(map[string]*OpenFigiAsset)

	for _, asset := range assets {
		query = append(query, &OpenFigiQuery{
			IdType:                  "TICKER",
			IdValue:                 asset.Ticker,
			ExchangeCode:            "US",
			MarketSectorDescription: "Equity",
			IncludeUnlistedEquities: &includeUnlisted,
		})

		if len(query) == maxBatch {
			if err := rateLimiter.Wait(context.Background()); err != nil {
				log.Panic().Err(err).Msg("rate limiter failed")
			}

			mappingResponse, _ := mapFigis(query)
			for _, resp := range mappingResponse {
				for _, figiAsset := range resp.Data {
					result[figiAsset.Ticker] = figiAsset
				}
			}

			query = make([]*OpenFigiQuery, 0, maxBatch)
		}
	}

	if len(query) > 0 {
		if err := rateLimiter.Wait(context.Background()); err != nil {
			log.Panic().Err(err).Msg("rate limiter failed")
		}

		mappingResponse, _ := mapFigis(query)
		for _, resp := range mappingResponse {
			for _, figiAsset := range resp.Data {
				result[figiAsset.Ticker] = figiAsset
			}
		}
	}

	return result
}
