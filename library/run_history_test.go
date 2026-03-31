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
	})
})
