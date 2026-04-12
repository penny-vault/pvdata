// Copyright 2024
// SPDX-License-Identifier: Apache-2.0

package provider

import (
	"context"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("SecurityFilterFromContext", func() {
	It("returns empty strings when no filter is set", func() {
		ctx := context.Background()
		ticker, figi := SecurityFilterFromContext(ctx)
		Expect(ticker).To(BeEmpty())
		Expect(figi).To(BeEmpty())
	})

	It("returns ticker when TickerFilterKey is set", func() {
		ctx := context.WithValue(context.Background(), TickerFilterKey, "AAPL")
		ticker, figi := SecurityFilterFromContext(ctx)
		Expect(ticker).To(Equal("AAPL"))
		Expect(figi).To(BeEmpty())
	})

	It("returns figi when FigiFilterKey is set", func() {
		ctx := context.WithValue(context.Background(), FigiFilterKey, "BBG000B9XRY4")
		ticker, figi := SecurityFilterFromContext(ctx)
		Expect(ticker).To(BeEmpty())
		Expect(figi).To(Equal("BBG000B9XRY4"))
	})
})
