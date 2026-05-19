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

// newSub builds a SubmissionsResponse with parallel form / date arrays
// in newest-first order, matching the shape SEC actually returns.
// Pairs is supplied newest-first, e.g. ("10-K","2020-03-15") first
// and ("N-1A","2010-06-01") last when 2010 is the earliest filing.
func newSub(tickers []string, pairs ...[2]string) *SubmissionsResponse {
	forms := make([]string, len(pairs))
	dates := make([]string, len(pairs))

	for i, p := range pairs {
		forms[i] = p[0]
		dates[i] = p[1]
	}

	return &SubmissionsResponse{
		Tickers: tickers,
		Filings: FilingsBlock{
			Recent: FilingsRecent{
				Form:       forms,
				FilingDate: dates,
			},
		},
	}
}

var _ = Describe("EarliestFilingDateForForm", func() {
	It("returns the earliest matching filing by prefix", func() {
		sub := newSub(nil,
			[2]string{"10-K", "2020-03-15"},
			[2]string{"N-1A/A", "2015-08-20"},
			[2]string{"8-K", "2014-01-01"},
			[2]string{"N-1A", "2010-06-01"},
		)
		Expect(sub.EarliestFilingDateForForm("N-1A")).To(Equal("2010-06-01"))
	})

	It("matches case-insensitively and tolerates whitespace", func() {
		sub := newSub(nil,
			[2]string{"  n-1a  ", "2018-04-10"},
			[2]string{"n-1A/A", "2015-08-20"},
		)
		Expect(sub.EarliestFilingDateForForm("N-1A")).To(Equal("2015-08-20"))
	})

	It("returns empty when no form matches the prefix", func() {
		sub := newSub(nil,
			[2]string{"10-K", "2020-03-15"},
			[2]string{"8-K", "2014-01-01"},
		)
		Expect(sub.EarliestFilingDateForForm("N-1A")).To(Equal(""))
	})

	It("returns empty when called on a nil receiver", func() {
		var sub *SubmissionsResponse
		Expect(sub.EarliestFilingDateForForm("N-1A")).To(Equal(""))
	})

	It("returns empty when prefix is empty", func() {
		sub := newSub(nil, [2]string{"N-1A", "2010-06-01"})
		Expect(sub.EarliestFilingDateForForm("")).To(Equal(""))
	})

	It("matches N-2 distinctly from N-2 prefixed forms only", func() {
		sub := newSub(nil,
			[2]string{"N-CSR", "2020-01-01"},
			[2]string{"N-2/A", "2012-07-15"},
			[2]string{"N-2", "2008-02-22"},
		)
		Expect(sub.EarliestFilingDateForForm("N-2")).To(Equal("2008-02-22"))
	})
})
