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
package cmd

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("lookbackFromStartDate", func() {
	now := time.Date(2026, 5, 9, 12, 0, 0, 0, time.UTC)

	It("converts a date in the past to the elapsed duration", func() {
		d, err := lookbackFromStartDate("2026-04-29", now)
		Expect(err).NotTo(HaveOccurred())
		Expect(d).To(Equal(10*24*time.Hour + 12*time.Hour))
	})

	It("rejects an empty value", func() {
		_, err := lookbackFromStartDate("", now)
		Expect(err).To(MatchError(ContainSubstring("empty start-date")))
	})

	It("rejects a non-date string", func() {
		_, err := lookbackFromStartDate("yesterday", now)
		Expect(err).To(MatchError(ContainSubstring("YYYY-MM-DD")))
	})

	It("rejects a future date", func() {
		_, err := lookbackFromStartDate("2099-01-01", now)
		Expect(err).To(MatchError(ContainSubstring("not before now")))
	})

	It("accepts a date earlier on the same UTC day", func() {
		// 2026-05-09 parses to 00:00 UTC; now is noon UTC, so this is
		// a valid 12-hour lookback (today's data so far).
		d, err := lookbackFromStartDate("2026-05-09", now)
		Expect(err).NotTo(HaveOccurred())
		Expect(d).To(Equal(12 * time.Hour))
	})

	It("trims whitespace", func() {
		d, err := lookbackFromStartDate("  2026-05-08  ", now)
		Expect(err).NotTo(HaveOccurred())
		Expect(d).To(Equal(36 * time.Hour))
	})
})
