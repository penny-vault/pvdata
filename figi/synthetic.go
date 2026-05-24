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
	"crypto/sha256"
	"fmt"
	"strings"
)

// figiAlphabet contains the valid characters for FIGI identifiers:
// digits 0-9 plus consonants (no vowels A, E, I, O, U).
const figiAlphabet = "0123456789BCDFGHJKLMNPQRSTVWXYZ"

// charToValue maps a character to its numeric value for check digit
// calculation. 0-9 map to 0-9; A-Z map to 10-35 (full alphabet, including
// vowels, since real FIGIs from other issuers may contain them in the
// issuer prefix).
func charToValue(c byte) int {
	if c >= '0' && c <= '9' {
		return int(c - '0')
	}

	return int(c-'A') + 10
}

// computeCheckDigit computes the FIGI check digit using the modified Luhn
// algorithm over the first 11 characters of a FIGI.
func computeCheckDigit(first11 string) int {
	sum := 0

	for i := 0; i < 11; i++ {
		val := charToValue(first11[i])

		if i%2 == 1 {
			val *= 2
		}

		// Sum the individual decimal digits of val.
		sum += digitSum(val)
	}

	return (10 - (sum % 10)) % 10
}

// digitSum returns the sum of the decimal digits of n.
func digitSum(n int) int {
	s := 0
	for n > 0 {
		s += n % 10
		n /= 10
	}

	return s
}

// ValidateFIGICheckDigit validates whether the 12th character of a FIGI
// identifier is the correct check digit per the modified Luhn algorithm.
func ValidateFIGICheckDigit(figi string) bool {
	if len(figi) != 12 {
		return false
	}

	// The last character must be a digit.
	checkChar := figi[11]
	if checkChar < '0' || checkChar > '9' {
		return false
	}

	expected := computeCheckDigit(figi[:11])

	return int(checkChar-'0') == expected
}

// GenerateSyntheticFIGI generates a deterministic FIGI-format identifier
// (PVG{8 alphanumeric chars}{check digit}) for a given ticker and name,
// using the "PV" issuer prefix to distinguish it from real OpenFIGI-assigned
// identifiers. Prefer GenerateSyntheticFIGIFromCIK when a SEC CIK is
// available; this fallback is for entities without a CIK.
func GenerateSyntheticFIGI(ticker, name string) string {
	hash := sha256.Sum256([]byte(ticker + "|" + name))

	var b strings.Builder
	b.Grow(12)

	// Positions 1-3: issuer prefix "PV" + required "G"
	b.WriteString("PVG")

	// Positions 4-11: 8 characters derived from hash
	alphabetLen := len(figiAlphabet)

	for i := 0; i < 8; i++ {
		idx := int(hash[i]) % alphabetLen
		b.WriteByte(figiAlphabet[idx])
	}

	// Position 12: check digit
	first11 := b.String()
	checkDigit := computeCheckDigit(first11)

	b.WriteByte(byte('0' + checkDigit))

	return b.String()
}

// GenerateSyntheticFIGIFromCIK generates a deterministic FIGI-format
// identifier (PVG{8 alphanumeric chars}{check digit}) for an entity keyed
// by its SEC CIK and ticker. The (cik, ticker) tuple disambiguates both
// share classes filed under the same CIK and tickers reused across
// entities. The seed input is namespaced with a "cik:" prefix so it
// cannot collide with GenerateSyntheticFIGI's "ticker|name" seed.
func GenerateSyntheticFIGIFromCIK(cik, ticker string) string {
	hash := sha256.Sum256([]byte("cik:" + cik + "|" + ticker))

	var b strings.Builder
	b.Grow(12)

	b.WriteString("PVG")

	alphabetLen := len(figiAlphabet)

	for i := 0; i < 8; i++ {
		idx := int(hash[i]) % alphabetLen
		b.WriteByte(figiAlphabet[idx])
	}

	first11 := b.String()
	checkDigit := computeCheckDigit(first11)

	b.WriteByte(byte('0' + checkDigit))

	return b.String()
}

// GenerateSyntheticFIGIFromCIKLifecycle is the lifecycle-aware variant of
// GenerateSyntheticFIGIFromCIK. It folds the lifecycle start date into the
// hash so that two adjacent lifecycles for the same (CIK, ticker) — caused
// by real trading gaps such as a delinquency suffix or relisting — receive
// distinct synthetic FIGIs and can coexist in tables keyed on
// (ticker, composite_figi). lifecycleStart should be a stable date string
// (YYYY-MM-DD) derived from the lifecycle's first trading date.
func GenerateSyntheticFIGIFromCIKLifecycle(cik, ticker, lifecycleStart string) string {
	hash := sha256.Sum256([]byte("cik:" + cik + "|" + ticker + "|lifecycle:" + lifecycleStart))

	var b strings.Builder
	b.Grow(12)

	b.WriteString("PVG")

	alphabetLen := len(figiAlphabet)

	for i := 0; i < 8; i++ {
		idx := int(hash[i]) % alphabetLen
		b.WriteByte(figiAlphabet[idx])
	}

	first11 := b.String()
	checkDigit := computeCheckDigit(first11)

	b.WriteByte(byte('0' + checkDigit))

	return b.String()
}

// GenerateSyntheticFIGILifecycle is the lifecycle-aware variant of
// GenerateSyntheticFIGI for entities without a CIK. See
// GenerateSyntheticFIGIFromCIKLifecycle for rationale.
func GenerateSyntheticFIGILifecycle(ticker, name, lifecycleStart string) string {
	hash := sha256.Sum256([]byte(ticker + "|" + name + "|lifecycle:" + lifecycleStart))

	var b strings.Builder
	b.Grow(12)

	b.WriteString("PVG")

	alphabetLen := len(figiAlphabet)

	for i := 0; i < 8; i++ {
		idx := int(hash[i]) % alphabetLen
		b.WriteByte(figiAlphabet[idx])
	}

	first11 := b.String()
	checkDigit := computeCheckDigit(first11)

	b.WriteByte(byte('0' + checkDigit))

	return b.String()
}

// IsSyntheticFIGI returns true if the given FIGI was generated by
// GenerateSyntheticFIGI (i.e., has the "PVG" prefix).
func IsSyntheticFIGI(figi string) bool {
	return len(figi) == 12 && figi[:3] == "PVG"
}

// FormatSyntheticFIGI is a convenience that generates a synthetic FIGI and
// returns it as a formatted string for logging/debugging.
func FormatSyntheticFIGI(ticker, name string) string {
	return fmt.Sprintf("synthetic:%s", GenerateSyntheticFIGI(ticker, name))
}
