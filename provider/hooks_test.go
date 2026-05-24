package provider

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("ComputeAdjustedClose", func() {
	It("returns unadjusted prices when no dividends or splits", func() {
		rows := []EodRow{
			{Close: 100.0, Dividend: 0, SplitFactor: 1.0},
			{Close: 99.0, Dividend: 0, SplitFactor: 1.0},
			{Close: 98.0, Dividend: 0, SplitFactor: 1.0},
		}
		result := ComputeAdjustedClose(rows)
		Expect(result[0].AdjClose).To(BeNumerically("~", 100.0, 0.01))
		Expect(result[1].AdjClose).To(BeNumerically("~", 99.0, 0.01))
		Expect(result[2].AdjClose).To(BeNumerically("~", 98.0, 0.01))
	})

	It("adjusts for a 2:1 stock split", func() {
		// Rows in reverse chronological order (newest first).
		// Row 1 has SplitFactor=2, meaning a 2:1 split occurred on that date.
		// The CRSP adjustment divides older prices by the accumulated split factor.
		rows := []EodRow{
			{Close: 50.0, Dividend: 0, SplitFactor: 1.0},  // newest
			{Close: 100.0, Dividend: 0, SplitFactor: 2.0}, // split date
			{Close: 99.0, Dividend: 0, SplitFactor: 1.0},  // oldest
		}
		result := ComputeAdjustedClose(rows)
		Expect(result[0].AdjClose).To(BeNumerically("~", 50.0, 0.01))
		Expect(result[1].AdjClose).To(BeNumerically("~", 100.0, 0.01))
		Expect(result[2].AdjClose).To(BeNumerically("~", 49.5, 0.01))
	})

	It("adjusts for a dividend", func() {
		// Rows in reverse chronological order (newest first).
		// Row 1 has Dividend=2.0. The CRSP adjustment reduces older prices
		// by the dividend ratio: adjustFactor *= (1 + dividend/close).
		// After row 1: adjustFactor = 1.0 * (1 + 2/102) = 1.01961
		// Row 2: AdjClose = 101 / 1.01961 ~= 99.07
		rows := []EodRow{
			{Close: 100.0, Dividend: 0, SplitFactor: 1.0},   // newest
			{Close: 102.0, Dividend: 2.0, SplitFactor: 1.0}, // ex-dividend date
			{Close: 101.0, Dividend: 0, SplitFactor: 1.0},   // oldest
		}
		result := ComputeAdjustedClose(rows)
		Expect(result[0].AdjClose).To(BeNumerically("~", 100.0, 0.01))
		Expect(result[1].AdjClose).To(BeNumerically("~", 102.0, 0.01))
		Expect(result[2].AdjClose).To(BeNumerically("~", 99.07, 0.1))
	})

	It("handles zero close price by resetting adjust factor", func() {
		rows := []EodRow{
			{Close: 10.0, Dividend: 0, SplitFactor: 1.0},
			{Close: 0.0, Dividend: 0, SplitFactor: 1.0},
			{Close: 9.0, Dividend: 0, SplitFactor: 1.0},
		}
		result := ComputeAdjustedClose(rows)
		Expect(result).To(HaveLen(3))
		// First row: adjustFactor=1.0, so AdjClose = 10.0/1.0 = 10.0
		Expect(result[0].AdjClose).To(BeNumerically("~", 10.0, 0.01))
		// Second row: adjustFactor=1.0 (unchanged), AdjClose = 0.0/1.0 = 0.0
		// Then adjustFactor resets to 1.0 because Close is 0
		Expect(result[1].AdjClose).To(BeNumerically("~", 0.0, 0.01))
		// Third row: adjustFactor=1.0 (reset), AdjClose = 9.0/1.0 = 9.0
		Expect(result[2].AdjClose).To(BeNumerically("~", 9.0, 0.01))
	})

	It("combines a split and a dividend on the same date", func() {
		// Row 1 is both a 2:1 split and a 2.0 cash dividend on the same
		// ex-date. The CRSP formula multiplies the dividend ratio by the
		// split factor in the same step:
		//   adjustFactor = 1.0 * (1 + 2/102) * 2.0 = 208/102
		// So the oldest row's adj_close = 101 / (208/102) = 10302/208
		//   = 49.52884615...
		rows := []EodRow{
			{Close: 50.0, Dividend: 0, SplitFactor: 1.0},
			{Close: 102.0, Dividend: 2.0, SplitFactor: 2.0},
			{Close: 101.0, Dividend: 0, SplitFactor: 1.0},
		}
		result := ComputeAdjustedClose(rows)
		Expect(result[0].AdjClose).To(BeNumerically("~", 50.0, 0.0001))
		Expect(result[1].AdjClose).To(BeNumerically("~", 102.0, 0.0001))
		Expect(result[2].AdjClose).To(BeNumerically("~", 49.5288, 0.0001))
	})

	It("stacks multiple splits multiplicatively over time", func() {
		// Two 2:1 splits at different dates compound to a 4x adjustment
		// for prices older than both.
		rows := []EodRow{
			{Close: 25.0, Dividend: 0, SplitFactor: 1.0},  // newest
			{Close: 50.0, Dividend: 0, SplitFactor: 2.0},  // 2nd split
			{Close: 100.0, Dividend: 0, SplitFactor: 2.0}, // 1st split
			{Close: 99.0, Dividend: 0, SplitFactor: 1.0},  // oldest
		}
		result := ComputeAdjustedClose(rows)
		Expect(result[0].AdjClose).To(BeNumerically("~", 25.0, 0.0001))
		Expect(result[1].AdjClose).To(BeNumerically("~", 50.0, 0.0001))
		Expect(result[2].AdjClose).To(BeNumerically("~", 50.0, 0.0001))
		Expect(result[3].AdjClose).To(BeNumerically("~", 24.75, 0.0001))
	})

	It("returns an empty slice unchanged", func() {
		var rows []EodRow
		Expect(ComputeAdjustedClose(rows)).To(BeEmpty())
	})

	It("leaves a single row's adj_close equal to its close even when it carries a corporate action", func() {
		// The newest row's adj_close is always close, because the
		// adjustment from its own dividend/split applies only to rows
		// older than it.
		rows := []EodRow{
			{Close: 100.0, Dividend: 5.0, SplitFactor: 2.0},
		}
		result := ComputeAdjustedClose(rows)
		Expect(result).To(HaveLen(1))
		Expect(result[0].AdjClose).To(BeNumerically("~", 100.0, 0.0001))
	})
})
