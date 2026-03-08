# Index Scraping Providers Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add iShares and Nasdaq providers to scrape index constituent data (snapshots with weights + membership changelog).

**Architecture:** Two new providers following existing patterns (Provider interface, Playwright for fetching, IndexSnapshot/IndexChange observations). Shared helper for snapshot frequency gating and changelog diffing. Data model change to add weight to IndexSnapshot.

**Tech Stack:** Go, Playwright (via playwright-go + go-rod/stealth), XML parsing (encoding/xml), PostgreSQL (pgx/v5), Ginkgo/Gomega for tests.

---

### Task 1: Add Weight to IndexSnapshot Data Model

**Files:**
- Modify: `data/index.go:26-31` (IndexSnapshot struct)
- Modify: `data/index.go:33-73` (SaveDB method)
- Modify: `data/datatype.go:232-257` (IndexKey schema + migration)

**Step 1: Add Weight field to IndexSnapshot struct**

In `data/index.go`, add the Weight field:

```go
type IndexSnapshot struct {
	Ticker        string
	CompositeFigi string
	IndexName     string
	SnapshotDate  time.Time
	Weight        float64
}
```

**Step 2: Update SaveDB to include weight**

In `data/index.go`, update the `SaveDB` method to include weight in the INSERT and change DO NOTHING to DO UPDATE:

```go
func (idx *IndexSnapshot) SaveDB(ctx context.Context, tbl string, dbConn *pgxpool.Conn) error {
	if idx.CompositeFigi == "" {
		return nil
	}

	tx, err := dbConn.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err := tx.Commit(ctx); err != nil {
			log.Error().Err(err).Msg("error committing index snapshot transaction to database")
		}
	}()

	sql := fmt.Sprintf(`INSERT INTO %[1]s_snapshot (
		"composite_figi",
		"ticker",
		"index_name",
		"snapshot_date",
		"weight"
	) VALUES (
		$1, $2, $3, $4, $5
	) ON CONFLICT ON CONSTRAINT %[1]s_snapshot_pkey DO UPDATE SET
		weight = EXCLUDED.weight`, tbl)

	_, err = tx.Exec(ctx, sql,
		idx.CompositeFigi,
		idx.Ticker,
		idx.IndexName,
		idx.SnapshotDate,
		idx.Weight,
	)

	if err != nil {
		log.Error().Err(err).Str("SQL", sql).Msg("save index snapshot to DB failed")
		if err2 := tx.Rollback(ctx); err2 != nil {
			log.Error().Err(err).Msg("error rollingback tx")
		}
	}

	return err
}
```

**Step 3: Update IndexKey schema and add migration**

In `data/datatype.go`, update the IndexKey entry:

```go
IndexKey: {
	Name:     IndexKey,
	ViewName: "indices",
	Schema: `CREATE TABLE %[1]s_snapshot (
    composite_figi CHARACTER(12)         NOT NULL,
    ticker         CHARACTER VARYING(10) NOT NULL,
    index_name     TEXT                  NOT NULL,
    snapshot_date  DATE                  NOT NULL,
    weight         REAL                  NOT NULL DEFAULT 0.0,
    PRIMARY KEY (composite_figi, index_name, snapshot_date)
);

CREATE TABLE %[1]s_changelog (
    composite_figi CHARACTER(12)         NOT NULL,
    ticker         CHARACTER VARYING(10) NOT NULL,
    index_name     TEXT                  NOT NULL,
    event_date     DATE                  NOT NULL,
    action         TEXT                  NOT NULL,
    PRIMARY KEY (composite_figi, index_name, event_date)
);

CREATE INDEX %[1]s_snapshot_index_name_idx ON %[1]s_snapshot(index_name, snapshot_date);
CREATE INDEX %[1]s_changelog_index_name_idx ON %[1]s_changelog(index_name, event_date);`,
	Migrations: []string{
		`ALTER TABLE %[1]s_snapshot ADD COLUMN IF NOT EXISTS weight REAL NOT NULL DEFAULT 0.0;`,
	},
	Version:       1,
	IsPartitioned: false,
},
```

**Step 4: Verify the project compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`
Expected: No errors. Existing code that emits IndexSnapshot without weight will use the zero value (0.0).

**Step 5: Commit**

```bash
git add data/index.go data/datatype.go
git commit -m "feat: add weight field to IndexSnapshot"
```

---

### Task 2: Add Index Helpers (Snapshot Frequency Gating + Changelog Diffing)

**Files:**
- Create: `provider/index_helpers.go`
- Create: `provider/index_helpers_test.go`

**Step 1: Write tests for shouldTakeSnapshot and diffSnapshots**

Create `provider/index_helpers_test.go`:

```go
package provider

