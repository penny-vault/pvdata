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

	"github.com/penny-vault/pvdata/data"
)

var _ = Describe("computeUniverseForDate", func() {
	// Helper: build a contiguous EOD slice for one FIGI from startDate going N days forward.
	// All trading days are simulated as consecutive calendar days for simplicity in tests.
	mkEodSeries := func(startDate string, n int, close, volume float64) []eodRow {
		t, _ := time.Parse("2006-01-02", startDate)
		out := make([]eodRow, n)
		for i := 0; i < n; i++ {
			out[i] = eodRow{Date: t.AddDate(0, 0, i), Close: close, Volume: volume}
		}
		return out
	}

	mkCSAsset := func(ticker, cik, figi, name string) *data.Asset {
		return &data.Asset{
			Ticker:          ticker,
			Name:            name,
			CompositeFigi:   figi,
			CIK:             cik,
			AssetType:       data.CommonStock,
			PrimaryExchange: "NASDAQ",
			Active:          true,
		}
	}

	It("includes a stock that passes all filters", func() {
		evalDate, _ := time.Parse("2006-01-02", "2024-12-31")
		// Window: [2024-06-14, 2024-12-30] = exactly 200 calendar days.
		windowStart := evalDate.AddDate(0, 0, -200)
		windowEnd := evalDate.AddDate(0, 0, -1)

		assets := []*data.Asset{mkCSAsset("AAPL", "0000320193", "BBG000B9XRY4", "Apple Inc.")}
		eodByFigi := map[string][]eodRow{
			"BBG000B9XRY4": mkEodSeries("2024-06-14", 200, 100.0, 15_000_000), // dv = 1.5B, turnover = 0.05%
		}
		mcapByFigi := map[string]int64{
			"BBG000B9XRY4": 3_000_000_000_000, // $3T
		}
		broadMcaps := []int64{1_000_000, 3_000_000_000_000}
		tradingDayCount := 200

		input := perDayInput{
			Date:            evalDate,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: tradingDayCount,
			Assets:          assets,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		result := computeUniverseForDate(input)
		sizeCutoff := percentileInt64(input.BroadMarketCaps, sizePercentileEntry)
		universe, _ := applyThresholds(result.Candidates, sizeCutoff, liquidityTurnoverEntry, priceFloorEntry)
		Expect(universe).To(HaveKey("AAPL"))
		Expect(universe["AAPL"].Weight).To(BeNumerically("~", 1.0, 1e-9))
	})

	It("excludes a stock with insufficient EOD history (under 30 days)", func() {
		evalDate, _ := time.Parse("2006-01-02", "2024-12-31")
		windowStart := evalDate.AddDate(0, 0, -200)
		windowEnd := evalDate.AddDate(0, 0, -1)

		assets := []*data.Asset{mkCSAsset("NEW", "0001111111", "BBG000NEW001", "New Inc.")}
		eodByFigi := map[string][]eodRow{
			"BBG000NEW001": mkEodSeries(evalDate.AddDate(0, 0, -20).Format("2006-01-02"), 20, 50.0, 100_000),
		}
		mcapByFigi := map[string]int64{"BBG000NEW001": 50_000_000_000}
		broadMcaps := []int64{50_000_000_000, 1_000_000}

		input := perDayInput{
			Date:            evalDate,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: 200,
			Assets:          assets,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		result := computeUniverseForDate(input)
		sizeCutoff := percentileInt64(input.BroadMarketCaps, sizePercentileEntry)
		universe, _ := applyThresholds(result.Candidates, sizeCutoff, liquidityTurnoverEntry, priceFloorEntry)
		Expect(universe).To(BeEmpty())
	})

	It("includes a stock via early-entry path (50 days, top quintile market cap)", func() {
		evalDate, _ := time.Parse("2006-01-02", "2024-12-31")
		windowStart := evalDate.AddDate(0, 0, -200)
		windowEnd := evalDate.AddDate(0, 0, -1)

		assets := []*data.Asset{mkCSAsset("BIGIPO", "0002222222", "BBG000BIGIPO", "BigIPO Inc.")}
		// 50 contiguous days ending day-1
		eodByFigi := map[string][]eodRow{
			"BBG000BIGIPO": mkEodSeries(evalDate.AddDate(0, 0, -50).Format("2006-01-02"), 50, 200.0, 500_000), // dv = 100M, turnover = 0.125%
		}
		mcapByFigi := map[string]int64{"BBG000BIGIPO": 80_000_000_000}
		// Broad market cap pool: BIGIPO at $80B is in top 20% of [1B, 5B, 10B, 80B] (80th percentile).
		broadMcaps := []int64{1_000_000_000, 5_000_000_000, 10_000_000_000, 80_000_000_000}

		input := perDayInput{
			Date:            evalDate,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: 200,
			Assets:          assets,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		result := computeUniverseForDate(input)
		sizeCutoff := percentileInt64(input.BroadMarketCaps, sizePercentileEntry)
		universe, _ := applyThresholds(result.Candidates, sizeCutoff, liquidityTurnoverEntry, priceFloorEntry)
		Expect(universe).To(HaveKey("BIGIPO"))
	})

	It("excludes a stock via early-entry path when market cap is below top quintile", func() {
		evalDate, _ := time.Parse("2006-01-02", "2024-12-31")
		windowStart := evalDate.AddDate(0, 0, -200)
		windowEnd := evalDate.AddDate(0, 0, -1)

		assets := []*data.Asset{mkCSAsset("SMALLIPO", "0003333333", "BBG000SMALLIPO", "SmallIPO Inc.")}
		eodByFigi := map[string][]eodRow{
			"BBG000SMALLIPO": mkEodSeries(evalDate.AddDate(0, 0, -50).Format("2006-01-02"), 50, 10.0, 100_000),
		}
		mcapByFigi := map[string]int64{"BBG000SMALLIPO": 500_000_000} // $500M
		// Broad market cap pool: SMALLIPO at $500M is bottom of [500M, 5B, 10B, 80B], not top 20%.
		broadMcaps := []int64{500_000_000, 5_000_000_000, 10_000_000_000, 80_000_000_000}

		input := perDayInput{
			Date:            evalDate,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: 200,
			Assets:          assets,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		result := computeUniverseForDate(input)
		sizeCutoff := percentileInt64(input.BroadMarketCaps, sizePercentileEntry)
		universe, _ := applyThresholds(result.Candidates, sizeCutoff, liquidityTurnoverEntry, priceFloorEntry)
		Expect(universe).To(BeEmpty())
	})

	It("excludes a stock with low ADV", func() {
		evalDate, _ := time.Parse("2006-01-02", "2024-12-31")
		windowStart := evalDate.AddDate(0, 0, -200)
		windowEnd := evalDate.AddDate(0, 0, -1)

		assets := []*data.Asset{mkCSAsset("ILLIQ", "0004444444", "BBG000ILLIQ01", "Illiquid Inc.")}
		eodByFigi := map[string][]eodRow{
			"BBG000ILLIQ01": mkEodSeries("2024-06-14", 200, 10.0, 1_000), // dv = 10K
		}
		mcapByFigi := map[string]int64{"BBG000ILLIQ01": 100_000_000_000}
		broadMcaps := []int64{100_000_000_000}

		input := perDayInput{
			Date:            evalDate,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: 200,
			Assets:          assets,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		result := computeUniverseForDate(input)
		sizeCutoff := percentileInt64(input.BroadMarketCaps, sizePercentileEntry)
		universe, _ := applyThresholds(result.Candidates, sizeCutoff, liquidityTurnoverEntry, priceFloorEntry)
		Expect(universe).To(BeEmpty())
	})

	It("excludes a stock with prior_close below $2", func() {
		evalDate, _ := time.Parse("2006-01-02", "2024-12-31")
		windowStart := evalDate.AddDate(0, 0, -200)
		windowEnd := evalDate.AddDate(0, 0, -1)

		assets := []*data.Asset{mkCSAsset("CHEAP", "0005555555", "BBG000CHEAP01", "Cheap Inc.")}
		eodByFigi := map[string][]eodRow{
			"BBG000CHEAP01": mkEodSeries("2024-06-14", 200, 1.5, 10_000_000), // dv = 15M, but price < $2
		}
		mcapByFigi := map[string]int64{"BBG000CHEAP01": 100_000_000_000}
		broadMcaps := []int64{100_000_000_000}

		input := perDayInput{
			Date:            evalDate,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: 200,
			Assets:          assets,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		result := computeUniverseForDate(input)
		sizeCutoff := percentileInt64(input.BroadMarketCaps, sizePercentileEntry)
		universe, _ := applyThresholds(result.Candidates, sizeCutoff, liquidityTurnoverEntry, priceFloorEntry)
		Expect(universe).To(BeEmpty())
	})

	It("excludes a stock below the 25th percentile market cap cutoff", func() {
		evalDate, _ := time.Parse("2006-01-02", "2024-12-31")
		windowStart := evalDate.AddDate(0, 0, -200)
		windowEnd := evalDate.AddDate(0, 0, -1)

		assets := []*data.Asset{mkCSAsset("TINY", "0006666666", "BBG000TINY001", "Tiny Inc.")}
		eodByFigi := map[string][]eodRow{
			"BBG000TINY001": mkEodSeries("2024-06-14", 200, 10.0, 1_000_000), // dv = 10M, passes ADV
		}
		mcapByFigi := map[string]int64{"BBG000TINY001": 50_000_000} // $50M
		// Broad pool: 25th percentile of [50M, 1B, 5B, 10B, 80B] = ceil(5*0.25)=2 -> sorted[1] = 1B.
		// TINY at $50M is below the 1B cutoff -> excluded.
		broadMcaps := []int64{50_000_000, 1_000_000_000, 5_000_000_000, 10_000_000_000, 80_000_000_000}

		input := perDayInput{
			Date:            evalDate,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: 200,
			Assets:          assets,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		result := computeUniverseForDate(input)
		sizeCutoff := percentileInt64(input.BroadMarketCaps, sizePercentileEntry)
		universe, _ := applyThresholds(result.Candidates, sizeCutoff, liquidityTurnoverEntry, priceFloorEntry)
		Expect(universe).To(BeEmpty())
	})

	It("excludes a stock with insufficient EOD rows after a gap", func() {
		evalDate, _ := time.Parse("2006-01-02", "2024-12-31")
		windowStart := evalDate.AddDate(0, 0, -200)
		windowEnd := evalDate.AddDate(0, 0, -1)

		// Build 200 consecutive calendar days starting 2024-06-15, then drop the middle row.
		// After removal, only ~198 rows fall inside the window, so the stock fails the standard-path
		// dayCount threshold (< 200). BroadMarketCaps is set so the stock is also below the
		// 80th-percentile early-entry threshold, so neither path admits it.
		series := mkEodSeries("2024-06-15", 200, 100.0, 100_000)
		series = append(series[:100], series[101:]...)

		assets := []*data.Asset{mkCSAsset("GAPPY", "0007777777", "BBG000GAPPY01", "Gappy Inc.")}
		eodByFigi := map[string][]eodRow{
			"BBG000GAPPY01": series,
		}
		mcapByFigi := map[string]int64{"BBG000GAPPY01": 100_000_000_000} // $100B
		// 80th percentile of [100B, 500B, 1T, 2T, 5T] = ceil(5*0.8)=4 -> sorted[3] = 2T.
		// GAPPY's 100B is well below the 2T early-entry cutoff.
		broadMcaps := []int64{100_000_000_000, 500_000_000_000, 1_000_000_000_000, 2_000_000_000_000, 5_000_000_000_000}

		input := perDayInput{
			Date:            evalDate,
			WindowStart:     windowStart,
			WindowEnd:       windowEnd,
			TradingDayCount: 200,
			Assets:          assets,
			EodByFigi:       eodByFigi,
			MarketCapByFigi: mcapByFigi,
			BroadMarketCaps: broadMcaps,
		}

		result := computeUniverseForDate(input)
		sizeCutoff := percentileInt64(input.BroadMarketCaps, sizePercentileEntry)
		universe, _ := applyThresholds(result.Candidates, sizeCutoff, liquidityTurnoverEntry, priceFloorEntry)
		Expect(universe).To(BeEmpty())
	})
})
