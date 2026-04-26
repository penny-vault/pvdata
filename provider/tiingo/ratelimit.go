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
package tiingo

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/rs/zerolog/log"
)

// errDailyRateLimit signals that a 429 persisted past the next hourly bucket
// reset, so the daily quota is presumed exhausted and the run should abort.
var errDailyRateLimit = errors.New("tiingo daily rate limit exhausted")

// rateLimitWaitCap bounds how long doWithRateLimit will block on a 429.
// Tiingo's hourly bucket resets within an hour; anything longer means we are
// almost certainly waiting on the daily quota and the wait is wasted.
const rateLimitWaitCap = 70 * time.Minute

// rateLimitSleepFn is the cancellable sleep used by doWithRateLimit.
// Tests override this to avoid real sleeps.
var rateLimitSleepFn = sleepCtx

// doWithRateLimit invokes doFn. On a 429 it sleeps until the bucket reset
// (Retry-After, top-of-hour, or rateLimitWaitCap), retries once, and returns
// errDailyRateLimit if the retry is also a 429. All other responses and
// errors are returned to the caller untouched.
func doWithRateLimit(ctx context.Context, doFn func() (*resty.Response, error)) (*resty.Response, error) {
	resp, err := doFn()
	if err != nil || resp.StatusCode() != http.StatusTooManyRequests {
		return resp, err
	}

	wait := computeRateLimitWait(resp.Header(), time.Now())

	log.Warn().
		Dur("sleep", wait).
		Time("resumeAt", time.Now().Add(wait)).
		Str("retryAfter", resp.Header().Get("Retry-After")).
		Msg("tiingo rate limit hit, sleeping until next bucket reset")

	if sleepErr := rateLimitSleepFn(ctx, wait); sleepErr != nil {
		return resp, sleepErr
	}

	resp, err = doFn()
	if err != nil {
		return resp, err
	}

	if resp.StatusCode() == http.StatusTooManyRequests {
		log.Error().Msg("tiingo rate limit still exceeded after hourly reset, aborting run as daily quota likely exhausted")

		return resp, errDailyRateLimit
	}

	return resp, nil
}

// computeRateLimitWait returns the duration to sleep before retrying a 429.
// Priority: Retry-After header (if parseable and positive), otherwise the
// time until the next top-of-hour plus a 30s safety margin. Capped at
// rateLimitWaitCap.
func computeRateLimitWait(h http.Header, now time.Time) time.Duration {
	if d, ok := parseRetryAfter(h.Get("Retry-After"), now); ok {
		if d > rateLimitWaitCap {
			return rateLimitWaitCap
		}

		return d
	}

	next := now.Truncate(time.Hour).Add(time.Hour).Add(30 * time.Second)

	d := next.Sub(now)
	if d > rateLimitWaitCap {
		return rateLimitWaitCap
	}

	return d
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	if value == "" {
		return 0, false
	}

	if secs, err := strconv.Atoi(value); err == nil && secs > 0 {
		return time.Duration(secs) * time.Second, true
	}

	if t, err := http.ParseTime(value); err == nil {
		d := t.Sub(now)
		if d > 0 {
			return d, true
		}
	}

	return 0, false
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
