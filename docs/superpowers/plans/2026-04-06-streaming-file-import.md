# Streaming File Import Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Refactor Sharadar file import to stream rows through a channel instead of loading entire files into memory, reducing memory from O(all rows) to O(buffer size) for 36M+ row files.

**Architecture:** Replace `readFileRows` (returns `[]map[string]string`) with `streamFileRows` (returns `<-chan RowResult`). Reader goroutines send rows as they're read; consumers range over the channel. Parquet reads in 10k batches, CSV reads line-by-line. SP500 and Tickers imports (small files) collect rows locally from the channel since they need multi-pass logic.

**Tech Stack:** Go, parquet-go (xitongsys), encoding/csv, zstd, archive/zip

---

### Task 1: Add RowResult Type and streamCSV

**Files:**
- Modify: `provider/sharadar/sharadar_import.go:225-262`

- [ ] **Step 1: Write failing test for streamCSV**

Add to `provider/sharadar/sharadar_import_test.go`:

```go
var _ = Describe("streamCSV", func() {
	It("streams CSV rows with lowercase headers", func() {
		csvData := "Ticker,Date,Value\nAAPL,2023-01-01,150.00\nMSFT,2023-01-02,250.00\n"
		r := strings.NewReader(csvData)
		ch := make(chan RowResult, 10)

		ctx := context.Background()
		go streamCSV(ctx, r, ch)

		var rows []map[string]string
		for rr := range ch {
			Expect(rr.Err).NotTo(HaveOccurred())
			rows = append(rows, rr.Row)
		}

		Expect(rows).To(HaveLen(2))
		Expect(rows[0]["ticker"]).To(Equal("AAPL"))
		Expect(rows[0]["date"]).To(Equal("2023-01-01"))
		Expect(rows[1]["ticker"]).To(Equal("MSFT"))
	})

	It("streams empty result for headers-only CSV", func() {
		r := strings.NewReader("Ticker,Date\n")
		ch := make(chan RowResult, 10)

		ctx := context.Background()
		go streamCSV(ctx, r, ch)

		var rows []map[string]string
		for rr := range ch {
			Expect(rr.Err).NotTo(HaveOccurred())
			rows = append(rows, rr.Row)
		}

		Expect(rows).To(BeEmpty())
	})

	It("sends error on malformed CSV", func() {
		r := strings.NewReader("Ticker,Date\n\"unclosed quote")
		ch := make(chan RowResult, 10)

		ctx := context.Background()
		go streamCSV(ctx, r, ch)

		var gotErr bool
		for rr := range ch {
			if rr.Err != nil {
				gotErr = true
			}
		}

		Expect(gotErr).To(BeTrue())
	})

	It("stops when context is cancelled", func() {
		// Create CSV with many rows
		var b strings.Builder
		b.WriteString("Ticker\n")
		for i := 0; i < 1000; i++ {
			b.WriteString("AAPL\n")
		}

		r := strings.NewReader(b.String())
		ch := make(chan RowResult) // unbuffered to force blocking

		ctx, cancel := context.WithCancel(context.Background())

		go streamCSV(ctx, r, ch)

		// Read one row then cancel
		rr := <-ch
		Expect(rr.Err).NotTo(HaveOccurred())
		cancel()

		// Drain remaining (should stop quickly)
		count := 1
		for range ch {
			count++
		}

		// Should have stopped well before 1000
		Expect(count).To(BeNumerically("<", 1000))
	})
})
```

