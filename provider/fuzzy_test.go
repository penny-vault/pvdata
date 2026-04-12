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
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SuggestMatch", func() {
	candidates := []string{"AAPL", "AMZN", "MSFT", "GOOGL", "META", "TSLA", "NVDA", "AAPLX"}

	It("returns a suggestion for a close match", func() {
		suggestions := SuggestMatch("APL", candidates)
		Expect(suggestions).To(ContainElement("AAPL"))
	})

	It("returns multiple suggestions sorted by similarity", func() {
		suggestions := SuggestMatch("AAPL", []string{"AAPLX", "AAPL", "AAPLS"})
		Expect(suggestions).NotTo(BeEmpty())
		Expect(suggestions[0]).To(Equal("AAPL"))
	})

	It("returns at most 3 suggestions", func() {
		many := []string{"AAP", "AAPL", "AAPLX", "AAPLS", "AAPLZ"}
		suggestions := SuggestMatch("AAPL", many)
		Expect(len(suggestions)).To(BeNumerically("<=", 3))
	})

	It("returns nil when no candidate is close enough", func() {
		suggestions := SuggestMatch("AAPL", []string{"ZZZZ", "XXXX", "QQQQ"})
		Expect(suggestions).To(BeNil())
	})

	It("is case-insensitive", func() {
		suggestions := SuggestMatch("aapl", []string{"AAPL", "MSFT"})
		Expect(suggestions).To(ContainElement("AAPL"))
	})
})
