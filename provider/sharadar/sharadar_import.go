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
package sharadar

import (
	"archive/zip"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/figi"
	"github.com/penny-vault/pvdata/library"
	"github.com/penny-vault/pvdata/provider"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/xitongsys/parquet-go-source/local"
	"github.com/xitongsys/parquet-go/reader"
)

// RowResult carries a single row or an error from a streaming file reader.
type RowResult struct {
	Row map[string]string
	Err error
}

// fileFormat represents the format of a data file.
type fileFormat int

const (
	fileFormatParquet fileFormat = iota
	fileFormatCSV
	fileFormatCSVZst
	fileFormatCSVZip
)

// parseCSVHeaders reads only the header row from a CSV reader and returns
// the column names normalized to lowercase.
func parseCSVHeaders(r io.Reader) ([]string, error) {
	cr := csv.NewReader(r)

	headers, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read CSV headers: %w", err)
	}

	for i, h := range headers {
		headers[i] = strings.ToLower(strings.TrimSpace(h))
	}

	return headers, nil
}

// ReadFileHeaders returns the column names from a data file without reading
// the full contents. For CSV-based formats it reads only the header row; for
// parquet it inspects the schema. All names are normalized to lowercase.
func ReadFileHeaders(path string) ([]string, error) {
	format, err := detectFileFormat(path)
	if err != nil {
		return nil, err
	}

	switch format {
	case fileFormatCSV:
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open CSV %s: %w", path, err)
		}
		defer f.Close()

		return parseCSVHeaders(f)

	case fileFormatCSVZst:
		f, err := os.Open(path)
		if err != nil {
			return nil, fmt.Errorf("open CSV.zst %s: %w", path, err)
		}
		defer f.Close()

		zr, err := zstd.NewReader(f)
		if err != nil {
			return nil, fmt.Errorf("create zstd reader for %s: %w", path, err)
		}
		defer zr.Close()

		return parseCSVHeaders(zr)

	case fileFormatCSVZip:
		zr, err := zip.OpenReader(path)
		if err != nil {
			return nil, fmt.Errorf("open ZIP %s: %w", path, err)
		}
		defer zr.Close()

		for _, f := range zr.File {
			if strings.HasSuffix(strings.ToLower(f.Name), ".csv") {
				rc, err := f.Open()
				if err != nil {
					return nil, fmt.Errorf("open CSV entry %s in ZIP %s: %w", f.Name, path, err)
				}
				defer rc.Close()

				return parseCSVHeaders(rc)
			}
		}

		return nil, fmt.Errorf("no CSV entry found in ZIP %s", path)

	case fileFormatParquet:
		fr, err := local.NewLocalFileReader(path)
		if err != nil {
			return nil, fmt.Errorf("open parquet %s: %w", path, err)
		}
		defer fr.Close()

		pr, err := reader.NewParquetReader(fr, nil, 1)
		if err != nil {
			return nil, fmt.Errorf("create parquet reader for %s: %w", path, err)
		}
		defer pr.ReadStop()

		if pr.GetNumRows() == 0 {
			return nil, fmt.Errorf("parquet file %s has no rows", path)
		}

		batch, err := pr.ReadByNumber(1)
		if err != nil {
			return nil, fmt.Errorf("read parquet schema row from %s: %w", path, err)
		}

		if len(batch) == 0 {
			return nil, fmt.Errorf("parquet file %s returned empty batch", path)
		}

		val := reflect.ValueOf(batch[0])
		if val.Kind() == reflect.Ptr {
			val = val.Elem()
		}

		if val.Kind() != reflect.Struct {
			return nil, fmt.Errorf("unexpected parquet row type in %s: %s", path, val.Kind())
		}

		typ := val.Type()
		headers := make([]string, typ.NumField())

		for i := 0; i < typ.NumField(); i++ {
			headers[i] = strings.ToLower(typ.Field(i).Name)
		}

		return headers, nil

	default:
		return nil, fmt.Errorf("unsupported file format for %q", path)
	}
}

