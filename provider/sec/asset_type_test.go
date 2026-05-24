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

	"github.com/penny-vault/pvdata/data"
)

var _ = Describe("resolveTypeFromForms", func() {
	DescribeTable("returns expected asset type",
		func(forms []string, wantType data.AssetType, wantOK bool) {
			gotType, gotOK := resolveTypeFromForms(forms)
			Expect(gotOK).To(Equal(wantOK))
			Expect(gotType).To(Equal(wantType))
		},
		Entry("modern 10-K/10-Q maps to CS",
			[]string{"10-K", "10-Q", "10-Q", "8-K"},
			data.CommonStock, true),
		Entry("small-business 10KSB/10QSB (no dash) maps to CS — Access Anytime Bancorp pattern",
			[]string{"10KSB", "10QSB", "10QSB", "10KSB40", "DEF 14A"},
			data.CommonStock, true),
		Entry("dashed small-business variants 10-KSB / 10-QSB also map to CS",
			[]string{"10-KSB", "10-QSB", "10-KSB/A"},
			data.CommonStock, true),
		Entry("amended forms with /A suffix map to CS",
			[]string{"10-K/A", "10-Q/A"},
			data.CommonStock, true),
		Entry("transition annual 10-KT maps to CS",
			[]string{"10-KT", "10-Q"},
			data.CommonStock, true),
		Entry("foreign 20-F maps to ADRC — ABN AMRO pattern",
			[]string{"20-F", "20-F/A", "6-K"},
			data.ADRC, true),
		Entry("Canadian 40-F maps to ADRC",
			[]string{"40-F", "40-F/A"},
			data.ADRC, true),
		Entry("N-CSR semi-annual maps to CEF",
			[]string{"N-CSR", "N-CSRS"},
			data.CEF, true),
		Entry("N-2 closed-end registration maps to CEF",
			[]string{"N-2", "N-2/A"},
			data.CEF, true),
		Entry("N-1A mutual-fund registration maps to MutualFund",
			[]string{"N-1A", "N-1A/A"},
			data.MutualFund, true),
		Entry("BDC that files both 10-K and N-2 maps to CS (tie-break)",
			[]string{"10-K", "10-Q", "N-2"},
			data.CommonStock, true),
		Entry("form list with no type signal returns false",
			[]string{"4", "SC 13G", "SC 13D", "DEF 14A", "8-K"},
			data.AssetType(""), false),
		Entry("empty form list returns false",
			[]string{},
			data.AssetType(""), false),
		Entry("mixed case and whitespace are normalized",
			[]string{"  10-k  ", "10K-SB"},
			data.CommonStock, true),
		Entry("CEF wins over MF when CEF votes are higher",
			[]string{"N-CSR", "N-CSR", "N-2", "N-1A"},
			data.CEF, true),
	)
})

var _ = Describe("classifyExclusivelyOperating", func() {
	DescribeTable("only fires when operating-company forms exist and zero fund forms appear",
		func(forms []string, wantType data.AssetType, wantOK bool) {
			gotType, gotOK := classifyExclusivelyOperating(forms)
			Expect(gotOK).To(Equal(wantOK))
			Expect(gotType).To(Equal(wantType))
		},
		Entry("ProxyMed pattern — exclusively CS-issuer forms (PILL historical lifecycle)",
			[]string{"4", "8-K", "3", "SC 13D/A", "10-Q", "DEF 14A", "S-3", "10-K"},
			data.CommonStock, true),
		Entry("ADRC pattern — foreign-issuer forms only, no N-series",
			[]string{"20-F", "6-K", "SC 13G", "20-F/A"},
			data.ADRC, true),
		Entry("BDC pattern — any N-2 filing aborts the override even with 10-Ks present",
			[]string{"10-K", "10-Q", "N-2"},
			data.AssetType(""), false),
		Entry("CEF pattern — N-CSR alone aborts the override",
			[]string{"N-CSR", "DEF 14A", "4"},
			data.AssetType(""), false),
		Entry("MF pattern — N-1A aborts the override",
			[]string{"N-1A", "N-1A/A"},
			data.AssetType(""), false),
		Entry("modern fund-reporting NPORT-P aborts the override (CIK files NPORT instead of legacy N-Q)",
			[]string{"10-K", "NPORT-P", "DEF 14A"},
			data.AssetType(""), false),
		Entry("no operating-form votes returns false (insider-only history)",
			[]string{"4", "3", "SC 13G", "DEF 14A"},
			data.AssetType(""), false),
		Entry("empty form list returns false",
			[]string{},
			data.AssetType(""), false),
		Entry("CS-prevalent (more CS than ADRC) returns CS",
			[]string{"10-K", "10-Q", "10-Q", "20-F"},
			data.CommonStock, true),
		Entry("ADRC-prevalent (more 20-F than 10-K) returns ADRC",
			[]string{"20-F", "20-F", "10-K"},
			data.ADRC, true),
	)
})

var _ = Describe("containsTicker", func() {
	It("returns true when the ticker is in the list", func() {
		Expect(containsTicker([]string{"AAPL", "MSFT"}, "AAPL")).To(BeTrue())
	})

	It("returns false when the ticker is not in the list", func() {
		Expect(containsTicker([]string{"AAPL", "MSFT"}, "GOOG")).To(BeFalse())
	})

	It("compares case-insensitively", func() {
		Expect(containsTicker([]string{"aapl"}, "AAPL")).To(BeTrue())
		Expect(containsTicker([]string{"AAPL"}, "aapl")).To(BeTrue())
	})

	It("trims surrounding whitespace from both list entries and the target", func() {
		Expect(containsTicker([]string{" AAPL "}, "AAPL")).To(BeTrue())
		Expect(containsTicker([]string{"AAPL"}, " AAPL ")).To(BeTrue())
	})

	It("returns false for an empty target ticker so the caller does not see a phantom match", func() {
		Expect(containsTicker([]string{"AAPL"}, "")).To(BeFalse())
		Expect(containsTicker([]string{}, "")).To(BeFalse())
	})
})
