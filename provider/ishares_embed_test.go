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
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

func TestISharesEmbed(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "iShares Embed Suite")
}

var _ = Describe("iSharesETFMap", func() {
	It("is populated with entries from the embedded JSON", func() {
		Expect(len(iSharesETFMap)).To(BeNumerically(">", 280))
	})

	It("contains IVV with correct metadata", func() {
		etf, ok := iSharesETFMap["IVV"]
		Expect(ok).To(BeTrue())
		Expect(etf.ProductID).To(Equal("239726"))
		Expect(etf.IndexName).To(Equal("sp500"))
		Expect(etf.Slug).To(ContainSubstring("sp-500"))
	})

	It("contains IWM with correct metadata", func() {
		etf, ok := iSharesETFMap["IWM"]
		Expect(ok).To(BeTrue())
		Expect(etf.ProductID).To(Equal("239710"))
		Expect(etf.IndexName).To(Equal("russell-2000"))
	})

	It("preserves all original 19 ETFs", func() {
		originalTickers := []string{
			"IVV", "IWB", "IWD", "IWF", "IWM", "IJH", "IJR",
			"IXUS", "IEFA", "IEMG", "IVW", "IVE", "ITOT",
			"IWV", "IWR", "IWS", "IWP", "IWO", "IWN",
		}
		for _, ticker := range originalTickers {
			_, ok := iSharesETFMap[ticker]
			Expect(ok).To(BeTrue(), "expected %s to be in iSharesETFMap", ticker)
		}
	})

	It("has unique index names", func() {
		seen := make(map[string]string)
		for ticker, etf := range iSharesETFMap {
			if prev, ok := seen[etf.IndexName]; ok {
				Fail("duplicate indexName '" + etf.IndexName + "' for tickers " + prev + " and " + ticker)
			}
			seen[etf.IndexName] = ticker
		}
	})
})
