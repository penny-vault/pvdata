# File Import Command Implementation Plan

> **For agentic workers:** REQUIRED: Use superpowers:subagent-driven-development (if subagents available) or superpowers:executing-plans to implement this plan. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a `pvdata import` CLI command that imports historical Sharadar data from local parquet/CSV files through the existing observation pipeline.

**Architecture:** A new `FileImporter` interface on the Provider level, implemented by Sharadar. A new `pvdata import` CLI command orchestrates the pipeline identically to `runSubscription` in `cmd/run.go`. File reading uses the existing `xitongsys/parquet-go` and `klauspost/compress` dependencies. Parsing reuses existing intermediate structs and conversion methods.

**Tech Stack:** Go, xitongsys/parquet-go, klauspost/compress/zstd, archive/zip, encoding/csv, Cobra CLI, Ginkgo/Gomega tests.

**Spec:** `docs/superpowers/specs/2026-03-15-file-import-design.md`

---

## File Structure

| File | Responsibility |
|------|---------------|
| `provider/provider.go` | Add `FileImporter` interface (3 lines) |
| `provider/sharadar_import.go` | Sharadar `ImportFiles` method, file format readers, per-dataset row mappers |
| `provider/sharadar_import_test.go` | Unit tests for file parsing, dataset detection, row mapping |
| `cmd/import.go` | CLI command: subscription lookup, validation, orchestration |
| `cmd/disable.go` | `pvdata disable` command (mirrors existing `cmd/enable.go`) |

**Existing files reused unchanged:**
- `provider/sharadar_fundamentals.go` -- `sharadarFundamental` struct, `PvFundamental()` method
- `provider/sharadar_metrics.go` -- `sharadarMetric` struct, `PvMetric()` method
- `provider/sharadar_tickers.go` -- `sharadarTicker` struct, `ToAsset()`, `figi.Enrich()`
- `library/database.go` -- `SaveObservations()`, `SubscriptionFromID()`
- `cmd/run.go` -- reference for orchestration pattern

---

## Chunk 1: Foundation (Interface + Disable Command + Dataset Detection)

### Task 1: Add FileImporter Interface

**Files:**
- Modify: `provider/provider.go:44-63`

- [ ] **Step 1: Add the FileImporter interface to provider.go**

Add after the existing `Provider` interface (after line 49):

```go
// FileImporter is an optional interface that providers can implement to support
// importing data from local files (parquet, CSV, etc.) instead of fetching from APIs.
type FileImporter interface {
	ImportFiles(ctx context.Context, sub *library.Subscription,
		files []string, out chan<- *data.Observation, exit chan<- data.RunSummary)
}
```

- [ ] **Step 2: Verify the project compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`
Expected: success (no code depends on the interface yet)

- [ ] **Step 3: Commit**

```bash
git add provider/provider.go
git commit -m "feat: add FileImporter interface to provider package"
```

---

### Task 2: Add pvdata disable Command

**Files:**
- Create: `cmd/disable.go`
- Reference: `cmd/enable.go` (mirror its structure)

- [ ] **Step 1: Create cmd/disable.go**

```go
/*
Copyright 2024
*/
package cmd

import (
	"context"

	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

// disableCmd represents the disable command
var disableCmd = &cobra.Command{
	Use:   "disable <subscription-id>",
	Short: "Disable active subscriptions",
	Args:  cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not load library info")
		}

		for _, id := range args {
			sub, err := myLibrary.SubscriptionFromID(ctx, id)
			if err != nil {
				log.Fatal().Err(err).Str("ID", id).Msg("could not get subscription for ID")
			}

			if err := sub.Deactivate(ctx); err != nil {
				log.Fatal().Err(err).Msg("could not deactivate subscription")
			}

			log.Info().Str("ID", id).Msg("subscription disabled")
		}
	},
}

func init() {
	rootCmd.AddCommand(disableCmd)
}
```

- [ ] **Step 2: Verify the project compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`
Expected: success

- [ ] **Step 3: Commit**

```bash
git add cmd/disable.go
git commit -m "feat: add pvdata disable command to deactivate subscriptions"
```

---

### Task 3: Dataset Detection and File Format Helpers

**Files:**
- Create: `provider/sharadar_import.go`
- Create: `provider/sharadar_import_test.go`

- [ ] **Step 1: Write failing tests for dataset detection from filename**

Create `provider/sharadar_import_test.go`. Note: do NOT add a `TestXxx` runner function here -- the existing `TestIndexHelpers` in `provider/index_helpers_test.go` already calls `RunSpecs` which will discover all `Describe` blocks in the package.

```go
package provider

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("DetectSharadarDataset", func() {
	It("detects SF1 fundamentals from lowercase filename", func() {
		Expect(DetectSharadarDataset("sharadar_sf1_20231226.parquet")).To(Equal("Fundamentals"))
	})

	It("detects SF1 fundamentals from uppercase filename", func() {
		Expect(DetectSharadarDataset("SHARADAR_SF1_2_1ef265.csv.zst")).To(Equal("Fundamentals"))
	})

	It("detects metrics from metrics filename", func() {
		Expect(DetectSharadarDataset("sharadar_metrics_20231228.parquet")).To(Equal("Metrics"))
	})

	It("detects metrics from daily filename", func() {
		Expect(DetectSharadarDataset("sharadar_daily_20231228.parquet")).To(Equal("Metrics"))
	})

	It("detects tickers from tickers filename", func() {
		Expect(DetectSharadarDataset("sharadar_tickers.parquet")).To(Equal("Stock Tickers"))
	})

	It("detects tickers from uppercase filename", func() {
		Expect(DetectSharadarDataset("SHARADAR_TICKERS_2_7ef16c.csv.zst")).To(Equal("Stock Tickers"))
	})

	It("returns error for unrecognized filename", func() {
		_, err := DetectSharadarDataset("sharadar_sep_20231228.parquet")
		Expect(err).To(HaveOccurred())
		Expect(err.Error()).To(ContainSubstring("cannot determine dataset"))
	})

	It("handles full path by using only base filename", func() {
		Expect(DetectSharadarDataset("/some/path/to/sharadar_sf1_20231226.parquet")).To(Equal("Fundamentals"))
	})
})

var _ = Describe("detectFileFormat", func() {
	It("detects parquet format", func() {
		Expect(detectFileFormat("data.parquet")).To(Equal(fileFormatParquet))
	})

	It("detects csv format", func() {
		Expect(detectFileFormat("data.csv")).To(Equal(fileFormatCSV))
	})

	It("detects zstd-compressed csv", func() {
		Expect(detectFileFormat("data.csv.zst")).To(Equal(fileFormatCSVZst))
	})

	It("detects zip-compressed csv", func() {
		Expect(detectFileFormat("data.csv.zip")).To(Equal(fileFormatCSVZip))
	})

	It("returns error for unknown extension", func() {
		_, err := detectFileFormat("data.xlsx")
		Expect(err).To(HaveOccurred())
	})
})
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run TestIndexHelpers -v`
Expected: FAIL (functions not defined)