import (
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestIndexHelpers(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Index Helpers Suite")
}

var _ = Describe("shouldTakeSnapshot", func() {
	It("returns true when no previous snapshot exists", func() {
		Expect(shouldTakeSnapshot(time.Time{}, "weekly")).To(BeTrue())
	})

	It("returns true when daily and last snapshot was yesterday", func() {
		yesterday := time.Now().AddDate(0, 0, -1)
		Expect(shouldTakeSnapshot(yesterday, "daily")).To(BeTrue())
	})

	It("returns false when daily and last snapshot was today", func() {
		today := time.Now()
		Expect(shouldTakeSnapshot(today, "daily")).To(BeFalse())
	})

	It("returns true when weekly and last snapshot was 8 days ago", func() {
		eightDaysAgo := time.Now().AddDate(0, 0, -8)
		Expect(shouldTakeSnapshot(eightDaysAgo, "weekly")).To(BeTrue())
	})

	It("returns false when weekly and last snapshot was 3 days ago", func() {
		threeDaysAgo := time.Now().AddDate(0, 0, -3)
		Expect(shouldTakeSnapshot(threeDaysAgo, "weekly")).To(BeFalse())
	})

	It("returns true when monthly and last snapshot was 32 days ago", func() {
		thirtyTwoDaysAgo := time.Now().AddDate(0, 0, -32)
		Expect(shouldTakeSnapshot(thirtyTwoDaysAgo, "monthly")).To(BeTrue())
	})

	It("returns false when monthly and last snapshot was 15 days ago", func() {
		fifteenDaysAgo := time.Now().AddDate(0, 0, -15)
		Expect(shouldTakeSnapshot(fifteenDaysAgo, "monthly")).To(BeFalse())
	})

	It("returns true when quarterly and last snapshot was 95 days ago", func() {
		ninetyFiveDaysAgo := time.Now().AddDate(0, 0, -95)
		Expect(shouldTakeSnapshot(ninetyFiveDaysAgo, "quarterly")).To(BeTrue())
	})

	It("defaults to weekly for unknown frequency", func() {
		eightDaysAgo := time.Now().AddDate(0, 0, -8)
		Expect(shouldTakeSnapshot(eightDaysAgo, "bogus")).To(BeTrue())
	})
})

type mockHolding struct {
	ticker string
	figi   string
}

var _ = Describe("diffSnapshots", func() {
	It("returns all as added when previous is empty", func() {
		current := map[string]string{"AAPL": "BBG000B9XRY4", "MSFT": "BBG000BPH459"}
		adds, removes := diffSnapshots(current, map[string]string{})
		Expect(adds).To(HaveLen(2))
		Expect(removes).To(BeEmpty())
	})

	It("returns all as removed when current is empty", func() {
		previous := map[string]string{"AAPL": "BBG000B9XRY4"}
		adds, removes := diffSnapshots(map[string]string{}, previous)
		Expect(adds).To(BeEmpty())
		Expect(removes).To(HaveLen(1))
		Expect(removes).To(HaveKey("AAPL"))
	})

	It("detects additions and removals", func() {
		current := map[string]string{"AAPL": "BBG000B9XRY4", "GOOG": "BBG009S39JX6"}
		previous := map[string]string{"AAPL": "BBG000B9XRY4", "MSFT": "BBG000BPH459"}
		adds, removes := diffSnapshots(current, previous)
		Expect(adds).To(HaveLen(1))
		Expect(adds).To(HaveKey("GOOG"))
		Expect(removes).To(HaveLen(1))
		Expect(removes).To(HaveKey("MSFT"))
	})

	It("returns empty when sets are identical", func() {
		current := map[string]string{"AAPL": "BBG000B9XRY4"}
		previous := map[string]string{"AAPL": "BBG000B9XRY4"}
		adds, removes := diffSnapshots(current, previous)
		Expect(adds).To(BeEmpty())
		Expect(removes).To(BeEmpty())
	})
})
```

**Step 2: Run tests to verify they fail**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run "Index Helpers" -v`
Expected: FAIL (functions not defined)

**Step 3: Implement index_helpers.go**

Create `provider/index_helpers.go`:

```go
package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/penny-vault/pvdata/data"
	"github.com/rs/zerolog/log"
)

// shouldTakeSnapshot returns true if a new snapshot should be taken based on the
// configured frequency and the date of the last snapshot.
func shouldTakeSnapshot(lastSnapshotDate time.Time, frequency string) bool {
	if lastSnapshotDate.IsZero() {
		return true
	}

	var interval time.Duration
	switch frequency {
	case "daily":
		interval = 24 * time.Hour
	case "weekly":
		interval = 7 * 24 * time.Hour
	case "monthly":
		interval = 30 * 24 * time.Hour
	case "quarterly":
		interval = 90 * 24 * time.Hour
	default:
		interval = 7 * 24 * time.Hour
	}

	return time.Since(lastSnapshotDate) >= interval
}

// diffSnapshots compares current holdings (ticker->figi) against previous holdings
// and returns maps of added and removed tickers.
func diffSnapshots(current, previous map[string]string) (added, removed map[string]string) {
	added = make(map[string]string)
	removed = make(map[string]string)

	for ticker, figi := range current {
		if _, ok := previous[ticker]; !ok {
			added[ticker] = figi
		}
	}

	for ticker, figi := range previous {
		if _, ok := current[ticker]; !ok {
			removed[ticker] = figi
		}
	}

	return
}

// lastSnapshotDate queries the database for the most recent snapshot date for the given index.
func lastSnapshotDate(ctx context.Context, pool *pgxpool.Pool, table, indexName string) time.Time {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire db connection for lastSnapshotDate")
		return time.Time{}
	}
	defer conn.Release()

	var snapshotDate time.Time
	sql := fmt.Sprintf(`SELECT COALESCE(MAX(snapshot_date), '0001-01-01') FROM %s_snapshot WHERE index_name = $1`, table)
	err = conn.QueryRow(ctx, sql, indexName).Scan(&snapshotDate)
	if err != nil {
		log.Error().Err(err).Msg("could not query last snapshot date")
		return time.Time{}
	}
	return snapshotDate
}

// previousSnapshotTickers queries the database for all tickers in the most recent
// snapshot for the given index name, returning a map of ticker->compositeFigi.
func previousSnapshotTickers(ctx context.Context, pool *pgxpool.Pool, table, indexName string) map[string]string {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		log.Error().Err(err).Msg("could not acquire db connection for previousSnapshotTickers")
		return map[string]string{}
	}
	defer conn.Release()

	sql := fmt.Sprintf(`SELECT ticker, composite_figi FROM %s_snapshot
		WHERE index_name = $1 AND snapshot_date = (
			SELECT MAX(snapshot_date) FROM %s_snapshot WHERE index_name = $1
		)`, table, table)

	rows, err := conn.Query(ctx, sql, indexName)
	if err != nil {
		log.Error().Err(err).Msg("could not query previous snapshot tickers")
		return map[string]string{}
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var ticker, figi string
		if err := rows.Scan(&ticker, &figi); err != nil {
			log.Error().Err(err).Msg("error scanning previous snapshot row")
			continue
		}
		result[ticker] = figi
	}
	return result
}

// emitChangelog emits IndexChange observations for adds and removes.
func emitChangelog(adds, removes map[string]string, indexName string, eventDate time.Time, subscription *data.Observation, out chan<- *data.Observation) {
	for ticker, figi := range adds {
		out <- &data.Observation{
			IndexChange: &data.IndexChange{
				Ticker:        ticker,
				CompositeFigi: figi,
				IndexName:     indexName,
				EventDate:     eventDate,
				Action:        "add",
			},
			ObservationDate:  subscription.ObservationDate,
			SubscriptionID:   subscription.SubscriptionID,
			SubscriptionName: subscription.SubscriptionName,
		}
	}

	for ticker, figi := range removes {
		out <- &data.Observation{
			IndexChange: &data.IndexChange{
				Ticker:        ticker,
				CompositeFigi: figi,
				IndexName:     indexName,
				EventDate:     eventDate,
				Action:        "remove",
			},
			ObservationDate:  subscription.ObservationDate,
			SubscriptionID:   subscription.SubscriptionID,
			SubscriptionName: subscription.SubscriptionName,
		}
	}
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run "Index Helpers" -v`
Expected: PASS

**Step 5: Verify full project compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`
Expected: No errors

**Step 6: Commit**

```bash
git add provider/index_helpers.go provider/index_helpers_test.go
git commit -m "feat: add index helper functions for snapshot gating and changelog diffing"
```

---

### Task 3: Add iShares XML Parser

**Files:**
- Create: `provider/ishares_parser.go`
- Create: `provider/ishares_parser_test.go`

**Step 1: Write tests for the XML parser**

Create `provider/ishares_parser_test.go`. Use a minimal XML fixture inline:

```go
package provider

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("parseISharesXML", func() {
	sampleXML := []byte(`<?xml version="1.0"?>
<ss:Workbook xmlns:ss="urn:schemas-microsoft-com:office:spreadsheet">
<ss:Worksheet ss:Name="Disclaimers">
<ss:Table></ss:Table>
</ss:Worksheet>
<ss:Worksheet ss:Name="Holdings">
<ss:Table>
<ss:Row>
<ss:Cell ss:StyleID="Left">
<ss:Data ss:Type="String">05-Mar-2026</ss:Data>
</ss:Cell>
</ss:Row>
<ss:Row>
<ss:Cell ss:StyleID="Left">
<ss:Data ss:Type="String">iShares Russell 1000 Value ETF</ss:Data>
</ss:Cell>
</ss:Row>
<ss:Row>
<ss:Cell><ss:Data ss:Type="String">Fund Holdings as of</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">Mar 05, 2026</ss:Data></ss:Cell>
</ss:Row>
<ss:Row><ss:Cell><ss:Data ss:Type="String"></ss:Data></ss:Cell></ss:Row>
<ss:Row>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Ticker</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Name</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Sector</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Asset Class</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Market Value</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Weight (%)</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Notional Value</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Quantity</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Price</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Location</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Exchange</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Currency</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">FX Rate</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="headerstyle"><ss:Data ss:Type="String">Accrual Date</ss:Data></ss:Cell>
</ss:Row>
<ss:Row>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">AAPL</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">APPLE INC</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">Information Technology</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">Equity</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">500000000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">5.25</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">500000000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">2000000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">250.0</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">United States</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">NASDAQ</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">USD</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">1</ss:Data></ss:Cell>
</ss:Row>
<ss:Row>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">CASH</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">CASH COLLATERAL</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">-</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">Cash and/or Derivatives</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">100000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">0.01</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">100000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">100000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">1.0</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">United States</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">-</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">USD</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">1</ss:Data></ss:Cell>
</ss:Row>
<ss:Row>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">MSFT</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">MICROSOFT CORP</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">Information Technology</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">Equity</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">400000000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">4.20</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">400000000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">1000000</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">400.0</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">United States</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">NASDAQ</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Left"><ss:Data ss:Type="String">USD</ss:Data></ss:Cell>
<ss:Cell ss:StyleID="Right"><ss:Data ss:Type="Number">1</ss:Data></ss:Cell>
</ss:Row>
</ss:Table>
</ss:Worksheet>
</ss:Workbook>`)

	It("parses holdings from XML", func() {
		result, err := parseISharesXML(sampleXML)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Holdings).To(HaveLen(2)) // CASH filtered out
	})

	It("extracts the snapshot date", func() {
		result, err := parseISharesXML(sampleXML)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.SnapshotDate.Year()).To(Equal(2026))
		Expect(result.SnapshotDate.Month()).To(Equal(time.March))
		Expect(result.SnapshotDate.Day()).To(Equal(5))
	})

	It("extracts ticker and weight", func() {
		result, err := parseISharesXML(sampleXML)
		Expect(err).ToNot(HaveOccurred())

		// Find AAPL
		var aapl *iSharesHolding
		for _, h := range result.Holdings {
			if h.Ticker == "AAPL" {
				aapl = &h
				break
			}
		}
		Expect(aapl).ToNot(BeNil())
		Expect(aapl.Weight).To(BeNumerically("~", 0.0525, 0.0001)) // 5.25% as decimal
	})

	It("filters out non-equity holdings", func() {
		result, err := parseISharesXML(sampleXML)
		Expect(err).ToNot(HaveOccurred())
		for _, h := range result.Holdings {
			Expect(h.Ticker).ToNot(Equal("CASH"))
		}
	})
})
```

**Step 2: Run tests to verify they fail**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run "parseISharesXML" -v`
Expected: FAIL (function not defined)

