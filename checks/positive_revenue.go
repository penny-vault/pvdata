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

// PositiveRevenue checks that Revenues is >= 0.
type PositiveRevenue struct{}

func (c *PositiveRevenue) Name() string        { return "positive_revenue" }
func (c *PositiveRevenue) Description() string { return "Revenues must be >= 0" }
func (c *PositiveRevenue) Phase() CheckPhase   { return PhaseInline }
func (c *PositiveRevenue) Severity() CheckSeverity {
	return SeverityError
}
func (c *PositiveRevenue) DataTypes() []string { return []string{"fundamental"} }

func (c *PositiveRevenue) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental

	if f.Revenues < 0 {
		return []CheckResult{
			{
				CheckName:     c.Name(),
				Severity:      SeverityError,
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         "revenues",
				Message:       "revenues must be >= 0",
				Expected:      ">= 0",
				Actual:        fmt.Sprintf("%d", f.Revenues),
				DataType:      "fundamental",
			},
		}, false
	}

	return nil, false
}
