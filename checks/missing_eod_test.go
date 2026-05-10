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
package checks

import (
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
)

var _ = Describe("MissingEOD metadata", func() {
	It("declares the eod data type", func() {
		c := &MissingEOD{}
		Expect(c.DataTypes()).To(Equal([]string{"eod"}))
		Expect(c.Phase()).To(Equal(PhaseAudit))
		Expect(c.Severity()).To(Equal(SeverityWarning))
		Expect(c.Name()).To(Equal("missing_eod"))
	})
})