**Step 3: Implement the parser**

Create `provider/ishares_parser.go`:

```go
package provider

import (
	"bytes"
	"encoding/xml"
	"strconv"
	"strings"
	"time"
)

type iSharesHolding struct {
	Ticker string
	Weight float64 // decimal fraction (e.g. 0.0525 for 5.25%)
}

type iSharesParseResult struct {
	SnapshotDate time.Time
	Holdings     []iSharesHolding
}

// XML structures for SpreadsheetML
type ssWorkbook struct {
	XMLName    xml.Name      `xml:"Workbook"`
	Worksheets []ssWorksheet `xml:"Worksheet"`
}

type ssWorksheet struct {
	Name  string  `xml:"Name,attr"`
	Table ssTable `xml:"Table"`
}

type ssTable struct {
	Rows []ssRow `xml:"Row"`
}

type ssRow struct {
	Cells []ssCell `xml:"Cell"`
}

type ssCell struct {
	StyleID string `xml:"StyleID,attr"`
	Data    ssData `xml:"Data"`
}

type ssData struct {
	Type  string `xml:"Type,attr"`
	Value string `xml:",chardata"`
}

func parseISharesXML(xmlData []byte) (*iSharesParseResult, error) {
	// Strip BOM if present
	xmlData = bytes.TrimPrefix(xmlData, []byte("\xef\xbb\xbf"))
	// Strip any additional BOMs (file has double BOM)
	xmlData = bytes.TrimPrefix(xmlData, []byte("\xef\xbb\xbf"))

	var workbook ssWorkbook
	if err := xml.Unmarshal(xmlData, &workbook); err != nil {
		return nil, err
	}

	result := &iSharesParseResult{}

	// Find the Holdings worksheet
	var holdingsSheet *ssWorksheet
	for i := range workbook.Worksheets {
		if workbook.Worksheets[i].Name == "Holdings" {
			holdingsSheet = &workbook.Worksheets[i]
			break
		}
	}
	if holdingsSheet == nil {
		return nil, xml.UnmarshalError("Holdings worksheet not found")
	}

	rows := holdingsSheet.Table.Rows

	// First row contains the date (format: "05-Mar-2026")
	if len(rows) > 0 && len(rows[0].Cells) > 0 {
		dateStr := strings.TrimSpace(rows[0].Cells[0].Data.Value)
		if t, err := time.Parse("02-Jan-2006", dateStr); err == nil {
			result.SnapshotDate = t
		}
	}

	// Find the header row (contains "Ticker" in first cell with headerstyle)
	headerIdx := -1
	for i, row := range rows {
		if len(row.Cells) > 0 && row.Cells[0].StyleID == "headerstyle" &&
			row.Cells[0].Data.Value == "Ticker" {
			headerIdx = i
			break
		}
	}
	if headerIdx < 0 {
		return result, nil
	}

	// Build column index from header row
	colIdx := make(map[string]int)
	for i, cell := range rows[headerIdx].Cells {
		colIdx[cell.Data.Value] = i
	}

	tickerCol := colIdx["Ticker"]
	assetClassCol := colIdx["Asset Class"]
	weightCol := colIdx["Weight (%)"]

	// Parse data rows after header
	for _, row := range rows[headerIdx+1:] {
		if len(row.Cells) <= weightCol {
			continue
		}

		assetClass := row.Cells[assetClassCol].Data.Value
		if assetClass != "Equity" {
			continue
		}

		ticker := strings.TrimSpace(row.Cells[tickerCol].Data.Value)
		if ticker == "" {
			continue
		}

		weightPct, err := strconv.ParseFloat(row.Cells[weightCol].Data.Value, 64)
		if err != nil {
			continue
		}

		result.Holdings = append(result.Holdings, iSharesHolding{
			Ticker: ticker,
			Weight: weightPct / 100.0, // convert percentage to decimal
		})
	}

	return result, nil
}
```

