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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("CIK Resolution", func() {
	Describe("ParseCompanyTickers", func() {
		It("parses SEC company_tickers JSON", func() {
			// Sample from https://www.sec.gov/files/company_tickers.json
			jsonData := []byte(`{
				"0": {"cik_str": 320193, "ticker": "AAPL", "title": "Apple Inc."},
				"1": {"cik_str": 789019, "ticker": "MSFT", "title": "MICROSOFT CORP"},
				"2": {"cik_str": 1652044, "ticker": "GOOGL", "title": "Alphabet Inc."}
			}`)

			m, err := ParseCompanyTickers(jsonData)
			Expect(err).NotTo(HaveOccurred())
			Expect(m).To(HaveKey(320193))
			Expect(m[320193].Ticker).To(Equal("AAPL"))
			Expect(m[320193].Name).To(Equal("Apple Inc."))
			Expect(m).To(HaveKey(789019))
			Expect(m[789019].Ticker).To(Equal("MSFT"))
		})
	})

	Describe("FormatCIK", func() {
		It("zero-pads CIK to 10 digits", func() {
			Expect(FormatCIK(320193)).To(Equal("CIK0000320193"))
		})

		It("handles large CIKs", func() {
			Expect(FormatCIK(1652044)).To(Equal("CIK0001652044"))
		})
	})
})
