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
package web

import (
	"fmt"

	"github.com/penny-vault/pvdata/data"
)

// summarizeObservation returns a type tag and a human-readable one-line summary.
func summarizeObservation(obs *data.Observation) (string, string) {
	switch {
	case obs.IndexSnapshot != nil:
		s := obs.IndexSnapshot

		return "index_snapshot", fmt.Sprintf("%s %d constituents %s",
			s.IndexTicker, len(s.Constituents), s.SnapshotDate.Format("2006-01-02"))

	case obs.IndexChange != nil:
		c := obs.IndexChange

		return "index_change", fmt.Sprintf("%s %s %s weight=%.4f %s",
			c.IndexTicker, c.Ticker, c.Action, c.Weight, c.EventDate.Format("2006-01-02"))

	case obs.EodQuote != nil:
		e := obs.EodQuote

		return "eod", fmt.Sprintf("%s close=%.2f vol=%.0f %s",
			e.Ticker, e.Close, e.Volume, e.Date.Format("2006-01-02"))

	case obs.Fundamental != nil:
		f := obs.Fundamental

		return "fundamental", fmt.Sprintf("%s %s %s",
			f.Ticker, f.Dimension, f.ReportPeriod.Format("2006-01-02"))

	default:
		return "observation", "observation"
	}
}