**Step 4: Run tests to verify they pass**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run "parseISharesXML" -v`
Expected: PASS

**Step 5: Commit**

```bash
git add provider/ishares_parser.go provider/ishares_parser_test.go
git commit -m "feat: add iShares XML parser for holdings data"
```

---

### Task 4: Add iShares Provider

**Files:**
- Create: `provider/ishares.go`
- Modify: `provider/discover.go` (add to registry)

**Step 1: Create the iShares provider**

Create `provider/ishares.go`:

```go
package provider

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/playwright_helpers"
	"github.com/playwright-community/playwright-go"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
)

// iSharesETF holds the metadata needed to construct a download URL for an iShares ETF.
type iSharesETF struct {
	ProductID string // numeric product ID in the URL
	Slug      string // URL slug (e.g., "ishares-russell-1000-value-etf")
	IndexName string // name used in IndexSnapshot (e.g., "russell-1000-value")
}

// iSharesETFMap maps ETF tickers to their iShares metadata.
// Product IDs and slugs sourced from iShares website.
var iSharesETFMap = map[string]iSharesETF{
	"IVV":  {ProductID: "239726", Slug: "ishares-core-s-p-500-etf", IndexName: "sp500"},
	"IWB":  {ProductID: "239707", Slug: "ishares-russell-1000-etf", IndexName: "russell-1000"},
	"IWD":  {ProductID: "239708", Slug: "ishares-russell-1000-value-etf", IndexName: "russell-1000-value"},
	"IWF":  {ProductID: "239706", Slug: "ishares-russell-1000-growth-etf", IndexName: "russell-1000-growth"},
	"IWM":  {ProductID: "239710", Slug: "ishares-russell-2000-etf", IndexName: "russell-2000"},
	"IJH":  {ProductID: "239763", Slug: "ishares-core-s-p-mid-cap-etf", IndexName: "sp-mid-cap-400"},
	"IJR":  {ProductID: "239774", Slug: "ishares-core-s-p-small-cap-etf", IndexName: "sp-small-cap-600"},
	"IXUS": {ProductID: "244048", Slug: "ishares-core-msci-total-international-stock-etf", IndexName: "msci-total-intl"},
	"IEFA": {ProductID: "244049", Slug: "ishares-core-msci-eafe-etf", IndexName: "msci-eafe"},
	"IEMG": {ProductID: "244050", Slug: "ishares-core-msci-emerging-markets-etf", IndexName: "msci-emerging"},
	"IVW":  {ProductID: "239725", Slug: "ishares-s-p-500-growth-etf", IndexName: "sp500-growth"},
	"IVE":  {ProductID: "239728", Slug: "ishares-s-p-500-value-etf", IndexName: "sp500-value"},
	"ITOT": {ProductID: "239724", Slug: "ishares-core-s-p-total-u-s-stock-market-etf", IndexName: "sp-total-us"},
	"IWV":  {ProductID: "239714", Slug: "ishares-russell-3000-etf", IndexName: "russell-3000"},
	"IWR":  {ProductID: "239718", Slug: "ishares-russell-mid-cap-etf", IndexName: "russell-mid-cap"},
	"IWS":  {ProductID: "239719", Slug: "ishares-russell-mid-cap-value-etf", IndexName: "russell-mid-cap-value"},
	"IWP":  {ProductID: "239717", Slug: "ishares-russell-mid-cap-growth-etf", IndexName: "russell-mid-cap-growth"},
	"IWO":  {ProductID: "239709", Slug: "ishares-russell-2000-growth-etf", IndexName: "russell-2000-growth"},
	"IWN":  {ProductID: "239711", Slug: "ishares-russell-2000-value-etf", IndexName: "russell-2000-value"},
}

