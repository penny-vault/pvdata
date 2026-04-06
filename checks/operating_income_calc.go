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

// OperatingIncomeCalc checks that OperatingIncome = GrossProfit - OperatingExpenses within 0.1% tolerance.
type OperatingIncomeCalc struct{}

func (c *OperatingIncomeCalc) Name() string { return "operating_income_calc" }
func (c *OperatingIncomeCalc) Description() string {
	return "OperatingIncome must equal GrossProfit - OperatingExpenses (0.1% tolerance)"
}
func (c *OperatingIncomeCalc) Phase() CheckPhase { return PhaseInline }
func (c *OperatingIncomeCalc) Severity() CheckSeverity {
	return SeverityWarning
}
func (c *OperatingIncomeCalc) DataTypes() []string { return []string{"fundamental"} }

func (c *OperatingIncomeCalc) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental

	if f.OperatingIncome == 0 && f.GrossProfit == 0 && f.OperatingExpenses == 0 {
		return nil, false
	}

	expected := f.GrossProfit - f.OperatingExpenses
	diff := math.Abs(float64(f.OperatingIncome - expected))
	tolerance := math.Max(0.001*math.Abs(float64(f.GrossProfit)), 1000)

	if diff > tolerance {
		return []CheckResult{
			{
				CheckName:     c.Name(),
				Severity:      SeverityWarning,
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         "operating_income",
				Message:       "operating_income must equal gross_profit - operating_expenses",
				Expected:      fmt.Sprintf("%d", expected),
				Actual:        fmt.Sprintf("%d", f.OperatingIncome),
				DataType:      "fundamental",
			},
		}, false
	}

	return nil, false
}