- [ ] **Step 3: Implement dataset detection and file format helpers**

Create `provider/sharadar_import.go`:

```go
package provider

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
)

type fileFormat int

const (
	fileFormatParquet fileFormat = iota
	fileFormatCSV
	fileFormatCSVZst
	fileFormatCSVZip
)

// detectSharadarDataset determines which Sharadar dataset a file belongs to
// based on its filename. Matching is case-insensitive on the base filename.
func DetectSharadarDataset(path string) (string, error) {
	base := strings.ToLower(filepath.Base(path))

	switch {
	case strings.Contains(base, "sf1"):
		return "Fundamentals", nil
	case strings.Contains(base, "metrics") || strings.Contains(base, "daily"):
		return "Metrics", nil
	case strings.Contains(base, "tickers"):
		return "Stock Tickers", nil
	default:
		return "", fmt.Errorf("cannot determine dataset from filename %q; expected sf1, metrics, daily, or tickers in the name", filepath.Base(path))
	}
}

// detectFileFormat determines how to read a file based on its extension.
func detectFileFormat(path string) (fileFormat, error) {
	lower := strings.ToLower(path)

	switch {
	case strings.HasSuffix(lower, ".parquet"):
		return fileFormatParquet, nil
	case strings.HasSuffix(lower, ".csv.zst"):
		return fileFormatCSVZst, nil
	case strings.HasSuffix(lower, ".csv.zip"):
		return fileFormatCSVZip, nil
	case strings.HasSuffix(lower, ".csv"):
		return fileFormatCSV, nil
	default:
		return 0, fmt.Errorf("unsupported file format for %q; expected .parquet, .csv, .csv.zst, or .csv.zip", filepath.Base(path))
	}
}

// ImportFiles implements the FileImporter interface for Sharadar.
// It reads local parquet/CSV files and emits observations through the existing pipeline.
func (sharadar *Sharadar) ImportFiles(ctx context.Context, sub *library.Subscription,
	files []string, out chan<- *data.Observation, exit chan<- data.RunSummary) {
	// Placeholder -- will be implemented in Task 5
	exit <- data.RunSummary{
		Status: data.RunFailed,
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run TestIndexHelpers -v`
Expected: PASS

- [ ] **Step 5: Verify the full project compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`
Expected: success

- [ ] **Step 6: Commit**

```bash
git add provider/sharadar_import.go provider/sharadar_import_test.go
git commit -m "feat: add dataset detection and file format helpers for Sharadar import"
```

---

## Chunk 2: CSV/Parquet File Readers

### Task 4: CSV and Parquet Row Readers

**Files:**
- Modify: `provider/sharadar_import.go`
- Modify: `provider/sharadar_import_test.go`

This task adds generic file readers that return rows as `map[string]string` (column name -> value). Each dataset parser (Task 6-8) will then map these to the existing structs.

- [ ] **Step 1: Write failing test for CSV reader**

Add to `provider/sharadar_import_test.go`:

```go
var _ = Describe("readCSVRows", func() {
	It("reads a CSV file and returns rows as maps", func() {
		// Create a temporary CSV file
		tmpFile, err := os.CreateTemp("", "test-*.csv")
		Expect(err).NotTo(HaveOccurred())
		defer os.Remove(tmpFile.Name())

		_, err = tmpFile.WriteString("ticker,date,value\nAAPL,2023-01-01,100.5\nGOOG,2023-01-02,200.3\n")
		Expect(err).NotTo(HaveOccurred())
		tmpFile.Close()

		rows, err := readCSVRows(tmpFile.Name())
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(2))
		Expect(rows[0]["ticker"]).To(Equal("AAPL"))
		Expect(rows[0]["date"]).To(Equal("2023-01-01"))
		Expect(rows[0]["value"]).To(Equal("100.5"))
		Expect(rows[1]["ticker"]).To(Equal("GOOG"))
	})
})
```

Add `"os"` to the imports.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run "TestIndexHelpers/readCSVRows" -v`
Expected: FAIL

- [ ] **Step 3: Implement CSV reader**

Add to `provider/sharadar_import.go`:

```go
import (
	"archive/zip"
	"encoding/csv"
	"io"
	"os"

	"github.com/klauspost/compress/zstd"
)

// readCSVRows reads a plain CSV file and returns rows as maps of column name -> value.
func readCSVRows(path string) ([]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	return parseCSV(f)
}

// readCSVZstRows reads a zstd-compressed CSV file.
func readCSVZstRows(path string) ([]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	reader, err := zstd.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("zstd reader for %s: %w", path, err)
	}
	defer reader.Close()

	return parseCSV(reader)
}

// readCSVZipRows reads the first CSV file from a zip archive.
func readCSVZipRows(path string) ([]map[string]string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open zip %s: %w", path, err)
	}
	defer r.Close()

	for _, f := range r.File {
		if strings.HasSuffix(strings.ToLower(f.Name), ".csv") {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("open %s in zip: %w", f.Name, err)
			}
			defer rc.Close()

			return parseCSV(rc)
		}
	}

	return nil, fmt.Errorf("no CSV file found in zip archive %s", path)
}

// parseCSV reads CSV from a reader, using the first row as column headers.
// Returns rows as maps of lowercase column name -> value.
func parseCSV(r io.Reader) ([]map[string]string, error) {
	csvReader := csv.NewReader(r)
	csvReader.LazyQuotes = true

	header, err := csvReader.Read()
	if err != nil {
		return nil, fmt.Errorf("read CSV header: %w", err)
	}

	// Normalize header to lowercase
	for i, h := range header {
		header[i] = strings.ToLower(strings.TrimSpace(h))
	}

	var rows []map[string]string

	for {
		record, err := csvReader.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read CSV row: %w", err)
		}

		row := make(map[string]string, len(header))
		for i, val := range record {
			if i < len(header) {
				row[header[i]] = val
			}
		}

		rows = append(rows, row)
	}

	return rows, nil
}
```

