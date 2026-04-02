//go:build integration

package provider

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/go-resty/resty/v2"
	"github.com/penny-vault/pvdata/playwright_helpers"
	"github.com/playwright-community/playwright-go"
)

// TestISharesDownloadAndParse tests the full flow: use resty to download
// the CSV holdings file from iShares and parse it.
// Run with: go test ./provider/ -tags integration -run TestISharesDownloadAndParse -v
func TestISharesDownloadAndParse(t *testing.T) {
	etf := iSharesETFMap["IWD"] // Russell 1000 Value -- a known ETF
	ticker := "IWD"

	client := resty.New().
		SetTimeout(60*time.Second).
		SetHeader("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36").
		SetHeader("Accept", "text/csv,text/plain,*/*")

	csvURL := fmt.Sprintf(iSharesHoldingsURLTemplate, etf.ProductID, etf.Slug, ticker)
	t.Logf("Fetching %s", csvURL)

	resp, err := client.R().Get(csvURL)
	if err != nil {
		t.Fatalf("HTTP request failed: %v", err)
	}

	if resp.StatusCode() != 200 {
		t.Fatalf("HTTP %d: %s", resp.StatusCode(), string(resp.Body()[:min(500, len(resp.Body()))]))
	}

	t.Logf("Downloaded %d bytes", len(resp.Body()))

	// Parse the CSV
	result, err := parseISharesCSV(resp.Body())
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
