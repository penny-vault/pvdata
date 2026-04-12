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

package sec

import (
	"math"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
)

var _ = Describe("EnrichMarketData", func() {
	var (
		dateKey   time.Time
		eventDate time.Time
	)

	BeforeEach(func() {
		dateKey = time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC)
		eventDate = time.Date(2024, 4, 15, 0, 0, 0, 0, time.UTC)
	})

	stubPriceFn := func(price float64) PriceLookupFn {
		return func(compositeFigi string, eventDate time.Time) float64 {
			return price
		}
	}

	Describe("All 14 fields on ART", func() {
		It("computes all market-data fields correctly for an ART fundamental", func() {
			f := &data.Fundamental{
				CompositeFigi:                "BBG000B9XRY4",
				Dimension:                    "ART",
				DateKey:                      dateKey,
				EventDate:                    eventDate,
				SharesBasic:                  int64(1_000_000),
				TotalDebt:                    int64(500_000_000),
				CashAndEquivalents:           int64(200_000_000),
				Equity:                       int64(2_000_000_000),
				NetIncomeCommonStock:         int64(100_000_000),
				Revenues:                     int64(800_000_000),
				EBIT:                         int64(120_000_000),
				EBITDA:                       int64(150_000_000),
				EPS:                          2.50,
				EPSDiluted:                   2.40,
				SalesPerShare:                8.00,
				DividendsPerBasicCommonShare: 1.20,
			}

			price := 236.0
			EnrichMarketData([]*data.Fundamental{f}, stubPriceFn(price))

			expectedMktCap := int64(price * float64(f.SharesBasic)) // 236,000,000
			expectedEV := expectedMktCap + f.TotalDebt - f.CashAndEquivalents
			// 236_000_000 + 500_000_000 - 200_000_000 = 536_000_000

			Expect(f.Price).To(BeNumerically("~", price, 1e-9))
			Expect(f.ShareFactor).To(BeNumerically("~", 1.0, 1e-9))
			Expect(f.FxUSD).To(BeNumerically("~", 1.0, 1e-9))
			Expect(f.MarketCapitalization).To(Equal(expectedMktCap))
			Expect(f.EnterpriseValue).To(Equal(expectedEV))

			expectedPB := float64(expectedMktCap) / float64(f.Equity)
			Expect(f.PB).To(BeNumerically("~", expectedPB, 1e-9))

			expectedPE := float64(expectedMktCap) / float64(f.NetIncomeCommonStock)
			Expect(f.PE).To(BeNumerically("~", expectedPE, 1e-9))

			expectedPS := float64(expectedMktCap) / float64(f.Revenues)
			Expect(f.PS).To(BeNumerically("~", expectedPS, 1e-9))

			expectedPE1 := price / f.EPS
			Expect(f.PE1).To(BeNumerically("~", expectedPE1, 1e-9))

			expectedPS1 := price / f.SalesPerShare
			Expect(f.PS1).To(BeNumerically("~", expectedPS1, 1e-9))

			expectedEVtoEBIT := int64(math.Round(float64(expectedEV) / float64(f.EBIT)))
			Expect(f.EVtoEBIT).To(Equal(expectedEVtoEBIT))

			expectedEVtoEBITDA := float64(expectedEV) / float64(f.EBITDA)
			Expect(f.EVtoEBITDA).To(BeNumerically("~", expectedEVtoEBITDA, 1e-9))

			expectedDivYield := f.DividendsPerBasicCommonShare / price
			Expect(f.DividendYield).To(BeNumerically("~", expectedDivYield, 1e-9))

			expectedPayoutRatio := f.DividendsPerBasicCommonShare / f.EPSDiluted
			Expect(f.PayoutRatio).To(BeNumerically("~", expectedPayoutRatio, 1e-9))
		})
	})

	Describe("ARQ copies ratio fields from ART", func() {
		It("ARQ gets its own price/mktcap/EV/PB but copies PE/PS/PE1/PS1/EVtoEBIT/EVtoEBITDA/DividendYield/PayoutRatio from ART", func() {
			art := &data.Fundamental{
				CompositeFigi:                "BBG000B9XRY4",
				Dimension:                    "ART",
				DateKey:                      dateKey,
				EventDate:                    eventDate,
				SharesBasic:                  int64(1_000_000),
				TotalDebt:                    int64(500_000_000),
				CashAndEquivalents:           int64(200_000_000),
				Equity:                       int64(2_000_000_000),
				NetIncomeCommonStock:         int64(100_000_000),
				Revenues:                     int64(800_000_000),
				EBIT:                         int64(120_000_000),
				EBITDA:                       int64(150_000_000),
				EPS:                          2.50,
				EPSDiluted:                   2.40,
				SalesPerShare:                8.00,
				DividendsPerBasicCommonShare: 1.20,
			}

			arq := &data.Fundamental{
				CompositeFigi:      "BBG000B9XRY4",
				Dimension:          "ARQ",
				DateKey:            dateKey,
				EventDate:          eventDate,
				SharesBasic:        int64(1_000_000),
				TotalDebt:          int64(500_000_000),
				CashAndEquivalents: int64(200_000_000),
				Equity:             int64(1_800_000_000), // different equity for own PB
			}

			price := 236.0
			EnrichMarketData([]*data.Fundamental{art, arq}, stubPriceFn(price))

			// ARQ should have its own price-based fields
			expectedMktCap := int64(price * float64(arq.SharesBasic))
			expectedEV := expectedMktCap + arq.TotalDebt - arq.CashAndEquivalents
			expectedPB := float64(expectedMktCap) / float64(arq.Equity)

			Expect(arq.Price).To(BeNumerically("~", price, 1e-9))
			Expect(arq.MarketCapitalization).To(Equal(expectedMktCap))
			Expect(arq.EnterpriseValue).To(Equal(expectedEV))
			Expect(arq.PB).To(BeNumerically("~", expectedPB, 1e-9))

			// ARQ should copy ratio fields from ART
			Expect(arq.PE).To(BeNumerically("~", art.PE, 1e-9))
			Expect(arq.PS).To(BeNumerically("~", art.PS, 1e-9))
			Expect(arq.PE1).To(BeNumerically("~", art.PE1, 1e-9))
			Expect(arq.PS1).To(BeNumerically("~", art.PS1, 1e-9))
			Expect(arq.EVtoEBIT).To(Equal(art.EVtoEBIT))
			Expect(arq.EVtoEBITDA).To(BeNumerically("~", art.EVtoEBITDA, 1e-9))
			Expect(arq.DividendYield).To(BeNumerically("~", art.DividendYield, 1e-9))
			Expect(arq.PayoutRatio).To(BeNumerically("~", art.PayoutRatio, 1e-9))
		})
	})

	Describe("MRT->MRQ copy", func() {
		It("MRQ gets its own price/mktcap/EV/PB but copies ratio fields from MRT", func() {
			mrt := &data.Fundamental{
				CompositeFigi:                "BBG000B9XRY4",
				Dimension:                    "MRT",
				DateKey:                      dateKey,
				EventDate:                    eventDate,
				SharesBasic:                  int64(1_000_000),
				TotalDebt:                    int64(300_000_000),
				CashAndEquivalents:           int64(100_000_000),
				Equity:                       int64(1_500_000_000),
				NetIncomeCommonStock:         int64(80_000_000),
				Revenues:                     int64(600_000_000),
				EBIT:                         int64(90_000_000),
				EBITDA:                       int64(110_000_000),
				EPS:                          1.80,
				EPSDiluted:                   1.70,
				SalesPerShare:                6.00,
				DividendsPerBasicCommonShare: 0.90,
			}

			mrq := &data.Fundamental{
				CompositeFigi:      "BBG000B9XRY4",
				Dimension:          "MRQ",
				DateKey:            dateKey,
				EventDate:          eventDate,
				SharesBasic:        int64(1_000_000),
				TotalDebt:          int64(300_000_000),
				CashAndEquivalents: int64(100_000_000),
				Equity:             int64(1_200_000_000),
			}

			price := 180.0
			EnrichMarketData([]*data.Fundamental{mrt, mrq}, stubPriceFn(price))

			expectedMktCap := int64(price * float64(mrq.SharesBasic))
			expectedEV := expectedMktCap + mrq.TotalDebt - mrq.CashAndEquivalents
			expectedPB := float64(expectedMktCap) / float64(mrq.Equity)

			Expect(mrq.Price).To(BeNumerically("~", price, 1e-9))
			Expect(mrq.MarketCapitalization).To(Equal(expectedMktCap))
			Expect(mrq.EnterpriseValue).To(Equal(expectedEV))
			Expect(mrq.PB).To(BeNumerically("~", expectedPB, 1e-9))

			Expect(mrq.PE).To(BeNumerically("~", mrt.PE, 1e-9))
			Expect(mrq.PS).To(BeNumerically("~", mrt.PS, 1e-9))
			Expect(mrq.PE1).To(BeNumerically("~", mrt.PE1, 1e-9))
			Expect(mrq.PS1).To(BeNumerically("~", mrt.PS1, 1e-9))
			Expect(mrq.EVtoEBIT).To(Equal(mrt.EVtoEBIT))
			Expect(mrq.EVtoEBITDA).To(BeNumerically("~", mrt.EVtoEBITDA, 1e-9))
			Expect(mrq.DividendYield).To(BeNumerically("~", mrt.DividendYield, 1e-9))
			Expect(mrq.PayoutRatio).To(BeNumerically("~", mrt.PayoutRatio, 1e-9))
		})
	})

	Describe("Division by zero", func() {
		It("leaves ratio fields at 0 when denominators are zero", func() {
			f := &data.Fundamental{
				CompositeFigi:                "BBG000B9XRY4",
				Dimension:                    "ART",
				DateKey:                      dateKey,
				EventDate:                    eventDate,
				SharesBasic:                  int64(1_000_000),
				TotalDebt:                    int64(0),
				CashAndEquivalents:           int64(0),
				Equity:                       int64(0),
				NetIncomeCommonStock:         int64(0),
				Revenues:                     int64(0),
				EBIT:                         int64(0),
				EBITDA:                       int64(0),
				EPS:                          0.0,
				EPSDiluted:                   0.0,
				SalesPerShare:                0.0,
				DividendsPerBasicCommonShare: 0.0,
			}

			EnrichMarketData([]*data.Fundamental{f}, stubPriceFn(100.0))

			Expect(f.PB).To(BeNumerically("~", 0.0, 1e-9))
			Expect(f.PE).To(BeNumerically("~", 0.0, 1e-9))
			Expect(f.PS).To(BeNumerically("~", 0.0, 1e-9))
			Expect(f.PE1).To(BeNumerically("~", 0.0, 1e-9))
			Expect(f.PS1).To(BeNumerically("~", 0.0, 1e-9))
			Expect(f.EVtoEBIT).To(Equal(int64(0)))
			Expect(f.EVtoEBITDA).To(BeNumerically("~", 0.0, 1e-9))
			Expect(f.DividendYield).To(BeNumerically("~", 0.0, 1e-9))
			Expect(f.PayoutRatio).To(BeNumerically("~", 0.0, 1e-9))
		})
	})

	Describe("Missing price", func() {
		It("leaves all fields at zero when price lookup returns 0", func() {
			f := &data.Fundamental{
				CompositeFigi:        "BBG000B9XRY4",
				Dimension:            "ART",
				DateKey:              dateKey,
				EventDate:            eventDate,
				SharesBasic:          int64(1_000_000),
				TotalDebt:            int64(500_000_000),
				CashAndEquivalents:   int64(200_000_000),
				Equity:               int64(2_000_000_000),
				NetIncomeCommonStock: int64(100_000_000),
				Revenues:             int64(800_000_000),
				EBIT:                 int64(120_000_000),
				EBITDA:               int64(150_000_000),
				EPS:                  2.50,
				EPSDiluted:           2.40,
				SalesPerShare:        8.00,
			}

			EnrichMarketData([]*data.Fundamental{f}, stubPriceFn(0.0))

			Expect(f.Price).To(BeNumerically("~", 0.0, 1e-9))
			Expect(f.MarketCapitalization).To(Equal(int64(0)))
			Expect(f.EnterpriseValue).To(Equal(int64(0)))
			Expect(f.PB).To(BeNumerically("~", 0.0, 1e-9))
			Expect(f.PE).To(BeNumerically("~", 0.0, 1e-9))
			Expect(f.PS).To(BeNumerically("~", 0.0, 1e-9))
			Expect(f.PE1).To(BeNumerically("~", 0.0, 1e-9))
			Expect(f.PS1).To(BeNumerically("~", 0.0, 1e-9))
			Expect(f.EVtoEBIT).To(Equal(int64(0)))
			Expect(f.EVtoEBITDA).To(BeNumerically("~", 0.0, 1e-9))
			Expect(f.DividendYield).To(BeNumerically("~", 0.0, 1e-9))
			Expect(f.PayoutRatio).To(BeNumerically("~", 0.0, 1e-9))
		})
	})

	Describe("PE1 uses basic EPS not EPSDiluted", func() {
		It("computes PE1 = Price / EPS (basic), not Price / EPSDiluted", func() {
			f := &data.Fundamental{
				CompositeFigi: "BBG000B9XRY4",
				Dimension:     "ART",
				DateKey:       dateKey,
				EventDate:     eventDate,
				SharesBasic:   int64(1_000_000),
				Equity:        int64(1_000_000_000),
				EPS:           5.00,
				EPSDiluted:    4.00,
				SalesPerShare: 10.00,
			}

			price := 100.0
			EnrichMarketData([]*data.Fundamental{f}, stubPriceFn(price))

			Expect(f.PE1).To(BeNumerically("~", price/f.EPS, 1e-9))
			Expect(f.PE1).NotTo(BeNumerically("~", price/f.EPSDiluted, 1e-9))
		})
	})

	Describe("PB is independent for quarterly", func() {
		It("ARQ computes its own PB from its own Equity, not ARTs", func() {
			art := &data.Fundamental{
				CompositeFigi: "BBG000B9XRY4",
				Dimension:     "ART",
				DateKey:       dateKey,
				EventDate:     eventDate,
				SharesBasic:   int64(1_000_000),
				Equity:        int64(2_000_000_000),
				EPS:           2.50,
				EPSDiluted:    2.40,
				SalesPerShare: 8.00,
			}

			arqEquity := int64(500_000_000) // distinctly different from ART's equity
			arq := &data.Fundamental{
				CompositeFigi: "BBG000B9XRY4",
				Dimension:     "ARQ",
				DateKey:       dateKey,
				EventDate:     eventDate,
				SharesBasic:   int64(1_000_000),
				Equity:        arqEquity,
			}

			price := 200.0
			EnrichMarketData([]*data.Fundamental{art, arq}, stubPriceFn(price))

			mktCap := int64(price * float64(arq.SharesBasic))
			expectedARQPB := float64(mktCap) / float64(arqEquity)
			artPB := float64(int64(price*float64(art.SharesBasic))) / float64(art.Equity)

			Expect(arq.PB).To(BeNumerically("~", expectedARQPB, 1e-9))
			Expect(arq.PB).NotTo(BeNumerically("~", artPB, 1e-9))
		})
	})
})

