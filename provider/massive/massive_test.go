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
package massive_test

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/penny-vault/pvdata/provider/massive"
)

var _ = Describe("Branding budget", func() {
	It("permits fetches up to the cap", func() {
		c := massive.NewBrandingBudget(2)
		Expect(c.Allow()).To(BeTrue())
		Expect(c.Allow()).To(BeTrue())
	})

	It("denies further fetches once the cap is reached", func() {
		c := massive.NewBrandingBudget(2)
		c.Allow()
		c.Allow()
		Expect(c.Allow()).To(BeFalse())
	})

	It("treats a non-positive cap as unlimited", func() {
		c := massive.NewBrandingBudget(0)

		for i := 0; i < 10_000; i++ {
			Expect(c.Allow()).To(BeTrue())
		}
	})
})
