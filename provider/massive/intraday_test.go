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

var _ = Describe("Flat-files 1-minute helpers", func() {
	Describe("minuteAggsKey", func() {
		It("zero-pads single-digit months", func() {
			d := time.Date(2026, 5, 8, 0, 0, 0, 0, time.UTC)
			Expect(minuteAggsKey(d)).To(Equal("us_stocks_sip/minute_aggs_v1/2026/05/2026-05-08.csv.gz"))
		})

		It("renders two-digit months without extra padding", func() {
			d := time.Date(2025, 11, 5, 0, 0, 0, 0, time.UTC)
			Expect(minuteAggsKey(d)).To(Equal("us_stocks_sip/minute_aggs_v1/2025/11/2025-11-05.csv.gz"))
		})
	})

	Describe("window_start nanosecond conversion", func() {
		It("decodes a known minute boundary back to UTC", func() {
			// 1778247000000000000 ns == 2026-05-08 13:30:00 UTC,
			// which is 09:30 ET (regular session open) during DST.
			ws := int64(1778247000000000000)
			got := time.Unix(0, ws).UTC()
			Expect(got).To(Equal(time.Date(2026, 5, 8, 13, 30, 0, 0, time.UTC)))
		})

		It("preserves seconds at minute granularity", func() {
			start := time.Date(2026, 5, 8, 13, 47, 0, 0, time.UTC)
			ws := start.UnixNano()
			Expect(time.Unix(0, ws).UTC()).To(Equal(start))
		})
	})
})
