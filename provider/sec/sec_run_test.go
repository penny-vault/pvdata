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

package sec

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"

	"github.com/go-resty/resty/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
)

// makeCompanyFactsJSON builds a minimal SEC companyfacts JSON document with a
// single Revenues fact for a 10-K period. This is enough for ParseCompanyFacts
// to identify a period and for emitFundamentals to emit observations.
func makeCompanyFactsJSON(cik int, entityName string, periodEnd, filed time.Time) []byte {
	periodStart := periodEnd.AddDate(-1, 0, 0).AddDate(0, 0, 1)

	return fmt.Appendf(nil, `{
  "cik": %d,
  "entityName": %q,
  "facts": {
    "us-gaap": {
      "Revenues": {
        "label": "Revenues",
        "description": "Revenue",
        "units": {
          "USD": [
            {
              "start": %q,
              "end": %q,
              "val": 1000000,
              "accn": "0000000000-00-000001",
              "fy": %d,
              "fp": "FY",
              "form": "10-K",
              "filed": %q
            }
          ]
        }
      }
    }
  }
}`,
		cik,
		entityName,
		periodStart.Format("2006-01-02"),
		periodEnd.Format("2006-01-02"),
		periodEnd.Year(),
		filed.Format("2006-01-02"),
	)
}

// makeCompanyFactsZip builds an in-memory zip archive containing one entry per
// (cik, jsonData) pair, named after the standard SEC convention
// "CIK0000320193.json".
func makeCompanyFactsZip(entries map[int][]byte) []byte {
	var buf bytes.Buffer

	zw := zip.NewWriter(&buf)
	for cik, data := range entries {
		w, err := zw.Create(fmt.Sprintf("CIK%010d.json", cik))
		Expect(err).NotTo(HaveOccurred())

		_, err = w.Write(data)
		Expect(err).NotTo(HaveOccurred())
	}

	Expect(zw.Close()).To(Succeed())

	return buf.Bytes()
}

// makeFeedXML builds a minimal EDGAR ATOM feed with the given entries.
func makeFeedXML(entries []feedEntryFixture) []byte {
	var b strings.Builder

	b.WriteString(`<?xml version="1.0" encoding="UTF-8" ?>`)
	b.WriteString(`<feed xmlns="http://www.w3.org/2005/Atom">`)
	b.WriteString(`<title>Test Feed</title>`)

	for _, e := range entries {
		fmt.Fprintf(&b,
			`<entry>`+
				`<title>%s - Test Co (%010d) (Filer)</title>`+
				`<link rel="alternate" type="text/html" href="https://www.sec.gov/Archives/edgar/data/%d/000000000000000000/0000000000-00-000000-index.htm"/>`+
				`<summary type="html"> &lt;b&gt;Filed:&lt;/b&gt; %s &lt;b&gt;AccNo:&lt;/b&gt; 0000000000-00-000000 &lt;b&gt;Size:&lt;/b&gt; 1 MB</summary>`+
				`<updated>%s</updated>`+
				`<category scheme="https://www.sec.gov/" label="form type" term="%s"/>`+
				`<id>urn:tag:sec.gov,2008:accession-number=0000000000-00-000000</id>`+
				`</entry>`,
			e.formType, e.cik, e.cik, e.filed.Format("2006-01-02"),
			e.filed.Format(time.RFC3339), e.formType,
		)
	}

	b.WriteString(`</feed>`)

	return []byte(b.String())
}

type feedEntryFixture struct {
	cik      int
	formType string
	filed    time.Time
}

// fakeSECServer is a small router that mimics the parts of SEC.gov used by
// runBackfill and runIncremental. The handlers can be overridden per test.
type fakeSECServer struct {
	mu sync.Mutex

	zipHandler       http.HandlerFunc
	feedHandler      http.HandlerFunc
	companyFactsFunc http.HandlerFunc

	feedRequests        int
	companyFactsRequests map[int]int
}

