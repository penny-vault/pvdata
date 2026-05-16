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
	"net/http"
	"net/http/httptest"
	"net/url"
	"time"

	"github.com/go-resty/resty/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/time/rate"
)

var _ = Describe("flatFileAvailableForDate", func() {
	var nyc *time.Location

	BeforeEach(func() {
		var err error
		nyc, err = time.LoadLocation("America/New_York")
		Expect(err).NotTo(HaveOccurred())
	})

	tradingDay := func(year, month, day int) time.Time {
		return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	}

	It("returns false when now is before 11:00 EST the day after d", func() {
		// Trading date Monday Nov 10, 2025. The flat file becomes
		// available Tuesday Nov 11 at 11:00 EST. At 10:59 EST that
		// Tuesday it is still unavailable.
		d := tradingDay(2025, 11, 10)
		now := time.Date(2025, 11, 11, 10, 59, 0, 0, nyc)

		Expect(flatFileAvailableForDate(d, now)).To(BeFalse())
	})

	It("returns true at exactly 11:00 EST the day after d", func() {
		d := tradingDay(2025, 11, 10)
		now := time.Date(2025, 11, 11, 11, 0, 0, 0, nyc)

		Expect(flatFileAvailableForDate(d, now)).To(BeTrue())
	})

	It("returns true at 12:00 EST the day after d", func() {
		d := tradingDay(2025, 11, 10)
		now := time.Date(2025, 11, 11, 12, 0, 0, 0, nyc)

		Expect(flatFileAvailableForDate(d, now)).To(BeTrue())
	})

	It("returns false for the current trading day itself", func() {
		d := tradingDay(2025, 11, 11)
		// 4pm EST on the same trading date - market just closed,
		// flat file not yet published.
		now := time.Date(2025, 11, 11, 16, 0, 0, 0, nyc)

		Expect(flatFileAvailableForDate(d, now)).To(BeFalse())
	})

	It("returns true for a date two days ago", func() {
		d := tradingDay(2025, 11, 7)
		now := time.Date(2025, 11, 11, 8, 0, 0, 0, nyc)

		Expect(flatFileAvailableForDate(d, now)).To(BeTrue())
	})

	It("handles a now value passed in UTC", func() {
		// 15:30 UTC is 10:30 EST (EST is UTC-5 in November). The
		// flat file for Monday Nov 10 is not yet available at
		// 10:30 EST Tuesday.
		d := tradingDay(2025, 11, 10)
		now := time.Date(2025, 11, 11, 15, 30, 0, 0, time.UTC)

		Expect(flatFileAvailableForDate(d, now)).To(BeFalse())
	})

	It("handles a now value past the cutoff passed in UTC", func() {
		// 17:00 UTC is 12:00 EST.
		d := tradingDay(2025, 11, 10)
		now := time.Date(2025, 11, 11, 17, 0, 0, 0, time.UTC)

		Expect(flatFileAvailableForDate(d, now)).To(BeTrue())
	})
})

var _ = Describe("fetchDailyMarketSummary", func() {
	var (
		server   *httptest.Server
		origURL  string
		received url.Values
	)

	BeforeEach(func() {
		received = nil
		server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			received = r.URL.Query()

			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{
				"adjusted": false,
				"queryCount": 2,
				"request_id": "abc123",
				"resultsCount": 2,
				"status": "OK",
				"results": [
					{"T":"AAPL","o":180.10,"h":182.50,"l":179.40,"c":181.20,"v":52000000,"vw":180.85,"n":410000,"t":1700850000000},
					{"T":"MSFT","o":350.00,"h":355.10,"l":349.20,"c":354.55,"v":21000000,"vw":352.10,"n":210000,"t":1700850000000}
				]
			}`))
		}))

		origURL = dailyMarketSummaryURL
		dailyMarketSummaryURL = server.URL + "/v2/aggs/grouped/locale/us/market/stocks"
	})

	AfterEach(func() {
		dailyMarketSummaryURL = origURL
		server.Close()
	})

	It("decodes the response into aggRow records", func() {
		client := resty.New()
		limiter := rate.NewLimiter(rate.Inf, 1)

		d := time.Date(2024, 11, 25, 0, 0, 0, 0, time.UTC)

		rows, err := fetchDailyMarketSummary(context.Background(), client, limiter, d)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(2))

		Expect(rows[0].Ticker).To(Equal("AAPL"))
		Expect(rows[0].Open).To(Equal(180.10))
		Expect(rows[0].High).To(Equal(182.50))
		Expect(rows[0].Low).To(Equal(179.40))
		Expect(rows[0].Close).To(Equal(181.20))
		Expect(rows[0].Volume).To(Equal(52000000.0))
		Expect(rows[0].Transactions).To(Equal(int64(410000)))
		Expect(rows[0].WindowStart).To(Equal(int64(1700850000000)))

		Expect(rows[1].Ticker).To(Equal("MSFT"))
		Expect(rows[1].Close).To(Equal(354.55))
	})

	It("requests unadjusted prices so the splits map applies cleanly", func() {
		client := resty.New()
		limiter := rate.NewLimiter(rate.Inf, 1)

		d := time.Date(2024, 11, 25, 0, 0, 0, 0, time.UTC)

		_, err := fetchDailyMarketSummary(context.Background(), client, limiter, d)
		Expect(err).NotTo(HaveOccurred())
		Expect(received.Get("adjusted")).To(Equal("false"))
		Expect(received.Get("include_otc")).To(Equal("false"))
	})

	It("returns an error when the server returns a non-2xx status", func() {
		errServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "bad request", http.StatusBadRequest)
		}))
		defer errServer.Close()

		dailyMarketSummaryURL = errServer.URL + "/v2/aggs/grouped/locale/us/market/stocks"

		client := resty.New()
		limiter := rate.NewLimiter(rate.Inf, 1)

		d := time.Date(2024, 11, 25, 0, 0, 0, 0, time.UTC)

		_, err := fetchDailyMarketSummary(context.Background(), client, limiter, d)
		Expect(err).To(MatchError(ErrInvalidStatusCode))
	})

	It("returns an empty slice for a date with no results (e.g. holiday)", func() {
		emptyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"status":"OK","queryCount":0,"resultsCount":0,"adjusted":false}`))
		}))
		defer emptyServer.Close()

		dailyMarketSummaryURL = emptyServer.URL + "/v2/aggs/grouped/locale/us/market/stocks"

		client := resty.New()
		limiter := rate.NewLimiter(rate.Inf, 1)

		d := time.Date(2024, 12, 25, 0, 0, 0, 0, time.UTC)

		rows, err := fetchDailyMarketSummary(context.Background(), client, limiter, d)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(BeEmpty())
	})
})
