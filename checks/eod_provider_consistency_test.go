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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("EODProviderConsistency tolerance helpers", func() {
	Describe("pricesAgree", func() {
		It("treats exact equality as agreement", func() {
			Expect(pricesAgree(123.45, 123.45)).To(BeTrue())
		})

		It("treats sub-cent differences as agreement (absolute tolerance)", func() {
			// $5 stock with 0.5 cent difference — well under priceAbsTolerance.
			Expect(pricesAgree(5.001, 5.005)).To(BeTrue())
		})

		It("treats penny differences as agreement", func() {
			Expect(pricesAgree(118.70, 118.71)).To(BeTrue())
		})

		It("rejects multi-cent differences on cheap stocks", func() {
			// 5-cent gap on a $5 stock: above absolute, above 0.01% relative.
			Expect(pricesAgree(5.00, 5.05)).To(BeFalse())
		})

		It("treats 0.01% relative differences as agreement on expensive stocks", func() {
			// $5000 stock with 25 cent gap: above absTolerance but below relTolerance.
			Expect(pricesAgree(5000.00, 5000.25)).To(BeTrue())
		})

		It("rejects 1% differences regardless of price", func() {
			Expect(pricesAgree(100.00, 101.00)).To(BeFalse())
			Expect(pricesAgree(5000.00, 5050.00)).To(BeFalse())
		})

		It("handles zero values without divide-by-zero", func() {
			Expect(pricesAgree(0, 0)).To(BeTrue())
			Expect(pricesAgree(0, 0.005)).To(BeTrue()) // within abs
			Expect(pricesAgree(0, 1.0)).To(BeFalse())  // 1.0 > abs and rel is undefined
		})
	})

	Describe("volumesAgree", func() {
		It("treats exact equality as agreement", func() {
			Expect(volumesAgree(1_000_000, 1_000_000)).To(BeTrue())
		})

		It("treats sub-0.5%% differences as agreement", func() {
			// 1M vs 1.004M = 0.4% — within tolerance.
			Expect(volumesAgree(1_000_000, 1_004_000)).To(BeTrue())
		})

		It("rejects 1%% differences", func() {
			Expect(volumesAgree(1_000_000, 1_010_000)).To(BeFalse())
		})

		It("rejects any disagreement with both zero is impossible (sentinel guard)", func() {
			// Both zero is exact equality, handled by the early return.
			Expect(volumesAgree(0, 0)).To(BeTrue())
		})

		It("handles small absolute volumes without false agreement", func() {
			// 100 vs 200 is 50% — should be flagged.
			Expect(volumesAgree(100, 200)).To(BeFalse())
		})
	})

	Describe("comparePriceFields", func() {
		It("emits no findings when every field agrees", func() {
			results := comparePriceFields("a", "b", "AAPL", "BBG000B9XRY4",
				time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC),
				100.0, 100.0, 101.0, 101.0, 99.0, 99.0, 100.5, 100.5)
			Expect(results).To(BeEmpty())
		})

		It("emits one finding per disagreeing field", func() {
			eventDate := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
			results := comparePriceFields("a", "b", "AAPL", "BBG000B9XRY4", eventDate,
				100.0, 100.0, // open agrees
				101.0, 102.0, // high disagrees
				99.0, 98.0, // low disagrees
				100.5, 100.5) // close agrees

			Expect(results).To(HaveLen(2))

			fields := []string{results[0].Field, results[1].Field}
			Expect(fields).To(ConsistOf("high", "low"))

			for _, r := range results {
				Expect(r.CheckName).To(Equal("eod_provider_consistency"))
				Expect(r.Severity).To(Equal(SeverityWarning))
				Expect(r.Ticker).To(Equal("AAPL"))
				Expect(r.EventDate).To(Equal(eventDate))
			}
		})
	})

	Describe("compareVolume", func() {
		It("returns nil when volumes agree", func() {
			result := compareVolume("a", "b", "AAPL", "BBG000B9XRY4",
				time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC), 1_000_000, 1_000_000)
			Expect(result).To(BeNil())
		})

		It("returns a finding when volumes disagree beyond tolerance", func() {
			eventDate := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
			result := compareVolume("massive", "tiingo", "AAPL", "BBG000B9XRY4", eventDate, 1_000_000, 2_000_000)

			Expect(result).NotTo(BeNil())
			Expect(result.Field).To(Equal("volume"))
			Expect(result.Expected).To(ContainSubstring("massive=1000000"))
			Expect(result.Actual).To(ContainSubstring("tiingo=2000000"))
		})
	})

	Describe("EODProviderConsistency metadata", func() {
		It("declares the eod data type", func() {
			c := &EODProviderConsistency{}
			Expect(c.DataTypes()).To(Equal([]string{"eod"}))
			Expect(c.Phase()).To(Equal(PhaseAudit))
			Expect(c.Severity()).To(Equal(SeverityWarning))
			Expect(c.Name()).To(Equal("eod_provider_consistency"))
		})
	})
})