Add imports at top of test file: `"context"`, `"strings"`.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race --focus "streamCSV" ./provider/sharadar/`
Expected: FAIL (RowResult and streamCSV undefined)

- [ ] **Step 3: Implement RowResult and streamCSV**

Add the `RowResult` type near the top of `provider/sharadar/sharadar_import.go` (after the imports, before `parseCSV`):

```go
// RowResult carries a single row or an error from a streaming file reader.
type RowResult struct {
	Row map[string]string
	Err error
}
```

Add `streamCSV` below the existing `parseCSV` function (keep `parseCSV` for now -- it will be removed in a later task):

```go
// streamCSV reads CSV data from r and sends each row to out as a map[string]string.
// Header names are normalized to lowercase. The channel is closed when reading
// completes or the context is cancelled.
func streamCSV(ctx context.Context, r io.Reader, out chan<- RowResult) {
	defer close(out)

	cr := csv.NewReader(r)

	headers, err := cr.Read()
	if err != nil {
		select {
		case out <- RowResult{Err: fmt.Errorf("read CSV headers: %w", err)}:
		case <-ctx.Done():
		}

		return
	}

	for i, h := range headers {
		headers[i] = strings.ToLower(strings.TrimSpace(h))
	}

	for {
		record, err := cr.Read()
		if err == io.EOF {
			return
		}

		if err != nil {
			select {
			case out <- RowResult{Err: fmt.Errorf("read CSV row: %w", err)}:
			case <-ctx.Done():
			}

			return
		}

		row := make(map[string]string, len(headers))
		for i, h := range headers {
			if i < len(record) {
				row[h] = record[i]
			}
		}

		select {
		case out <- RowResult{Row: row}:
		case <-ctx.Done():
			return
		}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race --focus "streamCSV" ./provider/sharadar/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add provider/sharadar/sharadar_import.go provider/sharadar/sharadar_import_test.go
git commit -m "feat: add RowResult type and streamCSV for streaming file reads"
```

---

### Task 2: Add streamParquetRows

**Files:**
- Modify: `provider/sharadar/sharadar_import.go`
- Modify: `provider/sharadar/sharadar_import_test.go`

- [ ] **Step 1: Write test for streamParquetRows**

Add to `provider/sharadar/sharadar_import_test.go`, replacing the existing `readParquetRows` tests:

```go
var _ = Describe("streamParquetRows", func() {
	It("streams parquet rows if test file exists", func() {
		parquetPath := "../../data/sharadar/sharadar_metrics_20231228.parquet"
		if _, err := os.Stat(parquetPath); os.IsNotExist(err) {
			Skip("test parquet file not available")
		}

		ch := make(chan RowResult, 100)
		ctx := context.Background()

		go streamParquetRows(ctx, parquetPath, ch)

		var rows []map[string]string
		for rr := range ch {
			Expect(rr.Err).NotTo(HaveOccurred())
			rows = append(rows, rr.Row)
		}

		Expect(rows).NotTo(BeEmpty())
		Expect(rows[0]).To(HaveKey("ticker"))
	})

	It("streams SP500 parquet rows if test file exists", func() {
		parquetPath := "../../data/sharadar/sharadar_sp500_20231228.parquet"
		if _, err := os.Stat(parquetPath); os.IsNotExist(err) {
			Skip("test parquet file not available")
		}

		ch := make(chan RowResult, 100)
		ctx := context.Background()

		go streamParquetRows(ctx, parquetPath, ch)

		var rows []map[string]string
		for rr := range ch {
			Expect(rr.Err).NotTo(HaveOccurred())
			rows = append(rows, rr.Row)
		}

		Expect(rows).NotTo(BeEmpty())
		Expect(rows[0]).To(HaveKey("ticker"))
		Expect(rows[0]).To(HaveKey("action"))
	})
})
```

- [ ] **Step 2: Implement streamParquetRows**

Add below `streamCSV` in `sharadar_import.go`:

```go
// streamParquetRows reads a parquet file and sends each row to out as a
// map[string]string. Column names are normalized to lowercase. Reads in
// batches of 10,000 internally but sends one row at a time.
func streamParquetRows(ctx context.Context, path string, out chan<- RowResult) {
	defer close(out)

	fr, err := local.NewLocalFileReader(path)
	if err != nil {
		select {
		case out <- RowResult{Err: fmt.Errorf("open parquet %s: %w", path, err)}:
		case <-ctx.Done():
		}

		return
	}
	defer fr.Close()

	pr, err := reader.NewParquetReader(fr, nil, 4)
	if err != nil {
		select {
		case out <- RowResult{Err: fmt.Errorf("create parquet reader for %s: %w", path, err)}:
		case <-ctx.Done():
		}

		return
	}
	defer pr.ReadStop()

	numRows := int(pr.GetNumRows())
	batchSize := 10000

	for numRows > 0 {
		readCount := batchSize
		if readCount > numRows {
			readCount = numRows
		}

		batch, err := pr.ReadByNumber(readCount)
		if err != nil {
			select {
			case out <- RowResult{Err: fmt.Errorf("read parquet rows: %w", err)}:
			case <-ctx.Done():
			}

			return
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
				field := val.Field(j)

				if field.Kind() == reflect.Ptr {
					if field.IsNil() {
						row[name] = ""

						continue
					}

					field = field.Elem()
				}

				row[name] = fmt.Sprintf("%v", field.Interface())
			}

			select {
			case out <- RowResult{Row: row}:
			case <-ctx.Done():
				return
			}
		}

		numRows -= readCount
	}
}
```

- [ ] **Step 3: Run tests**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race --focus "streamParquetRows" ./provider/sharadar/`
Expected: PASS (or Skip if test files not available)

- [ ] **Step 4: Commit**

```bash
git add provider/sharadar/sharadar_import.go provider/sharadar/sharadar_import_test.go
git commit -m "feat: add streamParquetRows for streaming parquet reads"
```

---

### Task 3: Add streamFileRows and CSV Variant Streamers

**Files:**
- Modify: `provider/sharadar/sharadar_import.go`
- Modify: `provider/sharadar/sharadar_import_test.go`

- [ ] **Step 1: Write test for streamFileRows with CSV**

Add to test file:

```go
var _ = Describe("streamFileRows", func() {
	It("streams rows from a plain CSV file", func() {
		dir := GinkgoT().TempDir()
		path := filepath.Join(dir, "test.csv")

		content := "Ticker,Date,Value\nAAPL,2023-01-01,150.00\nMSFT,2023-01-02,250.00\n"
		Expect(os.WriteFile(path, []byte(content), 0600)).To(Succeed())

		ctx := context.Background()
		ch := streamFileRows(ctx, path)

		var rows []map[string]string
		for rr := range ch {
			Expect(rr.Err).NotTo(HaveOccurred())
			rows = append(rows, rr.Row)
		}

		Expect(rows).To(HaveLen(2))
		Expect(rows[0]["ticker"]).To(Equal("AAPL"))
		Expect(rows[1]["ticker"]).To(Equal("MSFT"))
	})

	It("sends error for unsupported format", func() {
		dir := GinkgoT().TempDir()
		path := filepath.Join(dir, "test.xlsx")
		Expect(os.WriteFile(path, []byte("data"), 0600)).To(Succeed())

		ctx := context.Background()
		ch := streamFileRows(ctx, path)

		rr := <-ch
		Expect(rr.Err).To(HaveOccurred())
	})
})
```

- [ ] **Step 2: Implement streamFileRows and CSV variant streamers**

Add to `sharadar_import.go`:

```go
// streamCSVRows opens a plain CSV file and streams its rows.
func streamCSVRows(ctx context.Context, path string, out chan<- RowResult) {
	f, err := os.Open(path)
	if err != nil {
		out <- RowResult{Err: fmt.Errorf("open CSV %s: %w", path, err)}
		close(out)

		return
	}
	defer f.Close()

	streamCSV(ctx, f, out)
}

// streamCSVZstRows opens a zstd-compressed CSV file and streams its rows.
func streamCSVZstRows(ctx context.Context, path string, out chan<- RowResult) {
	f, err := os.Open(path)
	if err != nil {
		out <- RowResult{Err: fmt.Errorf("open CSV.zst %s: %w", path, err)}
		close(out)

		return
	}
	defer f.Close()

	zr, err := zstd.NewReader(f)
	if err != nil {
		out <- RowResult{Err: fmt.Errorf("create zstd reader for %s: %w", path, err)}
		close(out)

		return
	}
	defer zr.Close()

	streamCSV(ctx, zr, out)
}

// streamCSVZipRows opens a zip archive and streams rows from the first CSV entry.
func streamCSVZipRows(ctx context.Context, path string, out chan<- RowResult) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		out <- RowResult{Err: fmt.Errorf("open ZIP %s: %w", path, err)}
		close(out)

		return
	}
	defer zr.Close()

	for _, f := range zr.File {
		if strings.HasSuffix(strings.ToLower(f.Name), ".csv") {
			rc, err := f.Open()
			if err != nil {
				out <- RowResult{Err: fmt.Errorf("open CSV entry %s in ZIP %s: %w", f.Name, path, err)}
				close(out)

				return
			}
			defer rc.Close()

			streamCSV(ctx, rc, out)

			return
		}
	}

	out <- RowResult{Err: fmt.Errorf("no CSV entry found in ZIP %s", path)}
	close(out)
}

const rowChannelBuffer = 10000

// streamFileRows detects the file format and returns a channel that yields rows
// as they are read. The channel is closed when reading completes or on error.
func streamFileRows(ctx context.Context, path string) <-chan RowResult {
	ch := make(chan RowResult, rowChannelBuffer)

	format, err := detectFileFormat(path)
	if err != nil {
		go func() {
			ch <- RowResult{Err: err}
			close(ch)
		}()

		return ch
	}

	switch format {
	case fileFormatParquet:
		go streamParquetRows(ctx, path, ch)
	case fileFormatCSV:
		go streamCSVRows(ctx, path, ch)
	case fileFormatCSVZst:
		go streamCSVZstRows(ctx, path, ch)
	case fileFormatCSVZip:
		go streamCSVZipRows(ctx, path, ch)
	default:
		go func() {
			ch <- RowResult{Err: fmt.Errorf("unsupported file format for %q", path)}
			close(ch)
		}()
	}

	return ch
}
```

Note: `streamCSVRows`, `streamCSVZstRows`, and `streamCSVZipRows` do NOT call `defer close(out)` themselves -- `streamCSV` handles closing the channel. They only close + send error on the early-return paths before `streamCSV` is called.

- [ ] **Step 3: Run tests**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race --focus "streamFileRows" ./provider/sharadar/`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add provider/sharadar/sharadar_import.go provider/sharadar/sharadar_import_test.go
git commit -m "feat: add streamFileRows with CSV variant streamers"
```

---

### Task 4: Convert Import Functions to Channel Consumers

**Files:**
- Modify: `provider/sharadar/sharadar_import.go:688-777` (importFundamentalsRows, importMetricsRows)

- [ ] **Step 1: Convert importFundamentalsRows**

Change the signature and body of `importFundamentalsRows` from:

```go
func importFundamentalsRows(ctx context.Context, sub *library.Subscription, rows []map[string]string, out chan<- *data.Observation) (int, error) {
```

To:

```go
func importFundamentalsRows(ctx context.Context, sub *library.Subscription, rows <-chan RowResult, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)

	conn, err := sub.Library.Pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("could not acquire database connection: %w", err)
	}
	defer conn.Release()

	assets, err := data.ActiveAssets(ctx, conn)
	if err != nil {
		return 0, fmt.Errorf("could not load active assets: %w", err)
	}

	figiMap := make(map[string]string, len(assets))
	for _, asset := range assets {
		figiMap[asset.Ticker] = asset.CompositeFigi
	}

	count := 0

	for rr := range rows {
		if rr.Err != nil {
			return count, fmt.Errorf("reading file: %w", rr.Err)
		}

		fundamental := mapRowToSharadarFundamental(rr.Row)
		pvFundamental := fundamental.PvFundamental(figiMap)

		out <- &data.Observation{
			Fundamental:      pvFundamental,
			ObservationDate:  time.Now(),
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}

		count++

		if count%10000 == 0 {
			logger.Info().Int("rows_processed", count).Msg("fundamentals import progress")
		}
	}

	return count, nil
}
```

- [ ] **Step 2: Convert importMetricsRows**

Change the signature and body of `importMetricsRows` from:

```go
func importMetricsRows(ctx context.Context, sub *library.Subscription, rows []map[string]string, out chan<- *data.Observation) (int, error) {
```

To:

```go
func importMetricsRows(ctx context.Context, sub *library.Subscription, rows <-chan RowResult, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)

	conn, err := sub.Library.Pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("could not acquire database connection: %w", err)
	}
	defer conn.Release()

	assets, err := data.ActiveAssets(ctx, conn)
	if err != nil {
		return 0, fmt.Errorf("could not load active assets: %w", err)
	}

	figiMap := make(map[string]string, len(assets))
	for _, asset := range assets {
		figiMap[asset.Ticker] = asset.CompositeFigi
	}

	nyc, err := time.LoadLocation("America/New_York")
	if err != nil {
		return 0, fmt.Errorf("could not load NYC timezone: %w", err)
	}

	count := 0

	for rr := range rows {
		if rr.Err != nil {
			return count, fmt.Errorf("reading file: %w", rr.Err)
		}

		metric := mapRowToSharadarMetric(rr.Row)
		pvMetric := metric.PvMetric(figiMap, nyc)

		out <- &data.Observation{
			Metric:           pvMetric,
			ObservationDate:  time.Now(),
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}

		count++

		if count%10000 == 0 {
			logger.Info().Int("rows_processed", count).Msg("metrics import progress")
		}
	}

	return count, nil
}
```

- [ ] **Step 3: Convert importTickersRows**

Change `importTickersRows` signature to accept `<-chan RowResult`. Since it needs multi-pass logic (FIGI enrichment batching then emission), drain the channel into a local slice. Tickers files are small (thousands of rows).

```go
func importTickersRows(ctx context.Context, sub *library.Subscription, rows <-chan RowResult, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)

	// Drain channel into slice -- tickers files are small (thousands of rows)
	var rowSlice []map[string]string

	for rr := range rows {
		if rr.Err != nil {
			return 0, fmt.Errorf("reading file: %w", rr.Err)
		}

		rowSlice = append(rowSlice, rr.Row)
	}

	enrichAssets := make([]*data.Asset, 0, 5000)
	allAssets := make([]*data.Asset, 0, len(rowSlice))

	for _, row := range rowSlice {
		if row["table"] != "SF1" {
			continue
		}

		ticker := mapRowToSharadarTicker(row)

		if ticker.Exchange == "" {
			continue
		}

		pvAsset := ticker.ToAsset()

		if pvAsset.PrimaryExchange == data.OTCExchange ||
			pvAsset.PrimaryExchange == data.IndexExchange ||
			pvAsset.PrimaryExchange == data.UnknownExchange ||
			pvAsset.AssetType == data.INDEX ||
			pvAsset.AssetType == data.UnknownAsset {
			continue
		}

		if pvAsset.Active {
			enrichAssets = append(enrichAssets, pvAsset)
		}

		allAssets = append(allAssets, pvAsset)

		if len(enrichAssets) >= 5000 {
			log.Info().Int("batch_size", len(enrichAssets)).Msg("enriching asset batch with FIGI")
			figi.Enrich(enrichAssets...)
			enrichAssets = enrichAssets[:0]
		}
	}

	if len(enrichAssets) > 0 {
		log.Info().Int("batch_size", len(enrichAssets)).Msg("enriching final asset batch with FIGI")
		figi.Enrich(enrichAssets...)
	}

	count := 0

	for _, asset := range allAssets {
		out <- &data.Observation{
			AssetObject:      asset,
			ObservationDate:  time.Now(),
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}

		count++
	}

	logger.Info().Int("assets", count).Msg("tickers import complete")

	return count, nil
}
```

- [ ] **Step 4: Convert importSP500Rows**

Same approach -- drain channel into slice since SP500 files are small and the function needs multi-pass logic:

Change signature from `rows []map[string]string` to `rows <-chan RowResult`. Add drain at the beginning:

```go
func importSP500Rows(ctx context.Context, sub *library.Subscription, rows <-chan RowResult, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)

	// Drain channel into slice -- SP500 files are small
	var rowSlice []map[string]string

	for rr := range rows {
		if rr.Err != nil {
			return 0, fmt.Errorf("reading file: %w", rr.Err)
		}

		rowSlice = append(rowSlice, rr.Row)
	}

	// ... rest of function unchanged but uses rowSlice instead of rows ...
```

Replace all references to `rows` with `rowSlice` in the rest of the function body (the 4 `for _, row := range rows` loops become `for _, row := range rowSlice`). The `len(rows)` call in `allAssets := make([]*data.Asset, 0, len(rows))` if present becomes `len(rowSlice)`.

- [ ] **Step 5: Verify build compiles**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./...`
Expected: FAIL -- `ImportFiles` still calls `readFileRows` which returns `[]map[string]string`, and the import functions now expect channels. This will be fixed in the next task.

- [ ] **Step 6: Commit (work in progress)**

Don't commit yet -- proceed to Task 5 to update ImportFiles so the build succeeds.

---

### Task 5: Update ImportFiles Orchestrator and Remove Old Functions

**Files:**
- Modify: `provider/sharadar/sharadar_import.go:622-686` (ImportFiles)

- [ ] **Step 1: Update ImportFiles to use streamFileRows**

Replace the body of the `for _, filePath := range files` loop in `ImportFiles`:

From:
```go
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
		case "SP500":
			n, err = importSP500Rows(ctx, sub, rows, out)
		default:
			err = fmt.Errorf("unsupported dataset %q for file import", sub.Dataset)
		}
