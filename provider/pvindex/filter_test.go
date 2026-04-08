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
package pvindex

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("isLPName", func() {
	DescribeTable("LP suffix detection",
		func(name string, expected bool) {
			Expect(isLPName(name)).To(Equal(expected))
		},
		Entry("Enterprise Products LP", "Enterprise Products Partners LP", true),
		Entry("Energy Transfer LP", "Energy Transfer LP", true),
		Entry("MPLX LP", "MPLX LP", true),
		Entry("Brookfield Infrastructure L.P.", "Brookfield Infrastructure L.P.", true),
		Entry("L.P. with trailing whitespace", "Some Partnership L.P.   ", true),
		Entry("LLP suffix", "Big Law LLP", true),
		Entry("Limited Partnership full word", "Foo Limited Partnership", true),
		Entry("L P with space", "ABC Investments L P", true),
		Entry("lower case lp", "enterprise products lp", true),
		Entry("Marsh & McLennan Companies (negative - not LP)", "Marsh & McLennan Companies", false),
		Entry("Apple Inc (negative)", "Apple Inc.", false),
		Entry("MetLife Inc (negative)", "MetLife Inc", false),
		Entry("LP in middle of name (negative)", "LP Holdings Corporation", false),
		Entry("Compass Plc (negative - similar shape)", "Compass Group plc", false),
		Entry("Empty name (negative)", "", false),
	)
})
