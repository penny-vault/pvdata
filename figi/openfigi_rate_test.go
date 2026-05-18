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
package figi

import (
	"context"
	"net/http"
	"strconv"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RateLimit", func() {
	It("returns the same shared limiter on every call so concurrent callers coordinate", func() {
		a := RateLimit()
		b := RateLimit()
		Expect(a).To(BeIdenticalTo(b))
	})
})

var _ = Describe("parseRetryAfter", func() {
	It("returns the parsed duration when the header is an integer second count", func() {
		d, ok := parseRetryAfter("30")
		Expect(ok).To(BeTrue())
		Expect(d).To(Equal(30 * time.Second))
	})

	It("returns the parsed duration when the header is an HTTP-date", func() {
		future := time.Now().Add(45 * time.Second).UTC().Format(http.TimeFormat)
		d, ok := parseRetryAfter(future)
		Expect(ok).To(BeTrue())
		Expect(d).To(BeNumerically(">=", 30*time.Second))
		Expect(d).To(BeNumerically("<=", 60*time.Second))
	})

	It("clamps a past HTTP-date to zero rather than returning a negative duration", func() {
		past := time.Now().Add(-1 * time.Hour).UTC().Format(http.TimeFormat)
		d, ok := parseRetryAfter(past)
		Expect(ok).To(BeTrue())
		Expect(d).To(Equal(time.Duration(0)))
	})

	It("returns (0, false) on an empty header", func() {
		d, ok := parseRetryAfter("")
		Expect(ok).To(BeFalse())
		Expect(d).To(Equal(time.Duration(0)))
	})

	It("returns (0, false) on an unparseable header", func() {
		d, ok := parseRetryAfter("not a number or date")
		Expect(ok).To(BeFalse())
		Expect(d).To(Equal(time.Duration(0)))
	})

	It("accepts a zero-second integer header", func() {
		d, ok := parseRetryAfter("0")
		Expect(ok).To(BeTrue())
		Expect(d).To(Equal(time.Duration(0)))
	})

	It("rejects a negative integer header rather than treating it as zero", func() {
		_, ok := parseRetryAfter(strconv.Itoa(-5))
		Expect(ok).To(BeFalse())
	})
})

var _ = Describe("recordBackoff and waitForBackoff", func() {
	BeforeEach(func() {
		openFIGIBackoffMu.Lock()
		openFIGIBackoffUntil = time.Time{}
		openFIGIBackoffMu.Unlock()
	})

	It("returns immediately when no backoff window is set", func() {
		start := time.Now()
		Expect(waitForBackoff(context.Background())).To(Succeed())
		Expect(time.Since(start)).To(BeNumerically("<", 50*time.Millisecond))
	})

	It("blocks until a recorded backoff window expires", func() {
		recordBackoff(150 * time.Millisecond)

		start := time.Now()
		Expect(waitForBackoff(context.Background())).To(Succeed())
		Expect(time.Since(start)).To(BeNumerically(">=", 100*time.Millisecond))
	})

	It("does not extend an already-longer backoff with a shorter one", func() {
		recordBackoff(500 * time.Millisecond)
		recordBackoff(50 * time.Millisecond)

		openFIGIBackoffMu.Lock()
		until := openFIGIBackoffUntil
		openFIGIBackoffMu.Unlock()

		remaining := time.Until(until)
		Expect(remaining).To(BeNumerically(">=", 400*time.Millisecond))
	})

	It("returns ctx.Err() when the context is cancelled before the backoff expires", func() {
		recordBackoff(5 * time.Second)

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		Expect(waitForBackoff(ctx)).To(Equal(context.Canceled))
	})
})
