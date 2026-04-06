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
package checks_test

import (
	"context"
	"time"

	"github.com/penny-vault/pvdata/checks"
	"github.com/penny-vault/pvdata/data"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("BalanceSheetIdentity", func() {
	var check *checks.BalanceSheetIdentity

	BeforeEach(func() {
		check = &checks.BalanceSheetIdentity{}
	})

	It("passes when assets = liabilities + equity", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:           "AAPL",
				EventDate:        time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
				TotalAssets:      1_000_000,
				TotalLiabilities: 600_000,
				Equity:           400_000,
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(BeEmpty())
		Expect(block).To(BeFalse())
	})

	It("fails when assets do not equal liabilities + equity", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:           "AAPL",
				EventDate:        time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
				TotalAssets:      1_000_000,
				TotalLiabilities: 600_000,
				Equity:           300_000, // 100_000 off
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(HaveLen(1))
		Expect(results[0].Field).To(Equal("total_assets"))
		Expect(results[0].Severity).To(Equal(checks.SeverityError))
		Expect(block).To(BeFalse())
	})

	It("skips when all fields are zero", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:    "AAPL",
				EventDate: time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(BeEmpty())
		Expect(block).To(BeFalse())
	})

	It("skips non-fundamental observations", func() {
		obs := &data.Observation{}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(BeEmpty())
		Expect(block).To(BeFalse())
	})
})

var _ = Describe("GrossProfitCalc", func() {
	var check *checks.GrossProfitCalc

	BeforeEach(func() {
		check = &checks.GrossProfitCalc{}
	})

	It("passes when gross_profit = revenues - cost_of_revenue", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:        "AAPL",
				EventDate:     time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
				Revenues:      1_000_000,
				CostOfRevenue: 600_000,
				GrossProfit:   400_000,
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(BeEmpty())
		Expect(block).To(BeFalse())
	})

	It("fails when gross_profit does not match revenues - cost_of_revenue", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:        "AAPL",
				EventDate:     time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
				Revenues:      1_000_000,
				CostOfRevenue: 600_000,
				GrossProfit:   200_000, // should be 400_000
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(HaveLen(1))
		Expect(results[0].Field).To(Equal("gross_profit"))
		Expect(results[0].Severity).To(Equal(checks.SeverityWarning))
		Expect(block).To(BeFalse())
	})
})

var _ = Describe("NetIncomeEPS", func() {
	var check *checks.NetIncomeEPS

	BeforeEach(func() {
		check = &checks.NetIncomeEPS{}
	})

	It("passes when eps * shares ~= net_income", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:                       "AAPL",
				EventDate:                    time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
				NetIncome:                    100_000_000,
				EPSDiluted:                   1.0,
				WeightedAverageSharesDiluted: 100_000_000,
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(BeEmpty())
		Expect(block).To(BeFalse())
	})

	It("fails when eps * shares diverges significantly from net_income", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:                       "AAPL",
				EventDate:                    time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
				NetIncome:                    100_000_000,
				EPSDiluted:                   5.0,
				WeightedAverageSharesDiluted: 100_000_000, // derived = 500_000_000, 400% off
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(HaveLen(1))
		Expect(results[0].Field).To(Equal("eps_diluted"))
		Expect(results[0].Severity).To(Equal(checks.SeverityWarning))
		Expect(block).To(BeFalse())
	})

	It("skips when any value is zero", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:                       "AAPL",
				EventDate:                    time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
				NetIncome:                    0,
				EPSDiluted:                   1.0,
				WeightedAverageSharesDiluted: 100_000_000,
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(BeEmpty())
		Expect(block).To(BeFalse())
	})
})
