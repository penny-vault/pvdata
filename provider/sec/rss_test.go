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
	"os"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RSS Feed", func() {
	Describe("ParseFilingFeed", func() {
		var filings []FilingEntry

		BeforeEach(func() {
			xmlData, err := os.ReadFile("testdata/rss_sample.xml")
			Expect(err).NotTo(HaveOccurred())

			filings, err = ParseFilingFeed(xmlData)
			Expect(err).NotTo(HaveOccurred())
		})

		It("returns at least one filing", func() {
			Expect(len(filings)).To(BeNumerically(">", 0))
		})

		It("extracts CIKs and form types from EDGAR feed", func() {
			for _, f := range filings {
				Expect(f.CIK).To(BeNumerically(">", 0))
				Expect(f.FormType).To(BeElementOf("10-K", "10-Q"))
			}
		})

		It("filters out amendments such as 10-K/A", func() {
			for _, f := range filings {
				Expect(f.FormType).NotTo(ContainSubstring("/A"))
			}
		})

		It("extracts the company name from the title", func() {
			for _, f := range filings {
				Expect(f.CompanyName).NotTo(BeEmpty())
				Expect(f.CompanyName).NotTo(ContainSubstring("(Filer)"))
				Expect(f.CompanyName).NotTo(HavePrefix("10-"))
			}
		})

		It("extracts the accession number from the index link", func() {
			for _, f := range filings {
				Expect(f.Accn).To(MatchRegexp(`^[0-9]{10}-[0-9]{2}-[0-9]{6}$`))
			}
		})

		It("parses the filing timestamp", func() {
			for _, f := range filings {
				Expect(f.Filed.IsZero()).To(BeFalse())
			}
		})

		It("extracts a known fixture entry", func() {
			var found bool

			for _, f := range filings {
				if f.CIK == 1544190 {
					found = true

					Expect(f.FormType).To(Equal("10-K"))
					Expect(f.CompanyName).To(ContainSubstring("Shepherd"))
					Expect(f.Accn).To(Equal("0001493152-26-015327"))
				}
			}

			Expect(found).To(BeTrue(), "expected fixture CIK 1544190 in parsed feed")
		})
	})

	Describe("ParseFilingFeed error handling", func() {
		It("returns an error on malformed XML", func() {
			_, err := ParseFilingFeed([]byte("<not xml"))
			Expect(err).To(HaveOccurred())
		})

		It("returns an empty slice when there are no entries", func() {
			empty := []byte(`<?xml version="1.0" encoding="UTF-8"?>
<feed xmlns="http://www.w3.org/2005/Atom"></feed>`)

			filings, err := ParseFilingFeed(empty)
			Expect(err).NotTo(HaveOccurred())
			Expect(filings).To(BeEmpty())
		})
	})
})
