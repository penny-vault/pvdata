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

// RequiredFields checks that revenues, total_assets, and equity are non-zero.
type RequiredFields struct{}

func (c *RequiredFields) Name() string        { return "required_fields" }
func (c *RequiredFields) Description() string { return "revenues, total_assets, and equity must be non-zero" }
func (c *RequiredFields) Phase() CheckPhase   { return PhaseInline }
func (c *RequiredFields) Severity() CheckSeverity {
	return SeverityError
}
func (c *RequiredFields) DataTypes() []string { return []string{"fundamental"} }

func (c *RequiredFields) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental

	type field struct {
		name  string
		value int64
	}

	fields := []field{
		{"revenues", f.Revenues},
		{"total_assets", f.TotalAssets},
		{"equity", f.Equity},
	}

	var results []CheckResult

	for _, fld := range fields {
		if fld.value == 0 {
			results = append(results, CheckResult{
				CheckName:     c.Name(),
				Severity:      SeverityError,
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         fld.name,
				Message:       fld.name + " must be non-zero",
				Expected:      "!= 0",
				Actual:        fmt.Sprintf("%d", fld.value),
				DataType:      "fundamental",
			})
		}
	}

	return results, false
}
