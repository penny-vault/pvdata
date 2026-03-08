//go:build integration

package provider

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/penny-vault/pvdata/playwright_helpers"
	"github.com/playwright-community/playwright-go"
)

// TestISharesDownloadAndParse tests the full flow: Playwright navigates to
// an iShares product page, downloads the XLS file, and parses it.
// Run with: go test ./provider/ -tags integration -run TestISharesDownloadAndParse -v
func TestISharesDownloadAndParse(t *testing.T) {
	etf := iSharesETFMap["IWD"] // Russell 1000 Value -- a known ETF

	page, ctx, browser, pw := playwright_helpers.StartPlaywright(true) // non-headless for debugging
	defer playwright_helpers.StopPlaywright(page, ctx, browser, pw)

	productURL := fmt.Sprintf("https://www.ishares.com/us/products/%s/%s", etf.ProductID, etf.Slug)
	t.Logf("Navigating to %s", productURL)

	if _, err := page.Goto(productURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(60000),
	}); err != nil {
		t.Fatalf("could not navigate to product page: %v", err)
	}

	// Wait for the page to settle -- iShares pages are JS-heavy
	page.WaitForTimeout(5000)

	// Try to find and click the download link
	downloadLink := page.Locator("a[href*='.ajax'][href*='fileType=xls']")
	count, err := downloadLink.Count()
	if err != nil {
		t.Fatalf("could not count download links: %v", err)
	}
	t.Logf("Found %d download links matching selector", count)

	if count == 0 {
		// Try alternative selectors
		t.Log("No links found with primary selector. Trying alternatives...")

		altSelectors := []string{
			"a[href*='fileType=xls']",
			"a[href*='.ajax']",
			"a[download]",
			"a:has-text('Download')",
			"a:has-text('Export')",
		}

		for _, sel := range altSelectors {
			loc := page.Locator(sel)
			c, _ := loc.Count()
			t.Logf("  Selector %q: %d matches", sel, c)
			if c > 0 {
				// Print the href of the first match
				href, _ := loc.First().GetAttribute("href")
				t.Logf("    First href: %s", href)
			}
		}

		t.Fatal("Could not find download link. Adjust the selector.")
	}

	// Download the file
	download, err := page.ExpectDownload(func() error {
		return downloadLink.First().Click()
	})
	if err != nil {
		t.Fatalf("download failed: %v", err)
	}

	path, err := download.Path()
	if err != nil {
		t.Fatalf("could not get download path: %v", err)
	}

	fileData, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("could not read downloaded file: %v", err)
	}

	t.Logf("Downloaded %d bytes", len(fileData))

	// Parse the file
	result, err := parseISharesXML(fileData)
	if err != nil {
		t.Fatalf("parsing failed: %v", err)
	}

	t.Logf("Snapshot date: %s", result.SnapshotDate.Format("2006-01-02"))
	t.Logf("Number of holdings: %d", len(result.Holdings))

	if len(result.Holdings) < 100 {
		t.Errorf("Expected at least 100 holdings for Russell 1000 Value, got %d", len(result.Holdings))
	}

	// Print first 5 holdings
	for i, h := range result.Holdings {
		if i >= 5 {
			break
		}
		t.Logf("  %s: weight=%.4f (%.2f%%)", h.Ticker, h.Weight, h.Weight*100)
	}
}

// TestNasdaqScrape tests that we can scrape the Nasdaq NDX-100 page
// using the same selectors as the production code.
// Run with: go test ./provider/ -tags integration -run TestNasdaqScrape -v
func TestNasdaqScrape(t *testing.T) {
	// Nasdaq requires non-headless mode to avoid bot detection
	page, ctx, browser, pw := playwright_helpers.StartPlaywright(false)
	defer playwright_helpers.StopPlaywright(page, ctx, browser, pw)

	t.Log("Navigating to Nasdaq NDX-100 page")

	if _, err := page.Goto(NASDAQ_NDX_URL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateDomcontentloaded,
		Timeout:   playwright.Float(60000),
	}); err != nil {
		t.Fatalf("could not navigate: %v", err)
	}

	// Wait for the ARIA role-based table to load
	tableSelector := "[role='table']"
	if err := page.Locator(tableSelector).WaitFor(playwright.LocatorWaitForOptions{
		State:   playwright.WaitForSelectorStateVisible,
		Timeout: playwright.Float(30000),
	}); err != nil {
		t.Fatalf("timed out waiting for table: %v", err)
	}

	// Extract data rows using the same selector as production code
	rows, err := page.Locator(fmt.Sprintf("%s [data-row-index]", tableSelector)).All()
	if err != nil {
		t.Fatalf("could not locate data rows: %v", err)
	}

	t.Logf("Found %d data rows", len(rows))

	if len(rows) < 90 {
		t.Errorf("Expected at least 90 NDX-100 constituents, got %d", len(rows))
	}

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

	t.Logf("Extracted %d tickers", len(tickers))

	if len(tickers) < 90 {
		t.Errorf("Expected at least 90 tickers, got %d", len(tickers))
	}

	// Print first 10 tickers
	for i, ticker := range tickers {
		if i >= 10 {
			break
		}
		t.Logf("  %d: %s", i, ticker)
	}
}
