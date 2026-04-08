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

	"github.com/penny-vault/pvdata/provider"
)

var _ = Describe("SEC Provider", func() {
	It("registers itself in the provider map", func() {
		p, ok := provider.Map["sec"]
		Expect(ok).To(BeTrue())
		Expect(p.Name()).To(Equal("SEC"))
	})

	It("returns the correct description", func() {
		p := provider.Map["sec"]
		Expect(p.Description()).To(ContainSubstring("SEC EDGAR"))
	})

	It("requires userAgent config", func() {
		p := provider.Map["sec"]
		cfg := p.ConfigDescription()
		Expect(cfg).To(HaveKey("userAgent"))
	})

	It("defines a Fundamentals dataset", func() {
		p := provider.Map["sec"]
		ds := p.Datasets()
		Expect(ds).To(HaveKey("Fundamentals"))
		Expect(ds["Fundamentals"].Name).To(Equal("Fundamentals"))
	})
})
