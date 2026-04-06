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
	"math"

	"github.com/penny-vault/pvdata/data"
)

// NetIncomeEPS checks that EPS * SharesDiluted ~= NetIncome within 5% tolerance (min 1M).
type NetIncomeEPS struct{}

func (c *NetIncomeEPS) Name() string { return "net_income_eps" }
func (c *NetIncomeEPS) Description() string {
	return "EPS * WeightedAverageSharesDiluted must approximately equal NetIncome (5% tolerance)"
}
func (c *NetIncomeEPS) Phase() CheckPhase { return PhaseInline }
func (c *NetIncomeEPS) Severity() CheckSeverity {
	return SeverityWarning
}
func (c *NetIncomeEPS) DataTypes() []string { return []string{"fundamental"} }

func (c *NetIncomeEPS) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental

	if f.EPSDiluted == 0 || f.WeightedAverageSharesDiluted == 0 || f.NetIncome == 0 {
		return nil, false
	}

	derived := f.EPSDiluted * float64(f.WeightedAverageSharesDiluted)
	diff := math.Abs(derived - float64(f.NetIncome))
	tolerance := math.Max(0.05*math.Abs(float64(f.NetIncome)), 1_000_000)

	if diff > tolerance {
		return []CheckResult{
			{
				CheckName:     c.Name(),
				Severity:      SeverityWarning,
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         "eps_diluted",
				Message:       "eps_diluted * weighted_average_shares_diluted must approximately equal net_income",
				Expected:      fmt.Sprintf("%d", f.NetIncome),
				Actual:        fmt.Sprintf("%.0f", derived),
				DataType:      "fundamental",
			},
		}, false
	}

	return nil, false
}
