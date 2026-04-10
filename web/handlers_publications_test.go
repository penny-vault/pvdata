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
package web

import (
	"time"

	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/library"
)

var _ = Describe("Publication Handlers", func() {
	Describe("enrichPublishedView", func() {
		It("enriches sources with subscription metadata", func() {
			subID := uuid.New()
			fromDate := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			pv := &library.PublishedView{
				ID:          uuid.New(),
				ViewName:    "eod",
				DataTypeKey: "eod",
				Sources: []library.ViewSource{
					{
						TableName:      "tiingo_eod_eod_abc12",
						SubscriptionID: subID.String(),
						FromDate:       &fromDate,
					},
				},
			}

			subMap := map[string]*library.Subscription{
				subID.String(): {
					ID:       subID,
					Name:     "Tiingo EOD",
					Provider: "tiingo",
					Dataset:  "eod",
				},
			}

			result := enrichPublishedView(pv, subMap)

			Expect(result.ID).To(Equal(pv.ID))
			Expect(result.ViewName).To(Equal("eod"))
			Expect(result.Sources).To(HaveLen(1))
			Expect(result.Sources[0].SubscriptionName).To(Equal("Tiingo EOD"))
			Expect(result.Sources[0].Provider).To(Equal("tiingo"))
			Expect(result.Sources[0].Dataset).To(Equal("eod"))
			Expect(*result.Sources[0].FromDate).To(Equal("2023-01-01"))
			Expect(result.Sources[0].UntilDate).To(BeNil())
		})

		It("handles missing subscription gracefully", func() {
			pv := &library.PublishedView{
				ID:          uuid.New(),
				ViewName:    "eod",
				DataTypeKey: "eod",
				Sources: []library.ViewSource{
					{
						TableName:      "unknown_table",
						SubscriptionID: uuid.New().String(),
					},
				},
			}

			result := enrichPublishedView(pv, map[string]*library.Subscription{})

			Expect(result.Sources).To(HaveLen(1))
			Expect(result.Sources[0].SubscriptionName).To(Equal(""))
			Expect(result.Sources[0].Provider).To(Equal(""))
		})

		It("includes overlap warnings", func() {
			d1 := time.Date(2022, 1, 1, 0, 0, 0, 0, time.UTC)
			d2 := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
			d3 := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			pv := &library.PublishedView{
				ID:          uuid.New(),
				ViewName:    "eod",
				DataTypeKey: "eod",
				Sources: []library.ViewSource{
					{TableName: "t1", SubscriptionID: "sub-1", FromDate: &d1, UntilDate: &d2},
					{TableName: "t2", SubscriptionID: "sub-2", FromDate: &d3},
				},
			}

			result := enrichPublishedView(pv, map[string]*library.Subscription{})

			Expect(result.Overlaps).To(HaveLen(1))
			Expect(result.Overlaps[0]).To(ContainSubstring("t1"))
		})

		It("returns no overlaps for clean sources", func() {
			boundary := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
			pv := &library.PublishedView{
				ID:          uuid.New(),
				ViewName:    "eod",
				DataTypeKey: "eod",
				Sources: []library.ViewSource{
					{TableName: "t1", SubscriptionID: "sub-1", UntilDate: &boundary},
					{TableName: "t2", SubscriptionID: "sub-2", FromDate: &boundary},
				},
			}

			result := enrichPublishedView(pv, map[string]*library.Subscription{})

			Expect(result.Overlaps).To(BeEmpty())
		})
	})
})
