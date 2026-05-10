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
	"sync/atomic"
	"time"

	"github.com/rs/zerolog"
	"github.com/spf13/viper"
	"golang.org/x/sync/errgroup"
)

// defaultFlatFileDownloadConcurrency is the default worker pool size
// when the daily flat-file loop is parallelised. Operators can
// override via the `massive.flatfile_workers` viper key. Each worker
// pulls dates from a shared channel and runs download + parse +
// publish for that date; raising the number improves throughput at
// the cost of more peak memory (each worker holds one parsed file in
// memory, ~200 MB for a minute_aggs file).
const defaultFlatFileDownloadConcurrency = 8

// flatFileDownloadConcurrency reads the worker count from viper at
// run time so it can be tuned without recompiling. Falls back to the
// default constant when the key is unset or invalid.
func flatFileDownloadConcurrency() int {
	n := viper.GetInt("massive.flatfile_workers")
	if n <= 0 {
		return defaultFlatFileDownloadConcurrency
	}

	return n
}

// minTradingDaysForParallelDownload is the lookback threshold below
// which the daily loop stays sequential. For ranges shorter than ~1
// year of trading days the per-goroutine setup overhead and the log
// noise outweigh the wall-clock savings.
const minTradingDaysForParallelDownload = 252

// processDateFn handles one trading date, publishing any resulting
// observations. It returns the number of published observations and
// an error. NoSuchKey-style "skip this date" cases must be returned
// as (0, nil), not an error, so the loop continues silently.
type processDateFn func(ctx context.Context, d time.Time) (int, error)

// tradingDaysIn counts weekdays in [start, end] inclusive. Used to
// decide whether parallelism is worth the overhead.
func tradingDaysIn(start, end time.Time) int {
	if start.After(end) {
		return 0
	}

	count := 0

	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		if !isWeekend(d) {
			count++
		}
	}

	return count
}

// downloadDailyAggsRange iterates trading days in [start, end] and
// invokes process for each. When workers > 1 the work is fanned out
// across goroutines using errgroup; the rate limiter (if any) is the
// caller's responsibility since it lives inside process. Per-day
// failures (non-context errors) are logged and skipped so a single
// bad date cannot abort the whole run.
func downloadDailyAggsRange(ctx context.Context, logger *zerolog.Logger, label string, workers int, start, end time.Time, process processDateFn) (int, error) {
	if workers < 1 {
		workers = 1
	}

	workCh := make(chan time.Time, workers*4)

	var numObs atomic.Int64

	g, gctx := errgroup.WithContext(ctx)

	for range workers {
		g.Go(func() error {
			for d := range workCh {
				n, err := process(gctx, d)
				if err != nil {
					if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
						return err
					}

					logger.Warn().Err(err).Time("date", d).Msgf("%s fetch failed; skipping date", label)

					continue
				}

				numObs.Add(int64(n))
			}

			return nil
		})
	}

	g.Go(func() error {
		defer close(workCh)

		for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
			if isWeekend(d) {
				continue
			}

			select {
			case workCh <- d:
			case <-gctx.Done():
				return gctx.Err()
			}
		}

		return nil
	})

	if err := g.Wait(); err != nil {
		return int(numObs.Load()), err
	}

	return int(numObs.Load()), nil
}

// pickFlatFileWorkers returns 1 for short ranges and
// flatFileDownloadConcurrency for ranges above the threshold. Logs
// the choice so operators can see whether parallelism kicked in.
func pickFlatFileWorkers(logger *zerolog.Logger, start, end time.Time, label string) int {
	tradingDays := tradingDaysIn(start, end)

	workers := 1
	if tradingDays > minTradingDaysForParallelDownload {
		workers = flatFileDownloadConcurrency()
	}

	logger.Info().
		Str("label", label).
		Int("trading_days", tradingDays).
		Int("workers", workers).
		Msg("flat-file daily-loop concurrency")

	return workers
}
