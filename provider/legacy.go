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
package provider

import (
	"github.com/penny-vault/pvdata/data"
)

// Legacy is a provider for legacy database data that has been migrated.
// It has no Fetch function -- data is populated by the migrate-legacy command.
type Legacy struct{}

func (l *Legacy) Name() string {
	return "Legacy"
}

func (l *Legacy) ConfigDescription() map[string]string {
	return map[string]string{}
}

func (l *Legacy) Description() string {
	return "Migrated data from a legacy penny-vault database. Not fetchable."
}

func (l *Legacy) Datasets() map[string]Dataset {
	return map[string]Dataset{
		"eod": {
			Name:        "eod",
			Description: "Legacy EOD price data",
			DataTypes:   []*data.DataType{data.DataTypes[data.EODKey]},
		},
		"assets": {
			Name:        "assets",
			Description: "Legacy asset descriptions",
			DataTypes:   []*data.DataType{data.DataTypes[data.AssetKey]},
		},
		"market-holidays": {
			Name:        "market-holidays",
			Description: "Legacy market holidays",
			DataTypes:   []*data.DataType{data.DataTypes[data.MarketHolidaysKey]},
		},
		"Zacks Screener Data": {
			Name:        "Zacks Screener Data",
			Description: "Legacy Zacks screener data",
			DataTypes: []*data.DataType{
				data.DataTypes[data.RatingKey],
				data.DataTypes[data.MetricKey],
				data.DataTypes[data.EstimateKey],
				data.DataTypes[data.ConsensusKey],
			},
		},
	}
}
