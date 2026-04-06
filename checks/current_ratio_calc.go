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

// CurrentRatioCalc checks that CurrentRatio ~= CurrentAssets/CurrentLiabilities within 0.05 absolute tolerance.
type CurrentRatioCalc struct{}

func (c *CurrentRatioCalc) Name() string { return "current_ratio_calc" }
func (c *CurrentRatioCalc) Description() string {
	return "CurrentRatio must approximately equal CurrentAssets / CurrentLiabilities (0.05 tolerance)"
}
func (c *CurrentRatioCalc) Phase() CheckPhase { return PhaseInline }
func (c *CurrentRatioCalc) Severity() CheckSeverity {
	return SeverityWarning
}
func (c *CurrentRatioCalc) DataTypes() []string { return []string{"fundamental"} }

func (c *CurrentRatioCalc) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental

	if f.CurrentRatio == 0 || f.CurrentAssets == 0 || f.CurrentLiabilities == 0 {
		return nil, false
	}

	derived := float64(f.CurrentAssets) / float64(f.CurrentLiabilities)
	diff := math.Abs(f.CurrentRatio - derived)

	if diff > 0.05 {
		return []CheckResult{
			{
				CheckName:     c.Name(),
				Severity:      SeverityWarning,
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         "current_ratio",
				Message:       "current_ratio must approximately equal current_assets / current_liabilities",
				Expected:      fmt.Sprintf("%.4f", derived),
				Actual:        fmt.Sprintf("%.4f", f.CurrentRatio),
				DataType:      "fundamental",
			},
		}, false
	}

	return nil, false
}
