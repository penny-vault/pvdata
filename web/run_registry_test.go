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
package web

import (
	"github.com/google/uuid"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RunRegistry", func() {
	var registry *RunRegistry

	BeforeEach(func() {
		registry = NewRunRegistry()
	})

	It("reserves and retrieves an active run", func() {
		id := uuid.New().String()

		run, ok := registry.TryReserve(id)
		Expect(ok).To(BeTrue())
		Expect(run).NotTo(BeNil())

		got, found := registry.Load(id)
		Expect(found).To(BeTrue())
		Expect(got).To(Equal(run))
	})

	It("rejects a second reservation for the same subscription", func() {
		id := uuid.New().String()

		_, ok := registry.TryReserve(id)
		Expect(ok).To(BeTrue())

		_, ok = registry.TryReserve(id)
		Expect(ok).To(BeFalse())
	})

	It("returns false for unknown subscription", func() {
		_, ok := registry.Load("nonexistent")
		Expect(ok).To(BeFalse())
	})

	It("reports active subscriptions", func() {
		id := uuid.New().String()
		Expect(registry.IsActive(id)).To(BeFalse())

		_, _ = registry.TryReserve(id)
		Expect(registry.IsActive(id)).To(BeTrue())

		registry.Delete(id)
		Expect(registry.IsActive(id)).To(BeFalse())
	})

	It("removes a run", func() {
		id := uuid.New().String()

		_, _ = registry.TryReserve(id)
		registry.Delete(id)

		_, ok := registry.Load(id)
		Expect(ok).To(BeFalse())
	})
})
