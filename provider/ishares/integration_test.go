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

//go:build integration

package ishares

import (
	"fmt"
	"time"

	"github.com/go-resty/resty/v2"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("iShares Integration", func() {
	It("should download and parse IWD holdings CSV", func() {
		etf := iSharesETFMap["IWD"] // Russell 1000 Value -- a known ETF
		ticker := "IWD"

		client := resty.New().
			SetTimeout(60*time.Second).
			SetHeader("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36").
			SetHeader("Accept", "text/csv,text/plain,*/*")

		csvURL := fmt.Sprintf(iSharesHoldingsURLTemplate, etf.ProductID, etf.Slug, ticker)
		GinkgoWriter.Printf("Fetching %s\n", csvURL)

		resp, err := client.R().Get(csvURL)
		Expect(err).NotTo(HaveOccurred(), "HTTP request failed")
		Expect(resp.StatusCode()).To(Equal(200), "unexpected HTTP status: %d", resp.StatusCode())

		GinkgoWriter.Printf("Downloaded %d bytes\n", len(resp.Body()))

		// Parse the CSV
		result, err := parseISharesCSV(resp.Body())
		Expect(err).NotTo(HaveOccurred(), "parsing failed")

		GinkgoWriter.Printf("Snapshot date: %s\n", result.SnapshotDate.Format("2006-01-02"))
		GinkgoWriter.Printf("Number of holdings: %d\n", len(result.Holdings))

		Expect(len(result.Holdings)).To(BeNumerically(">=", 100),
			"Expected at least 100 holdings for Russell 1000 Value")

		// Print first 5 holdings
		for i, h := range result.Holdings {
			if i >= 5 {
				break
			}

			GinkgoWriter.Printf("  %s: weight=%.4f (%.2f%%)\n", h.Ticker, h.Weight, h.Weight*100)
		}
	})
})
