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
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"

	"github.com/go-resty/resty/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

// rewriteTransport is a test http.RoundTripper that rewrites every outbound
// request's scheme and host to point at target. It lets tests intercept
// hardcoded URLs (like SEC's company_tickers.json endpoint) without changing
// production code.
type rewriteTransport struct {
	target string
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	u, err := url.Parse(t.target)
	if err != nil {
		return nil, err
	}

	req.URL.Scheme = u.Scheme
	req.URL.Host = u.Host
	req.Host = u.Host

	return http.DefaultTransport.RoundTrip(req)
}

var _ = Describe("CIK Resolution", func() {
	Describe("ParseCompanyTickers", func() {
		It("parses SEC company_tickers JSON", func() {
			// Sample from https://www.sec.gov/files/company_tickers.json
			jsonData := []byte(`{
				"0": {"cik_str": 320193, "ticker": "AAPL", "title": "Apple Inc."},
				"1": {"cik_str": 789019, "ticker": "MSFT", "title": "MICROSOFT CORP"},
				"2": {"cik_str": 1652044, "ticker": "GOOGL", "title": "Alphabet Inc."}
			}`)

			m, err := ParseCompanyTickers(jsonData)
			Expect(err).NotTo(HaveOccurred())
			Expect(m).To(HaveKey(320193))
			Expect(m[320193].Ticker).To(Equal("AAPL"))
			Expect(m[320193].Name).To(Equal("Apple Inc."))
			Expect(m).To(HaveKey(789019))
			Expect(m[789019].Ticker).To(Equal("MSFT"))
		})
	})

	Describe("FetchCompanyTickers", func() {
		It("fetches and parses company_tickers.json from a server", func() {
			body := `{
				"0": {"cik_str": 320193, "ticker": "AAPL", "title": "Apple Inc."},
				"1": {"cik_str": 789019, "ticker": "MSFT", "title": "MICROSOFT CORP"}
			}`

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(body))
			}))
			defer server.Close()

			// Build a resty client whose transport rewrites the SEC host to
			// the test server. This lets the function-under-test call its
			// hardcoded companyTickersURL while we serve the response.
			client := resty.New().SetTransport(&rewriteTransport{target: server.URL})

			entries, err := FetchCompanyTickers(context.Background(), client)
			Expect(err).NotTo(HaveOccurred())
			Expect(entries).To(HaveLen(2))
			Expect(entries[320193].Ticker).To(Equal("AAPL"))
			Expect(entries[320193].Name).To(Equal("Apple Inc."))
			Expect(entries[789019].Ticker).To(Equal("MSFT"))
		})

		It("returns an error when the server responds with non-200", func() {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			}))
			defer server.Close()

			client := resty.New().
				SetTransport(&rewriteTransport{target: server.URL}).
				SetRetryCount(0)

			entries, err := FetchCompanyTickers(context.Background(), client)
			Expect(err).To(HaveOccurred())
			Expect(entries).To(BeNil())
		})
	})

	Describe("FormatCIK", func() {
		It("zero-pads CIK to 10 digits", func() {
			Expect(FormatCIK(320193)).To(Equal("CIK0000320193"))
		})

		It("handles large CIKs", func() {
			Expect(FormatCIK(1652044)).To(Equal("CIK0001652044"))
		})
	})
})
