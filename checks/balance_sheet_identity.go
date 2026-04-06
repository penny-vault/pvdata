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

// BalanceSheetIdentity checks that Assets = Liabilities + Equity within 0.1% tolerance (min 1000).
type BalanceSheetIdentity struct{}

func (c *BalanceSheetIdentity) Name() string { return "balance_sheet_identity" }
func (c *BalanceSheetIdentity) Description() string {
	return "Assets must equal Liabilities + Equity (0.1% tolerance)"
}
func (c *BalanceSheetIdentity) Phase() CheckPhase { return PhaseInline }
func (c *BalanceSheetIdentity) Severity() CheckSeverity {
	return SeverityError
}
func (c *BalanceSheetIdentity) DataTypes() []string { return []string{"fundamental"} }

func (c *BalanceSheetIdentity) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental

	if f.TotalAssets == 0 && f.TotalLiabilities == 0 && f.Equity == 0 {
		return nil, false
	}

	expected := f.TotalLiabilities + f.Equity
	diff := math.Abs(float64(f.TotalAssets - expected))
	tolerance := math.Max(0.001*math.Abs(float64(f.TotalAssets)), 1000)

	if diff > tolerance {
		return []CheckResult{
			{
				CheckName:     c.Name(),
				Severity:      SeverityError,
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         "total_assets",
				Message:       "assets must equal liabilities + equity",
				Expected:      fmt.Sprintf("%d", expected),
				Actual:        fmt.Sprintf("%d", f.TotalAssets),
				DataType:      "fundamental",
			},
		}, false
	}

	return nil, false
}