func newFakeSECServer() *fakeSECServer {
	return &fakeSECServer{companyFactsRequests: map[int]int{}}
}

func (f *fakeSECServer) handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/Archives/edgar/daily-index/xbrl/companyfacts.zip"):
			if f.zipHandler != nil {
				f.zipHandler(w, r)
				return
			}

			http.NotFound(w, r)

		case strings.Contains(r.URL.Path, "/cgi-bin/browse-edgar"):
			f.mu.Lock()
			f.feedRequests++
			f.mu.Unlock()

			if f.feedHandler != nil {
				f.feedHandler(w, r)
				return
			}

			http.NotFound(w, r)

		case strings.Contains(r.URL.Path, "/api/xbrl/companyfacts/"):
			// Track which CIK was requested.
			base := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/xbrl/companyfacts/"), ".json")
			base = strings.TrimPrefix(base, "CIK")

			var cik int
			_, _ = fmt.Sscanf(base, "%d", &cik)

			f.mu.Lock()
			f.companyFactsRequests[cik]++
			f.mu.Unlock()

			if f.companyFactsFunc != nil {
				f.companyFactsFunc(w, r)
				return
			}

			http.NotFound(w, r)

		default:
			http.NotFound(w, r)
		}
	})
}

// stubSubscription returns a minimal *library.Subscription suitable for
// passing to runBackfill/runIncremental. The full library.Subscription has
// many fields, but the run functions only consult ID, Name, and LastObsDate.
func stubSubscription(name string, lastObs time.Time) *library.Subscription {
	return &library.Subscription{
		Name:        name,
		LastObsDate: lastObs,
	}
}

// drainObservations reads everything currently buffered on out without
// blocking. Tests build a buffered channel large enough that no observations
// are lost during the run.
func drainObservations(out chan *data.Observation) []*data.Observation {
	var obs []*data.Observation

	for {
		select {
		case o := <-out:
			obs = append(obs, o)
		default:
			return obs
		}
	}
}

