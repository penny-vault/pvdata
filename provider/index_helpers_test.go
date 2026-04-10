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
package provider

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("shouldTakeSnapshot", func() {
	now := time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC)

	It("returns true when no previous snapshot exists", func() {
		Expect(ShouldTakeSnapshot(time.Time{}, now, "weekly")).To(BeTrue())
	})

	It("returns true when daily and last snapshot was yesterday", func() {
		yesterday := now.AddDate(0, 0, -1)
		Expect(ShouldTakeSnapshot(yesterday, now, "daily")).To(BeTrue())
	})

	It("returns false when daily and last snapshot was today", func() {
		Expect(ShouldTakeSnapshot(now, now, "daily")).To(BeFalse())
	})

	It("returns true when weekly and last snapshot was 8 days ago", func() {
		eightDaysAgo := now.AddDate(0, 0, -8)
		Expect(ShouldTakeSnapshot(eightDaysAgo, now, "weekly")).To(BeTrue())
	})

	It("returns false when weekly and last snapshot was 3 days ago", func() {
		threeDaysAgo := now.AddDate(0, 0, -3)
		Expect(ShouldTakeSnapshot(threeDaysAgo, now, "weekly")).To(BeFalse())
	})

	It("returns true when monthly and last snapshot was 32 days ago", func() {
		thirtyTwoDaysAgo := now.AddDate(0, 0, -32)
		Expect(ShouldTakeSnapshot(thirtyTwoDaysAgo, now, "monthly")).To(BeTrue())
	})

	It("returns false when monthly and last snapshot was 15 days ago", func() {
		fifteenDaysAgo := now.AddDate(0, 0, -15)
		Expect(ShouldTakeSnapshot(fifteenDaysAgo, now, "monthly")).To(BeFalse())
	})

	It("returns true when quarterly and last snapshot was 95 days ago", func() {
		ninetyFiveDaysAgo := now.AddDate(0, 0, -95)
		Expect(ShouldTakeSnapshot(ninetyFiveDaysAgo, now, "quarterly")).To(BeTrue())
	})

	It("defaults to weekly for unknown frequency", func() {
		eightDaysAgo := now.AddDate(0, 0, -8)
		Expect(ShouldTakeSnapshot(eightDaysAgo, now, "bogus")).To(BeTrue())
	})
})

var _ = Describe("ShouldTakeAnnualSnapshot", func() {
	It("returns true on cold start (zero last snapshot)", func() {
		current := time.Date(2025, 6, 15, 0, 0, 0, 0, time.UTC)
		Expect(ShouldTakeAnnualSnapshot(time.Time{}, current, time.December, 21)).To(BeTrue())
	})

	It("returns true on the anchor date when last snapshot was previous year", func() {
		last := time.Date(2024, 12, 21, 0, 0, 0, 0, time.UTC)
		current := time.Date(2025, 12, 21, 0, 0, 0, 0, time.UTC)
		Expect(ShouldTakeAnnualSnapshot(last, current, time.December, 21)).To(BeTrue())
	})

	It("returns true after the anchor date when last snapshot was previous year", func() {
		last := time.Date(2024, 12, 22, 0, 0, 0, 0, time.UTC)
		current := time.Date(2025, 12, 23, 0, 0, 0, 0, time.UTC)
		Expect(ShouldTakeAnnualSnapshot(last, current, time.December, 21)).To(BeTrue())
	})

	It("returns false before the anchor date", func() {
		last := time.Date(2024, 12, 21, 0, 0, 0, 0, time.UTC)
		current := time.Date(2025, 12, 19, 0, 0, 0, 0, time.UTC)
		Expect(ShouldTakeAnnualSnapshot(last, current, time.December, 21)).To(BeFalse())
	})

	It("returns false when snapshot was already taken this year after anchor", func() {
		last := time.Date(2025, 12, 22, 0, 0, 0, 0, time.UTC)
		current := time.Date(2025, 12, 23, 0, 0, 0, 0, time.UTC)
		Expect(ShouldTakeAnnualSnapshot(last, current, time.December, 21)).To(BeFalse())
	})

	It("returns false on the same day the snapshot was taken", func() {
		last := time.Date(2025, 12, 21, 0, 0, 0, 0, time.UTC)
		current := time.Date(2025, 12, 21, 0, 0, 0, 0, time.UTC)
		Expect(ShouldTakeAnnualSnapshot(last, current, time.December, 21)).To(BeFalse())
	})
})

