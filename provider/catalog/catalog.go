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
package catalog

import (
	"context"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
)

func init() {
	provider.Register("catalog", &Catalog{})
}

// Catalog is the Historical Asset Catalog provider. It reconstructs
// per-ticker asset lifecycles from the EOD parquet archive and fills
// each lifecycle with metadata gathered from the Massive reference
// endpoint, SEC filings, and OpenFIGI. The catalog provider is
// independent of provider/massive; it carries its own REST client,
// archive reader, and reconciliation logic.
type Catalog struct{}

func (c *Catalog) Name() string {
	return "catalog"
}

func (c *Catalog) Description() string {
	return `The Historical Asset Catalog reconstructs per-ticker asset lifecycles from the EOD parquet archive and enriches each lifecycle with reference metadata from the Massive REST API, SEC filings, and OpenFIGI. Use this provider to build a definitive cross-source asset catalog separate from the live Massive Stock Tickers feed.`
}

func (c *Catalog) ConfigDescription() map[string]string {
	return map[string]string{}
}

func (c *Catalog) Datasets() map[string]provider.Dataset {
	return map[string]provider.Dataset{
		"Historical Asset Catalog": {
			Name:        "Historical Asset Catalog",
			Description: "Per-ticker asset lifecycles reconstructed from the EOD parquet archive and enriched from Massive, SEC, and OpenFIGI.",
			DataTypes:   []*data.DataType{data.DataTypes[data.AssetKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(1949, 4, 19, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			ConfigDescription: map[string]string{
				"apiKey":    "Enter your Massive API key:",
				"rateLimit": "What is the maximum number of requests per minute?",
			},
			Fetch: downloadHistoricalAssetCatalog,
		},
	}
}

// downloadHistoricalAssetCatalog is the Historical Asset Catalog Fetch.
// Step 1 of the catalog migration lands this skeleton; the EOD-driven
// asset builder and its supporting code are moved into this package in
// step 2.
func downloadHistoricalAssetCatalog(_ context.Context, subscription *library.Subscription, _ chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
	now := time.Now()
	exitNotification <- data.RunSummary{
		StartTime:        now,
		EndTime:          now,
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
		Status:           data.RunSuccess,
		NumObservations:  0,
	}
}
