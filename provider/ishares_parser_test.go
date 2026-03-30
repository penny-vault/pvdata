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
})
