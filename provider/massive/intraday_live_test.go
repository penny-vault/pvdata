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
	"time"

	massivews "github.com/massive-com/client-go/v3/websocket"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Live 1-minute session helpers", func() {
	Describe("liveSessionDeadline", func() {
		It("returns 20:35 in NYC during EDT", func() {
			nyc, err := time.LoadLocation("America/New_York")
			Expect(err).NotTo(HaveOccurred())

			// 2026-05-08 is during EDT (UTC-4).
			now := time.Date(2026, 5, 8, 7, 25, 0, 0, nyc)
			got, err := liveSessionDeadline(now)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(time.Date(2026, 5, 8, 20, 35, 0, 0, nyc)))
		})

		It("returns 20:35 in NYC during EST", func() {
			nyc, err := time.LoadLocation("America/New_York")
			Expect(err).NotTo(HaveOccurred())

			// 2026-01-15 is during EST (UTC-5).
			now := time.Date(2026, 1, 15, 7, 25, 0, 0, nyc)
			got, err := liveSessionDeadline(now)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(time.Date(2026, 1, 15, 20, 35, 0, 0, nyc)))
		})

		It("anchors the deadline to the NYC-local date for a UTC input", func() {
			nyc, err := time.LoadLocation("America/New_York")
			Expect(err).NotTo(HaveOccurred())

			// 03:25 ET on 2026-05-08 is 07:25 UTC the same day. The
			// deadline must land on the NYC-local date, not the UTC date.
			now := time.Date(2026, 5, 8, 7, 25, 0, 0, time.UTC)
			got, err := liveSessionDeadline(now)
			Expect(err).NotTo(HaveOccurred())
			Expect(got).To(Equal(time.Date(2026, 5, 8, 20, 35, 0, 0, nyc)))
		})
	})

	Describe("resolveLiveFeed", func() {
		It("defaults an empty string to real-time", func() {
			feed, err := resolveLiveFeed("")
			Expect(err).NotTo(HaveOccurred())
			Expect(feed).To(Equal(massivews.RealTime))
		})

		It("accepts real-time", func() {
			feed, err := resolveLiveFeed("real-time")
			Expect(err).NotTo(HaveOccurred())
			Expect(feed).To(Equal(massivews.RealTime))
		})

		It("accepts delayed regardless of case and surrounding whitespace", func() {
			feed, err := resolveLiveFeed("  Delayed  ")
			Expect(err).NotTo(HaveOccurred())
			Expect(feed).To(Equal(massivews.Delayed))
		})

		It("rejects unknown values", func() {
			_, err := resolveLiveFeed("instant")
			Expect(err).To(HaveOccurred())
		})
	})
})
