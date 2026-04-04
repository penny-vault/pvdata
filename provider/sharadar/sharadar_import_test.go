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
	"os"
	"path/filepath"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("DetectSharadarDataset", func() {
	It("detects Fundamentals from sf1 filename", func() {
		dataset, err := DetectSharadarDataset("SHARADAR_SF1_20231228.parquet")
		Expect(err).NotTo(HaveOccurred())
		Expect(dataset).To(Equal("Fundamentals"))
	})

	It("detects Fundamentals case-insensitively", func() {
		dataset, err := DetectSharadarDataset("sharadar_SF1_data.csv")
		Expect(err).NotTo(HaveOccurred())
		Expect(dataset).To(Equal("Fundamentals"))
	})

	It("detects Metrics from metrics filename", func() {
		dataset, err := DetectSharadarDataset("sharadar_metrics_20231228.parquet")
		Expect(err).NotTo(HaveOccurred())
		Expect(dataset).To(Equal("Metrics"))
	})

	It("detects Metrics from daily filename", func() {
		dataset, err := DetectSharadarDataset("sharadar_daily_20231228.csv")
		Expect(err).NotTo(HaveOccurred())
		Expect(dataset).To(Equal("Metrics"))
	})

	It("detects Stock Tickers from tickers filename", func() {
		dataset, err := DetectSharadarDataset("sharadar_tickers.parquet")
		Expect(err).NotTo(HaveOccurred())
		Expect(dataset).To(Equal("Stock Tickers"))
	})

	It("detects Stock Tickers case-insensitively", func() {
		dataset, err := DetectSharadarDataset("SHARADAR_TICKERS_20231228.csv")
		Expect(err).NotTo(HaveOccurred())
		Expect(dataset).To(Equal("Stock Tickers"))
	})

	It("handles full paths by using only the base filename", func() {
		dataset, err := DetectSharadarDataset("/home/user/downloads/SHARADAR_SF1_20231228.parquet")
		Expect(err).NotTo(HaveOccurred())
		Expect(dataset).To(Equal("Fundamentals"))
	})

	It("detects SP500 from sp500 filename", func() {
		dataset, err := DetectSharadarDataset("sharadar_sp500_20231228.parquet")
		Expect(err).NotTo(HaveOccurred())
		Expect(dataset).To(Equal("SP500"))
	})

	It("detects SP500 case-insensitively", func() {
		dataset, err := DetectSharadarDataset("SHARADAR_SP500_data.csv")
		Expect(err).NotTo(HaveOccurred())
		Expect(dataset).To(Equal("SP500"))
	})

	It("returns error for unrecognized filenames", func() {
		_, err := DetectSharadarDataset("unknown_data.parquet")
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("detectFileFormat", func() {
	It("detects parquet format", func() {
		format, err := detectFileFormat("data.parquet")
		Expect(err).NotTo(HaveOccurred())
		Expect(format).To(Equal(fileFormatParquet))
	})

	It("detects CSV format", func() {
		format, err := detectFileFormat("data.csv")
		Expect(err).NotTo(HaveOccurred())
		Expect(format).To(Equal(fileFormatCSV))
	})

	It("detects CSV.zst format", func() {
		format, err := detectFileFormat("data.csv.zst")
		Expect(err).NotTo(HaveOccurred())
		Expect(format).To(Equal(fileFormatCSVZst))
	})

	It("detects CSV.zip format", func() {
		format, err := detectFileFormat("data.csv.zip")
		Expect(err).NotTo(HaveOccurred())
		Expect(format).To(Equal(fileFormatCSVZip))
	})

	It("handles uppercase extensions", func() {
		format, err := detectFileFormat("data.PARQUET")
		Expect(err).NotTo(HaveOccurred())
		Expect(format).To(Equal(fileFormatParquet))
	})

	It("returns error for unsupported format", func() {
		_, err := detectFileFormat("data.xlsx")
		Expect(err).To(HaveOccurred())
	})
})

var _ = Describe("readCSVRows", func() {
	It("reads CSV rows into maps with lowercase headers", func() {
		dir := GinkgoT().TempDir()
		path := filepath.Join(dir, "test.csv")

		content := "Ticker,Date,Value\nAAPL,2023-01-01,150.00\nMSFT,2023-01-02,250.00\n"
		Expect(os.WriteFile(path, []byte(content), 0600)).To(Succeed())

		rows, err := readCSVRows(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(HaveLen(2))
		Expect(rows[0]["ticker"]).To(Equal("AAPL"))
		Expect(rows[0]["date"]).To(Equal("2023-01-01"))
		Expect(rows[0]["value"]).To(Equal("150.00"))
		Expect(rows[1]["ticker"]).To(Equal("MSFT"))
	})

	It("returns empty slice for CSV with only headers", func() {
		dir := GinkgoT().TempDir()
		path := filepath.Join(dir, "empty.csv")

		Expect(os.WriteFile(path, []byte("Ticker,Date\n"), 0600)).To(Succeed())

		rows, err := readCSVRows(path)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).To(BeEmpty())
	})
})

var _ = Describe("readParquetRows", func() {
	It("reads parquet rows if test file exists", func() {
		parquetPath := "../../data/sharadar/sharadar_metrics_20231228.parquet"
		if _, err := os.Stat(parquetPath); os.IsNotExist(err) {
			Skip("test parquet file not available")
		}

		rows, err := readParquetRows(parquetPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).NotTo(BeEmpty())
		// Each row should have a ticker key
		Expect(rows[0]).To(HaveKey("ticker"))
	})

	It("reads SP500 parquet rows if test file exists", func() {
		parquetPath := "../../data/sharadar/sharadar_sp500_20231228.parquet"
		if _, err := os.Stat(parquetPath); os.IsNotExist(err) {
			Skip("test parquet file not available")
		}

		rows, err := readParquetRows(parquetPath)
		Expect(err).NotTo(HaveOccurred())
		Expect(rows).NotTo(BeEmpty())
		Expect(rows[0]).To(HaveKey("ticker"))
		Expect(rows[0]).To(HaveKey("action"))
		Expect(rows[0]).To(HaveKey("date"))
	})
})

var _ = Describe("mapRowToSharadarMetric", func() {
	It("maps a row to a sharadarMetric struct", func() {
		row := map[string]string{
			"ticker":      "AAPL",
			"date":        "2023-01-01",
			"lastupdated": "2023-01-02",
			"ev":          "2500000000",
			"evebit":      "25.5",
			"evebitda":    "20.1",
			"marketcap":   "2000000000",
			"pb":          "3.5",
			"pe":          "28.0",
			"ps":          "7.2",
		}

		metric := mapRowToSharadarMetric(row)
		Expect(metric).NotTo(BeNil())
		Expect(metric.Ticker).To(Equal("AAPL"))
		Expect(metric.Date).To(Equal("2023-01-01"))
		Expect(metric.LastUpdated).To(Equal("2023-01-02"))
		Expect(metric.EV).To(BeNumerically("~", 2500000000, 1))
		Expect(metric.EVtoEBIT).To(BeNumerically("~", 25.5, 0.001))
		Expect(metric.EVtoEBITDA).To(BeNumerically("~", 20.1, 0.001))
		Expect(metric.MarketCap).To(BeNumerically("~", 2000000000, 1))
		Expect(metric.PB).To(BeNumerically("~", 3.5, 0.001))
		Expect(metric.PE).To(BeNumerically("~", 28.0, 0.001))
		Expect(metric.PS).To(BeNumerically("~", 7.2, 0.001))
	})

	It("handles empty/nil fields gracefully", func() {
		row := map[string]string{
			"ticker": "AAPL",
			"date":   "2023-01-01",
			"ev":     "",
			"pe":     "<nil>",
			"ps":     "None",
		}

		metric := mapRowToSharadarMetric(row)
		Expect(metric).NotTo(BeNil())
		Expect(metric.EV).To(BeZero())
		Expect(metric.PE).To(BeZero())
		Expect(metric.PS).To(BeZero())
	})
})

var _ = Describe("mapRowToSharadarFundamental", func() {
	It("maps a row to a sharadarFundamental struct", func() {
		row := map[string]string{
			"ticker":       "AAPL",
			"dimension":    "ARQ",
			"calendardate": "2023-09-30",
			"datekey":      "2023-11-03",
			"reportperiod": "2023-09-30",
			"lastupdated":  "2023-11-05",
			"accoci":       "-12000000",
			"assets":       "352755000000",
			"revenue":      "89498000000",
			"netinc":       "22956000000",
			"eps":          "1.46",
			"pe":           "28.5",
			"pb":           "44.2",
		}

		fundamental := mapRowToSharadarFundamental(row)
		Expect(fundamental).NotTo(BeNil())
		Expect(fundamental.Ticker).To(Equal("AAPL"))
		Expect(fundamental.Dimension).To(Equal("ARQ"))
		Expect(fundamental.CalendarDate).To(Equal("2023-09-30"))
		Expect(fundamental.AccumulatedOtherComprehensiveIncome).To(Equal(int64(-12000000)))
		Expect(fundamental.TotalAssets).To(Equal(int64(352755000000)))
		Expect(fundamental.Revenues).To(Equal(int64(89498000000)))
		Expect(fundamental.NetIncome).To(Equal(int64(22956000000)))
		Expect(fundamental.EPS).To(BeNumerically("~", 1.46, 0.001))
		Expect(fundamental.PE).To(BeNumerically("~", 28.5, 0.001))
		Expect(fundamental.PB).To(BeNumerically("~", 44.2, 0.001))
	})
})

var _ = Describe("mapRowToSharadarTicker", func() {
	It("maps a row to a sharadarTicker struct", func() {
		row := map[string]string{
			"permaticker":    "199059",
			"ticker":         "AAPL",
			"name":           "Apple Inc",
			"exchange":       "NASDAQ",
			"isdelisted":     "N",
			"category":       "Domestic Common Stock",
			"cusips":         "037833100",
			"siccode":        "3571",
			"sicsector":      "Technology",
			"sicindustry":    "Electronic Computers",
			"famasector":     "",
			"famaindustry":   "Computers",
			"sector":         "Technology",
			"industry":       "Consumer Electronics",
			"scalemarketcap": "6 - Mega",
			"scalerevenue":   "6 - Mega",
			"relatedtickers": "",
			"currency":       "USD",
			"location":       "California; US",
			"lastupdated":    "2023-12-28",
			"firstadded":     "2014-09-24",
			"firstpricedate": "1980-12-12",
			"lastpricedate":  "2023-12-28",
			"firstquarter":   "1982-12-31",
			"lastquarter":    "2023-09-30",
			"secfilings":     "https://www.sec.gov/cgi-bin/browse-edgar?action=getcompany&CIK=0000320193",
			"companysite":    "https://www.apple.com",
		}

		ticker := mapRowToSharadarTicker(row)
		Expect(ticker).NotTo(BeNil())
		Expect(ticker.PermaTicker).To(Equal(int64(199059)))
		Expect(ticker.Ticker).To(Equal("AAPL"))
		Expect(ticker.Name).To(Equal("Apple Inc"))
		Expect(ticker.Exchange).To(Equal("NASDAQ"))
		Expect(ticker.IsDelisted).To(Equal("N"))
		Expect(ticker.Category).To(Equal("Domestic Common Stock"))
		Expect(ticker.CUSIPs).To(Equal("037833100"))
		Expect(ticker.SICCode).To(Equal(int64(3571)))
		Expect(ticker.Currency).To(Equal("USD"))
		Expect(ticker.LastUpdated).To(Equal(time.Date(2023, 12, 28, 0, 0, 0, 0, time.UTC)))
		Expect(ticker.FirstAdded).To(Equal(time.Date(2014, 9, 24, 0, 0, 0, 0, time.UTC)))
		Expect(ticker.FirstPriceDate).To(Equal(time.Date(1980, 12, 12, 0, 0, 0, 0, time.UTC)))
		Expect(ticker.LastPriceDate).To(Equal(time.Date(2023, 12, 28, 0, 0, 0, 0, time.UTC)))
		Expect(ticker.SECFilings).To(ContainSubstring("CIK=0000320193"))
		Expect(ticker.CompanySite).To(Equal("https://www.apple.com"))
	})

	It("handles empty date fields gracefully", func() {
		row := map[string]string{
			"ticker":      "TEST",
			"lastupdated": "",
			"firstadded":  "",
		}

		ticker := mapRowToSharadarTicker(row)
		Expect(ticker).NotTo(BeNil())
		Expect(ticker.LastUpdated).To(Equal(time.Time{}))
		Expect(ticker.FirstAdded).To(Equal(time.Time{}))
	})
})
