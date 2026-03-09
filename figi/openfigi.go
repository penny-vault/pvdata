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
}

func rateLimit() *rate.Limiter {
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

func Enrich(assets ...*data.Asset) {
	rateLimiter := rateLimit()

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
