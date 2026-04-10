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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("rollingStats", func() {
	mkRow := func(date string, close, vol float64) eodRow {
		t, _ := time.Parse("2006-01-02", date)
		return eodRow{Date: t, Close: close, Volume: vol}
	}

	It("returns count and median over a window with all rows in range", func() {
		rows := []eodRow{
			mkRow("2024-01-02", 10, 100), // dv=1000
			mkRow("2024-01-03", 20, 100), // dv=2000
			mkRow("2024-01-04", 30, 100), // dv=3000
		}
		windowStart, _ := time.Parse("2006-01-02", "2024-01-01")
		windowEnd, _ := time.Parse("2006-01-02", "2024-01-04")
		stats := rollingStats(rows, windowStart, windowEnd)
		Expect(stats.dayCount).To(Equal(3))
		Expect(stats.medianDV).To(Equal(2000.0))
	})

	It("ignores rows outside the window", func() {
		rows := []eodRow{
			mkRow("2023-12-30", 10, 100),
			mkRow("2024-01-02", 20, 100), // dv=2000
			mkRow("2024-01-03", 30, 100), // dv=3000
			mkRow("2024-01-05", 40, 100),
		}
		windowStart, _ := time.Parse("2006-01-02", "2024-01-02")
		windowEnd, _ := time.Parse("2006-01-02", "2024-01-04")
		stats := rollingStats(rows, windowStart, windowEnd)
		Expect(stats.dayCount).To(Equal(2))
		Expect(stats.medianDV).To(Equal(2500.0))
	})

	It("returns zero stats for empty input", func() {
		windowStart, _ := time.Parse("2006-01-02", "2024-01-01")
		windowEnd, _ := time.Parse("2006-01-02", "2024-01-31")
		stats := rollingStats(nil, windowStart, windowEnd)
		Expect(stats.dayCount).To(Equal(0))
		Expect(stats.medianDV).To(Equal(0.0))
	})

	It("computes median correctly for odd-count window", func() {
		rows := []eodRow{
			mkRow("2024-01-01", 10, 100), // dv=1000
			mkRow("2024-01-02", 20, 100), // dv=2000
			mkRow("2024-01-03", 30, 100), // dv=3000
			mkRow("2024-01-04", 40, 100), // dv=4000
			mkRow("2024-01-05", 50, 100), // dv=5000
		}
		windowStart, _ := time.Parse("2006-01-02", "2024-01-01")
		windowEnd, _ := time.Parse("2006-01-02", "2024-01-05")
		stats := rollingStats(rows, windowStart, windowEnd)
		Expect(stats.dayCount).To(Equal(5))
		Expect(stats.medianDV).To(Equal(3000.0))
	})

	It("computes median correctly for even-count window", func() {
		rows := []eodRow{
			mkRow("2024-01-01", 10, 100), // dv=1000
			mkRow("2024-01-02", 20, 100), // dv=2000
			mkRow("2024-01-03", 30, 100), // dv=3000
			mkRow("2024-01-04", 40, 100), // dv=4000
		}
		windowStart, _ := time.Parse("2006-01-02", "2024-01-01")
		windowEnd, _ := time.Parse("2006-01-02", "2024-01-04")
		stats := rollingStats(rows, windowStart, windowEnd)
		Expect(stats.dayCount).To(Equal(4))
		Expect(stats.medianDV).To(Equal(2500.0))
	})
})
