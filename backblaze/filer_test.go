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
package backblaze_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/backblaze"
)

var _ = Describe("Filer URL", func() {
	It("constructs the public file URL from bucket, prefix, and name", func() {
		f := backblaze.NewFilerForTest(
			"penny-vault",
			"assets/logos",
			"https://f002.backblazeb2.com",
		)

		Expect(f.PublicURL("BBG000FOO-icon.png")).
			To(Equal("https://f002.backblazeb2.com/file/penny-vault/assets/logos/BBG000FOO-icon.png"))
	})

	It("omits the prefix segment when prefix is empty", func() {
		f := backblaze.NewFilerForTest(
			"penny-vault",
			"",
			"https://f002.backblazeb2.com",
		)

		Expect(f.PublicURL("BBG000FOO-icon.png")).
			To(Equal("https://f002.backblazeb2.com/file/penny-vault/BBG000FOO-icon.png"))
	})
})
