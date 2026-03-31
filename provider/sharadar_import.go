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
package provider

import (
	"archive/zip"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/klauspost/compress/zstd"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/figi"
	"github.com/penny-vault/pvdata/library"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/xitongsys/parquet-go-source/local"
	"github.com/xitongsys/parquet-go/reader"
)

// fileFormat represents the format of a data file.
type fileFormat int

const (
	fileFormatParquet fileFormat = iota
	fileFormatCSV
	fileFormatCSVZst
	fileFormatCSVZip
)

// DetectSharadarDataset returns the Sharadar dataset name based on the filename.
// It does a case-insensitive match on the base filename.
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
		return "", fmt.Errorf("cannot detect Sharadar dataset from filename: %q", path)
	}
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
				row[name] = fmt.Sprintf("%v", val.Field(j).Interface())
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
