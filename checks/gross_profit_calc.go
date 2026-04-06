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

// GrossProfitCalc checks that GrossProfit = Revenue - CostOfRevenue within 0.1% tolerance.
type GrossProfitCalc struct{}

func (c *GrossProfitCalc) Name() string { return "gross_profit_calc" }
func (c *GrossProfitCalc) Description() string {
	return "GrossProfit must equal Revenue - CostOfRevenue (0.1% tolerance)"
}
func (c *GrossProfitCalc) Phase() CheckPhase { return PhaseInline }
func (c *GrossProfitCalc) Severity() CheckSeverity {
	return SeverityWarning
}
func (c *GrossProfitCalc) DataTypes() []string { return []string{"fundamental"} }

func (c *GrossProfitCalc) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental

	if f.GrossProfit == 0 && f.Revenues == 0 && f.CostOfRevenue == 0 {
		return nil, false
	}

	expected := f.Revenues - f.CostOfRevenue
	diff := math.Abs(float64(f.GrossProfit - expected))
	tolerance := math.Max(0.001*math.Abs(float64(f.Revenues)), 1000)

	if diff > tolerance {
		return []CheckResult{
			{
				CheckName:     c.Name(),
				Severity:      SeverityWarning,
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         "gross_profit",
				Message:       "gross_profit must equal revenues - cost_of_revenue",
				Expected:      fmt.Sprintf("%d", expected),
				Actual:        fmt.Sprintf("%d", f.GrossProfit),
				DataType:      "fundamental",
			},
		}, false
	}

	return nil, false
}
