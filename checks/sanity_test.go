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

var _ = Describe("PositiveAssets", func() {
	var check *checks.PositiveAssets

	BeforeEach(func() {
		check = &checks.PositiveAssets{}
	})

	It("passes when TotalAssets > 0", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:      "AAPL",
				TotalAssets: 1_000_000,
				EventDate:   time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(BeEmpty())
		Expect(block).To(BeFalse())
	})

	It("fails when TotalAssets == 0", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:      "AAPL",
				TotalAssets: 0,
				EventDate:   time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(HaveLen(1))
		Expect(results[0].Field).To(Equal("total_assets"))
		Expect(results[0].Severity).To(Equal(checks.SeverityCritical))
		Expect(block).To(BeTrue())
	})

	It("fails when TotalAssets < 0", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:      "AAPL",
				TotalAssets: -500,
				EventDate:   time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(HaveLen(1))
		Expect(block).To(BeTrue())
	})

	It("skips non-fundamental observations", func() {
		obs := &data.Observation{}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(BeEmpty())
		Expect(block).To(BeFalse())
	})
})

var _ = Describe("PositiveShares", func() {
	var check *checks.PositiveShares

	BeforeEach(func() {
		check = &checks.PositiveShares{}
	})

	It("passes when WeightedAverageSharesDiluted > 0", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:                       "AAPL",
				WeightedAverageSharesDiluted: 1_000_000,
				EventDate:                    time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(BeEmpty())
		Expect(block).To(BeFalse())
	})

	It("fails when WeightedAverageSharesDiluted == 0", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:                       "AAPL",
				WeightedAverageSharesDiluted: 0,
				EventDate:                    time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(HaveLen(1))
		Expect(results[0].Field).To(Equal("weighted_average_shares_diluted"))
		Expect(results[0].Severity).To(Equal(checks.SeverityCritical))
		Expect(block).To(BeTrue())
	})

	It("fails when WeightedAverageSharesDiluted < 0", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:                       "AAPL",
				WeightedAverageSharesDiluted: -100,
				EventDate:                    time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(HaveLen(1))
		Expect(block).To(BeTrue())
	})
})

var _ = Describe("ValidDates", func() {
	var check *checks.ValidDates

	BeforeEach(func() {
		check = &checks.ValidDates{}
	})

	It("passes with past dates", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:       "AAPL",
				EventDate:    time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
				ReportPeriod: time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
				DateKey:      time.Date(2024, 4, 15, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(BeEmpty())
		Expect(block).To(BeFalse())
	})

	It("fails with a future event_date", func() {
		future := time.Now().UTC().Add(24 * time.Hour)
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:    "AAPL",
				EventDate: future,
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(HaveLen(1))
		Expect(results[0].Field).To(Equal("event_date"))
		Expect(results[0].Severity).To(Equal(checks.SeverityCritical))
		Expect(block).To(BeTrue())
	})

	It("fails with multiple future dates", func() {
		future := time.Now().UTC().Add(24 * time.Hour)
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:       "AAPL",
				EventDate:    future,
				ReportPeriod: future,
				DateKey:      future,
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(HaveLen(3))
		Expect(block).To(BeTrue())
	})

	It("skips zero date fields", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:    "AAPL",
				EventDate: time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
				// ReportPeriod and DateKey are zero
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(BeEmpty())
		Expect(block).To(BeFalse())
	})
})

var _ = Describe("PositiveRevenue", func() {
	var check *checks.PositiveRevenue

	BeforeEach(func() {
		check = &checks.PositiveRevenue{}
	})

	It("passes when Revenues >= 0", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:    "AAPL",
				Revenues:  500_000,
				EventDate: time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(BeEmpty())
		Expect(block).To(BeFalse())
	})

	It("passes when Revenues == 0", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:    "AAPL",
				Revenues:  0,
				EventDate: time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(BeEmpty())
		Expect(block).To(BeFalse())
	})

	It("fails when Revenues < 0", func() {
		obs := &data.Observation{
			Fundamental: &data.Fundamental{
				Ticker:    "AAPL",
				Revenues:  -1_000,
				EventDate: time.Date(2024, 3, 31, 0, 0, 0, 0, time.UTC),
			},
		}
		results, block := check.Validate(context.Background(), obs)
		Expect(results).To(HaveLen(1))
		Expect(results[0].Field).To(Equal("revenues"))
		Expect(results[0].Severity).To(Equal(checks.SeverityError))
		Expect(block).To(BeFalse())
	})
})
