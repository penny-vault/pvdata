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

var _ = Describe("normalizeSearchName", func() {
	DescribeTable("normalizes corporate suffixes and punctuation",
		func(in, want string) {
			Expect(normalizeSearchName(in)).To(Equal(want))
		},
		Entry("uppercase + Corp", "AMERICREDIT CORP", "americredit"),
		Entry("EDGAR display name without CIK suffix", "AMERICREDIT CORP", "americredit"),
		Entry("trailing whitespace", "  AmeriCredit Corp   ", "americredit"),
		Entry("Inc suffix", "Brown & Brown, Inc.", "brown brown"),
		Entry("multiple suffixes layered", "Foo Holdings Group Inc", "foo"),
		Entry("Co not stripped if not at end", "Coca Co Inc", "coca"),
		Entry("LLC suffix", "Acme L.L.C.", "acme"),
		Entry("foreign suffix N.V.", "Royal Dutch Shell N.V.", "royal dutch shell"),
		Entry("empty string passes through", "", ""),
		Entry("just a suffix collapses to empty", "Corp", ""),
	)
})

var _ = Describe("normalizeDisplayName", func() {
	It("strips the trailing (CIK ...) annotation", func() {
		Expect(normalizeDisplayName("AMERICREDIT CORP  (CIK 0000804269)")).To(Equal("americredit"))
	})

	It("returns empty for empty input", func() {
		Expect(normalizeDisplayName("")).To(Equal(""))
	})

	It("handles no CIK annotation", func() {
		Expect(normalizeDisplayName("Apple Inc")).To(Equal("apple"))
	})
})

type fakeHit = struct {
	Source struct {
		CIKs         []string `json:"ciks"`
		DisplayNames []string `json:"display_names"`
		Form         string   `json:"form"`
		FileDate     string   `json:"file_date"`
	} `json:"_source"`
}

func makeHit(ciks, names []string) fakeHit {
	h := fakeHit{}
	h.Source.CIKs = ciks
	h.Source.DisplayNames = names
	h.Source.Form = "10-K"

	return h
}

var _ = Describe("pickBestCIK", func() {
	It("returns the most-frequent CIK whose display matches", func() {
		hits := []fakeHit{
			makeHit([]string{"0000804269"}, []string{"AMERICREDIT CORP  (CIK 0000804269)"}),
			makeHit([]string{"0000804269"}, []string{"AMERICREDIT CORP  (CIK 0000804269)"}),
			makeHit([]string{"0000804269"}, []string{"AMERICREDIT CORP  (CIK 0000804269)"}),
			makeHit([]string{"0001405287"}, []string{"STREAM GLOBAL SERVICES, INC.  (CIK 0001405287)"}),
		}
		got := pickBestCIK(hits, normalizeSearchName("AMERICREDIT CORP"))
		Expect(got).To(Equal("0000804269"))
	})

	It("returns empty when no hit's display normalizes to the query", func() {
		hits := []fakeHit{
			makeHit([]string{"0001405287"}, []string{"STREAM GLOBAL SERVICES, INC.  (CIK 0001405287)"}),
			makeHit([]string{"0001405287"}, []string{"STREAM GLOBAL SERVICES, INC.  (CIK 0001405287)"}),
		}
		Expect(pickBestCIK(hits, normalizeSearchName("AMERICREDIT CORP"))).To(Equal(""))
	})

	It("requires at least 2 hits with the winning CIK", func() {
		hits := []fakeHit{
			makeHit([]string{"0000804269"}, []string{"AMERICREDIT CORP  (CIK 0000804269)"}),
		}
		Expect(pickBestCIK(hits, normalizeSearchName("AMERICREDIT CORP"))).To(Equal(""))
	})

	It("returns empty when there are no hits", func() {
		Expect(pickBestCIK(nil, "americredit")).To(Equal(""))
	})
})

var _ = Describe("historicalNameAt", func() {
	makeSub := func(name string, formers ...FormerName) *SubmissionsResponse {
		return &SubmissionsResponse{Name: name, FormerNames: formers}
	}

	It("returns formerName when the asset's window falls inside it", func() {
		sub := makeSub("General Motors Financial Company, Inc.",
			FormerName{Name: "AMERICREDIT CORP", From: "1994-09-28T00:00:00.000Z", To: "2010-10-06T00:00:00.000Z"},
		)
		Expect(historicalNameAt(sub, "1996-10-11", "2006-01-04")).To(Equal("AMERICREDIT CORP"))
	})

	It("returns current name when window is after every formerName", func() {
		sub := makeSub("General Motors Financial Company, Inc.",
			FormerName{Name: "AMERICREDIT CORP", From: "1994-09-28T00:00:00.000Z", To: "2010-10-06T00:00:00.000Z"},
		)
		Expect(historicalNameAt(sub, "2020-01-01", "")).To(Equal("General Motors Financial Company, Inc."))
	})

	It("returns current name when window is before every formerName", func() {
		sub := makeSub("Current Co",
			FormerName{Name: "Recent Predecessor", From: "2015-01-01T00:00:00.000Z", To: "2020-01-01T00:00:00.000Z"},
		)
		Expect(historicalNameAt(sub, "1990-01-01", "1995-01-01")).To(Equal("Current Co"))
	})

	It("returns current name when sub has no formerNames", func() {
		sub := makeSub("Apple Inc.")
		Expect(historicalNameAt(sub, "2000-01-01", "")).To(Equal("Apple Inc."))
	})

	It("returns current name when both dates are empty", func() {
		sub := makeSub("Apple Inc.",
			FormerName{Name: "Apple Computer, Inc.", From: "1977-01-03T00:00:00.000Z", To: "2007-01-09T00:00:00.000Z"},
		)
		Expect(historicalNameAt(sub, "", "")).To(Equal("Apple Inc."))
	})

	It("returns empty for nil sub", func() {
		Expect(historicalNameAt(nil, "2010-01-01", "")).To(Equal(""))
	})

	It("treats open-ended formerName.To as +infinity", func() {
		sub := makeSub("Current",
			FormerName{Name: "Open Ended Predecessor", From: "2000-01-01T00:00:00.000Z", To: ""},
		)
		Expect(historicalNameAt(sub, "2010-01-01", "2015-01-01")).To(Equal("Open Ended Predecessor"))
	})
})

var _ = Describe("dateOnly", func() {
	DescribeTable("strips time portion of ISO timestamps",
		func(in, want string) {
			Expect(dateOnly(in)).To(Equal(want))
		},
		Entry("standard ISO with millis", "2010-10-06T00:00:00.000Z", "2010-10-06"),
		Entry("ISO without millis", "2010-10-06T00:00:00Z", "2010-10-06"),
		Entry("date only passes through", "2010-10-06", "2010-10-06"),
		Entry("empty string", "", ""),
		Entry("short string passes through", "abc", "abc"),
	)
})
