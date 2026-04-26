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
	"net/http/httptest"
	"sync/atomic"
	"time"

	"github.com/go-resty/resty/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("computeRateLimitWait", func() {
	var now time.Time

	BeforeEach(func() {
		now = time.Date(2026, 4, 26, 14, 23, 0, 0, time.UTC)
	})

	It("honors numeric Retry-After (seconds)", func() {
		h := http.Header{}
		h.Set("Retry-After", "120")

		Expect(computeRateLimitWait(h, now)).To(Equal(120 * time.Second))
	})

	It("honors HTTP-date Retry-After", func() {
		resume := now.Add(45 * time.Minute)
		h := http.Header{}
		h.Set("Retry-After", resume.Format(http.TimeFormat))

		got := computeRateLimitWait(h, now)
		Expect(got).To(BeNumerically("~", 45*time.Minute, time.Second))
	})

	It("falls back to next top-of-hour plus margin when header missing", func() {
		h := http.Header{}

		want := time.Date(2026, 4, 26, 15, 0, 30, 0, time.UTC).Sub(now)
		Expect(computeRateLimitWait(h, now)).To(Equal(want))
	})

	It("falls back when Retry-After is unparseable", func() {
		h := http.Header{}
		h.Set("Retry-After", "soon-ish")

		want := time.Date(2026, 4, 26, 15, 0, 30, 0, time.UTC).Sub(now)
		Expect(computeRateLimitWait(h, now)).To(Equal(want))
	})

	It("caps oversized Retry-After at the safety limit", func() {
		h := http.Header{}
		h.Set("Retry-After", "999999")

		Expect(computeRateLimitWait(h, now)).To(Equal(rateLimitWaitCap))
	})

	It("falls back when HTTP-date Retry-After is in the past", func() {
		past := now.Add(-1 * time.Hour)
		h := http.Header{}
		h.Set("Retry-After", past.Format(http.TimeFormat))

		want := time.Date(2026, 4, 26, 15, 0, 30, 0, time.UTC).Sub(now)
		Expect(computeRateLimitWait(h, now)).To(Equal(want))
	})
})

var _ = Describe("doWithRateLimit", func() {
	var (
		origSleep    func(context.Context, time.Duration) error
		sleepCount   atomic.Int32
		lastSleepFor time.Duration
	)

	BeforeEach(func() {
		origSleep = rateLimitSleepFn
		sleepCount.Store(0)
		lastSleepFor = 0
		rateLimitSleepFn = func(_ context.Context, d time.Duration) error {
			sleepCount.Add(1)
			lastSleepFor = d

			return nil
		}
	})

	AfterEach(func() {
		rateLimitSleepFn = origSleep
	})

	It("returns immediately on a 200", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		client := resty.New()
		resp, err := doWithRateLimit(context.Background(), func() (*resty.Response, error) {
			return client.R().Get(srv.URL)
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode()).To(Equal(http.StatusOK))
		Expect(sleepCount.Load()).To(Equal(int32(0)))
	})

	It("sleeps then succeeds when 429 is followed by 200", func() {
		var calls atomic.Int32

		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if calls.Add(1) == 1 {
				w.Header().Set("Retry-After", "30")
				w.WriteHeader(http.StatusTooManyRequests)

				return
			}

			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		client := resty.New()
		resp, err := doWithRateLimit(context.Background(), func() (*resty.Response, error) {
			return client.R().Get(srv.URL)
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode()).To(Equal(http.StatusOK))
		Expect(sleepCount.Load()).To(Equal(int32(1)))
		Expect(lastSleepFor).To(Equal(30 * time.Second))
		Expect(calls.Load()).To(Equal(int32(2)))
	})

	It("returns errDailyRateLimit on 429 then 429", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "5")
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer srv.Close()

		client := resty.New()
		resp, err := doWithRateLimit(context.Background(), func() (*resty.Response, error) {
			return client.R().Get(srv.URL)
		})

		Expect(errors.Is(err, errDailyRateLimit)).To(BeTrue())
		Expect(resp.StatusCode()).To(Equal(http.StatusTooManyRequests))
		Expect(sleepCount.Load()).To(Equal(int32(1)))
	})

	It("returns ctx error if cancellation interrupts the sleep", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusTooManyRequests)
		}))
		defer srv.Close()

		rateLimitSleepFn = func(_ context.Context, _ time.Duration) error {
			return context.Canceled
		}

		client := resty.New()
		_, err := doWithRateLimit(context.Background(), func() (*resty.Response, error) {
			return client.R().Get(srv.URL)
		})

		Expect(errors.Is(err, context.Canceled)).To(BeTrue())
	})

	It("propagates non-429 errors without sleeping", func() {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer srv.Close()

		client := resty.New()
		resp, err := doWithRateLimit(context.Background(), func() (*resty.Response, error) {
			return client.R().Get(srv.URL)
		})

		Expect(err).NotTo(HaveOccurred())
		Expect(resp.StatusCode()).To(Equal(http.StatusInternalServerError))
		Expect(sleepCount.Load()).To(Equal(int32(0)))
	})
})
