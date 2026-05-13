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
package permid

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("RateLimit", func() {
	It("returns the same shared limiter across calls", func() {
		// Singleton invariant: every caller within a process must
		// share one limiter so the aggregate request rate never
		// exceeds the Refinitiv 4 req/s cap. Allocating per-call
		// would defeat the cap under massive's 32-worker publish()
		// fan-out (32 independent 4-req/s limiters = ~128 req/s).
		Expect(RateLimit()).To(BeIdenticalTo(RateLimit()))
	})
})

var _ = Describe("apiBudgetFromContext", func() {
	It("returns the same counter pointer across calls within one ctx", func() {
		// Shared-budget invariant: every permid.Enrich call that
		// derives from the same ctx must see the same atomic
		// counter. Without this, a per-asset publish() path would
		// allocate a fresh 250 budget per call and the per-run cap
		// would never actually limit anything.
		ctx := WithAPIBudget(context.Background(), 100)
		first := apiBudgetFromContext(ctx)
		second := apiBudgetFromContext(ctx)
		Expect(first).To(BeIdenticalTo(second))
	})

	It("decrements visible across calls", func() {
		ctx := WithAPIBudget(context.Background(), 10)
		apiBudgetFromContext(ctx).Add(-3)
		Expect(apiBudgetFromContext(ctx).Load()).To(Equal(int64(7)))
	})

	It("starts at the budget supplied to WithAPIBudget", func() {
		ctx := WithAPIBudget(context.Background(), 42)
		Expect(apiBudgetFromContext(ctx).Load()).To(Equal(int64(42)))
	})

	It("falls back to a fresh DefaultEnrichAPIBudget when ctx has none", func() {
		// Unattached ctx is the stand-alone-call default (e.g. a
		// unit test calling Enrich directly). Each lookup gets its
		// own counter, but the value is DefaultEnrichAPIBudget so
		// the call still functions.
		got := apiBudgetFromContext(context.Background()).Load()
		Expect(got).To(Equal(int64(DefaultEnrichAPIBudget)))
	})

	It("allows independent budgets on derived ctxs", func() {
		// A nested WithAPIBudget overrides the parent — used by
		// BackfillEmpty to scope its own budget pool separately from
		// the surrounding run's inline Enrich budget.
		parent := WithAPIBudget(context.Background(), 100)
		child := WithAPIBudget(parent, 5)

		Expect(apiBudgetFromContext(child).Load()).To(Equal(int64(5)))
		Expect(apiBudgetFromContext(parent).Load()).To(Equal(int64(100)))

		apiBudgetFromContext(child).Add(-2)
		Expect(apiBudgetFromContext(parent).Load()).To(Equal(int64(100)))
	})
})