// DetectSharadarDataset returns the Sharadar dataset name by reading the
// column headers from the file. Each dataset has at least one unique column:
//   - SP500: "action"
//   - Fundamentals: "dimension"
//   - Stock Tickers: "permaticker"
//   - Metrics: "ev" (with none of the above)
func DetectSharadarDataset(path string) (string, error) {
	headers, err := ReadFileHeaders(path)
	if err != nil {
		return "", fmt.Errorf("detect dataset for %s: %w", path, err)
	}

	headerSet := make(map[string]struct{}, len(headers))
	for _, h := range headers {
		headerSet[h] = struct{}{}
	}

	if _, ok := headerSet["action"]; ok {
		return "SP500", nil
	}

	if _, ok := headerSet["dimension"]; ok {
		return "Fundamentals", nil
	}

	if _, ok := headerSet["permaticker"]; ok {
		return "Stock Tickers", nil
	}

	if _, ok := headerSet["ev"]; ok {
		return "Metrics", nil
	}

	return "", fmt.Errorf("cannot detect Sharadar dataset from columns in %q: no recognized column signature found", path)
}

// detectFileFormat returns the file format based on the file extension.
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
		return 0, fmt.Errorf("unsupported file format for %q", path)
	}
}

// parseCSV reads CSV data from r. The first row is used as headers; header names
// are normalized to lowercase. Each subsequent row is returned as a map[string]string.
func parseCSV(r io.Reader) ([]map[string]string, error) {
	cr := csv.NewReader(r)

	headers, err := cr.Read()
	if err != nil {
		return nil, fmt.Errorf("read CSV headers: %w", err)
	}

	for i, h := range headers {
		headers[i] = strings.ToLower(strings.TrimSpace(h))
	}

	var rows []map[string]string

	for {
		record, err := cr.Read()
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil, fmt.Errorf("read CSV row: %w", err)
		}

		row := make(map[string]string, len(headers))
		for i, h := range headers {
			if i < len(record) {
				row[h] = record[i]
			}
		}

		rows = append(rows, row)
	}

	return rows, nil
}

// readCSVRows opens a plain CSV file and returns its rows as maps.
func readCSVRows(path string) ([]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open CSV %s: %w", path, err)
	}
	defer f.Close()

	return parseCSV(f)
}

// readCSVZstRows opens a zstd-compressed CSV file and returns its rows as maps.
func readCSVZstRows(path string) ([]map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open CSV.zst %s: %w", path, err)
	}
	defer f.Close()

	zr, err := zstd.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("create zstd reader for %s: %w", path, err)
	}
	defer zr.Close()

	return parseCSV(zr)
}

// readCSVZipRows opens a zip archive, finds the first .csv entry, and returns its rows as maps.
func readCSVZipRows(path string) ([]map[string]string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open ZIP %s: %w", path, err)
	}
	defer zr.Close()

	for _, f := range zr.File {
		if strings.HasSuffix(strings.ToLower(f.Name), ".csv") {
			rc, err := f.Open()
			if err != nil {
				return nil, fmt.Errorf("open CSV entry %s in ZIP %s: %w", f.Name, path, err)
			}
			defer rc.Close()

			return parseCSV(rc)
		}
	}

	return nil, fmt.Errorf("no CSV entry found in ZIP %s", path)
}

// readParquetRows reads a parquet file and returns its rows as maps.
// Column names are normalized to lowercase.
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
				field := val.Field(j)

				// Dereference pointer fields (nullable parquet columns)
				if field.Kind() == reflect.Ptr {
					if field.IsNil() {
						row[name] = ""

						continue
					}

					field = field.Elem()
				}

				row[name] = fmt.Sprintf("%v", field.Interface())
			}

			rows = append(rows, row)
		}

		numRows -= readCount
	}

	return rows, nil
}

// readFileRows dispatches to the appropriate reader based on the file format.
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
		return nil, fmt.Errorf("unsupported file format for %q", path)
	}
}

// streamCSV reads CSV data from r and sends each row to out as a RowResult.
// Headers are normalized to lowercase. The channel is closed via defer when
// reading is complete or an error occurs. Cancellation is supported via ctx.
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

// parseFloat converts a string to float64, treating empty/"<nil>"/"None" as 0.
func parseFloat(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "<nil>" || s == "None" {
		return 0
	}

	v, _ := strconv.ParseFloat(s, 64)

	return v
}