type IShares struct{}

func (ishares *IShares) Name() string {
	return "iShares"
}

func (ishares *IShares) ConfigDescription() map[string]string {
	return map[string]string{
		"tickers":           "Enter ETF tickers to scrape (comma-separated, e.g. IVV,IWD,IWM):",
		"snapshotFrequency": "How often to take full snapshots (daily, weekly, monthly, quarterly):",
	}
}

func (ishares *IShares) Description() string {
	return "iShares ETF index holdings from BlackRock. Scrapes constituent data including weights."
}

func (ishares *IShares) Datasets() map[string]Dataset {
	return map[string]Dataset{
		"Index Holdings": {
			Name:        "Index Holdings",
			Description: "Download index constituent holdings and weights from iShares ETFs.",
			DataTypes:   []*data.DataType{data.DataTypes[data.IndexKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: downloadISharesHoldings,
		},
	}
}

func downloadISharesHoldings(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
	logger := zerolog.Ctx(ctx)

	runSummary := data.RunSummary{
		StartTime:        time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}

	numObs := 0

	defer func() {
		runSummary.EndTime = time.Now()
		runSummary.NumObservations = numObs
		exitNotification <- runSummary
	}()

	tickers := strings.Split(subscription.Config["tickers"], ",")
	snapshotFrequency := subscription.Config["snapshotFrequency"]
	if snapshotFrequency == "" {
		snapshotFrequency = "weekly"
	}

	// Load assets for FIGI lookup
	conn, err := subscription.Library.Pool.Acquire(ctx)
	if err != nil {
		logger.Error().Err(err).Msg("could not acquire db connection")
		return
	}
	assets, err := data.ActiveAssets(ctx, conn)
	conn.Release()
	if err != nil {
		logger.Error().Err(err).Msg("could not load active assets")
		return
	}

	figiMap := make(map[string]string, len(assets))
	for _, asset := range assets {
		figiMap[asset.Ticker] = asset.CompositeFigi
	}

	// Start Playwright
	page, pwContext, browser, pw := playwright_helpers.StartPlaywright(viper.GetBool("playwright.headless"))
	defer playwright_helpers.StopPlaywright(page, pwContext, browser, pw)

	indexTable := subscription.DataTablesMap[data.IndexKey]

	for _, ticker := range tickers {
		ticker = strings.TrimSpace(ticker)
		etf, ok := iSharesETFMap[ticker]
		if !ok {
			logger.Warn().Str("Ticker", ticker).Msg("unknown iShares ETF ticker, skipping")
			continue
		}

		n := downloadSingleISharesETF(ctx, page, etf, figiMap, snapshotFrequency, indexTable, subscription, out)
		numObs += n
	}
}

func downloadSingleISharesETF(
	ctx context.Context,
	page playwright.Page,
	etf iSharesETF,
	figiMap map[string]string,
	snapshotFrequency string,
	indexTable string,
	subscription *library.Subscription,
	out chan<- *data.Observation,
) int {
	logger := zerolog.Ctx(ctx)
	numObs := 0

	// Navigate to the product page
	productURL := "https://www.ishares.com/us/products/" + etf.ProductID + "/" + etf.Slug
	logger.Info().Str("URL", productURL).Str("Index", etf.IndexName).Msg("downloading iShares holdings")

	_, err := page.Goto(productURL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateNetworkidle,
		Timeout:   playwright.Float(30000),
	})
	if err != nil {
		logger.Error().Err(err).Str("URL", productURL).Msg("could not navigate to iShares product page")
		return 0
	}

	// Click the download button and capture the file
	download, err := page.ExpectDownload(func() error {
		// Look for the export/download link for holdings
		downloadLink := page.Locator("a[href*='.ajax'][href*='fileType=xls']").First()
		return downloadLink.Click()
	})
	if err != nil {
		logger.Error().Err(err).Str("Index", etf.IndexName).Msg("could not download iShares XLS file")
		return 0
	}

	downloadPath, err := download.Path()
	if err != nil {
		logger.Error().Err(err).Msg("could not get download path")
		return 0
	}

	xlsData, err := os.ReadFile(downloadPath)
	if err != nil {
		logger.Error().Err(err).Msg("could not read downloaded XLS file")
		return 0
	}

	// Parse the XML
	parsed, err := parseISharesXML(xlsData)
	if err != nil {
		logger.Error().Err(err).Str("Index", etf.IndexName).Msg("could not parse iShares XML")
		return 0
	}

	today := time.Now().UTC().Truncate(24 * time.Hour)
	if !parsed.SnapshotDate.IsZero() {
		today = parsed.SnapshotDate
	}

	// Build current holdings map (ticker -> figi)
	currentHoldings := make(map[string]string, len(parsed.Holdings))
	for _, h := range parsed.Holdings {
		if figi, ok := figiMap[h.Ticker]; ok {
			currentHoldings[h.Ticker] = figi
		}
	}

	// Get previous snapshot for diffing
	previous := previousSnapshotTickers(ctx, subscription.Library.Pool, indexTable, etf.IndexName)

	// Emit changelog
	adds, removes := diffSnapshots(currentHoldings, previous)
	obsTemplate := &data.Observation{
		ObservationDate:  time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}
	emitChangelog(adds, removes, etf.IndexName, today, obsTemplate, out)
	numObs += len(adds) + len(removes)

	// Check if we should take a snapshot
	lastDate := lastSnapshotDate(ctx, subscription.Library.Pool, indexTable, etf.IndexName)
	if shouldTakeSnapshot(lastDate, snapshotFrequency) {
		for _, h := range parsed.Holdings {
			figi, ok := figiMap[h.Ticker]
			if !ok {
				continue
			}

			out <- &data.Observation{
				IndexSnapshot: &data.IndexSnapshot{
					Ticker:        h.Ticker,
					CompositeFigi: figi,
					IndexName:     etf.IndexName,
					SnapshotDate:  today,
					Weight:        h.Weight,
				},
				ObservationDate:  time.Now(),
				SubscriptionID:   subscription.ID,
				SubscriptionName: subscription.Name,
			}
			numObs++
		}
	}

	return numObs
}
```

**Step 2: Register the provider**

In `provider/discover.go`, add `"ishares": &IShares{}`:

```go
var Map = map[string]Provider{
	"fred":     &Fred{},
	"ishares":  &IShares{},
	"massive":  &Massive{},
	"polygon":  &Massive{},
	"sharadar": &Sharadar{},
	"tiingo":   &Tiingo{},
	"zacks":    &Zacks{},
}
```

**Step 3: Verify the project compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`
Expected: No errors

**Step 4: Commit**

```bash
git add provider/ishares.go provider/discover.go
git commit -m "feat: add iShares provider for index holdings scraping"
```

---

### Task 5: Add Nasdaq Provider

**Files:**
- Create: `provider/nasdaq.go`
- Modify: `provider/discover.go` (add to registry)

**Step 1: Create the Nasdaq provider**

Create `provider/nasdaq.go`:

```go
package provider

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/playwright_helpers"
	"github.com/rs/zerolog"
	"github.com/spf13/viper"
)

const (
	NASDAQ_NDX_URL = "https://www.nasdaq.com/market-activity/quotes/nasdaq-ndx-index"
)

type Nasdaq struct{}

func (nasdaq *Nasdaq) Name() string {
	return "Nasdaq"
}

func (nasdaq *Nasdaq) ConfigDescription() map[string]string {
	return map[string]string{
		"snapshotFrequency": "How often to take full snapshots (daily, weekly, monthly, quarterly):",
	}
}

func (nasdaq *Nasdaq) Description() string {
	return "Nasdaq NDX-100 index constituents scraped from nasdaq.com."
}

func (nasdaq *Nasdaq) Datasets() map[string]Dataset {
	return map[string]Dataset{
		"Index Holdings": {
			Name:        "Index Holdings",
			Description: "Download Nasdaq 100 index constituents.",
			DataTypes:   []*data.DataType{data.DataTypes[data.IndexKey]},
			DateRange: func() (time.Time, time.Time) {
				return time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC), time.Now().UTC()
			},
			Fetch: downloadNasdaqHoldings,
		},
	}
}

type nasdaqHolding struct {
	Ticker string
	Weight float64
}

func downloadNasdaqHoldings(ctx context.Context, subscription *library.Subscription, out chan<- *data.Observation, exitNotification chan<- data.RunSummary) {
	logger := zerolog.Ctx(ctx)

	runSummary := data.RunSummary{
		StartTime:        time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}

	numObs := 0

	defer func() {
		runSummary.EndTime = time.Now()
		runSummary.NumObservations = numObs
		exitNotification <- runSummary
	}()

	snapshotFrequency := subscription.Config["snapshotFrequency"]
	if snapshotFrequency == "" {
		snapshotFrequency = "weekly"
	}

	// Load assets for FIGI lookup
	conn, err := subscription.Library.Pool.Acquire(ctx)
	if err != nil {
		logger.Error().Err(err).Msg("could not acquire db connection")
		return
	}
	assets, err := data.ActiveAssets(ctx, conn)
	conn.Release()
	if err != nil {
		logger.Error().Err(err).Msg("could not load active assets")
		return
	}

	figiMap := make(map[string]string, len(assets))
	for _, asset := range assets {
		figiMap[asset.Ticker] = asset.CompositeFigi
	}

	// Start Playwright
	page, pwContext, browser, pw := playwright_helpers.StartPlaywright(viper.GetBool("playwright.headless"))
	defer playwright_helpers.StopPlaywright(page, pwContext, browser, pw)

	logger.Info().Msg("downloading Nasdaq NDX-100 constituents")

	_, err = page.Goto(NASDAQ_NDX_URL, playwright.PageGotoOptions{
		WaitUntil: playwright.WaitUntilStateNetworkidle,
		Timeout:   playwright.Float(30000),
	})
	if err != nil {
		logger.Error().Err(err).Msg("could not navigate to Nasdaq NDX page")
		return
	}

	// Wait for the constituents table to load
	table := page.Locator("table.nasdaq-screener__table, table[data-testid='index-table']").First()
	err = table.WaitFor(playwright.LocatorWaitForOptions{
		Timeout: playwright.Float(15000),
	})
	if err != nil {
		logger.Error().Err(err).Msg("constituents table not found")
		return
	}

	// Extract rows from the table
	rows := table.Locator("tbody tr")
	count, err := rows.Count()
	if err != nil {
		logger.Error().Err(err).Msg("could not count table rows")
		return
	}

	holdings := make([]nasdaqHolding, 0, count)
	for i := 0; i < count; i++ {
		row := rows.Nth(i)
		cells := row.Locator("td")

		cellCount, _ := cells.Count()
		if cellCount < 2 {
			continue
		}

		ticker, _ := cells.Nth(0).InnerText()
		ticker = strings.TrimSpace(ticker)
		if ticker == "" {
			continue
		}

		var weight float64
		// Weight column may vary; try to find it
		if cellCount >= 4 {
			weightStr, _ := cells.Nth(3).InnerText()
			weightStr = strings.TrimSpace(strings.ReplaceAll(weightStr, "%", ""))
			if w, err := strconv.ParseFloat(weightStr, 64); err == nil {
				weight = w / 100.0
			}
		}

		holdings = append(holdings, nasdaqHolding{
			Ticker: ticker,
			Weight: weight,
		})
	}

	logger.Info().Int("Count", len(holdings)).Msg("parsed Nasdaq NDX-100 constituents")

	today := time.Now().UTC().Truncate(24 * time.Hour)
	indexName := "ndx100"
	indexTable := subscription.DataTablesMap[data.IndexKey]

	// Build current holdings map
	currentHoldings := make(map[string]string, len(holdings))
	for _, h := range holdings {
		if figi, ok := figiMap[h.Ticker]; ok {
			currentHoldings[h.Ticker] = figi
		}
	}

	// Diff against previous snapshot
	previous := previousSnapshotTickers(ctx, subscription.Library.Pool, indexTable, indexName)
	adds, removes := diffSnapshots(currentHoldings, previous)

	obsTemplate := &data.Observation{
		ObservationDate:  time.Now(),
		SubscriptionID:   subscription.ID,
		SubscriptionName: subscription.Name,
	}
	emitChangelog(adds, removes, indexName, today, obsTemplate, out)
	numObs += len(adds) + len(removes)

	// Check if we should take a snapshot
	lastDate := lastSnapshotDate(ctx, subscription.Library.Pool, indexTable, indexName)
	if shouldTakeSnapshot(lastDate, snapshotFrequency) {
		for _, h := range holdings {
			figi, ok := figiMap[h.Ticker]
			if !ok {
				continue
			}

			out <- &data.Observation{
				IndexSnapshot: &data.IndexSnapshot{
					Ticker:        h.Ticker,
					CompositeFigi: figi,
					IndexName:     indexName,
					SnapshotDate:  today,
					Weight:        h.Weight,
				},
				ObservationDate:  time.Now(),
				SubscriptionID:   subscription.ID,
				SubscriptionName: subscription.Name,
			}
			numObs++
		}
	}
}
```

Note: The Nasdaq page scraping uses Playwright locators that will need verification against the actual page structure at implementation time. The table selector and column layout may need adjustment.

**Step 2: Register the provider**

In `provider/discover.go`, add `"nasdaq": &Nasdaq{}`:

```go
var Map = map[string]Provider{
	"fred":     &Fred{},
	"ishares":  &IShares{},
	"massive":  &Massive{},
	"nasdaq":   &Nasdaq{},
	"polygon":  &Massive{},
	"sharadar": &Sharadar{},
	"tiingo":   &Tiingo{},
	"zacks":    &Zacks{},
}
```

**Step 3: Verify the project compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`
Expected: No errors

**Step 4: Commit**

```bash
git add provider/nasdaq.go provider/discover.go
git commit -m "feat: add Nasdaq provider for NDX-100 index holdings scraping"
```

---

### Task 6: Manual Integration Test

**Step 1: Test the iShares XML parser against the real file**

Write a quick test or use `go run` to parse the real downloaded file at `~/Downloads/iShares-Russell-1000-Value-ETF_fund.xls` and print the number of holdings and first few tickers. Verify the parser handles the real file correctly (double BOM, full set of rows, etc.).

**Step 2: Test iShares Playwright download**

Run the subscription in non-headless mode to verify:
- Navigation to the iShares product page works
- The download link locator finds the right element
- The XLS file downloads successfully
- Parsing produces the expected holdings

Adjust the Playwright locator in `downloadSingleISharesETF` if the download button selector needs refinement.

**Step 3: Test Nasdaq Playwright scraping**

Run the Nasdaq subscription in non-headless mode to verify:
- The NDX page loads and the table renders
- The table locator finds the constituents table
- Ticker and weight extraction works correctly
- Adjust selectors as needed based on actual page structure

**Step 4: Commit any fixes**

```bash
git add -u
git commit -m "fix: adjust Playwright locators for iShares and Nasdaq scraping"
```