- [ ] **Step 4: Run CSV test to verify it passes**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run "TestIndexHelpers/readCSVRows" -v`
Expected: PASS

- [ ] **Step 5: Write failing test for parquet reader**

Add to `provider/sharadar_import_test.go`:

```go
var _ = Describe("readParquetRows", func() {
	It("reads a real sharadar metrics parquet file", func() {
		// Use actual test data if available; skip if not
		path := "../data/sharadar/sharadar_metrics_20231228.parquet"
		if _, err := os.Stat(path); os.IsNotExist(err) {
			Skip("test parquet file not available")
		}

		rows, err := readParquetRows(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(len(rows)).To(BeNumerically(">", 0))

		// Check that expected columns exist
		first := rows[0]
		Expect(first).To(HaveKey("ticker"))
		Expect(first).To(HaveKey("date"))
	})
})
```

- [ ] **Step 6: Implement parquet reader**

Add to `provider/sharadar_import.go`:

```go
import (
	"reflect"

	"github.com/xitongsys/parquet-go-source/local"
	"github.com/xitongsys/parquet-go/reader"
)

// readParquetRows reads a parquet file and returns rows as maps of column name -> string value.
// Note: xitongsys/parquet-go's ReadByNumber with nil schema returns dynamically-constructed
// structs (via reflect.StructOf), NOT maps. We use reflection to extract field names and values.
func readParquetRows(path string) ([]map[string]string, error) {
	fr, err := local.NewLocalFileReader(path)
	if err != nil {
		return nil, fmt.Errorf("open parquet %s: %w", path, err)
	}
	defer fr.Close()

	pr, err := reader.NewParquetReader(fr, nil, 4)
	if err != nil {
		return nil, fmt.Errorf("create parquet reader for %s: %w", path, err)
	}
	defer pr.ReadStop()

	numRows := int(pr.GetNumRows())
	rows := make([]map[string]string, 0, numRows)

	batchSize := 10000
	for numRows > 0 {
		readCount := batchSize
		if readCount > numRows {
			readCount = numRows
		}

		batch, err := pr.ReadByNumber(readCount)
		if err != nil {
			return nil, fmt.Errorf("read parquet rows: %w", err)
		}

		for _, rawRow := range batch {
			val := reflect.ValueOf(rawRow)
			if val.Kind() == reflect.Ptr {
				val = val.Elem()
			}
			if val.Kind() != reflect.Struct {
				continue
			}

			typ := val.Type()
			row := make(map[string]string, typ.NumField())
			for j := 0; j < typ.NumField(); j++ {
				name := strings.ToLower(typ.Field(j).Name)
				row[name] = fmt.Sprintf("%v", val.Field(j).Interface())
			}

			rows = append(rows, row)
		}

		numRows -= readCount
	}

	return rows, nil
}
```

**Important:** The field names from the dynamically-constructed structs may be CamelCased or use different naming than the raw parquet column names. Test against a real Sharadar parquet file to verify the lowercase field names match expectations (e.g., that `ticker` comes through as `"ticker"` not `"Ticker"`). If names differ, adjust the `strings.ToLower` mapping or add a normalization step.

- [ ] **Step 7: Run parquet test to verify it passes**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run "TestIndexHelpers/readParquetRows" -v`
Expected: PASS (or Skip if test file not available)

- [ ] **Step 8: Add a helper to read rows by file format**

Add to `provider/sharadar_import.go`:

```go
// readFileRows reads rows from a file, dispatching to the appropriate reader
// based on file format.
func readFileRows(path string) ([]map[string]string, error) {
	format, err := detectFileFormat(path)
	if err != nil {
		return nil, err
	}

	switch format {
	case fileFormatParquet:
		return readParquetRows(path)
	case fileFormatCSV:
		return readCSVRows(path)
	case fileFormatCSVZst:
		return readCSVZstRows(path)
	case fileFormatCSVZip:
		return readCSVZipRows(path)
	default:
		return nil, fmt.Errorf("unsupported file format for %s", path)
	}
}
```

- [ ] **Step 9: Verify all tests pass**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run TestIndexHelpers -v`
Expected: PASS

- [ ] **Step 10: Commit**

```bash
git add provider/sharadar_import.go provider/sharadar_import_test.go
git commit -m "feat: add CSV and parquet file readers for Sharadar import"
```

---

## Chunk 3: Per-Dataset Row Mappers

### Task 5: Metrics Row Mapper

**Files:**
- Modify: `provider/sharadar_import.go`
- Modify: `provider/sharadar_import_test.go`

The metrics dataset is the simplest (10 columns), so start here.

- [ ] **Step 1: Write failing test for metrics row mapping**

Add to `provider/sharadar_import_test.go`:

```go
var _ = Describe("mapRowToSharadarMetric", func() {
	It("maps a row to sharadarMetric struct", func() {
		row := map[string]string{
			"ticker":      "AAPL",
			"date":        "2023-12-28",
			"lastupdated": "2024-01-25",
			"ev":          "3000000",
			"evebit":      "30.5",
			"evebitda":    "25.2",
			"marketcap":   "2500000",
			"pb":          "45.3",
			"pe":          "30.1",
			"ps":          "7.8",
		}

		metric := mapRowToSharadarMetric(row)
		Expect(metric.Ticker).To(Equal("AAPL"))
		Expect(metric.Date).To(Equal("2023-12-28"))
		Expect(metric.EV).To(BeNumerically("~", 3000000.0, 0.01))
		Expect(metric.MarketCap).To(BeNumerically("~", 2500000.0, 0.01))
		Expect(metric.PE).To(BeNumerically("~", 30.1, 0.01))
	})
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run "TestIndexHelpers/mapRowToSharadarMetric" -v`
Expected: FAIL

- [ ] **Step 3: Implement metrics row mapper**

Add to `provider/sharadar_import.go`:

```go
import "strconv"

// mapRowToSharadarMetric converts a map row to the existing sharadarMetric struct.
// Column names match the Sharadar DAILY table schema.
func mapRowToSharadarMetric(row map[string]string) *sharadarMetric {
	return &sharadarMetric{
		Ticker:      row["ticker"],
		Date:        row["date"],
		LastUpdated: row["lastupdated"],
		EV:          parseFloat(row["ev"]),
		EVtoEBIT:    parseFloat(row["evebit"]),
		EVtoEBITDA:  parseFloat(row["evebitda"]),
		MarketCap:   parseFloat(row["marketcap"]),
		PB:          parseFloat(row["pb"]),
		PE:          parseFloat(row["pe"]),
		PS:          parseFloat(row["ps"]),
	}
}

func parseFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "<nil>" || s == "None" {
		return 0
	}
	v, _ := strconv.ParseFloat(s, 64)
	return v
}

func parseInt(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "<nil>" || s == "None" {
		return 0
	}
	// Handle floats like "123.0" from parquet
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return int64(f)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run "TestIndexHelpers/mapRowToSharadarMetric" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add provider/sharadar_import.go provider/sharadar_import_test.go
git commit -m "feat: add metrics row mapper for Sharadar file import"
```

---

### Task 6: Fundamentals Row Mapper

**Files:**
- Modify: `provider/sharadar_import.go`
- Modify: `provider/sharadar_import_test.go`

- [ ] **Step 1: Write failing test for fundamentals row mapping**

Add to `provider/sharadar_import_test.go`:

```go
var _ = Describe("mapRowToSharadarFundamental", func() {
	It("maps a row to sharadarFundamental struct", func() {
		row := map[string]string{
			"ticker":       "AAPL",
			"dimension":    "ARQ",
			"calendardate": "2023-09-30",
			"datekey":      "2023-11-02",
			"reportperiod": "2023-09-30",
			"lastupdated":  "2023-12-28",
			"accoci":       "-11109000000",
			"assets":       "352583000000",
			"assetsavg":    "346747000000",
			"assetsc":      "143566000000",
			"assetsnc":     "209017000000",
			"assetturnover": "1.09",
			"bvps":         "4.41",
			"revenue":      "89498000000",
			"revenueusd":   "89498000000",
			"roa":          "0.075",
			"roe":          "1.72",
		}

		fundamental := mapRowToSharadarFundamental(row)
		Expect(fundamental.Ticker).To(Equal("AAPL"))
		Expect(fundamental.Dimension).To(Equal("ARQ"))
		Expect(fundamental.CalendarDate).To(Equal("2023-09-30"))
		Expect(fundamental.AccumulatedOtherComprehensiveIncome).To(Equal(int64(-11109000000)))
		Expect(fundamental.TotalAssets).To(Equal(int64(352583000000)))
		Expect(fundamental.AssetTurnover).To(BeNumerically("~", 1.09, 0.001))
		Expect(fundamental.Revenues).To(Equal(int64(89498000000)))
	})
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run "TestIndexHelpers/mapRowToSharadarFundamental" -v`
Expected: FAIL

- [ ] **Step 3: Implement fundamentals row mapper**

Add to `provider/sharadar_import.go`. The column names map to the sharadarFundamental struct fields using the Sharadar SF1 column names documented in the struct comments (e.g., `accoci` -> `AccumulatedOtherComprehensiveIncome`).

```go
// mapRowToSharadarFundamental converts a map row to the existing sharadarFundamental struct.
// Column names match the Sharadar SF1 table schema.
func mapRowToSharadarFundamental(row map[string]string) *sharadarFundamental {
	return &sharadarFundamental{
		Ticker:                                  row["ticker"],
		Dimension:                               row["dimension"],
		CalendarDate:                            row["calendardate"],
		DateKey:                                 row["datekey"],
		ReportPeriod:                            row["reportperiod"],
		LastUpdated:                             row["lastupdated"],
		AccumulatedOtherComprehensiveIncome:     parseInt(row["accoci"]),
		TotalAssets:                             parseInt(row["assets"]),
		AverageAssets:                           parseInt(row["assetsavg"]),
		CurrentAssets:                           parseInt(row["assetsc"]),
		AssetsNonCurrent:                        parseInt(row["assetsnc"]),
		AssetTurnover:                           parseFloat(row["assetturnover"]),
		BookValuePerShare:                       parseFloat(row["bvps"]),
		CapitalExpenditure:                      parseInt(row["capex"]),
		CashAndEquivalents:                      parseInt(row["cashneq"]),
		CashAndEquivalentsUSD:                   parseInt(row["cashnequsd"]),
		CostOfRevenue:                           parseInt(row["cor"]),
		ConsolidatedIncome:                      parseInt(row["consolinc"]),
		CurrentRatio:                            parseFloat(row["currentratio"]),
		DebtToEquityRatio:                       parseFloat(row["de"]),
		TotalDebt:                               parseInt(row["debt"]),
		DebtCurrent:                             parseInt(row["debtc"]),
		DebtNonCurrent:                          parseInt(row["debtnc"]),
		TotalDebtUSD:                            parseInt(row["debtusd"]),
		DeferredRevenue:                         parseInt(row["deferredrev"]),
		DepreciationAmortizationAndAccretion:    parseInt(row["depamor"]),
		Deposits:                                parseInt(row["deposits"]),
		DividendYield:                           parseFloat(row["divyield"]),
		DividendsPerBasicCommonShare:            parseFloat(row["dps"]),
		EBIT:                                    parseInt(row["ebit"]),
		EBITDA:                                  parseInt(row["ebitda"]),
		EBITDAMargin:                            parseFloat(row["ebitdamargin"]),
		EBITDAUSD:                               parseInt(row["ebitdausd"]),
		EBITUSD:                                 parseInt(row["ebitusd"]),
		EBT:                                     parseInt(row["ebt"]),
		EPS:                                     parseFloat(row["eps"]),
		EPSDiluted:                              parseFloat(row["epsdil"]),
		EPSUSD:                                  parseFloat(row["epsusd"]),
		Equity:                                  parseInt(row["equity"]),
		EquityAvg:                               parseInt(row["equityavg"]),
		EquityUSD:                               parseInt(row["equityusd"]),
		EnterpriseValue:                         parseInt(row["ev"]),
		EVtoEBIT:                                parseInt(row["evebit"]),
		EVtoEBITDA:                              parseFloat(row["evebitda"]),
		FreeCashFlow:                            parseInt(row["fcf"]),
		FreeCashFlowPerShare:                    parseFloat(row["fcfps"]),
		FxUSD:                                   parseFloat(row["fxusd"]),
		GrossProfit:                             parseInt(row["gp"]),
		GrossMargin:                             parseFloat(row["grossmargin"]),
		Intangibles:                             parseInt(row["intangibles"]),
		InterestExpense:                         parseInt(row["intexp"]),
		InvestedCapital:                         parseInt(row["invcap"]),
		InvestedCapitalAverage:                  parseInt(row["invcapavg"]),
		Inventory:                               parseInt(row["inventory"]),
		Investments:                             parseInt(row["investments"]),
		InvestmentsCurrent:                      parseInt(row["investmentsc"]),
		InvestmentsNonCurrent:                   parseInt(row["investmentsnc"]),
		TotalLiabilities:                        parseInt(row["liabilities"]),
		CurrentLiabilities:                      parseInt(row["liabilitiesc"]),
		LiabilitiesNonCurrent:                   parseInt(row["liabilitiesnc"]),
		MarketCapitalization:                    parseInt(row["marketcap"]),
		NetCashFlow:                             parseInt(row["ncf"]),
		NetCashFlowBusiness:                     parseInt(row["ncfbus"]),
		NetCashFlowCommon:                       parseInt(row["ncfcommon"]),
		NetCashFlowDebt:                         parseInt(row["ncfdebt"]),
		NetCashFlowDividend:                     parseInt(row["ncfdiv"]),
		NetCashFlowFromFinancing:                parseInt(row["ncff"]),
		NetCashFlowFromInvesting:                parseInt(row["ncfi"]),
		NetCashFlowInvest:                       parseInt(row["ncfinv"]),
		NetCashFlowFromOperations:               parseInt(row["ncfo"]),
		NetCashFlowFx:                           parseInt(row["ncfx"]),
		NetIncome:                               parseInt(row["netinc"]),
		NetIncomeCommonStock:                    parseInt(row["netinccmn"]),
		NetIncomeCommonStockUSD:                 parseInt(row["netinccmnusd"]),
		NetLossIncomeDiscontinuedOperations:     parseInt(row["netincdis"]),
		NetIncomeToNonControllingInterests:      parseInt(row["netincnci"]),
		ProfitMargin:                            parseFloat(row["netmargin"]),
		OperatingExpenses:                       parseInt(row["opex"]),
		OperatingIncome:                         parseInt(row["opinc"]),
		Payables:                                parseInt(row["payables"]),
		PayoutRatio:                             parseFloat(row["payoutratio"]),
		PB:                                      parseFloat(row["pb"]),
		PE:                                      parseFloat(row["pe"]),
		PE1:                                     parseFloat(row["pe1"]),
		PropertyPlantAndEquipmentNet:            parseInt(row["ppnenet"]),
		PreferredDividendsIncomeStatementImpact: parseInt(row["prefdivis"]),
		Price:                                   parseFloat(row["price"]),
		PS:                                      parseFloat(row["ps"]),
		PS1:                                     parseFloat(row["ps1"]),
		Receivables:                             parseInt(row["receivables"]),
		AccumulatedRetainedEarningsDeficit:      parseInt(row["retearn"]),
		Revenues:                                parseInt(row["revenue"]),
		RevenuesUSD:                             parseInt(row["revenueusd"]),
		RandDExpenses:                           parseInt(row["rnd"]),
		ROA:                                     parseFloat(row["roa"]),
		ROE:                                     parseFloat(row["roe"]),
		ROIC:                                    parseFloat(row["roic"]),
		ReturnOnSales:                           parseFloat(row["ros"]),
		ShareBasedCompensation:                  parseInt(row["sbcomp"]),
		SellingGeneralAndAdministrativeExpense:  parseInt(row["sgna"]),
		ShareFactor:                             parseFloat(row["sharefactor"]),
		SharesBasic:                             parseInt(row["sharesbas"]),
		WeightedAverageShares:                   parseInt(row["shareswa"]),
		WeightedAverageSharesDiluted:            parseInt(row["shareswadil"]),
		SalesPerShare:                           parseFloat(row["sps"]),
		TangibleAssetValue:                      parseInt(row["tangibles"]),
		TaxAssets:                               parseInt(row["taxassets"]),
		IncomeTaxExpense:                        parseInt(row["taxexp"]),
		TaxLiabilities:                          parseInt(row["taxliabilities"]),
		TangibleAssetsBookValuePerShare:         parseFloat(row["tbvps"]),
		WorkingCapital:                          parseInt(row["workingcapital"]),
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run "TestIndexHelpers/mapRowToSharadarFundamental" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add provider/sharadar_import.go provider/sharadar_import_test.go
git commit -m "feat: add fundamentals row mapper for Sharadar file import"
```

---

### Task 7: Tickers Row Mapper

**Files:**
- Modify: `provider/sharadar_import.go`
- Modify: `provider/sharadar_import_test.go`

- [ ] **Step 1: Write failing test for tickers row mapping**

Add to `provider/sharadar_import_test.go`:

```go
var _ = Describe("mapRowToSharadarTicker", func() {
	It("maps a row to sharadarTicker struct", func() {
		row := map[string]string{
			"table":          "SF1",
			"permaticker":    "196290",
			"ticker":         "A",
			"name":           "AGILENT TECHNOLOGIES INC",
			"exchange":       "NYSE",
			"isdelisted":     "N",
			"category":       "Domestic Common Stock",
			"cusips":         "00846U101",
			"siccode":        "3826",
			"sicsector":      "Manufacturing",
			"sicindustry":    "Laboratory Analytical Instruments",
			"famasector":     "",
			"famaindustry":   "Measuring and Control Equipment",
			"sector":         "Healthcare",
			"industry":       "Diagnostics & Research",
			"scalemarketcap": "5 - Large",
			"scalerevenue":   "5 - Large",
			"relatedtickers": "",
			"currency":       "USD",
			"location":       "California; U.S.A",
			"lastupdated":    "2023-12-20",
			"firstadded":     "2014-09-26",
			"firstpricedate": "1999-11-18",
			"lastpricedate":  "2023-12-28",
			"firstquarter":   "1997-06-30",
			"lastquarter":    "2023-09-30",
			"secfilings":     "https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&CIK=0001047469",
			"companysite":    "https://www.agilent.com",
		}

		ticker := mapRowToSharadarTicker(row)
		Expect(ticker.Ticker).To(Equal("A"))
		Expect(ticker.Name).To(Equal("AGILENT TECHNOLOGIES INC"))
		Expect(ticker.Exchange).To(Equal("NYSE"))
		Expect(ticker.PermaTicker).To(Equal(int64(196290)))
		Expect(ticker.SICCode).To(Equal(int64(3826)))
		Expect(ticker.IsDelisted).To(Equal("N"))
		Expect(ticker.LastUpdated.Year()).To(Equal(2023))
		Expect(ticker.CompanySite).To(Equal("https://www.agilent.com"))
	})
})
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run "TestIndexHelpers/mapRowToSharadarTicker" -v`
Expected: FAIL

- [ ] **Step 3: Implement tickers row mapper**

Add to `provider/sharadar_import.go`:

```go
import "time"

// mapRowToSharadarTicker converts a map row to the existing sharadarTicker struct.
// Column names match the Sharadar TICKERS table schema.
func mapRowToSharadarTicker(row map[string]string) *sharadarTicker {
	ticker := &sharadarTicker{
		PermaTicker:    parseInt(row["permaticker"]),
		Ticker:         row["ticker"],
		Name:           row["name"],
		Exchange:       row["exchange"],
		IsDelisted:     row["isdelisted"],
		Category:       row["category"],
		CUSIPs:         row["cusips"],
		SICCode:        parseInt(row["siccode"]),
		SICSector:      row["sicsector"],
		SICIndustry:    row["sicindustry"],
		FAMASector:     row["famasector"],
		FAMAIndustry:   row["famaindustry"],
		Sector:         row["sector"],
		Industry:       row["industry"],
		ScaleMarketcap: row["scalemarketcap"],
		ScaleRevenue:   row["scalerevenue"],
		RelatedTickers: row["relatedtickers"],
		Currency:       row["currency"],
		Location:       row["location"],
		FirstQuarter:   row["firstquarter"],
		LastQuarter:    row["lastquarter"],
		SECFilings:     row["secfilings"],
		CompanySite:    row["companysite"],
	}

	if s := row["lastupdated"]; s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			ticker.LastUpdated = t
		}
	}

	if s := row["firstadded"]; s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			ticker.FirstAdded = t
		}
	}

	if s := row["firstpricedate"]; s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			ticker.FirstPriceDate = t
		}
	}

	if s := row["lastpricedate"]; s != "" {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			ticker.LastPriceDate = t
		}
	}

	return ticker
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run "TestIndexHelpers/mapRowToSharadarTicker" -v`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add provider/sharadar_import.go provider/sharadar_import_test.go
git commit -m "feat: add tickers row mapper for Sharadar file import"
```

---

## Chunk 4: ImportFiles Implementation + CLI Command

### Task 8: Implement Sharadar ImportFiles Method

**Files:**
- Modify: `provider/sharadar_import.go`

Replace the placeholder `ImportFiles` method with the full implementation that dispatches to per-dataset import functions.

- [ ] **Step 1: Implement the per-dataset import functions and the ImportFiles dispatcher**

Replace the placeholder `ImportFiles` in `provider/sharadar_import.go`:

```go
import (
	"github.com/penny-vault/pvdata/figi"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// ImportFiles implements the FileImporter interface for Sharadar.
func (sharadar *Sharadar) ImportFiles(ctx context.Context, sub *library.Subscription,
	files []string, out chan<- *data.Observation, exit chan<- data.RunSummary) {
	logger := zerolog.Ctx(ctx)

	runSummary := data.RunSummary{
		StartTime:        time.Now(),
		SubscriptionID:   sub.ID,
		SubscriptionName: sub.Name,
	}

	numObs := 0

	defer func() {
		runSummary.EndTime = time.Now()
		runSummary.NumObservations = numObs
		if runSummary.Status != data.RunFailed {
			runSummary.Status = data.RunSuccess
		}
		exit <- runSummary
	}()

	for _, filePath := range files {
		logger.Info().Str("file", filePath).Msg("reading file")

		rows, err := readFileRows(filePath)
		if err != nil {
			logger.Error().Err(err).Str("file", filePath).Msg("failed to read file")
			runSummary.Status = data.RunFailed
			return
		}

		logger.Info().Int("rows", len(rows)).Str("file", filePath).Msg("file loaded")

		var n int
		switch sub.Dataset {
		case "Fundamentals":
			n, err = importFundamentalsRows(ctx, sub, rows, out)
		case "Metrics":
			n, err = importMetricsRows(ctx, sub, rows, out)
		case "Stock Tickers":
			n, err = importTickersRows(ctx, sub, rows, out)
		default:
			err = fmt.Errorf("unsupported dataset %q for file import", sub.Dataset)
		}

		if err != nil {
			logger.Error().Err(err).Str("file", filePath).Msg("failed to import file")
			runSummary.Status = data.RunFailed
			return
		}

		numObs += n
		logger.Info().Int("observations", n).Str("file", filePath).Msg("file imported")
	}
}

func importFundamentalsRows(ctx context.Context, sub *library.Subscription,
	rows []map[string]string, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)

	// Load FIGI map from DB
	conn, err := sub.Library.Pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire db connection: %w", err)
	}
	defer conn.Release()

	assets, err := data.ActiveAssets(ctx, conn)
	if err != nil {
		return 0, fmt.Errorf("load active assets: %w", err)
	}

	figiMap := make(map[string]string, len(assets))
	for _, asset := range assets {
		figiMap[asset.Ticker] = asset.CompositeFigi
	}

	count := 0
	for i, row := range rows {
		fundamental := mapRowToSharadarFundamental(row)
		pvFundamental := fundamental.PvFundamental(figiMap)

		out <- &data.Observation{
			Fundamental:      pvFundamental,
			ObservationDate:  time.Now(),
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}

		count++
		if (i+1)%10000 == 0 {
			logger.Info().Int("progress", i+1).Int("total", len(rows)).Msg("importing fundamentals")
		}
	}

	return count, nil
}

func importMetricsRows(ctx context.Context, sub *library.Subscription,
	rows []map[string]string, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)

	// Load FIGI map from DB
	conn, err := sub.Library.Pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("acquire db connection: %w", err)
	}
	defer conn.Release()

	assets, err := data.ActiveAssets(ctx, conn)
	if err != nil {
		return 0, fmt.Errorf("load active assets: %w", err)
	}

	figiMap := make(map[string]string, len(assets))
	for _, asset := range assets {
		figiMap[asset.Ticker] = asset.CompositeFigi
	}

	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return 0, fmt.Errorf("load timezone: %w", err)
	}

	count := 0
	for i, row := range rows {
		metric := mapRowToSharadarMetric(row)
		pvMetric := metric.PvMetric(figiMap, nyc)

		out <- &data.Observation{
			Metric:           pvMetric,
			ObservationDate:  time.Now(),
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}

		count++
		if (i+1)%10000 == 0 {
			logger.Info().Int("progress", i+1).Int("total", len(rows)).Msg("importing metrics")
		}
	}

	return count, nil
}

func importTickersRows(ctx context.Context, sub *library.Subscription,
	rows []map[string]string, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)

	enrichBatch := make([]*data.Asset, 0, 5000)
	allAssets := make([]*data.Asset, 0, len(rows))

	count := 0
	skipped := 0

	for i, row := range rows {
		// Filter to SF1 table only
		if table, ok := row["table"]; ok && strings.ToUpper(table) != "SF1" {
			skipped++
			continue
		}

		ticker := mapRowToSharadarTicker(row)

		if ticker.Exchange == "" {
			continue
		}

		pvAsset := ticker.ToAsset()

		// Same filtering as API fetcher
		if pvAsset.PrimaryExchange == data.OTCExchange ||
			pvAsset.PrimaryExchange == data.IndexExchange ||
			pvAsset.PrimaryExchange == data.UnknownExchange ||
			pvAsset.AssetType == data.INDEX ||
			pvAsset.AssetType == data.UnknownAsset {
			continue
		}

		if pvAsset.Active {
			enrichBatch = append(enrichBatch, pvAsset)
		}

		allAssets = append(allAssets, pvAsset)

		// Batch FIGI enrichment every 5000 active assets
		if len(enrichBatch) >= 5000 {
			logger.Info().Int("batch", len(enrichBatch)).Msg("enriching batch with FIGI")
			figi.Enrich(enrichBatch...)
			enrichBatch = enrichBatch[:0]
		}

		if (i+1)%10000 == 0 {
			logger.Info().Int("progress", i+1).Int("total", len(rows)).Msg("processing tickers")
		}
	}

	// Enrich remaining batch
	if len(enrichBatch) > 0 {
		logger.Info().Int("batch", len(enrichBatch)).Msg("enriching final batch with FIGI")
		figi.Enrich(enrichBatch...)
	}

	if skipped > 0 {
		logger.Info().Int("skipped", skipped).Msg("skipped non-SF1 rows")
	}

	// Emit all assets as observations
	for _, asset := range allAssets {
		out <- &data.Observation{
			AssetObject:      asset,
			ObservationDate:  time.Now(),
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}
		count++
	}

	return count, nil
}
```

- [ ] **Step 2: Verify the project compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`
Expected: success

- [ ] **Step 3: Run all existing tests to check for regressions**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./provider/ -run TestIndexHelpers -v`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add provider/sharadar_import.go
git commit -m "feat: implement Sharadar ImportFiles with per-dataset importers"
```

---

### Task 9: Create the pvdata import CLI Command

**Files:**
- Create: `cmd/import.go`
- Reference: `cmd/run.go` (for orchestration pattern), `cmd/enable.go` (for subscription lookup)

- [ ] **Step 1: Create cmd/import.go**

```go
/*
Copyright 2024
*/
package cmd

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var importCmd = &cobra.Command{
	Use:   "import [flags] <file1> [file2...]",
	Short: "Import data from local files into a subscription",
	Long: `Import data from local parquet or CSV files into an existing subscription.
The subscription must already exist (create with 'pvdata subscribe').

Supported file formats: .parquet, .csv, .csv.zst, .csv.zip

Examples:
  pvdata import --subscription my-fundamentals sharadar_sf1_20231226.parquet
  pvdata import --subscription abc123 data/*.parquet`,
	Args: cobra.MinimumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		ctx := context.Background()

		consoleWriter := zerolog.ConsoleWriter{Out: os.Stderr}
		log.Logger = zerolog.New(consoleWriter).With().Timestamp().Logger()

		// Load the library
		myLibrary, err := library.NewFromDB(ctx, viper.GetString("db.url"))
		if err != nil {
			log.Fatal().Err(err).Msg("could not connect to library")
		}

		// Look up subscription
		subFlag := viper.GetString("import.subscription")
		if subFlag == "" {
			log.Fatal().Msg("--subscription is required")
		}

		sub, err := resolveSubscription(ctx, myLibrary, subFlag)
		if err != nil {
			log.Fatal().Err(err).Str("subscription", subFlag).Msg("could not find subscription")
		}

		log.Info().Str("subscription", sub.Name).Str("provider", sub.Provider).Str("dataset", sub.Dataset).Msg("using subscription")

		// Look up provider and check FileImporter support
		prov, ok := provider.Map[sub.Provider]
		if !ok {
			log.Fatal().Str("provider", sub.Provider).Msg("provider not found")
		}

		fi, ok := prov.(provider.FileImporter)
		if !ok {
			log.Fatal().Str("provider", sub.Provider).Msg("provider does not support file import")
		}

		// Validate files exist and match the subscription's dataset
		files := args
		for _, f := range files {
			if _, err := os.Stat(f); os.IsNotExist(err) {
				log.Fatal().Str("file", f).Msg("file does not exist")
			}

			detected, err := provider.DetectSharadarDataset(f)
			if err != nil {
				log.Warn().Err(err).Str("file", f).Msg("could not detect dataset from filename, proceeding anyway")
			} else if detected != sub.Dataset {
				log.Fatal().
					Str("file", f).
					Str("detected", detected).
					Str("subscription_dataset", sub.Dataset).
					Msgf("file %s looks like %s data but subscription %q is for dataset %s", f, detected, sub.Name, sub.Dataset)
			}
		}

		// Manage partitions and run migrations
		if err := sub.ManagePartitions(ctx); err != nil {
			log.Error().Err(err).Msg("ManagePartitions failed")
		}

		if err := sub.RunMigrations(ctx); err != nil {
			log.Error().Err(err).Msg("RunMigrations failed")
		}

		// Set up the observation pipeline (same as runSubscription in cmd/run.go)
		outChan := make(chan *data.Observation, 1000)
		exitChan := make(chan data.RunSummary, 1)

		var wg sync.WaitGroup
		wg.Add(1)

		go myLibrary.SaveObservations(outChan, &wg)

		fetchLogger := log.With().Str("SubscriptionID", sub.ID.String()).Logger()
		fetchCtx := fetchLogger.WithContext(ctx)

		fi.ImportFiles(fetchCtx, sub, files, outChan, exitChan)

		summary := <-exitChan
		close(outChan)
		wg.Wait()

		// Run PostFetch hooks on success
		if summary.Status == data.RunSuccess {
			subDataset, dsOk := prov.Datasets()[sub.Dataset]
			if dsOk && len(subDataset.PostFetch) > 0 {
				for _, hook := range subDataset.PostFetch {
					if err := hook(ctx, sub); err != nil {
						log.Error().Err(err).Msg("post-fetch hook failed")
						break
					}
				}
			}
		}

		if summary.Status == data.RunFailed {
			log.Error().Int("observations", summary.NumObservations).Msg("import failed")
			os.Exit(1)
		} else {
			log.Info().Int("observations", summary.NumObservations).Msg("import completed successfully")
		}
	},
}