// parseInt converts a string to int64, treating empty/"<nil>"/"None" as 0.
// It handles float strings like "1.23e6" by parsing as float first.
func parseInt(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" || s == "<nil>" || s == "None" {
		return 0
	}

	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}

	return int64(f)
}

// mapRowToSharadarMetric maps a CSV/parquet row to a sharadarMetric struct.
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

// mapRowToSharadarFundamental maps a CSV/parquet row to a sharadarFundamental struct.
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

// mapRowToSharadarTicker maps a CSV/parquet row to a sharadarTicker struct.
func mapRowToSharadarTicker(row map[string]string) *sharadarTicker {
	t := &sharadarTicker{
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

	if s := strings.TrimSpace(row["lastupdated"]); s != "" {
		if parsed, err := time.Parse("2006-01-02", s); err == nil {
			t.LastUpdated = parsed
		}
	}

	if s := strings.TrimSpace(row["firstadded"]); s != "" {
		if parsed, err := time.Parse("2006-01-02", s); err == nil {
			t.FirstAdded = parsed
		}
	}

	if s := strings.TrimSpace(row["firstpricedate"]); s != "" {
		if parsed, err := time.Parse("2006-01-02", s); err == nil {
			t.FirstPriceDate = parsed
		}
	}

	if s := strings.TrimSpace(row["lastpricedate"]); s != "" {
		if parsed, err := time.Parse("2006-01-02", s); err == nil {
			t.LastPriceDate = parsed
		}
	}

	return t
}

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
		case "SP500":
			n, err = importSP500Rows(ctx, sub, rows, out)
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

// importFundamentalsRows processes rows for the Fundamentals dataset.
func importFundamentalsRows(ctx context.Context, sub *library.Subscription, rows []map[string]string, out chan<- *data.Observation) (int, error) {
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
			logger.Info().Int("rows_processed", i+1).Int("total_rows", len(rows)).Msg("fundamentals import progress")
		}
	}

	return count, nil
}

// importMetricsRows processes rows for the Metrics dataset.
func importMetricsRows(ctx context.Context, sub *library.Subscription, rows []map[string]string, out chan<- *data.Observation) (int, error) {
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
			logger.Info().Int("rows_processed", i+1).Int("total_rows", len(rows)).Msg("metrics import progress")
		}
	}

	return count, nil
}

// importTickersRows processes rows for the Stock Tickers dataset.
func importTickersRows(ctx context.Context, sub *library.Subscription, rows []map[string]string, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)

	enrichAssets := make([]*data.Asset, 0, 5000)
	allAssets := make([]*data.Asset, 0, len(rows))

	for _, row := range rows {
		// Only process SF1 table rows
		if row["table"] != "SF1" {
			continue
		}

		ticker := mapRowToSharadarTicker(row)

		if ticker.Exchange == "" {
			continue
		}

		pvAsset := ticker.ToAsset()

		// Ignore unknown assets or exchanges
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

		// Batch FIGI enrichment every 5000 active assets
		if len(enrichAssets) >= 5000 {
			log.Info().Int("batch_size", len(enrichAssets)).Msg("enriching asset batch with FIGI")
			figi.Enrich(enrichAssets...)
			enrichAssets = enrichAssets[:0]
		}
	}

	// Enrich any remaining active assets
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

const sp500IndexTicker = "SPX"

