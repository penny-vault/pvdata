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
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/rs/zerolog"
)

var _ = Describe("parseISharesCSV", func() {
	sampleCSV := []byte("\xef\xbb\xbf" + `iShares Russell 1000 Value ETF
Fund Holdings as of,"Mar 05, 2026"
Inception Date,"May 22, 2000"
Shares Outstanding,"188,400,000.00"
Stock,"-"
Bond,"-"
Cash,"-"
Other,"-"

Ticker,Name,Sector,Asset Class,Market Value,Weight (%),Notional Value,Quantity,Price,Location,Exchange,Currency,FX Rate,Market Currency,Accrual Date
"AAPL","APPLE INC","Information Technology","Equity","500,000,000.00","5.25","500,000,000.00","2,000,000.00","250.00","United States","NASDAQ","USD","1.00","USD","-"
"CASH","CASH COLLATERAL","-","Cash and/or Derivatives","100,000.00","0.01","100,000.00","100,000.00","1.00","United States","-","USD","1.00","USD","-"
"MSFT","MICROSOFT CORP","Information Technology","Equity","400,000,000.00","4.20","400,000,000.00","1,000,000.00","400.00","United States","NASDAQ","USD","1.00","USD","-"
`)

	It("parses holdings from CSV", func() {
		result, err := parseISharesCSV(sampleCSV)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Holdings).To(HaveLen(2))
	})

	It("extracts the snapshot date", func() {
		result, err := parseISharesCSV(sampleCSV)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.SnapshotDate.Year()).To(Equal(2026))
		Expect(result.SnapshotDate.Month()).To(Equal(time.March))
		Expect(result.SnapshotDate.Day()).To(Equal(5))
	})

	It("extracts ticker and weight", func() {
		result, err := parseISharesCSV(sampleCSV)
		Expect(err).ToNot(HaveOccurred())
		var aapl *iSharesHolding
		for _, h := range result.Holdings {
			if h.Ticker == "AAPL" {
				aapl = &h
				break
			}
		}
		Expect(aapl).ToNot(BeNil())
		Expect(aapl.Weight).To(BeNumerically("~", 0.0525, 0.0001))
	})

	It("filters out non-equity holdings", func() {
		result, err := parseISharesCSV(sampleCSV)
		Expect(err).ToNot(HaveOccurred())
		for _, h := range result.Holdings {
			Expect(h.Ticker).ToNot(Equal("CASH"))
		}
	})

	It("handles weight values with commas", func() {
		csv := []byte(`iShares Test ETF
Fund Holdings as of,"Jan 15, 2026"
Inception Date,"Jan 01, 2020"

Ticker,Name,Sector,Asset Class,Market Value,Weight (%),Notional Value,Quantity,Price,Location,Exchange,Currency,FX Rate,Market Currency,Accrual Date
"BIG","BIG CORP","Financials","Equity","1,234,567,890.00","1,234.56","1,234,567,890.00","100,000.00","12,345.68","United States","NYSE","USD","1.00","USD","-"
`)
		result, err := parseISharesCSV(csv)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Holdings).To(HaveLen(1))
		Expect(result.Holdings[0].Weight).To(BeNumerically("~", 12.3456, 0.0001))
	})

	It("returns empty holdings when all rows are non-equity", func() {
		csv := []byte(`iShares Bond ETF
Fund Holdings as of,"Feb 10, 2026"
Inception Date,"Jan 01, 2020"

Ticker,Name,Sector,Asset Class,Market Value,Weight (%),Notional Value,Quantity,Price,Location,Exchange,Currency,FX Rate,Market Currency,Accrual Date
"BOND1","SOME BOND","-","Fixed Income","100,000.00","50.00","100,000.00","100.00","1,000.00","United States","NYSE","USD","1.00","USD","-"
"CASH","CASH","-","Cash and/or Derivatives","50,000.00","25.00","50,000.00","50,000.00","1.00","United States","-","USD","1.00","USD","-"
"FUT","FUTURES","-","Futures","25,000.00","25.00","25,000.00","10.00","2,500.00","United States","-","USD","1.00","USD","-"
`)
		result, err := parseISharesCSV(csv)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Holdings).To(BeEmpty())
		Expect(result.SnapshotDate.Day()).To(Equal(10))
	})

	It("returns zero date when Fund Holdings as of row is missing", func() {
		csv := []byte(`iShares Mystery ETF
Inception Date,"Jan 01, 2020"

Ticker,Name,Sector,Asset Class,Market Value,Weight (%),Notional Value,Quantity,Price,Location,Exchange,Currency,FX Rate,Market Currency,Accrual Date
"FOO","FOO INC","Tech","Equity","100.00","100.00","100.00","1.00","100.00","United States","NYSE","USD","1.00","USD","-"
`)
		result, err := parseISharesCSV(csv)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.SnapshotDate.IsZero()).To(BeTrue())
		Expect(result.Holdings).To(HaveLen(1))
	})

	It("returns empty holdings when header row is missing", func() {
		csv := []byte(`iShares Broken ETF
Fund Holdings as of,"Mar 01, 2026"
some random data here
`)
		result, err := parseISharesCSV(csv)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Holdings).To(BeEmpty())
		Expect(result.SnapshotDate.Month()).To(Equal(time.March))
	})

	It("parses CSV without BOM", func() {
		csv := []byte(`iShares No BOM ETF
Fund Holdings as of,"Apr 20, 2026"
Inception Date,"Jan 01, 2020"

Ticker,Name,Sector,Asset Class,Market Value,Weight (%),Notional Value,Quantity,Price,Location,Exchange,Currency,FX Rate,Market Currency,Accrual Date
"ZZZ","ZZZ CORP","Industrials","Equity","1,000.00","99.50","1,000.00","10.00","100.00","United States","NYSE","USD","1.00","USD","-"
`)
		result, err := parseISharesCSV(csv)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Holdings).To(HaveLen(1))
		Expect(result.Holdings[0].Ticker).To(Equal("ZZZ"))
		Expect(result.SnapshotDate.Month()).To(Equal(time.April))
	})

	It("skips rows with empty ticker", func() {
		csv := []byte(`iShares Test ETF
Fund Holdings as of,"May 05, 2026"
Inception Date,"Jan 01, 2020"

Ticker,Name,Sector,Asset Class,Market Value,Weight (%),Notional Value,Quantity,Price,Location,Exchange,Currency,FX Rate,Market Currency,Accrual Date
"REAL","REAL CORP","Tech","Equity","100.00","60.00","100.00","1.00","100.00","United States","NYSE","USD","1.00","USD","-"
"","","","Equity","50.00","30.00","50.00","1.00","50.00","United States","","USD","1.00","USD","-"
" ","WHITESPACE","","Equity","25.00","10.00","25.00","1.00","25.00","United States","","USD","1.00","USD","-"
`)
		result, err := parseISharesCSV(csv)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Holdings).To(HaveLen(1))
		Expect(result.Holdings[0].Ticker).To(Equal("REAL"))
	})

	It("extracts holding name", func() {
		result, err := parseISharesCSV(sampleCSV)
		Expect(err).ToNot(HaveOccurred())
		var aapl *iSharesHolding
		for _, h := range result.Holdings {
			if h.Ticker == "AAPL" {
				aapl = &h
				break
			}
		}
		Expect(aapl).ToNot(BeNil())
		Expect(aapl.Name).To(Equal("APPLE INC"))
	})

	It("filters out dash ticker", func() {
		csv := []byte(`iShares Test ETF
Fund Holdings as of,"Jun 01, 2026"
Inception Date,"Jan 01, 2020"

Ticker,Name,Sector,Asset Class,Market Value,Weight (%),Notional Value,Quantity,Price,Location,Exchange,Currency,FX Rate,Market Currency,Accrual Date
"OK","OK CORP","Tech","Equity","100.00","5.50","100.00","1.00","100.00","United States","NYSE","USD","1.00","USD","-"
"-","BLACKROCK CASH","","Equity","50.00","0.01","50.00","50000.00","1.00","United States","-","USD","1.00","USD","-"
`)
		result, err := parseISharesCSV(csv)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Holdings).To(HaveLen(1))
		Expect(result.Holdings[0].Ticker).To(Equal("OK"))
	})

	It("skips rows with unparseable weight", func() {
		csv := []byte(`iShares Test ETF
Fund Holdings as of,"Jun 01, 2026"
Inception Date,"Jan 01, 2020"

Ticker,Name,Sector,Asset Class,Market Value,Weight (%),Notional Value,Quantity,Price,Location,Exchange,Currency,FX Rate,Market Currency,Accrual Date
"OK","OK CORP","Tech","Equity","100.00","5.50","100.00","1.00","100.00","United States","NYSE","USD","1.00","USD","-"
"BAD","BAD CORP","Tech","Equity","50.00","N/A","50.00","1.00","50.00","United States","NYSE","USD","1.00","USD","-"
"ALSO","ALSO BAD","Tech","Equity","25.00","-","25.00","1.00","25.00","United States","NYSE","USD","1.00","USD","-"
`)
		result, err := parseISharesCSV(csv)
		Expect(err).ToNot(HaveOccurred())
		Expect(result.Holdings).To(HaveLen(1))
		Expect(result.Holdings[0].Ticker).To(Equal("OK"))
	})
})

