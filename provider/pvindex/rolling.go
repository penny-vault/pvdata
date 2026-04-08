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
package pvindex

import (
	"sort"
	"time"
)

// eodRow is the in-memory representation of one EOD row used by the rolling-window
// computation. We do not reuse data.Eod because it carries OHLC fields we don't need
// and we want a tightly-packed struct for the chunk loader's memory budget.
type eodRow struct {
	Date   time.Time
	Close  float64
	Volume float64
}

// stats holds the rolling-window statistics for a single FIGI.
type stats struct {
	dayCount int
	medianDV float64
}

// rollingStats computes (count, median dollar volume) over the rows whose Date
// falls within [windowStart, windowEnd] inclusive. Rows are assumed to be sorted by
// date but the implementation does not require it. Returns zero values if no rows
// fall in the window.
func rollingStats(rows []eodRow, windowStart, windowEnd time.Time) stats {
	if len(rows) == 0 {
		return stats{}
	}

	dvs := make([]float64, 0, len(rows))

	for _, r := range rows {
		if r.Date.Before(windowStart) || r.Date.After(windowEnd) {
			continue
		}

		dvs = append(dvs, r.Close*r.Volume)
	}

	if len(dvs) == 0 {
		return stats{}
	}

	sort.Float64s(dvs)

	var median float64

	mid := len(dvs) / 2
	if len(dvs)%2 == 1 {
		median = dvs[mid]
	} else {
		median = (dvs[mid-1] + dvs[mid]) / 2
	}

	return stats{dayCount: len(dvs), medianDV: median}
}
