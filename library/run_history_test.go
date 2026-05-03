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
package library_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/library"
)

var _ = Describe("RunHistory", func() {
	Describe("StatusToString", func() {
		It("converts RunSuccess to \"success\"", func() {
			Expect(library.StatusToString(data.RunSuccess)).To(Equal("success"))
		})

		It("converts RunFailed to \"failed\"", func() {
			Expect(library.StatusToString(data.RunFailed)).To(Equal("failed"))
		})

		It("converts StatusUnknown to \"failed\"", func() {
			Expect(library.StatusToString(data.StatusUnknown)).To(Equal("failed"))
		})

		It("converts RunInProgress to \"running\"", func() {
			Expect(library.StatusToString(data.RunInProgress)).To(Equal("running"))
		})
	})

	Describe("NormalizeLogPageBounds", func() {
		It("applies the default limit when zero is passed", func() {
			limit, _ := library.NormalizeLogPageBounds(0, 0)
			Expect(limit).To(Equal(1000))
		})

		It("clamps limits above the maximum", func() {
			limit, _ := library.NormalizeLogPageBounds(50000, 0)
			Expect(limit).To(Equal(5000))
		})

		It("preserves explicit limits within range", func() {
			limit, _ := library.NormalizeLogPageBounds(250, 0)
			Expect(limit).To(Equal(250))
		})

		It("substitutes a tail sentinel when before is non-positive", func() {
			_, before := library.NormalizeLogPageBounds(0, 0)
			Expect(before).To(BeNumerically(">", 1_000_000_000))
		})

		It("preserves an explicit before cursor", func() {
			_, before := library.NormalizeLogPageBounds(0, 42)
			Expect(before).To(Equal(42))
		})
	})

	Describe("FinalizeLogPage", func() {
		It("returns an empty page for no rows", func() {
			page := library.FinalizeLogPage(nil)
			Expect(page.Lines).To(BeEmpty())
			Expect(page.Total).To(Equal(0))
			Expect(page.StartLine).To(Equal(0))
		})

		It("flips DESC rows back into chronological order", func() {
			rows := []library.LogPageRow{
				{Line: "c", Lineno: 7, Total: 7},
				{Line: "b", Lineno: 6, Total: 7},
				{Line: "a", Lineno: 5, Total: 7},
			}
			page := library.FinalizeLogPage(rows)
			Expect(page.Lines).To(Equal([]string{"a", "b", "c"}))
			Expect(page.Total).To(Equal(7))
			Expect(page.StartLine).To(Equal(5))
		})

		It("handles a single-row page", func() {
			page := library.FinalizeLogPage([]library.LogPageRow{
				{Line: "only", Lineno: 1, Total: 1},
			})
			Expect(page.Lines).To(Equal([]string{"only"}))
			Expect(page.Total).To(Equal(1))
			Expect(page.StartLine).To(Equal(1))
		})
	})
})