// importSP500Rows processes rows for the SP500 dataset.
// Rows with action "current" or "historical" are grouped by date into IndexSnapshots.
// Rows with action "added" or "removed" become IndexChange observations.
func importSP500Rows(ctx context.Context, sub *library.Subscription, rows []map[string]string, out chan<- *data.Observation) (int, error) {
	logger := zerolog.Ctx(ctx)

	conn, err := sub.Library.Pool.Acquire(ctx)
	if err != nil {
		return 0, fmt.Errorf("could not acquire database connection: %w", err)
	}
	defer conn.Release()

	// Step 1: Load all assets (active + delisted) for FIGI map
	assets, err := data.AllAssets(ctx, conn)
	if err != nil {
		return 0, fmt.Errorf("could not load assets: %w", err)
	}

	figiMap := make(map[string]string, len(assets))
	for _, asset := range assets {
		if asset.CompositeFigi != "" {
			figiMap[asset.Ticker] = asset.CompositeFigi
		}
	}

	// Step 2: Collect all unique tickers from the data
	uniqueTickers := make(map[string]string) // ticker -> name

	for _, row := range rows {
		ticker := strings.TrimSpace(row["ticker"])

		name := strings.TrimSpace(row["name"])
		if ticker != "" {
			uniqueTickers[ticker] = name
		}
	}

	// Step 3: Find tickers missing from FIGI map and resolve via OpenFIGI (including delisted)
	var unresolvedAssets []*data.Asset

	for ticker := range uniqueTickers {
		if figiMap[ticker] == "" {
			unresolvedAssets = append(unresolvedAssets, &data.Asset{Ticker: ticker})
		}
	}

	if len(unresolvedAssets) > 0 {
		logger.Info().Int("count", len(unresolvedAssets)).Msg("resolving unmatched tickers via OpenFIGI (including delisted)")

		rateLimiter := figi.RateLimit()
		figiResults := figi.LookupFigiUnlisted(unresolvedAssets, rateLimiter)

		for _, asset := range unresolvedAssets {
			if result, ok := figiResults[asset.Ticker]; ok && result.CompositeFIGI != "" {
				figiMap[asset.Ticker] = result.CompositeFIGI
			}
		}
	}

	count := 0

	// Step 4: Generate synthetic FIGIs for anything still unresolved, emit as assets
	for ticker, name := range uniqueTickers {
		if figiMap[ticker] == "" {
			syntheticFigi := figi.GenerateSyntheticFIGI(ticker, name)
			figiMap[ticker] = syntheticFigi

			logger.Info().Str("ticker", ticker).Str("name", name).Str("figi", syntheticFigi).Msg("generated synthetic FIGI")

			out <- &data.Observation{
				AssetObject: &data.Asset{
					Ticker:        ticker,
					Name:          name,
					CompositeFigi: syntheticFigi,
					Active:        false,
				},
				ObservationDate:  time.Now(),
				SubscriptionID:   sub.ID,
				SubscriptionName: sub.Name,
			}

			count++
		}
	}

	// Find the earliest "historical" snapshot date -- this is the first date
	// where the source data is known to be complete. Data before this date
	// is incomplete and must not be imported.
	var earliestHistoricalDate string

	for _, row := range rows {
		if strings.TrimSpace(row["action"]) == "historical" {
			dateStr := strings.TrimSpace(row["date"])
			if earliestHistoricalDate == "" || dateStr < earliestHistoricalDate {
				earliestHistoricalDate = dateStr
			}
		}
	}

	if earliestHistoricalDate == "" {
		return 0, fmt.Errorf("no historical snapshot rows found in source data")
	}

	logger.Info().Str("earliest_historical", earliestHistoricalDate).Msg("using earliest historical snapshot as baseline")

	// Seed membership from the earliest historical snapshot
	membership := make(map[string]string) // ticker -> compositeFigi

	for _, row := range rows {
		if strings.TrimSpace(row["action"]) == "historical" && strings.TrimSpace(row["date"]) == earliestHistoricalDate {
			ticker := strings.TrimSpace(row["ticker"])
			if ticker != "" {
				membership[ticker] = figiMap[ticker]
			}
		}
	}

	// Collect changelog rows on or after the earliest historical date
	var changes []*data.IndexChange

	for _, row := range rows {
		action := strings.TrimSpace(row["action"])
		ticker := strings.TrimSpace(row["ticker"])
		dateStr := strings.TrimSpace(row["date"])

		if ticker == "" || dateStr == "" || dateStr <= earliestHistoricalDate {
			continue
		}

		switch action {
		case "added":
			eventDate, err := time.Parse("2006-01-02", dateStr)
			if err != nil {
				logger.Warn().Str("date", dateStr).Str("ticker", ticker).Msg("skipping added row with invalid date")
				continue
			}

			changes = append(changes, &data.IndexChange{
				Ticker:        ticker,
				CompositeFigi: figiMap[ticker],
				IndexTicker:   sp500IndexTicker,
				EventDate:     eventDate,
				Action:        "add",
			})
		case "removed":
			eventDate, err := time.Parse("2006-01-02", dateStr)
			if err != nil {
				logger.Warn().Str("date", dateStr).Str("ticker", ticker).Msg("skipping removed row with invalid date")
				continue
			}

			changes = append(changes, &data.IndexChange{
				Ticker:        ticker,
				CompositeFigi: figiMap[ticker],
				IndexTicker:   sp500IndexTicker,
				EventDate:     eventDate,
				Action:        "remove",
			})
		}
	}

	// Sort changes by date for replay
	sort.Slice(changes, func(i, j int) bool {
		return changes[i].EventDate.Before(changes[j].EventDate)
	})

	// Emit yearly snapshots by replaying changes forward from the baseline
	snapshotTable := sub.DataTablesMap[data.IndexSnapshotKey]
	lastSnapshotDate := provider.LastSnapshotDate(ctx, sub.Library.Pool, snapshotTable, sp500IndexTicker)

	baselineDate, _ := time.Parse("2006-01-02", earliestHistoricalDate)
	changeIdx := 0

	// Emit the baseline snapshot first if due
	if provider.ShouldTakeSnapshot(lastSnapshotDate, baselineDate, "yearly") {
		constituents := make([]data.IndexConstituent, 0, len(membership))
		for ticker, compositeFigi := range membership {
			constituents = append(constituents, data.IndexConstituent{
				Ticker:        ticker,
				CompositeFigi: compositeFigi,
			})
		}

		sort.Slice(constituents, func(i, j int) bool {
			return constituents[i].Ticker < constituents[j].Ticker
		})

		out <- &data.Observation{
			IndexSnapshot: &data.IndexSnapshot{
				IndexTicker:  sp500IndexTicker,
				SnapshotDate: baselineDate,
				Constituents: constituents,
			},
			ObservationDate:  time.Now(),
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}

		lastSnapshotDate = baselineDate
		count++
	}

	// Walk year by year from baseline+1 through last change
	if len(changes) > 0 {
		lastChangeDate := changes[len(changes)-1].EventDate
		snapshotDate := baselineDate.AddDate(1, 0, 0)

		for !snapshotDate.After(lastChangeDate) {
			// Apply all changes up to and including this snapshot date
			for changeIdx < len(changes) && !changes[changeIdx].EventDate.After(snapshotDate) {
				ch := changes[changeIdx]
				switch ch.Action {
				case "add":
					membership[ch.Ticker] = ch.CompositeFigi
				case "remove":
					delete(membership, ch.Ticker)
				}

				changeIdx++
			}

			if provider.ShouldTakeSnapshot(lastSnapshotDate, snapshotDate, "yearly") {
				constituents := make([]data.IndexConstituent, 0, len(membership))
				for ticker, compositeFigi := range membership {
					constituents = append(constituents, data.IndexConstituent{
						Ticker:        ticker,
						CompositeFigi: compositeFigi,
					})
				}

				sort.Slice(constituents, func(i, j int) bool {
					return constituents[i].Ticker < constituents[j].Ticker
				})

				out <- &data.Observation{
					IndexSnapshot: &data.IndexSnapshot{
						IndexTicker:  sp500IndexTicker,
						SnapshotDate: snapshotDate,
						Constituents: constituents,
					},
					ObservationDate:  time.Now(),
					SubscriptionID:   sub.ID,
					SubscriptionName: sub.Name,
				}

				lastSnapshotDate = snapshotDate
				count++
			}

			snapshotDate = snapshotDate.AddDate(1, 0, 0)
		}
	}

	// Emit changelog entries
	for _, change := range changes {
		out <- &data.Observation{
			IndexChange:      change,
			ObservationDate:  time.Now(),
			SubscriptionID:   sub.ID,
			SubscriptionName: sub.Name,
		}

		count++
	}

	logger.Info().
		Int("changes", len(changes)).
		Int("total_observations", count).
		Msg("SP500 import complete")

	return count, nil
}
