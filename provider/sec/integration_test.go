//go:build integration

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
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"golang.org/x/time/rate"

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
)

var _ = Describe("Integration", func() {
	Describe("Apple fundamentals from SEC EDGAR", func() {
		var cf *CompanyFacts

		BeforeEach(func() {
			limiter := rate.NewLimiter(rate.Limit(10), 1)
			client := NewSECClient("pvdata/1.0 integration-test@example.com", limiter)

			var err error
			cf, err = FetchCompanyFacts(context.Background(), client, 320193) // Apple
			Expect(err).NotTo(HaveOccurred())
			Expect(cf.EntityName).To(Equal("Apple Inc."))
		})

		It("resolves revenue for FY2023 10-K", func() {
			// Apple FY2023 ended Sep 30, 2023
			periodEnd := time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC)
			fields := ResolveAllFields(cf, periodEnd, "10-K")

			rev, ok := fields["Revenues"]
			Expect(ok).To(BeTrue())
			// Apple FY2023 revenue was ~$383B
			Expect(rev).To(BeNumerically("~", 383285000000, 5000000000))
		})

		It("resolves total assets for FY2023 10-K", func() {
			periodEnd := time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC)
			fields := ResolveAllFields(cf, periodEnd, "10-K")

			assets, ok := fields["TotalAssets"]
			Expect(ok).To(BeTrue())
			// Apple FY2023 total assets were ~$352B
			Expect(assets).To(BeNumerically("~", 352583000000, 10000000000))
		})

		It("computes EBITDA for FY2023", func() {
			periodEnd := time.Date(2023, 9, 30, 0, 0, 0, 0, time.UTC)
			fields := ResolveAllFields(cf, periodEnd, "10-K")

			ebitda, ok := fields["EBITDA"]
			Expect(ok).To(BeTrue())
			// Apple FY2023 EBITDA was ~$125-130B
			Expect(ebitda).To(BeNumerically(">", 100000000000))
		})

		It("identifies multiple periods", func() {
			periods := IdentifyPeriods(cf)
			Expect(len(periods)).To(BeNumerically(">", 20))
		})

		It("produces fundamentals for all 6 dimensions", func() {
			asset := AssetInfo{Ticker: "AAPL", CompositeFigi: "BBG000B9XRY4", CIK: 320193}
			sub := &library.Subscription{Name: "sec-integration-test"}
			observations := make(chan *data.Observation, 1000)

			go func() {
				numObs := 0
				emitFundamentals(cf, asset, sub, observations, &numObs)
				close(observations)
			}()

			dimensions := make(map[string]int)
			for obs := range observations {
				dimensions[obs.Fundamental.Dimension]++
			}

			Expect(dimensions).To(HaveKey("ARQ"))
			Expect(dimensions).To(HaveKey("MRQ"))
			Expect(dimensions).To(HaveKey("ARY"))
			Expect(dimensions).To(HaveKey("MRY"))
			Expect(dimensions).To(HaveKey("ART"))
			Expect(dimensions).To(HaveKey("MRT"))
		})
	})
})