var _ = Describe("ResolveShareClass", func() {
	var (
		figiMap      map[string]string
		assetNameMap map[string]string
		logger       zerolog.Logger
	)

	BeforeEach(func() {
		figiMap = map[string]string{
			"BRK/B": "BBG000DWG505",
			"BF/B":  "BBG000BFCQN2",
			"MOG/A": "BBG000BNNKG9",
		}
		assetNameMap = map[string]string{
			"BRK/B": "Berkshire Hathaway Inc",
			"BF/B":  "Brown-Forman Corp",
			"MOG/A": "Moog Inc",
		}
		logger = zerolog.Nop()
	})

	It("resolves BRKB to BRK/B when names match", func() {
		resolved := ResolveShareClass("BRKB", "BERKSHIRE HATHAWAY INC CLASS B", figiMap, assetNameMap, &logger)
		Expect(resolved).To(BeTrue())
		Expect(figiMap["BRKB"]).To(Equal("BBG000DWG505"))
	})

	It("resolves BFB to BF/B when names match", func() {
		resolved := ResolveShareClass("BFB", "BROWN FORMAN CORP CLASS B", figiMap, assetNameMap, &logger)
		Expect(resolved).To(BeTrue())
		Expect(figiMap["BFB"]).To(Equal("BBG000BFCQN2"))
	})

	It("resolves MOGA to MOG/A when names match", func() {
		resolved := ResolveShareClass("MOGA", "MOOG INC CLASS A", figiMap, assetNameMap, &logger)
		Expect(resolved).To(BeTrue())
		Expect(figiMap["MOGA"]).To(Equal("BBG000BNNKG9"))
	})

	It("rejects when names do not match", func() {
		resolved := ResolveShareClass("BRKB", "TOTALLY DIFFERENT COMPANY", figiMap, assetNameMap, &logger)
		Expect(resolved).To(BeFalse())
		Expect(figiMap).ToNot(HaveKey("BRKB"))
	})

	It("rejects tickers not ending in A or B", func() {
		resolved := ResolveShareClass("AAPL", "APPLE INC", figiMap, assetNameMap, &logger)
		Expect(resolved).To(BeFalse())
	})

	It("rejects when candidate ticker does not exist", func() {
		resolved := ResolveShareClass("XYZB", "XYZ CORP CLASS B", figiMap, assetNameMap, &logger)
		Expect(resolved).To(BeFalse())
	})

	It("rejects when holding name is empty", func() {
		resolved := ResolveShareClass("BRKB", "", figiMap, assetNameMap, &logger)
		Expect(resolved).To(BeFalse())
	})

	It("rejects single-character tickers", func() {
		resolved := ResolveShareClass("B", "SOMETHING", figiMap, assetNameMap, &logger)
		Expect(resolved).To(BeFalse())
	})
})
