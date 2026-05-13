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
package data

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("filterEmpty", func() {
	It("returns nil when input is nil", func() {
		Expect(filterEmpty(nil)).To(BeNil())
	})

	It("returns nil when input is empty", func() {
		Expect(filterEmpty([]string{})).To(BeNil())
	})

	It("returns nil when input contains only an empty string (the {\"\"} case)", func() {
		Expect(filterEmpty([]string{""})).To(BeNil())
	})

	It("returns nil when input is multiple empty/whitespace strings", func() {
		Expect(filterEmpty([]string{"", "  ", "\t"})).To(BeNil())
	})

	It("strips empty entries and keeps non-empty ones", func() {
		Expect(filterEmpty([]string{"US0378331005", "", "US5949181045"})).
			To(Equal([]string{"US0378331005", "US5949181045"}))
	})

	It("trims surrounding whitespace on retained entries", func() {
		Expect(filterEmpty([]string{"  US0378331005  ", "US5949181045"})).
			To(Equal([]string{"US0378331005", "US5949181045"}))
	})
})