// resolveSubscription looks up a subscription by UUID prefix first, then by exact name.
func resolveSubscription(ctx context.Context, lib *library.Library, nameOrID string) (*library.Subscription, error) {
	// Try as UUID/ID prefix first
	sub, err := lib.SubscriptionFromID(ctx, nameOrID)
	if err == nil {
		return sub, nil
	}

	// Fall back to name search
	allSubs, err := lib.Subscriptions(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not load subscriptions: %w", err)
	}

	var matches []*library.Subscription
	for _, s := range allSubs {
		if s.Name == nameOrID {
			matches = append(matches, s)
		}
	}

	switch len(matches) {
	case 0:
		return nil, fmt.Errorf("no subscription found matching %q", nameOrID)
	case 1:
		return matches[0], nil
	default:
		return nil, fmt.Errorf("ambiguous subscription name %q matches %d subscriptions; use the subscription ID instead", nameOrID, len(matches))
	}
}

func init() {
	importCmd.Flags().StringP("subscription", "s", "", "Subscription name or ID (required)")
	if err := importCmd.MarkFlagRequired("subscription"); err != nil {
		log.Fatal().Err(err).Msg("could not mark subscription flag as required")
	}

	if err := viper.BindPFlag("import.subscription", importCmd.Flags().Lookup("subscription")); err != nil {
		log.Fatal().Err(err).Msg("could not bind subscription flag")
	}

	rootCmd.AddCommand(importCmd)
}
```

- [ ] **Step 2: Verify the project compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`
Expected: success

