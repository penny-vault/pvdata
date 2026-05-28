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
package massive

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("translateMassiveHolidays", func() {
	var (
		ctx context.Context
		nyc *time.Location
	)

	BeforeEach(func() {
		ctx = context.Background()

		loc, err := time.LoadLocation("America/New_York")
		Expect(err).NotTo(HaveOccurred())

		nyc = loc
	})

	It("translates NYSE to the 'us' market", func() {
		holidays, err := translateMassiveHolidays(ctx, []*massiveHoliday{{
			Date:     "2025-01-01",
			Exchange: "NYSE",
			Name:     "New Year's Day",
			Status:   "closed",
		}}, nyc)

		Expect(err).NotTo(HaveOccurred())
		Expect(holidays).To(HaveLen(1))
		Expect(holidays[0].Market).To(Equal("us"))
		Expect(holidays[0].Name).To(Equal("New Year's Day"))
		Expect(holidays[0].EarlyClose).To(BeFalse())
	})

	It("translates NASDAQ to the 'us' market", func() {
		holidays, err := translateMassiveHolidays(ctx, []*massiveHoliday{{
			Date:     "2025-01-01",
			Exchange: "NASDAQ",
			Name:     "New Year's Day",
			Status:   "closed",
		}}, nyc)

		Expect(err).NotTo(HaveOccurred())
		Expect(holidays).To(HaveLen(1))
		Expect(holidays[0].Market).To(Equal("us"))
	})

	It("collapses duplicate NYSE and NASDAQ records into a single observation", func() {
		holidays, err := translateMassiveHolidays(ctx, []*massiveHoliday{
			{Date: "2025-01-01", Exchange: "NYSE", Name: "New Year's Day", Status: "closed"},
			{Date: "2025-01-01", Exchange: "NASDAQ", Name: "New Year's Day", Status: "closed"},
		}, nyc)

		Expect(err).NotTo(HaveOccurred())
		Expect(holidays).To(HaveLen(1))
		Expect(holidays[0].Market).To(Equal("us"))
	})

	It("preserves distinct dates after collapsing duplicates", func() {
		holidays, err := translateMassiveHolidays(ctx, []*massiveHoliday{
			{Date: "2025-01-01", Exchange: "NYSE", Name: "New Year's Day", Status: "closed"},
			{Date: "2025-01-01", Exchange: "NASDAQ", Name: "New Year's Day", Status: "closed"},
			{Date: "2025-07-04", Exchange: "NYSE", Name: "Independence Day", Status: "closed"},
			{Date: "2025-07-04", Exchange: "NASDAQ", Name: "Independence Day", Status: "closed"},
		}, nyc)

		Expect(err).NotTo(HaveOccurred())
		Expect(holidays).To(HaveLen(2))
		Expect(holidays[0].EventDate.Year()).To(Equal(2025))
		Expect(holidays[0].EventDate.Month()).To(Equal(time.January))
		Expect(holidays[1].EventDate.Month()).To(Equal(time.July))
	})

	It("flags early-close days", func() {
		holidays, err := translateMassiveHolidays(ctx, []*massiveHoliday{{
			Date:     "2025-11-28",
			Exchange: "NYSE",
			Name:     "Day After Thanksgiving",
			Status:   "early-close",
			Close:    "2025-11-28T13:00:00-05:00",
		}}, nyc)

		Expect(err).NotTo(HaveOccurred())
		Expect(holidays).To(HaveLen(1))
		Expect(holidays[0].EarlyClose).To(BeTrue())
		Expect(holidays[0].CloseTime.In(nyc).Hour()).To(Equal(13))
	})

	It("skips holidays with unrecognized exchanges", func() {
		holidays, err := translateMassiveHolidays(ctx, []*massiveHoliday{
			{Date: "2025-01-01", Exchange: "LSE", Name: "New Year's Day", Status: "closed"},
			{Date: "2025-01-01", Exchange: "NYSE", Name: "New Year's Day", Status: "closed"},
		}, nyc)

		Expect(err).NotTo(HaveOccurred())
		Expect(holidays).To(HaveLen(1))
		Expect(holidays[0].Market).To(Equal("us"))
	})

	It("returns an error when a close time fails to parse", func() {
		_, err := translateMassiveHolidays(ctx, []*massiveHoliday{{
			Date:     "2025-11-28",
			Exchange: "NYSE",
			Name:     "Day After Thanksgiving",
			Status:   "early-close",
			Close:    "not-a-timestamp",
		}}, nyc)

		Expect(err).To(HaveOccurred())
	})

	It("skips holidays with unparseable dates without aborting", func() {
		holidays, err := translateMassiveHolidays(ctx, []*massiveHoliday{
			{Date: "not-a-date", Exchange: "NYSE", Name: "Garbage", Status: "closed"},
			{Date: "2025-01-01", Exchange: "NYSE", Name: "New Year's Day", Status: "closed"},
		}, nyc)

		Expect(err).NotTo(HaveOccurred())
		Expect(holidays).To(HaveLen(1))
		Expect(holidays[0].Name).To(Equal("New Year's Day"))
	})
})
