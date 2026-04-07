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
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
)

func init() {
	provider.Register("sec", &SEC{})
}

type SEC struct{}

func (s *SEC) Name() string {
	return "SEC"
}

func (s *SEC) Description() string {
	return "SEC EDGAR fundamentals extracted from 10-K and 10-Q XBRL filings via the companyfacts API"
}

func (s *SEC) ConfigDescription() map[string]string {
	return map[string]string{
		"userAgent": "Email address for SEC User-Agent header (e.g. pvdata/1.0 user@email.com):",
		"rateLimit": "Maximum requests per second to SEC EDGAR (default 10):",
	}
}

func (s *SEC) Datasets() map[string]provider.Dataset {
	return map[string]provider.Dataset{
		"Fundamentals": {
			Name:        "Fundamentals",
			Description: "Financial statement fundamentals from SEC EDGAR XBRL filings (10-K and 10-Q).",
			DataTypes:   []*data.DataType{data.DataTypes[data.FundamentalsKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2009, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: fetchFundamentals,
		},
	}
}

func fetchFundamentals(_ context.Context, _ *library.Subscription, _ chan<- *data.Observation, exit chan<- data.RunSummary) {
	// stub -- will be implemented in Task 8
	exit <- data.RunSummary{}
}
