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

// CashFlowSum checks that OpCF + InvCF + FinCF ~= NetCashFlow within 1% tolerance (min 1M).
type CashFlowSum struct{}

func (c *CashFlowSum) Name() string { return "cash_flow_sum" }
func (c *CashFlowSum) Description() string {
	return "NetCashFlowFromOperations + NetCashFlowFromInvesting + NetCashFlowFromFinancing must approximately equal NetCashFlow (1% tolerance)"
}
func (c *CashFlowSum) Phase() CheckPhase { return PhaseInline }
func (c *CashFlowSum) Severity() CheckSeverity {
	return SeverityWarning
}
func (c *CashFlowSum) DataTypes() []string { return []string{"fundamental"} }

func (c *CashFlowSum) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental

	if f.NetCashFlow == 0 && f.NetCashFlowFromOperations == 0 && f.NetCashFlowFromInvesting == 0 && f.NetCashFlowFromFinancing == 0 {
		return nil, false
	}

	derived := f.NetCashFlowFromOperations + f.NetCashFlowFromInvesting + f.NetCashFlowFromFinancing
	diff := math.Abs(float64(f.NetCashFlow - derived))
	tolerance := math.Max(0.01*math.Abs(float64(f.NetCashFlow)), 1_000_000)

	if diff > tolerance {
		return []CheckResult{
			{
				CheckName:     c.Name(),
				Severity:      SeverityWarning,
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         "net_cash_flow",
				Message:       "net_cash_flow_from_operations + net_cash_flow_from_investing + net_cash_flow_from_financing must approximately equal net_cash_flow",
				Expected:      fmt.Sprintf("%d", f.NetCashFlow),
				Actual:        fmt.Sprintf("%d", derived),
				DataType:      "fundamental",
			},
		}, false
	}

	return nil, false
}
