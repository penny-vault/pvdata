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
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-resty/resty/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/time/rate"
)

// fakeMassiveServer simulates Polygon's paginated splits/dividends
// endpoint. Backing data is generated as one record per day in the
// configured range, with a deterministic ticker so tests can verify
// no records are dropped or duplicated.
type fakeMassiveServer struct {
	server   *httptest.Server
	pageSize int

	// requestCount tracks how many HTTP requests the server has
	// served so tests can verify pagination actually happened.
	requestCount atomic.Int64

	// errOnRequest, if > 0, makes the Nth request return a 500 so
	// tests can verify error propagation through errgroup.
	errOnRequest atomic.Int64
}

// newFakeMassiveServer builds an httptest.Server that responds to both
// splits and dividends requests. Records are synthesised as one per
// day in the queried range; the ticker is the date so de-duplication
// is verifiable without ambiguity.
func newFakeMassiveServer(pageSize int) *fakeMassiveServer {
	f := &fakeMassiveServer{pageSize: pageSize}
	f.server = httptest.NewServer(http.HandlerFunc(f.handle))

	return f
}

func (f *fakeMassiveServer) Close() { f.server.Close() }

// handle generates synthetic records for [gte, lte] in descending
// date order, paginating via cursor=<offset>.
func (f *fakeMassiveServer) handle(w http.ResponseWriter, r *http.Request) {
	count := f.requestCount.Add(1)

	if errOn := f.errOnRequest.Load(); errOn > 0 && count == errOn {
		http.Error(w, "synthetic error", http.StatusInternalServerError)
		return
	}

	q := r.URL.Query()

	dateField := "execution_date"
	isSplits := strings.HasSuffix(r.URL.Path, "/splits")

	if !isSplits {
		dateField = "ex_dividend_date"
	}

	gte, _ := time.Parse("2006-01-02", q.Get(dateField+".gte"))
	lte, _ := time.Parse("2006-01-02", q.Get(dateField+".lte"))

	cursor := 0
	if c := q.Get("cursor"); c != "" {
		cursor, _ = strconv.Atoi(c)
	}

	totalDays := max(int(lte.Sub(gte).Hours()/24)+1, 0)

	startIdx := cursor
	endIdx := min(startIdx+f.pageSize, totalDays)

	results := make([]map[string]any, 0, endIdx-startIdx)

	for i := startIdx; i < endIdx; i++ {
		// Descending order: newest first.
		d := lte.AddDate(0, 0, -i)
		dateStr := d.Format("2006-01-02")

		if isSplits {
			results = append(results, map[string]any{
				"id":             "split-" + dateStr,
				"ticker":         "T" + dateStr,
				"execution_date": dateStr,
				"split_from":     1,
				"split_to":       2,
			})
		} else {
			results = append(results, map[string]any{
				"id":               "div-" + dateStr,
				"ticker":           "T" + dateStr,
				"ex_dividend_date": dateStr,
				"cash_amount":      0.5,
				"currency":         "USD",
				"dividend_type":    "CD",
				"frequency":        4,
			})
		}
	}

	resp := map[string]any{
		"status":  "OK",
		"results": results,
	}

	if endIdx < totalDays {
		nextURL := *r.URL
		v := nextURL.Query()
		v.Set("cursor", strconv.Itoa(endIdx))
		nextURL.RawQuery = v.Encode()
		nextURL.Scheme = "http"
		nextURL.Host = r.Host
		resp["next_url"] = nextURL.String()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (f *fakeMassiveServer) URL() string { return f.server.URL }

var _ = Describe("parallel paginated fetch (integration)", func() {
	var (
		fake          *fakeMassiveServer
		origSplits    string
		origDividends string
	)

	BeforeEach(func() {
		fake = newFakeMassiveServer(50) // 50 records per page

		origSplits = splitsURL
		origDividends = dividendsURL

		splitsURL = fake.URL() + "/v3/reference/splits"
		dividendsURL = fake.URL() + "/v3/reference/dividends"
	})

	AfterEach(func() {
		splitsURL = origSplits
		dividendsURL = origDividends
		fake.Close()
	})

	d := func(year, month, day int) time.Time {
		return time.Date(year, time.Month(month), day, 0, 0, 0, 0, time.UTC)
	}

	Describe("fetchSplitsRange", func() {
		It("collects every synthetic record exactly once across windows", func() {
			ctx := context.Background()
			limiter := rate.NewLimiter(rate.Inf, 1)
			start := d(2024, 1, 1)
			end := d(2024, 12, 31)
			expectedCount := int(end.Sub(start).Hours()/24) + 1

			_, all, err := fetchSplitsRange(ctx, resty.New(), limiter, start, end)
			Expect(err).NotTo(HaveOccurred())
			Expect(all).To(HaveLen(expectedCount))

			seen := map[string]int{}
			for _, r := range all {
				seen[r.ExecutionDate]++
			}

			Expect(seen).To(HaveLen(expectedCount), "every date should appear at least once")

			for date, n := range seen {
				Expect(n).To(Equal(1),
					"date %s appeared %d times - seam overlap or duplicate page", date, n)
			}
		})

		It("populates the corporateActions map for every non-zero split", func() {
			ctx := context.Background()
			limiter := rate.NewLimiter(rate.Inf, 1)
			start := d(2024, 1, 1)
			end := d(2024, 1, 30)
			expectedCount := 30

			ca, all, err := fetchSplitsRange(ctx, resty.New(), limiter, start, end)
			Expect(err).NotTo(HaveOccurred())
			Expect(all).To(HaveLen(expectedCount))

			// Every synthetic split has split_from=1 split_to=2, so
			// every (ticker, date) entry should be present with
			// factor 2.
			tickerCount := 0

			for _, r := range all {
				factor := ca.lookup(massiveTicker2PvTicker(r.Ticker), r.ExecutionDate)
				Expect(factor).To(Equal(2.0))
				tickerCount++
			}

			Expect(tickerCount).To(Equal(expectedCount))
		})

		It("falls back to a single window when range is smaller than n days", func() {
			ctx := context.Background()
			limiter := rate.NewLimiter(rate.Inf, 1)
			start := d(2024, 1, 1)
			end := d(2024, 1, 2) // 2 days, n=4 => 1 window

			_, all, err := fetchSplitsRange(ctx, resty.New(), limiter, start, end)
			Expect(err).NotTo(HaveOccurred())
			Expect(all).To(HaveLen(2))
		})

		It("propagates errors from one window to the whole errgroup", func() {
			ctx := context.Background()
			limiter := rate.NewLimiter(rate.Inf, 1)
			start := d(2024, 1, 1)
			end := d(2024, 12, 31)

			fake.errOnRequest.Store(2) // fail the second request

			_, _, err := fetchSplitsRange(ctx, resty.New(), limiter, start, end)
			Expect(err).To(HaveOccurred())
		})

		It("respects context cancellation", func() {
			ctx, cancel := context.WithCancel(context.Background())
			cancel() // cancel before starting

			limiter := rate.NewLimiter(rate.Inf, 1)

			_, _, err := fetchSplitsRange(ctx, resty.New(), limiter, d(2024, 1, 1), d(2024, 12, 31))
			Expect(err).To(HaveOccurred())
		})
	})

	Describe("fetchDividendsRange", func() {
		It("collects every synthetic record exactly once across windows", func() {
			ctx := context.Background()
			limiter := rate.NewLimiter(rate.Inf, 1)
			start := d(2020, 1, 1)
			end := d(2024, 12, 31)
			expectedCount := int(end.Sub(start).Hours()/24) + 1

			_, all, err := fetchDividendsRange(ctx, resty.New(), limiter, start, end)
			Expect(err).NotTo(HaveOccurred())
			Expect(all).To(HaveLen(expectedCount))

			dates := make([]string, 0, len(all))
			for _, r := range all {
				dates = append(dates, r.ExDividendDate)
			}

			sort.Strings(dates)

			// Every consecutive day must be present exactly once.
			for i := 1; i < len(dates); i++ {
				prev, _ := time.Parse("2006-01-02", dates[i-1])
				cur, _ := time.Parse("2006-01-02", dates[i])
				gap := cur.Sub(prev)
				Expect(gap).To(Equal(24*time.Hour),
					"gap between %s and %s should be 1 day; got %s",
					dates[i-1], dates[i], gap)
			}
		})

		It("captures all documented dividend fields", func() {
			ctx := context.Background()
			limiter := rate.NewLimiter(rate.Inf, 1)

			_, all, err := fetchDividendsRange(ctx, resty.New(), limiter, d(2024, 6, 1), d(2024, 6, 1))
			Expect(err).NotTo(HaveOccurred())
			Expect(all).To(HaveLen(1))

			r := all[0]
			Expect(r.ID).To(Equal("div-2024-06-01"))
			Expect(r.Ticker).To(Equal("T2024-06-01"))
			Expect(r.CashAmount).To(Equal(0.5))
			Expect(r.Currency).To(Equal("USD"))
			Expect(r.DividendType).To(Equal("CD"))
			Expect(r.Frequency).To(Equal(4))
			Expect(r.ExDividendDate).To(Equal("2024-06-01"))
		})
	})

	Describe("seam correctness with realistic 24-year range", func() {
		It("fetches every synthetic split exactly once across 4 windows over 24 years", func() {
			// Use a smaller server pageSize so we still paginate;
			// 24 years = 8767 days, pageSize=500 → ~18 pages total
			// across 4 windows.
			ctx := context.Background()
			limiter := rate.NewLimiter(rate.Inf, 1)
			start := d(2002, 5, 16)
			end := d(2026, 5, 9)
			expectedCount := int(end.Sub(start).Hours()/24) + 1

			_, all, err := fetchSplitsRange(ctx, resty.New(), limiter, start, end)
			Expect(err).NotTo(HaveOccurred())
			Expect(all).To(HaveLen(expectedCount))

			// Verify no duplicate IDs.
			ids := map[string]bool{}
			for _, r := range all {
				Expect(ids[r.ID]).To(BeFalse(), "duplicate id %s", r.ID)
				ids[r.ID] = true
			}

			Expect(ids).To(HaveLen(expectedCount))
		})
	})

	Describe("verifies the request distribution across windows", func() {
		It("sees requests spread across all 4 windows", func() {
			ctx := context.Background()
			limiter := rate.NewLimiter(rate.Inf, 1)
			start := d(2020, 1, 1)
			end := d(2024, 12, 31)

			before := fake.requestCount.Load()

			_, _, err := fetchSplitsRange(ctx, resty.New(), limiter, start, end)
			Expect(err).NotTo(HaveOccurred())

			total := fake.requestCount.Load() - before
			expectedDays := int(end.Sub(start).Hours()/24) + 1
			expectedPages := (expectedDays + fake.pageSize - 1) / fake.pageSize

			// Every paginated chain has at least one terminating
			// page that returns no next_url, but the test page size
			// (50) and 5-year range (~1827 days) means roughly
			// 37 pages total split across 4 windows.
			Expect(total).To(BeNumerically("~", expectedPages, 4))
		})
	})

	Describe("query-param boundary verification", func() {
		It("queries each window with disjoint gte/lte ranges", func() {
			// Capture the gte/lte for every request so we can
			// verify no two windows ever share a date.
			origHandler := fake.server.Config.Handler
			seenRanges := map[string][2]string{}

			var seenLock atomicMutex

			fake.server.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()

				dateField := "execution_date"
				if !strings.Contains(r.URL.Path, "splits") {
					dateField = "ex_dividend_date"
				}

				gte := q.Get(dateField + ".gte")
				lte := q.Get(dateField + ".lte")

				if gte != "" && lte != "" {
					seenLock.Do(func() {
						key := gte + "|" + lte
						seenRanges[key] = [2]string{gte, lte}
					})
				}

				origHandler.ServeHTTP(w, r)
			})

			ctx := context.Background()
			limiter := rate.NewLimiter(rate.Inf, 1)
			start := d(2010, 1, 1)
			end := d(2018, 12, 31)

			_, _, err := fetchSplitsRange(ctx, resty.New(), limiter, start, end)
			Expect(err).NotTo(HaveOccurred())

			// We should have exactly numFetchWindows distinct ranges.
			ranges := make([][2]string, 0, len(seenRanges))
			for _, r := range seenRanges {
				ranges = append(ranges, r)
			}

			sort.Slice(ranges, func(i, j int) bool { return ranges[i][0] < ranges[j][0] })

			Expect(ranges).To(HaveLen(numFetchWindows))

			// Verify no overlap and no gap between consecutive windows.
			for i := 1; i < len(ranges); i++ {
				prevEnd, _ := time.Parse("2006-01-02", ranges[i-1][1])
				curStart, _ := time.Parse("2006-01-02", ranges[i][0])
				Expect(curStart.Sub(prevEnd)).To(Equal(24*time.Hour),
					"gap between window %d (ends %s) and window %d (starts %s) should be exactly 1 day",
					i-1, ranges[i-1][1], i, ranges[i][0])
			}

			// Window 0 must start at the original start; final
			// window must end at the original end.
			Expect(ranges[0][0]).To(Equal("2010-01-01"))
			Expect(ranges[len(ranges)-1][1]).To(Equal("2018-12-31"))

			// Total day coverage must equal the full range.
			covered := 0

			for _, r := range ranges {
				gte, _ := time.Parse("2006-01-02", r[0])
				lte, _ := time.Parse("2006-01-02", r[1])
				covered += int(lte.Sub(gte).Hours()/24) + 1
			}

			expectedDays := int(end.Sub(start).Hours()/24) + 1
			Expect(covered).To(Equal(expectedDays),
				"sum of window day-counts must equal total days")
		})
	})
})

// atomicMutex is a tiny spinlock substitute used so we don't add a
// "sync" import just for one test helper. Tests don't contend heavily.
type atomicMutex struct {
	flag atomic.Bool
}

func (m *atomicMutex) Do(fn func()) {
	for !m.flag.CompareAndSwap(false, true) {
	}

	defer m.flag.Store(false)

	fn()
}
