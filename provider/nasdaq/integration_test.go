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

package nasdaq

import (
	"context"
	"fmt"
	"strings"

	"github.com/penny-vault/pvdata/playwright_helpers"
	"github.com/playwright-community/playwright-go"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("Nasdaq Integration", func() {
	It("should scrape NDX-100 constituents from Nasdaq website", func() {
		ctx := context.Background()
		// Nasdaq requires non-headless mode to avoid bot detection
		page, browserContext, browser, pw := playwright_helpers.StartPlaywright(ctx, false)
		defer playwright_helpers.StopPlaywright(ctx, page, browserContext, browser, pw)

		GinkgoWriter.Println("Navigating to Nasdaq NDX-100 page")

		_, err := page.Goto(NASDAQ_NDX_URL, playwright.PageGotoOptions{
			WaitUntil: playwright.WaitUntilStateDomcontentloaded,
			Timeout:   playwright.Float(60000),
		})
		Expect(err).NotTo(HaveOccurred(), "could not navigate")

		// Wait for the ARIA role-based table to load
		tableSelector := "[role='table']"
		err = page.Locator(tableSelector).WaitFor(playwright.LocatorWaitForOptions{
			State:   playwright.WaitForSelectorStateVisible,
			Timeout: playwright.Float(30000),
		})
		Expect(err).NotTo(HaveOccurred(), "timed out waiting for table")

		// Extract data rows using the same selector as production code
		rows, err := page.Locator(fmt.Sprintf("%s [data-row-index]", tableSelector)).All()
		Expect(err).NotTo(HaveOccurred(), "could not locate data rows")

		GinkgoWriter.Printf("Found %d data rows\n", len(rows))

		Expect(len(rows)).To(BeNumerically(">=", 90),
			"Expected at least 90 NDX-100 constituents")

		// Extract tickers using the same approach as production code
		var tickers []string
		for _, row := range rows {
			tickerLink := row.Locator("a[href*='/market-activity/stocks/']").First()

			tickerText, err := tickerLink.InnerText()
			if err != nil {
				continue
			}

			ticker := strings.TrimSpace(tickerText)
			if ticker != "" {
				tickers = append(tickers, ticker)
			}
		}

		GinkgoWriter.Printf("Extracted %d tickers\n", len(tickers))

		Expect(len(tickers)).To(BeNumerically(">=", 90),
			"Expected at least 90 tickers")

		// Print first 10 tickers
		for i, ticker := range tickers {
			if i >= 10 {
				break
			}

			GinkgoWriter.Printf("  %d: %s\n", i, ticker)
		}
	})
})
