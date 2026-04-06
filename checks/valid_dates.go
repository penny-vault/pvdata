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
	"time"

	"github.com/penny-vault/pvdata/data"
)

// ValidDates checks that EventDate, ReportPeriod, and DateKey are not in the future.
type ValidDates struct{}

func (c *ValidDates) Name() string        { return "valid_dates" }
func (c *ValidDates) Description() string { return "EventDate, ReportPeriod, and DateKey must not be in the future" }
func (c *ValidDates) Phase() CheckPhase   { return PhaseInline }
func (c *ValidDates) Severity() CheckSeverity {
	return SeverityCritical
}
func (c *ValidDates) DataTypes() []string { return []string{"fundamental"} }

func (c *ValidDates) Validate(_ context.Context, obs *data.Observation) ([]CheckResult, bool) {
	if obs.Fundamental == nil {
		return nil, false
	}

	f := obs.Fundamental
	now := time.Now().UTC()

	type dateField struct {
		name  string
		value time.Time
	}

	fields := []dateField{
		{"event_date", f.EventDate},
		{"report_period", f.ReportPeriod},
		{"date_key", f.DateKey},
	}

	var results []CheckResult

	for _, df := range fields {
		if df.value.IsZero() {
			continue
		}

		if df.value.After(now) {
			results = append(results, CheckResult{
				CheckName:     c.Name(),
				Severity:      SeverityCritical,
				Ticker:        f.Ticker,
				CompositeFigi: f.CompositeFigi,
				Dimension:     f.Dimension,
				EventDate:     f.EventDate,
				Field:         df.name,
				Message:       df.name + " must not be in the future",
				Expected:      "<= " + now.Format(time.DateOnly),
				Actual:        df.value.Format(time.DateOnly),
				DataType:      "fundamental",
			})
		}
	}

	if len(results) > 0 {
		return results, true
	}

	return nil, false
}
