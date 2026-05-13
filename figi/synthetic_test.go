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
package figi

import (
	"strings"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var validFIGIChars = "0123456789BCDFGHJKLMNPQRSTVWXYZ"

var _ = Describe("GenerateSyntheticFIGI", func() {
	It("returns a 12-character string", func() {
		result := GenerateSyntheticFIGI("AAPL", "Apple Inc")
		Expect(result).To(HaveLen(12))
	})

	It("starts with PVG prefix", func() {
		result := GenerateSyntheticFIGI("AAPL", "Apple Inc")
		Expect(result[:3]).To(Equal("PVG"))
	})

	It("uses only valid FIGI characters in positions 4-11", func() {
		result := GenerateSyntheticFIGI("AAPL", "Apple Inc")
		body := result[3:11]
		for i, ch := range body {
			Expect(strings.ContainsRune(validFIGIChars, ch)).To(BeTrue(),
				"character at body position %d (%c) is not a valid FIGI character", i, ch)
		}
	})

	It("has a numeric digit at position 12 (check digit)", func() {
		result := GenerateSyntheticFIGI("AAPL", "Apple Inc")
		checkDigit := result[11]
		Expect(checkDigit).To(SatisfyAll(
			BeNumerically(">=", byte('0')),
			BeNumerically("<=", byte('9')),
		))
	})

	It("has a valid check digit per modified Luhn algorithm", func() {
		result := GenerateSyntheticFIGI("AAPL", "Apple Inc")
		Expect(ValidateFIGICheckDigit(result)).To(BeTrue())
	})

	It("is deterministic (same inputs produce same output)", func() {
		result1 := GenerateSyntheticFIGI("AAPL", "Apple Inc")
		result2 := GenerateSyntheticFIGI("AAPL", "Apple Inc")
		Expect(result1).To(Equal(result2))
	})

	It("produces different outputs for different inputs", func() {
		result1 := GenerateSyntheticFIGI("AAPL", "Apple Inc")
		result2 := GenerateSyntheticFIGI("MSFT", "Microsoft Corporation")
		Expect(result1).NotTo(Equal(result2))
	})
})

var _ = Describe("ValidateFIGICheckDigit", func() {
	It("validates a known real FIGI (BBG000BLNNV0)", func() {
		Expect(ValidateFIGICheckDigit("BBG000BLNNV0")).To(BeTrue())
	})

	It("rejects a FIGI with an incorrect check digit", func() {
		Expect(ValidateFIGICheckDigit("BBG000BLNNV1")).To(BeFalse())
	})

	It("rejects a string that is not 12 characters", func() {
		Expect(ValidateFIGICheckDigit("BBG000BLNNV")).To(BeFalse())
		Expect(ValidateFIGICheckDigit("BBG000BLNNV00")).To(BeFalse())
	})
})

var _ = Describe("GenerateSyntheticFIGIFromCIK", func() {
	It("returns a 12-character string", func() {
		Expect(GenerateSyntheticFIGIFromCIK("0001085734", "BBI")).To(HaveLen(12))
	})

	It("starts with PVG prefix", func() {
		result := GenerateSyntheticFIGIFromCIK("0001085734", "BBI")
		Expect(result[:3]).To(Equal("PVG"))
	})

	It("uses only valid FIGI characters in positions 4-11", func() {
		result := GenerateSyntheticFIGIFromCIK("0001085734", "BBI")
		body := result[3:11]
		for i, ch := range body {
			Expect(strings.ContainsRune(validFIGIChars, ch)).To(BeTrue(),
				"character at body position %d (%c) is not a valid FIGI character", i, ch)
		}
	})

	It("has a valid check digit per modified Luhn algorithm", func() {
		result := GenerateSyntheticFIGIFromCIK("0001085734", "BBI")
		Expect(ValidateFIGICheckDigit(result)).To(BeTrue())
	})

	It("is deterministic (same inputs produce same output)", func() {
		Expect(GenerateSyntheticFIGIFromCIK("0001085734", "BBI")).
			To(Equal(GenerateSyntheticFIGIFromCIK("0001085734", "BBI")))
	})

	It("distinct CIKs with the same ticker produce distinct FIGIs (ticker reuse)", func() {
		blockbuster := GenerateSyntheticFIGIFromCIK("0001085734", "BBI")
		brickell := GenerateSyntheticFIGIFromCIK("0000819050", "BBI")
		Expect(blockbuster).NotTo(Equal(brickell))
	})

	It("same CIK with distinct tickers produces distinct FIGIs (share classes)", func() {
		brkA := GenerateSyntheticFIGIFromCIK("0001067983", "BRK.A")
		brkB := GenerateSyntheticFIGIFromCIK("0001067983", "BRK.B")
		Expect(brkA).NotTo(Equal(brkB))
	})

	It("does not collide with GenerateSyntheticFIGI for the same string pair", func() {
		fromCIK := GenerateSyntheticFIGIFromCIK("0001085734", "BBI")
		fromName := GenerateSyntheticFIGI("0001085734", "BBI")
		Expect(fromCIK).NotTo(Equal(fromName))
	})
})
