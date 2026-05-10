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
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rs/zerolog"
)

var _ = Describe("flat-file daily-loop helpers", func() {
	d := func(year, month, day int) time.Time {
		return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	}

	silentLogger := zerolog.Nop()

	Describe("tradingDaysIn", func() {
		It("counts only weekdays inclusive", func() {
			// Mon 2024-06-03 → Sun 2024-06-09 = 5 weekdays
			Expect(tradingDaysIn(d(2024, 6, 3), d(2024, 6, 9))).To(Equal(5))
		})

		It("returns 0 for inverted ranges", func() {
			Expect(tradingDaysIn(d(2024, 6, 9), d(2024, 6, 3))).To(Equal(0))
		})

		It("counts a single weekday as 1", func() {
			Expect(tradingDaysIn(d(2024, 6, 3), d(2024, 6, 3))).To(Equal(1))
		})

		It("counts a single weekend day as 0", func() {
			Expect(tradingDaysIn(d(2024, 6, 8), d(2024, 6, 8))).To(Equal(0))
		})

		It("approximates ~252 trading days for a calendar year", func() {
			// Doesn't subtract holidays, so this is weekdays-only and
			// will be slightly higher than the official 252.
			n := tradingDaysIn(d(2024, 1, 1), d(2024, 12, 31))
			Expect(n).To(BeNumerically("~", 261, 5))
		})
	})

	Describe("pickFlatFileWorkers", func() {
		It("uses 1 worker for short ranges", func() {
			// 14 days = ~10 trading days, well under threshold
			workers := pickFlatFileWorkers(&silentLogger, d(2024, 6, 1), d(2024, 6, 14), "test")
			Expect(workers).To(Equal(1))
		})

		It("uses 1 worker even at 252 weekday count (just at threshold)", func() {
			// minTradingDaysForParallelDownload requires >, not >=
			workers := pickFlatFileWorkers(&silentLogger, d(2024, 1, 1), d(2024, 12, 14), "test")
			// 2024 has ~250 weekdays through Dec 14 → still 1 worker
			Expect(workers).To(Equal(1))
		})

		It("scales to flatFileDownloadConcurrency() when range is multi-year", func() {
			workers := pickFlatFileWorkers(&silentLogger, d(2020, 1, 1), d(2024, 12, 31), "test")
			Expect(workers).To(Equal(flatFileDownloadConcurrency()))
		})

		It("scales to flatFileDownloadConcurrency() for the realistic 24-year backfill", func() {
			workers := pickFlatFileWorkers(&silentLogger, d(2002, 5, 16), d(2026, 5, 9), "test")
			Expect(workers).To(Equal(flatFileDownloadConcurrency()))
		})
	})

	Describe("downloadDailyAggsRange", func() {
		It("invokes process once per weekday and aggregates the count", func() {
			start := d(2024, 6, 3) // Mon
			end := d(2024, 6, 9)   // Sun
			// Expected weekdays: Mon 3, Tue 4, Wed 5, Thu 6, Fri 7

			var (
				mu    sync.Mutex
				calls []time.Time
			)

			process := func(_ context.Context, when time.Time) (int, error) {
				mu.Lock()
				defer mu.Unlock()

				calls = append(calls, when)

				return 7, nil
			}

			n, err := downloadDailyAggsRange(context.Background(), &silentLogger, "test", 1, start, end, process)
			Expect(err).NotTo(HaveOccurred())
			Expect(n).To(Equal(35)) // 5 weekdays * 7
			Expect(calls).To(HaveLen(5))

			for _, c := range calls {
				Expect(isWeekend(c)).To(BeFalse(), "process should never be called for weekends")
			}
		})

		It("recovers from a single-day failure and continues", func() {
			start := d(2024, 6, 3)
			end := d(2024, 6, 7) // 5 weekdays

			var counter atomic.Int32

			process := func(_ context.Context, when time.Time) (int, error) {
				counter.Add(1)

				if when.Day() == 5 {
					return 0, errors.New("synthetic per-day failure")
				}

				return 1, nil
			}

			n, err := downloadDailyAggsRange(context.Background(), &silentLogger, "test", 1, start, end, process)
			Expect(err).NotTo(HaveOccurred()) // per-day failures are skipped, not fatal
			Expect(n).To(Equal(4))            // 5 days - 1 failed day
			Expect(counter.Load()).To(Equal(int32(5)))
		})

		It("aborts on context cancellation", func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()

			process := func(ctx context.Context, _ time.Time) (int, error) {
				return 0, ctx.Err()
			}

			_, err := downloadDailyAggsRange(ctx, &silentLogger, "test", 1, d(2024, 6, 3), d(2024, 6, 7), process)
			Expect(err).To(HaveOccurred())
			Expect(errors.Is(err, context.Canceled)).To(BeTrue())
		})

		It("works correctly with multiple workers (race-detector clean)", func() {
			// 1 month = ~22 weekdays; with 4 workers and a slow
			// process function we exercise concurrent dispatch.
			start := d(2024, 6, 1)
			end := d(2024, 6, 30)

			var counter atomic.Int32

			process := func(_ context.Context, _ time.Time) (int, error) {
				counter.Add(1)
				time.Sleep(time.Millisecond) // tiny sleep to overlap workers

				return 10, nil
			}

			n, err := downloadDailyAggsRange(context.Background(), &silentLogger, "test", 4, start, end, process)
			Expect(err).NotTo(HaveOccurred())
			Expect(n).To(Equal(int(counter.Load()) * 10))
			// 2024-06 has 20 weekdays.
			Expect(counter.Load()).To(Equal(int32(20)))
		})

		It("treats workers < 1 as 1", func() {
			start := d(2024, 6, 3)
			end := d(2024, 6, 7)

			var counter atomic.Int32

			process := func(_ context.Context, _ time.Time) (int, error) {
				counter.Add(1)

				return 1, nil
			}

			n, err := downloadDailyAggsRange(context.Background(), &silentLogger, "test", 0, start, end, process)
			Expect(err).NotTo(HaveOccurred())
			Expect(n).To(Equal(5))
			Expect(counter.Load()).To(Equal(int32(5)))
		})
	})
})
