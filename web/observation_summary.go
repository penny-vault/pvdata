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
	const dateFmt = "2006-01-02"

	switch {
	case obs.IndexSnapshot != nil:
		s := obs.IndexSnapshot

		return "index_snapshot", fmt.Sprintf("%s %d constituents %s",
			s.IndexTicker, len(s.Constituents), s.SnapshotDate.Format(dateFmt))

	case obs.IndexChange != nil:
		c := obs.IndexChange

		return "index_change", fmt.Sprintf("%s %s %s weight=%.4f %s",
			c.IndexTicker, c.Ticker, c.Action, c.Weight, c.EventDate.Format(dateFmt))

	case obs.EodQuote != nil:
		e := obs.EodQuote

		return "eod", fmt.Sprintf("%s close=%.2f vol=%.0f %s",
			e.Ticker, e.Close, e.Volume, e.Date.Format(dateFmt))

	case obs.Fundamental != nil:
		f := obs.Fundamental

		return "fundamental", fmt.Sprintf("%s %s %s",
			f.Ticker, f.Dimension, f.ReportPeriod.Format(dateFmt))

	case obs.EconomicIndicator != nil:
		e := obs.EconomicIndicator

		return "economic_indicator", fmt.Sprintf("%s value=%.2f %s",
			e.Series, e.Value, e.EventDate.Format(dateFmt))

	case obs.Rating != nil:
		r := obs.Rating

		return "rating", fmt.Sprintf("%s %s rating=%d %s",
			r.Ticker, r.Analyst, r.Rating, r.EventDate.Format(dateFmt))

	case obs.Metric != nil:
		m := obs.Metric

		return "metric", fmt.Sprintf("%s mktcap=%d pe=%.2f %s",
			m.Ticker, m.MarketCap, m.PE, m.EventDate.Format(dateFmt))

	case obs.Consensus != nil:
		c := obs.Consensus

		return "consensus", fmt.Sprintf("%s rec=%.2f analysts=%d target=%.2f %s",
			c.Ticker, c.AvgRecommendation, c.NumAnalysts, c.AvgTargetPrice, c.EventDate.Format(dateFmt))

	case obs.Estimate != nil:
		e := obs.Estimate

		return "estimate", fmt.Sprintf("%s %s value=%.2f analysts=%d %s",
			e.Ticker, e.Series, e.Value, e.NumAnalysts, e.EventDate.Format(dateFmt))

	case obs.AssetObject != nil:
		a := obs.AssetObject

		return "asset", fmt.Sprintf("%s %s %s (%s)",
			a.Ticker, a.AssetType, a.Name, a.PrimaryExchange)

	case obs.CustomObject != nil:
		c := obs.CustomObject

		return "custom", fmt.Sprintf("%s %s=%v %s",
			c.Ticker, c.Key, c.Value, c.EventDate.Format(dateFmt))

	case obs.MarketHoliday != nil:
		h := obs.MarketHoliday

		return "market_holiday", fmt.Sprintf("%s %s %s",
			h.Name, h.Market, h.EventDate.Format(dateFmt))

	default:
		return "observation", "observation"
	}
}