```

To:
```go
		logger.Info().Str("file", filePath).Msg("streaming file")

		rowChan := streamFileRows(ctx, filePath)

		var n int

		switch sub.Dataset {
		case "Fundamentals":
			n, err = importFundamentalsRows(ctx, sub, rowChan, out)
		case "Metrics":
			n, err = importMetricsRows(ctx, sub, rowChan, out)
		case "Stock Tickers":
			n, err = importTickersRows(ctx, sub, rowChan, out)
		case "SP500":
			n, err = importSP500Rows(ctx, sub, rowChan, out)
		default:
			err = fmt.Errorf("unsupported dataset %q for file import", sub.Dataset)
		}
```

- [ ] **Step 2: Remove old functions**

Delete these functions from `sharadar_import.go`:
- `parseCSV` (lines 227-262)
- `readCSVRows` (lines 264-273)
- `readCSVZstRows` (lines 275-290)
- `readCSVZipRows` (lines 292-313)
- `readParquetRows` (lines 315-383)
- `readFileRows` (lines 385-404)

- [ ] **Step 3: Update readCSVHeaders to use streamCSV internally**

Check if `DetectSharadarDataset` or `countFileRows` still uses `parseCSV` or `readCSVRows`. If `DetectSharadarDataset` reads headers only, it may use its own header-reading logic. Verify and fix any remaining references to the deleted functions.

Look at `DetectSharadarDataset` and `countFileRows` -- if they call `readFileRows` or `readCSVRows`, they need to be updated. `countFileRows` likely needs to stream too or can use the format-specific counters. `DetectSharadarDataset` only reads headers so it should be fine.

- [ ] **Step 4: Update existing tests**

Update `readCSVRows` tests to test `streamFileRows` instead. The `readParquetRows` tests are already replaced in Task 2. Update the `readCSVRows` describe block:

```go
var _ = Describe("streamFileRows with CSV", func() {
	It("streams CSV rows with lowercase headers", func() {
		dir := GinkgoT().TempDir()
		path := filepath.Join(dir, "test.csv")

		content := "Ticker,Date,Value\nAAPL,2023-01-01,150.00\nMSFT,2023-01-02,250.00\n"
		Expect(os.WriteFile(path, []byte(content), 0600)).To(Succeed())

		ctx := context.Background()
		ch := streamFileRows(ctx, path)

		var rows []map[string]string
		for rr := range ch {
			Expect(rr.Err).NotTo(HaveOccurred())
			rows = append(rows, rr.Row)
		}

		Expect(rows).To(HaveLen(2))
		Expect(rows[0]["ticker"]).To(Equal("AAPL"))
		Expect(rows[0]["date"]).To(Equal("2023-01-01"))
		Expect(rows[0]["value"]).To(Equal("150.00"))
		Expect(rows[1]["ticker"]).To(Equal("MSFT"))
	})

	It("streams empty result for CSV with only headers", func() {
		dir := GinkgoT().TempDir()
		path := filepath.Join(dir, "empty.csv")

		Expect(os.WriteFile(path, []byte("Ticker,Date\n"), 0600)).To(Succeed())

		ctx := context.Background()
		ch := streamFileRows(ctx, path)

		var rows []map[string]string
		for rr := range ch {
			Expect(rr.Err).NotTo(HaveOccurred())
			rows = append(rows, rr.Row)
		}

		Expect(rows).To(BeEmpty())
	})
})
```

- [ ] **Step 5: Verify build and tests**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && go build ./... && ginkgo run -race ./provider/sharadar/`
Expected: PASS

- [ ] **Step 6: Run lint**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && golangci-lint run --fix ./...`
Expected: 0 issues (or auto-fixed)

- [ ] **Step 7: Commit**

```bash
git add provider/sharadar/sharadar_import.go provider/sharadar/sharadar_import_test.go
git commit -m "feat: switch ImportFiles to streaming row channel, remove batch readers"
```

---

### Task 6: Final Verification

**Files:** (no new files)

- [ ] **Step 1: Run full test suite**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && ginkgo run -race ./...`
Expected: PASS

- [ ] **Step 2: Run full lint**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && golangci-lint run --fix ./...`
Expected: 0 issues

- [ ] **Step 3: Verify build**

Run: `cd /Users/jdf/Developer/penny-vault/pv-data && make build`
Expected: no errors

- [ ] **Step 4: Commit any remaining fixes**

```bash
git add -A
git commit -m "chore: final lint fixes for streaming file import"
```
