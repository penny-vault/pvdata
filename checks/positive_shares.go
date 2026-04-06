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
package checks

import (
	"context"
	"fmt"

	"github.com/penny-vault/pvdata/data"
)

// PositiveShares checks that WeightedAverageSharesDiluted is > 0.
type PositiveShares struct{}

func (c *PositiveShares) Name() string        { return "positive_shares" }
func (c *PositiveShares) Description() string { return "WeightedAverageSharesDiluted must be > 0" }
func (c *PositiveShares) Phase() CheckPhase   { return PhaseInline }
func (c *PositiveShares) Severity() CheckSeverity {
	return SeverityCritical
}
func (c *PositiveShares) DataTypes() []string { return []string{"fundamental"} }

func (c *PositiveShares) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental

	if f.WeightedAverageSharesDiluted <= 0 {
		return []CheckResult{
			{
				CheckName:     c.Name(),
				Severity:      SeverityCritical,
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         "weighted_average_shares_diluted",
				Message:       "weighted_average_shares_diluted must be > 0",
				Expected:      "> 0",
				Actual:        fmt.Sprintf("%d", f.WeightedAverageSharesDiluted),
				DataType:      "fundamental",
			},
		}, true
	}

	return nil, false
}