- [ ] **Step 3: Verify the help output shows the new command**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go run . import --help`
Expected: shows import command usage with --subscription flag

- [ ] **Step 4: Commit**

```bash
git add cmd/import.go
git commit -m "feat: add pvdata import CLI command for file-based data import"
```

---

## Chunk 5: Smoke Test and Verification

### Task 10: End-to-End Smoke Test

This task verifies the full pipeline works with real data files. Requires a running database with an existing Sharadar subscription.

- [ ] **Step 1: Run all unit tests**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go test ./... -v`
Expected: all tests PASS

- [ ] **Step 2: Build the binary**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build -o pvdata .`
Expected: binary builds without errors

- [ ] **Step 3: Test with a small tickers import (if subscription exists)**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ./pvdata import --subscription <tickers-subscription-id> ../data/sharadar/sharadar_tickers.parquet`

Check that:
- Progress logging appears on stderr
- Import completes with observation count
- No panics or errors

If no subscription exists yet, create one first:
```bash
./pvdata subscribe sharadar  # select Stock Tickers dataset
./pvdata disable <id>        # optional: prevent scheduled runs
```

- [ ] **Step 4: Test with metrics parquet**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ./pvdata import --subscription <metrics-subscription-id> ../data/sharadar/sharadar_metrics_20231228.parquet`

- [ ] **Step 5: Test with fundamentals parquet**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ./pvdata import --subscription <fundamentals-subscription-id> ../data/sharadar/sharadar_sf1_20231226.parquet`

- [ ] **Step 6: Test error cases**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ./pvdata import --subscription nonexistent file.parquet`
Expected: error "no subscription found matching..."

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ./pvdata import --subscription <id> nonexistent.parquet`
Expected: error "file does not exist"

- [ ] **Step 7: Commit any fixes from smoke testing**

If any issues were found and fixed during smoke testing, commit them:

```bash
git add -A
git commit -m "fix: address issues found during import smoke testing"
```