var _ = Describe("runBackfill", func() {
	var (
		server  *httptest.Server
		fake    *fakeSECServer
		client  *resty.Client
		out     chan *data.Observation
		sub     *library.Subscription
		cikMap  map[int]AssetInfo
		numObs  int
		skipped int
	)

	BeforeEach(func() {
		fake = newFakeSECServer()
		server = httptest.NewServer(fake.handler())
		client = resty.New().
			SetTransport(&rewriteTransport{target: server.URL}).
			SetRetryCount(0)
		out = make(chan *data.Observation, 1000)
		sub = stubSubscription("test-backfill", time.Time{})
		numObs = 0
		skipped = 0

		cikMap = map[int]AssetInfo{
			320193: {Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", CIK: 320193},
			789019: {Ticker: "MSFT", CompositeFigi: "BBG000BPH459", CIK: 789019},
		}
	})

	AfterEach(func() {
		server.Close()
	})

	It("processes both companies and emits observations (happy path)", func() {
		periodEnd := time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC)
		filed := time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC)

		zipBytes := makeCompanyFactsZip(map[int][]byte{
			320193: makeCompanyFactsJSON(320193, "Apple Inc.", periodEnd, filed),
			789019: makeCompanyFactsJSON(789019, "Microsoft Corp", periodEnd, filed),
		})

		fake.zipHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/zip")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(zipBytes)
		}

		err := runBackfill(context.Background(), client, cikMap, sub, out, &numObs, &skipped)
		Expect(err).NotTo(HaveOccurred())

		obs := drainObservations(out)
		// Each 10-K period emits ARY + MRY (2 observations per company), 2 companies = 4.
		Expect(obs).To(HaveLen(4))
		Expect(numObs).To(Equal(4))
		Expect(skipped).To(Equal(0))

		// Verify both tickers appear in the emitted observations.
		tickers := map[string]bool{}
		for _, o := range obs {
			Expect(o.Fundamental).NotTo(BeNil())
			tickers[o.Fundamental.Ticker] = true
		}

		Expect(tickers).To(HaveKey("AAPL"))
		Expect(tickers).To(HaveKey("MSFT"))
	})

	It("skips CIKs not in the cikMap without emitting", func() {
		periodEnd := time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC)
		filed := time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC)

		zipBytes := makeCompanyFactsZip(map[int][]byte{
			999999: makeCompanyFactsJSON(999999, "Unknown Co", periodEnd, filed),
		})

		fake.zipHandler = func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/zip")
			_, _ = w.Write(zipBytes)
		}

		err := runBackfill(context.Background(), client, cikMap, sub, out, &numObs, &skipped)
		Expect(err).NotTo(HaveOccurred())

		Expect(drainObservations(out)).To(BeEmpty())
		Expect(numObs).To(Equal(0))
		Expect(skipped).To(Equal(0))
	})

	It("skips CIKs that have no composite FIGI and increments skipped counter", func() {
		// Add a CIK with empty FIGI -- simulates the SEC tickers fallback path.
		cikMap[111111] = AssetInfo{Ticker: "FIGIX", CompositeFigi: "", CIK: 111111}

		periodEnd := time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC)
		filed := time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC)

		zipBytes := makeCompanyFactsZip(map[int][]byte{
			111111: makeCompanyFactsJSON(111111, "FigiX Corp", periodEnd, filed),
		})

		fake.zipHandler = func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(zipBytes)
		}

		err := runBackfill(context.Background(), client, cikMap, sub, out, &numObs, &skipped)
		Expect(err).NotTo(HaveOccurred())

		Expect(drainObservations(out)).To(BeEmpty())
		Expect(skipped).To(Equal(1))
	})

	It("returns an error when the zip body is invalid", func() {
		fake.zipHandler = func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("this is not a zip file"))
		}

		err := runBackfill(context.Background(), client, cikMap, sub, out, &numObs, &skipped)
		Expect(err).To(HaveOccurred())
	})

	It("processes other files when one zip entry has malformed JSON", func() {
		periodEnd := time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC)
		filed := time.Date(2024, 2, 15, 0, 0, 0, 0, time.UTC)

		// Three files: two valid, one malformed JSON.
		// 320193 (AAPL) and 789019 (MSFT) are valid; 320193 will share its CIK
		// with the broken entry but file names are unique so we use a different
		// CIK for the broken file too.
		validApple := makeCompanyFactsJSON(320193, "Apple Inc.", periodEnd, filed)
		validMSFT := makeCompanyFactsJSON(789019, "Microsoft Corp", periodEnd, filed)
		broken := []byte(`{"cik": 222222, "entityName": "Broken`)

		// 222222 must be in cikMap so the parse error path is exercised.
		cikMap[222222] = AssetInfo{Ticker: "BRKN", CompositeFigi: "BBG000BROKEN", CIK: 222222}

		zipBytes := makeCompanyFactsZip(map[int][]byte{
			320193: validApple,
			789019: validMSFT,
			222222: broken,
		})

		fake.zipHandler = func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(zipBytes)
		}

		err := runBackfill(context.Background(), client, cikMap, sub, out, &numObs, &skipped)
		Expect(err).NotTo(HaveOccurred())

		obs := drainObservations(out)
		// 2 valid companies x 2 observations each (ARY + MRY) = 4.
		Expect(obs).To(HaveLen(4))

		tickers := map[string]bool{}
		for _, o := range obs {
			tickers[o.Fundamental.Ticker] = true
		}

		Expect(tickers).To(HaveKey("AAPL"))
		Expect(tickers).To(HaveKey("MSFT"))
		Expect(tickers).NotTo(HaveKey("BRKN"))
	})
})