var _ = Describe("diffSnapshots", func() {
	It("returns all as added when previous is empty", func() {
		current := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
			"MSFT": {CompositeFigi: "BBG000BPH459", Weight: 0.04},
		}
		adds, removes, weightChanges := DiffSnapshots(current, map[string]IndexMember{})
		Expect(adds).To(HaveLen(2))
		Expect(removes).To(BeEmpty())
		Expect(weightChanges).To(BeEmpty())
	})

	It("returns all as removed when current is empty", func() {
		previous := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
		}
		adds, removes, weightChanges := DiffSnapshots(map[string]IndexMember{}, previous)
		Expect(adds).To(BeEmpty())
		Expect(removes).To(HaveLen(1))
		Expect(removes).To(HaveKey("AAPL"))
		Expect(weightChanges).To(BeEmpty())
	})

	It("detects additions and removals", func() {
		current := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
			"GOOG": {CompositeFigi: "BBG009S39JX6", Weight: 0.03},
		}
		previous := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
			"MSFT": {CompositeFigi: "BBG000BPH459", Weight: 0.04},
		}
		adds, removes, weightChanges := DiffSnapshots(current, previous)
		Expect(adds).To(HaveLen(1))
		Expect(adds).To(HaveKey("GOOG"))
		Expect(removes).To(HaveLen(1))
		Expect(removes).To(HaveKey("MSFT"))
		Expect(weightChanges).To(BeEmpty())
	})

	It("returns empty when sets are identical", func() {
		current := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
		}
		previous := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
		}
		adds, removes, weightChanges := DiffSnapshots(current, previous)
		Expect(adds).To(BeEmpty())
		Expect(removes).To(BeEmpty())
		Expect(weightChanges).To(BeEmpty())
	})

	It("detects significant weight change above 0.01 threshold", func() {
		current := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.065},
		}
		previous := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
		}
		adds, removes, weightChanges := DiffSnapshots(current, previous)
		Expect(adds).To(BeEmpty())
		Expect(removes).To(BeEmpty())
		Expect(weightChanges).To(HaveLen(1))
		Expect(weightChanges).To(HaveKey("AAPL"))
		Expect(weightChanges["AAPL"].Weight).To(BeNumerically("~", 0.065, 0.0001))
	})

	It("ignores weight change below 0.01 threshold", func() {
		current := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.055},
		}
		previous := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
		}
		adds, removes, weightChanges := DiffSnapshots(current, previous)
		Expect(adds).To(BeEmpty())
		Expect(removes).To(BeEmpty())
		Expect(weightChanges).To(BeEmpty())
	})

	It("detects weight change exactly at 0.01 boundary", func() {
		current := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.06},
		}
		previous := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
		}
		_, _, weightChanges := DiffSnapshots(current, previous)
		Expect(weightChanges).To(HaveLen(1))
	})

	It("handles simultaneous adds, removes, and weight changes", func() {
		current := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.08},
			"GOOG": {CompositeFigi: "BBG009S39JX6", Weight: 0.03},
		}
		previous := map[string]IndexMember{
			"AAPL": {CompositeFigi: "BBG000B9XRY4", Weight: 0.05},
			"MSFT": {CompositeFigi: "BBG000BPH459", Weight: 0.04},
		}
		adds, removes, weightChanges := DiffSnapshots(current, previous)
		Expect(adds).To(HaveLen(1))
		Expect(adds).To(HaveKey("GOOG"))
		Expect(removes).To(HaveLen(1))
		Expect(removes).To(HaveKey("MSFT"))
		Expect(weightChanges).To(HaveLen(1))
		Expect(weightChanges).To(HaveKey("AAPL"))
	})
})

var _ = Describe("tradingDays", func() {
	It("returns an error when pool is nil", func() {
		start := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
		end := time.Date(2026, 1, 9, 0, 0, 0, 0, time.UTC)
		_, err := TradingDays(context.Background(), nil, start, end)
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("currentIndexMembers", func() {
	It("returns empty map when pool is nil", func() {
		asOf := time.Date(2026, 3, 30, 0, 0, 0, 0, time.UTC)
		result := CurrentIndexMembers(context.Background(), nil, "test_snapshot", "test_changelog", "sp500", asOf)
		Expect(result).To(BeEmpty())
	})
})

