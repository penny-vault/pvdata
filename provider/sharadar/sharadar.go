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
package sharadar

import (
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/provider"
)

func init() {
	provider.Register("sharadar", &Sharadar{})
}

type Sharadar struct{}

func (sharadar *Sharadar) Name() string {
	return "Sharadar"
}

func (sharadar *Sharadar) ConfigDescription() map[string]string {
	return map[string]string{
		"apiKey":    "Enter your Nasdaq Data Link API key:",
		"rateLimit": "What is the maximum number of requests per minute?",
	}
}

func (sharadar *Sharadar) Description() string {
	return `Sharadar publishes fundamentals, daily metrics, and other investment data via the Nasdaq Data Link API`
}

func (sharadar *Sharadar) Datasets() map[string]provider.Dataset {
	return map[string]provider.Dataset{
		"Fundamentals": {
			Name:        "Fundamentals",
			Description: "Download stock fundamentals.",
			DataTypes:   []*data.DataType{data.DataTypes[data.FundamentalsKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2007, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: downloadAllSharadarFundamentals,
		},

		"Metrics": {
			Name:        "Metrics",
			Description: "Download daily stock metrics.",
			DataTypes: []*data.DataType{
				data.DataTypes[data.MetricKey],
				data.DataTypes[data.IndexSnapshotKey],
				data.DataTypes[data.IndexChangelogKey],
			},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2007, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: downloadAllSharadarMetrics,
		},

		"Stock Tickers": {
			Name:        "Stock Tickers",
			Description: "Details about tradeable stocks.",
			DataTypes:   []*data.DataType{data.DataTypes[data.AssetKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2014, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: downloadAllSharadarTickers,
		},

		"SP500": {
			Name:        "SP500",
			Description: "S&P 500 index constituents and changes.",
			DataTypes: []*data.DataType{
				data.DataTypes[data.IndexSnapshotKey],
				data.DataTypes[data.IndexChangelogKey],
			},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(1957, 3, 4, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
		},
	}
}