var _ = Describe("runIncremental", func() {
	var (
		server  *httptest.Server
		fake    *fakeSECServer
		client  *resty.Client
		out     chan *data.Observation
		sub     *library.Subscription
		cikMap  map[int]AssetInfo
		numObs  int
		skipped int
		since   time.Time
	)

	BeforeEach(func() {
		fake = newFakeSECServer()
		server = httptest.NewServer(fake.handler())
		client = resty.New().
			SetTransport(&rewriteTransport{target: server.URL}).
			SetRetryCount(0)
		out = make(chan *data.Observation, 1000)
		since = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
		sub = stubSubscription("test-incremental", since)
		numObs = 0
		skipped = 0

		cikMap = map[int]AssetInfo{
			320193: {Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", CIK: 320193},
			789019: {Ticker: "MSFT", CompositeFigi: "BBG000BPH459", CIK: 789019},
		}
	})

	AfterEach(func() {
		server.Close()
	})

	It("fetches both CIKs and emits observations (happy path)", func() {
		periodEnd := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
		filedNew := time.Date(2025, 2, 15, 0, 0, 0, 0, time.UTC)

		fake.feedHandler = func(w http.ResponseWriter, r *http.Request) {
			// Only return entries for start=0; everything else is empty.
			start := r.URL.Query().Get("start")
			if start != "0" {
				_, _ = w.Write(makeFeedXML(nil))
				return
			}
			_, _ = w.Write(makeFeedXML([]feedEntryFixture{
				{cik: 320193, formType: "10-K", filed: filedNew},
				{cik: 789019, formType: "10-Q", filed: filedNew},
			}))
		}

		fake.companyFactsFunc = func(w http.ResponseWriter, r *http.Request) {
			base := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/xbrl/companyfacts/"), ".json")
			base = strings.TrimPrefix(base, "CIK")

			var cik int
			_, _ = fmt.Sscanf(base, "%d", &cik)

			name := "Apple Inc."
			if cik == 789019 {
				name = "Microsoft Corp"
			}

			_, _ = w.Write(makeCompanyFactsJSON(cik, name, periodEnd, filedNew))
		}

		err := runIncremental(context.Background(), client, cikMap, sub, since, out, &numObs, &skipped)
		Expect(err).NotTo(HaveOccurred())

		obs := drainObservations(out)
		// Each company emits ARY+MRY (2) for the FY2024 10-K period.
		Expect(obs).To(HaveLen(4))

		tickers := map[string]bool{}
		for _, o := range obs {
			tickers[o.Fundamental.Ticker] = true
			// Filing date >= since: incremental filter must hold.
			Expect(o.Fundamental.LastUpdated).To(BeTemporally(">=", since))
		}

		Expect(tickers).To(HaveKey("AAPL"))
		Expect(tickers).To(HaveKey("MSFT"))

		// Both CIKs should have been fetched exactly once.
		Expect(fake.companyFactsRequests[320193]).To(Equal(1))
		Expect(fake.companyFactsRequests[789019]).To(Equal(1))
	})

	It("fetches both pages and dedupes CIKs across pages", func() {
		periodEnd := time.Date(2024, 12, 31, 0, 0, 0, 0, time.UTC)
		filedNew := time.Date(2025, 2, 15, 0, 0, 0, 0, time.UTC)

		// Fill cikMap so the dedup test has 100+ unique CIKs available.
		for cik := 1000; cik < 1300; cik++ {
			cikMap[cik] = AssetInfo{
				Ticker:        fmt.Sprintf("T%d", cik),
				CompositeFigi: fmt.Sprintf("BBG%09d", cik),
				CIK:           cik,
			}
		}

		fake.feedHandler = func(w http.ResponseWriter, r *http.Request) {
			start := r.URL.Query().Get("start")

			var entries []feedEntryFixture
			switch start {
			case "0":
				// Page 1: 100 entries; CIK 1050 will repeat on page 2.
				for cik := 1000; cik < 1100; cik++ {
					entries = append(entries, feedEntryFixture{
						cik: cik, formType: "10-K", filed: filedNew,
					})
				}
			case "100":
				// Page 2: 100 entries, including a repeat of 1050.
				entries = append(entries, feedEntryFixture{
					cik: 1050, formType: "10-K", filed: filedNew,
				})

				for cik := 1100; cik < 1199; cik++ {
					entries = append(entries, feedEntryFixture{
						cik: cik, formType: "10-K", filed: filedNew,
					})
				}
			case "200":
				// Page 3: empty -- stop pagination.
			}

			_, _ = w.Write(makeFeedXML(entries))
		}

		fake.companyFactsFunc = func(w http.ResponseWriter, r *http.Request) {
			base := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/xbrl/companyfacts/"), ".json")
			base = strings.TrimPrefix(base, "CIK")

			var cik int
			_, _ = fmt.Sscanf(base, "%d", &cik)

			_, _ = w.Write(makeCompanyFactsJSON(cik, fmt.Sprintf("Co%d", cik), periodEnd, filedNew))
		}

		err := runIncremental(context.Background(), client, cikMap, sub, since, out, &numObs, &skipped)
		Expect(err).NotTo(HaveOccurred())

		// Page 1 had 100 unique CIKs, page 2 added 99 new + 1 duplicate, so we
		// expect 199 unique CIKs to be fetched.
		Expect(len(fake.companyFactsRequests)).To(Equal(199))

		// Duplicate CIK should only have been fetched once.
		Expect(fake.companyFactsRequests[1050]).To(Equal(1))

		// Verify pagination actually fetched at least 3 pages (the empty stop
		// page counts).
		Expect(fake.feedRequests).To(BeNumerically(">=", 3))
	})

	It("stops paginating when a page contains only filings older than since", func() {
		periodEnd := time.Date(2023, 12, 31, 0, 0, 0, 0, time.UTC)
		oldFiled := time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)

		fake.feedHandler = func(w http.ResponseWriter, r *http.Request) {
			start := r.URL.Query().Get("start")
			if start != "0" {
				// Should not get here -- pagination must stop after page 1.
				Fail(fmt.Sprintf("unexpected pagination request for start=%s", start))
			}
			// All entries on this page are older than since.
			_, _ = w.Write(makeFeedXML([]feedEntryFixture{
				{cik: 320193, formType: "10-K", filed: oldFiled},
				{cik: 789019, formType: "10-Q", filed: oldFiled},
			}))
		}

		fake.companyFactsFunc = func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write(makeCompanyFactsJSON(320193, "Apple Inc.", periodEnd, oldFiled))
		}

		err := runIncremental(context.Background(), client, cikMap, sub, since, out, &numObs, &skipped)
		Expect(err).NotTo(HaveOccurred())

		// Only one page fetched.
		Expect(fake.feedRequests).To(Equal(1))

		// All filings older than since -> no companyfacts fetched.
		Expect(fake.companyFactsRequests).To(BeEmpty())
	})

	It("skips CIKs not in cikMap without fetching companyfacts", func() {
		filedNew := time.Date(2025, 2, 15, 0, 0, 0, 0, time.UTC)

		fake.feedHandler = func(w http.ResponseWriter, r *http.Request) {
			start := r.URL.Query().Get("start")
			if start != "0" {
				_, _ = w.Write(makeFeedXML(nil))
				return
			}
			// 999999 is NOT in the cikMap and must be skipped.
			_, _ = w.Write(makeFeedXML([]feedEntryFixture{
				{cik: 999999, formType: "10-K", filed: filedNew},
			}))
		}

		fake.companyFactsFunc = func(w http.ResponseWriter, _ *http.Request) {
			Fail("companyfacts should not be fetched for an unknown CIK")
		}

		err := runIncremental(context.Background(), client, cikMap, sub, since, out, &numObs, &skipped)
		Expect(err).NotTo(HaveOccurred())

		Expect(fake.companyFactsRequests).To(BeEmpty())
		Expect(drainObservations(out)).To(BeEmpty())
	})
})
