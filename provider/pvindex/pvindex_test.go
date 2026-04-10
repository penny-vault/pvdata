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

	"github.com/penny-vault/pvdata/data"
	"github.com/penny-vault/pvdata/provider"
)

var _ = Describe("pvindex Provider", func() {
	It("registers itself in the provider map", func() {
		p, ok := provider.Map["pvindex"]
		Expect(ok).To(BeTrue())
		Expect(p.Name()).To(Equal("pvindex"))
	})

	It("returns a non-empty description", func() {
		p := provider.Map["pvindex"]
		Expect(p.Description()).NotTo(BeEmpty())
	})

	It("declares the US Tradable Universe dataset", func() {
		p := provider.Map["pvindex"]
		datasets := p.Datasets()
		Expect(datasets).To(HaveKey("US Tradable Universe"))
	})

	It("US Tradable Universe dataset emits IndexSnapshot and IndexChangelog data types", func() {
		p := provider.Map["pvindex"]
		ds := p.Datasets()["US Tradable Universe"]
		Expect(ds.DataTypes).To(HaveLen(2))
		typeNames := []string{ds.DataTypes[0].Name, ds.DataTypes[1].Name}
		Expect(typeNames).To(ConsistOf(data.IndexSnapshotKey, data.IndexChangelogKey))
	})
})
