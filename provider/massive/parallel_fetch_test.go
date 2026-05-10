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
package massive

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// daysIn returns the inclusive day count of a window.
func daysIn(w fetchWindow) int {
	return int(w.end.Sub(w.start).Hours()/24) + 1
}

var _ = Describe("splitFetchWindows", func() {
	d := func(year, month, day int) time.Time {
		return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	}

	Describe("degenerate inputs", func() {
		It("returns a single window when n <= 1", func() {
			start := d(2003, 1, 1)
			end := d(2026, 12, 31)

			Expect(splitFetchWindows(start, end, 0)).To(Equal([]fetchWindow{{start, end}}))
			Expect(splitFetchWindows(start, end, 1)).To(Equal([]fetchWindow{{start, end}}))
		})

		It("returns a single window when start equals end", func() {
			t := d(2024, 6, 1)
			Expect(splitFetchWindows(t, t, 4)).To(Equal([]fetchWindow{{t, t}}))
		})

		It("returns a single window when start is after end", func() {
			start := d(2024, 6, 2)
			end := d(2024, 6, 1)

			Expect(splitFetchWindows(start, end, 4)).To(Equal([]fetchWindow{{start, end}}))
		})

		It("returns a single window when range is shorter than n days", func() {
			// 2 days, n=4 → can't split, fall back to 1 window
			start := d(2024, 6, 1)
			end := d(2024, 6, 2)

			Expect(splitFetchWindows(start, end, 4)).To(Equal([]fetchWindow{{start, end}}))
		})

		It("returns a single window when range is below the parallel threshold", func() {
			// 14-day lookback (typical incremental run): below
			// minDaysForParallel even though 14 > n. Stay serial
			// to avoid spawning goroutines for what is almost
			// certainly a single page of results.
			start := d(2024, 6, 1)
			end := d(2024, 6, 14)

			Expect(splitFetchWindows(start, end, 4)).To(Equal([]fetchWindow{{start, end}}))
		})

		It("returns a single window for a 6-month lookback", func() {
			start := d(2024, 1, 1)
			end := d(2024, 7, 1)

			Expect(splitFetchWindows(start, end, 4)).To(Equal([]fetchWindow{{start, end}}))
		})

		It("starts parallelising once the range crosses the threshold", func() {
			start := d(2024, 1, 1)
			end := d(2025, 1, 1) // 367 days, above threshold

			windows := splitFetchWindows(start, end, 4)
			Expect(windows).To(HaveLen(4))
		})
	})

	Describe("partition correctness", func() {
		It("covers the full range exactly with no gaps and no overlaps", func() {
			start := d(2002, 5, 16)
			end := d(2026, 5, 9)
			windows := splitFetchWindows(start, end, 4)

			Expect(windows).To(HaveLen(4))
			Expect(windows[0].start).To(Equal(start))
			Expect(windows[3].end).To(Equal(end))

			for i := 1; i < len(windows); i++ {
				gap := windows[i].start.Sub(windows[i-1].end)
				Expect(gap).To(Equal(24*time.Hour),
					"window %d should start exactly 1 day after window %d ends", i, i-1)
			}
		})

		It("the union of all windows covers every day of the input range", func() {
			start := d(2024, 1, 1)
			end := d(2024, 1, 12)
			windows := splitFetchWindows(start, end, 4)

			covered := map[string]int{}

			for _, w := range windows {
				for cur := w.start; !cur.After(w.end); cur = cur.AddDate(0, 0, 1) {
					covered[cur.Format("2006-01-02")]++
				}
			}

			expected := map[string]int{}
			for cur := start; !cur.After(end); cur = cur.AddDate(0, 0, 1) {
				expected[cur.Format("2006-01-02")] = 1
			}

			Expect(covered).To(Equal(expected),
				"every day must be covered exactly once across all windows")
		})

		It("produces n windows when range is above the parallel threshold", func() {
			windows := splitFetchWindows(d(2020, 1, 1), d(2023, 12, 31), 4)
			Expect(windows).To(HaveLen(4))
		})

		It("distributes days evenly when total is divisible by n", func() {
			// 1460 day-difference + 1 = 1461 inclusive days; 1461 / 4 = 365 r 1.
			windows := splitFetchWindows(d(2020, 1, 1), d(2023, 12, 31), 4)
			Expect(windows).To(HaveLen(4))

			// First three windows get 365 days each; remainder
			// goes to the last window.
			for _, w := range windows[:3] {
				Expect(daysIn(w)).To(Equal(365))
			}

			Expect(daysIn(windows[3])).To(Equal(366))
		})

		It("places remainder days into the last window for uneven divisions", func() {
			// 1463-day range (just above 4×365): expect 365+365+365+368.
			windows := splitFetchWindows(d(2020, 1, 1), d(2024, 1, 2), 4)
			Expect(windows).To(HaveLen(4))

			for i, expected := range []int{365, 365, 365, 368} {
				Expect(daysIn(windows[i])).To(Equal(expected),
					"window %d should have %d days", i, expected)
			}
		})
	})

	Describe("the 24-year backfill case", func() {
		It("never overlaps on seam dates for the realistic input", func() {
			start := d(2002, 5, 16)
			end := d(2026, 5, 9)
			windows := splitFetchWindows(start, end, numFetchWindows)

			Expect(windows).To(HaveLen(numFetchWindows))

			for i := 1; i < len(windows); i++ {
				Expect(windows[i].start.After(windows[i-1].end)).To(BeTrue(),
					"window %d start must be strictly after window %d end", i, i-1)
			}
		})

		It("each window is roughly 6 years for a 24-year range", func() {
			start := d(2002, 5, 16)
			end := d(2026, 5, 9)
			windows := splitFetchWindows(start, end, 4)

			for _, w := range windows {
				years := w.end.Sub(w.start).Hours() / 24 / 365.25
				Expect(years).To(BeNumerically("~", 6.0, 0.5))
			}
		})
	})
})
