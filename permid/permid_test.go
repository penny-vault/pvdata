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
package permid_test

import (
	"encoding/json"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/penny-vault/pvdata/permid"
)

var _ = Describe("NormalizeCIK", func() {
	It("zero-pads a short numeric CIK to 10 digits", func() {
		Expect(permid.NormalizeCIK("320193")).To(Equal("0000320193"))
	})

	It("leaves an already-padded CIK unchanged", func() {
		Expect(permid.NormalizeCIK("0000320193")).To(Equal("0000320193"))
	})

	It("trims surrounding whitespace before normalizing", func() {
		Expect(permid.NormalizeCIK("  320193  ")).To(Equal("0000320193"))
	})

	It("returns empty for empty input", func() {
		Expect(permid.NormalizeCIK("")).To(Equal(""))
		Expect(permid.NormalizeCIK("   ")).To(Equal(""))
	})

	It("returns empty for an all-zero string", func() {
		Expect(permid.NormalizeCIK("0000000000")).To(Equal(""))
	})

	It("returns empty for a CIK longer than 10 significant digits", func() {
		Expect(permid.NormalizeCIK("12345678901")).To(Equal(""))
	})
})

var _ = Describe("NameMatches", func() {
	It("accepts identical names", func() {
		Expect(permid.NameMatches("Apple Inc", "Apple Inc")).To(BeTrue())
	})

	It("accepts case-different variants above the similarity threshold", func() {
		Expect(permid.NameMatches("apple inc", "Apple Inc")).To(BeTrue())
	})

	It("rejects unrelated entities", func() {
		Expect(permid.NameMatches("Blockbuster Inc", "Brickell Biotech")).To(BeFalse())
	})

	It("falls back to first-words match when JW similarity is borderline", func() {
		// First two normalized words match identically; suffixes
		// diverge enough that raw JW would reject.
		Expect(permid.NameMatches(
			"U-Haul Holding Company",
			"U HAUL NON VOTING SERIES N",
		)).To(BeTrue())
	})

	It("returns false on empty inputs", func() {
		Expect(permid.NameMatches("", "Apple Inc")).To(BeFalse())
		Expect(permid.NameMatches("Apple Inc", "")).To(BeFalse())
		Expect(permid.NameMatches("", "")).To(BeFalse())
	})
})

var _ = Describe("SearchResponse decoding", func() {
	// Sample response cribbed from a real Apple ticker query, trimmed
	// for the fields we use. Verifies the JSON tags line up with the
	// Refinitiv shape and that nested entities round-trip through the
	// SearchResponse struct.
	const appleJSON = `{
		"result": {
			"organizations": {
				"entityType": "organizations",
				"total": 1,
				"start": 1,
				"num": 1,
				"entities": [
					{
						"@id": "https://permid.org/1-4295905573",
						"organizationName": "Apple Inc",
						"primaryTicker": "AAPL",
						"orgSubtype": "Company"
					}
				]
			},
			"instruments": {
				"entityType": "instruments",
				"total": 1,
				"start": 1,
				"num": 1,
				"entities": [
					{
						"@id": "https://permid.org/1-8590932301",
						"hasName": "Apple Ord Shs",
						"assetClass": "Ordinary Shares",
						"isIssuedByName": "Apple Inc",
						"isIssuedBy": "https://permid.org/1-4295905573",
						"primaryTicker": "AAPL"
					}
				]
			},
			"quotes": { "entityType": "quotes", "total": 0, "start": 1, "num": 0, "entities": [] }
		}
	}`

	It("parses organizations and instruments off the response shape", func() {
		var resp permid.SearchResponse
		Expect(json.Unmarshal([]byte(appleJSON), &resp)).To(Succeed())

		Expect(resp.Result.Organizations.Total).To(Equal(1))
		Expect(resp.Result.Organizations.Entities).To(HaveLen(1))
		Expect(resp.Result.Organizations.Entities[0].OrganizationName).To(Equal("Apple Inc"))
		Expect(resp.Result.Organizations.Entities[0].PrimaryTicker).To(Equal("AAPL"))
		Expect(resp.Result.Organizations.Entities[0].ID).To(Equal("https://permid.org/1-4295905573"))

		Expect(resp.Result.Instruments.Entities).To(HaveLen(1))
		Expect(resp.Result.Instruments.Entities[0].IsIssuedBy).To(Equal("https://permid.org/1-4295905573"))
		Expect(resp.Result.Instruments.Entities[0].IsIssuedByName).To(Equal("Apple Inc"))
		Expect(resp.Result.Instruments.Entities[0].PrimaryTicker).To(Equal("AAPL"))
		Expect(resp.Result.Instruments.Entities[0].HasName).To(Equal("Apple Ord Shs"))
	})
})