var _ = Describe("emitFundamentals market-data enrichment", func() {
	It("populates price on emitted fundamentals when priceLookupFn is set", func() {
		cf := &CompanyFacts{
			CIK:        999,
			EntityName: "TestCo",
			Facts: map[string][]Fact{
				"Revenues": {
					{Val: 100000, Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC), Form: "10-Q", Filed: time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)},
				},
			},
		}

		asset := AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST01", CIK: 999}
		sub := &library.Subscription{Name: "test"}
		out := make(chan *data.Observation, 100)
		numObs := 0

		SetPriceLookupFn(func(compositeFigi string, eventDate time.Time) float64 {
			return 150.0
		})
		defer SetPriceLookupFn(nil)

		emitFundamentals(cf, asset, sub, time.Time{}, out, &numObs)
		close(out)

		foundPriced := false
		for obs := range out {
			if obs.Fundamental != nil && obs.Fundamental.Price > 0 {
				foundPriced = true
				Expect(obs.Fundamental.Price).To(BeNumerically("~", 150.0, 0.001))
			}
		}
		Expect(foundPriced).To(BeTrue(), "at least one observation should have a non-zero price")
	})

	It("emits fundamentals with zero price when no lookup function is set", func() {
		cf := &CompanyFacts{
			CIK:        999,
			EntityName: "TestCo",
			Facts: map[string][]Fact{
				"Revenues": {
					{Val: 100000, Start: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), End: time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC), Form: "10-Q", Filed: time.Date(2024, 5, 1, 0, 0, 0, 0, time.UTC)},
				},
			},
		}

		asset := AssetInfo{Ticker: "TEST", CompositeFigi: "BBG000TEST01", CIK: 999}
		sub := &library.Subscription{Name: "test"}
		out := make(chan *data.Observation, 100)
		numObs := 0

		SetPriceLookupFn(nil)

		emitFundamentals(cf, asset, sub, time.Time{}, out, &numObs)
		close(out)

		for obs := range out {
			if obs.Fundamental != nil {
				Expect(obs.Fundamental.Price).To(BeNumerically("==", 0))
			}
		}
	})
})
